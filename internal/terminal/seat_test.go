// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package terminal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// seatRig is a window driven directly (no goroutine): the test owns the
// loop, so every transition is deterministic. check and take script the
// seat; takeCalls counts takeovers.
type seatRig struct {
	w         *window
	be        *fakeBackend
	ds        *dialServer
	takeCalls int
	seatErr   error
	takeErr   error
	inUse     bool
}

func newSeatRig(t *testing.T, inUse bool) *seatRig {
	t.Helper()
	be := newFakeBackend()
	ds := &dialServer{}
	r := &seatRig{be: be, ds: ds, inUse: inUse}
	w, err := buildWindow(ds.dial, Options{
		Backend: be,
		CheckSeat: func(ctx context.Context) (bool, error) {
			if r.seatErr != nil {
				return false, r.seatErr
			}
			return r.inUse, nil
		},
		TakeSeat: func(ctx context.Context) error {
			r.takeCalls++
			return r.takeErr
		},
	})
	if err != nil {
		t.Fatalf("buildWindow: %v", err)
	}
	r.w = w
	if err := w.openWindow(context.Background()); err != nil {
		t.Fatalf("openWindow: %v", err)
	}
	t.Cleanup(w.closeWindow)
	return r
}

func (r *seatRig) step() { r.w.stepExternal(time.Now(), nil) }

func (r *seatRig) pressEnter() {
	r.w.handleEvent(viewer.EventKey{Key: keysym.KeyReturn, Down: true})
	r.w.handleEvent(viewer.EventKey{Key: keysym.KeyReturn, Down: false})
}

// TestSeatBusyParksWithoutDialling: a held seat shows the busy plate and
// never dials behind the holder's back.
func TestSeatBusyParksWithoutDialling(t *testing.T) {
	r := newSeatRig(t, true)
	r.step()

	if r.ds.callCount() != 0 {
		t.Fatalf("dials = %d, want 0 (seat held, no consent yet)", r.ds.callCount())
	}
	if status, _ := r.w.statusNow(); status != viewer.StatusDisplayInUse {
		t.Fatalf("status = %v, want display-in-use", status)
	}
	// The plate names the console and both exits.
	lines := r.w.plateLines(viewer.StatusDisplayInUse, "")
	if len(lines) < 2 || lines[0] == "" || lines[1] == "" {
		t.Fatalf("busy plate lines = %q, want headline + exits", lines)
	}
}

// TestSeatEnterTakesOverAndDials: Enter on the busy plate evicts the holder
// once and connects.
func TestSeatEnterTakesOverAndDials(t *testing.T) {
	r := newSeatRig(t, true)
	r.step()
	r.pressEnter()
	r.step()

	if r.takeCalls != 1 {
		t.Fatalf("takeovers = %d, want 1", r.takeCalls)
	}
	if r.ds.callCount() != 1 {
		t.Fatalf("dials = %d, want 1 after consent", r.ds.callCount())
	}
	r.step()
	if status, _ := r.w.statusNow(); status != viewer.StatusLive {
		t.Fatalf("status = %v, want live after takeover dial", status)
	}
}

// TestSeatRacedConflictReturnsToBusy: a 409 between the check and the dial
// parks back on the busy plate without stealing anything.
func TestSeatRacedConflictReturnsToBusy(t *testing.T) {
	r := newSeatRig(t, false)
	r.ds.failErr = ErrInUse
	r.step()

	if r.ds.callCount() != 1 {
		t.Fatalf("dials = %d, want 1 (check was free)", r.ds.callCount())
	}
	if r.takeCalls != 0 {
		t.Fatalf("takeovers = %d, want 0 (a dial never evicts)", r.takeCalls)
	}
	if status, _ := r.w.statusNow(); status != viewer.StatusDisplayInUse {
		t.Fatalf("status = %v, want display-in-use after raced 409", status)
	}
	// Consent still works from the raced state.
	r.ds.failErr = nil
	r.pressEnter()
	r.step()
	if r.takeCalls != 1 || r.ds.callCount() != 2 {
		t.Fatalf("takeovers = %d dials = %d, want 1 and 2", r.takeCalls, r.ds.callCount())
	}
}

// TestSeatCheckFailureIsFailOpen: a status hiccup dials anyway and lets the
// bridge answer.
func TestSeatCheckFailureIsFailOpen(t *testing.T) {
	r := newSeatRig(t, true)
	r.seatErr = errors.New("status 503")
	r.step()

	if r.ds.callCount() != 1 {
		t.Fatalf("dials = %d, want 1 (fail-open on status error)", r.ds.callCount())
	}
}

// TestSeatTakeoverFailureStaysBusy: a refused takeover keeps the prompt with
// the reason, so Enter retries instead of stranding the window.
func TestSeatTakeoverFailureStaysBusy(t *testing.T) {
	r := newSeatRig(t, true)
	r.takeErr = errors.New("takeover declined")
	r.step()
	r.pressEnter()

	if r.takeCalls != 1 {
		t.Fatalf("takeovers = %d, want 1", r.takeCalls)
	}
	if r.ds.callCount() != 0 {
		t.Fatalf("dials = %d, want 0 after refused takeover", r.ds.callCount())
	}
	if status, _ := r.w.statusNow(); status != viewer.StatusDisplayInUse {
		t.Fatalf("status = %v, want display-in-use after refused takeover", status)
	}
	r.takeErr = nil
	r.pressEnter()
	r.step()
	if r.ds.callCount() != 1 {
		t.Fatalf("dials = %d, want 1 after retry", r.ds.callCount())
	}
}

// TestSeatFreeSkipsConsent: an approved window redials without re-prompting,
// and a later drop that finds the seat busy parks again.
func TestSeatFreeSkipsConsent(t *testing.T) {
	r := newSeatRig(t, false)
	r.step()
	if r.ds.callCount() != 1 {
		t.Fatalf("dials = %d, want 1 (seat free)", r.ds.callCount())
	}
	// Drop the wire: the window redials without asking again.
	conn := r.ds.last()
	conn.breakWire()
	deadline := time.Now().Add(2 * time.Second)
	for r.ds.callCount() < 2 && time.Now().Before(deadline) {
		r.step()
		time.Sleep(5 * time.Millisecond)
	}
	if r.ds.callCount() < 2 {
		t.Fatalf("dials = %d, want redial after drop without re-prompt", r.ds.callCount())
	}
	if r.takeCalls != 0 {
		t.Fatalf("takeovers = %d, want 0 (no consent needed to redial)", r.takeCalls)
	}
}
