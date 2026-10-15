package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
)

func TestControlOnlyAttachNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		advertise, request, acknowledge, wantError bool
	}{
		{"modern watcher", true, true, true, false},
		{"legacy watcher", false, true, false, false},
		{"premium unchanged", true, false, false, false},
		{"missing confirmation", true, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := make(chan Envelope, 1)
			upgrader := websocket.Upgrader{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer ws.Close()
				send := func(kind string, payload map[string]any) error {
					body, _ := json.Marshal(Envelope{Protocol: "kw-agent-v1", Type: kind, SessionID: "test", Payload: payload})
					frame := append([]byte{0}, uint32be(len(body))...)
					return ws.WriteMessage(websocket.BinaryMessage, append(frame, body...))
				}
				if err := send("hello", map[string]any{"controlOnlyAvailable": tc.advertise, "inputAvailable": true, "resizeAvailable": true, "clipboardText": true}); err != nil {
					return
				}
				_, raw, err := ws.ReadMessage()
				if err != nil {
					return
				}
				var message Envelope
				if len(raw) < 5 || json.Unmarshal(raw[5:], &message) != nil {
					t.Error("invalid attach frame")
					return
				}
				request <- message
				_ = send("attachResult", map[string]any{"admitted": true, "controlOnly": tc.acknowledge})
			}))
			defer srv.Close()
			conn := dialAgent(t, srv.URL)
			var session *Session
			var err error
			if tc.request {
				session, err = AttachControlOnly(conn, signedTestTicket())
			} else {
				session, err = Attach(conn, signedTestTicket())
			}
			if (err != nil) != tc.wantError {
				t.Fatalf("attach error = %v", err)
			}
			message := <-request
			if got := message.Payload["controlOnly"] == true; got != (tc.request && tc.advertise) {
				t.Fatalf("controlOnly request = %v", got)
			}
			if session != nil && tc.request && tc.advertise && (session.InputAvailable() || session.ResizeAvailable() || session.ClipboardAvailable()) {
				t.Fatal("control-only session must not expose guest mutation capabilities")
			}
		})
	}
}
