package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/kube-workspaces/desktop-client/internal/wsio"
)

// fakeAgent speaks the framed protocol over a gorilla server endpoint:
// hello, ticket-gated attach, paired resize ACKs, bye.
func fakeAgent(t *testing.T, acceptTicket string) (*httptest.Server, *string) {
	t.Helper()
	var lastResize string
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer ws.Close()
		send := func(messageType string, payload map[string]any) {
			t.Helper()
			body, _ := json.Marshal(Envelope{
				Protocol: "kw-agent-v1", ProtocolVersion: 1,
				SessionID: "sess-test", Generation: 1, Sequence: 1,
				SentAtNs: 1, Channel: "control", Type: messageType, Payload: payload,
			})
			frame := append([]byte{0x00}, uint32be(len(body))...)
			if err := ws.WriteMessage(websocket.BinaryMessage, append(frame, body...)); err != nil {
				t.Errorf("server write: %v", err)
			}
		}
		read := func() Envelope {
			t.Helper()
			_, data, err := ws.ReadMessage()
			if err != nil {
				t.Fatalf("server read: %v", err)
			}
			if len(data) < 5 || data[0] != 0x00 {
				t.Fatalf("bad frame: %v", data)
			}
			var message Envelope
			if err := json.Unmarshal(data[5:], &message); err != nil {
				t.Fatalf("bad envelope: %v", err)
			}
			return message
		}
		send("hello", map[string]any{"role": "controller-only"})
		attach := read()
		if attach.Type != "attach" {
			t.Fatalf("want attach, got %q", attach.Type)
		}
		admitted := attach.Payload["ticket"] == acceptTicket
		reason := ""
		if !admitted {
			reason = "ticket-rejected"
		}
		send("attachResult", map[string]any{"admitted": admitted, "reason": reason})
		if !admitted {
			return
		}
		for {
			message := read()
			switch message.Type {
			case "resizeRequest":
				id, _ := message.Payload["requestId"].(string)
				lastResize = id
				send("resizeAck", map[string]any{
					"requestId": id, "codecReconfigured": false, "idrSent": false,
					"reason": "display-backend-p0-gated",
				})
			case "keyframeRequest":
			case "bye":
				return
			default:
				t.Fatalf("unexpected %q", message.Type)
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &lastResize
}

func dialAgent(t *testing.T, url string) *wsio.Conn {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial(strings.Replace(url, "http", "ws", 1), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { ws.Close() })
	return wsio.New(ws)
}

func TestAttachResizeBye(t *testing.T) {
	srv, lastResize := fakeAgent(t, "good-ticket")
	session, err := Attach(dialAgent(t, srv.URL), "good-ticket")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if session.SessionID() != "sess-test" {
		t.Fatalf("session id = %q", session.SessionID())
	}
	ack, err := session.Resize("r-1", 1920, 1080)
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if ack["requestId"] != "r-1" || ack["reason"] != "display-backend-p0-gated" {
		t.Fatalf("ack not paired/honest: %v", ack)
	}
	if *lastResize != "r-1" {
		t.Fatalf("server saw %q", *lastResize)
	}
	if err := session.Keyframe(); err != nil {
		t.Fatalf("keyframe: %v", err)
	}
	counters := session.Counters()
	if counters.ResizeACKs != 1 || counters.Keyframes != 1 || counters.ControlFrames < 3 {
		t.Fatalf("counters: %+v", counters)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestAttachRefused(t *testing.T) {
	srv, _ := fakeAgent(t, "good-ticket")
	_, err := Attach(dialAgent(t, srv.URL), "bad-ticket")
	if err == nil || !strings.Contains(err.Error(), "ticket-rejected") {
		t.Fatalf("want refusal, got %v", err)
	}
}

func TestFrameCapsEnforced(t *testing.T) {
	// Oversize control frame must fail loudly, never buffer unboundedly.
	huge := append([]byte{0x00}, uint32be(maxControlBytes+1)...)
	if _, _, err := readFrame(bytes.NewReader(huge)); err == nil {
		t.Fatal("oversize control frame must fail")
	}
	if _, _, err := readFrame(bytes.NewReader([]byte{0x09})); err == nil {
		t.Fatal("unknown media kind must fail")
	}
}
