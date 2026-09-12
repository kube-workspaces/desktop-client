// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import "testing"

func TestInsetAndCuts(t *testing.T) {
	r := Rect{X: 10, Y: 20, W: 100, H: 50}

	if got, want := Inset(r, 5), (Rect{X: 15, Y: 25, W: 90, H: 40}); got != want {
		t.Errorf("Inset = %v, want %v", got, want)
	}
	// An inset that would invert the rectangle empties it instead, because
	// every caller then draws nothing rather than something negative.
	if got := Inset(r, 40); got.H != 0 {
		t.Errorf("an over-large inset produced %v", got)
	}

	top, rest := CutTop(r, 20)
	if top != (Rect{X: 10, Y: 20, W: 100, H: 20}) || rest != (Rect{X: 10, Y: 40, W: 100, H: 30}) {
		t.Errorf("CutTop = %v, %v", top, rest)
	}
	bottom, rest := CutBottom(r, 20)
	if bottom != (Rect{X: 10, Y: 50, W: 100, H: 20}) || rest != (Rect{X: 10, Y: 20, W: 100, H: 30}) {
		t.Errorf("CutBottom = %v, %v", bottom, rest)
	}
	left, rest := CutLeft(r, 30)
	if left != (Rect{X: 10, Y: 20, W: 30, H: 50}) || rest != (Rect{X: 40, Y: 20, W: 70, H: 50}) {
		t.Errorf("CutLeft = %v, %v", left, rest)
	}
	right, rest := CutRight(r, 30)
	if right != (Rect{X: 80, Y: 20, W: 30, H: 50}) || rest != (Rect{X: 10, Y: 20, W: 70, H: 50}) {
		t.Errorf("CutRight = %v, %v", right, rest)
	}

	// Cuts larger than the rectangle take all of it, leaving nothing, rather
	// than producing a negative remainder for the next cut to compound.
	all, none := CutTop(r, 1000)
	if all != r || none.H != 0 {
		t.Errorf("an over-large CutTop = %v, %v", all, none)
	}
	if _, none := CutLeft(r, -10); none != r {
		t.Errorf("a negative CutLeft consumed %v", none)
	}
}

func TestCenterAndIntersect(t *testing.T) {
	outer := Rect{X: 0, Y: 0, W: 100, H: 100}
	if got, want := CenterRect(outer, 40, 20), (Rect{X: 30, Y: 40, W: 40, H: 20}); got != want {
		t.Errorf("CenterRect = %v, want %v", got, want)
	}
	if got := Intersect(Rect{W: 10, H: 10}, Rect{X: 5, Y: 5, W: 10, H: 10}); got != (Rect{X: 5, Y: 5, W: 5, H: 5}) {
		t.Errorf("Intersect = %v", got)
	}
	if got := Intersect(Rect{W: 10, H: 10}, Rect{X: 20, W: 10, H: 10}); got != (Rect{}) {
		t.Errorf("disjoint rectangles intersected to %v", got)
	}
}

func TestStack(t *testing.T) {
	s := NewStack(Rect{X: 10, Y: 10, W: 200, H: 300}, 8)

	first := s.Next(30)
	if first != (Rect{X: 10, Y: 10, W: 200, H: 30}) {
		t.Fatalf("first row = %v", first)
	}
	// The gap goes between rows, not before the first one: n rows produce
	// n-1 gaps, so a stack does not float away from the top of its container.
	second := s.Next(20)
	if second.Y != 10+30+8 {
		t.Fatalf("second row at y=%d, want %d", second.Y, 10+30+8)
	}
	if s.Height() != 30+8+20 {
		t.Fatalf("Height = %d", s.Height())
	}

	s.Skip(12)
	third := s.Next(10)
	if third.Y != 10+30+8+20+12+8 {
		t.Fatalf("Skip and the gap did not compose: third row at y=%d", third.Y)
	}

	rest := s.Rest()
	if rest.Y+rest.H != 310 {
		t.Fatalf("Rest ends at %d, want the bottom of the stack (310)", rest.Y+rest.H)
	}
	// Rest consumes what is left; asking twice yields nothing rather than
	// silently overlapping the first answer.
	if again := s.Rest(); again.H != 0 {
		t.Fatalf("a second Rest returned %v", again)
	}
}

func TestRow(t *testing.T) {
	r := Rect{X: 0, Y: 0, W: 300, H: 40}

	// Two fixed columns and one flexible, with the gaps taken out first.
	cols := Row(r, 10, 100, 0, 50)
	if len(cols) != 3 {
		t.Fatalf("Row produced %d columns", len(cols))
	}
	if cols[0].W != 100 || cols[2].W != 50 {
		t.Fatalf("fixed widths were not honoured: %v", cols)
	}
	if cols[1].W != 300-100-50-20 {
		t.Fatalf("the flexible column is %d wide, want %d", cols[1].W, 300-100-50-20)
	}
	if end := cols[2].X + cols[2].W; end != 300 {
		t.Fatalf("the row ends at %d, want 300", end)
	}

	// Equal shares, with the division remainder spread so the row still ends
	// exactly on the right edge.
	cols = Row(Rect{W: 100, H: 10}, 0, 0, 0, 0)
	total := 0
	for _, c := range cols {
		total += c.W
	}
	if total != 100 {
		t.Fatalf("equal columns total %d, want 100", total)
	}

	// More columns than there is room for: widths clamp at zero rather than
	// going negative and drawing backwards.
	for _, c := range Row(Rect{W: 20, H: 10}, 10, 0, 0, 0) {
		if c.W < 0 {
			t.Fatalf("a column has negative width: %v", c)
		}
	}
	if Row(r, 10) != nil {
		t.Fatal("Row with no widths should produce nothing")
	}
}
