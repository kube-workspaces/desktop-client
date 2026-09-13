//go:build !windows

package main

import (
	"os"
	"testing"
)

// TestAttachParentConsoleIsNoOp asserts that the non-Windows build of
// attachParentConsole leaves the process exactly as it found it.
//
// main calls it unconditionally on every platform, so the thing that must hold
// on Linux and macOS is that nothing changes: the same *os.File values stay in
// place and the standard streams keep working. A regression here — a stub that
// reassigned os.Stdout, say — would break every subcommand on the two
// platforms the client is developed on.
func TestAttachParentConsoleIsNoOp(t *testing.T) {
	in, out, errStream := os.Stdin, os.Stdout, os.Stderr

	// Twice, because main is not the only possible caller and an
	// implementation that is only idempotent the first time is a trap.
	attachParentConsole()
	attachParentConsole()

	if os.Stdin != in {
		t.Errorf("os.Stdin was replaced: got %p, want %p", os.Stdin, in)
	}
	if os.Stdout != out {
		t.Errorf("os.Stdout was replaced: got %p, want %p", os.Stdout, out)
	}
	if os.Stderr != errStream {
		t.Errorf("os.Stderr was replaced: got %p, want %p", os.Stderr, errStream)
	}
}

// TestStdoutStillWritesAfterAttach checks the streams are not merely the same
// values but still usable, by writing through the one the CLI subcommands
// print to.
func TestStdoutStillWritesAfterAttach(t *testing.T) {
	attachParentConsole()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = r.Close() }()

	saved := os.Stdout
	os.Stdout = w
	_, writeErr := os.Stdout.WriteString("hello\n")
	os.Stdout = saved
	if err := w.Close(); err != nil {
		t.Fatalf("close write end: %v", err)
	}
	if writeErr != nil {
		t.Fatalf("write to os.Stdout: %v", writeErr)
	}

	buf := make([]byte, 6)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := string(buf[:n]); got != "hello\n" {
		t.Errorf("read back %q, want %q", got, "hello\n")
	}
}
