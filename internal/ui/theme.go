// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import "image/color"

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

	// Body, Title and Small are integer glyph scale factors. The font is a
	// 5x8 bitmap, so text is only ever drawn at a whole multiple of its own
	// size: a scaled bitmap font with fractional steps is mush.
	Body  int
	Title int
	Small int

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

// DefaultTheme returns the client's palette: a muted, low-contrast dark
// scheme.
//
// Dark, because this is a window that spends its life next to a remote desktop
// and should not be the brightest thing on the screen. Muted, because the only
// saturated colour in the interface is the accent, and that is reserved for
// the primary action and the keyboard focus; everything that competes with
// those makes both harder to find.
func DefaultTheme() *Theme {
	return &Theme{
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
