// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// SessionStatus reports whether a single-seat session (serial console or SSH)
// is currently held.
type SessionStatus struct {
	// InUse reports that somebody else holds the session.
	InUse bool `json:"inUse"`
}

// TakeoverResult is the outcome of stealing a single-seat session.
type TakeoverResult struct {
	// OK reports that the takeover was accepted.
	OK bool `json:"ok"`
	// WasInUse reports whether an existing session was actually evicted.
	WasInUse bool `json:"wasInUse"`
}

// okResponse is the {"ok":true} body returned by simple actions.
type okResponse struct {
	OK bool `json:"ok"`
}

// The console, SSH and reboot endpoints exist for VM workspaces only; calling
// them on a container workspace fails with [ErrNotVM].

// ConsoleStatus fetches GET /v1/workspaces/{name}/console/status.
func (c *Client) ConsoleStatus(ctx context.Context, namespace, name string) (*SessionStatus, error) {
	return c.sessionStatus(ctx, namespace, name, "console")
}

// ConsoleTakeover posts to /v1/workspaces/{name}/console/takeover, evicting any
// existing serial console session.
func (c *Client) ConsoleTakeover(ctx context.Context, namespace, name string) (*TakeoverResult, error) {
	return c.sessionTakeover(ctx, namespace, name, "console")
}

// SSHStatus fetches GET /v1/workspaces/{name}/ssh/status.
func (c *Client) SSHStatus(ctx context.Context, namespace, name string) (*SessionStatus, error) {
	return c.sessionStatus(ctx, namespace, name, "ssh")
}

// SSHTakeover posts to /v1/workspaces/{name}/ssh/takeover, evicting any
// existing SSH session.
func (c *Client) SSHTakeover(ctx context.Context, namespace, name string) (*TakeoverResult, error) {
	return c.sessionTakeover(ctx, namespace, name, "ssh")
}

// TODO(vnc): the server has no /vnc/status or /vnc/takeover endpoint yet; they
// are planned. Until then a busy VNC display is only discoverable by dialing
// [Client.DialVNC] and handling [ErrSessionInUse].

// Reboot posts to /v1/workspaces/{name}/reboot, restarting a VM workspace.
func (c *Client) Reboot(ctx context.Context, namespace, name string) error {
	spec := requestSpec{
		method: http.MethodPost,
		path:   workspacePath(name, "reboot"),
		query:  namespaceQuery(namespace),
	}
	var out okResponse
	if err := c.doJSON(ctx, spec, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("kwclient: %s: server did not acknowledge reboot", spec.op())
	}
	return nil
}

func (c *Client) sessionStatus(ctx context.Context, namespace, name, kind string) (*SessionStatus, error) {
	var out SessionStatus
	if err := c.doJSON(ctx, requestSpec{
		method: http.MethodGet,
		path:   workspacePath(name, kind, "status"),
		query:  namespaceQuery(namespace),
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) sessionTakeover(ctx context.Context, namespace, name, kind string) (*TakeoverResult, error) {
	var out TakeoverResult
	if err := c.doJSON(ctx, requestSpec{
		method: http.MethodPost,
		path:   workspacePath(name, kind, "takeover"),
		query:  namespaceQuery(namespace),
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// workspacePath builds /v1/workspaces/{name}/<sub...>.
func workspacePath(name string, sub ...string) string {
	p := "/v1/workspaces/" + url.PathEscape(name)
	for _, s := range sub {
		p += "/" + s
	}
	return p
}
