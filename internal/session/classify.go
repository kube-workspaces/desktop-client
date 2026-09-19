// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"io"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
	"github.com/kube-workspaces/desktop-client/internal/wsio"
)

// Retry is what a supervisor should do about a failed connection attempt or a
// session that dropped.
//
// Getting this classification right matters more than the retry schedule
// itself: retrying a permanent failure hides the real problem from the user
// and, in the case of a contended single-seat console, actively harms someone
// else's session.
type Retry int

const (
	// RetryNever means the failure is permanent for this session: the token is
	// gone, access was revoked, or the workspace simply cannot serve a
	// display. Reconnecting cannot fix any of those, so the error belongs in
	// front of the user instead.
	RetryNever Retry = iota

	// RetryBackoff means the failure is transient: reconnect on the capped
	// exponential backoff schedule.
	RetryBackoff

	// RetrySlow means the far end is healthy but unwilling right now, so the
	// client should poll gently and indefinitely rather than climb a backoff
	// curve. See [Classify] for why a busy VNC display is handled this way.
	RetrySlow
)

// String implements fmt.Stringer.
func (r Retry) String() string {
	switch r {
	case RetryNever:
		return "never"
	case RetryBackoff:
		return "backoff"
	case RetrySlow:
		return "slow"
	default:
		return "unknown"
	}
}

// Classify decides how to react to an error from dialling the VNC bridge or
// from a running RFB stream.
//
// The table, and the reasoning behind each row:
//
//   - nil — a clean end of stream. The server closed the WebSocket without an
//     error, which happens on a VM reboot, a virt-handler restart, or an API
//     rollout. It is a disconnect, not a shutdown, so it is retried. A session
//     the *user* ended never reaches here; the supervisor checks its context
//     first.
//   - context.Canceled, context.DeadlineExceeded — we are the ones stopping.
//     Never retried.
//   - [kwclient.ErrSessionInUse] (HTTP 409) — the KubeVirt VNC console is
//     single-seat and, unlike the serial console and SSH, the API exposes no
//     VNC takeover endpoint. A tight retry loop here would not win the slot,
//     it would just race the other client's reconnects and hammer the API for
//     as long as somebody else is using the display. A slow fixed poll
//     attaches within a few seconds of the other client disconnecting, which
//     is the behaviour a user actually wants, at a cost of one request every
//     several seconds.
//   - [kwclient.ErrControllerPresent] (HTTP 409 on the shared display) — a
//     controller attach found the control lease still held, typically the
//     moments between a takeover's REST acquire and the old holder's fence
//     releasing. It frees itself within seconds, so it polls like a busy
//     display.
//   - [kwclient.ErrParticipantNotFound] (HTTP 404 on the shared display) —
//     the membership this session is bound to is gone. Re-dialling cannot
//     revive it; only a fresh join can, and that is the caller's decision to
//     make, not the supervisor's.
//   - [kwclient.ErrRateLimited] (429) — the server has explicitly asked for
//     less traffic. The slow fixed poll is the honest response; a backoff
//     curve that starts at one second is not.
//   - [kwclient.ErrUnauthorized] (401), [kwclient.ErrForbidden] (403) and the
//     account-state sentinels — the token expired or access was revoked. No
//     amount of retrying mints a new token, and a client that silently retries
//     a 401 is a client that never prompts for re-login.
//   - [kwclient.ErrNotVM] (400 on the bridge) — a permanent property of the
//     workspace: container workspaces have no display at all.
//   - [kwclient.ErrNotFound] (404) — retried, which is the one non-obvious
//     row. On this bridge a 404 overwhelmingly means "the VMI is not running
//     right now" (a rebooting or just-started VM), not "no such workspace":
//     the workspace name is resolved over REST before the bridge is ever
//     dialled, so a typo has already failed by this point.
//   - [kwclient.ErrUnavailable] (503) — maintenance mode, or the API could not
//     reach the VM's network. Both clear on their own.
//   - [kwclient.ErrBadGateway] (502) — an upstream hop (virt-api) was
//     restarting.
//   - io.EOF, io.ErrUnexpectedEOF, [rfb.ErrClosed] — the stream ended
//     underneath us. This is also where a clean WebSocket close with a reason
//     lands, including the "taken over by another user" message the platform
//     sends; retrying is still correct, because the very next dial returns 409
//     and the session drops into the slow poll on its own.
//   - anything else — transport errors (net.Error, TLS failures, a DNS
//     hiccup) and protocol desynchronisation. Retried: a fresh handshake is
//     precisely the cure for a desynchronised stream, and an unknown failure
//     that turns out to be permanent still surfaces through the state
//     callback on every attempt.
func Classify(err error) Retry {
	switch {
	case err == nil:
		return RetryBackoff

	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return RetryNever

	case errors.Is(err, kwclient.ErrSessionInUse),
		errors.Is(err, kwclient.ErrControllerPresent),
		errors.Is(err, kwclient.ErrRateLimited):
		return RetrySlow

	case errors.Is(err, kwclient.ErrParticipantNotFound):
		return RetryNever

	case errors.Is(err, kwclient.ErrUnauthorized),
		errors.Is(err, kwclient.ErrForbidden),
		errors.Is(err, kwclient.ErrInvalidToken),
		errors.Is(err, kwclient.ErrAccountDisabled),
		errors.Is(err, kwclient.ErrAccountLocked),
		errors.Is(err, kwclient.ErrInvalidCredentials),
		errors.Is(err, kwclient.ErrLocalAuthDisabled):
		return RetryNever

	case errors.Is(err, kwclient.ErrNotVM):
		return RetryNever

	case errors.Is(err, kwclient.ErrNotFound),
		errors.Is(err, kwclient.ErrUnavailable),
		errors.Is(err, kwclient.ErrBadGateway):
		return RetryBackoff

	case errors.Is(err, io.EOF),
		errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, rfb.ErrClosed):
		return RetryBackoff

	default:
		return RetryBackoff
	}
}

// Fatal reports whether err ends the session for good. It is the negation of
// "worth retrying", named for the way call sites read.
func Fatal(err error) bool { return Classify(err) == RetryNever }

// CloseReason returns the text the server attached to a WebSocket close frame,
// if the error carries one.
//
// The API uses the close reason to explain itself — "taken over by another
// user", "workspace stopped" — and that sentence is far more useful to a user
// than the transport error wrapping it.
//
// Note the limitation: [wsio] flattens a *normal* closure into io.EOF with the
// reason appended to the message, so the structured reason only survives for
// abnormal close codes. Callers should fall back to err.Error() when ok is
// false.
func CloseReason(err error) (code int, text string, ok bool) {
	return wsio.CloseReason(err)
}
