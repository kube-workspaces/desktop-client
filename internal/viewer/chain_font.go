// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

// The clean face's coverage chain: Inter first, DejaVu Sans behind it.
//
// Inter is a UI-first geometric sans but its repertoire is Latin-centric; a
// workspace named "müller", a Greek error string from a server, or Cyrillic
// in a username rendered as a row of missing-glyph boxes. DejaVu Sans (the
// Bitstream Vera license ships beside it in assets/DejaVu-LICENSE.txt) covers
// Latin-Extended, Greek, Cyrillic and a wide symbol range, so it fills exactly
// the gap: every rune Inter draws still comes from Inter, and only the runes
// it lacks fall through to DejaVu. Scripts neither face covers (CJK,
// Arabic/Hebrew, emoji) still render as the hollow missing box — visible, and
// therefore debuggable, rather than silently dropped.
//
// Both fonts are parsed once and rasterised lazily per rune per scale into a
// cache, so startup pays nothing and a session pays for exactly the runes it
// shows. Everything here runs on the window-owning goroutine, like the rest
// of the package, so the cache needs no lock.

import (
	_ "embed"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

//go:embed assets/DejaVuSans.ttf
var dejaVuSans []byte

// chainParsed records whether the two typefaces parsed; chainBroken latches a
// parse failure, in which case every rune is the missing box (and stays
// visible) rather than blank text.
var (
	chainInterRegular  *opentype.Font
	chainInterSemiBold *opentype.Font
	chainDejaVu        *opentype.Font
	chainParsed        bool
	chainBroken        bool
)

// parseChainFonts parses the embedded typefaces once. A failure is impossible
// with the committed assets, but a field of blank text is the subtlest way to
// break, so the failure path is explicit and total: missing boxes everywhere.
func parseChainFonts() {
	if chainParsed {
		return
	}
	chainParsed = true
	var err error
	if chainInterRegular, err = opentype.Parse(interRegular); err != nil {
		chainBroken = true
		return
	}
	if chainInterSemiBold, err = opentype.Parse(interSemiBold); err != nil {
		chainBroken = true
		return
	}
	if chainDejaVu, err = opentype.Parse(dejaVuSans); err != nil {
		chainBroken = true
		return
	}
}

// chainEntry is one cached rune: its raster, its advance, and whether either
// face in the chain can draw it.
type chainEntry struct {
	raster Raster
	adv    int
	ok     bool
}

// chainSet is one scale's rasterisation state: the two faces plus the rune
// cache. The primary face follows the clean face's weight rule (SemiBold at
// title scale and above, where titles live; body text at large HiDPI scales
// shares the weight, which beats the alternative of light titles); the
// fallback is always DejaVu Regular.
type chainSet struct {
	ready    bool
	primary  font.Face
	fallback font.Face
	entries  map[rune]chainEntry
}

// chainCache holds one set per scale, built on first use. Nothing about a set
// ever becomes stale: the embedded font bytes cannot change after build.
var chainCache [cleanMaxScale + 1]chainSet

// chainSetFor returns the rasterisation state for scale, building it on first
// use. Faces that fail to build leave the set with nil faces, which reads as
// "no coverage" rune by rune — the same missing-box-everywhere guarantee as a
// parse failure.
func chainSetFor(scale int) *chainSet {
	s := cleanScale(scale)
	cs := &chainCache[s]
	if cs.ready {
		return cs
	}
	cs.entries = make(map[rune]chainEntry)
	parseChainFonts()
	if !chainBroken {
		ttf := chainInterRegular
		if s >= 3 {
			ttf = chainInterSemiBold
		}
		if primary, err := opentype.NewFace(ttf, &opentype.FaceOptions{
			Size:    float64(cleanSize[s]),
			DPI:     72,
			Hinting: font.HintingFull,
		}); err == nil {
			cs.primary = primary
		}
		if fallback, err := opentype.NewFace(chainDejaVu, &opentype.FaceOptions{
			Size:    float64(cleanSize[s]),
			DPI:     72,
			Hinting: font.HintingFull,
		}); err == nil {
			cs.fallback = fallback
		}
	}
	cs.ready = true
	return cs
}

// chainBuild resolves one rune through the chain: Inter first, DejaVu behind
// it, the missing box when neither covers it. Coverage is whatever the faces
// report via GlyphAdvance — no hardcoded ranges to drift out of sync with the
// fonts.
func (cs *chainSet) chainBuild(r rune, scale int) chainEntry {
	if _, ok := faceAdvance(cs.primary, r); ok {
		return chainEntry{raster: rasterizeGlyph(cs.primary, r, cleanBase[scale], cleanCell[scale]), adv: chainAdvanceOf(cs.primary, r, scale), ok: true}
	}
	if _, ok := faceAdvance(cs.fallback, r); ok {
		return chainEntry{raster: rasterizeGlyph(cs.fallback, r, cleanBase[scale], cleanCell[scale]), adv: chainAdvanceOf(cs.fallback, r, scale), ok: true}
	}
	return chainEntry{raster: cleanMissing(scale), adv: cleanAvg[scale]}
}

// faceAdvance reports a face's advance for r, or false when the face is nil
// or the font has no glyph for r.
func faceAdvance(f font.Face, r rune) (int, bool) {
	if f == nil {
		return 0, false
	}
	adv, ok := f.GlyphAdvance(r)
	if !ok {
		return 0, false
	}
	return adv.Round(), true
}

// chainAdvanceOf reads the advance the raster was laid out with: the face's
// own advance plus the one-pixel tracking the clean face applies above scale
// 1, so measurement and drawing use the same step.
func chainAdvanceOf(f font.Face, r rune, scale int) int {
	adv, ok := faceAdvance(f, r)
	if !ok {
		return cleanAvg[cleanScale(scale)]
	}
	if cleanScale(scale) > 1 {
		adv += 1
	}
	return adv
}

// chainLookup returns the cached entry for r at scale, building it on first
// use.
func chainLookup(r rune, scale int) chainEntry {
	s := cleanScale(scale)
	cs := chainSetFor(s)
	if e, ok := cs.entries[r]; ok {
		return e
	}
	e := cs.chainBuild(r, s)
	cs.entries[r] = e
	return e
}

// chainRaster returns the chain's raster for r at scale. It is the clean
// font's Glyph accessor.
func chainRaster(r rune, scale int) Raster {
	return chainLookup(r, scale).raster
}

// chainGlyphAdvance returns the chain's advance for r at scale. It is the
// clean font's Advance accessor.
func chainGlyphAdvance(r rune, scale int) int {
	return chainLookup(r, scale).adv
}

// chainCovers reports whether either face in the chain can draw r. It is the
// clean font's Covers accessor and deliberately shares the raster cache, so
// measuring text warms the drawing of it.
func chainCovers(r rune) bool {
	return chainLookup(r, 2).ok
}
