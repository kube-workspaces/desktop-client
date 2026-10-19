// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

type workspaceContextMenu struct {
	key     string
	anchor  ui.Point
	scroll  int
	focus   ui.FocusID
	swallow bool
}

func (a *App) closeWorkspaceMenu() {
	a.workspaceMenu.key = ""
	a.workspaceMenu.scroll = 0
	a.ctx.Focus().Begin()
	a.ctx.Focus().Register(a.list.ID)
	a.ctx.Focus().Set(a.list.ID)
	a.ctx.Repaint()
}

func (a *App) drawWorkspacesWithContextMenu(bounds ui.Rect) intent {
	ctx := a.ctx
	wasOpen := a.workspaceMenu.key != ""
	blocked := wasOpen || a.workspaceMenu.swallow
	var background *ui.Context
	if blocked {
		background = ui.NewContext(a.opts.Theme)
		background.Begin(a.canvas, ui.Input{Now: ctx.Input.Now})
		a.ctx = background
	}
	out := a.drawWorkspacesWithUserMenu(bounds)
	a.ctx = ctx
	if background != nil {
		if delay, ok := background.RepaintDelay(); ok {
			ctx.RepaintAfter(delay)
		}
		out = intent{}
	}
	if a.workspaceMenu.key != "" {
		input := ctx.Input
		if !wasOpen {
			ctx.Input = ui.Input{Now: input.Now}
		}
		out = a.drawWorkspaceMenu(bounds)
		ctx.Input = input
	}
	if a.workspaceMenu.swallow && !ctx.Input.Down {
		a.workspaceMenu.swallow = false
	}
	return out
}

func (a *App) drawWorkspaceMenu(bounds ui.Rect) intent {
	ctx, th := a.ctx, a.opts.Theme
	m := &a.workspaceMenu
	// Resolve by key on every frame: watch snapshots can change state or remove
	// the workspace while the menu is open.
	rows := a.m.Filtered()
	index := SelectedIndex(rows, m.key)
	if index < 0 {
		a.closeWorkspaceMenu()
		return intent{}
	}
	ws := rows[index]
	actions := workspaceActions(ws, true)
	outer := ui.Inset(bounds, th.Gap)
	w := 0
	for _, action := range actions {
		w = max(w, action.button.Width(ctx)+2*th.Gap)
	}
	w = min(w, outer.W)
	contentH := len(actions) * th.ControlHeight
	h := min(contentH+2*th.BorderWidth, outer.H)
	r := ui.Rect{X: max(outer.X, min(m.anchor.X, outer.X+outer.W-w)), Y: max(outer.Y, min(m.anchor.Y, outer.Y+outer.H-h)), W: w, H: h}
	if ctx.Input.KeyPressed(keysym.KeyEscape) || ctx.Input.RightPressed || (ctx.Input.Pressed && !ctx.Input.Mouse.In(r)) {
		m.swallow = ctx.Input.Down
		a.closeWorkspaceMenu()
		return intent{}
	}
	ctx.Focus().Begin()
	for _, action := range actions {
		if !action.button.Disabled {
			ctx.Focus().Register(action.button.ID)
		}
	}
	ctx.Focus().Normalise()
	move := 0
	if ctx.Input.KeyPressed(keysym.KeyDown) {
		move = 1
	}
	if ctx.Input.KeyPressed(keysym.KeyUp) {
		move = -1
	}
	if move != 0 {
		ctx.Focus().Move(move)
		ctx.Repaint()
	}
	view := ui.Inset(r, th.BorderWidth)
	if ctx.Input.Hovering(r) {
		m.scroll -= ctx.Input.Wheel.Y * th.ControlHeight
	}
	if move != 0 || m.focus != ctx.Focus().Focus() {
		for i, action := range actions {
			if ctx.Focused(action.button.ID) {
				top := i * th.ControlHeight
				m.scroll = max(top+th.ControlHeight-view.H, min(m.scroll, top))
			}
		}
	}
	m.focus = ctx.Focus().Focus()
	m.scroll = max(0, min(m.scroll, contentH-view.H))
	ctx.Canvas.FillRounded(r, th.Radius, th.Surface)
	ctx.Canvas.StrokeRounded(r, th.Radius, th.BorderWidth, th.BorderStrong)
	ctx.Canvas.PushClip(view)
	var out intent
	// Buttons register themselves during layout; the first pass above exists
	// only to resolve arrow traversal before drawing the focused item.
	ctx.Focus().Begin()
	for i, action := range actions {
		box := ui.Rect{X: view.X, Y: view.Y + i*th.ControlHeight - m.scroll, W: view.W, H: th.ControlHeight}
		hit := ui.Intersect(box, view)
		b := action.button
		b.Variant, b.Raised = ui.ButtonQuiet, false
		input := ctx.Input
		if !ctx.Input.Mouse.In(hit) {
			ctx.Input.Pressed, ctx.Input.Released, ctx.Input.Down = false, false, false
		}
		if b.Layout(ctx, box) {
			out = intent{kind: action.kind, workspace: ws}
		}
		ctx.Input = input
	}
	ctx.Canvas.PopClip()
	if out.kind != intentNone {
		a.closeWorkspaceMenu()
	}
	return out
}
