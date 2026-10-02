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
	"os"
	"runtime/cgo"
	"strings"

	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/i18n"
	webview "github.com/webview/webview_go"
)

//export goWebToolbarAction
func goWebToolbarAction(handle C.uintptr_t, action C.int) {
	cgo.Handle(handle).Value().(func(int))(int(action))
}

func installToolbar(w webview.WebView, title, rawURL string) (func(), error) {
	endpoint, token := os.Getenv(connection.NavigationURLEnv), os.Getenv(connection.NavigationTokenEnv)
	// Retain no browser-session grant query in native chrome or diagnostics.
	origin := ""
	if u, err := url.Parse(rawURL); err == nil {
		origin = u.Scheme + "://" + u.Host
	}
	labels := []string{title + " · " + i18n.Get("toolbar.surface.web"), i18n.Get("toolbar.fullscreen"),
		i18n.Get("toolbar.sessions"), i18n.Get("toolbar.workspace-list"), i18n.Get("toolbar.connection"), i18n.Get("toolbar.pin"), i18n.Get("toolbar.disconnect")}
	if endpoint == "" {
		labels[2], labels[3] = "", ""
	}
	details := fmt.Sprintf("%s\n%s: Webview\n%s\n%s", title, i18n.Get("toolbar.transport"), origin, i18n.Get("toolbar.disconnectHint"))
	handle := cgo.NewHandle(func(action int) {
		switch action {
		case 2, 3:
			a := connection.Sessions
			if action == 3 {
				a = connection.WorkspaceList
			}
			// Never block a native UI callback on HTTP. This capability lives
			// outside page JavaScript and accepts navigation only.
			go func() {
				if err := connection.SendNavigation(endpoint, token, a); err != nil {
					fmt.Fprintln(os.Stderr, "connection navigation:", err)
				}
			}()
		case 6:
			w.Terminate()
		}
	})
	cleanup, err := attachNativeToolbar(w.Window(), uintptr(handle), strings.Join(labels, "\n"), details)
	if err != nil {
		handle.Delete()
		return nil, err
	}
	return func() { cleanup(); handle.Delete() }, nil
}
