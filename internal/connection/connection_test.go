// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package connection

import "testing"

func TestObserverCannotMutateGuestButCanNavigateAndRequestControl(t *testing.T) {
	s := Snapshot{Surface: Desktop, State: Connected, Role: Observer,
		Capabilities: Capabilities{Shell: true, SpecialKeys: true, ClipboardSync: true, TypeClipboard: true, GuestResize: true, SharedControl: true, Audio: true}}
	for _, a := range []Action{SpecialKeys, ClipboardSync, TypeClipboard, GuestResize} {
		v := s.Availability(a)
		if !v.Visible || v.Enabled || v.Reason != "viewOnly" {
			t.Fatalf("observer %s: %+v", a, v)
		}
	}
	for _, a := range []Action{Fullscreen, Sessions, WorkspaceList, Disconnect, FitDesktop, SharedControl, Audio} {
		if !s.Availability(a).Enabled {
			t.Fatalf("observer cannot use local/control action %s", a)
		}
	}
	s.Role = Controller
	if !s.Availability(GuestResize).Enabled {
		t.Fatal("promotion did not restore guest resize")
	}
}

func TestUnavailableConnectionStillHasEscapeActions(t *testing.T) {
	for _, state := range []State{Connecting, Reconnecting, Busy, Failed} {
		s := Snapshot{Surface: Desktop, State: state, Capabilities: Capabilities{Shell: true, SpecialKeys: true}}
		if got := s.Availability(SpecialKeys); !got.Visible || got.Enabled || got.Reason != string(state) {
			t.Fatalf("%s special keys: %+v", state, got)
		}
		for _, a := range []Action{Fullscreen, Sessions, WorkspaceList, Disconnect} {
			if !s.Availability(a).Enabled {
				t.Fatalf("%s cannot escape via %s", state, a)
			}
		}
	}
}

func TestToolsFollowSurfaceAndLiveCapabilities(t *testing.T) {
	caps := Capabilities{SpecialKeys: true, ClipboardSync: true, GuestResize: true, Audio: true, Paste: true}
	for _, surface := range []Surface{Console, Serial, SSH, Web} {
		s := Snapshot{Surface: surface, State: Connected, Capabilities: caps}
		for _, a := range []Action{SpecialKeys, ClipboardSync, GuestResize, Audio} {
			if s.Availability(a).Visible {
				t.Fatalf("%s exposes desktop tool %s", surface, a)
			}
		}
		if s.Availability(Paste).Visible != (surface != Web) {
			t.Fatalf("%s paste availability wrong", surface)
		}
	}
	s := Snapshot{Surface: Desktop, State: Connected, Capabilities: caps}
	s.Capabilities.Audio = false // A recovered/fallback adapter lost audio.
	if s.Availability(Audio).Visible {
		t.Fatal("stale audio action after capability loss")
	}
	if s.Availability(Sessions).Visible || s.Availability(Action("unknown")).Visible {
		t.Fatal("standalone/unknown action unexpectedly offered")
	}
}
