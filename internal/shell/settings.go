// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"time"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/ui"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
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
	// UIScale pins the interface scale: 0 means automatic (follow the
	// display). A positive value is the factor the theme is multiplied by;
	// the settings screen offers 1, 1.5 and 2, and the --ui-scale flag
	// accepts anything in [1, 3].
	UIScale float64
}

// DefaultSettings is what a client with no recorded preferences uses. It is
// also what every unknown value in a stored settings block falls back to, so a
// config written by a future version never crashes a current one.
//
// The default mode is "follow the platform": an unconfigured client looks like
// the desktop it is running on. A platform that reports no scheme — or a
// backend that cannot say — resolves to dark, the look this client launched
// with, so the default is only visibly different on a desktop that is light.
func DefaultSettings() Settings {
	return Settings{Style: ui.StyleBubbly, Mode: ui.ModeSystem}
}

// uiScaleSteps are the pinned scales the settings screen offers, in row
// order. The first entry is automatic; the rest are factors.
func uiScaleSteps() []float64 { return []float64{0, 1, 1.5, 2} }

// uiScaleIndex maps a scale to its settings-screen row, falling back to
// automatic for anything the row does not offer (a flag-pinned 1.25, or a
// hand-edited value).
func uiScaleIndex(f float64) int {
	for i, s := range uiScaleSteps() {
		if s == f {
			return i
		}
	}
	return 0
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
	if c.UIScale > 0 && c.UIScale <= 3 {
		s.UIScale = c.UIScale
	}
	return s
}

// toConfig flattens the typed settings to what the store persists.
func (s Settings) toConfig() config.Settings {
	return config.Settings{Style: s.Style.String(), Mode: s.Mode.String(), UIScale: s.UIScale}
}

// interfaceScale resolves the theme multiplier: the launch flag first, then
// the stored setting, then the display the window is on.
func (a *App) interfaceScale() float64 {
	if a.opts.UIScale > 0 {
		return a.opts.UIScale
	}
	if a.settings.UIScale > 0 {
		return a.settings.UIScale
	}
	return ui.QuantizeUIScale(a.be.ScaleFactor())
}

// refreshTheme rebuilds the theme from the style, mode and effective
// interface scale. The theme lives in two places — the Context the widgets
// read and the Options the screen functions read — and this keeps them the
// same pointer, as [App.New] did when it built them.
func (a *App) refreshTheme() {
	th := ui.ThemeFor(a.settings.Style, a.resolveMode()).Scaled(a.interfaceScale())
	a.opts.Theme = th
	a.ctx.Theme = th
	a.dirty = true
}

// resolveMode turns the stored preference into the concrete mode the theme is
// built from. ModeSystem is an instruction, not a scheme: it names the
// platform's preference, read from the backend's [viewer.SystemThemeProvider]
// capability when there is one. Unknown — no capability, or a platform that
// does not report — resolves to dark, the client's historic default.
func (a *App) resolveMode() ui.Mode {
	if a.settings.Mode != ui.ModeSystem {
		return a.settings.Mode
	}
	if p, ok := a.be.(viewer.SystemThemeProvider); ok {
		switch p.SystemTheme() {
		case viewer.SystemThemeLight:
			return ui.ModeLight
		case viewer.SystemThemeDark:
			return ui.ModeDark
		}
	}
	return ui.ModeDark
}

// applySettings installs the appearance for the given settings and asks for a
// redraw.
func (a *App) applySettings(s Settings) {
	a.settings = s
	a.refreshTheme()
}

// saveSettings persists the current settings.
//
// The write is performed by the loop's own goroutine, exactly like the profile
// saves in the sign-in flow: what is offloaded is the scheduling, not the
// store access, so the store is only ever touched from one goroutine and a
// test that settles can assert on what was saved.
func (a *App) saveSettings() {
	a.background(func() func() {
		return func() {
			// Results may arrive out of order; persist the current preferences,
			// not the snapshot from when this save was scheduled.
			saved := a.settings.toConfig()
			saved.AutoUpdate = config.Bool(a.updates.auto)
			saved.LastUpdateCheck = a.updates.last
			saved.WindowWidth, saved.WindowHeight = a.geomW, a.geomH
			if err := a.opts.Store.SaveSettings(saved); err != nil {
				a.logf("save settings: %v", err)
			}
		}
	})
}

// geomSaveInterval rate-limits geometry writes: a live resize drag produces a
// resize event per frame, and each one fsyncing the config would be felt.
const geomSaveInterval = 5 * time.Second

// recordGeometry notes a new window size and persists it when due. The final
// size of a session is always written on the way out (see [App.Run]), so the
// interval only bounds writes mid-drag.
func (a *App) recordGeometry(now time.Time) {
	w, h := a.be.Size()
	if w <= 0 || h <= 0 || (w == a.geomW && h == a.geomH) {
		return
	}
	a.geomW, a.geomH = w, h
	if now.Sub(a.geomSavedAt) < geomSaveInterval {
		return
	}
	a.geomSavedAt = now
	a.saveSettings()
}

// flushGeometry writes the current window size synchronously. It runs on the
// way out ([App.Run] defers it) so the size at close — the one the user
// arranged — is what the next launch restores, regardless of where the
// rate limiter stood.
func (a *App) flushGeometry() {
	w, h := a.be.Size()
	if w <= 0 || h <= 0 {
		return
	}
	a.geomW, a.geomH = w, h
	saved := a.settings.toConfig()
	saved.AutoUpdate = config.Bool(a.updates.auto)
	saved.LastUpdateCheck = a.updates.last
	saved.WindowWidth, saved.WindowHeight = w, h
	if err := a.opts.Store.SaveSettings(saved); err != nil {
		a.logf("save window size: %v", err)
	}
}
