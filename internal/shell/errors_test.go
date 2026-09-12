// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

func TestDescribeNamesFailuresAUserCanActOn(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string // a substring the message must contain
	}{
		{"nothing", nil, ""},
		{"cancelled", context.Canceled, "Cancelled"},
		{"bad password", fmt.Errorf("x: %w", kwclient.ErrInvalidCredentials), "not accepted"},
		{"locked", fmt.Errorf("x: %w", kwclient.ErrAccountLocked), "locked"},
		{"disabled", fmt.Errorf("x: %w", kwclient.ErrAccountDisabled), "disabled"},
		{"rate limited", fmt.Errorf("x: %w", kwclient.ErrRateLimited), "Too many attempts"},
		{"local auth off", fmt.Errorf("x: %w", kwclient.ErrLocalAuthDisabled), "Sign in with browser"},
		{"expired", fmt.Errorf("x: %w", kwclient.ErrUnauthorized), "expired"},
		{"forbidden", fmt.Errorf("x: %w", kwclient.ErrForbidden), "access"},
		{"display in use", fmt.Errorf("x: %w", kwclient.ErrSessionInUse), "take it over"},
		{"not a vm", fmt.Errorf("x: %w", kwclient.ErrNotVM), "browser"},
		{"maintenance", fmt.Errorf("x: %w", kwclient.ErrUnavailable), "unavailable"},
		{"browser timeout", fmt.Errorf("x: %w", kwclient.ErrBrowserLoginTimeout), "not completed in time"},
		{"state mismatch", fmt.Errorf("x: %w", kwclient.ErrStateMismatch), "could not be verified"},
		{"old instance", fmt.Errorf("x: %w", kwclient.ErrNativeAuthUnsupported), "without browser sign-in"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Describe(tt.err)
			if tt.want == "" {
				if got != "" {
					t.Fatalf("Describe(nil) = %q", got)
				}
				return
			}
			if !strings.Contains(got, tt.want) {
				t.Fatalf("Describe = %q, want it to mention %q", got, tt.want)
			}
		})
	}
}

// TestDescribeNamesTransportFailures covers what a user typing a server
// address for the first time actually hits, and what Go's own error strings
// are worst at explaining.
func TestDescribeNamesTransportFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			"untrusted certificate",
			&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}},
			"trusted authority",
		},
		{
			"wrong host on the certificate",
			&tls.CertificateVerificationError{Err: x509.HostnameError{Host: "kw.example.com"}},
			"not valid for kw.example.com",
		},
		{
			"plaintext behind an https URL",
			tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"},
			"http:// rather than https://",
		},
		{
			"no such host",
			&net.DNSError{Name: "typo.example.com", Err: "no such host", IsNotFound: true},
			"typo.example.com",
		},
		{
			"connection refused",
			&net.OpError{Op: "dial", Err: errors.New("connect: connection refused")},
			"connection refused",
		},
		{
			"a wrapped url error",
			&url.Error{Op: "Get", URL: "https://kw.example.com", Err: errors.New("EOF")},
			"reach the instance",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Describe(fmt.Errorf("kwclient: GET /auth/config: %w", tt.err))
			if !strings.Contains(got, tt.want) {
				t.Fatalf("Describe = %q, want it to mention %q", got, tt.want)
			}
			// Whatever the cause, the user gets a sentence.
			if !strings.HasSuffix(got, ".") || got[0] < 'A' || got[0] > 'Z' {
				t.Fatalf("Describe = %q, which does not read as a sentence", got)
			}
		})
	}

	// A certificate failure must point at the way out, since this is the one
	// the people installing the platform will meet on day one.
	got := Describe(&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}})
	if !strings.Contains(got, "Ignore TLS certificate errors") {
		t.Fatalf("the certificate error does not name the escape hatch: %q", got)
	}
}

func TestDescribeFallsBackToTheErrorItself(t *testing.T) {
	// An unrecognised failure is still shown: something the user can quote to
	// an administrator beats "something went wrong".
	got := Describe(errors.New("the flux capacitor is misaligned"))
	if !strings.Contains(got, "flux capacitor") {
		t.Fatalf("the fallback hid the error: %q", got)
	}
	if !strings.HasSuffix(got, ".") {
		t.Fatalf("the fallback is not a sentence: %q", got)
	}

	// The layered client prefixes are stripped rather than shown three deep.
	got = Describe(fmt.Errorf("kwclient: GET /v1/workspaces: %w", errors.New("unexpected end of JSON input")))
	if strings.Contains(got, "kwclient") || strings.Contains(got, "GET /v1") {
		t.Fatalf("the fallback showed the plumbing: %q", got)
	}
}

func TestNormaliseServer(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"   ", ""},
		// https is assumed: the session token is a bearer credential, and
		// defaulting to cleartext would hand it to the network.
		{"kw.example.com", "https://kw.example.com"},
		{"  kw.example.com/  ", "https://kw.example.com"},
		{"https://kw.example.com", "https://kw.example.com"},
		{"https://kw.example.com/", "https://kw.example.com"},
		{"http://localhost:8080", "http://localhost:8080"},
		{"https://kw.example.com/base///", "https://kw.example.com/base"},
	}
	for _, tt := range tests {
		if got := NormaliseServer(tt.in); got != tt.want {
			t.Errorf("NormaliseServer(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
