// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"image"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/i18n"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/tray"
	"github.com/kube-workspaces/desktop-client/internal/ui"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// This file is the shell's popup windows: the tray's About panel and
// quick-pick chooser as windows of their own, so neither relies on the main
// window being open (or visible — a minimized shell stays minimized while a
// popup answers the click).
//
// A popup is deliberately not a session and rides none of its machinery: no
// transport, no parking, no switcher entry. It is one SDL window with its
// own backend and immediate-mode context, stepped by the same pump beside
// the shell and the sessions, and it closes itself the moment its job is
// done. At most one popup is open at a time — a second request closes the
// first — because tray clicks are sequential and a pile of choosers would
// be a bug report, not a feature.

type popupKind int

const (
	popupAbout popupKind = iota
	popupPick
)

// popupWindow is one About/pick window. Everything in it belongs to the
// loop's goroutine, like every other window the pump owns.
type popupWindow struct {
	kind popupKind
	// ws is the pick's workspace snapshot; it is meaningful for popupPick
	// only. Clicks re-resolve it against the current list at action time,
	// so a refresh between open and pick cannot open a dead row.
	ws kwclient.Workspace
	// err is a failed pick's error, kept on screen for a retry or another
	// tile — the pick analogue of Model.PickerErr.
	err string

	app    *App
	be     viewer.Backend
	ctx    *ui.Context
	img    *image.RGBA
	canvas *ui.Canvas
	// imgW/imgH are the canvas size; texW/texH the backend texture size.
	// They are tracked separately like [App]: uploading into a texture
	// that was never sized to the frame fails.
	imgW, imgH int
	texW, texH int
	in         ui.Input
	events     []viewer.Event
	closed     bool
	dirty      bool

	// lastTheme is the theme the window was sized for. A theme change
	// (the Interface Size row while a popup is open) re-fits the window
	// rather than clipping the content.
	lastTheme *ui.Theme
}

// popupWidth is the popup window width for the theme: the card width the
// panels were designed around, plus the inset they draw with.
func popupWidth(th *ui.Theme) int {
	return infoCardWidth + 2*th.Pad
}

// openAboutPopup opens the About panel in its own window.
func (a *App) openAboutPopup() {
	a.openPopup(popupAbout, kwclient.Workspace{})
}

// openPickerPopup opens the quick-pick chooser for ws in its own window.
func (a *App) openPickerPopup(ws kwclient.Workspace) {
	a.openPopup(popupPick, ws)
}

// openPopup opens (or replaces) the single popup window. A backend that
// will not open is reported on the main window instead: popups are
// best-effort chrome, never worth stranding a click in silence.
func (a *App) openPopup(kind popupKind, ws kwclient.Workspace) {
	a.closePopup()
	th := a.opts.Theme
	be := a.newPopupBackend()
	title := i18n.Get("about.title")
	h := aboutContentHeight(th)
	if kind == popupPick {
		title = ws.Key()
		h = pickContentHeight(th, false)
	}
	w := popupWidth(th)
	if maxH := popupMaxHeight(); maxH > 0 && h > maxH {
		h = maxH
	}
	if err := be.Open(viewer.WindowOptions{Title: title, Width: w, Height: h, VSync: true}); err != nil {
		a.logf("popup: open window: %v", err)
		a.ensureShellVisible()
		a.m.Err = ""
		a.m.Notice = i18n.Sprintf("pick.openFailed", err)
		return
	}
	ctx := ui.NewContext(th)
	ctx.Clipboard = be.Clipboard
	ctx.SetClipboard = be.SetClipboard
	p := &popupWindow{kind: kind, ws: ws, app: a, be: be, ctx: ctx, dirty: true, lastTheme: th}
	a.popup = p
	a.fitPopup()
	a.dirty = true
}

// popupMaxHeight caps a popup to the launch display when known, so a large
// interface scale on a short screen still fits. Zero means unknown: no cap.
func popupMaxHeight() int {
	bounds, ok := viewer.LaunchDisplayBounds()
	if !ok || bounds.H <= 0 {
		return 0
	}
	return bounds.H * 9 / 10
}

// newPopupBackend builds the popup's window behind the PopupNew seam.
// Nil means the real SDL window; tests inject a fake.
func (a *App) newPopupBackend() viewer.Backend {
	if a.opts.PopupNew != nil {
		return a.opts.PopupNew()
	}
	return viewer.NewSDLBackend()
}

// closePopup tears the popup window down, if any. It is idempotent: picks
// that open sessions and picks that fail both end here, as does any popup
// replacement and the session teardown on sign-out and exit.
func (a *App) closePopup() {
	if a.popup == nil {
		return
	}
	a.popup.be.Close()
	a.popup = nil
}

// fitPopup sizes the window to its content in drawable pixels, the way the
// shell sizes its surface from the backend rather than assuming. It runs at
// open and whenever the theme changes underneath an open popup.
func (a *App) fitPopup() {
	p := a.popup
	if p == nil {
		return
	}
	th := a.opts.Theme
	w := popupWidth(th)
	h := aboutContentHeight(th)
	if p.kind == popupPick {
		h = pickContentHeight(th, p.err != "")
	}
	if maxH := popupMaxHeight(); maxH > 0 && h > maxH {
		h = maxH
	}
	dw, dh := p.be.Size()
	if dw != w || dh != h {
		if err := p.be.SetSize(w, h); err != nil {
			a.logf("popup: fit window: %v", err)
		}
	}
	p.lastTheme = th
}

// stepPopups steps the popup window, if any, with its own routed events.
// A popup that closed itself (Close button, Esc, a completed pick) is torn
// down; a popup whose step fails is torn down too, with the failure logged
// rather than reported — a broken chooser must never take the shell with
// it, and the tray menu that opened it still works.
func (a *App) stepPopups(ctx context.Context, now time.Time) {
	p := a.popup
	if p == nil {
		return
	}
	evs := p.events
	p.events = p.events[:0]
	if err := p.step(ctx, now, evs); err != nil {
		a.logf("popup: step: %v", err)
		a.closePopup()
		return
	}
	if p.closed {
		a.closePopup()
	}
}

// step runs one popup iteration: fold input, draw the panel, present it,
// and apply the resulting intent. It runs on the loop's goroutine, so the
// intent applies inline like the shell's own draw-then-act.
func (p *popupWindow) step(ctx context.Context, now time.Time, events []viewer.Event) error {
	a := p.app
	p.in = p.in.Fold(now, events)
	if len(events) > 0 {
		p.dirty = true
	}
	if p.ctx.Theme != a.opts.Theme {
		p.ctx.Theme = a.opts.Theme
		p.dirty = true
		a.fitPopup()
	}
	if !p.dirty {
		return nil
	}
	p.dirty = false
	w, h := p.be.Size()
	if w <= 0 || h <= 0 {
		return nil
	}
	if p.img == nil || p.imgW != w || p.imgH != h {
		p.img = image.NewRGBA(image.Rect(0, 0, w, h))
		p.canvas = ui.NewCanvas(p.img)
		p.imgW, p.imgH = w, h
	}
	p.ctx.Begin(p.canvas, p.in)
	bounds := ui.Rect{W: w, H: h}
	var out intent
	switch p.kind {
	case popupAbout:
		out = a.drawAboutModal(p.ctx, bounds)
	case popupPick:
		out = a.drawQuickPickModal(p.ctx, bounds, p.ws, p.err)
	}
	p.ctx.End()
	if err := p.present(w, h); err != nil {
		return err
	}
	a.applyPopupIntent(ctx, p, out)
	return nil
}

// present uploads the popup canvas and shows it, mirroring [App.present].
func (p *popupWindow) present(w, h int) error {
	if p.texW != w || p.texH != h {
		if err := p.be.SetTextureSize(w, h); err != nil {
			return err
		}
		p.texW, p.texH = w, h
	}
	full := viewer.Rect{W: w, H: h}
	if err := p.be.Upload(full, p.img.Pix, p.img.Stride); err != nil {
		return err
	}
	return p.be.Present(full, viewer.Overlay{})
}

// IdleWait bounds how long the pump may block on a popup's account. Popup
// content is static between input, so this is only the missed-wake
// backstop — the same second the shell already budgets.
func (p *popupWindow) IdleWait(time.Time) time.Duration { return time.Second }

// applyPopupIntent performs one popup frame's outcome. About only ever
// closes; a pick either closes into its session or stays open with the
// failure inside for a retry or another tile.
func (a *App) applyPopupIntent(ctx context.Context, p *popupWindow, in intent) {
	switch in.kind {
	case intentNone:
		return
	case intentAboutClose:
		a.closePopup()
	case intentPickClose:
		a.closePopup()
	case intentPickMode:
		a.applyPopupPick(ctx, p, in.mode)
	}
}

// applyPopupPick opens the picked mode for the popup's workspace. The key
// is re-resolved against the current list: the menu and the window may
// predate a refresh that stopped or removed the workspace.
func (a *App) applyPopupPick(ctx context.Context, p *popupWindow, mode tray.Mode) {
	ws, ok := lookupWorkspace(a.m.Workspaces, p.ws.Key())
	if !ok || !ws.Running() {
		p.err = i18n.Get("pick.gone")
		p.dirty = true
		return
	}
	offered := false
	for _, m := range tray.ModesFor(trayKindOf(ws)) {
		if m == mode {
			offered = true
			break
		}
	}
	if !offered {
		p.err = i18n.Get("pick.gone")
		p.dirty = true
		return
	}
	a.openPickedMode(ctx, ws, mode, a.closePopup, func(msg string) {
		p.err = msg
		p.dirty = true
	})
}

// openPickedMode opens ws through one mode. close runs on the paths that
// leave the picker (a session screen, the SSH form, the browser flow);
// fail carries a synchronous failure back for a retry instead.
func (a *App) openPickedMode(ctx context.Context, ws kwclient.Workspace, mode tray.Mode, close func(), fail func(string)) {
	switch mode {
	case tray.ModeDisplay:
		close()
		a.activate(ctx, ws, false)
	case tray.ModeSerial:
		if e, ok := a.live[sessionKey(ws, "serial")]; ok {
			close()
			_ = e.window.Raise()
			a.m.Err = ""
			a.m.Notice = i18n.Sprintf("sessions.opened", sessionKey(ws, "serial"))
			return
		}
		close()
		a.m.OpenConsole(ws, "serial", "", nil)
	case tray.ModeSSH:
		if e, ok := a.live[sessionKey(ws, "ssh")]; ok {
			close()
			_ = e.window.Raise()
			a.m.Err = ""
			a.m.Notice = i18n.Sprintf("sessions.opened", sessionKey(ws, "ssh"))
			return
		}
		close()
		a.openSSH(ws)
	case tray.ModeWeb:
		a.openWeb(ctx, ws)
		if a.m.Err != "" {
			fail(a.m.Err)
			a.m.Err = ""
		} else {
			close()
		}
	case tray.ModeTerminal:
		close()
		a.activate(ctx, ws, false)
	case tray.ModeBrowser:
		close()
		a.openInBrowser(ctx, ws)
	}
}
