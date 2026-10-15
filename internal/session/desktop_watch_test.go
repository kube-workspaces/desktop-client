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

func TestDesktopWatcherCancellationAndTypedTransitions(t *testing.T) {
	for _, stallHello := range []bool{false, true} {
		t.Run(map[bool]string{false: "typed transitions", true: "cancel stalled hello"}[stallHello], func(t *testing.T) {
			var releases atomic.Int32
			connected := make(chan struct{})
			upgrader := websocket.Upgrader{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-token" {
					w.WriteHeader(401)
					return
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/attach"):
					raw, _ := json.Marshal(agent.Ticket{WorkspaceUID: "ws-1", WorkspaceGeneration: "gen-1", Role: "controller", Audience: "workspace-agent", ControlEpoch: 1, ExpiresAtNs: 4000000000000000000})
					_ = json.NewEncoder(w).Encode(kwclient.AgentTicket{ID: "id", Ticket: "id." + base64.RawURLEncoding.EncodeToString(raw) + ".sig", Protocol: 1, TTLMs: 60000})
				case strings.HasSuffix(r.URL.Path, "/release"):
					releases.Add(1)
					_, _ = w.Write([]byte(`{"ok":true}`))
				case strings.HasSuffix(r.URL.Path, "/agent/"):
					ws, err := upgrader.Upgrade(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer ws.Close()
					close(connected)
					send := func(kind string, payload map[string]any) error {
						body, _ := json.Marshal(agent.Envelope{Protocol: "kw-agent-v1", ProtocolVersion: 1, SessionID: "session", Type: kind, Payload: payload})
						header := make([]byte, 5)
						binary.BigEndian.PutUint32(header[1:], uint32(len(body)))
						return ws.WriteMessage(websocket.BinaryMessage, append(header, body...))
					}
					if !stallHello {
						if send("hello", map[string]any{"controlOnlyAvailable": true, "telemetryAvailable": true}) != nil {
							return
						}
					}
					for {
						_, raw, err := ws.ReadMessage()
						if err != nil {
							return
						}
						var message agent.Envelope
						if len(raw) < 5 || json.Unmarshal(raw[5:], &message) != nil {
							t.Error("bad frame")
							return
						}
						switch message.Type {
						case "attach":
							if message.Payload["controlOnly"] != true {
								t.Error("watcher requested media attach")
							}
							if send("attachResult", map[string]any{"admitted": true, "controlOnly": true}) != nil {
								return
							}
						case "telemetry":
							for _, payload := range []map[string]any{
								{"session": map[string]any{"desktop": map[string]any{"requiresConsole": true, "reason": "protected-desktop"}}},
								{"session": map[string]any{"desktop": map[string]any{"requiresConsole": true, "reason": "protected-desktop"}}},
								{"session": map[string]any{}}, // Missing classification is not a clear reading.
								{"session": map[string]any{"desktop": map[string]any{"requiresConsole": false, "reason": ""}}},
							} {
								if send("telemetry", payload) != nil {
									return
								}
							}
						default:
							t.Errorf("watcher sent unexpected %s", message.Type)
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
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			states := make(chan bool, 8)
			finished := make(chan error, 1)
			go func() {
				finished <- WatchAgentDesktop(ctx, client, "ns", "vm", func(required bool, _ string) { states <- required })
			}()
			select {
			case <-connected:
			case <-time.After(2 * time.Second):
				t.Fatal("watcher never connected")
			}
			if !stallHello {
				for _, want := range []bool{true, false} {
					select {
					case got := <-states:
						if got != want {
							t.Fatalf("classification=%v want=%v", got, want)
						}
					case <-time.After(2 * time.Second):
						t.Fatal("missing typed classification")
					}
				}
			}
			cancel()
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("watcher cancellation stalled")
			}
			if releases.Load() != 1 {
				t.Fatalf("ticket releases=%d", releases.Load())
			}
			select {
			case <-states:
				t.Fatal("duplicate or malformed classification was forwarded")
			default:
			}
		})
	}
}
