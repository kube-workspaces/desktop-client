// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build windows && cgo

// The Win32 native toolbar smoke test. [toolbar_windows.go] subclasses the
// browser window, insets the WebView2 child under a strip in windowed mode,
// floats and centres that strip over the page in fullscreen, drags it, hides it
// after three idle seconds, and routes six buttons back into Go. None of that
// runs in the cgo-free `go test ./...` the rest of the tree uses, and none of it
// can be observed from outside the process on any other platform, so this test
// builds the toolbar onto a synthetic window tree — a top-level host plus a
// "webview_widget" child, which is all kw_attach looks for — and then asks
// user32 where every piece of chrome actually ended up.
//
// It is opt-in and self-skipping:
//
//	KW_WEB_CHROME_NATIVE=1 go test ./internal/web -run Win32Toolbar -v
//
// The same test runs against Wine on Linux, which is how it is verified there:
//
//	GOOS=windows GOARCH=amd64 CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc \
//	  go test -c ./internal/web -o /tmp/web.test.exe
//	xvfb-run -a wine /tmp/web.test.exe -test.run Win32Toolbar -test.v
package web

import (
	"errors"
	"os"
	"runtime"
	"runtime/cgo"
	"sort"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Win32 constants this test needs. x/sys/windows does not export the
// window-management half of user32, so the expectations are spelled out here
// rather than pulled from a binding the test would then be comparing the
// toolbar against itself with.
const (
	wsOverlappedWindow = 0x00CF0000
	wsChild            = 0x40000000
	wsVisible          = 0x10000000
	wsPopup            = 0x80000000
	wsCaption          = 0x00C00000

	wmMouseMove   = 0x0200
	wmLButtonDown = 0x0201
	wmLButtonUp   = 0x0202
	wmCommand     = 0x0111
	wmTimer       = 0x0113
	wmHotkey      = 0x0312

	bnClicked = 0

	// -16 and -4 in the pointer width the toolbar is compiled for.
	gwlStyle   uintptr = 0xFFFFFFFFFFFFFFF0
	gwlWndProc uintptr = 0xFFFFFFFFFFFFFFFC

	whiteBrush = 4

	// The hotkey and timer ids kw_hotkeys and kw_window_proc use.
	hotkeyReveal     = 12306
	hotkeyDisconnect = 12307
	timerIdle        = 12304

	// The window text of the reveal handle kw_attach creates in fullscreen.
	handleText = "Tools"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procUnregisterClassW = user32.NewProc("UnregisterClassW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procGetWindowRect    = user32.NewProc("GetWindowRect")
	procGetClientRect    = user32.NewProc("GetClientRect")
	procClientToScreen   = user32.NewProc("ClientToScreen")
	procGetWindowTextW   = user32.NewProc("GetWindowTextW")
	procFindWindowExW    = user32.NewProc("FindWindowExW")
	procIsWindow         = user32.NewProc("IsWindow")
	procIsWindowVisible  = user32.NewProc("IsWindowVisible")
	procSendMessageW     = user32.NewProc("SendMessageW")
	procSetFocus         = user32.NewProc("SetFocus")
	procSetCursorPos     = user32.NewProc("SetCursorPos")
	procGetStockObject   = gdi32.NewProc("GetStockObject")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")

	// GetWindowLongPtr only exists as a distinct export in 64-bit user32.
	procGetWindowLong = func() *windows.LazyProc {
		if unsafe.Sizeof(uintptr(0)) == 8 {
			return user32.NewProc("GetWindowLongPtrW")
		}
		return user32.NewProc("GetWindowLongW")
	}()
)

type rect struct{ left, top, right, bottom int32 }

type point struct{ x, y int32 }

// wndClassEx mirrors tagWNDCLASSEXW. The field order and the padding Go adds
// for the pointer-sized fields line up with windows.h on both amd64 and 386.
type wndClassEx struct {
	cbSize                        uint32
	style                         uint32
	proc                          uintptr
	clsExtra, wndExtra            int32
	instance, icon, cursor, brush uintptr
	menu, class                   *uint16
	iconSmall                     uintptr
}

// TestWin32Toolbar exercises the whole Win32 toolbar: layout in both modes, the
// drag, pin, idle auto-hide, reveal, the Go callback wiring, and teardown.
func TestWin32Toolbar(t *testing.T) {
	if os.Getenv("KW_WEB_CHROME_NATIVE") == "" {
		t.Skip("set KW_WEB_CHROME_NATIVE=1 to run the Win32 native toolbar test")
	}
	// A Win32 window belongs to the thread that created it, and SendMessage to a
	// window owned by another thread is delivered through that thread's queue and
	// waits for it to pump. Go moves goroutines between OS threads, so this test
	// stays on one for its whole run.
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)

	instance, _, _ := procGetModuleHandleW.Call(0)
	defProc := windows.NewCallback(func(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
		r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
		return r
	})
	// The host class is per-run so a previous crashed run cannot leave a stale
	// registration behind. The child class name is fixed because kw_attach
	// looks the WebView2 widget up by exactly "webview_widget".
	const hostClass, webClass = "KwToolbarTestHost", "webview_widget"
	registerClass(t, hostClass, defProc, instance)
	registerClass(t, webClass, defProc, instance)
	t.Cleanup(func() {
		unregisterClass(t, webClass, instance)
		unregisterClass(t, hostClass, instance)
	})

	host := createWindow(t, hostClass, "chrome host", wsOverlappedWindow|wsVisible, 120, 120, 1200, 760, 0, instance)
	t.Cleanup(func() { destroyWindow(t, host) })
	// The real child is WebView2's. kw_attach only needs a direct child with
	// this class and places it itself, so an empty one is enough.
	page := createWindow(t, webClass, "page", wsChild|wsVisible, 0, 0, 0, 0, host, instance)

	originalProc := getWindowLong(t, host, gwlWndProc)
	var actions []int
	token := cgo.NewHandle(func(action int) { actions = append(actions, action) })
	t.Cleanup(token.Delete)

	labels := "code · Webview\nFullscreen\nSessions\nWorkspace list\nConnection\nPin\nDisconnect"
	detach, err := attachNativeToolbar(hwndPtr(host), uintptr(token), labels, "code\nTransport: Webview")
	if err != nil {
		t.Fatalf("attachNativeToolbar: %v", err)
	}
	if got := getWindowLong(t, host, gwlWndProc); got == originalProc {
		t.Fatalf("window procedure was not subclassed (still %#x)", got)
	}

	bar := findChild(t, host, "STATIC")
	if got := windowText(t, findChild(t, bar, "STATIC")); got != "code · Webview" {
		t.Errorf("identity text = %q, want the joined title and surface", got)
	}
	handle := findChild(t, host, "BUTTON")
	if got := windowText(t, handle); got != handleText {
		t.Errorf("reveal handle text = %q, want %q", got, handleText)
	}
	wantLabels := []string{"Fullscreen", "Sessions", "Workspace list", "Connection", "Pin", "Disconnect"}
	if got := buttonTexts(t, bar); !equal(got, wantLabels) {
		t.Errorf("button labels = %q, want %q", got, wantLabels)
	}
	fullscreenButton := buttonWith(t, bar, "Fullscreen")
	pinButton := buttonWith(t, bar, "Pin")

	// Windowed: the strip spans the client area and the page is pushed down by
	// exactly the strip, with the reveal handle out of the way.
	cw, ch := clientSize(t, host)
	barR := clientRect(t, bar, host)
	if barR.left != 0 || barR.right != cw {
		t.Errorf("windowed strip = %+v, want the full client width %d", barR, cw)
	}
	if r := clientRect(t, page, host); r.top != barR.bottom || r.right != cw || r.bottom != ch {
		t.Errorf("windowed page = %+v, want it to fill under the strip %+v in %dx%d", r, barR, cw, ch)
	}
	if visible(t, handle) {
		t.Error("reveal handle is visible in windowed mode")
	}
	if !visible(t, bar) {
		t.Error("the strip is not visible after attaching")
	}

	// Fullscreen: the strip floats, centred and narrower than the page, and the
	// page gets the whole client area back.
	sendCommand(t, bar, 1)
	style := getWindowLong(t, host, gwlStyle)
	if style&wsPopup == 0 || style&wsCaption != 0 {
		t.Errorf("fullscreen style = %#x, want a borderless popup", style)
	}
	cw, ch = clientSize(t, host)
	barR = clientRect(t, bar, host)
	barW := int(barR.right - barR.left)
	if barW >= int(cw) {
		t.Errorf("fullscreen strip is %dpx wide, want it narrower than the %dpx client", barW, cw)
	}
	if want := (int(cw) - barW) / 2; abs(int(barR.left)-want) > 2 {
		t.Errorf("fullscreen strip left = %d, want it centred at %d", barR.left, want)
	}
	if r := clientRect(t, page, host); r.top != 0 || r.bottom != ch || r.right != cw {
		t.Errorf("fullscreen page = %+v, want the whole client area %dx%d", r, cw, ch)
	}
	if visible(t, handle) {
		t.Error("the reveal handle is shown alongside the strip")
	}
	if got := windowText(t, fullscreenButton); got != "Windowed" {
		t.Errorf("fullscreen toggle now reads %q, want %q", got, "Windowed")
	}

	// Drag: grabbing the strip 40px in and moving the pointer 100px right moves
	// the strip 100px right, and no further than the client area.
	awayFromBar(t, host)
	origin := clientOrigin(t, host)
	grab := origin.x + barR.left + 40
	setCursor(t, grab, origin.y+barR.bottom-4)
	sendMessage(t, bar, wmLButtonDown, 0, 40)
	setCursor(t, grab+100, origin.y+barR.bottom-4)
	sendMessage(t, bar, wmMouseMove, 1, 0)
	sendMessage(t, bar, wmLButtonUp, 0, 0)
	moved := clientRect(t, bar, host)
	movedW := int(moved.right - moved.left)
	wantLeft := min((int(cw)-movedW)/2+100, int(cw)-movedW)
	if abs(int(moved.left)-wantLeft) > 2 {
		t.Errorf("dragged strip left = %d, want %d", moved.left, wantLeft)
	}
	if moved.left < 0 || moved.right > cw {
		t.Errorf("dragged strip = %+v, want it inside the %dpx client area", moved, cw)
	}

	// Pin: the strip survives the idle window that hides an unpinned one.
	sendCommand(t, bar, 5)
	if got := windowText(t, pinButton); got != "Unpin" {
		t.Fatalf("pin toggle now reads %q, want %q", got, "Unpin")
	}
	awayFromBar(t, host)
	idle(t, host)
	if !visible(t, bar) {
		t.Error("the pinned strip auto-hid")
	}
	sendCommand(t, bar, 5)
	if got := windowText(t, pinButton); got != "Pin" {
		t.Fatalf("unpin toggle now reads %q, want %q", got, "Pin")
	}

	// Unpinned and idle, with the pointer off the strip and focus in the page,
	// the strip trades itself for the small reveal handle.
	setFocus(t, page)
	idle(t, host)
	if visible(t, bar) {
		t.Error("the strip is still visible after the idle timeout")
	}
	if !visible(t, handle) {
		t.Error("the reveal handle is not shown after the idle timeout")
	}

	// The reveal hotkey brings the whole strip back.
	sendMessage(t, host, wmHotkey, hotkeyReveal, 0)
	if !visible(t, bar) {
		t.Fatal("the reveal hotkey did not bring the strip back")
	}
	if visible(t, handle) {
		t.Error("the reveal handle is still shown alongside the strip")
	}

	// The disconnect hotkey is the one action that crosses back into Go.
	setFocus(t, page)
	sendMessage(t, host, wmHotkey, hotkeyDisconnect, 0)
	if len(actions) != 1 || actions[0] != 6 {
		t.Errorf("reported actions = %v, want a single disconnect (6)", actions)
	}

	// Back to windowed, the strip is a full-width band again.
	sendCommand(t, bar, 1)
	cw, ch = clientSize(t, host)
	barR = clientRect(t, bar, host)
	if barR.left != 0 || barR.right != cw {
		t.Errorf("restored strip = %+v, want the full client width %d", barR, cw)
	}
	if r := clientRect(t, page, host); r.top != barR.bottom || r.bottom != ch || r.right != cw {
		t.Errorf("restored page = %+v, want it to fill under the strip %+v in %dx%d", r, barR, cw, ch)
	}
	if visible(t, handle) {
		t.Error("reveal handle is visible back in windowed mode")
	}

	// Teardown restores the window procedure and takes the chrome with it.
	detach()
	if got := getWindowLong(t, host, gwlWndProc); got != originalProc {
		t.Errorf("window procedure = %#x after detach, want the original %#x", got, originalProc)
	}
	if isWindow(t, bar) {
		t.Error("the strip outlived the toolbar")
	}
	if isWindow(t, handle) {
		t.Error("the reveal handle outlived the toolbar")
	}
}

// registerClass registers a window class whose procedure forwards to
// DefWindowProc, which is all the synthetic host and page need: the toolbar is
// what is under test.
func registerClass(t *testing.T, name string, proc uintptr, instance uintptr) {
	t.Helper()
	wc := wndClassEx{
		cbSize:   uint32(unsafe.Sizeof(wndClassEx{})),
		proc:     proc,
		instance: instance,
		brush:    stockObject(whiteBrush),
		class:    utf16(name),
	}
	if ok, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); ok == 0 && !errors.Is(err, windows.ERROR_CLASS_ALREADY_EXISTS) {
		t.Fatalf("RegisterClassExW(%s): %v", name, err)
	}
}

func unregisterClass(t *testing.T, name string, instance uintptr) {
	t.Helper()
	procUnregisterClassW.Call(uintptr(unsafe.Pointer(utf16(name))), instance)
}

func createWindow(t *testing.T, class, title string, style uint32, x, y, w, h int32, parent, instance uintptr) uintptr {
	t.Helper()
	hwnd, _, err := procCreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(utf16(class))), uintptr(unsafe.Pointer(utf16(title))),
		uintptr(style), uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, 0, instance, 0,
	)
	if hwnd == 0 {
		t.Fatalf("CreateWindowExW(%s): %v", class, err)
	}
	return hwnd
}

func destroyWindow(t *testing.T, hwnd uintptr) {
	t.Helper()
	if hwnd != 0 {
		procDestroyWindow.Call(hwnd)
	}
}

func findChild(t *testing.T, parent uintptr, class string) uintptr {
	t.Helper()
	children := findChildren(t, parent, class)
	if len(children) != 1 {
		t.Fatalf("found %d %s children, want 1", len(children), class)
	}
	return children[0]
}

func findChildren(t *testing.T, parent uintptr, class string) []uintptr {
	t.Helper()
	var found []uintptr
	for after := uintptr(0); ; after = found[len(found)-1] {
		child, _, _ := procFindWindowExW.Call(parent, after, uintptr(unsafe.Pointer(utf16(class))), 0)
		if child == 0 {
			return found
		}
		found = append(found, child)
	}
}

func windowText(t *testing.T, hwnd uintptr) string {
	t.Helper()
	buf := make([]uint16, 128)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf)
}

// buttonTexts lists the strip's button labels. FindWindowEx walks the child
// z-order, not the creation order, so the labels are compared as a set.
func buttonTexts(t *testing.T, bar uintptr) []string {
	t.Helper()
	var texts []string
	for _, button := range findChildren(t, bar, "BUTTON") {
		texts = append(texts, windowText(t, button))
	}
	return texts
}

func buttonWith(t *testing.T, bar uintptr, label string) uintptr {
	t.Helper()
	for _, button := range findChildren(t, bar, "BUTTON") {
		if windowText(t, button) == label {
			return button
		}
	}
	t.Fatalf("no button labelled %q", label)
	return 0
}

func equal(got, want []string) bool {
	got, want = append([]string(nil), got...), append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// clientSize is a window's client area, the coordinate space the toolbar places
// its chrome in.
func clientSize(t *testing.T, hwnd uintptr) (int32, int32) {
	t.Helper()
	var r rect
	if ok, _, err := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		t.Fatalf("GetClientRect: %v", err)
	}
	return r.right, r.bottom
}

// screenRect is where a window sits on the desktop.
func screenRect(t *testing.T, hwnd uintptr) rect {
	t.Helper()
	var r rect
	if ok, _, err := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		t.Fatalf("GetWindowRect: %v", err)
	}
	return r
}

func clientOrigin(t *testing.T, parent uintptr) point {
	t.Helper()
	var origin point
	if ok, _, err := procClientToScreen.Call(parent, uintptr(unsafe.Pointer(&origin))); ok == 0 {
		t.Fatalf("ClientToScreen: %v", err)
	}
	return origin
}

// clientRect reports a child window in its parent's client coordinates, which
// is where every placement in toolbar_windows.go is expressed.
func clientRect(t *testing.T, hwnd, parent uintptr) rect {
	t.Helper()
	r := screenRect(t, hwnd)
	o := clientOrigin(t, parent)
	r.left -= o.x
	r.top -= o.y
	r.right -= o.x
	r.bottom -= o.y
	return r
}

func getWindowLong(t *testing.T, hwnd, index uintptr) uintptr {
	t.Helper()
	v, _, _ := procGetWindowLong.Call(hwnd, index)
	return v
}

// sendCommand clicks a toolbar button the way the button itself would. The bar
// only reads the action out of the low word of wParam, so the button handle is
// not needed here.
func sendCommand(t *testing.T, bar uintptr, action int) {
	t.Helper()
	sendMessage(t, bar, wmCommand, uintptr(action)|uintptr(bnClicked)<<16, 0)
}

func sendMessage(t *testing.T, hwnd uintptr, msg, wParam, lParam uintptr) {
	t.Helper()
	procSendMessageW.Call(hwnd, uintptr(msg), wParam, lParam)
}

// idle waits out the strip's three second reveal window and then runs the check
// its 100ms timer would have run. Sending the message directly keeps the test
// exact instead of racing a timer.
func idle(t *testing.T, host uintptr) {
	t.Helper()
	time.Sleep(3300 * time.Millisecond)
	sendMessage(t, host, wmTimer, timerIdle, 0)
}

// awayFromBar parks the pointer in the host window's top left corner, outside
// both the floating strip and the centred reveal handle, so neither reads as
// hovered.
func awayFromBar(t *testing.T, host uintptr) {
	t.Helper()
	r := screenRect(t, host)
	setCursor(t, r.left+1, r.top+1)
}

func setCursor(t *testing.T, x, y int32) {
	t.Helper()
	procSetCursorPos.Call(uintptr(x), uintptr(y))
}

func setFocus(t *testing.T, hwnd uintptr) {
	t.Helper()
	procSetFocus.Call(hwnd)
}

func visible(t *testing.T, hwnd uintptr) bool {
	t.Helper()
	ok, _, _ := procIsWindowVisible.Call(hwnd)
	return ok != 0
}

func isWindow(t *testing.T, hwnd uintptr) bool {
	t.Helper()
	ok, _, _ := procIsWindow.Call(hwnd)
	return ok != 0
}

func stockObject(index uintptr) uintptr {
	v, _, _ := procGetStockObject.Call(index)
	return v
}

// utf16 keeps the string pointer alive for as long as the window it names, so
// a garbage collection between setup and the first Win32 call that reads it
// cannot take the registration or the class lookup down with it.
var utf16KeepAlive [][]uint16

func utf16(s string) *uint16 {
	buf, err := windows.UTF16FromString(s)
	if err != nil {
		panic(err)
	}
	utf16KeepAlive = append(utf16KeepAlive, buf)
	return &buf[0]
}

// hwndPtr hands an HWND to the toolbar as the unsafe.Pointer it takes. The value
// came from a user32 call on this thread and is only meaningful while that
// window lives, which the test guarantees for the whole run.
func hwndPtr(hwnd uintptr) unsafe.Pointer { return unsafe.Pointer(hwnd) } //nolint:govet // HWND, not a Go pointer

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
