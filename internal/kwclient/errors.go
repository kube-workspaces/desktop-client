// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Sentinel errors returned (wrapped in an [*APIError]) by the client. Use
// errors.Is to test for them; the concrete error always carries the HTTP
// status code and the server's message as well.
var (
	// ErrUnauthorized means the request had no valid session (HTTP 401).
	ErrUnauthorized = errors.New("unauthorized")
	// ErrForbidden means the session is valid but lacks access (HTTP 403).
	ErrForbidden = errors.New("forbidden")
	// ErrNotFound means the addressed object does not exist (HTTP 404).
	ErrNotFound = errors.New("not found")
	// ErrSessionInUse means a single-seat console/SSH/VNC session is already
	// held by somebody else (HTTP 409). Call the matching takeover endpoint to
	// steal it.
	ErrSessionInUse = errors.New("session in use")
	// ErrNotVM means the endpoint is only valid for spec.type: vm workspaces
	// (HTTP 400 on the console/ssh/vnc bridges).
	ErrNotVM = errors.New("not a vm workspace")
	// ErrUnavailable means the backend is temporarily unavailable (HTTP 503):
	// maintenance mode, or the VM network could not be reached.
	ErrUnavailable = errors.New("service unavailable")
	// ErrBadGateway means an upstream hop failed (HTTP 502).
	ErrBadGateway = errors.New("bad gateway")
	// ErrRateLimited means the caller was throttled (HTTP 429).
	ErrRateLimited = errors.New("rate limited")
	// ErrAccountDisabled means the local account exists but is disabled.
	ErrAccountDisabled = errors.New("account disabled")
	// ErrAccountLocked means the local account is temporarily locked after too
	// many failed logins.
	ErrAccountLocked = errors.New("account locked")
	// ErrInvalidCredentials means the email/password pair was rejected.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrLocalAuthDisabled means local (password) auth is turned off on this
	// deployment; the user must use the OIDC issuer instead.
	ErrLocalAuthDisabled = errors.New("local auth disabled")
	// ErrInvalidRequest means the server rejected the request payload
	// (error code "invalid_request").
	ErrInvalidRequest = errors.New("invalid request")
)

// Client-side sentinels, i.e. failures detected without (or after) a server
// round trip.
var (
	// ErrInvalidToken means a session token could not be decoded by
	// [ParseToken].
	ErrInvalidToken = errors.New("invalid session token")
	// ErrNoSessionCookie means a login call succeeded but the response carried
	// no kw-session cookie, so there is no token to keep.
	ErrNoSessionCookie = errors.New("no session cookie in response")
)

// Server error codes as emitted by the auth endpoints in
// {"error":"<code>","message":"<msg>"} bodies.
const (
	CodeRateLimited        = "rate_limited"
	CodeLocalAuthDisabled  = "local_auth_disabled"
	CodeInvalidRequest     = "invalid_request"
	CodeInvalidCredentials = "invalid_credentials"
	CodeAccountDisabled    = "account_disabled"
	CodeAccountLocked      = "account_locked"
)

// maxErrorBody bounds how much of an error response we read. Error bodies are
// tiny; the limit only exists so a misbehaving proxy cannot stream us out of
// memory.
const maxErrorBody = 64 << 10

// APIError is a non-2xx response from the kube-workspaces API.
//
// It unwraps to one of the package sentinels so that
// errors.Is(err, ErrSessionInUse) works, while Status/Code/Message stay
// available for the UI.
type APIError struct {
	// Op describes the request, e.g. "GET /v1/workspaces".
	Op string
	// StatusCode is the HTTP status code of the response.
	StatusCode int
	// Code is the machine-readable error code from a JSON error body, if any.
	Code string
	// Message is the human-readable message from the body, if any.
	Message string
	// Body is the raw (truncated) response body, useful for the text/plain
	// errors returned by the WebSocket bridges.
	Body string

	// kind is the sentinel this error unwraps to. It may be nil for statuses
	// with no dedicated sentinel.
	kind error
}

// Error implements the error interface.
func (e *APIError) Error() string {
	detail := e.Message
	if detail == "" && e.kind != nil {
		detail = e.kind.Error()
	}
	if detail == "" {
		detail = e.Body
	}
	var b strings.Builder
	b.WriteString("kwclient: ")
	if e.Op != "" {
		b.WriteString(e.Op)
		b.WriteString(": ")
	}
	fmt.Fprintf(&b, "http %d", e.StatusCode)
	if e.Code != "" {
		fmt.Fprintf(&b, " (%s)", e.Code)
	}
	if detail != "" {
		b.WriteString(": ")
		b.WriteString(detail)
	}
	return b.String()
}

// Unwrap returns the sentinel error this failure maps to, if any.
func (e *APIError) Unwrap() error { return e.kind }

// Temporary reports whether retrying the request later may succeed.
func (e *APIError) Temporary() bool {
	switch e.StatusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// jsonError is the shape of the API's structured error bodies.
type jsonError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// sentinelForCode maps a server error code to a sentinel. Codes win over
// status codes because they are more specific: account_locked and rate_limited
// share HTTP 429, for instance.
func sentinelForCode(code string) error {
	switch code {
	case CodeRateLimited:
		return ErrRateLimited
	case CodeLocalAuthDisabled:
		return ErrLocalAuthDisabled
	case CodeInvalidRequest:
		return ErrInvalidRequest
	case CodeInvalidCredentials:
		return ErrInvalidCredentials
	case CodeAccountDisabled:
		return ErrAccountDisabled
	case CodeAccountLocked:
		return ErrAccountLocked
	default:
		return nil
	}
}

// sentinelForStatus maps an HTTP status to a sentinel for ordinary REST calls.
//
// Note that 400 is deliberately not mapped here: on REST endpoints it means
// "bad request", only on the WebSocket bridges does it mean "not a VM".
func sentinelForStatus(status int) error {
	switch status {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusConflict:
		return ErrSessionInUse
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case http.StatusBadGateway:
		return ErrBadGateway
	case http.StatusServiceUnavailable:
		return ErrUnavailable
	default:
		return nil
	}
}

// sentinelForWSStatus maps the HTTP status of a failed WebSocket handshake.
//
// The console/exec/ssh/vnc bridges reject before the upgrade with a plain HTTP
// status (never a close frame), and 400 specifically means the workspace is not
// a VM.
func sentinelForWSStatus(status int) error {
	if status == http.StatusBadRequest {
		return ErrNotVM
	}
	return sentinelForStatus(status)
}

// newAPIError builds an [*APIError] from a response whose body has not been
// read yet. It never returns nil. The caller keeps ownership of resp.Body.
func newAPIError(op string, resp *http.Response, sentinel func(int) error) *APIError {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	return apiErrorFromBody(op, resp.StatusCode, raw, sentinel)
}

// apiErrorFromBody decodes the three body shapes the API uses for errors:
//
//   - {"error":"code","message":"msg"} from the auth endpoints,
//   - a bare JSON string such as "workspace ns/name not found" from the
//     workspace endpoints,
//   - text/plain from the WebSocket bridges.
func apiErrorFromBody(op string, status int, raw []byte, sentinel func(int) error) *APIError {
	body := strings.TrimSpace(string(raw))
	e := &APIError{Op: op, StatusCode: status, Body: body}

	switch {
	case strings.HasPrefix(body, "{"):
		var je jsonError
		if err := json.Unmarshal([]byte(body), &je); err == nil {
			e.Code = je.Error
			e.Message = je.Message
			// The WebSocket bridges return {"error":"<human message>"} with no
			// message field and no code vocabulary, so an unrecognized code
			// with no message is treated as the message itself.
			if e.Message == "" && sentinelForCode(e.Code) == nil {
				e.Message = e.Code
				e.Code = ""
			}
		}
	case strings.HasPrefix(body, `"`):
		var s string
		if err := json.Unmarshal([]byte(body), &s); err == nil {
			e.Message = s
		}
	default:
		e.Message = body
	}

	if k := sentinelForCode(e.Code); k != nil {
		e.kind = k
	} else if sentinel != nil {
		e.kind = sentinel(status)
	}
	return e
}
