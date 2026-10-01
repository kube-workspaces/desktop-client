// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package instance

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func tryLock(f *os.File) (bool, error) {
	// Lock a byte beyond the metadata: Windows locks are mandatory and must
	// not prevent a second launch from reading the activation endpoint.
	o := windows.Overlapped{Offset: 0x7fffffff}
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &o)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

var allowSetForegroundWindow = windows.NewLazySystemDLL("user32.dll").NewProc("AllowSetForegroundWindow")

func allowForeground(pid int) {
	// The user-launched second process grants its foreground permission to
	// the owner before asking SDL to raise it. Failure is best-effort.
	_, _, _ = allowSetForegroundWindow.Call(uintptr(pid))
}
