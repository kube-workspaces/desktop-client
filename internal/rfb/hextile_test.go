package rfb

import (
	"image/color"
	"testing"
)

// hextileSubrect packs one sub-rectangle's position/size nibble pair.
func hextileSubrect(x, y, w, h int) []byte {
	return []byte{byte(x<<4 | y), byte((w-1)<<4 | (h - 1))}
}

// TestHextilePersistentBackgroundAndForeground walks a 32x32 rectangle made of
// four tiles that between them use every subencoding bit, and checks that the
// background and foreground colours set by one tile are still in force for the
// next.
func TestHextilePersistentBackgroundAndForeground(t *testing.T) {
	pf := PreferredPixelFormat
	red := wireColor(pf, 200, 10, 10)
	green := wireColor(pf, 10, 200, 10)
	blue := wireColor(pf, 10, 10, 200)
	white := wireColor(pf, 255, 255, 255)

	s := &sb{}

	// Tile 0 (0,0): background red, foreground green, one uncoloured subrect.
	s.u8(hextileBackgroundSpecified | hextileForegroundSpecified | hextileAnySubrects)
	s.raw(wirePixel(pf, 200, 10, 10))
	s.raw(wirePixel(pf, 10, 200, 10))
	s.u8(1)
	s.raw(hextileSubrect(1, 2, 3, 4))

	// Tile 1 (16,0): mask 0. Nothing is transmitted at all, so the tile must
	// be painted with the background inherited from tile 0.
	s.u8(0)

	// Tile 2 (0,16): raw. Every pixel distinct so a transposed blit is
	// obvious: pixel (x,y) is (x*8, y*8, 128).
	s.u8(hextileRaw)
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			s.raw(wirePixel(pf, uint32(x*8), uint32(y*8), 128))
		}
	}

	// Tile 3 (16,16): new background blue, two coloured subrects. The
	// foreground from tile 0 is not respecified and must not be used, since
	// SubrectsColoured supplies each colour inline.
	s.u8(hextileBackgroundSpecified | hextileAnySubrects | hextileSubrectsColoured)
	s.raw(wirePixel(pf, 10, 10, 200))
	s.u8(2)
	s.raw(wirePixel(pf, 255, 255, 255))
	s.raw(hextileSubrect(0, 0, 2, 2))
	s.raw(wirePixel(pf, 10, 200, 10))
	s.raw(hextileSubrect(14, 14, 2, 2))

	trailer := []byte{0xde, 0xad, 0xbe, 0xef}
	c := newTestConn(t, 32, 32, pf, append(s.bytes(), trailer...))
	d := NewHextileDecoder()
	if got := d.Encoding(); got != EncodingHextile {
		t.Fatalf("Encoding() = %v, want Hextile", got)
	}
	if err := d.Decode(c, Rect{0, 0, 32, 32}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()

	// Tile 0: red background with a green 3x4 subrect at (1,2).
	assertPixel(t, fb, 0, 0, red)
	assertUniform(t, fb, 1, 2, 3, 4, green)
	assertPixel(t, fb, 4, 2, red) // just right of the subrect
	assertPixel(t, fb, 1, 6, red) // just below the subrect

	// Tile 1: entirely the inherited background.
	assertUniform(t, fb, 16, 0, 16, 16, red)

	// Tile 2: raw pixels, checked at three corners.
	assertPixel(t, fb, 0, 16, wireColor(pf, 0, 0, 128))
	assertPixel(t, fb, 15, 16, wireColor(pf, 120, 0, 128))
	assertPixel(t, fb, 15, 31, wireColor(pf, 120, 120, 128))

	// Tile 3: blue background with a white and a green corner.
	assertUniform(t, fb, 16, 16, 2, 2, white)
	assertUniform(t, fb, 30, 30, 2, 2, green)
	assertPixel(t, fb, 20, 20, blue)

	assertDrained(t, c, trailer)
}

// TestHextileEdgeTiles uses a rectangle whose size is not a multiple of 16, so
// the right-hand and bottom tiles are short. Getting the tile size wrong there
// is the classic Hextile bug and shows up immediately as a desynchronised
// stream, which assertDrained catches.
func TestHextileEdgeTiles(t *testing.T) {
	pf := PreferredPixelFormat
	// 20x18 at origin (2,3): tiles are 16x16, 4x16, 16x2 and 4x2.
	s := &sb{}
	tiles := []struct{ w, h int }{{16, 16}, {4, 16}, {16, 2}, {4, 2}}
	for i, tile := range tiles {
		s.u8(hextileRaw)
		for p := 0; p < tile.w*tile.h; p++ {
			s.raw(wirePixel(pf, uint32(i*60), uint32(p%256), 7))
		}
	}
	trailer := []byte{0x11}
	c := newTestConn(t, 40, 40, pf, append(s.bytes(), trailer...))
	if err := NewHextileDecoder().Decode(c, Rect{2, 3, 20, 18}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	// First pixel of each tile identifies which tile painted it.
	assertPixel(t, fb, 2, 3, wireColor(pf, 0, 0, 7))    // tile 0 origin
	assertPixel(t, fb, 18, 3, wireColor(pf, 60, 0, 7))  // tile 1 origin
	assertPixel(t, fb, 2, 19, wireColor(pf, 120, 0, 7)) // tile 2 origin
	assertPixel(t, fb, 18, 19, wireColor(pf, 180, 0, 7))
	// Bottom-right pixel of the 4x2 tile is its last pixel, index 7.
	assertPixel(t, fb, 21, 20, wireColor(pf, 180, 7, 7))
	assertDrained(t, c, trailer)
}

// TestHextileNonDefaultPixelFormat repeats a minimal case over 16bpp 5-6-5 to
// prove the decoder goes through PixelReader rather than assuming RGBA.
func TestHextileNonDefaultPixelFormat(t *testing.T) {
	pf := format565
	s := &sb{}
	s.u8(hextileBackgroundSpecified | hextileForegroundSpecified | hextileAnySubrects)
	s.raw(wirePixel(pf, 31, 0, 0))  // background: full red
	s.raw(wirePixel(pf, 0, 63, 31)) // foreground: cyan
	s.u8(1)
	s.raw(hextileSubrect(2, 2, 4, 4))

	c := newTestConn(t, 16, 16, pf, s.bytes())
	if err := NewHextileDecoder().Decode(c, Rect{0, 0, 16, 16}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	assertPixel(t, fb, 0, 0, wireColor(pf, 31, 0, 0))
	assertUniform(t, fb, 2, 2, 4, 4, wireColor(pf, 0, 63, 31))
	assertPixel(t, fb, 6, 2, wireColor(pf, 31, 0, 0))
	assertDrained(t, c, nil)
}

// TestHextileClampsOversizedSubrect checks that a sub-rectangle whose packed
// nibbles push it past the tile edge cannot repaint a neighbouring tile.
func TestHextileClampsOversizedSubrect(t *testing.T) {
	pf := PreferredPixelFormat
	s := &sb{}
	// Tile 0: background black, one subrect at x=15 with width 16, which would
	// run 15 pixels into tile 1 if it were not clamped.
	s.u8(hextileBackgroundSpecified | hextileForegroundSpecified | hextileAnySubrects)
	s.raw(wirePixel(pf, 0, 0, 0))
	s.raw(wirePixel(pf, 255, 0, 0))
	s.u8(1)
	s.raw([]byte{15 << 4, 15<<4 | 0}) // x=15,y=0,w=16,h=1
	// Tile 1: solid white background.
	s.u8(hextileBackgroundSpecified)
	s.raw(wirePixel(pf, 255, 255, 255))

	c := newTestConn(t, 32, 16, pf, s.bytes())
	if err := NewHextileDecoder().Decode(c, Rect{0, 0, 32, 16}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	assertPixel(t, fb, 15, 0, wireColor(pf, 255, 0, 0))
	// Tile 1 is decoded after tile 0, so this would pass either way; check the
	// whole of tile 1 is white, which it is only if the clamp held.
	assertUniform(t, fb, 16, 0, 16, 16, wireColor(pf, 255, 255, 255))
	assertDrained(t, c, nil)
}

// TestHextileOutOfBoundsRectStillConsumesPayload is the safety property that
// matters most: a rectangle the server places outside the framebuffer must
// still be read off the wire in full, or every subsequent rectangle is
// garbage.
func TestHextileOutOfBoundsRectStillConsumesPayload(t *testing.T) {
	pf := PreferredPixelFormat
	s := &sb{}
	// Rect 1 is 32x32 at (100,100) on a 16x16 framebuffer: entirely off
	// screen, but four tiles of payload all the same.
	for i := 0; i < 4; i++ {
		s.u8(hextileRaw)
		s.rep(16*16, wirePixel(pf, 1, 2, 3))
	}
	// Rect 2 is on screen and must decode correctly.
	s.u8(hextileBackgroundSpecified)
	s.raw(wirePixel(pf, 9, 8, 7))

	c := newTestConn(t, 16, 16, pf, s.bytes())
	d := NewHextileDecoder()
	if err := d.Decode(c, Rect{100, 100, 32, 32}); err != nil {
		t.Fatalf("Decode off-screen rect: %v", err)
	}
	if err := d.Decode(c, Rect{0, 0, 16, 16}); err != nil {
		t.Fatalf("Decode on-screen rect: %v", err)
	}
	assertUniform(t, c.framebuffer(), 0, 0, 16, 16, wireColor(pf, 9, 8, 7))
	assertDrained(t, c, nil)
}

// TestHextilePartiallyClippedRect covers the half-on-screen case, where each
// tile row must be read whole but written short.
func TestHextilePartiallyClippedRect(t *testing.T) {
	pf := PreferredPixelFormat
	s := &sb{}
	// One 16x16 raw tile at (8,8) on a 16x16 framebuffer: only the top-left
	// 8x8 of the tile is visible.
	s.u8(hextileRaw)
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			s.raw(wirePixel(pf, uint32(x), uint32(y), 99))
		}
	}
	trailer := []byte{0x5a}
	c := newTestConn(t, 16, 16, pf, append(s.bytes(), trailer...))
	if err := NewHextileDecoder().Decode(c, Rect{8, 8, 16, 16}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	fb := c.framebuffer()
	assertPixel(t, fb, 8, 8, wireColor(pf, 0, 0, 99))
	assertPixel(t, fb, 15, 8, wireColor(pf, 7, 0, 99))
	assertPixel(t, fb, 15, 15, wireColor(pf, 7, 7, 99))
	// Nothing outside the framebuffer was written, and nothing before the
	// rectangle was disturbed.
	assertPixel(t, fb, 7, 7, color.RGBA{0, 0, 0, 0xff})
	assertDrained(t, c, trailer)
}
