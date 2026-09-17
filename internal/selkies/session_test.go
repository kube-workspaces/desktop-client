// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package selkies

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeVideoDec implements VideoDecoder without native libraries. The payload
// encodes [start code][w][h] first so the decoder can reconstruct the geometry
// the session checks against the wire header; marker lands in pixel (0,0).
type fakeVideoDec struct {
	mu sync.Mutex
}

func (f *fakeVideoDec) Decode(p []byte) (*image.RGBA, error) {
	if len(p) < 9 {
		return nil, errors.New("fake: short NAL")
	}
	w, h := int(binary.BigEndian.Uint16(p[4:6])), int(binary.BigEndian.Uint16(p[6:8]))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.SetRGBA(0, 0, color.RGBA{R: p[8], G: p[8], B: p[8], A: 0xff})
	return img, nil
}

func (f *fakeVideoDec) Close() {}

type fakeAudioDec struct{}

func (a *fakeAudioDec) Decode(p []byte) ([]byte, error) {
	return append([]byte(nil), p...), nil
}

func (a *fakeAudioDec) Close() {}

// serverKeyFrame builds a Video binary message for the scripted peer: a full
// frame (Y=0) keyframe carrying the Annex-B start code and dims the fake
// decoder reads back. marker lands in pixel (0,0).
func serverKeyFrame(w, h uint16, frameID uint16, marker byte) []byte {
	data := make([]byte, 10+4+2+2+1)
	data[0] = Video
	data[1] = 1
	binary.BigEndian.PutUint16(data[2:4], frameID)
	binary.BigEndian.PutUint16(data[4:6], 0)
	binary.BigEndian.PutUint16(data[6:8], w)
	binary.BigEndian.PutUint16(data[8:10], h)
	p := data[10:]
	p[0], p[1], p[2], p[3] = 0, 0, 0, 1
	binary.BigEndian.PutUint16(p[4:6], w)
	binary.BigEndian.PutUint16(p[6:8], h)
	p[8] = marker
	return data
}

// serverOpus builds an Audio binary message (RED block count 0).
func serverOpus(opcode byte) []byte {
	return []byte{Audio, 0, opcode}
}

// scriptedPeer is a test WebSocket server: server-side writes are driven by the
// test (guarded by mu) and exactly one goroutine (the handler) reads client
// text into fromClient in wire order. p.client is the peer the Session dials
// into; p.server is the server-side socket the test writes on.
type scriptedPeer struct {
	server     *websocket.Conn
	client     *websocket.Conn
	fromClient chan string
	mu         sync.Mutex
}

// newSessionPeer spins up the scripted server, returns the server-side peer the
// test drives and the client socket the Session dials.
func newSessionPeer(t *testing.T) (*scriptedPeer, *websocket.Conn) {
	t.Helper()
	p := &scriptedPeer{fromClient: make(chan string, 512)}
	upgraded := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := websocket.Upgrader{}
		ws, err := u.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		upgraded <- ws
		defer ws.Close()
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			p.fromClient <- string(data)
		}
	}))
	t.Cleanup(srv.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	select {
	case p.server = <-upgraded:
	case <-time.After(2 * time.Second):
		t.Fatal("peer: server socket never upgraded")
	}
	return p, client
}

func (p *scriptedPeer) sendText(t *testing.T, s string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.server.WriteMessage(websocket.TextMessage, []byte(s)); err != nil {
		t.Fatal(err)
	}
}

func (p *scriptedPeer) sendBinary(t *testing.T, data []byte) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.server.WriteMessage(websocket.BinaryMessage, data); err != nil {
		t.Fatal(err)
	}
}

func (p *scriptedPeer) close(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.server.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
		t.Fatal(err)
	}
}

// read blocks until a client text message with the given prefix arrives, then
// returns it; other messages are consumed silently.
func (p *scriptedPeer) read(t *testing.T, prefix string) string {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg := <-p.fromClient:
			if strings.HasPrefix(msg, prefix) {
				return msg
			}
		case <-deadline:
			t.Fatalf("read: timeout waiting for %q", prefix)
		}
	}
}

func (p *scriptedPeer) maybe(t *testing.T, d time.Duration) string {
	t.Helper()
	select {
	case msg := <-p.fromClient:
		return msg
	case <-time.After(d):
		return ""
	}
}

func testSession(t *testing.T, cfg SessionConfig, sink Sink) (*Session, *scriptedPeer, *websocket.Conn) {
	t.Helper()
	p, client := newSessionPeer(t)
	s := NewSession(client, cfg, sink)
	return s, p, client
}

// runSession starts Run in the background and feeds the scripted MODE so the
// handshake advances.
func runSession(t *testing.T, s *Session, p *scriptedPeer) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { done <- s.Run(ctx) }()
	p.sendText(t, "MODE websockets")
	return done
}

func waitForBool(t *testing.T, d time.Duration, f func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func TestSessionHandshakeAndFirstFrame(t *testing.T) {
	var (
		videoMu  sync.Mutex
		frame    *image.RGBA
		gotVideo bool
	)
	var (
		eventsMu sync.Mutex
		events   []ControlEvent
	)
	cfg := SessionConfig{
		Audio:          false,
		StartupTimeout: 2 * time.Second,
		VideoDec:       &fakeVideoDec{},
		Logf:           t.Logf,
	}
	sink := Sink{
		Video: func(img *image.RGBA) {
			videoMu.Lock()
			frame, gotVideo = img, true
			videoMu.Unlock()
		},
		Control: func(evt ControlEvent) {
			eventsMu.Lock()
			events = append(events, evt)
			eventsMu.Unlock()
		},
	}
	s, p, _ := testSession(t, cfg, sink)
	done := runSession(t, s, p)

	// Setup commands arrive in order: SETTINGS before START_VIDEO before STOP_AUDIO.
	settings := p.read(t, "SETTINGS,")
	if strings.Contains(settings, "manual_resolution") {
		t.Fatalf("interactive SETTINGS locked manual resolution: %s", settings)
	}
	for _, want := range []string{"h264enc", "cbr", "video_bitrate", "displayId", "framerate"} {
		if !strings.Contains(settings, want) {
			t.Fatalf("SETTINGS missing %q: %s", want, settings)
		}
	}
	p.read(t, "START_VIDEO")
	p.read(t, "STOP_AUDIO")

	// Server control events before video.
	p.sendText(t, "cursor,{\"curdata\":\"aGk=\",\"width\":1,\"height\":2,\"hotx\":3,\"hoty\":4,\"handle\":9}")
	p.sendText(t, "{\"type\":\"server_settings\",\"settings\":{\"framerate\":30,\"video_crf\":20}}")

	// First keyframe.
	p.sendBinary(t, serverKeyFrame(320, 200, 1, 0xAB))

	if !waitForBool(t, 2*time.Second, func() bool {
		videoMu.Lock()
		defer videoMu.Unlock()
		return gotVideo
	}) {
		t.Fatal("no video frame delivered")
	}
	videoMu.Lock()
	if frame.Rect.Dx() != 320 || frame.Rect.Dy() != 200 {
		t.Fatalf("bad frame size %v", frame.Rect)
	}
	if marker := frame.RGBAAt(0, 0); marker.R != 0xAB {
		t.Fatalf("marker not present: %v", marker)
	}
	videoMu.Unlock()

	eventsMu.Lock()
	gotCursor, gotSettings := false, false
	for _, evt := range events {
		switch evt.Kind {
		case EventCursor:
			gotCursor = evt.Cursor.Handle == 9
		case EventSettings:
			_, gotSettings = evt.Settings["framerate"]
		}
	}
	eventsMu.Unlock()
	if !gotCursor {
		t.Fatal("EventCursor not delivered")
	}
	if !gotSettings {
		t.Fatal("EventSettings not delivered")
	}

	// After the first frame the link carries an ACK and the restored native
	// cursor (p,1), in either order; the read helper skips non-matching
	// messages, so wait for both explicitly.
	seenAck, seenCursor := false, false
	ackDeadline := time.After(2 * time.Second)
	for !seenAck || !seenCursor {
		select {
		case msg := <-p.fromClient:
			seenAck = seenAck || strings.HasPrefix(msg, "CLIENT_FRAME_ACK 1 ")
			seenCursor = seenCursor || msg == "p,1"
		case <-ackDeadline:
			t.Fatalf("read: timeout waiting for ACK=%v cursor-restored=%v", seenAck, seenCursor)
		}
	}

	// The Control adapter stays live during Run.
	if err := s.ctrl.Send("p,1"); err != nil {
		t.Fatal(err)
	}
	if msg := p.read(t, "p,1"); msg != "p,1" {
		t.Fatalf("Input did not reach peer: %q", msg)
	}

	p.close(t)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "peer closed") {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
}

func TestSessionAudioNegotiationAndDecode(t *testing.T) {
	var (
		audioMu sync.Mutex
		got     []byte
	)
	cfg := SessionConfig{
		Audio:          true,
		StartupTimeout: time.Second,
		VideoDec:       &fakeVideoDec{},
		AudioDec:       &fakeAudioDec{},
	}
	sink := Sink{
		Audio: func(pcm []byte) {
			audioMu.Lock()
			got = append([]byte(nil), pcm...)
			audioMu.Unlock()
		},
	}
	s, p, _ := testSession(t, cfg, sink)
	done := runSession(t, s, p)

	p.read(t, "SETTINGS,")
	p.read(t, "START_VIDEO")
	if verb := p.read(t, "START_"); verb != "START_AUDIO" {
		t.Fatalf("audio negotiation: got %q, want START_AUDIO", verb)
	}

	p.sendBinary(t, serverOpus(0x7E))
	if !waitForBool(t, 2*time.Second, func() bool {
		audioMu.Lock()
		defer audioMu.Unlock()
		return len(got) == 1 && got[0] == 0x7E
	}) {
		t.Fatal("Opus payload not decoded and delivered")
	}

	p.sendBinary(t, serverKeyFrame(64, 64, 2, 0x01))
	p.close(t)
	<-done
}

func TestSessionStartupTimeoutFallsBack(t *testing.T) {
	cfg := SessionConfig{StartupTimeout: 150 * time.Millisecond, VideoDec: &fakeVideoDec{}}
	s, p, _ := testSession(t, cfg, Sink{})
	done := runSession(t, s, p)
	p.read(t, "SETTINGS,")
	p.read(t, "START_VIDEO")

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected startup timeout, got %v", err)
	}
}

func TestSessionBinaryBeforeMode(t *testing.T) {
	cfg := SessionConfig{StartupTimeout: time.Second, VideoDec: &fakeVideoDec{}}
	s, p, _ := testSession(t, cfg, Sink{})
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { done <- s.Run(ctx) }()

	p.sendBinary(t, serverKeyFrame(64, 64, 1, 0x01))
	if err := <-done; err == nil || !strings.Contains(err.Error(), "before MODE") {
		t.Fatalf("expected media-before-mode rejection, got %v", err)
	}
}

func TestSessionRunIsIdempotent(t *testing.T) {
	cfg := SessionConfig{StartupTimeout: 150 * time.Millisecond, VideoDec: &fakeVideoDec{}}
	s, p, _ := testSession(t, cfg, Sink{})
	done := runSession(t, s, p)
	p.read(t, "SETTINGS,")
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected startup timeout, got %v", err)
	}
	// A second Run must not touch the closed socket or block forever.
	if again := s.Run(context.Background()); again != err {
		t.Fatalf("second Run returned %v, want %v", again, err)
	}
}

func TestSessionAgentRefusalWrapsErrRefused(t *testing.T) {
	cfg := SessionConfig{StartupTimeout: 2 * time.Second, VideoDec: &fakeVideoDec{}}
	s, p, _ := testSession(t, cfg, Sink{})
	done := runSession(t, s, p)
	p.read(t, "SETTINGS,")
	p.sendText(t, "KILL stale session, reconnect")
	err := <-done
	if err == nil || !errors.Is(err, ErrRefused) {
		t.Fatalf("KILL returned %v, want an error wrapping ErrRefused", err)
	}
	if !strings.Contains(err.Error(), "stale session") {
		t.Fatalf("refusal lost the agent's reason: %v", err)
	}
}
