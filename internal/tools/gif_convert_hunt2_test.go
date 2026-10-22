package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHunt2ConvertToGIFAwkwardNames runs Convert to GIF from and to names
// holding characters filtergraphs, image sequences and shells read as
// structure, with every dithering choice, and checks the palette goes
func TestHunt2ConvertToGIFAwkwardNames(t *testing.T) {
	bins := needFFmpeg(t)
	tool, err := Get("video.gif")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "it's [a], b; c=1 ü")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "100%done [x], y's.mp4")
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=320x180:r=15:d=2", "-c:v", "libx264", "-preset", "ultrafast", "-y", src)
	for _, dither := range []string{"sierra2_4a", "bayer", "none"} {
		t.Run(dither, func(t *testing.T) {
			out := filepath.Join(dir, "clip%d %03d "+dither+".gif")
			opts, err := tool.Validate(map[string]string{"dither": dither, "colors": "8"})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := tool.Build(Inputs{Path: src, Output: out, Options: opts}, softwareEncode())
			if err != nil {
				t.Fatal(err)
			}
			runBuilt(t, bins.FFmpeg, plan)
			info, err := exec.Command(bins.FFprobe, "-v", "error", "-show_entries", "stream=codec_name",
				"-of", "default=nw=1", out).Output()
			if err != nil || !strings.Contains(string(info), "codec_name=gif") {
				t.Errorf("no GIF at %q: %v %s", out, err, info)
			}
			if _, err := os.Stat(out + ".palette.png"); !os.IsNotExist(err) {
				t.Errorf("the palette is still beside the GIF: %v", err)
			}
		})
	}
}
