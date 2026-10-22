package tools

import (
	"strings"
	"testing"
)

// TestExtensionForPicksTheRightType covers each way a tool decides the
// output type.
func TestExtensionForPicksTheRightType(t *testing.T) {
	fixed := Tool{OutputExt: "gif"}
	byFormat := Tool{}
	keepsVideo := Tool{VideoKeepsFormat: true}

	cases := []struct {
		name  string
		tool  Tool
		opts  map[string]string
		input string
		want  string
	}{
		{"a fixed type wins", fixed, map[string]string{"format": "png"}, "a.mp4", "gif"},
		{"the format option", byFormat, map[string]string{"format": "mkv"}, "a.mp4", "mkv"},
		{"keep means the input's own", byFormat, map[string]string{"format": "keep"}, "a.MOV", "mov"},
		{"no format means the input's own", byFormat, nil, "a.flac", "flac"},
		{"a video stays a video", keepsVideo, map[string]string{"format": "mp3"}, "a.mkv", "mkv"},
		{"sound follows the format", keepsVideo, map[string]string{"format": "mp3"}, "a.wav", "mp3"},
	}
	for _, c := range cases {
		if got := c.tool.ExtensionFor(c.opts, c.input); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestEscapeFilterValueEscapesBothLevels checks a value is escaped for its
// option and then for the filtergraph, the forms verified against ffmpeg.
func TestEscapeFilterValueEscapesBothLevels(t *testing.T) {
	cases := map[string]string{
		`plain`:       `plain`,
		`C:\a`:        `C\\:\\\\a`,
		`it's`:        `it\\\'s`,
		`[x],y;z`:     `\[x\]\,y\;z`,
		`C:/Films/'`:  `C\\:/Films/\\\'`,
		`a:b`:         `a\\:b`,
		`100%`:        `100%`,
		`two  spaces`: `two  spaces`,
	}
	for in, want := range cases {
		if got := escapeFilterValue(in); got != want {
			t.Errorf("escapeFilterValue(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestConcatListQuotesEveryPath checks the join list survives apostrophes.
func TestConcatListQuotesEveryPath(t *testing.T) {
	got := concatList([]string{`C:\clips\one.mp4`, `C:\clips\Ocean's.mp4`})
	want := "ffconcat version 1.0\n" +
		"file 'C:\\clips\\one.mp4'\n" +
		"file 'C:\\clips\\Ocean'\\''s.mp4'\n"
	if got != want {
		t.Errorf("concatList =\n%s\nwant\n%s", got, want)
	}
}

// TestSizeBudgetLeavesHeadroom checks the bitrate for a size keeps the
// margin for container overhead, and the smallest sensible size is enough
// for the minimum picture and the sound.
func TestSizeBudgetLeavesHeadroom(t *testing.T) {
	// 10 MB over 100 s is 800 kbit/s, less the 5% margin.
	if got := sizeBudgetKbps(10, 100); got != 760 {
		t.Errorf("sizeBudgetKbps(10, 100) = %d, want 760", got)
	}
	least := smallestSensibleMB(600, 128)
	if budget := sizeBudgetKbps(float64(least), 600); budget < minVideoKbps+128 {
		t.Errorf("the smallest sensible size, %d MB, leaves only %d kbit/s", least, budget)
	}
	if budget := sizeBudgetKbps(float64(least-1), 600); budget >= minVideoKbps+128 {
		t.Errorf("%d MB would do, so %d MB isn't the smallest", least-1, least)
	}
}

// TestAutoHeightFollowsTheBitrate checks lower bitrates get smaller frames.
func TestAutoHeightFollowsTheBitrate(t *testing.T) {
	cases := map[int]int{200: 360, 400: 480, 899: 480, 1500: 720, 3000: 1080, 8000: 0}
	for kbps, want := range cases {
		if got := autoHeight(kbps); got != want {
			t.Errorf("autoHeight(%d) = %d, want %d", kbps, got, want)
		}
	}
}

// TestCompressToSizeRefusesImpossibleTargets checks a target too small for
// the length says so, rather than producing an unwatchable file.
func TestCompressToSizeRefusesImpossibleTargets(t *testing.T) {
	tool, _ := Get("video.compress")
	opts, _ := tool.Validate(map[string]string{"size": "1"})
	_, err := tool.Build(Inputs{Path: "a.mp4", Output: "b.mp4", Options: opts, DurationS: 3600}, softwareEncode())
	if err == nil {
		t.Fatal("1 MB for an hour was accepted")
	}
	if !strings.Contains(err.Error(), "MB") {
		t.Errorf("the error should name a size that would work: %v", err)
	}
}

// TestChangeContainerCopiesStreams checks nothing is re-encoded and
// subtitles are converted only where the container needs it.
func TestChangeContainerCopiesStreams(t *testing.T) {
	tool, _ := Get("video.remux")
	for format, subs := range map[string]string{"mp4": "mov_text", "mkv": "", "webm": "webvtt"} {
		opts, _ := tool.Validate(map[string]string{"format": format, "subs": "true"})
		plan, err := tool.Build(Inputs{Path: "a.mov", Output: "b." + format, Options: opts}, softwareEncode())
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if !argsContain(plan.Args, "-c", "copy") {
			t.Errorf("%s: streams are not copied: %v", format, plan.Args)
		}
		if subs != "" && !argsContain(plan.Args, "-c:s", subs) {
			t.Errorf("%s: subtitles should become %s: %v", format, subs, plan.Args)
		}
	}
}

// TestTranscribeNeedsAModel checks Transcribe explains what's missing.
func TestTranscribeNeedsAModel(t *testing.T) {
	tool, _ := Get("subtitles.transcribe")
	opts, _ := tool.Validate(nil)
	enc := softwareEncode()
	enc.SpeechModel = ""
	_, err := tool.Build(Inputs{Path: "a.mp4", Output: "a.srt", Options: opts}, enc)
	if err == nil || !strings.Contains(err.Error(), "Settings") {
		t.Errorf("without a model: %v, want a pointer to Settings", err)
	}
	if tool.Requires != RequireSpeechModel {
		t.Error("the interface can't tell Transcribe needs a model")
	}
}
