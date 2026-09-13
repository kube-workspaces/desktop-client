// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

// Raster and Font are the interface between a typeface and the software that
// draws it (internal/ui, and this package's own status overlay).
//
// A Raster is one glyph's ink as per-cell coverage, 0..255 per pixel, so it can
// carry both the 1-bit block cells of the two bitmap faces and the antialiased
// pixels of the rasterised clean face. Plotting a Raster is exactly one code
// path in the ui layer; the faces differ only in the Rasters they return.
//
// A Font is a face plus its metrics at the scales it is drawn at. The four
// integer cells describe the bitmap faces and are what every monospace
// measurement in internal/ui uses. The clean face is proportional, so it also
// carries its own per-glyph advances and per-scale heights; when those fields
// are nil the integer cells are used, which is how the layout code keeps one
// code path for both without knowing which face it is measuring.
type Raster struct {
	// W and H are the raster's size in pixels: the face's cell multiplied by
	// the scale the Glyph was called with.
	W, H int
	// Px holds the coverage, row-major, W*H entries. 0 is background, 255 is
	// fully inked ink, and the values between are antialiasing.
	Px [MaxRasterW * MaxRasterH]uint8
}

// Font is a typeface the ui can draw and measure.
type Font struct {
	// GlyphW and GlyphH are the cell size at scale 1; GlyphAdvance the
	// horizontal step from one glyph to the next; LineAdvance the vertical
	// step from one line to the next. For the clean face these are scale-1
	// placeholders that the accessors below override.
	GlyphW, GlyphH, GlyphAdvance, LineAdvance int
	// Glyph returns the raster for r at the given integer scale. Runes the
	// face cannot draw come back as a hollow box, exactly like the bitmap
	// faces' missingGlyph, so unknown text is visible rather than lost.
	Glyph func(r rune, scale int) Raster

	// Advance returns the horizontal step for r at the given scale, in
	// pixels. nil means every glyph advances by GlyphAdvance*scale, which is
	// exact for the bitmap faces and is what they were laid out with.
	Advance func(r rune, scale int) int
	// TextHeight returns the height of one line at the given scale and
	// LineHeight the distance to the next line. nil means GlyphH*scale and
	// LineAdvance*scale. The clean face overrides them because its three
	// scales (11/16/24px) are not multiples of one another.
	TextHeight func(scale int) int
	LineHeight func(scale int) int
}

// MaxRasterW and MaxRasterH bound every Raster this package produces, so the
// raster can live in a fixed array with no allocation. They cover the largest
// cell any face draws: the clean face's 24px title row, whose ink is at most
// 24px wide and whose raster is 32 rows tall.
const (
	MaxRasterW = 26
	MaxRasterH = 34
)

// rasterScaleCap is the largest scale a glyph will be asked at. The ui uses
// its theme's Body/Title/Small scales, all well under this.
const rasterScaleCap = 3

// RetroFont is the client's original 5x8 face, exactly as shipped.
var RetroFont = Font{
	GlyphW:       GlyphWidth,
	GlyphH:       GlyphHeight,
	GlyphAdvance: GlyphAdvance,
	LineAdvance:  LineAdvance,
	Glyph:        bitmapRaster(GlyphFor),
}

// BubblyFont is the chunky 5x8 face, same cell and metrics as RetroFont.
var BubblyFont = Font{
	GlyphW:       GlyphWidth,
	GlyphH:       GlyphHeight,
	GlyphAdvance: GlyphAdvance,
	LineAdvance:  LineAdvance,
	Glyph:        bitmapRaster(BubblyGlyphFor),
}

// bitmapRaster turns one of the 1-bit 5x8 faces into per-cell coverage by
// expanding each inked bit into a scale-by-scale block of fully covered
// pixels. That is byte-for-byte how the ui used to stamp these fonts, so the
// pixel-identical guarantee on the retro face is preserved by construction.
func bitmapRaster(get func(rune) Glyph) func(rune, int) Raster {
	return func(r rune, scale int) Raster {
		if scale < 1 {
			scale = 1
		}
		if scale > rasterScaleCap {
			scale = rasterScaleCap
		}
		g := get(r)
		w, h := GlyphWidth*scale, GlyphHeight*scale
		var out Raster
		out.W, out.H = w, h
		for y := 0; y < GlyphHeight; y++ {
			bits := g[y]
			if bits == 0 {
				continue
			}
			for x := 0; x < GlyphWidth; x++ {
				if bits&(1<<(GlyphWidth-1-x)) == 0 {
					continue
				}
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						out.Px[(y*scale+dy)*w+x*scale+dx] = 255
					}
				}
			}
		}
		return out
	}
}
