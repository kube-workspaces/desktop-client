// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package terminal

import (
	"image"
	"image/color"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// blankPixels counts the pixels in the region that still equal bg — it is the
// inverse of "a glyph was painted here".
func blankPixels(img *image.RGBA, x0, y0, w, h int, bg color.RGBA) int {
	n := 0
	for y := y0; y < y0+h; y++ {
		for x := x0; x < x0+w; x++ {
			if c := img.RGBAAt(x, y); c == bg {
				n++
			}
		}
	}
	return n
}

func TestFirstFrameIsFullyDirty(t *testing.T) {
	r := newRenderer(4, 1, 1)
	emu := NewEmulator(4, 1)
	dirty, changed := r.frame(emu)
	if !changed {
		t.Fatal("first frame reported unchanged")
	}
	if want := (viewer.Rect{X: 0, Y: 0, W: 4 * r.cellW, H: r.cellH}); dirty != want {
		t.Fatalf("dirty = %s, want %s", dirty, want)
	}
}

func TestUnchangedFrameIsClean(t *testing.T) {
	r := newRenderer(4, 1, 1)
	emu := NewEmulator(4, 1)
	r.frame(emu)
	dirty, changed := r.frame(emu)
	if changed {
		t.Fatalf("unchanged frame = dirty %s", dirty)
	}
	if !dirty.Empty() {
		t.Fatalf("unchanged frame returned non-empty dirty rect %s", dirty)
	}
}

func TestFramePaintsBackground(t *testing.T) {
	r := newRenderer(4, 1, 1)
	emu := NewEmulator(4, 1)
	r.frame(emu)
	bg := emu.Palette().DefaultBg

	// The cursor sits on (0,0), which paints with the swapped colours: the
	// cell looks like a solid block of the default foreground.
	if c := r.img.RGBAAt(3, 5); c != emu.Palette().DefaultFg {
		t.Fatalf("cursor cell pixel = %v, want foreground %v", c, emu.Palette().DefaultFg)
	}
	// A cell the cursor never touched is plain background.
	if c := r.img.RGBAAt(r.cellW+3, r.cellH-1); c != bg {
		t.Fatalf("cell 1 pixel = %v, want background %v", c, bg)
	}
}

func TestWriteRepaintsOnlyChangedCells(t *testing.T) {
	r := newRenderer(4, 1, 1)
	emu := NewEmulator(4, 1)
	r.frame(emu)

	if err := emu.Write([]byte("hi")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	dirty, changed := r.frame(emu)
	if !changed {
		t.Fatal("write produced no change")
	}
	// Cells 0 and 1 got ink; the cursor moved into cell 2. All three are
	// within one row, so the union is cells 0..2 wide.
	want := viewer.Rect{X: 0, Y: 0, W: 3 * r.cellW, H: r.cellH}
	if dirty != want {
		t.Fatalf("dirty = %s, want %s", dirty, want)
	}
}

func TestGlyphIsInk(t *testing.T) {
	r := newRenderer(4, 1, 1)
	emu := NewEmulator(4, 1)
	r.frame(emu)
	bg := emu.Palette().DefaultBg

	// Move the cursor out of the way and draw a letter over background.
	if err := emu.Write([]byte("a\x1b[2;1H")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	r.frame(emu)
	if n := blankPixels(r.img, 0, 0, r.cellW, r.cellH, bg); n == r.cellW*r.cellH {
		t.Fatal("letter cell is entirely background; the glyph was not painted")
	}
}

func TestCursorDrawsSwappedBlock(t *testing.T) {
	r := newRenderer(4, 1, 1)
	emu := NewEmulator(4, 1)
	// Cursor parked at (0,0) paints that cell with the foreground colour.
	r.frame(emu)
	fg := emu.Palette().DefaultFg
	if c := r.img.RGBAAt(3, 5); c != fg {
		t.Fatalf("cursor cell pixel = %v, want foreground %v", c, fg)
	}
}

func TestCursorMoveRepaintsBothCells(t *testing.T) {
	r := newRenderer(4, 1, 1)
	emu := NewEmulator(4, 1)
	r.frame(emu)
	if err := emu.Write([]byte("hi")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	r.frame(emu)

	// Move the cursor from (2,0) back to (0,0): both cells change.
	if err := emu.Write([]byte("\x1b[1;1H")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	dirty, changed := r.frame(emu)
	if !changed {
		t.Fatal("cursor move produced no change")
	}
	if dirty != (viewer.Rect{X: 0, Y: 0, W: 3 * r.cellW, H: r.cellH}) {
		t.Fatalf("dirty = %s, want 18x11+0+0", dirty)
	}
}

func TestWideCellDrawsBackgroundAcrossBothColumns(t *testing.T) {
	r := newRenderer(3, 1, 1)
	emu := NewEmulator(3, 1)
	r.frame(emu)
	if err := emu.Write([]byte("中")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, changed := r.frame(emu); !changed {
		t.Fatal("wide char produced no change")
	}
	// The leading cell painted a glyph; its continuation painted background.
	if n := blankPixels(r.img, r.cellW, 0, r.cellW, r.cellH, emu.Palette().DefaultBg); n != r.cellW*r.cellH {
		t.Fatal("wide continuation cell drew ink")
	}
}

func TestResizeClearsShadow(t *testing.T) {
	r := newRenderer(4, 1, 1)
	emu := NewEmulator(4, 1)
	r.frame(emu)

	r.resize(2, 1)
	if w, h := r.Size(); w != 2*r.cellW || h != r.cellH {
		t.Fatalf("Size after resize = %dx%d, want %dx%d", w, h, 2*r.cellW, r.cellH)
	}
	dirty, changed := r.frame(emu)
	if !changed {
		t.Fatal("resize produced no change")
	}
	if dirty != (viewer.Rect{X: 0, Y: 0, W: 2 * r.cellW, H: r.cellH}) {
		t.Fatalf("dirty = %s, want 12x11+0+0", dirty)
	}
}
