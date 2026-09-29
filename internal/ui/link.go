// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

// Link is a clickable hyperlink: accent body text with an underline,
// activated by a complete click inside it, or by Enter or Space while it
// has the keyboard. It is single-line by design — a target that wraps is
// still one target, and the hit box stays the row.
type Link struct {
	// ID identifies the link to the focus ring. A link with no ID is not
	// focusable and can only be clicked.
	ID FocusID
	// Text is both the label and, by convention, the target the caller
	// opens on activation.
	Text string
	// Scale overrides the theme's body scale.
	Scale int
}

// Width reports the untruncated label width, for laying the link out.
func (l *Link) Width(ctx *Context) int {
	scale := orInt(l.Scale, ctx.Theme.Body)
	return TextWidth(l.Text, scale, ctx.Theme.Font)
}

// Layout draws the link in r and reports whether it was activated.
//
// Activation mirrors [Button]: a complete click inside r, or Enter or Space
// while the link has the keyboard.
func (l *Link) Layout(ctx *Context, r Rect) bool {
	th := ctx.Theme
	focused := false
	if l.ID != NoFocus {
		focused = ctx.register(l.ID, r)
	}

	scale := orInt(l.Scale, ctx.Theme.Body)
	ink := th.Accent
	if ctx.Input.Hovering(r) {
		ink = th.AccentHover
	}
	line := Truncate(l.Text, scale, th.Font, r.W)
	Label(ctx, r, line, LabelStyle{Color: ink, Scale: scale})
	// The underline doubles as the focus indicator: links never draw
	// the boxed ring buttons use, which reads as a selection box
	// around text.
	if line != "" {
		w := TextWidth(line, scale, th.Font)
		y := r.Y + TextHeight(scale, th.Font)
		uw, uink := 1, ink
		if focused {
			uw, uink = th.FocusWidth, th.Focus
		}
		ctx.Canvas.Line(r.X, y, r.X+w, y, uw, uink)
	}

	if ctx.Input.ClickedIn(r) {
		return true
	}
	return focused && (ctx.Input.KeyPressed(keysym.KeyReturn) ||
		ctx.Input.KeyPressed(keysym.KeyKPEnter) ||
		ctx.Input.RuneChord(keysym.ModNone, ' '))
}
