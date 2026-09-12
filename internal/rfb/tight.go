package rfb

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
)

// Tight compression types, taken from the top nibble of the
// compression-control byte.
const (
	tightOpFill = 0x08
	tightOpJPEG = 0x09
	tightOpPNG  = 0x0a
)

// Tight filter identifiers, used by the "basic" compression types.
const (
	tightFilterCopy     = 0
	tightFilterPalette  = 1
	tightFilterGradient = 2
)

// tightMinToCompress is the threshold below which filtered data is sent
// uncompressed and without a length prefix. Below a dozen bytes zlib's
// per-block overhead exceeds anything it could save.
const tightMinToCompress = 12

// tightStreams is the number of independent zlib streams Tight multiplexes.
// Servers assign them by content type — typically one for solid-ish fills,
// one for palette indices, one for gradients and one for full-colour data —
// so that dissimilar data never pollutes one another's dictionary.
const tightStreams = 4

// TightDecoder implements the Tight encoding (7).
//
// Tight is the reason this client asks for it first: it is the only encoding
// QEMU will emit JPEG with, and JPEG is what makes photographic and video
// content affordable over a WebSocket. Each rectangle independently picks a
// solid fill, a JPEG image, or a "basic" filtered-and-deflated block, and the
// four zlib streams persist for the life of the connection.
type TightDecoder struct {
	streams [tightStreams]zlibStream
	comp    []byte
	data    []byte
	pal     [256]color.RGBA
	// grad holds two rows of per-channel component values for the gradient
	// filter: the row being reconstructed and the one above it.
	grad []uint32
}

// NewTightDecoder returns a decoder for the Tight encoding. It carries four
// connection-scoped zlib dictionaries and must not be shared between
// connections.
func NewTightDecoder() Decoder { return &TightDecoder{} }

// Encoding implements Decoder.
func (d *TightDecoder) Encoding() Encoding { return EncodingTight }

// Decode implements Decoder.
func (d *TightDecoder) Decode(c *Conn, r Rect) error {
	br := c.reader()
	ctl, err := br.ReadByte()
	if err != nil {
		return fmt.Errorf("read compression control: %w", err)
	}

	// The low nibble asks for zlib stream resets. These are processed before
	// anything else in the rectangle, because the very next thing the
	// rectangle does may be to use one of the streams it just reset.
	for i := range tightStreams {
		if ctl&(1<<i) != 0 {
			d.streams[i].reset()
		}
	}

	switch op := ctl >> 4; {
	case op == tightOpFill:
		return d.fill(c, r)
	case op == tightOpJPEG:
		return d.decodeJPEG(c, r)
	case op == tightOpPNG:
		// TightPNG is a separate encoding number that we never advertise, so
		// a server sending this has ignored our encoding list. There is no way
		// to know the payload length without parsing it, so the stream is lost
		// either way; fail loudly rather than desynchronise silently.
		return fmt.Errorf("server used TightPNG, which was not advertised")
	case op > tightOpPNG:
		return fmt.Errorf("invalid compression type %#x", op)
	default:
		return d.basic(c, r, op)
	}
}

// fill paints the whole rectangle one colour. There is no length prefix: the
// payload is exactly one TPIXEL.
func (d *TightDecoder) fill(c *Conn, r Rect) error {
	pr := c.PixelReader()
	d.data = growBytes(d.data, pr.TPixelSize())
	if err := readFull(c.reader(), d.data); err != nil {
		return fmt.Errorf("read fill colour: %w", err)
	}
	fb := c.framebuffer()
	fb.FillRect(clipRect(fb, int(r.X), int(r.Y), int(r.Width), int(r.Height)), pr.TColor(d.data))
	return nil
}

// decodeJPEG decodes a JPEG-compressed rectangle. The JPEG is always RGB no
// matter what pixel format was negotiated, so the PixelReader plays no part
// here.
func (d *TightDecoder) decodeJPEG(c *Conn, r Rect) error {
	n, err := readCompactLen(c.reader())
	if err != nil {
		return err
	}
	d.comp = growBytes(d.comp, n)
	if err := readFull(c.reader(), d.comp); err != nil {
		return fmt.Errorf("read %d jpeg bytes: %w", n, err)
	}
	// Decoding from a bytes.Reader rather than the connection guarantees the
	// wire stays in sync even if the JPEG itself is malformed or the decoder
	// stops short of the declared length.
	img, err := jpeg.Decode(bytes.NewReader(d.comp))
	if err != nil {
		return fmt.Errorf("decode jpeg: %w", err)
	}
	blitImage(c.framebuffer(), img, int(r.X), int(r.Y), int(r.Width), int(r.Height))
	return nil
}

// blitImage copies a decoded image into the framebuffer, clipped to both the
// rectangle and the framebuffer.
func blitImage(fb *Framebuffer, img image.Image, ox, oy, w, h int) {
	b := img.Bounds()
	w = min(w, b.Dx())
	h = min(h, b.Dy())
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// At().RGBA() returns 16-bit alpha-premultiplied components;
			// JPEG is always opaque, so the high byte is the value we want.
			cr, cg, cb, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			fb.SetRGBA(ox+x, oy+y, color.RGBA{R: uint8(cr >> 8), G: uint8(cg >> 8), B: uint8(cb >> 8), A: 0xff})
		}
	}
}

// basic decodes the filtered-and-optionally-deflated compression types.
func (d *TightDecoder) basic(c *Conn, r Rect, op byte) error {
	br := c.reader()
	pr := c.PixelReader()
	fb := c.framebuffer()

	streamID := int(op & 0x03)
	filter := byte(tightFilterCopy)
	if op&0x04 != 0 {
		var err error
		if filter, err = br.ReadByte(); err != nil {
			return fmt.Errorf("read filter id: %w", err)
		}
	}

	w, h := int(r.Width), int(r.Height)
	tpix := pr.TPixelSize()

	// Work out how many bytes the filtered data occupies once decompressed.
	// This is the only thing that tells us how much to inflate, and for the
	// palette filter it also decides the packing of the indices.
	var dataLen, palSize int
	switch filter {
	case tightFilterCopy, tightFilterGradient:
		dataLen = w * h * tpix
	case tightFilterPalette:
		n, err := br.ReadByte()
		if err != nil {
			return fmt.Errorf("read palette size: %w", err)
		}
		palSize = int(n) + 1
		// The palette itself is never compressed: it sits in the clear
		// between the filter header and the (possibly deflated) indices.
		d.data = growBytes(d.data, palSize*tpix)
		if err := readFull(br, d.data); err != nil {
			return fmt.Errorf("read %d-entry palette: %w", palSize, err)
		}
		for i := 0; i < palSize; i++ {
			d.pal[i] = pr.TColor(d.data[i*tpix:])
		}
		if palSize == 2 {
			dataLen = ((w + 7) / 8) * h
		} else {
			dataLen = w * h
		}
	default:
		return fmt.Errorf("unknown filter id %d", filter)
	}

	if dataLen == 0 {
		return nil
	}
	d.data = growBytes(d.data, dataLen)
	if dataLen < tightMinToCompress {
		if err := readFull(br, d.data); err != nil {
			return fmt.Errorf("read %d uncompressed bytes: %w", dataLen, err)
		}
	} else {
		n, err := readCompactLen(br)
		if err != nil {
			return err
		}
		if n > maxCompressedBlock {
			return fmt.Errorf("implausible compressed length %d", n)
		}
		d.comp = growBytes(d.comp, n)
		if err := readFull(br, d.comp); err != nil {
			return fmt.Errorf("read %d compressed bytes: %w", n, err)
		}
		d.streams[streamID].push(d.comp)
		if err := d.streams[streamID].readFull(d.data); err != nil {
			return fmt.Errorf("stream %d: %w", streamID, err)
		}
	}

	switch filter {
	case tightFilterCopy:
		rowBytes := w * tpix
		for y := 0; y < h; y++ {
			blitTRow(fb, pr, int(r.X), int(r.Y)+y, w, d.data[y*rowBytes:])
		}
		return nil
	case tightFilterPalette:
		return d.blitPalette(fb, r, w, h, palSize)
	default:
		return d.blitGradient(pr, fb, r, w, h)
	}
}

// blitPalette expands palette indices into the framebuffer.
func (d *TightDecoder) blitPalette(fb *Framebuffer, r Rect, w, h, palSize int) error {
	ox, oy := int(r.X), int(r.Y)
	if palSize == 2 {
		// Two colours are packed one bit per pixel, most significant bit
		// first, with every row starting on a fresh byte.
		rowBytes := (w + 7) / 8
		for y := 0; y < h; y++ {
			row := d.data[y*rowBytes:]
			for x := 0; x < w; x++ {
				fb.SetRGBA(ox+x, oy+y, d.pal[(row[x/8]>>(7-uint(x)%8))&1])
			}
		}
		return nil
	}
	for y := 0; y < h; y++ {
		row := d.data[y*w:]
		for x := 0; x < w; x++ {
			idx := int(row[x])
			if idx >= palSize {
				return fmt.Errorf("palette index %d out of range (size %d)", idx, palSize)
			}
			fb.SetRGBA(ox+x, oy+y, d.pal[idx])
		}
	}
	return nil
}

// blitGradient undoes the gradient filter.
//
// Each colour component is predicted from its left, upper and upper-left
// neighbours as left+above-aboveleft, clamped into the channel's range, and
// the transmitted byte is the difference modulo the channel's range. Pixels
// off the top or left edge count as zero.
//
// The arithmetic is done on the wire format's channel values rather than on
// scaled 0..255 ones: the modulus is the channel maximum plus one, and
// rounding a 5-bit channel up to 8 bits before subtracting would not
// round-trip.
func (d *TightDecoder) blitGradient(pr *PixelReader, fb *Framebuffer, r Rect, w, h int) error {
	tpix := pr.TPixelSize()
	compact := pr.CompactTPIXEL()
	pf := pr.Format()

	var maxes [3]uint32
	var shifts [3]uint8
	if compact {
		// The 3-byte TPIXEL is plain R,G,B bytes regardless of the negotiated
		// shifts, so the filter operates on those bytes directly.
		maxes = [3]uint32{255, 255, 255}
	} else {
		maxes = [3]uint32{uint32(pf.RedMax), uint32(pf.GreenMax), uint32(pf.BlueMax)}
		shifts = [3]uint8{pf.RedShift, pf.GreenShift, pf.BlueShift}
	}
	for i, m := range maxes {
		// The modulo step below is a mask, which is only equivalent for
		// maxima that are all-ones. Every real RFB format qualifies.
		if m == 0 || m&(m+1) != 0 {
			return fmt.Errorf("gradient filter needs power-of-two channel maxima, got %d for channel %d", m, i)
		}
	}

	// Two rows of three components: the row being built and the one above.
	d.grad = growUint32(d.grad, 2*w*3)
	above := d.grad[:w*3]
	cur := d.grad[w*3 : 2*w*3]
	clear(above)

	ox, oy := int(r.X), int(r.Y)
	rowBytes := w * tpix
	for y := 0; y < h; y++ {
		src := d.data[y*rowBytes:]
		var left [3]uint32
		for x := 0; x < w; x++ {
			px := src[x*tpix:]
			var out color.RGBA
			for ci := 0; ci < 3; ci++ {
				maxc := maxes[ci]
				var delta uint32
				if compact {
					delta = uint32(px[ci])
				} else {
					delta = (pr.raw(px) >> shifts[ci]) & maxc
				}
				var upLeft uint32
				if x > 0 {
					upLeft = above[(x-1)*3+ci]
				}
				pred := int64(left[ci]) + int64(above[x*3+ci]) - int64(upLeft)
				if pred < 0 {
					pred = 0
				} else if pred > int64(maxc) {
					pred = int64(maxc)
				}
				v := (uint32(pred) + delta) & maxc
				cur[x*3+ci] = v
				left[ci] = v
				switch ci {
				case 0:
					out.R = scaleChannel(v, maxc)
				case 1:
					out.G = scaleChannel(v, maxc)
				default:
					out.B = scaleChannel(v, maxc)
				}
			}
			out.A = 0xff
			fb.SetRGBA(ox+x, oy+y, out)
		}
		above, cur = cur, above
	}
	return nil
}

// growUint32 is growBytes for the gradient filter's component rows.
func growUint32(b []uint32, n int) []uint32 {
	if cap(b) >= n {
		return b[:n]
	}
	return make([]uint32, n)
}
