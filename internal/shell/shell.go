// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package shell is the client's graphical front door: the window a user opens
// to pick an instance, sign in, and get into a workspace.
//
// # One process, shared SDL
//
// The shell and the display sessions run in the same process and share the
// same SDL library, each in its own window. Activating a workspace does not
// spawn anything: the session window registers with the pump in windows.go
// and the shell's loop steps it beside the shell's own window on the same
// main OS thread. A user therefore keeps the workspace list interactive
// beside any number of live session windows, the SDL library is loaded
// exactly once, and there is no child process to supervise, no IPC to design
// and no orphan to clean up after a crash.
//
// All windows must agree about who owns the main thread — the pump does —
// which is what the [State] machine in model.go and the [liveWindow]
// contract in windows.go are for.
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
	"sync/atomic"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/autostart"
	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/i18n"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/tray"
	"github.com/kube-workspaces/desktop-client/internal/ui"
	"github.com/kube-workspaces/desktop-client/internal/update"
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
	// Autostart manages per-user graphical login startup. Nil uses the OS.
	Autostart     autostart.Service
	Version       string
	Updater       UpdateService
	UpdatePolicy  func() (managed, envDisabled bool)
	RestartUpdate func(*update.Prepared) error
	// RestartMSI restarts into an installer update (MSI-managed Windows
	// installs). Nil means installer updates cannot restart: the shell keeps
	// the verified package and reports the failure, like [RestartUpdate].
	RestartMSI func(*update.MSIPackage) error
	// FirstFrame supplies the Updates status for the first frame: a pending
	// update notice, a previous helper failure, or "". The bool reports
	// whether the text is a failure, so the Copy button tracks it.
	FirstFrame func() (string, bool)
	// Backend is the shell's own window. The shell opens and closes it;
	// every session window brings a backend of its own, and the pump in
	// windows.go steps them all on the same main thread.
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

	// OpenWeb spawns the embedded-webview child process for a non-VM
	// workspace: the standalone kube-workspaces-web binary when it ships
	// beside this executable (releases), otherwise this binary's own `web`
	// subcommand (developer copies). The profile pin travels with the spawn so
	// the child opens the very instance the shell shows. Nil means [spawnWeb].
	OpenWeb   func(profile, namespace, name string) error
	nativeWeb bool

	// Theme overrides the palette. Nil means [ui.DefaultTheme].
	Theme *ui.Theme

	// RefreshInterval overrides [DefaultRefreshInterval]. A negative value
	// turns automatic refresh off, leaving only the manual one.
	RefreshInterval time.Duration

	// Width and Height are the initial window size.
	Width, Height int

	// UIScale overrides the interface scale: 0 means automatic (the
	// display's scale factor, or the stored setting below it). A positive
	// value pins the shell to it, which is how a user on an odd panel — or a
	// test — gets a predictable size. Valid range is 1 to 3.
	UIScale float64

	// NoTray disables the system-tray icon for this run, overriding the
	// stored setting. It is the --no-tray flag.
	NoTray bool

	// TrayNew builds the tray backend behind the menu. Nil means the real
	// SDL tray when the shell's own window is an SDL one, and no tray
	// otherwise. Tests inject a fake here.
	TrayNew func(tray.Handler) tray.Backend

	// PopupNew builds a popup window's backend. Nil means a real SDL
	// window. Tests inject a fake here.
	PopupNew func() viewer.Backend

	// Title is the window title. Empty means "Kube Workspaces".
	Title string

	// Logf, if set, receives diagnostics.
	Logf func(format string, args ...any)
}

func (o *Options) applyDefaults() {
	if o.Autostart == nil {
		o.Autostart = autostart.Native{}
	}
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
	if o.OpenWeb == nil {
		o.OpenWeb = spawnWeb
		o.nativeWeb = true
	}
}

// App is the shell: its own window, one state machine, one user.
//
// Everything in it belongs to the goroutine that called [App.Run] — the same
// goroutine that owns the window — except [App.results], which is how work
// done elsewhere gets back. Nothing else crosses a goroutine boundary, so
// there is no lock in this package.
type App struct {
	updates updateState
	opts    Options
	be      viewer.Backend
	ctx     *ui.Context
	m       Model

	api     API
	dialer  SessionDialer
	profile *config.Profile

	userMenuOpen    bool
	userMenuScroll  int
	userMenuFocus   ui.FocusID
	userMenuSwallow bool // consume the release of an outside-dismissal press
	avatar          avatarState

	// sessions are the held transports, keyed by workspace key. Everything
	// here belongs to the loop's goroutine, like the model.
	sessions map[string]*sessionRecord

	// live are the open session windows, keyed by workspace key: a subset
	// of sessions with a window on screen. The shell's own window stays
	// interactive beside them; the pump in windows.go steps them all.
	live map[string]*liveEntry

	// profiles is the cached profile list for the switcher UI. It is local
	// disk state, reloaded whenever the switcher can be reached, so it never
	// goes stale across CLI edits made while the shell runs.
	profiles []*config.Profile

	// settings is the client's own appearance. It is applied as a theme at
	// startup and whenever the settings screen changes it.
	settings            Settings
	settingsReturn      State
	account             *accountView
	startup             startupState
	settingsScroll      int
	settingsFocus       ui.FocusID
	settingsFocusedRect ui.Rect

	// geomW/geomH cache the shell window's last seen size for the geometry
	// persistence below; geomSavedAt rate-limits the writes.
	geomW, geomH int
	geomSavedAt  time.Time

	// The widgets whose state has to survive a frame.
	serverField   ui.TextInput
	insecureBox   ui.Checkbox
	emailField    ui.TextInput
	passwordField ui.TextInput
	filterField   ui.TextInput
	list          ui.List

	// Selectable read-only values: the message strip, the browser sign-in
	// URL, the installed version, and the Updates status line (failure
	// sentences get pasted into bug reports). Each keeps its own selection.
	msgSel    ui.SelectableText
	urlSel    ui.SelectableText
	verSel    ui.SelectableText
	statusSel ui.SelectableText

	// The "new workspace" form's widget state. The type and image selections
	// are indices rather than widgets: the type row is three buttons and the
	// images ride a second list widget.
	createNameField      ui.TextInput
	createNamespaceField ui.TextInput
	createType           kwclient.WorkspaceType
	createImageIdx       int
	createImageList      ui.List

	// The SSH credential form's widget state: the guest username and the
	// private key file. The key bytes themselves are read at submit and
	// carried in the model for the opening window only — never written.
	sshUserField    ui.TextInput
	sshKeyFileField ui.TextInput

	// The system tray: its backend, the click queue from the tray's
	// thread, the signature of the last menu built (to skip rebuilds),
	// and when a failed creation may be retried.
	tray        tray.Backend
	trayCh      chan tray.Action
	traySig     string
	trayRetryAt time.Time
	// shellHidden reports that the main window is minimized into the
	// tray. Sessions keep stepping while it is set; the tray's opener
	// clears it.
	shellHidden bool

	// popup is the single About/quick-pick window, or nil. Tray clicks
	// open it instead of touching the main window, so a hidden shell
	// stays hidden throughout.
	popup *popupWindow

	// aboutLogo is the decoded app icon for the About panel, loaded once.
	aboutLogo *image.NRGBA

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
	inflight       atomic.Int64
	dirty          bool
	quit           bool
	refreshing     bool
	refreshStarted time.Time
	nextRefresh    time.Time
	repaintAt      time.Time

	watchCancel          context.CancelFunc
	watchGeneration      uint64
	watchSnapshotVersion uint64
	watchKey             string
	watchConnected       bool
	nextAuthCheck        time.Time
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
		opts:     opts,
		be:       opts.Backend,
		ctx:      ui.NewContext(opts.Theme),
		results:  make(chan func(), resultQueue),
		done:     make(chan struct{}),
		trayCh:   make(chan tray.Action, 16),
		sessions: make(map[string]*sessionRecord),
		live:     make(map[string]*liveEntry),
		// A state the machine can never be in, so that the first frame counts
		// as a screen change and places the initial focus.
		lastState: State(-1),
	}
	a.updates.auto = true
	a.serverField.ID = idServer
	a.serverField.Placeholder = i18n.Get("server.placeholder")
	a.emailField.ID = idEmail
	a.emailField.Placeholder = i18n.Get("login.emailPlaceholder")
	a.passwordField.ID = idPassword
	a.passwordField.Password = true
	a.filterField.ID = idFilter
	a.filterField.Placeholder = i18n.Get("workspaces.filter")
	a.list.ID = idList
	a.createNameField.ID = idCreateName
	a.createNameField.Placeholder = i18n.Get("create.namePlaceholder")
	a.createNamespaceField.ID = idCreateNamespace
	a.createNamespaceField.Placeholder = "workspaces"
	a.sshUserField.ID = idSSHUser
	a.sshUserField.Placeholder = i18n.Get("ssh.userPlaceholder")
	a.sshKeyFileField.ID = idSSHKeyFile
	a.sshKeyFileField.Placeholder = i18n.Get("ssh.keyPlaceholder")
	a.createType = kwclient.WorkspaceTypeContainer
	a.createImageList.ID = idCreateImages
	a.insecureBox.ID = idInsecure
	a.insecureBox.Label = i18n.Get("server.tls")
	a.msgSel.ID = idMsgSel
	a.urlSel.ID = idURLSel
	a.verSel.ID = idVerSel
	a.statusSel.ID = idStatusSel

	a.ctx.Clipboard = a.be.Clipboard
	a.ctx.SetClipboard = a.be.SetClipboard
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
	// The tray icon goes with the window: destroy it before the backend
	// closes, while the SDL library is still up.
	defer a.closeTray()
	// Held sessions never outlive the process: the windows going away is
	// the last chance to hand the server's single-seat slots back.
	// closeAllSessions closes the open session windows first (so the
	// guests see their keys released while the connections are still up)
	// and then the held transports.
	defer a.closeAllSessions()
	// The size at close is the size the next launch restores. The resize
	// path rate-limits its writes mid-drag; this flush is what makes the
	// final arrangement durable.
	defer a.flushGeometry()
	// Background work outlives nothing: closing done releases any goroutine
	// still trying to deliver a result into a queue nobody is draining.
	defer close(a.done)
	defer a.stopUpdates()
	defer a.stopWorkspaceWatch()
	defer a.stopAvatar()
	ctx, cancelUpdates := context.WithCancel(ctx)
	defer cancelUpdates()

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
		// cancellation end it through Wake. With session windows open the
		// wait covers their deadlines too and harvests their events
		// alongside the shell's, routed by window.
		a.waitPump(a.idleTimeout(time.Now()))
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

	// A live session window with harvested input steps without waiting.
	for _, e := range a.liveSorted() {
		if len(e.pending) > 0 {
			return 0
		}
	}
	// The popup's input joins the same union: a click on it steps now.
	if a.popup != nil && len(a.popup.events) > 0 {
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
	// Every live window's next deadline joins the union: the pump wakes
	// for whichever window needs it first.
	for _, e := range a.liveSorted() {
		earlier(now.Add(e.window.IdleWait(now)))
	}
	if a.popup != nil {
		earlier(now.Add(a.popup.IdleWait(now)))
	}
	if a.watchConnected {
		earlier(a.nextAuthCheck)
	}
	if a.m.State == StateWorkspaces && a.opts.RefreshInterval > 0 && !a.watchConnected {
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
	a.loadProfiles()

	// Seed the geometry cache from the launch size so an appearance save
	// before the first resize keeps the size the window actually opened at.
	a.geomW, a.geomH = a.opts.Width, a.opts.Height

	// Appearance first: unlike the profile it needs no instance, so even a
	// first-run user sees their own chosen look on the very first screen.
	// A store that cannot be read is not worth stopping over — the defaults
	// are a fine place to start.
	if s, err := a.opts.Store.LoadSettings(); err != nil {
		a.logf("load settings: %v", err)
		a.applySettings(DefaultSettings())
	} else {
		a.applySettings(settingsFromConfig(s))
		a.updates.auto = s.AutoUpdateEnabled()
		a.updates.last = s.LastUpdateCheck
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
	// Tray menu clicks land here, on the loop's goroutine, before the
	// frame: a click's window, panel or notice is part of the next draw.
	a.drainTray(ctx)

	// a.events arrives holding whatever the wait at the bottom of [App.Run]
	// harvested — that wait consumes events, it does not merely observe them —
	// and the poll below appends anything that has landed since. The buffer
	// is handed back to the field empty immediately, so an early return below
	// cannot leave an event to be folded in twice. Live session windows
	// harvest and poll the same way, routed by window (see windows.go).
	a.pollFresh()
	events := a.events
	a.events = events[:0]
	closeRequested, globalQuit := scanQuit(events)
	a.in = a.in.Fold(now, events)
	if len(events) > 0 {
		a.dirty = true
	}
	if globalQuit {
		a.quit = true
		return nil
	}
	if closeRequested {
		// The main window's close button arrived. With a live tray this
		// asks first — quit, or minimize into the tray and keep
		// running — instead of quitting outright. in.Quit is sticky,
		// so diverting consumes it here; the dialog's answer is what
		// finally quits or hides. A repeat close while the question is
		// open is swallowed: the answer is already being asked for.
		a.in.Quit = false
		if a.tray != nil && !a.m.CloseConfirm {
			a.m.ShowCloseConfirm()
		} else if a.tray == nil {
			a.quit = true
			return nil
		}
	} else if a.in.Quit {
		a.quit = true
		return nil
	}
	if a.in.Resized {
		a.dirty = true
		// A scale change arrives as a resize (SDL emits
		// PIXEL_SIZE_CHANGED for it), so this is also where the theme
		// follows the window across mixed-DPI displays.
		a.refreshTheme()
		a.recordGeometry(now)
	}
	if a.in.SystemThemeChanged {
		// The platform flipped its light/dark scheme while the client was
		// running. Re-resolving picks the new scheme up; refreshTheme is
		// cheap enough to run on the change rather than tracking which
		// mode changed.
		a.refreshTheme()
	}

	if a.m.State == StateSession {
		// Opening a session registers a live window with the pump instead
		// of parking the shell inside it: the list stays interactive
		// beside every open display.
		if err := a.openLiveSession(ctx); err != nil {
			return err
		}
	}

	// Every open session window steps beside the shell, on the same thread.
	a.drainWebNavigation()
	if err := a.stepLive(ctx, now); err != nil {
		return err
	}
	// The popup window, if any, steps beside them on the same thread.
	a.stepPopups(ctx, now)

	a.tick(ctx, now)
	// The tray follows the list: its submenu rebuilds when the running
	// set changed, and its driver pumps once per iteration.
	a.reconcileTray(now)
	if !a.dirty {
		return nil
	}
	return a.draw(ctx)
}

// tick handles everything that happens because time passed rather than because
// the user did something: the list refresh and a deferred repaint.
func (a *App) tick(ctx context.Context, now time.Time) {
	a.reconcileAvatar(ctx)
	a.reconcileWorkspaceWatch(ctx)
	a.checkWatchIdentity(ctx, now)
	if a.updates.busy || a.m.State == StateUpdates {
		a.dirty = true
	}
	if !a.repaintAt.IsZero() && !now.Before(a.repaintAt) {
		a.repaintAt = time.Time{}
		a.dirty = true
	}
	if a.m.State != StateWorkspaces || a.opts.RefreshInterval < 0 || a.watchConnected {
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
		// Restoring a valid token bypasses sign-in discovery. Every return
		// to login must discover its controls, including sign-out and a
		// credential revoked while the list/watch was open. Probe only on
		// entry so discovery failures do not retry on every frame.
		if a.m.State == StateLogin {
			a.ensureAuthConfig(ctx)
		}
		// Focus belongs to a screen. Carrying it across a transition would
		// leave the keyboard on a control that is no longer drawn, which is
		// indistinguishable from the keyboard not working.
		a.ctx.Focus().Clear()
		if a.m.State == StateWorkspaces && (a.lastState == StateSettings || a.lastState == StateUpdates || a.lastState == StateProfile) {
			a.ctx.Focus().Set(idUserMenu)
		}
		a.lastState = a.m.State
		if a.m.State != StateProfile {
			a.account = nil
		}
	}

	a.ctx.Begin(a.canvas, a.in)
	// A modal keeps the previous frame as a frozen, dimmed backdrop. The
	// background is deliberately not cleared and the list is not drawn, so the
	// buffer still holds the frame the user last saw and the modal dims it.
	modal := a.m.State == StateWorkspaces && (a.m.Info != nil || a.m.Creating || a.m.Profiles || a.m.SessionList || a.m.SSH != nil)
	if a.m.State != StateWorkspaces || modal || a.m.CloseConfirm {
		a.userMenuOpen = false
		a.userMenuScroll = 0
	}
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
		switch {
		case a.m.SessionList:
			intent = a.drawSessionsModal(a.canvas.Bounds())
		case a.m.Profiles:
			intent = a.drawProfilesModal(a.canvas.Bounds())
		case a.m.Creating:
			intent = a.drawCreateModal(a.canvas.Bounds())
		case a.m.SSH != nil:
			intent = a.drawSSHModal(a.canvas.Bounds())
		case modal:
			intent = a.drawWorkspaceInfoModal(a.canvas.Bounds())
		default:
			intent = a.drawWorkspacesWithUserMenu(a.canvas.Bounds())
		}
	case StateSettings:
		intent = a.drawSettingsScreen(a.canvas.Bounds())
	case StateUpdates:
		intent = a.drawUpdatesScreen(a.canvas.Bounds())
	case StateProfile:
		intent = a.drawAccountScreen(a.canvas.Bounds())
	}
	if a.m.State == StateServer || a.m.State == StateLogin {
		if kind := a.drawStandaloneSettings(a.canvas.Bounds()); kind != intentNone {
			intent.kind = kind
		}
	}
	// The close question overlays any screen, and wins over other
	// intents: it answers whether the application keeps running at all.
	// (About and the quick-pick chooser are popup windows of their own,
	// so they never appear here.)
	if a.m.CloseConfirm {
		intent = a.drawCloseConfirmModal(a.canvas.Bounds())
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
	if !a.updates.started {
		a.updates.started = true
		if a.opts.FirstFrame != nil {
			a.updates.status, a.updates.failed = a.opts.FirstFrame()
		}
		a.checkUpdate(ctx, false)
	}
	return nil
}

// drainWebNavigation applies the navigation requests an embedded web child
// made over its authenticated loopback capability. A webview window is an OS
// process outside the pump, so it cannot raise itself: the shell comes forward
// and answers the request here, on the next step.
func (a *App) drainWebNavigation() {
	if !a.opts.nativeWeb {
		return
	}
	for {
		select {
		case action := <-webNavigationEvents:
			_ = a.be.Show()
			_ = a.be.Raise()
			if action == connection.Sessions {
				a.m.ShowSessionList()
			}
			a.dirty = true
		default:
			return
		}
	}
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
// inflight counts pieces of work that have been started but whose result the
// loop has not consumed yet; a test that settles can use it to wait for a
// save to land before asserting on the store.
func (a *App) background(fn func() func()) {
	a.inflight.Add(1)
	go func() {
		defer a.inflight.Add(-1)
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
	a.stopWorkspaceWatch()
	api, dialer, err := a.opts.NewClient(server, insecure)
	if err != nil {
		return err
	}
	a.api, a.dialer = api, dialer
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
