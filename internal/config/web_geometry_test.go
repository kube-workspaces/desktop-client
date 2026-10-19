// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWebGeometryIsolation(t *testing.T) {
	seen := map[string]bool{}
	for _, identity := range [][3]string{
		{"one", "https://example.com", "team/code"},
		{"two", "https://example.com", "team/code"},
		{"one", "https://other.example.com", "team/code"},
		{"one", "https://example.com", "team/other"},
		{"one", "https://example.com", "other/code"},
	} {
		path, err := WebGeometryPath(identity[0], identity[1], identity[2])
		if err != nil {
			t.Fatal(err)
		}
		if seen[path] {
			t.Fatalf("identities share path %q", path)
		}
		seen[path] = true
	}
}

func TestWebGeometryAtomicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "web-geometry", "child.json")
	// The child must never touch the shell's settings document.
	shell := filepath.Join(dir, "config.json")
	const settings = `{"settings":{"uiScale":1.5}}`
	if err := os.WriteFile(shell, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		want := WindowRect{X: -1200 + i*20, Y: 30, Width: 1000, Height: 700}
		if err := SaveWebGeometry(path, want); err != nil {
			t.Fatal(err)
		}
		got, err := LoadWebGeometry(path)
		if err != nil || got != want {
			t.Fatalf("round trip = %+v, %v; want %+v", got, err, want)
		}
	}
	if err := SaveWebGeometry(path, WindowRect{Width: -1, Height: 700}); err == nil {
		t.Fatal("accepted invalid native size")
	}
	b, err := os.ReadFile(shell)
	if err != nil || string(b) != settings {
		t.Fatalf("shell settings changed: %s, %v", b, err)
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 1 {
		t.Fatalf("temporary files leaked: %v, %v", files, err)
	}
	if err := os.WriteFile(path, []byte(`{"width":2147483648,"height":700}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWebGeometry(path); err == nil {
		t.Fatal("loaded geometry that overflows a native dimension")
	}
}
