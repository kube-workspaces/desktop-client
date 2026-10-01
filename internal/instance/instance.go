// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package instance coordinates one graphical shell per user. CLI commands and
// web children do not participate. An OS-held lock survives neither crashes nor
// process exit; an authenticated loopback connection activates the owner.
package instance

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrAlreadyRunning means the lock is held but activation could not be delivered.
var ErrAlreadyRunning = errors.New("graphical application is already running; switch to its existing window or open it from the system tray")

type endpoint struct {
	Port  int    `json:"port"`
	PID   int    `json:"pid"`
	Token string `json:"token"`
}

// Guard owns the GUI lock and activation listener until Close. The lock file is
// deliberately never removed: unlinking it would let two processes lock different
// inodes. Stale endpoint contents are overwritten only after acquiring the lock.
type Guard struct {
	file     *os.File
	listener net.Listener
	token    string
	wg       sync.WaitGroup
}

// Acquire returns a guard for the first launch, or nil after activating the
// existing process. It retries startup/exit races for a bounded five seconds.
func Acquire(ctx context.Context, dir string) (*Guard, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "shell-instance.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		locked, err := tryLock(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock graphical shell: %w", err)
		}
		if locked {
			g, err := newGuard(f)
			if err != nil {
				_ = f.Close()
			}
			return g, err
		}
		if activate(ctx, path) {
			_ = f.Close()
			return nil, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, ErrAlreadyRunning
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func newGuard(f *os.File) (*Guard, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		_ = listener.Close()
		return nil, err
	}
	e := endpoint{Port: listener.Addr().(*net.TCPAddr).Port, PID: os.Getpid(), Token: hex.EncodeToString(token)}
	data, err := json.Marshal(e)
	if err == nil {
		err = f.Truncate(0)
	}
	if err == nil {
		_, err = f.WriteAt(data, 0)
	}
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	return &Guard{file: f, listener: listener, token: e.Token}, nil
}

func activate(ctx context.Context, path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var e endpoint
	if json.Unmarshal(data, &e) != nil || e.Port <= 0 || e.Port > 65535 || len(e.Token) != 64 || e.PID <= 0 {
		return false
	}
	dialer := net.Dialer{Timeout: 250 * time.Millisecond}
	c, err := dialer.DialContext(ctx, "tcp4", fmt.Sprintf("127.0.0.1:%d", e.Port))
	if err != nil {
		return false
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(250 * time.Millisecond))
	allowForeground(e.PID)
	if _, err := fmt.Fprintf(c, "%s\n", e.Token); err != nil {
		return false
	}
	// Read the exact response, rather than treating any listening process as
	// the owner if a stale port has since been reused.
	ack, err := bufio.NewReader(c).ReadSlice('\n')
	return err == nil && string(ack) == "ok\n"
}

// Start delivers activation requests on its own goroutine. The handler must
// queue work for the UI thread, never call windowing functions itself.
func (g *Guard) Start(handler func()) {
	g.wg.Go(func() {
		for {
			c, err := g.listener.Accept()
			if err != nil {
				return
			}
			_ = c.SetDeadline(time.Now().Add(250 * time.Millisecond))
			// Bound reads even if another local process sends no newline.
			reader := bufio.NewReaderSize(c, 128)
			token, err := reader.ReadSlice('\n')
			if err == nil && string(token) == g.token+"\n" {
				handler()
				_, _ = c.Write([]byte("ok\n"))
			}
			_ = c.Close()
		}
	})
}

// Close stops activation handling before releasing the lock. Closing the file
// releases the native lock automatically, including on Windows.
func (g *Guard) Close() {
	_ = g.listener.Close()
	g.wg.Wait()
	_ = g.file.Close()
}
