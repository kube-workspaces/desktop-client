// Command kube-workspaces-web is the embedded-webview child of the desktop
// client: it opens a container/scratch workspace's web UI in a native webview
// window and blocks until it is closed.
//
// It exists so that the browser engine's cgo/windowing stack can ship in its
// own per-OS binary, built with CGO_ENABLED=1, while the shell binary
// (cmd/kube-workspaces) stays cgo-free. The shell spawns it as
// `kube-workspaces-web [-profile NAME] <namespace>/<name>` whenever it ships
// beside the shell; it can equally be run by hand.
//
// The Windows build is linked as a GUI-subsystem binary (the Makefile's
// WINDOWS_LDFLAGS apply), so before anything prints it reattaches the parent
// console, exactly like the parent binary does; that call is a no-op on every
// other platform.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kube-workspaces/desktop-client/internal/console"
	"github.com/kube-workspaces/desktop-client/internal/webcmd"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	console.AttachParent()

	// Ctrl-C should tear down the webview cleanly rather than kill the child
	// mid-handshake.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := webcmd.RunWeb(ctx, version, os.Args[1:])

	// A canceled context means a signal interrupted the webview, not a failure
	// worth printing, so on cancel the exit code just stays quiet. The error is
	// compared through exitErr because the cgo-less variant of this child never
	// returns nil and would look to staticcheck like a constant comparison.
	exitErr := err
	if ctx.Err() != nil {
		exitErr = nil
	}
	if exitErr != nil {
		fmt.Fprintf(os.Stderr, "kube-workspaces-web: %v\n", err)
		os.Exit(1)
	}
}
