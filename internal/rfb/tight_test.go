package rfb

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// tightCtl assembles a compression-control byte from a compression type and a
// set of zlib stream-reset flags.
func tightCtl(op, resets byte) byte { return op<<4 | resets }

// tightCompactLen encodes Tight's variable-length integer, the mirror of
// readCompactLen.
func tightCompactLen(n int) []byte {
	if n <= 0x7f {
		return []byte{byte(n)}
	}
	out := []byte{byte(n&0x7f) | 0x80, byte((n >> 7) & 0x7f)}
	if n <= 0x3fff {
		return out
	}
	out[1] |= 0x80
	return append(out, byte((n>>14)&0xff))
}

// tightBlock compresses data on enc and returns it with its compact length
// prefix, the form Tight uses for basic rectangles of 12 bytes or more.
func tightBlock(enc *zlibBlocks, data []byte) []byte {
	comp := enc.next(data)
	return append(tightCompactLen(len(comp)), comp...)
}

// pseudoRandom produces deterministic incompressible bytes, so tests can force
// a compressed block past the one- and two-byte compact length boundaries.
func pseudoRandom(n int, seed uint32) []byte {
	out := make([]byte, n)
	s := seed
	for i := range out {
		s = s*1664525 + 1013904223
		out[i] = byte(s >> 24)
	}
	return out
}

func assertPixelNear(t *testing.T, fb *Framebuffer, x, y int, want color.RGBA, tol int) {
	t.Helper()
	got := fb.At(x, y).(color.RGBA)
	diff := func(a, b uint8) int {
		if a > b {
			return int(a) - int(b)
		}
		return int(b) - int(a)
	}
	if diff(got.R, want.R) > tol || diff(got.G, want.G) > tol || diff(got.B, want.B) > tol {
		t.Errorf("pixel (%d,%d) = %v, want within %d of %v", x, y, got, tol, want)
	}
}

func TestTightEncodingNumber(t *testing.T) {
	if got := NewTightDecoder().Encoding(); got != EncodingTight {
		t.Fatalf("Encoding() = %v, want Tight", got)
	}
}

// TestTightFill covers the Fill compression type, which carries a single
// TPIXEL and no length prefix at all.
func TestTightFill(t *testing.T) {
	pf := PreferredPixelFormat
	s := &sb{}
	s.u8(tightCtl(tightOpFill, 0))
	s.raw(tPixel(pf, 12, 34, 56))
	// Fill has no length prefix, so a decoder that expected one would eat
	// these trailing bytes.
	trailer := []byte{0x01, 0x02, 0x03}

	c := newTestConn(t, 16, 16, pf, append(s.bytes(), trailer...))
	if err := NewTightDecoder().Decode(c, Rect{2, 3, 6, 5}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	assertUniform(t, c.framebuffer(), 2, 3, 6, 5, wireColor(pf, 12, 34, 56))
	assertDrained(t, c, trailer)
}

// TestTightBasicCopyCompressed covers the plain Copy filter above the
// compression threshold: a compact length followed by deflated TPIXELs.
func TestTightBasicCopyCompressed(t *testing.T) {
	pf := PreferredPixelFormat
	const w, h = 4, 4

	data := &sb{}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			data.raw(tPixel(pf, uint32(x*16), uint32(y*16), 128))
		}
	}
	if got := w * h * 3; got < tightMinToCompress {
		t.Fatalf("payload of %d bytes would take the raw path", got)
	}

	enc := newZlibBlocks()
	s := &sb{}
	s.u8(tightCtl(0, 0)) // stream 0, implicit Copy filter
	s.raw(tightBlock(enc, data.bytes()))

	c := newTestConn(t, 16, 16, pf, s.bytes())
	if err := NewTightDecoder().Decode(c, Rect{0, 0, w, h}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			assertPixel(t, fb, x, y, wireColor(pf, uint32(x*16), uint32(y*16), 128))
		}
	}
	assertDrained(t, c, nil)
}

// TestTightBasicCopyRawUnderThreshold covers the sub-12-byte case, where the
// filtered data is sent uncompressed and with no length prefix.
func TestTightBasicCopyRawUnderThreshold(t *testing.T) {
	pf := PreferredPixelFormat
	const w, h = 3, 1 // 3 pixels * 3 bytes = 9, under the threshold

	data := &sb{}
	for x := 0; x < w; x++ {
		data.raw(tPixel(pf, uint32(x+1), uint32(x+2), uint32(x+3)))
	}
	if data.len() >= tightMinToCompress {
		t.Fatalf("payload of %d bytes is not under the threshold", data.len())
	}

	s := &sb{}
	s.u8(tightCtl(0x04, 0)) // explicit filter byte follows, stream 0
	s.u8(tightFilterCopy)
	s.raw(data.bytes())
	trailer := []byte{0xfe}

	c := newTestConn(t, 8, 8, pf, append(s.bytes(), trailer...))
	if err := NewTightDecoder().Decode(c, Rect{1, 1, w, h}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	for x := 0; x < w; x++ {
		assertPixel(t, fb, 1+x, 1, wireColor(pf, uint32(x+1), uint32(x+2), uint32(x+3)))
	}
	assertDrained(t, c, trailer)
}

// TestTightPaletteTwoColours covers the bit-packed two-entry palette, whose
// rows are padded to whole bytes.
func TestTightPaletteTwoColours(t *testing.T) {
	pf := PreferredPixelFormat
	// 9 wide so each row needs two bytes with seven bits of padding.
	rows := [][]int{
		{0, 1, 1, 0, 1, 0, 0, 1, 1},
		{1, 0, 0, 1, 0, 1, 1, 0, 0},
		{1, 1, 1, 1, 1, 1, 1, 1, 1},
	}
	w, h := len(rows[0]), len(rows)

	s := &sb{}
	s.u8(tightCtl(0x04|1, 0)) // explicit filter, stream 1
	s.u8(tightFilterPalette)
	s.u8(1) // numColours - 1
	s.raw(tPixel(pf, 20, 0, 0))
	s.raw(tPixel(pf, 0, 0, 20))

	rowBytes := (w + 7) / 8
	data := make([]byte, 0, rowBytes*h)
	for _, row := range rows {
		packed := make([]byte, rowBytes)
		for x, idx := range row {
			packed[x/8] |= byte(idx) << (7 - uint(x)%8)
		}
		data = append(data, packed...)
	}
	if len(data) >= tightMinToCompress {
		t.Fatalf("packed indices are %d bytes; expected the raw path", len(data))
	}
	s.raw(data)

	c := newTestConn(t, 16, 16, pf, s.bytes())
	if err := NewTightDecoder().Decode(c, Rect{0, 0, uint16(w), uint16(h)}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	palette := []color.RGBA{wireColor(pf, 20, 0, 0), wireColor(pf, 0, 0, 20)}
	for y, row := range rows {
		for x, idx := range row {
			assertPixel(t, fb, x, y, palette[idx])
		}
	}
	assertDrained(t, c, nil)
}

// TestTightPaletteManyColours covers palettes larger than two, where indices
// are one whole byte each and, here, large enough to be deflated.
func TestTightPaletteManyColours(t *testing.T) {
	pf := PreferredPixelFormat
	palette := [][3]uint32{{100, 0, 0}, {0, 100, 0}, {0, 0, 100}, {100, 100, 100}}
	const w, h = 4, 4
	indices := []byte{
		0, 1, 2, 3,
		3, 2, 1, 0,
		1, 1, 2, 2,
		0, 3, 0, 3,
	}
	if len(indices) < tightMinToCompress {
		t.Fatal("expected the compressed path")
	}

	enc := newZlibBlocks()
	s := &sb{}
	s.u8(tightCtl(0x04|2, 0)) // explicit filter, stream 2
	s.u8(tightFilterPalette)
	s.u8(byte(len(palette) - 1))
	for _, p := range palette {
		s.raw(tPixel(pf, p[0], p[1], p[2]))
	}
	s.raw(tightBlock(enc, indices))

	c := newTestConn(t, 8, 8, pf, s.bytes())
	if err := NewTightDecoder().Decode(c, Rect{0, 0, w, h}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	for i, idx := range indices {
		p := palette[idx]
		assertPixel(t, fb, i%w, i/w, wireColor(pf, p[0], p[1], p[2]))
	}
	assertDrained(t, c, nil)
}

// TestTightGradient checks the gradient filter against hand-computed values.
//
// The deltas are chosen so the reconstruction passes through every branch of
// the predictor: a plain accumulation, a wrap past 255, a prediction clamped
// up at the maximum and one clamped up from negative.
func TestTightGradient(t *testing.T) {
	pf := PreferredPixelFormat
	const w, h = 3, 3

	// Per-pixel (R,G,B) deltas, row major.
	deltas := [][3]uint32{
		{0, 1, 0}, {200, 1, 0}, {56, 1, 0},
		{0, 1, 0}, {56, 1, 0}, {10, 1, 0},
		{250, 1, 0}, {0, 1, 0}, {0, 1, 0},
	}
	// Hand-computed reconstruction:
	//   R row 0: pred 0,0,200        -> 0, 200, (200+56)&255 = 0
	//   R row 1: pred 0,200,-200->0  -> 0, (200+56)&255 = 0, 10
	//   R row 2: pred 0,250,260->255 -> 250, 250, 255
	//   G accumulates by one per step in both directions.
	want := [][3]uint32{
		{0, 1, 0}, {200, 2, 0}, {0, 3, 0},
		{0, 2, 0}, {0, 4, 0}, {10, 6, 0},
		{250, 3, 0}, {250, 6, 0}, {255, 9, 0},
	}

	data := &sb{}
	for _, d := range deltas {
		data.raw(tPixel(pf, d[0], d[1], d[2]))
	}
	if data.len() < tightMinToCompress {
		t.Fatal("expected the compressed path")
	}

	enc := newZlibBlocks()
	s := &sb{}
	s.u8(tightCtl(0x04|3, 0)) // explicit filter, stream 3
	s.u8(tightFilterGradient)
	s.raw(tightBlock(enc, data.bytes()))

	c := newTestConn(t, 8, 8, pf, s.bytes())
	if err := NewTightDecoder().Decode(c, Rect{0, 0, w, h}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	for i, v := range want {
		assertPixel(t, fb, i%w, i/w, wireColor(pf, v[0], v[1], v[2]))
	}
	assertDrained(t, c, nil)
}

// TestTightGradientNonDefaultPixelFormat runs the gradient filter at 16bpp
// 5-6-5, where the modulus is the channel maximum plus one rather than 256 and
// the components have to be unpacked from the wire pixel. It also lands on the
// sub-threshold raw path, since 2x2 pixels of two bytes is only eight bytes.
func TestTightGradientNonDefaultPixelFormat(t *testing.T) {
	pf := format565
	const w, h = 2, 2

	deltas := [][3]uint32{
		{1, 2, 3}, {2, 0, 0},
		{0, 1, 0}, {31, 63, 31},
	}
	// Hand-computed, per channel, with maxima 31/63/31:
	//   R: 1, (1+2)=3 | (pred 1)+0=1, (pred 3)+31 = 34&31 = 2
	//   G: 2, (pred 2)+0=2 | (pred 2)+1=3, (pred 3)+63 = 66&63 = 2
	//   B: 3, (pred 3)+0=3 | (pred 3)+0=3, (pred 3)+31 = 34&31 = 2
	want := [][3]uint32{
		{1, 2, 3}, {3, 2, 3},
		{1, 3, 3}, {2, 2, 2},
	}

	data := &sb{}
	for _, d := range deltas {
		data.raw(tPixel(pf, d[0], d[1], d[2]))
	}
	if data.len() >= tightMinToCompress {
		t.Fatalf("payload of %d bytes; expected the raw path", data.len())
	}

	s := &sb{}
	s.u8(tightCtl(0x04, 0))
	s.u8(tightFilterGradient)
	s.raw(data.bytes())

	c := newTestConn(t, 8, 8, pf, s.bytes())
	if err := NewTightDecoder().Decode(c, Rect{0, 0, w, h}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	for i, v := range want {
		assertPixel(t, fb, i%w, i/w, wireColor(pf, v[0], v[1], v[2]))
	}
	assertDrained(t, c, nil)
}

// TestTightJPEG covers the JPEG compression type. JPEG carries its own colour
// information, so the negotiated pixel format plays no part; the comparison is
// approximate because the codec is lossy.
func TestTightJPEG(t *testing.T) {
	pf := PreferredPixelFormat
	const w, h = 16, 16

	src := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Two flat blocks, which JPEG reproduces closely.
			col := color.RGBA{R: 200, G: 40, B: 40, A: 255}
			if x >= w/2 {
				col = color.RGBA{R: 40, G: 40, B: 200, A: 255}
			}
			src.SetRGBA(x, y, col)
		}
	}
	var jbuf bytes.Buffer
	if err := jpeg.Encode(&jbuf, src, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}

	s := &sb{}
	s.u8(tightCtl(tightOpJPEG, 0))
	s.raw(tightCompactLen(jbuf.Len()))
	s.raw(jbuf.Bytes())
	trailer := []byte{0x42}

	c := newTestConn(t, 32, 32, pf, append(s.bytes(), trailer...))
	if err := NewTightDecoder().Decode(c, Rect{4, 4, w, h}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	assertPixelNear(t, fb, 6, 6, color.RGBA{200, 40, 40, 255}, 12)
	assertPixelNear(t, fb, 17, 17, color.RGBA{40, 40, 200, 255}, 12)
	// Nothing outside the rectangle should have been touched.
	assertPixel(t, fb, 3, 3, color.RGBA{0, 0, 0, 0xff})
	assertDrained(t, c, trailer)
}

// TestTightRejectsCorruptJPEG checks that a malformed JPEG is an error rather
// than a panic, and that the declared length is still consumed in full so the
// failure is contained to this rectangle.
func TestTightRejectsCorruptJPEG(t *testing.T) {
	pf := PreferredPixelFormat
	garbage := pseudoRandom(200, 0xabcdef)
	s := &sb{}
	s.u8(tightCtl(tightOpJPEG, 0))
	s.raw(tightCompactLen(len(garbage)))
	s.raw(garbage)
	trailer := []byte{0x77}

	c := newTestConn(t, 16, 16, pf, append(s.bytes(), trailer...))
	if err := NewTightDecoder().Decode(c, Rect{0, 0, 16, 16}); err == nil {
		t.Fatal("expected an error for a corrupt JPEG")
	}
	// The payload was read from a bytes.Reader, not the connection, so the
	// wire is still exactly where it should be.
	assertDrained(t, c, trailer)
}

// TestTightCompactLengthWidths drives real rectangles whose compressed payload
// needs a one-, two- and three-byte length prefix, since mis-parsing the
// continuation bit is silent until the next rectangle.
func TestTightCompactLengthWidths(t *testing.T) {
	pf := PreferredPixelFormat
	tests := []struct {
		name      string
		w, h      int
		wantWidth int
	}{
		{"one byte", 4, 4, 1},
		{"two bytes", 12, 12, 2},
		{"three bytes", 90, 90, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Incompressible data, so the compressed size tracks the raw size
			// and reliably crosses the 128 and 16384 byte boundaries.
			data := pseudoRandom(tc.w*tc.h*3, 0x12345678)
			enc := newZlibBlocks()
			block := tightBlock(enc, data)

			// Read the prefix width back off the block we built, so the test
			// fails loudly if the payload ever stops crossing the boundary it
			// is meant to.
			width := 1
			if block[0]&0x80 != 0 {
				width = 2
				if block[1]&0x80 != 0 {
					width = 3
				}
			}
			if width != tc.wantWidth {
				t.Fatalf("compact length is %d bytes wide, want %d", width, tc.wantWidth)
			}

			s := &sb{}
			s.u8(tightCtl(0, 0))
			s.raw(block)

			c := newTestConn(t, 128, 128, pf, s.bytes())
			if err := NewTightDecoder().Decode(c, Rect{0, 0, uint16(tc.w), uint16(tc.h)}); err != nil {
				t.Fatalf("Decode: %v", err)
			}
			fb := c.framebuffer()
			for _, p := range [][2]int{{0, 0}, {tc.w - 1, 0}, {0, tc.h - 1}, {tc.w - 1, tc.h - 1}} {
				i := (p[1]*tc.w + p[0]) * 3
				assertPixel(t, fb, p[0], p[1], color.RGBA{data[i], data[i+1], data[i+2], 0xff})
			}
			assertDrained(t, c, nil)
		})
	}
}

// TestTightStreamResetAndIndependence checks both halves of the zlib stream
// multiplexing: a reset really does start a fresh stream, and the four streams
// keep separate dictionaries.
func TestTightStreamResetAndIndependence(t *testing.T) {
	pf := PreferredPixelFormat
	const w, h = 4, 4

	mk := func(base uint32) []byte {
		d := &sb{}
		for i := 0; i < w*h; i++ {
			d.raw(tPixel(pf, base, uint32(i), 7))
		}
		return d.bytes()
	}

	// Stream 0 gets two rectangles, then a reset and a third from a brand new
	// deflate stream. Stream 1 gets one rectangle interleaved between them; if
	// the decoder muddled the streams, stream 0's dictionary would be
	// corrupted and rectangle three would fail.
	stream0a := newZlibBlocks()
	stream1 := newZlibBlocks()
	stream0b := newZlibBlocks() // the post-reset stream

	s := &sb{}
	s.u8(tightCtl(0, 0)).raw(tightBlock(stream0a, mk(10)))
	s.u8(tightCtl(1, 0)).raw(tightBlock(stream1, mk(20)))
	s.u8(tightCtl(0, 0)).raw(tightBlock(stream0a, mk(30)))
	// Reset flag for stream 0 only; the payload is a fresh zlib stream, which
	// only decodes if the old inflater really was discarded.
	s.u8(tightCtl(0, 1<<0)).raw(tightBlock(stream0b, mk(40)))
	// Stream 1 must be untouched by that reset, so it continues from its own
	// dictionary.
	s.u8(tightCtl(1, 0)).raw(tightBlock(stream1, mk(50)))

	c := newTestConn(t, 32, 32, pf, s.bytes())
	d := NewTightDecoder()
	bases := []uint32{10, 20, 30, 40, 50}
	for i, base := range bases {
		r := Rect{X: uint16((i % 4) * 4), Y: uint16((i / 4) * 4), Width: w, Height: h}
		if err := d.Decode(c, r); err != nil {
			t.Fatalf("decode rect %d (base %d): %v", i, base, err)
		}
		fb := c.framebuffer()
		for p := 0; p < w*h; p++ {
			assertPixel(t, fb, int(r.X)+p%w, int(r.Y)+p/w, wireColor(pf, base, uint32(p), 7))
		}
	}
	assertDrained(t, c, nil)
}

// TestTightClippedRectStillConsumesPayload decodes an off-screen rectangle and
// then an on-screen one, across the fill, copy and JPEG paths.
func TestTightClippedRectStillConsumesPayload(t *testing.T) {
	pf := PreferredPixelFormat
	const w, h = 8, 8

	data := &sb{}
	for i := 0; i < w*h; i++ {
		data.raw(tPixel(pf, uint32(i), 1, 2))
	}
	enc := newZlibBlocks()

	s := &sb{}
	// Rect 1: off-screen basic copy.
	s.u8(tightCtl(0, 0)).raw(tightBlock(enc, data.bytes()))
	// Rect 2: on-screen fill.
	s.u8(tightCtl(tightOpFill, 0)).raw(tPixel(pf, 77, 88, 99))

	c := newTestConn(t, 16, 16, pf, s.bytes())
	d := NewTightDecoder()
	if err := d.Decode(c, Rect{200, 200, w, h}); err != nil {
		t.Fatalf("decode off-screen rect: %v", err)
	}
	if err := d.Decode(c, Rect{0, 0, 16, 16}); err != nil {
		t.Fatalf("decode on-screen rect: %v", err)
	}
	assertUniform(t, c.framebuffer(), 0, 0, 16, 16, wireColor(pf, 77, 88, 99))
	assertDrained(t, c, nil)
}

// TestTightPartiallyClippedRect checks a rectangle straddling the edge: the
// whole payload is consumed, only the visible part is drawn.
func TestTightPartiallyClippedRect(t *testing.T) {
	pf := PreferredPixelFormat
	const w, h = 8, 8
	data := &sb{}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			data.raw(tPixel(pf, uint32(x), uint32(y), 3))
		}
	}
	enc := newZlibBlocks()
	s := &sb{}
	s.u8(tightCtl(0, 0)).raw(tightBlock(enc, data.bytes()))
	trailer := []byte{0x99}

	c := newTestConn(t, 16, 16, pf, append(s.bytes(), trailer...))
	if err := NewTightDecoder().Decode(c, Rect{12, 12, w, h}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	assertPixel(t, fb, 12, 12, wireColor(pf, 0, 0, 3))
	assertPixel(t, fb, 15, 15, wireColor(pf, 3, 3, 3))
	assertDrained(t, c, trailer)
}

// TestTightRejectsUnsupportedCompressionTypes covers the two cases where the
// only safe response is to fail: TightPNG (which we never advertise) and a
// reserved compression type. Both leave the payload length unknowable, so
// continuing would desynchronise the connection.
func TestTightRejectsUnsupportedCompressionTypes(t *testing.T) {
	tests := []struct {
		name string
		op   byte
	}{
		{"png", tightOpPNG},
		{"reserved 0x0b", 0x0b},
		{"reserved 0x0f", 0x0f},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &sb{}
			s.u8(tightCtl(tc.op, 0))
			s.rep(32, []byte{0})
			c := newTestConn(t, 16, 16, PreferredPixelFormat, s.bytes())
			if err := NewTightDecoder().Decode(c, Rect{0, 0, 4, 4}); err == nil {
				t.Fatalf("compression type %#x was accepted, want an error", tc.op)
			}
		})
	}
}

// TestTightRejectsOutOfRangePaletteIndex checks the bounds guard on the
// one-byte-per-pixel palette form.
func TestTightRejectsOutOfRangePaletteIndex(t *testing.T) {
	pf := PreferredPixelFormat
	s := &sb{}
	s.u8(tightCtl(0x04, 0))
	s.u8(tightFilterPalette)
	s.u8(2) // three colours
	for i := 0; i < 3; i++ {
		s.raw(tPixel(pf, uint32(i), 0, 0))
	}
	// 3x3 indices is 9 bytes, under the compression threshold, and index 200
	// is well past the end of the palette.
	s.raw([]byte{0, 1, 2, 0, 200, 1, 2, 0, 1})

	c := newTestConn(t, 16, 16, pf, s.bytes())
	if err := NewTightDecoder().Decode(c, Rect{0, 0, 3, 3}); err == nil {
		t.Fatal("expected an error for an out-of-range palette index")
	}
}

// TestTightZeroAreaRect checks that a rectangle with no pixels still consumes
// its compression-control byte and nothing more.
func TestTightZeroAreaRect(t *testing.T) {
	pf := PreferredPixelFormat
	s := &sb{}
	s.u8(tightCtl(0, 0)) // basic copy of nothing
	trailer := []byte{0xab, 0xcd}

	c := newTestConn(t, 8, 8, pf, append(s.bytes(), trailer...))
	if err := NewTightDecoder().Decode(c, Rect{0, 0, 0, 0}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	assertDrained(t, c, trailer)
}

// TestTightRejectsUnknownFilter checks the filter-id guard.
func TestTightRejectsUnknownFilter(t *testing.T) {
	s := &sb{}
	s.u8(tightCtl(0x04, 0))
	s.u8(9) // no such filter
	s.rep(32, []byte{0})
	c := newTestConn(t, 16, 16, PreferredPixelFormat, s.bytes())
	if err := NewTightDecoder().Decode(c, Rect{0, 0, 4, 4}); err == nil {
		t.Fatal("expected an error for an unknown filter id")
	}
}

// TestDefaultDecodersCoverNegotiatedEncodings ties the constructors back to the
// encoding list the client actually advertises.
func TestDefaultDecodersCoverNegotiatedEncodings(t *testing.T) {
	byEnc := map[Encoding]Decoder{}
	for _, d := range DefaultDecoders() {
		if _, dup := byEnc[d.Encoding()]; dup {
			t.Fatalf("duplicate decoder for %v", d.Encoding())
		}
		byEnc[d.Encoding()] = d
	}
	for _, enc := range []Encoding{EncodingHextile, EncodingZlib, EncodingTight, EncodingZRLE} {
		if _, ok := byEnc[enc]; !ok {
			t.Errorf("no decoder registered for %v", enc)
		}
	}
	// Every real encoding we advertise must have a decoder, or the connection
	// dies the first time the server picks it.
	for _, enc := range DefaultEncodings {
		if enc < 0 {
			continue
		}
		if _, ok := byEnc[enc]; !ok {
			t.Errorf("advertised encoding %v has no decoder", enc)
		}
	}
}
