package rfb

import (
	"encoding/binary"
	"fmt"
	"image/color"
)

// PixelReader converts pixels from a server's wire format into the RGBA byte
// order the framebuffer uses.
//
// The conversion is set up once per connection rather than per pixel: when the
// server honours PreferredPixelFormat the whole thing collapses to a copy plus
// an alpha fixup, which is the case we care about for QEMU.
type PixelReader struct {
	format PixelFormat
	bpp    int
	// fast is true when the wire bytes are already R,G,B,pad little-endian.
	fast bool
	// scale factors turn a raw channel value into 0..255. Precomputed because
	// the maxima are almost always 255 (a no-op) but need not be.
	redMax, greenMax, blueMax       uint32
	redShift, greenShift, blueShift uint8
	bigEndian                       bool
}

// NewPixelReader builds a converter for the given format. It returns an error
// for formats this client cannot represent, notably colour-mapped (non
// true-colour) formats, which we never negotiate.
func NewPixelReader(pf PixelFormat) (*PixelReader, error) {
	if !pf.TrueColor {
		return nil, fmt.Errorf("rfb: colour-mapped pixel formats are not supported (depth %d)", pf.Depth)
	}
	switch pf.BitsPerPixel {
	case 8, 16, 32:
	default:
		return nil, fmt.Errorf("rfb: unsupported bits-per-pixel %d", pf.BitsPerPixel)
	}
	return &PixelReader{
		format:     pf,
		bpp:        pf.BytesPerPixel(),
		fast:       pf.IsRGBA(),
		redMax:     uint32(pf.RedMax),
		greenMax:   uint32(pf.GreenMax),
		blueMax:    uint32(pf.BlueMax),
		redShift:   pf.RedShift,
		greenShift: pf.GreenShift,
		blueShift:  pf.BlueShift,
		bigEndian:  pf.BigEndian,
	}, nil
}

// BytesPerPixel returns the wire size of one pixel.
func (p *PixelReader) BytesPerPixel() int { return p.bpp }

// Format returns the wire format being converted from.
func (p *PixelReader) Format() PixelFormat { return p.format }

// raw assembles the little- or big-endian integer for one wire pixel.
func (p *PixelReader) raw(src []byte) uint32 {
	switch p.bpp {
	case 1:
		return uint32(src[0])
	case 2:
		if p.bigEndian {
			return uint32(binary.BigEndian.Uint16(src))
		}
		return uint32(binary.LittleEndian.Uint16(src))
	default:
		if p.bigEndian {
			return binary.BigEndian.Uint32(src)
		}
		return binary.LittleEndian.Uint32(src)
	}
}

// Color converts a single wire pixel. src must hold at least BytesPerPixel
// bytes.
func (p *PixelReader) Color(src []byte) color.RGBA {
	if p.fast {
		return color.RGBA{R: src[0], G: src[1], B: src[2], A: 0xff}
	}
	v := p.raw(src)
	return color.RGBA{
		R: scaleChannel((v>>p.redShift)&p.redMax, p.redMax),
		G: scaleChannel((v>>p.greenShift)&p.greenMax, p.greenMax),
		B: scaleChannel((v>>p.blueShift)&p.blueMax, p.blueMax),
		A: 0xff,
	}
}

// Convert converts n consecutive wire pixels from src into dst, which must have
// room for n*4 bytes of RGBA.
func (p *PixelReader) Convert(dst, src []byte, n int) error {
	if len(src) < n*p.bpp {
		return fmt.Errorf("rfb: pixel source too short: have %d, need %d", len(src), n*p.bpp)
	}
	if len(dst) < n*4 {
		return fmt.Errorf("rfb: pixel destination too short: have %d, need %d", len(dst), n*4)
	}
	if p.fast {
		// Wire bytes are R,G,B,pad: copy wholesale, then force alpha opaque.
		copy(dst[:n*4], src[:n*4])
		for i := 3; i < n*4; i += 4 {
			dst[i] = 0xff
		}
		return nil
	}
	for i := 0; i < n; i++ {
		c := p.Color(src[i*p.bpp:])
		o := i * 4
		dst[o] = c.R
		dst[o+1] = c.G
		dst[o+2] = c.B
		dst[o+3] = 0xff
	}
	return nil
}

// scaleChannel maps a raw channel value in 0..max onto 0..255.
func scaleChannel(v, max uint32) uint8 {
	if max == 0 {
		return 0
	}
	if max == 255 {
		return uint8(v)
	}
	return uint8((v*255 + max/2) / max)
}

// CompactTPIXEL reports whether Tight's 3-byte pixel compaction is in force for
// this format.
func (p *PixelReader) CompactTPIXEL() bool { return p.format.SupportsCompactTPIXEL() }

// TPixelSize returns the number of bytes Tight uses for one pixel: 3 when the
// compaction applies, otherwise the normal pixel size.
func (p *PixelReader) TPixelSize() int {
	if p.CompactTPIXEL() {
		return 3
	}
	return p.bpp
}

// TColor converts one Tight "TPIXEL". When the compaction is in force the three
// bytes are always R,G,B in that order regardless of the negotiated shifts.
func (p *PixelReader) TColor(src []byte) color.RGBA {
	if p.CompactTPIXEL() {
		return color.RGBA{R: src[0], G: src[1], B: src[2], A: 0xff}
	}
	return p.Color(src)
}
