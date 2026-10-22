package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestGIFKeepsTheEdges checks the two-step GIF keeps still parts of the
// picture in their own colours, where a palette per frame that only redrew
// the changed rectangle left the edges on the last frame's colour numbers
// under a new palette, so they flashed wrong colours (worst frame 5.7 dB on a
// minute of 720p)
func TestGIFKeepsTheEdges(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	gif := filepath.Join(dir, "out.gif")
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "gradients=s=320x180:speed=0.02:c0=0x3060c0:c1=0xe0a040:c2=0x40c080:nb_colors=3:r=15",
		"-f", "lavfi", "-i", "testsrc2=s=100x76:r=15",
		"-filter_complex", "[0][1]overlay=x='110+50*sin(t)':y=52:shortest=1",
		"-t", "4", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "12", "-pix_fmt", "yuv420p", "-y", src)
	stages := gifDownloadStages(map[string]string{"sizing": gifOriginal, "pace": gifOriginal})
	runBuilt(t, bins.FFmpeg, gifTwoPass(src, gif, filepath.Join(dir, "palette.png"), stages, 256, "bayer"))

	stats := filepath.Join(dir, "psnr.log")
	// The filter reads the stats path as a filter option, so it runs from dir with a bare name
	cmd := exec.Command(bins.FFmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-i", gif, "-i", src, "-lavfi",
		"[0]format=rgb24,crop=40:ih:0:0[a];[1]format=rgb24,crop=40:ih:0:0[b];[a][b]psnr=stats_file=psnr.log", "-f", "null", "-")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("psnr: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(stats)
	if err != nil {
		t.Fatal(err)
	}
	worst, frames := 99.0, 0
	for _, line := range strings.Split(string(raw), "\n") {
		for _, f := range strings.Fields(line) {
			if v, ok := strings.CutPrefix(f, "psnr_avg:"); ok {
				frames++
				if d, err := strconv.ParseFloat(v, 64); err == nil && d < worst {
					worst = d
				}
			}
		}
	}
	if frames < 30 {
		t.Fatalf("only %d frames compared", frames)
	}
	if worst < 25 {
		t.Errorf("the left edge's worst frame is %.1f dB against the source, want 25 or more", worst)
	}
}

// runBuilt runs each of a built plan's commands with ffmpeg, then moves a staged
// result into place, the way the queue does
func runBuilt(t *testing.T, ffmpegBin string, p Plan) {
	t.Helper()
	for _, args := range p.Commands() {
		run(t, ffmpegBin, args...)
	}
	if p.Staged != "" {
		if err := os.Rename(p.Staged, p.OutputPath); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range p.Cleanup {
		_ = os.Remove(f)
	}
}
