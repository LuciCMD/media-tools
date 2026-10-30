package giffix

import (
	"bufio"
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"os"
	"testing"
)

// huntBase is a two-frame GIF89a from the standard encoder, frames marked to
// be left, with pixels whose LZW data could hold 0x21 0xf9 bytes
func huntBase(t *testing.T) []byte {
	t.Helper()
	var pal color.Palette
	for i := 0; i < 256; i++ {
		pal = append(pal, color.RGBA{uint8(i), uint8(255 - i), uint8(i * 7), 255})
	}
	frames := []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 40, 40), pal), image.NewPaletted(image.Rect(0, 0, 40, 40), pal)}
	for i := range frames[0].Pix {
		// Bytes that spell a graphic control block, over and over
		frames[0].Pix[i] = []byte{0x21, 0xf9, 0x04, 0x05}[i%4]
		frames[1].Pix[i] = uint8(i * 31)
	}
	g := &gif.GIF{Image: frames, Delay: []int{5, 5}, Disposal: []byte{1, 1},
		Config: image.Config{ColorModel: pal, Width: 40, Height: 40}}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// huntInsertBefore puts block in front of the first graphic control block
func huntInsertBefore(t *testing.T, data, block []byte) []byte {
	t.Helper()
	at := bytes.Index(data, []byte{0x21, 0xf9, 0x04})
	if at < 0 {
		t.Fatal("no graphic control block")
	}
	return append(append(append([]byte{}, data[:at]...), block...), data[at:]...)
}

func huntDisposals(t *testing.T, data []byte) []byte {
	t.Helper()
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the rewritten GIF doesn't decode: %v", err)
	}
	return g.Disposal
}

func huntSet(t *testing.T, data []byte, method int) ([]byte, error) {
	t.Helper()
	path := writeSample(t, data)
	err := SetDisposal(path, method)
	out, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	return out, err
}

// TestHuntSetDisposalAcrossBlockKinds checks every frame is marked through
// extensions ffmpeg or another tool may add, and nothing else is touched
func TestHuntSetDisposalAcrossBlockKinds(t *testing.T) {
	base := huntBase(t)
	plainText := []byte{0x21, 0x01, 12, 0, 0, 0, 0, 10, 0, 10, 0, 8, 8, 1, 0, 3, 'h', 'i', '!', 0}
	app := []byte{0x21, 0xff, 11, 'X', 'M', 'P', ' ', 'D', 'a', 't', 'a', 'X', 'M', 'P', 4, 0x21, 0xf9, 0x04, 0x09, 0}
	unknown := []byte{0x21, 0x99, 2, 0xf9, 0x04, 0}
	cases := map[string][]byte{
		"plain":         base,
		"plain text":    huntInsertBefore(t, base, plainText),
		"application":   huntInsertBefore(t, base, app),
		"unknown":       huntInsertBefore(t, base, unknown),
		"trailing junk": append(append([]byte{}, base...), 0x21, 0xf9, 0x04, 0x00),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := huntSet(t, data, 2)
			if err != nil {
				t.Fatal(err)
			}
			// Go's decoder refuses an extension label it doesn't know, viewers skip it
			if name != "unknown" {
				for i, d := range huntDisposals(t, out) {
					if d != 2 {
						t.Errorf("frame %d has disposal %d", i, d)
					}
				}
			}
			diff := 0
			for i := range out {
				if out[i] != data[i] {
					diff++
				}
			}
			if diff != 2 {
				t.Errorf("%d bytes changed, want the two packed bytes", diff)
			}
		})
	}
}

// TestHuntSetDisposalGIF87a checks an 87a file, which has no extensions, is
// left alone rather than broken
func TestHuntSetDisposalGIF87a(t *testing.T) {
	base := huntBase(t)
	var b87 []byte
	b87 = append(b87, []byte("GIF87a")...)
	b87 = append(b87, base[6:13]...)
	b87 = append(b87, base[13:13+768]...)
	// The first image descriptor on, without any graphic control block
	i := bytes.IndexByte(base[13+768:], 0x2c)
	rest := base[13+768+i:]
	b87 = append(b87, rest...)
	// Drop the second frame's control block too, since 87a has none
	at := bytes.Index(b87, []byte{0x21, 0xf9, 0x04})
	if at >= 0 {
		b87 = append(b87[:at:at], b87[at+8:]...)
	}
	out, err := huntSet(t, b87, 2)
	if err != nil {
		t.Fatalf("an 87a GIF fails: %v", err)
	}
	if !bytes.Equal(out, b87) {
		t.Error("an 87a GIF with no control blocks was changed")
	}
}

// TestHuntSetDisposalRejectsBrokenFiles checks a cut-short or foreign file
// fails without being half rewritten
func TestHuntSetDisposalRejectsBrokenFiles(t *testing.T) {
	base := huntBase(t)
	cut := base[:len(base)-40]
	for name, data := range map[string][]byte{
		"cut short":  cut,
		"empty":      {},
		"png":        []byte("\x89PNG\r\n\x1a\n0000000000000"),
		"no trailer": base[:len(base)-1],
	} {
		t.Run(name, func(t *testing.T) {
			out, err := huntSet(t, data, 2)
			if err == nil {
				t.Error("accepted")
			}
			if !bytes.Equal(out, data) {
				t.Error("the file was changed even though it failed")
			}
		})
	}
}

// TestHuntControlOffsetsLargeFile walks a GIF with many frames and sub-blocks
// to check the bounds don't stop a real file
func TestHuntControlOffsetsLargeFile(t *testing.T) {
	pal := color.Palette{color.Black, color.White}
	var frames []*image.Paletted
	var delays []int
	for i := 0; i < 600; i++ {
		frames = append(frames, image.NewPaletted(image.Rect(0, 0, 300, 300), pal))
		delays = append(delays, 2)
	}
	g := &gif.GIF{Image: frames, Delay: delays}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	offs, err := ControlOffsets(bufio.NewReader(bytes.NewReader(buf.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	if len(offs) != 600 {
		t.Errorf("%d control blocks found, want 600", len(offs))
	}
}

// TestHuntSetDisposalReadOnly checks a file that can't be written fails with
// a reason rather than succeeding silently
func TestHuntSetDisposalReadOnly(t *testing.T) {
	path := writeSample(t, huntBase(t))
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if err := SetDisposal(path, 2); err == nil {
		t.Error("a read-only GIF was reported fixed")
	}
}
