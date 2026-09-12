package rfb

import (
	"image"
	"image/color"
)

// Framebuffer is the client-side copy of the remote screen.
//
// Pixels are stored in RGBA byte order (R,G,B,A) with a fixed alpha of 255,
// which is both Go's image.RGBA layout and what a GPU texture upload wants, so
// the viewer can hand Pix straight to SDL with no conversion pass.
//
// A Framebuffer is not safe for concurrent use. The connection's read loop owns
// it; the renderer should snapshot or lock around it (see Conn.WithFramebuffer).
type Framebuffer struct {
	// Pix holds the pixels, len(Pix) == Stride*Height.
	Pix []byte
	// Stride is the byte distance between vertically adjacent pixels.
	Stride int
	// Width and Height are the framebuffer dimensions in pixels.
	Width, Height int

	// damage accumulates the regions changed since the last call to
	// TakeDamage. Tracking damage lets the viewer upload only the rectangles
	// that actually changed, which is the single biggest win when blitting a
	// 1080p surface at 30-60fps.
	damage []Rect
}

// NewFramebuffer allocates a framebuffer of the given size.
func NewFramebuffer(width, height int) *Framebuffer {
	fb := &Framebuffer{}
	fb.Resize(width, height)
	return fb
}

// Resize reallocates the framebuffer. Existing contents are not preserved; the
// caller is expected to request a full, non-incremental update afterwards.
func (fb *Framebuffer) Resize(width, height int) {
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	fb.Width = width
	fb.Height = height
	fb.Stride = width * 4
	need := fb.Stride * height
	if cap(fb.Pix) >= need {
		fb.Pix = fb.Pix[:need]
	} else {
		fb.Pix = make([]byte, need)
	}
	// Start fully opaque so decoders that only write RGB still produce a
	// visible image.
	for i := 3; i < len(fb.Pix); i += 4 {
		fb.Pix[i] = 0xff
	}
	fb.damage = fb.damage[:0]
	if width > 0 && height > 0 {
		fb.damage = append(fb.damage, Rect{0, 0, uint16(width), uint16(height)})
	}
}

// Bounds implements part of image.Image.
func (fb *Framebuffer) Bounds() image.Rectangle {
	return image.Rect(0, 0, fb.Width, fb.Height)
}

// ColorModel implements part of image.Image.
func (fb *Framebuffer) ColorModel() color.Model { return color.RGBAModel }

// At implements image.Image so a framebuffer can be passed straight to
// image/png for screenshots and tests.
func (fb *Framebuffer) At(x, y int) color.Color {
	if x < 0 || y < 0 || x >= fb.Width || y >= fb.Height {
		return color.RGBA{}
	}
	i := y*fb.Stride + x*4
	return color.RGBA{R: fb.Pix[i], G: fb.Pix[i+1], B: fb.Pix[i+2], A: 0xff}
}

// RGBA returns an image.RGBA sharing fb's pixel memory. Mutating one mutates
// the other; it is a view, not a copy.
func (fb *Framebuffer) RGBA() *image.RGBA {
	return &image.RGBA{Pix: fb.Pix, Stride: fb.Stride, Rect: fb.Bounds()}
}

// Clone returns an independent copy, used to hand a stable snapshot to a
// renderer running on another goroutine.
func (fb *Framebuffer) Clone() *Framebuffer {
	out := &Framebuffer{
		Pix:    make([]byte, len(fb.Pix)),
		Stride: fb.Stride,
		Width:  fb.Width,
		Height: fb.Height,
	}
	copy(out.Pix, fb.Pix)
	return out
}

// Clip reduces r to the part that lies inside the framebuffer. A rectangle that
// falls entirely outside comes back empty. Every decoder must clip before
// writing: a malicious or buggy server must not be able to index out of bounds.
func (fb *Framebuffer) Clip(r Rect) Rect {
	if int(r.X) >= fb.Width || int(r.Y) >= fb.Height {
		return Rect{}
	}
	if int(r.X)+int(r.Width) > fb.Width {
		r.Width = uint16(fb.Width - int(r.X))
	}
	if int(r.Y)+int(r.Height) > fb.Height {
		r.Height = uint16(fb.Height - int(r.Y))
	}
	return r
}

// Row returns a slice of the pixel bytes for n pixels starting at (x,y). The
// slice aliases the framebuffer, so decoders can write straight into it.
// It returns nil if the span is out of bounds.
func (fb *Framebuffer) Row(x, y, n int) []byte {
	if x < 0 || y < 0 || n < 0 || y >= fb.Height || x+n > fb.Width {
		return nil
	}
	i := y*fb.Stride + x*4
	return fb.Pix[i : i+n*4 : i+n*4]
}

// SetRGBA writes one pixel without bounds checking beyond the framebuffer
// dimensions. Out-of-range coordinates are ignored.
func (fb *Framebuffer) SetRGBA(x, y int, c color.RGBA) {
	if x < 0 || y < 0 || x >= fb.Width || y >= fb.Height {
		return
	}
	i := y*fb.Stride + x*4
	fb.Pix[i] = c.R
	fb.Pix[i+1] = c.G
	fb.Pix[i+2] = c.B
	fb.Pix[i+3] = 0xff
}

// FillRect paints a solid rectangle. r must already be clipped.
func (fb *Framebuffer) FillRect(r Rect, c color.RGBA) {
	if r.Empty() {
		return
	}
	first := fb.Row(int(r.X), int(r.Y), int(r.Width))
	if first == nil {
		return
	}
	for i := 0; i < len(first); i += 4 {
		first[i] = c.R
		first[i+1] = c.G
		first[i+2] = c.B
		first[i+3] = 0xff
	}
	// Subsequent rows are memcpy of the first, which the runtime turns into a
	// wide copy — much faster than per-pixel stores.
	for y := 1; y < int(r.Height); y++ {
		dst := fb.Row(int(r.X), int(r.Y)+y, int(r.Width))
		if dst == nil {
			return
		}
		copy(dst, first)
	}
}

// CopyRect implements the CopyRect encoding: move a block of pixels already
// present in the framebuffer to a new location. Overlapping regions are handled
// by choosing a row order that cannot read already-overwritten pixels.
func (fb *Framebuffer) CopyRect(dst Rect, srcX, srcY int) {
	dst = fb.Clip(dst)
	if dst.Empty() {
		return
	}
	w, h := int(dst.Width), int(dst.Height)
	if srcX < 0 || srcY < 0 || srcX+w > fb.Width || srcY+h > fb.Height {
		return
	}
	if srcY < int(dst.Y) {
		// Moving down: copy bottom-up.
		for y := h - 1; y >= 0; y-- {
			copy(fb.Row(int(dst.X), int(dst.Y)+y, w), fb.Row(srcX, srcY+y, w))
		}
		return
	}
	for y := 0; y < h; y++ {
		copy(fb.Row(int(dst.X), int(dst.Y)+y, w), fb.Row(srcX, srcY+y, w))
	}
}

// MarkDamaged records that r changed and should be re-uploaded by the renderer.
func (fb *Framebuffer) MarkDamaged(r Rect) {
	r = fb.Clip(r)
	if r.Empty() {
		return
	}
	fb.damage = append(fb.damage, r)
}

// TakeDamage returns the accumulated damage rectangles and resets the list.
// The returned slice is only valid until the next call.
func (fb *Framebuffer) TakeDamage() []Rect {
	d := fb.damage
	fb.damage = make([]Rect, 0, cap(d))
	return d
}

// DamageBounds returns a single rectangle covering all accumulated damage,
// which is what a renderer wants when it would rather do one upload than many.
func (fb *Framebuffer) DamageBounds() Rect {
	if len(fb.damage) == 0 {
		return Rect{}
	}
	minX, minY := int(fb.damage[0].X), int(fb.damage[0].Y)
	maxX, maxY := minX+int(fb.damage[0].Width), minY+int(fb.damage[0].Height)
	for _, r := range fb.damage[1:] {
		if int(r.X) < minX {
			minX = int(r.X)
		}
		if int(r.Y) < minY {
			minY = int(r.Y)
		}
		if int(r.X)+int(r.Width) > maxX {
			maxX = int(r.X) + int(r.Width)
		}
		if int(r.Y)+int(r.Height) > maxY {
			maxY = int(r.Y) + int(r.Height)
		}
	}
	return Rect{X: uint16(minX), Y: uint16(minY), Width: uint16(maxX - minX), Height: uint16(maxY - minY)}
}
