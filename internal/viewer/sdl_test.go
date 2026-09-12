// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"testing"

	"github.com/Zyko0/go-sdl3/sdl"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

// These tests exercise the SDL backend's pure translation layer. They import
// the SDL binding but never load the library: everything below is table
// lookup and arithmetic, so it runs on a machine with no display and no SDL
// installed. Anything that needs a window is not unit-testable and is not
// tested here.

// sdlKeyAliases lists the scancodes that deliberately share a keysym.Key with
// another, more canonical scancode, together with the reason. Every group of
// colliding scancodes must consist of exactly one canonical entry plus aliases
// listed here; anything else is a mistake in the table.
var sdlKeyAliases = map[sdl.Scancode]string{
	sdl.SCANCODE_MENU:       "SDL reports the context-menu key as either APPLICATION or MENU",
	sdl.SCANCODE_RETURN2:    "RETURN2 is the legacy duplicate of RETURN",
	sdl.SCANCODE_PRIOR:      "PRIOR is the legacy name for PAGEUP",
	sdl.SCANCODE_SEPARATOR:  "SEPARATOR and KP_COMMA are the same physical keypad key",
	sdl.SCANCODE_MEDIA_PLAY: "a keyboard's play/pause key is XF86AudioPlay either way",
}

func TestScancodeKeysHasNoAccidentalDuplicates(t *testing.T) {
	seen := make(map[keysym.Key][]sdl.Scancode)
	for sc, key := range scancodeKeys {
		seen[key] = append(seen[key], sc)
	}
	for key, codes := range seen {
		if len(codes) == 1 {
			continue
		}
		canonical := make([]sdl.Scancode, 0, 1)
		for _, sc := range codes {
			if _, ok := sdlKeyAliases[sc]; !ok {
				canonical = append(canonical, sc)
			}
		}
		if len(canonical) != 1 {
			t.Errorf("scancodes %v all map to %s with %d of them undocumented (%v); "+
				"add the duplicates to sdlKeyAliases with a reason, or fix the table",
				codes, key, len(canonical), canonical)
		}
	}

	// An alias that no longer collides is stale documentation.
	for sc := range sdlKeyAliases {
		key, ok := scancodeKeys[sc]
		if !ok {
			t.Errorf("sdlKeyAliases lists scancode %d, which is not in the table", sc)
			continue
		}
		if len(seen[key]) < 2 {
			t.Errorf("sdlKeyAliases lists scancode %d as an alias for %s, but nothing else maps there", sc, key)
		}
	}
}

func TestScancodeKeysAreAllReal(t *testing.T) {
	for sc, key := range scancodeKeys {
		if key == keysym.KeyUnknown {
			t.Errorf("scancode %d maps to KeyUnknown; omit it instead", sc)
		}
		if key.Keysym() == keysym.NoSymbol {
			t.Errorf("scancode %d maps to %s, which has no keysym and must never be sent", sc, key)
		}
		if sc == sdl.SCANCODE_UNKNOWN {
			t.Error("SCANCODE_UNKNOWN must not be in the table")
		}
	}
}

// TestScancodeKeysCoversEveryKeyWeCareAbout is the missing-entry half of the
// contract. A key that falls out of this table is not simply dropped: it falls
// through to the layout path, which for a non-text key produces nothing at
// all, so the guest silently never sees it.
func TestScancodeKeysCoversEveryKeyWeCareAbout(t *testing.T) {
	required := map[sdl.Scancode]keysym.Key{
		// Editing and control.
		sdl.SCANCODE_RETURN:    keysym.KeyReturn,
		sdl.SCANCODE_ESCAPE:    keysym.KeyEscape,
		sdl.SCANCODE_BACKSPACE: keysym.KeyBackSpace,
		sdl.SCANCODE_TAB:       keysym.KeyTab,
		sdl.SCANCODE_DELETE:    keysym.KeyDelete,
		sdl.SCANCODE_INSERT:    keysym.KeyInsert,

		// Navigation.
		sdl.SCANCODE_HOME:     keysym.KeyHome,
		sdl.SCANCODE_END:      keysym.KeyEnd,
		sdl.SCANCODE_PAGEUP:   keysym.KeyPageUp,
		sdl.SCANCODE_PAGEDOWN: keysym.KeyPageDown,
		sdl.SCANCODE_LEFT:     keysym.KeyLeft,
		sdl.SCANCODE_RIGHT:    keysym.KeyRight,
		sdl.SCANCODE_UP:       keysym.KeyUp,
		sdl.SCANCODE_DOWN:     keysym.KeyDown,

		// Locks and system keys.
		sdl.SCANCODE_CAPSLOCK:     keysym.KeyCapsLock,
		sdl.SCANCODE_NUMLOCKCLEAR: keysym.KeyNumLock,
		sdl.SCANCODE_SCROLLLOCK:   keysym.KeyScrollLock,
		sdl.SCANCODE_PAUSE:        keysym.KeyPause,
		sdl.SCANCODE_PRINTSCREEN:  keysym.KeyPrint,
		sdl.SCANCODE_SYSREQ:       keysym.KeySysReq,
		sdl.SCANCODE_APPLICATION:  keysym.KeyMenu,

		// Modifiers. All eight must be present and must keep left and right
		// distinct: guests bind them separately, and a viewer that collapses
		// them cannot release the one it actually sent.
		sdl.SCANCODE_LCTRL:  keysym.KeyControlL,
		sdl.SCANCODE_RCTRL:  keysym.KeyControlR,
		sdl.SCANCODE_LSHIFT: keysym.KeyShiftL,
		sdl.SCANCODE_RSHIFT: keysym.KeyShiftR,
		sdl.SCANCODE_LALT:   keysym.KeyAltL,
		sdl.SCANCODE_RALT:   keysym.KeyAltR,
		sdl.SCANCODE_LGUI:   keysym.KeySuperL,
		sdl.SCANCODE_RGUI:   keysym.KeySuperR,

		// Keypad. The guest distinguishes KP_Enter from Return and KP_1 from
		// End, so these must not fall through to the character path.
		sdl.SCANCODE_KP_ENTER:    keysym.KeyKPEnter,
		sdl.SCANCODE_KP_0:        keysym.KeyKP0,
		sdl.SCANCODE_KP_1:        keysym.KeyKP1,
		sdl.SCANCODE_KP_2:        keysym.KeyKP2,
		sdl.SCANCODE_KP_3:        keysym.KeyKP3,
		sdl.SCANCODE_KP_4:        keysym.KeyKP4,
		sdl.SCANCODE_KP_5:        keysym.KeyKP5,
		sdl.SCANCODE_KP_6:        keysym.KeyKP6,
		sdl.SCANCODE_KP_7:        keysym.KeyKP7,
		sdl.SCANCODE_KP_8:        keysym.KeyKP8,
		sdl.SCANCODE_KP_9:        keysym.KeyKP9,
		sdl.SCANCODE_KP_PERIOD:   keysym.KeyKPDecimal,
		sdl.SCANCODE_KP_PLUS:     keysym.KeyKPAdd,
		sdl.SCANCODE_KP_MINUS:    keysym.KeyKPSubtract,
		sdl.SCANCODE_KP_MULTIPLY: keysym.KeyKPMultiply,
		sdl.SCANCODE_KP_DIVIDE:   keysym.KeyKPDivide,
	}
	for sc, want := range required {
		got, ok := scancodeKeys[sc]
		if !ok {
			t.Errorf("scancode %d is missing from the table (want %s)", sc, want)
			continue
		}
		if got != want {
			t.Errorf("scancode %d maps to %s, want %s", sc, got, want)
		}
	}

	// Every function key, checked by construction rather than by listing.
	fkeys := []sdl.Scancode{
		sdl.SCANCODE_F1, sdl.SCANCODE_F2, sdl.SCANCODE_F3, sdl.SCANCODE_F4,
		sdl.SCANCODE_F5, sdl.SCANCODE_F6, sdl.SCANCODE_F7, sdl.SCANCODE_F8,
		sdl.SCANCODE_F9, sdl.SCANCODE_F10, sdl.SCANCODE_F11, sdl.SCANCODE_F12,
		sdl.SCANCODE_F13, sdl.SCANCODE_F14, sdl.SCANCODE_F15, sdl.SCANCODE_F16,
		sdl.SCANCODE_F17, sdl.SCANCODE_F18, sdl.SCANCODE_F19, sdl.SCANCODE_F20,
		sdl.SCANCODE_F21, sdl.SCANCODE_F22, sdl.SCANCODE_F23, sdl.SCANCODE_F24,
	}
	for i, sc := range fkeys {
		want, _ := keysym.FunctionKey(i + 1)
		if got := scancodeKeys[sc]; got != want {
			t.Errorf("F%d (scancode %d) maps to %s, want %s", i+1, sc, got, want)
		}
	}
}

// TestScancodeKeysExcludesTextKeys protects the other side of the design: text
// keys must be resolved through the keyboard layout, not through a table. A
// letter in this table would send a US-layout keysym to a user typing on a
// French or German keyboard.
func TestScancodeKeysExcludesTextKeys(t *testing.T) {
	textScancodes := []sdl.Scancode{
		sdl.SCANCODE_A, sdl.SCANCODE_M, sdl.SCANCODE_Z,
		sdl.SCANCODE_0, sdl.SCANCODE_1, sdl.SCANCODE_9,
		sdl.SCANCODE_SPACE, sdl.SCANCODE_MINUS, sdl.SCANCODE_EQUALS,
		sdl.SCANCODE_LEFTBRACKET, sdl.SCANCODE_RIGHTBRACKET, sdl.SCANCODE_BACKSLASH,
		sdl.SCANCODE_SEMICOLON, sdl.SCANCODE_APOSTROPHE, sdl.SCANCODE_GRAVE,
		sdl.SCANCODE_COMMA, sdl.SCANCODE_PERIOD, sdl.SCANCODE_SLASH,
		sdl.SCANCODE_NONUSBACKSLASH, sdl.SCANCODE_NONUSHASH,
	}
	for _, sc := range textScancodes {
		if key, ok := scancodeKeys[sc]; ok {
			t.Errorf("text scancode %d must not be in the table (maps to %s)", sc, key)
		}
	}
}

func TestKeyForScancodeUnknown(t *testing.T) {
	if got := keyForScancode(sdl.SCANCODE_UNKNOWN); got != keysym.KeyUnknown {
		t.Fatalf("keyForScancode(UNKNOWN) = %s, want unknown", got)
	}
	if got := keyForScancode(sdl.SCANCODE_A); got != keysym.KeyUnknown {
		t.Fatalf("keyForScancode(A) = %s, want unknown so the layout resolves it", got)
	}
}

func TestRuneFromKeycode(t *testing.T) {
	tests := []struct {
		name string
		code sdl.Keycode
		want rune
	}{
		{"lowercase letter", sdl.K_A, 'a'},
		{"digit", sdl.K_5, '5'},
		{"space", sdl.K_SPACE, ' '},
		{"punctuation", sdl.K_SLASH, '/'},
		{"shifted punctuation", sdl.K_QUESTION, '?'},
		{"latin-1", sdl.Keycode(0xe9), 'é'},
		{"unknown", sdl.K_UNKNOWN, 0},
		// Named keys are handled by the scancode table; letting them through
		// here would send Return or F1 a second time as a "character".
		{"named key", sdl.K_F1, 0},
		{"arrow", sdl.K_LEFT, 0},
		{"modifier", sdl.K_LCTRL, 0},
		{"extended key", sdl.K_LMETA, 0},
		{"control character", sdl.K_RETURN, 0},
		{"tab", sdl.K_TAB, 0},
		{"escape", sdl.K_ESCAPE, 0},
		{"backspace", sdl.K_BACKSPACE, 0},
		{"delete", sdl.K_DELETE, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runeFromKeycode(tt.code); got != tt.want {
				t.Fatalf("runeFromKeycode(%#x) = %q, want %q", uint32(tt.code), got, tt.want)
			}
		})
	}
}

// TestRuneFromKeycodeReachesKeysyms ties the two packages together: whatever
// rune the backend extracts has to survive keysym.FromRune, or the character
// is dropped on the way to the guest.
func TestRuneFromKeycodeReachesKeysyms(t *testing.T) {
	for code := sdl.Keycode(0x20); code <= 0xff; code++ {
		r := runeFromKeycode(code)
		if r == 0 {
			if code != 0x7f {
				t.Errorf("keycode %#x produced no rune", uint32(code))
			}
			continue
		}
		if sym := keysym.FromRune(r); sym == keysym.NoSymbol {
			t.Errorf("keycode %#x -> %q has no keysym", uint32(code), r)
		}
	}
}

func TestTranslateMods(t *testing.T) {
	tests := []struct {
		name string
		in   sdl.Keymod
		want keysym.Modifiers
	}{
		{"none", sdl.KMOD_NONE, keysym.ModNone},
		{"left shift", sdl.KMOD_LSHIFT, keysym.ModShift},
		{"right shift", sdl.KMOD_RSHIFT, keysym.ModShift},
		{"left ctrl", sdl.KMOD_LCTRL, keysym.ModControl},
		{"right ctrl", sdl.KMOD_RCTRL, keysym.ModControl},
		{"left alt", sdl.KMOD_LALT, keysym.ModAlt},
		{"right alt", sdl.KMOD_RALT, keysym.ModAlt},
		{"gui", sdl.KMOD_LGUI, keysym.ModSuper},
		{"altgr", sdl.KMOD_MODE, keysym.ModAltGr},
		{"caps", sdl.KMOD_CAPS, keysym.ModCapsLock},
		{"num", sdl.KMOD_NUM, keysym.ModNumLock},
		{"ctrl+alt", sdl.KMOD_LCTRL | sdl.KMOD_LALT, keysym.ModControl | keysym.ModAlt},
		{
			"everything",
			sdl.KMOD_LSHIFT | sdl.KMOD_RCTRL | sdl.KMOD_LALT | sdl.KMOD_RGUI | sdl.KMOD_MODE | sdl.KMOD_CAPS | sdl.KMOD_NUM,
			keysym.ModShift | keysym.ModControl | keysym.ModAlt | keysym.ModSuper | keysym.ModAltGr | keysym.ModCapsLock | keysym.ModNumLock,
		},
		// Scroll lock has no entry in keysym.Modifiers and must not smear
		// into a neighbouring bit.
		{"scroll lock is ignored", sdl.KMOD_SCROLL, keysym.ModNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := translateMods(tt.in); got != tt.want {
				t.Fatalf("translateMods(%#x) = %s, want %s", uint16(tt.in), got, tt.want)
			}
		})
	}
}

func TestWheelTicks(t *testing.T) {
	tests := []struct {
		name      string
		acc       float32
		delta     float32
		wantTicks int
		wantRest  float32
	}{
		{"one click up", 0, 1, 1, 0},
		{"one click down", 0, -1, -1, 0},
		{"three clicks", 0, 3, 3, 0},
		{"no movement", 0, 0, 0, 0},
		{"partial movement is held back", 0, 0.4, 0, 0.4},
		{"partial movement accumulates", 0.7, 0.4, 1, 0.1},
		{"negative partial", 0, -0.5, 0, -0.5},
		{"negative accumulates", -0.6, -0.6, -1, -0.2},
		{"reversal cancels", 0.8, -0.8, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ticks, rest := wheelTicks(tt.acc, tt.delta)
			if ticks != tt.wantTicks {
				t.Fatalf("wheelTicks(%v, %v) ticks = %d, want %d", tt.acc, tt.delta, ticks, tt.wantTicks)
			}
			if diff := rest - tt.wantRest; diff > 0.0001 || diff < -0.0001 {
				t.Fatalf("wheelTicks(%v, %v) rest = %v, want %v", tt.acc, tt.delta, rest, tt.wantRest)
			}
		})
	}
}

// TestWheelTicksNeverLosesMovement: a trackpad emits many small deltas, and
// the accumulator must eventually deliver every whole click the user scrolled.
func TestWheelTicksNeverLosesMovement(t *testing.T) {
	var acc float32
	total := 0
	for i := 0; i < 100; i++ {
		var ticks int
		ticks, acc = wheelTicks(acc, 0.1)
		total += ticks
	}
	if total != 10 {
		t.Fatalf("100 deltas of 0.1 produced %d clicks, want 10", total)
	}
}

func TestButtonBit(t *testing.T) {
	tests := []struct {
		index uint8
		want  Buttons
		ok    bool
	}{
		{uint8(sdl.BUTTON_LEFT), ButtonLeft, true},
		{uint8(sdl.BUTTON_MIDDLE), ButtonMiddle, true},
		{uint8(sdl.BUTTON_RIGHT), ButtonRight, true},
		// X1/X2 have no RFB button; mapping them onto a wheel bit (as some
		// clients do) makes the browser back button scroll the guest.
		{uint8(sdl.BUTTON_X1), 0, false},
		{uint8(sdl.BUTTON_X2), 0, false},
		{0, 0, false},
		{99, 0, false},
	}
	for _, tt := range tests {
		got, ok := buttonBit(tt.index)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("buttonBit(%d) = (%d, %t), want (%d, %t)", tt.index, got, ok, tt.want, tt.ok)
		}
	}
}

func TestButtonsFromState(t *testing.T) {
	left := sdl.MouseButtonFlags(1 << (sdl.BUTTON_LEFT - 1))
	middle := sdl.MouseButtonFlags(1 << (sdl.BUTTON_MIDDLE - 1))
	right := sdl.MouseButtonFlags(1 << (sdl.BUTTON_RIGHT - 1))
	x1 := sdl.MouseButtonFlags(1 << (sdl.BUTTON_X1 - 1))

	tests := []struct {
		name  string
		state sdl.MouseButtonFlags
		want  Buttons
	}{
		{"none", 0, 0},
		{"left", left, ButtonLeft},
		{"middle", middle, ButtonMiddle},
		{"right", right, ButtonRight},
		{"left+right", left | right, ButtonLeft | ButtonRight},
		{"all three", left | middle | right, ButtonLeft | ButtonMiddle | ButtonRight},
		{"extra buttons ignored", left | x1, ButtonLeft},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buttonsFromState(tt.state); got != tt.want {
				t.Fatalf("buttonsFromState(%#x) = %d, want %d", uint32(tt.state), got, tt.want)
			}
		})
	}
}

func TestSDLScaleMode(t *testing.T) {
	tests := []struct {
		q    ScaleQuality
		want sdl.ScaleMode
	}{
		{ScaleNearest, sdl.SCALEMODE_NEAREST},
		{ScaleLinear, sdl.SCALEMODE_LINEAR},
		{ScalePixelArt, sdl.SCALEMODE_PIXELART},
		{"", sdl.SCALEMODE_LINEAR},
		{"nonsense", sdl.SCALEMODE_LINEAR},
	}
	for _, tt := range tests {
		if got := sdlScaleMode(tt.q); got != tt.want {
			t.Fatalf("sdlScaleMode(%q) = %d, want %d", tt.q, got, tt.want)
		}
	}
}

func TestSDLBackendToSurface(t *testing.T) {
	// The common case: no display scaling, so window and drawable agree.
	b := &SDLBackend{winW: 800, winH: 600, outW: 800, outH: 600}
	if x, y := b.toSurface(100, 200); x != 100 || y != 200 {
		t.Fatalf("toSurface without scaling = (%d,%d), want (100,200)", x, y)
	}

	// A 2x high-density backbuffer: pointer positions arrive in window points
	// and must be converted, or every click lands at half the intended
	// position.
	b = &SDLBackend{winW: 800, winH: 600, outW: 1600, outH: 1200}
	if x, y := b.toSurface(100, 200); x != 200 || y != 400 {
		t.Fatalf("toSurface with 2x density = (%d,%d), want (200,400)", x, y)
	}

	// Before the window exists there is nothing to scale by; the conversion
	// must not divide by zero.
	b = &SDLBackend{}
	if x, y := b.toSurface(10, 20); x != 10 || y != 20 {
		t.Fatalf("toSurface with no window = (%d,%d), want (10,20)", x, y)
	}
}

// TestSDLBackendImplementsBackend is redundant with the compile-time assertion
// in sdl.go, but it fails with a clearer message when someone changes the
// interface.
func TestSDLBackendImplementsBackend(t *testing.T) {
	concrete := NewSDLBackend()
	if concrete == nil {
		t.Fatal("NewSDLBackend returned nil")
	}
	// The interface variable is the point of the test: it will not compile if
	// SDLBackend stops satisfying Backend. Comparing it to nil would be
	// meaningless, since an interface holding a typed nil is itself non-nil.
	var b Backend = concrete
	if b.Fullscreen() {
		t.Fatal("an unopened backend should not report fullscreen")
	}
	if w, h := b.Size(); w != 0 || h != 0 {
		t.Fatalf("an unopened backend reported size %dx%d", w, h)
	}
	// Closing without opening must not panic or try to unload a library that
	// was never loaded.
	b.Close()
	b.Close()
}
