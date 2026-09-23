// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"runtime"
	"testing"
	"unsafe"

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

func TestTranslateText(t *testing.T) {
	tests := []struct {
		name string
		in   *sdl.TextInputEvent
		want string
		ok   bool
	}{
		{"a character", &sdl.TextInputEvent{Text: "a"}, "a", true},
		// The keystroke from the bug report: shift and the ";" key.
		{"a shifted symbol", &sdl.TextInputEvent{Text: ":"}, ":", true},
		// An IME commits a whole word, and some platforms deliver a paste
		// this way. Taking the first rune would silently drop the rest.
		{"an IME commit", &sdl.TextInputEvent{Text: "日本語"}, "日本語", true},
		{"a pasted line", &sdl.TextInputEvent{Text: "https://kw.example.com"}, "https://kw.example.com", true},
		// A composition that ended without producing anything. Passing it on
		// would look to a widget like the user typing nothing.
		{"an empty commit", &sdl.TextInputEvent{Text: ""}, "", false},
		{"no event at all", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := translateText(tt.in)
			if ok != tt.ok {
				t.Fatalf("translateText ok = %t, want %t", ok, tt.ok)
			}
			if got.Text != tt.want {
				t.Fatalf("translateText text = %q, want %q", got.Text, tt.want)
			}
		})
	}
}

// textInputEventBytes mirrors SDL_TextInputEvent, padded to the size of an
// SDL_Event so that a test can put one on a backend's event slot.
//
// The pointer is a real *byte field rather than a word in the padding, so the
// string it points at stays reachable while the binding decodes it; the
// binding reads exactly these fields out of the union.
type textInputEventBytes struct {
	Type      sdl.EventType
	Reserved  uint32
	Timestamp uint64
	WindowID  sdl.WindowID
	Text      *byte
	_         [96]byte
}

// TestTranslateDispatchesTextInput drives the real event switch, not just the
// helper under it, so that a text event that SDL delivers cannot be dropped by
// a missing case.
func TestTranslateDispatchesTextInput(t *testing.T) {
	if unsafe.Sizeof(textInputEventBytes{}) != unsafe.Sizeof(sdl.Event{}) {
		t.Fatalf("the stand-in event is %d bytes and sdl.Event is %d",
			unsafe.Sizeof(textInputEventBytes{}), unsafe.Sizeof(sdl.Event{}))
	}

	text := append([]byte(":"), 0)
	raw := textInputEventBytes{Type: sdl.EVENT_TEXT_INPUT, Text: &text[0]}

	b := NewSDLBackend()
	b.event = *(*sdl.Event)(unsafe.Pointer(&raw))
	got := b.translate(nil)

	if len(got) != 1 {
		t.Fatalf("translate produced %d events, want 1", len(got))
	}
	ev, ok := got[0].(EventText)
	if !ok {
		t.Fatalf("translate produced %T, want EventText", got[0])
	}
	if ev.Text != ":" {
		t.Fatalf("translate produced %q, want %q", ev.Text, ":")
	}
	runtime.KeepAlive(raw)
	runtime.KeepAlive(text)
}

// TestTranslateDropsPreEditText: the half-composed text an IME is still
// showing is not something the user has typed, and forwarding it would insert
// a candidate they have not chosen.
func TestTranslateDropsPreEditText(t *testing.T) {
	for _, typ := range []sdl.EventType{sdl.EVENT_TEXT_EDITING, sdl.EVENT_TEXT_EDITING_CANDIDATES} {
		b := NewSDLBackend()
		b.event = sdl.Event{Type: typ}
		if got := b.translate(nil); len(got) != 0 {
			t.Fatalf("event type %d produced %d events, want none", typ, len(got))
		}
	}
}

// usLayout is enough of a US keyboard layout to type a server URL, keyed by
// physical key and giving the unshifted and shifted characters.
var usLayout = map[sdl.Scancode][2]sdl.Keycode{
	sdl.SCANCODE_SEMICOLON: {sdl.K_SEMICOLON, sdl.K_COLON},
	sdl.SCANCODE_SLASH:     {sdl.K_SLASH, sdl.K_QUESTION},
	sdl.SCANCODE_2:         {sdl.K_2, sdl.K_AT},
	sdl.SCANCODE_1:         {sdl.K_1, sdl.K_EXCLAIM},
	sdl.SCANCODE_A:         {sdl.K_A, sdl.Keycode('A')},
}

// layoutQuery records one call into the stand-in keyboard layout.
type layoutQuery struct {
	mods     sdl.Keymod
	keyEvent bool
}

// stubLayout replaces the live keymap lookup with usLayout, so the guest-facing
// half of the translation can be tested without a keyboard or a loaded SDL.
//
// The stand-in reproduces the behaviour of SDL_GetKeyFromScancode, including
// the part that caused the bug: asked for a key-event keycode it throws the
// modifier state away. That is not decoration — it is what makes these tests
// fail if the production code ever asks for one again.
func stubLayout(t *testing.T) *[]layoutQuery {
	t.Helper()
	asked := new([]layoutQuery)
	previous := keycodeFromScancode
	t.Cleanup(func() { keycodeFromScancode = previous })
	keycodeFromScancode = func(sc sdl.Scancode, mods sdl.Keymod, keyEvent bool) sdl.Keycode {
		*asked = append(*asked, layoutQuery{mods: mods, keyEvent: keyEvent})
		if keyEvent {
			// SDL_keyboard.c: "We won't be applying any modifiers by default".
			mods = sdl.KMOD_NONE
		}
		pair, ok := usLayout[sc]
		if !ok {
			return sdl.K_UNKNOWN
		}
		if mods&sdl.KMOD_SHIFT != 0 {
			return pair[1]
		}
		return pair[0]
	}
	return asked
}

// TestTranslateKeyResolvesTheShiftedCharacter is the guest-side half of the
// reported bug.
//
// SDL3 builds the keycode in a key event with the modifier state deliberately
// thrown away ("We won't be applying any modifiers by default", SDL_keyboard.c),
// and asking SDL_GetKeyFromScancode for a key-event keycode does the same. So
// the ";" key reports ';' whether or not shift is held, and a viewer that
// believes it tells the guest the user pressed semicolon.
func TestTranslateKeyResolvesTheShiftedCharacter(t *testing.T) {
	asked := stubLayout(t)
	b := NewSDLBackend()

	// The event is exactly what SDL3 delivers for shift and the ";" key: the
	// modifier is in Mod, and Key is still the unshifted value.
	down, ok := b.translateKey(&sdl.KeyboardEvent{
		Type:     sdl.EVENT_KEY_DOWN,
		Scancode: sdl.SCANCODE_SEMICOLON,
		Key:      sdl.K_SEMICOLON,
		Mod:      sdl.KMOD_LSHIFT,
		Down:     true,
	})
	if !ok {
		t.Fatal("shift and the ; key produced no event at all")
	}
	if down.Rune != ':' {
		t.Fatalf("shift and the ; key produced %q, want %q", down.Rune, ':')
	}
	if down.Rune == ';' {
		t.Fatal("the unshifted keycode was used; this is the reported bug")
	}
	if !down.Mods.Has(keysym.ModShift) {
		t.Fatalf("the modifier state was lost: %s", down.Mods)
	}
	if len(*asked) != 1 {
		t.Fatalf("the layout was queried %d times, want once", len(*asked))
	}
	if q := (*asked)[0]; q.mods != sdl.KMOD_LSHIFT || q.keyEvent {
		t.Fatalf("the layout was asked with mods=%#x keyEvent=%t; it must be asked with the "+
			"modifiers from the event and keyEvent false, or SDL discards them",
			uint16(q.mods), q.keyEvent)
	}

	// The release must report the same character, or the guest is left
	// holding a colon it never sees released.
	up, ok := b.translateKey(&sdl.KeyboardEvent{
		Type:     sdl.EVENT_KEY_UP,
		Scancode: sdl.SCANCODE_SEMICOLON,
		Key:      sdl.K_SEMICOLON,
		Mod:      sdl.KMOD_NONE,
		Down:     false,
	})
	if !ok || up.Rune != ':' || up.Down {
		t.Fatalf("the release reported %q down=%t, want ':' up", up.Rune, up.Down)
	}
}

// TestTranslateKeyResolvesShiftedPunctuation is the rest of the row: every
// shifted symbol was wrong, not only the colon.
func TestTranslateKeyResolvesShiftedPunctuation(t *testing.T) {
	stubLayout(t)
	tests := []struct {
		scancode sdl.Scancode
		mod      sdl.Keymod
		want     rune
	}{
		{sdl.SCANCODE_SEMICOLON, sdl.KMOD_NONE, ';'},
		{sdl.SCANCODE_SEMICOLON, sdl.KMOD_RSHIFT, ':'},
		{sdl.SCANCODE_SLASH, sdl.KMOD_NONE, '/'},
		{sdl.SCANCODE_SLASH, sdl.KMOD_LSHIFT, '?'},
		{sdl.SCANCODE_2, sdl.KMOD_NONE, '2'},
		{sdl.SCANCODE_2, sdl.KMOD_LSHIFT, '@'},
		{sdl.SCANCODE_1, sdl.KMOD_LSHIFT, '!'},
		{sdl.SCANCODE_A, sdl.KMOD_NONE, 'a'},
		{sdl.SCANCODE_A, sdl.KMOD_LSHIFT, 'A'},
	}
	for _, tt := range tests {
		b := NewSDLBackend()
		got, ok := b.translateKey(&sdl.KeyboardEvent{
			Type:     sdl.EVENT_KEY_DOWN,
			Scancode: tt.scancode,
			Key:      sdl.K_UNKNOWN,
			Mod:      tt.mod,
			Down:     true,
		})
		if !ok || got.Rune != tt.want {
			t.Errorf("scancode %d with mod %#x produced %q (ok=%t), want %q",
				tt.scancode, uint16(tt.mod), got.Rune, ok, tt.want)
		}
	}
}

// TestTranslateKeyPrefersTheScancodeTable: only character keys go through the
// layout. A named key must not be resolved into a character, or Return would
// reach the guest twice.
func TestTranslateKeyPrefersTheScancodeTable(t *testing.T) {
	asked := stubLayout(t)
	b := NewSDLBackend()

	got, ok := b.translateKey(&sdl.KeyboardEvent{
		Type:     sdl.EVENT_KEY_DOWN,
		Scancode: sdl.SCANCODE_RETURN,
		Key:      sdl.K_RETURN,
		Down:     true,
	})
	if !ok || got.Key != keysym.KeyReturn || got.Rune != 0 {
		t.Fatalf("Return translated to key=%s rune=%q", got.Key, got.Rune)
	}
	if len(*asked) != 0 {
		t.Fatal("a named key was put through the keyboard layout")
	}

	// A key the layout knows nothing about produces nothing rather than a
	// zero rune the guest would be asked to send.
	if _, ok := b.translateKey(&sdl.KeyboardEvent{
		Type:     sdl.EVENT_KEY_DOWN,
		Scancode: sdl.SCANCODE_INTERNATIONAL1,
		Key:      sdl.K_UNKNOWN,
		Down:     true,
	}); ok {
		t.Fatal("an unmapped key produced an event")
	}

	if _, ok := b.translateKey(nil); ok {
		t.Fatal("a nil event produced an event")
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

func TestSDLBackendToSurface(t *testing.T) { // The common case: no display scaling, so window and drawable agree.
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

func TestSDLBackendScaleFactor(t *testing.T) {
	// Unscaled, fractional, and Retina densities read straight off the
	// window/drawable ratio the metrics refresh maintains.
	for _, tc := range []struct {
		winW, outW int
		want       float64
	}{
		{800, 800, 1},
		{800, 1200, 1.5},
		{800, 1600, 2},
	} {
		b := &SDLBackend{winW: tc.winW, winH: 600, outW: tc.outW, outH: 1200}
		if got := b.ScaleFactor(); got != tc.want {
			t.Errorf("ScaleFactor(%d/%d) = %v, want %v", tc.outW, tc.winW, got, tc.want)
		}
	}
	// Before the window exists there is no ratio; unscaled, not NaN.
	if got := (&SDLBackend{}).ScaleFactor(); got != 1 {
		t.Fatalf("ScaleFactor with no window = %v, want 1", got)
	}
}

func TestCenterInBounds(t *testing.T) {
	tests := []struct {
		name         string
		bounds       sdl.Rect
		w, h         int32
		wantX, wantY int32
	}{
		{
			name:   "window smaller than the display",
			bounds: sdl.Rect{X: 0, Y: 0, W: 1920, H: 1080},
			w:      1280, h: 800,
			wantX: 320, wantY: 140,
		},
		{
			// A secondary display left of the primary has negative screen
			// coordinates; the offset must stay inside its own bounds.
			name:   "secondary display left of the primary",
			bounds: sdl.Rect{X: -1920, Y: 0, W: 1920, H: 1080},
			w:      1024, h: 768,
			wantX: -1472, wantY: 156,
		},
		{
			name:   "window the size of the display keeps its origin",
			bounds: sdl.Rect{X: 100, Y: 200, W: 3840, H: 2160},
			w:      3840, h: 2160,
			wantX: 100, wantY: 200,
		},
		{
			// Integer halves: a one-pixel sliver goes to the top-left, never
			// the bottom-right.
			name:   "odd extra pixel rounds up and left",
			bounds: sdl.Rect{X: 0, Y: 0, W: 1921, H: 1081},
			w:      1280, h: 800,
			wantX: 320, wantY: 140,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x, y := centerInBounds(tt.bounds, tt.w, tt.h)
			if x != tt.wantX || y != tt.wantY {
				t.Fatalf("centerInBounds(%+v, %dx%d) = (%d,%d), want (%d,%d)",
					tt.bounds, tt.w, tt.h, x, y, tt.wantX, tt.wantY)
			}
		})
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
