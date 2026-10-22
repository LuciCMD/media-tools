package tools

import (
	"errors"
	"path/filepath"
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

// testDownloaded stands for the video a GIF download left in its work folder
const testDownloaded = `C:\downloads\.mediatools-test\Clip [abc].mp4`

// gifFinishOf returns the commands that make the GIF from what plan downloads
func gifFinishOf(t *testing.T, plan Plan) Plan {
	t.Helper()
	if plan.Finish == nil || plan.FinishCount != 2 {
		t.Fatalf("a GIF download isn't finished by two commands: %v %d", plan.Finish == nil, plan.FinishCount)
	}
	fp, err := plan.Finish(testDownloaded, SourceColor{})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if n := len(fp.Commands()); n != plan.FinishCount {
		t.Fatalf("Finish gave %d commands, FinishCount says %d", n, plan.FinishCount)
	}
	return fp
}

// gifGraphs returns the palette command's graph and the drawing command's
func gifGraphs(fp Plan) (palette, draw string) {
	return argValue(fp.Args, "-vf"), argValue(fp.Passes[0], "-lavfi")
}

// TestDownloadGIFDownloadsTheChosenClip checks the default GIF downloads a
// short, exactly cut clip into its work folder, which ffmpeg then makes into
// a silent GIF with one palette, where yt-dlp used to convert it in one go
func TestDownloadGIFDownloadsTheChosenClip(t *testing.T) {
	plan := buildVideoDownload(t, map[string]string{"container": "gif", "subs": "true", "sponsorblock": "true"})

	if !argsContain(plan.Args, "--download-sections", "*00:00:00-00:00:10") ||
		!slices.Contains(plan.Args, "--force-keyframes-at-cuts") {
		t.Errorf("the default clip isn't cut exactly: %v", plan.Args)
	}
	if !strings.HasPrefix(argValue(plan.Args, "-o"), `C:\downloads\.mediatools-test/`) {
		t.Errorf("the video doesn't land in the work folder: %q", argValue(plan.Args, "-o"))
	}
	// Hidden for a GIF, so their remembered values must not apply, and yt-dlp no longer converts
	for _, flag := range []string{"--merge-output-format", "--embed-subs", "--sponsorblock-remove", "--embed-thumbnail",
		"--recode-video", "--postprocessor-args", "--force-overwrites"} {
		if slices.Contains(plan.Args, flag) {
			t.Errorf("a GIF download passes %s: %v", flag, plan.Args)
		}
	}

	fp := gifFinishOf(t, plan)
	palette, draw := gifGraphs(fp)
	if palette != "fps=15,scale=480:-1:flags=lanczos,palettegen=max_colors=256:stats_mode=diff" {
		t.Errorf("palette graph %q", palette)
	}
	if draw != "[0:v]fps=15,scale=480:-1:flags=lanczos[x];[x][1:v]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle" {
		t.Errorf("drawing graph %q", draw)
	}
	for _, cmd := range fp.Commands() {
		if !argsContain(cmd, "-i", testDownloaded) || !slices.Contains(cmd, "-an") || !slices.Contains(cmd, "-sn") {
			t.Errorf("a command doesn't read the download silently: %q", cmd)
		}
	}
	if !slices.Equal(fp.Steps, []string{"Making the palette", "Drawing the GIF"}) {
		t.Errorf("steps %q", fp.Steps)
	}
}

// TestDownloadGIFIsMovedOutWhole checks the GIF is drawn inside the work
// folder and named after the download in the output folder, so a cancelled
// conversion leaves nothing half made beside the person's files, and a
// download that's itself a .gif isn't read and written at once
func TestDownloadGIFIsMovedOutWhole(t *testing.T) {
	fp := gifFinishOf(t, buildVideoDownload(t, map[string]string{"container": "gif"}))
	work := `C:\downloads\.mediatools-test`
	if fp.Staged != filepath.Join(work, "result.gif") || fp.Passes[0][len(fp.Passes[0])-1] != fp.Staged {
		t.Errorf("drawn to %q, staged %q", fp.Passes[0][len(fp.Passes[0])-1], fp.Staged)
	}
	if fp.OutputPath != filepath.Join(`C:\downloads`, "Clip [abc].gif") {
		t.Errorf("output %q", fp.OutputPath)
	}
	if !slices.Equal(fp.Cleanup, []string{filepath.Join(work, "palette.png")}) {
		t.Errorf("cleanup %q", fp.Cleanup)
	}

	plan := buildVideoDownload(t, map[string]string{"container": "gif"})
	gif, err := plan.Finish(filepath.Join(work, "Funny [x1].gif"), SourceColor{})
	if err != nil {
		t.Fatal(err)
	}
	if gif.Staged == filepath.Join(work, "Funny [x1].gif") || gif.OutputPath != filepath.Join(`C:\downloads`, "Funny [x1].gif") {
		t.Errorf("a .gif download is staged at %q for %q", gif.Staged, gif.OutputPath)
	}
	if _, err := plan.Finish("", SourceColor{}); err == nil {
		t.Error("no downloaded file was accepted")
	}
}

// TestDownloadGIFNeedsItsWorkFolder checks a GIF can't be built to download
// straight into the output folder, where the video would be left beside it
func TestDownloadGIFNeedsItsWorkFolder(t *testing.T) {
	tool, _ := Get("download.video")
	opts, _ := tool.Validate(map[string]string{"container": "gif"})
	in := defaultInputs(tool, opts)
	in.WorkDir = " "
	if _, err := tool.Build(in, softwareEncode()); !errors.Is(err, ErrNeedsWorkDir) {
		t.Errorf("a GIF without a work folder gave %v, want ErrNeedsWorkDir", err)
	}
	// Bad times are refused first, so no folder is made for a job that can't run
	bad, _ := tool.Validate(map[string]string{"container": "gif", "start": "00:00:09", "end": "00:00:01"})
	in = defaultInputs(tool, bad)
	in.WorkDir = ""
	if _, err := tool.Build(in, softwareEncode()); err == nil || errors.Is(err, ErrNeedsWorkDir) {
		t.Errorf("a backwards clip without a work folder gave %v, want its own error", err)
	}
	mp4, _ := tool.Validate(map[string]string{"container": "mp4"})
	in = defaultInputs(tool, mp4)
	in.WorkDir = ""
	plan, err := tool.Build(in, softwareEncode())
	if err != nil || plan.Finish != nil {
		t.Errorf("an MP4 needs a work folder or a finish: %v %v", err, plan.Finish != nil)
	}
}

// TestDownloadGIFPaletteChoices checks Palette Size and Dithering reach the
// palette and the drawing
func TestDownloadGIFPaletteChoices(t *testing.T) {
	fp := gifFinishOf(t, buildVideoDownload(t, map[string]string{"container": "gif", "colors": "64", "dither": "sierra2_4a"}))
	palette, draw := gifGraphs(fp)
	if !strings.Contains(palette, "max_colors=64:") || !strings.Contains(draw, "dither=sierra2_4a:") {
		t.Errorf("palette %q, drawing %q", palette, draw)
	}
	tool, _ := Get("download.video")
	for _, bad := range []map[string]string{{"colors": "512"}, {"colors": "4"}, {"dither": "floyd_steinberg"}, {"dither": "bayer:bayer_scale=5"}} {
		bad["container"] = "gif"
		if _, err := tool.Validate(bad); err == nil {
			t.Errorf("%v passes validation", bad)
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
	gifFinishOf(t, plan)
}

// TestDownloadGIFOriginalSizeAndRate checks Original drops the scale and
// keeps the source frame rate, capped at the 50 fps a GIF can play, in both
// the palette and the drawing
func TestDownloadGIFOriginalSizeAndRate(t *testing.T) {
	palette, draw := gifGraphs(gifFinishOf(t,
		buildVideoDownload(t, map[string]string{"container": "gif", "sizing": "original", "pace": "original"})))
	// The scale filter, not Bayer's bayer_scale
	if strings.Contains(palette+draw, ",scale=") || strings.Contains(palette+draw, "]scale=") {
		t.Errorf("original width still scales: %q %q", palette, draw)
	}
	if !strings.HasPrefix(palette, "fps='min(source_fps,50)',palettegen") ||
		!strings.HasPrefix(draw, "[0:v]fps='min(source_fps,50)'[x]") {
		t.Errorf("original frame rate isn't the capped source rate: %q %q", palette, draw)
	}

	palette, draw = gifGraphs(gifFinishOf(t,
		buildVideoDownload(t, map[string]string{"container": "gif", "width": "320", "fps": "10"})))
	if !strings.HasPrefix(palette, "fps=10,scale=320:-1:flags=lanczos,") ||
		!strings.HasPrefix(draw, "[0:v]fps=10,scale=320:-1:flags=lanczos[x]") {
		t.Errorf("set width and rate are missing: %q %q", palette, draw)
	}
}

// TestDownloadGIFUsesOnePalette checks every GIF, however long or large, gets
// one palette from its own command, where a long one used a palette per
// frame that flashed and crawled, and a short one held every frame in memory
func TestDownloadGIFUsesOnePalette(t *testing.T) {
	for _, opts := range []map[string]string{
		{},
		{"span": "original"},
		{"sizing": "original"},
		{"pace": "original"},
		{"end": "00:01:00", "width": "1280", "fps": "30"},
	} {
		opts["container"] = "gif"
		palette, draw := gifGraphs(gifFinishOf(t, buildVideoDownload(t, opts)))
		if !strings.Contains(palette, "stats_mode=diff") || strings.Contains(palette+draw, "split") ||
			strings.Contains(palette+draw, "stats_mode=single") || strings.Contains(draw, "new=1") {
			t.Errorf("%v: palette %q, drawing %q", opts, palette, draw)
		}
	}
}
