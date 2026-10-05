// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"fmt"

	"github.com/kube-workspaces/desktop-client/internal/autostart"
	"github.com/kube-workspaces/desktop-client/internal/i18n"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

type startupState struct {
	status autostart.Status
	busy   bool
	err    string
}

func (a *App) refreshStartup() {
	if a.startup.busy {
		return
	}
	a.startup.busy = true
	a.background(func() func() {
		status, err := a.opts.Autostart.Status()
		return func() { a.finishStartup(status, err) }
	})
}

func (a *App) toggleStartup() {
	if a.startup.busy {
		return
	}
	enabled := !a.startup.status.Enabled
	a.startup.busy, a.startup.err = true, ""
	a.background(func() func() {
		status, err := a.opts.Autostart.SetEnabled(enabled)
		if err != nil {
			// Registration might have succeeded even if a subsequent status
			// query failed. Re-read instead of presenting a stale selection.
			if actual, readErr := a.opts.Autostart.Status(); readErr == nil {
				status = actual
			}
		}
		return func() { a.finishStartup(status, err) }
	})
}

func (a *App) finishStartup(status autostart.Status, err error) {
	a.startup.status, a.startup.busy, a.startup.err = status, false, ""
	if err != nil {
		a.startup.err = fmt.Sprintf(i18n.Get("settings.startupError"), err)
	}
	a.dirty = true
}

func (a *App) finishSettingsScroll(view ui.Rect, bottom int) {
	a.finishPreferenceScroll(view, bottom, &a.settingsScroll, &a.settingsFocus, a.settingsFocusedRect)
}

// finishPreferenceScroll keeps a preference page's focused control visible.
func (a *App) finishPreferenceScroll(view ui.Rect, bottom int, offset *int, previousFocus *ui.FocusID, focusedRect ui.Rect) {
	contentHeight := bottom + *offset - view.Y
	scroll := *offset
	if focus := a.ctx.Focus().Focus(); focus != *previousFocus {
		r := focusedRect
		if r.H > 0 {
			if r.Y < view.Y {
				scroll -= view.Y - r.Y
			} else if r.Y+r.H > view.Y+view.H {
				scroll += r.Y + r.H - view.Y - view.H
			}
		}
		*previousFocus = focus
	}
	scroll = max(0, min(scroll, contentHeight-view.H))
	if scroll != *offset {
		*offset = scroll
		a.ctx.Repaint()
	}
	if contentHeight > view.H {
		thumb := max(a.opts.Theme.Gap, view.H*view.H/contentHeight)
		y := view.Y + (view.H-thumb)*scroll/max(1, contentHeight-view.H)
		a.ctx.Canvas.FillRounded(ui.Rect{X: view.X + view.W - 3, Y: y, W: 3, H: thumb}, 1, a.opts.Theme.TextMuted)
	}
}
