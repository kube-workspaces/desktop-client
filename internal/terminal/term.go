// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package terminal

import (
	"context"
	"errors"
	"io"

	"github.com/kube-workspaces/desktop-client/internal/chrome"
	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/reconnect"
	"github.com/kube-workspaces/desktop-client/internal/ui"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// Options configures one terminal session. The zero value is usable.
type Options struct {
	// Backend renders the window; nil means a new [viewer.NewSDLBackend].
	Backend viewer.Backend

	// Theme supplies the default colours the uncoloured cell draws with and
	// the grid scale; nil means [ui.DefaultTheme]. The grid always renders in
	// the monospace bitmap face, whatever face the theme's widgets use.
	Theme *ui.Theme

	// Scale is the integer grid scale: one pixel of the 5x8 bitmap face per
	// Scale pixel of cell. Zero means the theme's Body scale. 1 gives 6x11 px
	// cells, 2 gives 12x22 px cells. A terminal often wants a smaller glyph
	// than the shell's body text, which is why it is no longer wedded to it.
	Scale int

	// Title is the window title. It is shown plain while connected and with
	// " — reconnecting" appended while the session is without a connection.
	Title   string
	Surface connection.Surface

	// QuitRune is the letter that ends the session when pressed with Ctrl+Alt;
	// zero means 'q'. Esc is deliberately left alone so it reaches the shell:
	// quitting on Esc would make vim unusable.
	QuitRune rune

	// CheckSeat reports whether a single-seat console is currently held by
	// somebody else. Nil means the bridge is multi-session (container /exec):
	// no check happens and the window dials straight away, exactly as before.
	// A check failure is fail-open — a status hiccup must not block the
	// console — so the window dials and lets the bridge answer instead.
	CheckSeat func(ctx context.Context) (inUse bool, err error)

	// TakeSeat evicts the current holder so the next dial can connect. It is
	// called only after explicit user consent: Enter on the busy plate. Nil
	// means no takeover is available and the window waits for the seat to
	// clear on its own.
	TakeSeat func(ctx context.Context) error

	// Logf receives non-fatal diagnostics. Nil drops them.
	Logf func(string, ...any)
}

// ErrInUse reports that a single-seat console is held by somebody else.
//
// A dial returns it (wrapping is fine; detected with errors.Is) when the
// bridge answers 409. The window answers with the busy plate and waits for
// explicit takeover consent instead of retrying behind the holder's back,
// mirroring the web console's consent flow. A dial that never evicts needs
// no consent to retry once the seat is free.
var ErrInUse = errors.New("console in use")

// Dial opens one session transport: a ReadWriteCloser whose reads are the
// shell's output and whose writes are the shell's input. cols and rows are
// the grid the dialed session should start at.
//
// One transport is a single connection. [Run] calls Dial again after a
// transport failure, with the cols and rows the window has since grown to.
type Dial func(ctx context.Context, cols, rows uint16) (io.ReadWriteCloser, error)

// Run opens the integrated-terminal window and drives it until the session
// ends.
//
// A nil return means the session ended normally: the shell exited (a clean
// close from the /exec bridge), the user quit (closing the window, or
// pressing Ctrl+Alt+Q), or ctx was cancelled. Run only returns an error when
// the window itself cannot be created, or when dial is nil. Connection and
// transport failures are handled inside: the loop retries Dial with
// [reconnect.Default] pacing until one succeeds or the user quits.
//
// Run must be called from the goroutine that will own the window; the
// backend's event loop is that goroutine. See [viewer.Backend].
func Run(ctx context.Context, dial Dial, opts Options) error {
	w, err := buildWindow(dial, opts)
	if err != nil {
		return err
	}
	return w.run(ctx)
}

// buildWindow validates the dial and assembles the window without opening
// it, shared by [Run] and [OpenDetached] so the two paths cannot drift.
func buildWindow(dial Dial, opts Options) (*window, error) {
	if dial == nil {
		return nil, errors.New("terminal: nil dial")
	}
	be := opts.Backend
	if be == nil {
		be = chrome.New(viewer.NewSDLBackend())
	}
	theme := opts.Theme
	if theme == nil {
		theme = ui.DefaultTheme()
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	scale := opts.Scale
	if scale < 1 {
		scale = theme.Body
	}
	if scale < 1 {
		scale = 1
	}

	emu := NewEmulator(defaultCols, defaultRows)
	pal := defaultPalette()
	pal.DefaultFg, pal.DefaultBg = theme.Text, theme.Background
	emu.SetPalette(pal)

	in := newInput()
	if opts.QuitRune != 0 {
		in.quitRune = opts.QuitRune
	}

	return &window{
		surface:   opts.Surface,
		be:        be,
		emu:       emu,
		ren:       newRenderer(defaultCols, defaultRows, scale),
		in:        in,
		dial:      dial,
		checkSeat: opts.CheckSeat,
		takeSeat:  opts.TakeSeat,
		backoff:   reconnect.Default(),
		logf:      logf,
		title:     opts.Title,
		scale:     scale,
		cellW:     ui.GlyphAdvance * scale,
		cellH:     ui.LineAdvance * scale,
		cols:      defaultCols,
		rows:      defaultRows,
	}, nil
}
