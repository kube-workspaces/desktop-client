package agent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kube-workspaces/desktop-client/internal/wsio"
)

// signedTestTicket mints a viewer-side ticket with an empty live claim,
// exactly like the API issues them (id.payload.signature).
func signedTestTicket() string {
	raw, _ := json.Marshal(map[string]any{
		"workspaceUid": "ws-1", "workspaceGeneration": "gen-1", "sessionId": "",
		"participant": "tester", "role": "controller", "controlEpoch": 1,
		"audience": "workspace-agent", "expiresAtNs": 4000000000000000000,
	})
	return "sessid." + base64.RawURLEncoding.EncodeToString(raw) + ".sig"
}

// fakeAgent speaks the framed protocol over a gorilla server endpoint:
// hello, ticket-gated attach, paired resize ACKs, bye. The ticket must
// arrive as a binding OBJECT stamped with the live hello claim — raw
// strings and unstamped claims are rejected exactly like the guest.
func fakeAgent(t *testing.T, refuse bool, media ...[]byte) (*httptest.Server, *string) {
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
		send("hello", map[string]any{"role": "controller-only", "clipboardText": true})
		attach := read()
		if attach.Type != "attach" {
			t.Fatalf("want attach, got %q", attach.Type)
		}
		ticket, _ := attach.Payload["ticket"].(map[string]any)
		admitted := !refuse &&
			ticket["sessionId"] == "sess-test" &&
			ticket["workspaceUid"] == "ws-1" &&
			ticket["audience"] == "workspace-agent"
		reason := ""
		if !admitted {
			reason = "ticket-rejected"
		}
		send("attachResult", map[string]any{"admitted": admitted, "reason": reason})
		if !admitted {
			return
		}
		for _, frame := range media {
			if err := ws.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				t.Errorf("server media write: %v", err)
				return
			}
		}
		var clipboardText string
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
			case "clipboardSet":
				clipboardText, _ = message.Payload["text"].(string)
				send("clipboardResult", map[string]any{"requestId": message.Payload["requestId"], "ok": true})
			case "clipboardGet":
				send("clipboardResult", map[string]any{"requestId": message.Payload["requestId"], "ok": true, "text": clipboardText})
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
	srv, lastResize := fakeAgent(t, false)
	session, err := Attach(dialAgent(t, srv.URL), signedTestTicket())
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
	srv, _ := fakeAgent(t, true)
	_, err := Attach(dialAgent(t, srv.URL), signedTestTicket())
	if err == nil || !strings.Contains(err.Error(), "ticket-rejected") {
		t.Fatalf("want refusal, got %v", err)
	}
}

func TestClipboardUnicodeRoundtripAndLimits(t *testing.T) {
	srv, _ := fakeAgent(t, false)
	session, err := Attach(dialAgent(t, srv.URL), signedTestTicket())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, text := range []string{"PowerShell\r\nλ 🦀\t", ""} {
		if _, err := session.Clipboard(&text); err != nil {
			t.Fatal(err)
		}
		got, err := session.Clipboard(nil)
		if err != nil || got == nil || *got != text {
			t.Fatalf("clipboard text roundtrip failed: %v", err)
		}
	}
	for _, text := range []string{"left\x00right", strings.Repeat("x", MaxClipboardBytes+1), string([]byte{0xff})} {
		if _, err := session.Clipboard(&text); err == nil {
			t.Fatal("invalid clipboard text accepted")
		}
	}
	if _, err := (&Session{}).Clipboard(nil); !errors.Is(err, ErrClipboardUnavailable) {
		t.Fatalf("missing advert must refuse clipboard: %v", err)
	}
}

func TestClipboardRunPairsResultsAlongsideMedia(t *testing.T) {
	srv, _ := fakeAgent(t, false, mediaFrame(0x01, []byte{1, 2, 3}))
	session, err := Attach(dialAgent(t, srv.URL), signedTestTicket())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	text := "copied output λ"
	if _, err := session.Clipboard(&text); err != nil {
		t.Fatal(err)
	}
	if err := session.RequestClipboard("get-1", nil); err != nil {
		t.Fatal(err)
	}
	paired := false
	err = session.Run(Callbacks{OnClipboard: func(id string, got *string, err error) error {
		if err != nil {
			return err
		}
		paired = id == "get-1" && got != nil && *got == text
		return nil
	}}, time.Now().Add(50*time.Millisecond))
	if err != nil || !paired {
		t.Fatalf("clipboard result not paired: %v", err)
	}
}

func TestQuietWebSocketRunEndsAtDeadline(t *testing.T) {
	srv, _ := fakeAgent(t, false)
	session, err := Attach(dialAgent(t, srv.URL), signedTestTicket())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	start := time.Now()
	if err := session.Run(Callbacks{}, start.Add(50*time.Millisecond)); err != nil {
		t.Fatalf("quiet deadline: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("quiet session did not honor its deadline")
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

func mediaFrame(tag byte, payload []byte) []byte {
	frame := append([]byte{tag}, uint32be(len(payload))...)
	return append(frame, payload...)
}

func TestDecodeTicketVectors(t *testing.T) {
	id, ticket, err := DecodeTicket(signedTestTicket())
	if err != nil {
		t.Fatalf("valid ticket rejected: %v", err)
	}
	if id != "sessid" || ticket.WorkspaceUID != "ws-1" || ticket.Role != "controller" {
		t.Fatalf("ticket fields lost: %+v", ticket)
	}
	if ticket.SessionID != "" {
		t.Fatalf("minted ticket must carry empty session claim, got %q", ticket.SessionID)
	}
	for _, bad := range []string{"", "a.b", "a.b.c.d", ".e30.sig", "id.!!!.sig", "id.e30.sig"} {
		if _, _, err := DecodeTicket(bad); err == nil {
			t.Fatalf("malformed ticket %q accepted", bad)
		}
	}
}

func TestProbeFullFlow(t *testing.T) {
	srv, _ := fakeAgent(t, false,
		mediaFrame(0x01, []byte{0, 0, 0, 1, 0x65, 1, 2, 3}),
		mediaFrame(0x02, []byte{10, 20, 30}),
	)
	session, err := Attach(dialAgent(t, srv.URL), signedTestTicket())
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	result, err := session.Probe(5 * time.Second)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	_ = session.Close()
	if !result.Admitted {
		t.Fatal("probe must admit")
	}
	if result.VideoFrames != 1 || result.VideoBytes != 8 {
		t.Fatalf("video not counted: %+v", result)
	}
	if result.AudioFrames != 1 || result.AudioBytes != 3 {
		t.Fatalf("audio not counted: %+v", result)
	}
	if !result.ResizePaired || result.ResizeReason != "display-backend-p0-gated" {
		t.Fatalf("resize not paired: %+v", result)
	}
	counters := session.Counters()
	if counters.VideoFrames != 1 || counters.AudioFrames != 1 || counters.ResizeACKs != 1 {
		t.Fatalf("counters: %+v", counters)
	}
}

func TestUnknownTagFailsLoud(t *testing.T) {
	srv, _ := fakeAgent(t, false, mediaFrame(0x09, []byte{1, 2, 3}))
	session, err := Attach(dialAgent(t, srv.URL), signedTestTicket())
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := session.Probe(5 * time.Second); err == nil {
		t.Fatal("unknown media kind must fail the session")
	}
	_ = session.Close()
}
