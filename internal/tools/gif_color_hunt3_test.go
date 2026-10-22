package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"media-tools/internal/ffmpeg"
)

// hunt3Color probes path and describes its first video stream as the app does
func hunt3Color(t *testing.T, path string) SourceColor {
	t.Helper()
	info, err := ffmpeg.Probe(context.Background(), path)
	if err != nil {
		t.Fatalf("probe %s: %v", path, err)
	}
	for _, s := range info.Streams {
		if s.Type == "video" {
			return SourceColor{Space: s.ColorSpace, Width: s.Width, Height: s.Height}
		}
	}
	t.Fatalf("%s has no video", path)
	return SourceColor{}
}

// hunt3Bars writes 1280x720 bars in pixFmt and rng, made with BT.709's
// matrix, then strips the tag unless keep names one, re-encoding with enc
func hunt3Bars(t *testing.T, bin, dir, name, size, pixFmt, rng, tag string, enc ...string) string {
	t.Helper()
	made := filepath.Join(dir, name+".made"+filepath.Ext(name))
	src := filepath.Join(dir, name)
	matrix := "bt709"
	if tag != "" && tag != "unknown" && tag != "bt709" {
		matrix = map[string]string{"bt470bg": "bt601", "smpte170m": "bt601", "bt2020nc": "bt2020"}[tag]
	}
	args := []string{"-f", "lavfi", "-i", "smptehdbars=s=" + size + ":r=10:d=1",
		"-vf", fmt.Sprintf("scale=out_color_matrix=%s:out_range=%s,format=%s", matrix, rng, pixFmt)}
	args = append(args, enc...)
	run(t, bin, append(args, "-color_range", rng, "-y", made)...)
	set := "setparams=colorspace=unknown:color_primaries=unknown:color_trc=unknown"
	if tag != "" {
		set = "setparams=colorspace=" + tag
	}
	run(t, bin, append(append([]string{"-i", made, "-vf", set}, enc...), "-y", src)...)
	return src
}

var hunt3Average = regexp.MustCompile(`average:([0-9.]+|inf)`)

// hunt3PSNR compares the GIF, blurred, with src as a player shows it in
// matrix and rng, scaled to the GIF's size
func hunt3PSNR(t *testing.T, bin, gif, src, matrix, rng string) float64 {
	t.Helper()
	info, err := ffmpeg.Probe(context.Background(), gif)
	if err != nil {
		t.Fatal(err)
	}
	w, h := info.Streams[0].Width, info.Streams[0].Height
	graph := fmt.Sprintf("[0]format=rgb24,gblur=sigma=6[a];"+
		"[1]fps=10,scale=in_color_matrix=%s:in_range=%s,format=rgb24,scale=%d:%d:flags=lanczos,gblur=sigma=6[b];"+
		"[a][b]psnr", matrix, rng, w, h)
	out, err := exec.Command(bin, "-hide_banner", "-nostdin", "-i", gif, "-i", src, "-lavfi", graph,
		"-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("psnr: %v\n%s", err, out)
	}
	m := hunt3Average.FindSubmatch(out)
	if m == nil {
		t.Fatalf("no psnr in\n%s", out)
	}
	if string(m[1]) == "inf" {
		return 99
	}
	v, _ := strconv.ParseFloat(string(m[1]), 64)
	return v
}

// hunt3Convert builds Convert to GIF for src as the worker does, once probed, and runs it
func hunt3Convert(t *testing.T, bin, src, out string, color SourceColor) Plan {
	t.Helper()
	tool, err := Get("video.gif")
	if err != nil {
		t.Fatal(err)
	}
	opts, err := tool.Validate(map[string]string{"fps": "10", "width": "640"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := tool.Build(Inputs{Path: src, Output: out, Options: opts, Color: color}, softwareEncode())
	if err != nil {
		t.Fatal(err)
	}
	runBuilt(t, bin, p)
	return p
}

// TestGIFColorHunt3EveryUntaggedHDFormat checks the tag reaches the turn into
// RGB in both commands for each common way an untagged HD video is stored,
// limited and full range, 8 and 10 bit, 4:2:0, 4:4:4 and NV12, H.264, VP9 and
// AV1, so each GIF matches the video as a player shows it
func TestGIFColorHunt3EveryUntaggedHDFormat(t *testing.T) {
	bins := needFFmpeg(t)
	cases := []struct {
		name, pixFmt, rng string
		enc               []string
	}{
		{"x264.mp4", "yuv420p", "tv", []string{"-c:v", "libx264", "-crf", "10"}},
		{"x264-full.mp4", "yuvj420p", "pc", []string{"-c:v", "libx264", "-crf", "10"}},
		{"x264-full-yuv.mp4", "yuv420p", "pc", []string{"-c:v", "libx264", "-crf", "10"}},
		{"x264-444.mp4", "yuv444p", "tv", []string{"-c:v", "libx264", "-crf", "10"}},
		{"x264-10bit.mkv", "yuv420p10le", "tv", []string{"-c:v", "libx264", "-crf", "10"}},
		{"nv12.nut", "nv12", "tv", []string{"-c:v", "rawvideo"}},
		{"vp9.webm", "yuv420p", "tv", []string{"-c:v", "libvpx-vp9", "-crf", "20", "-b:v", "0", "-deadline", "realtime", "-cpu-used", "8"}},
		{"av1.webm", "yuv420p", "tv", []string{"-c:v", "libsvtav1", "-crf", "20", "-preset", "12"}},
		{"ffv1-444-10.mkv", "yuv444p10le", "tv", []string{"-c:v", "ffv1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			src := hunt3Bars(t, bins.FFmpeg, dir, c.name, "1280x720", c.pixFmt, c.rng, "", c.enc...)
			color := hunt3Color(t, src)
			if gifColorStage(color) == "" {
				t.Fatalf("probed %+v wasn't tagged", color)
			}
			gif := filepath.Join(dir, "out.gif")
			hunt3Convert(t, bins.FFmpeg, src, gif, color)
			if db := hunt3PSNR(t, bins.FFmpeg, gif, src, "bt709", c.rng); db < 40 {
				t.Errorf("GIF is %.1f dB from the source as a player shows it, want 40 or more", db)
			}
		})
	}
}

// TestGIFColorHunt3TaggedLeftAlone checks HD video that names its own
// matrix gets no tag and still converts in that matrix
func TestGIFColorHunt3TaggedLeftAlone(t *testing.T) {
	bins := needFFmpeg(t)
	cases := []struct {
		tag, matrix, pixFmt string
	}{
		{"bt709", "bt709", "yuv420p"},
		{"bt470bg", "bt601", "yuv420p"},
		{"smpte170m", "bt601", "yuv420p"},
		{"bt2020nc", "bt2020", "yuv420p10le"},
	}
	for _, c := range cases {
		t.Run(c.tag, func(t *testing.T) {
			dir := t.TempDir()
			src := hunt3Bars(t, bins.FFmpeg, dir, c.tag+".mkv", "1920x1080", c.pixFmt, "tv", c.tag,
				"-c:v", "libx264", "-crf", "10", "-preset", "ultrafast")
			color := hunt3Color(t, src)
			if color.Space != c.tag {
				t.Fatalf("probed %+v, want %s", color, c.tag)
			}
			if s := gifColorStage(color); s != "" {
				t.Fatalf("tagged %s source retagged %q", c.tag, s)
			}
			gif := filepath.Join(dir, "out.gif")
			hunt3Convert(t, bins.FFmpeg, src, gif, color)
			if db := hunt3PSNR(t, bins.FFmpeg, gif, src, c.matrix, "tv"); db < 40 {
				t.Errorf("GIF is %.1f dB from the source in %s, want 40 or more", db, c.matrix)
			}
		})
	}
}

// TestGIFColorHunt3RGBSourcesUnchanged checks an HD GIF or RGB video, which
// names no matrix but needs none, comes out the same with the tag as without
func TestGIFColorHunt3RGBSourcesUnchanged(t *testing.T) {
	bins := needFFmpeg(t)
	for _, c := range []struct{ name, codec, pixFmt string }{
		{"hd.gif", "gif", "bgra"}, {"rgb.mov", "qtrle", "rgb24"}, {"gbr.mkv", "libx264rgb", "gbrp"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, c.name)
			run(t, bins.FFmpeg, "-f", "lavfi", "-i", "smptehdbars=s=1280x720:r=10:d=1", "-vf", "format="+c.pixFmt,
				"-c:v", c.codec, "-y", src)
			color := hunt3Color(t, src)
			hd := SourceColor{Width: color.Width, Height: color.Height}
			with := filepath.Join(dir, "with.gif")
			without := filepath.Join(dir, "without.gif")
			hunt3Convert(t, bins.FFmpeg, src, with, hd)
			hunt3Convert(t, bins.FFmpeg, src, without, SourceColor{})
			out, err := exec.Command(bins.FFmpeg, "-hide_banner", "-nostdin", "-i", with, "-i", without,
				"-lavfi", "[0]format=rgb24[a];[1]format=rgb24[b];[a][b]psnr", "-f", "null", "-").CombinedOutput()
			if err != nil {
				t.Fatalf("psnr: %v\n%s", err, out)
			}
			if m := hunt3Average.FindSubmatch(out); m == nil || string(m[1]) != "inf" {
				t.Errorf("probed %+v, the tag changed an RGB source's GIF: %s", color, m)
			}
		})
	}
}

// TestGIFColorHunt3ShapesPlayersCallHD checks rotated phone video, anamorphic
// HD and wide but short video are tagged and come out right, while
// anamorphic PAL is left as SD
func TestGIFColorHunt3ShapesPlayersCallHD(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	base := hunt3Bars(t, bins.FFmpeg, dir, "base.mp4", "1280x720", "yuv420p", "tv", "",
		"-c:v", "libx264", "-crf", "10")

	rotated := filepath.Join(dir, "rotated.mp4")
	run(t, bins.FFmpeg, "-display_rotation", "90", "-i", base, "-c", "copy", "-y", rotated)
	anamorphic := hunt3Bars(t, bins.FFmpeg, dir, "hdv.mp4", "1440x1080", "yuv420p", "tv", "",
		"-c:v", "libx264", "-crf", "10", "-aspect", "16:9")
	wide := hunt3Bars(t, bins.FFmpeg, dir, "wide.mp4", "1920x200", "yuv420p", "tv", "",
		"-c:v", "libx264", "-crf", "10")
	for _, src := range []string{rotated, anamorphic, wide} {
		color := hunt3Color(t, src)
		if gifColorStage(color) == "" {
			t.Errorf("%s probed %+v wasn't tagged", filepath.Base(src), color)
			continue
		}
		gif := strings.TrimSuffix(src, filepath.Ext(src)) + ".gif"
		hunt3Convert(t, bins.FFmpeg, src, gif, color)
		if db := hunt3PSNR(t, bins.FFmpeg, gif, src, "bt709", "tv"); db < 40 {
			t.Errorf("%s GIF is %.1f dB from the source, want 40 or more", filepath.Base(src), db)
		}
	}

	pal := hunt3Bars(t, bins.FFmpeg, dir, "pal.mp4", "720x576", "yuv420p", "tv", "",
		"-c:v", "libx264", "-crf", "10", "-aspect", "16:9")
	if s := gifColorStage(hunt3Color(t, pal)); s != "" {
		t.Errorf("anamorphic PAL was tagged as HD: %q", s)
	}
}

// TestGIFColorHunt3DownloadEveryPaceAndSize runs a GIF download's finish on
// an untagged HD file for each mix of set and original pace and width, so the
// tag before the source_fps expression or no scale at all still works
func TestGIFColorHunt3DownloadEveryPaceAndSize(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	src := hunt3Bars(t, bins.FFmpeg, dir, "Clip [x].mp4", "1280x720", "yuv420p", "tv", "",
		"-c:v", "libx264", "-crf", "10")
	color := hunt3Color(t, src)
	tool, err := Get("download.video")
	if err != nil {
		t.Fatal(err)
	}
	for _, pace := range []string{gifSet, gifOriginal} {
		for _, sizing := range []string{gifSet, gifOriginal} {
			name := pace + "-" + sizing
			t.Run(name, func(t *testing.T) {
				opts, err := tool.Validate(map[string]string{"container": "gif", "pace": pace, "sizing": sizing, "fps": "10"})
				if err != nil {
					t.Fatal(err)
				}
				work, out := t.TempDir(), t.TempDir()
				p, err := gifFinish(opts, work, out)(src, color)
				if err != nil {
					t.Fatal(err)
				}
				if pal, draw := gifGraphs(p); !strings.HasPrefix(pal, "setparams=colorspace=bt709,") ||
					!strings.HasPrefix(draw, "[0:v]setparams=colorspace=bt709,") {
					t.Fatalf("palette %q, drawing %q", pal, draw)
				}
				runBuilt(t, bins.FFmpeg, p)
				if db := hunt3PSNR(t, bins.FFmpeg, p.OutputPath, src, "bt709", "tv"); db < 40 {
					t.Errorf("GIF is %.1f dB from the source, want 40 or more", db)
				}
			})
		}
	}
}
