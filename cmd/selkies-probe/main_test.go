// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/kube-workspaces/desktop-client/internal/selkies"
)

func TestDirectDoesNotSendPlatformCredentials(t *testing.T) {
	t.Setenv("KW_SESSION", "private-platform-token")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("platform credentials reached the direct agent")
		}
		if r.URL.Path != "/desk/api/websockets" {
			t.Errorf("path=%q", r.URL.Path)
		}
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		conn.WriteMessage(websocket.TextMessage, []byte("MODE webrtc"))
	}))
	defer srv.Close()
	if err := run(context.Background(), []string{"--direct", srv.URL, "--base-path", "/desk/"}); !errors.Is(err, selkies.ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--server", "http://example.test", "--direct", "http://localhost"},
		{"--direct", "http://localhost", "--workspace", "test"},
		{"--direct", "http://user:password@localhost"},
		{"--direct", "http://localhost/path"},
		{"--direct", "http://localhost?token=secret"},
		{"--direct", "http://localhost", "--duration", "0s"},
		{"--direct", "http://localhost", "--width", "65535"},
		{"--direct", "http://localhost", "--fps", "0"},
		{"--server", "http://localhost", "--workspace", "../other", "--namespace", "test"},
	} {
		if err := run(context.Background(), args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
