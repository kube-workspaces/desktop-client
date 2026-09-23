package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/cmdutil"
	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/shell"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// shellCommand exposes the graphical shell as a subcommand, so that it can be
// asked for explicitly and shows up in `--help` next to everything else. It is
// also what running the binary with no arguments does; see main().
func shellCommand() command {
	return command{
		name:    "shell",
		summary: "Open the graphical workspace browser (the default)",
		run:     runShell,
	}
}

// runShell opens the desktop application.
//
// This is the whole client: the profile, login and workspace screens, and the
// display sessions they open. The shell and the sessions run in the same
// process on the same OS thread, but each opens its own window.
func runShell(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("shell", flag.ExitOnError)
	profileName := fs.String("profile", "", "profile to open (defaults to the active profile)")
	quality := fs.Int("quality", 8, "JPEG quality level 0-9 to request in a session (-1 to omit)")
	compress := fs.Int("compress", -1, "zlib compression level 0-9 to request in a session (-1 to omit)")
	adaptive := fs.Bool("adaptive-quality", true, "adapt RFB quality automatically (explicit --quality/--compress selects fixed mode)")
	scaleQuality := fs.String("scale-quality", "linear", "session scaling filter: nearest, linear or pixelart")
	interval := fs.Duration("interval", 16*time.Millisecond, "session framebuffer update request interval")
	refresh := fs.Duration("refresh", shell.DefaultRefreshInterval, "how often to refresh the workspace list (0 to disable)")
	width := fs.Int("width", 0, "initial window width in pixels (0 restores the last size)")
	height := fs.Int("height", 0, "initial window height in pixels (0 restores the last size)")
	uiScale := fs.Float64("ui-scale", 0, "interface scale factor 1-3 (0 follows the display)")
	verbose := fs.Bool("v", false, "log diagnostics to stderr")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: kube-workspaces shell [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Running kube-workspaces with no arguments does the same thing.\n\n")
		fs.PrintDefaults()
	}
	if err := cmdutil.ParseFlags(fs, args); err != nil {
		return err
	}

	scale, err := viewer.ParseScaleQuality(*scaleQuality)
	if err != nil {
		return err
	}
	// The flag reads better as "0 disables"; the package spells that -1, so
	// that a zero Options value can still mean "use the default".
	if *refresh == 0 {
		*refresh = -1
	}
	if err := checkUIScale(*uiScale); err != nil {
		return err
	}

	// An unset size restores the last arrangement: the shell records its
	// window on resize and on exit, so a relaunch opens where the user left
	// it. An explicit flag always wins over the stored size.
	if *width <= 0 || *height <= 0 {
		store := profileStore{name: *profileName}
		if s, err := store.LoadSettings(); err == nil {
			if *width <= 0 && s.WindowWidth > 0 {
				*width = s.WindowWidth
			}
			if *height <= 0 && s.WindowHeight > 0 {
				*height = s.WindowHeight
			}
		}
		if *width <= 0 {
			*width = shell.DefaultWidth
		}
		if *height <= 0 {
			*height = shell.DefaultHeight
		}
	}

	var logf func(string, ...any)
	if *verbose {
		logf = func(format string, a ...any) {
			fmt.Fprintf(os.Stderr, "shell: "+format+"\n", a...)
		}
	}

	sessionOpts := shell.SessionOptions{
		FixedQuality:   !adaptiveQualityEnabled(fs, *adaptive),
		Quality:        *quality,
		Compress:       *compress,
		UpdateInterval: *interval,
		ScaleQuality:   scale,
		Logf:           logf,
	}

	// The backend is created once and outlives every session: it is the
	// window, and the window is the thing the user thinks of as the
	// application.
	backend := viewer.NewSDLBackend()

	app, err := shell.New(shell.Options{
		Backend: backend,
		// The factory returns the API client and the connector together, so
		// that the connector can close over the concrete *kwclient.Client the
		// session bridge needs without the shell having to know it exists.
		NewClient: func(server string, insecure bool) (shell.API, shell.SessionDialer, error) {
			opts := []kwclient.Option{kwclient.WithUserAgent("kube-workspaces-desktop/" + version)}
			if insecure {
				opts = append(opts, kwclient.WithInsecureSkipVerify(true))
			}
			client, err := kwclient.New(server, opts...)
			if err != nil {
				return nil, nil, err
			}
			return client, shell.NewSessionDialer(client, sessionOpts), nil
		},
		Store:           profileStore{name: *profileName},
		RefreshInterval: *refresh,
		Width:           *width,
		Height:          *height,
		UIScale:         *uiScale,
		Title:           windowTitle,
		Logf:            logf,
	})
	if err != nil {
		return err
	}

	// SDL must be driven from the thread that initialised the video
	// subsystem, and on macOS that must be the process's first thread. main()
	// calls this directly, so this is the main goroutine; locking it pins the
	// shell's loop — and every session it opens — to the main OS thread. The
	// lock is never released: the process exits when the window closes.
	runtime.LockOSThread()

	return app.Run(ctx)
}

// windowTitle is the application's name as the window manager shows it.
const windowTitle = "Kube Workspaces"

// checkUIScale validates a --ui-scale value: 0 follows the display, 1-3
// pins the interface to a factor. It is a separate function so the rule can
// be tested without running the shell.
func checkUIScale(f float64) error {
	if f == 0 || (f >= 1 && f <= 3) {
		return nil
	}
	return fmt.Errorf("invalid --ui-scale %v (want 0 for automatic, or 1-3)", f)
}

// profileStore is the shell's [shell.Store], with the --profile flag applied.
//
// It exists only to honour that flag: with no flag it is exactly
// [shell.ConfigStore], and with one it pins the shell to a named profile the
// way every other subcommand's --profile does.
type profileStore struct {
	name string
}

func (p profileStore) Load() (*config.Profile, string, error) {
	if p.name == "" {
		return shell.ConfigStore{}.Load()
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, "", err
	}
	profile := cfg.Get(p.name)
	if profile == nil {
		return nil, "", fmt.Errorf("no such profile %q", p.name)
	}
	token, err := config.LoadToken(profile.Name)
	if err != nil {
		// A profile with no usable token is the normal "signed out" state;
		// the shell will ask for credentials.
		return profile, "", nil
	}
	return profile, token, nil
}

func (p profileStore) Save(profile *config.Profile, token string) error {
	return shell.ConfigStore{}.Save(profile, token)
}

func (p profileStore) Forget(profile *config.Profile) error {
	return shell.ConfigStore{}.Forget(profile)
}

// ListProfiles honours the pin: a pinned shell sees exactly the pinned
// profile, so --profile keeps meaning "this one instance". Unpinned it is the
// full list for the in-shell switcher.
func (p profileStore) ListProfiles() ([]*config.Profile, error) {
	if p.name == "" {
		return shell.ConfigStore{}.ListProfiles()
	}
	profile, _, err := p.Load()
	if err != nil || profile == nil {
		return nil, err
	}
	return []*config.Profile{profile}, nil
}

// TokenFor reads a profile's token without changing the active profile.
func (p profileStore) TokenFor(profile *config.Profile) (string, error) {
	return shell.ConfigStore{}.TokenFor(profile)
}

func (p profileStore) LoadSettings() (config.Settings, error) {
	return shell.ConfigStore{}.LoadSettings()
}

func (p profileStore) SaveSettings(settings config.Settings) error {
	return shell.ConfigStore{}.SaveSettings(settings)
}
