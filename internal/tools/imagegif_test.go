package tools

import (
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"media-tools/internal/giffix"
)

// gifInputs is Images to GIF over paths with opts over its defaults, the
// first image being first wide and high
func gifInputs(t *testing.T, paths []string, out string, first SourceColor, opts map[string]string) Inputs {
	t.Helper()
	tool, err := Get("image.gif")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := tool.Validate(opts)
	if err != nil {
		t.Fatal(err)
	}
	return Inputs{Path: paths[0], Paths: paths, Output: out, Options: valid, Color: first}
}

func buildGIFPlan(t *testing.T, in Inputs) Plan {
	t.Helper()
	p, err := buildImagesToGIF(in, Encode{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// drawArgs is the last command, the one drawing the GIF
func drawArgs(p Plan) string {
	return strings.Join(p.Passes[len(p.Passes)-1], " ")
}

func TestImagesToGIFTiming(t *testing.T) {
	cases := []struct {
		opts      map[string]string
		pts, last string
	}{
		{map[string]string{}, "setpts=round(N*100/10)", "-final_delay 10"},
		{map[string]string{"fps": "30"}, "setpts=round(N*100/30)", "-final_delay 3"},
		{map[string]string{"timing": "delay", "delay": "125"}, "setpts=N*13", "-final_delay 13"},
		{map[string]string{"timing": "delay", "delay": "20", "hold": "1500"}, "setpts=N*2", "-final_delay 152"},
		{map[string]string{"fps": "50", "hold": "990"}, "setpts=round(N*100/50)", "-final_delay 101"},
	}
	paths := []string{"/a/1.png", "/a/2.png"}
	for _, c := range cases {
		draw := drawArgs(buildGIFPlan(t, gifInputs(t, paths, "/a/out.gif", SourceColor{}, c.opts)))
		if !strings.Contains(draw, c.pts) || !strings.Contains(draw, c.last+" ") {
			t.Errorf("%v: draw is %q, want %q and %q", c.opts, draw, c.pts, c.last)
		}
		// Without this the delays came out 0.32 and 0.36 for a third of a second
		if !strings.Contains(draw, "-enc_time_base 1/100") {
			t.Errorf("%v: delays aren't kept in hundredths", c.opts)
		}
	}
}

func TestImagesToGIFLoops(t *testing.T) {
	cases := map[string]map[string]string{
		"0":  {},
		"-1": {"loop": "once"},
		"4":  {"loop": "count", "plays": "5"},
		"1":  {"loop": "count", "plays": "2"},
	}
	paths := []string{"/a/1.png", "/a/2.png"}
	for want, opts := range cases {
		draw := drawArgs(buildGIFPlan(t, gifInputs(t, paths, "/a/out.gif", SourceColor{}, opts)))
		if !strings.Contains(draw, "-loop "+want+" ") {
			t.Errorf("%v: draw is %q, want -loop %s", opts, draw, want)
		}
	}
}

func TestImagesToGIFDisposal(t *testing.T) {
	cases := []struct {
		opts     map[string]string
		disposal int
		whole    bool
	}{
		{map[string]string{}, DisposeClear, true},
		{map[string]string{"fit": "crop"}, 0, false},
		{map[string]string{"fit": "stretch"}, 0, false},
		{map[string]string{"dispose": "clear", "fit": "crop"}, DisposeClear, true},
		{map[string]string{"dispose": "keep"}, DisposeKeep, false},
	}
	paths := []string{"/a/1.png", "/a/2.png"}
	for _, c := range cases {
		p := buildGIFPlan(t, gifInputs(t, paths, "/a/out.gif", SourceColor{}, c.opts))
		if p.Disposal != c.disposal {
			t.Errorf("%v: disposal %d, want %d", c.opts, p.Disposal, c.disposal)
		}
		// Clearing a frame that only holds what changed would lose the rest
		if whole := strings.Contains(drawArgs(p), "-gifflags 0"); whole != c.whole {
			t.Errorf("%v: whole frames %v, want %v", c.opts, whole, c.whole)
		}
	}
}

func TestImagesToGIFCanvasTakesTheFirstShape(t *testing.T) {
	paths := []string{"/a/1.png", "/a/2.png"}
	cases := []struct {
		first SourceColor
		opts  map[string]string
		want  string
	}{
		{SourceColor{Width: 1000, Height: 500}, map[string]string{}, "pad=480:240:"},
		{SourceColor{Width: 1000, Height: 500}, map[string]string{"sizing": gifOriginal}, "pad=1000:500:"},
		{SourceColor{Width: 300, Height: 400}, map[string]string{"width": "600"}, "pad=600:800:"},
		{SourceColor{Width: 1000, Height: 500}, map[string]string{"fit": "crop"}, "crop=480:240"},
		{SourceColor{Width: 1000, Height: 500}, map[string]string{"fit": "stretch"}, "scale=480:240:flags"},
	}
	for _, c := range cases {
		p := buildGIFPlan(t, gifInputs(t, paths, "/a/out.gif", c.first, c.opts))
		for i, args := range p.Commands()[:len(paths)] {
			if prep := strings.Join(args, " "); !strings.Contains(prep, c.want) {
				t.Errorf("%v %v: image %d's command lacks %q: %s", c.first, c.opts, i, c.want, prep)
			}
		}
	}
	tall := gifInputs(t, paths, "/a/out.gif", SourceColor{Width: 10, Height: 100000}, map[string]string{"width": "1920"})
	if _, err := buildImagesToGIF(tall, Encode{}); err == nil {
		t.Error("a GIF taller than its header can hold was accepted")
	}
}

func TestImagesToGIFImageCount(t *testing.T) {
	one := gifInputs(t, []string{"/a/1.png"}, "/a/out.gif", SourceColor{}, nil)
	if _, err := buildImagesToGIF(one, Encode{}); err == nil {
		t.Error("one image was accepted")
	}
	many := make([]string, MaxGIFImages+1)
	for i := range many {
		many[i] = "/a/x.png"
	}
	if _, err := buildImagesToGIF(gifInputs(t, many, "/a/out.gif", SourceColor{}, nil), Encode{}); err == nil {
		t.Errorf("%d images were accepted", len(many))
	}
	p := buildGIFPlan(t, gifInputs(t, many[:MaxGIFImages], "/a/out.gif", SourceColor{}, nil))
	if p.Frames != MaxGIFImages || len(p.Commands()) != MaxGIFImages+2 {
		t.Errorf("%d frames counted and %d commands, want %d and %d", p.Frames, len(p.Commands()), MaxGIFImages, MaxGIFImages+2)
	}
	// Each image is opened by a command of its own, so only one is in memory
	for i, args := range p.Commands()[:MaxGIFImages] {
		if inputs := strings.Count(strings.Join(args, " "), "-i "); inputs != 1 {
			t.Fatalf("image %d's command opens %d inputs", i, inputs)
		}
		if !strings.Contains(strings.Join(args, " "), "-frames:v 1") {
			t.Fatalf("image %d's command doesn't stop at one frame, so an animated image adds them all", i)
		}
	}
}

func TestImagesToGIFStagesAndTidies(t *testing.T) {
	p := buildGIFPlan(t, gifInputs(t, []string{"/a/1.png", "/a/2.png"}, "/a/out.gif", SourceColor{}, nil))
	if len(p.Steps) != len(p.Commands()) {
		t.Errorf("%d steps for %d commands", len(p.Steps), len(p.Commands()))
	}
	if p.Staged == "" || p.Staged == p.OutputPath {
		t.Fatalf("staged at %q", p.Staged)
	}
	for _, c := range p.Commands() {
		if c[len(c)-1] == p.OutputPath {
			t.Error("a command writes straight to the output")
		}
	}
	left := map[string]bool{}
	for _, f := range p.Cleanup {
		left[f] = true
	}
	for _, c := range p.Commands() {
		if out := c[len(c)-1]; !left[out] {
			t.Errorf("%s isn't tidied away if the job stops", out)
		}
	}
}

// TestImagesToGIFReadsPercentNamesAsNamed checks a name with a % isn't read
// as a numbered sequence, where frame%d.png opened frame1.png beside it
func TestImagesToGIFReadsPercentNamesAsNamed(t *testing.T) {
	paths := []string{`C:\Pics\frame%d.png`, `C:\Pics\50%.avif`, `C:\shots%d\plain.jpg`, `C:\Pics\plain.png`}
	p := buildGIFPlan(t, gifInputs(t, paths, "/a/out.gif", SourceColor{}, nil))
	want := []bool{true, false, true, false}
	for i, args := range p.Commands()[:len(paths)] {
		if named := strings.Contains(strings.Join(args, " "), "-f image2 -pattern_type none -i "); named != want[i] {
			t.Errorf("%s: read as named %v, want %v", paths[i], named, want[i])
		}
	}
}

func TestImagesToGIFRoundsTypedFractions(t *testing.T) {
	p := buildGIFPlan(t, gifInputs(t, []string{"/a/1.png", "/a/2.png"}, "/a/out.gif", SourceColor{Width: 4, Height: 3},
		map[string]string{"width": "640.6", "fps": "24.6", "colors": "64.2"}))
	all := strings.Join(p.Commands()[0], " ") + " " + strings.Join(p.Commands()[2], " ") + " " + drawArgs(p)
	for _, want := range []string{"pad=641:481:", "round(N*100/25)", "max_colors=64:"} {
		if !strings.Contains(all, want) {
			t.Errorf("a typed fraction fell back to the default, %q missing", want)
		}
	}
}

// gifFixtures writes four images of different shapes and formats, the last
// half transparent, under names a filtergraph treats as structural
func gifFixtures(t *testing.T, bin string) []string {
	t.Helper()
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "frame 1's [a], b.jpg"),
		filepath.Join(dir, "frame 2.png"),
		filepath.Join(dir, "frame 3.webp"),
		filepath.Join(dir, "frame 4; last.png"),
	}
	run(t, bin, "-f", "lavfi", "-i", "color=c=0xC00000:s=640x480", "-frames:v", "1", "-y", paths[0])
	run(t, bin, "-f", "lavfi", "-i", "color=c=0x00A000:s=200x300", "-frames:v", "1", "-y", paths[1])
	run(t, bin, "-f", "lavfi", "-i", "color=c=0xE0E000:s=400x200", "-frames:v", "1", "-y", paths[2])
	run(t, bin, "-f", "lavfi", "-i", "color=c=0x0000FF@0.5:s=300x300,format=rgba", "-frames:v", "1", "-y", paths[3])
	return paths
}

// makeGIF runs the plan the way the queue does, fix-up included, and decodes
// the result
func makeGIF(t *testing.T, bin string, in Inputs) *gif.GIF {
	t.Helper()
	p := buildGIFPlan(t, in)
	for _, f := range p.Files {
		if err := os.WriteFile(f.Path, []byte(f.Content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range p.Commands() {
		run(t, bin, args...)
	}
	if p.Disposal != 0 {
		if err := giffix.SetDisposal(p.Staged, p.Disposal); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(p.Staged, p.OutputPath); err != nil {
		t.Fatal(err)
	}
	for _, f := range append(filesOf(p), p.Cleanup...) {
		_ = os.Remove(f)
	}
	f, err := os.Open(p.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, err := gif.DecodeAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// shown composites a GIF the way a viewer does, each frame as seen
func shown(g *gif.GIF) []*image.RGBA {
	canvas := image.NewRGBA(image.Rect(0, 0, g.Config.Width, g.Config.Height))
	out := make([]*image.RGBA, 0, len(g.Image))
	for i, im := range g.Image {
		before := image.NewRGBA(canvas.Rect)
		copy(before.Pix, canvas.Pix)
		b := im.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if _, _, _, a := im.At(x, y).RGBA(); a > 0 {
					canvas.Set(x, y, im.At(x, y))
				}
			}
		}
		frame := image.NewRGBA(canvas.Rect)
		copy(frame.Pix, canvas.Pix)
		out = append(out, frame)
		switch g.Disposal[i] {
		case gif.DisposalBackground:
			for y := b.Min.Y; y < b.Max.Y; y++ {
				for x := b.Min.X; x < b.Max.X; x++ {
					canvas.Set(x, y, color.RGBA{})
				}
			}
		case gif.DisposalPrevious:
			copy(canvas.Pix, before.Pix)
		}
	}
	return out
}

// near reports whether c is within 40 of r, g, b and fully solid
func near(c color.RGBA, r, g, b int) bool {
	d := func(x uint8, y int) bool { return int(x)-y < 40 && y-int(x) < 40 }
	return c.A == 255 && d(c.R, r) && d(c.G, g) && d(c.B, b)
}

// TestImagesToGIFOnRealImages makes GIFs from mixed formats and shapes and
// checks what a viewer would show, bars clear rather than showing the image
// before, every image's colours kept, and the timing
func TestImagesToGIFOnRealImages(t *testing.T) {
	bins := needFFmpeg(t)
	paths := gifFixtures(t, bins.FFmpeg)
	first := SourceColor{Width: 640, Height: 480}
	// The colour each image shows at the centre, the last flattened onto black
	centres := [][3]int{{0xC0, 0, 0}, {0, 0xA0, 0}, {0xE0, 0xE0, 0}, {0, 0, 0x80}}

	t.Run("bars", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "bars.gif")
		g := makeGIF(t, bins.FFmpeg, gifInputs(t, paths, out, first,
			map[string]string{"width": "320", "fps": "4", "loop": "count", "plays": "3", "hold": "500"}))
		if g.Config.Width != 320 || g.Config.Height != 240 || len(g.Image) != 4 {
			t.Fatalf("%d×%d with %d frames, want 320×240 with 4", g.Config.Width, g.Config.Height, len(g.Image))
		}
		if g.LoopCount != 2 {
			t.Errorf("loop count %d, want 2 for three plays", g.LoopCount)
		}
		for i, want := range []int{25, 25, 25, 75} {
			if g.Delay[i] != want {
				t.Errorf("frame %d shows for %d hundredths, want %d", i, g.Delay[i], want)
			}
		}
		frames := shown(g)
		for i, c := range centres {
			if got := frames[i].RGBAAt(160, 120); !near(got, c[0], c[1], c[2]) {
				t.Errorf("frame %d's centre is %v, want about %v", i, got, c)
			}
		}
		// The second image is narrower, so its sides are bars where the first showed
		if got := frames[1].RGBAAt(5, 120); got.A != 0 {
			t.Errorf("the second frame's bar shows %v, the frame before showing through", got)
		}
		if got := frames[2].RGBAAt(160, 5); got.A != 0 {
			t.Errorf("the third frame's bar shows %v", got)
		}
	})

	t.Run("crop", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "crop.gif")
		g := makeGIF(t, bins.FFmpeg, gifInputs(t, paths, out, first, map[string]string{"width": "320", "fit": "crop"}))
		frames := shown(g)
		for i, c := range centres {
			for _, p := range []image.Point{{160, 120}, {3, 3}, {316, 236}} {
				if got := frames[i].RGBAAt(p.X, p.Y); !near(got, c[0], c[1], c[2]) {
					t.Errorf("frame %d at %v is %v, want about %v", i, p, got, c)
				}
			}
		}
	})

	t.Run("draw over", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "over.gif")
		g := makeGIF(t, bins.FFmpeg, gifInputs(t, paths, out, first, map[string]string{"width": "320", "dispose": "keep"}))
		if got := shown(g)[1].RGBAAt(5, 120); !near(got, 0xC0, 0, 0) {
			t.Errorf("drawn over, the second frame's bar is %v, want the first image showing", got)
		}
	})
}
