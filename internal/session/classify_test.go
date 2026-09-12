// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Retry
	}{
		// --- clean ends -----------------------------------------------------
		{
			name: "clean end of stream",
			err:  nil,
			want: RetryBackoff,
		},
		{
			name: "eof",
			err:  io.EOF,
			want: RetryBackoff,
		},
		{
			name: "unexpected eof mid-message",
			err:  io.ErrUnexpectedEOF,
			want: RetryBackoff,
		},
		{
			name: "clean websocket close flattened to eof with a reason",
			err:  fmt.Errorf("%w: taken over by another user", io.EOF),
			want: RetryBackoff,
		},
		{
			name: "rfb connection closed",
			err:  rfb.ErrClosed,
			want: RetryBackoff,
		},

		// --- our own shutdown ----------------------------------------------
		{
			name: "context cancelled",
			err:  context.Canceled,
			want: RetryNever,
		},
		{
			name: "context deadline exceeded",
			err:  context.DeadlineExceeded,
			want: RetryNever,
		},

		// --- single-seat display -------------------------------------------
		{
			name: "session in use",
			err:  kwclient.ErrSessionInUse,
			want: RetrySlow,
		},
		{
			name: "rate limited",
			err:  kwclient.ErrRateLimited,
			want: RetrySlow,
		},

		// --- permanent ------------------------------------------------------
		{
			name: "unauthorized",
			err:  kwclient.ErrUnauthorized,
			want: RetryNever,
		},
		{
			name: "forbidden",
			err:  kwclient.ErrForbidden,
			want: RetryNever,
		},
		{
			name: "invalid token",
			err:  kwclient.ErrInvalidToken,
			want: RetryNever,
		},
		{
			name: "account disabled",
			err:  kwclient.ErrAccountDisabled,
			want: RetryNever,
		},
		{
			name: "account locked",
			err:  kwclient.ErrAccountLocked,
			want: RetryNever,
		},
		{
			name: "invalid credentials",
			err:  kwclient.ErrInvalidCredentials,
			want: RetryNever,
		},
		{
			name: "local auth disabled",
			err:  kwclient.ErrLocalAuthDisabled,
			want: RetryNever,
		},
		{
			name: "not a vm workspace",
			err:  kwclient.ErrNotVM,
			want: RetryNever,
		},

		// --- transient server side ------------------------------------------
		{
			name: "service unavailable",
			err:  kwclient.ErrUnavailable,
			want: RetryBackoff,
		},
		{
			name: "bad gateway",
			err:  kwclient.ErrBadGateway,
			want: RetryBackoff,
		},
		{
			// A 404 from the bridge means the VMI is not running yet, not that
			// the workspace is gone: the name was resolved over REST already.
			name: "not found while the vm is starting",
			err:  kwclient.ErrNotFound,
			want: RetryBackoff,
		},

		// --- transport ------------------------------------------------------
		{
			name: "connection refused",
			err:  &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
			want: RetryBackoff,
		},
		{
			name: "connection reset",
			err:  &net.OpError{Op: "read", Err: syscall.ECONNRESET},
			want: RetryBackoff,
		},
		{
			name: "dns failure",
			err:  &net.DNSError{Err: "no such host", IsNotFound: true},
			want: RetryBackoff,
		},
		{
			name: "use of closed network connection",
			err:  net.ErrClosed,
			want: RetryBackoff,
		},
		{
			name: "abnormal websocket close",
			err:  &websocket.CloseError{Code: websocket.CloseAbnormalClosure},
			want: RetryBackoff,
		},
		{
			name: "websocket going away with a reason",
			err:  &websocket.CloseError{Code: websocket.CloseGoingAway, Text: "workspace stopped"},
			want: RetryBackoff,
		},

		// --- protocol -------------------------------------------------------
		{
			// A desynchronised stream is unrecoverable on this connection but
			// perfectly fixable by a fresh handshake.
			name: "rfb protocol desynchronisation",
			err:  errors.New("rfb: unknown server message type 42"),
			want: RetryBackoff,
		},
		{
			name: "unrecognised error defaults to retrying",
			err:  errors.New("something nobody has seen before"),
			want: RetryBackoff,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.err); got != tt.want {
				t.Errorf("Classify(%v) = %v, want %v", tt.err, got, tt.want)
			}
			if got, want := Fatal(tt.err), tt.want == RetryNever; got != want {
				t.Errorf("Fatal(%v) = %v, want %v", tt.err, got, want)
			}
		})
	}
}

// The session layer wraps everything it returns, so classification has to see
// through however many layers of fmt.Errorf are in the way. This mirrors what
// Dial actually produces: "dial vnc bridge for ns/name: %w" around an
// *kwclient.APIError which itself unwraps to a sentinel.
func TestClassifySeesThroughWrapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Retry
	}{
		{
			name: "single wrap",
			err:  fmt.Errorf("dial vnc bridge for ns/name: %w", kwclient.ErrSessionInUse),
			want: RetrySlow,
		},
		{
			name: "double wrap",
			err: fmt.Errorf("session: %w",
				fmt.Errorf("dial vnc bridge for ns/name: %w", kwclient.ErrUnauthorized)),
			want: RetryNever,
		},
		{
			name: "api error carrying the status and body",
			err: fmt.Errorf("dial vnc bridge for ns/name: %w",
				apiError(409, "VNC display is in use")),
			want: RetrySlow,
		},
		{
			name: "api error for a container workspace",
			err: fmt.Errorf("dial vnc bridge for ns/name: %w",
				apiError(400, "workspace is not a vm")),
			want: RetryNever,
		},
		{
			name: "api error in maintenance mode",
			err: fmt.Errorf("dial vnc bridge for ns/name: %w",
				apiError(503, "maintenance mode")),
			want: RetryBackoff,
		},
		{
			name: "handshake failure over a dead transport",
			err:  fmt.Errorf("rfb handshake with ns/name: %w", fmt.Errorf("rfb: read protocol version: %w", io.EOF)),
			want: RetryBackoff,
		},
		{
			name: "joined errors keep the fatal one",
			err:  errors.Join(io.EOF, kwclient.ErrForbidden),
			want: RetryNever,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.err); got != tt.want {
				t.Errorf("Classify(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestRetryString(t *testing.T) {
	tests := []struct {
		retry Retry
		want  string
	}{
		{RetryNever, "never"},
		{RetryBackoff, "backoff"},
		{RetrySlow, "slow"},
		{Retry(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.retry.String(); got != tt.want {
			t.Errorf("Retry(%d).String() = %q, want %q", tt.retry, got, tt.want)
		}
	}
}

func TestCloseReason(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantText string
		wantOK   bool
	}{
		{
			name:     "abnormal close with a reason",
			err:      &websocket.CloseError{Code: websocket.CloseGoingAway, Text: "taken over by another user"},
			wantCode: websocket.CloseGoingAway,
			wantText: "taken over by another user",
			wantOK:   true,
		},
		{
			name:     "wrapped close error",
			err:      fmt.Errorf("read: %w", &websocket.CloseError{Code: 4000, Text: "workspace stopped"}),
			wantCode: 4000,
			wantText: "workspace stopped",
			wantOK:   true,
		},
		{
			name: "plain eof carries nothing structured",
			err:  io.EOF,
		},
		{
			name: "nil",
			err:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, text, ok := CloseReason(tt.err)
			if ok != tt.wantOK || code != tt.wantCode || text != tt.wantText {
				t.Errorf("CloseReason(%v) = (%d, %q, %v), want (%d, %q, %v)",
					tt.err, code, text, ok, tt.wantCode, tt.wantText, tt.wantOK)
			}
		})
	}
}

// apiError reproduces the shape of *kwclient.APIError: a rich error carrying
// the status and the server's message, which unwraps to a package sentinel.
// Classification has to work through that indirection, not just off a bare
// sentinel value.
type apiErr struct {
	status int
	msg    string
	kind   error
}

func (e *apiErr) Error() string { return fmt.Sprintf("kwclient: http %d: %s", e.status, e.msg) }
func (e *apiErr) Unwrap() error { return e.kind }

// apiError maps a status the way kwclient's WebSocket bridges do, where 400
// means "not a VM" rather than "bad request".
func apiError(status int, body string) error {
	var kind error
	switch status {
	case 400:
		kind = kwclient.ErrNotVM
	case 401:
		kind = kwclient.ErrUnauthorized
	case 403:
		kind = kwclient.ErrForbidden
	case 404:
		kind = kwclient.ErrNotFound
	case 409:
		kind = kwclient.ErrSessionInUse
	case 429:
		kind = kwclient.ErrRateLimited
	case 502:
		kind = kwclient.ErrBadGateway
	case 503:
		kind = kwclient.ErrUnavailable
	}
	return &apiErr{status: status, msg: body, kind: kind}
}
