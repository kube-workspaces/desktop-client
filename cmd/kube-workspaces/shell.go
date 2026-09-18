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
	width := fs.Int("width", shell.DefaultWidth, "initial window width")
	height := fs.Int("height", shell.DefaultHeight, "initial window height")
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
		NewClient: func(server string, insecure bool) (shell.API, shell.Connector, error) {
			opts := []kwclient.Option{kwclient.WithUserAgent("kube-workspaces-desktop/" + version)}
			if insecure {
				opts = append(opts, kwclient.WithInsecureSkipVerify(true))
			}
			client, err := kwclient.New(server, opts...)
			if err != nil {
				return nil, nil, err
			}
			return client, shell.SessionConnector(client, sessionOpts), nil
		},
		Store:           profileStore{name: *profileName},
		RefreshInterval: *refresh,
		Width:           *width,
		Height:          *height,
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

func (p profileStore) LoadSettings() (config.Settings, error) {
	return shell.ConfigStore{}.LoadSettings()
}

func (p profileStore) SaveSettings(settings config.Settings) error {
	return shell.ConfigStore{}.SaveSettings(settings)
}
