// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package keysym

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrInvalidChord is returned by [ParseChord] for any unparseable input.
// Errors wrap it, so callers can test with errors.Is while still showing the
// detailed message to the user.
var ErrInvalidChord = errors.New("keysym: invalid chord")

// KeyAction is a single press or release of one keysym: exactly the payload of
// one RFB KeyEvent message.
type KeyAction struct {
	// Sym is the X11 keysym to send.
	Sym Keysym
	// Down is true for a press and false for a release.
	Down bool
}

// String renders the action for logs and test failures.
func (a KeyAction) String() string {
	if a.Down {
		return a.Sym.String() + "↓"
	}
	return a.Sym.String() + "↑"
}

// Chord is a key combination such as Ctrl-Alt-Del: zero or more modifiers held
// while one key is struck.
//
// A VDI client needs chords as first-class values because the host window
// manager swallows most interesting combinations before the client ever sees
// them. Ctrl-Alt-Del, Alt-Tab and Super are all intercepted by the host, so the
// only way to deliver them to the guest is for the user to pick them from a
// menu and for the client to synthesise the event stream itself.
type Chord struct {
	// Mods are the modifier keys held down, in the order they are pressed.
	Mods []Key
	// Key is the key struck while the modifiers are held.
	Key Key
}

// Common chords. These are the combinations a VDI client is expected to offer
// in its "send keys" menu, because the host intercepts them.
var (
	// ChordCtrlAltDel is the secure attention sequence. On Windows guests it
	// is the only way to reach the lock screen or Task Manager, which makes it
	// the single most important chord a remote console can send.
	ChordCtrlAltDel = Chord{Mods: []Key{KeyControlL, KeyAltL}, Key: KeyDelete}

	// ChordCtrlAltBackspace historically killed the X server; on modern
	// systems it is usually disabled, but guests may still bind it.
	ChordCtrlAltBackspace = Chord{Mods: []Key{KeyControlL, KeyAltL}, Key: KeyBackSpace}

	// ChordAltTab switches windows inside the guest rather than on the host.
	ChordAltTab = Chord{Mods: []Key{KeyAltL}, Key: KeyTab}

	// ChordAltF4 closes the focused guest window.
	ChordAltF4 = Chord{Mods: []Key{KeyAltL}, Key: KeyF4}

	// ChordSuper opens the guest's start menu or activities overview. It has
	// no modifiers: the Super key alone is the chord.
	ChordSuper = Chord{Key: KeySuperL}

	// ChordCtrlEscape is the fallback start-menu combination for guests where
	// Super is unavailable.
	ChordCtrlEscape = Chord{Mods: []Key{KeyControlL}, Key: KeyEscape}

	// ChordCtrlAltF1 through ChordCtrlAltF12 switch virtual terminals on Linux
	// guests; build the rest with [CtrlAltF].
	ChordCtrlAltF1 = Chord{Mods: []Key{KeyControlL, KeyAltL}, Key: KeyF1}
	ChordCtrlAltF2 = Chord{Mods: []Key{KeyControlL, KeyAltL}, Key: KeyF2}
)

// commonChords is the menu-ready set, keyed by canonical chord name.
var commonChords = map[string]Chord{
	"ctrl-alt-del":       ChordCtrlAltDel,
	"ctrl-alt-backspace": ChordCtrlAltBackspace,
	"alt-tab":            ChordAltTab,
	"alt-f4":             ChordAltF4,
	"super":              ChordSuper,
	"ctrl-escape":        ChordCtrlEscape,
}

// CommonChords returns the named chords a client should offer in its send-keys
// menu, plus ctrl-alt-f1 through ctrl-alt-f12. It returns a fresh map on each
// call so callers cannot mutate the package's state.
func CommonChords() map[string]Chord {
	m := make(map[string]Chord, len(commonChords)+12)
	for name, c := range commonChords {
		m[name] = c.clone()
	}
	for n := 1; n <= 12; n++ {
		c, ok := CtrlAltF(n)
		if !ok {
			continue
		}
		m[fmt.Sprintf("ctrl-alt-f%d", n)] = c
	}
	return m
}

// CommonChordNames returns the names in [CommonChords], sorted.
func CommonChordNames() []string {
	m := CommonChords()
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// CtrlAltF returns the Ctrl-Alt-Fn chord that switches to Linux virtual
// terminal n, for n in 1..24, and reports whether n was in range.
func CtrlAltF(n int) (Chord, bool) {
	k, ok := FunctionKey(n)
	if !ok {
		return Chord{}, false
	}
	return Chord{Mods: []Key{KeyControlL, KeyAltL}, Key: k}, true
}

// ParseChord parses a chord written as modifiers and a key joined by "-", for
// example "ctrl-alt-del", "ctrl-alt-f2", "alt-tab", "shift-insert" or plain
// "super". Parsing is case-insensitive and accepts every alias [ParseKey]
// does.
//
// The last element is the key; everything before it must name a modifier. A
// bare key name with no modifiers is a valid chord.
//
// Only named keys are accepted, because [Chord.Key] is a [Key] and [Key]
// covers only non-text keys. Chords over characters (Ctrl-C and friends) are
// not expressible here; build them directly with [FromRune] for the key and
// the modifier keys for the mods.
func ParseChord(s string) (Chord, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return Chord{}, fmt.Errorf("%w: empty", ErrInvalidChord)
	}

	parts := strings.Split(strings.ToLower(trimmed), "-")
	for i, p := range parts {
		if strings.TrimSpace(p) == "" {
			return Chord{}, fmt.Errorf("%w: %q has an empty element at position %d", ErrInvalidChord, s, i+1)
		}
		parts[i] = strings.TrimSpace(p)
	}

	keyName := parts[len(parts)-1]
	key, ok := ParseKey(keyName)
	if !ok {
		return Chord{}, fmt.Errorf("%w: unknown key %q in %q", ErrInvalidChord, keyName, s)
	}
	if key == KeyUnknown {
		return Chord{}, fmt.Errorf("%w: %q is not a sendable key", ErrInvalidChord, keyName)
	}

	modNames := parts[:len(parts)-1]
	c := Chord{Key: key}
	if len(modNames) > 0 {
		c.Mods = make([]Key, 0, len(modNames))
	}
	seen := make(map[Key]bool, len(modNames))
	for _, name := range modNames {
		mod, ok := ParseKey(name)
		if !ok {
			return Chord{}, fmt.Errorf("%w: unknown modifier %q in %q", ErrInvalidChord, name, s)
		}
		if !mod.IsModifier() {
			return Chord{}, fmt.Errorf("%w: %q is not a modifier in %q", ErrInvalidChord, name, s)
		}
		if seen[mod] {
			return Chord{}, fmt.Errorf("%w: modifier %q repeated in %q", ErrInvalidChord, name, s)
		}
		seen[mod] = true
		c.Mods = append(c.Mods, mod)
	}
	return c, nil
}

// MustParseChord is [ParseChord] for package-level variables and tests, where
// a malformed literal is a programming error. It panics on failure.
func MustParseChord(s string) Chord {
	c, err := ParseChord(s)
	if err != nil {
		panic(err)
	}
	return c
}

// Sequence returns the ordered press and release events that deliver the chord
// to the guest:
//
//	modifiers down, in order
//	key down
//	key up
//	modifiers up, in REVERSE order
//
// The order is not cosmetic. RFB has no notion of modifier state — the guest
// reconstructs it from the stream of KeyEvents — so the key press only counts
// as modified if it arrives strictly between the modifier press and release.
// Releasing in reverse order unwinds the modifiers as a stack, which is what a
// real keyboard produces and what guest input layers are written to expect;
// releasing them in forward order can momentarily leave a lone Alt held, and
// on Windows guests a lone Alt press-and-release activates the menu bar.
//
// Keys with no keysym are skipped, so an invalid chord yields a short or empty
// sequence rather than sending [NoSymbol] to the guest.
func (c Chord) Sequence() []KeyAction {
	actions := make([]KeyAction, 0, 2*len(c.Mods)+2)

	for _, m := range c.Mods {
		if sym := m.Keysym(); sym != NoSymbol {
			actions = append(actions, KeyAction{Sym: sym, Down: true})
		}
	}
	if sym := c.Key.Keysym(); sym != NoSymbol {
		actions = append(actions,
			KeyAction{Sym: sym, Down: true},
			KeyAction{Sym: sym, Down: false},
		)
	}
	for i := len(c.Mods) - 1; i >= 0; i-- {
		if sym := c.Mods[i].Keysym(); sym != NoSymbol {
			actions = append(actions, KeyAction{Sym: sym, Down: false})
		}
	}
	return actions
}

// String renders the chord in the form [ParseChord] accepts, using canonical
// key names: ChordCtrlAltDel becomes "control_l-alt_l-delete".
func (c Chord) String() string {
	parts := make([]string, 0, len(c.Mods)+1)
	for _, m := range c.Mods {
		parts = append(parts, m.String())
	}
	parts = append(parts, c.Key.String())
	return strings.Join(parts, "-")
}

// clone returns a copy with its own Mods slice, so that callers handed a chord
// from this package cannot mutate the package's copy.
func (c Chord) clone() Chord {
	if c.Mods == nil {
		return c
	}
	mods := make([]Key, len(c.Mods))
	copy(mods, c.Mods)
	return Chord{Mods: mods, Key: c.Key}
}
