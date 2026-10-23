// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package connection

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// DisplayMode is a guest-pixel size, independent of local presentation scaling.
type DisplayMode struct{ Width, Height int }

func (m DisplayMode) Valid() bool {
	return m.Width >= 320 && m.Height >= 200 && m.Width <= 8192 && m.Height <= 8192
}

func (m DisplayMode) String() string { return fmt.Sprintf("%d×%d", m.Width, m.Height) }

const ResolutionAuto Action = "resolution-auto"

func ResolutionAction(m DisplayMode) Action {
	return Action(fmt.Sprintf("resolution-%dx%d", m.Width, m.Height))
}

func ParseResolution(a Action) (DisplayMode, bool) {
	s, ok := strings.CutPrefix(string(a), "resolution-")
	if !ok {
		return DisplayMode{}, false
	}
	w, h, ok := strings.Cut(s, "x")
	if !ok {
		return DisplayMode{}, false
	}
	width, we := strconv.Atoi(w)
	height, he := strconv.Atoi(h)
	m := DisplayMode{width, height}
	return m, we == nil && he == nil && m.Valid()
}

type ResolutionChoice struct {
	DisplayMode
	Advertised bool
}

// ResolutionChoices mirrors frontend presets. Off-list modes are requests,
// never support claims: the guest's mode test remains authoritative.
func ResolutionChoices(modes [][2]int) []ResolutionChoice {
	presets := [][2]int{{800, 600}, {1024, 768}, {1280, 720}, {1280, 800}, {1280, 960}, {1280, 1024}, {1366, 768}, {1440, 900}, {1600, 900}, {1600, 1200}, {1680, 1050}, {1920, 1080}, {1920, 1200}, {2048, 1152}, {2560, 1440}, {2560, 1600}, {3440, 1440}, {3840, 2160}}
	choices := map[DisplayMode]bool{}
	for _, m := range presets {
		choices[DisplayMode{m[0], m[1]}] = false
	}
	for _, m := range modes {
		mode := DisplayMode{m[0], m[1]}
		if mode.Valid() {
			choices[mode] = true
		}
	}
	out := make([]ResolutionChoice, 0, len(choices))
	for m, advertised := range choices {
		out = append(out, ResolutionChoice{m, advertised})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Width*a.Height == b.Width*b.Height {
			return a.Width < b.Width
		}
		return a.Width*a.Height < b.Width*b.Height
	})
	return out
}

// ClosestMode uses normalized dimension distance, including slightly larger
// modes so window chrome does not force 1080p down to 720p. Ties prefer area.
func ClosestMode(modes [][2]int, width, height int) (DisplayMode, bool) {
	if width <= 0 || height <= 0 {
		return DisplayMode{}, false
	}
	best, distance := DisplayMode{}, math.Inf(1)
	for _, size := range modes {
		m := DisplayMode{size[0], size[1]}
		if !m.Valid() {
			continue
		}
		dx, dy := float64(m.Width-width)/float64(width), float64(m.Height-height)/float64(height)
		d := dx*dx + dy*dy
		if d < distance-1e-9 || math.Abs(d-distance) < 1e-9 && m.Width*m.Height > best.Width*best.Height {
			best, distance = m, d
		}
	}
	return best, best.Valid()
}
