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

func TestDebugCardIsDesktopChromeAvailableWhileBroken(t *testing.T) {
	// The debug readout must be there while connecting, reconnecting and
	// failed too — that is when connection detail is most useful — and for
	// observers, since it never touches the guest.
	for _, state := range []State{Connecting, Connected, Reconnecting, Busy, Failed} {
		for _, role := range []Role{Exclusive, Controller, Observer} {
			s := Snapshot{Surface: Desktop, State: state, Role: role}
			if v := s.Availability(Debug); !v.Visible || !v.Enabled {
				t.Fatalf("desktop debug %s/%s: %+v", state, role, v)
			}
		}
	}
	for _, surface := range []Surface{Console, Serial, SSH, Web} {
		s := Snapshot{Surface: surface, State: Connected}
		if s.Availability(Debug).Visible {
			t.Fatalf("%s exposes the desktop debug card", surface)
		}
	}
	if s := (Snapshot{}); s.Availability(Debug).Visible {
		t.Fatal("zero snapshot exposes debug")
	}
}

func TestFormatBytesScalesOnce(t *testing.T) {
	for in, want := range map[uint64]string{
		0:          "0 B",
		512:        "512 B",
		1023:       "1023 B",
		1024:       "1.0 KB",
		1536:       "1.5 KB",
		1048576:    "1.0 MB",
		1289748480: "1.2 GB",
		343064:     "335.0 KB",
		3430640000: "3.2 GB",
	} {
		if got := FormatBytes(in); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestSnapshotEqualSeesExtraRows(t *testing.T) {
	base := Snapshot{Surface: Desktop, State: Connected, Transport: "RFB"}
	if !base.Equal(base) {
		t.Fatal("snapshot differs from itself")
	}
	changed := base
	changed.BytesIn = 1
	if base.Equal(changed) {
		t.Fatal("byte drift ignored")
	}
	withExtra := base
	withExtra.Extra = [][2]string{{"Framebuffer", "1280×800"}}
	if base.Equal(withExtra) {
		t.Fatal("added detail rows ignored")
	}
	renamed := base
	renamed.Extra = [][2]string{{"Framebuffer", "800×600"}}
	if withExtra.Equal(renamed) {
		t.Fatal("changed detail value ignored")
	}
	if !withExtra.Equal(Snapshot{Surface: Desktop, State: Connected, Transport: "RFB", Extra: [][2]string{{"Framebuffer", "1280×800"}}}) {
		t.Fatal("identical detail rows compare unequal")
	}
}
