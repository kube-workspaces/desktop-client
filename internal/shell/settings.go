// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

// Settings is the client's own appearance as the shell understands it: typed,
// so the screens and the theme switch can read fields and compare them, rather
// than the untyped strings [config.Settings] stores on disk.
//
// The type belongs to the shell and not to config because a config package
// should not depend on a UI package; the mapping is the two function calls
// below and nothing else.
type Settings struct {
	Style ui.Style
	Mode  ui.Mode
}

// DefaultSettings is what a client with no recorded preferences uses. It is
// also what every unknown value in a stored settings block falls back to, so a
// config written by a future version never crashes a current one.
func DefaultSettings() Settings {
	return Settings{Style: ui.StyleBubbly, Mode: ui.ModeDark}
}

// settingsFromConfig interprets a stored settings block. Unknown names are
// dropped for the default rather than rejected, because a settings file is
// the one place a user might reasonably have hand-edited a value.
func settingsFromConfig(c config.Settings) Settings {
	s := DefaultSettings()
	if v, ok := ui.ParseStyle(c.Style); ok {
		s.Style = v
	}
	if v, ok := ui.ParseMode(c.Mode); ok {
		s.Mode = v
	}
	return s
}

// toConfig flattens the typed settings to what the store persists.
func (s Settings) toConfig() config.Settings {
	return config.Settings{Style: s.Style.String(), Mode: s.Mode.String()}
}

// applySettings installs the theme for the given settings and asks for a
// redraw. The theme lives in two places — the Context the widgets read and the
// Options the screen functions read — and this keeps them the same pointer, as
// [App.New] did when it built them.
func (a *App) applySettings(s Settings) {
	a.settings = s
	a.opts.Theme = ui.ThemeFor(s.Style, s.Mode)
	a.ctx.Theme = a.opts.Theme
	a.dirty = true
}

// saveSettings persists the current settings.
//
// The write is performed by the loop's own goroutine, exactly like the profile
// saves in the sign-in flow: what is offloaded is the scheduling, not the
// store access, so the store is only ever touched from one goroutine and a
// test that settles can assert on what was saved.
func (a *App) saveSettings() {
	saved := a.settings.toConfig()
	a.background(func() func() {
		return func() {
			if err := a.opts.Store.SaveSettings(saved); err != nil {
				a.logf("save settings: %v", err)
			}
		}
	})
}
