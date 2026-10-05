// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package viewer

import "github.com/kube-workspaces/desktop-client/internal/tray"

// NewTray creates the platform tray with separate icon and menu actions.
func NewTray(handler tray.Handler) (tray.Backend, error) {
	return NewSDLTray(handler)
}
