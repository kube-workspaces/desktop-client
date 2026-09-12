package rfb

import (
	"bytes"
	"testing"
)

// rawPixels builds width*height wire pixels in scanline order using fn.
func rawPixels(pf PixelFormat, w, h int, fn func(x, y int) (uint32, uint32, uint32)) []byte {
	s := &sb{}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b := fn(x, y)
			s.raw(wirePixel(pf, r, g, b))
		}
	}
	return s.bytes()
}

// TestZlibDictionaryPersistsAcrossRectangles is the point of the Zlib decoder.
// Only the first block carries a zlib header, and the second is deflated
// against the dictionary the first built, so a decoder that made a fresh
// zlib.Reader per rectangle fails outright on rectangle two.
func TestZlibDictionaryPersistsAcrossRectangles(t *testing.T) {
	pf := PreferredPixelFormat
	enc := newZlibBlocks()

	first := rawPixels(pf, 4, 2, func(x, y int) (uint32, uint32, uint32) {
		return uint32(x * 10), uint32(y * 20), 200
	})
	// Identical content in the second rectangle: if the dictionary really did
	// carry over, this compresses to almost nothing, which is also a cheap
	// check that we are exercising the intended path.
	second := rawPixels(pf, 4, 2, func(x, y int) (uint32, uint32, uint32) {
		return uint32(x * 10), uint32(y * 20), 200
	})

	block1 := enc.block(first)
	block2 := enc.block(second)
	if len(block2) >= len(block1) {
		t.Fatalf("second block (%d bytes) is not smaller than the first (%d); "+
			"the test is not exercising dictionary reuse", len(block2), len(block1))
	}

	trailer := []byte{0xaa, 0xbb}
	payload := append(append(bytes.Clone(block1), block2...), trailer...)
	c := newTestConn(t, 8, 4, pf, payload)

	d := NewZlibDecoder()
	if got := d.Encoding(); got != EncodingZlib {
		t.Fatalf("Encoding() = %v, want Zlib", got)
	}
	if err := d.Decode(c, Rect{0, 0, 4, 2}); err != nil {
		t.Fatalf("decode rect 1: %v", err)
	}
	if err := d.Decode(c, Rect{4, 2, 4, 2}); err != nil {
		t.Fatalf("decode rect 2: %v", err)
	}

	fb := c.framebuffer()
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			want := wireColor(pf, uint32(x*10), uint32(y*20), 200)
			assertPixel(t, fb, x, y, want)
			assertPixel(t, fb, 4+x, 2+y, want)
		}
	}
	assertDrained(t, c, trailer)
}

// TestZlibManyRectangles pushes ten rectangles through one stream to make sure
// nothing accumulates or drifts in the block source over a long session.
func TestZlibManyRectangles(t *testing.T) {
	pf := PreferredPixelFormat
	enc := newZlibBlocks()
	const n = 10

	var payload []byte
	for i := 0; i < n; i++ {
		payload = append(payload, enc.block(rawPixels(pf, 4, 1, func(x, _ int) (uint32, uint32, uint32) {
			return uint32(i * 20), uint32(x * 30), 5
		}))...)
	}

	c := newTestConn(t, 4, n, pf, payload)
	d := NewZlibDecoder()
	for i := 0; i < n; i++ {
		if err := d.Decode(c, Rect{0, uint16(i), 4, 1}); err != nil {
			t.Fatalf("decode rect %d: %v", i, err)
		}
	}
	fb := c.framebuffer()
	for i := 0; i < n; i++ {
		for x := 0; x < 4; x++ {
			assertPixel(t, fb, x, i, wireColor(pf, uint32(i*20), uint32(x*30), 5))
		}
	}
	assertDrained(t, c, nil)
}

// TestZlibNonDefaultPixelFormat runs the 16bpp path, where a pixel is two wire
// bytes rather than four.
func TestZlibNonDefaultPixelFormat(t *testing.T) {
	pf := format565
	enc := newZlibBlocks()
	data := rawPixels(pf, 3, 3, func(x, y int) (uint32, uint32, uint32) {
		return uint32(x * 10), uint32(y * 20), 15
	})
	c := newTestConn(t, 8, 8, pf, enc.block(data))
	if err := NewZlibDecoder().Decode(c, Rect{1, 1, 3, 3}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			assertPixel(t, fb, 1+x, 1+y, wireColor(pf, uint32(x*10), uint32(y*20), 15))
		}
	}
	assertDrained(t, c, nil)
}

// TestZlibClippedRectStillConsumesPayload decodes an off-screen rectangle
// followed by an on-screen one. The off-screen block must still be fed to the
// zlib stream, or the second rectangle inflates to nonsense.
func TestZlibClippedRectStillConsumesPayload(t *testing.T) {
	pf := PreferredPixelFormat
	enc := newZlibBlocks()

	// Rect 1 straddles the right and bottom edges of a 16x16 framebuffer.
	offscreen := rawPixels(pf, 12, 12, func(x, y int) (uint32, uint32, uint32) {
		return uint32(x), uint32(y), 1
	})
	onscreen := rawPixels(pf, 4, 4, func(x, y int) (uint32, uint32, uint32) {
		return 7, uint32(x), uint32(y)
	})
	payload := append(enc.block(offscreen), enc.block(onscreen)...)

	c := newTestConn(t, 16, 16, pf, payload)
	d := NewZlibDecoder()
	if err := d.Decode(c, Rect{10, 10, 12, 12}); err != nil {
		t.Fatalf("decode clipped rect: %v", err)
	}
	if err := d.Decode(c, Rect{0, 0, 4, 4}); err != nil {
		t.Fatalf("decode following rect: %v", err)
	}

	fb := c.framebuffer()
	// The visible 6x6 corner of the clipped rectangle.
	assertPixel(t, fb, 10, 10, wireColor(pf, 0, 0, 1))
	assertPixel(t, fb, 15, 15, wireColor(pf, 5, 5, 1))
	// And the rectangle that followed it.
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			assertPixel(t, fb, x, y, wireColor(pf, 7, uint32(x), uint32(y)))
		}
	}
	assertDrained(t, c, nil)
}

// TestZlibZeroAreaRect checks that an empty rectangle still hands its block to
// the stream, so the rectangle after it inflates correctly.
func TestZlibZeroAreaRect(t *testing.T) {
	pf := PreferredPixelFormat
	enc := newZlibBlocks()
	payload := append(enc.block(nil), enc.block(rawPixels(pf, 2, 2, func(x, y int) (uint32, uint32, uint32) {
		return 3, 4, 5
	}))...)

	c := newTestConn(t, 8, 8, pf, payload)
	d := NewZlibDecoder()
	if err := d.Decode(c, Rect{0, 0, 0, 4}); err != nil {
		t.Fatalf("decode zero-area rect: %v", err)
	}
	if err := d.Decode(c, Rect{0, 0, 2, 2}); err != nil {
		t.Fatalf("decode following rect: %v", err)
	}
	assertUniform(t, c.framebuffer(), 0, 0, 2, 2, wireColor(pf, 3, 4, 5))
	assertDrained(t, c, nil)
}

// TestZlibRejectsTruncatedData checks that a block inflating to fewer bytes
// than the rectangle needs is an error.
func TestZlibRejectsTruncatedData(t *testing.T) {
	pf := PreferredPixelFormat
	enc := newZlibBlocks()
	// Two pixels of data for a rectangle that needs sixteen.
	c := newTestConn(t, 8, 8, pf, enc.block(rawPixels(pf, 2, 1, func(int, int) (uint32, uint32, uint32) {
		return 1, 2, 3
	})))
	if err := NewZlibDecoder().Decode(c, Rect{0, 0, 4, 4}); err == nil {
		t.Fatal("expected an error for a truncated zlib payload")
	}
}

// TestZlibRejectsCorruptStream checks that garbage where a zlib header should
// be is reported rather than panicking.
func TestZlibRejectsCorruptStream(t *testing.T) {
	pf := PreferredPixelFormat
	s := &sb{}
	s.u32(8)
	s.raw([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	c := newTestConn(t, 8, 8, pf, s.bytes())
	if err := NewZlibDecoder().Decode(c, Rect{0, 0, 2, 2}); err == nil {
		t.Fatal("expected an error for a corrupt zlib stream")
	}
}

// TestZlibRejectsImplausibleLength makes sure a corrupt length prefix is
// refused rather than turned into a huge allocation.
func TestZlibRejectsImplausibleLength(t *testing.T) {
	pf := PreferredPixelFormat
	s := &sb{}
	s.u32(0xffffffff)
	c := newTestConn(t, 8, 8, pf, s.bytes())
	if err := NewZlibDecoder().Decode(c, Rect{0, 0, 4, 4}); err == nil {
		t.Fatal("expected an error for an implausible compressed length")
	}
}
