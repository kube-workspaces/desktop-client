// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

func profilesRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
	})
	r.store.profiles = []*config.Profile{
		{Name: "other.example.com", Server: "https://other.example.com", Email: "user@example.com"},
	}
	r.store.tokens = map[string]string{"other.example.com": "other-token"}
	r.start()
	return r
}

// TestProfilesModalListsProfiles: the header button opens the switcher with
// every configured profile, marking the current one.
func TestProfilesModalListsProfiles(t *testing.T) {
	r := profilesRig(t)

	r.focus(idProfiles)
	r.clickFocused()
	r.step()

	if !r.app.m.Profiles {
		t.Fatal("profiles modal did not open")
	}
	if len(r.app.profiles) != 2 {
		t.Fatalf("profiles = %d, want 2 (current + other)", len(r.app.profiles))
	}
}

// TestSwitchProfileMovesInstances: picking another profile repoints the
// shell, restores its token and refreshes the list.
func TestSwitchProfileMovesInstances(t *testing.T) {
	r := profilesRig(t)

	r.focus(idProfiles)
	r.clickFocused()
	r.step()

	r.app.act(context.Background(), intent{kind: intentSwitchProfile, profile: "other.example.com"})
	r.settle()

	if r.app.m.Profiles {
		t.Fatal("profiles modal stayed open after the switch")
	}
	if r.app.profile == nil || r.app.profile.Server != "https://other.example.com" {
		t.Fatalf("profile = %+v, want other.example.com", r.app.profile)
	}
	if got := r.app.api.Token(); got != "other-token" {
		t.Errorf("token = %q, want the other profile's token", got)
	}
	if r.app.m.Server != "https://other.example.com" {
		t.Errorf("model server = %q, want other.example.com", r.app.m.Server)
	}
	if r.app.m.State != StateWorkspaces {
		t.Errorf("state = %v, want workspaces after a stored-token switch", r.app.m.State)
	}
}

// TestSwitchProfileWithoutTokenAsksToSignIn: a profile with no stored token
// lands on the login screen for its own instance.
func TestSwitchProfileWithoutTokenAsksToSignIn(t *testing.T) {
	r := profilesRig(t)
	r.store.tokens = map[string]string{}

	r.app.act(context.Background(), intent{kind: intentSwitchProfile, profile: "other.example.com"})
	r.settle()

	if r.app.m.State != StateLogin {
		t.Errorf("state = %v, want login for a tokenless profile", r.app.m.State)
	}
	if r.app.m.Server != "https://other.example.com" {
		t.Errorf("model server = %q, want other.example.com", r.app.m.Server)
	}
}

// TestSwitchProfileToCurrentJustCloses: picking the active profile is a
// no-op that dismisses the modal.
func TestSwitchProfileToCurrentJustCloses(t *testing.T) {
	r := profilesRig(t)

	r.focus(idProfiles)
	r.clickFocused()
	r.step()

	r.app.act(context.Background(), intent{kind: intentSwitchProfile, profile: "kw.example.com"})
	r.step()

	if r.app.m.Profiles {
		t.Fatal("profiles modal stayed open after picking the current profile")
	}
	if r.app.profile.Server != "https://kw.example.com" {
		t.Errorf("profile moved to %+v, want no move", r.app.profile)
	}
}

// TestSwitchProfileMissingIsAnError: a profile deleted (e.g. by the CLI)
// while the shell runs reports instead of switching nowhere.
func TestSwitchProfileMissingIsAnError(t *testing.T) {
	r := profilesRig(t)

	r.app.act(context.Background(), intent{kind: intentSwitchProfile, profile: "gone.example.com"})
	r.step()

	if r.app.m.Err == "" {
		t.Fatal("missing profile produced no error")
	}
	if r.app.profile.Server != "https://kw.example.com" {
		t.Errorf("profile moved to %+v, want no move", r.app.profile)
	}
}
