// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package media

import "github.com/ebitengine/purego"

func openLibrary(name string) (uintptr, error) {
	return purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_LOCAL)
}

func closeLibrary(handle uintptr)                         { _ = purego.Dlclose(handle) }
func symbol(handle uintptr, name string) (uintptr, error) { return purego.Dlsym(handle, name) }
