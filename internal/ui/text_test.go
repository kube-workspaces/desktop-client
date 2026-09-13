// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/viewer"
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
		if got := TextWidth(tt.s, tt.scale, viewer.RetroFont); got != tt.want {
			t.Errorf("TextWidth(%q, %d) = %d, want %d", tt.s, tt.scale, got, tt.want)
		}
	}
}

// TestTextWidthMatchesWhatIsDrawn is the property layout depends on: a label
// measured at one width and drawn at another is a label that overlaps its
// neighbour. It holds for both faces: the retro bitmap and the clean
// antialiased one.
func TestTextWidthMatchesWhatIsDrawn(t *testing.T) {
	canvas := newCanvas(200, 40)
	for _, f := range []viewer.Font{viewer.RetroFont, viewer.CleanFont} {
		canvas.font = f
		for _, s := range []string{"a", "kube", "cf-debian-gnome-vm-0", "Sign in", "…"} {
			for _, scale := range []int{1, 2, 3} {
				got := canvas.Text(s, 0, 0, scale, white)
				if want := TextWidth(s, scale, f); got != want {
					t.Errorf("drawing %q at scale %d advanced %d, TextWidth says %d", s, scale, got, want)
				}
			}
		}
	}
}

// TestCleanGlyphsCarryCoverage pins the whole point of the clean face: its
// pixels are coverage values, not full-on bits (the coverage lands in the RGB
// channels because the canvas raster is opaque). At the body scale (2, the
// SemiBold cut), edges land between pixels and show as intermediate ink.
func TestCleanGlyphsCarryCoverage(t *testing.T) {
	c := newCanvas(60, 60)
	c.font = viewer.CleanFont
	c.Text("ag", 0, 0, 2, white)
	var mid int // pixels that are neither background nor fully inked
	for y := 0; y < 60; y++ {
		for x := 0; x < 60; x++ {
			if p := at(c, x, y); p.R != 0 && p.R != 255 {
				mid++
			}
		}
	}
	if mid == 0 {
		t.Error("clean glyph has no antialiased pixels")
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
	fullW := TextWidth(full, scale, viewer.RetroFont)

	if got := Truncate(full, scale, viewer.RetroFont, fullW); got != full {
		t.Errorf("text that fits exactly was truncated to %q", got)
	}
	if got := Truncate(full, scale, viewer.RetroFont, fullW+100); got != full {
		t.Errorf("text with room to spare was truncated to %q", got)
	}

	got := Truncate(full, scale, viewer.RetroFont, fullW-GlyphAdvance)
	if !strings.HasSuffix(got, Ellipsis) {
		t.Fatalf("Truncate(%q) = %q, want a trailing ellipsis", full, got)
	}
	if w := TextWidth(got, scale, viewer.RetroFont); w > fullW-GlyphAdvance {
		t.Fatalf("Truncate returned %q, %d px wide, which does not fit in %d", got, w, fullW-GlyphAdvance)
	}
	if !strings.HasPrefix(full, strings.TrimSuffix(got, Ellipsis)) {
		t.Fatalf("Truncate(%q) = %q, which is not a prefix of the input", full, got)
	}

	// Narrower than the marker itself: show what fits rather than nothing.
	tiny := Truncate(full, scale, viewer.RetroFont, GlyphWidth)
	if tiny != "c" {
		t.Errorf("Truncate into one glyph = %q, want %q", tiny, "c")
	}
	if Truncate(full, scale, viewer.RetroFont, 0) != "" || Truncate(full, 0, viewer.RetroFont, 100) != "" {
		t.Error("Truncate into no space should be empty")
	}
}

// TestTruncateAlwaysFits is the invariant that matters: whatever the input,
// the result is drawable inside the width it was given.
func TestTruncateAlwaysFits(t *testing.T) {
	inputs := []string{"", "a", "workspace", "cf-debian-gnome-vm-0", strings.Repeat("wide ", 40), "…—…"}
	for _, s := range inputs {
		for _, f := range []viewer.Font{viewer.RetroFont, viewer.BubblyFont, viewer.CleanFont} {
			for _, scale := range []int{1, 2, 3} {
				for w := 0; w < 120; w += 3 {
					got := Truncate(s, scale, f, w)
					if TextWidth(got, scale, f) > w {
						t.Fatalf("Truncate(%q, %d, %d) = %q, %d px wide", s, scale, w, got, TextWidth(got, scale, f))
					}
				}
			}
		}
	}
}

func TestWrap(t *testing.T) {
	const scale = 1
	width := TextWidth("aaaaaaaaaa", scale, viewer.RetroFont) // ten columns

	got := Wrap("the quick brown fox jumps", scale, viewer.RetroFont, width)
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
	long := Wrap("supercalifragilistic", scale, viewer.RetroFont, width)
	if len(long) != 2 || long[0] != "supercalif" {
		t.Fatalf("Wrap of a long word = %q", long)
	}

	// Newlines start a new line, and every line fits.
	for _, line := range Wrap("first line\nsecond line that is rather longer", scale, viewer.RetroFont, width) {
		if TextWidth(line, scale, viewer.RetroFont) > width {
			t.Fatalf("wrapped line %q is %d px wide, limit %d", line, TextWidth(line, scale, viewer.RetroFont), width)
		}
	}
	if got := Wrap("", scale, viewer.RetroFont, width); len(got) != 1 || got[0] != "" {
		t.Fatalf("Wrap of an empty string = %q, want one empty line", got)
	}
	if Wrap("anything", scale, viewer.RetroFont, 0) != nil {
		t.Error("Wrap into no width should produce nothing")
	}
}

// TestWrapProportionalKeepsEveryLineInside is the clean face's version of the
// fit invariant: wrapping must stay exact even though the glyphs are not all
// one width.
func TestWrapProportionalKeepsEveryLineInside(t *testing.T) {
	text := "connection lost: the upstream proxy closed the WebSocket before any VNC handshake byte was exchanged (status 503)"
	widths := []int{40, 77, 100, 200}
	for _, scale := range []int{1, 2} {
		for _, w := range widths {
			for _, line := range Wrap(text, scale, viewer.CleanFont, w) {
				if got := TextWidth(line, scale, viewer.CleanFont); got > w {
					t.Fatalf("scale %d: wrapped line %q is %d px wide, limit %d", scale, line, got, w)
				}
			}
		}
	}
}
