// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"strings"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/i18n"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/tray"
	"github.com/kube-workspaces/desktop-client/internal/ui"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// This file is the shell's side of the system-tray icon: lifetime,
// menu contents, and what a menu click does. The native menu itself lives
// in internal/viewer (tray.go); the pure menu rules live in internal/tray.
// Everything here runs on the loop's goroutine except [App.Handle], which
// runs on the tray's thread and only queues.

// trayRetryInterval is how long a failed tray creation waits before the
// loop tries again. Creation fails where the platform offers no tray, and
// retrying that every frame would pointlessly prod D-Bus forever.
const trayRetryInterval = 30 * time.Second

var _ tray.Handler = (*App)(nil)

// Handle implements [tray.Handler]: it queues a menu click for the loop
// and wakes it. It runs on the tray's thread, so it touches nothing but
// the channel — the model belongs to the loop's goroutine, which applies
// the action in [App.Step].
func (a *App) Handle(action tray.Action) {
	select {
	case a.trayCh <- action:
	default:
		a.logf("tray: action queue full, dropping %v", action.Kind)
	}
	a.be.Wake()
}

// newTray builds the native tray behind the TrayNew seam when tests inject
// one, or the real SDL tray when the shell's own window is an SDL one. A
// fake backend (tests) gets no tray: calling into the SDL binding without
// a loaded library would take the process down, and headless runs have no
// notification area anyway.
func (a *App) newTray() tray.Backend {
	if a.opts.TrayNew != nil {
		return a.opts.TrayNew(a)
	}
	if _, ok := a.be.(*viewer.SDLBackend); !ok {
		return nil
	}
	t, err := viewer.NewSDLTray(a)
	if err != nil {
		a.logf("tray: %v", err)
		return nil
	}
	return t
}

// reconcileTray owns the tray's lifetime and contents, once per loop
// iteration: create when enabled and absent (with backoff after a
// failure), destroy when disabled, rebuild the Workspaces submenu when the
// running set changed, and pump tray housekeeping.
func (a *App) reconcileTray(now time.Time) {
	enabled := !a.opts.NoTray && a.settings.Tray
	if a.tray == nil && enabled {
		if now.Before(a.trayRetryAt) {
			return
		}
		a.tray = a.newTray()
		if a.tray == nil {
			a.trayRetryAt = now.Add(trayRetryInterval)
			return
		}
		a.traySig = ""
	}
	if a.tray != nil && !enabled {
		a.closeTray()
		a.traySig = ""
		return
	}
	if a.tray == nil {
		return
	}
	if sig, targets := a.trayTargets(); sig != a.traySig {
		a.tray.Update(targets)
		a.traySig = sig
	}
	a.tray.Pump()
}

// scanQuit separates a main-window close request from a global quit in one
// event batch. a.events holds only the shell window's routed events, so a
// close here is the main window's close button; session windows consume
// their own closes before the pump ever sees them.
func scanQuit(events []ui.Event) (closeRequested, globalQuit bool) {
	for _, e := range events {
		switch e.(type) {
		case viewer.EventWindowClose:
			closeRequested = true
		case viewer.EventQuit:
			globalQuit = true
		}
	}
	return closeRequested, globalQuit
}

// hideShellToTray minimizes the main window into the tray: the window
// leaves the screen, taskbar and window list while sessions keep stepping
// beside the tray icon. The window, its textures and the event queue all
// survive, so showing it later restores exactly what was there.
func (a *App) hideShellToTray() {
	if err := a.be.Hide(); err != nil {
		a.logf("tray: hide window: %v", err)
		return
	}
	a.shellHidden = true
	a.dirty = true
}

// showShell restores a minimized main window and raises it. Showing an
// already-visible window only raises, so the tray's opener needs no state
// of its own.
func (a *App) showShell() {
	if a.shellHidden {
		if err := a.be.Show(); err != nil {
			a.logf("tray: show window: %v", err)
			return
		}
		a.shellHidden = false
	}
	_ = a.be.Raise()
	a.dirty = true
}

// closeTray destroys the tray on the way out. A hidden main window is
// shown first: destroying the tray while the window is hidden would strand
// the process with no way back in except the task manager.
func (a *App) closeTray() {
	if a.shellHidden {
		a.showShell()
	}
	if a.tray != nil {
		a.tray.Close()
		a.tray = nil
	}
}

// trayTargets lists the running workspaces for the menu, with a signature
// the loop compares to skip rebuilds when nothing changed. Only running
// workspaces are listed: the menu offers open actions, and anything else
// would bait a click that fails.
func (a *App) trayTargets() (string, []tray.Target) {
	var targets []tray.Target
	var sb strings.Builder
	for _, ws := range a.m.Workspaces {
		if !ws.Running() {
			continue
		}
		targets = append(targets, tray.Target{
			Key:   ws.Key(),
			Label: ws.Name + " (" + ws.Namespace + ")",
			Kind:  trayKindOf(ws),
			Tier1: ws.HasTier1(),
		})
		sb.WriteString(ws.Key())
		sb.WriteByte(0)
		if ws.HasTier1() {
			sb.WriteByte('1')
		}
		sb.WriteByte(0)
	}
	return sb.String(), targets
}

// trayKindOf maps a workspace to the tray's kind: which open modes apply.
func trayKindOf(ws kwclient.Workspace) tray.Kind {
	switch {
	case ws.IsVM():
		return tray.KindVM
	case ws.Type == kwclient.WorkspaceTypeContainer:
		return tray.KindContainer
	case ws.Type == kwclient.WorkspaceTypeScratch:
		return tray.KindScratch
	default:
		return tray.KindOther
	}
}

// drainTray applies every queued menu click. It runs on the loop's
// goroutine, before the frame is drawn, so a click's window or notice is
// part of the next frame.
func (a *App) drainTray(ctx context.Context) {
	for {
		select {
		case action := <-a.trayCh:
			a.applyTrayAction(ctx, action)
			a.dirty = true
		default:
			return
		}
	}
}

// applyTrayAction performs one menu click: Show restores the main window,
// About opens the panel, Quit quits through the same flag the window-close
// path sets, and Open focuses, picks or opens the workspace.
func (a *App) applyTrayAction(ctx context.Context, action tray.Action) {
	switch action.Kind {
	case tray.ActionShow:
		a.showShell()
	case tray.ActionAbout:
		a.m.ShowAbout()
	case tray.ActionQuit:
		a.quit = true
	case tray.ActionOpen:
		a.trayOpenWorkspace(ctx, action.Key)
	}
}

// trayOpenWorkspace handles a Workspaces-submenu click for key: focus the
// live window, open directly when exactly one mode applies, or offer the
// quick-pick window. The key is re-resolved against the current list at
// click time: the menu may predate a refresh that stopped or removed the
// workspace.
func (a *App) trayOpenWorkspace(ctx context.Context, key string) {
	ws, ok := lookupWorkspace(a.m.Workspaces, key)
	if !ok || !ws.Running() {
		a.m.Err = ""
		a.m.Notice = i18n.Get("pick.gone")
		a.refreshWorkspaces(ctx, false)
		return
	}
	if e, ok := a.live[ws.Key()]; ok && !e.observer {
		// The primary surface is already on screen: focus it, exactly as
		// the sessions switcher does.
		_ = e.window.Raise()
		a.m.Err = ""
		a.m.Notice = i18n.Sprintf("sessions.opened", ws.Key())
		return
	}
	modes := tray.ModesFor(trayKindOf(ws))
	if len(modes) == 1 {
		a.openTrayMode(ctx, ws, modes[0])
		return
	}
	a.m.ShowPicker(ws)
}

// pickMode performs one quick-pick tile. Synchronous failures (a stale
// workspace, a web child that will not spawn) stay in the picker as
// PickerErr for a retry or another choice; a display or console open moves
// to the session screen and closes the picker, with any later failure
// surfacing on the list through the normal session path.
func (a *App) pickMode(ctx context.Context, mode tray.Mode) {
	sub := a.m.Picker
	if sub == nil {
		return
	}
	ws, ok := lookupWorkspace(a.m.Workspaces, sub.Key())
	if !ok || !ws.Running() {
		a.m.PickerErr = i18n.Get("pick.gone")
		return
	}
	offered := false
	for _, m := range tray.ModesFor(trayKindOf(ws)) {
		if m == mode {
			offered = true
			break
		}
	}
	if !offered {
		a.m.PickerErr = i18n.Get("pick.gone")
		return
	}
	a.openTrayMode(ctx, ws, mode)
}

// openTrayMode opens ws through one mode, closing the picker on the paths
// that leave it (a session screen, the SSH form, the browser flow) and
// keeping it with PickerErr on the paths that fail synchronously.
func (a *App) openTrayMode(ctx context.Context, ws kwclient.Workspace, mode tray.Mode) {
	switch mode {
	case tray.ModeDisplay:
		a.m.ClosePicker()
		a.activate(ctx, ws, false)
	case tray.ModeSerial:
		if e, ok := a.live[sessionKey(ws, "serial")]; ok {
			a.m.ClosePicker()
			_ = e.window.Raise()
			a.m.Err = ""
			a.m.Notice = i18n.Sprintf("sessions.opened", sessionKey(ws, "serial"))
			return
		}
		a.m.ClosePicker()
		a.m.OpenConsole(ws, "serial", "", nil)
	case tray.ModeSSH:
		if e, ok := a.live[sessionKey(ws, "ssh")]; ok {
			a.m.ClosePicker()
			_ = e.window.Raise()
			a.m.Err = ""
			a.m.Notice = i18n.Sprintf("sessions.opened", sessionKey(ws, "ssh"))
			return
		}
		a.m.ClosePicker()
		a.openSSH(ws)
	case tray.ModeWeb:
		a.openWeb(ctx, ws)
		if a.m.Err != "" {
			a.m.PickerErr = a.m.Err
			a.m.Err = ""
		} else {
			a.m.ClosePicker()
		}
	case tray.ModeTerminal:
		a.m.ClosePicker()
		a.activate(ctx, ws, false)
	case tray.ModeBrowser:
		a.m.ClosePicker()
		a.openInBrowser(ctx, ws)
	}
}

// lookupWorkspace finds key ("namespace/name") in the current list.
func lookupWorkspace(list []kwclient.Workspace, key string) (kwclient.Workspace, bool) {
	for _, ws := range list {
		if ws.Key() == key {
			return ws, true
		}
	}
	return kwclient.Workspace{}, false
}
