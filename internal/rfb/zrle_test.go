package rfb

import (
	"testing"
)

// zrleRunLen encodes a run length the way ZRLE does: 255s until the remainder
// fits, then a final byte one less than what is left.
// zrlePaletteRun builds a palette-RLE index byte. The top bit tells the decoder
// that a run length follows; without it the byte is a single pixel.
func zrlePaletteRun(index byte) byte { return 0x80 | index }

func zrleRunLen(n int) []byte {
	if n < 1 {
		panic("run length must be at least 1")
	}
	n--
	var out []byte
	for n >= 255 {
		out = append(out, 255)
		n -= 255
	}
	return append(out, byte(n))
}

func TestZRLEEncodingNumber(t *testing.T) {
	if got := NewZRLEDecoder().Encoding(); got != EncodingZRLE {
		t.Fatalf("Encoding() = %v, want ZRLE", got)
	}
}

// TestZRLERawTile covers subencoding 0 and, with the default pixel format,
// the 3-byte CPIXEL compaction.
func TestZRLERawTile(t *testing.T) {
	pf := PreferredPixelFormat
	if !pf.SupportsCompactTPIXEL() {
		t.Fatal("expected the preferred format to use 3-byte CPIXELs")
	}
	tile := &sb{}
	tile.u8(0) // raw
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			tile.raw(tPixel(pf, uint32(x*16), uint32(y*16), 240))
		}
	}
	// 4x4 raw CPIXELs at three bytes each, plus the subencoding byte.
	if got, want := tile.len(), 1+4*4*3; got != want {
		t.Fatalf("tile payload is %d bytes, want %d (CPIXEL compaction not in effect)", got, want)
	}

	enc := newZlibBlocks()
	c := newTestConn(t, 16, 16, pf, enc.block(tile.bytes()))
	if err := NewZRLEDecoder().Decode(c, Rect{2, 2, 4, 4}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			assertPixel(t, fb, 2+x, 2+y, wireColor(pf, uint32(x*16), uint32(y*16), 240))
		}
	}
	assertDrained(t, c, nil)
}

// TestZRLESolidTile covers subencoding 1.
func TestZRLESolidTile(t *testing.T) {
	pf := PreferredPixelFormat
	tile := &sb{}
	tile.u8(1)
	tile.raw(tPixel(pf, 33, 66, 99))

	enc := newZlibBlocks()
	c := newTestConn(t, 16, 16, pf, enc.block(tile.bytes()))
	if err := NewZRLEDecoder().Decode(c, Rect{0, 0, 5, 3}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	assertUniform(t, c.framebuffer(), 0, 0, 5, 3, wireColor(pf, 33, 66, 99))
	assertDrained(t, c, nil)
}

// TestZRLEPackedPalette covers subencodings 2..16 at all three index widths.
// The bits-per-index steps (1, 2, 4) are the spec's, not ceil(log2(n)), and
// each row of indices restarts on a byte boundary.
func TestZRLEPackedPalette(t *testing.T) {
	pf := PreferredPixelFormat
	palette := [][3]uint32{
		{10, 0, 0}, {0, 20, 0}, {0, 0, 30}, {40, 40, 0}, {50, 0, 50},
	}

	tests := []struct {
		name    string
		size    int
		bits    int
		indices [][]int // one row per tile row
	}{
		{"two colours, one bit", 2, 1, [][]int{
			{0, 1, 1, 0, 1, 0, 0, 1, 1}, // 9 wide: row spans two bytes
			{1, 1, 0, 0, 0, 1, 1, 0, 0},
		}},
		{"three colours, two bits", 3, 2, [][]int{
			{0, 1, 2, 0, 2},
			{2, 2, 1, 1, 0},
		}},
		{"four colours, two bits", 4, 2, [][]int{
			{3, 2, 1, 0, 3},
			{0, 1, 2, 3, 1},
		}},
		{"five colours, four bits", 5, 4, [][]int{
			{4, 3, 2, 1, 0},
			{0, 2, 4, 1, 3},
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := zrlePackedBits(tc.size); got != tc.bits {
				t.Fatalf("zrlePackedBits(%d) = %d, want %d", tc.size, got, tc.bits)
			}
			w, h := len(tc.indices[0]), len(tc.indices)

			tile := &sb{}
			tile.u8(byte(tc.size))
			for i := 0; i < tc.size; i++ {
				tile.raw(tPixel(pf, palette[i][0], palette[i][1], palette[i][2]))
			}
			rowBytes := (w*tc.bits + 7) / 8
			for _, row := range tc.indices {
				packed := make([]byte, rowBytes)
				for x, idx := range row {
					bit := x * tc.bits
					packed[bit/8] |= byte(idx) << (8 - tc.bits - bit%8)
				}
				tile.raw(packed)
			}

			enc := newZlibBlocks()
			c := newTestConn(t, 16, 16, pf, enc.block(tile.bytes()))
			if err := NewZRLEDecoder().Decode(c, Rect{0, 0, uint16(w), uint16(h)}); err != nil {
				t.Fatalf("Decode: %v", err)
			}
			fb := c.framebuffer()
			for y, row := range tc.indices {
				for x, idx := range row {
					p := palette[idx]
					assertPixel(t, fb, x, y, wireColor(pf, p[0], p[1], p[2]))
				}
			}
			assertDrained(t, c, nil)
		})
	}
}

// TestZRLEPlainRLE covers subencoding 128, including a run longer than 255 so
// the multi-byte run-length encoding is exercised.
func TestZRLEPlainRLE(t *testing.T) {
	pf := PreferredPixelFormat
	// A 20x20 tile is 400 pixels: one run of 300, then 100.
	tile := &sb{}
	tile.u8(128)
	tile.raw(tPixel(pf, 11, 22, 33))
	tile.raw(zrleRunLen(300))
	tile.raw(tPixel(pf, 44, 55, 66))
	tile.raw(zrleRunLen(100))

	// Sanity check the 300 encoding is genuinely multi-byte.
	if got := zrleRunLen(300); len(got) != 2 || got[0] != 255 || got[1] != 44 {
		t.Fatalf("run length 300 encoded as %v, want [255 44]", got)
	}

	enc := newZlibBlocks()
	c := newTestConn(t, 32, 32, pf, enc.block(tile.bytes()))
	if err := NewZRLEDecoder().Decode(c, Rect{0, 0, 20, 20}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	first := wireColor(pf, 11, 22, 33)
	second := wireColor(pf, 44, 55, 66)
	// Pixel 299 is the last of the first run: (299%20, 299/20) = (19,14).
	assertPixel(t, fb, 0, 0, first)
	assertPixel(t, fb, 19, 14, first)
	assertPixel(t, fb, 0, 15, second) // pixel 300
	assertPixel(t, fb, 19, 19, second)
	assertDrained(t, c, nil)
}

// TestZRLEPaletteRLE covers subencodings 130..255, mixing single pixels (top
// bit clear) with runs (top bit set).
func TestZRLEPaletteRLE(t *testing.T) {
	pf := PreferredPixelFormat
	palette := [][3]uint32{{1, 1, 1}, {2, 2, 2}, {3, 3, 3}}

	// 4x3 tile = 12 pixels: run of 5 idx0, single idx1, run of 4 idx2,
	// single idx0, single idx1.
	tile := &sb{}
	tile.u8(128 + byte(len(palette)))
	for _, p := range palette {
		tile.raw(tPixel(pf, p[0], p[1], p[2]))
	}
	tile.u8(zrlePaletteRun(0)).raw(zrleRunLen(5))
	tile.u8(1)
	tile.u8(zrlePaletteRun(2)).raw(zrleRunLen(4))
	tile.u8(0)
	tile.u8(1)

	enc := newZlibBlocks()
	c := newTestConn(t, 16, 16, pf, enc.block(tile.bytes()))
	if err := NewZRLEDecoder().Decode(c, Rect{0, 0, 4, 3}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	want := []int{0, 0, 0, 0, 0, 1, 2, 2, 2, 2, 0, 1}
	for i, idx := range want {
		p := palette[idx]
		assertPixel(t, fb, i%4, i/4, wireColor(pf, p[0], p[1], p[2]))
	}
	assertDrained(t, c, nil)
}

// TestZRLEEdgeTiles uses a rectangle larger than one 64x64 tile in both
// directions and not a multiple of it, so all four tile shapes appear.
func TestZRLEEdgeTiles(t *testing.T) {
	pf := PreferredPixelFormat
	// 70x66 produces tiles 64x64, 6x64, 64x2 and 6x2, in that order.
	colours := [][3]uint32{{200, 0, 0}, {0, 200, 0}, {0, 0, 200}, {200, 200, 0}}
	tile := &sb{}
	for _, col := range colours {
		tile.u8(1) // solid
		tile.raw(tPixel(pf, col[0], col[1], col[2]))
	}

	enc := newZlibBlocks()
	trailer := []byte{0x7f}
	c := newTestConn(t, 80, 80, pf, append(enc.block(tile.bytes()), trailer...))
	if err := NewZRLEDecoder().Decode(c, Rect{0, 0, 70, 66}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	assertUniform(t, fb, 0, 0, 64, 64, wireColor(pf, 200, 0, 0))
	assertUniform(t, fb, 64, 0, 6, 64, wireColor(pf, 0, 200, 0))
	assertUniform(t, fb, 0, 64, 64, 2, wireColor(pf, 0, 0, 200))
	assertUniform(t, fb, 64, 64, 6, 2, wireColor(pf, 200, 200, 0))
	assertDrained(t, c, trailer)
}

// TestZRLENonDefaultPixelFormat proves the CPIXEL width follows the pixel
// format: at 16bpp there is no compaction, so a CPIXEL is two bytes.
func TestZRLENonDefaultPixelFormat(t *testing.T) {
	pf := format565
	if pf.SupportsCompactTPIXEL() {
		t.Fatal("16bpp must not use compact CPIXELs")
	}
	tile := &sb{}
	tile.u8(0) // raw
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			tile.raw(tPixel(pf, uint32(x*8), uint32(y*16), 7))
		}
	}
	if got, want := tile.len(), 1+2*3*2; got != want {
		t.Fatalf("tile payload is %d bytes, want %d", got, want)
	}

	enc := newZlibBlocks()
	c := newTestConn(t, 8, 8, pf, enc.block(tile.bytes()))
	if err := NewZRLEDecoder().Decode(c, Rect{0, 0, 3, 2}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			assertPixel(t, fb, x, y, wireColor(pf, uint32(x*8), uint32(y*16), 7))
		}
	}
	assertDrained(t, c, nil)
}

// TestZRLEDictionaryPersistsAcrossRectangles mirrors the Zlib case: ZRLE uses
// one stream for the whole connection too.
func TestZRLEDictionaryPersistsAcrossRectangles(t *testing.T) {
	pf := PreferredPixelFormat
	enc := newZlibBlocks()

	mk := func(r, g, b uint32) []byte {
		tile := &sb{}
		tile.u8(1)
		tile.raw(tPixel(pf, r, g, b))
		return tile.bytes()
	}
	payload := append(enc.block(mk(9, 9, 9)), enc.block(mk(8, 8, 8))...)

	c := newTestConn(t, 16, 16, pf, payload)
	d := NewZRLEDecoder()
	if err := d.Decode(c, Rect{0, 0, 8, 8}); err != nil {
		t.Fatalf("decode rect 1: %v", err)
	}
	if err := d.Decode(c, Rect{8, 8, 8, 8}); err != nil {
		t.Fatalf("decode rect 2: %v", err)
	}
	fb := c.framebuffer()
	assertUniform(t, fb, 0, 0, 8, 8, wireColor(pf, 9, 9, 9))
	assertUniform(t, fb, 8, 8, 8, 8, wireColor(pf, 8, 8, 8))
	assertDrained(t, c, nil)
}

// TestZRLEClippedRectStillConsumesPayload decodes an entirely off-screen
// rectangle and then an on-screen one through the same stream.
func TestZRLEClippedRectStillConsumesPayload(t *testing.T) {
	pf := PreferredPixelFormat
	enc := newZlibBlocks()

	// Off-screen rect is 70x66, so it has the same four tiles as the edge
	// test and exercises the full tile walk with nowhere to draw.
	off := &sb{}
	for i := 0; i < 4; i++ {
		off.u8(1)
		off.raw(tPixel(pf, 1, 2, 3))
	}
	on := &sb{}
	on.u8(1)
	on.raw(tPixel(pf, 250, 240, 230))

	payload := append(enc.block(off.bytes()), enc.block(on.bytes())...)
	c := newTestConn(t, 16, 16, pf, payload)
	d := NewZRLEDecoder()
	if err := d.Decode(c, Rect{500, 500, 70, 66}); err != nil {
		t.Fatalf("decode off-screen rect: %v", err)
	}
	if err := d.Decode(c, Rect{0, 0, 16, 16}); err != nil {
		t.Fatalf("decode on-screen rect: %v", err)
	}
	assertUniform(t, c.framebuffer(), 0, 0, 16, 16, wireColor(pf, 250, 240, 230))
	assertDrained(t, c, nil)
}

// TestZRLERejectsInvalidSubencodings checks the two reserved ranges.
func TestZRLERejectsInvalidSubencodings(t *testing.T) {
	for _, subenc := range []byte{17, 100, 127, 129} {
		enc := newZlibBlocks()
		tile := &sb{}
		tile.u8(subenc)
		tile.rep(64, []byte{0}) // filler so the failure is not merely EOF
		c := newTestConn(t, 16, 16, PreferredPixelFormat, enc.block(tile.bytes()))
		if err := NewZRLEDecoder().Decode(c, Rect{0, 0, 4, 4}); err == nil {
			t.Errorf("subencoding %d was accepted, want an error", subenc)
		}
	}
}

// TestZRLERejectsOutOfRangePaletteIndex covers both palette forms. An index
// past the end of the palette must be refused, not read out of the array.
func TestZRLERejectsOutOfRangePaletteIndex(t *testing.T) {
	pf := PreferredPixelFormat

	t.Run("packed palette", func(t *testing.T) {
		tile := &sb{}
		tile.u8(3) // three colours, so two bits per index
		for i := 0; i < 3; i++ {
			tile.raw(tPixel(pf, uint32(i), 0, 0))
		}
		// Index 3 is representable in two bits but outside a 3-entry palette.
		tile.u8(0xc0, 0xc0)
		enc := newZlibBlocks()
		c := newTestConn(t, 16, 16, pf, enc.block(tile.bytes()))
		if err := NewZRLEDecoder().Decode(c, Rect{0, 0, 2, 2}); err == nil {
			t.Fatal("expected an error for an out-of-range packed palette index")
		}
	})

	t.Run("palette rle", func(t *testing.T) {
		tile := &sb{}
		tile.u8(128 + 2) // two colours
		tile.raw(tPixel(pf, 1, 1, 1))
		tile.raw(tPixel(pf, 2, 2, 2))
		tile.u8(0x80 | 5).raw(zrleRunLen(4)) // index 5 is out of range
		enc := newZlibBlocks()
		c := newTestConn(t, 16, 16, pf, enc.block(tile.bytes()))
		if err := NewZRLEDecoder().Decode(c, Rect{0, 0, 2, 2}); err == nil {
			t.Fatal("expected an error for an out-of-range palette RLE index")
		}
	})
}

// TestZRLEZeroAreaRect checks the degenerate case: no tiles follow, but the
// compressed block still belongs to the stream and must be handed over so the
// next rectangle inflates correctly.
func TestZRLEZeroAreaRect(t *testing.T) {
	pf := PreferredPixelFormat
	enc := newZlibBlocks()

	// The first block is empty; the second carries a real tile. If the empty
	// rectangle dropped its block, the stream would be short by a sync marker.
	tile := &sb{}
	tile.u8(1)
	tile.raw(tPixel(pf, 5, 6, 7))
	payload := append(enc.block(nil), enc.block(tile.bytes())...)

	c := newTestConn(t, 8, 8, pf, payload)
	d := NewZRLEDecoder()
	if err := d.Decode(c, Rect{0, 0, 0, 0}); err != nil {
		t.Fatalf("decode zero-area rect: %v", err)
	}
	if err := d.Decode(c, Rect{0, 0, 8, 8}); err != nil {
		t.Fatalf("decode following rect: %v", err)
	}
	assertUniform(t, c.framebuffer(), 0, 0, 8, 8, wireColor(pf, 5, 6, 7))
	assertDrained(t, c, nil)
}

// TestZRLERejectsTruncatedStream checks that a block which inflates to less
// than the tiles demand is reported rather than silently accepted.
func TestZRLERejectsTruncatedStream(t *testing.T) {
	pf := PreferredPixelFormat
	tile := &sb{}
	tile.u8(0)                    // raw tile...
	tile.raw(tPixel(pf, 1, 2, 3)) // ...but only one of the sixteen pixels
	enc := newZlibBlocks()
	c := newTestConn(t, 16, 16, pf, enc.block(tile.bytes()))
	if err := NewZRLEDecoder().Decode(c, Rect{0, 0, 4, 4}); err == nil {
		t.Fatal("expected an error for a truncated raw tile")
	}
}

// TestZRLERejectsOverlongRun makes sure a run that overflows its tile is
// rejected rather than silently painting outside it.
func TestZRLERejectsOverlongRun(t *testing.T) {
	pf := PreferredPixelFormat
	tile := &sb{}
	tile.u8(128)
	tile.raw(tPixel(pf, 1, 1, 1))
	tile.raw(zrleRunLen(100)) // tile is only 4x4
	enc := newZlibBlocks()
	c := newTestConn(t, 16, 16, pf, enc.block(tile.bytes()))
	if err := NewZRLEDecoder().Decode(c, Rect{0, 0, 4, 4}); err == nil {
		t.Fatal("expected an error for a run longer than the tile")
	}
}
