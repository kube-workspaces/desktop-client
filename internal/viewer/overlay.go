// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import "strings"

// Status is what the viewer tells the user about its connection.
//
// It exists because the viewer now outlives any single connection: the window
// stays up while the session supervisor reconnects underneath it, and the only
// honest way to show that is a modal layer over the last frame the guest sent.
// The values mirror the states a supervised session goes through, but the type
// is the viewer's own so that this package does not depend on the session
// package (and so that a different supervisor can drive the same window).
type Status int

const (
	// StatusLive means a connection is delivering frames. No overlay is drawn.
	StatusLive Status = iota
	// StatusConnecting is the first connection attempt, before any frame has
	// arrived.
	StatusConnecting
	// StatusReconnecting means the connection dropped and another attempt is
	// pending. The last frame stays on screen, dimmed.
	StatusReconnecting
	// StatusDisplayInUse means another client holds the workspace's single
	// display slot. There is no takeover endpoint, so the only options are to
	// wait or to close the window; the overlay says so.
	StatusDisplayInUse
	// StatusFailed is terminal: the reason is shown and the viewer exits
	// shortly afterwards.
	StatusFailed
)

// String implements fmt.Stringer.
func (s Status) String() string {
	switch s {
	case StatusLive:
		return "live"
	case StatusConnecting:
		return "connecting"
	case StatusReconnecting:
		return "reconnecting"
	case StatusDisplayInUse:
		return "display in use"
	case StatusFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Text returns the headline the overlay shows for this status.
func (s Status) Text() string {
	switch s {
	case StatusLive:
		return ""
	case StatusConnecting:
		return "Connecting…"
	case StatusReconnecting:
		return "Reconnecting…"
	case StatusDisplayInUse:
		return "Display in use by another session"
	case StatusFailed:
		return "Reconnecting failed"
	default:
		return "Unknown state"
	}
}

// maxDetailRunes bounds the reason text the overlay shows.
//
// The detail comes from a server error string or a transport failure and can
// be arbitrarily long. A long line would shrink the whole overlay to stay
// inside the window, so the headline would become unreadable in order to show
// the tail of a stack of wrapped errors nobody reads from a dialog.
const maxDetailRunes = 56

// statusLines renders a status and its cause into the lines of the overlay.
func statusLines(s Status, detail string) []string {
	if s == StatusLive {
		return nil
	}
	detail = shortDetail(detail)
	switch s {
	case StatusFailed:
		if detail == "" {
			return []string{s.Text()}
		}
		return []string{s.Text() + " — " + detail}

	case StatusDisplayInUse:
		lines := []string{s.Text(), "Waiting for it to be released"}
		if detail != "" {
			lines = append(lines, detail)
		}
		return lines

	default:
		if detail == "" {
			return []string{s.Text()}
		}
		return []string{s.Text(), detail}
	}
}

// shortDetail flattens a cause into one line of bounded length.
func shortDetail(detail string) string {
	detail = strings.TrimSpace(strings.Join(strings.Fields(detail), " "))
	if detail == "" {
		return ""
	}
	runes := []rune(detail)
	if len(runes) > maxDetailRunes {
		return string(runes[:maxDetailRunes-1]) + "…"
	}
	return detail
}

// Overlay colours and geometry.
const (
	// overlayDim is the alpha of the black rectangle drawn over the whole
	// window while a status is showing. It is deliberately partial: the point
	// is that the user still sees their desktop, frozen, underneath a state
	// that is obviously the client's and not the guest's.
	overlayDim = 150

	// overlayPanelAlpha is the alpha of the panel behind the text. The dim
	// alone is not enough contrast for white text over a white document.
	overlayPanelAlpha = 210

	// overlayBorderAlpha outlines the panel so it reads as a plate rather
	// than as a smudge on the guest's screen.
	overlayBorderAlpha = 70

	// overlayWidthNumer/overlayWidthDenom is the fraction of the window width
	// the text aims to fill. Any smaller and a 4K window renders a postage
	// stamp; any larger and there is no margin.
	overlayWidthNumer = 3
	overlayWidthDenom = 5

	// overlayHeightDenom bounds the panel to this fraction of the window
	// height, so a long message cannot cover the whole frozen frame.
	overlayHeightDenom = 3

	// overlayMaxScale caps the glyph scale. Past this the pixels of a 5x7
	// font are the only thing on screen.
	overlayMaxScale = 10
)

// overlayKey identifies a rasterised overlay, so that the image is rebuilt
// only when the text or the window size actually changes rather than on every
// frame of a reconnect that may last minutes.
type overlayKey struct {
	text string
	w, h int
}

// overlayImage is a rasterised overlay panel in RGBA, ready to upload.
type overlayImage struct {
	pix    []byte
	w, h   int
	stride int
	scale  int
}

// renderOverlay rasterises lines into a panel sized for a winW by winH window.
//
// The glyphs are scaled by an integer factor chosen from the window size
// rather than being drawn once and stretched by the renderer: a 5x7 font
// stretched by a filter turns to mush, and an integer scale keeps every glyph
// pixel square and crisp at any window size. The cost is re-rasterising on
// resize, which is bounded by [overlayKey].
func renderOverlay(lines []string, winW, winH int) overlayImage {
	folded := make([]string, 0, len(lines))
	widest := 0
	for _, line := range lines {
		line = foldToFont(line)
		folded = append(folded, line)
		if n := len([]rune(line)); n > widest {
			widest = n
		}
	}
	if len(folded) == 0 || widest == 0 || winW <= 0 || winH <= 0 {
		return overlayImage{}
	}

	// Text extent in unscaled glyph pixels; the trailing inter-glyph gap and
	// the trailing line gap are not part of the block.
	textW := widest*glyphAdvance - (glyphAdvance - glyphWidth)
	textH := len(folded)*lineAdvance - (lineAdvance - glyphHeight)

	scale := winW * overlayWidthNumer / overlayWidthDenom / textW
	if maxByHeight := winH / overlayHeightDenom / textH; scale > maxByHeight {
		scale = maxByHeight
	}
	scale = clamp(scale, 1, overlayMaxScale)

	padX, padY := 3*glyphWidth*scale/2, glyphHeight*scale/2
	w := textW*scale + 2*padX
	h := textH*scale + 2*padY
	// A window narrower than the panel would place it partly off-screen; the
	// panel is clipped to the window instead, which is ugly but visible.
	if w > winW {
		w = winW
	}
	if h > winH {
		h = winH
	}

	img := overlayImage{w: w, h: h, stride: w * 4, scale: scale}
	img.pix = make([]byte, img.stride*h)
	img.fill(Rect{W: w, H: h}, 0, 0, 0, overlayPanelAlpha)
	img.border(max(1, scale/2))

	y := (h - textH*scale) / 2
	for _, line := range folded {
		lineW := len([]rune(line))*glyphAdvance - (glyphAdvance - glyphWidth)
		img.text(line, (w-lineW*scale)/2, y, scale)
		y += lineAdvance * scale
	}
	return img
}

// fill paints a solid RGBA rectangle, clipped to the image.
func (img *overlayImage) fill(r Rect, cr, cg, cb, ca byte) {
	x0, y0 := max(r.X, 0), max(r.Y, 0)
	x1, y1 := min(r.X+r.W, img.w), min(r.Y+r.H, img.h)
	for y := y0; y < y1; y++ {
		row := y * img.stride
		for x := x0; x < x1; x++ {
			p := row + x*4
			img.pix[p], img.pix[p+1], img.pix[p+2], img.pix[p+3] = cr, cg, cb, ca
		}
	}
}

// border outlines the panel with a thin translucent white frame.
func (img *overlayImage) border(t int) {
	img.fill(Rect{W: img.w, H: t}, 255, 255, 255, overlayBorderAlpha)
	img.fill(Rect{Y: img.h - t, W: img.w, H: t}, 255, 255, 255, overlayBorderAlpha)
	img.fill(Rect{W: t, H: img.h}, 255, 255, 255, overlayBorderAlpha)
	img.fill(Rect{X: img.w - t, W: t, H: img.h}, 255, 255, 255, overlayBorderAlpha)
}

// text draws s at (x, y) with each glyph pixel expanded to scale by scale.
func (img *overlayImage) text(s string, x, y, scale int) {
	for _, r := range s {
		g := glyphFor(r)
		for row := 0; row < glyphHeight; row++ {
			bits := g[row]
			for col := 0; col < glyphWidth; col++ {
				if bits&(1<<(glyphWidth-1-col)) == 0 {
					continue
				}
				img.fill(Rect{
					X: x + col*scale,
					Y: y + row*scale,
					W: scale,
					H: scale,
				}, 255, 255, 255, 255)
			}
		}
		x += glyphAdvance * scale
	}
}
