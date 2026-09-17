// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package session

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
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// fakeVideoDec implements selkies.VideoDecoder without native libraries. The
// payload encodes [start code][w][h]; a marker lands in pixel (0,0).
type fakeVideoDec struct{}

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

func (a *fakeAudioDec) Decode(p []byte) ([]byte, error) { return append([]byte(nil), p...), nil }
func (a *fakeAudioDec) Close()                          {}

// selkiesPeer is a scripted Selkies agent. Every successful upgrade hands its
// server-side socket to the test so the driving goroutine can speak the
// agent's side of the handshake; client text verbs are collected in wire order.
type selkiesPeer struct {
	conns      chan *websocket.Conn
	fromClient chan string
}

func (p *selkiesPeer) waitText(prefix string, d time.Duration) (string, error) {
	deadline := time.After(d)
	for {
		select {
		case msg := <-p.fromClient:
			if strings.HasPrefix(msg, prefix) {
				return msg, nil
			}
		case <-deadline:
			return "", errors.New("timeout waiting for client verb " + prefix)
		}
	}
}

func writeText(srv *websocket.Conn, text string) error {
	return srv.WriteMessage(websocket.TextMessage, []byte(text))
}

// startSelkiesPeer spins up the scripted agent. The driving goroutine must
// first take the upgraded server socket from p.conns (the glue dials it) and
// then speak against it.
func startSelkiesPeer(t *testing.T) (*selkiesPeer, string) {
	t.Helper()
	p := &selkiesPeer{
		conns:      make(chan *websocket.Conn, 1),
		fromClient: make(chan string, 256),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		select {
		case p.conns <- ws:
		default: // a second upgrade goes unclaimed; the test only needs one
		}
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			p.fromClient <- string(data)
		}
	}))
	t.Cleanup(srv.Close)
	return p, srv.URL
}

// tier1Client builds a kwclient for the scripted base URL.
func tier1Client(t *testing.T, base string) *kwclient.Client {
	t.Helper()
	client, err := kwclient.New(base, kwclient.WithToken("tok.sig"))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// fakeTier1Backend satisfies viewer.Backend without SDL. Every call is a no-op
// or a record, WaitEvents never blocks, and queued events are drained by
// PollEvents — the render loop therefore spins and surfaces a dead produce
// worker at once.
type fakeTier1Backend struct {
	mu     sync.Mutex
	events []viewer.Event
}

func (f *fakeTier1Backend) Open(viewer.WindowOptions) error { return nil }
func (f *fakeTier1Backend) Close()                          {}
func (f *fakeTier1Backend) SetTextureSize(w, h int) error   { return nil }
func (f *fakeTier1Backend) Upload(viewer.Rect, []byte, int) error {
	return nil
}
func (f *fakeTier1Backend) SetOverlaySize(w, h int) error { return nil }
func (f *fakeTier1Backend) UploadOverlay(viewer.Rect, []byte, int) error {
	return nil
}
func (f *fakeTier1Backend) Present(viewer.Rect, viewer.Overlay) error { return nil }
func (f *fakeTier1Backend) SetSize(w, h int) error                    { return nil }
func (f *fakeTier1Backend) SetTitle(string) error                     { return nil }
func (f *fakeTier1Backend) SetFullscreen(bool) error                  { return nil }
func (f *fakeTier1Backend) Fullscreen() bool                          { return false }
func (f *fakeTier1Backend) Size() (int, int)                          { return 1280, 800 }
func (f *fakeTier1Backend) Clipboard() (string, error)                { return "", nil }
func (f *fakeTier1Backend) SetClipboard(string) error                 { return nil }
func (f *fakeTier1Backend) Wake()                                     {}
func (f *fakeTier1Backend) WaitEvents(dst []viewer.Event, timeout time.Duration) []viewer.Event {
	return dst
}
func (f *fakeTier1Backend) PollEvents(dst []viewer.Event) []viewer.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	dst = append(dst, f.events...)
	f.events = f.events[:0]
	return dst
}

// baseTier1 runs the glue with isolated decoders and a short startup budget.
func baseTier1(opts ...func(*Tier1Config)) Tier1Config {
	cfg := Tier1Config{
		Title:          "demo/vm-a",
		StartupTimeout: 200 * time.Millisecond,
		VideoDec:       &fakeVideoDec{},
		AudioDec:       &fakeAudioDec{},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

func TestRunTier1AgentRefusalNotFallback(t *testing.T) {
	// The agent accepts the establishment and then refuses the session; the
	// glue must surface the refusal, never route around it to Tier 0.
	p, url := startSelkiesPeer(t)
	client := tier1Client(t, url)

	go func() {
		var srv *websocket.Conn
		select {
		case srv = <-p.conns:
		case <-time.After(5 * time.Second):
			t.Errorf("agent never upgraded")
			return
		}
		if err := writeText(srv, "MODE websockets"); err != nil {
			t.Errorf("send MODE: %v", err)
			return
		}
		if _, err := p.waitText("SETTINGS,", 3*time.Second); err != nil {
			t.Errorf("handshake did not advance: %v", err)
			return
		}
		if err := writeText(srv, "KILL stale session, reconnect"); err != nil {
			t.Errorf("send KILL: %v", err)
		}
	}()

	err := RunTier1(context.Background(), client, "demo", "vm-a", "", &fakeTier1Backend{}, baseTier1())
	if err == nil {
		t.Fatal("agent refusal expected an error, got nil")
	}
	if !errors.Is(err, ErrNoFallback) {
		t.Fatalf("agent refusal must not fall back: %v", err)
	}
	if !strings.Contains(err.Error(), "stale session") {
		t.Fatalf("refusal lost the agent's reason: %v", err)
	}
}

func TestRunTier1StartupTimeoutFallsBack(t *testing.T) {
	// The agent establishes but never streams video: the bounded handshake
	// expires. That is a recoverable failure the caller files under fallback.
	p, url := startSelkiesPeer(t)
	client := tier1Client(t, url)

	go func() {
		var srv *websocket.Conn
		select {
		case srv = <-p.conns:
		case <-time.After(5 * time.Second):
			t.Errorf("agent never upgraded")
			return
		}
		if err := writeText(srv, "MODE websockets"); err != nil {
			t.Errorf("send MODE: %v", err)
		}
	}()

	err := RunTier1(context.Background(), client, "demo", "vm-a", "", &fakeTier1Backend{}, baseTier1())
	if err == nil {
		t.Fatal("startup timeout expected an error, got nil")
	}
	if errors.Is(err, ErrNoFallback) {
		t.Fatalf("startup timeout must be fallable: %v", err)
	}
	if !strings.Contains(err.Error(), "H.264") {
		t.Fatalf("expected the startup-timeout diagnosis, got: %v", err)
	}
}

func TestRunTier1DialRejectedNotFallback(t *testing.T) {
	// A rejected credential must not trigger an alternative route intended to
	// evade it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	err := RunTier1(context.Background(), tier1Client(t, srv.URL), "demo", "vm-a", "", &fakeTier1Backend{}, baseTier1())
	if err == nil {
		t.Fatal("403 dial expected an error, got nil")
	}
	if !errors.Is(err, ErrNoFallback) {
		t.Fatalf("403 must not be routed around: %v", err)
	}
}

func TestRunTier1DialDownFallsBack(t *testing.T) {
	// A dead proxy is a recoverable transport failure: the caller may try
	// Tier 0 for the same connection.
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	err := RunTier1(context.Background(), tier1Client(t, srv.URL), "demo", "vm-a", "", &fakeTier1Backend{}, baseTier1())
	if err == nil {
		t.Fatal("dead proxy expected an error, got nil")
	}
	if errors.Is(err, ErrNoFallback) {
		t.Fatalf("dead proxy must be fallable: %v", err)
	}
}

func TestRunTier1QuitReturnsNil(t *testing.T) {
	// The user closes the window while the transport is still establishing;
	// that is a clean quit, not a transport failure.
	_, url := startSelkiesPeer(t)
	be := &fakeTier1Backend{}
	be.events = append(be.events, viewer.EventQuit{})

	err := RunTier1(context.Background(), tier1Client(t, url), "demo", "vm-a", "", be, baseTier1())
	if err != nil {
		t.Fatalf("clean quit must return nil, got %v", err)
	}
}
