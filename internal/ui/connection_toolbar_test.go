// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

func TestConnectionToolbarFullscreenDoesNotResizeContent(t *testing.T) {
	for _, mode := range []Mode{ModeDark, ModeLight, ModeHighContrast} {
		h := newHarness(1280, 720)
		h.ctx.Theme = ThemeFor(StyleClean, mode)
		bar := &ConnectionToolbar{}
		s := connection.Snapshot{Workspace: "team/linux", Surface: connection.Desktop, State: connection.Connected}
		bounds := Rect{W: 1280, H: 720}
		var layout ToolbarLayout
		body := func(ctx *Context) { layout, _ = bar.Layout(ctx, bounds, s) }
		h.frame(nil, body)
		if layout.Content.Y != layout.Bar.H || layout.Content.H+layout.Bar.H != bounds.H {
			t.Fatalf("windowed geometry: %+v", layout)
		}
		s.Fullscreen = true
		h.frame(nil, body)
		if layout.Content != bounds {
			t.Fatalf("fullscreen chrome changed guest size: %+v", layout)
		}
		bar.Menu = "connection"
		h.frame(nil, body)
		if layout.Content != bounds || layout.Panel.W == 0 {
			t.Fatal("opening a fullscreen panel resized the guest or did not render")
		}
	}
}

func TestConnectionToolbarDoesNotDispatchDisabledTool(t *testing.T) {
	h := newHarness(1280, 720)
	bar := &ConnectionToolbar{Menu: "tools"}
	s := connection.Snapshot{Surface: connection.Desktop, State: connection.Connected, Role: connection.Observer,
		Capabilities: connection.Capabilities{SpecialKeys: true}}
	var requested connection.Action
	body := func(ctx *Context) { _, requested = bar.Layout(ctx, Rect{W: 1280, H: 720}, s) }
	h.frame(nil, body)
	h.ctx.Focus().Set("toolbar-special-keys")
	h.frame([]Event{keyDown(keysym.KeyReturn, keysym.ModNone)}, body)
	if requested != "" {
		t.Fatalf("observer dispatched guest tool %s", requested)
	}
	s.Role = connection.Controller
	h.frame(nil, body)
	h.ctx.Focus().Set("toolbar-special-keys")
	h.frame([]Event{keyDown(keysym.KeyReturn, keysym.ModNone)}, body)
	if requested != connection.SpecialKeys || bar.Menu != "" {
		t.Fatalf("promoted controller: action=%s menu=%s", requested, bar.Menu)
	}
}

func TestConnectionToolbarEscapeAndDisconnectDuringRecovery(t *testing.T) {
	h := newHarness(500, 720)
	bar := &ConnectionToolbar{Menu: "connection"}
	s := connection.Snapshot{Surface: connection.SSH, State: connection.Reconnecting}
	var requested connection.Action
	body := func(ctx *Context) { _, requested = bar.Layout(ctx, Rect{W: 500, H: 720}, s) }
	h.frame(nil, body)
	h.frame([]Event{keyDown(keysym.KeyEscape, keysym.ModNone)}, body)
	if bar.Menu != "" {
		t.Fatal("Esc left the client menu open")
	}
	h.ctx.Focus().Set("toolbar-disconnect")
	h.frame([]Event{keyDown(keysym.KeyReturn, keysym.ModNone)}, body)
	if requested != connection.Disconnect {
		t.Fatal("reconnecting terminal could not disconnect via keyboard")
	}
}
