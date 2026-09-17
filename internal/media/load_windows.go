// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package media

import "golang.org/x/sys/windows"

func openLibrary(name string) (uintptr, error) {
	// Exclude the working directory and PATH from DLL resolution. Codec DLLs
	// belong beside the application, including their transitive dependencies.
	h, err := windows.LoadLibraryEx(name, 0, windows.LOAD_LIBRARY_SEARCH_DEFAULT_DIRS)
	return uintptr(h), err
}

func closeLibrary(handle uintptr) { _ = windows.FreeLibrary(windows.Handle(handle)) }
func symbol(handle uintptr, name string) (uintptr, error) {
	return windows.GetProcAddress(windows.Handle(handle), name)
}
