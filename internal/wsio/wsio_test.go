// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package wsio

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
)

// newDialedPair spins up an in-memory WebSocket server and returns the
// upgraded server connection and the client's wrapped connection, wired so
// reads of the wrapped connection arrive from the server side.
func newDialedPair(t *testing.T) (server *websocket.Conn, client *Conn) {
	t.Helper()
	srv, cli := newRawPair(t)
	return srv, New(cli)
}

// newRawPair is newDialedPair without the wsio wrapper, for tests that
// exercise the frame-level contract of the two writer kinds.
func newRawPair(t *testing.T) (server, client *websocket.Conn) {
	t.Helper()
	upgrader := &websocket.Upgrader{}
	upgraded := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			upgraded <- nil
			return
		}
		upgraded <- conn
	}))
	t.Cleanup(srv.Close)

	cli, _, err := websocket.DefaultDialer.Dial("ws"+srv.URL[4:], nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	serverConn := <-upgraded
	if serverConn == nil {
		t.Fatal("server upgrade failed")
	}
	return serverConn, cli
}

// nextFrame reads one message off the server side and labels it by frame kind.
func nextFrame(t *testing.T, srv *websocket.Conn) (mt int, body string) {
	t.Helper()
	mt, r, err := srv.NextReader()
	if err != nil {
		t.Fatalf("NextReader: %v", err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return mt, string(b)
}

func TestWriteTextSendsATextFrame(t *testing.T) {
	srv, cli := newDialedPair(t)
	defer cli.Close()

	want := `{"type":"resize","cols":120,"rows":40}`
	if _, err := cli.WriteText([]byte(want)); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	mt, got := nextFrame(t, srv)
	if mt != websocket.TextMessage {
		t.Fatalf("frame type = %d, want TextMessage (%d)", mt, websocket.TextMessage)
	}
	if got != want {
		t.Fatalf("text frame = %q", got)
	}
}

func TestWriteSendsABinaryFrame(t *testing.T) {
	srv, cli := newDialedPair(t)
	defer cli.Close()

	if _, err := cli.Write([]byte("ls\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	mt, got := nextFrame(t, srv)
	if mt != websocket.BinaryMessage {
		t.Fatalf("frame type = %d, want BinaryMessage (%d)", mt, websocket.BinaryMessage)
	}
	if got != "ls\r" {
		t.Fatalf("binary frame = %q", got)
	}
}

func TestWriteTextAndWriteDoNotInterleave(t *testing.T) {
	srv, cli := newDialedPair(t)
	defer cli.Close()

	const frames = 12
	errCh := make(chan error, frames)
	go func() {
		for i := 0; i < frames; i++ {
			var err error
			if i%2 == 0 {
				_, err = cli.Write([]byte("a"))
			} else {
				_, err = cli.WriteText([]byte("b"))
			}
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	for i := 0; i < frames; i++ {
		select {
		case err := <-errCh:
			t.Fatalf("client write %d: %v", i, err)
		default:
		}
		mt, body := nextFrame(t, srv)
		got := ""
		switch mt {
		case websocket.BinaryMessage:
			got = "bin:" + body
		case websocket.TextMessage:
			got = "txt:" + body
		default:
			t.Fatalf("frame %d has unexpected type %d", i, mt)
		}
		want := "bin:a"
		if i%2 == 1 {
			want = "txt:b"
		}
		if got != want {
			t.Fatalf("frame %d = %q, want %q", i, got, want)
		}
	}
}

func TestWriteTextAfterCloseFails(t *testing.T) {
	srv, cli := newRawPair(t)
	c := New(cli)
	_ = srv.Close()
	_ = c.Close()

	if _, err := c.WriteText([]byte("x")); err == nil {
		t.Fatal("WriteText on a closed connection succeeded")
	}
}
