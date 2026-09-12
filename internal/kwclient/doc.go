// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package kwclient is a client for the kube-workspaces REST API and its
// WebSocket bridges (VNC, exec and SSH).
//
// # Base URL
//
// The native desktop client talks to the API server directly, so the base URL
// is the API origin only (for example https://kw.example.com). Resources live
// under /v1/... and /auth/.... The /api prefix that appears in browser
// dev-tools is a frontend rewrite artifact and must not be used here.
//
// # Authentication
//
// The session token is not a JWT. It is
//
//	base64url(jsonPayload) "." base64url(HMAC-SHA256)
//
// so the payload can be decoded locally with [ParseToken] to learn the email,
// role and expiry without a network round trip. Only the server can verify the
// signature; [ParseToken] deliberately does not attempt to.
//
// Transport of the token differs per endpoint:
//
//   - /v1/* accepts Authorization: Bearer <token>.
//   - /auth/me is cookie-only and ignores the Authorization header.
//
// The client therefore always sends both the bearer header and the
// kw-session cookie, which is accepted everywhere.
//
// # Errors
//
// Every non-2xx response is turned into an [*APIError] that wraps one of the
// package sentinels (for example [ErrSessionInUse]), so callers can branch with
// errors.Is while still having the status code, server error code and message
// available for display.
package kwclient
