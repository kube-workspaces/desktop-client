package rfb

import (
	"encoding/binary"
	"fmt"
)

// Decoder decodes the body of one framebuffer rectangle.
//
// A Decoder is owned by a single Conn and may keep per-connection state, which
// several encodings require: Tight and ZRLE both carry zlib streams whose
// dictionaries persist across rectangles, so they cannot be shared between
// connections or reset mid-stream.
//
// Decode is called with the rectangle header already consumed. It must read
// exactly the rectangle's payload from c.reader() — over-reading or
// under-reading desynchronises the stream irrecoverably, because RFB has no
// framing to resynchronise against.
type Decoder interface {
	// Encoding returns the encoding number this decoder handles.
	Encoding() Encoding
	// Decode reads and applies one rectangle.
	Decode(c *Conn, r Rect) error
}

// DefaultDecoders returns a fresh decoder set for one connection. Decoders are
// stateful, so each Conn needs its own.
func DefaultDecoders() []Decoder {
	return []Decoder{
		&RawDecoder{},
		&CopyRectDecoder{},
		&RREDecoder{},
		NewHextileDecoder(),
		NewZlibDecoder(),
		NewTightDecoder(),
		NewZRLEDecoder(),
	}
}

// RawDecoder implements the Raw encoding: width*height pixels in scanline
// order. Every server must support it and every client must implement it, so it
// is the fallback when nothing else is negotiated.
type RawDecoder struct {
	buf []byte
}

// Encoding implements Decoder.
func (d *RawDecoder) Encoding() Encoding { return EncodingRaw }

// Decode implements Decoder.
func (d *RawDecoder) Decode(c *Conn, r Rect) error {
	pr := c.PixelReader()
	bpp := pr.BytesPerPixel()
	rowBytes := int(r.Width) * bpp
	if rowBytes == 0 || r.Height == 0 {
		return nil
	}
	if cap(d.buf) < rowBytes {
		d.buf = make([]byte, rowBytes)
	}
	row := d.buf[:rowBytes]

	fb := c.framebuffer()
	for y := 0; y < int(r.Height); y++ {
		if err := readFull(c.reader(), row); err != nil {
			return fmt.Errorf("read scanline %d: %w", y, err)
		}
		// Rows beyond the framebuffer still have to be read off the wire, they
		// just have nowhere to go.
		dst := fb.Row(int(r.X), int(r.Y)+y, int(r.Width))
		if dst == nil {
			continue
		}
		if err := pr.Convert(dst, row, int(r.Width)); err != nil {
			return err
		}
	}
	return nil
}

// CopyRectDecoder implements the CopyRect encoding: the rectangle's contents
// are already on screen somewhere else, so only the source coordinates travel.
// It is what makes window drags and scrolling nearly free.
type CopyRectDecoder struct{}

// Encoding implements Decoder.
func (d *CopyRectDecoder) Encoding() Encoding { return EncodingCopyRect }

// Decode implements Decoder.
func (d *CopyRectDecoder) Decode(c *Conn, r Rect) error {
	var buf [4]byte
	if err := readFull(c.reader(), buf[:]); err != nil {
		return fmt.Errorf("read source position: %w", err)
	}
	srcX := int(binary.BigEndian.Uint16(buf[0:]))
	srcY := int(binary.BigEndian.Uint16(buf[2:]))
	c.framebuffer().CopyRect(r, srcX, srcY)
	return nil
}

// RREDecoder implements the RRE encoding: a background colour plus a list of
// solid sub-rectangles. Modern servers rarely choose it, but it is cheap to
// support and appears in older or minimal servers.
type RREDecoder struct {
	buf []byte
}

// Encoding implements Decoder.
func (d *RREDecoder) Encoding() Encoding { return EncodingRRE }

// Decode implements Decoder.
func (d *RREDecoder) Decode(c *Conn, r Rect) error {
	pr := c.PixelReader()
	bpp := pr.BytesPerPixel()

	head := make([]byte, 4+bpp)
	if err := readFull(c.reader(), head); err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	numSubRects := binary.BigEndian.Uint32(head[:4])
	background := pr.Color(head[4:])

	fb := c.framebuffer()
	fb.FillRect(fb.Clip(r), background)

	// Each sub-rectangle is a pixel plus four 16-bit coordinates.
	subSize := bpp + 8
	need := int(numSubRects) * subSize
	if need < 0 {
		return fmt.Errorf("implausible sub-rectangle count %d", numSubRects)
	}
	if cap(d.buf) < need {
		d.buf = make([]byte, need)
	}
	body := d.buf[:need]
	if err := readFull(c.reader(), body); err != nil {
		return fmt.Errorf("read sub-rectangles: %w", err)
	}

	for i := 0; i < int(numSubRects); i++ {
		off := i * subSize
		col := pr.Color(body[off:])
		sub := Rect{
			X:      r.X + binary.BigEndian.Uint16(body[off+bpp:]),
			Y:      r.Y + binary.BigEndian.Uint16(body[off+bpp+2:]),
			Width:  binary.BigEndian.Uint16(body[off+bpp+4:]),
			Height: binary.BigEndian.Uint16(body[off+bpp+6:]),
		}
		fb.FillRect(fb.Clip(sub), col)
	}
	return nil
}
