// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package terminal

import (
	"image"
	"image/color"

	"github.com/kube-workspaces/desktop-client/internal/ui"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// visage is everything the renderer needs to decide whether a cell changed.
// It deliberately includes the cursor: a cursor that moved into a cell
// changes that cell's picture as much as a letter landing there does.
type visage struct {
	r         rune
	fg, bg    color.RGBA
	wide      bool
	invisible bool
	cursor    bool
}

// renderer paints the emulator's grid into one RGBA image and tracks which
// cells changed since the last frame.
//
// A session redraws far more often than it changes: the shell writes a cursor
// move and the whole screen would repaint if the renderer did not remember
// the previous frame. It keeps a shadow grid of visages instead, so a frame
// repaints exactly the cells whose picture changed and returns that rectangle
// for a partial texture upload.
type renderer struct {
	scale      int
	cellW      int // cell width in pixels
	cellH      int // cell height in pixels
	cols, rows int // grid size in cells
	img        *image.RGBA
	canvas     *ui.Canvas
	shadow     [][]visage
}

// newRenderer returns a renderer for a cols by rows grid at the given integer
// scale, using the bitmap monospace face for the letters. A terminal must
// stay a grid: proportional faces would break columns, so the grid never
// follows the theme's proportional typeface — only its scale.
func newRenderer(cols, rows, scale int) *renderer {
	if scale < 1 {
		scale = 1
	}
	r := &renderer{
		scale:  scale,
		cellW:  ui.GlyphAdvance * scale,
		cellH:  ui.LineAdvance * scale,
		cols:   cols,
		rows:   rows,
		shadow: make([][]visage, rows),
	}
	r.image()
	return r
}

// image rebuilds the backing image and canvas for the current grid size. A
// nil shadow row means "never drawn", which is how every cell counts as dirty.
func (r *renderer) image() {
	r.img = image.NewRGBA(image.Rect(0, 0, r.cols*r.cellW, r.rows*r.cellH))
	r.canvas = ui.NewCanvas(r.img)
	for y := 0; y < r.rows; y++ {
		r.shadow[y] = nil
	}
}

// resize changes the grid size. The emulator must already have been resized;
// this reallocates the image, so the whole grid counts as dirty.
func (r *renderer) resize(cols, rows int) {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	r.cols, r.rows = cols, rows
	r.shadow = make([][]visage, rows)
	r.image()
}

// Size returns the pixel size of the grid image, for texture allocation and
// letterboxing.
func (r *renderer) Size() (w, h int) {
	return r.cols * r.cellW, r.rows * r.cellH
}

// Image returns the grid image; the window uploads it (or its dirty rect) to
// the backend texture.
func (r *renderer) Image() *image.RGBA { return r.img }

// frame repaints the cells whose visage changed since the last frame and
// returns the pixel rectangle those cells cover. It reports false when the
// grid did not change and nothing needs uploading.
//
// The cursor is part of a cell's visage, so moving the cursor repaints both
// the cell it left and the one it entered, with no extra bookkeeping.
func (r *renderer) frame(e *Emulator) (viewer.Rect, bool) {
	cx, cy, cok := e.Cursor()
	dirty := viewer.Rect{}
	changed := false

	for y := 0; y < r.rows; y++ {
		row := r.shadow[y]
		if row == nil {
			row = make([]visage, r.cols)
			r.shadow[y] = row
		}
		for x := 0; x < r.cols; x++ {
			v := visageOf(e.Cell(x, y), cok && x == cx && y == cy)
			if row[x] == v {
				continue
			}
			row[x] = v
			r.draw(x, y, v)
			changed = true
			dirty = union(dirty, viewer.Rect{X: x, Y: y, W: 1, H: 1})
		}
	}
	if !changed {
		return viewer.Rect{}, false
	}
	// The union is in cell units; scale it to the image's pixels.
	return viewer.Rect{
		X: dirty.X * r.cellW,
		Y: dirty.Y * r.cellH,
		W: dirty.W * r.cellW,
		H: dirty.H * r.cellH,
	}, true
}

// visageOf derives a cell's picture from its resolved attributes plus whether
// the cursor sits on it.
func visageOf(c Cell, cursor bool) visage {
	return visage{
		r:         c.Rune,
		fg:        c.Fg,
		bg:        c.Bg,
		wide:      c.Wide,
		invisible: c.Invisible,
		cursor:    cursor,
	}
}

// draw paints one cell into the image.
func (r *renderer) draw(x, y int, v visage) {
	fg, bg := v.fg, v.bg
	if v.cursor {
		// The classic block cursor: the cell fills with the foreground colour
		// and the letter is punched out in the background colour, so the
		// cursor sits exactly where the next character will land.
		fg, bg = bg, fg
	}
	px, py := x*r.cellW, y*r.cellH
	r.canvas.Fill(viewer.Rect{X: px, Y: py, W: r.cellW, H: r.cellH}, bg)
	if v.invisible || v.r == 0 || v.r == ' ' {
		return
	}
	r.canvas.Text(string(v.r), px, py, r.scale, fg)
}

// union returns the smallest rectangle containing a and b. An empty a (or b)
// contributes nothing.
func union(a, b viewer.Rect) viewer.Rect {
	if a.Empty() {
		return b
	}
	if b.Empty() {
		return a
	}
	x1 := min(a.X, b.X)
	y1 := min(a.Y, b.Y)
	x2 := max(a.X+a.W, b.X+b.W)
	y2 := max(a.Y+a.H, b.Y+b.H)
	return viewer.Rect{X: x1, Y: y1, W: x2 - x1, H: y2 - y1}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
