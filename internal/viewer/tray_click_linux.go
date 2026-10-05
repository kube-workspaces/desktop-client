// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"unsafe"

	"github.com/kube-workspaces/desktop-client/internal/tray"
)

// NewTray uses StatusNotifierItem directly: SDL's AppIndicator backend
// exposes a menu but no primary-click callback.
func NewTray(handler tray.Handler) (tray.Backend, error) {
	img, err := trayIconImage()
	if err != nil {
		return nil, err
	}
	return tray.NewStatusNotifier(handler, img)
}

// Legacy SDL tray creation is kept for its headless/error-path tests.
func installTrayClicks(_ unsafe.Pointer, _ tray.Handler) (func(), error) {
	return nil, nil
}
