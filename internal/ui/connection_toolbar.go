// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"

	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/i18n"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

// ConnectionToolbar is the shared chrome prototype. Its owner dispatches the
// returned action against a fresh snapshot; drawing never touches a transport.
// The owner also routes input and controls fullscreen reveal/auto-hide.
type ConnectionToolbar struct {
	Menu   string // "", "tools", or "connection"
	Pinned bool
	Offset int // horizontal fullscreen drag offset in drawable pixels
}

// ToolbarLayout identifies client-owned pixels and the remaining content area.
// Fullscreen chrome floats, so showing it never changes Content.
type ToolbarLayout struct {
	Bar, Panel, Content, Grip Rect
}

// Layout draws the toolbar and returns at most one requested session action.
// It reuses the shell's theme, widgets and focus ring without importing SDL.
func (t *ConnectionToolbar) Layout(ctx *Context, bounds Rect, s connection.Snapshot) (ToolbarLayout, connection.Action) {
	th := ctx.Theme
	rowH := th.ControlHeight
	barH := rowH + 2*th.Pad
	wideItems := []toolbarItem{}
	if s.Availability(connection.Sessions).Visible {
		wideItems = append(wideItems, toolbarItem{action: connection.Sessions})
	}
	wideItems = append(wideItems, toolbarItem{action: connection.Fullscreen}, toolbarItem{menu: "tools"}, toolbarItem{menu: "connection"})
	if s.Fullscreen {
		wideItems = append(wideItems, toolbarItem{menu: "pin"})
	}
	wideItems = append(wideItems, toolbarItem{action: connection.Disconnect})
	wideWidth := 2*th.Pad + TextWidth("namespace/workspace", th.Small, th.Font)
	for _, item := range wideItems {
		b := t.button(item, s, th.Small)
		wideWidth += b.Width(ctx) + th.Gap
	}
	narrow := bounds.W < wideWidth
	if narrow {
		barH += LineHeight(th.Small, th.Font) + th.Gap
	}
	bar := Rect{X: bounds.X, Y: bounds.Y, W: bounds.W, H: barH}
	content := Rect{X: bounds.X, Y: bounds.Y + barH, W: bounds.W, H: max(0, bounds.H-barH)}
	if s.Fullscreen {
		bar.W = min(bar.W, max(wideWidth, 32*th.ControlHeight))
		bar.X += (bounds.W - bar.W) / 2
		bar.X = max(bounds.X, min(bar.X+t.Offset, bounds.X+bounds.W-bar.W))
		bar.Y += th.Gap
		content = bounds
	}
	out := ToolbarLayout{Bar: bar, Content: content}
	ctx.Canvas.FillRounded(bar, th.Radius, th.Surface)
	ctx.Canvas.StrokeRounded(bar, th.Radius, th.BorderWidth, th.Border)
	if ctx.Input.KeyPressed(keysym.KeyEscape) {
		t.Menu = ""
	}

	identity := s.Workspace + " · " + i18n.Get("toolbar.surface."+string(s.Surface)) + " · " + i18n.Get("toolbar.state."+string(s.State))
	if s.Role == connection.Observer {
		identity += " · " + i18n.Get("toolbar.viewOnly")
	}
	inner := Inset(bar, th.Pad)
	if s.Fullscreen {
		out.Grip = Rect{X: inner.X, Y: inner.Y, W: th.Pad, H: rowH}
		Label(ctx, out.Grip, "::", LabelStyle{Scale: th.Small, Middle: true})
		inner.X += th.Pad + th.Gap
		inner.W -= th.Pad + th.Gap
	}
	y := inner.Y
	if narrow {
		Label(ctx, Rect{X: inner.X, Y: y, W: inner.W, H: LineHeight(th.Small, th.Font)}, identity, LabelStyle{Scale: th.Small})
		y += LineHeight(th.Small, th.Font) + th.Gap
	}
	items := wideItems
	if narrow {
		items = []toolbarItem{{action: connection.Fullscreen}, {menu: "tools"}, {action: connection.Disconnect}}
	}

	width := 0
	for _, item := range items {
		b := t.button(item, s, th.Small)
		width += b.Width(ctx) + th.Gap
	}
	x := inner.X + max(0, inner.W-width+th.Gap)
	if !narrow {
		Label(ctx, Rect{X: inner.X, Y: y, W: max(0, x-inner.X-th.Pad), H: rowH}, identity, LabelStyle{Scale: th.Small, Middle: true})
	}
	var requested connection.Action
	for _, item := range items {
		b := t.button(item, s, th.Small)
		r := Rect{X: x, Y: y, W: b.Width(ctx), H: rowH}
		if b.Layout(ctx, r) {
			if item.action != "" {
				requested = item.action
			} else {
				t.toggle(item.menu)
			}
		}
		x += r.W + th.Gap
	}
	if t.Menu == "" {
		return out, requested
	}

	panelItems := []toolbarItem{}
	if t.Menu == "tools" {
		if narrow {
			if s.Availability(connection.Sessions).Visible {
				panelItems = append(panelItems, toolbarItem{action: connection.Sessions})
			}
			panelItems = append(panelItems, toolbarItem{menu: "connection"})
			if s.Fullscreen {
				panelItems = append(panelItems, toolbarItem{menu: "pin"})
			}
		}
		for _, a := range s.Tools() {
			panelItems = append(panelItems, toolbarItem{action: a})
		}
	}
	panelH := (rowH+th.Gap)*len(panelItems) + 2*th.Pad
	if t.Menu == "connection" || len(panelItems) == 0 {
		panelH = 3*LineHeight(th.Small, th.Font) + 2*th.Pad
	}
	panelW := min(bar.W, 12*th.ControlHeight)
	panel := Rect{X: bar.X + bar.W - panelW, Y: bar.Y + bar.H + th.Gap, W: panelW, H: panelH}
	out.Panel = panel
	ctx.Canvas.FillRounded(panel, th.Radius, th.Surface)
	ctx.Canvas.StrokeRounded(panel, th.Radius, th.BorderWidth, th.Border)
	if t.Menu == "connection" {
		lines := []string{identity, i18n.Get("toolbar.transport") + ": " + s.Transport, i18n.Get("toolbar.disconnectHint")}
		if s.HasMetrics && s.State == connection.Connected {
			lines[1] += fmt.Sprintf(" · %.0f fps · %.0f kbit/s", s.FPS, s.Kbps)
		}
		for n, line := range lines {
			Label(ctx, Rect{X: panel.X + th.Pad, Y: panel.Y + th.Pad + n*LineHeight(th.Small, th.Font), W: panel.W - 2*th.Pad, H: LineHeight(th.Small, th.Font)}, line, LabelStyle{Scale: th.Small})
		}
	} else if len(panelItems) == 0 {
		Label(ctx, Inset(panel, th.Pad), i18n.Get("toolbar.noTools"), LabelStyle{Scale: th.Small})
	} else {
		for n, item := range panelItems {
			b := t.button(item, s, th.Small)
			if b.Disabled {
				b.Text += " — " + i18n.Get("toolbar."+s.Availability(item.action).Reason)
			}
			r := Rect{X: panel.X + th.Pad, Y: panel.Y + th.Pad + n*(rowH+th.Gap), W: panel.W - 2*th.Pad, H: rowH}
			if b.Layout(ctx, r) {
				if item.action != "" {
					requested, t.Menu = item.action, ""
				} else {
					t.toggle(item.menu)
				}
			}
		}
	}
	return out, requested
}

type toolbarItem struct {
	action connection.Action
	menu   string
}

func (t *ConnectionToolbar) toggle(menu string) {
	if menu == "pin" {
		t.Pinned = !t.Pinned
	} else if t.Menu == menu {
		t.Menu = ""
	} else {
		t.Menu = menu
	}
}

func (t *ConnectionToolbar) button(item toolbarItem, s connection.Snapshot, scale int) Button {
	key := item.menu
	if item.action != "" {
		key = string(item.action)
	}
	label := i18n.Get("toolbar." + key)
	switch item.action {
	case connection.Fullscreen:
		if s.Fullscreen {
			label = i18n.Get("toolbar.windowed")
		}
	case connection.SharedControl:
		if s.Role == connection.Controller {
			label = i18n.Get("toolbar.releaseControl")
		}
	case connection.ClipboardSync:
		label = fmt.Sprintf("%s: %s", label, toolbarOnOff(s.Clipboard))
	case connection.GuestResize:
		label = fmt.Sprintf("%s: %s", label, toolbarOnOff(s.ResizeGuest))
	case connection.Audio:
		if s.Muted {
			label = i18n.Get("toolbar.unmute")
		}
	}
	if item.menu == "pin" && t.Pinned {
		label = i18n.Get("toolbar.unpin")
	}
	b := Button{ID: FocusID("toolbar-" + key), Text: label, Variant: ButtonSecondary, Scale: scale}
	if item.action != "" {
		b.Disabled = !s.Availability(item.action).Enabled
	}
	if item.action == connection.Disconnect {
		b.Variant = ButtonDanger
	}
	return b
}

func toolbarOnOff(on bool) string {
	if on {
		return i18n.Get("toolbar.on")
	}
	return i18n.Get("toolbar.off")
}
