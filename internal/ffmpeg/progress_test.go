package ffmpeg

import (
	"math"
	"strings"
	"testing"
)

// TestParseProgressDerivesFraction checks the duration-based percentage path.
func TestParseProgressDerivesFraction(t *testing.T) {
	stream := strings.Join([]string{
		"frame=120",
		"fps=48.5",
		"bitrate=2500.0kbits/s",
		"total_size=1048576",
		"out_time_us=5000000",
		"speed=2.4x",
		"progress=continue",
	}, "\n")

	var got []Progress
	err := ParseProgress(strings.NewReader(stream), Totals{DurationS: 20}, func(p Progress) {
		got = append(got, p)
	})
	if err != nil {
		t.Fatalf("ParseProgress returned %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(got))
	}

	sample := got[0]
	if math.Abs(sample.Fraction-0.25) > 1e-9 {
		t.Errorf("fraction = %v, want 0.25", sample.Fraction)
	}
	if math.Abs(sample.OutTimeS-5) > 1e-9 {
		t.Errorf("outTime = %v, want 5", sample.OutTimeS)
	}
	if sample.Frame != 120 {
		t.Errorf("frame = %d, want 120", sample.Frame)
	}
	if math.Abs(sample.Speed-2.4) > 1e-9 {
		t.Errorf("speed = %v, want 2.4", sample.Speed)
	}
	// 15 seconds of media remain at 2.4x realtime.
	if math.Abs(sample.ETASeconds-6.25) > 1e-9 {
		t.Errorf("eta = %v, want 6.25", sample.ETASeconds)
	}
}

// TestParseProgressFallsBackToFrames checks the frame-count percentage path.
func TestParseProgressFallsBackToFrames(t *testing.T) {
	stream := "frame=300\nfps=60\nprogress=continue\n"

	var sample Progress
	err := ParseProgress(strings.NewReader(stream), Totals{Frames: 1200}, func(p Progress) {
		sample = p
	})
	if err != nil {
		t.Fatalf("ParseProgress returned %v", err)
	}
	if math.Abs(sample.Fraction-0.25) > 1e-9 {
		t.Errorf("fraction = %v, want 0.25", sample.Fraction)
	}
	// 900 frames remain at 60 fps.
	if math.Abs(sample.ETASeconds-15) > 1e-9 {
		t.Errorf("eta = %v, want 15", sample.ETASeconds)
	}
}

// TestParseProgressIndeterminate verifies that an unknown extent reports -1
// rather than inventing a percentage.
func TestParseProgressIndeterminate(t *testing.T) {
	stream := "frame=42\nprogress=continue\n"

	var sample Progress
	err := ParseProgress(strings.NewReader(stream), Totals{}, func(p Progress) {
		sample = p
	})
	if err != nil {
		t.Fatalf("ParseProgress returned %v", err)
	}
	if sample.Fraction != -1 {
		t.Errorf("fraction = %v, want -1 for unknown totals", sample.Fraction)
	}
	if sample.ETASeconds != -1 {
		t.Errorf("eta = %v, want -1 for unknown totals", sample.ETASeconds)
	}
}

// TestParseProgressEndBlock verifies the terminal sample is complete.
func TestParseProgressEndBlock(t *testing.T) {
	stream := "out_time_us=20000000\nprogress=end\n"

	var samples []Progress
	err := ParseProgress(strings.NewReader(stream), Totals{DurationS: 20}, func(p Progress) {
		samples = append(samples, p)
	})
	if err != nil {
		t.Fatalf("ParseProgress returned %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}
	if !samples[0].Done {
		t.Error("final sample should be marked done")
	}
	if samples[0].Fraction != 1 {
		t.Errorf("final fraction = %v, want 1", samples[0].Fraction)
	}
}

// TestParseProgressClampsOvershoot guards against a fraction above 1, which
// happens when ffmpeg reports a position past the probed duration.
func TestParseProgressClampsOvershoot(t *testing.T) {
	stream := "out_time_us=30000000\nprogress=continue\n"

	var sample Progress
	err := ParseProgress(strings.NewReader(stream), Totals{DurationS: 20}, func(p Progress) {
		sample = p
	})
	if err != nil {
		t.Fatalf("ParseProgress returned %v", err)
	}
	if sample.Fraction != 1 {
		t.Errorf("fraction = %v, want it clamped to 1", sample.Fraction)
	}
}

// TestParseProgressSkipsNotAvailable checks that N/A fields are ignored.
func TestParseProgressSkipsNotAvailable(t *testing.T) {
	stream := "speed=N/A\nbitrate=N/A\nout_time_us=1000000\nprogress=continue\n"

	var sample Progress
	err := ParseProgress(strings.NewReader(stream), Totals{DurationS: 10}, func(p Progress) {
		sample = p
	})
	if err != nil {
		t.Fatalf("ParseProgress returned %v", err)
	}
	if sample.Speed != 0 {
		t.Errorf("speed = %v, want 0 when reported as N/A", sample.Speed)
	}
	if math.Abs(sample.Fraction-0.1) > 1e-9 {
		t.Errorf("fraction = %v, want 0.1", sample.Fraction)
	}
}

// TestParseRate covers the rational frame-rate parser.
func TestParseRate(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"30000/1001", 29.97002997002997},
		{"25/1", 25},
		{"0/0", 0},
		{"", 0},
		{"60", 60},
	}
	for _, c := range cases {
		got := ParseRate(c.in)
		if math.Abs(got-c.want) > 1e-6 {
			t.Errorf("ParseRate(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestParseTimecode covers the HH:MM:SS parser and its rejection path.
func TestParseTimecode(t *testing.T) {
	if got := parseTimecode("01:02:03.5"); math.Abs(got-3723.5) > 1e-9 {
		t.Errorf("parseTimecode = %v, want 3723.5", got)
	}
	if got := parseTimecode("bogus"); got != -1 {
		t.Errorf("parseTimecode(bogus) = %v, want -1", got)
	}
}

// TestFormatDuration covers the human-readable clock renderer.
func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0:00"},
		{65, "1:05"},
		{3661, "1:01:01"},
		{-1, "--:--"},
	}
	for _, c := range cases {
		if got := FormatDuration(c.in); got != c.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDecodeProbe checks that ffprobe JSON is mapped onto MediaInfo.
func TestDecodeProbe(t *testing.T) {
	raw := []byte(`{
		"format": {"format_name":"mov,mp4","duration":"120.5","size":"1048576","bit_rate":"800000"},
		"streams": [
			{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,
			 "avg_frame_rate":"30/1","nb_frames":"3615","color_space":"bt709"},
			{"index":1,"codec_type":"audio","codec_name":"aac","sample_rate":"48000","channels":2,
			 "tags":{"language":"eng"}}
		]
	}`)

	info, err := decodeProbe("clip.mp4", raw)
	if err != nil {
		t.Fatalf("decodeProbe returned %v", err)
	}
	if math.Abs(info.DurationS-120.5) > 1e-9 {
		t.Errorf("duration = %v, want 120.5", info.DurationS)
	}
	if info.TotalFrame != 3615 {
		t.Errorf("totalFrames = %d, want 3615", info.TotalFrame)
	}
	if len(info.Streams) != 2 {
		t.Fatalf("streams = %d, want 2", len(info.Streams))
	}
	if info.Streams[1].Language != "eng" {
		t.Errorf("language = %q, want eng", info.Streams[1].Language)
	}
	// A GIF needs it to know whether the video names its colour standard
	if info.Streams[0].ColorSpace != "bt709" || info.Streams[1].ColorSpace != "" {
		t.Errorf("colour spaces %q and %q, want bt709 and none", info.Streams[0].ColorSpace, info.Streams[1].ColorSpace)
	}
}

// TestDecodeProbeRejectsMalformed verifies bad JSON is an error, not a panic.
func TestDecodeProbeRejectsMalformed(t *testing.T) {
	if _, err := decodeProbe("x.mp4", []byte("{not json")); err == nil {
		t.Error("expected an error for malformed ffprobe output")
	}
}

// TestEstimateFramesFallback checks the fps*duration path when nb_frames is
// absent, which is common for streamed and variable-rate sources.
func TestEstimateFramesFallback(t *testing.T) {
	got := estimateFrames("", "30/1", 10)
	if got != 300 {
		t.Errorf("estimateFrames = %d, want 300", got)
	}
	if got := estimateFrames("", "", 10); got != 0 {
		t.Errorf("estimateFrames with no rate = %d, want 0", got)
	}
}
