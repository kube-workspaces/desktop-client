// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// This file implements the client half of the browser-session grant: handing a
// session this client holds to a browser that has not seen it. Container
// workspaces are web applications, and "Open in browser" opens them in the
// system browser; but the browser's only credential is the kw-session cookie,
// and the client logged in over the loopback flow or with --token, so the
// browser has none. Opening the /proxy/ URL bare would land on a 401.
//
// The flow is the mirror image of the RFC 8252 native login: instead of the
// browser handing the app a single-use code, the app hands the browser one.
//
//  1. [Client.GrantBrowserSession] POSTs /auth/browser-session/grant,
//     authenticated with the session token this client holds, and the server
//     parks that token behind a single-use code,
//  2. the returned URL (/auth/browser-session?code=...) is opened in the
//     browser,
//  3. the server redeems the code once, sets the kw-session cookie exactly as
//     a login would, and redirects the browser to the requested /proxy/...
//     workspace URL.
//
// The browser only ever sees the single-use code; the 24-hour session token
// never appears in a URL, browser history or Referer header.

// BrowserSessionGrant is the outcome of a successful [Client.GrantBrowserSession].
type BrowserSessionGrant struct {
	// Code is the single-use code the server will redeem for a session cookie.
	Code string
	// ExpiresAt is when the server stops accepting the code. Zero when the
	// server did not report one.
	ExpiresAt time.Time
	// URL is the same-origin /auth/browser-session?code=... URL that, opened
	// in a browser, sets the kw-session cookie and redirects to the workspace.
	URL string
}

// GrantBrowserSession asks the server to hand this client's session to the
// browser, in exchange for a single-use code and the URL that redeems it.
//
// redirect is the root-relative /proxy/... path the browser should end up on
// (see [Client.WorkspacePath]); the server validates it before minting the
// code and stores it, keeping the redeem step from trusting a client-supplied
// target.
func (c *Client) GrantBrowserSession(ctx context.Context, redirect string) (*BrowserSessionGrant, error) {
	if redirect == "" {
		return nil, fmt.Errorf("kwclient: POST /auth/browser-session/grant: redirect is required")
	}
	var out struct {
		Code      string `json:"code"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := c.doJSON(ctx, requestSpec{
		method: http.MethodPost,
		path:   "/auth/browser-session/grant",
		body:   map[string]string{"redirect": redirect},
	}, &out); err != nil {
		return nil, err
	}
	if out.Code == "" {
		return nil, fmt.Errorf("kwclient: POST /auth/browser-session/grant: response carried no code")
	}

	grant := &BrowserSessionGrant{
		Code: out.Code,
		URL:  c.resolve("/auth/browser-session", url.Values{"code": {out.Code}}).String(),
	}
	if out.ExpiresAt > 0 {
		grant.ExpiresAt = time.Unix(out.ExpiresAt, 0)
	}
	return grant, nil
}

// WorkspacePath returns the root-relative proxy path for a workspace, e.g.
// /proxy/{namespace}/{name}/?folder=/workspace, the same path [Client.ProxyURL]
// would open. It is what "Open in browser" asks a browser-session grant to
// redirect to.
//
// The workspace proxy is mounted at the origin root (the frontend's /proxy/*
// route), so the path is independent of any base path the API client is
// configured with.
func (c *Client) WorkspacePath(ws Workspace, img *Image) string {
	path := ""
	if img != nil {
		path = img.DefaultPath
	}
	return "/proxy/" + ws.Namespace + "/" + ws.Name + "/" + strings.TrimPrefix(path, "/")
}
