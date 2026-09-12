// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"image"
	"image/color"
	"strings"
	"testing"
)

var (
	white = color.RGBA{R: 255, G: 255, B: 255, A: 255}
	red   = color.RGBA{R: 255, A: 255}
	black = color.RGBA{A: 255}
)

func newCanvas(w, h int) *Canvas {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	c := NewCanvas(img)
	c.Fill(c.Bounds(), black)
	return c
}

// at returns a pixel as an RGBA value.
func at(c *Canvas, x, y int) color.RGBA {
	i := c.Image().PixOffset(x, y)
	p := c.Image().Pix
	return color.RGBA{R: p[i], G: p[i+1], B: p[i+2], A: p[i+3]}
}

func TestFillClipsToTheSurface(t *testing.T) {
	c := newCanvas(10, 10)
	// A rectangle that starts outside the surface on both axes and ends
	// outside it on both: every layout in the shell can produce one of these
	// on a window the user has dragged small.
	c.Fill(Rect{X: -5, Y: -5, W: 100, H: 100}, white)
	for _, p := range [][2]int{{0, 0}, {9, 9}, {5, 5}} {
		if at(c, p[0], p[1]) != white {
			t.Fatalf("pixel %v was not filled", p)
		}
	}

	c = newCanvas(10, 10)
	for _, r := range []Rect{{}, {W: -1, H: 5}, {X: 20, Y: 20, W: 5, H: 5}, {X: -20, W: 5, H: 5}} {
		c.Fill(r, white)
	}
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			if at(c, x, y) != black {
				t.Fatalf("empty or off-surface rect %v painted (%d,%d)", r0, x, y)
			}
		}
	}
}

var r0 = Rect{} // named so the failure message above reads sensibly

func TestClipStack(t *testing.T) {
	c := newCanvas(20, 20)
	c.PushClip(Rect{X: 5, Y: 5, W: 5, H: 5})
	c.Fill(c.Bounds(), white)
	c.PopClip()

	if at(c, 5, 5) != white || at(c, 9, 9) != white {
		t.Fatal("the clipped region was not painted")
	}
	if at(c, 4, 5) != black || at(c, 10, 9) != black {
		t.Fatal("paint escaped the clip")
	}

	// Nested clips intersect rather than replace.
	c.PushClip(Rect{X: 0, Y: 0, W: 8, H: 20})
	c.PushClip(Rect{X: 4, Y: 0, W: 20, H: 20})
	c.Fill(c.Bounds(), red)
	c.PopClip()
	c.PopClip()
	if at(c, 3, 1) != black || at(c, 8, 1) != black {
		t.Fatal("the nested clip did not intersect")
	}
	if at(c, 4, 1) != red || at(c, 7, 1) != red {
		t.Fatal("the intersection was not painted")
	}

	// An unbalanced pop must not panic: a widget that returns early mid-frame
	// should degrade, not take the window down.
	c.PopClip()
	c.PopClip()
}

func TestFillBlendsStraightAlpha(t *testing.T) {
	c := newCanvas(4, 4)
	c.Fill(c.Bounds(), black)
	c.Fill(c.Bounds(), color.RGBA{R: 255, G: 255, B: 255, A: 128})

	got := at(c, 1, 1)
	if got.R < 126 || got.R > 130 {
		t.Fatalf("half-alpha white over black gave R=%d, want about 128", got.R)
	}
	if got.A != 255 {
		t.Fatalf("the destination lost its opacity (A=%d); it is uploaded to a texture and shown directly", got.A)
	}
	// A fully transparent colour draws nothing at all.
	c.Fill(c.Bounds(), color.RGBA{R: 255})
	if at(c, 1, 1) != got {
		t.Fatal("a zero-alpha fill changed the surface")
	}
}

func TestFillRoundedCutsTheCorners(t *testing.T) {
	c := newCanvas(40, 40)
	r := Rect{X: 5, Y: 5, W: 30, H: 30}
	c.FillRounded(r, 8, white)

	// The very corner pixel is outside the arc; the centre of each edge is
	// well inside it.
	if at(c, 5, 5) == white {
		t.Error("the corner pixel was painted solid; the radius did nothing")
	}
	for _, p := range [][2]int{{20, 5}, {20, 34}, {5, 20}, {34, 20}, {20, 20}} {
		if at(c, p[0], p[1]) != white {
			t.Errorf("pixel %v on an edge or in the middle was not painted", p)
		}
	}

	// The corner is antialiased, not simply absent: there is at least one
	// partially covered pixel between the arc and the corner.
	partial := 0
	for y := 5; y < 14; y++ {
		for x := 5; x < 14; x++ {
			if v := at(c, x, y); v.R > 0 && v.R < 255 {
				partial++
			}
		}
	}
	if partial == 0 {
		t.Error("the rounded corner has no antialiased pixels")
	}

	// A radius larger than the rectangle is clamped rather than inverted.
	c2 := newCanvas(20, 20)
	c2.FillRounded(Rect{X: 2, Y: 2, W: 10, H: 6}, 40, white)
	if at(c2, 7, 5) != white {
		t.Error("an over-large radius emptied the rectangle")
	}
}

func TestStrokeRoundedDrawsInside(t *testing.T) {
	c := newCanvas(40, 40)
	r := Rect{X: 5, Y: 5, W: 30, H: 30}
	c.StrokeRounded(r, 6, 2, white)

	// The border is on the rectangle's own edge...
	if at(c, 20, 5) != white || at(c, 20, 6) != white {
		t.Error("the top border was not drawn on the rectangle's edge")
	}
	// ...and nowhere outside it.
	if at(c, 20, 4) != black || at(c, 4, 20) != black {
		t.Error("the border was drawn outside the rectangle")
	}
	// The middle is untouched: this is a stroke, not a fill.
	if at(c, 20, 20) != black {
		t.Error("StrokeRounded filled the interior")
	}
}

// renderText draws s and returns it as rows of '#' and '.', the only readable
// way to assert on a bitmap.
func renderText(s string, scale int) []string {
	w := TextWidth(s, scale) + GlyphAdvance*scale
	c := newCanvas(max(w, 1), GlyphHeight*scale)
	c.Text(s, 0, 0, scale, white)

	rows := make([]string, 0, c.Bounds().H)
	for y := 0; y < c.Bounds().H; y++ {
		var row strings.Builder
		for x := 0; x < GlyphWidth*scale; x++ {
			if at(c, x, y) == white {
				row.WriteByte('#')
			} else {
				row.WriteByte('.')
			}
		}
		rows = append(rows, row.String())
	}
	return rows
}

// TestCanvasRasterisesLowercase is the whole point of the font work: the
// shell's text is full of lowercase, and it has to come out of the canvas as
// the glyphs the table describes rather than folded to capitals.
func TestCanvasRasterisesLowercase(t *testing.T) {
	tests := []struct {
		name string
		r    string
		want []string
	}{
		{
			"lowercase a", "a",
			[]string{
				".....",
				".....",
				".###.",
				"....#",
				".####",
				"#...#",
				".####",
				".....",
			},
		},
		{
			// g is the glyph that justifies the eight-row cell.
			"lowercase g descends below the baseline", "g",
			[]string{
				".....",
				".....",
				".####",
				"#...#",
				"#...#",
				".####",
				"....#",
				".###.",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderText(tt.r, 1)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Fatalf("%q rendered as\n%s\nwant\n%s", tt.r, strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}

	// And it is not the uppercase glyph: a list of workspace names that
	// shouted would be a regression nobody would notice in a unit test unless
	// it was asserted.
	if strings.Join(renderText("a", 1), "") == strings.Join(renderText("A", 1), "") {
		t.Fatal("lowercase 'a' rendered as 'A'")
	}
}

func TestTextScalesByWholePixels(t *testing.T) {
	one := renderText("a", 1)
	two := renderText("a", 2)
	if len(two) != 2*len(one) {
		t.Fatalf("scale 2 produced %d rows, want %d", len(two), 2*len(one))
	}
	for y, row := range one {
		for x, ch := range row {
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					if got := two[y*2+dy][x*2+dx]; got != byte(ch) {
						t.Fatalf("scale 2 pixel (%d,%d) is %q, want %q", x*2+dx, y*2+dy, got, ch)
					}
				}
			}
		}
	}
}

func TestTextDrawsUnknownRunesVisibly(t *testing.T) {
	// A workspace name or a server error can contain anything. It must not
	// silently vanish, and it must not panic.
	rows := renderText("☃", 1)
	ink := 0
	for _, row := range rows {
		ink += strings.Count(row, "#")
	}
	if ink == 0 {
		t.Fatal("an unmapped rune drew nothing")
	}
}

func TestLineDrawsExactSpans(t *testing.T) {
	c := newCanvas(20, 20)
	c.Line(2, 5, 8, 5, 1, white)
	for x := 2; x <= 8; x++ {
		if at(c, x, 5) != white {
			t.Fatalf("horizontal line missing at x=%d", x)
		}
	}
	if at(c, 1, 5) != black || at(c, 9, 5) != black || at(c, 2, 6) != black {
		t.Fatal("a one-pixel horizontal line painted more than one pixel row")
	}

	c = newCanvas(20, 20)
	c.Line(5, 2, 5, 8, 1, white)
	for y := 2; y <= 8; y++ {
		if at(c, 5, y) != white {
			t.Fatalf("vertical line missing at y=%d", y)
		}
	}

	// Diagonals go through Bresenham; they only need to be connected.
	c = newCanvas(20, 20)
	c.Line(2, 2, 10, 8, 1, white)
	if at(c, 2, 2) != white || at(c, 10, 8) != white {
		t.Fatal("a diagonal line did not reach its endpoints")
	}
}
