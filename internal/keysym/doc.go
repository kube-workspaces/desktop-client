// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package keysym translates keyboard input into X11 keysyms for the RFB
// (VNC) protocol.
//
// RFB's KeyEvent message (internal/rfb, Conn.KeyEvent) carries an X11 keysym
// as a uint32 together with a down flag. Everything the viewer knows about the
// keyboard — a rune the user typed, an arrow key, a modifier, a chord from a
// menu — has to become one of those numbers before it can reach the guest.
// This package owns that translation and nothing else.
//
// # No windowing dependency
//
// The session viewer receives input from SDL3, but this package deliberately
// does not import any SDL binding. Instead it defines a backend-neutral [Key]
// enumeration that the viewer maps SDL scancodes and keycodes onto. That keeps
// the translation tables pure Go, unit-testable without a display, and leaves
// the SDL binding swappable. The same reasoning keeps internal/rfb and
// internal/kwclient cgo-free.
//
// # The three layers
//
//   - [FromRune] converts text the user typed into a keysym. Use it whenever
//     the platform reports a character rather than a key.
//   - [Key] names the non-text keys (function keys, arrows, modifiers, keypad,
//     editing keys) and [Key.Keysym] gives each one its keysym. [ParseKey] and
//     [Key.String] provide stable lowercase names such as "f1", "kp_enter" and
//     "control_l" so key bindings can live in configuration files.
//   - [Chord] expresses combinations such as Ctrl-Alt-Del that a VDI client
//     must be able to inject, and [Chord.Sequence] expands one into the exact
//     ordered press/release stream the protocol requires.
//
// # Modifier hygiene
//
// [Tracker] records which modifier keysyms the client has told the guest are
// held. The guest has no other way to know: RFB modifier state is implicit in
// the KeyEvent stream, so a missed release leaves the modifier stuck down
// forever. [Tracker.ReleaseAll] produces the events needed to clear that state
// and must be sent on focus loss.
package keysym
