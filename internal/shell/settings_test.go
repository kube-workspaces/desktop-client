// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"image/color"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

func TestDefaultSettingsAreBubblyDark(t *testing.T) {
	r := newRig(nil, "")
	r.start()

	if r.app.settings != DefaultSettings() {
		t.Fatalf("an unconfigured client used %+v, want bubbly/dark", r.app.settings)
	}
	// The whole shell draws with the theme the settings name: the Context the
	// widgets read and the Options the screens read must be the same look.
	th := r.app.opts.Theme
	if th.Body != 2 || th.Radius != 2 {
		t.Fatalf("the default client is not bubbly (body=%d radius=%d)", th.Body, th.Radius)
	}
	if r.app.ctx.Theme != th {
		t.Fatal("the Context and the Options disagree about the theme")
	}
}

func TestStoredSettingsApplyToTheWholeShellAtStartup(t *testing.T) {
	r := newRig(nil, "")
	r.store.settings = config.Settings{Style: "retro", Mode: "light"}
	r.start()

	if r.app.settings.Style != ui.StyleRetro || r.app.settings.Mode != ui.ModeLight {
		t.Fatalf("settings = %+v, want retro light", r.app.settings)
	}
	th := r.app.opts.Theme
	if th.Body != 2 || th.Radius != 6 {
		t.Fatalf("the retro style was not applied (body=%d radius=%d)", th.Body, th.Radius)
	}
	lightBg := color.RGBA{R: 0xf6, G: 0xf7, B: 0xf9, A: 0xff}
	if th.Background != lightBg {
		t.Fatalf("the light mode was not applied (background=%v)", th.Background)
	}
	if r.app.ctx.Theme != th {
		t.Fatal("the first frame drew without the stored theme")
	}
}

func TestUnknownSettingsFallBackToDefaults(t *testing.T) {
	r := newRig(nil, "")
	r.store.settings = config.Settings{Style: "chartreuse", Mode: "sepia"}
	r.start()

	if r.app.settings != DefaultSettings() {
		t.Fatalf("a settings block naming nothing produced %+v", r.app.settings)
	}
}

func TestSettingsScreenSwitchesStyleAndColours(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
	})
	r.start()
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("setup failed: state = %v", r.app.m.State)
	}

	// The header's Settings button opens the screen.
	r.focus(idSettings)
	r.clickFocused()
	r.settle()
	if r.app.m.State != StateSettings {
		t.Fatalf("the settings button left the shell on %v", r.app.m.State)
	}

	// Turning retro restyles the shell immediately and remembers the choice.
	r.focus(idStyleRetro)
	r.clickFocused()
	r.settle()
	if r.app.settings.Style != ui.StyleRetro {
		t.Fatalf("retro was not selected: %+v", r.app.settings)
	}
	if r.app.opts.Theme.Body != 2 || r.app.opts.Theme.Radius != 6 {
		t.Fatalf("the retro style did not re-theme the shell (body=%d radius=%d)",
			r.app.opts.Theme.Body, r.app.opts.Theme.Radius)
	}
	if r.store.settings.Style != "retro" {
		t.Fatalf("the choice was not persisted: %+v", r.store.settings)
	}

	// Light swaps the ink without dropping the style that was just picked.
	r.focus(idModeLight)
	r.clickFocused()
	r.settle()
	if r.app.settings.Mode != ui.ModeLight {
		t.Fatalf("light was not selected: %+v", r.app.settings)
	}
	lightBg := color.RGBA{R: 0xf6, G: 0xf7, B: 0xf9, A: 0xff}
	if r.app.opts.Theme.Background != lightBg {
		t.Fatal("the light colours did not arrive")
	}
	if r.app.opts.Theme.Body != 2 {
		t.Fatal("picking a colour reset the retro style")
	}
	if r.store.settings.Mode != "light" || r.store.settings.Style != "retro" {
		t.Fatalf("the saved settings are %+v, want retro light", r.store.settings)
	}

	// Clean restyles with its own metrics and face without dropping the mode.
	r.focus(idStyleClean)
	r.clickFocused()
	r.settle()
	if r.app.settings.Style != ui.StyleClean {
		t.Fatalf("clean was not selected: %+v", r.app.settings)
	}
	th := r.app.opts.Theme
	if th.Radius != 2 {
		t.Fatalf("the clean style did not re-theme the shell (radius=%d)", th.Radius)
	}
	if th.Font.Advance == nil {
		t.Fatal("the clean typeface is not in use (advance is monospace)")
	}
	if r.app.settings.Mode != ui.ModeLight {
		t.Fatal("picking a style reset the colour mode")
	}
	if r.store.settings.Style != "clean" || r.store.settings.Mode != "light" {
		t.Fatalf("the saved settings are %+v, want clean light", r.store.settings)
	}

	// Escape closes the screen back to the list.
	r.press(keysym.KeyEscape, keysym.ModNone)
	r.settle()
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("Escape left the shell on %v", r.app.m.State)
	}
}

func TestSettingsScreenDoneCloses(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.api.set(func(f *fakeAPI) {
		f.workspaces = []kwclient.Workspace{workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)}
	})
	r.start()

	r.focus(idSettings)
	r.clickFocused()
	r.settle()
	if r.app.m.State != StateSettings {
		t.Fatalf("setup failed: state = %v", r.app.m.State)
	}

	r.focus(idSettingsDone)
	r.clickFocused()
	r.settle()
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("Done left the shell on %v", r.app.m.State)
	}
}
