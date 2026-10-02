// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build cgo

// Package webcmd's native web chrome smoke test. The web child is the only
// place the native toolbar code runs, and it only runs on the host OS with a
// browser engine loaded, so ordinary `go test ./...` (which is cgo-free and
// headless here) never touches it. This test builds and runs the real child
// against a stub instance, drives its window with X automation, and reads the
// pixels back to decide what the toolbar actually did.
//
// It is opt-in and self-skipping:
//
//	KW_WEB_CHROME_NATIVE=1 go test ./internal/webcmd -run WebChrome -v
//
// Requirements, all outside the repository's control: a display (Xvfb is
// fine) with a window manager running (openbox does, and fullscreen is a
// request the window manager has to honour, so without one the F11 step
// times out), `xdotool` for key and pointer input, ImageMagick's `import` for
// screenshots, and WebKitGTK 4.1 development files for the build. Without
// them the test skips rather than fails.
//
//	xvfb-run -a -s "-screen 0 1400x900x24" sh -c 'openbox & go test ./internal/webcmd -run WebChrome -v'
package webcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// pageBackground is the solid colour the stub instance's page fills the
// viewport with. Every assertion about "the toolbar is/is not there" is made
// against this colour, so it must not appear anywhere in the chrome.
var pageBackground = color.RGBA{R: 0x20, G: 0x40, B: 0x60, A: 0xff}

const nativeEnv = "KW_WEB_CHROME_NATIVE"

// skipNative reports why the native chrome test cannot run here, if it cannot.
func skipNative(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the native chrome smoke test drives GTK through X automation; linux only")
	}
	if os.Getenv(nativeEnv) == "" {
		t.Skip("set " + nativeEnv + "=1 to run the native web chrome smoke test")
	}
	if os.Getenv("DISPLAY") == "" {
		t.Skip("no DISPLAY; run under xvfb-run")
	}
	for _, tool := range []string{"xdotool", "import"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed: %v", tool, err)
		}
	}
}

// repoRoot walks up from this file to the module root, so the test can build
// the child the way the Makefile does without depending on the caller's
// working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("module root not found above the test directory")
		}
		dir = parent
	}
}

// stubInstance is the smallest thing the child believes is a kube-workspaces
// instance: a browser-session grant, and a page to land on once the grant is
// redeemed. The child reaches no other endpoint with --namespace set.
type stubInstance struct {
	*httptest.Server
	grants atomic.Int64
}

func newStubInstance(t *testing.T) *stubInstance {
	t.Helper()
	s := &stubInstance{}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/browser-session/grant", func(w http.ResponseWriter, r *http.Request) {
		s.grants.Add(1)
		var body struct {
			Redirect string `json:"redirect"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("grant body: %v", err)
		}
		if !strings.HasPrefix(body.Redirect, "/proxy/") {
			t.Errorf("grant redirect = %q, want a /proxy/ workspace path", body.Redirect)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":"smoke","expires_at":%d}`, time.Now().Add(time.Hour).Unix())
	})
	mux.HandleFunc("/auth/browser-session", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("code") != "smoke" {
			http.Error(w, "unknown code", http.StatusBadRequest)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "kw-session", Value: "smoke"})
		http.Redirect(w, r, "/proxy/team/code/", http.StatusFound)
	})
	mux.HandleFunc("/proxy/team/code/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>smoke</title>
<body style="margin:0;background:#204060;color:#204060">.</body>`)
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// writeProfile points the child at the stub instance through an isolated
// config directory, so a test run never touches the developer's own profiles
// or tokens.
func writeProfile(t *testing.T, dir, server string) {
	t.Helper()
	cfgDir := filepath.Join(dir, "kube-workspaces")
	if err := os.MkdirAll(filepath.Join(cfgDir, "tokens"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"current": "smoke",
		"profiles": map[string]any{
			"smoke": map[string]string{"name": "smoke", "server": server},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "tokens", "smoke.token"), []byte("smoke.token.value"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// buildChild compiles the web child with cgo the way the Makefile's build-web
// target does.
func buildChild(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "kube-workspaces-web")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/kube-workspaces-web")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the web child: %v\n%s", err, b)
	}
	return out
}

// xrun runs one X automation command, failing the test with its output.
func xrun(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command("xdotool", append([]string{name}, args...)...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("xdotool %s %s: %v\n%s", name, strings.Join(args, " "), err, b)
	}
	return strings.TrimSpace(string(b))
}

// tryXrun runs one X automation command and tolerates failure, for the searches
// where "nothing matched yet" is the normal answer.
func tryXrun(name string, args ...string) string {
	cmd := exec.Command("xdotool", append([]string{name}, args...)...)
	b, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(b))
}

// waitForWindow searches for the child's window by title and returns its id.
// The child titles the window "<namespace>/<name>".
func waitForWindow(t *testing.T, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if id := strings.Split(tryXrun("search", "--name", "^team/code$"), "\n")[0]; id != "" {
			return id
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the web child never mapped a window")
	return ""
}

// screenshot captures one window's contents as an image. Reading pixels back
// is the point: a toolbar that draws nothing, draws in the wrong place, or
// fails to hide is invisible to any other assertion.
func screenshot(t *testing.T, window string) image.Image {
	t.Helper()
	cmd := exec.Command("import", "-silent", "-window", window, "png:-")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("import -window %s: %v\n%s", window, err, stderr.String())
	}
	img, err := png.Decode(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("decode screenshot: %v", err)
	}
	return img
}

func at(img image.Image, x, y int) color.RGBA {
	r, g, b, _ := img.At(x, y).RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 0xff}
}

func isPage(c color.RGBA) bool { return c == pageBackground }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// geometry is a window's size on the display.
type geometry struct{ W, H int }

// windowGeometry reads the window's current size from the X server, which is
// how "the window actually went fullscreen" is decided rather than assumed
// from the key press.
func windowGeometry(t *testing.T, window string) geometry {
	t.Helper()
	out := xrun(t, "getwindowgeometry", "--shell", window)
	var g geometry
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			continue
		}
		switch key {
		case "WIDTH":
			g.W = n
		case "HEIGHT":
			g.H = n
		}
	}
	if g.W == 0 || g.H == 0 {
		t.Fatalf("xdotool reported no geometry for window %s:\n%s", window, out)
	}
	return g
}

// nonPagePixels counts the pixels in a horizontal band that are not the page
// background, which measures how much chrome is on screen without depending on
// where inside the window the chrome happens to sit.
func nonPagePixels(img image.Image, top, height int) int {
	bounds := img.Bounds()
	n := 0
	for y := bounds.Min.Y + top; y < bounds.Min.Y+top+height && y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if !isPage(at(img, x, y)) {
				n++
			}
		}
	}
	return n
}

// run is the longest unbroken stretch of chrome in one row, which is what tells
// a full-width strip from a centred strip from the small reveal handle. The
// earlier bug this exists for: a transparent strip scored the same as no strip
// at all, because only the button glyphs were painted.
func run(img image.Image, y int) (start, length int) {
	bounds := img.Bounds()
	if y < bounds.Min.Y || y >= bounds.Max.Y {
		return 0, 0
	}
	best, current, from := 0, 0, 0
	for x := bounds.Min.X; x < bounds.Max.X; x++ {
		if isPage(at(img, x, y)) {
			current = 0
			continue
		}
		if current == 0 {
			from = x
		}
		current++
		if current > best {
			best, start = current, from
		}
	}
	return start - bounds.Min.X, best
}

// waitFor polls until check reports true, returning the last screenshot it
// took. Native chrome settles on its own schedule - WebKit under a virtual
// display can take seconds to finish a fullscreen resize - so the assertions
// wait for the state instead of racing it.
func waitFor(t *testing.T, window string, timeout time.Duration, what string, check func(image.Image) bool) image.Image {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var img image.Image
	for {
		img = screenshot(t, window)
		if check(img) {
			return img
		}
		if time.Now().After(deadline) {
			start, length := run(img, 6)
			t.Fatalf("timed out after %s waiting for %s: %dx%d screenshot, chrome run at row 6 is %dpx from x=%d",
				timeout, what, img.Bounds().Dx(), img.Bounds().Dy(), length, start)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// distinctColours counts the different colours in a horizontal band, which is
// how "the chrome is actually drawn" is told apart from "something filled the
// window with one colour".
func distinctColours(img image.Image, top, height int) int {
	seen := map[color.RGBA]bool{}
	bounds := img.Bounds()
	for y := bounds.Min.Y + top; y < bounds.Min.Y+top+height && y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			seen[at(img, x, y)] = true
			if len(seen) > 8 {
				return len(seen)
			}
		}
	}
	return len(seen)
}

// TestWebChromeNative runs the real child on a real display and checks the
// windowed strip, the fullscreen auto-hide and reveal, and Disconnect.
func TestWebChromeNative(t *testing.T) {
	skipNative(t)

	stub := newStubInstance(t)
	home := t.TempDir()
	writeProfile(t, home, stub.URL)

	child := buildChild(t)
	cmd := exec.Command(child, "--namespace", "team", "code")
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+home,
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
		"XDG_DATA_HOME="+filepath.Join(home, "data"),
		// WebKitGTK composites through DMA-BUF and GL by default, neither of
		// which a virtual display has; ask it for software rendering instead
		// of failing to draw.
		"WEBKIT_DISABLE_COMPOSITING_MODE=1",
		"WEBKIT_DISABLE_DMABUF_RENDERER=1",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	window := waitForWindow(t, 60*time.Second)
	xrun(t, "windowfocus", "--sync", window)
	xrun(t, "windowmove", window, "0", "0")

	// The page must have loaded before any of this means anything: an empty
	// window would also fail every "the strip is not here" assertion.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		img := screenshot(t, window)
		if img.Bounds().Dx() > 400 && isPage(at(img, img.Bounds().Dx()/2, img.Bounds().Dy()/2)) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	windowed := waitFor(t, window, 30*time.Second, "the page to load behind the windowed strip", func(img image.Image) bool {
		b := img.Bounds()
		return b.Dx() > 400 && b.Dy() > 300 && isPage(at(img, b.Dx()/2, b.Dy()/2))
	})
	bounds := windowed.Bounds()
	if bounds.Dx() < 400 || bounds.Dy() < 300 {
		t.Fatalf("child window is %dx%d, too small to hold the page and a strip", bounds.Dx(), bounds.Dy())
	}
	// Windowed chrome is a strip across the full width: one unbroken run of
	// chrome in the top rows, with the page starting below it.
	stripStart, stripWidth := run(windowed, 6)
	if stripWidth < bounds.Dx()-4 {
		t.Errorf("the windowed strip covers %dpx of a %dpx window from x=%d, want the full width", stripWidth, bounds.Dx(), stripStart)
	}
	if isPage(at(windowed, 4, 4)) {
		t.Errorf("no chrome at the top-left corner: the windowed strip is missing (window %dx%d)", bounds.Dx(), bounds.Dy())
	}
	if n := distinctColours(windowed, 0, 40); n < 4 {
		t.Errorf("only %d colours in the top 40 rows: the strip looks empty", n)
	}
	// The page starts below the strip, so the middle of the window is the page.
	if !isPage(at(windowed, bounds.Dx()/2, bounds.Dy()/2)) {
		t.Errorf("the page is not visible below the strip: centre is %v, want the page colour", at(windowed, bounds.Dx()/2, bounds.Dy()/2))
	}

	// F11 turns the strip into floating chrome: capped in width, centred, and
	// hidden after its idle delay until the pointer or the hotkey brings it
	// back. The pointer is parked on the top edge first: hovering the chrome
	// keeps it up while WebKit finishes the fullscreen resize, which under a
	// virtual display takes longer than the idle delay.
	xrun(t, "key", "--clearmodifiers", "F11")
	xrun(t, "mousemove", strconv.Itoa(bounds.Dx()/2), "5")
	waitFor(t, window, 30*time.Second, "the window to go fullscreen", func(img image.Image) bool {
		g := windowGeometry(t, window)
		return g.W > bounds.Dx() && g.H > bounds.Dy()
	})
	full := windowGeometry(t, window)
	floating := waitFor(t, window, 30*time.Second, "the strip to float centred in fullscreen", func(img image.Image) bool {
		start, width := run(img, 6)
		return width >= 900 && abs(start+width/2-full.W/2) <= 20
	})
	start, width := run(floating, 6)
	if width > full.W {
		t.Errorf("the fullscreen strip is %dpx wide, wider than the %dpx window", width, full.W)
	}
	if !isPage(at(floating, 4, 4)) || !isPage(at(floating, full.W-5, 4)) {
		t.Errorf("the fullscreen strip runs to the window edges: top-left %v, top-right %v",
			at(floating, 4, 4), at(floating, full.W-5, 4))
	}
	if n := distinctColours(floating, 0, 40); n < 4 {
		t.Errorf("the floating strip looks empty (%d colours in the top 40 rows)", n)
	}

	// Pointer input over the strip is local: with the pointer on the strip it
	// must stay put and keep its chrome.
	overBar := screenshot(t, window)
	if _, over := run(overBar, 6); over < 900 {
		t.Errorf("the strip vanished while the pointer was over it (%dpx of chrome)", over)
	}

	// Off the strip, the idle delay hides it and the reveal handle takes its
	// place: a small centred control, with the page back at both top corners.
	xrun(t, "mousemove", strconv.Itoa(full.W/2), strconv.Itoa(full.H/2))
	hidden := waitFor(t, window, 30*time.Second, "the strip to auto-hide and the reveal handle to appear", func(img image.Image) bool {
		_, width := run(img, 6)
		return width > 0 && width < 300
	})
	hiddenStart, hiddenWidth := run(hidden, 6)
	if hiddenPaint := nonPagePixels(hidden, 0, 24); hiddenPaint < 200 {
		t.Errorf("no reveal handle in the hidden state (%d painted pixels in the top 24 rows)", hiddenPaint)
	}
	if !isPage(at(hidden, 4, 4)) || !isPage(at(hidden, full.W-5, 4)) {
		t.Errorf("chrome is still drawn edge to edge while hidden: top-left %v, top-right %v",
			at(hidden, 4, 4), at(hidden, full.W-5, 4))
	}
	if abs(hiddenStart+hiddenWidth/2-full.W/2) > 20 {
		t.Errorf("the reveal handle is not centred: x=%d..%d of %d", hiddenStart, hiddenStart+hiddenWidth, full.W)
	}

	// One hotkey brings the strip back, still centred.
	xrun(t, "key", "--clearmodifiers", "ctrl+alt+shift+t")
	revealed := waitFor(t, window, 30*time.Second, "Ctrl+Alt+Shift+T to bring the strip back", func(img image.Image) bool {
		s, width := run(img, 6)
		return width >= 900 && abs(s+width/2-full.W/2) <= 20
	})
	if revealedStart, revealedWidth := run(revealed, 6); revealedWidth < 5*hiddenWidth {
		t.Errorf("Ctrl+Alt+Shift+T did not bring the strip back: %dpx of chrome across the top (x=%d), was %dpx while hidden",
			revealedWidth, revealedStart, hiddenWidth)
	}

	// The grip drags the floating strip along the top edge, and the strip
	// stays on screen afterwards.
	dragFrom := start + 14
	xrun(t, "mousemove", strconv.Itoa(dragFrom), "16")
	time.Sleep(200 * time.Millisecond)
	xrun(t, "mousedown", "1")
	time.Sleep(200 * time.Millisecond)
	for step := 1; step <= 10; step++ {
		xrun(t, "mousemove", strconv.Itoa(dragFrom+step*15), "16")
		time.Sleep(40 * time.Millisecond)
	}
	dragged := screenshot(t, window)
	xrun(t, "mouseup", "1")
	draggedStart, draggedWidth := run(dragged, 6)
	if abs((draggedStart-start)-150) > 30 {
		t.Errorf("dragging the grip moved the strip %dpx, want about 150px (strip was at x=%d, now x=%d)", draggedStart-start, start, draggedStart)
	}
	if draggedWidth < 900 {
		t.Errorf("the strip shrank while dragging: %dpx of chrome", draggedWidth)
	}
	if draggedStart < 0 || draggedStart+draggedWidth > full.W {
		t.Errorf("the dragged strip left the window: x=%d..%d of %d", draggedStart, draggedStart+draggedWidth, full.W)
	}

	// F11 again restores the windowed full-width strip.
	xrun(t, "mousemove", "0", "0")
	xrun(t, "key", "--clearmodifiers", "F11")
	back := waitFor(t, window, 30*time.Second, "the window to leave fullscreen", func(img image.Image) bool {
		g := windowGeometry(t, window)
		_, width := run(img, 6)
		return g.W < full.W && width > g.W-4
	})
	if backStart, backWidth := run(back, 6); backWidth < back.Bounds().Dx()-4 || backStart != 0 {
		t.Errorf("the strip did not return to the full window width: x=%d..%d of %d", backStart, backStart+backWidth, back.Bounds().Dx())
	}
	xrun(t, "mousemove", "0", "0")

	// Ctrl+Alt+Q is a true Disconnect: the child closes its window and exits.
	xrun(t, "key", "--clearmodifiers", "ctrl+alt+q")
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("Disconnect exited the child with %v\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("Disconnect did not end the child window\nstderr:\n%s", stderr.String())
	}
	if stub.grants.Load() == 0 {
		t.Error("the child never asked for a browser session: the run did not get as far as the page")
	}
}
