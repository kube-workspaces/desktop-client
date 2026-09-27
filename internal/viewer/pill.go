// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package viewer

import "fmt"

// PillLayer owns the rasterisation and upload of the session's quality pill:
// one compact line — frames per second and link bitrate — drawn top-right
// over the live guest image.
//
// It is the first resident of the future session tool palette: the strip of
// session-scoped affordances (fullscreen, clipboard, disconnect) the window
// will grow. The pill is deliberately display-only — it reports, it never
// takes input — so the palette's interactive half stays a separate change.
//
// The pill shares the overlay texture with the status plate ([StatusLayer])
// instead of growing a second one: the two never show at once (the plate
// means the session is not live, and the pill only shows while it is), and
// the viewer resets the idle layer whenever the other draws, so neither can
// present the other's pixels.
type PillLayer struct {
	key      overlayKey
	w, h     int
	margin   int
	scale    int
	placedAt Rect
}

// Reset forgets the cached raster, so the next Build re-uploads. The viewer
// calls it whenever the status plate draws, because the plate owns the
// shared texture while it is up.
func (l *PillLayer) Reset() { l.key = overlayKey{} }

// Build renders line into the overlay texture of be for a winW by winH
// surface and returns how the pill should be drawn this frame: no dim, a
// small rectangle top-right. An empty line clears the pill and draws
// nothing, which is what a session without stats yet (or without a
// connection) asks for.
func (l *PillLayer) Build(be Backend, line string, winW, winH int) (Overlay, error) {
	if line == "" || winW <= 0 || winH <= 0 {
		l.Reset()
		return Overlay{}, nil
	}

	key := overlayKey{text: line, w: winW, h: winH}
	if key != l.key {
		img, margin, scale := renderPill(line, winW)
		if img.w <= 0 || img.h <= 0 {
			l.Reset()
			return Overlay{}, nil
		}
		if img.w != l.w || img.h != l.h {
			if err := be.SetOverlaySize(img.w, img.h); err != nil {
				return Overlay{}, fmt.Errorf("allocate pill texture: %w", err)
			}
			l.w, l.h = img.w, img.h
		}
		if err := be.UploadOverlay(Rect{W: img.w, H: img.h}, img.pix, img.stride); err != nil {
			return Overlay{}, fmt.Errorf("upload pill: %w", err)
		}
		l.key, l.margin, l.scale = key, margin, scale
		l.placedAt = Rect{X: winW - l.w - margin, Y: margin, W: l.w, H: l.h}
	}
	return Overlay{Rect: l.placedAt}, nil
}

// pillMaxScale caps the pill glyphs below the status plate's ceiling: the
// pill is a readout, not a headline, and must stay out of the guest's way.
const pillMaxScale = 3

// renderPill rasterises one line into a small panel, returning the image
// and the margin/scale it was laid out with. The glyphs stay small on
// purpose — scale follows the window width coarsely (1 below 960 px, 2 to
// 1440, 3 above) rather than filling a fraction of it the way the centred
// plate does — and the panel hugs the corner with a margin in the same
// units, so the pill keeps its proportions across window sizes.
func renderPill(line string, winW int) (img overlayImage, margin, scale int) {
	line = foldToFont(line)
	if line == "" || winW <= 0 {
		return overlayImage{}, 0, 0
	}
	switch {
	case winW >= 1440:
		scale = 3
	case winW >= 960:
		scale = 2
	default:
		scale = 1
	}
	if scale > pillMaxScale {
		scale = pillMaxScale
	}

	n := len([]rune(line))
	textW := n*glyphAdvance - (glyphAdvance - glyphWidth)
	textH := glyphHeight
	padX, padY := glyphWidth*scale, glyphHeight*scale/2
	w := textW*scale + 2*padX
	h := textH*scale + 2*padY
	if w > winW {
		w = winW
	}

	img = overlayImage{w: w, h: h, stride: w * 4, scale: scale}
	img.pix = make([]byte, img.stride*h)
	img.fill(Rect{W: w, H: h}, 0, 0, 0, overlayPanelAlpha)
	img.border(max(1, scale/2))
	img.text(line, (w-textW*scale)/2, (h-textH*scale)/2, scale)
	margin = 2 * glyphAdvance * scale
	return img, margin, scale
}
