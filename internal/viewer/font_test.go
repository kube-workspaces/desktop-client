// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"strings"
	"testing"
)

// renderGlyph draws one rune the way the overlay does and returns it as rows
// of '#' and '.', which is the only readable way to assert on a bitmap.
func renderGlyph(r rune, scale int) []string {
	img := overlayImage{
		w:      glyphWidth * scale,
		h:      glyphHeight * scale,
		stride: glyphWidth * scale * 4,
	}
	img.pix = make([]byte, img.stride*img.h)
	img.text(string(r), 0, 0, scale)

	rows := make([]string, 0, img.h)
	for y := 0; y < img.h; y++ {
		var row strings.Builder
		for x := 0; x < img.w; x++ {
			// Only fully opaque white is a glyph pixel; the panel fill and the
			// border are drawn translucent.
			if img.pix[y*img.stride+x*4+3] == 255 {
				row.WriteByte('#')
			} else {
				row.WriteByte('.')
			}
		}
		rows = append(rows, row.String())
	}
	return rows
}

func TestGlyphsRasteriseToTheirBitmap(t *testing.T) {
	tests := []struct {
		name string
		r    rune
		want []string
	}{
		{
			// A capital fills rows 0 to 6 and leaves the descender row clear.
			"uppercase A",
			'A',
			[]string{
				".###.",
				"#...#",
				"#...#",
				"#####",
				"#...#",
				"#...#",
				"#...#",
				".....",
			},
		},
		{
			// Lowercase has its own glyphs now: a workspace list that folded
			// to uppercase would shout every name back at the user.
			"lowercase a",
			'a',
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
			// The point of the eight-row cell: g descends past the baseline
			// on row 7 instead of being squashed into the body.
			"lowercase g descends",
			'g',
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
		{
			// g and y share a descender and must not share a glyph: the
			// closed bowl is the only thing telling them apart.
			"lowercase y",
			'y',
			[]string{
				".....",
				".....",
				"#...#",
				"#...#",
				"#...#",
				".####",
				"....#",
				".###.",
			},
		},
		{
			"digit 1",
			'1',
			[]string{
				"..#..",
				".##..",
				"..#..",
				"..#..",
				"..#..",
				"..#..",
				".###.",
				".....",
			},
		},
		{
			"full stop",
			'.',
			[]string{
				".....",
				".....",
				".....",
				".....",
				".....",
				".##..",
				".##..",
				".....",
			},
		},
		{
			"space is blank",
			' ',
			[]string{
				".....",
				".....",
				".....",
				".....",
				".....",
				".....",
				".....",
				".....",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderGlyph(tt.r, 1)
			if len(got) != len(tt.want) {
				t.Fatalf("glyph %q rendered %d rows, want %d", tt.r, len(got), len(tt.want))
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("glyph %q:\ngot\n%s\nwant\n%s",
						tt.r, strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
				}
			}
		})
	}
}

// TestUnknownRunesDegradeGracefully: the overlay renders server-supplied error
// text, so a rune the table does not cover must draw the missing-glyph box —
// not panic, and not silently vanish.
func TestUnknownRunesDegradeGracefully(t *testing.T) {
	for _, r := range []rune{'€', '☃', 0, 0x10FFFF, '\n', '\t', 0x7f, 0x80} {
		got := glyphFor(r)
		if got == (glyph{}) {
			t.Fatalf("rune %q rendered as blank; a missing glyph must be visible", r)
		}
	}
	if glyphFor('☃') != missingGlyph {
		t.Fatal("an unmapped rune did not render as the missing-glyph box")
	}
	// Every rune in the table's own range is drawable without falling back.
	for r := rune(glyphFirst); r <= glyphLast; r++ {
		if r == ' ' {
			continue
		}
		if glyphFor(r) == (glyph{}) {
			t.Fatalf("rune %q has an empty glyph", r)
		}
		if glyphFor(r) == missingGlyph {
			t.Fatalf("rune %q fell back to the missing-glyph box", r)
		}
	}

	// And the layout path survives whatever it is handed.
	img := renderOverlay([]string{"☃ 500 Internal Server Error ☃", "\x00\x01"}, 1024, 768)
	if img.w <= 0 || img.h <= 0 {
		t.Fatal("an overlay of unmappable text rendered nothing")
	}
}

// TestFontCoversPrintableASCII is the property the shell depends on: a
// workspace name, a namespace or an error message is arbitrary printable
// ASCII, and every one of those runes must have a distinct glyph.
func TestFontCoversPrintableASCII(t *testing.T) {
	if glyphFirst != 0x20 || glyphLast != 0x7e {
		t.Fatalf("font covers %#x..%#x, want 0x20..0x7e", glyphFirst, glyphLast)
	}
	if got, want := len(glyphs), 0x7f-0x20; got != want {
		t.Fatalf("table holds %d glyphs, want %d", got, want)
	}

	// Case must be distinguishable: the fold to uppercase is gone.
	for r := 'a'; r <= 'z'; r++ {
		upper := r - ('a' - 'A')
		if glyphFor(r) == glyphFor(upper) {
			t.Fatalf("%q and %q share a glyph; lowercase is no longer folded", r, upper)
		}
	}

	// Descenders are what the eighth row is for.
	for _, r := range []rune{'g', 'j', 'p', 'q', 'y', ','} {
		if glyphFor(r)[glyphHeight-1] == 0 {
			t.Fatalf("%q does not use the descender row", r)
		}
	}
	// And nothing else may, or a line of capitals would sit unevenly.
	for r := 'A'; r <= 'Z'; r++ {
		if glyphFor(r)[glyphHeight-1] != 0 {
			t.Fatalf("capital %q draws on the descender row", r)
		}
	}
	for r := '0'; r <= '9'; r++ {
		if glyphFor(r)[glyphHeight-1] != 0 {
			t.Fatalf("digit %q draws on the descender row", r)
		}
	}

	// No glyph may spill outside the cell.
	for r := rune(glyphFirst); r <= glyphLast; r++ {
		for row, bits := range glyphFor(r) {
			if bits>>glyphWidth != 0 {
				t.Fatalf("glyph %q row %d has bits outside the %d-pixel cell", r, row, glyphWidth)
			}
		}
	}
}

// TestExportedFontMatchesTheTable guards the accessors internal/ui draws
// through: they must be the same font, not a copy that can drift.
func TestExportedFontMatchesTheTable(t *testing.T) {
	if GlyphWidth != glyphWidth || GlyphHeight != glyphHeight ||
		GlyphAdvance != glyphAdvance || LineAdvance != lineAdvance {
		t.Fatal("the exported metrics disagree with the table's own")
	}
	if Baseline >= GlyphHeight {
		t.Fatalf("baseline row %d is outside a %d-row cell", Baseline, GlyphHeight)
	}
	for r := rune(0); r < 0x100; r++ {
		if GlyphFor(r) != glyphFor(r) {
			t.Fatalf("GlyphFor(%q) does not match the table", r)
		}
	}
	if got := FoldToFont("Reconnecting…"); got != "Reconnecting..." {
		t.Fatalf("FoldToFont = %q", got)
	}
}

func TestFoldToFontRewritesTypography(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Reconnecting…", "Reconnecting..."},
		{"Reconnecting failed — timeout", "Reconnecting failed - timeout"},
		{"plain ascii", "plain ascii"},
		{"tab\there", "tab here"},
		{"line\nbreak", "line break"},
	}
	for _, tt := range tests {
		if got := foldToFont(tt.in); got != tt.want {
			t.Fatalf("foldToFont(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestShortDetailFlattensAndTruncates(t *testing.T) {
	if got := shortDetail("  dial tcp:\n  connection   refused  "); got != "dial tcp: connection refused" {
		t.Fatalf("shortDetail collapsed to %q", got)
	}
	long := strings.Repeat("a", 200)
	got := shortDetail(long)
	if n := len([]rune(got)); n != maxDetailRunes {
		t.Fatalf("shortDetail returned %d runes, want %d", n, maxDetailRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("a truncated detail (%q) should say so", got)
	}
	if shortDetail("") != "" {
		t.Fatal("an empty detail should stay empty")
	}
}

// TestOverlayPanelIsLegible checks the two things that make the plate readable
// at all: opaque text, and a background that is not.
func TestOverlayPanelIsLegible(t *testing.T) {
	img := renderOverlay([]string{"Reconnecting…"}, 1280, 800)
	if img.w <= 0 || img.h <= 0 {
		t.Fatal("nothing was rasterised")
	}
	if len(img.pix) != img.stride*img.h {
		t.Fatalf("pixel buffer is %d bytes, want %d", len(img.pix), img.stride*img.h)
	}

	var opaque, translucent int
	for i := 3; i < len(img.pix); i += 4 {
		switch img.pix[i] {
		case 255:
			opaque++
		case overlayPanelAlpha:
			translucent++
		}
	}
	if opaque == 0 {
		t.Fatal("the overlay has no opaque text pixels")
	}
	if translucent == 0 {
		t.Fatal("the overlay has no translucent panel, so it would hide the frozen frame entirely")
	}
	if opaque > translucent {
		t.Fatalf("text (%d px) covers more of the plate than the panel (%d px)", opaque, translucent)
	}
}
