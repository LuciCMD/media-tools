package tools

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"media-tools/internal/ffmpeg"
	"media-tools/internal/giffix"
)

// huntProbeSize reads the first video stream's size the way the app does
func huntProbeSize(t *testing.T, path string) SourceColor {
	t.Helper()
	w, h, err := ffmpeg.ShownSize(context.Background(), path)
	if err != nil {
		t.Fatalf("probe %s: %v", path, err)
	}
	return SourceColor{Width: w, Height: h}
}

// huntRun runs ffmpeg, returning its output on failure instead of failing
func huntRun(bin string, args ...string) (string, error) {
	out, err := exec.Command(bin, append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, args...)...).CombinedOutput()
	return string(out), err
}

// huntMake runs the plan the way the queue does and decodes the GIF, keeping
// frames.mkv when keepFrames so the result can be compared with it
func huntMake(t *testing.T, bin string, in Inputs, keepFrames bool) (*gif.GIF, Plan, error) {
	t.Helper()
	p, err := buildImagesToGIF(in, Encode{})
	if err != nil {
		return nil, p, err
	}
	for _, f := range p.Files {
		if err := os.WriteFile(f.Path, []byte(f.Content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range p.Commands() {
		if out, err := huntRun(bin, args...); err != nil {
			return nil, p, fmt.Errorf("%v: %s", err, out)
		}
	}
	if p.Disposal != 0 {
		if err := giffix.SetDisposal(p.Staged, p.Disposal); err != nil {
			return nil, p, err
		}
	}
	if err := os.Rename(p.Staged, p.OutputPath); err != nil {
		t.Fatal(err)
	}
	for _, f := range filesOf(p) {
		_ = os.Remove(f)
	}
	if !keepFrames {
		for _, f := range p.Cleanup {
			_ = os.Remove(f)
		}
	}
	data, err := os.ReadFile(p.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return nil, p, err
	}
	return g, p, nil
}

func huntSolid(t *testing.T, bin, path, c, size string) {
	t.Helper()
	// -update 1 writes the name as given, where frame%d.png would become frame1.png
	run(t, bin, "-f", "lavfi", "-i", "color=c="+c+":s="+size, "-frames:v", "1", "-update", "1", "-y", path)
}

// TestHuntAwkwardNamesAreReadAsNamed feeds names a filtergraph or ffmpeg's image
// demuxer might read as syntax, each as the first image, and expects two frames
func TestHuntAwkwardNamesAreReadAsNamed(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	other := filepath.Join(dir, "other.png")
	huntSolid(t, bins.FFmpeg, other, "green", "64x48")
	// What a numbered-sequence reading of frame%d.png and frame%03d.png would open
	huntSolid(t, bins.FFmpeg, filepath.Join(dir, "frame1.png"), "blue", "64x48")
	huntSolid(t, bins.FFmpeg, filepath.Join(dir, "frame001.png"), "blue", "64x48")
	names := []string{
		"100% done.png",
		"50%.png",
		"frame%d.png",
		"frame%03d.png",
		"日本語 é ü.png",
		"a=b.png",
		" leading space.png",
		"x{1}.png",
		"a#b@c~$^&!.png",
		"semi;colon'quote,comma[1].png",
		"back`tick.png",
		"pipe-ish file_ concat_ name.png",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			src := filepath.Join(dir, name)
			huntSolid(t, bins.FFmpeg, src, "red", "64x48")
			out := filepath.Join(t.TempDir(), "out.gif")
			in := gifInputs(t, []string{src, other}, out, SourceColor{Width: 64, Height: 48},
				map[string]string{"sizing": gifOriginal})
			g, _, err := huntMake(t, bins.FFmpeg, in, false)
			if err != nil {
				t.Fatalf("%q as an image fails the job: %v", name, err)
			}
			if len(g.Image) != 2 {
				t.Errorf("%d frames, want 2", len(g.Image))
			}
			if c := shown(g)[0].RGBAAt(32, 24); !near(c, 255, 0, 0) {
				t.Errorf("the first frame is %v, not the red image named %q", c, name)
			}
		})
	}
}

// TestHuntDuplicatePaths uses the same image twice, which Run accepts
func TestHuntDuplicatePaths(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "1.png"), filepath.Join(dir, "2.png")
	huntSolid(t, bins.FFmpeg, a, "red", "64x48")
	huntSolid(t, bins.FFmpeg, b, "blue", "64x48")
	in := gifInputs(t, []string{a, b, a, a}, filepath.Join(dir, "o.gif"), SourceColor{Width: 64, Height: 48}, nil)
	g, _, err := huntMake(t, bins.FFmpeg, in, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Image) != 4 {
		t.Errorf("%d frames, want 4", len(g.Image))
	}
}

// TestHuntAnimatedInputsGiveOneFrameEach checks an animated PNG or WebP, both
// accepted extensions, adds one frame as the tool says, not all of its own
func TestHuntAnimatedInputsGiveOneFrameEach(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	still := filepath.Join(dir, "still.png")
	huntSolid(t, bins.FFmpeg, still, "red", "64x48")
	cases := map[string][]string{
		"anim.png":  {"-f", "apng", "-plays", "0"},
		"anim.webp": {"-c:v", "libwebp_anim", "-loop", "0"},
	}
	for name, enc := range cases {
		t.Run(name, func(t *testing.T) {
			src := filepath.Join(dir, name)
			args := append([]string{"-f", "lavfi", "-i", "testsrc2=s=64x48:r=5:d=2"}, enc...)
			if out, err := huntRun(bins.FFmpeg, append(args, "-y", src)...); err != nil {
				t.Skipf("can't make %s here: %s", name, out)
			}
			out := filepath.Join(t.TempDir(), "o.gif")
			in := gifInputs(t, []string{still, src, still}, out, SourceColor{Width: 64, Height: 48},
				map[string]string{"sizing": gifOriginal})
			g, p, err := huntMake(t, bins.FFmpeg, in, false)
			if err != nil {
				t.Fatalf("the job fails: %v", err)
			}
			if len(g.Image) != 3 {
				t.Errorf("three images made %d frames, the bar expecting %d", len(g.Image), p.Frames)
			}
		})
	}
}

// huntJPEGWithOrientation writes a w×h JPEG, left half red and right half
// blue as stored, tagged with EXIF orientation
func huntJPEGWithOrientation(t *testing.T, path string, w, h int, orientation uint16) {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{220, 0, 0, 255}
			if x >= w/2 {
				c = color.RGBA{0, 0, 220, 255}
			}
			im.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, im, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	// A little-endian TIFF with one IFD entry, Orientation
	var tiff bytes.Buffer
	tiff.WriteString("II")
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(42))
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(8))
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(1))
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(0x0112))
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(3))
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(1))
	_ = binary.Write(&tiff, binary.LittleEndian, orientation)
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(0))
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(0))
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	app1 := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(len(payload)+2))
	app1 = append(app1, payload...)
	out := append(append(append([]byte{}, data[:2]...), app1...), data[2:]...)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestHuntExifRotatedFirstImage checks a phone photo stored sideways with an
// orientation tag, the way every viewer shows it upright, comes out upright
func TestHuntExifRotatedFirstImage(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	// Stored 64×32, shown 32×64 since orientation 6 turns it a quarter clockwise
	photo := filepath.Join(dir, "photo.jpg")
	huntJPEGWithOrientation(t, photo, 64, 32, 6)
	other := filepath.Join(dir, "other.png")
	huntSolid(t, bins.FFmpeg, other, "green", "32x64")
	first := huntProbeSize(t, photo)
	t.Logf("ffprobe gives the first image as %d×%d", first.Width, first.Height)
	side, _ := exec.Command(bins.FFprobe, "-v", "error", "-show_entries", "stream_side_data:frame_side_data",
		"-show_frames", "-of", "compact", photo).CombinedOutput()
	t.Logf("side data: %s", side)
	out := filepath.Join(dir, "o.gif")
	g, _, err := huntMake(t, bins.FFmpeg, gifInputs(t, []string{photo, other}, out, first,
		map[string]string{"sizing": gifOriginal}), false)
	if err != nil {
		t.Fatal(err)
	}
	if g.Config.Width != 32 || g.Config.Height != 64 {
		t.Errorf("the GIF is %d×%d, want 32×64 as the photo is shown", g.Config.Width, g.Config.Height)
	}
	frames := shown(g)
	// Turned upright, the stored left half is on top
	if c := frames[0].RGBAAt(g.Config.Width/2, 4); !near(c, 220, 0, 0) {
		t.Errorf("the top of the first frame is %v, want the red half on top as viewers show it", c)
	}
}

// TestHuntExtremeShapes fits a very tall image into a very short canvas and
// the other way round, where a side could round to nothing
func TestHuntExtremeShapes(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	wide, tall, dot := filepath.Join(dir, "wide.png"), filepath.Join(dir, "tall.png"), filepath.Join(dir, "dot.png")
	huntSolid(t, bins.FFmpeg, wide, "red", "2000x10")
	huntSolid(t, bins.FFmpeg, tall, "blue", "10x2000")
	huntSolid(t, bins.FFmpeg, dot, "white", "1x1")
	cases := []struct {
		name  string
		paths []string
		first SourceColor
		opts  map[string]string
	}{
		{"wide then tall, bars", []string{wide, tall}, SourceColor{Width: 2000, Height: 10}, map[string]string{}},
		{"wide then tall, crop", []string{wide, tall}, SourceColor{Width: 2000, Height: 10}, map[string]string{"fit": "crop"}},
		{"wide then tall, 32", []string{wide, tall}, SourceColor{Width: 2000, Height: 10}, map[string]string{"width": "32"}},
		{"tall then wide, 32", []string{tall, wide}, SourceColor{Width: 10, Height: 2000}, map[string]string{"width": "32"}},
		{"dot first, original", []string{dot, wide, tall}, SourceColor{Width: 1, Height: 1}, map[string]string{"sizing": gifOriginal}},
		{"dot first, 1920", []string{dot, wide, tall}, SourceColor{Width: 1, Height: 1}, map[string]string{"width": "1920"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "o.gif")
			g, _, err := huntMake(t, bins.FFmpeg, gifInputs(t, c.paths, out, c.first, c.opts), false)
			if err != nil {
				t.Fatalf("the job fails: %v", err)
			}
			if len(g.Image) != len(c.paths) {
				t.Errorf("%d frames, want %d", len(g.Image), len(c.paths))
			}
		})
	}
}

// TestHuntPixelFormats runs stills in formats a camera, editor or screenshot
// tool writes, checking each one's centre colour survives
func TestHuntPixelFormats(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	write := func(name string, im image.Image) string {
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, im); err != nil {
			t.Fatal(err)
		}
		f.Close()
		return p
	}
	r := image.Rect(0, 0, 64, 48)
	gray := image.NewGray(r)
	gray16 := image.NewGray16(r)
	rgba64 := image.NewNRGBA64(r)
	pal := image.NewPaletted(r, color.Palette{color.NRGBA{0, 0, 0, 0}, color.NRGBA{0, 200, 0, 255}})
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			gray.SetGray(x, y, color.Gray{200})
			gray16.SetGray16(x, y, color.Gray16{200 << 8})
			rgba64.SetNRGBA64(x, y, color.NRGBA64{200 << 8, 100 << 8, 0, 0xffff})
			if x > 8 && x < 56 {
				pal.SetColorIndex(x, y, 1)
			}
		}
	}
	first := filepath.Join(dir, "first.png")
	huntSolid(t, bins.FFmpeg, first, "red", "64x48")
	cases := []struct {
		path    string
		r, g, b int
	}{
		{write("gray.png", gray), 200, 200, 200},
		{write("gray16.png", gray16), 200, 200, 200},
		{write("rgba64.png", rgba64), 200, 100, 0},
		{write("pal.png", pal), 0, 200, 0},
	}
	tiff16 := filepath.Join(dir, "rgb48.tif")
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "color=c=0x0000C8:s=64x48", "-frames:v", "1", "-pix_fmt", "rgb48le", "-y", tiff16)
	cases = append(cases, struct {
		path    string
		r, g, b int
	}{tiff16, 0, 0, 200})
	ya := filepath.Join(dir, "ya8.png")
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "color=c=0xC8C8C8:s=64x48,format=ya8", "-frames:v", "1", "-pix_fmt", "ya8", "-y", ya)
	cases = append(cases, struct {
		path    string
		r, g, b int
	}{ya, 200, 200, 200})
	paths := []string{first}
	for _, c := range cases {
		paths = append(paths, c.path)
	}
	out := filepath.Join(dir, "o.gif")
	g, _, err := huntMake(t, bins.FFmpeg, gifInputs(t, paths, out, SourceColor{Width: 64, Height: 48},
		map[string]string{"sizing": gifOriginal}), false)
	if err != nil {
		t.Fatal(err)
	}
	frames := shown(g)
	if len(frames) != len(paths) {
		t.Fatalf("%d frames, want %d", len(frames), len(paths))
	}
	for i, c := range cases {
		if got := frames[i+1].RGBAAt(32, 24); !near(got, c.r, c.g, c.b) {
			t.Errorf("%s shows %v at its centre, want about %d,%d,%d", filepath.Base(c.path), got, c.r, c.g, c.b)
		}
	}
	// The paletted image's clear edge is flattened onto black, not left clear
	if got := frames[4].RGBAAt(2, 24); !near(got, 0, 0, 0) {
		t.Errorf("the paletted image's clear edge shows %v, want black", got)
	}
}

// TestHuntOtherContainers runs an AVIF, a BMP and a progressive JPEG, each
// accepted by the tool, after a PNG
func TestHuntOtherContainers(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	first := filepath.Join(dir, "first.png")
	huntSolid(t, bins.FFmpeg, first, "red", "64x48")
	made := []string{first}
	for name, enc := range map[string][]string{
		"a.avif": {"-c:v", "libaom-av1", "-still-picture", "1", "-pix_fmt", "yuv420p"},
		"b.bmp":  {"-pix_fmt", "bgra"},
		"c.jpg":  {"-c:v", "mjpeg", "-pix_fmt", "yuvj444p"},
		"d.tiff": {"-compression_algo", "lzw", "-pix_fmt", "rgba"},
		"e.webp": {"-c:v", "libwebp", "-lossless", "1"},
	} {
		p := filepath.Join(dir, name)
		args := append([]string{"-f", "lavfi", "-i", "color=c=0x0000C8:s=64x48", "-frames:v", "1"}, enc...)
		if out, err := huntRun(bins.FFmpeg, append(args, "-y", p)...); err != nil {
			t.Logf("can't make %s here: %s", name, out)
			continue
		}
		made = append(made, p)
	}
	out := filepath.Join(dir, "o.gif")
	g, _, err := huntMake(t, bins.FFmpeg, gifInputs(t, made, out, SourceColor{Width: 64, Height: 48},
		map[string]string{"sizing": gifOriginal}), false)
	if err != nil {
		t.Fatal(err)
	}
	frames := shown(g)
	if len(frames) != len(made) {
		t.Fatalf("%d frames for %d images", len(frames), len(made))
	}
	for i := 1; i < len(made); i++ {
		if c := frames[i].RGBAAt(32, 24); !near(c, 0, 0, 0xC8) {
			t.Errorf("%s shows %v", filepath.Base(made[i]), c)
		}
	}
}

// TestHuntTimingAsAViewerSeesIt decodes the delays and loop count at the
// options' bounds
func TestHuntTimingAsAViewerSeesIt(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	paths := make([]string, 0, 7)
	for i, c := range []string{"red", "green", "blue", "white", "yellow", "cyan", "magenta"} {
		p := filepath.Join(dir, fmt.Sprintf("%d.png", i))
		huntSolid(t, bins.FFmpeg, p, c, "64x48")
		paths = append(paths, p)
	}
	cases := []struct {
		opts   map[string]string
		delays []int
		loop   int
	}{
		{map[string]string{"fps": "30"}, []int{3, 4, 3, 3, 4, 3, 3}, 0},
		{map[string]string{"fps": "50"}, []int{2, 2, 2, 2, 2, 2, 2}, 0},
		{map[string]string{"fps": "1", "loop": "once"}, []int{100, 100, 100, 100, 100, 100, 100}, -1},
		{map[string]string{"fps": "7", "hold": "10000", "loop": "count", "plays": "100"}, []int{14, 15, 14, 14, 14, 15, 1014}, 99},
		{map[string]string{"timing": "delay", "delay": "20", "loop": "count", "plays": "2"}, []int{2, 2, 2, 2, 2, 2, 2}, 1},
		{map[string]string{"timing": "delay", "delay": "10000", "hold": "10000"}, []int{1000, 1000, 1000, 1000, 1000, 1000, 2000}, 0},
		{map[string]string{"timing": "delay", "delay": "25"}, []int{3, 3, 3, 3, 3, 3, 3}, 0},
	}
	for _, c := range cases {
		t.Run(fmt.Sprint(c.opts), func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "o.gif")
			g, _, err := huntMake(t, bins.FFmpeg, gifInputs(t, paths, out, SourceColor{Width: 64, Height: 48}, c.opts), false)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(g.Delay) != fmt.Sprint(c.delays) {
				t.Errorf("delays %v, want %v", g.Delay, c.delays)
			}
			if g.LoopCount != c.loop {
				t.Errorf("loop count %d, want %d", g.LoopCount, c.loop)
			}
		})
	}
}

// huntRGBA decodes every frame of a video file as RGBA at w×h
func huntRGBA(t *testing.T, bin, path string, w, h int) [][]byte {
	t.Helper()
	out, err := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-i", path,
		"-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", "rgba", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	size := w * h * 4
	var frames [][]byte
	for i := 0; i+size <= len(out); i += size {
		frames = append(frames, out[i:i+size])
	}
	return frames
}

// TestHuntCompositeMatchesFrames composites the GIF the way a viewer does and
// compares each frame with the image it was made from, for every disposal and
// fit, with images that share some pixels so ffmpeg's diffing has work to do
func TestHuntCompositeMatchesFrames(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	srcs := []string{
		filepath.Join(dir, "1.png"), filepath.Join(dir, "2.png"), filepath.Join(dir, "3.png"),
		filepath.Join(dir, "4.png"), filepath.Join(dir, "5.png"), filepath.Join(dir, "6.png"),
	}
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=160x120", "-frames:v", "1", "-y", srcs[0])
	// The same picture with a box drawn on it, so most pixels match the one before
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=160x120,drawbox=x=40:y=30:w=30:h=30:c=white:t=fill", "-frames:v", "1", "-y", srcs[1])
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "color=c=0x0000C0:s=80x120", "-frames:v", "1", "-y", srcs[2])
	run(t, bins.FFmpeg, "-i", srcs[0], "-y", srcs[3])
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "color=c=0x00FF00@0.0:s=160x120,format=rgba,drawbox=x=60:y=40:w=40:h=40:c=red:t=fill", "-frames:v", "1", "-y", srcs[4])
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=160x60", "-frames:v", "1", "-y", srcs[5])

	for _, opts := range []map[string]string{
		{"fit": "pad"}, {"fit": "crop"}, {"fit": "stretch"},
		{"fit": "pad", "dispose": "clear"}, {"fit": "crop", "dispose": "clear"}, {"fit": "stretch", "dispose": "keep"},
	} {
		t.Run(fmt.Sprint(opts), func(t *testing.T) {
			opts["sizing"] = gifOriginal
			out := filepath.Join(t.TempDir(), "o.gif")
			g, p, err := huntMake(t, bins.FFmpeg, gifInputs(t, srcs, out, SourceColor{Width: 160, Height: 120}, opts), true)
			if err != nil {
				t.Fatal(err)
			}
			// The prepared frames, one file per image ahead of the palette and GIF
			var want [][]byte
			for _, f := range p.Cleanup[:len(srcs)] {
				want = append(want, huntRGBA(t, bins.FFmpeg, f, 160, 120)...)
			}
			got := shown(g)
			if len(want) != len(got) {
				t.Fatalf("%d frames made and %d in the GIF", len(want), len(got))
			}
			for i := range got {
				bad, clearWrong := 0, 0
				for px := 0; px < 160*120; px++ {
					w := want[i][px*4 : px*4+4]
					c := got[i].Pix[px*4 : px*4+4]
					if (w[3] == 0) != (c[3] == 0) {
						clearWrong++
						continue
					}
					if w[3] == 0 {
						continue
					}
					d := 0
					for k := 0; k < 3; k++ {
						x := int(w[k]) - int(c[k])
						if x < 0 {
							x = -x
						}
						d += x
					}
					if d > 150 {
						bad++
					}
				}
				if opts["dispose"] == "keep" {
					clearWrong = 0
				}
				if bad > 160*120/50 || clearWrong > 0 {
					t.Errorf("frame %d as shown: %d pixels far off and %d clear where they shouldn't be or the other way", i, bad, clearWrong)
				}
			}
		})
	}
}

// TestHuntEveryFrameHasAControlBlock checks the fix-up reaches every frame,
// since one without a graphic control block keeps no mark at all
func TestHuntEveryFrameHasAControlBlock(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "1.png"), filepath.Join(dir, "2.png")
	huntSolid(t, bins.FFmpeg, a, "red", "64x48")
	huntSolid(t, bins.FFmpeg, b, "blue", "48x48")
	for _, opts := range []map[string]string{
		{"dispose": "keep"}, {"dispose": "clear", "fit": "crop"}, {"dispose": "keep", "fit": "stretch"}, {},
	} {
		out := filepath.Join(t.TempDir(), "o.gif")
		g, p, err := huntMake(t, bins.FFmpeg, gifInputs(t, []string{a, b, a, a, b}, out,
			SourceColor{Width: 64, Height: 48}, opts), false)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(out)
		offs, err := giffix.ControlOffsets(bytesReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if len(offs) != len(g.Image) {
			t.Errorf("%v: %d control blocks for %d frames", opts, len(offs), len(g.Image))
		}
		if p.Disposal != 0 {
			for i, d := range g.Disposal {
				if int(d) != p.Disposal {
					t.Errorf("%v: frame %d has disposal %d, want %d", opts, i, d, p.Disposal)
				}
			}
		}
	}
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

var huntMaxRSS = regexp.MustCompile(`maxrss=(\d+)KiB`)

// TestHuntManyLargePhotos makes a GIF from many camera-sized JPEGs and checks
// no command's peak memory grows with the count, as a graph opening every
// image at once did at about 245 MB a photo
func TestHuntManyLargePhotos(t *testing.T) {
	if os.Getenv("HUNT_HEAVY") == "" {
		t.Skip("set HUNT_HEAVY=1")
	}
	bins := needFFmpeg(t)
	dir := t.TempDir()
	photo := filepath.Join(dir, "photo.jpg")
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=4000x3000", "-frames:v", "1", "-q:v", "3", "-y", photo)
	data, err := os.ReadFile(photo)
	if err != nil {
		t.Fatal(err)
	}
	count := 30
	if n, err := strconv.Atoi(os.Getenv("HUNT_PHOTOS")); err == nil && n > 1 && n <= MaxGIFImages {
		count = n
	}
	paths := make([]string, count)
	for i := range paths {
		paths[i] = filepath.Join(dir, fmt.Sprintf("IMG_%04d.jpg", i))
		if err := os.WriteFile(paths[i], data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "o.gif")
	p := buildGIFPlan(t, gifInputs(t, paths, out, SourceColor{Width: 4000, Height: 3000}, nil))
	for _, f := range p.Files {
		if err := os.WriteFile(f.Path, []byte(f.Content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	peak := 0
	for _, args := range p.Commands() {
		res, err := exec.Command(bins.FFmpeg, append([]string{"-hide_banner", "-benchmark", "-nostdin"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, res)
		}
		if m := huntMaxRSS.FindStringSubmatch(string(res)); m != nil {
			if kib, _ := strconv.Atoi(m[1]); kib > peak {
				peak = kib
			}
		}
	}
	t.Logf("%d photos of 4000×3000 took %v, the largest command peaking at %d MiB", count, time.Since(start), peak/1024)
	if peak > 1<<20 {
		t.Errorf("a command peaked at %d MiB", peak/1024)
	}
}

// TestHuntFiveHundredImages runs the most images a GIF takes
func TestHuntFiveHundredImages(t *testing.T) {
	if os.Getenv("HUNT_HEAVY") == "" {
		t.Skip("set HUNT_HEAVY=1")
	}
	bins := needFFmpeg(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=320x240", "-frames:v", "1", "-y", src)
	data, _ := os.ReadFile(src)
	paths := make([]string, MaxGIFImages)
	for i := range paths {
		paths[i] = filepath.Join(dir, fmt.Sprintf("f%d.png", i))
		if err := os.WriteFile(paths[i], data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	g, _, err := huntMake(t, bins.FFmpeg, gifInputs(t, paths, filepath.Join(dir, "o.gif"),
		SourceColor{Width: 320, Height: 240}, nil), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d frames in %v", len(g.Image), time.Since(start))
	if len(g.Image) != MaxGIFImages {
		t.Errorf("%d frames, want %d", len(g.Image), MaxGIFImages)
	}
}

// TestHuntLongOutputPath puts the output where its working files pass the
// old 260 character limit, which the output itself stays under
func TestHuntLongOutputPath(t *testing.T) {
	bins := needFFmpeg(t)
	base := t.TempDir()
	a, b := filepath.Join(base, "1.png"), filepath.Join(base, "2.png")
	huntSolid(t, bins.FFmpeg, a, "red", "64x48")
	huntSolid(t, bins.FFmpeg, b, "blue", "64x48")
	dir := base
	for len(dir) < 225 {
		dir = filepath.Join(dir, "deep folder")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.gif")
	t.Logf("output is %d characters, its staged GIF %d", len(out), len(out+".mediatools-staged.gif"))
	_, _, err := huntMake(t, bins.FFmpeg, gifInputs(t, []string{a, b}, out, SourceColor{Width: 64, Height: 48}, nil), false)
	if err != nil {
		t.Errorf("an output of %d characters fails: %v", len(out), err)
	}
}
