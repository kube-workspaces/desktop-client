// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"sort"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// liveWindow is one open session window driven by the shell's pump.
//
// The shell plus every open session window share one process, one main OS
// thread and one platform event queue. Exactly one consumer may drain that
// queue per iteration (a second drain would steal the first window's input),
// so windows never poll for themselves: the pump drains once, routes each
// event to its owner, and steps every window with only its own slice. A
// window that is stepped with another window's events misbehaves; a window
// whose events are dropped looks dead. See internal/viewer.PollRouted.
//
// Every method runs on the loop's goroutine, which is the goroutine that
// owns every window.
type liveWindow interface {
	// Step runs one iteration against the window's own routed events.
	Step(ctx context.Context, now time.Time, events []viewer.Event) error
	// IdleWait reports how long the pump may block before this window has
	// scheduled work of its own. The pump waits on the union of its
	// windows' deadlines.
	IdleWait(now time.Time) time.Duration
	// Closed reports whether the window asked to close: its close button,
	// the quit chord, cancellation, or a lingered terminal failure.
	Closed() bool
	// Result is the terminal failure to report when the window closed, or
	// nil for a clean close (which parks the held transport).
	Result() error
	// Close tears the window down.
	Close()
	// WindowBackend returns the backend owning this window, for the pump's
	// routing and waiting. Nil means the window has no platform queue of
	// its own (a test double) and is stepped with an empty slice.
	WindowBackend() viewer.Backend
	// Raise moves the window to the front and focuses it, for the
	// sessions switcher to focus an already-open window instead of
	// opening a second one on the same transport.
	Raise() error
	// ReleaseInput releases every key the guest believes is held, for
	// the pump to call when focus moves to another window.
	ReleaseInput()
}

// liveEntry is one open session window plus the workspace it shows. The
// transport underneath stays in App.sessions under the same key, exactly as
// a parked session's does; the only difference is that a live entry also
// has a window in App.live.
type liveEntry struct {
	key      string
	ws       kwclient.Workspace
	observer bool
	window   liveWindow
	// pending holds this window's harvested events between the pump's wait
	// at the bottom of the loop and the step at the top of the next
	// iteration, mirroring App.events for the shell window.
	pending []viewer.Event
}

// liveSorted returns the live entries in stable key order, so that stepping
// several windows is deterministic.
func (a *App) liveSorted() []*liveEntry {
	out := make([]*liveEntry, 0, len(a.live))
	for _, e := range a.live {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// isLive reports whether key currently has an open window.
func (a *App) isLive(key string) bool {
	_, ok := a.live[key]
	return ok
}

// sdlTargets returns the shell backend followed by every live window
// backend as *viewer.SDLBackend, for the single routed drain, plus the
// popup backend when one is open. The fourth result is false when any
// window is not SDL (a test double with a private queue), when no session
// window is open and no popup is either — both cases poll each backend
// individually, which is also what the single-window loop has always done.
func (a *App) sdlTargets() ([]*viewer.SDLBackend, map[*viewer.SDLBackend]*liveEntry, *viewer.SDLBackend, bool) {
	shell, ok := a.be.(*viewer.SDLBackend)
	if !ok {
		return nil, nil, nil, false
	}
	var popupBe *viewer.SDLBackend
	if a.popup != nil {
		popupBe, ok = a.popup.be.(*viewer.SDLBackend)
		if !ok || popupBe == nil {
			return nil, nil, nil, false
		}
	}
	if len(a.live) == 0 && popupBe == nil {
		return nil, nil, nil, false
	}
	targets := make([]*viewer.SDLBackend, 0, len(a.live)+2)
	byBackend := make(map[*viewer.SDLBackend]*liveEntry, len(a.live))
	targets = append(targets, shell)
	for _, e := range a.liveSorted() {
		be, ok := e.window.WindowBackend().(*viewer.SDLBackend)
		if !ok || be == nil {
			return nil, nil, nil, false
		}
		targets = append(targets, be)
		byBackend[be] = e
	}
	if popupBe != nil {
		targets = append(targets, popupBe)
	}
	return targets, byBackend, popupBe, true
}

// pollFresh drains whatever platform input has landed since the last step,
// appending it onto the shell's harvest and each live window's pending
// slice, plus the popup's when one is open. In a multi-window SDL process
// the queue is drained once and routed; otherwise each backend drains its
// own queue.
func (a *App) pollFresh() {
	targets, byBackend, popupBe, ok := a.sdlTargets()
	if !ok {
		a.events = a.be.PollEvents(a.events)
		for _, e := range a.liveSorted() {
			if be := e.window.WindowBackend(); be != nil {
				e.pending = be.PollEvents(e.pending)
			}
		}
		if a.popup != nil && a.popup.be != nil {
			a.popup.events = a.popup.be.PollEvents(a.popup.events)
		}
		return
	}
	routed := viewer.PollRouted(targets)
	for be, evs := range routed {
		if popupBe != nil && be == popupBe && a.popup != nil {
			a.popup.events = append(a.popup.events, evs...)
		} else if e, ok := byBackend[be]; ok {
			e.pending = append(e.pending, evs...)
		} else {
			a.events = append(a.events, evs...)
		}
	}
}

// waitPump blocks until platform input arrives or timeout elapses, then
// harvests exactly like pollFresh does. A wake (see viewer.Backend.Wake)
// ends the wait without producing an event.
func (a *App) waitPump(timeout time.Duration) {
	targets, byBackend, popupBe, ok := a.sdlTargets()
	if !ok {
		a.events = a.be.WaitEvents(a.events, timeout)
		for _, e := range a.liveSorted() {
			if be := e.window.WindowBackend(); be != nil {
				e.pending = be.PollEvents(e.pending)
			}
		}
		if a.popup != nil && a.popup.be != nil {
			a.popup.events = a.popup.be.PollEvents(a.popup.events)
		}
		return
	}
	routed := viewer.WaitRouted(targets, timeout)
	for be, evs := range routed {
		if popupBe != nil && be == popupBe && a.popup != nil {
			a.popup.events = append(a.popup.events, evs...)
		} else if e, ok := byBackend[be]; ok {
			e.pending = append(e.pending, evs...)
		} else {
			a.events = append(a.events, evs...)
		}
	}
}

// stepLive steps every open session window with its own routed events and
// parks the ones that closed: a clean close keeps the transport held (the
// switcher resumes it), a failure releases it and reports. A window whose
// step itself fails is treated as a failed session.
func (a *App) stepLive(ctx context.Context, now time.Time) error {
	for _, e := range a.liveSorted() {
		// The entry may already be gone: closing one window parks it, and
		// parking re-arms nothing else, so a second close in the same pass
		// is simply skipped.
		if _, ok := a.live[e.key]; !ok {
			continue
		}
		evs := e.pending
		e.pending = e.pending[:0]
		if err := e.window.Step(ctx, now, evs); err != nil {
			a.closeLiveFailed(e.key, err)
			continue
		}
		if e.window.Closed() {
			a.parkLive(ctx, e.key)
		}
	}
	return nil
}

// parkLive unregisters a closed session window. A clean close parks the
// transport exactly as the old blocking loop did; a terminal failure
// releases it and reports. Cancellation on the way out parks quietly: the
// deferred teardown hands every slot back together.
func (a *App) parkLive(ctx context.Context, key string) {
	e, ok := a.live[key]
	if !ok {
		return
	}
	delete(a.live, key)
	res := e.window.Result()
	e.window.Close()
	if ctx.Err() != nil {
		a.quit = true
		return
	}
	if res != nil {
		a.closeSession(key)
		a.m.SessionEnded(res)
		a.dirty = true
		return
	}
	a.m.SessionParked(e.ws)
	a.dirty = true
}

// closeLiveFailed releases a session whose window step itself failed. A
// write to a dead connection ends the connection, not the session, inside
// the viewer — so reaching here means the window is unusable and the
// transport goes with it, exactly like a failed Attach did.
func (a *App) closeLiveFailed(key string, err error) {
	e, ok := a.live[key]
	if !ok {
		return
	}
	delete(a.live, key)
	e.window.Close()
	a.closeSession(key)
	a.m.SessionEnded(err)
	a.dirty = true
}

// closeLiveWindow tears down one open window without touching its held
// transport, for the paths that release the transport separately
// (disconnecting from the switcher) or replace it (opening an observer
// over a live display).
func (a *App) closeLiveWindow(key string) {
	if e, ok := a.live[key]; ok {
		delete(a.live, key)
		e.window.Close()
	}
}

// closeAllLiveWindows tears down every open session window. Held
// transports survive it — the switcher can still resume them — which is
// why sign-out and profile switch pair it with closeAllSessions, and why
// process exit runs both as defers, windows first so the guests are told
// their keys are up while the connections are still open.
func (a *App) closeAllLiveWindows() {
	for key, e := range a.live {
		delete(a.live, key)
		e.window.Close()
	}
	a.syncSessions()
}
