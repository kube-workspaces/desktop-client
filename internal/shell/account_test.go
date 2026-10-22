// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

func (f *fakeAPI) ListDevices(context.Context) ([]kwclient.DeviceInfo, error) { return nil, nil }
func (f *fakeAPI) RevokeDevice(context.Context, string) error                 { return nil }
func (f *fakeAPI) ListSSHKeys(context.Context) ([]kwclient.SSHKey, error)     { return nil, nil }
func (f *fakeAPI) CreateSSHKey(_ context.Context, p kwclient.CreateSSHKeyPayload) (*kwclient.SSHKey, error) {
	return &kwclient.SSHKey{Name: p.Name, KeyName: p.KeyName, PublicKey: p.PublicKey}, nil
}
func (f *fakeAPI) DeleteSSHKey(context.Context, string, string) error   { return nil }
func (f *fakeAPI) ChangePassword(context.Context, string, string) error { return nil }

type accountAPI struct {
	*fakeAPI
	keys                       []kwclient.SSHKey
	devices                    []kwclient.DeviceInfo
	created                    kwclient.CreateSSHKeyPayload
	deleted, revoked, password string
	err                        error
	gate                       chan struct{}
	rotatePassword             bool
}

func (f *accountAPI) ListSSHKeys(context.Context) ([]kwclient.SSHKey, error) {
	if f.gate != nil {
		<-f.gate
	}
	return slices.Clone(f.keys), f.err
}
func (f *accountAPI) ListDevices(context.Context) ([]kwclient.DeviceInfo, error) {
	return slices.Clone(f.devices), nil
}
func (f *accountAPI) CreateSSHKey(_ context.Context, p kwclient.CreateSSHKeyPayload) (*kwclient.SSHKey, error) {
	f.created = p
	k := kwclient.SSHKey{Name: p.Name, Namespace: "personal", KeyName: p.KeyName, PublicKey: p.PublicKey}
	f.keys = append(f.keys, k)
	return &k, nil
}
func (f *accountAPI) DeleteSSHKey(_ context.Context, namespace, name string) error {
	f.deleted = namespace + "/" + name
	return f.err
}
func (f *accountAPI) RevokeDevice(_ context.Context, id string) error { f.revoked = id; return f.err }
func (f *accountAPI) ChangePassword(_ context.Context, _, next string) error {
	f.password = next
	if f.err == nil && f.rotatePassword {
		f.set(func(api *fakeAPI) {
			identity := *api.identity
			identity.MustChangePassword = false
			api.identity = &identity
			api.token = "replacement-session"
		})
	}
	return f.err
}

func TestMandatoryPasswordChangeGatesRestoredAndFreshNativeSessions(t *testing.T) {
	for _, restored := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "restored"}[restored], func(t *testing.T) {
			token := ""
			if restored {
				token = "restricted-session"
			}
			r := newRig(savedProfile(), token)
			r.api.set(func(f *fakeAPI) {
				identity := *f.identity
				identity.MustChangePassword = true
				canChange := true
				identity.CanChangePassword = &canChange
				f.identity = &identity
			})
			r.start()
			if !restored {
				r.app.finishLogin(context.Background(), "restricted-session", "user@example.com", true)
				r.settle()
			}
			if r.app.m.State != StateProfile || r.app.account == nil || !r.app.account.passwordOpen {
				t.Fatal("required change did not open the native password form")
			}
			r.app.act(context.Background(), intent{kind: intentRefresh})
			r.app.act(context.Background(), intent{kind: intentAccountAddKey})
			r.settle()
			r.api.set(func(f *fakeAPI) {
				if f.listCalls != 0 || f.deviceCalls != 0 {
					t.Fatalf("restricted native sign-in accessed workspaces/devices: %d/%d", f.listCalls, f.deviceCalls)
				}
			})
			f := &accountAPI{fakeAPI: r.api, rotatePassword: true}
			r.app.api = f
			p := r.app.account
			p.current.SetValue("temporary-password")
			p.next.SetValue("replacement-password")
			p.confirmPassword.SetValue("replacement-password")
			r.app.act(context.Background(), intent{kind: intentAccountPassword})
			r.settle()
			if r.app.m.Identity.MustChangePassword || r.store.token != "replacement-session" || p.busy || p.current.Value() != "" {
				t.Fatal("password replacement was not verified/persisted or restriction remained")
			}
			r.app.act(context.Background(), intent{kind: intentAccountDone})
			r.settle()
			if r.app.m.State != StateWorkspaces {
				t.Fatal("verified password change did not release workspace access")
			}
		})
	}
}

func TestCancelMandatoryPasswordChangeSignsOut(t *testing.T) {
	r := newRig(savedProfile(), "restricted-session")
	r.api.set(func(f *fakeAPI) { identity := *f.identity; identity.MustChangePassword = true; f.identity = &identity })
	r.start()
	r.app.act(context.Background(), intent{kind: intentAccountDone})
	r.settle()
	if r.app.m.State != StateLogin || r.app.api.Token() != "" || r.store.token != "" {
		t.Fatal("closing required password change bypassed restriction")
	}
}

func accountRig(t *testing.T) (*rig, *accountAPI) {
	t.Helper()
	r := newRig(savedProfile(), "token")
	r.start()
	f := &accountAPI{fakeAPI: r.api, devices: []kwclient.DeviceInfo{{DeviceID: "device", Name: "Laptop", CreatedAt: 1, ExpiresAt: 2}}}
	r.app.api = f
	r.app.m.Identity.PersonalNamespace = "personal"
	r.app.m.Identity.Namespaces = []string{"personal", "team"}
	r.app.m.Identity.Groups = []string{"developers"}
	r.focus(idAccount)
	r.clickFocused()
	r.settle()
	if r.app.m.State != StateProfile || r.app.account == nil || r.app.account.loading {
		t.Fatal("Profile entry did not load native account view")
	}
	return r, f
}

func TestAccountNavigationAndNamespace(t *testing.T) {
	r, _ := accountRig(t)
	r.focus("account-namespace-personal")
	r.clickFocused()
	if r.app.profile.Namespace != "personal" || r.store.profile.Namespace != "personal" {
		t.Fatal("namespace not selected and persisted")
	}
	r.focus("account-close")
	r.clickFocused()
	if r.app.m.State != StateWorkspaces || r.app.account != nil || !r.app.ctx.Focused(idUserMenu) {
		t.Fatal("Close did not clear view and restore avatar focus")
	}
}

func TestAccountSSHKeysAndDeviceConfirmation(t *testing.T) {
	r, f := accountRig(t)
	p := r.app.account
	p.name.SetValue("Work Laptop!")
	p.publicKey.SetValue(" ssh-ed25519 AAAA comment ")
	r.focus("account-add-key")
	r.clickFocused()
	r.settle()
	if f.created.Name != "work-laptop" || f.created.KeyName != "Work Laptop!" || f.created.PublicKey != "ssh-ed25519 AAAA comment" || len(p.keys) != 1 || p.publicKey.Value() != "" {
		t.Fatalf("key add failed: %+v %+v", f.created, p.keys)
	}
	r.focus("account-delete-personal/work-laptop")
	r.clickFocused()
	r.settle()
	if f.deleted != "personal/work-laptop" || len(p.keys) != 0 {
		t.Fatal("key deletion failed")
	}
	r.focus("account-revoke-device")
	r.clickFocused()
	if f.revoked != "" || p.confirm != "device" {
		t.Fatal("revoke did not require confirmation")
	}
	if !r.app.ctx.Focused("account-confirm-device") {
		t.Fatal("device confirmation lost keyboard focus")
	}
	r.focus("account-confirm-device")
	r.clickFocused()
	r.settle()
	if f.revoked != "device" || len(p.devices) != 0 {
		t.Fatal("confirmed revoke failed")
	}
}

func TestAccountPasswordValidationAndErrors(t *testing.T) {
	r, f := accountRig(t)
	r.app.m.Auth.LocalAuth.Enabled = true
	r.app.dirty = true
	r.step()
	r.focus("account-password-toggle")
	r.clickFocused()
	p := r.app.account
	p.current.SetValue("old")
	p.next.SetValue("short")
	p.confirmPassword.SetValue("short")
	r.focus("account-password-save")
	r.clickFocused()
	if p.err == "" || f.password != "" {
		t.Fatal("short password accepted")
	}
	p.next.SetValue("new-password-long")
	p.confirmPassword.SetValue("different")
	r.clickFocused()
	if f.password != "" {
		t.Fatal("mismatching confirmation accepted")
	}
	p.confirmPassword.SetValue("new-password-long")
	r.clickFocused()
	r.settle()
	if f.password != "new-password-long" || p.passwordOpen || p.current.Value() != "" || p.notice == "" {
		t.Fatal("password change did not clear secrets and show success")
	}
	f.err = kwclient.ErrUnauthorized
	p.name.SetValue("new")
	p.publicKey.SetValue("ssh-ed25519 AAAA")
	r.focus("account-add-key")
	r.clickFocused()
	r.settle()
	if r.app.m.State != StateLogin || r.app.account != nil {
		t.Fatal("unauthorized Profile operation did not end sign-in")
	}
}

func TestAccountLateResultsAndKeyboardScroll(t *testing.T) {
	r, f := accountRig(t)
	r.focus("account-revoke-device")
	r.app.dirty = true
	r.step()
	if r.app.account.scroll <= 0 {
		t.Fatal("keyboard focus did not reveal lower card")
	}
	f.gate = make(chan struct{})
	r.app.openAccount(context.Background())
	r.app.act(context.Background(), intent{kind: intentAccountDone})
	close(f.gate)
	r.settle()
	if r.app.account != nil || r.app.m.State != StateWorkspaces {
		t.Fatal("late load restored departed Profile view")
	}
}

func TestAccountPartialLoadError(t *testing.T) {
	r, f := accountRig(t)
	f.err = errors.New("keys unavailable")
	r.app.openAccount(context.Background())
	r.settle()
	if r.app.account.keyErr != "keys unavailable" || len(r.app.account.devices) != 1 {
		t.Fatal("one panel failure discarded other account data")
	}
	if !slices.Contains(r.app.ctx.Focus().Order(), ui.FocusID("account-revoke-device")) {
		t.Fatal("devices unreachable after SSH key error")
	}
}
