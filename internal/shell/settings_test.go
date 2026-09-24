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
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

func TestDefaultSettingsFollowTheSystemAndFallBackToDark(t *testing.T) {
	r := newRig(nil, "")
	r.start()

	if r.app.settings != DefaultSettings() {
		t.Fatalf("an unconfigured client used %+v, want %+v", r.app.settings, DefaultSettings())
	}
	if r.app.settings.Mode != ui.ModeSystem {
		t.Fatalf("an unconfigured client should follow the platform, not pin a scheme: %+v", r.app.settings)
	}
	// The test backend has no opinion (unknown scheme), so the default
	// resolves to the look this client has always launched with. The style
	// part of the default is bubbly, whatever the scheme does.
	th := r.app.opts.Theme
	if th.Body != 2 || th.Radius != 2 {
		t.Fatalf("the default client is not bubbly (body=%d radius=%d)", th.Body, th.Radius)
	}
	darkBg := color.RGBA{R: 0x14, G: 0x16, B: 0x1a, A: 0xff}
	if th.Background != darkBg {
		t.Fatalf("an unknown platform scheme resolved to %v, want dark", th.Background)
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

func TestSystemModeResolvesThePlatformScheme(t *testing.T) {
	lightBg := color.RGBA{R: 0xf6, G: 0xf7, B: 0xf9, A: 0xff}
	darkBg := color.RGBA{R: 0x14, G: 0x16, B: 0x1a, A: 0xff}
	for _, tc := range []struct {
		name  string
		theme viewer.SystemTheme
		want  color.RGBA
	}{
		{"light", viewer.SystemThemeLight, lightBg},
		{"dark", viewer.SystemThemeDark, darkBg},
		{"unknown", viewer.SystemThemeUnknown, darkBg},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(nil, "")
			r.be.systemTheme = tc.theme
			r.start()

			if got := r.app.opts.Theme.Background; got != tc.want {
				t.Fatalf("platform scheme resolved to %v, want %v", got, tc.want)
			}
			// Following the platform must not touch the stored preference:
			// the mode stays "system", ready to re-resolve next launch.
			if r.app.settings.Mode != ui.ModeSystem {
				t.Fatalf("resolving the platform changed the stored mode to %v", r.app.settings.Mode)
			}
		})
	}
}

func TestPinnedModeOverridesThePlatform(t *testing.T) {
	// Following the system is the default, not a user's choice to override:
	// someone who explicitly picked light keeps light while the platform is
	// dark, and the same on the other side.
	r := newRig(nil, "")
	r.store.settings = config.Settings{Style: "bubbly", Mode: "light"}
	r.be.systemTheme = viewer.SystemThemeDark
	r.start()

	if r.app.settings.Mode != ui.ModeLight {
		t.Fatalf("a stored light mode was not honoured: %+v", r.app.settings)
	}
	lightBg := color.RGBA{R: 0xf6, G: 0xf7, B: 0xf9, A: 0xff}
	if got := r.app.opts.Theme.Background; got != lightBg {
		t.Fatalf("an explicit light mode resolved to %v under a dark platform", got)
	}
}

func TestSystemThemeChangeFollowsWhileRunning(t *testing.T) {
	lightBg := color.RGBA{R: 0xf6, G: 0xf7, B: 0xf9, A: 0xff}
	darkBg := color.RGBA{R: 0x14, G: 0x16, B: 0x1a, A: 0xff}

	r := newRig(nil, "")
	r.start()
	if got := r.app.opts.Theme.Background; got != darkBg {
		t.Fatalf("setup failed: an unknown scheme resolved to %v, want dark", got)
	}

	// The platform flips to light while the client is running. SDL would
	// deliver the event and answer the re-query with the new value; the fake
	// stands in for both.
	r.be.systemTheme = viewer.SystemThemeLight
	r.be.send(viewer.EventSystemTheme{})
	r.settle()
	if got := r.app.opts.Theme.Background; got != lightBg {
		t.Fatalf("the running client did not follow the platform to light (background=%v)", got)
	}

	// And back to dark, the same way.
	r.be.systemTheme = viewer.SystemThemeDark
	r.be.send(viewer.EventSystemTheme{})
	r.settle()
	if got := r.app.opts.Theme.Background; got != darkBg {
		t.Fatalf("the running client did not follow the platform back to dark (background=%v)", got)
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
