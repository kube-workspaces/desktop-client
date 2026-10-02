// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/connection"
)

// nativeWebRig flips the rig onto the embedded-web path: the real child is
// spawned through a navigation capability, where the rig's own OpenWeb fake
// skips the capability entirely.
func nativeWebRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(savedProfile(), "token")
	r.app.opts.nativeWeb = true
	t.Cleanup(func() {
		webLive.Lock()
		for key := range webLive.navigation {
			delete(webLive.navigation, key)
		}
		webLive.Unlock()
	})
	return r
}

func drainOnce(t *testing.T) {
	t.Helper()
	// The channel is process-wide; drain anything a parallel test left.
	for len(webNavigationEvents) > 0 {
		<-webNavigationEvents
	}
}

// TestDrainWebNavigationAnswersChildRequests: a web child cannot raise itself,
// so its Sessions request must bring the shell forward and open the list,
// while the workspace-list request only takes focus.
func TestDrainWebNavigationAnswersChildRequests(t *testing.T) {
	r := nativeWebRig(t)
	drainOnce(t)

	raises := r.be.raises
	webNavigationEvents <- connection.WorkspaceList
	r.app.drainWebNavigation()
	if r.app.m.SessionList {
		t.Fatal("a workspace-list request opened the session list")
	}
	if r.be.raises <= raises {
		t.Fatal("the shell was not raised for the child request")
	}
	if r.app.dirty != true {
		t.Fatal("the shell did not mark itself for redraw")
	}

	raises = r.be.raises
	webNavigationEvents <- connection.Sessions
	r.app.drainWebNavigation()
	if !r.app.m.SessionList {
		t.Fatal("a sessions request did not open the session list")
	}
	if r.be.raises <= raises {
		t.Fatal("the shell was not raised for the sessions request")
	}

	// The queue is drained, not sampled: one request is answered once.
	raises = r.be.raises
	r.app.drainWebNavigation()
	if r.be.raises != raises {
		t.Fatalf("an empty queue still raised the shell (%d -> %d)", raises, r.be.raises)
	}
}

// TestDrainWebNavigationIgnoresCustomWebOpener: an OpenWeb the embedder
// supplied is not a toolbar child, so the shell must not listen for it.
func TestDrainWebNavigationIgnoresCustomWebOpener(t *testing.T) {
	r := newRig(savedProfile(), "token")
	drainOnce(t)
	if r.app.opts.nativeWeb {
		t.Fatal("a custom OpenWeb must not claim the native child path")
	}
	r.app.drainWebNavigation()
	if len(webNavigationEvents) != 0 {
		t.Fatal("the shared queue was drained for a non-native opener")
	}
}

// TestOpenWebStartsNavigationForChild pins the whole capability handoff: the
// shell arms one navigation endpoint per workspace, and a spawn that fails
// closes it again instead of leaking a loopback listener.
func TestOpenWebStartsNavigationForChild(t *testing.T) {
	r, _ := trayRig(t)
	r.app.opts.nativeWeb = true
	const key = "team/web-a"
	for webChildOpen(key) {
		webChildExited(key)
	}
	ws := lookupMust(t, r, key)

	calls := 0
	r.app.opts.OpenWeb = func(string, string, string) error {
		calls++
		return errors.New("no webview here")
	}
	r.app.openWeb(context.Background(), ws)
	if calls != 1 {
		t.Fatalf("OpenWeb calls = %d, want 1", calls)
	}
	webLive.Lock()
	left := len(webLive.navigation)
	webLive.Unlock()
	if left != 0 {
		t.Fatalf("a failed spawn left %d navigation endpoints behind", left)
	}
	if r.app.m.Err == "" {
		t.Fatal("the failed spawn reported nothing")
	}
}

// TestSpawnWebForwardsNavigationCapability: the child receives its own
// capability in the environment and nothing else about the shell.
func TestSpawnWebForwardsNavigationCapability(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("spawn tests exec /bin/sh shell scripts; verify on linux/darwin")
	}
	nav, err := connection.StartNavigation(func(connection.Action) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	const key = "team/code"
	webLive.Lock()
	webLive.navigation[key] = nav
	webLive.Unlock()
	t.Cleanup(func() {
		for webChildOpen(key) {
			webChildExited(key)
		}
	})

	dir := t.TempDir()
	marker := filepath.Join(dir, "env")
	exe := filepath.Join(dir, "kube-workspaces")
	body := "#!/bin/sh\nprintf '%s\\n%s\\n' \"" + connection.NavigationURLEnv + "=${" + connection.NavigationURLEnv + "}\" \"" + connection.NavigationTokenEnv + "=${" + connection.NavigationTokenEnv + "}\" > \"" + marker + "\"\n"
	if err := os.WriteFile(exe, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, webChildName()), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := spawnWebExe(exe, "team-profile", "team", "code", ""); err != nil {
		t.Fatalf("spawnWebExe: %v", err)
	}
	got := spawnedArgs(t, marker)
	want := []string{connection.NavigationURLEnv + "=" + nav.URL, connection.NavigationTokenEnv + "=" + nav.Token}
	assertArgs(t, got, want)
}

// TestSpawnWebWithoutCapabilitySendsNothing: a child spawned for a workspace
// the shell never armed gets no navigation env, so it shows chrome without a
// navigation target rather than inheriting a stale one.
func TestSpawnWebWithoutCapabilitySendsNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("spawn tests exec /bin/sh shell scripts; verify on linux/darwin")
	}
	for key := range webLive.navigation {
		delete(webLive.navigation, key)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "env")
	exe := filepath.Join(dir, "kube-workspaces")
	body := "#!/bin/sh\nprintf '%s\\n' \"" + connection.NavigationURLEnv + "=${" + connection.NavigationURLEnv + "}\" > \"" + marker + "\"\n"
	if err := os.WriteFile(exe, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := spawnWebExe(exe, "", "team", "bare", ""); err != nil {
		t.Fatalf("spawnWebExe: %v", err)
	}
	assertArgs(t, spawnedArgs(t, marker), []string{connection.NavigationURLEnv + "="})
}
