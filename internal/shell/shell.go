// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package shell is the client's graphical front door: the window a user opens
// to pick an instance, sign in, and get into a workspace.
//
// # One process, shared SDL
//
// The shell and the display session run in the same process and share the same
// SDL library, but each opens its own window. Activating a VM workspace does
// not spawn anything: the shell's loop parks while the session viewer runs its
// own window and loop on the same main OS thread. A user therefore sees the
// session window open alongside the shell, the SDL library is loaded exactly
// once, and there is no child process to supervise, no IPC to design and no
// orphan to clean up after a crash.
//
// Both halves must agree about which one owns the main thread, which is what
// the [State] machine in model.go is for.
//
// # Shape
//
//   - model.go holds [Model]: the state machine, with no window, no client and
//     no goroutine in it, so the transitions can be tested directly.
//   - screens.go draws each state. The drawing is a pure function of the model
//     and returns the user's intent as a value; it never performs I/O.
//   - shell.go (this file) is the loop: it polls the window, runs the network
//     work on other goroutines, and applies the results to the model.
//   - api.go declares the seams ([API], [Store], [Connector]) that let all of
//     the above be exercised without a display or a network.
package shell

import (
	"context"
	"fmt"
	"image"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/ui"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// Defaults for [Options].
const (
	// DefaultRefreshInterval is how often the workspace list is refetched.
	//
	// Five seconds is a compromise the platform picks for us: workspaces take
	// tens of seconds to start, so a slower poll would leave a user staring at
	// "starting" long after it finished, and a faster one would put a
	// pointless request per second per client on an API that lists across
	// every namespace the user can see.
	DefaultRefreshInterval = 5 * time.Second

	// DefaultWidth and DefaultHeight size the window at startup.
	//
	// It is also the size the window keeps: sessions run in their own
	// windows and the user resizes this one, so the only thing that changes
	// it is the user.
	DefaultWidth  = 1280
	DefaultHeight = 800

	// maxIdleWait bounds how long the loop will sleep in one blocking wait.
	//
	// It is not a poll interval and not a frame rate: the loop is woken by
	// input, by finished background work and by cancellation, so in the
	// steady state this timeout is what expires, does nothing, and goes back
	// to sleep. It exists only as a backstop — a missed wake should cost a
	// beat, not hang the window — and one second is short enough that nobody
	// would notice such a bug as anything but a stutter, and long enough that
	// a shell nobody is touching costs no measurable CPU.
	maxIdleWait = time.Second

	// resultQueue is how many completed background operations may be waiting
	// to be applied. The shell never has more than a handful in flight, so
	// this only exists so that a result can never block the goroutine that
	// produced it.
	resultQueue = 16
)

// Options configures an [App]. Backend and NewClient are required.
type Options struct {
	// Backend is the window. The shell opens and closes it, and lends it to
	// the session viewer in between.
	Backend viewer.Backend

	// NewClient builds the API client for an instance and the connector that
	// opens sessions through it.
	NewClient ClientFactory

	// Store persists the profile and its token. Nil means [ConfigStore].
	Store Store

	// OpenBrowser opens a URL in the user's browser, for the non-VM
	// workspaces that are web applications rather than displays. Nil means
	// the platform's default handler.
	OpenBrowser func(rawURL string) error

	// Theme overrides the palette. Nil means [ui.DefaultTheme].
	Theme *ui.Theme

	// RefreshInterval overrides [DefaultRefreshInterval]. A negative value
	// turns automatic refresh off, leaving only the manual one.
	RefreshInterval time.Duration

	// Width and Height are the initial window size.
	Width, Height int

	// Title is the window title. Empty means "Kube Workspaces".
	Title string

	// Logf, if set, receives diagnostics.
	Logf func(format string, args ...any)
}

func (o *Options) applyDefaults() {
	if o.Store == nil {
		o.Store = ConfigStore{}
	}
	if o.Theme == nil {
		o.Theme = ui.DefaultTheme()
	}
	if o.RefreshInterval == 0 {
		o.RefreshInterval = DefaultRefreshInterval
	}
	if o.Width <= 0 {
		o.Width = DefaultWidth
	}
	if o.Height <= 0 {
		o.Height = DefaultHeight
	}
	if o.Title == "" {
		o.Title = "Kube Workspaces"
	}
	if o.OpenBrowser == nil {
		o.OpenBrowser = openBrowser
	}
}

// App is the shell: its own window, one state machine, one user.
//
// Everything in it belongs to the goroutine that called [App.Run] — the same
// goroutine that owns the window — except [App.results], which is how work
// done elsewhere gets back. Nothing else crosses a goroutine boundary, so
// there is no lock in this package.
type App struct {
	opts Options
	be   viewer.Backend
	ctx  *ui.Context
	m    Model

	api     API
	connect Connector
	profile *config.Profile

	// settings is the client's own appearance. It is applied as a theme at
	// startup and whenever the settings screen changes it.
	settings Settings

	// The widgets whose state has to survive a frame.
	serverField   ui.TextInput
	insecureBox   ui.Checkbox
	emailField    ui.TextInput
	passwordField ui.TextInput
	filterField   ui.TextInput
	list          ui.List

	// The surface. img is reallocated on resize; canvas wraps it.
	img            *image.RGBA
	canvas         *ui.Canvas
	texW, texH     int
	surfW, surfH   int
	in             ui.Input
	events         []ui.Event
	lastState      State
	authorizeURL   string
	cancelPending  context.CancelFunc
	results        chan func()
	done           chan struct{}
	dirty          bool
	quit           bool
	refreshing     bool
	nextRefresh    time.Time
	repaintAt      time.Time
}

// New returns an App. It opens no window; see [App.Run].
func New(opts Options) (*App, error) {
	if opts.Backend == nil {
		return nil, fmt.Errorf("shell: no window backend")
	}
	if opts.NewClient == nil {
		return nil, fmt.Errorf("shell: no client factory")
	}
	opts.applyDefaults()

	a := &App{
		opts:    opts,
		be:      opts.Backend,
		ctx:     ui.NewContext(opts.Theme),
		results: make(chan func(), resultQueue),
		done:    make(chan struct{}),
		// A state the machine can never be in, so that the first frame counts
		// as a screen change and places the initial focus.
		lastState: State(-1),
	}
	a.serverField.ID = idServer
	a.serverField.Placeholder = "https://workspaces.example.com"
	a.emailField.ID = idEmail
	a.emailField.Placeholder = "you@example.com"
	a.passwordField.ID = idPassword
	a.passwordField.Password = true
	a.filterField.ID = idFilter
	a.filterField.Placeholder = "Filter workspaces"
	a.list.ID = idList
	a.insecureBox.ID = idInsecure
	a.insecureBox.Label = "Ignore TLS certificate errors"

	a.ctx.Clipboard = a.be.Clipboard
	return a, nil
}

// Model returns the current state, for tests and diagnostics.
func (a *App) Model() *Model { return &a.m }

// Run opens the window and drives the shell until the user closes it or ctx is
// cancelled.
//
// It must be called from the goroutine that owns the main OS thread: the
// backend demands it, and so does the session viewer this hands the window to.
func (a *App) Run(ctx context.Context) error {
	if err := a.be.Open(viewer.WindowOptions{
		Title:        a.opts.Title,
		Width:        a.opts.Width,
		Height:       a.opts.Height,
		ScaleQuality: viewer.ScaleNearest,
		VSync:        true,
	}); err != nil {
		return fmt.Errorf("shell: open window: %w", err)
	}
	defer a.be.Close()
	// Background work outlives nothing: closing done releases any goroutine
	// still trying to deliver a result into a queue nobody is draining.
	defer close(a.done)

	// The loop below blocks in the backend rather than in a select, so
	// cancellation has to arrive as an event like everything else. Registered
	// after the defers above so that it is stopped before the window closes,
	// and safe regardless: Wake is the one backend method a foreign goroutine
	// may call.
	stopWake := context.AfterFunc(ctx, a.be.Wake)
	defer stopWake()

	a.Start(ctx)

	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := a.Step(ctx, time.Now()); err != nil {
			return err
		}
		if a.quit {
			return nil
		}
		// Sleep until there is something to do. A shell sitting on the
		// workspace list has no animation and no frame rate — it repaints
		// when the user acts or when data arrives — so polling it every 8ms
		// was 125 wakeups a second to discover, 125 times, that nothing had
		// changed. Input ends the wait by itself; background work and
		// cancellation end it through Wake.
		a.events = a.be.WaitEvents(a.events[:0], a.idleTimeout(time.Now()))
	}
}

// idleTimeout is how long the loop may block before it has work to do anyway.
func (a *App) idleTimeout(now time.Time) time.Duration {
	switch {
	case a.quit || a.dirty:
		// A frame is owed; drawing it is the next thing that happens.
		return 0
	case a.m.State == StateSession:
		// The next Step hands the window to the session viewer. Waiting first
		// would add this timeout to the time-to-first-pixel of every session.
		return 0
	case len(a.results) > 0:
		// Background work finished after this iteration drained the queue.
		// The Wake that came with it may already have been consumed by this
		// iteration's PollEvents, so the queue itself is what is trusted here
		// — the wake is an optimisation, not the mechanism.
		return 0
	}

	var due time.Time
	earlier := func(t time.Time) {
		if !t.IsZero() && (due.IsZero() || t.Before(due)) {
			due = t
		}
	}
	// A deferred repaint is a widget that asked to be drawn again (a caret
	// blinking, a spinner turning), so it is a real deadline.
	earlier(a.repaintAt)
	if a.m.State == StateWorkspaces && a.opts.RefreshInterval > 0 {
		if a.nextRefresh.IsZero() {
			return 0
		}
		earlier(a.nextRefresh)
	}
	if due.IsZero() {
		return maxIdleWait
	}
	if d := due.Sub(now); d > 0 {
		return min(d, maxIdleWait)
	}
	return 0
}

// Start restores the stored profile and decides which screen to open on.
//
// It is separate from [App.Run] so that a test can drive the shell a step at a
// time against a fake backend, which is the only way to assert on a state
// machine whose transitions are network results.
func (a *App) Start(ctx context.Context) {
	a.dirty = true

	// Appearance first: unlike the profile it needs no instance, so even a
	// first-run user sees their own chosen look on the very first screen.
	// A store that cannot be read is not worth stopping over — the defaults
	// are a fine place to start.
	if s, err := a.opts.Store.LoadSettings(); err != nil {
		a.logf("load settings: %v", err)
		a.applySettings(DefaultSettings())
	} else {
		a.applySettings(settingsFromConfig(s))
	}

	profile, token, err := a.opts.Store.Load()
	if err != nil {
		a.m.NeedServer(Describe(err))
		return
	}
	if profile == nil || profile.Server == "" {
		a.m.NeedServer("")
		return
	}

	a.profile = profile
	a.m.Server, a.m.Insecure = profile.Server, profile.InsecureSkipVerify
	a.serverField.SetValue(profile.Server)

	a.emailField.SetValue(profile.Email)
	a.insecureBox.Checked = profile.InsecureSkipVerify

	if err := a.useServer(profile.Server, profile.InsecureSkipVerify); err != nil {
		a.m.NeedServer(Describe(err))
		return
	}
	if token == "" {
		// A known instance with no token: go straight to signing in, which
		// needs the auth configuration first.
		a.m.State = StateLogin
		a.ensureAuthConfig(ctx)
		return
	}
	a.api.SetToken(token)
	a.verifySession(ctx)
}

// Step runs one iteration: drain results, poll input, act, draw.
//
// It is exported for the same reason [App.Start] is — the tests drive the loop
// themselves — and takes the time rather than reading the clock so those tests
// are not racing it.
func (a *App) Step(ctx context.Context, now time.Time) error {
	a.drainResults()

	// a.events arrives holding whatever the wait at the bottom of [App.Run]
	// harvested — that wait consumes events, it does not merely observe them —
	// and PollEvents appends anything that has landed since. The buffer is
	// handed back to the field empty immediately, so an early return below
	// cannot leave an event to be folded in twice.
	events := a.be.PollEvents(a.events)
	a.events = events[:0]
	a.in = a.in.Fold(now, events)
	if len(events) > 0 {
		a.dirty = true
	}
	if a.in.Quit {
		a.quit = true
		return nil
	}
	if a.in.Resized {
		a.dirty = true
	}

	if a.m.State == StateSession {
		return a.runSession(ctx)
	}

	a.tick(ctx, now)
	if !a.dirty {
		return nil
	}
	return a.draw(ctx)
}

// tick handles everything that happens because time passed rather than because
// the user did something: the list refresh and a deferred repaint.
func (a *App) tick(ctx context.Context, now time.Time) {
	if !a.repaintAt.IsZero() && !now.Before(a.repaintAt) {
		a.repaintAt = time.Time{}
		a.dirty = true
	}
	if a.m.State != StateWorkspaces || a.opts.RefreshInterval < 0 {
		return
	}
	if a.nextRefresh.IsZero() || !now.Before(a.nextRefresh) {
		a.nextRefresh = now.Add(a.opts.RefreshInterval)
		a.refreshWorkspaces(ctx, false)
	}
}

// draw lays out and renders one frame, then uploads it.
//
// Layout and input handling are the same pass — that is what immediate mode
// means — so this is also where the user's actions are collected and turned
// into work.
func (a *App) draw(ctx context.Context) error {
	w, h := a.be.Size()
	if w <= 0 || h <= 0 {
		return nil
	}
	a.ensureSurface(w, h)

	if a.m.State != a.lastState {
		// Focus belongs to a screen. Carrying it across a transition would
		// leave the keyboard on a control that is no longer drawn, which is
		// indistinguishable from the keyboard not working.
		a.ctx.Focus().Clear()
		a.lastState = a.m.State
	}

	a.ctx.Begin(a.canvas, a.in)
	// A modal keeps the previous frame as a frozen, dimmed backdrop. The
	// background is deliberately not cleared and the list is not drawn, so the
	// buffer still holds the frame the user last saw and the modal dims it.
	modal := a.m.State == StateWorkspaces && a.m.Info != nil
	if !modal {
		a.canvas.Fill(a.canvas.Bounds(), a.opts.Theme.Background)
	}

	var intent intent
	switch a.m.State {
	case StateServer:
		intent = a.drawServerScreen(a.canvas.Bounds())
	case StateLogin:
		intent = a.drawLoginScreen(a.canvas.Bounds())
	case StateWorkspaces:
		if modal {
			intent = a.drawWorkspaceInfoModal(a.canvas.Bounds())
		} else {
			intent = a.drawWorkspacesScreen(a.canvas.Bounds())
		}
	case StateSettings:
		intent = a.drawSettingsScreen(a.canvas.Bounds())
	}
	a.ctx.End()

	if a.ctx.NeedsRepaint() {
		a.dirty = true
	} else {
		a.dirty = false
	}
	if d, ok := a.ctx.RepaintDelay(); ok {
		a.repaintAt = a.in.Now.Add(d)
	}

	if err := a.present(w, h); err != nil {
		return err
	}
	// Acting after presenting means the frame the user sees is the one their
	// click was tested against, and that a command which blocks (opening a
	// browser) does so with the window already updated.
	a.act(ctx, intent)
	return nil
}

// present uploads the canvas and shows it.
func (a *App) present(w, h int) error {
	if a.texW != w || a.texH != h {
		if err := a.be.SetTextureSize(w, h); err != nil {
			return fmt.Errorf("shell: allocate %dx%d texture: %w", w, h, err)
		}
		a.texW, a.texH = w, h
	}
	full := viewer.Rect{W: w, H: h}
	if err := a.be.Upload(full, a.img.Pix, a.img.Stride); err != nil {
		return fmt.Errorf("shell: upload frame: %w", err)
	}
	if err := a.be.Present(full, viewer.Overlay{}); err != nil {
		return fmt.Errorf("shell: present: %w", err)
	}
	return nil
}

// ensureSurface allocates the drawing surface when the window size changes.
func (a *App) ensureSurface(w, h int) {
	if a.img != nil && a.surfW == w && a.surfH == h {
		return
	}
	a.img = image.NewRGBA(image.Rect(0, 0, w, h))
	a.canvas = ui.NewCanvas(a.img)
	a.surfW, a.surfH = w, h
}

// drainResults applies everything background work has finished.
func (a *App) drainResults() {
	for {
		select {
		case fn := <-a.results:
			fn()
			a.dirty = true
		default:
			return
		}
	}
}

// background runs fn off the UI goroutine and delivers its result — a closure
// that applies it — back to the loop.
//
// Returning a closure rather than a value is what keeps this package free of
// locks: everything that touches the model runs on the loop's goroutine, and
// the only thing crossing the boundary is a function nobody else will call.
func (a *App) background(fn func() func()) {
	go func() {
		apply := fn()
		if apply == nil {
			return
		}
		select {
		case a.results <- apply:
			// The loop is very likely parked in a blocking event wait, and
			// this result is the only thing that has happened. Nudge it, or
			// the answer to a click sits in the queue until the wait times
			// out.
			a.be.Wake()
		case <-a.done:
			// The window is going away; there is nobody to tell.
		}
	}()
}

// useServer points the shell at an instance.
func (a *App) useServer(server string, insecure bool) error {
	api, connect, err := a.opts.NewClient(server, insecure)
	if err != nil {
		return err
	}
	a.api, a.connect = api, connect
	a.m.Server, a.m.Insecure = server, insecure
	return nil
}

// cancelInFlight cancels whatever long-running operation is outstanding. Only
// one can be, because every screen that starts one disables the controls that
// could start another.
func (a *App) cancelInFlight() {
	if a.cancelPending != nil {
		a.cancelPending()
		a.cancelPending = nil
	}
}

// logf reports a diagnostic.
func (a *App) logf(format string, args ...any) {
	if a.opts.Logf != nil {
		a.opts.Logf(format, args...)
	}
}

// imageFor returns the catalog entry for a workspace, or nil.
func (a *App) imageFor(ws kwclient.Workspace) *kwclient.Image {
	return kwclient.ImageFor(a.m.Images, ws)
}
