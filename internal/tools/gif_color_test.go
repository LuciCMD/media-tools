package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestGIFColorStageFollowsPlayers checks only HD video that names no colour
// standard is tagged with HD's, as a player assumes, while tagged, SD and
// unprobed video is left alone
func TestGIFColorStageFollowsPlayers(t *testing.T) {
	tag := "setparams=colorspace=bt709,"
	cases := []struct {
		c    SourceColor
		want string
	}{
		{SourceColor{"", 1280, 720}, tag},
		{SourceColor{"unknown", 1920, 1080}, tag},
		{SourceColor{"", 1280, 544}, tag},
		{SourceColor{"", 720, 1280}, tag},
		{SourceColor{"", 1024, 600}, tag},
		{SourceColor{"bt709", 1920, 1080}, ""},
		{SourceColor{"bt470bg", 1920, 1080}, ""},
		{SourceColor{"", 854, 480}, ""},
		{SourceColor{"", 720, 576}, ""},
		{SourceColor{}, ""},
	}
	for _, c := range cases {
		if got := gifColorStage(c.c); got != c.want {
			t.Errorf("%+v gave %q, want %q", c.c, got, c.want)
		}
	}
}

// TestGIFsTagUntaggedHD checks both a GIF download and Convert to GIF put the
// tag first in the palette and the drawing, before any scaling
func TestGIFsTagUntaggedHD(t *testing.T) {
	plan := buildVideoDownload(t, map[string]string{"container": "gif"})
	fp, err := plan.Finish(testDownloaded, SourceColor{Width: 1280, Height: 720})
	if err != nil {
		t.Fatal(err)
	}
	palette, draw := gifGraphs(fp)
	if !strings.HasPrefix(palette, "setparams=colorspace=bt709,fps=15,scale=480") ||
		!strings.HasPrefix(draw, "[0:v]setparams=colorspace=bt709,fps=15,scale=480") {
		t.Errorf("download: palette %q, drawing %q", palette, draw)
	}
	tagged, _ := plan.Finish(testDownloaded, SourceColor{Space: "bt709", Width: 1280, Height: 720})
	if p, _ := gifGraphs(tagged); strings.Contains(p, "setparams") {
		t.Errorf("a tagged download was retagged: %q", p)
	}

	tool, _ := Get("video.gif")
	if !tool.NeedsColor || tool.NeedsStreams {
		t.Fatal("Convert to GIF is built before its source's colours are known")
	}
	opts, _ := tool.Validate(nil)
	convert, err := tool.Build(Inputs{Path: "a.mp4", Output: "a.gif", Options: opts,
		Color: SourceColor{Width: 1920, Height: 1080}}, softwareEncode())
	if err != nil {
		t.Fatal(err)
	}
	if p := argValue(convert.Args, "-vf"); !strings.HasPrefix(p, "setparams=colorspace=bt709,fps=") {
		t.Errorf("Convert to GIF palette %q", p)
	}
}

// TestGIFOfUntaggedHDKeepsItsColours makes a GIF of HD colour bars that name
// no colour standard and compares it, blurred so only true colour shifts
// count and not dither grain, with the bars as a player shows them, where
// ffmpeg's SD assumption made greens brighter and reds and blues duller
// (30 dB, against 51 dB once tagged)
func TestGIFOfUntaggedHDKeepsItsColours(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	tagged := filepath.Join(dir, "tagged.mp4")
	src := filepath.Join(dir, "bars.mp4")
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "smptehdbars=s=1280x720:r=10:d=2",
		"-vf", "scale=out_color_matrix=bt709:out_range=tv,format=yuv420p", "-c:v", "libx264", "-crf", "10",
		"-colorspace", "bt709", "-color_range", "tv", "-y", tagged)
	run(t, bins.FFmpeg, "-i", tagged,
		"-vf", "setparams=colorspace=unknown:color_primaries=unknown:color_trc=unknown",
		"-c:v", "libx264", "-crf", "10", "-y", src)
	gif := filepath.Join(dir, "bars.gif")
	stages := gifColorStage(SourceColor{Width: 1280, Height: 720}) + "fps=10"
	runBuilt(t, bins.FFmpeg, gifTwoPass(src, gif, filepath.Join(dir, "palette.png"), stages, 256, "bayer"))

	cmd := exec.Command(bins.FFmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-i", gif, "-i", src, "-lavfi",
		"[0]format=rgb24,gblur=sigma=6[a];[1]fps=10,scale=in_color_matrix=bt709:in_range=tv,format=rgb24,gblur=sigma=6[b];"+
			"[a][b]psnr=stats_file=psnr.log", "-f", "null", "-")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("psnr: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "psnr.log"))
	if err != nil {
		t.Fatal(err)
	}
	sum, n := 0.0, 0
	for _, f := range strings.Fields(string(raw)) {
		if v, ok := strings.CutPrefix(f, "psnr_avg:"); ok {
			if d, err := strconv.ParseFloat(v, 64); err == nil {
				sum, n = sum+d, n+1
			}
		}
	}
	if n == 0 {
		t.Fatal("nothing compared")
	}
	if mean := sum / float64(n); mean < 45 {
		t.Errorf("the GIF's colours are %.1f dB from the bars as a player shows them, want 45 or more", mean)
	}
}
