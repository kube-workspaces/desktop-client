// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"image/color"
	"strconv"
)

// ButtonIcon is a software-rendered action glyph.
type ButtonIcon int

const (
	ButtonIconNone ButtonIcon = iota
	ButtonIconFocus
	ButtonIconClose
)

func drawButtonIcon(c *Canvas, r Rect, icon ButtonIcon, ink color.RGBA) {
	s := max(1, r.W/10)
	switch icon {
	case ButtonIconFocus:
		// Four corner brackets: "this window, here" without pretending to
		// be a pointer, and legible at every scale.
		arm := max(2*s, r.W*3/10)
		top, bottom := r.Y, r.Y+r.H-s
		left, right := r.X, r.X+r.W-s
		c.Fill(Rect{X: left, Y: top, W: arm, H: s}, ink)
		c.Fill(Rect{X: left, Y: top, W: s, H: arm}, ink)
		c.Fill(Rect{X: right + s - arm, Y: top, W: arm, H: s}, ink)
		c.Fill(Rect{X: right, Y: top, W: s, H: arm}, ink)
		c.Fill(Rect{X: left, Y: bottom, W: arm, H: s}, ink)
		c.Fill(Rect{X: left, Y: bottom + s - arm, W: s, H: arm}, ink)
		c.Fill(Rect{X: right + s - arm, Y: bottom, W: arm, H: s}, ink)
		c.Fill(Rect{X: right, Y: bottom + s - arm, W: s, H: arm}, ink)
	case ButtonIconClose:
		// Square pixels along both diagonals keep the cross crisp at every scale.
		for i := 2 * s; i < r.W-2*s; i++ {
			c.Fill(Rect{X: r.X + i, Y: r.Y + i, W: s, H: s}, ink)
			c.Fill(Rect{X: r.X + r.W - i - s, Y: r.Y + i, W: s, H: s}, ink)
		}
	}
}

// SessionsButton pairs stacked windows with an accent count badge, matching
// the quiet icon-only Add workspace action beside it.
type SessionsButton struct {
	ID    FocusID
	Count int
}

func (b SessionsButton) Layout(ctx *Context, r Rect) bool {
	button := Button{ID: b.ID, Variant: ButtonQuiet}
	activated := button.Layout(ctx, r)
	size := min(r.W, r.H)
	if size <= 0 {
		return activated
	}
	th := ctx.Theme
	ctx.Canvas.PushClip(r)
	defer ctx.Canvas.PopClip()
	s := max(1, size/20)

	// The stacked windows sit low and left, leaving the top-right corner to
	// the badge so the two read as one glyph rather than as a pile-up.
	back := Rect{X: r.X + size*10/100, Y: r.Y + size*30/100, W: size * 48 / 100, H: size * 48 / 100}
	front := Rect{X: back.X + size*12/100, Y: back.Y + size*14/100, W: back.W, H: back.H}
	ctx.Canvas.StrokeRounded(back, 2*s, s, th.Text)
	ctx.Canvas.FillRounded(front, 2*s, th.Background)
	ctx.Canvas.StrokeRounded(front, 2*s, s, th.Text)

	if b.Count > 0 {
		text := strconv.Itoa(b.Count)
		if b.Count > 99 {
			text = "99+"
		}
		// Inset past the focus ring so a focused Sessions button still
		// shows its whole outline.
		inset := th.FocusWidth + 1
		h := TextHeight(th.Small, th.Font) + 2*s
		w := max(h, TextWidth(text, th.Small, th.Font)+3*s)
		badge := Rect{X: r.X + r.W - w - inset, Y: r.Y + inset, W: w, H: h}
		ctx.Canvas.FillRounded(badge, h/2, th.Accent)
		Label(ctx, badge, text, LabelStyle{Color: th.TextOnAccent, Scale: th.Small, Align: AlignCenter, Middle: true})
	}
	return activated
}
