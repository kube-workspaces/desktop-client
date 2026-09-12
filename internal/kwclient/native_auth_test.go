// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// No test in this file opens a browser: every one of them injects a fake
// launcher through BrowserLoginOptions.openBrowser.

// --- PKCE -------------------------------------------------------------------

// TestCodeChallengeS256RFC7636Vector checks the derivation against the worked
// example published in RFC 7636 appendix B. Getting this wrong (padded base64,
// standard rather than URL alphabet, hashing the raw bytes of a decoded
// verifier) yields a challenge the server can never match, so pinning the
// published vector is worth more than any amount of round-trip testing.
func TestCodeChallengeS256RFC7636Vector(t *testing.T) {
	const (
		verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	)
	if got := CodeChallengeS256(verifier); got != challenge {
		t.Fatalf("CodeChallengeS256(%q) = %q, want %q (RFC 7636 appendix B)", verifier, got, challenge)
	}
}

func TestNewCodeVerifierCharsetAndLength(t *testing.T) {
	seen := make(map[string]bool, 64)
	for i := 0; i < 64; i++ {
		v, err := newCodeVerifier()
		if err != nil {
			t.Fatalf("newCodeVerifier() error = %v", err)
		}
		if len(v) < codeVerifierMinLen || len(v) > codeVerifierMaxLen {
			t.Fatalf("newCodeVerifier() length = %d, want %d..%d", len(v), codeVerifierMinLen, codeVerifierMaxLen)
		}
		if !validCodeVerifier(v) {
			t.Fatalf("newCodeVerifier() = %q, which is outside the RFC 7636 unreserved charset", v)
		}
		if seen[v] {
			t.Fatalf("newCodeVerifier() repeated a verifier: %q", v)
		}
		seen[v] = true
	}
}

func TestValidCodeVerifier(t *testing.T) {
	const min43 = "0123456789012345678901234567890123456789012"
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "minimum length", in: min43, want: true},
		{name: "one under minimum", in: min43[:42]},
		{name: "maximum length", in: strings.Repeat("a", codeVerifierMaxLen), want: true},
		{name: "one over maximum", in: strings.Repeat("a", codeVerifierMaxLen+1)},
		{name: "empty", in: ""},
		{name: "all unreserved punctuation", in: strings.Repeat("-._~", 11) + "abc", want: true},
		{name: "plus is reserved", in: min43[:42] + "+"},
		{name: "slash is reserved", in: min43[:42] + "/"},
		{name: "equals is reserved", in: min43[:42] + "="},
		{name: "space", in: min43[:42] + " "},
		{name: "non-ascii", in: min43[:42] + "é"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validCodeVerifier(tt.in); got != tt.want {
				t.Fatalf("validCodeVerifier(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestNewOAuthStateIsUnreservedAndUnique(t *testing.T) {
	seen := make(map[string]bool, 32)
	for i := 0; i < 32; i++ {
		s, err := newOAuthState()
		if err != nil {
			t.Fatalf("newOAuthState() error = %v", err)
		}
		// The API restricts the state it will echo to unreserved characters.
		if !validCodeVerifier(s) {
			t.Fatalf("newOAuthState() = %q, which the API would reject", s)
		}
		if seen[s] {
			t.Fatalf("newOAuthState() repeated a state: %q", s)
		}
		seen[s] = true
	}
}

// --- loopback listener ------------------------------------------------------

func TestLoopbackRedirectURIUsesTheIPLiteral(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	got := loopbackRedirectURI(ln.Addr())
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("loopbackRedirectURI() = %q, which does not parse: %v", got, err)
	}
	if u.Scheme != "http" {
		t.Fatalf("scheme = %q, want http", u.Scheme)
	}
	// RFC 8252 §8.3: the literal, never "localhost".
	if u.Hostname() != "127.0.0.1" {
		t.Fatalf("host = %q, want the loopback literal 127.0.0.1", u.Hostname())
	}
	if u.Port() == "" || u.Port() == "0" {
		t.Fatalf("port = %q, want the bound ephemeral port", u.Port())
	}
	if u.Path != nativeCallbackPath {
		t.Fatalf("path = %q, want %q", u.Path, nativeCallbackPath)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		t.Fatalf("loopbackRedirectURI() = %q, want no query or fragment (the API rejects both)", got)
	}
}

func TestServeLoopbackCallbackReturnsTheCode(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	results, shutdown := serveLoopbackCallback(ln)
	defer shutdown()

	base := "http://" + ln.Addr().String()
	resp, err := http.Get(base + nativeCallbackPath + "?code=the-code&state=the-state")
	if err != nil {
		t.Fatalf("callback request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("callback Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(string(body), "close this window") {
		t.Fatalf("callback page did not tell the user to close the window:\n%s", body)
	}

	select {
	case res := <-results:
		if res.code != "the-code" || res.state != "the-state" || !res.stateWasSupplied {
			t.Fatalf("callbackResult = %+v, want the code and state from the query", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serveLoopbackCallback() never reported the callback")
	}

	// RFC 8252 §8.3: the port is only open for the duration of the request.
	shutdown()
	if _, err := http.Get(base + nativeCallbackPath + "?code=again"); err == nil {
		t.Fatal("the loopback port is still accepting connections after shutdown")
	}
}

func TestServeLoopbackCallbackIgnoresOtherPaths(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	results, shutdown := serveLoopbackCallback(ln)
	defer shutdown()

	base := "http://" + ln.Addr().String()
	// A browser favicon probe must not end the login.
	resp, err := http.Get(base + "/favicon.ico")
	if err != nil {
		t.Fatalf("favicon request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("favicon status = %d, want 404", resp.StatusCode)
	}

	// Nor must a bare hit on the callback path with no parameters.
	resp, err = http.Get(base + nativeCallbackPath)
	if err != nil {
		t.Fatalf("bare callback request: %v", err)
	}
	resp.Body.Close()

	select {
	case res := <-results:
		t.Fatalf("serveLoopbackCallback() completed the flow on a non-redirect request: %+v", res)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, shutdown := serveLoopbackCallback(ln)
	shutdown()
	shutdown() // must not panic or block
}

// --- LoginBrowser ------------------------------------------------------------

// nativeAuthServer is a stand-in for the API: it implements /auth/login (which
// validates the native parameters the way the real one does and redirects
// straight back to the loopback listener) and /auth/native/token.
type nativeAuthServer struct {
	*httptest.Server

	token string
	// issued maps an authorization code to the challenge it is bound to.
	issued map[string]string
	// stateOverride, when non-empty, is echoed instead of the client's state.
	stateOverride string
	// errorRedirect, when non-empty, is returned as ?error= instead of a code.
	errorRedirect string
	// loginQuery records the query /auth/login was called with.
	loginQuery url.Values
	// exchanges counts POST /auth/native/token calls.
	exchanges int
}

func newNativeAuthServer(t *testing.T) *nativeAuthServer {
	t.Helper()
	s := &nativeAuthServer{token: "session-token-value", issued: map[string]string{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/login", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		s.loginQuery = q

		redirect := q.Get("native_redirect")
		u, err := url.Parse(redirect)
		if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
			http.Error(w, "bad native_redirect", http.StatusBadRequest)
			return
		}
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
			http.Error(w, "bad pkce", http.StatusBadRequest)
			return
		}

		state := q.Get("state")
		if s.stateOverride != "" {
			state = s.stateOverride
		}
		out := url.Values{"state": {state}}
		if s.errorRedirect != "" {
			out.Set("error", s.errorRedirect)
			out.Set("error_description", "the user said no")
		} else {
			code := "code-for-" + q.Get("code_challenge")[:8]
			s.issued[code] = q.Get("code_challenge")
			out.Set("code", code)
		}
		u.RawQuery = out.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("/auth/native/token", func(w http.ResponseWriter, r *http.Request) {
		s.exchanges++
		var body nativeTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONErrorBody(w, http.StatusBadRequest, "invalid_request", "bad body")
			return
		}
		challenge, ok := s.issued[body.Code]
		if !ok {
			writeJSONErrorBody(w, http.StatusBadRequest, "invalid_code", "unknown, expired or already used")
			return
		}
		delete(s.issued, body.Code) // single use, like the real thing
		if CodeChallengeS256(body.CodeVerifier) != challenge {
			writeJSONErrorBody(w, http.StatusBadRequest, "invalid_verifier", "verifier does not match")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(nativeTokenResponse{
			Token:     s.token,
			ExpiresAt: time.Now().Add(24 * time.Hour).Unix(),
			Email:     "user@example.com",
			Role:      "admin",
		})
	})
	mux.HandleFunc("/auth/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true,"localAuth":{"enabled":false},` +
			`"nativeAuth":{"enabled":true,"methods":["loopback-pkce"]}}`))
	})

	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func writeJSONErrorBody(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(jsonError{Error: code, Message: msg})
}

// browserFetch is a fake system browser: it simply follows the authorize URL,
// which is exactly what a real one does for this flow.
func browserFetch(t *testing.T) func(string) error {
	t.Helper()
	return func(authorizeURL string) error {
		go func() {
			resp, err := http.Get(authorizeURL)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
		return nil
	}
}

func TestLoginBrowserEndToEnd(t *testing.T) {
	srv := newNativeAuthServer(t)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	var notifiedURL string
	got, err := c.LoginBrowser(context.Background(), &BrowserLoginOptions{
		Timeout:     10 * time.Second,
		openBrowser: browserFetch(t),
		Notify:      func(u string, err error) { notifiedURL = u },
	})
	if err != nil {
		t.Fatalf("LoginBrowser() error = %v", err)
	}
	if got.Token != srv.token {
		t.Fatalf("token = %q, want %q", got.Token, srv.token)
	}
	if got.Email != "user@example.com" || got.Role != "admin" {
		t.Fatalf("identity = %+v, want user@example.com/admin", got)
	}
	if got.ExpiresAt.IsZero() {
		t.Fatal("ExpiresAt was not populated")
	}

	// The authorize request must carry exactly the RFC 8252 / RFC 7636
	// parameters, with a loopback-literal redirect and no leaked verifier.
	q := srv.loginQuery
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	if q.Get("code_challenge") == "" {
		t.Fatal("no code_challenge was sent")
	}
	if q.Get("state") == "" {
		t.Fatal("no state was sent")
	}
	redirect, err := url.Parse(q.Get("native_redirect"))
	if err != nil {
		t.Fatalf("native_redirect does not parse: %v", err)
	}
	if redirect.Hostname() != "127.0.0.1" {
		t.Fatalf("native_redirect host = %q, want 127.0.0.1 (RFC 8252 §8.3)", redirect.Hostname())
	}
	if strings.Contains(notifiedURL, "code_verifier") {
		t.Fatal("the code verifier leaked into the authorize URL")
	}
	if notifiedURL == "" {
		t.Fatal("Notify was never called with the authorize URL")
	}

	// And the port is closed again afterwards.
	if _, err := http.Get(redirect.String()); err == nil {
		t.Fatal("the loopback port is still open after a completed login")
	}
}

func TestLoginBrowserRejectsStateMismatch(t *testing.T) {
	srv := newNativeAuthServer(t)
	srv.stateOverride = "a-state-we-never-generated"

	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = c.LoginBrowser(context.Background(), &BrowserLoginOptions{
		Timeout:     10 * time.Second,
		openBrowser: browserFetch(t),
	})
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("LoginBrowser() error = %v, want ErrStateMismatch", err)
	}
	// Critically: a mismatched state must abort before the code is spent.
	if srv.exchanges != 0 {
		t.Fatalf("the code was exchanged despite the state mismatch (%d exchanges)", srv.exchanges)
	}
}

func TestLoginBrowserRejectsMissingState(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	results, shutdown := serveLoopbackCallback(ln)
	defer shutdown()

	resp, err := http.Get("http://" + ln.Addr().String() + nativeCallbackPath + "?code=c")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	resp.Body.Close()

	res := <-results
	if res.stateWasSupplied {
		t.Fatal("stateWasSupplied is true for a callback with no state parameter")
	}
	// LoginBrowser treats that as a mismatch; see the check in LoginBrowser.
}

func TestLoginBrowserSurfacesAuthorizationErrors(t *testing.T) {
	srv := newNativeAuthServer(t)
	srv.errorRedirect = "access_denied"

	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = c.LoginBrowser(context.Background(), &BrowserLoginOptions{
		Timeout:     10 * time.Second,
		openBrowser: browserFetch(t),
	})
	if !errors.Is(err, ErrAuthorizationDenied) {
		t.Fatalf("LoginBrowser() error = %v, want ErrAuthorizationDenied", err)
	}
	if !strings.Contains(err.Error(), "the user said no") {
		t.Fatalf("LoginBrowser() error = %v, want the provider's description", err)
	}
}

func TestLoginBrowserTimesOut(t *testing.T) {
	srv := newNativeAuthServer(t)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// A browser that never gets around to it.
	start := time.Now()
	_, err = c.LoginBrowser(context.Background(), &BrowserLoginOptions{
		Timeout:     150 * time.Millisecond,
		openBrowser: func(string) error { return nil },
	})
	if !errors.Is(err, ErrBrowserLoginTimeout) {
		t.Fatalf("LoginBrowser() error = %v, want ErrBrowserLoginTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("LoginBrowser() took %s to time out", elapsed)
	}
}

func TestLoginBrowserHonoursContextCancellation(t *testing.T) {
	srv := newNativeAuthServer(t)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err = c.LoginBrowser(ctx, &BrowserLoginOptions{
		Timeout:     30 * time.Second,
		openBrowser: func(string) error { return nil },
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("LoginBrowser() error = %v, want context.Canceled", err)
	}
}

func TestLoginBrowserReportsBrowserLaunchFailure(t *testing.T) {
	srv := newNativeAuthServer(t)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	var launchErr error
	_, err = c.LoginBrowser(context.Background(), &BrowserLoginOptions{
		Timeout:     150 * time.Millisecond,
		openBrowser: func(string) error { return errors.New("no browser here") },
		Notify: func(_ string, err error) {
			if err != nil {
				launchErr = err
			}
		},
	})
	// A failed launch is not fatal: the user can paste the URL, so the flow
	// carries on and only the timeout ends it.
	if !errors.Is(err, ErrBrowserLoginTimeout) {
		t.Fatalf("LoginBrowser() error = %v, want the flow to continue to its timeout", err)
	}
	if !errors.Is(launchErr, ErrBrowserLaunchFailed) {
		t.Fatalf("Notify launch error = %v, want ErrBrowserLaunchFailed", launchErr)
	}
}

// --- token exchange ----------------------------------------------------------

func TestExchangeNativeCodeRejectsAReplay(t *testing.T) {
	srv := newNativeAuthServer(t)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	verifier, err := newCodeVerifier()
	if err != nil {
		t.Fatalf("newCodeVerifier() error = %v", err)
	}
	challenge := CodeChallengeS256(verifier)
	code := "code-for-" + challenge[:8]
	srv.issued[code] = challenge

	if _, err := c.exchangeNativeCode(context.Background(), code, verifier); err != nil {
		t.Fatalf("first exchange error = %v", err)
	}
	_, err = c.exchangeNativeCode(context.Background(), code, verifier)
	if err == nil {
		t.Fatal("the authorization code was accepted twice")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "invalid_code" {
		t.Fatalf("replay error = %v, want an invalid_code APIError", err)
	}
}

func TestExchangeNativeCodeRejectsAWrongVerifier(t *testing.T) {
	srv := newNativeAuthServer(t)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	verifier, _ := newCodeVerifier()
	other, _ := newCodeVerifier()
	challenge := CodeChallengeS256(verifier)
	code := "code-for-" + challenge[:8]
	srv.issued[code] = challenge

	_, err = c.exchangeNativeCode(context.Background(), code, other)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "invalid_verifier" {
		t.Fatalf("wrong-verifier error = %v, want an invalid_verifier APIError", err)
	}
}

func TestExchangeNativeCodeSendsNoStaleCredentials(t *testing.T) {
	var sawAuth, sawCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawCookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(nativeTokenResponse{Token: "new-token"})
	}))
	defer srv.Close()

	c, err := New(srv.URL, WithToken("a-stale-token"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := c.exchangeNativeCode(context.Background(), "code", "verifier"); err != nil {
		t.Fatalf("exchangeNativeCode() error = %v", err)
	}
	if sawAuth != "" || sawCookie != "" {
		t.Fatalf("the exchange carried stale credentials: auth=%q cookie=%q", sawAuth, sawCookie)
	}
}

func TestExchangeNativeCodeRejectsAnEmptyToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"expires_at":1}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := c.exchangeNativeCode(context.Background(), "code", "verifier"); err == nil {
		t.Fatal("an empty token was accepted")
	}
}

// --- capability discovery ----------------------------------------------------

func TestNativeAuthDiscovery(t *testing.T) {
	srv := newNativeAuthServer(t)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	na, err := c.NativeAuth(context.Background())
	if err != nil {
		t.Fatalf("NativeAuth() error = %v", err)
	}
	if !na.Supported() {
		t.Fatalf("NativeAuth() = %+v, want supported", na)
	}
}

func TestNativeAuthConfigSupported(t *testing.T) {
	tests := []struct {
		name string
		cfg  *NativeAuthConfig
		want bool
	}{
		{name: "nil (old build, field absent)", cfg: nil},
		{name: "disabled", cfg: &NativeAuthConfig{}},
		{name: "loopback-pkce", cfg: &NativeAuthConfig{Enabled: true, Methods: []string{"loopback-pkce"}}, want: true},
		{name: "enabled with no methods", cfg: &NativeAuthConfig{Enabled: true}, want: true},
		{name: "only an unknown method", cfg: &NativeAuthConfig{Enabled: true, Methods: []string{"private-uri-scheme"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.Supported(); got != tt.want {
				t.Fatalf("Supported() = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- browser launcher --------------------------------------------------------

func TestOpenSystemBrowserRefusesNonHTTPURLs(t *testing.T) {
	for _, bad := range []string{
		"file:///etc/passwd",
		"javascript:alert(1)",
		"vbscript:x",
		"data:text/html,<script>1</script>",
		"://nonsense",
	} {
		if err := openSystemBrowser(bad); err == nil {
			t.Fatalf("openSystemBrowser(%q) returned nil; it must refuse non-http(s) URLs", bad)
		}
	}
}
