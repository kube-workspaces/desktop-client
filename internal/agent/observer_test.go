package agent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/kube-workspaces/desktop-client/internal/wsio"
)

func TestObserverAttachRequiresNoGuestTicketAndBlocksMutations(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		send := func(raw string) {
			body := []byte(raw)
			_ = ws.WriteMessage(websocket.BinaryMessage, append(append([]byte{0}, uint32be(len(body))...), body...))
		}
		send(`{"protocol":"kw-agent-v1","sessionId":"s","type":"hello","payload":{"observer":true,"inputAvailable":true,"resizeAvailable":true,"clipboardText":true}}`)
		if _, _, err := ws.ReadMessage(); err != nil {
			return
		}
		send(`{"type":"attachResult","payload":{"admitted":true,"role":"observer"}}`)
		for {
			_, _, err := ws.ReadMessage()
			if err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := AttachObserver(wsio.New(ws))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close() //nolint:errcheck
	if session.InputAvailable() || session.ResizeAvailable() || session.ClipboardAvailable() || session.TelemetryAvailable() {
		t.Fatal("observer gained guest capabilities")
	}
	for _, kind := range []string{"input", "resizeRequest", "clipboardGet", "clipboardSet", "telemetry", "encoderConfig"} {
		if session.writeControl(kind, map[string]any{}) == nil {
			t.Fatalf("observer sent %s", kind)
		}
	}
}
