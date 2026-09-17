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
	"errors"
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

	if err := webcmd.RunWeb(ctx, version, os.Args[1:]); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintf(os.Stderr, "kube-workspaces-web: %v\n", err)
		os.Exit(1)
	}
}
