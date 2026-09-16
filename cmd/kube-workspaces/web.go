package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/web"
)

// webCommand opens a non-VM workspace's web UI in the embedded webview
// (Track B of integrated-container-workspaces-plan.md). It is deliberately a
// separate child process (spawned by the shell's Options.OpenWeb seam or run
// directly), so the browser engine's cgo/windowing stack never meets the
// SDL shell's main-thread ownership.
func webCommand() command {
	return command{
		name:    "web",
		summary: "Open a container/scratch workspace in the embedded webview",
		run:     runWeb,
	}
}

func runWeb(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	profileName := fs.String("profile", "", "profile to use")
	namespace := fs.String("namespace", "", "workspace namespace")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: kube-workspaces web <workspace> [<namespace>/{name}] [flags]\n\n")
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	name := fs.Arg(0)
	if name == "" {
		fs.Usage()
		return fmt.Errorf("a workspace name is required")
	}

	client, profile, err := clientFor(*profileName)
	if err != nil {
		return err
	}

	// The positional argument may carry namespace/name; a namespace flag wins.
	ns := *namespace
	if strings.Contains(name, "/") {
		parts := strings.SplitN(name, "/", 2)
		if ns == "" {
			ns = parts[0]
		}
		name = parts[1]
	}
	if name == "" {
		return fmt.Errorf("bad workspace argument %q", fs.Arg(0))
	}
	if ns == "" {
		ns, err = resolveNamespace(ctx, client, profile, "", name)
		if err != nil {
			return err
		}
	}

	ws := kwclient.Workspace{Namespace: ns, Name: name}
	redirect := client.WorkspacePath(ws, nil)
	grant, err := client.GrantBrowserSession(ctx, redirect)
	if err != nil {
		return fmt.Errorf("grant browser session: %w", err)
	}

	return web.Run(ns+"/"+name, grant.URL)
}
