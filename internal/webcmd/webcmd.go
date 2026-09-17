// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package webcmd implements the `web` command: opening a container/scratch
// workspace's web UI in the embedded webview (Track B of
// integrated-container-workspaces-plan.md). It is the body shared by the shell
// binary's `web` subcommand (cmd/kube-workspaces) and by the standalone web
// child binary (cmd/kube-workspaces-web).
//
// The webview drags in a cgo/windowing stack (webkitview_go links GTK/WebKit,
// Cocoa/WebKit or WebView2 depending on the platform), which is why it has its
// own binary: releases ship the child built with CGO_ENABLED=1 next to a shell
// built without it, so the browser engine can never touch the SDL shell's
// main OS thread.
package webcmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kube-workspaces/desktop-client/internal/cmdutil"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/web"
)

// RunWeb opens a non-VM workspace's web UI in the embedded webview and blocks
// until the window is closed. uaVersion is the binary's version, stamped into
// the HTTP user agent; args are the command-line arguments after the
// subcommand name.
func RunWeb(ctx context.Context, uaVersion string, args []string) error {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	profileName := fs.String("profile", "", "profile to use")
	namespace := fs.String("namespace", "", "workspace namespace")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: kube-workspaces-web <workspace> [<namespace>/{name}] [flags]\n\n")
		fs.PrintDefaults()
	}
	if err := cmdutil.ParseFlags(fs, args); err != nil {
		return err
	}
	name := fs.Arg(0)
	if name == "" {
		fs.Usage()
		return fmt.Errorf("a workspace name is required")
	}

	client, profile, err := cmdutil.For(uaVersion, *profileName)
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
		ns, err = cmdutil.ResolveNamespace(ctx, client, profile, "", name)
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
