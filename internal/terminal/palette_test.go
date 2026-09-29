// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package terminal

import (
	"strings"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// TestTerminalF11TogglesFullscreen: F11 is the palette's fullscreen
// affordance in console windows too — it flips the window and is never
// typed into the shell.
func TestTerminalF11TogglesFullscreen(t *testing.T) {
	r := newSeatRig(t, false)
	r.step()
	conn := r.ds.last()
	if conn == nil {
		t.Fatal("no connection after dial")
	}

	r.w.handleEvent(viewer.EventKey{Key: keysym.KeyF11, Down: true})
	if !r.be.Fullscreen() {
		t.Fatal("F11 did not enter fullscreen")
	}
	r.w.handleEvent(viewer.EventKey{Key: keysym.KeyF11, Down: true})
	if r.be.Fullscreen() {
		t.Fatal("second F11 did not leave fullscreen")
	}
	if out := conn.readOut(); strings.Contains(out, "\x1b[23~") {
		t.Fatalf("F11 leaked to the shell: %q", out)
	}
}

// TestTerminalF11WorksWhileBusy: the palette stays usable on the busy
// plate — fullscreen toggles without disturbing the seat wait.
func TestTerminalF11WorksWhileBusy(t *testing.T) {
	r := newSeatRig(t, true)
	r.step()

	r.w.handleEvent(viewer.EventKey{Key: keysym.KeyF11, Down: true})
	if !r.be.Fullscreen() {
		t.Fatal("F11 did not enter fullscreen on the busy plate")
	}
	if status, _ := r.w.statusNow(); status != viewer.StatusDisplayInUse {
		t.Fatalf("status = %v, want the busy plate still up", status)
	}
	if r.ds.callCount() != 0 {
		t.Fatalf("dials = %d, want 0 (F11 is not consent)", r.ds.callCount())
	}
}
