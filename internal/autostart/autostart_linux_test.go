// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeDesktopLifecycle(t *testing.T) {
	t.Setenv("FLATPAK_ID", "")
	t.Setenv("APPIMAGE", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	n := Native{}
	status, err := n.Status()
	if err != nil || status.Enabled {
		t.Fatalf("initial state: %+v, %v", status, err)
	}
	status, err = n.SetEnabled(true)
	if err != nil || !status.Enabled {
		t.Fatalf("enable: %+v, %v", status, err)
	}
	path, err := autostartPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), " shell\n") || strings.Contains(string(data), "--profile") {
		t.Fatalf("startup must open the shell with no launch-specific arguments: %s, %v", data, err)
	}
	// External removal is authoritative; a query never repairs the entry.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if status, err = n.Status(); err != nil || status.Enabled {
		t.Fatalf("external removal: %+v, %v", status, err)
	}
	for range 2 {
		if status, err = n.SetEnabled(false); err != nil || status.Enabled {
			t.Fatalf("idempotent disable: %+v, %v", status, err)
		}
	}
	data, err = os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "Hidden=true") {
		t.Fatalf("disable must mask system entries too: %s, %v", data, err)
	}
	if status, err = n.SetEnabled(true); err != nil || !status.Enabled {
		t.Fatalf("re-enable: %+v, %v", status, err)
	}
}

func TestDesktopExternalDisable(t *testing.T) {
	path := filepath.Join(t.TempDir(), desktopName)
	for _, data := range []string{
		"[Desktop Entry]\nHidden=true\n",
		"[Desktop Entry]\nX-GNOME-Autostart-enabled=false\n",
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if status, err := desktopStatus(path); err != nil || status.Enabled {
			t.Fatalf("external disable ignored: %+v, %v", status, err)
		}
	}
	if err := os.WriteFile(path, []byte("[Desktop Entry]\nType=Application\n[Other]\nHidden=true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if status, err := desktopStatus(path); err != nil || !status.Enabled {
		t.Fatalf("unrelated group affected startup: %+v, %v", status, err)
	}
}

func TestDesktopExecEscapesBothGrammars(t *testing.T) {
	entry := desktopEntry("/home/a b/$cash`quote\"\\percent%/kube-workspaces")
	want := "Exec=\"/home/a b/\\\\$cash\\\\`quote\\\\\"\\\\\\\\percent%%/kube-workspaces\" shell\n"
	if !strings.Contains(entry, want) {
		t.Fatalf("unsafe Exec escaping:\n%s\nwant %q", entry, want)
	}
}

func TestRelativeXDGDirectoryRejected(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if _, err := autostartPath(); err == nil {
		t.Fatal("relative config directory accepted")
	}
}

func TestAppImageRegistersPersistentOuterImage(t *testing.T) {
	t.Setenv("FLATPAK_ID", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPIMAGE", "/home/user/Apps/Kube Workspaces.AppImage")
	if _, err := (Native{}).SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	path, err := autostartPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `Exec="/home/user/Apps/Kube Workspaces.AppImage" shell`) {
		t.Fatalf("did not register the persistent AppImage: %s, %v", data, err)
	}
}
