// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"image/color"

	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// Style selects the typography and geometry of a theme.
//
// There are two, and both share the same two colour palettes: a style is about
// how text and chrome are drawn, and [Mode] is about how the whole thing is
// coloured. Keeping the two axes orthogonal is what lets a user switch retro
// colours to light without also losing the chunky glyphs.
type Style int

// The styles. StyleBubbly is the default and the chunky, 8-bit look; StyleRetro
// is the client's original design, kept exactly as it was shipped in v0.1.0;
// StyleClean is the rasterised, antialiased face.
const (
	StyleBubbly Style = iota
	StyleRetro
	StyleClean
)

// String implements fmt.Stringer and is also the name persisted in config.
func (s Style) String() string {
	switch s {
	case StyleBubbly:
		return "bubbly"
	case StyleRetro:
		return "retro"
	case StyleClean:
		return "clean"
	default:
		return "bubbly"
	}
}

// ParseStyle reads the name [Style.String] writes. It reports false for
// anything unknown, leaving the caller to fall back to the default.
//
// "modern" is accepted as a legacy alias for the bubbly style: the style was
// renamed after shipping, and settings files written before the rename still
// say "modern".
func ParseStyle(s string) (Style, bool) {
	switch s {
	case "bubbly", "modern":
		return StyleBubbly, true
	case "retro":
		return StyleRetro, true
	case "clean":
		return StyleClean, true
	default:
		return StyleBubbly, false
	}
}

// Mode selects the light or dark colour scheme.
type Mode int

// The modes. ModeDark matches the palette the client launched with.
const (
	ModeDark Mode = iota
	ModeLight
)

// String implements fmt.Stringer and is also the name persisted in config.
func (m Mode) String() string {
	switch m {
	case ModeDark:
		return "dark"
	case ModeLight:
		return "light"
	default:
		return "dark"
	}
}

// ParseMode reads the name [Mode.String] writes. It reports false for anything
// unknown, leaving the caller to fall back to the default.
func ParseMode(s string) (Mode, bool) {
	switch s {
	case "dark":
		return ModeDark, true
	case "light":
		return ModeLight, true
	default:
		return ModeDark, false
	}
}

// Theme is every colour and measurement the widgets use.
//
// It is one struct in one file on purpose. A palette scattered across the
// widgets that happen to need it is how an interface ends up with four
// not-quite-identical greys, and a tool people stare at all day should look
// like it was designed rather than accumulated.
type Theme struct {
	// Background is the window behind everything.
	Background color.RGBA
	// Surface is a panel, a card or a list row.
	Surface color.RGBA
	// SurfaceAlt is a raised or interactive surface: an input field, a
	// hovered row, a secondary button.
	SurfaceAlt color.RGBA
	// SurfaceSelected is the background of the selected list row.
	SurfaceSelected color.RGBA
	// Border outlines surfaces at rest.
	Border color.RGBA
	// BorderStrong outlines a hovered or pressed control.
	BorderStrong color.RGBA

	// Text is body copy. TextMuted is secondary information — a namespace, a
	// hint, a timestamp — and TextOnAccent is text drawn over Accent.
	Text         color.RGBA
	TextMuted    color.RGBA
	TextOnAccent color.RGBA
	// TextDisabled is a control the user cannot currently use.
	TextDisabled color.RGBA

	// Accent is the single colour that means "this is the primary action" or
	// "this has the keyboard". Using one colour for both is what makes focus
	// legible without a second visual language.
	Accent color.RGBA
	// AccentHover is Accent under the pointer.
	AccentHover color.RGBA
	// Focus is the focus ring. It is Accent at reduced weight so that focus
	// reads as emphasis rather than as a fifth kind of border.
	Focus color.RGBA

	// Danger, Success and Warning are status colours, used for the workspace
	// state dots and for error banners.
	Danger  color.RGBA
	Success color.RGBA
	Warning color.RGBA
	// DangerSurface is the background of an error banner.
	DangerSurface color.RGBA

	// Body, Title and Small are integer glyph scale factors. For the bitmap
	// faces a scaled font with fractional steps is mush, so text is only
	// drawn at whole multiples of the cell. The clean face is a different
	// typeface per scale (11/16/24px), and carries its own per-scale heights,
	// so the three numbers select from that scale set instead.
	Body  int
	Title int
	Small int

	// Font is the typeface the theme draws with. The bitmap faces are 5x8;
	// the clean face is a proportional antialiased raster, and the Font
	// carrying metrics for both is what lets one layout arithmetic cover the
	// two without the layout knowing which face it is measuring.
	Font viewer.Font

	// Pad is the standard inset from the edge of a container, Gap the
	// standard space between two controls, and Radius the corner radius of
	// every rounded rectangle.
	Pad    int
	Gap    int
	Radius int

	// ControlHeight is the height of a button or a text field; RowHeight the
	// height of one list row.
	ControlHeight int
	RowHeight     int

	// BorderWidth and FocusWidth are stroke widths in pixels.
	BorderWidth int
	FocusWidth  int
}

// ThemeFor returns the theme for a style and a colour mode.
func ThemeFor(style Style, mode Mode) *Theme {
	t := palette(mode)
	switch style {
	case StyleBubbly:
		bubbly(&t)
	case StyleClean:
		clean(&t)
	default:
		t.Font = viewer.RetroFont
	}
	return &t
}

// DefaultTheme returns the client's default look: the bubbly style in dark
// mode. It is what an unconfigured client opens with, which is why
// "bubbly, dark" is also the fallback for settings that name nothing.
func DefaultTheme() *Theme { return ThemeFor(StyleBubbly, ModeDark) }

// palette returns the colours for a mode, with the retro metrics baked in so
// that StyleRetro is exactly the original theme — a user who switches back
// should get back the interface they had, pixel for pixel.
func palette(mode Mode) Theme {
	if mode == ModeLight {
		return lightPalette()
	}
	return darkPalette()
}

// darkPalette is the original, larger-than-life design: a muted, low-contrast
// dark scheme.
//
// Dark, because this is a window that spends its life next to a remote desktop
// and should not be the brightest thing on the screen. Muted, because the only
// saturated colour in the interface is the accent, and that is reserved for
// the primary action and the keyboard focus; everything that competes with
// those makes both harder to find.
func darkPalette() Theme {
	return Theme{
		Background:      rgb(0x14, 0x16, 0x1a),
		Surface:         rgb(0x1c, 0x1f, 0x25),
		SurfaceAlt:      rgb(0x24, 0x28, 0x30),
		SurfaceSelected: rgb(0x2c, 0x34, 0x44),
		Border:          rgb(0x2e, 0x34, 0x3e),
		BorderStrong:    rgb(0x42, 0x4a, 0x58),

		Text:         rgb(0xe3, 0xe6, 0xea),
		TextMuted:    rgb(0x8a, 0x93, 0xa1),
		TextOnAccent: rgb(0xf5, 0xf7, 0xfa),
		TextDisabled: rgb(0x5a, 0x62, 0x6e),

		Accent:      rgb(0x3f, 0x6f, 0xc4),
		AccentHover: rgb(0x4e, 0x81, 0xd8),
		Focus:       rgba(0x6d, 0x9b, 0xe8, 0xd0),

		Danger:        rgb(0xc4, 0x5c, 0x5c),
		Success:       rgb(0x56, 0xa0, 0x6e),
		Warning:       rgb(0xc7, 0xa1, 0x4c),
		DangerSurface: rgb(0x3a, 0x22, 0x24),

		Body:  2,
		Title: 3,
		Small: 1,

		Pad:    20,
		Gap:    12,
		Radius: 6,

		ControlHeight: 34,
		RowHeight:     46,

		BorderWidth: 1,
		FocusWidth:  2,
	}
}

// lightPalette is the same interface with the lights on: a near-white
// background with toned-down ink and a slightly deeper accent so text on it
// keeps the same weight it has over dark.
func lightPalette() Theme {
	return Theme{
		Background:      rgb(0xf6, 0xf7, 0xf9),
		Surface:         rgb(0xff, 0xff, 0xff),
		SurfaceAlt:      rgb(0xeb, 0xee, 0xf2),
		SurfaceSelected: rgb(0xd9, 0xe1, 0xee),
		Border:          rgb(0xd6, 0xda, 0xe0),
		BorderStrong:    rgb(0xb4, 0xbc, 0xc6),

		Text:         rgb(0x1e, 0x23, 0x2a),
		TextMuted:    rgb(0x5d, 0x66, 0x73),
		TextOnAccent: rgb(0xff, 0xff, 0xff),
		TextDisabled: rgb(0x9b, 0xa3, 0xac),

		Accent:      rgb(0x2f, 0x5f, 0xb3),
		AccentHover: rgb(0x3a, 0x6e, 0xc8),
		Focus:       rgba(0x2f, 0x5f, 0xb3, 0xd0),

		Danger:        rgb(0xc0, 0x39, 0x2b),
		Success:       rgb(0x2d, 0x8a, 0x54),
		Warning:       rgb(0xa8, 0x82, 0x0a),
		DangerSurface: rgb(0xfb, 0xea, 0xe8),

		Body:  2,
		Title: 3,
		Small: 1,

		Pad:    20,
		Gap:    12,
		Radius: 6,

		ControlHeight: 34,
		RowHeight:     46,

		BorderWidth: 1,
		FocusWidth:  2,
	}
}

// bubbly restyles the palette in place for StyleBubbly: text is drawn in the
// chunky geometric face at the same sizes the retro style uses, corners go
// flat, density goes up and the chrome shrinks to match. The colours are
// untouched — style and colour are two axes, and [ThemeFor] keeps them that
// way.
func bubbly(t *Theme) {
	t.Body, t.Title, t.Small = 2, 3, 1
	t.Pad, t.Gap, t.Radius = 14, 8, 2
	t.ControlHeight, t.RowHeight = 30, 32
	t.FocusWidth = 1
	t.Font = viewer.BubblyFont
}

// clean restyles the palette in place for StyleClean: the same flat corners
// and density as the bubbly style, but the rasterised face is drawn at its
// three natural sizes — Small 11px, Body 16px, Title 24px SemiBold — which
// mirror the bitmap faces' Small/Body/Title rows instead of borrowing their
// scale indices. The self-describing per-scale heights (see
// viewer.CleanFont.TextHeight) are what let the one layout arithmetic cover
// both kinds of face.
func clean(t *Theme) {
	t.Body, t.Title, t.Small = 2, 3, 1
	t.Pad, t.Gap, t.Radius = 16, 10, 2
	t.ControlHeight, t.RowHeight = 34, 46
	t.FocusWidth = 1
	t.Font = viewer.CleanFont
}

// rgb builds an opaque colour.
func rgb(r, g, b uint8) color.RGBA { return color.RGBA{R: r, G: g, B: b, A: 255} }

// rgba builds a colour with straight (not premultiplied) alpha; see
// [Canvas.Fill] for how it is composited.
func rgba(r, g, b, a uint8) color.RGBA { return color.RGBA{R: r, G: g, B: b, A: a} }

// Transparent is the zero colour: drawing with it does nothing. Widgets use it
// as "unset" in their option structs, so a zero-valued option means "take the
// theme's value" without needing a pointer.
var Transparent = color.RGBA{}

// or returns c unless it is unset, in which case it returns fallback.
func or(c, fallback color.RGBA) color.RGBA {
	if c == Transparent {
		return fallback
	}
	return c
}

// orInt returns v unless it is zero or negative, in which case it returns
// fallback.
func orInt(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}
