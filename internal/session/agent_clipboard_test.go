package session

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kube-workspaces/desktop-client/internal/agent"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

func TestAgentClipboardBesideConsoleAndRelease(t *testing.T) {
	var releases atomic.Int32
	var renewals atomic.Int32
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/attach"):
			raw, _ := json.Marshal(agent.Ticket{WorkspaceUID: "ws-1", WorkspaceGeneration: "gen-1", Audience: "workspace-agent", Role: "controller", ControlEpoch: 1, ExpiresAtNs: 4000000000000000000})
			_ = json.NewEncoder(w).Encode(kwclient.AgentTicket{ID: "id", Ticket: "id." + base64.RawURLEncoding.EncodeToString(raw) + ".sig", Protocol: 1, TTLMs: 1000})
		case strings.HasSuffix(r.URL.Path, "/release"):
			releases.Add(1)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.HasSuffix(r.URL.Path, "/renew"):
			renewals.Add(1)
			_, _ = w.Write([]byte(`{"protocol":1,"ttl_ms":1000}`))
		case strings.HasSuffix(r.URL.Path, "/agent/"):
			ws, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer ws.Close()
			send := func(kind string, payload map[string]any) error {
				body, _ := json.Marshal(agent.Envelope{Protocol: "kw-agent-v1", ProtocolVersion: 1, SessionID: "session", Type: kind, Payload: payload})
				header := make([]byte, 5)
				binary.BigEndian.PutUint32(header[1:], uint32(len(body)))
				return ws.WriteMessage(websocket.BinaryMessage, append(header, body...))
			}
			if err := send("hello", map[string]any{"clipboardText": true}); err != nil {
				return
			}
			text := "initial guest output"
			for {
				_, raw, err := ws.ReadMessage()
				if err != nil {
					return
				}
				var message agent.Envelope
				if len(raw) < 5 || json.Unmarshal(raw[5:], &message) != nil {
					t.Error("bad control frame")
					return
				}
				switch message.Type {
				case "attach":
					if err := send("attachResult", map[string]any{"admitted": true}); err != nil {
						return
					}
				case "clipboardSet":
					text, _ = message.Payload["text"].(string)
					if err := send("clipboardResult", map[string]any{"requestId": message.Payload["requestId"], "ok": true}); err != nil {
						return
					}
				case "clipboardGet":
					if err := send("clipboardResult", map[string]any{"requestId": message.Payload["requestId"], "ok": true, "text": text}); err != nil {
						return
					}
				case "bye":
					return
				default:
					t.Errorf("unexpected request: %s", message.Type)
					return
				}
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	client, err := kwclient.New(srv.URL, kwclient.WithToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	updates := make(chan string, 4)
	bridge, err := OpenAgentClipboard(context.Background(), client, "ns", "vm", func(text string) { updates <- text })
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	select {
	case text := <-updates:
		if text != "initial guest output" {
			t.Fatal("initial clipboard read mismatch")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("guest clipboard did not arrive")
	}
	if err := bridge.SetText("command λ 🦀\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-updates:
		if text != "command λ 🦀\r\n" {
			t.Fatal("host clipboard did not roundtrip")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("updated guest clipboard did not arrive")
	}
	select {
	case <-updates:
		t.Fatal("clipboard echoed unchanged text")
	case <-time.After(1200 * time.Millisecond):
	}
	bridge.Close()
	if releases.Load() != 1 || renewals.Load() == 0 {
		t.Fatalf("seat lifecycle release=%d renew=%d", releases.Load(), renewals.Load())
	}
}
