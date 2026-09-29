// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package tray

import (
	"fmt"
	"strings"
	"testing"
)

func TestModesFor(t *testing.T) {
	cases := map[Kind][]Mode{
		KindVM:        {ModeDisplay, ModeSerial, ModeSSH},
		KindContainer: {ModeWeb, ModeTerminal, ModeBrowser},
		KindScratch:   {ModeWeb, ModeTerminal, ModeBrowser},
		KindOther:     {ModeBrowser},
		Kind(99):      {ModeBrowser},
	}
	for kind, want := range cases {
		got := ModesFor(kind)
		if len(got) != len(want) {
			t.Fatalf("ModesFor(%v) = %v, want %v", kind, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("ModesFor(%v) = %v, want %v", kind, got, want)
			}
		}
	}
}

func TestMenuItemsEmpty(t *testing.T) {
	items := MenuItems(nil)
	if len(items) != 1 || !items[0].Disabled || items[0].Key != "" {
		t.Fatalf("empty set should give one disabled placeholder, got %+v", items)
	}
}

func TestMenuItemsSorted(t *testing.T) {
	targets := []Target{
		{Key: "b/vm-1", Label: "vm-1 (b)"},
		{Key: "a/web-0", Label: "web-0 (a)"},
		{Key: "a/vm-0", Label: "vm-0 (a)"},
	}
	items := MenuItems(targets)
	if len(items) != 3 {
		t.Fatalf("got %+v", items)
	}
	for i, want := range []string{"a/vm-0", "a/web-0", "b/vm-1"} {
		if items[i].Key != want || items[i].Disabled {
			t.Fatalf("item %d = %+v, want key %q enabled", i, items[i], want)
		}
	}
}

func TestMenuItemsCapped(t *testing.T) {
	var targets []Target
	for i := 0; i < MaxWorkspaces+7; i++ {
		key := fmt.Sprintf("ns/ws-%03d", i)
		targets = append(targets, Target{Key: key, Label: key})
	}
	items := MenuItems(targets)
	if len(items) != MaxWorkspaces+1 {
		t.Fatalf("got %d items, want %d + overflow", len(items), MaxWorkspaces)
	}
	last := items[len(items)-1]
	if !last.Disabled || last.Key != "" || !strings.Contains(last.Label, "more") {
		t.Fatalf("last item should be a disabled overflow row, got %+v", last)
	}
	for _, it := range items[:MaxWorkspaces] {
		if it.Disabled || it.Key == "" {
			t.Fatalf("workspace row should be enabled with a key, got %+v", it)
		}
	}
}

func TestMenuItemsExactlyAtCap(t *testing.T) {
	var targets []Target
	for i := 0; i < MaxWorkspaces; i++ {
		key := fmt.Sprintf("ns/ws-%03d", i)
		targets = append(targets, Target{Key: key, Label: key})
	}
	if items := MenuItems(targets); len(items) != MaxWorkspaces {
		t.Fatalf("at exactly the cap there must be no overflow row, got %d items", len(items))
	}
}
