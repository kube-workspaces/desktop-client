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
			},
		},
		{
			// Lowercase folds to the uppercase glyph rather than doubling the
			// table; the overlay is short status text drawn large.
			"lowercase a folds to A",
			'a',
			[]string{
				".###.",
				"#...#",
				"#...#",
				"#####",
				"#...#",
				"#...#",
				"#...#",
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
	for _, r := range []rune{'€', '☃', 0, 0x10FFFF, '\n', '\t', 0x7f, '{', '~'} {
		got := glyphFor(r)
		if got == (glyph{}) && r != ' ' {
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
	}

	// And the layout path survives whatever it is handed.
	img := renderOverlay([]string{"☃ 500 Internal Server Error ☃", "\x00\x01"}, 1024, 768)
	if img.w <= 0 || img.h <= 0 {
		t.Fatal("an overlay of unmappable text rendered nothing")
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
