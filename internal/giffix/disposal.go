// Package giffix rewrites what ffmpeg can't be told to set in a GIF it made
package giffix

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
)

// Bounds on the walk through a GIF's blocks (Power of Ten rule 2)
const (
	maxBlocks    = 1 << 20
	maxSubBlocks = 1 << 26
)

// GIF block markers
const (
	extIntroducer   = 0x21
	imageSeparator  = 0x2c
	trailer         = 0x3b
	graphicsControl = 0xf9
)

// disposalBits sit in bits 2 to 4 of a graphic control block's packed byte
const disposalBits = 0x1c

// SetDisposal writes method into every frame's graphic control block of the
// GIF at path, in place, 1 to leave a frame or 2 to clear it
func SetDisposal(path string, method int) error {
	if method < 1 || method > 3 {
		return fmt.Errorf("giffix: disposal %d isn't one a GIF has", method)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	offsets, err := ControlOffsets(bufio.NewReader(f))
	if err == nil {
		err = rewrite(f, offsets, method)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("couldn't set how the GIF's frames are cleared: %w", err)
	}
	return nil
}

// rewrite sets the disposal bits of the packed byte at each offset
func rewrite(f *os.File, offsets []int64, method int) error {
	b := make([]byte, 1)
	for _, off := range offsets {
		if _, err := f.ReadAt(b, off); err != nil {
			return err
		}
		b[0] = b[0]&^disposalBits | byte(method)<<2
		if _, err := f.WriteAt(b, off); err != nil {
			return err
		}
	}
	return nil
}

// ControlOffsets walks a GIF and returns where each graphic control block's
// packed byte is, from the start of the file
func ControlOffsets(r io.ByteReader) ([]int64, error) {
	w := walker{r: r}
	head := make([]byte, 13)
	if err := w.read(head); err != nil {
		return nil, err
	}
	if string(head[:3]) != "GIF" {
		return nil, errors.New("not a GIF")
	}
	if err := w.skip(colorTable(head[10])); err != nil {
		return nil, err
	}
	var offsets []int64
	for i := 0; i < maxBlocks; i++ {
		marker, err := w.byte()
		if err != nil {
			return nil, err
		}
		switch marker {
		case trailer:
			return offsets, nil
		case extIntroducer:
			off, err := w.extension()
			if err != nil {
				return nil, err
			}
			if off >= 0 {
				offsets = append(offsets, off)
			}
		case imageSeparator:
			if err := w.image(); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unexpected block 0x%02x at %d", marker, w.pos-1)
		}
	}
	return nil, errors.New("too many blocks")
}

// colorTable is the size of the colour table a packed byte announces
func colorTable(packed byte) int {
	if packed&0x80 == 0 {
		return 0
	}
	return 3 << ((packed & 7) + 1)
}

// walker reads a GIF byte by byte, counting where it is
type walker struct {
	r   io.ByteReader
	pos int64
}

func (w *walker) byte() (byte, error) {
	b, err := w.r.ReadByte()
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	if err == nil {
		w.pos++
	}
	return b, err
}

func (w *walker) read(dst []byte) error {
	for i := range dst {
		b, err := w.byte()
		if err != nil {
			return err
		}
		dst[i] = b
	}
	return nil
}

func (w *walker) skip(n int) error {
	for i := 0; i < n; i++ {
		if _, err := w.byte(); err != nil {
			return err
		}
	}
	return nil
}

// extension reads one extension and returns where its packed byte is when
// it's a graphic control block, or -1
func (w *walker) extension() (int64, error) {
	label, err := w.byte()
	if err != nil {
		return -1, err
	}
	if label != graphicsControl {
		return -1, w.subBlocks()
	}
	size, err := w.byte()
	if err != nil {
		return -1, err
	}
	if size < 4 {
		return -1, fmt.Errorf("short graphic control block at %d", w.pos-1)
	}
	// The packed byte leads the block
	off := w.pos
	if err := w.skip(int(size)); err != nil {
		return -1, err
	}
	if err := w.subBlocks(); err != nil {
		return -1, err
	}
	return off, nil
}

// image skips an image descriptor, its colour table and its picture data
func (w *walker) image() error {
	desc := make([]byte, 9)
	if err := w.read(desc); err != nil {
		return err
	}
	// The colour table, then the LZW code size
	if err := w.skip(colorTable(desc[8]) + 1); err != nil {
		return err
	}
	return w.subBlocks()
}

// subBlocks skips sized sub-blocks up to the empty one ending them
func (w *walker) subBlocks() error {
	for i := 0; i < maxSubBlocks; i++ {
		n, err := w.byte()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if err := w.skip(int(n)); err != nil {
			return err
		}
	}
	return errors.New("too many sub-blocks")
}
