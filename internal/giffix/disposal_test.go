package giffix

import (
	"bufio"
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"
	"testing"
)

// sample is a three-frame GIF whose frames are marked to be left, the second
// with a local colour table, and a comment block before the first frame
func sample(t *testing.T) []byte {
	t.Helper()
	global := color.Palette{color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}, color.RGBA{}}
	local := color.Palette{color.RGBA{0, 255, 0, 255}, color.RGBA{}}
	frames := []*image.Paletted{
		image.NewPaletted(image.Rect(0, 0, 8, 6), global),
		image.NewPaletted(image.Rect(2, 1, 6, 5), local),
		image.NewPaletted(image.Rect(0, 0, 8, 6), global),
	}
	for i := range frames[1].Pix {
		frames[1].Pix[i] = 1
	}
	g := &gif.GIF{Image: frames, Delay: []int{10, 20, 30}, Disposal: []byte{1, 1, 1},
		Config: image.Config{ColorModel: global, Width: 8, Height: 6}}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	at := bytes.Index(data, []byte{0x21, 0xf9, 0x04})
	if at < 0 {
		t.Fatal("the sample has no graphic control block")
	}
	comment := []byte{0x21, 0xfe, 0x03, 'a', 'b', 'c', 0x00}
	return append(append(append([]byte{}, data[:at]...), comment...), data[at:]...)
}

func writeSample(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.gif")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSetDisposalMarksEveryFrame checks every frame takes the new disposal and
// keeps its delay and pictures
func TestSetDisposalMarksEveryFrame(t *testing.T) {
	path := writeSample(t, sample(t))
	if err := SetDisposal(path, 2); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, err := gif.DecodeAll(f)
	if err != nil {
		t.Fatalf("the rewritten GIF doesn't decode: %v", err)
	}
	if len(g.Image) != 3 {
		t.Fatalf("%d frames, want 3", len(g.Image))
	}
	for i, d := range g.Disposal {
		if d != gif.DisposalBackground {
			t.Errorf("frame %d has disposal %d, want 2", i, d)
		}
		if g.Delay[i] != 10*(i+1) {
			t.Errorf("frame %d's delay became %d", i, g.Delay[i])
		}
	}
	if g.Image[1].Rect != image.Rect(2, 1, 6, 5) || g.Image[1].Pix[0] != 1 {
		t.Error("the second frame's picture changed")
	}
}

// TestControlOffsetsFindsOnlyControlBlocks checks the walk passes over the
// comment and the picture data
func TestControlOffsetsFindsOnlyControlBlocks(t *testing.T) {
	data := sample(t)
	offsets, err := ControlOffsets(bufio.NewReader(bytes.NewReader(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 3 {
		t.Fatalf("found %d control blocks, want 3", len(offsets))
	}
	for _, off := range offsets {
		if data[off-3] != 0x21 || data[off-2] != 0xf9 {
			t.Errorf("offset %d isn't a control block's packed byte", off)
		}
	}
}

// TestSetDisposalLeavesABrokenFileAlone checks a cut-off or foreign file is
// refused before anything is written
func TestSetDisposalLeavesABrokenFileAlone(t *testing.T) {
	data := sample(t)
	cases := map[string][]byte{
		"cut off":   data[:len(data)-12],
		"not a GIF": []byte("PNG and then some bytes"),
		"empty":     {},
	}
	for name, content := range cases {
		path := writeSample(t, content)
		if err := SetDisposal(path, 2); err == nil {
			t.Errorf("%s: accepted", name)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(after, content) {
			t.Errorf("%s: the file was changed", name)
		}
	}
}

// TestSetDisposalRefusesAShortControlBlock checks a control block too short
// to hold its packed byte is refused rather than the next byte overwritten
func TestSetDisposalRefusesAShortControlBlock(t *testing.T) {
	data := sample(t)
	at := bytes.Index(data, []byte{0x21, 0xf9, 0x04})
	short := append(append(append([]byte{}, data[:at]...), 0x21, 0xf9, 0x00), data[at:]...)
	if _, err := ControlOffsets(bufio.NewReader(bytes.NewReader(short))); err == nil {
		t.Error("a control block with no packed byte was accepted")
	}
}

// TestSetDisposalRefusesUnknownMethods checks only a GIF's own values are written
func TestSetDisposalRefusesUnknownMethods(t *testing.T) {
	path := writeSample(t, sample(t))
	for _, m := range []int{0, 4, -1} {
		if err := SetDisposal(path, m); err == nil {
			t.Errorf("disposal %d was accepted", m)
		}
	}
}
