// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package media

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Bundled libraries beside the executable must win over distro ones, and the
// bare system names must always remain as the final fallback (a .deb
// dependency install keeps working with no bundle present).
func TestBundleCandidatesPrecedeSystemNames(t *testing.T) {
	names := withBundle("libavcodec.so.59")
	if len(names) == 0 {
		t.Fatal("withBundle returned no candidates")
	}
	if got := names[len(names)-1]; got != "libavcodec.so.59" {
		t.Fatalf("last candidate = %q, want the bare system name", got)
	}
	if runtime.GOOS == "windows" {
		if len(names) != 1 {
			t.Fatalf("windows candidates = %v, want passthrough", names)
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	dir := filepath.Dir(exe)
	// The macOS Frameworks entry lives beside the bundle, not inside the
	// executable directory; both roots are exe-anchored.
	frameworks := filepath.Join(dir, "..", "Frameworks")
	for _, got := range names[:len(names)-1] {
		if !filepath.IsAbs(got) {
			t.Fatalf("bundle candidate %q is not absolute", got)
		}
		if !strings.HasPrefix(got, dir) && !strings.HasPrefix(got, frameworks) {
			t.Fatalf("bundle candidate %q is not exe-anchored at %q", got, dir)
		}
	}
	if got := names[0]; got != filepath.Join(dir, "libavcodec.so.59") {
		t.Fatalf("first candidate = %q, want the exe-side library", got)
	}
}

// The macOS bundle layout keeps third-party libraries out of the executable
// directory; Frameworks must be among the candidates there.
func TestBundleDirsCoverMacFrameworks(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only layout")
	}
	found := false
	for _, d := range bundleDirs() {
		if filepath.Base(d) == "Frameworks" {
			found = true
		}
	}
	if !found {
		t.Fatalf("bundleDirs = %v, want a Frameworks entry", bundleDirs())
	}
}
