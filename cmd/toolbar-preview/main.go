// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// toolbar-preview renders the shared connection-chrome prototype without SDL,
// a live server or codec libraries. It is a visual review tool, not a session.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"

	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

func main() {
	output := flag.String("output", "toolbar-preview.png", "output PNG path (parent directory must exist)")
	flag.Parse()
	if err := render(*output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func render(path string) error {
	const width, height = 1280, 560
	cases := []struct {
		name string
		mode ui.Mode
		s    connection.Snapshot
		menu string
	}{
		{"VM desktop / windowed / dark", ui.ModeDark, desktop(false, connection.Controller, connection.Connected), "tools"},
		{"VM desktop / fullscreen / light / view-only", ui.ModeLight, desktop(true, connection.Observer, connection.Connected), "tools"},
		{"VM desktop / reconnecting / high contrast", ui.ModeHighContrast, desktop(true, connection.Controller, connection.Reconnecting), "tools"},
		{"VM SSH / terminal / dark", ui.ModeDark, connection.Snapshot{Workspace: "team/linux", Surface: connection.SSH, State: connection.Connected, Transport: "SSH", Capabilities: connection.Capabilities{Shell: true, Paste: true}}, "tools"},
		{"Container web / light", ui.ModeLight, connection.Snapshot{Workspace: "team/code", Surface: connection.Web, State: connection.Ready, Transport: "Webview", Capabilities: connection.Capabilities{Shell: true}}, "connection"},
		{"Scratch console / narrow / high contrast", ui.ModeHighContrast, connection.Snapshot{Workspace: "team/scratch", Surface: connection.Console, State: connection.Reconnecting, Transport: "Exec", Capabilities: connection.Capabilities{Shell: true, Paste: true}}, "tools"},
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height*len(cases)))
	canvas := ui.NewCanvas(img)
	for n, c := range cases {
		th := ui.ThemeFor(ui.StyleClean, c.mode)
		ctx := ui.NewContext(th)
		ctx.Begin(canvas, ui.Input{})
		bounds := ui.Rect{Y: n * height, W: width, H: height}
		canvas.Fill(bounds, th.Background)
		ui.Label(ctx, ui.Rect{X: th.Pad, Y: bounds.Y + th.Pad, W: width - 2*th.Pad, H: th.ControlHeight}, c.name+" — PROTOTYPE", ui.LabelStyle{})
		bounds.Y += 60
		bounds.H -= 60
		if n == len(cases)-1 {
			bounds.W = 500
		}
		bar := ui.ConnectionToolbar{Menu: c.menu}
		_, _ = bar.Layout(ctx, bounds, c.s)
		ctx.End()
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func desktop(fullscreen bool, role connection.Role, state connection.State) connection.Snapshot {
	return connection.Snapshot{Workspace: "team/linux", Surface: connection.Desktop, State: state, Role: role,
		Transport: "RFB", Fullscreen: fullscreen, Clipboard: true, ResizeGuest: true,
		Capabilities: connection.Capabilities{Shell: true, SpecialKeys: true, ClipboardSync: true, GuestResize: true, Audio: true, SharedControl: true}}
}
