package rfb

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image/color"
	"io"
	"testing"
)

// --- pixel format fixtures --------------------------------------------------

// format565 is a deliberately awkward non-default format: 16bpp 5-6-5 with the
// red channel at the top. It exercises the PixelReader conversion path and,
// for ZRLE and Tight, the non-compact (full width) CPIXEL/TPIXEL path.
var format565 = PixelFormat{
	BitsPerPixel: 16,
	Depth:        16,
	BigEndian:    false,
	TrueColor:    true,
	RedMax:       31,
	GreenMax:     63,
	BlueMax:      31,
	RedShift:     11,
	GreenShift:   5,
	BlueShift:    0,
}

// wirePixel encodes raw channel values (already in 0..max for the format) into
// one on-the-wire pixel.
func wirePixel(pf PixelFormat, r, g, b uint32) []byte {
	v := (r << pf.RedShift) | (g << pf.GreenShift) | (b << pf.BlueShift)
	switch pf.BytesPerPixel() {
	case 1:
		return []byte{byte(v)}
	case 2:
		out := make([]byte, 2)
		if pf.BigEndian {
			binary.BigEndian.PutUint16(out, uint16(v))
		} else {
			binary.LittleEndian.PutUint16(out, uint16(v))
		}
		return out
	default:
		out := make([]byte, 4)
		if pf.BigEndian {
			binary.BigEndian.PutUint32(out, v)
		} else {
			binary.LittleEndian.PutUint32(out, v)
		}
		return out
	}
}

// wireColor is the RGBA a decoder must produce for the same raw channel values.
func wireColor(pf PixelFormat, r, g, b uint32) color.RGBA {
	return color.RGBA{
		R: scaleChannel(r, uint32(pf.RedMax)),
		G: scaleChannel(g, uint32(pf.GreenMax)),
		B: scaleChannel(b, uint32(pf.BlueMax)),
		A: 0xff,
	}
}

// tPixel encodes a TPIXEL (Tight) / CPIXEL (ZRLE), which drops the padding byte
// when the format allows.
func tPixel(pf PixelFormat, r, g, b uint32) []byte {
	if pf.SupportsCompactTPIXEL() {
		return []byte{byte(r), byte(g), byte(b)}
	}
	return wirePixel(pf, r, g, b)
}

// --- stream building --------------------------------------------------------

// sb accumulates a byte stream the way a server would emit one.
type sb struct{ b bytes.Buffer }

func (s *sb) u8(v ...byte) *sb { s.b.Write(v); return s }
func (s *sb) raw(p []byte) *sb { s.b.Write(p); return s }
func (s *sb) bytes() []byte    { return s.b.Bytes() }
func (s *sb) len() int         { return s.b.Len() }
func (s *sb) u16(v uint16) *sb { return s.raw(binary.BigEndian.AppendUint16(nil, v)) }
func (s *sb) u32(v uint32) *sb { return s.raw(binary.BigEndian.AppendUint32(nil, v)) }
func (s *sb) rep(n int, p []byte) *sb {
	for i := 0; i < n; i++ {
		s.b.Write(p)
	}
	return s
}

// zlibBlocks mimics a server's persistent zlib stream: one deflate stream whose
// dictionary carries across rectangles, flushed (Z_SYNC_FLUSH) at each
// rectangle boundary.
type zlibBlocks struct {
	buf bytes.Buffer
	w   *zlib.Writer
}

func newZlibBlocks() *zlibBlocks {
	z := &zlibBlocks{}
	z.w = zlib.NewWriter(&z.buf)
	return z
}

// next compresses one rectangle's worth of data and returns just the bytes
// produced for it.
func (z *zlibBlocks) next(data []byte) []byte {
	z.buf.Reset()
	if _, err := z.w.Write(data); err != nil {
		panic(err)
	}
	if err := z.w.Flush(); err != nil {
		panic(err)
	}
	return bytes.Clone(z.buf.Bytes())
}

// block returns a u32-length-prefixed compressed block, the wire form used by
// both the Zlib and ZRLE encodings.
func (z *zlibBlocks) block(data []byte) []byte {
	c := z.next(data)
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(c))), c...)
}

// --- connection harness -----------------------------------------------------

// scriptedRW is an io.ReadWriter that replays a pre-recorded server stream and
// swallows everything the client writes.
type scriptedRW struct {
	r   *bytes.Reader
	out bytes.Buffer
}

func (s *scriptedRW) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *scriptedRW) Write(p []byte) (int, error) { return s.out.Write(p) }

// serverHandshake builds the bytes a server sends up to and including
// ServerInit, so tests can drive the real NewConn path rather than
// hand-assembling a Conn.
func serverHandshake(width, height int, pf PixelFormat, name string) []byte {
	s := &sb{}
	s.raw([]byte(ProtocolVersion))
	s.u8(1, secNone) // one security type: None
	s.u32(0)         // security result: OK
	s.u16(uint16(width)).u16(uint16(height))
	s.raw(pf.marshal())
	s.u32(uint32(len(name)))
	s.raw([]byte(name))
	return s.bytes()
}

// newTestConn runs a full handshake against a scripted server and leaves
// payload queued on the reader for a decoder to consume.
func newTestConn(t *testing.T, width, height int, pf PixelFormat, payload []byte) *Conn {
	t.Helper()
	stream := append(serverHandshake(width, height, pf, "test"), payload...)
	c, err := NewConn(&scriptedRW{r: bytes.NewReader(stream)}, Config{PixelFormat: &pf})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if got, want := c.ServerName(), "test"; got != want {
		t.Fatalf("server name = %q, want %q", got, want)
	}
	if w, h := c.Size(); w != width || h != height {
		t.Fatalf("size = %dx%d, want %dx%d", w, h, width, height)
	}
	return c
}

// --- assertions -------------------------------------------------------------

func assertPixel(t *testing.T, fb *Framebuffer, x, y int, want color.RGBA) {
	t.Helper()
	got := fb.At(x, y).(color.RGBA)
	if got != want {
		t.Errorf("pixel (%d,%d) = %v, want %v", x, y, got, want)
	}
}

func assertUniform(t *testing.T, fb *Framebuffer, x, y, w, h int, want color.RGBA) {
	t.Helper()
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			got := fb.At(x+dx, y+dy).(color.RGBA)
			if got != want {
				t.Fatalf("pixel (%d,%d) = %v, want %v (uniform block %dx%d at %d,%d)",
					x+dx, y+dy, got, want, w, h, x, y)
			}
		}
	}
}

// assertDrained checks that the decoders consumed exactly what they should
// have: the reader must hold precisely want and nothing else. This is the
// single most important property of every decoder, since RFB cannot
// resynchronise after a byte of drift.
func assertDrained(t *testing.T, c *Conn, want []byte) {
	t.Helper()
	got, err := io.ReadAll(c.reader())
	if err != nil {
		t.Fatalf("drain reader: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("stream position wrong: %d trailing bytes %x, want %d bytes %x",
			len(got), got, len(want), want)
	}
}

// --- helper unit tests ------------------------------------------------------

func TestReadCompactLen(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want int
	}{
		{"one byte zero", []byte{0x00}, 0},
		{"one byte max", []byte{0x7f}, 127},
		{"two bytes min", []byte{0x80, 0x01}, 128},
		{"two bytes max", []byte{0xff, 0x7f}, 16383},
		{"three bytes min", []byte{0x80, 0x80, 0x01}, 16384},
		{"three bytes max", []byte{0xff, 0xff, 0xff}, 0x3fffff},
		{"three bytes mixed", []byte{0x01, 0x00}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := bufio.NewReader(bytes.NewReader(tc.in))
			got, err := readCompactLen(r)
			if err != nil {
				t.Fatalf("readCompactLen: %v", err)
			}
			if got != tc.want {
				t.Fatalf("readCompactLen = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestVisibleSpan(t *testing.T) {
	fb := NewFramebuffer(10, 10)
	tests := []struct {
		name               string
		x, y, n            int
		skip, start, count int
		ok                 bool
	}{
		{"fully inside", 2, 3, 4, 0, 2, 4, true},
		{"clipped right", 8, 0, 5, 0, 8, 2, true},
		{"starts left of origin", -3, 0, 5, 3, 0, 2, true},
		{"entirely left of origin", -9, 0, 5, 0, 0, 0, false},
		{"entirely right", 10, 0, 4, 0, 0, 0, false},
		{"row below framebuffer", 0, 10, 4, 0, 0, 0, false},
		{"negative row", 0, -1, 4, 0, 0, 0, false},
		{"zero length", 0, 0, 0, 0, 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			skip, start, count, ok := visibleSpan(fb, tc.x, tc.y, tc.n)
			if ok != tc.ok || (ok && (skip != tc.skip || start != tc.start || count != tc.count)) {
				t.Fatalf("visibleSpan(%d,%d,%d) = (%d,%d,%d,%t), want (%d,%d,%d,%t)",
					tc.x, tc.y, tc.n, skip, start, count, ok, tc.skip, tc.start, tc.count, tc.ok)
			}
		})
	}
}

// TestDecodersSurviveHostileInput feeds each decoder deterministic garbage and
// truncated payloads. The contract is narrow but absolute: a decoder may
// return an error, but it must never panic and must never write outside the
// framebuffer. Rectangle geometry comes straight off the wire, so this is the
// path a malicious server would take.
func TestDecodersSurviveHostileInput(t *testing.T) {
	rects := []Rect{
		{0, 0, 16, 16},
		{0, 0, 1, 1},
		{0, 0, 0, 0},
		{14, 14, 64, 64},     // straddles the edge
		{9000, 9000, 64, 64}, // entirely off screen
		{65535, 65535, 64, 64},
		{0, 0, 4096, 4096}, // absurdly large
	}
	ctors := map[string]func() Decoder{
		"hextile": NewHextileDecoder,
		"zlib":    NewZlibDecoder,
		"tight":   NewTightDecoder,
		"zrle":    NewZRLEDecoder,
	}
	for name, ctor := range ctors {
		for i, r := range rects {
			for _, size := range []int{0, 1, 7, 64, 4096} {
				payload := pseudoRandom(size, uint32(i*31+size))
				t.Run(name, func(t *testing.T) {
					c := newTestConn(t, 16, 16, PreferredPixelFormat, payload)
					// A panic here fails the test; an error is fine.
					_ = ctor().Decode(c, r)
					// The framebuffer must still be intact and the right size.
					fb := c.framebuffer()
					if len(fb.Pix) != fb.Stride*fb.Height {
						t.Fatalf("framebuffer corrupted: len(Pix)=%d, stride=%d, height=%d",
							len(fb.Pix), fb.Stride, fb.Height)
					}
				})
			}
		}
	}
}

func TestClipRectRejectsOverflowingGeometry(t *testing.T) {
	fb := NewFramebuffer(10, 10)
	tests := []struct {
		name       string
		x, y, w, h int
		want       Rect
	}{
		{"fully inside", 1, 2, 3, 4, Rect{1, 2, 3, 4}},
		{"clipped right", 8, 0, 5, 2, Rect{8, 0, 2, 2}},
		{"clipped bottom", 0, 9, 2, 5, Rect{0, 9, 2, 1}},
		{"entirely right", 10, 0, 4, 4, Rect{}},
		{"entirely below", 0, 10, 4, 4, Rect{}},
		// A tile offset added to a near-uint16-max origin must not wrap into
		// a valid-looking rectangle.
		{"would overflow uint16", 65530, 65530, 16, 16, Rect{}},
		{"zero width", 1, 1, 0, 4, Rect{}},
		{"negative origin", -2, -2, 5, 5, Rect{0, 0, 3, 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clipRect(fb, tc.x, tc.y, tc.w, tc.h); got != tc.want {
				t.Fatalf("clipRect = %v, want %v", got, tc.want)
			}
		})
	}
}

// rectHeader builds the 12-byte per-rectangle header of a FramebufferUpdate.
func rectHeader(r Rect, enc Encoding) []byte {
	s := &sb{}
	s.u16(r.X).u16(r.Y).u16(r.Width).u16(r.Height)
	s.u32(uint32(enc))
	return s.bytes()
}

// TestInterleavedEncodingsThroughRunLoop drives all four new decoders through
// the real message loop, in one update, back to back.
//
// This is the arrangement that actually breaks a decoder with an off-by-one in
// its payload accounting: on its own each decoder looks fine, but the moment
// another rectangle follows, a byte of drift turns the next rectangle header
// into nonsense and the connection dies with "unknown server message type".
func TestInterleavedEncodingsThroughRunLoop(t *testing.T) {
	pf := PreferredPixelFormat
	zlibEnc := newZlibBlocks()
	zrleEnc := newZlibBlocks()
	tightEnc := newZlibBlocks()

	s := &sb{}
	s.u8(msgFramebufferUpdate, 0) // message type, padding
	s.u16(4)                      // four rectangles

	// 1. Hextile: solid background over 8x8 at (0,0).
	s.raw(rectHeader(Rect{0, 0, 8, 8}, EncodingHextile))
	s.u8(hextileBackgroundSpecified)
	s.raw(wirePixel(pf, 1, 2, 3))

	// 2. Zlib: 8x8 raw pixels at (8,0).
	s.raw(rectHeader(Rect{8, 0, 8, 8}, EncodingZlib))
	s.raw(zlibEnc.block(rawPixels(pf, 8, 8, func(x, y int) (uint32, uint32, uint32) {
		return 4, 5, 6
	})))

	// 3. ZRLE: one solid tile over 8x8 at (0,8).
	s.raw(rectHeader(Rect{0, 8, 8, 8}, EncodingZRLE))
	zrleTileData := &sb{}
	zrleTileData.u8(1)
	zrleTileData.raw(tPixel(pf, 7, 8, 9))
	s.raw(zrleEnc.block(zrleTileData.bytes()))

	// 4. Tight: basic copy over 8x8 at (8,8).
	s.raw(rectHeader(Rect{8, 8, 8, 8}, EncodingTight))
	tightData := &sb{}
	tightData.rep(64, tPixel(pf, 10, 11, 12))
	s.u8(tightCtl(0, 0))
	s.raw(tightBlock(tightEnc, tightData.bytes()))

	var updates int
	var damage []Rect
	stream := append(serverHandshake(16, 16, pf, "test"), s.bytes()...)
	c, err := NewConn(&scriptedRW{r: bytes.NewReader(stream)}, Config{
		PixelFormat: &pf,
		OnFramebufferUpdate: func(_ *Framebuffer, d []Rect) {
			updates++
			damage = append(damage, d...)
		},
	})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	// Run returns nil at a clean EOF, which is what a fully consumed stream
	// looks like. Any decoder that over- or under-read would instead fail here
	// with an unknown message type or a short read.
	if err := c.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if updates != 1 {
		t.Fatalf("got %d updates, want 1", updates)
	}
	// Resize seeds a full-screen damage rectangle at handshake, so the four
	// rectangles of this update follow it.
	want := []Rect{
		{0, 0, 16, 16},
		{0, 0, 8, 8}, {8, 0, 8, 8}, {0, 8, 8, 8}, {8, 8, 8, 8},
	}
	if len(damage) != len(want) {
		t.Fatalf("got %d damage rectangles %v, want %d", len(damage), damage, len(want))
	}
	for i := range want {
		if damage[i] != want[i] {
			t.Errorf("damage[%d] = %v, want %v", i, damage[i], want[i])
		}
	}

	c.WithFramebuffer(func(fb *Framebuffer) {
		assertUniform(t, fb, 0, 0, 8, 8, wireColor(pf, 1, 2, 3))
		assertUniform(t, fb, 8, 0, 8, 8, wireColor(pf, 4, 5, 6))
		assertUniform(t, fb, 0, 8, 8, 8, wireColor(pf, 7, 8, 9))
		assertUniform(t, fb, 8, 8, 8, 8, wireColor(pf, 10, 11, 12))
	})

	stats := c.Stats()
	if stats.Rects != 4 {
		t.Errorf("Stats().Rects = %d, want 4", stats.Rects)
	}
	for _, enc := range []Encoding{EncodingHextile, EncodingZlib, EncodingZRLE, EncodingTight} {
		if stats.RectsByEncoding[enc] != 1 {
			t.Errorf("Stats().RectsByEncoding[%v] = %d, want 1", enc, stats.RectsByEncoding[enc])
		}
	}
	// Note: Stats().BytesByEncoding is not asserted here. It is derived from
	// the countingReader, which sits under a 64KiB bufio.Reader, so in a test
	// the whole stream is pulled in one Read and every per-rectangle delta is
	// zero. That is a property of conn.go's accounting, not of the decoders.
}

// TestBlockSourcePreservesLeftovers checks the retention rule the persistent
// zlib streams depend on: bytes the inflater has not consumed from one block
// must still be there when the next block is pushed.
func TestBlockSourcePreservesLeftovers(t *testing.T) {
	var s blockSource
	s.push([]byte{1, 2, 3, 4})
	got := make([]byte, 2)
	if _, err := io.ReadFull(&s, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, []byte{1, 2}) {
		t.Fatalf("first read = %x", got)
	}
	s.push([]byte{5, 6})
	rest, err := io.ReadAll(&s)
	if err != nil {
		t.Fatalf("read rest: %v", err)
	}
	if !bytes.Equal(rest, []byte{3, 4, 5, 6}) {
		t.Fatalf("leftovers lost: got %x, want 03040506", rest)
	}
	if _, err := s.ReadByte(); err != io.EOF {
		t.Fatalf("drained source returned %v, want io.EOF", err)
	}
}
