// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package selkies

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func probePeer(t *testing.T, handler func(*websocket.Conn)) *websocket.Conn {
	t.Helper()
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		u := websocket.Upgrader{WriteBufferSize: 16}
		ws, err := u.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer ws.Close()
		ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		handler(ws)
	}))
	t.Cleanup(func() { srv.Close(); <-done })
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func setupPeer(t *testing.T, ws *websocket.Conn, audio bool) {
	t.Helper()
	ws.WriteMessage(websocket.TextMessage, []byte("MODE websockets"))
	_, data, err := ws.ReadMessage()
	if err != nil || !strings.HasPrefix(string(data), "SETTINGS,") {
		t.Errorf("initial settings: %q %v", data, err)
		return
	}
	var settings map[string]any
	if err := json.Unmarshal(data[len("SETTINGS,"):], &settings); err != nil {
		t.Error(err)
	}
	if settings["encoder"] != "h264enc" || settings["use_cpu"] != true || settings["audioRedundancy"] != false {
		t.Errorf("unexpected negotiation: %v", settings)
	}
	commands := []string{"START_VIDEO", "STOP_AUDIO"}
	if audio {
		commands[1] = "START_AUDIO"
	}
	for _, want := range commands {
		_, data, err := ws.ReadMessage()
		if err != nil || string(data) != want {
			t.Errorf("command %q, err %v; want %s", data, err, want)
		}
	}
}

func TestProbeStreamAndACK(t *testing.T) {
	acks := make(chan string, 8)
	ws := probePeer(t, func(ws *websocket.Conn) {
		setupPeer(t, ws, true)
		// Delta before first IDR must not start the measurement interval.
		ws.WriteMessage(websocket.BinaryMessage, videoPacket(65534, false, []byte{0, 0, 1, 0x41}))
		// Small server write buffer forces WS fragmentation. A frame ID wrap
		// must not turn the next valid frame into a duplicate/negative ACK.
		frame := videoPacket(65535, true, append([]byte{0, 0, 0, 1, 0x65}, make([]byte, 256)...))
		ws.WriteMessage(websocket.BinaryMessage, frame)
		ws.WriteMessage(websocket.BinaryMessage, videoPacket(0, false, []byte{0, 0, 1, 0x41}))
		ws.WriteMessage(websocket.BinaryMessage, videoPacket(0, false, nil))
		ws.WriteMessage(websocket.BinaryMessage, []byte{1, 0, 0xf8, 0xff, 0xfe})
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			acks <- string(data)
		}
	})
	cfg := DefaultProbeConfig()
	cfg.Duration = 250 * time.Millisecond
	stats, err := Probe(context.Background(), ws, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.VideoFrames != 2 || stats.Keyframes != 1 || stats.Heartbeats != 1 || stats.AudioPackets != 1 || stats.AudioBytes != 3 || stats.Width != 1920 {
		t.Fatalf("stats: %+v", stats)
	}
	if stats.ElapsedSeconds < .2 || stats.FirstKeyframeMS < 0 || stats.ApplicationBytes != stats.VideoBytes+20+10+5 {
		t.Fatalf("measurement bounds: %+v", stats)
	}
	select {
	case ack := <-acks:
		if !strings.HasPrefix(ack, "CLIENT_FRAME_ACK 0 ") {
			t.Fatalf("ACK: %q", ack)
		}
	default:
		t.Fatal("missing frame receipt ACK")
	}
}

func TestProbeStartupTimeoutAndCancellation(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		ws := probePeer(t, func(ws *websocket.Conn) { _, _, _ = ws.ReadMessage() })
		ctx, cancel := context.WithCancel(context.Background())
		if cancelFirst {
			cancel()
		}
		cfg := DefaultProbeConfig()
		cfg.StartupTimeout = 20 * time.Millisecond
		_, err := Probe(ctx, ws, cfg)
		cancel()
		if cancelFirst && !errors.Is(err, context.Canceled) || !cancelFirst && (err == nil || !strings.Contains(err.Error(), "startup timed out")) {
			t.Fatalf("cancel=%v: %v", cancelFirst, err)
		}
	}
}

func TestProbeRejectsProtocolAndRedactsClose(t *testing.T) {
	for _, mode := range []string{"MODE webrtc", "KILL private-guest-data"} {
		ws := probePeer(t, func(ws *websocket.Conn) {
			ws.WriteMessage(websocket.TextMessage, []byte(mode))
			_, _, _ = ws.ReadMessage()
		})
		_, err := Probe(context.Background(), ws, DefaultProbeConfig())
		if err == nil || strings.Contains(err.Error(), "private-guest-data") {
			t.Fatalf("error was missing or leaked guest text: %v", err)
		}
	}
	ws := probePeer(t, func(ws *websocket.Conn) {
		ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "private-guest-data"), time.Now().Add(time.Second))
		_, _, _ = ws.ReadMessage()
	})
	_, err := Probe(context.Background(), ws, DefaultProbeConfig())
	if err == nil || !strings.Contains(err.Error(), "4001") || strings.Contains(err.Error(), "private-guest-data") {
		t.Fatalf("close error: %v", err)
	}
}

func TestProbeRequiresAudioAndSustainedVideo(t *testing.T) {
	for _, frames := range []int{1, 2} {
		ws := probePeer(t, func(ws *websocket.Conn) {
			setupPeer(t, ws, true)
			for i := range frames {
				ws.WriteMessage(websocket.BinaryMessage, videoPacket(uint16(i), i == 0, []byte{0, 0, 1, 0x65}))
			}
			_, _, _ = ws.ReadMessage()
		})
		cfg := DefaultProbeConfig()
		cfg.Duration = 20 * time.Millisecond
		_, err := Probe(context.Background(), ws, cfg)
		want := "no sustained"
		if frames == 2 {
			want = "no Opus"
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("got %v, want %s", err, want)
		}
	}
}
