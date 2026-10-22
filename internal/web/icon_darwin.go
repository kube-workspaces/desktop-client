// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin && cgo

package web

import "unsafe"

// Cocoa uses the .app bundle's Dock icon; document proxy title-bar icons are
// reserved for represented files, which a workspace browser window is not.
func setNativeWindowIcon(_ unsafe.Pointer) {}
