// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

// The clean face: Inter, rasterised on demand into coverage cells.
//
// The two bitmap faces are hand-authored and deliberately 8-bit, and the client
// is cgo-free, so a real font renderer would cost the single-host cross-build.
// golang.org/x/image/font is as pure-Go as the bitmap tables: it parses and
// rasterises TrueType in-process, needs no system font stack, and renders
// identically on all six targets.
//
// Inter (SIL Open Font License 1.1; the license text ships beside the binaries
// in assets/OFL.txt) is a UI-first geometric sans. Regular is used for the
// small and body scales, SemiBold for the title scale, so a heading reads as
// heavier as well as larger.
//
// Unlike the bitmap faces, the clean face is proportional: each glyph is
// rasterised into a cell exactly as wide as its own ink, and [Font.Advance]
// returns each glyph's natural advance. Nothing is clipped horizontally, a
// line of "W"s is not squeezed, and an "i" does not drag three cells of slack
// behind it. The three scales are sized to match the bitmap faces' Small,
// Body and Title rows: 11px, 16px and a 24px SemiBold title. A 5x8 face at a
// scale is a multiple of its cell; the clean face cannot do that and keep all
// three sizes usable, so [Font.TextHeight] and [Font.LineHeight] carry the
// per-scale row heights instead.
//
// A glyph's raster is as tall as the whole line box for its scale — the band
// between the top of the ascent and the bottom of the descent — and the
// baseline sits inside it, so a ragged mix of descenders and tall characters
// still types on one true baseline. Rasters are resolved once per rune per
// scale on first use and cached (see chain_font.go), so a long session pays
// the rasterisation cost once per distinct character it shows.

import (
	_ "embed"
	"image"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

//go:embed assets/Inter-Regular.ttf
var interRegular []byte

//go:embed assets/Inter-SemiBold.ttf
var interSemiBold []byte

const (
	cleanMaxScale = 6
)

var (
	// The three small scales match the bitmap faces' Small, Body and Title
	// rows (11/16/24px); the three large ones carry the HiDPI theme scales
	// (a 2x interface draws body text at scale 4, titles higher). Scales
	// above 6 clamp: the raster bound in fonts.go sizes the cache entries.
	cleanSize = [cleanMaxScale + 1]int{0: 0, 1: 11, 2: 16, 3: 24, 4: 32, 5: 40, 6: 48}
	cleanBase = [cleanMaxScale + 1]int{0: 0, 1: 12, 2: 17, 3: 25, 4: 33, 5: 41, 6: 49}
	cleanCell = [cleanMaxScale + 1]int{0: 0, 1: 16, 2: 22, 3: 32, 4: 43, 5: 53, 6: 63}
	cleanLine = [cleanMaxScale + 1]int{0: 0, 1: 18, 2: 24, 3: 34, 4: 45, 5: 55, 6: 65}
	cleanAvg  = [cleanMaxScale + 1]int{0: 0, 1: 6, 2: 9, 3: 13, 4: 18, 5: 22, 6: 26}
)

// CleanFont is the clean face the "clean" style draws with: Inter where it
// reaches, DejaVu Sans behind it (see chain_font.go).
var CleanFont = Font{
	GlyphW:       cleanAvg[1],
	GlyphH:       cleanCell[1],
	GlyphAdvance: cleanAvg[1],
	LineAdvance:  cleanLine[1],
	Glyph:        chainRaster,
	Advance:      chainGlyphAdvance,
	TextHeight:   cleanTextHeight,
	LineHeight:   cleanLineHeight,
	Covers:       chainCovers,
}

// cleanScale clamps a requested scale into the range the face is built for.
func cleanScale(scale int) int {
	if scale < 1 {
		return 1
	}
	if scale > cleanMaxScale {
		return cleanMaxScale
	}
	return scale
}

// cleanTextHeight and cleanLineHeight are the face's per-scale row heights,
// because non-multiple sizes cannot be described by one base cell.
func cleanTextHeight(scale int) int { return cleanCell[cleanScale(scale)] }
func cleanLineHeight(scale int) int { return cleanLine[cleanScale(scale)] }

// rasterizeGlyph draws one rune into a cell exactly as wide as its ink and
// returns the ink coverage. The advance that steps past the cell is the
// face's own, so left and right side bearings are preserved and a glyph never
// loses an extremity.
//
// The mask arrives positioned by the face such that the destination row for
// mask pixel my is my + dr.Min.Y - maskp.Y: read the alpha by inverting that
// mapping for each dr row, and no intermediate image is allocated. Getting
// this wrong moves every glyph up or down by its own ink top — a line then
// looks like it types on stairs rather than a baseline.
func rasterizeGlyph(face font.Face, r rune, baseY, cellH int) Raster {
	var out Raster
	dr, mask, maskp, _, ok := face.Glyph(fixed.P(0, baseY), r)
	if !ok {
		return out
	}
	am, ok := mask.(*image.Alpha)
	if !ok {
		return out
	}
	w := dr.Dx()
	if w > MaxRasterW {
		w = MaxRasterW
	}
	out.W, out.H = w, cellH
	for y := dr.Min.Y; y < dr.Max.Y; y++ {
		if y < 0 || y >= cellH {
			continue
		}
		my := y - dr.Min.Y + maskp.Y
		for x := dr.Min.X; x < dr.Max.X; x++ {
			i := x - dr.Min.X
			if i >= w {
				break
			}
			mx := x - dr.Min.X + maskp.X
			out.Px[y*w+i] = am.AlphaAt(mx, my).A
		}
	}
	return out
}

// cleanMissing is the clean face's missing-glyph box: a hollow rectangle
// about as wide as a typical glyph, the same convention the bitmap faces use.
func cleanMissing(scale int) Raster {
	w, h := cleanAvg[scale], cleanCell[scale]
	var out Raster
	out.W, out.H = w, h
	for x := 0; x < w; x++ {
		out.Px[x] = 255
		out.Px[(h-1)*w+x] = 255
	}
	for y := 0; y < h; y++ {
		out.Px[y*w] = 255
		out.Px[y*w+w-1] = 255
	}
	return out
}
