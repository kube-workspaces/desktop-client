// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package cmdutil holds the command-line plumbing shared by the desktop
// client's two binaries: cmd/kube-workspaces (the shell plus its CLI
// subcommands) and cmd/kube-workspaces-web (the embedded-webview child).
// Keeping it in one package means the child is not a fork of the parent's
// argument parsing or its profile/session handling.
package cmdutil

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

// ParseFlags parses args allowing flags and positional arguments to appear in
// any order.
//
// The standard flag package stops parsing at the first non-flag argument, so
// `screenshot my-vm -o out.png` would silently ignore -o. Users reasonably
// expect the subject of a command to come first, so permute the arguments and
// hand flag a list it can handle.
func ParseFlags(fs *flag.FlagSet, args []string) error {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		// "-flag=value" carries its own value; "-flag value" consumes the next
		// argument, but only for flags that actually take one.
		if strings.Contains(arg, "=") {
			continue
		}
		name := strings.TrimLeft(arg, "-")
		if f := fs.Lookup(name); f != nil && !isBoolFlag(f) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return fs.Parse(append(flags, positional...))
}

// isBoolFlag reports whether a flag is a boolean, which the flag package
// signals through an optional method on the value.
func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}

// For builds an API client for the named profile, or the active one when name
// is empty. The stored session token is attached if present. uaVersion is the
// binary's version and becomes the client-version part of the HTTP user agent.
func For(uaVersion, profileName string) (*kwclient.Client, *config.Profile, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	var profile *config.Profile
	if profileName != "" {
		profile = cfg.Get(profileName)
		if profile == nil {
			return nil, nil, fmt.Errorf("no such profile %q", profileName)
		}
	} else {
		profile, err = cfg.Active()
		if err != nil {
			return nil, nil, err
		}
	}

	opts := []kwclient.Option{kwclient.WithUserAgent("kube-workspaces-desktop/" + uaVersion)}
	if profile.InsecureSkipVerify {
		opts = append(opts, kwclient.WithInsecureSkipVerify(true))
	}
	token, err := config.LoadToken(profile.Name)
	switch {
	case err == nil:
		opts = append(opts, kwclient.WithToken(token))
	case errors.Is(err, config.ErrNoToken):
		// Leave the client unauthenticated; the caller reports the failure in
		// context, which is friendlier than erroring here.
	default:
		return nil, nil, err
	}

	client, err := kwclient.New(profile.Server, opts...)
	if err != nil {
		return nil, nil, err
	}
	return client, profile, nil
}

// ResolveNamespace determines which namespace a workspace lives in, looking it
// up when the user did not say. Workspaces live in per-user namespaces, so
// requiring the flag every time would be tedious.
func ResolveNamespace(ctx context.Context, client *kwclient.Client, profile *config.Profile, explicit, name string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if profile.Namespace != "" {
		return profile.Namespace, nil
	}
	workspaces, err := client.ListWorkspaces(ctx, kwclient.AllNamespaces)
	if err != nil {
		return "", fmt.Errorf("look up workspace namespace: %w", err)
	}
	var matches []kwclient.Workspace
	for _, ws := range workspaces {
		if ws.Name == name {
			matches = append(matches, ws)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no workspace named %q found", name)
	case 1:
		return matches[0].Namespace, nil
	default:
		var ns []string
		for _, m := range matches {
			ns = append(ns, m.Namespace)
		}
		return "", fmt.Errorf("workspace %q exists in several namespaces (%s); pass --namespace",
			name, strings.Join(ns, ", "))
	}
}
