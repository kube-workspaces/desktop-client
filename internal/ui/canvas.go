// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"image"
	"image/color"
	"math"

	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// Canvas is a software drawing surface: an [image.RGBA] plus a clipping stack.
//
// Every primitive is clipped and bounds-checked, so a widget handed a
// rectangle bigger than the window, or a negative one, draws nothing rather
// than corrupting the buffer. That is not defensiveness for its own sake: the
// rectangles come from layout arithmetic on a window the user is free to
// resize to twenty pixels wide.
//
// A Canvas is not safe for concurrent use, and the shell draws from one
// goroutine.
type Canvas struct {
	img *image.RGBA
	// clip is the current clipping rectangle in image coordinates, always
	// inside the image's own bounds.
	clip  image.Rectangle
	stack []image.Rectangle
}

// NewCanvas returns a canvas drawing into img.
//
// The image is used as-is; the canvas takes no copy and does not clear it. The
// image's origin may be non-zero, and all coordinates passed to the canvas are
// relative to it.
func NewCanvas(img *image.RGBA) *Canvas {
	return &Canvas{img: img, clip: img.Bounds()}
}

// Image returns the underlying image.
func (c *Canvas) Image() *image.RGBA { return c.img }

// Bounds returns the whole drawable area, ignoring the clip.
func (c *Canvas) Bounds() Rect {
	b := c.img.Bounds()
	return Rect{X: 0, Y: 0, W: b.Dx(), H: b.Dy()}
}

// Clip returns the current clipping rectangle.
func (c *Canvas) Clip() Rect {
	origin := c.img.Bounds().Min
	return Rect{
		X: c.clip.Min.X - origin.X,
		Y: c.clip.Min.Y - origin.Y,
		W: c.clip.Dx(),
		H: c.clip.Dy(),
	}
}

// PushClip intersects the clipping rectangle with r and remembers the previous
// one. Every PushClip must be matched by a [Canvas.PopClip].
func (c *Canvas) PushClip(r Rect) {
	c.stack = append(c.stack, c.clip)
	c.clip = c.clip.Intersect(c.rectToImage(r))
}

// PopClip restores the clipping rectangle saved by the matching PushClip. It
// is a no-op when the stack is empty, so an unbalanced widget cannot panic the
// shell mid-frame.
func (c *Canvas) PopClip() {
	if n := len(c.stack); n > 0 {
		c.clip = c.stack[n-1]
		c.stack = c.stack[:n-1]
	}
}

// Fill paints r with col, composited over what is already there.
//
// Colours are straight (non-premultiplied) alpha, which is what a human writes
// in a palette. The destination is kept fully opaque — the shell's canvas is
// uploaded to a texture and shown directly — so source-over with a straight
// alpha source and an opaque destination is exact.
func (c *Canvas) Fill(r Rect, col color.RGBA) {
	if col.A == 0 {
		return
	}
	box := c.clip.Intersect(c.rectToImage(r))
	if box.Empty() {
		return
	}
	for y := box.Min.Y; y < box.Max.Y; y++ {
		row := c.img.PixOffset(box.Min.X, y)
		for x := box.Min.X; x < box.Max.X; x++ {
			c.blend(row, col, col.A)
			row += 4
		}
	}
}

// FillRounded paints r with col, rounding the corners by radius.
//
// The corners are antialiased. At the radii this interface uses (six pixels)
// an aliased corner is the difference between "software-rendered" and "badly
// software-rendered", and the coverage calculation only runs on the corner
// squares, so it costs four small loops per rectangle.
func (c *Canvas) FillRounded(r Rect, radius int, col color.RGBA) {
	if col.A == 0 || r.Empty() {
		return
	}
	radius = clampRadius(radius, r)
	if radius <= 0 {
		c.Fill(r, col)
		return
	}
	// The band between the two corner rows is a plain rectangle.
	c.Fill(Rect{X: r.X, Y: r.Y + radius, W: r.W, H: r.H - 2*radius}, col)

	fr := float64(radius)
	for i := 0; i < radius; i++ {
		// The straight middle of each cap row.
		c.Fill(Rect{X: r.X + radius, Y: r.Y + i, W: r.W - 2*radius, H: 1}, col)
		c.Fill(Rect{X: r.X + radius, Y: r.Y + r.H - 1 - i, W: r.W - 2*radius, H: 1}, col)

		for j := 0; j < radius; j++ {
			cov := cornerCoverage(float64(j)+0.5, float64(i)+0.5, fr)
			if cov <= 0 {
				continue
			}
			a := scaleAlpha(col.A, cov)
			c.plot(r.X+j, r.Y+i, col, a)
			c.plot(r.X+r.W-1-j, r.Y+i, col, a)
			c.plot(r.X+j, r.Y+r.H-1-i, col, a)
			c.plot(r.X+r.W-1-j, r.Y+r.H-1-i, col, a)
		}
	}
}

// StrokeRounded outlines r with a border of the given width, drawn inside the
// rectangle so that a focus ring never overlaps its neighbour.
func (c *Canvas) StrokeRounded(r Rect, radius, width int, col color.RGBA) {
	if col.A == 0 || r.Empty() || width <= 0 {
		return
	}
	radius = clampRadius(radius, r)
	if radius <= 0 {
		c.strokeSquare(r, width, col)
		return
	}
	inner := Inset(r, width)
	innerRadius := radius - width
	if innerRadius < 0 {
		innerRadius = 0
	}

	// Only the corner squares need the general ring calculation; the four
	// straight edges are plain rectangles.
	c.Fill(Rect{X: r.X + radius, Y: r.Y, W: r.W - 2*radius, H: width}, col)
	c.Fill(Rect{X: r.X + radius, Y: r.Y + r.H - width, W: r.W - 2*radius, H: width}, col)
	c.Fill(Rect{X: r.X, Y: r.Y + radius, W: width, H: r.H - 2*radius}, col)
	c.Fill(Rect{X: r.X + r.W - width, Y: r.Y + radius, W: width, H: r.H - 2*radius}, col)

	fr, fi := float64(radius), float64(innerRadius)
	for i := 0; i < radius; i++ {
		for j := 0; j < radius; j++ {
			fx, fy := float64(j)+0.5, float64(i)+0.5
			cov := cornerCoverage(fx, fy, fr)
			if cov <= 0 {
				continue
			}
			// Subtract the inner disc's coverage to leave the ring. The inner
			// corner's centre is offset by the border width on both axes.
			if inner.W > 0 && inner.H > 0 {
				cov -= cornerCoverage(fx-float64(width), fy-float64(width), fi)
			}
			if cov <= 0 {
				continue
			}
			a := scaleAlpha(col.A, cov)
			c.plot(r.X+j, r.Y+i, col, a)
			c.plot(r.X+r.W-1-j, r.Y+i, col, a)
			c.plot(r.X+j, r.Y+r.H-1-i, col, a)
			c.plot(r.X+r.W-1-j, r.Y+r.H-1-i, col, a)
		}
	}
}

// strokeSquare outlines a rectangle with no corner radius.
func (c *Canvas) strokeSquare(r Rect, width int, col color.RGBA) {
	c.Fill(Rect{X: r.X, Y: r.Y, W: r.W, H: width}, col)
	c.Fill(Rect{X: r.X, Y: r.Y + r.H - width, W: r.W, H: width}, col)
	c.Fill(Rect{X: r.X, Y: r.Y + width, W: width, H: r.H - 2*width}, col)
	c.Fill(Rect{X: r.X + r.W - width, Y: r.Y + width, W: width, H: r.H - 2*width}, col)
}

// Line draws a line from (x0, y0) to (x1, y1) with the given thickness.
//
// Horizontal and vertical lines — which is every line this interface actually
// draws — take an exact rectangle path, so a one-pixel separator is one pixel
// and not a row of blended approximations.
func (c *Canvas) Line(x0, y0, x1, y1, width int, col color.RGBA) {
	if width <= 0 || col.A == 0 {
		return
	}
	switch {
	case y0 == y1:
		x, w := minMaxSpan(x0, x1)
		c.Fill(Rect{X: x, Y: y0, W: w, H: width}, col)
		return
	case x0 == x1:
		y, h := minMaxSpan(y0, y1)
		c.Fill(Rect{X: x0, Y: y, W: width, H: h}, col)
		return
	}

	// Bresenham with a square brush. Diagonals are rare enough here (nothing
	// but decoration) that a proper antialiased line would be ceremony.
	dx, sx := abs(x1-x0), sign(x1-x0)
	dy, sy := -abs(y1-y0), sign(y1-y0)
	err := dx + dy
	for {
		c.Fill(Rect{X: x0, Y: y0, W: width, H: width}, col)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

// Text draws s with its top-left corner at (x, y), each glyph pixel expanded
// to a scale by scale block. It returns the width drawn, so a caller can lay
// out a run of differently styled fragments.
//
// Runes the font does not cover are drawn as a hollow box rather than skipped:
// this renders workspace names and server error strings, and text that
// silently loses characters is worse than text that visibly cannot show them.
func (c *Canvas) Text(s string, x, y, scale int, col color.RGBA) int {
	if scale <= 0 || col.A == 0 {
		return 0
	}
	start := x
	for _, r := range viewer.FoldToFont(s) {
		g := viewer.GlyphFor(r)
		for row := 0; row < GlyphHeight; row++ {
			bits := g[row]
			if bits == 0 {
				continue
			}
			for col0 := 0; col0 < GlyphWidth; col0++ {
				if bits&(1<<(GlyphWidth-1-col0)) == 0 {
					continue
				}
				c.Fill(Rect{
					X: x + col0*scale,
					Y: y + row*scale,
					W: scale,
					H: scale,
				}, col)
			}
		}
		x += GlyphAdvance * scale
	}
	if x == start {
		return 0
	}
	return x - start - (GlyphAdvance-GlyphWidth)*scale
}

// plot blends a single pixel, clipped.
func (c *Canvas) plot(x, y int, col color.RGBA, alpha uint8) {
	if alpha == 0 {
		return
	}
	ix, iy := x+c.img.Bounds().Min.X, y+c.img.Bounds().Min.Y
	if ix < c.clip.Min.X || ix >= c.clip.Max.X || iy < c.clip.Min.Y || iy >= c.clip.Max.Y {
		return
	}
	c.blend(c.img.PixOffset(ix, iy), col, alpha)
}

// blend composites col at alpha over the pixel starting at byte offset i.
func (c *Canvas) blend(i int, col color.RGBA, alpha uint8) {
	pix := c.img.Pix
	if alpha == 255 {
		pix[i], pix[i+1], pix[i+2], pix[i+3] = col.R, col.G, col.B, 255
		return
	}
	a := uint32(alpha)
	inv := 255 - a
	pix[i] = uint8((uint32(col.R)*a + uint32(pix[i])*inv + 127) / 255)
	pix[i+1] = uint8((uint32(col.G)*a + uint32(pix[i+1])*inv + 127) / 255)
	pix[i+2] = uint8((uint32(col.B)*a + uint32(pix[i+2])*inv + 127) / 255)
	pix[i+3] = 255
}

// rectToImage converts a canvas rectangle to image coordinates, normalising a
// negative size to an empty rectangle.
func (c *Canvas) rectToImage(r Rect) image.Rectangle {
	if r.W <= 0 || r.H <= 0 {
		return image.Rectangle{}
	}
	origin := c.img.Bounds().Min
	return image.Rect(origin.X+r.X, origin.Y+r.Y, origin.X+r.X+r.W, origin.Y+r.Y+r.H)
}

// cornerCoverage returns how much of the pixel centred at (fx, fy) is inside a
// quarter disc of the given radius centred at (radius, radius).
//
// The half-pixel term is the cheap antialiasing trick: coverage falls linearly
// from 1 to 0 across the one-pixel band straddling the arc, which is visually
// indistinguishable from exact area coverage at these radii and is one
// hypotenuse rather than a supersampled integral.
func cornerCoverage(fx, fy, radius float64) float64 {
	if radius <= 0 {
		return 1
	}
	dx, dy := radius-fx, radius-fy
	if dx < 0 {
		dx = 0
	}
	if dy < 0 {
		dy = 0
	}
	d := math.Hypot(dx, dy)
	cov := radius + 0.5 - d
	switch {
	case cov <= 0:
		return 0
	case cov >= 1:
		return 1
	default:
		return cov
	}
}

// scaleAlpha multiplies an alpha by a coverage in [0, 1].
func scaleAlpha(a uint8, cov float64) uint8 {
	v := float64(a) * cov
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	default:
		return uint8(v + 0.5)
	}
}

// clampRadius bounds a corner radius to something the rectangle can actually
// accommodate: half its shorter side.
func clampRadius(radius int, r Rect) int {
	if radius < 0 {
		return 0
	}
	if half := min(r.W, r.H) / 2; radius > half {
		return half
	}
	return radius
}

func minMaxSpan(a, b int) (start, length int) {
	if a > b {
		a, b = b, a
	}
	return a, b - a + 1
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func sign(v int) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	default:
		return 0
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
