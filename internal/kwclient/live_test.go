// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kube-workspaces/desktop-client/internal/wsio"
)

// Opt-in acceptance against a real API/VM. Tokens are read from the environment
// and never printed or persisted. Only the test's own device and annotation are
// removed. Supply KW_LIVE_URL, KW_LIVE_TOKEN, KW_LIVE_NAMESPACE, KW_LIVE_VM,
// and KW_LIVE_CONTEXT (kubectl context for the reversible watch mutation).
func TestLivePlatformFollowUps(t *testing.T) {
	base := os.Getenv("KW_LIVE_URL")
	if base == "" {
		t.Skip("set KW_LIVE_URL to enable live acceptance")
	}
	token, namespace, vm := os.Getenv("KW_LIVE_TOKEN"), os.Getenv("KW_LIVE_NAMESPACE"), os.Getenv("KW_LIVE_VM")
	if token == "" || namespace == "" || vm == "" {
		t.Fatal("live token, namespace and VM are required")
	}
	client, err := New(base, WithToken(token))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if identity, err := client.Me(ctx); err != nil || !identity.Authenticated {
		t.Fatal("live credential was not accepted")
	}

	t.Run("device", func(t *testing.T) {
		device, err := client.CreateDeviceToken(ctx, "platform-acceptance")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cleanupCancel()
			if err := client.RevokeDevice(cleanupCtx, device.DeviceID); err != nil {
				t.Errorf("cleanup device: %v", err)
			}
		})
		registered, err := client.ListDevices(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, item := range registered {
			found = found || item.DeviceID == device.DeviceID
		}
		if !found {
			t.Fatal("created device missing from list")
		}
		deviceClient, err := New(base, WithToken(device.Token))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := deviceClient.ListWorkspaces(ctx, namespace); err != nil {
			t.Fatal(err)
		}
		if err := client.RevokeDevice(ctx, device.DeviceID); err != nil {
			t.Fatal(err)
		}
		if _, err := deviceClient.ListWorkspaces(ctx, namespace); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("revoked device still authorized")
		}
		registered, err = client.ListDevices(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range registered {
			if item.DeviceID == device.DeviceID {
				t.Fatal("revoked device still listed")
			}
		}
	})

	t.Run("watch", func(t *testing.T) {
		kubeContext := os.Getenv("KW_LIVE_CONTEXT")
		if kubeContext == "" {
			t.Skip("set KW_LIVE_CONTEXT for watch mutation/latency")
		}
		watchCtx, stop := context.WithCancel(ctx)
		defer stop()
		snapshots := make(chan time.Time, 16)
		ended := make(chan error, 1)
		go func() {
			ended <- client.WatchWorkspaces(watchCtx, namespace, func(items []Workspace) {
				found := false
				for _, item := range items {
					found = found || item.Name == vm && item.Namespace == namespace
				}
				if !found {
					return
				}
				select {
				case snapshots <- time.Now():
				case <-watchCtx.Done():
				}
			})
		}()
		select {
		case <-snapshots:
		case err := <-ended:
			t.Fatal(err)
		case <-time.After(15 * time.Second):
			t.Fatal("no initial snapshot")
		}
		key := fmt.Sprintf("tracking.kubeworkspaces.io/acceptance-%d", time.Now().UnixNano())
		annotate := func(value string) error {
			cmd := exec.CommandContext(ctx, "kubectl", "--context", kubeContext, "-n", namespace, "annotate", "workspace", vm, key+value)
			return cmd.Run()
		}
		t.Cleanup(func() {
			cmd := exec.Command("kubectl", "--context", kubeContext, "-n", namespace, "annotate", "workspace", vm, key+"-")
			if err := cmd.Run(); err != nil {
				t.Errorf("cleanup watch annotation: %v", err)
			}
		})
		started := time.Now()
		if err := annotate("=probe"); err != nil {
			t.Fatal(err)
		}
		select {
		case at := <-snapshots:
			t.Logf("workspace watch latency including kubectl write: %s (polling interval: 5s)", at.Sub(started).Round(time.Millisecond))
		case err := <-ended:
			t.Fatal(err)
		case <-time.After(10 * time.Second):
			t.Fatal("no mutation snapshot")
		}
		stop()
		select {
		case <-ended:
		case <-time.After(time.Second):
			t.Fatal("watch did not cancel")
		}
	})

	t.Run("screenshot", func(t *testing.T) {
		raw, err := client.Screenshot(ctx, namespace, vm)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("real VM PNG: %dx%d, %d bytes", img.Bounds().Dx(), img.Bounds().Dy(), len(raw))
		// Do not take over anyone's display: only dial if the capture is free.
		conn, err := client.DialVNC(ctx, namespace, vm)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if _, err := client.Screenshot(ctx, namespace, vm); !errors.Is(err, ErrSessionInUse) {
			t.Fatalf("occupied display screenshot: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, message, err := conn.ReadMessage()
		if err != nil || !bytes.HasPrefix(message, []byte("RFB ")) {
			t.Fatal("screenshot disturbed the held VNC stream")
		}
	})

	for _, port := range []int{8080, 22} {
		t.Run(fmt.Sprintf("tcp-%d", port), func(t *testing.T) {
			conn, err := client.DialTCP(ctx, namespace, vm, port)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			status, err := client.TCPStatus(ctx, namespace, vm, port)
			if err != nil || !status.InUse {
				t.Fatal("bridge not marked occupied")
			}
			second, err := client.DialTCP(ctx, namespace, vm, port)
			if second != nil {
				_ = second.Close()
			}
			if !errors.Is(err, ErrSessionInUse) {
				t.Fatalf("TCP contention: %v", err)
			}
			stream := wsio.New(conn)
			if port == 8080 {
				if err := conn.WriteMessage(websocket.BinaryMessage, []byte("GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")); err != nil {
					t.Fatal(err)
				}
				response, err := http.ReadResponse(bufio.NewReader(stream), nil)
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				t.Logf("guest HTTP response: %d", response.StatusCode)
			} else {
				banner, err := bufio.NewReader(stream).ReadString('\n')
				if err != nil || !strings.HasPrefix(banner, "SSH-2.0-") {
					t.Fatal("guest SSH banner not received")
				}
				t.Log("guest SSH protocol banner received")
			}
		})
	}
}
