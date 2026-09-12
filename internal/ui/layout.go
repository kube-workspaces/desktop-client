// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

// Layout here is deliberately arithmetic rather than a constraint solver.
// The shell's screens are columns of full-width controls with the occasional
// row of buttons, and a rectangle-cutting vocabulary describes that exactly,
// reads in source order, and needs no second pass.

// Inset shrinks r by d pixels on every side. A d large enough to invert the
// rectangle yields an empty one rather than a negative one.
func Inset(r Rect, d int) Rect { return InsetXY(r, d, d) }

// InsetXY shrinks r by dx on the left and right and dy on the top and bottom.
func InsetXY(r Rect, dx, dy int) Rect {
	out := Rect{X: r.X + dx, Y: r.Y + dy, W: r.W - 2*dx, H: r.H - 2*dy}
	if out.W < 0 {
		out.W = 0
	}
	if out.H < 0 {
		out.H = 0
	}
	return out
}

// CutTop splits h pixels off the top of r, returning that strip and what is
// left. A request larger than r yields the whole of r and an empty remainder.
func CutTop(r Rect, h int) (top, rest Rect) {
	h = clampInt(h, 0, r.H)
	return Rect{X: r.X, Y: r.Y, W: r.W, H: h},
		Rect{X: r.X, Y: r.Y + h, W: r.W, H: r.H - h}
}

// CutBottom splits h pixels off the bottom of r.
func CutBottom(r Rect, h int) (bottom, rest Rect) {
	h = clampInt(h, 0, r.H)
	return Rect{X: r.X, Y: r.Y + r.H - h, W: r.W, H: h},
		Rect{X: r.X, Y: r.Y, W: r.W, H: r.H - h}
}

// CutLeft splits w pixels off the left of r.
func CutLeft(r Rect, w int) (left, rest Rect) {
	w = clampInt(w, 0, r.W)
	return Rect{X: r.X, Y: r.Y, W: w, H: r.H},
		Rect{X: r.X + w, Y: r.Y, W: r.W - w, H: r.H}
}

// CutRight splits w pixels off the right of r.
func CutRight(r Rect, w int) (right, rest Rect) {
	w = clampInt(w, 0, r.W)
	return Rect{X: r.X + r.W - w, Y: r.Y, W: w, H: r.H},
		Rect{X: r.X, Y: r.Y, W: r.W - w, H: r.H}
}

// CenterRect returns a w by h rectangle centred inside outer. It is allowed to
// be larger than outer, which is what a fixed-width login card does in a
// window the user has made very narrow; the caller clips.
func CenterRect(outer Rect, w, h int) Rect {
	return Rect{
		X: outer.X + (outer.W-w)/2,
		Y: outer.Y + (outer.H-h)/2,
		W: w,
		H: h,
	}
}

// CutRightGap trims w pixels off the right of r without returning them, for
// putting space between two columns that are both right-aligned.
func CutRightGap(r Rect, w int) Rect {
	_, rest := CutRight(r, w)
	return rest
}

// Intersect returns the overlap of a and b, or an empty rectangle.
func Intersect(a, b Rect) Rect {
	x0, y0 := max(a.X, b.X), max(a.Y, b.Y)
	x1, y1 := min(a.X+a.W, b.X+b.W), min(a.Y+a.H, b.Y+b.H)
	if x1 <= x0 || y1 <= y0 {
		return Rect{}
	}
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// Stack lays out a column of rows down a rectangle, inserting a fixed gap
// between them.
//
// It is a cursor, not a container: it neither draws nor remembers what was put
// in it, so a screen can mix stacked rows with hand-placed rectangles without
// having to opt out of a layout system.
type Stack struct {
	rect Rect
	y    int
	gap  int
	// first suppresses the gap before the first row, so a stack of n rows has
	// n-1 gaps rather than n.
	first bool
}

// NewStack returns a stack filling r, separating rows by gap pixels.
func NewStack(r Rect, gap int) *Stack {
	return &Stack{rect: r, y: r.Y, gap: gap, first: true}
}

// Next returns the next row of height h and advances the cursor past it.
func (s *Stack) Next(h int) Rect {
	if !s.first {
		s.y += s.gap
	}
	s.first = false
	row := Rect{X: s.rect.X, Y: s.y, W: s.rect.W, H: h}
	s.y += h
	return row
}

// Skip advances the cursor by h pixels without producing a row, and without
// the automatic gap. It is for deliberate whitespace.
func (s *Stack) Skip(h int) { s.y += h }

// Rest returns everything below the cursor, with the automatic gap applied.
// The cursor is left at the bottom, so calling Rest twice is a mistake the
// second call makes visible by returning an empty rectangle.
func (s *Stack) Rest() Rect {
	if !s.first {
		s.y += s.gap
	}
	s.first = false
	rest := Rect{X: s.rect.X, Y: s.y, W: s.rect.W, H: s.rect.Y + s.rect.H - s.y}
	if rest.H < 0 {
		rest.H = 0
	}
	s.y = s.rect.Y + s.rect.H
	return rest
}

// Height returns how far the cursor has travelled from the top of the stack,
// which is how tall the content laid out so far is.
func (s *Stack) Height() int { return s.y - s.rect.Y }

// Row splits r into len(widths) columns separated by gap pixels.
//
// A width of zero means "share whatever is left equally with the other zeroes",
// which covers both a row of equal buttons and a row of one fixed-width label
// beside a field that should take the rest.
func Row(r Rect, gap int, widths ...int) []Rect {
	if len(widths) == 0 {
		return nil
	}
	fixed, flexible := 0, 0
	for _, w := range widths {
		if w > 0 {
			fixed += w
		} else {
			flexible++
		}
	}
	free := r.W - fixed - gap*(len(widths)-1)
	if free < 0 {
		free = 0
	}
	share, remainder := 0, 0
	if flexible > 0 {
		share = free / flexible
		remainder = free % flexible
	}

	out := make([]Rect, 0, len(widths))
	x := r.X
	for _, w := range widths {
		if w <= 0 {
			w = share
			// Spread the division remainder over the leading flexible columns
			// so the row ends exactly on r's right edge.
			if remainder > 0 {
				w++
				remainder--
			}
		}
		out = append(out, Rect{X: x, Y: r.Y, W: w, H: r.H})
		x += w + gap
	}
	return out
}
