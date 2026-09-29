// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/tray"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

// fakeTray is a [tray.Backend] that records menus instead of showing one.
type fakeTray struct {
	updates [][]tray.Target
	pumps   int
	closes  int
}

func (f *fakeTray) Update(targets []tray.Target) {
	cp := append([]tray.Target(nil), targets...)
	f.updates = append(f.updates, cp)
}
func (f *fakeTray) Pump()  { f.pumps++ }
func (f *fakeTray) Close() { f.closes++ }
func (f *fakeTray) last() []tray.Target {
	if len(f.updates) == 0 {
		return nil
	}
	return f.updates[len(f.updates)-1]
}

var _ tray.Backend = (*fakeTray)(nil)

// trayRig starts a rig with a fake tray injected: one running VM and one
// running container selected on the VM.
func trayRig(t *testing.T) (*rig, *fakeTray) {
	t.Helper()
	r := newRig(savedProfile(), "stored-token")
	ft := &fakeTray{}
	r.app.opts.TrayNew = func(tray.Handler) tray.Backend { return ft }
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{
			workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true),
			workspace("team", "web-a", kwclient.WorkspaceTypeContainer, true),
			workspace("team", "old-a", kwclient.WorkspaceTypeVM, false),
		}
	})
	r.start()
	r.app.m.Selected = "team/vm-a"
	r.settle()
	return r, ft
}

func trayKeys(targets []tray.Target) []string {
	out := make([]string, 0, len(targets))
	for _, tg := range targets {
		out = append(out, tg.Key)
	}
	return out
}

func equalKeys(got []tray.Target, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i].Key != want[i] {
			return false
		}
	}
	return true
}

// TestTrayMenuListsRunningOnly pins the menu contents: running workspaces
// are listed, stopped ones are not, and the VM carries its Tier 1 flag
// through for the pick window's hint.
func TestTrayMenuListsRunningOnly(t *testing.T) {
	r, ft := trayRig(t)
	if r.app.tray == nil {
		t.Fatal("no tray backend after start")
	}
	if got := trayKeys(ft.last()); !equalKeys(ft.last(), "team/vm-a", "team/web-a") {
		t.Fatalf("menu = %v, want team/vm-a + team/web-a", got)
	}
	for _, tg := range ft.last() {
		if tg.Label == "" {
			t.Fatalf("menu row has no label: %+v", tg)
		}
	}
}

// TestTrayMenuRefreshesWithList stops a workspace and refreshes: the menu
// must drop it without a restart.
func TestTrayMenuRefreshesWithList(t *testing.T) {
	r, ft := trayRig(t)
	before := len(ft.updates)
	r.api.set(func(f *fakeAPI) {
		for i := range f.workspaces {
			if f.workspaces[i].Name == "web-a" {
				f.workspaces[i].Stopped = true
				f.workspaces[i].ReadyReplicas = 0
			}
		}
	})
	r.app.refreshWorkspaces(context.Background(), true)
	r.settle()
	if len(ft.updates) <= before {
		t.Fatal("menu did not rebuild after the list changed")
	}
	if got := trayKeys(ft.last()); !equalKeys(ft.last(), "team/vm-a") {
		t.Fatalf("menu = %v, want only team/vm-a", got)
	}
}

// TestTraySkipsRebuildWhenUnchanged: identical lists must not rebuild the
// native menu every frame.
func TestTraySkipsRebuildWhenUnchanged(t *testing.T) {
	r, ft := trayRig(t)
	n := len(ft.updates)
	for i := 0; i < 5; i++ {
		r.step()
	}
	if len(ft.updates) != n {
		t.Fatalf("menu rebuilt %d times with an unchanged list", len(ft.updates)-n)
	}
}

// TestTrayPumps: the loop must run tray housekeeping every iteration.
func TestTrayPumps(t *testing.T) {
	_, ft := trayRig(t)
	if ft.pumps == 0 {
		t.Fatal("tray never pumped")
	}
}

// TestNoTrayFlag: --no-tray means no backend, no menu, no error.
func TestNoTrayFlag(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.app.opts.NoTray = true
	called := false
	r.app.opts.TrayNew = func(tray.Handler) tray.Backend { called = true; return &fakeTray{} }
	r.start()
	r.settle()
	if called || r.app.tray != nil {
		t.Fatal("tray created despite --no-tray")
	}
}

// TestTraySettingsToggle wires the settings row to the tray's lifetime:
// off destroys it, on recreates it, and the choice persists.
func TestTraySettingsToggle(t *testing.T) {
	r, ft := trayRig(t)
	if r.app.tray == nil {
		t.Fatal("no tray backend after start")
	}
	r.app.applySettings(Settings{Style: r.app.settings.Style, Mode: r.app.settings.Mode, UIScale: r.app.settings.UIScale, Tray: false})
	r.app.saveSettings()
	r.step()
	if r.app.tray != nil {
		t.Fatal("tray survives being switched off")
	}
	if ft.closes != 1 {
		t.Fatalf("closes = %d, want 1", ft.closes)
	}
	r.settle()
	saved, err := r.store.LoadSettings()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if saved.TrayEnabled() {
		t.Fatal("tray-off choice did not persist")
	}
	r.app.applySettings(Settings{Style: r.app.settings.Style, Mode: r.app.settings.Mode, UIScale: r.app.settings.UIScale, Tray: true})
	r.step()
	if r.app.tray == nil {
		t.Fatal("tray did not come back when switched on")
	}
}

// TestTraySettingsRoundTrip: absent means on, explicit false means off.
func TestTraySettingsRoundTrip(t *testing.T) {
	if got := settingsFromConfig(config.Settings{}); !got.Tray {
		t.Fatal("zero settings should enable the tray")
	}
	off := settingsFromConfig(config.Settings{Tray: config.Bool(false)})
	if off.Tray {
		t.Fatal("explicit false should disable the tray")
	}
	if got := off.toConfig(); got.TrayEnabled() {
		t.Fatal("explicit false did not persist")
	}
}

// TestHandleQueuesAndWakes: the tray thread's Handle must queue without
// touching the model and must wake the loop.
func TestHandleQueuesAndWakes(t *testing.T) {
	r, _ := trayRig(t)
	wakes := r.be.wakeCount()
	r.app.Handle(tray.Action{Kind: tray.ActionAbout})
	if len(r.app.trayCh) != 1 {
		t.Fatalf("trayCh holds %d actions, want 1", len(r.app.trayCh))
	}
	if r.app.m.About {
		t.Fatal("Handle touched the model off the loop's goroutine")
	}
	if r.be.wakeCount() <= wakes {
		t.Fatal("Handle did not wake the loop")
	}
	r.step()
	if !r.app.m.About {
		t.Fatal("queued About action did not open the panel")
	}
}

// TestTrayAboutAndQuit: About opens the panel (Esc closes it), Quit quits.
func TestTrayAboutAndQuit(t *testing.T) {
	r, _ := trayRig(t)
	r.app.trayCh <- tray.Action{Kind: tray.ActionAbout}
	r.step()
	if !r.app.m.About {
		t.Fatal("About action did not open the panel")
	}
	r.press(keysym.KeyEscape, keysym.ModNone)
	if r.app.m.About {
		t.Fatal("Esc did not close the About panel")
	}
	r.app.trayCh <- tray.Action{Kind: tray.ActionQuit}
	r.step()
	if !r.app.quit {
		t.Fatal("Quit action did not quit")
	}
}

// TestTrayOpenFocusesLiveWindow: clicking a workspace with a live display
// focuses it instead of opening a second window.
func TestTrayOpenFocusesLiveWindow(t *testing.T) {
	r, _ := trayRig(t)
	r.app.m.Open(lookupMust(t, r, "team/vm-a"))
	r.settle()
	if !r.app.isLive("team/vm-a") {
		t.Fatalf("no live display (state %v, err %q)", r.app.m.State, r.app.m.Err)
	}
	w := r.app.live["team/vm-a"].window.(*fakeLiveWindow)
	raises := w.raises
	r.app.trayCh <- tray.Action{Kind: tray.ActionOpen, Key: "team/vm-a"}
	r.step()
	if w.raises != raises+1 {
		t.Fatal("tray click did not raise the live window")
	}
	if r.app.m.Picker != nil {
		t.Fatal("picker opened for a live workspace")
	}
}

// TestTrayOpenShowsPicker: a running VM with no live window offers the
// quick-pick window, snapshotted by key.
func TestTrayOpenShowsPicker(t *testing.T) {
	r, _ := trayRig(t)
	r.app.trayCh <- tray.Action{Kind: tray.ActionOpen, Key: "team/vm-a"}
	r.step()
	if r.app.m.Picker == nil || r.app.m.Picker.Key() != "team/vm-a" {
		t.Fatalf("picker = %+v, want team/vm-a", r.app.m.Picker)
	}
}

// TestTrayOpenStaleKey: a click for a workspace that stopped since the
// menu was built reports and refreshes instead of dialling.
func TestTrayOpenStaleKey(t *testing.T) {
	r, _ := trayRig(t)
	r.app.trayCh <- tray.Action{Kind: tray.ActionOpen, Key: "team/gone"}
	r.step()
	if r.app.m.Notice == "" {
		t.Fatal("stale click reported nothing")
	}
	if r.app.m.Picker != nil || len(r.opened) != 0 {
		t.Fatal("stale click opened something")
	}
}

// TestPickDisplayOpensDisplay: the Display tile opens the display session
// and closes the picker.
func TestPickDisplayOpensDisplay(t *testing.T) {
	r, _ := trayRig(t)
	r.app.m.ShowPicker(lookupMust(t, r, "team/vm-a"))
	r.app.act(context.Background(), intent{kind: intentPickMode, mode: tray.ModeDisplay})
	r.settle()
	if r.app.m.Picker != nil {
		t.Fatal("picker stayed open after picking Display")
	}
	if !r.app.isLive("team/vm-a") {
		t.Fatalf("no live display (state %v, err %q)", r.app.m.State, r.app.m.Err)
	}
}

// TestPickSerialOpensSerialSeat: the Serial tile dials the serial console
// seat beside the display.
func TestPickSerialOpensSerialSeat(t *testing.T) {
	r, _ := trayRig(t)
	r.app.m.ShowPicker(lookupMust(t, r, "team/vm-a"))
	r.app.act(context.Background(), intent{kind: intentPickMode, mode: tray.ModeSerial})
	r.settle()
	if r.app.m.Picker != nil {
		t.Fatal("picker stayed open after picking Serial")
	}
	if !r.app.isLive("team/vm-a#serial") {
		t.Fatalf("no live serial seat (state %v, err %q)", r.app.m.State, r.app.m.Err)
	}
}

// TestPickSSHOpensCredentialForm: the SSH tile opens the existing key-file
// form rather than dialling directly.
func TestPickSSHOpensCredentialForm(t *testing.T) {
	r, _ := trayRig(t)
	r.app.m.ShowPicker(lookupMust(t, r, "team/vm-a"))
	r.app.act(context.Background(), intent{kind: intentPickMode, mode: tray.ModeSSH})
	if r.app.m.Picker != nil {
		t.Fatal("picker stayed open after picking SSH")
	}
	if r.app.m.SSH == nil || r.app.m.SSH.Key() != "team/vm-a" {
		t.Fatalf("SSH form = %+v, want team/vm-a", r.app.m.SSH)
	}
}

// TestPickWebOpensWebview: the Web tile spawns the embedded webview for a
// container and closes the picker.
func TestPickWebOpensWebview(t *testing.T) {
	r, _ := trayRig(t)
	r.app.m.ShowPicker(lookupMust(t, r, "team/web-a"))
	r.app.act(context.Background(), intent{kind: intentPickMode, mode: tray.ModeWeb})
	if r.app.m.Picker != nil {
		t.Fatal("picker stayed open after picking Web")
	}
	if len(r.webbed) != 1 || r.webbed[0].Name != "web-a" {
		t.Fatalf("webbed = %+v, want web-a", r.webbed)
	}
}

// TestPickWebFailureStaysInPicker: a web child that will not spawn keeps
// the picker open with the error inside, for a retry or another tile.
func TestPickWebFailureStaysInPicker(t *testing.T) {
	r, _ := trayRig(t)
	r.app.opts.OpenWeb = func(profile, namespace, name string) error {
		return errors.New("no webview here")
	}
	r.app.m.ShowPicker(lookupMust(t, r, "team/web-a"))
	r.app.act(context.Background(), intent{kind: intentPickMode, mode: tray.ModeWeb})
	if r.app.m.Picker == nil {
		t.Fatal("picker closed on a failed Web pick")
	}
	if r.app.m.PickerErr == "" {
		t.Fatal("failed Web pick reported nothing in the picker")
	}
	if r.app.m.Err != "" {
		t.Fatalf("failed Web pick leaked to the list error: %q", r.app.m.Err)
	}
}

// TestPickTerminalOpensConsole: the Terminal tile opens the integrated
// terminal over /exec for a container.
func TestPickTerminalOpensConsole(t *testing.T) {
	r, _ := trayRig(t)
	r.app.m.ShowPicker(lookupMust(t, r, "team/web-a"))
	r.app.act(context.Background(), intent{kind: intentPickMode, mode: tray.ModeTerminal})
	r.settle()
	if r.app.m.Picker != nil {
		t.Fatal("picker stayed open after picking Terminal")
	}
	if !r.app.isLive("team/web-a") {
		t.Fatalf("no live terminal (state %v, err %q)", r.app.m.State, r.app.m.Err)
	}
}

// TestTrayOpenSingleModeDirect: an unknown workspace type offers only the
// browser, so the click goes straight there with no picker.
func TestTrayOpenSingleModeDirect(t *testing.T) {
	r, _ := trayRig(t)
	r.api.set(func(f *fakeAPI) {
		f.workspaces = append(f.workspaces, workspace("team", "odd-a", "weird", true))
	})
	r.app.refreshWorkspaces(context.Background(), true)
	r.settle()
	r.app.trayCh <- tray.Action{Kind: tray.ActionOpen, Key: "team/odd-a"}
	r.step()
	r.settle()
	if r.app.m.Picker != nil {
		t.Fatal("picker opened for a single-mode workspace")
	}
	if len(r.browsed) != 1 {
		t.Fatalf("browsed = %v, want one browser open", r.browsed)
	}
}

// TestPickerClose: Esc and the Close button both dismiss the picker.
func TestPickerClose(t *testing.T) {
	r, _ := trayRig(t)
	r.app.m.ShowPicker(lookupMust(t, r, "team/vm-a"))
	r.press(keysym.KeyEscape, keysym.ModNone)
	if r.app.m.Picker != nil {
		t.Fatal("Esc did not close the picker")
	}
	r.app.m.ShowPicker(lookupMust(t, r, "team/vm-a"))
	r.focus(idPickClose)
	r.clickFocused()
	r.step()
	if r.app.m.Picker != nil {
		t.Fatal("Close did not close the picker")
	}
}

// TestPickerTilesAreFocusable: every offered mode has a keyboard-reachable
// control, so the picker is operable without a pointer.
func TestPickerTilesAreFocusable(t *testing.T) {
	r, _ := trayRig(t)
	r.app.m.ShowPicker(lookupMust(t, r, "team/vm-a"))
	r.app.dirty = true
	r.step()
	order := append([]ui.FocusID(nil), r.app.ctx.Focus().Order()...)
	for _, mode := range []tray.Mode{tray.ModeDisplay, tray.ModeSerial, tray.ModeSSH} {
		id := ui.FocusID(fmt.Sprintf("pick-mode-%d", int(mode)))
		found := false
		for _, got := range order {
			if got == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("tile for mode %v missing from focus order %v", mode, order)
		}
	}
}

func lookupMust(t *testing.T, r *rig, key string) kwclient.Workspace {
	t.Helper()
	ws, ok := lookupWorkspace(r.app.m.Workspaces, key)
	if !ok {
		t.Fatalf("workspace %s not in list", key)
	}
	return ws
}
