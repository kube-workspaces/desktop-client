// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
)

// Subprotocols offered to the KubeVirt-backed VNC bridge. "binary" is what the
// browser client negotiates; "plain.kubevirt.io" is accepted by virt-api.
var vncSubprotocols = []string{"binary", "plain.kubevirt.io"}

// DialWS opens a WebSocket to an API path.
//
// The base URL scheme is switched to ws/wss, and both credential forms are sent
// (the auth middleware wraps the whole mux and accepts the bearer header when
// the cookie is absent). No Origin header is set: all three upgraders run with
// CheckOrigin returning true.
//
// The returned *http.Response is the handshake response; it is non-nil on a
// failed handshake and carries the (already buffered) error body.
//
// Handshake failures are mapped to an [*APIError] wrapping [ErrSessionInUse]
// (409), [ErrUnauthorized] (401), [ErrForbidden] (403), [ErrNotVM] (400),
// [ErrUnavailable] (503) or [ErrBadGateway] (502). These bridges reject before
// the upgrade with a plain HTTP status, never with a close frame, so this is
// the only place such failures surface.
func (c *Client) DialWS(ctx context.Context, path string, query url.Values, subprotocols []string) (*websocket.Conn, *http.Response, error) {
	u := c.resolve(path, query)
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}

	header := http.Header{}
	header.Set("User-Agent", c.userAgent)
	c.authenticate(header)

	dialer := &websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: c.handshakeTimeout,
		TLSClientConfig:  c.tlsConfig(),
		// Subprotocols must go through the dialer: gorilla rejects a manually
		// set Sec-WebSocket-Protocol header.
		Subprotocols: subprotocols,
	}

	op := "WS " + path
	conn, resp, err := dialer.DialContext(ctx, u.String(), header)
	if err != nil {
		if errors.Is(err, websocket.ErrBadHandshake) && resp != nil {
			return nil, resp, wsHandshakeError(op, resp)
		}
		return nil, resp, fmt.Errorf("kwclient: %s: %w", op, err)
	}
	return conn, resp, nil
}

// wsHandshakeError converts a failed handshake response into an [*APIError].
//
// gorilla has already replaced resp.Body with an in-memory reader holding the
// first 1 KiB of the body, so reading it here cannot block.
func wsHandshakeError(op string, resp *http.Response) error {
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	// Re-arm the body so callers inspecting the response still see it.
	resp.Body = io.NopCloser(strings.NewReader(string(raw)))
	return apiErrorFromBody(op, resp.StatusCode, raw, sentinelForWSStatus)
}

// DialVNC opens the VNC bridge of a VM workspace
// (/v1/workspaces/{name}/vnc).
//
// A busy display fails with [ErrSessionInUse]; there is no VNC takeover
// endpoint yet, so the caller must ask the user to retry later.
func (c *Client) DialVNC(ctx context.Context, namespace, name string) (*websocket.Conn, error) {
	conn, _, err := c.DialWS(ctx, workspacePath(name, "vnc"), namespaceQuery(namespace), vncSubprotocols)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// DialExec opens an exec terminal to a workspace
// (/v1/workspaces/{name}/exec) with the given initial terminal size.
//
// No subprotocol is negotiated: the bridge streams raw PTY bytes.
func (c *Client) DialExec(ctx context.Context, namespace, name string, cols, rows uint16) (*websocket.Conn, error) {
	conn, _, err := c.DialWS(ctx, workspacePath(name, "exec"), terminalQuery(namespace, cols, rows), nil)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// DialSSH opens an SSH terminal to a VM workspace
// (/v1/workspaces/{name}/ssh) with the given initial terminal size.
//
// No subprotocol is negotiated. A session already in use fails with
// [ErrSessionInUse]; use [Client.SSHTakeover] to steal it.
func (c *Client) DialSSH(ctx context.Context, namespace, name string, cols, rows uint16) (*websocket.Conn, error) {
	conn, _, err := c.DialWS(ctx, workspacePath(name, "ssh"), terminalQuery(namespace, cols, rows), nil)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// terminalQuery builds ?namespace=&cols=&rows= for the terminal bridges.
func terminalQuery(namespace string, cols, rows uint16) url.Values {
	q := namespaceQuery(namespace)
	q.Set("cols", strconv.FormatUint(uint64(cols), 10))
	q.Set("rows", strconv.FormatUint(uint64(rows), 10))
	return q
}
