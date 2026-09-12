package rfb

import (
	"fmt"
	"image/color"
)

// zrleTile is ZRLE's fixed tile edge length.
const zrleTile = 64

// ZRLE subencoding boundaries (RFC 6143 §7.7.6).
const (
	zrleRaw          = 0
	zrleSolid        = 1
	zrleMaxPalette   = 16  // 2..16 are packed-palette tiles
	zrlePlainRLE     = 128 // 128 is plain RLE
	zrlePaletteRLE   = 130 // 130..255 are palette RLE, size = subenc-128
	zrleMaxPaletteSz = 127
)

// ZRLEDecoder implements the ZRLE encoding (16).
//
// ZRLE is Hextile's idea done properly: 64x64 tiles, each independently coded
// as raw, a solid colour, a packed palette, or a run-length list, with the
// whole lot pushed through one persistent zlib stream. Unlike Tight it needs
// no JPEG and produces bit-exact output, which makes it the right choice for
// text-heavy screens where JPEG ringing is visible.
//
// The decompressed length of a rectangle is not transmitted and cannot be
// derived up front — it depends on which subencoding each tile chose — so the
// tile structure is parsed incrementally straight off the inflater rather than
// decompressed into one buffer first.
type ZRLEDecoder struct {
	z    zlibStream
	comp []byte
	buf  []byte
	pal  [zrleMaxPaletteSz + 1]color.RGBA
}

// NewZRLEDecoder returns a decoder for the ZRLE encoding. It carries a
// connection-scoped zlib dictionary and must not be shared between
// connections.
func NewZRLEDecoder() Decoder { return &ZRLEDecoder{} }

// Encoding implements Decoder.
func (d *ZRLEDecoder) Encoding() Encoding { return EncodingZRLE }

// Decode implements Decoder.
func (d *ZRLEDecoder) Decode(c *Conn, r Rect) error {
	comp, err := readCompressedBlock(c, &d.z, d.comp)
	d.comp = comp
	if err != nil {
		return err
	}
	if r.Empty() {
		// No tiles follow. The block has been handed to the stream regardless,
		// which is what keeps the dictionary intact.
		return nil
	}

	pr := c.PixelReader()
	fb := c.framebuffer()
	cpix := pr.TPixelSize()

	// Worst case per tile is a raw 64x64 tile of CPIXELs; the packed-palette
	// and palette-RLE cases are both smaller than that.
	d.buf = growBytes(d.buf, zrleTile*zrleTile*max(cpix, 1))

	width, height := int(r.Width), int(r.Height)
	for ty := 0; ty < height; ty += zrleTile {
		th := min(zrleTile, height-ty)
		for tx := 0; tx < width; tx += zrleTile {
			tw := min(zrleTile, width-tx)
			if err := d.tile(pr, fb, int(r.X)+tx, int(r.Y)+ty, tw, th); err != nil {
				return fmt.Errorf("tile at +%d+%d: %w", tx, ty, err)
			}
		}
	}
	return nil
}

// readByte pulls one decompressed byte from the stream.
func (d *ZRLEDecoder) readByte() (byte, error) {
	var b [1]byte
	if err := d.z.readFull(b[:]); err != nil {
		return 0, err
	}
	return b[0], nil
}

// tile decodes one ZRLE tile.
func (d *ZRLEDecoder) tile(pr *PixelReader, fb *Framebuffer, ox, oy, tw, th int) error {
	cpix := pr.TPixelSize()

	subenc, err := d.readByte()
	if err != nil {
		return fmt.Errorf("read subencoding: %w", err)
	}

	switch {
	case subenc == zrleRaw:
		buf := d.buf[:tw*th*cpix]
		if err := d.z.readFull(buf); err != nil {
			return fmt.Errorf("read raw tile: %w", err)
		}
		rowBytes := tw * cpix
		for y := 0; y < th; y++ {
			blitTRow(fb, pr, ox, oy+y, tw, buf[y*rowBytes:])
		}
		return nil

	case subenc == zrleSolid:
		buf := d.buf[:cpix]
		if err := d.z.readFull(buf); err != nil {
			return fmt.Errorf("read solid colour: %w", err)
		}
		fb.FillRect(clipRect(fb, ox, oy, tw, th), pr.TColor(buf))
		return nil

	case subenc <= zrleMaxPalette:
		return d.packedPalette(pr, fb, ox, oy, tw, th, int(subenc))

	case subenc < zrlePlainRLE:
		return fmt.Errorf("invalid subencoding %d", subenc)

	case subenc == zrlePlainRLE:
		return d.plainRLE(pr, fb, ox, oy, tw, th)

	case subenc < zrlePaletteRLE:
		// 129 would be a one-entry palette, which is what subencoding 1 is
		// for; the spec reserves it.
		return fmt.Errorf("invalid subencoding %d", subenc)

	default:
		return d.paletteRLE(pr, fb, ox, oy, tw, th, int(subenc)-128)
	}
}

// readPalette reads n CPIXELs into d.pal.
func (d *ZRLEDecoder) readPalette(pr *PixelReader, n int) error {
	cpix := pr.TPixelSize()
	buf := d.buf[:n*cpix]
	if err := d.z.readFull(buf); err != nil {
		return fmt.Errorf("read %d-entry palette: %w", n, err)
	}
	for i := 0; i < n; i++ {
		d.pal[i] = pr.TColor(buf[i*cpix:])
	}
	return nil
}

// zrlePackedBits returns the number of index bits used for a palette of n
// entries. The steps are the spec's, not ceil(log2(n)): 3- and 4-entry
// palettes both use 2 bits and everything up to 16 uses 4.
func zrlePackedBits(n int) int {
	switch {
	case n == 2:
		return 1
	case n <= 4:
		return 2
	default:
		return 4
	}
}

// packedPalette decodes subencodings 2..16.
func (d *ZRLEDecoder) packedPalette(pr *PixelReader, fb *Framebuffer, ox, oy, tw, th, n int) error {
	if err := d.readPalette(pr, n); err != nil {
		return err
	}
	bits := zrlePackedBits(n)
	// Each row restarts on a byte boundary, so a tile narrower than the
	// padding unit wastes the tail bits rather than running rows together.
	rowBytes := (tw*bits + 7) / 8
	buf := d.buf[:rowBytes*th]
	if err := d.z.readFull(buf); err != nil {
		return fmt.Errorf("read packed indices: %w", err)
	}

	mask := byte(1<<bits) - 1
	for y := 0; y < th; y++ {
		row := buf[y*rowBytes:]
		for x := 0; x < tw; x++ {
			bit := x * bits
			// Indices are packed most-significant bits first.
			shift := 8 - bits - (bit % 8)
			idx := (row[bit/8] >> shift) & mask
			if int(idx) >= n {
				return fmt.Errorf("palette index %d out of range (size %d)", idx, n)
			}
			fb.SetRGBA(ox+x, oy+y, d.pal[idx])
		}
	}
	return nil
}

// runLength reads a run length: a sequence of bytes where 255 means "add 255
// and keep going" and the terminating byte (0..254) contributes its value plus
// one. Starting the accumulator at one folds in that final increment.
func (d *ZRLEDecoder) runLength(limit int) (int, error) {
	length := 1
	for {
		b, err := d.readByte()
		if err != nil {
			return 0, fmt.Errorf("read run length: %w", err)
		}
		length += int(b)
		if b != 255 {
			return length, nil
		}
		if length > limit {
			// Bail out rather than loop forever on a stream of 0xff.
			return 0, fmt.Errorf("run length exceeds tile area %d", limit)
		}
	}
}

// plainRLE decodes subencoding 128.
func (d *ZRLEDecoder) plainRLE(pr *PixelReader, fb *Framebuffer, ox, oy, tw, th int) error {
	cpix := pr.TPixelSize()
	area := tw * th
	for i := 0; i < area; {
		buf := d.buf[:cpix]
		if err := d.z.readFull(buf); err != nil {
			return fmt.Errorf("read run colour: %w", err)
		}
		col := pr.TColor(buf)
		run, err := d.runLength(area)
		if err != nil {
			return err
		}
		if i+run > area {
			return fmt.Errorf("run of %d at offset %d overflows tile area %d", run, i, area)
		}
		i = paintRun(fb, ox, oy, tw, i, run, col)
	}
	return nil
}

// paletteRLE decodes subencodings 130..255.
func (d *ZRLEDecoder) paletteRLE(pr *PixelReader, fb *Framebuffer, ox, oy, tw, th, n int) error {
	if err := d.readPalette(pr, n); err != nil {
		return err
	}
	area := tw * th
	for i := 0; i < area; {
		b, err := d.readByte()
		if err != nil {
			return fmt.Errorf("read palette run index: %w", err)
		}
		run := 1
		idx := int(b)
		// The top bit distinguishes a run from a lone pixel, which is what
		// keeps palette RLE competitive with a packed palette on noisy tiles.
		if b&0x80 != 0 {
			idx = int(b & 0x7f)
			if run, err = d.runLength(area); err != nil {
				return err
			}
		}
		if idx >= n {
			return fmt.Errorf("palette index %d out of range (size %d)", idx, n)
		}
		if i+run > area {
			return fmt.Errorf("run of %d at offset %d overflows tile area %d", run, i, area)
		}
		i = paintRun(fb, ox, oy, tw, i, run, d.pal[idx])
	}
	return nil
}

// paintRun writes run pixels of col starting at linear tile offset i and
// returns the new offset. Runs wrap across tile rows in scanline order.
func paintRun(fb *Framebuffer, ox, oy, tw, i, run int, col color.RGBA) int {
	for n := 0; n < run; n++ {
		fb.SetRGBA(ox+i%tw, oy+i/tw, col)
		i++
	}
	return i
}
