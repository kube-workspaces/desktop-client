// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestDeviceCredentialLifecycle(t *testing.T) {
	var revoked atomic.Bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer device.sig" && revoked.Load() {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/auth/device/create":
			if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer tok.sig" {
				t.Error("missing session authentication")
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["name"] != "laptop" {
				t.Error("device name lost")
			}
			writeJSON(t, w, 200, `{"token":"device.sig","device_id":"d1","name":"laptop","expires_at":2000000000}`)
		case "/auth/device/list":
			writeJSON(t, w, 200, `{"devices":[{"deviceId":"d1","name":"laptop","createdAt":100,"expiresAt":2000000000}]}`)
		case "/auth/device/revoke":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["device_id"] != "d1" {
				t.Error("revoke ID lost")
			}
			revoked.Store(true)
			writeJSON(t, w, 200, `{"status":"revoked"}`)
		default:
			writeJSON(t, w, 200, `[]`)
		}
	})
	ctx := context.Background()
	device, err := c.CreateDeviceToken(ctx, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if c.Token() != "tok.sig" {
		t.Fatal("create unexpectedly replaced the session")
	}
	devices, err := c.ListDevices(ctx)
	if err != nil || len(devices) != 1 || devices[0].DeviceID != device.DeviceID || devices[0].ExpiresAt != device.ExpiresAt {
		t.Fatalf("list metadata mismatch: %v", err)
	}
	c.SetToken(device.Token)
	if _, err := c.ListWorkspaces(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if err := c.RevokeDevice(ctx, device.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListWorkspaces(ctx, "team"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked token error: %v", err)
	}
}

func TestWatchStreamingOutlivesRESTTimeoutAndCancels(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("namespace") != "team" || r.Header.Get("Authorization") != "Bearer tok.sig" {
			t.Error("watch scope/authentication lost")
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = fmt.Fprint(w, ": heartbeat\n\nevent: snapshot\ndata: [{\ndata: \"name\":\"first\",\"namespace\":\"team\",\"type\":\"vm\",\"image\":\"example/image\",\"ready_replicas\":1,\"stopped\":false}]\n\n")
		w.(http.Flusher).Flush()
		timer := time.NewTimer(50 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
		}
		_, _ = fmt.Fprint(w, "event: snapshot\ndata: []\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, WithTimeout(10*time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var snapshots int
	err := c.WatchWorkspaces(ctx, "team", func(items []Workspace) {
		snapshots++
		if snapshots == 1 && (len(items) != 1 || items[0].Name != "first" || !items[0].Running()) {
			t.Error("multiline snapshot corrupted")
		}
		if snapshots == 2 {
			cancel()
		}
	})
	if snapshots != 2 || !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshots=%d, error=%v", snapshots, err)
	}
}

func TestWatchRejectsMalformedSnapshot(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: snapshot\ndata: not-json\n\n")
	})
	if err := c.WatchWorkspaces(context.Background(), "team", func([]Workspace) { t.Error("invalid snapshot delivered") }); err == nil {
		t.Fatal("invalid snapshot accepted")
	}
}

func TestWatchRejectsServiceModelJSON(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: snapshot\ndata: [{\"Name\":\"vm\",\"Namespace\":\"team\",\"Type\":\"vm\",\"Image\":\"example/image\",\"ReadyReplicas\":1,\"Stopped\":false}]\n\n")
	})
	if err := c.WatchWorkspaces(context.Background(), "team", func([]Workspace) { t.Error("invalid wire format delivered") }); err == nil {
		t.Fatal("service-model JSON accepted")
	}
}

func TestScreenshotPNGAndBusy(t *testing.T) {
	var busy atomic.Bool
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok.sig" {
			t.Error("missing screenshot authentication")
		}
		if busy.Load() {
			w.WriteHeader(409)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(buf.Bytes())
	})
	raw, err := c.Screenshot(context.Background(), "team", "vm")
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil || img.Bounds().Dx() != 3 {
		t.Fatal("PNG changed")
	}
	busy.Store(true)
	if _, err := c.Screenshot(context.Background(), "team", "vm"); !errors.Is(err, ErrSessionInUse) {
		t.Fatalf("busy error = %v", err)
	}
}

func TestTCPBinaryBridgeAndPortValidation(t *testing.T) {
	upgrader := websocket.Upgrader{}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("port") != "8080" || r.URL.Query().Get("namespace") != "team" || r.Header.Get("Authorization") != "Bearer tok.sig" {
			t.Error("TCP target/authentication lost")
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		kind, payload, err := conn.ReadMessage()
		if err != nil || kind != websocket.BinaryMessage {
			t.Error("not binary TCP data")
			return
		}
		_ = conn.WriteMessage(kind, payload)
	})
	ctx := context.Background()
	if _, err := c.DialTCP(ctx, "team", "vm", 0); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid port: %v", err)
	}
	conn, err := c.DialTCP(ctx, "team", "vm", 8080)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	payload := []byte{0, 1, 255, 10}
	if err := conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
		t.Fatal(err)
	}
	kind, received, err := conn.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage || !bytes.Equal(payload, received) {
		t.Fatalf("TCP round-trip failed: %v", err)
	}
}
