// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

func TestLoginPersistsOnlyVerifiedDeviceCredential(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "rejected"}[rejected], func(t *testing.T) {
			r := newRig(savedProfile(), "")
			r.start()
			r.api.set(func(f *fakeAPI) {
				f.deviceToken = &kwclient.DeviceToken{Token: "device-token", DeviceID: "d1"}
				if rejected {
					f.rejectedToken = "device-token"
				}
			})
			r.app.finishLogin(context.Background(), "fresh-token", "user@example.com", false)
			r.settle()
			if rejected {
				if r.store.token != "" || r.app.m.State != StateLogin {
					t.Fatal("rejected device credential was persisted")
				}
			} else if r.store.token != "device-token" || r.app.m.State != StateWorkspaces {
				t.Fatal("verified device credential did not complete sign-in")
			}
		})
	}
}

type streamingAPI struct {
	*fakeAPI
	snapshots chan []kwclient.Workspace
	cancelled chan struct{}
}

func (s *streamingAPI) WatchWorkspaces(ctx context.Context, _ string, snapshot func([]kwclient.Workspace)) error {
	defer close(s.cancelled)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case items := <-s.snapshots:
			snapshot(items)
		}
	}
}

func TestWorkspaceWatchSuppressesPollingAndStopsOnSignOut(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.start()
	r.app.opts.RefreshInterval = time.Second
	stream := &streamingAPI{fakeAPI: r.api, snapshots: make(chan []kwclient.Workspace, 1), cancelled: make(chan struct{})}
	r.app.api = stream
	t.Cleanup(r.app.stopWorkspaceWatch)
	r.app.tick(context.Background(), r.now)
	stream.snapshots <- []kwclient.Workspace{workspace("team", "live", kwclient.WorkspaceTypeVM, true)}
	deadline := time.Now().Add(time.Second)
	for !r.app.watchConnected && time.Now().Before(deadline) {
		r.step()
	}
	if !r.app.watchConnected || len(r.app.m.Workspaces) != 1 || r.app.m.Workspaces[0].Name != "live" {
		t.Fatal("snapshot not applied")
	}
	r.settle()
	r.api.mu.Lock()
	before := r.api.listCalls
	r.api.mu.Unlock()
	r.app.tick(context.Background(), r.now.Add(10*time.Second))
	r.settle()
	r.api.mu.Lock()
	after := r.api.listCalls
	r.api.mu.Unlock()
	if before != after {
		t.Fatal("healthy stream still polls")
	}
	r.app.signOut()
	select {
	case <-stream.cancelled:
	case <-time.After(time.Second):
		t.Fatal("sign-out leaked the stream")
	}
	r.app.drainResults()
	if r.app.m.State != StateLogin || r.app.watchConnected {
		t.Fatal("late watch result restored signed-in state")
	}
}
