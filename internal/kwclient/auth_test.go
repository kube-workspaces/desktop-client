// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestAuthConfig(t *testing.T) {
	tests := []struct {
		name string
		body string
		want AuthConfig
	}{
		{
			name: "enabled",
			body: `{
				"enabled": true,
				"issuerURL": "https://dex.example.com",
				"personalNamespaces": {"enabled": true, "template": "ws-{user}"},
				"registration": {"autoProvision": true},
				"localAuth": {"enabled": true}
			}`,
			want: AuthConfig{
				Enabled:            true,
				IssuerURL:          "https://dex.example.com",
				PersonalNamespaces: PersonalNamespacesConfig{Enabled: true, Template: "ws-{user}"},
				Registration:       RegistrationConfig{AutoProvision: true},
				LocalAuth:          LocalAuthConfig{Enabled: true},
			},
		},
		{
			// When auth is off the server sends the short shape only.
			name: "disabled",
			body: `{"enabled":false}`,
			want: AuthConfig{Enabled: false},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				writeJSON(t, w, http.StatusOK, tc.body)
			})

			got, err := c.AuthConfig(context.Background())
			if err != nil {
				t.Fatalf("AuthConfig: %v", err)
			}
			if gotPath != "/auth/config" {
				t.Errorf("path = %q, want /auth/config", gotPath)
			}
			if *got != tc.want {
				t.Errorf("AuthConfig = %+v, want %+v", *got, tc.want)
			}
		})
	}
}

func TestLoginLocalSuccess(t *testing.T) {
	tests := []struct {
		name               string
		body               string
		mustChangePassword bool
	}{
		{name: "ok", body: `{"status":"ok","mustChangePassword":false}`},
		{name: "must change password", body: `{"status":"ok","mustChangePassword":true}`, mustChangePassword: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotReq loginRequest
			var gotAuthHeader string
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/auth/login/local" {
					t.Errorf("got %s %s, want POST /auth/login/local", r.Method, r.URL.Path)
				}
				gotAuthHeader = r.Header.Get("Authorization")
				if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
					t.Errorf("decode request: %v", err)
				}
				http.SetCookie(w, &http.Cookie{
					Name:     SessionCookieName,
					Value:    "payload.signature",
					Path:     "/",
					HttpOnly: true,
				})
				writeJSON(t, w, http.StatusOK, tc.body)
			})

			token, mustChange, err := c.LoginLocal(context.Background(), "ada@example.com", "s3cret")
			if err != nil {
				t.Fatalf("LoginLocal: %v", err)
			}
			if token != "payload.signature" {
				t.Errorf("token = %q, want payload.signature", token)
			}
			if mustChange != tc.mustChangePassword {
				t.Errorf("mustChangePassword = %v, want %v", mustChange, tc.mustChangePassword)
			}
			if gotReq.Email != "ada@example.com" || gotReq.Password != "s3cret" {
				t.Errorf("request body = %+v", gotReq)
			}
			// A stale token must not be sent with a login attempt.
			if gotAuthHeader != "" {
				t.Errorf("Authorization = %q, want none on login", gotAuthHeader)
			}
			// The client does not adopt the token implicitly.
			if c.Token() == token {
				t.Error("LoginLocal must not mutate the client token")
			}
		})
	}
}

func TestLoginLocalMissingCookie(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"status":"ok","mustChangePassword":false}`)
	})

	token, _, err := c.LoginLocal(context.Background(), "ada@example.com", "s3cret")
	if !errors.Is(err, ErrNoSessionCookie) {
		t.Fatalf("err = %v, want ErrNoSessionCookie", err)
	}
	if token != "" {
		t.Errorf("token = %q, want empty", token)
	}
}

func TestLoginLocalErrorCodes(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		code    string
		message string
		want    error
	}{
		{name: "rate limited", status: http.StatusTooManyRequests, code: CodeRateLimited, message: "too many attempts", want: ErrRateLimited},
		{name: "local auth disabled", status: http.StatusBadRequest, code: CodeLocalAuthDisabled, message: "local auth is disabled", want: ErrLocalAuthDisabled},
		{name: "invalid request", status: http.StatusBadRequest, code: CodeInvalidRequest, message: "email is required", want: ErrInvalidRequest},
		{name: "invalid credentials", status: http.StatusUnauthorized, code: CodeInvalidCredentials, message: "invalid email or password", want: ErrInvalidCredentials},
		{name: "account disabled", status: http.StatusForbidden, code: CodeAccountDisabled, message: "account is disabled", want: ErrAccountDisabled},
		{name: "account locked", status: http.StatusTooManyRequests, code: CodeAccountLocked, message: "account is locked", want: ErrAccountLocked},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				body, err := json.Marshal(jsonError{Error: tc.code, Message: tc.message})
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				writeJSON(t, w, tc.status, string(body))
			})

			_, _, err := c.LoginLocal(context.Background(), "ada@example.com", "nope")
			if err == nil {
				t.Fatal("LoginLocal: want error, got nil")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("errors.Is(%v, %v) = false", err, tc.want)
			}

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("errors.As(%v, *APIError) = false", err)
			}
			if apiErr.StatusCode != tc.status {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tc.status)
			}
			if apiErr.Code != tc.code {
				t.Errorf("Code = %q, want %q", apiErr.Code, tc.code)
			}
			if apiErr.Message != tc.message {
				t.Errorf("Message = %q, want %q", apiErr.Message, tc.message)
			}
		})
	}
}

// TestDefaultClientDoesNotFollowRedirects guards the reason the default client
// stops at the first redirect: the local-login session token arrives in a
// Set-Cookie header, and a followed redirect (there is no cookie jar) would
// drop it before LoginLocal could read it.
func TestDefaultClientDoesNotFollowRedirects(t *testing.T) {
	c, err := New("https://kw.example.com")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.httpc.CheckRedirect == nil {
		t.Fatal("CheckRedirect = nil, want a no-follow policy")
	}
	if got := c.httpc.CheckRedirect(nil, nil); !errors.Is(got, http.ErrUseLastResponse) {
		t.Errorf("CheckRedirect = %v, want http.ErrUseLastResponse", got)
	}

	// A redirected login is surfaced as an error rather than silently losing
	// the token on a second request.
	redirected := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/login/local" {
			t.Errorf("redirect was followed to %s", r.URL.Path)
			return
		}
		http.Redirect(w, r, "/", http.StatusFound)
	})
	if _, _, err := redirected.LoginLocal(context.Background(), "ada@example.com", "s3cret"); err == nil {
		t.Fatal("LoginLocal: want error on redirect, got nil")
	}
}

func TestMe(t *testing.T) {
	var gotCookie, gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if ck, err := r.Cookie(SessionCookieName); err == nil {
			gotCookie = ck.Value
		}
		writeJSON(t, w, http.StatusOK, `{
			"authenticated": true,
			"authEnabled": true,
			"email": "ada@example.com",
			"displayName": "Ada Lovelace",
			"role": "admin",
			"groups": ["platform"],
			"namespaces": ["demo", "ws-ada"],
			"personalNamespace": "ws-ada",
			"avatarURL": "https://example.com/a.png",
			"mustChangePassword": false
		}`)
	})

	me, err := c.Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if gotPath != "/auth/me" {
		t.Errorf("path = %q, want /auth/me", gotPath)
	}
	// The client sends both bearer and cookie; the cookie must be present.
	if gotCookie != "tok.sig" {
		t.Errorf("kw-session cookie = %q, want tok.sig", gotCookie)
	}
	if !me.Authenticated || !me.AuthEnabled {
		t.Errorf("Authenticated=%v AuthEnabled=%v", me.Authenticated, me.AuthEnabled)
	}
	if me.Email != "ada@example.com" || me.DisplayName != "Ada Lovelace" || me.Role != "admin" {
		t.Errorf("identity = %+v", me)
	}
	if me.PersonalNamespace != "ws-ada" || len(me.Namespaces) != 2 || me.Namespaces[1] != "ws-ada" {
		t.Errorf("namespaces = %v, personal = %q", me.Namespaces, me.PersonalNamespace)
	}
	if me.AvatarURL != "https://example.com/a.png" {
		t.Errorf("AvatarURL = %q", me.AvatarURL)
	}
}

func TestMeUnauthorized(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusUnauthorized, `{"error":"unauthorized","message":"no session"}`)
	})

	if _, err := c.Me(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Me error = %v, want ErrUnauthorized", err)
	}
}
