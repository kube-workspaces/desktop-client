// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"time"

	"github.com/kube-workspaces/desktop-client/internal/config"
)

// GeometryOptions keeps persistence in the authenticated command lifecycle,
// outside page JavaScript and the browser-session grant URL.
type GeometryOptions struct {
	Restore config.WindowRect
	Save    func(config.WindowRect) error
}

type geometryRecorder struct {
	opts        GeometryOptions
	last, saved config.WindowRect
	written     time.Time
}

func (g *geometryRecorder) observe(r config.WindowRect, now time.Time) {
	if !r.Valid() {
		return
	}
	g.last = r
	if g.written.IsZero() || now.Sub(g.written) >= 5*time.Second {
		g.flush()
		g.written = now
	}
}

func (g *geometryRecorder) flush() {
	if g.opts.Save != nil && g.last.Valid() && g.last != g.saved {
		if g.opts.Save(g.last) == nil {
			g.saved = g.last
		}
	}
}

// fitGeometry uses each native toolkit's own coordinate space throughout. A
// saved centre on a removed display falls back to the launch window's monitor.
func fitGeometry(r config.WindowRect, monitors []config.WindowRect, fallback int) (config.WindowRect, bool) {
	if !r.Valid() || len(monitors) == 0 {
		return config.WindowRect{}, false
	}
	i := fallback
	if i < 0 || i >= len(monitors) {
		i = 0
	}
	cx, cy := r.X+r.Width/2, r.Y+r.Height/2
	found := false
	for j, m := range monitors {
		if m.Valid() && cx >= m.X && cx < m.X+m.Width && cy >= m.Y && cy < m.Y+m.Height {
			i, found = j, true
			break
		}
	}
	m := monitors[i]
	if !m.Valid() {
		return config.WindowRect{}, false
	}
	r.Width, r.Height = min(r.Width, m.Width), min(r.Height, m.Height)
	if !found {
		r.X, r.Y = m.X+(m.Width-r.Width)/2, m.Y+(m.Height-r.Height)/2
	}
	r.X = max(m.X, min(r.X, m.X+m.Width-r.Width))
	r.Y = max(m.Y, min(r.Y, m.Y+m.Height-r.Height))
	return r, true
}
