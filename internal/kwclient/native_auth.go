// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// This file implements the client half of the RFC 8252 ("OAuth 2.0 for Native
// Apps") loopback + PKCE flow, i.e. the AppAuth pattern:
//
//  1. generate a PKCE verifier and its S256 challenge,
//  2. bind a throwaway HTTP listener to 127.0.0.1 on an ephemeral port,
//  3. open the SYSTEM browser at
//     <server>/auth/login?native_redirect=http://127.0.0.1:<port>/callback&...,
//  4. the API brokers the whole OIDC dance with whatever IdP the administrator
//     configured and bounces the browser back to the listener with a
//     single-use code,
//  5. exchange that code plus the verifier at POST /auth/native/token.
//
// Two rules from RFC 8252 shape the details and are easy to get wrong:
//
//   - §8.3: the redirect must name the loopback IP *literal*, never
//     "localhost". "localhost" resolves through the OS and can be pointed
//     somewhere else; the API rejects it for the same reason.
//   - §8.3: the loopback port is open only for the duration of the request.
//     We shut the server down as soon as the callback has been answered so we
//     are not left listening on an authenticated-looking endpoint.
//
// The system browser is used deliberately, never an embedded webview: a
// webview would let this process observe the user's IdP credentials, which is
// exactly what the native-app flow exists to avoid.

const (
	// nativeCallbackPath is the single path the loopback listener answers.
	nativeCallbackPath = "/callback"

	// DefaultBrowserLoginTimeout bounds how long [Client.LoginBrowser] waits
	// for the browser round trip. It has to cover a human reading an IdP
	// consent screen and possibly an MFA prompt, so it is generous.
	DefaultBrowserLoginTimeout = 3 * time.Minute

	// codeVerifierBytes is the entropy of the PKCE code verifier. 32 bytes is
	// the size RFC 7636 §4.1 recommends and encodes to exactly 43 unreserved
	// characters, the minimum legal verifier length.
	codeVerifierBytes = 32

	// stateBytes is the entropy of the OAuth state parameter.
	stateBytes = 32

	// codeVerifierMinLen/codeVerifierMaxLen are the RFC 7636 §4.1 bounds.
	codeVerifierMinLen = 43
	codeVerifierMaxLen = 128

	// nativeCallbackReadTimeout bounds how long a single loopback request may
	// take. Anything on the far side is a browser on the same machine, so this
	// only exists to stop a stuck connection pinning the port open.
	nativeCallbackReadTimeout = 10 * time.Second

	// nativeLoginAttempts is how many full RFC 8252 round trips LoginBrowser
	// runs before giving up when the platform refuses to redeem a single-use
	// code. Each attempt mints a fresh verifier, challenge, state and code; the
	// code itself is single-use and can never be re-sent. With a multi-replica
	// API whose code store is per-replica, the first exchange can fail by
	// landing on the wrong replica and the next login usually succeeds.
	nativeLoginAttempts = 3

	// nativeExchangeAttempts is how many times the same single-use code is
	// re-sent to POST /auth/native/token before the flow gives up on it and
	// restarts the whole browser login. The platform keeps each code on the
	// API replica that minted it (in-memory per replica on older builds, and
	// on the memory fallback during a K8s outage even on Secret-backed builds),
	// so an exchange that lands elsewhere fails with invalid_code without
	// consuming the code — re-exchanging lets the load balancer try another
	// replica with no browser round trip and no repeated IdP consent. The
	// endpoint is rate-limited at 10/min per client IP, which bounds this:
	// nativeLoginAttempts × (1 + nativeExchangeAttempts) stays just under it.
	nativeExchangeAttempts = 2
)

// Sentinel errors specific to the browser login flow.
var (
	// ErrBrowserLoginTimeout means the user never completed the login before
	// the timeout elapsed.
	ErrBrowserLoginTimeout = errors.New("browser login timed out")
	// ErrStateMismatch means the loopback callback carried a state parameter
	// that was not the one we generated. Something interfered with the flow,
	// so it is abandoned rather than completed.
	ErrStateMismatch = errors.New("oauth state mismatch")
	// ErrAuthorizationDenied means the IdP or the API refused the login; the
	// wrapped [*APIError]-free message carries the provider's description.
	ErrAuthorizationDenied = errors.New("authorization denied")
	// ErrNativeAuthUnsupported means the instance does not advertise the
	// RFC 8252 loopback flow, so it is running a build that predates it.
	ErrNativeAuthUnsupported = errors.New("instance does not support browser login")
	// ErrBrowserLaunchFailed means no system browser could be started. It is
	// informational: the caller should show the URL and keep waiting.
	ErrBrowserLaunchFailed = errors.New("could not open a system browser")
	// ErrNativeCodeUnredeemable means a completed loopback login had its
	// single-use code refused by the platform, both by re-exchanging the same
	// code and by restarting the flow the bounded number of times, so no
	// redeemable code could be produced. The identity provider's sign-in
	// succeeded; the token exchange did not. No retry is possible from here —
	// a fresh login is the only recovery.
	ErrNativeCodeUnredeemable = errors.New("the sign-in code could not be redeemed")
)

// NativeAuthConfig is the "nativeAuth" object of GET /auth/config. It is absent
// on builds that predate the native flow, which decodes to the zero value.
type NativeAuthConfig struct {
	// Enabled reports whether [Client.LoginBrowser] can be used.
	Enabled bool `json:"enabled"`
	// Methods lists the supported native flows, currently "loopback-pkce".
	Methods []string `json:"methods"`
}

// NativeAuth fetches just the nativeAuth section of GET /auth/config.
//
// It is a separate call rather than a field on [AuthConfig] so that support can
// be probed without disturbing existing callers of [Client.AuthConfig].
func (c *Client) NativeAuth(ctx context.Context) (*NativeAuthConfig, error) {
	var out struct {
		NativeAuth NativeAuthConfig `json:"nativeAuth"`
	}
	if err := c.doJSON(ctx, requestSpec{
		method: http.MethodGet,
		path:   "/auth/config",
		noAuth: true,
	}, &out); err != nil {
		return nil, err
	}
	return &out.NativeAuth, nil
}

// BrowserLoginOptions tunes [Client.LoginBrowser]. A nil *BrowserLoginOptions
// is valid and means "all defaults".
type BrowserLoginOptions struct {
	// Timeout bounds the wait for the browser round trip.
	// Zero means [DefaultBrowserLoginTimeout].
	Timeout time.Duration

	// Notify, when set, is called once with the authorization URL just before
	// the browser is launched, and again with a non-nil err if the launch
	// failed. It exists so the CLI can print the URL for the user to paste;
	// this package never writes to stdout or stderr itself.
	Notify func(authorizeURL string, err error)

	// openBrowser overrides the system browser launcher. Tests set it; it is
	// unexported so no caller can be tricked into supplying one.
	openBrowser func(authorizeURL string) error
}

func (o *BrowserLoginOptions) timeout() time.Duration {
	if o == nil || o.Timeout <= 0 {
		return DefaultBrowserLoginTimeout
	}
	return o.Timeout
}

func (o *BrowserLoginOptions) notify(authorizeURL string, err error) {
	if o != nil && o.Notify != nil {
		o.Notify(authorizeURL, err)
	}
}

func (o *BrowserLoginOptions) launcher() func(string) error {
	if o != nil && o.openBrowser != nil {
		return o.openBrowser
	}
	return openSystemBrowser
}

// BrowserLogin is the outcome of a successful [Client.LoginBrowser].
type BrowserLogin struct {
	// Token is the kube-workspaces session token. Unlike every other delivery
	// path it arrives in a response body, because a native app cannot read the
	// HttpOnly kw-session cookie.
	Token string
	// ExpiresAt is when the token stops being accepted.
	ExpiresAt time.Time
	// Email is the authenticated user, as the server sees them.
	Email string
	// Role is the platform role granted to that user.
	Role string
}

// nativeTokenRequest is the POST /auth/native/token body.
type nativeTokenRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
}

// nativeTokenResponse is the POST /auth/native/token success body.
type nativeTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Email     string `json:"email"`
	Role      string `json:"role"`
}

// callbackResult is what the loopback listener observed.
type callbackResult struct {
	code             string
	state            string
	errCode          string
	errDescription   string
	stateWasSupplied bool
}

// LoginBrowser performs an RFC 8252 loopback + PKCE login against the instance
// and returns the resulting session token.
//
// It blocks until the user finishes in the browser, the context is cancelled,
// or the timeout elapses ([ErrBrowserLoginTimeout]). The client's own token is
// neither read nor written; persist the result and call [Client.SetToken].
//
// The loopback listener is bound before the browser is opened (so the port in
// the redirect is real) and torn down the moment the callback is answered.
//
// A completed login whose code the platform will not redeem is first
// re-exchanged against the same code (a store miss never consumes it, so
// riding the load balancer around to the issuing replica needs no new browser
// round trip) and then retried with a fresh login, up to [nativeLoginAttempts]
// times. The platform stores each single-use code on the API replica that
// finished the OIDC dance, so an exchange that reaches a different replica (or
// a replica mid-fallback) fails with invalid_code; its own handler treats that
// as "just restart the login", which is what this loop does. The verifier,
// challenge, state and code are regenerated every full restart — a used code
// is never re-sent. Cancelling the context (or exhausting the timeout) aborts
// between attempts.
func (c *Client) LoginBrowser(ctx context.Context, opts *BrowserLoginOptions) (*BrowserLogin, error) {
	// One deadline covers every attempt: consent screens and MFA prompts take
	// as long as the user takes, and a restart with a warm IdP session is far
	// faster than the original dance.
	ctx, cancel := context.WithTimeout(ctx, opts.timeout())
	defer cancel()

	var lastErr error
	for attempt := 1; attempt <= nativeLoginAttempts; attempt++ {
		login, err := c.loginBrowserOnce(ctx, opts)
		if err == nil {
			return login, nil
		}
		lastErr = err
		if !retryNativeExchange(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("kwclient: browser login: %w after %d attempts: %w; please try again", ErrNativeCodeUnredeemable, nativeLoginAttempts, lastErr)
}

// loginBrowserOnce runs one full RFC 8252 round trip: generate a fresh verifier
// and state, bind a loopback listener, open the system browser, and exchange
// the resulting single-use code for a session token.
func (c *Client) loginBrowserOnce(ctx context.Context, opts *BrowserLoginOptions) (*BrowserLogin, error) {
	verifier, err := newCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("kwclient: browser login: generate code verifier: %w", err)
	}
	state, err := newOAuthState()
	if err != nil {
		return nil, fmt.Errorf("kwclient: browser login: generate state: %w", err)
	}

	// Bind first: the redirect URI has to carry the port we actually got.
	// "127.0.0.1" rather than "localhost" per RFC 8252 §8.3 — and the API
	// rejects "localhost" anyway.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("kwclient: browser login: listen on loopback: %w", err)
	}

	redirectURI := loopbackRedirectURI(ln.Addr())
	authorizeURL := c.resolve("/auth/login", url.Values{
		"native_redirect":       {redirectURI},
		"code_challenge":        {CodeChallengeS256(verifier)},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}).String()

	results, shutdown := serveLoopbackCallback(ln)
	defer shutdown()

	// Launching the browser is best-effort: on a headless box, over SSH, or in
	// a stripped container there may be no handler at all. That is not fatal —
	// the user can paste the URL — so we report it and keep waiting.
	opts.notify(authorizeURL, nil)
	if err := opts.launcher()(authorizeURL); err != nil {
		opts.notify(authorizeURL, fmt.Errorf("%w: %v", ErrBrowserLaunchFailed, err))
	}

	var res callbackResult
	select {
	case res = <-results:
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("kwclient: browser login: %w after %s", ErrBrowserLoginTimeout, opts.timeout())
		}
		return nil, fmt.Errorf("kwclient: browser login: %w", ctx.Err())
	}

	// RFC 6749 §10.12. Checked before anything else is believed, and in
	// constant time so the comparison cannot be probed byte by byte.
	if !res.stateWasSupplied || subtle.ConstantTimeCompare([]byte(res.state), []byte(state)) != 1 {
		return nil, fmt.Errorf("kwclient: browser login: %w", ErrStateMismatch)
	}

	if res.errCode != "" {
		detail := res.errDescription
		if detail == "" {
			detail = res.errCode
		}
		return nil, fmt.Errorf("kwclient: browser login: %w: %s", ErrAuthorizationDenied, detail)
	}
	if res.code == "" {
		return nil, fmt.Errorf("kwclient: browser login: callback carried neither a code nor an error")
	}

	// Stop listening before the exchange: the port has done its job, and
	// RFC 8252 §8.3 wants it open only for the duration of the request.
	shutdown()

	login, err := c.exchangeNativeCode(ctx, res.code, verifier)
	if err == nil {
		return login, nil
	}

	// A code minted by one API replica cannot be redeemed from another when
	// the platform keeps codes per replica (in-memory on older builds, and on
	// the memory fallback during a K8s outage). A miss never consumes the
	// code, so re-exchanging the SAME code lets the load balancer land the
	// request on the issuing replica — no browser round trip, no repeated IdP
	// consent. invalid_verifier is deliberately not a miss: the store had
	// already surrendered the code before the PKCE check could run, so it can
	// never be redeemed again.
	if isNativeCodeMiss(err) {
		for i := 0; i < nativeExchangeAttempts; i++ {
			if login, err = c.exchangeNativeCode(ctx, res.code, verifier); err == nil {
				return login, nil
			}
			if !isNativeCodeMiss(err) {
				break
			}
		}
	}
	return nil, err
}

// retryNativeExchange reports whether a failed token exchange is worth another
// full login attempt.
//
// The platform keeps each single-use code in a store the exchanging replica
// must be able to read (per-replica in memory on older builds, with an
// in-memory fallback on K8s outage even on Secret-backed builds), so invalid_code
// does not mean the user did anything wrong — the exchange simply did not reach
// a replica holding the code, or the 60-second code TTL lapsed. [loginBrowserOnce]
// already re-exchanges an invalid_code against the same code before this is
// reached; what survives to a restart report is a code that no replica will
// redeem. invalid_verifier means the code was already consumed against a
// challenge we can no longer reproduce. The API's own handler documents the
// recovery for both: "a failed exchange just restarts the login". A fresh
// login is the remaining meaningful retry because a used code can never be
// redeemed again.
func retryNativeExchange(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == "invalid_code" || apiErr.Code == "invalid_verifier"
}

// isNativeCodeMiss reports whether a failed token exchange means the code was
// simply not present in the store the request reached — i.e. the exchange
// landed on an API replica that did not issue it. A miss is non-destructive
// (Redeem only deletes a found entry), so re-sending the same code is safe.
// invalid_verifier is deliberately not a miss: the store gave up the code
// before the PKCE check ran, so it can never be redeemed again.
func isNativeCodeMiss(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == "invalid_code"
}

// exchangeNativeCode redeems a single-use authorization code for the session
// token it stands for.
func (c *Client) exchangeNativeCode(ctx context.Context, code, verifier string) (*BrowserLogin, error) {
	var out nativeTokenResponse
	err := c.doJSON(ctx, requestSpec{
		method: http.MethodPost,
		path:   "/auth/native/token",
		body:   nativeTokenRequest{Code: code, CodeVerifier: verifier},
		// A stale token must not influence a fresh login, and this endpoint
		// authenticates with the code, not with a session.
		noAuth: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Token == "" {
		return nil, fmt.Errorf("kwclient: POST /auth/native/token: response carried no token")
	}

	result := &BrowserLogin{Token: out.Token, Email: out.Email, Role: out.Role}
	if out.ExpiresAt > 0 {
		result.ExpiresAt = time.Unix(out.ExpiresAt, 0)
	}
	return result, nil
}

// loopbackRedirectURI renders the redirect URI for a bound listener.
//
// It uses the address the listener reports rather than re-rendering
// "127.0.0.1", so the port is guaranteed to be the one we hold.
func loopbackRedirectURI(addr net.Addr) string {
	u := url.URL{Scheme: "http", Host: addr.String(), Path: nativeCallbackPath}
	return u.String()
}

// serveLoopbackCallback serves ln until the callback arrives. It returns a
// buffered channel that yields exactly one result, and a shutdown function that
// is safe to call more than once and blocks until the port is released.
func serveLoopbackCallback(ln net.Listener) (<-chan callbackResult, func()) {
	results := make(chan callbackResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != nativeCallbackPath {
			// Browsers cheerfully ask for /favicon.ico. Answering 404 without
			// completing the flow keeps those from ending the login early.
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		res := callbackResult{
			code:           q.Get("code"),
			state:          q.Get("state"),
			errCode:        q.Get("error"),
			errDescription: q.Get("error_description"),
		}
		res.stateWasSupplied = q.Has("state")

		if res.code == "" && res.errCode == "" {
			// A bare hit on the callback path (a prefetch, or a curious user)
			// is not the redirect we are waiting for.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("Waiting for a kube-workspaces login callback.\n"))
			return
		}

		writeCallbackPage(w, res)

		// Buffered and non-blocking: the first callback wins and any later one
		// is answered but ignored.
		select {
		case results <- res:
		default:
		}
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: nativeCallbackReadTimeout,
		ReadTimeout:       nativeCallbackReadTimeout,
		WriteTimeout:      nativeCallbackReadTimeout,
	}
	go func() { _ = srv.Serve(ln) }()

	var once sync.Once
	shutdown := func() {
		once.Do(func() {
			// Shutdown (not Close) so the response the browser is reading is
			// finished before the port goes away; the deadline stops a wedged
			// keep-alive connection from holding us forever.
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(sctx); err != nil {
				_ = srv.Close()
			}
		})
	}
	return results, shutdown
}

// writeCallbackPage renders the page the user is left looking at. It is
// deliberately self-contained: no network references, so it renders identically
// offline and cannot leak the visit to a third party.
func writeCallbackPage(w http.ResponseWriter, res callbackResult) {
	title, heading, detail := "Signed in", "You're signed in", "You can close this window and return to Kube Workspaces."
	status := http.StatusOK
	if res.errCode != "" {
		title, heading = "Sign-in failed", "Sign-in failed"
		detail = "You can close this window and try again in the terminal."
		status = http.StatusOK // the browser did its job; the IdP said no
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The page reflects nothing the IdP sent us, but a strict CSP and
	// no-referrer cost nothing and keep the authorization code out of any
	// outbound request.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<title>%s — Kube Workspaces</title>
<style>
 html{height:100%%}
 body{margin:0;height:100%%;display:flex;align-items:center;justify-content:center;
      font:16px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Ubuntu,sans-serif;
      color:#111;background:#f6f7f9}
 main{max-width:26rem;padding:2.5rem;text-align:center;background:#fff;
      border-radius:14px;box-shadow:0 1px 3px rgba(0,0,0,.08),0 8px 24px rgba(0,0,0,.06)}
 h1{margin:0 0 .5rem;font-size:1.25rem;font-weight:600}
 p{margin:0;color:#555}
 @media (prefers-color-scheme:dark){
  body{color:#e8e8ea;background:#151517} main{background:#1f1f22;box-shadow:none}
  p{color:#a9a9b2}}
</style></head>
<body><main><h1>%s</h1><p>%s</p></main></body></html>
`, title, heading, detail)
}

// newCodeVerifier returns a fresh PKCE code verifier (RFC 7636 §4.1).
//
// base64url without padding emits only "A-Z a-z 0-9 - _", a subset of the
// unreserved characters the grammar allows, so the value never needs escaping
// in a query string or a JSON body.
func newCodeVerifier() (string, error) {
	b := make([]byte, codeVerifierBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newOAuthState returns a fresh state parameter (RFC 6749 §10.12).
func newOAuthState() (string, error) {
	b := make([]byte, stateBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CodeChallengeS256 derives the PKCE challenge BASE64URL(SHA256(ASCII(verifier)))
// for a code verifier, per RFC 7636 §4.2.
func CodeChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// validCodeVerifier reports whether s satisfies the RFC 7636 §4.1 grammar:
// 43-128 characters from unreserved = ALPHA / DIGIT / "-" / "." / "_" / "~".
func validCodeVerifier(s string) bool {
	if len(s) < codeVerifierMinLen || len(s) > codeVerifierMaxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-', c == '.', c == '_', c == '~':
		default:
			return false
		}
	}
	return true
}

// openSystemBrowser launches the platform's default handler for an https URL.
//
// The URL is passed as a distinct argv element and never through a shell, so
// nothing in it can be interpreted as a command. We only ever pass URLs we
// built ourselves, but the flow's whole premise is that the browser is outside
// our trust boundary, so the launcher stays free of shell interpolation.
func openSystemBrowser(authorizeURL string) error {
	u, err := url.Parse(authorizeURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("refusing to open non-http(s) URL")
	}

	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{authorizeURL}
	case "windows":
		// rundll32 is the launcher that works without a shell and without
		// cmd.exe's "&" quoting minefield.
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", authorizeURL}
	default:
		// Linux and the BSDs: xdg-open is the freedesktop standard.
		name, args = "xdg-open", []string{authorizeURL}
	}

	path, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("%s not found on PATH", name)
	}
	cmd := exec.Command(path, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	// Reap the child so it does not linger as a zombie. xdg-open and open both
	// exit as soon as they have handed off to the browser.
	go func() { _ = cmd.Wait() }()
	return nil
}

// Supported reports whether the instance advertises the loopback+PKCE flow.
func (n *NativeAuthConfig) Supported() bool {
	if n == nil || !n.Enabled {
		return false
	}
	for _, m := range n.Methods {
		if strings.EqualFold(m, "loopback-pkce") {
			return true
		}
	}
	// Enabled with no method list still means "supported"; the list is
	// advisory and a future build may add to it.
	return len(n.Methods) == 0
}
