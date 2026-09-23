// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"fmt"
	"os"
)

// osTranslocated reports whether exePath is running from a location an
// update cannot write back to. Windows has no translocation: an extracted
// zip runs where it sits.
//
// Renaming the running .exe aside works on Windows — the loader holds the
// image by handle, not by directory entry — which is what the shared Apply
// relies on. When something else holds the files (a locked web child, an AV
// scan), Apply rolls back. The GUI normally invokes Apply through the detached
// helper after the parent exits, avoiding the running-image lock entirely.
func osTranslocated(string) error { return nil }

// preserveAttrs carries platform file metadata from the previous binary to
// the new one. Authenticode signatures do not survive a byte swap by design,
// and there is nothing free to preserve on Windows until signing is funded.
func preserveAttrs(_, _ string) error { return nil }

// mapAccessError translates a filesystem error into the elevation story:
// Program-Files-style installs are not writable without admin rights, and
// the user must hear "re-run elevated or defer" rather than a raw
// ACCESS_DENIED.
func mapAccessError(dir string, err error) error {
	if err == nil {
		return nil
	}
	if os.IsPermission(err) {
		return fmt.Errorf("%w in %s: %w", ErrNeedsElevation, dir, err)
	}
	return fmt.Errorf("update: cannot write %s: %w", dir, err)
}
