package tools

import (
	"slices"
	"strings"
	"testing"
)

// buildVideoDownload validates opts the way the app does, then builds
func buildVideoDownload(t *testing.T, opts map[string]string) Plan {
	t.Helper()
	tool, err := Get("download.video")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := tool.Validate(opts)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, err := tool.Build(defaultInputs(tool, valid), softwareEncode())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return plan
}

// TestDownloadVideoComesOutInTheChosenContainer checks a single-file download
// is remuxed too, since merging alone only applies to separate streams
func TestDownloadVideoComesOutInTheChosenContainer(t *testing.T) {
	for _, c := range []string{"mp4", "mkv", "webm", "mov"} {
		plan := buildVideoDownload(t, map[string]string{"container": c})
		if !argsContain(plan.Args, "--remux-video", c) || !argsContain(plan.Args, "--merge-output-format", c) {
			t.Errorf("%s: not merged and remuxed into %s: %v", c, c, plan.Args)
		}
		thumb := slices.Contains(plan.Args, "--embed-thumbnail")
		if thumb == (c == "webm") {
			t.Errorf("%s: embeds a thumbnail = %v, want %v", c, thumb, c != "webm")
		}
	}
}

// TestDownloadWebMTakesOnlyWebMStreams checks H.264 and AAC never reach a
// WebM merge, which would fail
func TestDownloadWebMTakesOnlyWebMStreams(t *testing.T) {
	for _, q := range []string{"best", "1080"} {
		plan := buildVideoDownload(t, map[string]string{"container": "webm", "quality": q})
		for _, alt := range strings.Split(argValue(plan.Args, "-f"), "/") {
			for _, part := range strings.Split(alt, "+") {
				if !strings.Contains(part, "[ext=webm]") {
					t.Errorf("quality %s: %q can pick a non-WebM stream", q, part)
				}
			}
		}
	}
}

// TestDownloadMOVTakesOnlyH264AndAAC checks VP9, AV1 and Opus never reach a
// MOV merge, which ffmpeg's mov muxer refuses, apart from the last resort
// for sites that don't name their codecs
func TestDownloadMOVTakesOnlyH264AndAAC(t *testing.T) {
	for _, q := range []string{"best", "2160", "1080"} {
		plan := buildVideoDownload(t, map[string]string{"container": "mov", "quality": q})
		for _, alt := range strings.Split(argValue(plan.Args, "-f"), "/") {
			if strings.Contains(alt, "[ext=mp4]") && !strings.Contains(alt, "vcodec") {
				continue
			}
			parts := strings.Split(alt, "+")
			if !strings.Contains(parts[0], "[vcodec^=avc1]") {
				t.Errorf("quality %s: %q can pick video that isn't H.264", q, alt)
			}
			if len(parts) > 1 && !strings.Contains(parts[1], "[acodec^=mp4a]") {
				t.Errorf("quality %s: %q can pick audio that isn't AAC", q, alt)
			}
		}
	}
}

// TestDownloadGIFPrefersH264 checks the GIF's source is H.264 where the site
// has it, since the exact cut re-encodes in the source codec and VP9 is slow
func TestDownloadGIFPrefersH264(t *testing.T) {
	for _, q := range []string{"best", "1080"} {
		plan := buildVideoDownload(t, map[string]string{"container": "gif", "quality": q})
		first := strings.Split(argValue(plan.Args, "-f"), "/")[0]
		if !strings.Contains(first, "[vcodec^=avc1]") {
			t.Errorf("quality %s: first choice %q isn't H.264", q, first)
		}
	}
}

// TestDownloadGIFRecodesTheChosenClip checks the default GIF is a short,
// exactly cut, silent clip using the palette graph
func TestDownloadGIFRecodesTheChosenClip(t *testing.T) {
	plan := buildVideoDownload(t, map[string]string{"container": "gif", "subs": "true", "sponsorblock": "true"})

	if !argsContain(plan.Args, "--recode-video", "gif") {
		t.Errorf("not recoded to gif: %v", plan.Args)
	}
	if !argsContain(plan.Args, "--download-sections", "*00:00:00-00:00:10") ||
		!slices.Contains(plan.Args, "--force-keyframes-at-cuts") {
		t.Errorf("the default clip isn't cut exactly: %v", plan.Args)
	}
	pp := argValue(plan.Args, "--postprocessor-args")
	for _, want := range []string{"VideoConvertor:", "-an", "-sn", "palettegen", "paletteuse", "fps=15", "scale=480:-1"} {
		if !strings.Contains(pp, want) {
			t.Errorf("postprocessor args %q are missing %q", pp, want)
		}
	}
	// Hidden for a GIF, so their remembered values must not apply
	for _, flag := range []string{"--merge-output-format", "--embed-subs", "--sponsorblock-remove", "--embed-thumbnail"} {
		if slices.Contains(plan.Args, flag) {
			t.Errorf("a GIF download passes %s: %v", flag, plan.Args)
		}
	}
}

// TestDownloadGIFRanges covers fractions and a blank end, which falls back to
// the default rather than running to the end of the video
func TestDownloadGIFRanges(t *testing.T) {
	cases := []struct {
		start, end, want string
	}{
		{"00:00:05.5", "00:00:08", "*00:00:05.5-00:00:08"},
		{"00:00:02", "", "*00:00:02-00:00:10"},
		{"", "00:01:00", "*00:00:00-00:01:00"},
	}
	for _, c := range cases {
		plan := buildVideoDownload(t, map[string]string{"container": "gif", "start": c.start, "end": c.end})
		if got := argValue(plan.Args, "--download-sections"); got != c.want {
			t.Errorf("%s to %q: section %q, want %q", c.start, c.end, got, c.want)
		}
	}
}

// TestDownloadGIFRejectsBadTimes checks values that would read as a negative
// time, a chapter name or a backwards range for yt-dlp never reach it
func TestDownloadGIFRejectsBadTimes(t *testing.T) {
	tool, _ := Get("download.video")
	bad := []struct{ start, end string }{
		{"00:00:10", "00:00:05"},
		{"00:00:05", "00:00:05"},
		{"-00:00:05", "00:00:10"},
		{"00:00:00", "00:00:1e3"},
		{"00:00:00", "00:00:10,intro"},
		{"00:00", "00:00:10"},
		{"00:00:00", "inf"},
		{"00:01:30", ""},
	}
	for _, b := range bad {
		opts, err := tool.Validate(map[string]string{"container": "gif", "start": b.start, "end": b.end})
		if err != nil {
			continue
		}
		if _, err := tool.Build(defaultInputs(tool, opts), softwareEncode()); err == nil {
			t.Errorf("%q to %q was accepted", b.start, b.end)
		}
	}
}

// TestDownloadGIFWholeVideo checks Whole Video downloads without a section,
// and ignores clip times it keeps while hidden, even bad ones
func TestDownloadGIFWholeVideo(t *testing.T) {
	plan := buildVideoDownload(t, map[string]string{"container": "gif", "span": "original",
		"start": "00:00:10", "end": "00:00:05"})
	for _, flag := range []string{"--download-sections", "--force-keyframes-at-cuts"} {
		if slices.Contains(plan.Args, flag) {
			t.Errorf("a whole-video GIF passes %s: %v", flag, plan.Args)
		}
	}
	if !argsContain(plan.Args, "--recode-video", "gif") {
		t.Errorf("not recoded to gif: %v", plan.Args)
	}
}

// TestDownloadGIFOriginalSizeAndRate checks Original drops the scale and
// keeps the source frame rate, capped at the 50 fps a GIF can play
func TestDownloadGIFOriginalSizeAndRate(t *testing.T) {
	plan := buildVideoDownload(t, map[string]string{"container": "gif", "sizing": "original", "pace": "original"})
	pp := argValue(plan.Args, "--postprocessor-args")
	if strings.Contains(pp, "scale=") {
		t.Errorf("original width still scales: %q", pp)
	}
	if !strings.Contains(pp, `-vf "fps='min(source_fps,50)',split`) {
		t.Errorf("original frame rate isn't the capped source rate, quoted for yt-dlp: %q", pp)
	}

	set := argValue(buildVideoDownload(t, map[string]string{"container": "gif", "width": "320", "fps": "10"}).Args,
		"--postprocessor-args")
	if !strings.Contains(set, `-vf "fps=10,scale=320:-1:flags=lanczos,split`) {
		t.Errorf("set width and rate are missing: %q", set)
	}
}

// TestDownloadGIFPaletteFitsMemory checks one palette over the whole GIF is
// kept for small clips only, since it holds every frame until the end
func TestDownloadGIFPaletteFitsMemory(t *testing.T) {
	cases := []struct {
		name string
		opts map[string]string
		one  bool
	}{
		{"default 10 s clip", map[string]string{}, true},
		{"whole video", map[string]string{"span": "original"}, false},
		{"original width", map[string]string{"sizing": "original"}, false},
		{"original rate", map[string]string{"pace": "original"}, false},
		{"long wide clip", map[string]string{"end": "00:01:00", "width": "1280", "fps": "30"}, false},
	}
	for _, c := range cases {
		c.opts["container"] = "gif"
		pp := argValue(buildVideoDownload(t, c.opts).Args, "--postprocessor-args")
		if one := strings.Contains(pp, "stats_mode=diff"); one != c.one {
			t.Errorf("%s: one palette = %v, want %v: %q", c.name, one, c.one, pp)
		}
		if !c.one && !strings.Contains(pp, "stats_mode=single") || !c.one && !strings.Contains(pp, "new=1") {
			t.Errorf("%s: no palette per frame: %q", c.name, pp)
		}
	}
}

// TestDownloadGIFOverwrites checks a GIF replaces one of the same name, which
// may be a cancelled conversion cut short or an earlier clip of the video,
// while other downloads keep yt-dlp's skip when the file is already there
func TestDownloadGIFOverwrites(t *testing.T) {
	gif := buildVideoDownload(t, map[string]string{"container": "gif"})
	if !slices.Contains(gif.Args, "--force-overwrites") {
		t.Errorf("a GIF download doesn't overwrite: %v", gif.Args)
	}
	mp4 := buildVideoDownload(t, map[string]string{"container": "mp4"})
	if slices.Contains(mp4.Args, "--force-overwrites") {
		t.Errorf("an MP4 download now overwrites: %v", mp4.Args)
	}
}
