//go:build cgo

// Package web hosts the embedded webview used by the `web` subcommand for
// non-VM (container/scratch) workspaces. It is always built and run as a
// separate child process, so the SDL/cgo-free shell and viewer never link the
// browser engine's second windowing stack onto the main OS thread.
//
// macOS needs WebKit/Cocoa, Windows WebView2, Linux GTK/WebKitGTK; only this
// file forms the cgo link closure (`webview_go` + its C webview). The package
// is excluded from `CGO_ENABLED=0` builds on every platform.
package web

import (
	"runtime"

	webview "github.com/webview/webview_go"
)

// Run opens a native window navigating url and blocks until the user closes
// it. webview.Run must stay on the main OS thread (matches SDL's ownership
// when it happened to be a viewer — here the whole process exists for this).
func Run(title, url string) error {
	runtime.LockOSThread()
	w := webview.New(true)
	defer w.Destroy()
	w.SetTitle(title)
	w.SetSize(1280, 800, webview.HintNone)
	w.Navigate(url)
	w.Run()
	return nil
}
