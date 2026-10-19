// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"slices"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

func rightClickWorkspace(t *testing.T, r *rig, index int) {
	t.Helper()
	th := r.app.opts.Theme
	y := th.Pad + ui.TextHeight(th.Title, th.Font) + th.Gap + th.Gap + th.Gap/2 + th.ControlHeight + th.Gap + th.BorderWidth
	rowH := r.app.list.RowHeight
	if rowH == 0 {
		rowH = th.RowHeight
	}
	p := ui.Point{X: th.Pad + 40, Y: y + index*rowH + rowH/2}
	r.be.send(ui.EventPointer{X: p.X, Y: p.Y, Buttons: ui.ButtonRight})
	r.step()
	r.be.send(ui.EventPointer{X: p.X, Y: p.Y})
	r.step()
	if r.app.workspaceMenu.key == "" {
		t.Fatal("right-click did not open workspace menu")
	}
}

func TestWorkspaceMenuSelectsRowAndDispatches(t *testing.T) {
	r := newRig(savedProfile(), "token")
	r.start()
	first := workspace("team", "first", kwclient.WorkspaceTypeVM, true)
	second := workspace("team", "second", kwclient.WorkspaceTypeContainer, true)
	r.app.m.WorkspacesLoaded([]kwclient.Workspace{first, second}, r.now)
	r.app.m.Selected = first.Key()
	rightClickWorkspace(t, r, 1)
	if r.app.m.Selected != second.Key() || len(r.webbed) != 0 || len(r.opened) != 0 {
		t.Fatal("right-click did not select its row, or activated it immediately")
	}
	want := []ui.FocusID{idOpen, idOpenInBrowser, idStop, idConsole, idInfo}
	if got := r.app.ctx.Focus().Order(); !slices.Equal(got, want) {
		t.Fatalf("container menu traversal = %v, want %v", got, want)
	}
	r.clickFocused()
	r.settle()
	if len(r.webbed) != 1 || r.webbed[0].Key() != second.Key() || r.app.workspaceMenu.key != "" {
		t.Fatal("menu did not open the right-clicked workspace's web view")
	}
}

func TestWorkspaceMenuDismissalAndLiveState(t *testing.T) {
	r := newRig(savedProfile(), "token")
	r.start()
	ws := workspace("team", "vm", kwclient.WorkspaceTypeVM, true)
	r.app.m.WorkspacesLoaded([]kwclient.Workspace{ws}, r.now)
	rightClickWorkspace(t, r, 0)
	if !slices.Contains(r.app.ctx.Focus().Order(), idObserve) {
		t.Fatal("running VM menu lacks Observe")
	}
	r.app.m.Notice = "keep this"
	r.press(keysym.KeyEscape, keysym.ModNone)
	if r.app.workspaceMenu.key != "" || r.app.m.Notice != "keep this" || !r.app.ctx.Focused(idList) {
		t.Fatal("Escape leaked to background or failed to restore list focus")
	}
	rightClickWorkspace(t, r, 0)
	ws.Stopped, ws.ReadyReplicas = true, 0
	r.app.m.WorkspacesLoaded([]kwclient.Workspace{ws}, r.now)
	r.app.dirty = true
	r.step()
	if got := r.app.ctx.Focus().Order(); !slices.Equal(got, []ui.FocusID{idOpen, idInfo}) {
		t.Fatalf("stopped VM menu retained running actions: %v", got)
	}
	// An outside dismissal must consume both halves of the click.
	p := ui.Point{X: r.be.w - 1, Y: r.be.h - 1}
	menuPointer(r, p, true)
	menuPointer(r, p, false)
	if r.app.workspaceMenu.key != "" || !r.app.ctx.Focused(idList) {
		t.Fatal("outside click did not dismiss cleanly")
	}
	rightClickWorkspace(t, r, 0)
	r.app.m.WorkspacesLoaded(nil, r.now)
	r.app.dirty = true
	r.step()
	if r.app.workspaceMenu.key != "" {
		t.Fatal("menu survived removal of its workspace")
	}
}

func TestWorkspaceMenuMouseAction(t *testing.T) {
	r := newRig(savedProfile(), "token")
	r.start()
	ws := workspace("team", "vm", kwclient.WorkspaceTypeVM, true)
	r.app.m.WorkspacesLoaded([]kwclient.Workspace{ws}, r.now)
	rightClickWorkspace(t, r, 0)
	th := r.app.opts.Theme
	anchor := r.app.workspaceMenu.anchor
	// Info is the sixth action for a running Linux VM.
	p := ui.Point{X: anchor.X + th.Pad, Y: anchor.Y + th.BorderWidth + 5*th.ControlHeight + th.ControlHeight/2}
	menuPointer(r, p, true)
	menuPointer(r, p, false)
	if r.app.m.Info == nil || r.app.m.Info.Key() != ws.Key() || r.app.workspaceMenu.key != "" {
		t.Fatal("clicking Info did not open the selected workspace's details")
	}
}
