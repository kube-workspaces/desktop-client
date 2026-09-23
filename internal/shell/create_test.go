// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"strings"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

func TestValidWorkspaceName(t *testing.T) {
	valid := []string{"a", "dev", "dev-1", "a-b-c-0", strings.Repeat("x", 63)}
	invalid := []string{"", "-dev", "dev-", "Dev", "dev_1", "dev 1", "dev!", strings.Repeat("x", 64), "a/b"}
	for _, name := range valid {
		if !validWorkspaceName(name) {
			t.Errorf("validWorkspaceName(%q) = false, want true", name)
		}
	}
	for _, name := range invalid {
		if validWorkspaceName(name) {
			t.Errorf("validWorkspaceName(%q) = true, want false", name)
		}
	}
}

func createTestRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
		f.images = []kwclient.Image{
			{Name: "code", DisplayName: "VS Code", Image: "img/code", WorkspaceTypes: []string{"container"}},
			{Name: "xfce", DisplayName: "Debian XFCE", Image: "img/xfce", WorkspaceTypes: []string{"vm"}},
		}
	})
	r.start()
	return r
}

// TestCreateModalOpensWithDefaults: the New button opens the form with the
// profile namespace, the container type and a cleared name field.
func TestCreateModalOpensWithDefaults(t *testing.T) {
	r := createTestRig(t)

	r.focus(idCreate)
	r.clickFocused()
	r.step()

	if !r.app.m.Creating {
		t.Fatal("create modal did not open")
	}
	// No profile namespace: the platform default.
	if got := r.app.createNamespaceField.Value(); got != "workspaces" {
		t.Errorf("namespace = %q, want workspaces", got)
	}
	if r.app.createType != kwclient.WorkspaceTypeContainer {
		t.Errorf("type = %q, want container", r.app.createType)
	}
	if got := r.app.createNameField.Value(); got != "" {
		t.Errorf("name = %q, want empty", got)
	}
	r.app.m.CloseCreate()

	// A profile namespace prefills the form instead.
	r.app.profile.Namespace = "team"
	r.focus(idCreate)
	r.clickFocused()
	r.step()
	if got := r.app.createNamespaceField.Value(); got != "team" {
		t.Errorf("namespace = %q, want the profile namespace team", got)
	}
}

// TestCreateWorkspaceSubmits: typing a name and submitting posts the payload,
// closes the modal and shows the new workspace after the refresh.
func TestCreateWorkspaceSubmits(t *testing.T) {
	r := createTestRig(t)

	r.focus(idCreate)
	r.clickFocused()
	r.step()

	r.focus(idCreateName)
	r.typeText("dev-1")
	r.focus(idCreateSubmit)
	r.clickFocused()
	r.settle()

	if len(r.api.createCalls) != 1 {
		t.Fatalf("CreateWorkspace calls = %d, want 1", len(r.api.createCalls))
	}
	got := r.api.createCalls[0]
	if got.Name != "dev-1" || got.Namespace != "workspaces" || got.Type != kwclient.WorkspaceTypeContainer {
		t.Errorf("payload = %+v, want dev-1/workspaces/container", got)
	}
	if got.Container == nil || got.Container.Image != "img/code" || got.Container.Name != "dev-1" {
		t.Errorf("container = %+v, want name dev-1 image img/code", got.Container)
	}
	if r.app.m.Creating {
		t.Fatal("create modal stayed open after success")
	}
	if !strings.Contains(r.app.m.Notice, "dev-1") {
		t.Errorf("notice = %q, want it to name the created workspace", r.app.m.Notice)
	}
	found := false
	for _, ws := range r.app.m.Workspaces {
		if ws.Name == "dev-1" {
			found = true
		}
	}
	if !found {
		t.Fatal("created workspace missing from the refreshed list")
	}
}

// TestCreateWorkspaceRejectsBadName: an invalid name fails in the form with no
// network call.
func TestCreateWorkspaceRejectsBadName(t *testing.T) {
	r := createTestRig(t)

	r.focus(idCreate)
	r.clickFocused()
	r.step()

	r.focus(idCreateName)
	r.typeText("Bad_Name!")
	r.focus(idCreateSubmit)
	r.clickFocused()
	r.step()

	if len(r.api.createCalls) != 0 {
		t.Fatalf("CreateWorkspace calls = %d, want 0", len(r.api.createCalls))
	}
	if r.app.m.Err == "" {
		t.Fatal("invalid name produced no error")
	}
	if !r.app.m.Creating {
		t.Fatal("modal closed on a validation failure")
	}
}

// TestCreateWorkspaceTakenName: a 409 keeps the form open with a specific
// error so the user can pick another name.
func TestCreateWorkspaceTakenName(t *testing.T) {
	r := createTestRig(t)
	r.api.set(func(f *fakeAPI) { f.createErr = kwclient.ErrAlreadyExists })

	r.focus(idCreate)
	r.clickFocused()
	r.step()

	r.focus(idCreateName)
	r.typeText("dev-1")
	r.focus(idCreateSubmit)
	r.clickFocused()
	r.settle()

	if len(r.api.createCalls) != 1 {
		t.Fatalf("CreateWorkspace calls = %d, want 1", len(r.api.createCalls))
	}
	if !strings.Contains(r.app.m.Err, "dev-1") {
		t.Errorf("err = %q, want it to name the taken workspace", r.app.m.Err)
	}
	if !r.app.m.Creating {
		t.Fatal("modal closed on a taken name")
	}
}

// TestCreateModalClosesOnEscape: Esc backs out of the form without creating.
func TestCreateModalClosesOnEscape(t *testing.T) {
	r := createTestRig(t)

	r.focus(idCreate)
	r.clickFocused()
	r.step()
	if !r.app.m.Creating {
		t.Fatal("create modal did not open")
	}

	r.press(keysym.KeyEscape, keysym.ModNone)
	if r.app.m.Creating {
		t.Fatal("create modal stayed open after Esc")
	}
	if len(r.api.createCalls) != 0 {
		t.Fatalf("CreateWorkspace calls = %d, want 0", len(r.api.createCalls))
	}
}
