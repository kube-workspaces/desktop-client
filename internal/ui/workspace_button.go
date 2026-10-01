// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

// NewWorkspaceButton is a quiet monitor-with-plus action for creating a workspace.
type NewWorkspaceButton struct {
	ID FocusID
}

// Layout draws the icon with the usual button focus and activation behavior.
func (b NewWorkspaceButton) Layout(ctx *Context, r Rect) bool {
	button := Button{ID: b.ID, Variant: ButtonQuiet}
	activated := button.Layout(ctx, r)
	size := min(r.W, r.H)
	if size <= 0 {
		return activated
	}
	x, y := r.X+(r.W-size)/2, r.Y+(r.H-size)/2
	stroke := max(1, size/20)
	ink := ctx.Theme.Text
	ctx.Canvas.PushClip(r)
	defer ctx.Canvas.PopClip()

	// Keep the plus outside the screen outline so both symbols stay legible.
	screen := Rect{X: x + size*14/100, Y: y + size*30/100, W: size * 52 / 100, H: size * 38 / 100}
	ctx.Canvas.StrokeRounded(screen, max(2, size/16), stroke, ink)
	stemX := screen.X + (screen.W-stroke)/2
	ctx.Canvas.Fill(Rect{X: stemX, Y: screen.Y + screen.H, W: stroke, H: size * 12 / 100}, ink)
	ctx.Canvas.Fill(Rect{X: x + size*27/100, Y: y + size*78/100, W: size * 26 / 100, H: stroke}, ink)

	plusSize := max(5, size*22/100)
	plusX, plusY := x+size*80/100, y+size*25/100
	ctx.Canvas.Fill(Rect{X: plusX - plusSize/2, Y: plusY - stroke/2, W: plusSize, H: stroke}, ink)
	ctx.Canvas.Fill(Rect{X: plusX - stroke/2, Y: plusY - plusSize/2, W: stroke, H: plusSize}, ink)
	return activated
}
