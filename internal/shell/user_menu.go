// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"image/color"
	"math"

	"github.com/kube-workspaces/desktop-client/internal/i18n"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

type userMenuAction struct {
	id    ui.FocusID
	label string
	icon  string
	kind  intentKind
}

func (a *App) userMenuActions() []userMenuAction {
	rows := []userMenuAction{
		{idAccount, i18n.Get("account.title"), "person", intentOpenAccount},
		{idSettings, i18n.Get("header.settings"), "gear", intentOpenSettings},
		{idProfiles, i18n.Get("header.profiles"), "users", intentOpenProfiles},
	}
	if a.updates.result.Available {
		rows = append(rows, userMenuAction{idUpdates, i18n.Get("updates.availableBadge"), "update", intentUpdates})
	}
	return append(rows, userMenuAction{idSignOut, i18n.Get("header.signout"), "exit", intentSignOut})
}

func (a *App) userMenuInfo() []string {
	var lines []string
	if who := a.m.Identity; who != nil {
		if who.DisplayName != "" {
			lines = append(lines, who.DisplayName)
		}
		if who.Email != "" && who.Email != who.DisplayName {
			lines = append(lines, who.Email)
		}
	}
	if len(lines) == 0 {
		lines = append(lines, i18n.Get("header.noAuth"))
	}
	if a.profile != nil && a.profile.Name != "" {
		lines = append(lines, i18n.Sprintf("header.profile", a.profile.Name))
	}
	lines = append(lines, i18n.Sprintf("header.endpoint", a.m.Server))
	if a.m.Identity != nil && a.m.Identity.Role != "" {
		lines = append(lines, i18n.Sprintf("header.role", a.m.Identity.Role))
	}
	if warn := sessionExpiryWarning(a.m.TokenExpiry, a.ctx.Input.Now); warn != "" {
		lines = append(lines, warn)
	}
	return lines
}

func (a *App) userMenuAnchor(bounds ui.Rect) ui.Rect {
	th := a.opts.Theme
	header, _ := ui.CutTop(ui.Inset(bounds, th.Pad), ui.TextHeight(th.Title, th.Font)+th.Gap)
	avatar, _ := ui.CutRight(header, header.H)
	return avatar
}

// The background gets a drawing-only context while the popup owns input and
// focus. It still renders fresh workspace data, without a frozen modal backdrop.
func (a *App) drawWorkspacesWithUserMenu(bounds ui.Rect) intent {
	ctx := a.ctx
	wasOpen := a.userMenuOpen
	blocked := wasOpen || a.userMenuSwallow
	var background *ui.Context
	if blocked {
		background = ui.NewContext(a.opts.Theme)
		background.Begin(a.canvas, ui.Input{Now: ctx.Input.Now})
		a.ctx = background
	}
	out := a.drawWorkspacesScreen(bounds)
	a.ctx = ctx
	if background != nil {
		if delay, ok := background.RepaintDelay(); ok {
			ctx.RepaintAfter(delay)
		}
		if background.NeedsRepaint() {
			ctx.Repaint()
		}
	}
	anchor := a.userMenuAnchor(bounds)
	if blocked {
		// Redraw the trigger with the real context, above the inert background.
		ctx.Focus().Begin()
		if a.drawAvatar(anchor) {
			a.closeUserMenu()
		}
		out = intent{}
	}
	if a.userMenuOpen {
		if !wasOpen {
			// Opening Enter/click must not activate the first menu action too.
			input := ctx.Input
			ctx.Input = ui.Input{Now: input.Now}
			ctx.Focus().Set(idAccount)
			out = a.drawUserMenu(bounds, anchor)
			ctx.Input = input
		} else {
			out = a.drawUserMenu(bounds, anchor)
		}
	} else if !blocked && ctx.Input.Hovering(anchor) {
		a.drawUserMenuTooltip(bounds, anchor)
	}
	if a.userMenuSwallow && !ctx.Input.Down {
		a.userMenuSwallow = false
	}
	return out
}

func (a *App) closeUserMenu() {
	a.userMenuOpen = false
	a.userMenuScroll = 0
	a.userMenuFocus = ui.NoFocus
	a.ctx.Focus().Set(idUserMenu)
	a.ctx.Focus().Begin()
	a.ctx.Focus().Register(idUserMenu)
	a.ctx.Repaint()
}

func (a *App) drawAvatar(r ui.Rect) bool {
	ctx, th := a.ctx, a.opts.Theme
	ctx.Focus().Register(idUserMenu)
	if ctx.Input.PressedIn(r) {
		ctx.Focus().Set(idUserMenu)
	}
	fill := th.SurfaceAlt
	if a.userMenuOpen || ctx.Input.Hovering(r) {
		fill = th.SurfaceSelected
	}
	if ctx.Input.Holding(r) && ctx.Input.Hovering(r) {
		fill = th.AccentPressed
	}
	ctx.Canvas.FillRounded(r, r.H/2, fill)
	if !a.drawAvatarImage(ui.Inset(r, th.BorderWidth)) {
		if initials := avatarInitials(a.m.Identity); initials != "" {
			ui.Label(ctx, r, initials, ui.LabelStyle{Scale: th.Body, Color: th.Text, Align: ui.AlignCenter, Middle: true})
		} else {
			a.drawMenuIcon(ui.Inset(r, max(1, r.H/5)), "person", th.Text)
		}
	}
	ctx.Canvas.StrokeRounded(r, r.H/2, th.BorderWidth, th.BorderStrong)
	if ctx.Focused(idUserMenu) {
		ctx.Canvas.StrokeRounded(r, r.H/2, th.FocusWidth, th.Focus)
	}
	warning := sessionExpiryWarning(a.m.TokenExpiry, ctx.Input.Now) != ""
	if a.updates.result.Available || warning {
		d := max(6, r.H/3)
		badge := ui.Rect{X: r.X + r.W - d, Y: r.Y, W: d, H: d}
		ctx.Canvas.FillRounded(badge, d/2, th.Accent)
		mark := "!"
		if !warning {
			mark = "↑"
		}
		ui.Label(ctx, badge, mark, ui.LabelStyle{Scale: th.Small, Color: th.TextOnAccent, Align: ui.AlignCenter, Middle: true})
	}
	return ctx.Input.ClickedIn(r) || (ctx.Focused(idUserMenu) &&
		(ctx.Input.KeyPressed(keysym.KeyReturn) || ctx.Input.KeyPressed(keysym.KeyKPEnter) || ctx.Input.RuneChord(keysym.ModNone, ' ')))
}

func (a *App) drawUserMenuTooltip(bounds, anchor ui.Rect) {
	th := a.opts.Theme
	text := i18n.Get("header.userMenu")
	if a.updates.result.Available {
		text += " · " + i18n.Get("updates.availableBadge")
	}
	if warn := sessionExpiryWarning(a.m.TokenExpiry, a.ctx.Input.Now); warn != "" {
		text += " · " + warn
	}
	w := min(ui.TextWidth(text, th.Small, th.Font)+2*th.Gap, bounds.W-2*th.Pad)
	r := ui.Rect{X: max(th.Pad, anchor.X+anchor.W-w), Y: anchor.Y + anchor.H + th.Gap/2, W: w, H: ui.LineHeight(th.Small, th.Font) + th.Gap}
	a.ctx.Canvas.FillRounded(r, th.Radius, th.Surface)
	a.ctx.Canvas.StrokeRounded(r, th.Radius, th.BorderWidth, th.BorderStrong)
	ui.Label(a.ctx, ui.InsetXY(r, th.Gap, 0), text, ui.LabelStyle{Scale: th.Small, Middle: true})
}

func (a *App) drawUserMenu(bounds, anchor ui.Rect) intent {
	ctx, th := a.ctx, a.opts.Theme
	actions := a.userMenuActions()
	outer := ui.Inset(bounds, th.Pad)
	w := min(16*th.Pad, outer.W) // 320 logical pixels at the default scale
	for _, row := range actions {
		w = min(outer.W, max(w, ui.TextWidth(row.label, th.Body, th.Font)+4*th.Pad+th.ControlHeight))
	}
	info := a.userMenuInfo()
	infoHeights := make([]int, len(info))
	infoH := th.Gap
	for i, text := range info {
		infoHeights[i] = max(1, len(ui.Wrap(text, th.Small, th.Font, max(1, w-2*th.Pad))))*ui.LineHeight(th.Small, th.Font) + th.Gap/2
		infoH += infoHeights[i]
	}
	contentH := infoH + th.Gap + len(actions)*th.ControlHeight + th.Gap
	y := min(anchor.Y+anchor.H+th.Gap/2, outer.Y+outer.H)
	r := ui.Rect{X: max(outer.X, anchor.X+anchor.W-w), Y: y, W: w, H: min(contentH+2*th.BorderWidth, outer.Y+outer.H-y)}
	if ctx.Input.KeyPressed(keysym.KeyEscape) || (ctx.Input.Pressed && !ctx.Input.Mouse.In(r) && !ctx.Input.Mouse.In(anchor)) {
		a.userMenuSwallow = ctx.Input.Down
		a.closeUserMenu()
		return intent{}
	}
	// Only actions participate in traversal while the popup is open.
	ctx.Focus().Begin()
	for _, row := range actions {
		ctx.Focus().Register(row.id)
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
		a.userMenuScroll -= ctx.Input.Wheel.Y * th.ControlHeight
	}
	// Keyboard traversal brings the focused action into the clipped viewport.
	if move != 0 || a.userMenuFocus != ctx.Focus().Focus() {
		for i, row := range actions {
			if ctx.Focused(row.id) {
				top := infoH + th.Gap + i*th.ControlHeight
				if top < a.userMenuScroll {
					a.userMenuScroll = top
				}
				if top+th.ControlHeight > a.userMenuScroll+view.H {
					a.userMenuScroll = top + th.ControlHeight - view.H
				}
			}
		}
	}
	a.userMenuFocus = ctx.Focus().Focus()
	a.userMenuScroll = max(0, min(a.userMenuScroll, contentH-view.H))
	shadow := r
	shadow.Y += max(2, th.Body)
	ctx.Canvas.FillRounded(shadow, th.Radius, color.RGBA{A: 55})
	ctx.Canvas.FillRounded(r, th.Radius, th.Surface)
	ctx.Canvas.StrokeRounded(r, th.Radius, th.BorderWidth, th.BorderStrong)
	ctx.Canvas.PushClip(view)
	y = view.Y + th.Gap - a.userMenuScroll
	for i, text := range info {
		ink := th.TextMuted
		if i == 0 {
			ink = th.Text
		}
		ui.Label(ctx, ui.Rect{X: view.X + th.Pad, Y: y, W: view.W - 2*th.Pad, H: infoHeights[i]}, text, ui.LabelStyle{Scale: th.Small, Color: ink, Wrap: true})
		y += infoHeights[i]
	}
	ctx.Canvas.Fill(ui.Rect{X: view.X + th.Gap, Y: y, W: view.W - 2*th.Gap, H: th.BorderWidth}, th.Border)
	y += th.Gap
	var out intent
	for i, row := range actions {
		box := ui.Rect{X: view.X + th.Gap/2, Y: y, W: view.W - th.Gap, H: th.ControlHeight}
		hit := ui.Intersect(box, view)
		if i == len(actions)-1 {
			ctx.Canvas.Fill(ui.Rect{X: box.X + th.Gap/2, Y: box.Y, W: box.W - th.Gap, H: th.BorderWidth}, th.Border)
		}
		if ctx.Input.PressedIn(hit) {
			ctx.Focus().Set(row.id)
			ctx.Repaint()
		}
		if ctx.Input.Hovering(hit) || ctx.Focused(row.id) {
			ctx.Canvas.FillRounded(box, th.Radius, th.SurfaceSelected)
		}
		if ctx.Input.Holding(hit) && ctx.Input.Hovering(hit) {
			ctx.Canvas.FillRounded(box, th.Radius, th.AccentPressed)
		}
		if ctx.Focused(row.id) {
			ctx.Canvas.StrokeRounded(ui.Inset(box, th.BorderWidth), th.Radius, th.FocusWidth, th.Focus)
		}
		ink := th.Text
		if row.kind == intentSignOut {
			ink = th.Danger
		}
		icon := ui.Rect{X: box.X + th.Gap, Y: box.Y + box.H/4, W: box.H / 2, H: box.H / 2}
		a.drawMenuIcon(icon, row.icon, ink)
		ui.Label(ctx, ui.Rect{X: icon.X + icon.W + th.Gap, Y: box.Y, W: box.W - icon.W - 3*th.Gap, H: box.H}, row.label, ui.LabelStyle{Color: ink, Middle: true})
		if ctx.Input.ClickedIn(hit) || (ctx.Focused(row.id) && (ctx.Input.KeyPressed(keysym.KeyReturn) || ctx.Input.KeyPressed(keysym.KeyKPEnter) || ctx.Input.RuneChord(keysym.ModNone, ' '))) {
			out = intent{kind: row.kind}
		}
		y += th.ControlHeight
	}
	if contentH > view.H {
		trackH := view.H
		thumbH := max(th.Gap, trackH*view.H/contentH)
		thumbY := view.Y + (trackH-thumbH)*a.userMenuScroll/max(1, contentH-view.H)
		ctx.Canvas.FillRounded(ui.Rect{X: view.X + view.W - max(2, th.Body), Y: thumbY, W: max(2, th.Body), H: thumbH}, th.Body, th.TextMuted)
	}
	ctx.Canvas.PopClip()
	if out.kind != intentNone {
		a.closeUserMenu()
	}
	return out
}

// Local vector-style icons stay crisp in every font, palette and UI scale.
func (a *App) drawMenuIcon(r ui.Rect, kind string, ink color.RGBA) {
	c := a.ctx.Canvas
	u := max(1, r.W/12)
	x, y, w, h := r.X, r.Y, r.W, r.H
	person := func(px, py, size int) {
		head := ui.Rect{X: px + size/3, Y: py, W: size / 3, H: size / 3}
		c.FillRounded(head, size/6, ink)
		c.FillRounded(ui.Rect{X: px + size/6, Y: py + size/2, W: size * 2 / 3, H: size / 2}, size/4, ink)
	}
	switch kind {
	case "person":
		person(x, y, min(w, h))
	case "users":
		person(x+w/3, y, w*2/3)
		person(x, y+h/4, w*2/3)
	case "gear":
		cx, cy := x+w/2, y+h/2
		for i := range 8 {
			t := float64(i) * math.Pi / 4
			c.Line(cx+int(math.Cos(t)*float64(w)*0.30), cy+int(math.Sin(t)*float64(h)*0.30), cx+int(math.Cos(t)*float64(w)*0.46), cy+int(math.Sin(t)*float64(h)*0.46), 2*u, ink)
		}
		c.StrokeRounded(ui.Inset(r, w/5), w/2, 2*u, ink)
		c.StrokeRounded(ui.Inset(r, w/3), w/2, u, ink)
	case "exit":
		c.Line(x+w/3, y, x, y, u, ink)
		c.Line(x, y, x, y+h, u, ink)
		c.Line(x, y+h, x+w/3, y+h, u, ink)
		c.Line(x+w/3, y+h/2, x+w, y+h/2, u, ink)
		c.Line(x+w, y+h/2, x+w*2/3, y+h/4, u, ink)
		c.Line(x+w, y+h/2, x+w*2/3, y+h*3/4, u, ink)
	case "update":
		c.Line(x+w/2, y, x+w/2, y+h*3/4, u, ink)
		c.Line(x+w/4, y+h/2, x+w/2, y+h*3/4, u, ink)
		c.Line(x+w*3/4, y+h/2, x+w/2, y+h*3/4, u, ink)
		c.Line(x, y+h, x+w, y+h, u, ink)
	}
}
