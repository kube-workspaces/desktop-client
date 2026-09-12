// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// makeToken builds a token in the server's format: base64url(payload) "."
// base64url(signature). The signature is opaque to the client, so any bytes
// will do.
func makeToken(t *testing.T, claims TokenClaims) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString([]byte("not-a-real-hmac"))
}

func TestParseToken(t *testing.T) {
	now := time.Now()
	valid := TokenClaims{
		Email:       "ada@example.com",
		DisplayName: "Ada Lovelace",
		Role:        "admin",
		Groups:      []string{"platform", "eng"},
		IssuedAt:    now.Add(-time.Hour).Unix(),
		Expires:     now.Add(time.Hour).Unix(),
	}
	expired := TokenClaims{
		Email:    "grace@example.com",
		Role:     "user",
		IssuedAt: now.Add(-48 * time.Hour).Unix(),
		Expires:  now.Add(-24 * time.Hour).Unix(),
	}

	tests := []struct {
		name        string
		token       string
		wantErr     bool
		wantEmail   string
		wantRole    string
		wantGroups  []string
		wantExpired bool
	}{
		{
			name:        "valid",
			token:       makeToken(t, valid),
			wantEmail:   "ada@example.com",
			wantRole:    "admin",
			wantGroups:  []string{"platform", "eng"},
			wantExpired: false,
		},
		{
			name:        "expired",
			token:       makeToken(t, expired),
			wantEmail:   "grace@example.com",
			wantRole:    "user",
			wantExpired: true,
		},
		{
			name:      "no expiry claim never expires",
			token:     makeToken(t, TokenClaims{Email: "n@example.com", Role: "user"}),
			wantEmail: "n@example.com",
			wantRole:  "user",
		},
		{
			name:    "missing signature part",
			token:   base64.RawURLEncoding.EncodeToString([]byte(`{"email":"a@b.c"}`)),
			wantErr: true,
		},
		{
			name:    "empty signature part",
			token:   base64.RawURLEncoding.EncodeToString([]byte(`{"email":"a@b.c"}`)) + ".",
			wantErr: true,
		},
		{
			name:    "three parts (jwt shaped)",
			token:   "aGVhZGVy.cGF5bG9hZA.c2ln",
			wantErr: true,
		},
		{
			name:    "malformed base64 payload",
			token:   "!!!not-base64!!!.c2ln",
			wantErr: true,
		},
		{
			name:    "payload is not json",
			token:   base64.RawURLEncoding.EncodeToString([]byte("hello")) + ".c2ln",
			wantErr: true,
		},
		{
			name:    "empty token",
			token:   "   ",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := ParseToken(tc.token)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseToken(%q) = %+v, want error", tc.token, claims)
				}
				if !errors.Is(err, ErrInvalidToken) {
					t.Fatalf("errors.Is(%v, ErrInvalidToken) = false", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseToken: %v", err)
			}
			if claims.Email != tc.wantEmail {
				t.Errorf("Email = %q, want %q", claims.Email, tc.wantEmail)
			}
			if claims.Role != tc.wantRole {
				t.Errorf("Role = %q, want %q", claims.Role, tc.wantRole)
			}
			if len(claims.Groups) != len(tc.wantGroups) {
				t.Errorf("Groups = %v, want %v", claims.Groups, tc.wantGroups)
			}
			if got := claims.Expired(); got != tc.wantExpired {
				t.Errorf("Expired() = %v, want %v", got, tc.wantExpired)
			}
		})
	}
}

func TestTokenClaimsTimes(t *testing.T) {
	iat := time.Now().Add(-time.Hour).Truncate(time.Second)
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	claims, err := ParseToken(makeToken(t, TokenClaims{
		Email:    "ada@example.com",
		IssuedAt: iat.Unix(),
		Expires:  exp.Unix(),
	}))
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}

	if got := claims.ExpiresAt(); !got.Equal(exp) {
		t.Errorf("ExpiresAt() = %s, want %s", got, exp)
	}
	if got := claims.IssuedAtTime(); !got.Equal(iat) {
		t.Errorf("IssuedAtTime() = %s, want %s", got, iat)
	}
	if claims.ExpiresWithin(30 * time.Minute) {
		t.Error("ExpiresWithin(30m) = true, want false")
	}
	if !claims.ExpiresWithin(2 * time.Hour) {
		t.Error("ExpiresWithin(2h) = false, want true")
	}
	if !claims.ExpiredAt(exp) {
		t.Error("ExpiredAt(exp) = false, want true (exp is inclusive)")
	}
}

func TestTokenClaimsNilSafe(t *testing.T) {
	var claims *TokenClaims
	if !claims.ExpiresAt().IsZero() || !claims.IssuedAtTime().IsZero() {
		t.Error("nil claims should yield zero times")
	}
	if claims.Expired() {
		t.Error("nil claims should not report expired")
	}
}

func TestClientClaims(t *testing.T) {
	token := makeToken(t, TokenClaims{Email: "ada@example.com", Role: "admin"})
	c, err := New("https://kw.example.com", WithToken(token))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	claims, err := c.Claims()
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if claims.Email != "ada@example.com" {
		t.Errorf("Email = %q", claims.Email)
	}

	c.SetToken("")
	if _, err := c.Claims(); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("Claims() with no token = %v, want ErrInvalidToken", err)
	}
}
