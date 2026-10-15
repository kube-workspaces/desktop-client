// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

// windowsWorkspace returns a running managed-Windows workspace: profile
// set, provisioned when asked, agent transport advertised.
func windowsWorkspace(provisioned bool) kwclient.Workspace {
	profile := kwclient.VMProfileWindows11AMD64V1
	ws := kwclient.Workspace{
		Name:          "win-0",
		Namespace:     "team",
		Type:          kwclient.WorkspaceTypeVM,
		Image:         "registry/private/windows11@sha256:0",
		ReadyReplicas: 1,
		VMProfile:     &profile,
		RemoteDesktop: &kwclient.ImageRemoteDesktop{Protocol: "agent", Port: 44787},
	}
	if provisioned {
		yes := true
		ws.Provisioned = &yes
	}
	return ws
}

func windowsImage() kwclient.Image {
	profile := kwclient.VMProfileWindows11AMD64V1
	size := "80Gi"
	yes := true
	return kwclient.Image{
		Name: "winimg", DisplayName: "Windows 11", Image: "registry/private/windows11@sha256:0",
		DefaultPath: "/", WorkspaceTypes: []string{"vm"},
		VMProfile: &profile, PersistentRootDisk: &yes, PersistentRootDiskSize: &size,
		RemoteDesktop: &kwclient.ImageRemoteDesktop{Protocol: "agent", Port: 44787},
	}
}

// windowsRig starts a rig with one provisioned Windows workspace selected.
func windowsRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{windowsWorkspace(true)}
		f.images = []kwclient.Image{windowsImage()}
	})
	r.start()
	r.app.m.Selected = "team/win-0"
	return r
}

// TestWindowsFooterHidesConsolesShowsSignin: a Windows workspace offers
// Observe and Sign-in where a Linux VM offers Observe, Serial and SSH.
func TestWindowsFooterHidesConsolesShowsSignin(t *testing.T) {
	r := windowsRig(t)
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
	for _, id := range []ui.FocusID{idOpen, idObserve, idCredentials, idStop, idInfo} {
		if at(id) < 0 {
			t.Fatalf("footer button %q missing from traversal order %v", id, order)
		}
	}
	for _, id := range []ui.FocusID{idSerial, idSSH} {
		if at(id) >= 0 {
			t.Fatalf("footer button %q offered for a Windows guest", id)
		}
	}
	if at(idOpen) > at(idObserve) || at(idObserve) > at(idCredentials) || at(idCredentials) > at(idStop) {
		t.Fatalf("footer order = %v, want display, observe, sign-in, stop in sequence", order)
	}
}

// TestWindowsSerialSSHGuarded: stale serial/SSH intents against a Windows
// guest explain themselves instead of opening a useless console.
func TestWindowsSerialSSHGuarded(t *testing.T) {
	r := windowsRig(t)
	ctx := context.Background()
	ws := windowsWorkspace(true)

	r.app.act(ctx, intent{kind: intentOpenSerial, workspace: ws})
	if r.app.m.Notice == "" {
		t.Fatal("serial on Windows produced no notice")
	}
	r.app.act(ctx, intent{kind: intentOpenSSH, workspace: ws})
	if r.app.m.Notice == "" {
		t.Fatal("ssh on Windows produced no notice")
	}
	if len(r.app.m.Sessions) != 0 {
		t.Fatalf("sessions = %v, want none held", r.app.m.Sessions)
	}
	if r.app.m.SSH != nil {
		t.Fatal("SSH form opened for a Windows guest")
	}
}

// TestWindowsSSHFormGuarded: the SSH submit path refuses Windows even when
// the form somehow names one.
func TestWindowsSSHFormGuarded(t *testing.T) {
	r := windowsRig(t)
	ws := windowsWorkspace(true)
	r.app.m.ShowSSH(ws)
	r.app.submitSSH()
	if r.app.m.SSH != nil {
		t.Fatal("SSH form stayed open for a Windows guest")
	}
	if r.app.m.Err == "" {
		t.Fatal("no error reported for Windows SSH submit")
	}
}

// TestCredentialsFlow: Sign-in fetches the initial account and shows it;
// closing forgets the password.
func TestCredentialsFlow(t *testing.T) {
	r := windowsRig(t)
	r.app.act(context.Background(), intent{kind: intentCredentials, workspace: windowsWorkspace(true)})
	r.settle()

	creds := r.app.m.Credentials
	if creds == nil {
		t.Fatalf("no credentials modal (err %q)", r.app.m.Err)
	}
	if creds.Username != "workspace" || creds.Password != "s3cret!9aA" {
		t.Fatalf("credentials = %+v, want the fake pair", creds)
	}
	if creds.Workspace.Key() != "team/win-0" {
		t.Fatalf("workspace = %q, want team/win-0", creds.Workspace.Key())
	}

	r.app.act(context.Background(), intent{kind: intentCredentialsClose})
	if r.app.m.Credentials != nil {
		t.Fatal("credentials survived close; the password must be forgotten")
	}
}

// TestCredentialsUnreadyIgnored: Sign-in on a still-provisioning Windows
// guest does nothing — the button is not offered there either.
func TestCredentialsUnreadyIgnored(t *testing.T) {
	r := windowsRig(t)
	r.app.act(context.Background(), intent{kind: intentCredentials, workspace: windowsWorkspace(false)})
	r.settle()
	if r.app.m.Credentials != nil {
		t.Fatal("credentials opened for an unprovisioned guest")
	}
}

// TestCredentialsFetchError: a failing fetch reports instead of opening.
func TestCredentialsFetchError(t *testing.T) {
	r := windowsRig(t)
	r.api.set(func(f *fakeAPI) { f.credsErr = errors.New("credential store exploded") })
	r.app.act(context.Background(), intent{kind: intentCredentials, workspace: windowsWorkspace(true)})
	r.settle()
	if r.app.m.Credentials != nil {
		t.Fatal("credentials opened despite the fetch failure")
	}
	if r.app.m.Err == "" {
		t.Fatal("fetch failure produced no error")
	}
}

// TestWindowsInfoRows: the info modal names the profile and setup state.
func TestWindowsInfoRows(t *testing.T) {
	rows := workspaceInfoRows(ptrWorkspace(windowsWorkspace(true)))
	profile, setup := "", ""
	for _, row := range rows {
		switch row[0] {
		case "Profile":
			profile = row[1]
		case "Windows setup":
			setup = row[1]
		}
	}
	if profile != kwclient.VMProfileWindows11AMD64V1 {
		t.Errorf("profile row = %q, want the Windows profile", profile)
	}
	if setup == "" {
		t.Error("no Windows setup row for a Windows guest")
	}
	plain := workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)
	for _, row := range workspaceInfoRows(&plain) {
		if row[0] == "Profile" || row[0] == "Windows setup" {
			t.Errorf("non-Windows info shows %q", row[0])
		}
	}
}

func ptrWorkspace(ws kwclient.Workspace) *kwclient.Workspace { return &ws }

// TestWindowsCreateSendsNodeSelector: a Windows image forces type vm and
// carries the certified worker node; everything else stays server-defaulted.
func TestWindowsCreateSendsNodeSelector(t *testing.T) {
	r := windowsRig(t)

	r.focus(idCreate)
	r.clickFocused()
	r.step()

	// Pick the VM type so the Windows image lists, then select it.
	r.app.createType = kwclient.WorkspaceTypeVM
	r.app.createImageIdx = 0
	r.app.createNodeField.SetValue("talkie")
	r.focus(idCreateName)
	r.typeText("win-1")
	r.focus(idCreateSubmit)
	r.clickFocused()
	r.settle()

	if len(r.api.createCalls) != 1 {
		t.Fatalf("CreateWorkspace calls = %d, want 1", len(r.api.createCalls))
	}
	got := r.api.createCalls[0]
	if got.Type != kwclient.WorkspaceTypeVM {
		t.Errorf("type = %q, want vm for a Windows image", got.Type)
	}
	if got.NodeSelector["kubernetes.io/hostname"] != "talkie" {
		t.Errorf("node_selector = %v, want the certified worker", got.NodeSelector)
	}
	if got.VMOptions != nil {
		t.Errorf("vm_options = %+v, want nil (catalog defaults apply)", got.VMOptions)
	}
	if got.Container == nil || got.Container.Image != "registry/private/windows11@sha256:0" {
		t.Errorf("container = %+v, want the Windows image", got.Container)
	}
}

// TestContainerCreateOmitsWindowsFields: non-Windows creates send neither
// node pinning nor vm options.
func TestContainerCreateOmitsWindowsFields(t *testing.T) {
	r := createTestRig(t)

	r.focus(idCreate)
	r.clickFocused()
	r.step()

	r.focus(idCreateName)
	r.typeText("dev-9")
	r.focus(idCreateSubmit)
	r.clickFocused()
	r.settle()

	if len(r.api.createCalls) != 1 {
		t.Fatalf("CreateWorkspace calls = %d, want 1", len(r.api.createCalls))
	}
	got := r.api.createCalls[0]
	if len(got.NodeSelector) != 0 {
		t.Errorf("node_selector = %v, want empty for a container", got.NodeSelector)
	}
	if got.VMOptions != nil {
		t.Errorf("vm_options = %+v, want nil for a container", got.VMOptions)
	}
}
