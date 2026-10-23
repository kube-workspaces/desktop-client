// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

func authConfigServer(t *testing.T, body string) *kwclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/config" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	client, err := kwclient.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestInteractiveLoginMethodSelectionErrors(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		browser bool
		method  string
		want    string
	}{
		{
			name:   "unknown method",
			config: `{"enabled":true,"methods":["local","oidc"]}`,
			method: "kerberos",
			want:   "unknown login method",
		},
		{
			name:   "local requested but unavailable",
			config: `{"enabled":true,"methods":["oidc"],"issuerURL":"https://idp.example.com"}`,
			method: "local",
			want:   "local authentication is not enabled",
		},
		{
			name:   "oidc requested but unavailable",
			config: `{"enabled":true,"methods":["local"],"localAuth":{"enabled":true}}`,
			method: "oidc",
			want:   "OIDC authentication is not enabled",
		},
		{
			name:    "local conflicts with browser",
			config:  `{"enabled":true,"methods":["local"],"localAuth":{"enabled":true}}`,
			browser: true,
			method:  "local",
			want:    "--method local cannot be combined with --browser",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := authConfigServer(t, tc.config)
			profile := &config.Profile{Name: "test", Server: "https://example.com"}
			_, err := interactiveLogin(context.Background(), client, profile, tc.browser, tc.method)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", err, tc.want)
			}
		})
	}
}
