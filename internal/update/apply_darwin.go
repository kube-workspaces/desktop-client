// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
)

func osTranslocated(exePath string) error {
	if _, err := os.Stat(filepath.Join(filepath.Dir(exePath), "..", "_CodeSignature", "CodeResources")); err == nil {
		return fmt.Errorf("update: signed app bundles require a whole-bundle updater; install the new release manually")
	}
	if strings.Contains(exePath, "/AppTranslocation/") {
		return fmt.Errorf("%w: move Kube Workspaces.app to a writable location first", ErrTranslocated)
	}
	return nil
}

func preserveAttrs(newPath, oldPath string) error {
	n, err := unix.Listxattr(oldPath, nil)
	if err != nil || n == 0 {
		return err
	}
	names := make([]byte, n)
	n, err = unix.Listxattr(oldPath, names)
	if err != nil {
		return err
	}
	for _, name := range strings.Split(string(names[:n]), "\x00") {
		if name == "" {
			continue
		}
		size, err := unix.Getxattr(oldPath, name, nil)
		if err != nil {
			return err
		}
		value := make([]byte, size)
		size, err = unix.Getxattr(oldPath, name, value)
		if err != nil {
			return err
		}
		if err := unix.Setxattr(newPath, name, value[:size], 0); err != nil {
			return err
		}
	}
	return nil
}

func mapAccessError(dir string, err error) error {
	return fmt.Errorf("update: cannot write %s: %w", dir, err)
}
