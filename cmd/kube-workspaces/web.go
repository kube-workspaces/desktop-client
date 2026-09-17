package main

import (
	"context"

	"github.com/kube-workspaces/desktop-client/internal/webcmd"
)

// webCommand opens a non-VM workspace's web UI in the embedded webview
// (Track B of integrated-container-workspaces-plan.md). It is deliberately a
// separate child process (spawned by the shell's Options.OpenWeb seam or run
// directly), so the browser engine's cgo/windowing stack never meets the
// SDL shell's main-thread ownership.
//
// The implementation lives in internal/webcmd so that the standalone web
// child binary (cmd/kube-workspaces-web) runs the exact same code. This
// subcommand is the fallback for developer copies that have no such sibling.
func webCommand() command {
	return command{
		name:    "web",
		summary: "Open a container/scratch workspace in the embedded webview",
		run: func(ctx context.Context, args []string) error {
			return webcmd.RunWeb(ctx, version, args)
		},
	}
}
