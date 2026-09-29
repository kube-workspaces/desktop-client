// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"reflect"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/tray"
)

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
