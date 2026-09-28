// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	errSwapFailed   = errors.New("update: install failed, previous version restored: rename: access denied")
	errCacheFailure = errors.New("update: earlier failure kept in the cache")
)

// isolateCache points [CacheDir] at a private directory so the update-error
// marker cannot leak between tests. The three names cover the three platform
// spellings of the user cache root.
func isolateCache(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, k := range []string{"XDG_CACHE_HOME", "LocalAppData", "HOME"} {
		t.Setenv(k, root)
	}
	cache, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

// fakeExe writes an executable stand-in for the running binary, so
// [LayoutForExe] resolves a directory the way it resolves a real install.
func fakeExe(t *testing.T, dir string) string {
	t.Helper()
	exe := filepath.Join(dir, ShellBinary+".exe")
	if err := os.WriteFile(exe, []byte("not really a shell"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestTakeErrorReportsAndConsumesTheMarker(t *testing.T) {
	isolateCache(t)
	dir := t.TempDir()
	exe := fakeExe(t, dir)

	recordUpdateError(dir, errSwapFailed)
	if got := TakeError(exe); got != errSwapFailed.Error() {
		t.Fatalf("TakeError = %q, want %q", got, errSwapFailed.Error())
	}
	// Once only: the notice is not replayed on every launch.
	if got := TakeError(exe); got != "" {
		t.Fatalf("TakeError replayed the marker: %q", got)
	}
}

// TestRecordUpdateErrorFallsBackToTheCache is the per-machine MSI case. The
// helper is an ordinary user process, so a Program Files install directory is
// not writable to it and the marker must not be lost with it: a failed update
// that cannot report itself on the next launch is indistinguishable from one
// that never ran.
func TestRecordUpdateErrorFallsBackToTheCache(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	cache := isolateCache(t)
	dir := filepath.Join(t.TempDir(), "Program Files", "Kube Workspaces")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	recordUpdateError(dir, errSwapFailed)

	if _, err := os.Stat(filepath.Join(dir, errorMarker)); err == nil {
		t.Fatal("the marker reached the unwritable install dir; the fallback was not exercised")
	}
	if b, err := os.ReadFile(filepath.Join(cache, errorMarker)); err != nil {
		t.Fatalf("no marker in the cache fallback: %v", err)
	} else if string(b) != errSwapFailed.Error() {
		t.Fatalf("cached marker = %q, want %q", b, errSwapFailed.Error())
	}
}

// TestTakeErrorPrefersTheInstallDir pins the order: a marker beside the
// binaries is the one that describes this install, and a stale cached failure
// must not shadow it.
func TestTakeErrorPrefersTheInstallDir(t *testing.T) {
	cache := isolateCache(t)
	dir := t.TempDir()
	exe := fakeExe(t, dir)

	// Seed the fallback first; the install dir must still win.
	if err := os.WriteFile(filepath.Join(cache, errorMarker), []byte(errCacheFailure.Error()), 0o600); err != nil {
		t.Fatal(err)
	}
	recordUpdateError(dir, errSwapFailed)

	if got := TakeError(exe); got != errSwapFailed.Error() {
		t.Fatalf("TakeError = %q, want the install-dir marker %q", got, errSwapFailed)
	}
	// With the install-dir marker consumed, the cached one is next.
	if got := TakeError(exe); got != errCacheFailure.Error() {
		t.Fatalf("TakeError = %q, want the cached marker %q", got, errCacheFailure)
	}
	if got := TakeError(exe); got != "" {
		t.Fatalf("TakeError = %q, want both markers consumed", got)
	}
}

func TestTakeErrorIgnoresANonExe(t *testing.T) {
	isolateCache(t)
	if got := TakeError(filepath.Join(t.TempDir(), "nowhere", ShellBinary+".exe")); got != "" {
		t.Fatalf("TakeError = %q for an unresolvable path", got)
	}
}

func TestRecordUpdateErrorIgnoresNil(t *testing.T) {
	cache := isolateCache(t)
	recordUpdateError(t.TempDir(), nil)
	if _, err := os.Stat(filepath.Join(cache, errorMarker)); !os.IsNotExist(err) {
		t.Fatalf("a nil error left a marker behind: %v", err)
	}
}

// TestErrorMarkerNamesTheFailure is a cheap guard on the message the user
// actually reads: it must carry the installer's own words, not a placeholder.
func TestErrorMarkerNamesTheFailure(t *testing.T) {
	isolateCache(t)
	dir := t.TempDir()
	recordUpdateError(dir, errSwapFailed)
	b, err := os.ReadFile(filepath.Join(dir, errorMarker))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "access denied") {
		t.Fatalf("marker %q does not carry the failure", b)
	}
}
