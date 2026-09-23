// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"testing"
)

func TestChainCoversEuropeanScripts(t *testing.T) {
	// Latin-1 accented (Inter), Greek, Cyrillic and Hebrew (DejaVu behind
	// it): the workspace names, usernames and server strings that used to
	// box. Punctuation both faces carry (em dash, ellipsis) and DejaVu's
	// monochrome emoji stay too — a real glyph beats a missing box.
	for _, r := range []rune{'ü', 'ö', 'é', 'ł', 'Φ', 'Ω', 'Ж', 'д', 'א', '…', '–', '—', '😀'} {
		if !chainCovers(r) {
			t.Errorf("chain does not cover %q (U+%04X)", r, r)
		}
	}
	// Plain ASCII is covered by construction.
	for r := glyphFirst; r <= glyphLast; r++ {
		if !chainCovers(r) {
			t.Fatalf("chain does not cover ASCII %q", r)
		}
	}
}

func TestChainLeavesUncoveredScriptsVisible(t *testing.T) {
	// CJK, Hangul, Thai and Devanagari are in neither embedded face: they
	// must render as the hollow missing box — visible and debuggable —
	// never as blank.
	for _, r := range []rune{'中', '日', '한', 'ก', 'अ'} {
		if chainCovers(r) {
			t.Errorf("chain claims to cover %q (U+%04X)", r, r)
		}
		got := chainRaster(r, 2)
		if got == (Raster{}) {
			t.Errorf("%q rasterised to nothing; uncovered text must stay visible", r)
		}
		if got != cleanMissing(2) {
			t.Errorf("%q did not render as the missing box", r)
		}
	}
}

func TestChainDrawsCoveredRunesAsInk(t *testing.T) {
	// A covered rune's raster is real ink, not the missing box and not empty.
	for _, r := range []rune{'ü', 'Φ', 'Ж'} {
		got := chainRaster(r, 2)
		if got == (Raster{}) {
			t.Fatalf("%q rasterised to nothing", r)
		}
		if got == cleanMissing(2) {
			t.Fatalf("%q rendered as the missing box although covered", r)
		}
	}
	// Advances are positive and cached: the second call answers from the map.
	for _, r := range []rune{'ü', 'm'} {
		a1, a2 := chainGlyphAdvance(r, 2), chainGlyphAdvance(r, 2)
		if a1 <= 0 || a1 != a2 {
			t.Fatalf("advance for %q = %d/%d, want a stable positive step", r, a1, a2)
		}
	}
}

func TestChainExtendsToLargeHiDPIScales(t *testing.T) {
	// A 2x interface draws body text at scale 4 and titles at 6: the chain
	// must rasterise there, not clamp to the old three-size set.
	for _, scale := range []int{4, 5, 6} {
		got := chainRaster('ü', scale)
		if got == (Raster{}) || got == cleanMissing(scale) {
			t.Fatalf("covered 'ü' at scale %d rendered as %v", scale, got == (Raster{}))
		}
		if cleanTextHeight(scale) <= cleanTextHeight(3) {
			t.Fatalf("scale %d row height did not grow past the title row", scale)
		}
	}
}

func TestFoldToFaceKeepsCoveredRunes(t *testing.T) {
	got := FoldToFace("müller … —\t", chainCovers)
	// ü, … and — survive (the chain draws all three); the tab becomes a
	// space. The bitmap fold keeps its old behaviour for the same string.
	const want = "müller … — "
	if got != want {
		t.Fatalf("FoldToFace = %q, want %q", got, want)
	}
	// The bitmap fold keeps its old behaviour for the same string: the
	// ellipsis folds, and what it cannot fold (ü) is left to box at draw.
	if got := FoldToFont("müller …"); got != "müller ..." {
		t.Fatalf("FoldToFont = %q, want %q", got, "müller ...")
	}
}
