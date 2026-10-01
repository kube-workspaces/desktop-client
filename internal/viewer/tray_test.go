// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/tray"
)

func TestTrayIconPreservesStraightAlpha(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	img.SetNRGBA(0, 0, color.NRGBA{})
	img.SetNRGBA(1, 0, color.NRGBA{R: 13, G: 148, B: 136, A: 128})
	img.SetNRGBA(2, 0, color.NRGBA{R: 13, G: 148, B: 136, A: 255})
	small := scaleTrayIcon(img, 3)
	for x := range 3 {
		if got, want := small.NRGBAAt(x, 0), img.NRGBAAt(x, 0); got != want {
			t.Errorf("pixel %d = %v, want %v", x, got, want)
		}
	}
}

func TestEmbeddedTrayIconHasTransparentBackground(t *testing.T) {
	img, err := png.Decode(bytes.NewReader(appIconPNG))
	if err != nil {
		t.Fatal(err)
	}
	small := scaleTrayIcon(img, trayIconSize)
	if small.NRGBAAt(0, 0).A != 0 {
		t.Fatal("tray background is not transparent")
	}
	visible := false
	for y := range trayIconSize {
		for x := range trayIconSize {
			visible = visible || small.NRGBAAt(x, y).A > 0
		}
	}
	if !visible {
		t.Fatal("tray glyph is invisible")
	}
}

// TestTrayCallbackShape pins the trampoline's signature: Windows
// (syscall.NewCallback, which purego delegates to) requires exactly one
// uintptr-sized result, and a void func panics the whole process at
// startup — seen live on a Windows build. The test runs everywhere so the
// shape cannot regress on a Linux-only pass.
func TestTrayCallbackShape(t *testing.T) {
	typ := reflect.TypeOf(trayCallbackFunc(nil))
	if typ.NumIn() != 2 || typ.In(0).Kind() != reflect.Uintptr || typ.In(1).Kind() != reflect.Uintptr {
		t.Fatalf("trayCallbackFunc = %v, want func(uintptr, uintptr) uintptr", typ)
	}
	if typ.NumOut() != 1 || typ.Out(0).Kind() != reflect.Uintptr {
		t.Fatalf("trayCallbackFunc = %v, want func(uintptr, uintptr) uintptr", typ)
	}
}

// TestNewSDLTrayNeedsHandler: a nil handler is an error, not a panic, and
// needs no display to prove.
func TestNewSDLTrayNeedsHandler(t *testing.T) {
	if _, err := NewSDLTray(nil); err == nil {
		t.Fatal("nil handler should fail")
	}
}

// TestNewSDLTrayNeverPanics: creation degrades to an error where the
// platform offers no tray (headless runs included) instead of taking the
// process down. The dummy driver has no notification area, so this
// expects the error path here.
func TestNewSDLTrayNeverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewSDLTray panicked: %v", r)
		}
	}()
	tr, err := NewSDLTray(discardHandler{})
	if err != nil {
		return
	}
	tr.Close()
}

type discardHandler struct{}

func (discardHandler) Handle(tray.Action) {}

// TestStaticTrayEntriesOrder pins the fixed menu: the main-window opener
// leads (it is the entry a minimized window is reopened from), then the
// Workspaces submenu's neighbours About and Quit.
func TestStaticTrayEntriesOrder(t *testing.T) {
	entries := staticTrayEntries()
	if len(entries) != 3 {
		t.Fatalf("static entries = %d, want 3", len(entries))
	}
	if entries[0].label != "Open Kube Workspaces" || entries[0].action.Kind != tray.ActionShow {
		t.Fatalf("first entry = %+v, want the main-window opener", entries[0])
	}
	if entries[1].action.Kind != tray.ActionAbout || entries[2].action.Kind != tray.ActionQuit {
		t.Fatalf("entries = %+v, want About then Quit", entries)
	}
}
