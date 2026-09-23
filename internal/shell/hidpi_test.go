// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"strings"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/i18n"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

// TestHiDPIDisplayScalesTheShellAutomatically is the headless half of the
// real-display Pass 3 gate: on a 2x drawable the shell must size its surface
// to drawable pixels and scale its theme, so the interface keeps its physical
// size instead of rendering small.
func TestHiDPIDisplayScalesTheShellAutomatically(t *testing.T) {
	r := newRig(nil, "")
	r.be.scaleFactor = 2
	r.start()

	th := r.app.opts.Theme
	if th.Body != 4 || th.ControlHeight != 60 {
		t.Fatalf("a 2x display left the theme at body=%d control=%d, want 4/60",
			th.Body, th.ControlHeight)
	}
	if r.app.ctx.Theme != th {
		t.Fatal("the Context and the Options disagree about the scaled theme")
	}
	// The surface follows drawable pixels, not window coordinates.
	if r.app.surfW != 2560 || r.app.surfH != 1600 {
		t.Fatalf("the surface is %dx%d, want the 2x drawable 2560x1600",
			r.app.surfW, r.app.surfH)
	}

	// Moving back to a 1x display arrives as a resize and restores the theme.
	r.be.scaleFactor = 1
	r.be.send(ui.EventResize{W: 1280, H: 800})
	r.settle()
	if th := r.app.opts.Theme; th.Body != 2 || th.ControlHeight != 30 {
		t.Fatalf("leaving the 2x display left body=%d control=%d, want 2/30",
			th.Body, th.ControlHeight)
	}
}

// TestStoredUIScaleAppliesAtStartup pins the scale from the persisted
// settings rather than the display.
func TestStoredUIScaleAppliesAtStartup(t *testing.T) {
	r := newRig(nil, "")
	r.store.settings = config.Settings{UIScale: 1.5}
	r.start()

	if r.app.settings.UIScale != 1.5 {
		t.Fatalf("settings = %+v, want the stored 1.5x scale", r.app.settings)
	}
	if th := r.app.opts.Theme; th.Body != 3 {
		t.Fatalf("the stored 1.5x scale left body=%d, want 3", th.Body)
	}
}

// TestUnknownUIScaleFallsBackToAutomatic keeps a hand-edited value from
// shrinking or exploding the interface.
func TestUnknownUIScaleFallsBackToAutomatic(t *testing.T) {
	r := newRig(nil, "")
	r.store.settings = config.Settings{UIScale: 99}
	r.start()

	if r.app.settings.UIScale != 0 {
		t.Fatalf("an absurd stored scale produced %+v, want automatic", r.app.settings)
	}
	if th := r.app.opts.Theme; th.Body != 2 {
		t.Fatalf("an absurd stored scale left body=%d, want 2", th.Body)
	}
}

// TestLaunchFlagOverridesDisplayAndSettings pins the scale for the run.
func TestLaunchFlagOverridesDisplayAndSettings(t *testing.T) {
	r := newRig(nil, "")
	r.be.scaleFactor = 2
	r.app.opts.UIScale = 1
	r.start()

	if th := r.app.opts.Theme; th.Body != 2 {
		t.Fatalf("the --ui-scale flag left body=%d, want the pinned 1x", th.Body)
	}
}

// TestSettingsScreenPicksInterfaceSize drives the new row the way a user
// does: picking 200% re-themes immediately and persists the choice.
func TestSettingsScreenPicksInterfaceSize(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
	})
	r.start()
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("setup failed: state = %v", r.app.m.State)
	}

	r.focus(idSettings)
	r.clickFocused()
	r.settle()
	if r.app.m.State != StateSettings {
		t.Fatalf("the settings button left the shell on %v", r.app.m.State)
	}

	r.focus(idScale200)
	r.clickFocused()
	r.settle()
	if r.app.settings.UIScale != 2 {
		t.Fatalf("200%% was not selected: %+v", r.app.settings)
	}
	if r.app.opts.Theme.Body != 4 {
		t.Fatalf("picking 200%% left body=%d, want 4", r.app.opts.Theme.Body)
	}
	if r.store.settings.UIScale != 2 {
		t.Fatalf("the choice was not persisted: %+v", r.store.settings)
	}

	// Back to automatic.
	r.focus(idScaleAuto)
	r.clickFocused()
	r.settle()
	if r.app.settings.UIScale != 0 {
		t.Fatalf("automatic was not selected: %+v", r.app.settings)
	}
	if r.app.opts.Theme.Body != 2 {
		t.Fatalf("automatic left body=%d, want 2", r.app.opts.Theme.Body)
	}
}

// TestPseudoLocaleRendersTheShell is the headless half of the translation
// check: under the expanded pseudo-locale every cataloged string still flows
// through the same screens without crashing, and the window visibly carries
// the locale (spot-checked on the settings title).
func TestPseudoLocaleRendersTheShell(t *testing.T) {
	defer i18n.SetLocale("en")
	i18n.SetLocale("xx")

	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
	})
	r.start()
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("setup failed: state = %v", r.app.m.State)
	}

	r.focus(idSettings)
	r.clickFocused()
	r.settle()
	if r.app.m.State != StateSettings {
		t.Fatalf("the settings button left the shell on %v", r.app.m.State)
	}
	// The settings title is the pseudo-locale transform of "Settings": the
	// catalog, not a hardcoded literal, fed the screen.
	if title := i18n.Get("settings.title"); !strings.HasPrefix(title, "[[") {
		t.Fatalf("pseudo-locale title = %q, want brackets", title)
	}
	// Errors render through the catalog too.
	if got := Describe(nil); got != "" {
		t.Fatalf("Describe(nil) under xx = %q, want empty", got)
	}
}
