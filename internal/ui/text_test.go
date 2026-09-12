// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"
	"testing"
)

func TestTextWidthCountsInkNotCells(t *testing.T) {
	// One glyph is GlyphWidth wide; each further glyph adds a whole advance.
	// The trailing gap is not part of the string, which is what keeps a
	// centred label actually centred.
	tests := []struct {
		s     string
		scale int
		want  int
	}{
		{"", 1, 0},
		{"", 4, 0},
		{"a", 1, GlyphWidth},
		{"a", 3, GlyphWidth * 3},
		{"ab", 1, GlyphAdvance + GlyphWidth},
		{"abc", 2, (2*GlyphAdvance + GlyphWidth) * 2},
	}
	for _, tt := range tests {
		if got := TextWidth(tt.s, tt.scale); got != tt.want {
			t.Errorf("TextWidth(%q, %d) = %d, want %d", tt.s, tt.scale, got, tt.want)
		}
	}
}

// TestTextWidthMatchesWhatIsDrawn is the property layout depends on: a label
// measured at one width and drawn at another is a label that overlaps its
// neighbour.
func TestTextWidthMatchesWhatIsDrawn(t *testing.T) {
	c := newCanvas(200, 40)
	for _, s := range []string{"a", "kube", "cf-debian-gnome-vm-0", "Sign in", "…"} {
		for _, scale := range []int{1, 2, 3} {
			got := c.Text(s, 0, 0, scale, white)
			if want := TextWidth(s, scale); got != want {
				t.Errorf("drawing %q at scale %d advanced %d, TextWidth says %d", s, scale, got, want)
			}
		}
	}
}

func TestRuneCountFollowsTheFold(t *testing.T) {
	// An ellipsis folds to three cells, so measuring it as one rune would
	// under-measure every truncated string in the interface.
	if got, want := RuneCount("…"), 3; got != want {
		t.Errorf("RuneCount(ellipsis) = %d, want %d", got, want)
	}
	if got, want := RuneCount("ab\tc"), 4; got != want {
		t.Errorf("RuneCount with a tab = %d, want %d", got, want)
	}
}

func TestTruncate(t *testing.T) {
	const scale = 1
	full := "cf-debian-gnome-vm-0"
	fullW := TextWidth(full, scale)

	if got := Truncate(full, scale, fullW); got != full {
		t.Errorf("text that fits exactly was truncated to %q", got)
	}
	if got := Truncate(full, scale, fullW+100); got != full {
		t.Errorf("text with room to spare was truncated to %q", got)
	}

	got := Truncate(full, scale, fullW-GlyphAdvance)
	if !strings.HasSuffix(got, Ellipsis) {
		t.Fatalf("Truncate(%q) = %q, want a trailing ellipsis", full, got)
	}
	if w := TextWidth(got, scale); w > fullW-GlyphAdvance {
		t.Fatalf("Truncate returned %q, %d px wide, which does not fit in %d", got, w, fullW-GlyphAdvance)
	}
	if !strings.HasPrefix(full, strings.TrimSuffix(got, Ellipsis)) {
		t.Fatalf("Truncate(%q) = %q, which is not a prefix of the input", full, got)
	}

	// Narrower than the marker itself: show what fits rather than nothing.
	tiny := Truncate(full, scale, GlyphWidth)
	if tiny != "c" {
		t.Errorf("Truncate into one glyph = %q, want %q", tiny, "c")
	}
	if Truncate(full, scale, 0) != "" || Truncate(full, 0, 100) != "" {
		t.Error("Truncate into no space should be empty")
	}
}

// TestTruncateAlwaysFits is the invariant that matters: whatever the input,
// the result is drawable inside the width it was given.
func TestTruncateAlwaysFits(t *testing.T) {
	inputs := []string{"", "a", "workspace", "cf-debian-gnome-vm-0", strings.Repeat("wide ", 40), "…—…"}
	for _, s := range inputs {
		for _, scale := range []int{1, 2, 3} {
			for w := 0; w < 80; w += 3 {
				got := Truncate(s, scale, w)
				if TextWidth(got, scale) > w {
					t.Fatalf("Truncate(%q, %d, %d) = %q, %d px wide", s, scale, w, got, TextWidth(got, scale))
				}
			}
		}
	}
}

func TestWrap(t *testing.T) {
	const scale = 1
	width := TextWidth("aaaaaaaaaa", scale) // ten columns

	got := Wrap("the quick brown fox jumps", scale, width)
	want := []string{"the quick", "brown fox", "jumps"}
	if len(got) != len(want) {
		t.Fatalf("Wrap produced %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Wrap produced %q, want %q", got, want)
		}
	}

	// A word longer than the line is broken rather than left to overflow:
	// server error text contains URLs.
	long := Wrap("supercalifragilistic", scale, width)
	if len(long) != 2 || long[0] != "supercalif" {
		t.Fatalf("Wrap of a long word = %q", long)
	}

	// Newlines start a new line, and every line fits.
	for _, line := range Wrap("first line\nsecond line that is rather longer", scale, width) {
		if TextWidth(line, scale) > width {
			t.Fatalf("wrapped line %q is %d px wide, limit %d", line, TextWidth(line, scale), width)
		}
	}
	if got := Wrap("", scale, width); len(got) != 1 || got[0] != "" {
		t.Fatalf("Wrap of an empty string = %q, want one empty line", got)
	}
	if Wrap("anything", scale, 0) != nil {
		t.Error("Wrap into no width should produce nothing")
	}
}
