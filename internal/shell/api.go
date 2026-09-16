// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

// API is the part of the platform client the shell uses.
//
// It exists so the screens can be driven by a fake. Every method on it does
// network I/O, and a test that has to stand up an HTTP server to assert that
// Tab moves the focus from the email field to the password field is a test
// nobody will run. [*kwclient.Client] satisfies it as it stands — the
// assertion below is the whole adapter — so the interface costs the
// production path nothing.
type API interface {
	// BaseURL returns the instance this client talks to.
	BaseURL() string
	// Token returns the current session token, and SetToken replaces it.
	Token() string
	SetToken(token string)

	// AuthConfig reports how the instance authenticates. It is
	// unauthenticated, so it is also the shell's reachability probe.
	AuthConfig(ctx context.Context) (*kwclient.AuthConfig, error)
	// NativeAuth reports whether the RFC 8252 loopback flow is available.
	NativeAuth(ctx context.Context) (*kwclient.NativeAuthConfig, error)
	// LoginLocal exchanges an email and password for a session token.
	LoginLocal(ctx context.Context, email, password string) (token string, mustChangePassword bool, err error)
	// LoginBrowser runs the loopback + PKCE flow in the system browser.
	LoginBrowser(ctx context.Context, opts *kwclient.BrowserLoginOptions) (*kwclient.BrowserLogin, error)
	// Me reports who the server thinks the session belongs to.
	Me(ctx context.Context) (*kwclient.Identity, error)

	// ListWorkspaces and ListImages populate the main screen.
	ListWorkspaces(ctx context.Context, namespace string) ([]kwclient.Workspace, error)
	ListImages(ctx context.Context) ([]kwclient.Image, error)
	// WorkspaceURL builds the URL a browser should open for a workspace.
	WorkspaceURL(ws kwclient.Workspace, img *kwclient.Image) string
	// GrantBrowserSession hands this client's session to the browser: the
	// server parks the session token behind a single-use code and returns the
	// URL that redeems it into a kw-session cookie.
	GrantBrowserSession(ctx context.Context, redirect string) (*kwclient.BrowserSessionGrant, error)
	// WorkspacePath builds the root-relative /proxy/... path for a workspace,
	// what a browser-session grant should redirect to.
	WorkspacePath(ws kwclient.Workspace, img *kwclient.Image) string
}

// The production client is the interface, with no adapter in between.
var _ API = (*kwclient.Client)(nil)

// Connector opens a workspace's session and returns when it ends.
//
// It blocks the shell's own loop on purpose: the session and the shell run on
// the same main OS thread, so while a session is up the shell is not drawing
// anything anyway. A nil error means the user closed the session window
// normally, and the shell goes back to the workspace list. A VM workspace gets
// its display session; a non-VM workspace opens an integrated terminal.
type Connector func(ctx context.Context, ws kwclient.Workspace) error

// ClientFactory builds the client for an instance, and the connector that goes
// with it.
//
// The two are built together because a display session needs the concrete
// [*kwclient.Client] (it dials the WebSocket bridge through it) while the
// screens only need [API]. Returning both from one call keeps the type
// assertion that would otherwise be needed out of the shell entirely: the
// caller in cmd/ has the concrete client in hand and closes over it.
type ClientFactory func(server string, insecure bool) (API, Connector, error)

// Store persists the instance profile and its session token between runs.
//
// It is an interface for the same reason [API] is: the real implementation
// writes to the user's config directory and their OS keychain, and a UI test
// must do neither.
type Store interface {
	// Load returns the active profile and its stored token. A nil profile
	// with a nil error means "nothing configured yet", which is the state the
	// first-run screen exists for, not a failure.
	Load() (profile *config.Profile, token string, err error)
	// Save records the profile and, when it is not empty, the token.
	Save(profile *config.Profile, token string) error
	// Forget removes the stored token, leaving the profile in place so the
	// user does not have to retype the server URL to sign back in.
	Forget(profile *config.Profile) error

	// LoadSettings returns the client's own preferences. Zero-valued fields
	// mean the defaults, so a never-written config reads back as empty, not
	// as an error.
	LoadSettings() (config.Settings, error)
	// SaveSettings records the client's own preferences.
	SaveSettings(settings config.Settings) error
}

// ConfigStore is the production [Store]: profiles in the user's config
// directory, tokens in the OS keychain.
type ConfigStore struct{}

// Load implements [Store].
func (ConfigStore) Load() (*config.Profile, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "", err
	}
	profile, err := cfg.Active()
	if err != nil {
		// "No profile" and "no active profile" are both first-run states as
		// far as a GUI is concerned: it asks for a server URL either way.
		// Only a config file that could not be read is worth reporting, and
		// config.Load has already done that.
		return nil, "", nil
	}
	token, err := config.LoadToken(profile.Name)
	if err != nil && !errors.Is(err, config.ErrNoToken) {
		// A keychain that is present but unhappy is worth knowing about; a
		// missing entry just means "not signed in".
		return profile, "", err
	}
	return profile, token, nil
}

// Save implements [Store].
func (ConfigStore) Save(profile *config.Profile, token string) error {
	if profile == nil {
		return errors.New("shell: no profile to save")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Put(profile)
	if err := cfg.Save(); err != nil {
		return err
	}
	if token == "" {
		return nil
	}
	_, err = config.SaveToken(profile.Name, token)
	return err
}

// Forget implements [Store].
func (ConfigStore) Forget(profile *config.Profile) error {
	if profile == nil {
		return nil
	}
	return config.DeleteToken(profile.Name)
}

// LoadSettings implements [Store].
func (ConfigStore) LoadSettings() (config.Settings, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Settings{}, err
	}
	return cfg.Settings, nil
}

// SaveSettings implements [Store].
func (ConfigStore) SaveSettings(settings config.Settings) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Settings = settings
	return cfg.Save()
}
