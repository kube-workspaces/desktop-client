// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
)

func TestViewerDisconnectIsDistinctFromWindowClose(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		v := New(&fakeBackend{}, Config{})
		var err error
		if explicit {
			err = v.handleKey(EventKey{Rune: 'q', Down: true, Mods: keysym.ModControl | keysym.ModAlt})
		} else {
			err = v.handleEvent(time.Now(), EventWindowClose{})
		}
		if err != nil {
			t.Fatal(err)
		}
		if !v.quit || (v.CloseDisposition() == connection.Release) != explicit {
			t.Fatalf("explicit=%v: quit=%v disposition=%v", explicit, v.quit, v.CloseDisposition())
		}
	}
}

func TestTier1DisconnectIsDistinctFromWindowClose(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		w := &tier1Window{opts: Tier1Config{QuitRune: 'q'}}
		var ev Event = EventWindowClose{}
		if explicit {
			ev = EventKey{Rune: 'q', Down: true, Mods: keysym.ModControl | keysym.ModAlt}
		}
		if err := w.handleEvent(time.Now(), ev); err != nil {
			t.Fatal(err)
		}
		d := &Tier1Detached{w: w}
		if !w.quit || (d.CloseDisposition() == connection.Release) != explicit {
			t.Fatalf("explicit=%v: quit=%v disposition=%v", explicit, w.quit, d.CloseDisposition())
		}
	}
}
