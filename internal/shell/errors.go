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

	"github.com/kube-workspaces/desktop-client/internal/i18n"
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
		return i18n.Get("errors.cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return i18n.Get("errors.timeout")

	// Authentication, in the order a user meets it.
	case errors.Is(err, kwclient.ErrInvalidCredentials):
		return i18n.Get("errors.badCredentials")
	case errors.Is(err, kwclient.ErrAccountLocked):
		return i18n.Get("errors.locked")
	case errors.Is(err, kwclient.ErrAccountDisabled):
		return i18n.Get("errors.disabled")
	case errors.Is(err, kwclient.ErrRateLimited):
		return i18n.Get("errors.rateLimited")
	case errors.Is(err, kwclient.ErrLocalAuthDisabled):
		return i18n.Get("errors.localDisabled")
	case errors.Is(err, kwclient.ErrNoSessionCookie):
		return i18n.Get("errors.noSession")
	case errors.Is(err, kwclient.ErrInvalidRequest):
		return i18n.Get("errors.badRequest")

	// The browser flow.
	case errors.Is(err, kwclient.ErrBrowserLoginTimeout):
		return i18n.Get("errors.browserTimeout")
	case errors.Is(err, kwclient.ErrStateMismatch):
		return i18n.Get("errors.stateMismatch")
	case errors.Is(err, kwclient.ErrAuthorizationDenied):
		return i18n.Sprintf("errors.authDenied", tail(err))
	case errors.Is(err, kwclient.ErrNativeAuthUnsupported):
		return i18n.Get("errors.nativeMissing")

	// The session.
	case errors.Is(err, kwclient.ErrUnauthorized):
		return i18n.Get("errors.expired")
	case errors.Is(err, kwclient.ErrForbidden):
		return i18n.Get("errors.forbidden")
	case errors.Is(err, kwclient.ErrNotFound):
		return i18n.Get("errors.notFound")
	case errors.Is(err, kwclient.ErrSessionInUse):
		return i18n.Get("errors.inUse")
	case errors.Is(err, kwclient.ErrNotVM):
		return i18n.Get("errors.notVM")
	case errors.Is(err, kwclient.ErrUnavailable):
		return i18n.Get("errors.unavailable")
	case errors.Is(err, kwclient.ErrBadGateway):
		return i18n.Get("errors.badGateway")
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
		return i18n.Sprintf("errors.cert", certificateReason(certErr.Err), i18n.Get("errors.certSelfSigned")), true
	}
	var recordErr tls.RecordHeaderError
	if errors.As(err, &recordErr) {
		return i18n.Get("errors.noTLS"), true
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return i18n.Sprintf("errors.dns", dnsErr.Name), true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return i18n.Get("errors.timeoutAddr"), true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return i18n.Sprintf("errors.unreachable", opErr.Err.Error()+"."), true
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return i18n.Sprintf("errors.unreachable", tail(urlErr.Err)), true
	}
	return "", false
}

// certificateReason names the three ways certificate verification usually
// fails, because the distinction changes what the user should do.
func certificateReason(err error) string {
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return i18n.Get("errors.certUnknown")
	}
	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return i18n.Sprintf("errors.certHost", hostname.Host)
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
		return i18n.Get("errors.requestFailed")
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
