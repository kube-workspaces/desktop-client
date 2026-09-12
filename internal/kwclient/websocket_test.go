// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// echoUpgrader accepts the handshake and echoes one message back, so tests can
// prove the connection is usable and not just established.
func echoUpgrader(t *testing.T, subprotocols []string) (http.HandlerFunc, *http.Request) {
	t.Helper()
	seen := &http.Request{}
	up := websocket.Upgrader{
		// Mirrors the server: all three bridges accept any origin.
		CheckOrigin:  func(*http.Request) bool { return true },
		Subprotocols: subprotocols,
	}
	h := func(w http.ResponseWriter, r *http.Request) {
		*seen = *r.Clone(r.Context())
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		typ, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.WriteMessage(typ, msg)
	}
	return h, seen
}

func TestDialVNC(t *testing.T) {
	handler, seen := echoUpgrader(t, []string{"binary"})
	c := newTestClient(t, handler)

	conn, err := c.DialVNC(context.Background(), "demo", "dev")
	if err != nil {
		t.Fatalf("DialVNC: %v", err)
	}
	defer conn.Close()

	if got := seen.URL.Path; got != "/v1/workspaces/dev/vnc" {
		t.Errorf("path = %q, want /v1/workspaces/dev/vnc", got)
	}
	if got := seen.URL.Query().Get("namespace"); got != "demo" {
		t.Errorf("namespace = %q, want demo", got)
	}
	// The bridges accept the bearer header even though the browser uses the
	// cookie; the client sends both.
	if got := seen.Header.Get("Authorization"); got != "Bearer tok.sig" {
		t.Errorf("Authorization = %q, want Bearer tok.sig", got)
	}
	if ck, err := seen.Cookie(SessionCookieName); err != nil || ck.Value != "tok.sig" {
		t.Errorf("kw-session cookie = %v (err %v), want tok.sig", ck, err)
	}
	if got := seen.Header.Get("User-Agent"); got != defaultUserAgent {
		t.Errorf("User-Agent = %q, want %q", got, defaultUserAgent)
	}

	offered := seen.Header.Get("Sec-WebSocket-Protocol")
	for _, want := range vncSubprotocols {
		if !strings.Contains(offered, want) {
			t.Errorf("Sec-WebSocket-Protocol = %q, want it to offer %q", offered, want)
		}
	}
	if got := conn.Subprotocol(); got != "binary" {
		t.Errorf("negotiated subprotocol = %q, want binary", got)
	}

	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("RFB")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(msg) != "RFB" {
		t.Errorf("echo = %q, want RFB", msg)
	}
}

func TestDialTerminals(t *testing.T) {
	tests := []struct {
		name     string
		dial     func(c *Client) (*websocket.Conn, error)
		wantPath string
	}{
		{
			name: "exec",
			dial: func(c *Client) (*websocket.Conn, error) {
				return c.DialExec(context.Background(), "demo", "dev", 120, 40)
			},
			wantPath: "/v1/workspaces/dev/exec",
		},
		{
			name: "ssh",
			dial: func(c *Client) (*websocket.Conn, error) {
				return c.DialSSH(context.Background(), "demo", "dev", 120, 40)
			},
			wantPath: "/v1/workspaces/dev/ssh",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler, seen := echoUpgrader(t, nil)
			c := newTestClient(t, handler)

			conn, err := tc.dial(c)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()

			if seen.URL.Path != tc.wantPath {
				t.Errorf("path = %q, want %q", seen.URL.Path, tc.wantPath)
			}
			q := seen.URL.Query()
			if q.Get("namespace") != "demo" || q.Get("cols") != "120" || q.Get("rows") != "40" {
				t.Errorf("query = %v, want namespace=demo cols=120 rows=40", q)
			}
			// The terminal bridges stream raw bytes: no subprotocol offered.
			if got := seen.Header.Get("Sec-WebSocket-Protocol"); got != "" {
				t.Errorf("Sec-WebSocket-Protocol = %q, want none", got)
			}
			if got := conn.Subprotocol(); got != "" {
				t.Errorf("negotiated subprotocol = %q, want none", got)
			}
		})
	}
}

func TestDialWSErrorMapping(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		want        error
		wantMsg     string
	}{
		{
			name:        "409 session in use",
			status:      http.StatusConflict,
			contentType: "text/plain; charset=utf-8",
			body:        "VNC display is in use for this workspace",
			want:        ErrSessionInUse,
			wantMsg:     "VNC display is in use for this workspace",
		},
		{
			name:        "400 not a vm",
			status:      http.StatusBadRequest,
			contentType: "application/json",
			body:        `{"error":"vnc console is only available for VM workspaces"}`,
			want:        ErrNotVM,
			wantMsg:     "vnc console is only available for VM workspaces",
		},
		{
			name:        "401 unauthorized",
			status:      http.StatusUnauthorized,
			contentType: "text/plain; charset=utf-8",
			body:        "unauthorized",
			want:        ErrUnauthorized,
		},
		{
			name:        "403 forbidden",
			status:      http.StatusForbidden,
			contentType: "text/plain; charset=utf-8",
			body:        "forbidden",
			want:        ErrForbidden,
		},
		{
			name:        "503 cannot reach vm network",
			status:      http.StatusServiceUnavailable,
			contentType: "text/plain; charset=utf-8",
			body:        "cannot reach VM network",
			want:        ErrUnavailable,
			wantMsg:     "cannot reach VM network",
		},
		{
			name:        "502 bad gateway",
			status:      http.StatusBadGateway,
			contentType: "text/plain; charset=utf-8",
			body:        "upstream hung up",
			want:        ErrBadGateway,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				// These bridges reject before the upgrade, so there is no
				// close frame to inspect: only a plain HTTP status.
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				if _, err := w.Write([]byte(tc.body)); err != nil {
					t.Errorf("write: %v", err)
				}
			})

			conn, err := c.DialVNC(context.Background(), "demo", "dev")
			if conn != nil {
				conn.Close()
				t.Fatal("DialVNC returned a connection, want none")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("errors.Is(%v, %v) = false", err, tc.want)
			}

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("errors.As(%v, *APIError) = false", err)
			}
			if apiErr.StatusCode != tc.status {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tc.status)
			}
			if tc.wantMsg != "" && apiErr.Message != tc.wantMsg {
				t.Errorf("Message = %q, want %q", apiErr.Message, tc.wantMsg)
			}
			// The body text must reach the user-facing message.
			if !strings.Contains(err.Error(), tc.body) && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("Error() = %q, want it to include the response body", err.Error())
			}
		})
	}
}

// TestDialWSReturnsHandshakeResponse checks the caller can still inspect the
// failed handshake response (headers such as Retry-After) after the error.
func TestDialWSReturnsHandshakeResponse(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusConflict)
		if _, err := w.Write([]byte("serial console is in use")); err != nil {
			t.Errorf("write: %v", err)
		}
	})

	conn, resp, err := c.DialWS(context.Background(), workspacePath("dev", "console"), namespaceQuery("demo"), nil)
	if conn != nil {
		conn.Close()
		t.Fatal("want no connection")
	}
	if !errors.Is(err, ErrSessionInUse) {
		t.Fatalf("err = %v, want ErrSessionInUse", err)
	}
	if resp == nil {
		t.Fatal("resp = nil, want the handshake response")
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want 30", got)
	}
}

func TestDialWSSchemeConversion(t *testing.T) {
	tests := []struct {
		base string
		want string
	}{
		{base: "https://kw.example.com", want: "wss://kw.example.com/v1/workspaces/dev/vnc?namespace=demo"},
		{base: "http://localhost:8080", want: "ws://localhost:8080/v1/workspaces/dev/vnc?namespace=demo"},
		{base: "https://kw.example.com/kw", want: "wss://kw.example.com/kw/v1/workspaces/dev/vnc?namespace=demo"},
	}
	for _, tc := range tests {
		t.Run(tc.base, func(t *testing.T) {
			c, err := New(tc.base)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			u := c.resolve(workspacePath("dev", "vnc"), namespaceQuery("demo"))
			switch u.Scheme {
			case "https":
				u.Scheme = "wss"
			case "http":
				u.Scheme = "ws"
			}
			if got := u.String(); got != tc.want {
				t.Errorf("ws URL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDialWSHonorsContext(t *testing.T) {
	handler, _ := echoUpgrader(t, nil)
	c := newTestClient(t, handler)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	conn, _, err := c.DialWS(ctx, workspacePath("dev", "exec"), url.Values{}, nil)
	if conn != nil {
		conn.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
