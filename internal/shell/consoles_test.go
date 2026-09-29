// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

// consoleRig starts a rig with one running VM selected.
func consoleRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{
			workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true),
		}
	})
	r.start()
	r.app.m.Selected = "team/vm-a"
	return r
}

// TestVMFooterOffersConsoles pins the VM footer: display, observer and both
// consoles ride the open row before stop and info.
func TestVMFooterOffersConsoles(t *testing.T) {
	r := consoleRig(t)
	r.app.dirty = true
	r.step()
	order := append([]ui.FocusID(nil), r.app.ctx.Focus().Order()...)
	at := func(id ui.FocusID) int {
		for i, got := range order {
			if got == id {
				return i
			}
		}
		return -1
	}
	want := []ui.FocusID{idOpen, idObserve, idSerial, idSSH, idStop, idInfo}
	for _, id := range want {
		if at(id) < 0 {
			t.Fatalf("footer button %q missing from traversal order %v", id, order)
		}
	}
	for i := 1; i < len(want); i++ {
		if at(want[i-1]) > at(want[i]) {
			t.Fatalf("footer order = %v, want display, observe, serial, ssh, stop, info in sequence", order)
		}
	}
}

// openSerial opens the serial console through the footer button and settles.
func openSerial(t *testing.T, r *rig) {
	t.Helper()
	r.focus(idSerial)
	r.clickFocused()
	r.step()
	r.settle()
	if !r.app.isLive("team/vm-a#serial") {
		t.Fatalf("no live serial window after open (state %v, err %q)", r.app.m.State, r.app.m.Err)
	}
}

// TestSerialButtonOpensSerialSeat: the Serial button dials a serial console
// beside the display, keyed apart from it.
func TestSerialButtonOpensSerialSeat(t *testing.T) {
	r := consoleRig(t)
	var kinds []string
	var gotOpts TerminalDialOpts
	d := r.app.dialer.(*fakeDialer)
	d.terminal = func(ctx context.Context, ws kwclient.Workspace, opts TerminalDialOpts) (SessionHandle, error) {
		gotOpts = opts
		kinds = append(kinds, opts.Kind)
		h, err := d.Dial(ctx, ws, false)
		if fh, ok := h.(*fakeHandle); ok {
			fh.kind = opts.Kind
		}
		return h, err
	}

	openSerial(t, r)

	if len(kinds) != 1 || kinds[0] != "serial" {
		t.Fatalf("terminal dials = %v, want [serial]", kinds)
	}
	if gotOpts.Kind != "serial" {
		t.Fatalf("dial kind = %q, want serial", gotOpts.Kind)
	}
	if len(r.app.m.Sessions) != 1 {
		t.Fatalf("sessions = %v, want 1 held", r.app.m.Sessions)
	}
	entry := r.app.m.Sessions[0]
	if entry.Key != "team/vm-a#serial" || entry.Kind != "serial" || !entry.Open {
		t.Fatalf("session entry = %+v, want key team/vm-a#serial kind serial open", entry)
	}
}

// TestSerialAndDisplayCoexist: a display and its serial console are
// independent seats — opening one never disturbs the other.
func TestSerialAndDisplayCoexist(t *testing.T) {
	r := consoleRig(t)

	openSerial(t, r)
	// Open the display too: the primary button still dials a display.
	r.focus(idOpen)
	r.clickFocused()
	r.step()
	r.settle()

	if !r.app.isLive("team/vm-a#serial") || !r.app.isLive("team/vm-a") {
		t.Fatalf("live = %v, want display and serial side by side", liveKeys(r))
	}
	if len(r.app.m.Sessions) != 2 {
		t.Fatalf("sessions = %v, want 2 held", r.app.m.Sessions)
	}
}

// TestSerialResumeFromSwitcher: parking a console and switching back resumes
// the held seat without a new dial.
func TestSerialResumeFromSwitcher(t *testing.T) {
	r := consoleRig(t)
	dials := 0
	d := r.app.dialer.(*fakeDialer)
	prev := d.terminal
	d.terminal = func(ctx context.Context, ws kwclient.Workspace, opts TerminalDialOpts) (SessionHandle, error) {
		dials++
		if prev != nil {
			return prev(ctx, ws, opts)
		}
		return d.Dial(ctx, ws, false)
	}

	openSerial(t, r)
	r.app.live["team/vm-a#serial"].window.(*fakeLiveWindow).closed = true
	r.step()
	r.settle()
	if r.app.isLive("team/vm-a#serial") {
		t.Fatal("serial window still live after its close")
	}

	r.app.act(context.Background(), intent{kind: intentSwitchSession, sessionKey: "team/vm-a#serial"})
	r.step()
	r.settle()
	if dials != 1 {
		t.Fatalf("terminal dials = %d, want 1 (resume reuses the seat)", dials)
	}
	if !r.app.isLive("team/vm-a#serial") {
		t.Fatal("serial console did not resume into a live window")
	}
}

// TestSSHButtonOpensCredentialForm: the SSH button asks for credentials
// rather than dialling blind — the bridge needs a key the client cannot
// invent.
func TestSSHButtonOpensCredentialForm(t *testing.T) {
	r := consoleRig(t)
	r.focus(idSSH)
	r.clickFocused()
	r.step()

	if r.app.m.SSH == nil || r.app.m.SSH.Key() != "team/vm-a" {
		t.Fatalf("SSH form subject = %v, want team/vm-a", r.app.m.SSH)
	}
	r.app.dirty = true
	r.step()
	order := append([]ui.FocusID(nil), r.app.ctx.Focus().Order()...)
	for _, id := range []ui.FocusID{idSSHUser, idSSHKeyFile, idSSHSubmit, idSSHClose} {
		found := false
		for _, got := range order {
			if got == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("SSH form control %q missing from traversal order %v", id, order)
		}
	}
}

// writeKeyFile stages a PEM-looking private key for the SSH form.
func writeKeyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "id_ed25519")
	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n"
	if err := os.WriteFile(path, []byte(pem), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}

// submitSSHForm types credentials and connects.
func submitSSHForm(t *testing.T, r *rig, user, keyFile string) {
	t.Helper()
	r.focus(idSSHUser)
	r.typeText(user)
	r.focus(idSSHKeyFile)
	r.typeText(keyFile)
	r.focus(idSSHSubmit)
	r.clickFocused()
	r.step()
	r.settle()
}

// TestSSHSubmitOpensSSHSeat: valid credentials open an SSH console carrying
// the username and the key bytes (in memory only).
func TestSSHSubmitOpensSSHSeat(t *testing.T) {
	r := consoleRig(t)
	var gotOpts TerminalDialOpts
	d := r.app.dialer.(*fakeDialer)
	d.terminal = func(ctx context.Context, ws kwclient.Workspace, opts TerminalDialOpts) (SessionHandle, error) {
		gotOpts = opts
		h, err := d.Dial(ctx, ws, false)
		if fh, ok := h.(*fakeHandle); ok {
			fh.kind = opts.Kind
		}
		return h, err
	}

	r.focus(idSSH)
	r.clickFocused()
	r.step()
	submitSSHForm(t, r, "debian", writeKeyFile(t))

	if r.app.m.SSH != nil {
		t.Fatal("SSH form stayed open after connect")
	}
	if !r.app.isLive("team/vm-a#ssh") {
		t.Fatalf("no live SSH window after submit (state %v, err %q)", r.app.m.State, r.app.m.Err)
	}
	if gotOpts.Kind != "ssh" || gotOpts.SSHUser != "debian" || len(gotOpts.SSHKeyPEM) == 0 {
		t.Fatalf("dial opts = %+v (key %d bytes), want ssh/debian with a key", gotOpts, len(gotOpts.SSHKeyPEM))
	}
	entry := r.app.m.Sessions[0]
	if entry.Key != "team/vm-a#ssh" || entry.Kind != "ssh" {
		t.Fatalf("session entry = %+v, want key team/vm-a#ssh kind ssh", entry)
	}
}

// TestSSHSubmitValidates: an empty username and an unreadable key file fail
// in the form, never in a blank window.
func TestSSHSubmitValidates(t *testing.T) {
	r := consoleRig(t)
	r.focus(idSSH)
	r.clickFocused()
	r.step()

	// No username.
	submitSSHForm(t, r, "", writeKeyFile(t))
	if r.app.m.Err == "" {
		t.Fatal("empty username submitted without an error")
	}
	if r.app.isLive("team/vm-a#ssh") {
		t.Fatal("SSH window opened without a username")
	}

	// Unreadable key.
	r.focus(idSSHUser)
	r.typeText("debian")
	r.focus(idSSHKeyFile)
	r.typeText(filepath.Join(t.TempDir(), "missing"))
	r.focus(idSSHSubmit)
	r.clickFocused()
	r.step()
	if r.app.m.Err == "" {
		t.Fatal("missing key file submitted without an error")
	}
	if r.app.isLive("team/vm-a#ssh") {
		t.Fatal("SSH window opened without a key")
	}
	if r.app.m.SSH == nil {
		t.Fatal("SSH form closed on a validation failure")
	}
}

// TestDialTerminalValidation pins the production dialer's fail-fast
// rejections: non-VMs, unknown seats and SSH without credentials never
// reach the network.
func TestDialTerminalValidation(t *testing.T) {
	d := &sessionDialer{}
	vm := workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)
	ctr := workspace("team", "code", kwclient.WorkspaceTypeContainer, true)

	if _, err := d.DialTerminal(context.Background(), vm, TerminalDialOpts{Kind: "serial"}); err != nil {
		t.Fatalf("serial dial: %v", err)
	}
	if _, err := d.DialTerminal(context.Background(), ctr, TerminalDialOpts{Kind: "serial"}); err == nil {
		t.Fatal("container serial dial succeeded, want rejection")
	}
	if _, err := d.DialTerminal(context.Background(), vm, TerminalDialOpts{Kind: "vnc"}); err == nil {
		t.Fatal("unknown seat dial succeeded, want rejection")
	}
	if _, err := d.DialTerminal(context.Background(), vm, TerminalDialOpts{Kind: "ssh"}); err == nil {
		t.Fatal("SSH dial without credentials succeeded, want rejection")
	}
	if _, err := d.DialTerminal(context.Background(), vm,
		TerminalDialOpts{Kind: "ssh", SSHUser: "debian", SSHKeyPEM: []byte("PEM")}); err != nil {
		t.Fatalf("SSH dial with credentials: %v", err)
	}
}
