// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestClient spins up an httptest server running h and returns a client
// pointed at it, with a token set unless tokenOpt overrides it.
func newTestClient(t *testing.T, h http.HandlerFunc, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	all := append([]Option{WithToken("tok.sig"), WithTimeout(5 * time.Second)}, opts...)
	c, err := New(srv.URL, all...)
	if err != nil {
		t.Fatalf("New(%q) = %v", srv.URL, err)
	}
	return c
}

// writeJSON is a tiny test helper for handlers.
func writeJSON(t *testing.T, w http.ResponseWriter, status int, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write([]byte(body)); err != nil {
		t.Errorf("write body: %v", err)
	}
}

func TestNewValidatesBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		want    string
		wantErr bool
	}{
		{name: "https", base: "https://kw.example.com", want: "https://kw.example.com"},
		{name: "trailing slash trimmed", base: "https://kw.example.com/", want: "https://kw.example.com"},
		{name: "sub path kept", base: "https://kw.example.com/kw/", want: "https://kw.example.com/kw"},
		{name: "query dropped", base: "https://kw.example.com/?a=b", want: "https://kw.example.com"},
		{name: "whitespace trimmed", base: "  http://localhost:8080 ", want: "http://localhost:8080"},
		{name: "no scheme", base: "kw.example.com", wantErr: true},
		{name: "bad scheme", base: "ftp://kw.example.com", wantErr: true},
		{name: "no host", base: "https://", wantErr: true},
		{name: "unparseable", base: "https://%zz", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(tc.base)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("New(%q) = %v, want error", tc.base, c.BaseURL())
				}
				return
			}
			if err != nil {
				t.Fatalf("New(%q): %v", tc.base, err)
			}
			if got := c.BaseURL(); got != tc.want {
				t.Errorf("BaseURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDefaultUserAgentAndAuthHeaders(t *testing.T) {
	var got *http.Request
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		writeJSON(t, w, http.StatusOK, `[]`)
	})

	if _, err := c.ListWorkspaces(context.Background(), ""); err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}

	if want := "Bearer tok.sig"; got.Header.Get("Authorization") != want {
		t.Errorf("Authorization = %q, want %q", got.Header.Get("Authorization"), want)
	}
	ck, err := got.Cookie(SessionCookieName)
	if err != nil {
		t.Fatalf("cookie %s: %v", SessionCookieName, err)
	}
	if ck.Value != "tok.sig" {
		t.Errorf("cookie value = %q, want %q", ck.Value, "tok.sig")
	}
	if ua := got.Header.Get("User-Agent"); ua != defaultUserAgent {
		t.Errorf("User-Agent = %q, want %q", ua, defaultUserAgent)
	}
	if cc := got.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if acc := got.Header.Get("Accept"); acc != "application/json" {
		t.Errorf("Accept = %q, want application/json", acc)
	}
}

func TestWithUserAgentAndSetToken(t *testing.T) {
	var gotUA, gotAuth string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAuth = r.Header.Get("Authorization")
		writeJSON(t, w, http.StatusOK, `[]`)
	}, WithUserAgent("kw-test/9"))

	c.SetToken("newtoken.sig")
	if _, err := c.ListImages(context.Background()); err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if gotUA != "kw-test/9" {
		t.Errorf("User-Agent = %q, want kw-test/9", gotUA)
	}
	if gotAuth != "Bearer newtoken.sig" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if c.Token() != "newtoken.sig" {
		t.Errorf("Token() = %q", c.Token())
	}
}

func TestAnonymousClientSendsNoCredentials(t *testing.T) {
	var hasAuth, hasCookie bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, hasAuth = r.Header["Authorization"]
		_, hasCookie = r.Header["Cookie"]
		writeJSON(t, w, http.StatusOK, `{"enabled":false}`)
	}, WithToken(""))

	if _, err := c.AuthConfig(context.Background()); err != nil {
		t.Fatalf("AuthConfig: %v", err)
	}
	if hasAuth || hasCookie {
		t.Errorf("anonymous client sent credentials: auth=%v cookie=%v", hasAuth, hasCookie)
	}
}

func TestStatusErrorMapping(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    error
		wantMsg string
	}{
		{
			name:    "401 unauthorized",
			status:  http.StatusUnauthorized,
			body:    `{"error":"unauthorized","message":"missing session"}`,
			want:    ErrUnauthorized,
			wantMsg: "missing session",
		},
		{
			name:    "403 forbidden",
			status:  http.StatusForbidden,
			body:    `{"error":"forbidden","message":"namespace not allowed"}`,
			want:    ErrForbidden,
			wantMsg: "namespace not allowed",
		},
		{
			name:    "404 json string body",
			status:  http.StatusNotFound,
			body:    `"workspace demo/dev not found"`,
			want:    ErrNotFound,
			wantMsg: "workspace demo/dev not found",
		},
		{
			name:    "409 conflict",
			status:  http.StatusConflict,
			body:    "serial console is in use for this workspace",
			want:    ErrSessionInUse,
			wantMsg: "serial console is in use for this workspace",
		},
		{
			name:   "503 unavailable",
			status: http.StatusServiceUnavailable,
			body:   `{"error":"maintenance mode"}`,
			want:   ErrUnavailable,
		},
		{
			name:   "502 bad gateway",
			status: http.StatusBadGateway,
			body:   "upstream failure",
			want:   ErrBadGateway,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, tc.status, tc.body)
			})

			_, err := c.GetWorkspace(context.Background(), "demo", "dev")
			if err == nil {
				t.Fatal("GetWorkspace: want error, got nil")
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
			if tc.wantMsg != "" && apiErr.Message != tc.wantMsg {
				t.Errorf("Message = %q, want %q", apiErr.Message, tc.wantMsg)
			}
			if apiErr.Body == "" {
				t.Error("Body is empty, want the raw response body")
			}
		})
	}
}

func TestAPIErrorTemporary(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{http.StatusServiceUnavailable, true},
		{http.StatusBadGateway, true},
		{http.StatusTooManyRequests, true},
		{http.StatusNotFound, false},
		{http.StatusUnauthorized, false},
	}
	for _, tc := range tests {
		e := &APIError{StatusCode: tc.status}
		if got := e.Temporary(); got != tc.want {
			t.Errorf("status %d: Temporary() = %v, want %v", tc.status, got, tc.want)
		}
	}
}

// TestInsecureSkipVerify covers the self-signed dev-cluster path for both REST
// and WebSocket traffic, since the two use different TLS configurations.
func TestInsecureSkipVerify(t *testing.T) {
	handler, _ := echoUpgrader(t, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/workspaces/dev/exec", handler)
	mux.HandleFunc("/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `[]`)
	})

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	t.Run("rejected without the option", func(t *testing.T) {
		c, err := New(srv.URL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := c.ListWorkspaces(context.Background(), "demo"); err == nil {
			t.Error("ListWorkspaces: want TLS verification failure")
		}
		if _, err := c.DialExec(context.Background(), "demo", "dev", 80, 24); err == nil {
			t.Error("DialExec: want TLS verification failure")
		}
	})

	t.Run("accepted with the option", func(t *testing.T) {
		c, err := New(srv.URL, WithInsecureSkipVerify(true))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := c.ListWorkspaces(context.Background(), "demo"); err != nil {
			t.Errorf("ListWorkspaces: %v", err)
		}
		conn, err := c.DialExec(context.Background(), "demo", "dev", 80, 24)
		if err != nil {
			t.Fatalf("DialExec: %v", err)
		}
		conn.Close()
	})
}

func TestWithHTTPClientIsNotMutated(t *testing.T) {
	custom := &http.Client{}
	c, err := New("https://kw.example.com", WithHTTPClient(custom), WithTimeout(3*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if custom.Timeout != 0 {
		t.Errorf("caller's client was mutated: Timeout = %s", custom.Timeout)
	}
	if c.httpc.Timeout != 3*time.Second {
		t.Errorf("client Timeout = %s, want 3s", c.httpc.Timeout)
	}
	if c.httpc == custom {
		t.Error("WithTimeout must copy a caller-supplied client before changing it")
	}
}

func TestContextCancellationIsHonored(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.ListWorkspaces(ctx, "demo"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListWorkspaces error = %v, want context.Canceled", err)
	}
}
