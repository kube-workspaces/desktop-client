package main

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestNativePresentation(t *testing.T) {
	if os.Getenv("KW_NATIVE_MEDIA_TEST") != "1" {
		t.Skip("set KW_NATIVE_MEDIA_TEST=1 for SDL/native runtime validation")
	}
	t.Setenv("SDL_VIDEODRIVER", "dummy")
	t.Setenv("SDL_AUDIODRIVER", "dummy")
	au, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=32x24",
		"-frames:v", "1", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-tune", "zerolatency", "-f", "h264", "pipe:1").Output()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if err = conn.WriteMessage(websocket.TextMessage, []byte("MODE websockets")); err != nil {
			return
		}
		for range 3 {
			if _, _, err = conn.ReadMessage(); err != nil {
				return
			}
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		var id uint16
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				packet := make([]byte, 10+len(au))
				packet[0], packet[1] = 4, 1
				binary.BigEndian.PutUint16(packet[2:4], id)
				binary.BigEndian.PutUint16(packet[6:8], 32)
				binary.BigEndian.PutUint16(packet[8:10], 24)
				copy(packet[10:], au)
				if err := conn.WriteMessage(websocket.BinaryMessage, packet); err != nil {
					return
				}
				if err := conn.WriteMessage(websocket.BinaryMessage, []byte{1, 0, 0xf8, 0xff, 0xfe}); err != nil {
					return
				}
				id++
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := run(ctx, []string{"--direct", server.URL, "--present", "--width", "32", "--height", "24", "--duration", "300ms"}); err != nil {
		t.Fatal(err)
	}
}
