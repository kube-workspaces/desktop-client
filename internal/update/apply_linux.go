// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import "fmt"

// osTranslocated reports whether exePath is running from a location an
// update cannot write back to. Linux archive installs run from wherever the
// user extracted them, which is always writable-or-clearly-not, so there is
// nothing translocation-like to detect.
func osTranslocated(string) error { return nil }

// preserveAttrs carries platform file metadata from the previous binary to
// the new one. Linux archives carry no xattrs worth preserving.
func preserveAttrs(_, _ string) error { return nil }

// mapAccessError adds the install directory to a filesystem error. A plain
// Linux install fails writable-or-loudly; there is no elevation story to
// detect here.
func mapAccessError(dir string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("update: cannot write %s: %w", dir, err)
}
