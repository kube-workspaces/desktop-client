// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
)

// SelkiesPath constructs an instance-relative proxy endpoint for the pinned
// Selkies protocol. AgentBase is an absolute path relative to the guest origin,
// not a URL. It is deliberately conservative: no encoding, queries, traversal,
// empty segments, or authority syntax can change the credential destination.
func SelkiesPath(namespace, name, agentBase string) (string, error) {
	if !dnsLabel(namespace) || !dnsLabel(name) {
		return "", errors.New("kwclient: Selkies requires DNS-label namespace and workspace name")
	}
	endpoint, err := SelkiesAgentPath(agentBase)
	if err != nil {
		return "", err
	}
	return "/proxy/" + namespace + "/" + name + endpoint, nil
}

// SelkiesAgentPath resolves the agent-relative base path to its data socket.
func SelkiesAgentPath(base string) (string, error) {
	if base == "" {
		base = "/"
	}
	if !strings.HasPrefix(base, "/") || strings.Contains(base, "//") {
		return "", errors.New("kwclient: invalid Selkies base path")
	}
	trimmed := strings.Trim(base, "/")
	if trimmed != "" {
		for _, part := range strings.Split(trimmed, "/") {
			if part == "." || part == ".." || part == "" {
				return "", errors.New("kwclient: invalid Selkies path segment")
			}
			for _, c := range part {
				switch {
				case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
				default:
					return "", errors.New("kwclient: invalid character in Selkies base path")
				}
			}
		}
		return "/" + trimmed + "/api/websockets", nil
	}
	return "/api/websockets", nil
}

func dnsLabel(s string) bool {
	if len(s) == 0 || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}

// DialSelkies opens the agent socket through the instance proxy. This is probe
// groundwork only: it neither discovers capability nor claims a display slot.
func (c *Client) DialSelkies(ctx context.Context, namespace, name, agentBase string) (*websocket.Conn, error) {
	path, err := SelkiesPath(namespace, name, agentBase)
	if err != nil {
		return nil, err
	}
	conn, _, err := c.DialWS(ctx, path, nil, nil)
	return conn, err
}

// Tier1Takeover force-ends the active Tier 1 transport session for the
// workspace, if any.
func (c *Client) Tier1Takeover(ctx context.Context, namespace, name string) error {
	return c.doJSON(ctx, requestSpec{
		method: http.MethodPost,
		path:   workspacePath(name, "tier1/takeover"),
		query:  namespaceQuery(namespace),
	}, nil)
}
