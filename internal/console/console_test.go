// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package console

import "testing"

// TestHandleUsable covers the predicate that decides whether AttachParent may
// replace a standard stream with the parent console. Getting it wrong in the
// permissive direction breaks shell redirection, which is the failure mode
// worth a test: `kube-workspaces.exe list > out.txt` would silently write to
// the console and leave out.txt empty.
func TestHandleUsable(t *testing.T) {
	tests := []struct {
		name   string
		handle uintptr
		want   bool
	}{
		{
			name:   "zero means the stream was never assigned a handle",
			handle: 0,
		},
		{
			name:   "INVALID_HANDLE_VALUE means GetStdHandle failed",
			handle: invalidConsoleHandle,
		},
		{
			// What a GUI subsystem process is handed by `> out.txt` or by a
			// pipe. Real handle values are small multiples of four, but any
			// non-sentinel value must be respected.
			name:   "a redirected file or pipe handle must be left alone",
			handle: 0x1c,
			want:   true,
		},
		{
			name:   "the lowest possible real handle",
			handle: 1,
			want:   true,
		},
		{
			// One below the sentinel: adjacent to INVALID_HANDLE_VALUE but not
			// it, so it must not be rejected by a sloppy comparison.
			name:   "just below INVALID_HANDLE_VALUE",
			handle: invalidConsoleHandle - 1,
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HandleUsable(tt.handle); got != tt.want {
				t.Errorf("HandleUsable(%#x) = %v, want %v", tt.handle, got, tt.want)
			}
		})
	}
}

// TestInvalidConsoleHandleIsAllOnes pins the sentinel to Windows'
// INVALID_HANDLE_VALUE, (HANDLE)-1, which is all bits set at the platform's
// pointer width.
func TestInvalidConsoleHandleIsAllOnes(t *testing.T) {
	// Through a variable: as a constant expression the +1 would overflow
	// uintptr and fail to compile rather than wrap.
	allOnes := invalidConsoleHandle
	if allOnes+1 != 0 {
		t.Errorf("invalidConsoleHandle = %#x, want the all-ones value", invalidConsoleHandle)
	}
}
