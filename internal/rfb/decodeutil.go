package rfb

import (
	"bufio"
	"fmt"
)

// Shared plumbing for the Hextile, Zlib, Tight and ZRLE decoders.
//
// Two rules drive everything here:
//
//  1. A decoder must consume exactly the rectangle's payload. RFB has no
//     framing, so a single byte of drift desynchronises the connection for
//     good. Every read is therefore unconditional; only the *writes* are
//     skipped when a rectangle falls outside the framebuffer.
//  2. A decoder must never index outside the framebuffer. Rectangle
//     coordinates come straight off the wire, so all geometry is computed in
//     int (never uint16, which would wrap) and clipped before use.

// growBytes returns a slice of exactly n bytes, reusing b's storage when it is
// large enough. Decoders keep their scratch buffers across rectangles so a
// steady-state session does no allocation at all.
func growBytes(b []byte, n int) []byte {
	if n < 0 {
		return nil
	}
	if cap(b) >= n {
		return b[:n]
	}
	return make([]byte, n)
}

// clipRect builds a framebuffer-relative rectangle from int coordinates,
// reduced to the part that is actually on screen. It returns the empty
// rectangle when nothing is visible.
//
// This exists rather than Framebuffer.Clip because tile and sub-rectangle
// offsets are added to a uint16 rectangle origin, and that sum can overflow
// 16 bits for a hostile server. Doing the arithmetic in int and clipping
// before the conversion makes the overflow unrepresentable.
func clipRect(fb *Framebuffer, x, y, w, h int) Rect {
	if w <= 0 || h <= 0 {
		return Rect{}
	}
	if x < 0 {
		w += x
		x = 0
	}
	if y < 0 {
		h += y
		y = 0
	}
	if w <= 0 || h <= 0 || x >= fb.Width || y >= fb.Height {
		return Rect{}
	}
	if x+w > fb.Width {
		w = fb.Width - x
	}
	if y+h > fb.Height {
		h = fb.Height - y
	}
	if w <= 0 || h <= 0 {
		return Rect{}
	}
	return Rect{X: uint16(x), Y: uint16(y), Width: uint16(w), Height: uint16(h)}
}

// visibleSpan reduces a horizontal run of n pixels starting at (x,y) to the
// part inside the framebuffer, reporting how many source pixels to skip at the
// front. It returns ok=false when nothing is visible.
func visibleSpan(fb *Framebuffer, x, y, n int) (skip, start, count int, ok bool) {
	if n <= 0 || y < 0 || y >= fb.Height {
		return 0, 0, 0, false
	}
	skip = 0
	if x < 0 {
		skip = -x
		if skip >= n {
			return 0, 0, 0, false
		}
		n -= skip
		x = 0
	}
	if x >= fb.Width {
		return 0, 0, 0, false
	}
	if x+n > fb.Width {
		n = fb.Width - x
	}
	if n <= 0 {
		return 0, 0, 0, false
	}
	return skip, x, n, true
}

// blitRow converts n wire pixels from src and writes them at (x,y), clipping
// to the framebuffer. src must hold at least n whole pixels; the caller is
// expected to have read them all regardless of whether they are visible.
func blitRow(fb *Framebuffer, pr *PixelReader, x, y, n int, src []byte) error {
	skip, start, count, ok := visibleSpan(fb, x, y, n)
	if !ok {
		return nil
	}
	dst := fb.Row(start, y, count)
	if dst == nil {
		return nil
	}
	return pr.Convert(dst, src[skip*pr.BytesPerPixel():], count)
}

// blitTRow is blitRow for Tight TPIXELs / ZRLE CPIXELs, which may be three
// bytes wide where a normal pixel is four.
func blitTRow(fb *Framebuffer, pr *PixelReader, x, y, n int, src []byte) {
	tpix := pr.TPixelSize()
	skip, start, count, ok := visibleSpan(fb, x, y, n)
	if !ok {
		return
	}
	dst := fb.Row(start, y, count)
	if dst == nil {
		return
	}
	src = src[skip*tpix:]
	for i := 0; i < count; i++ {
		col := pr.TColor(src[i*tpix:])
		o := i * 4
		dst[o] = col.R
		dst[o+1] = col.G
		dst[o+2] = col.B
		dst[o+3] = 0xff
	}
}

// readCompactLen reads Tight's variable-length integer: up to three bytes,
// seven payload bits each, low group first, with the high bit meaning "another
// byte follows". The third byte contributes all eight of its bits, so the
// largest representable value is 0x3FFFFF.
func readCompactLen(r *bufio.Reader) (int, error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, fmt.Errorf("read compact length: %w", err)
	}
	n := int(b & 0x7f)
	if b&0x80 == 0 {
		return n, nil
	}
	b, err = r.ReadByte()
	if err != nil {
		return 0, fmt.Errorf("read compact length: %w", err)
	}
	n |= int(b&0x7f) << 7
	if b&0x80 == 0 {
		return n, nil
	}
	b, err = r.ReadByte()
	if err != nil {
		return 0, fmt.Errorf("read compact length: %w", err)
	}
	n |= int(b) << 14
	return n, nil
}
