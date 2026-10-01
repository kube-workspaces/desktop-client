// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

func TestRestoredSessionDiscoversSignInMethodsWhenReturningToLogin(t *testing.T) {
	for _, browser := range []bool{false, true} {
		for _, transition := range []string{"sign-out", "expired-list", "revoked-watch"} {
			name := "local/" + transition
			if browser {
				name = "browser/" + transition
			}
			t.Run(name, func(t *testing.T) {
				r := newRig(savedProfile(), "stored-token")
				r.api.set(func(f *fakeAPI) {
					f.authConfig = &kwclient.AuthConfig{Enabled: true, LocalAuth: kwclient.LocalAuthConfig{Enabled: !browser}}
					if browser {
						f.native = &kwclient.NativeAuthConfig{Enabled: true, Methods: []string{"loopback-pkce"}}
					}
				})
				r.start()
				if r.app.m.State != StateWorkspaces || r.app.m.Auth != nil {
					t.Fatal("setup did not restore a session without auth discovery")
				}
				switch transition {
				case "sign-out":
					r.app.act(context.Background(), intent{kind: intentSignOut})
				case "expired-list":
					r.api.set(func(f *fakeAPI) { f.listErr = kwclient.ErrUnauthorized })
					r.app.refreshWorkspaces(context.Background(), true)
				case "revoked-watch":
					r.api.set(func(f *fakeAPI) { f.meErr = kwclient.ErrUnauthorized })
					r.app.watchConnected = true
					r.app.checkWatchIdentity(context.Background(), r.now)
				}
				r.settle()
				if r.app.m.State != StateLogin || r.app.m.Busy {
					t.Fatalf("login did not settle: state=%v busy=%t", r.app.m.State, r.app.m.Busy)
				}
				if browser && !r.app.m.CanUseBrowserAuth() || !browser && !r.app.m.CanUseLocalAuth() {
					t.Fatal("restored session returned to login with no usable sign-in method")
				}
				if transition == "sign-out" && r.app.m.Notice == "" {
					t.Fatal("sign-out explanation lost during discovery")
				}
				if transition != "sign-out" && !strings.Contains(r.app.m.Err, "expired") {
					t.Fatalf("expiry explanation lost: %q", r.app.m.Err)
				}
			})
		}
	}
}

func TestSignOutDiscoveryFailureDoesNotRetryEveryFrame(t *testing.T) {
	r := newRig(savedProfile(), "stored-token")
	r.start()
	r.api.set(func(f *fakeAPI) { f.authErr = errors.New("auth discovery unavailable") })
	r.app.act(context.Background(), intent{kind: intentSignOut})
	r.settle()
	for range 10 {
		r.app.dirty = true
		r.step()
	}
	if r.app.m.State != StateLogin || r.app.m.Busy || !strings.Contains(strings.ToLower(r.app.m.Err), "auth discovery unavailable") {
		t.Fatalf("discovery failure not reported: state=%v busy=%t err=%q", r.app.m.State, r.app.m.Busy, r.app.m.Err)
	}
	r.api.set(func(f *fakeAPI) {
		if f.authCalls != 1 {
			t.Fatalf("failed discovery retried %d times", f.authCalls)
		}
	})
}

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
