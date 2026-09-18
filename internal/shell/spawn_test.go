// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestWebChildPath covers the sibling-detection helper that decides whether a
// spawn goes to the standalone kube-workspaces-web binary or falls back to the
// `web` subcommand.
func TestWebChildPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "kube-workspaces")

	if got := webChildPath(exe); got != "" {
		t.Fatalf("no sibling yet: webChildPath = %q, want \"\"", got)
	}

	// A directory with the child's name does not count as a child binary.
	sibling := webChildName()
	if err := os.Mkdir(filepath.Join(dir, sibling), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := webChildPath(exe); got != "" {
		t.Fatalf("directory is not a child binary: webChildPath = %q, want \"\"", got)
	}
	// webChildName must be stable between calls, otherwise the assertions
	// below say nothing.
	if got := webChildName(); got != sibling {
		t.Fatalf("webChildName = %q, want stable %q", got, sibling)
	}

	if err := os.Remove(filepath.Join(dir, sibling)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sibling), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, sibling)
	if got := webChildPath(exe); got != want {
		t.Fatalf("webChildPath = %q, want %q", got, want)
	}
}

// webChildName is the sibling-child filename webChildPath looks for. It is
// factored out so the test can manufacture the right name on any platform.
func webChildName() string {
	name := "kube-workspaces-web"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// writeArgsRecorder writes an executable script at path that records its own
// argv into marker and exits, so a spawning test can assert on exactly what
// spawnWebExe handed the child.
func writeArgsRecorder(t *testing.T, path, marker string) {
	t.Helper()
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + marker + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// spawnedArgs waits for the recorder's marker to appear and returns its lines.
func spawnedArgs(t *testing.T, marker string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(marker)
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(lines) == 1 && lines[0] == "" {
				return nil
			}
			return lines
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child never wrote its arguments")
	return nil
}

// assertArgs fails the test unless the spawned argv is exactly want.
func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("child got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("child got %q, want %q", got, want)
		}
	}
}

// TestSpawnWebPrefersChildBinary checks that spawnWeb execs the sibling
// kube-workspaces-web binary (with the profile pin) when one sits beside the
// shell. Both the shell and the child record their argv to the same marker, so
// a wrong choice of target is a different argument vector and fails the
// assertArgs check — e.g. a fallback to the shell's `web` subcommand would
// arrive as `web --profile team-profile team/code`.
func TestSpawnWebPrefersChildBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("spawn tests exec /bin/sh shell scripts; verify on linux/darwin")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "args")

	exe := filepath.Join(dir, "kube-workspaces")
	writeArgsRecorder(t, exe, marker)
	writeArgsRecorder(t, filepath.Join(dir, webChildName()), marker)

	if err := spawnWebExe(exe, "team-profile", "team", "code", ""); err != nil {
		t.Fatalf("spawnWeb: %v", err)
	}
	assertArgs(t, spawnedArgs(t, marker), []string{"--profile", "team-profile", "team/code"})
}

// TestSpawnWebFallsBackToSubcommand checks that a developer copy with no
// sibling child re-execs the shell's own `web` subcommand, and that an empty
// profile pin is not forwarded (the child then resolves the active profile
// itself).
func TestSpawnWebFallsBackToSubcommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("spawn tests exec /bin/sh shell scripts; verify on linux/darwin")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "args")

	exe := filepath.Join(dir, "kube-workspaces")
	writeArgsRecorder(t, exe, marker)

	if err := spawnWebExe(exe, "", "team", "code", ""); err != nil {
		t.Fatalf("spawnWeb: %v", err)
	}
	assertArgs(t, spawnedArgs(t, marker), []string{"web", "team/code"})
}

// TestSpawnWebForwardsLaunchDisplay checks that the launch-display bounds the
// shell computed are handed to the child on the env var it reads, and that an
// empty value leaves the environment untouched (so the recorder sees nothing).
func TestSpawnWebForwardsLaunchDisplay(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("spawn tests exec /bin/sh shell scripts; verify on linux/darwin")
	}

	writeEnvRecorder := func(t *testing.T, path, marker string) {
		t.Helper()
		body := "#!/bin/sh\nprintf '%s\\n' \"$KW_WEB_LAUNCH_DISPLAY\" > \"" + marker + "\"\n"
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("bounds forwarded", func(t *testing.T) {
		dir := t.TempDir()
		marker := filepath.Join(dir, "env")
		exe := filepath.Join(dir, "kube-workspaces")
		writeEnvRecorder(t, exe, marker)
		if err := spawnWebExe(exe, "", "team", "code", "-1920,0,1920,1080"); err != nil {
			t.Fatalf("spawnWebExe: %v", err)
		}
		got := spawnedArgs(t, marker)
		if len(got) != 1 || got[0] != "-1920,0,1920,1080" {
			t.Fatalf("child saw KW_WEB_LAUNCH_DISPLAY=%q, want [\"-1920,0,1920,1080\"]", got)
		}
	})

	t.Run("empty not forwarded", func(t *testing.T) {
		dir := t.TempDir()
		marker := filepath.Join(dir, "env")
		exe := filepath.Join(dir, "kube-workspaces")
		writeEnvRecorder(t, exe, marker)
		if err := spawnWebExe(exe, "", "team", "code", ""); err != nil {
			t.Fatalf("spawnWebExe: %v", err)
		}
		// The recorder prints an empty line for an unset variable; spawnedArgs
		// turns that into nil, which is exactly "not set" in env terms.
		if got := spawnedArgs(t, marker); got != nil {
			t.Fatalf("child saw KW_WEB_LAUNCH_DISPLAY set to %q, want unset", got)
		}
	})
}
