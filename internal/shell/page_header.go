// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import "github.com/kube-workspaces/desktop-client/internal/ui"

// drawPageHeader aligns the close control with the page content's right edge,
// level with the heading.
func (a *App) drawPageHeader(heading ui.Rect, title string, closeID ui.FocusID) bool {
	th := a.opts.Theme
	size := min(th.ControlHeight, heading.W, heading.H)
	closeRect := ui.Rect{X: heading.X + heading.W - size, Y: heading.Y + (heading.H-size)/2, W: size, H: size}
	heading.W = max(0, min(heading.W, closeRect.X-th.Gap-heading.X))
	ui.Label(a.ctx, heading, title, ui.LabelStyle{Scale: th.Title, Middle: true})
	return (ui.CloseButton{ID: closeID}).Layout(a.ctx, closeRect)
}
