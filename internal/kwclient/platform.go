// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

// Screenshot fetches one PNG without taking over an active display. A busy
// capture lease returns ErrSessionInUse; callers should retain a cached image.
func (c *Client) Screenshot(ctx context.Context, namespace, name string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := c.do(ctx, requestSpec{method: http.MethodGet, path: workspacePath(name, "vnc", "screenshot"), query: namespaceQuery(namespace), sentinel: sentinelForWSStatus})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	const limit = 32 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("kwclient: screenshot: %w", err)
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("kwclient: screenshot exceeds 32 MiB")
	}
	if _, err := png.DecodeConfig(bytes.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("kwclient: screenshot is not a PNG: %w", err)
	}
	return raw, nil
}

func tcpQuery(namespace string, port int) (url.Values, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("kwclient: TCP port must be between 1 and 65535: %w", ErrInvalidRequest)
	}
	query := namespaceQuery(namespace)
	query.Set("port", strconv.Itoa(port))
	return query, nil
}

// DialTCP opens the VM's raw TCP bridge. Send binary frames only, or wrap it
// with wsio.New for stream I/O. The guest port must be masquerade-forwarded.
// Contention returns ErrSessionInUse; takeover requires explicit user consent.
func (c *Client) DialTCP(ctx context.Context, namespace, name string, port int) (*websocket.Conn, error) {
	query, err := tcpQuery(namespace, port)
	if err != nil {
		return nil, err
	}
	conn, _, err := c.DialWS(ctx, workspacePath(name, "tcp"), query, nil)
	return conn, err
}

// TCPStatus reports whether this workspace/port's bridge is occupied.
func (c *Client) TCPStatus(ctx context.Context, namespace, name string, port int) (*SessionStatus, error) {
	query, err := tcpQuery(namespace, port)
	if err != nil {
		return nil, err
	}
	var out SessionStatus
	if err := c.doJSON(ctx, requestSpec{method: http.MethodGet, path: workspacePath(name, "tcp", "status"), query: query}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TCPTakeover evicts an existing TCP bridge; call only after user consent.
func (c *Client) TCPTakeover(ctx context.Context, namespace, name string, port int) (*TakeoverResult, error) {
	query, err := tcpQuery(namespace, port)
	if err != nil {
		return nil, err
	}
	var out TakeoverResult
	if err := c.doJSON(ctx, requestSpec{method: http.MethodPost, path: workspacePath(name, "tcp", "takeover"), query: query}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
