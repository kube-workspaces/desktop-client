// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/session"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// fakeAgentDet stands in for *viewer.Tier1Detached. Its Step mirrors the real
// window: when the producer has ended, Step reports the terminal error and
// Closed reports true in the same pass.
type fakeAgentDet struct {
	stepErr error
	closed  bool
	result  error
}

func (f *fakeAgentDet) Step(time.Time, []viewer.Event) error { return f.stepErr }
func (f *fakeAgentDet) IdleWait(time.Time) time.Duration     { return 0 }
func (f *fakeAgentDet) Closed() bool                         { return f.closed }
func (f *fakeAgentDet) Result() error                        { return f.result }
func (f *fakeAgentDet) CloseDisposition() connection.CloseDisposition {
	return connection.Park
}
func (f *fakeAgentDet) Close()                  {}
func (f *fakeAgentDet) Backend() viewer.Backend { return nil }
func (f *fakeAgentDet) ReleaseInput()           {}

func newAgentWindow(det agentDetached) *agentLiveWindow {
	return &agentLiveWindow{
		handle: &agentHandle{
			ws: kwclient.Workspace{Namespace: "ns", Name: "vm"},
		},
		det: det,
	}
}

// A premium window whose producer ends with ErrAgentConsoleRequired must be
// classified as a console handoff, not surfaced as a window failure. The
// producer error arrives through Step (the det reaps it), so Step must not
// return it before the terminal result is mapped. Regression guard: the old
// ordering returned the raw error and the pump closed the session instead of
// dialing the console.
func TestAgentWindowConsoleRequiredIsHandoffNotFailure(t *testing.T) {
	w := newAgentWindow(&fakeAgentDet{
		stepErr: session.ErrAgentConsoleRequired,
		closed:  true,
		result:  session.ErrAgentConsoleRequired,
	})

	err := w.Step(context.Background(), time.Now(), nil)
	if err != nil {
		t.Fatalf("Step returned %v; a console requirement must not surface as a window error", err)
	}
	if !w.closed {
		t.Fatalf("window not closed after the handoff decision")
	}
	// The handoff is entered with a nil API client, so dialing the console
	// fails: the result is that dial error, never the raw console sentinel.
	if w.result == nil {
		t.Fatalf("expected the handoff dial error to be recorded")
	}
	if errors.Is(w.result, session.ErrAgentConsoleRequired) {
		t.Fatalf("result = %v; the console sentinel leaked through the handoff", w.result)
	}
}

// A premium window whose producer ends with a recoverable transport error
// falls back to Tier 0 (the console), exactly like the blocking path. The
// classification must reach MapAgentResult; the raw error must not surface as
// a window failure.
func TestAgentWindowTransportErrorFallsBack(t *testing.T) {
	boom := errors.New("transport lost")
	w := newAgentWindow(&fakeAgentDet{stepErr: boom, closed: true, result: boom})

	if err := w.Step(context.Background(), time.Now(), nil); err != nil {
		t.Fatalf("Step returned %v; a closed window's error belongs in Result", err)
	}
	if !w.closed || w.result == nil || errors.Is(w.result, boom) {
		t.Fatalf("closed=%v result=%v, want a Tier 0 fallback attempt", w.closed, w.result)
	}
}

// ErrNoFallback ends the session with the underlying error: refusal, busy and
// auth never dial the console.
func TestAgentWindowNoFallbackEndsSession(t *testing.T) {
	refused := fmt.Errorf("%w: account is busy", session.ErrNoFallback)
	w := newAgentWindow(&fakeAgentDet{stepErr: refused, closed: true, result: refused})

	if err := w.Step(context.Background(), time.Now(), nil); err != nil {
		t.Fatalf("Step returned %v; a closed window's error belongs in Result", err)
	}
	if !w.closed || !errors.Is(w.result, session.ErrNoFallback) {
		t.Fatalf("closed=%v result=%v, want the no-fallback error", w.closed, w.result)
	}
}

// A live premium window keeps stepping and reports any mid-flight step error.
func TestAgentWindowLiveStepErrorSurfaces(t *testing.T) {
	boom := errors.New("draw failed")
	w := newAgentWindow(&fakeAgentDet{stepErr: boom, closed: false})

	if err := w.Step(context.Background(), time.Now(), nil); !errors.Is(err, boom) {
		t.Fatalf("Step returned %v, want the live step error", err)
	}
	if w.closed {
		t.Fatalf("live window must not be marked closed")
	}
}
