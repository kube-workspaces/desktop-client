// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/session"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// TestTheWindowKeepsItsSizeAcrossASession drives the whole thing: the shell
// opens a session on a workspace, and since it runs in its own window, the
// shell's window is untouched and the user's list comes back exactly as they
// left it.
func TestTheWindowKeepsItsSizeAcrossASession(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	vm := workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)
	r.api.set(func(f *fakeAPI) { f.workspaces = []kwclient.Workspace{vm} })

	r.connect = func(ctx context.Context, ws kwclient.Workspace) error {
		// In the new architecture, the connector creates a fresh backend
		// and runs the session in it. The shell's own backend (r.be) is
		// never touched.
		return nil
	}
	r.start()

	before := [2]int{}
	before[0], before[1] = r.be.size()
	if before != [2]int{1280, 800} {
		t.Fatalf("the shell opened at %v, want 1280x800", before)
	}

	r.focus(idList)
	r.clickFocused()
	r.step()
	// Opening registers a live window within the first Step of StateSession
	// and returns to the list at once.
	r.settle()

	if len(r.opened) != 1 {
		t.Fatalf("the connector was called %d times", len(r.opened))
	}
	if w, h := r.be.size(); [2]int{w, h} != before {
		t.Fatalf("the window is %dx%d after the session, want %v", w, h, before)
	}
	if r.app.m.State != StateWorkspaces {
		t.Fatalf("state after the session = %v", r.app.m.State)
	}
}

func TestViewerStatusMapping(t *testing.T) {
	tests := []struct {
		state session.State
		want  viewer.Status
		ok    bool
	}{
		{session.StateConnecting, viewer.StatusConnecting, true},
		{session.StateConnected, viewer.StatusLive, true},
		{session.StateReconnecting, viewer.StatusReconnecting, true},
		{session.StateDisplayInUse, viewer.StatusDisplayInUse, true},
		{session.StateFailed, viewer.StatusFailed, true},
		{session.StateClosed, viewer.StatusLive, false},
	}
	for _, tt := range tests {
		got, detail, ok := viewerStatus(tt.state, nil)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Fatalf("viewerStatus(%v) = (%v, %q, %t), want (%v, _, %t)", tt.state, got, detail, ok, tt.want, tt.ok)
		}
		if tt.state == session.StateFailed && detail == "" {
			t.Fatal("a failure with no error must still say something")
		}
	}
}

func TestAgentTierDialSelectsAgentHandle(t *testing.T) {
	vm := workspace("team", "vm-a", kwclient.WorkspaceTypeVM, true)
	d := &sessionDialer{opts: SessionOptions{AgentTier: true}, tier1Available: func() error { return nil }}
	handle, err := d.Dial(context.Background(), vm, false)
	if err != nil {
		t.Fatalf("agent-tier dial: %v", err)
	}
	agent, ok := handle.(*agentHandle)
	if !ok {
		t.Fatalf("agent-tier dial opened %T, want *agentHandle", handle)
	}
	if agent.Kind() != "agent" {
		t.Fatalf("kind = %q, want agent", agent.Kind())
	}
	// The force flag is VM-scoped: a non-VM workspace never yields an agent
	// handle.
	plain := workspace("team", "ctr-a", kwclient.WorkspaceTypeContainer, true)
	other, err := d.Dial(context.Background(), plain, false)
	if err != nil {
		t.Fatalf("plain dial: %v", err)
	}
	if _, ok := other.(*agentHandle); ok {
		t.Fatal("agent handle must never serve a non-VM workspace")
	}
}

func TestAgentTierDialDefaultsToAdvertisedTransport(t *testing.T) {
	agentVM := workspace("team", "vm-agent", kwclient.WorkspaceTypeVM, true)
	agentVM.RemoteDesktop = &kwclient.ImageRemoteDesktop{Protocol: "agent", Port: 44787}
	selkiesVM := workspace("team", "vm-selkies", kwclient.WorkspaceTypeVM, true)
	selkiesVM.RemoteDesktop = &kwclient.ImageRemoteDesktop{Protocol: "selkies", Port: 8080}
	plainVM := workspace("team", "vm-plain", kwclient.WorkspaceTypeVM, true)
	container := workspace("team", "ctr", kwclient.WorkspaceTypeContainer, true)
	for _, tc := range []struct {
		name string
		opts SessionOptions
		ws   kwclient.Workspace
		want bool
	}{
		{"advertised default", SessionOptions{}, agentVM, true},
		{"advertised forced", SessionOptions{AgentTier: true}, agentVM, true},
		{"advertised opted out", SessionOptions{NoAgentTier: true}, agentVM, false},
		{"selkies advert is not agent", SessionOptions{}, selkiesVM, false},
		{"unadvertised default", SessionOptions{}, plainVM, false},
		{"unadvertised forced", SessionOptions{AgentTier: true}, plainVM, true},
		{"unadvertised opted out", SessionOptions{NoAgentTier: true}, plainVM, false},
		{"container forced", SessionOptions{AgentTier: true}, container, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := useAgentTransport(tc.opts, tc.ws); got != tc.want {
				t.Fatalf("useAgentTransport = %v, want %v", got, tc.want)
			}
		})
	}
	// The advertised default must reach the live agent handle without an API
	// client at dial time (the transport dials lazily on Open).
	d := &sessionDialer{opts: SessionOptions{}, tier1Available: func() error { return nil }}
	handle, err := d.Dial(context.Background(), agentVM, false)
	if err != nil {
		t.Fatalf("advertised dial: %v", err)
	}
	if _, ok := handle.(*agentHandle); !ok {
		t.Fatalf("advertised dial opened %T, want *agentHandle", handle)
	}
}
