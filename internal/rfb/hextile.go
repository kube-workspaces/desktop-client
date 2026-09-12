package rfb

import (
	"fmt"
	"image/color"
)

// Hextile subencoding bits (RFC 6143 §7.7.4).
const (
	hextileRaw                 = 1 << 0
	hextileBackgroundSpecified = 1 << 1
	hextileForegroundSpecified = 1 << 2
	hextileAnySubrects         = 1 << 3
	hextileSubrectsColoured    = 1 << 4
)

// hextileTile is the fixed tile edge length; the last row and column of tiles
// in a rectangle are smaller when the rectangle is not a multiple of it.
const hextileTile = 16

// HextileDecoder implements the Hextile encoding (5).
//
// The rectangle is cut into 16x16 tiles, each of which is either raw pixels or
// a background fill plus a handful of solid sub-rectangles. It compresses
// nothing, so it loses badly to Tight and ZRLE on photographic content, but it
// needs no zlib state and stays useful as a low-latency fallback and for
// servers that offer nothing better.
type HextileDecoder struct {
	buf []byte
}

// NewHextileDecoder returns a decoder for the Hextile encoding.
func NewHextileDecoder() Decoder { return &HextileDecoder{} }

// Encoding implements Decoder.
func (d *HextileDecoder) Encoding() Encoding { return EncodingHextile }

// Decode implements Decoder.
func (d *HextileDecoder) Decode(c *Conn, r Rect) error {
	if r.Empty() {
		return nil
	}
	br := c.reader()
	pr := c.PixelReader()
	fb := c.framebuffer()
	bpp := pr.BytesPerPixel()

	// One scratch buffer serves both the raw-tile case and the
	// sub-rectangle list, so size it for whichever is larger. A full tile is
	// 16*16 pixels; a sub-rectangle list is at most 255 entries of a pixel
	// plus two packed position/size bytes.
	d.buf = growBytes(d.buf, max(hextileTile*hextileTile*bpp, 255*(bpp+2)))

	// Background and foreground persist from tile to tile until a tile
	// respecifies them, which is where most of Hextile's savings come from.
	// They are reset per rectangle: the spec only guarantees persistence
	// within one rectangle, and carrying them across would make decoding
	// depend on rectangles the server may believe we never received.
	var bg, fg color.RGBA

	width, height := int(r.Width), int(r.Height)
	for ty := 0; ty < height; ty += hextileTile {
		th := min(hextileTile, height-ty)
		for tx := 0; tx < width; tx += hextileTile {
			tw := min(hextileTile, width-tx)
			var err error
			bg, fg, err = d.tile(br, pr, fb, int(r.X)+tx, int(r.Y)+ty, tw, th, bg, fg)
			if err != nil {
				return fmt.Errorf("tile at +%d+%d: %w", tx, ty, err)
			}
		}
	}
	return nil
}

// tile decodes one tile and returns the (possibly updated) background and
// foreground colours for the next one.
func (d *HextileDecoder) tile(
	br byteReader, pr *PixelReader, fb *Framebuffer,
	ox, oy, tw, th int, bg, fg color.RGBA,
) (color.RGBA, color.RGBA, error) {
	bpp := pr.BytesPerPixel()

	mask, err := br.ReadByte()
	if err != nil {
		return bg, fg, fmt.Errorf("read subencoding: %w", err)
	}

	if mask&hextileRaw != 0 {
		// Raw wins outright: no other bit applies and neither background nor
		// foreground is touched.
		buf := d.buf[:tw*th*bpp]
		if err := readFull(br, buf); err != nil {
			return bg, fg, fmt.Errorf("read raw tile: %w", err)
		}
		rowBytes := tw * bpp
		for y := 0; y < th; y++ {
			if err := blitRow(fb, pr, ox, oy+y, tw, buf[y*rowBytes:]); err != nil {
				return bg, fg, err
			}
		}
		return bg, fg, nil
	}

	if mask&hextileBackgroundSpecified != 0 {
		p := d.buf[:bpp]
		if err := readFull(br, p); err != nil {
			return bg, fg, fmt.Errorf("read background: %w", err)
		}
		bg = pr.Color(p)
	}
	if mask&hextileForegroundSpecified != 0 {
		p := d.buf[:bpp]
		if err := readFull(br, p); err != nil {
			return bg, fg, fmt.Errorf("read foreground: %w", err)
		}
		fg = pr.Color(p)
	}

	fb.FillRect(clipRect(fb, ox, oy, tw, th), bg)

	if mask&hextileAnySubrects == 0 {
		return bg, fg, nil
	}

	count, err := br.ReadByte()
	if err != nil {
		return bg, fg, fmt.Errorf("read sub-rectangle count: %w", err)
	}
	coloured := mask&hextileSubrectsColoured != 0
	stride := 2
	if coloured {
		stride += bpp
	}
	buf := d.buf[:int(count)*stride]
	if err := readFull(br, buf); err != nil {
		return bg, fg, fmt.Errorf("read %d sub-rectangles: %w", count, err)
	}

	for i := 0; i < int(count); i++ {
		off := i * stride
		col := fg
		if coloured {
			col = pr.Color(buf[off:])
			off += bpp
		}
		xy, wh := buf[off], buf[off+1]
		sx, sy := int(xy>>4), int(xy&0x0f)
		sw, sh := int(wh>>4)+1, int(wh&0x0f)+1
		// Clamp to the tile. A conforming server never sends a sub-rectangle
		// that overflows its tile, but the packed nibbles can express one
		// (x=15, w=16), and letting it through would repaint pixels belonging
		// to a neighbouring tile that may already have been decoded.
		sw = min(sw, tw-sx)
		sh = min(sh, th-sy)
		fb.FillRect(clipRect(fb, ox+sx, oy+sy, sw, sh), col)
	}
	return bg, fg, nil
}

// byteReader is the slice of *bufio.Reader the tile decoders need. Naming it
// keeps the tile helper testable without a whole Conn.
type byteReader interface {
	Read(p []byte) (int, error)
	ReadByte() (byte, error)
}
