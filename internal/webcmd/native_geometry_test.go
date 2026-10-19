// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build cgo

package webcmd

import (
	"bytes"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/config"
)

// TestWebChromeGeometryNative drives three separate authenticated child
// lifetimes, proving restoration and close-time persistence without allowing
// fullscreen/maximized rectangles to overwrite the normal arrangement.
func TestWebChromeGeometryNative(t *testing.T) {
	skipNative(t)
	stub := newStubInstance(t)
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	writeProfile(t, home, stub.URL)
	path, err := config.WebGeometryPath("smoke", stub.URL, "team/code")
	if err != nil {
		t.Fatal(err)
	}
	child := buildChild(t)
	var want config.WindowRect
	for i, mode := range []string{"normal", "fullscreen", "maximized"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(child, "--namespace", "team", "code")
			cmd.Env = append(os.Environ(),
				"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
				"XDG_DATA_HOME="+filepath.Join(home, "data"),
				"WEBKIT_DISABLE_COMPOSITING_MODE=1", "WEBKIT_DISABLE_DMABUF_RENDERER=1")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			exited := make(chan error, 1)
			go func() {
				exited <- cmd.Wait()
				close(done)
			}()
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				<-done
				if t.Failed() {
					t.Logf("child diagnostics: %s", stderr.String())
				}
			})
			window := waitForWindow(t, 60*time.Second)
			xrun(t, "windowactivate", "--sync", window)
			if i == 0 {
				xrun(t, "windowsize", "--sync", window, "1000", "650")
				xrun(t, "windowmove", "--sync", window, "110", "90")
			}
			// Allow the native timer to observe the normal window before the
			// state transition. Writes are rate-limited; final flush is required.
			time.Sleep(750 * time.Millisecond)
			switch mode {
			case "fullscreen":
				xrun(t, "key", "--clearmodifiers", "F11")
			case "maximized":
				xrun(t, "key", "alt+space")
				xrun(t, "key", "x")
			}
			if mode != "normal" {
				waitFor(t, window, 10*time.Second, mode+" window enlargement", func(img image.Image) bool {
					return img.Bounds().Dx() > want.Width && img.Bounds().Dy() > want.Height
				})
			}
			xrun(t, "key", "alt+F4")
			select {
			case err := <-exited:
				if err != nil {
					t.Fatalf("child exit: %v\n%s", err, stderr.String())
				}
			case <-time.After(15 * time.Second):
				t.Fatal("child did not exit on close")
			}
			got, err := config.LoadWebGeometry(path)
			if err != nil {
				t.Fatalf("saved geometry: %v\n%s", err, stderr.String())
			}
			if i == 0 {
				if got.Width != 1000 || got.Height != 650 {
					t.Fatalf("normal size = %+v, want 1000x650", got)
				}
				want = got
			} else if got != want {
				t.Fatalf("%s close saved %+v, want original normal %+v", mode, got, want)
			}
		})
		if t.Failed() {
			break
		}
	}
}
