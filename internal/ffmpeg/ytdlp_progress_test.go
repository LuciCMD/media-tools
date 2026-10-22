package ffmpeg

import (
	"math"
	"strings"
	"testing"
)

// parseLines runs ParseProgress over lines and returns every sample emitted
func parseLines(t *testing.T, lines ...string) []Progress {
	t.Helper()
	var got []Progress
	err := ParseProgress(strings.NewReader(strings.Join(lines, "\n")), Totals{}, func(p Progress) {
		got = append(got, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestDownloadProgressFromYtdlp checks yt-dlp's templated lines, as it printed
// them for a local clip, become a rising fraction with sizes, speed and time
// left, where they used to be ignored and a download sat at 0% until done
func TestDownloadProgressFromYtdlp(t *testing.T) {
	got := parseLines(t,
		"[generic] Extracting URL: http://127.0.0.1:8766/clip.mp4?v=1",
		"mediatools-dl downloading 1024 8033304 NA NA NA NA NA NA NA",
		"mediatools-dl downloading 4016652 8033304 NA 978159.35 4 NA NA avc1.4d401f none",
		"mediatools-dl finished 8033304 8033304 NA 230922405.44 NA NA NA avc1.4d401f none",
		"mediatools-pp VideoRemuxer started",
		"mediatools-pp VideoRemuxer finished",
	)
	if len(got) != 5 {
		t.Fatalf("got %d samples, want 5: %+v", len(got), got)
	}
	half := got[1]
	if math.Abs(half.Fraction-0.5) > 0.001 || half.SizeBytes != 4016652 || half.TotalBytes != 8033304 {
		t.Errorf("halfway reads %+v", half)
	}
	if half.ETASeconds != 4 || half.RateBps < 978159 || half.Stage != "Downloading video" {
		t.Errorf("halfway time, speed or stage: %+v", half)
	}
	if got[0].ETASeconds != -1 || got[0].RateBps != 0 || got[0].Stage != "Downloading" {
		t.Errorf("NA fields aren't unknown: %+v", got[0])
	}
	if done := got[2]; done.Fraction != 1 || done.ETASeconds != 0 {
		t.Errorf("a finished part reads %+v", done)
	}
	if remux := got[3]; remux.Stage != "Changing container" || remux.Fraction != -1 || remux.ETASeconds != -1 {
		t.Errorf("a started step reads %+v, want an unknown fraction with its name", remux)
	}
	if got[4].Stage != "Changing container" {
		t.Errorf("a step's end changed the stage to %q", got[4].Stage)
	}
}

// TestDownloadProgressAcrossAPlaylist checks a playlist's fraction counts
// the entries already done, with no time left from one entry alone
func TestDownloadProgressAcrossAPlaylist(t *testing.T) {
	got := parseLines(t, "mediatools-dl downloading 50 100 NA 10 5 3 4 none opus")
	p := got[0]
	if math.Abs(p.Fraction-0.625) > 0.001 || p.Item != 3 || p.Items != 4 || p.ETASeconds != -1 {
		t.Errorf("entry 3 of 4 half done reads %+v, want 62.5%% with no time left", p)
	}
	if p.Stage != "Downloading audio" {
		t.Errorf("an audio-only part is %q", p.Stage)
	}
}

// TestDownloadProgressStaysWithinThePlaylist checks a position past the
// count, or a site's codec name with spaces, can't push the fraction past
// done or spoil the numbers
func TestDownloadProgressStaysWithinThePlaylist(t *testing.T) {
	got := parseLines(t,
		"mediatools-dl finished 100 100 NA NA NA 10 6 avc1 none",
		"mediatools-dl downloading 50 100 NA 10 5 2 4 some codec with spaces mp4a",
	)
	if p := got[0]; p.Fraction > 1 || p.Item > p.Items {
		t.Errorf("entry 10 of 6 reads %+v, want at most done and the count", p)
	}
	if p := got[1]; math.Abs(p.Fraction-0.375) > 0.001 || p.Item != 2 || p.RateBps != 10 {
		t.Errorf("a codec with spaces spoiled the numbers: %+v", p)
	}
}

// TestDownloadProgressUsesTheEstimate checks a size yt-dlp only estimates
// still gives a fraction, and that odd values stay unknown
func TestDownloadProgressUsesTheEstimate(t *testing.T) {
	got := parseLines(t,
		"mediatools-dl downloading 25 NA 100 NA NA NA NA NA NA",
		"mediatools-dl downloading NaN NA NA -5 inf NA NA NA NA",
		"mediatools-dl downloading 1 2",
	)
	if len(got) != 3 {
		t.Fatalf("got %d samples, want 3", len(got))
	}
	if got[0].Fraction != 0.25 || got[0].TotalBytes != 100 {
		t.Errorf("an estimated size reads %+v", got[0])
	}
	if p := got[1]; p.Fraction != -1 || p.RateBps != 0 || p.ETASeconds != -1 || p.SizeBytes != 0 {
		t.Errorf("odd values read %+v, want all unknown", p)
	}
}

// TestFFmpegProgressUnchanged checks ffmpeg's own blocks still parse with
// no download fields set
func TestFFmpegProgressUnchanged(t *testing.T) {
	var got []Progress
	_ = ParseProgress(strings.NewReader("out_time_us=5000000\nprogress=continue\n"), Totals{DurationS: 10},
		func(p Progress) { got = append(got, p) })
	if len(got) != 1 || got[0].Fraction != 0.5 || got[0].Stage != "" || got[0].TotalBytes != 0 {
		t.Errorf("ffmpeg progress reads %+v", got)
	}
}

// TestDownloadProgressArgsMatchTheParser checks the template asks for the
// ten fields applyDownloadFields reads, in order
func TestDownloadProgressArgsMatchTheParser(t *testing.T) {
	args := DownloadProgressArgs()
	if len(args) != 4 || args[0] != "--progress-template" || args[2] != "--progress-template" {
		t.Fatalf("args %q", args)
	}
	fields := strings.Fields(strings.TrimPrefix(args[1], "download:"+downloadLinePrefix))
	if len(fields) != 10 || fields[0] != "%(progress.status)s" || fields[6] != "%(info.playlist_autonumber)s" {
		t.Errorf("the template has %d fields: %q", len(fields), fields)
	}
}

// TestDownloadProgressCarriesOnAcrossParts checks the audio after the video
// counts on from the video's bytes, where it used to restart the bar at 0%,
// and a playlist entry's later steps keep the entries already done
func TestDownloadProgressCarriesOnAcrossParts(t *testing.T) {
	got := parseLines(t,
		"mediatools-dl downloading 45 90 NA 9 5 NA NA avc1 none",
		"mediatools-dl finished 90 90 NA 9 NA NA NA avc1 none",
		"mediatools-dl downloading 5 10 NA 9 1 NA NA none mp4a",
		"mediatools-pp Merger started",
	)
	audio := got[2]
	if math.Abs(audio.Fraction-0.95) > 0.001 || audio.SizeBytes != 95 || audio.TotalBytes != 100 {
		t.Errorf("halfway through the audio reads %+v, want 95 of 100 bytes", audio)
	}

	list := parseLines(t,
		"mediatools-dl finished 10 10 NA NA NA 2 4 avc1 mp4a",
		"mediatools-pp Metadata started",
		"mediatools-dl downloading 5 10 NA NA NA 3 4 avc1 mp4a",
	)
	if p := list[1]; p.Fraction != 0.5 || p.Stage != "Adding details" {
		t.Errorf("entry 2 of 4's later step reads %+v, want 50%% kept", p)
	}
	if p := list[2]; math.Abs(p.Fraction-0.625) > 0.001 || p.SizeBytes != 5 {
		t.Errorf("the next entry carried the last one's bytes: %+v", p)
	}
}
