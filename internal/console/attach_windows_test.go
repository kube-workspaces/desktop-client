//go:build windows

// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package console

import (
	"os"
	"syscall"
	"testing"
)

// The behaviour this file's subject exists for — a GUI subsystem binary
// adopting the console of the shell that launched it — cannot be exercised by
// `go test`. Test binaries are linked as console subsystem executables, so the
// process already owns a console, AttachConsole returns ERROR_ACCESS_DENIED and
// AttachParent takes its early return. Verifying the real path needs a
// binary built with `-H=windowsgui` and run by hand from cmd.exe, PowerShell
// and Explorer.
//
// What is worth asserting here, and does run on the windows-latest CI runner,
// is that the early return is genuinely inert: a process that already has
// working standard streams must come out of AttachParent with exactly those
// streams.

// TestAttachParentWithExistingConsoleIsInert checks that calling AttachParent
// from a process that already has a console leaves the standard streams
// untouched.
func TestAttachParentWithExistingConsoleIsInert(t *testing.T) {
	in, out, errStream := os.Stdin, os.Stdout, os.Stderr
	// Restore regardless, so that a surprise on some future Windows build
	// cannot take the rest of the test binary's output down with it.
	defer func() { os.Stdin, os.Stdout, os.Stderr = in, out, errStream }()

	AttachParent()

	if os.Stdout != out {
		t.Errorf("os.Stdout was replaced: got %p, want %p", os.Stdout, out)
	}
	if os.Stderr != errStream {
		t.Errorf("os.Stderr was replaced: got %p, want %p", os.Stderr, errStream)
	}
	if os.Stdin != in {
		t.Errorf("os.Stdin was replaced: got %p, want %p", os.Stdin, in)
	}
}

// TestStdHandleMatchesSyscallPackage checks the helper that reads the process's
// standard handles against the values the syscall package captured at startup.
// A test binary always has all three, so all three must come back usable.
func TestStdHandleMatchesSyscallPackage(t *testing.T) {
	tests := []struct {
		name string
		id   int
		want syscall.Handle
	}{
		{"stdin", syscall.STD_INPUT_HANDLE, syscall.Stdin},
		{"stdout", syscall.STD_OUTPUT_HANDLE, syscall.Stdout},
		{"stderr", syscall.STD_ERROR_HANDLE, syscall.Stderr},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stdHandle(tt.id)
			if got != uintptr(tt.want) {
				t.Errorf("stdHandle(%d) = %#x, want %#x", tt.id, got, uintptr(tt.want))
			}
			if !HandleUsable(got) {
				t.Errorf("stdHandle(%d) = %#x, which HandleUsable rejects; a test "+
					"binary has all three standard streams", tt.id, got)
			}
		})
	}
}

// TestKernel32ProcsResolve checks that the two kernel32 entry points are
// actually findable, which a typo in the name would otherwise only reveal on a
// user's machine — Call on an unresolvable LazyProc panics.
func TestKernel32ProcsResolve(t *testing.T) {
	for _, p := range []*syscall.LazyProc{procAttachConsole, procSetStdHandle} {
		if err := p.Find(); err != nil {
			t.Errorf("%s: %v", p.Name, err)
		}
	}
}
