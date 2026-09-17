// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package console reconnects the standard streams of a Windows GUI-subsystem
// binary to the console it was launched from. The desktop client ships as two
// binaries on Windows (the shell and the embedded-webview child), both linked
// as GUI-subsystem executables so that launching from Explorer opens no stray
// console window; [AttachParent] repairs the resulting loss of standard
// streams. Off Windows it is a no-op — every other platform hands a process
// working streams regardless of how it was launched.
package console

// invalidConsoleHandle is Windows' INVALID_HANDLE_VALUE, the (HANDLE)-1 that
// GetStdHandle returns when a standard stream has no handle behind it. It is
// declared here rather than in the Windows-only file so that the predicate
// below, and its test, build everywhere.
const invalidConsoleHandle = ^uintptr(0)

// HandleUsable reports whether a Windows standard handle actually refers to
// something the process can read from or write to.
//
// This is the predicate that keeps redirection working. A GUI subsystem binary
// started from Explorer has no standard handles at all, and GetStdHandle
// returns either 0 (the stream was never assigned) or INVALID_HANDLE_VALUE (the
// call failed) — those are the cases where [AttachParent] should point the
// stream at the parent's console. But `kube-workspaces.exe list > out.txt` or
// `... | Select-String foo` hands the process a perfectly good file or pipe
// handle even under the GUI subsystem, and redirecting that to CONOUT$ would
// throw the user's output away. So: a usable handle is left strictly alone.
//
// Only attach_windows.go calls it; it lives in a platform-neutral file because
// the rule is worth testing on the machines this code is written on.
func HandleUsable(h uintptr) bool {
	return h != 0 && h != invalidConsoleHandle
}
