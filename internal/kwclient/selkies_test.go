// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSelkiesPath(t *testing.T) {
	for _, base := range []string{"", "/", "/desktop", "/desktop/", "/a/b-c_1/"} {
		got, err := SelkiesPath("alice", "desktop-0", base)
		if err != nil || got[:len("/proxy/alice/desktop-0/")] != "/proxy/alice/desktop-0/" {
			t.Fatalf("base %q: %q %v", base, got, err)
		}
	}
	got, _ := SelkiesPath("alice", "desktop-0", "/desktop/")
	if got != "/proxy/alice/desktop-0/desktop/api/websockets" {
		t.Fatal(got)
	}
	for _, base := range []string{"https://evil.test/", "//evil.test/", "/../", "/a/./b", "/a//b", "/%2e%2e/", "/a?token=x", "/a#x", "/a\\b", "/a\nb"} {
		if _, err := SelkiesPath("alice", "desktop-0", base); err == nil {
			t.Errorf("accepted unsafe base %q", base)
		}
	}
	for _, name := range []string{"", "../bob", "a/b", "a%2fb", "-a", "a-", "a.b"} {
		if _, err := SelkiesPath("alice", name, "/"); err == nil {
			t.Errorf("accepted invalid workspace %q", name)
		}
	}
}

func TestDialSelkiesAuthorizationAndStatus(t *testing.T) {
	for _, code := range []int{401, 403, 409, 502, 503} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/proxy/alice/desktop-0/api/websockets" || r.Header.Get("Authorization") != "Bearer test-token" || r.URL.RawQuery != "" {
				t.Errorf("unexpected Selkies upgrade: %s", r.URL.Path)
			}
			if r.Header.Get("Sec-WebSocket-Protocol") != "" {
				t.Error("Selkies must not offer RFB subprotocols")
			}
			w.WriteHeader(code)
		}))
		c, err := New(srv.URL, WithToken("test-token"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.DialSelkies(context.Background(), "alice", "desktop-0", "/")
		srv.Close()
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != code {
			t.Fatalf("status %d: %v", code, err)
		}
	}
}
