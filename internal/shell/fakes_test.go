// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/ui"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// fakeBackend is a [viewer.Backend] with no window behind it.
//
// It is the same shape as the fake in internal/viewer's own tests, which
// cannot be reused because it is unexported and in that package's test files.
// Duplicating forty lines is cheaper than exporting a test double from a
// production package.
//
// Every method takes the mutex: the shell only calls the backend from its own
// goroutine, but a test that pushes events from another one should not have to
// think about it.
type fakeBackend struct {
	mu sync.Mutex

	opened, closed int
	w, h           int
	texW, texH     int
	title          string
	fullscreen     bool
	queue          []viewer.Event
	uploads        int
	presents       int
	clipboard      string
	sized          [][2]int

	// wake stands in for SDL's event queue as far as WaitEvents is concerned:
	// a buffered slot, so that a wake delivered while nobody is waiting is
	// still there for the next wait. That is the property the real backend
	// gets from pushing a user event, and the property the shell's loop rests
	// on — a result that lands a microsecond before the wait begins must not
	// sit there until the timeout.
	wake  chan struct{}
	wakes int
	// polls counts drains of the event queue, which is how a test measures
	// how hard the loop is spinning.
	polls int

	// audio tracks the AudioSink half of the backend. audioErr makes
	// OpenAudio fail, so a test can exercise the "backend cannot play audio"
	// branch without a second type.
	audioOpens  int
	audioCloses int
	audioFormat viewer.AudioFormat
	audioPlayed [][]byte
	audioErr    error
}

func newFakeBackend(w, h int) *fakeBackend {
	return &fakeBackend{w: w, h: h, wake: make(chan struct{}, 1)}
}

func (f *fakeBackend) Open(opts viewer.WindowOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened++
	f.title = opts.Title
	if opts.Width > 0 && opts.Height > 0 {
		f.w, f.h = opts.Width, opts.Height
	}
	return nil
}

func (f *fakeBackend) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
}

func (f *fakeBackend) SetTextureSize(w, h int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w <= 0 || h <= 0 {
		return fmt.Errorf("invalid texture size %dx%d", w, h)
	}
	f.texW, f.texH = w, h
	return nil
}

func (f *fakeBackend) Upload(r viewer.Rect, pix []byte, stride int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Mirror the bounds check a real backend must do, so that a frame which
	// would read past the image fails here rather than in a driver.
	if stride > 0 && !r.Empty() {
		if last := (r.Y+r.H-1)*stride + (r.X+r.W)*4; last > len(pix) {
			return fmt.Errorf("upload %s exceeds %d bytes at stride %d", r, len(pix), stride)
		}
	}
	if r.W > f.texW || r.H > f.texH {
		return fmt.Errorf("upload %s exceeds the %dx%d texture", r, f.texW, f.texH)
	}
	f.uploads++
	return nil
}

func (f *fakeBackend) SetOverlaySize(int, int) error                { return nil }
func (f *fakeBackend) UploadOverlay(viewer.Rect, []byte, int) error { return nil }
func (f *fakeBackend) Present(viewer.Rect, viewer.Overlay) error    { f.count(); return nil }
func (f *fakeBackend) SetSize(w, h int) error                       { f.resize(w, h); return nil }
func (f *fakeBackend) SetTitle(title string) error                  { f.setTitle(title); return nil }
func (f *fakeBackend) SetFullscreen(on bool) error                  { f.setFullscreen(on); return nil }
func (f *fakeBackend) SetClipboard(text string) error               { f.setClipboard(text); return nil }
func (f *fakeBackend) Clipboard() (string, error)                   { return f.getClipboard(), nil }
func (f *fakeBackend) PollEvents(dst []viewer.Event) []viewer.Event { return f.drain(dst) }
func (f *fakeBackend) Size() (int, int)                             { return f.size() }
func (f *fakeBackend) Fullscreen() bool                             { return f.isFullscreen() }

func (f *fakeBackend) OpenAudio(format viewer.AudioFormat) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.audioErr != nil {
		return f.audioErr
	}
	f.audioOpens++
	f.audioFormat = format
	return nil
}

func (f *fakeBackend) PlayPCM(data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audioPlayed = append(f.audioPlayed, append([]byte(nil), data...))
}

func (f *fakeBackend) CloseAudio() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audioCloses++
}

// WaitEvents blocks until an event is queued, a wake arrives, or the timeout
// expires, then drains like PollEvents.
func (f *fakeBackend) WaitEvents(dst []viewer.Event, timeout time.Duration) []viewer.Event {
	if timeout > 0 && !f.queued() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-f.wake:
		case <-timer.C:
		}
	}
	return f.drain(dst)
}

func (f *fakeBackend) Wake() {
	f.mu.Lock()
	f.wakes++
	f.mu.Unlock()
	f.nudge()
}

// nudge fills the wake slot if it is empty, which is what both Wake and a
// pushed event do to a real event queue.
func (f *fakeBackend) nudge() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *fakeBackend) queued() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queue) > 0
}

// wakeCount reports how many times the loop has been nudged from off-thread.
func (f *fakeBackend) wakeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wakes
}

func (f *fakeBackend) count() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presents++
}

func (f *fakeBackend) resize(w, h int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.w, f.h = w, h
	f.sized = append(f.sized, [2]int{w, h})
}

func (f *fakeBackend) setTitle(title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.title = title
}

func (f *fakeBackend) setFullscreen(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fullscreen = on
}

func (f *fakeBackend) isFullscreen() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fullscreen
}

func (f *fakeBackend) setClipboard(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clipboard = text
}

func (f *fakeBackend) getClipboard() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clipboard
}

func (f *fakeBackend) size() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.w, f.h
}

func (f *fakeBackend) drain(dst []viewer.Event) []viewer.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls++
	dst = append(dst, f.queue...)
	f.queue = f.queue[:0]
	return dst
}

// pollCount reports how many times the event queue has been drained.
func (f *fakeBackend) pollCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.polls
}

// presentCount reports how many frames have been shown.
func (f *fakeBackend) presentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.presents
}

// send queues events for the next poll.
func (f *fakeBackend) send(events ...viewer.Event) {
	f.mu.Lock()
	f.queue = append(f.queue, events...)
	f.mu.Unlock()
	// A real backend's queue ends a blocking wait; this one has to say so.
	f.nudge()
}

var _ viewer.Backend = (*fakeBackend)(nil)
var _ viewer.AudioSink = (*fakeBackend)(nil)

// fakeAPI is an [API] that answers from fields instead of a network.
type fakeAPI struct {
	mu sync.Mutex

	base     string
	token    string
	insecure bool

	authConfig *kwclient.AuthConfig
	authErr    error
	native     *kwclient.NativeAuthConfig

	identity  *kwclient.Identity
	meErr     error
	meCalls   int
	loginTok  string
	loginErr  error
	loginMust bool

	browserLogin *kwclient.BrowserLogin
	browserErr   error
	// browserGate, when non-nil, blocks LoginBrowser until it is closed, so a
	// test can assert on the waiting screen.
	browserGate chan struct{}

	workspaces []kwclient.Workspace
	listErr    error
	listCalls  int
	images     []kwclient.Image

	// lifecycleErr makes StartWorkspace/StopWorkspace fail.
	lifecycleErr error
	// startCalls records each StartWorkspace target as ns/name, stopCalls the
	// StopWorkspace targets, so a test can assert on what was asked for.
	startCalls []string
	stopCalls  []string

	// grantPaths records the redirect each GrantBrowserSession is asked for.
	grantPaths []string
	grantErr   error
	// grantGate, when non-nil, blocks GrantBrowserSession until it is closed,
	// so a test can assert on the waiting screen.
	grantGate chan struct{}
}

func newFakeAPI(base string) *fakeAPI {
	return &fakeAPI{
		base: base,
		authConfig: &kwclient.AuthConfig{
			Enabled:   true,
			LocalAuth: kwclient.LocalAuthConfig{Enabled: true},
		},
		native:   &kwclient.NativeAuthConfig{},
		identity: &kwclient.Identity{Authenticated: true, AuthEnabled: true, Email: "user@example.com", Role: "editor"},
		loginTok: "fresh-token",
	}
}

func (f *fakeAPI) BaseURL() string { return f.base }

func (f *fakeAPI) Token() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token
}

func (f *fakeAPI) SetToken(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token = token
}

func (f *fakeAPI) AuthConfig(context.Context) (*kwclient.AuthConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.authErr != nil {
		return nil, f.authErr
	}
	return f.authConfig, nil
}

func (f *fakeAPI) NativeAuth(context.Context) (*kwclient.NativeAuthConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.native, nil
}

func (f *fakeAPI) LoginLocal(_ context.Context, email, password string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loginErr != nil {
		return "", false, f.loginErr
	}
	if email == "" || password == "" {
		return "", false, fmt.Errorf("fake: empty credentials")
	}
	return f.loginTok, f.loginMust, nil
}

func (f *fakeAPI) LoginBrowser(ctx context.Context, opts *kwclient.BrowserLoginOptions) (*kwclient.BrowserLogin, error) {
	f.mu.Lock()
	gate, login, err := f.browserGate, f.browserLogin, f.browserErr
	f.mu.Unlock()

	if opts != nil && opts.Notify != nil {
		opts.Notify(f.base+"/auth/login?native_redirect=http://127.0.0.1:1234/callback", nil)
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	if login == nil {
		login = &kwclient.BrowserLogin{Token: "browser-token", Email: "user@example.com"}
	}
	return login, nil
}

func (f *fakeAPI) Me(context.Context) (*kwclient.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.meCalls++
	if f.meErr != nil {
		return nil, f.meErr
	}
	return f.identity, nil
}

func (f *fakeAPI) ListWorkspaces(context.Context, string) ([]kwclient.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.workspaces, nil
}

func (f *fakeAPI) ListImages(context.Context) ([]kwclient.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.images, nil
}

// startStop sets the named workspace's stopped state on the fake's copy, which
// is what the real server does, so a refresh after the call shows the change.
func (f *fakeAPI) startStop(namespace, name string, stopped bool) (*kwclient.Workspace, error) {
	if f.lifecycleErr != nil {
		return nil, f.lifecycleErr
	}
	for i := range f.workspaces {
		if f.workspaces[i].Name == name && f.workspaces[i].Namespace == namespace {
			f.workspaces[i].Stopped = stopped
			ws := f.workspaces[i]
			return &ws, nil
		}
	}
	return nil, fmt.Errorf("fake: workspace %s/%s not found", namespace, name)
}

func (f *fakeAPI) StartWorkspace(_ context.Context, namespace, name string) (*kwclient.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls = append(f.startCalls, namespace+"/"+name)
	return f.startStop(namespace, name, false)
}

func (f *fakeAPI) StopWorkspace(_ context.Context, namespace, name string) (*kwclient.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls = append(f.stopCalls, namespace+"/"+name)
	return f.startStop(namespace, name, true)
}

func (f *fakeAPI) WorkspaceURL(ws kwclient.Workspace, img *kwclient.Image) string {
	path := ""
	if img != nil {
		path = img.DefaultPath
	}
	return f.base + "/proxy/" + ws.Namespace + "/" + ws.Name + "/" + path
}

func (f *fakeAPI) WorkspacePath(ws kwclient.Workspace, img *kwclient.Image) string {
	path := ""
	if img != nil {
		path = img.DefaultPath
	}
	return "/proxy/" + ws.Namespace + "/" + ws.Name + "/" + strings.TrimPrefix(path, "/")
}

func (f *fakeAPI) GrantBrowserSession(ctx context.Context, redirect string) (*kwclient.BrowserSessionGrant, error) {
	f.mu.Lock()
	f.grantPaths = append(f.grantPaths, redirect)
	gate, err := f.grantGate, f.grantErr
	f.mu.Unlock()

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return &kwclient.BrowserSessionGrant{
		Code: "grant-code",
		URL:  f.base + "/auth/browser-session?code=grant-code",
	}, nil
}

// set applies a mutation under the lock, for a test changing the fake's
// answers while the shell is running.
func (f *fakeAPI) set(fn func(*fakeAPI)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

var _ API = (*fakeAPI)(nil)

// memStore is an in-memory [Store].
type memStore struct {
	profile  *config.Profile
	token    string
	loadErr  error
	saveErr  error
	forgot   int
	savedTok []string
	settings config.Settings
	saves    int
}

func (m *memStore) Load() (*config.Profile, string, error) {
	if m.loadErr != nil {
		return nil, "", m.loadErr
	}
	return m.profile, m.token, nil
}

func (m *memStore) Save(profile *config.Profile, token string) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.profile = profile
	if token != "" {
		m.token = token
		m.savedTok = append(m.savedTok, token)
	}
	return nil
}

func (m *memStore) Forget(*config.Profile) error {
	m.forgot++
	m.token = ""
	return nil
}

func (m *memStore) LoadSettings() (config.Settings, error) {
	if m.loadErr != nil {
		return config.Settings{}, m.loadErr
	}
	return m.settings, nil
}

func (m *memStore) SaveSettings(settings config.Settings) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.settings = settings
	m.saves++
	return nil
}

var _ Store = (*memStore)(nil)

// --- test rig ---------------------------------------------------------------

// rig is an App wired to fakes, driven a step at a time on a controlled clock.
type rig struct {
	app     *App
	api     *fakeAPI
	be      *fakeBackend
	store   *memStore
	now     time.Time
	opened  []kwclient.Workspace
	connect func(context.Context, kwclient.Workspace) error
	browsed []string
	webbed  []kwclient.Workspace
}

func newRig(profile *config.Profile, token string) *rig {
	r := &rig{
		api:   newFakeAPI("https://kw.example.com"),
		be:    newFakeBackend(1280, 800),
		store: &memStore{profile: profile, token: token},
		now:   time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC),
	}
	app, err := New(Options{
		Backend: r.be,
		NewClient: func(server string, insecure bool) (API, Connector, error) {
			r.api.set(func(f *fakeAPI) {
				f.base = server
				f.insecure = insecure
			})
			return r.api, func(ctx context.Context, ws kwclient.Workspace) error {
				r.opened = append(r.opened, ws)
				if r.connect != nil {
					return r.connect(ctx, ws)
				}
				return nil
			}, nil
		},
		Store: r.store,
		OpenBrowser: func(rawURL string) error {
			r.browsed = append(r.browsed, rawURL)
			return nil
		},
		OpenWeb: func(profile, namespace, name string) error {
			r.webbed = append(r.webbed, kwclient.Workspace{Namespace: namespace, Name: name})
			return nil
		},
		// Off by default: the tests that want a refresh ask for one, and the
		// rest should not have their timings perturbed by a background poll.
		RefreshInterval: -1,
	})
	if err != nil {
		panic(err)
	}
	r.app = app
	return r
}

// start runs App.Start and settles the resulting background work.
func (r *rig) start() {
	r.app.Start(context.Background())
	r.settle()
}

// step advances the clock and runs one iteration.
func (r *rig) step() {
	r.now = r.now.Add(20 * time.Millisecond)
	if err := r.app.Step(context.Background(), r.now); err != nil {
		panic(err)
	}
}

// settle runs steps until nothing is in flight, so that a test can assert on
// the state a chain of network calls ends in rather than on an intermediate
// one. It gives up rather than spinning forever on a stuck operation.
func (r *rig) settle() {
	for i := 0; i < 200; i++ {
		r.step()
		if !r.app.m.Busy && len(r.app.results) == 0 && !r.app.refreshing && r.app.cancelPending == nil && r.app.inflight.Load() == 0 {
			// One more, so the frame that applies the last result is drawn.
			r.step()
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// press queues a key press and its release, the way a real backend delivers
// one, and runs a frame for each.
func (r *rig) press(k keysym.Key, mods keysym.Modifiers) {
	r.be.send(ui.EventKey{Key: k, Down: true, Mods: mods})
	r.step()
	r.be.send(ui.EventKey{Key: k, Down: false, Mods: mods})
	r.step()
}

// typeText sends a run of characters to whatever has the focus.
func (r *rig) typeText(s string) {
	for _, c := range s {
		r.be.send(ui.EventKey{Rune: c, Down: true})
		r.step()
	}
}

// focus places the keyboard focus directly, for tests that are about what a
// control does rather than about how it is reached.
func (r *rig) focus(id ui.FocusID) { r.app.ctx.Focus().Set(id) }

// clickFocused activates the focused control with Enter.
func (r *rig) clickFocused() { r.press(keysym.KeyReturn, keysym.ModNone) }

func workspace(ns, name string, kind kwclient.WorkspaceType, running bool) kwclient.Workspace {
	ws := kwclient.Workspace{Name: name, Namespace: ns, Type: kind, Image: "img/" + name}
	if running {
		ws.ReadyReplicas = 1
	} else {
		ws.Stopped = true
	}
	return ws
}
