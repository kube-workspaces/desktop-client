// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"fmt"
	"net/http"
)

// AuthConfig is the deployment's authentication configuration as served by
// GET /auth/config.
//
// When authentication is disabled the server sends only {"enabled":false}, so
// every other field is zero in that case.
type AuthConfig struct {
	// Enabled reports whether authentication is switched on at all.
	Enabled bool `json:"enabled"`
	// IssuerURL is the OIDC issuer to start a browser login against.
	IssuerURL string `json:"issuerURL,omitempty"`
	// PersonalNamespaces describes per-user namespace provisioning.
	PersonalNamespaces PersonalNamespacesConfig `json:"personalNamespaces"`
	// Registration describes self-service account creation.
	Registration RegistrationConfig `json:"registration"`
	// LocalAuth describes email/password login availability.
	LocalAuth LocalAuthConfig `json:"localAuth"`
}

// PersonalNamespacesConfig describes per-user namespace provisioning.
type PersonalNamespacesConfig struct {
	// Enabled reports whether each user gets a personal namespace.
	Enabled bool `json:"enabled"`
	// Template is the namespace name template, e.g. "ws-{user}".
	Template string `json:"template"`
}

// RegistrationConfig describes self-service account creation.
type RegistrationConfig struct {
	// AutoProvision reports whether unknown but authenticated users are
	// created on first login.
	AutoProvision bool `json:"autoProvision"`
}

// LocalAuthConfig describes email/password login availability.
type LocalAuthConfig struct {
	// Enabled reports whether [Client.LoginLocal] can be used.
	Enabled bool `json:"enabled"`
}

// Identity is the response of GET /auth/me: who the server thinks we are.
type Identity struct {
	// Authenticated reports whether the request carried a valid session.
	Authenticated bool `json:"authenticated"`
	// AuthEnabled mirrors AuthConfig.Enabled.
	AuthEnabled bool `json:"authEnabled"`
	// Email is the user's email address.
	Email string `json:"email"`
	// DisplayName is the human-friendly name, when known.
	DisplayName string `json:"displayName"`
	// Role is the platform role, e.g. "admin" or "user".
	Role string `json:"role"`
	// Groups are the IdP groups the user belongs to.
	Groups []string `json:"groups"`
	// Namespaces are the namespaces the user may use.
	Namespaces []string `json:"namespaces"`
	// PersonalNamespace is the user's own namespace, if provisioned.
	PersonalNamespace string `json:"personalNamespace"`
	// AvatarURL is an optional avatar image URL.
	AvatarURL string `json:"avatarURL"`
	// MustChangePassword reports that the local password is expired or was
	// administratively reset.
	MustChangePassword bool `json:"mustChangePassword"`
}

// loginRequest is the POST /auth/login/local body.
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// loginResponse is the POST /auth/login/local success body. The token is
// deliberately absent: it only ever travels in the Set-Cookie header.
type loginResponse struct {
	Status             string `json:"status"`
	MustChangePassword bool   `json:"mustChangePassword"`
}

// AuthConfig fetches GET /auth/config. The endpoint is unauthenticated, so it
// is the right first call when deciding how to log in.
func (c *Client) AuthConfig(ctx context.Context) (*AuthConfig, error) {
	var out AuthConfig
	if err := c.doJSON(ctx, requestSpec{
		method: http.MethodGet,
		path:   "/auth/config",
		noAuth: true,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LoginLocal performs an email/password login against POST /auth/login/local
// and returns the session token plus whether the password must be changed.
//
// The token is extracted from the kw-session Set-Cookie header: the response
// body never contains it. The token is not stored on the client; call
// [Client.SetToken] once it has been persisted to the keyring.
//
// Failures map to [ErrRateLimited], [ErrLocalAuthDisabled],
// [ErrInvalidRequest], [ErrInvalidCredentials], [ErrAccountDisabled] and
// [ErrAccountLocked].
func (c *Client) LoginLocal(ctx context.Context, email, password string) (token string, mustChangePassword bool, err error) {
	spec := requestSpec{
		method: http.MethodPost,
		path:   "/auth/login/local",
		body:   loginRequest{Email: email, Password: password},
		// A stale token must not influence a fresh login.
		noAuth: true,
	}
	resp, err := c.do(ctx, spec)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()

	var out loginResponse
	if err := decodeJSON(spec.op(), resp, &out); err != nil {
		return "", false, err
	}

	for _, ck := range resp.Cookies() {
		if ck.Name == SessionCookieName && ck.Value != "" {
			return ck.Value, out.MustChangePassword, nil
		}
	}
	return "", out.MustChangePassword, fmt.Errorf("kwclient: %s: %w", spec.op(), ErrNoSessionCookie)
}

// Me fetches GET /auth/me.
//
// This endpoint reads the kw-session cookie only and ignores
// Authorization: Bearer, which is why the client always sends both.
func (c *Client) Me(ctx context.Context) (*Identity, error) {
	var out Identity
	if err := c.doJSON(ctx, requestSpec{
		method: http.MethodGet,
		path:   "/auth/me",
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
