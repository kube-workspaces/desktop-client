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

func TestConnectionToolbarDebugCardOpensClosesAndDispatchesNothing(t *testing.T) {
	h := newHarness(1280, 720)
	bar := &ConnectionToolbar{}
	s := connection.Snapshot{Workspace: "team/win", Surface: connection.Desktop,
		State: connection.Connected, Role: connection.Exclusive,
		Transport: "Agent", Tier: "Tier 1", Proto: "kw-agent-v1 over WebSocket",
		Muted: false, AudioLive: true,
		Capabilities: connection.Capabilities{SpecialKeys: false, ClipboardSync: true, GuestResize: true, Audio: true},
		BytesIn:      343064, BytesOut: 512, HasActivity: true,
		Extra: [][2]string{{"Capture", "1920×1080"}, {"Video", "386 frames · 335.0 KB"}}}
	var requested connection.Action
	body := func(ctx *Context) { _, requested = bar.Layout(ctx, Rect{W: 1280, H: 720}, s) }
	h.frame(nil, body)
	if bar.Menu != "" {
		t.Fatal("debug card opened itself")
	}
	h.ctx.Focus().Set("toolbar-debug")
	h.frame([]Event{keyDown(keysym.KeyReturn, keysym.ModNone)}, body)
	if bar.Menu != "debug" {
		t.Fatalf("debug button left menu %q", bar.Menu)
	}
	h.frame(nil, body)
	if requested != "" {
		t.Fatalf("debug card dispatched action %s", requested)
	}
	if layout, _ := bar.Layout(h.ctx, Rect{W: 1280, H: 720}, s); layout.Panel.W == 0 || layout.Panel.H == 0 {
		t.Fatal("debug card has no panel rect")
	}
	h.ctx.Focus().Set("toolbar-debug-close")
	h.frame([]Event{keyDown(keysym.KeyReturn, keysym.ModNone)}, body)
	if bar.Menu != "" {
		t.Fatal("Close left the debug card open")
	}
	bar.Menu = "debug"
	h.frame(nil, body)
	h.frame([]Event{keyDown(keysym.KeyEscape, keysym.ModNone)}, body)
	if bar.Menu != "" {
		t.Fatal("Esc left the debug card open")
	}
}

func TestConnectionToolbarDebugRowsReportHonestly(t *testing.T) {
	full := connection.Snapshot{Surface: connection.Desktop, State: connection.Connected,
		Transport: "Agent", Tier: "Tier 1", Proto: "kw-agent-v1 over WebSocket",
		Capabilities: connection.Capabilities{Audio: true}, AudioLive: true,
		HasMetrics: true, FPS: 30, Kbps: 6000,
		BytesIn: 1048576, BytesOut: 1536, HasActivity: true}
	rows := debugRows(full)
	byLabel := map[string]string{}
	for _, r := range rows {
		byLabel[r[0]] = r[1]
	}
	if byLabel["Tier"] != "Tier 1" || byLabel["Transport"] != "Agent" || byLabel["Protocol"] != "kw-agent-v1 over WebSocket" {
		t.Fatalf("identity rows: %v", rows)
	}
	if byLabel["Audio"] != "Live" {
		t.Fatalf("audio row: %v", rows)
	}
	if byLabel["Throughput"] != "30 fps · 6000 kbit/s" {
		t.Fatalf("throughput row: %v", rows)
	}
	if byLabel["Activity in / out"] != "1.0 MB in · 1.5 KB out" {
		t.Fatalf("activity row: %v", rows)
	}
	quiet := connection.Snapshot{Surface: connection.Desktop, State: connection.Connecting}
	rows = debugRows(quiet)
	byLabel = map[string]string{}
	for _, r := range rows {
		byLabel[r[0]] = r[1]
	}
	if byLabel["Tier"] != "—" || byLabel["Throughput"] != "—" {
		t.Fatalf("connecting rows claim data: %v", rows)
	}
	if byLabel["Activity in / out"] != "not tracked for this transport" {
		t.Fatalf("untracked activity misreported: %v", rows)
	}
	if byLabel["Audio"] != "Unavailable" {
		t.Fatalf("missing audio misreported: %v", rows)
	}
}
