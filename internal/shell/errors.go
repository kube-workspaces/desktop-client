// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

// Describe turns an error into the sentence the user sees.
//
// Two rules. First, say what happened and, where there is one, what to do
// about it — "this account is locked" is only useful with "try again later"
// attached. Second, never show a Go error verbatim if it can be helped: a
// dialog reading `kwclient: POST /auth/login/local: http 401
// (invalid_credentials): invalid credentials` tells the user nothing they
// could act on and tells them it three times.
//
// The fallback does show the error, because an unrecognised failure the user
// can quote to an administrator is far better than "something went wrong".
func Describe(err error) string {
	if err == nil {
		return ""
	}

	switch {
	case errors.Is(err, context.Canceled):
		return "Cancelled."
	case errors.Is(err, context.DeadlineExceeded):
		return "The server took too long to answer."

	// Authentication, in the order a user meets it.
	case errors.Is(err, kwclient.ErrInvalidCredentials):
		return "That email address and password were not accepted."
	case errors.Is(err, kwclient.ErrAccountLocked):
		return "This account is locked after too many failed sign-ins. Wait a few minutes and try again."
	case errors.Is(err, kwclient.ErrAccountDisabled):
		return "This account is disabled. Ask an administrator to re-enable it."
	case errors.Is(err, kwclient.ErrRateLimited):
		return "Too many attempts. Wait a moment and try again."
	case errors.Is(err, kwclient.ErrLocalAuthDisabled):
		return "This instance does not accept a password here. Use Sign in with browser."
	case errors.Is(err, kwclient.ErrNoSessionCookie):
		return "The server accepted the sign-in but issued no session. Report this to an administrator."
	case errors.Is(err, kwclient.ErrInvalidRequest):
		return "The server rejected the request. Check the email address."

	// The browser flow.
	case errors.Is(err, kwclient.ErrBrowserLoginTimeout):
		return "The browser sign-in was not completed in time. Try again."
	case errors.Is(err, kwclient.ErrStateMismatch):
		return "The browser sign-in could not be verified and was abandoned. Try again."
	case errors.Is(err, kwclient.ErrAuthorizationDenied):
		return "The identity provider refused the sign-in: " + tail(err)
	case errors.Is(err, kwclient.ErrNativeAuthUnsupported):
		return "This instance is running an API build without browser sign-in."

	// The session.
	case errors.Is(err, kwclient.ErrUnauthorized):
		return "Your session has expired. Please sign in again."
	case errors.Is(err, kwclient.ErrForbidden):
		return "You do not have access to that. Opening a display needs the editor or admin role."
	case errors.Is(err, kwclient.ErrNotFound):
		return "That workspace no longer exists."
	case errors.Is(err, kwclient.ErrSessionInUse):
		return "Another client is using this workspace's display. There is no way to take it over; wait for it to be released."
	case errors.Is(err, kwclient.ErrNotVM):
		return "This workspace has no display. Open it in a browser instead."
	case errors.Is(err, kwclient.ErrUnavailable):
		return "The instance is unavailable. It may be in maintenance."
	case errors.Is(err, kwclient.ErrBadGateway):
		return "The instance could not reach the workspace."
	}

	if msg, ok := describeTransport(err); ok {
		return msg
	}
	return tail(err)
}

// describeTransport names the failures that happen before any HTTP status
// exists: DNS, TCP and TLS. They are the ones a user typing a server URL for
// the first time will actually hit, and Go's wrapped forms of them are
// unreadable.
func describeTransport(err error) (string, bool) {
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return "The server's TLS certificate was not accepted: " + certificateReason(certErr.Err) +
			" If this is a development instance with a self-signed certificate, enable \"Ignore TLS certificate errors\".", true
	}
	var recordErr tls.RecordHeaderError
	if errors.As(err, &recordErr) {
		return "That address did not answer with TLS. Check whether it should be http:// rather than https://.", true
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "That host name could not be resolved (" + dnsErr.Name + "). Check the address.", true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "The instance did not answer in time. Check the address and your network.", true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "Could not reach the instance: " + opErr.Err.Error() + ".", true
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return "Could not reach the instance: " + tail(urlErr.Err), true
	}
	return "", false
}

// certificateReason names the three ways certificate verification usually
// fails, because the distinction changes what the user should do.
func certificateReason(err error) string {
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return "it was not issued by a trusted authority."
	}
	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return "it is not valid for " + hostname.Host + "."
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) {
		return strings.TrimSuffix(invalid.Error(), ".") + "."
	}
	return tail(err)
}

// tail strips the layered "package: operation: " prefixes off an error and
// returns the last, most specific clause with a full stop on the end.
//
// It is deliberately crude. The alternative is a typed error for every
// failure in the client, which is the right answer for a library and overkill
// for the last line of a fallback path.
func tail(err error) string {
	msg := strings.TrimSpace(err.Error())
	msg = strings.TrimPrefix(msg, "kwclient: ")
	if i := strings.LastIndex(msg, ": "); i >= 0 && i < len(msg)-2 {
		if candidate := strings.TrimSpace(msg[i+2:]); len(candidate) > 8 {
			msg = candidate
		}
	}
	if msg == "" {
		return "The request failed."
	}
	// Capitalise, since it is being shown as a sentence.
	runes := []rune(msg)
	if runes[0] >= 'a' && runes[0] <= 'z' {
		runes[0] -= 'a' - 'A'
	}
	msg = string(runes)
	if !strings.HasSuffix(msg, ".") && !strings.HasSuffix(msg, "!") && !strings.HasSuffix(msg, "?") {
		msg += "."
	}
	return msg
}
