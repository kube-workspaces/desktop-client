// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Version is the client version reported in the default User-Agent.
const Version = "0.2.0"

const (
	// SessionCookieName is the cookie the API sets on login and accepts on
	// every endpoint.
	SessionCookieName = "kw-session"

	// AllNamespaces is the magic ?namespace= value that makes the API list
	// workspaces across every namespace the caller can see. It is the client's
	// default for list calls.
	AllNamespaces = "_all"
)

const (
	defaultTimeout          = 30 * time.Second
	defaultHandshakeTimeout = 30 * time.Second
	defaultUserAgent        = "kube-workspaces-desktop/" + Version
)

// Client talks to a kube-workspaces API server.
//
// A Client is safe for concurrent use; the session token may be replaced at any
// time with [Client.SetToken] (for example after a re-login) without
// invalidating in-flight requests.
type Client struct {
	baseURL          *url.URL
	httpc            *http.Client
	userAgent        string
	insecure         bool
	handshakeTimeout time.Duration

	mu    sync.RWMutex
	token string
}

// config collects the values set by [Option] before the Client is built.
type config struct {
	token            string
	httpc            *http.Client
	insecure         bool
	userAgent        string
	timeout          time.Duration
	timeoutSet       bool
	handshakeTimeout time.Duration
}

// An Option customizes a [Client] created by [New].
type Option func(*config)

// WithToken sets the initial session token.
func WithToken(token string) Option {
	return func(c *config) { c.token = token }
}

// WithHTTPClient supplies the underlying *http.Client.
//
// The client is used as-is (its Transport, CookieJar and CheckRedirect are
// respected); [WithInsecureSkipVerify] then only affects the WebSocket dialer,
// since the caller owns the transport's TLS configuration.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *config) { c.httpc = hc }
}

// WithInsecureSkipVerify disables TLS certificate verification.
//
// This exists for self-signed development clusters only; it must never be the
// default in a shipped build.
func WithInsecureSkipVerify(insecure bool) Option {
	return func(c *config) { c.insecure = insecure }
}

// WithUserAgent overrides the User-Agent header sent on every request.
func WithUserAgent(ua string) Option {
	return func(c *config) {
		if ua != "" {
			c.userAgent = ua
		}
	}
}

// WithTimeout sets the per-request timeout for REST calls and the default
// WebSocket handshake timeout.
//
// It never applies to an established WebSocket connection, which is long-lived
// by design.
func WithTimeout(d time.Duration) Option {
	return func(c *config) {
		c.timeout = d
		c.timeoutSet = true
	}
}

// WithHandshakeTimeout sets the WebSocket handshake timeout independently of
// [WithTimeout].
func WithHandshakeTimeout(d time.Duration) Option {
	return func(c *config) { c.handshakeTimeout = d }
}

// New creates a Client for the given API base URL, e.g.
// "https://kw.example.com".
//
// The base URL is the API origin only: paths such as /v1/workspaces are
// appended by the client. A trailing slash is accepted and trimmed.
func New(baseURL string, opts ...Option) (*Client, error) {
	cfg := config{
		userAgent:        defaultUserAgent,
		timeout:          defaultTimeout,
		handshakeTimeout: defaultHandshakeTimeout,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("kwclient: parse base URL %q: %w", baseURL, err)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return nil, fmt.Errorf("kwclient: base URL %q: scheme must be http or https", baseURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("kwclient: base URL %q: missing host", baseURL)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""

	hc := cfg.httpc
	switch {
	case hc == nil:
		tr, _ := http.DefaultTransport.(*http.Transport)
		if tr != nil {
			tr = tr.Clone()
		} else {
			tr = &http.Transport{}
		}
		if cfg.insecure {
			tr.TLSClientConfig = insecureTLSConfig()
		}
		hc = &http.Client{
			Transport: tr,
			Timeout:   cfg.timeout,
			// Stop at the first redirect: the login response carries the
			// session token in a Set-Cookie header, and a followed redirect
			// (without a cookie jar) would drop it on the floor.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	case cfg.timeoutSet:
		// Do not mutate a client owned by the caller.
		clone := *hc
		clone.Timeout = cfg.timeout
		hc = &clone
	}

	if cfg.handshakeTimeout <= 0 {
		cfg.handshakeTimeout = defaultHandshakeTimeout
	}

	return &Client{
		baseURL:          u,
		httpc:            hc,
		userAgent:        cfg.userAgent,
		insecure:         cfg.insecure,
		handshakeTimeout: cfg.handshakeTimeout,
		token:            cfg.token,
	}, nil
}

// BaseURL returns the API base URL, without a trailing slash.
func (c *Client) BaseURL() string { return c.baseURL.String() }

// UserAgent returns the User-Agent sent with every request.
func (c *Client) UserAgent() string { return c.userAgent }

// Token returns the current session token, or "" if the client is anonymous.
func (c *Client) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// SetToken replaces the session token. Passing "" makes the client anonymous.
func (c *Client) SetToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
}

// Claims decodes the current session token locally. It returns an error
// wrapping [ErrInvalidToken] when no usable token is set.
func (c *Client) Claims() (*TokenClaims, error) {
	return ParseToken(c.Token())
}

// insecureTLSConfig returns the TLS configuration used when certificate
// verification is disabled.
func insecureTLSConfig() *tls.Config {
	// #nosec G402 -- opt-in, for self-signed dev clusters only.
	return &tls.Config{InsecureSkipVerify: true}
}

// tlsConfig returns the TLS settings the WebSocket dialer should use so that
// WS connections trust exactly what REST calls trust.
//
// ALPN is deliberately forced back to HTTP/1.1. Go's default transport
// advertises "h2", and a server that accepts it (as an nginx ingress does)
// will speak HTTP/2 — in which the HTTP/1.1 Upgrade handshake that WebSocket
// relies on does not exist. Sharing the REST transport's tls.Config verbatim
// therefore makes every WebSocket dial fail with a malformed-response error
// containing a raw HTTP/2 settings frame.
func (c *Client) tlsConfig() *tls.Config {
	var cfg *tls.Config
	switch {
	case c.insecure:
		cfg = insecureTLSConfig()
	default:
		if tr, ok := c.httpc.Transport.(*http.Transport); ok && tr.TLSClientConfig != nil {
			cfg = tr.TLSClientConfig.Clone()
		}
	}
	if cfg == nil {
		cfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	cfg.NextProtos = []string{"http/1.1"}
	return cfg
}

// resolve builds an absolute URL for an API path plus query.
func (c *Client) resolve(path string, query url.Values) *url.URL {
	u := *c.baseURL
	u.Path = c.baseURL.Path + "/" + strings.TrimPrefix(path, "/")
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return &u
}

// requestSpec describes a single REST call.
type requestSpec struct {
	method string
	path   string
	query  url.Values
	// body, when non-nil, is marshaled as JSON.
	body any
	// sentinel, when non-nil, maps non-2xx statuses on this request to errors
	// instead of the default [sentinelForStatus]. It exists so endpoints that
	// reuse a shared status code with a different meaning (shared display
	// membership 409s, 400s, 404s) can surface distinct sentinel errors.
	sentinel func(int) error
	// noAuth suppresses the credentials headers, for endpoints that are
	// unauthenticated (/auth/config) or that must not see a stale token
	// (/auth/login/local).
	noAuth bool
}

// op renders the request for error messages.
func (s requestSpec) op() string { return s.method + " " + s.path }

// statusSentinel returns the sentinel mapper for this request, defaulting to
// the shared one so most endpoints need not specify an override.
func (s requestSpec) statusSentinel() func(int) error {
	if s.sentinel != nil {
		return s.sentinel
	}
	return sentinelForStatus
}

func (c *Client) newRequest(ctx context.Context, spec requestSpec) (*http.Request, error) {
	var body io.Reader
	if spec.body != nil {
		buf, err := json.Marshal(spec.body)
		if err != nil {
			return nil, fmt.Errorf("kwclient: %s: encode request body: %w", spec.op(), err)
		}
		body = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, spec.method, c.resolve(spec.path, spec.query).String(), body)
	if err != nil {
		return nil, fmt.Errorf("kwclient: %s: %w", spec.op(), err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if spec.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if spec.method == http.MethodGet || spec.method == http.MethodHead {
		// Workspace state changes constantly; never serve it from a cache.
		req.Header.Set("Cache-Control", "no-store")
	}
	if !spec.noAuth {
		c.authenticate(req.Header)
	}
	return req, nil
}

// authenticate adds both credential forms to h.
//
// /v1/*, /auth/me and /auth/change-password all accept the bearer token as
// well as the cookie, so sending both keeps a single code path for all endpoints.
func (c *Client) authenticate(h http.Header) {
	token := c.Token()
	if token == "" {
		return
	}
	h.Set("Authorization", "Bearer "+token)
	h.Set("Cookie", SessionCookieName+"="+token)
}

// do performs the request and maps non-2xx responses to an [*APIError]. On a
// nil error the caller owns (and must close) the response body.
func (c *Client) do(ctx context.Context, spec requestSpec) (*http.Response, error) {
	req, err := c.newRequest(ctx, spec)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		// Surface context cancellation unwrapped-but-joined so callers can
		// test both errors.Is(err, context.Canceled) and their own conditions.
		return nil, fmt.Errorf("kwclient: %s: %w", spec.op(), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer func() { _ = resp.Body.Close() }()
		return nil, newAPIError(spec.op(), resp, spec.statusSentinel())
	}
	return resp, nil
}

// doJSON performs the request and decodes a JSON response into out. A nil out
// discards the body.
func (c *Client) doJSON(ctx context.Context, spec requestSpec, out any) error {
	resp, err := c.do(ctx, spec)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return decodeJSON(spec.op(), resp, out)
}

// decodeJSON decodes resp.Body into out, draining the body either way so the
// connection can be reused.
func decodeJSON(op string, resp *http.Response, out any) error {
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("kwclient: %s: empty response body", op)
		}
		return fmt.Errorf("kwclient: %s: decode response: %w", op, err)
	}
	return nil
}

// namespaceQuery builds ?namespace=<ns>, substituting the default when empty.
func namespaceQuery(ns string) url.Values {
	if ns == "" {
		ns = AllNamespaces
	}
	return url.Values{"namespace": []string{ns}}
}
