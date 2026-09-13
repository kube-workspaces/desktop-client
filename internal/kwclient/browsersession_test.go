// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestGrantBrowserSession(t *testing.T) {
	var gotPath, gotBody, gotToken string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotToken = r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		writeJSON(t, w, http.StatusOK, `{"code":"abc123","expires_at":1770000000}`)
	})

	grant, err := c.GrantBrowserSession(context.Background(), "/proxy/team/code/?folder=/workspace")
	if err != nil {
		t.Fatalf("GrantBrowserSession: %v", err)
	}
	if gotPath != "/auth/browser-session/grant" {
		t.Errorf("path = %q, want /auth/browser-session/grant", gotPath)
	}
	if gotToken != "Bearer tok.sig" {
		t.Errorf("Authorization = %q, want Bearer tok.sig", gotToken)
	}
	var sent struct {
		Redirect string `json:"redirect"`
	}
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("request body %q is not JSON: %v", gotBody, err)
	}
	if sent.Redirect != "/proxy/team/code/?folder=/workspace" {
		t.Errorf("redirect = %q, want /proxy/team/code/?folder=/workspace", sent.Redirect)
	}

	if grant.Code != "abc123" {
		t.Errorf("Code = %q, want abc123", grant.Code)
	}
	if want := c.resolve("/auth/browser-session", url.Values{"code": {"abc123"}}).String(); grant.URL != want {
		t.Errorf("URL = %q, want %q", grant.URL, want)
	}
	if grant.ExpiresAt.IsZero() {
		t.Error("ExpiresAt is zero, want a parsed timestamp")
	}
}

func TestGrantBrowserSessionRejected(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Unauthorized","message":"authentication required"}`))
	})

	if _, err := c.GrantBrowserSession(context.Background(), "/proxy/team/code/"); err == nil {
		t.Fatal("GrantBrowserSession succeeded, want an error for a 401")
	}
}

func TestGrantBrowserSessionRejectsEmptyRedirect(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a request was sent despite an empty redirect")
	})
	if _, err := c.GrantBrowserSession(context.Background(), ""); err == nil {
		t.Fatal("GrantBrowserSession succeeded, want an error for an empty redirect")
	}
}

func TestWorkspacePath(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("WorkspacePath is not a network call")
	})
	ws := Workspace{Name: "code", Namespace: "team"}
	img := &Image{Image: "img/code", DefaultPath: "?folder=/workspace"}

	if got := c.WorkspacePath(ws, img); got != "/proxy/team/code/?folder=/workspace" {
		t.Errorf("WorkspacePath with default path = %q", got)
	}
	if got := c.WorkspacePath(ws, nil); got != "/proxy/team/code/" {
		t.Errorf("WorkspacePath without image = %q", got)
	}
}

func TestWorkspaceURLIsGrantCompatible(t *testing.T) {
	// The browser is opened at the redeem URL, whose redirect target comes
	// from WorkspacePath; WorkspaceURL must point the same way, so a user
	// told "visit <url>" lands somewhere the grant understands.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {})
	ws := Workspace{Name: "code", Namespace: "team"}
	img := &Image{Image: "img/code", DefaultPath: "?folder=/workspace"}

	want := c.WorkspaceURL(ws, img)
	if !strings.Contains(want, c.WorkspacePath(ws, img)) {
		t.Errorf("WorkspaceURL %q does not contain WorkspacePath %q", want, c.WorkspacePath(ws, img))
	}
}
