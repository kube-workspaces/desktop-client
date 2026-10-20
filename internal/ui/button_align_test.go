// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import "testing"

// A menu row pins its glyph to the left edge so a column of rows lines up,
// while the default stays centred for footers and toolbars.
func TestButtonIconAlignment(t *testing.T) {
	h := newHarness(200, 100)
	th := h.ctx.Theme
	size := th.ControlHeight / 2
	probe := Point{
		X: buttonRect.X + th.Gap + 1,
		Y: buttonRect.Y + (buttonRect.H-size)/2 + size/2,
	}
	img := h.ctx.Canvas.Image()
	draw := func(b *Button) func(*Context) {
		return func(ctx *Context) {
			ctx.Canvas.Fill(ctx.Canvas.Bounds(), th.Background)
			b.Layout(ctx, buttonRect)
		}
	}

	left := &Button{Text: "Open", Variant: ButtonQuiet, Icon: ButtonIconPlay, Align: ButtonAlignLeft}
	h.frame(nil, draw(left))
	if got := img.RGBAAt(probe.X, probe.Y); got != th.TextMuted {
		t.Fatalf("left-aligned icon at (%d,%d) = %v, want the muted ink", probe.X, probe.Y, got)
	}

	centred := &Button{Text: "Open", Variant: ButtonQuiet, Icon: ButtonIconPlay}
	h.frame(nil, draw(centred))
	if got := img.RGBAAt(probe.X, probe.Y); got != th.Background {
		t.Fatalf("centred button leaked content into the left margin: %v", got)
	}
}
