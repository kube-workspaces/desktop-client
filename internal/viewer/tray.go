// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"unsafe"

	"github.com/Zyko0/go-sdl3/sdl"
	"github.com/ebitengine/purego"

	"github.com/kube-workspaces/desktop-client/internal/tray"
)

// This file and sdl.go are the only files that may import an SDL binding.
// See the package comment in backend.go: the binding is young and
// single-maintainer, and replacing it must stay a days-not-months job, so
// every SDL type stops at this file's boundary. The shell talks to the tray
// through internal/tray's Backend/Handler seams and never sees an SDL type.

// Tray entry flags from SDL_tray.h. The binding exposes the TrayEntryFlags
// type but not these C macros (its generator only emits enums), so they are
// declared here with SDL's documented values.
const (
	trayEntryButton   = sdl.TrayEntryFlags(0x00000001)
	trayEntrySubmenu  = sdl.TrayEntryFlags(0x00000004)
	trayEntryDisabled = sdl.TrayEntryFlags(0x80000000)
)

// trayCallbackFunc is the menu-click trampoline shape. Windows
// (syscall.NewCallback, which purego delegates to) requires exactly one
// uintptr-sized result — a void func panics the process at startup — so
// the result exists even though SDL ignores it. See TestTrayCallbackShape.
type trayCallbackFunc func(userdata, entry uintptr) uintptr

// staticTrayEntries is the fixed menu around the Workspaces submenu, in
// order: the main-window opener first (it is the reason the tray exists
// once the window can hide into it), then About and Quit. Pure data, so
// the order is pinned by test without a display.
func staticTrayEntries() []struct {
	label  string
	action tray.Action
} {
	return []struct {
		label  string
		action tray.Action
	}{
		{"Open Kube Workspaces", tray.Action{Kind: tray.ActionShow}},
		{"About", tray.Action{Kind: tray.ActionAbout}},
		{"Quit", tray.Action{Kind: tray.ActionQuit}},
	}
}

// trayIconSize is the tray icon's pixel size. Notification areas render
// small; handing SDL the 256 px window icon and hoping the platform
// downscales it well is how tray icons end up a blurry cube.
const trayIconSize = 32

// SDLTray is the native system-tray menu behind [tray.Backend].
//
// The tray is best-effort by design: NewSDLTray returns an error (and no
// tray) wherever the platform offers none — no indicator host on Linux,
// the dummy video driver in headless runs — and the shell carries on
// exactly as without it. Every method runs on the loop's goroutine, which
// owns every window and the tray with them; menu clicks arrive on the
// tray's thread and are marshalled to the shell through [tray.Handler].
type SDLTray struct {
	handler tray.Handler
	tray    *sdl.Tray
	icon    *sdl.Surface
	wsMenu  *sdl.TrayMenu
	entries []*sdl.TrayEntry
	byEntry map[uintptr]tray.Action
	// cb and cbFunc are the menu-click trampoline. The uintptr handed to
	// SDL points at a purego closure that must stay reachable: if it is
	// garbage-collected the next click jumps nowhere.
	cb     sdl.TrayCallback
	cbFunc trayCallbackFunc
}

var _ tray.Backend = (*SDLTray)(nil)

// NewSDLTray creates the tray icon with its static menu (Workspaces
// submenu, About, Quit) and an empty workspace list. A nil icon surface —
// the PNG failed to decode — still creates the tray: an iconless tray
// entry beats no tray at all.
//
// A creation failure is an error, never a panic: the binding panics
// rather than returning errors (missing symbols on an older SDL, platform
// callback limits), and the tray is best-effort — a tray the platform
// cannot support must degrade to today's shell, not take the application
// down. Seen live: Windows rejects a callback without a uintptr-sized
// result, so the trampoline returns one.
func NewSDLTray(handler tray.Handler) (t *SDLTray, err error) {
	if handler == nil {
		return nil, errors.New("viewer: tray needs a handler")
	}
	defer func() {
		if r := recover(); r != nil {
			t = nil
			err = fmt.Errorf("viewer: tray unavailable: %v", r)
		}
	}()
	t = &SDLTray{handler: handler, byEntry: make(map[uintptr]tray.Action)}
	icon, err := trayIconSurface()
	if err != nil {
		icon = nil
	}
	t.icon = icon
	tr := sdl.CreateTray(icon, "Kube Workspaces")
	if tr == nil {
		if icon != nil {
			icon.Destroy()
		}
		return nil, errors.New("viewer: platform offers no system tray")
	}
	t.tray = tr
	menu := tr.CreateMenu()
	if menu == nil {
		tr.Destroy()
		if icon != nil {
			icon.Destroy()
		}
		return nil, errors.New("viewer: tray menu creation failed")
	}
	t.cbFunc = func(_, entry uintptr) uintptr { t.dispatch(entry); return 0 }
	t.cb = sdl.TrayCallback(purego.NewCallback(t.cbFunc))
	// The main-window opener leads the menu, before the Workspaces
	// submenu: it is the entry the user reaches for when the window is
	// hidden in the tray.
	for _, s := range staticTrayEntries() {
		if s.action.Kind == tray.ActionShow {
			if !t.insertStatic(menu, s.label, s.action) {
				t.Close()
				return nil, errors.New("viewer: tray entry creation failed")
			}
		}
	}
	wsHolder := menu.InsertEntryAt(-1, "Workspaces", trayEntrySubmenu)
	if wsHolder == nil {
		t.Close()
		return nil, errors.New("viewer: tray submenu entry creation failed")
	}
	t.wsMenu = sdl.CreateTraySubmenu(wsHolder)
	if t.wsMenu == nil {
		t.Close()
		return nil, errors.New("viewer: tray submenu creation failed")
	}
	for _, s := range staticTrayEntries() {
		if s.action.Kind != tray.ActionShow {
			if !t.insertStatic(menu, s.label, s.action) {
				t.Close()
				return nil, errors.New("viewer: tray entry creation failed")
			}
		}
	}
	return t, nil
}

// insertStatic appends one fixed menu entry and wires its click action,
// reporting whether the platform accepted it.
func (t *SDLTray) insertStatic(menu *sdl.TrayMenu, label string, action tray.Action) bool {
	e := menu.InsertEntryAt(-1, label, trayEntryButton)
	if e == nil {
		return false
	}
	e.SetCallback(t.cb)
	t.byEntry[entryID(e)] = action
	return true
}

// Update implements [tray.Backend]: it rebuilds the Workspaces submenu from
// the current running set. Entries are removed and re-inserted wholesale —
// lists are small and this avoids index bookkeeping — and every workspace
// entry carries its click action by entry identity.
func (t *SDLTray) Update(targets []tray.Target) {
	if t == nil || t.wsMenu == nil {
		return
	}
	for _, e := range t.entries {
		sdl.RemoveTrayEntry(e)
	}
	t.entries = t.entries[:0]
	for key := range t.byEntry {
		if t.byEntry[key].Kind == tray.ActionOpen {
			delete(t.byEntry, key)
		}
	}
	for _, item := range tray.MenuItems(targets) {
		flags := trayEntryButton
		if item.Disabled {
			flags |= trayEntryDisabled
		}
		e := t.wsMenu.InsertEntryAt(-1, item.Label, flags)
		if e == nil {
			continue
		}
		t.entries = append(t.entries, e)
		if !item.Disabled {
			e.SetCallback(t.cb)
			t.byEntry[entryID(e)] = tray.Action{Kind: tray.ActionOpen, Key: item.Key}
		}
	}
}

// Pump implements [tray.Backend]: it runs the tray housekeeping that needs
// the loop's thread. The Linux AppIndicator driver dispatches menu clicks
// through it, so a pump that never runs is a menu that never answers there.
func (t *SDLTray) Pump() {
	if t == nil || t.tray == nil {
		return
	}
	sdl.UpdateTrays()
}

// Close implements [tray.Backend]: it destroys the tray — the menu and its
// entries go with it — and the icon surface. It is safe to call on a tray
// that never came up, and safe to call twice.
func (t *SDLTray) Close() {
	if t == nil {
		return
	}
	if t.tray != nil {
		t.tray.Destroy()
		t.tray = nil
	}
	if t.icon != nil {
		t.icon.Destroy()
		t.icon = nil
	}
	t.wsMenu = nil
	t.entries = nil
	t.byEntry = make(map[uintptr]tray.Action)
}

// dispatch maps a clicked entry back to its action and hands it to the
// shell. It runs on the tray's thread: the handler queues and wakes, and
// must never be called with anything the loop owns.
func (t *SDLTray) dispatch(entry uintptr) {
	if t == nil {
		return
	}
	if action, ok := t.byEntry[entry]; ok {
		t.handler.Handle(action)
	}
}

// entryID identifies a tray entry by its object address, which is what the
// click callback hands back. Entries live as long as the tray, so the
// address is stable between Update and click.
func entryID(e *sdl.TrayEntry) uintptr {
	return uintptr(unsafe.Pointer(e))
}

// trayIconSurface decodes the embedded app icon down to tray size.
//
// The pixels are copied straight into an RGBA32 surface: that format is
// byte-order R,G,B,A on every architecture this repo builds (see the
// texture allocation in sdl.go), which is exactly Go's image.RGBA layout,
// so no byte swap is needed. The caller owns the surface.
func trayIconSurface() (*sdl.Surface, error) {
	if len(appIconPNG) == 0 {
		return nil, errors.New("viewer: no embedded icon")
	}
	img, err := png.Decode(bytes.NewReader(appIconPNG))
	if err != nil {
		return nil, err
	}
	small := scaleRGBA(img, trayIconSize)
	surface, err := sdl.CreateSurface(small.Bounds().Dx(), small.Bounds().Dy(), sdl.PIXELFORMAT_RGBA32)
	if err != nil {
		return nil, err
	}
	pix := surface.Pixels()
	if len(pix) < len(small.Pix) {
		surface.Destroy()
		return nil, errors.New("viewer: tray surface too small")
	}
	copy(pix, small.Pix)
	return surface, nil
}

// scaleRGBA renders img into a size×size RGBA with nearest-neighbour
// sampling. Nearest is honest at these sizes: the source is a flat vector
// cube, and a 32 px tray glyph wants crisp edges, not a smooth blur.
func scaleRGBA(img image.Image, size int) *image.RGBA {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	if sw <= 0 || sh <= 0 {
		return out
	}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// Alpha is forced opaque: the app icon is fully opaque, and
			// a half-transparent cube would ghost over dark
			// notification areas.
			r, g, b, _ := img.At(b.Min.X+x*sw/size, b.Min.Y+y*sh/size).RGBA()
			out.SetRGBA(x, y, color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 0xff})
		}
	}
	return out
}
