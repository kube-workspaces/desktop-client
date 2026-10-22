// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

func TestEndpointSignInMethodMatrix(t *testing.T) {
	for _, tc := range []struct {
		name           string
		cfg            *kwclient.AuthConfig
		native         bool
		local, browser bool
	}{
		{"unknown", nil, true, false, false},
		{"modern local without legacy flag", &kwclient.AuthConfig{Enabled: true, Methods: []string{"local"}}, true, true, false},
		{"modern oidc overrides legacy local", &kwclient.AuthConfig{Enabled: true, Methods: []string{"oidc"}, LocalAuth: kwclient.LocalAuthConfig{Enabled: true}}, true, false, true},
		{"modern mixed", &kwclient.AuthConfig{Enabled: true, Methods: []string{"local", "oidc"}}, true, true, true},
		{"mixed without native support", &kwclient.AuthConfig{Enabled: true, Methods: []string{"local", "oidc"}}, false, true, false},
		{"empty methods", &kwclient.AuthConfig{Enabled: true, Methods: []string{}, LocalAuth: kwclient.LocalAuthConfig{Enabled: true}, IssuerURL: "https://idp.example.com"}, true, false, false},
		{"unknown method", &kwclient.AuthConfig{Enabled: true, Methods: []string{"future"}}, true, false, false},
		{"legacy local", &kwclient.AuthConfig{Enabled: true, LocalAuth: kwclient.LocalAuthConfig{Enabled: true}}, true, true, false},
		{"legacy mixed", &kwclient.AuthConfig{Enabled: true, LocalAuth: kwclient.LocalAuthConfig{Enabled: true}, IssuerURL: "https://idp.example.com"}, true, true, true},
		{"disabled", &kwclient.AuthConfig{Methods: []string{"local", "oidc"}}, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{Auth: tc.cfg, Native: tc.native}
			if m.CanUseLocalAuth() != tc.local || m.CanUseBrowserAuth() != tc.browser {
				t.Fatalf("local/browser = %t/%t, want %t/%t", m.CanUseLocalAuth(), m.CanUseBrowserAuth(), tc.local, tc.browser)
			}
		})
	}
}

func TestModernLocalSignInThroughNativeForm(t *testing.T) {
	for _, methods := range [][]string{{"local"}, {"local", "oidc"}} {
		t.Run(methods[len(methods)-1], func(t *testing.T) {
			r := newRig(savedProfile(), "")
			r.api.set(func(f *fakeAPI) {
				f.authConfig = &kwclient.AuthConfig{Enabled: true, Methods: methods}
				f.native = &kwclient.NativeAuthConfig{Enabled: true}
			})
			r.start()
			r.focus(idEmail)
			r.typeText("user@example.com")
			r.press(keysym.KeyTab, keysym.ModNone)
			r.typeText("test-password")
			r.clickFocused()
			r.settle()
			if r.app.m.State != StateWorkspaces || r.store.token != "fresh-token" || r.app.passwordField.Value() != "" {
				t.Fatalf("local sign-in failed: state=%v error=%q", r.app.m.State, r.app.m.Err)
			}
		})
	}
}

func TestDiscoveryFailureCanRetryFromLogin(t *testing.T) {
	r := newRig(savedProfile(), "")
	r.api.set(func(f *fakeAPI) { f.authErr = errors.New("discovery unavailable") })
	r.start()
	if r.app.m.Auth != nil || r.app.m.State != StateLogin {
		t.Fatal("failed discovery did not stay on login")
	}
	r.api.set(func(f *fakeAPI) {
		f.authErr = nil
		f.authConfig = &kwclient.AuthConfig{Enabled: true, Methods: []string{"local"}}
	})
	r.focus(idRetryAuth)
	r.clickFocused()
	r.settle()
	if !r.app.m.CanUseLocalAuth() || r.app.m.Err != "" || r.app.m.Busy {
		t.Fatalf("retry did not recover: error=%q busy=%t", r.app.m.Err, r.app.m.Busy)
	}
}

func TestLocalMethodDisabledRefreshesDiscovery(t *testing.T) {
	r := newRig(savedProfile(), "")
	r.start()
	r.api.set(func(f *fakeAPI) {
		f.loginErr = kwclient.ErrLocalAuthDisabled
		f.authConfig = &kwclient.AuthConfig{Enabled: true, Methods: []string{"oidc"}, NativeAuth: &kwclient.NativeAuthConfig{Enabled: true}}
	})
	r.app.signInLocal(context.Background(), "user@example.com", "test-password")
	r.settle()
	if r.app.m.CanUseLocalAuth() || !r.app.m.CanUseBrowserAuth() || r.app.m.Err == "" || r.store.token != "" {
		t.Fatal("disabled method did not refresh capabilities and retain the explanation")
	}
}

// Deliberately returns a successful response after cancellation: a real response
// may already be in flight or queued when the user leaves the screen.
type delayedLocalAPI struct {
	*fakeAPI
	started chan struct{}
	release chan struct{}
}

func (d *delayedLocalAPI) LoginLocal(context.Context, string, string) (string, bool, error) {
	close(d.started)
	<-d.release
	return "late-token", false, nil
}

func TestLateLocalLoginCannotCompleteAfterLeaving(t *testing.T) {
	for _, action := range []intentKind{intentCancel, intentChangeServer} {
		t.Run(map[intentKind]string{intentCancel: "cancel", intentChangeServer: "change-server"}[action], func(t *testing.T) {
			r := newRig(savedProfile(), "")
			r.start()
			d := &delayedLocalAPI{fakeAPI: r.api, started: make(chan struct{}), release: make(chan struct{})}
			r.app.api = d
			r.app.passwordField.SetValue("test-password")
			r.app.signInLocal(context.Background(), "user@example.com", "test-password")
			select {
			case <-d.started:
			case <-time.After(time.Second):
				t.Fatal("login did not start")
			}
			r.app.act(context.Background(), intent{kind: action})
			close(d.release)
			r.settle()
			if r.store.token != "" || r.api.Token() != "" || r.app.passwordField.Value() != "" || r.app.m.Busy {
				t.Fatal("late response installed credentials or retained the password/busy state")
			}
			want := StateLogin
			if action == intentChangeServer {
				want = StateServer
			}
			if r.app.m.State != want {
				t.Fatalf("late response changed screen to %v", r.app.m.State)
			}
		})
	}
}

type delayedDiscoveryAPI struct {
	*fakeAPI
	started chan struct{}
	release chan struct{}
}

func TestCancelLoginVerificationClearsTransientCredential(t *testing.T) {
	r := newRig(savedProfile(), "")
	r.start()
	r.app.finishLogin(context.Background(), "pending-token", "user@example.com", false)
	r.app.act(context.Background(), intent{kind: intentCancel})
	r.settle()
	if r.api.Token() != "" || r.store.token != "" || r.app.m.State != StateLogin || r.app.m.Busy {
		t.Fatal("cancelled verification retained or persisted a credential")
	}
}

func (d *delayedDiscoveryAPI) AuthConfig(context.Context) (*kwclient.AuthConfig, error) {
	close(d.started)
	<-d.release
	return &kwclient.AuthConfig{Enabled: true, Methods: []string{"oidc"}, NativeAuth: &kwclient.NativeAuthConfig{Enabled: true}}, nil
}

func TestLateDiscoveryCannotReplaceLoginAfterCancellation(t *testing.T) {
	r := newRig(savedProfile(), "")
	r.start()
	d := &delayedDiscoveryAPI{fakeAPI: r.api, started: make(chan struct{}), release: make(chan struct{})}
	r.app.api = d
	r.app.m.Auth = nil
	r.app.ensureAuthConfig(context.Background())
	select {
	case <-d.started:
	case <-time.After(time.Second):
		t.Fatal("discovery did not start")
	}
	r.app.act(context.Background(), intent{kind: intentCancel})
	close(d.release)
	r.settle()
	if r.app.m.Auth != nil || r.app.m.Busy {
		t.Fatal("cancelled discovery installed a late snapshot")
	}
	r.app.api = r.api
	r.focus(idRetryAuth)
	r.clickFocused()
	r.settle()
	if !r.app.m.CanUseLocalAuth() || r.app.m.CanUseBrowserAuth() {
		t.Fatal("retry did not discover the current capabilities")
	}
}

type unavailableNativeAPI struct{ *fakeAPI }

func (u *unavailableNativeAPI) NativeAuth(context.Context) (*kwclient.NativeAuthConfig, error) {
	return nil, errors.New("native browser discovery unavailable")
}

func TestLocalLoginSurvivesNativeDiscoveryFailure(t *testing.T) {
	r := newRig(savedProfile(), "")
	r.start()
	r.app.api = &unavailableNativeAPI{r.api}
	r.app.m.Auth = nil
	r.app.ensureAuthConfig(context.Background())
	r.settle()
	if !r.app.m.CanUseLocalAuth() || r.app.m.CanUseBrowserAuth() {
		t.Fatal("native discovery failure suppressed local sign-in")
	}
	r.app.signInLocal(context.Background(), "user@example.com", "test-password")
	r.settle()
	if r.app.m.State != StateWorkspaces || r.store.token != "fresh-token" {
		t.Fatal("local sign-in depended on native browser support")
	}
}
