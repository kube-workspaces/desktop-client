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
	"github.com/kube-workspaces/desktop-client/internal/viewer"
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
	pbe := popupBackend(r)
	wakes := r.be.wakeCount()
	r.app.Handle(tray.Action{Kind: tray.ActionAbout})
	if len(r.app.trayCh) != 1 {
		t.Fatalf("trayCh holds %d actions, want 1", len(r.app.trayCh))
	}
	if r.app.popup != nil {
		t.Fatal("Handle touched the model off the loop's goroutine")
	}
	if r.be.wakeCount() <= wakes {
		t.Fatal("Handle did not wake the loop")
	}
	r.step()
	if r.app.popup == nil || r.app.popup.kind != popupAbout {
		t.Fatal("queued About action did not open the popup")
	}
	if pbe.opened != 1 {
		t.Fatal("About popup opened no window")
	}
}

// TestTrayAboutAndQuit: About opens its own window (Esc closes it), Quit
// quits. The main window is untouched throughout.
func TestTrayAboutAndQuit(t *testing.T) {
	r, _ := trayRig(t)
	pbe := popupBackend(r)
	r.app.trayCh <- tray.Action{Kind: tray.ActionAbout}
	r.step()
	if r.app.popup == nil || r.app.popup.kind != popupAbout {
		t.Fatal("About action did not open the popup")
	}
	r.app.popup.events = append(r.app.popup.events, ui.EventKey{Key: keysym.KeyEscape, Down: true})
	r.step()
	if r.app.popup != nil {
		t.Fatal("Esc did not close the About popup")
	}
	if pbe.closed != 1 {
		t.Fatal("About popup did not close its window")
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
	if r.app.popup != nil {
		t.Fatal("picker opened for a live workspace")
	}
}

// TestTrayOpenShowsPopup: a running VM with no live window opens the
// quick-pick popup, leaving the main window exactly as it was.
func TestTrayOpenShowsPopup(t *testing.T) {
	r, _ := trayRig(t)
	popupBackend(r)
	r.app.trayCh <- tray.Action{Kind: tray.ActionOpen, Key: "team/vm-a"}
	r.step()
	p := r.app.popup
	if p == nil || p.kind != popupPick || p.ws.Key() != "team/vm-a" {
		t.Fatalf("popup = %+v, want the team/vm-a picker", p)
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
	if r.app.popup != nil || len(r.opened) != 0 {
		t.Fatal("stale click opened something")
	}
}

// openTestPopup opens the picker popup for key with a fake window behind
// it, returning the popup and its backend for assertions.
func openTestPopup(t *testing.T, r *rig, key string) (*popupWindow, *fakeBackend) {
	t.Helper()
	pbe := popupBackend(r)
	r.app.openPickerPopup(lookupMust(t, r, key))
	if r.app.popup == nil {
		t.Fatal("picker popup did not open")
	}
	return r.app.popup, pbe
}

// TestPickDisplayOpensDisplay: the Display tile opens the display session
// and closes the picker.
func TestPickDisplayOpensDisplay(t *testing.T) {
	r, _ := trayRig(t)
	p, _ := openTestPopup(t, r, "team/vm-a")
	r.app.applyPopupPick(context.Background(), p, tray.ModeDisplay)
	r.settle()
	if r.app.popup != nil {
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
	p, _ := openTestPopup(t, r, "team/vm-a")
	r.app.applyPopupPick(context.Background(), p, tray.ModeSerial)
	r.settle()
	if r.app.popup != nil {
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
	p, _ := openTestPopup(t, r, "team/vm-a")
	r.app.applyPopupPick(context.Background(), p, tray.ModeSSH)
	if r.app.popup != nil {
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
	p, _ := openTestPopup(t, r, "team/web-a")
	r.app.applyPopupPick(context.Background(), p, tray.ModeWeb)
	if r.app.popup != nil {
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
	p, _ := openTestPopup(t, r, "team/web-a")
	r.app.applyPopupPick(context.Background(), p, tray.ModeWeb)
	if r.app.popup == nil {
		t.Fatal("picker closed on a failed Web pick")
	}
	if p.err == "" {
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
	p, _ := openTestPopup(t, r, "team/web-a")
	r.app.applyPopupPick(context.Background(), p, tray.ModeTerminal)
	r.settle()
	if r.app.popup != nil {
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
	if r.app.popup != nil {
		t.Fatal("picker opened for a single-mode workspace")
	}
	if len(r.browsed) != 1 {
		t.Fatalf("browsed = %v, want one browser open", r.browsed)
	}
}

// TestPickerClose: Esc and the Close button both dismiss the picker window.
func TestPickerClose(t *testing.T) {
	r, _ := trayRig(t)
	openTestPopup(t, r, "team/vm-a")
	r.app.popup.events = append(r.app.popup.events, ui.EventKey{Key: keysym.KeyEscape, Down: true})
	r.step()
	if r.app.popup != nil {
		t.Fatal("Esc did not close the picker")
	}
	p, _ := openTestPopup(t, r, "team/vm-a")
	p.ctx.Focus().Set(idPickClose)
	p.events = append(p.events, ui.EventKey{Key: keysym.KeyReturn, Down: true})
	r.step()
	if r.app.popup != nil {
		t.Fatal("Close did not close the picker")
	}
}

// TestPickerTilesAreFocusable: every offered mode has a keyboard-reachable
// control in the popup window, so the picker is operable without a pointer.
func TestPickerTilesAreFocusable(t *testing.T) {
	r, _ := trayRig(t)
	p, _ := openTestPopup(t, r, "team/vm-a")
	r.step()
	order := append([]ui.FocusID(nil), p.ctx.Focus().Order()...)
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

// TestPopupSingleSlot: a second popup replaces the first — tray clicks are
// sequential, and a pile of choosers would be a bug report.
func TestPopupSingleSlot(t *testing.T) {
	r, _ := trayRig(t)
	first := popupBackend(r)
	r.app.openPickerPopup(lookupMust(t, r, "team/vm-a"))
	second := newFakeBackend(64, 48)
	r.app.opts.PopupNew = func() viewer.Backend { return second }
	r.app.openAboutPopup()
	if r.app.popup == nil || r.app.popup.kind != popupAbout {
		t.Fatal("About popup did not replace the picker")
	}
	if first.closed != 1 {
		t.Fatal("replaced picker did not close its window")
	}
	if second.opened != 1 {
		t.Fatal("About popup opened no window")
	}
}

// TestPopupBackendFailure: a window that will not open falls back to a
// notice on the main window instead of stranding the click in silence.
func TestPopupBackendFailure(t *testing.T) {
	r, _ := trayRig(t)
	be := popupBackend(r)
	be.openErr = errors.New("no display")
	r.app.openPickerPopup(lookupMust(t, r, "team/vm-a"))
	if r.app.popup != nil {
		t.Fatal("popup exists without a window")
	}
	if r.app.m.Err == "" && r.app.m.Notice == "" {
		t.Fatal("failed popup reported nothing")
	}
}

// TestPopupClosesWithSessions: sign-out and friends tear the popup down
// with the windows — its workspace snapshot belongs to the old identity.
func TestPopupClosesWithSessions(t *testing.T) {
	r, _ := trayRig(t)
	_, pbe := openTestPopup(t, r, "team/vm-a")
	r.app.closeAllSessions()
	if r.app.popup != nil {
		t.Fatal("popup survived the session teardown")
	}
	if pbe.closed != 1 {
		t.Fatal("popup did not close its window")
	}
}

// TestPopupRoutedThroughPump: popup input arrives through the same pump as
// every other window — here, Esc on the popup backend's own queue.
func TestPopupRoutedThroughPump(t *testing.T) {
	r, _ := trayRig(t)
	_, pbe := openTestPopup(t, r, "team/vm-a")
	pbe.send(ui.EventKey{Key: keysym.KeyEscape, Down: true})
	r.step()
	if r.app.popup != nil {
		t.Fatal("pumped Esc did not close the picker")
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

// popupBackend wires a fake popup window backend into the rig and returns
// it, so popup tests can assert on the window without a display. The
// production path builds a real SDL window instead.
func popupBackend(r *rig) *fakeBackend {
	t := newFakeBackend(64, 48)
	r.app.opts.PopupNew = func() viewer.Backend { return t }
	return t
}

// TestWebChildRegistry pins the per-workspace child tracking: starts and
// exits pair up, double starts refcount, and a stray exit changes nothing.
func TestWebChildRegistry(t *testing.T) {
	const key = "test/registry-ws"
	t.Cleanup(func() {
		for webChildOpen(key) {
			webChildExited(key)
		}
	})
	if webChildOpen(key) {
		t.Fatal("fresh key reads open")
	}
	webChildExited(key)
	if got := webChildCount(); got < 0 {
		t.Fatalf("stray exit drove the count to %d", got)
	}
	webChildStarted(key)
	webChildStarted(key)
	if !webChildOpen(key) {
		t.Fatal("started key does not read open")
	}
	webChildExited(key)
	if !webChildOpen(key) {
		t.Fatal("double start collapsed after one exit")
	}
	webChildExited(key)
	if webChildOpen(key) {
		t.Fatal("key still open after paired exits")
	}
}

// TestOpenWebSkipsDuplicate: opening the web view while its child is alive
// reports instead of spawning a second window; after the child exits the
// next open spawns again.
func TestOpenWebSkipsDuplicate(t *testing.T) {
	r, _ := trayRig(t)
	ws := lookupMust(t, r, "team/web-a")
	const key = "team/web-a"
	t.Cleanup(func() {
		for webChildOpen(key) {
			webChildExited(key)
		}
	})
	webChildStarted(key)
	r.app.openWeb(context.Background(), ws)
	if len(r.webbed) != 0 {
		t.Fatalf("duplicate web child spawned: %+v", r.webbed)
	}
	if r.app.m.Notice == "" || r.app.m.Err != "" {
		t.Fatalf("duplicate open should notice, got err %q notice %q", r.app.m.Err, r.app.m.Notice)
	}
	webChildExited(key)
	r.app.openWeb(context.Background(), ws)
	if len(r.webbed) != 1 {
		t.Fatalf("webbed = %+v, want one spawn after the child exited", r.webbed)
	}
}

// TestPickWebSkipsDuplicate: the picker's Web tile on an already-open web
// view closes the picker with a notice and spawns nothing.
func TestPickWebSkipsDuplicate(t *testing.T) {
	r, _ := trayRig(t)
	const key = "team/web-a"
	t.Cleanup(func() {
		for webChildOpen(key) {
			webChildExited(key)
		}
	})
	webChildStarted(key)
	p, _ := openTestPopup(t, r, key)
	r.app.applyPopupPick(context.Background(), p, tray.ModeWeb)
	if r.app.popup != nil {
		t.Fatal("picker stayed open on a duplicate Web pick")
	}
	if len(r.webbed) != 0 {
		t.Fatalf("duplicate web child spawned: %+v", r.webbed)
	}
	if r.app.m.Notice == "" {
		t.Fatal("duplicate Web pick reported nothing")
	}
}

// TestCloseWithTrayAsksFirst: the main window's close button opens the
// quit-or-minimize question instead of quitting when a tray is live.
func TestCloseWithTrayAsksFirst(t *testing.T) {
	r, _ := trayRig(t)
	if r.app.tray == nil {
		t.Fatal("no tray backend after start")
	}
	r.be.send(viewer.EventWindowClose{})
	r.step()
	if r.app.quit {
		t.Fatal("window close quit outright with a live tray")
	}
	if !r.app.m.CloseConfirm {
		t.Fatal("window close did not open the close question")
	}
}

// TestCloseWithoutTrayQuits: with no tray there is nothing to minimize
// into, so the close quits exactly as before.
func TestCloseWithoutTrayQuits(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.start()
	r.settle()
	if r.app.tray != nil {
		t.Fatal("fake backend should get no tray")
	}
	r.be.send(viewer.EventWindowClose{})
	r.step()
	if !r.app.quit {
		t.Fatal("window close did not quit with no tray")
	}
	if r.app.m.CloseConfirm {
		t.Fatal("close question opened with no tray")
	}
}

// TestCloseConfirmQuit: answering Quit quits and closes the question.
func TestCloseConfirmQuit(t *testing.T) {
	r, _ := trayRig(t)
	r.app.m.ShowCloseConfirm()
	r.app.act(context.Background(), intent{kind: intentCloseQuit})
	if !r.app.quit {
		t.Fatal("Quit answer did not quit")
	}
	if r.app.m.CloseConfirm {
		t.Fatal("question stayed open after Quit")
	}
}

// TestCloseConfirmCancel: Esc dismisses the question; the app keeps running
// with its window.
func TestCloseConfirmCancel(t *testing.T) {
	r, _ := trayRig(t)
	r.app.m.ShowCloseConfirm()
	r.press(keysym.KeyEscape, keysym.ModNone)
	if r.app.m.CloseConfirm {
		t.Fatal("Esc did not dismiss the close question")
	}
	if r.app.quit || r.app.shellHidden || r.be.isHidden() {
		t.Fatal("cancel changed more than the question")
	}
}

// TestCloseConfirmMinimizeHides: answering Minimize hides the main window
// without quitting, and live sessions keep stepping beside the tray.
func TestCloseConfirmMinimizeHides(t *testing.T) {
	r, _ := trayRig(t)
	r.app.m.Open(lookupMust(t, r, "team/vm-a"))
	r.settle()
	if !r.app.isLive("team/vm-a") {
		t.Fatalf("no live display (state %v, err %q)", r.app.m.State, r.app.m.Err)
	}
	w := r.app.live["team/vm-a"].window.(*fakeLiveWindow)
	r.app.m.ShowCloseConfirm()
	r.app.act(context.Background(), intent{kind: intentCloseMinimize})
	if r.app.quit {
		t.Fatal("minimize quit the app")
	}
	if r.app.m.CloseConfirm {
		t.Fatal("question stayed open after Minimize")
	}
	if !r.app.shellHidden || !r.be.isHidden() {
		t.Fatal("main window did not hide")
	}
	steps := w.steps
	for i := 0; i < 3; i++ {
		r.step()
	}
	if !r.app.isLive("team/vm-a") {
		t.Fatal("session parked when the main window hid")
	}
	if w.steps <= steps {
		t.Fatal("session stopped stepping while the main window hid")
	}
}

// TestCloseRepeatWhileModalStays: hammering the close button with the
// question open neither quits nor stacks questions.
func TestCloseRepeatWhileModalStays(t *testing.T) {
	r, _ := trayRig(t)
	r.be.send(viewer.EventWindowClose{})
	r.step()
	r.be.send(viewer.EventWindowClose{})
	r.step()
	if r.app.quit {
		t.Fatal("repeat close quit with the question open")
	}
	if !r.app.m.CloseConfirm {
		t.Fatal("question closed without an answer")
	}
}

// TestTrayShowRestoresHiddenWindow: the tray's opener shows a minimized
// main window; on a visible one it only raises.
func TestTrayShowRestoresHiddenWindow(t *testing.T) {
	r, _ := trayRig(t)
	r.app.hideShellToTray()
	if !r.be.isHidden() {
		t.Fatal("hide did not hide")
	}
	raises := r.be.raises
	r.app.trayCh <- tray.Action{Kind: tray.ActionShow}
	r.step()
	if r.be.isHidden() || r.app.shellHidden {
		t.Fatal("Show action did not restore the window")
	}
	if r.be.raises != raises+1 {
		t.Fatal("Show action did not raise the window")
	}
}

// TestDisableTrayWhileHiddenRestores: switching the tray off while the
// window is hidden in it shows the window first — otherwise the process
// would run on with no way back in.
func TestDisableTrayWhileHiddenRestores(t *testing.T) {
	r, _ := trayRig(t)
	r.app.hideShellToTray()
	r.app.applySettings(Settings{Style: r.app.settings.Style, Mode: r.app.settings.Mode, UIScale: r.app.settings.UIScale, Tray: false})
	r.step()
	if r.app.tray != nil {
		t.Fatal("tray survives being switched off")
	}
	if r.be.isHidden() || r.app.shellHidden {
		t.Fatal("window stayed hidden with the tray gone")
	}
}

// TestTrayOpenShowsPopupWhenHidden: a tray click that needs the picker
// while the main window is minimized opens the picker popup and leaves
// the shell hidden — the popup is a window of its own.
func TestTrayOpenShowsPopupWhenHidden(t *testing.T) {
	r, _ := trayRig(t)
	popupBackend(r)
	r.app.hideShellToTray()
	if !r.app.shellHidden {
		t.Fatal("hide did not hide")
	}
	r.app.trayCh <- tray.Action{Kind: tray.ActionOpen, Key: "team/vm-a"}
	r.step()
	p := r.app.popup
	if p == nil || p.kind != popupPick || p.ws.Key() != "team/vm-a" {
		t.Fatalf("popup = %+v, want the team/vm-a picker", p)
	}
	if !r.app.shellHidden || !r.be.isHidden() {
		t.Fatal("picker popup popped the hidden shell")
	}
}

// TestTrayOpenLiveKeepsShellHidden: focusing a live window needs no shell
// UI, so a hidden shell stays hidden and only the session raises.
func TestTrayOpenLiveKeepsShellHidden(t *testing.T) {
	r, _ := trayRig(t)
	r.app.m.Open(lookupMust(t, r, "team/vm-a"))
	r.settle()
	r.app.hideShellToTray()
	w := r.app.live["team/vm-a"].window.(*fakeLiveWindow)
	raises := w.raises
	r.app.trayCh <- tray.Action{Kind: tray.ActionOpen, Key: "team/vm-a"}
	r.step()
	if w.raises != raises+1 {
		t.Fatal("tray click did not raise the live window")
	}
	if !r.app.shellHidden || !r.be.isHidden() {
		t.Fatal("focusing a live window popped the hidden shell")
	}
}

// TestTrayOpenStaleShowsShellWhenHidden: a stale click's only outcome is a
// notice, which needs a visible window to be seen.
func TestTrayOpenStaleShowsShellWhenHidden(t *testing.T) {
	r, _ := trayRig(t)
	r.app.hideShellToTray()
	r.app.trayCh <- tray.Action{Kind: tray.ActionOpen, Key: "team/gone"}
	r.step()
	if r.app.m.Notice == "" {
		t.Fatal("stale click reported nothing")
	}
	if r.app.shellHidden || r.be.isHidden() {
		t.Fatal("stale-click notice went to a hidden window")
	}
}

// TestTrayAboutKeepsShellHidden: the About popup is a window of its own,
// so a minimized shell stays minimized behind it.
func TestTrayAboutKeepsShellHidden(t *testing.T) {
	r, _ := trayRig(t)
	popupBackend(r)
	r.app.hideShellToTray()
	r.app.trayCh <- tray.Action{Kind: tray.ActionAbout}
	r.step()
	if r.app.popup == nil || r.app.popup.kind != popupAbout {
		t.Fatal("About action did not open the popup")
	}
	if !r.app.shellHidden || !r.be.isHidden() {
		t.Fatal("About popup popped the hidden shell")
	}
}
