// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build cgo

package web

/*
#include <stdint.h>
*/
import "C"

import (
	"fmt"
	"net/url"
	"runtime/cgo"
	"strings"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/i18n"
	webview "github.com/webview/webview_go"
)

//export goWebToolbarAction
func goWebToolbarAction(handle C.uintptr_t, action C.int) {
	cgo.Handle(handle).Value().(func(int))(int(action))
}

func installToolbar(w webview.WebView, title, rawURL string, geometry *geometryRecorder) (func(), error) {
	// Retain no browser-session grant query in native chrome or diagnostics.
	origin := ""
	if u, err := url.Parse(rawURL); err == nil {
		origin = u.Scheme + "://" + u.Host
	}
	// The line-separated contract with the native side, by position: the
	// identity, then one line per button action, then the strings the chrome
	// needs later and cannot derive from a button title it already replaced —
	// the reveal handle, the unpin label and the windowed label. The buttons
	// relabel themselves in place, so passing both halves is what keeps the
	// native toolbars translatable like the shell's.
	// Slots 2 and 3 remain empty for the removed Sessions/Workspace list
	// actions, preserving the native action IDs and alternate-label positions.
	labels := []string{title + " · " + i18n.Get("toolbar.surface.web"), i18n.Get("toolbar.fullscreen"),
		"", "", i18n.Get("toolbar.connection"), i18n.Get("toolbar.pin"), i18n.Get("toolbar.disconnect"),
		i18n.Get("toolbar.tools"), i18n.Get("toolbar.unpin"), i18n.Get("toolbar.windowed")}
	details := fmt.Sprintf("%s\n%s: Webview\n%s\n%s", title, i18n.Get("toolbar.transport"), origin, i18n.Get("toolbar.disconnectHint"))
	handle := cgo.NewHandle(func(action int) {
		switch action {
		case 6:
			if r, ok := nativeGeometry(w.Window()); ok {
				geometry.observe(r, time.Now())
			}
			w.Terminate()
		case 8:
			// Native host sampling only; never exposed as a page binding.
			if r, ok := nativeGeometry(w.Window()); ok {
				geometry.observe(r, time.Now())
			}
		}
	})
	cleanup, err := attachNativeToolbar(w.Window(), uintptr(handle), strings.Join(labels, "\n"), details)
	if err != nil {
		handle.Delete()
		return nil, err
	}
	return func() { cleanup(); handle.Delete() }, nil
}
