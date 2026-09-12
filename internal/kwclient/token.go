// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TokenClaims is the payload of a kube-workspaces session token.
//
// The token is not a JWT: it is base64url(payload) "." base64url(HMAC-SHA256)
// with no header segment, so there is no "alg" to inspect and the signature can
// only be checked by the server, which holds the secret.
type TokenClaims struct {
	// Email identifies the user and is the API's primary subject.
	Email string `json:"email"`
	// DisplayName is the human-friendly name, when the IdP supplied one.
	DisplayName string `json:"displayName,omitempty"`
	// Role is the platform role, e.g. "admin" or "user".
	Role string `json:"role"`
	// Groups are the IdP groups the user belongs to.
	Groups []string `json:"groups,omitempty"`
	// IssuedAt is the issue time as a Unix timestamp in seconds.
	IssuedAt int64 `json:"iat"`
	// Expires is the expiry time as a Unix timestamp in seconds.
	Expires int64 `json:"exp"`
}

// ParseToken decodes the payload of a session token without contacting the
// server and without verifying the signature.
//
// Use it to show who is logged in and to pre-empt an expired session; never use
// it to make an authorization decision, since an unverified payload is
// attacker-controlled. The server re-validates every request anyway.
func ParseToken(token string) (*TokenClaims, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("kwclient: parse token: empty token: %w", ErrInvalidToken)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, fmt.Errorf("kwclient: parse token: expected 2 dot-separated parts, got %d: %w",
			len(parts), ErrInvalidToken)
	}
	if parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("kwclient: parse token: empty payload or signature part: %w", ErrInvalidToken)
	}

	raw, err := decodeBase64URL(parts[0])
	if err != nil {
		return nil, fmt.Errorf("kwclient: parse token: decode payload: %w", ErrInvalidToken)
	}

	var claims TokenClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, fmt.Errorf("kwclient: parse token: decode payload JSON: %v: %w", err, ErrInvalidToken)
	}
	return &claims, nil
}

// decodeBase64URL decodes unpadded base64url, falling back to the padded form.
//
// The server encodes with base64.RawURLEncoding; the fallback only guards
// against a future change in padding behavior.
func decodeBase64URL(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// ExpiresAt returns the expiry as a time.Time. The zero time means the token
// carries no expiry claim.
func (c *TokenClaims) ExpiresAt() time.Time {
	if c == nil || c.Expires == 0 {
		return time.Time{}
	}
	return time.Unix(c.Expires, 0).UTC()
}

// IssuedAtTime returns the issue time as a time.Time, or the zero time when the
// claim is absent.
func (c *TokenClaims) IssuedAtTime() time.Time {
	if c == nil || c.IssuedAt == 0 {
		return time.Time{}
	}
	return time.Unix(c.IssuedAt, 0).UTC()
}

// Expired reports whether the token's expiry has passed. A token without an
// expiry claim never expires client-side.
func (c *TokenClaims) Expired() bool {
	return c.ExpiredAt(time.Now())
}

// ExpiredAt reports whether the token is expired at the given instant. It
// exists so callers (and tests) can reason about a clock they control.
func (c *TokenClaims) ExpiredAt(now time.Time) bool {
	exp := c.ExpiresAt()
	if exp.IsZero() {
		return false
	}
	return !now.Before(exp)
}

// ExpiresWithin reports whether the token expires within d from now, which is a
// cue to refresh the session before starting a long-lived console stream.
func (c *TokenClaims) ExpiresWithin(d time.Duration) bool {
	return c.ExpiredAt(time.Now().Add(d))
}
