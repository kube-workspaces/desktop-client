// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/config"
)

func TestFitGeometry(t *testing.T) {
	monitors := []config.WindowRect{
		{X: -1600, Y: 20, Width: 1600, Height: 880},
		{X: 0, Y: 20, Width: 1920, Height: 1060},
	}
	for _, tc := range []struct {
		name        string
		saved, want config.WindowRect
	}{
		{"negative origin", config.WindowRect{X: -1500, Y: 40, Width: 900, Height: 700}, config.WindowRect{X: -1500, Y: 40, Width: 900, Height: 700}},
		{"shrunken display", config.WindowRect{X: -1800, Y: -100, Width: 1900, Height: 1200}, monitors[0]},
		{"removed monitor", config.WindowRect{X: 4000, Y: 100, Width: 900, Height: 700}, config.WindowRect{X: 510, Y: 200, Width: 900, Height: 700}},
		{"keep title reachable", config.WindowRect{X: 1600, Y: -30, Width: 600, Height: 400}, config.WindowRect{X: 1320, Y: 20, Width: 600, Height: 400}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := fitGeometry(tc.saved, monitors, 1)
			if !ok || got != tc.want {
				t.Fatalf("fit = %+v, %v; want %+v", got, ok, tc.want)
			}
		})
	}
	if _, ok := fitGeometry(config.WindowRect{Width: 1, Height: 1}, nil, 0); ok {
		t.Fatal("restored with no displays")
	}
}

func TestGeometryRecorderRetainsNormalAndFlushesLastMove(t *testing.T) {
	var writes []config.WindowRect
	g := geometryRecorder{opts: GeometryOptions{Save: func(r config.WindowRect) error { writes = append(writes, r); return nil }}}
	now := time.Now()
	first := config.WindowRect{X: -1000, Width: 900, Height: 700}
	last := config.WindowRect{X: -900, Y: 30, Width: 1000, Height: 750}
	g.observe(first, now)
	g.observe(last, now.Add(time.Second))
	g.observe(config.WindowRect{}, now.Add(2*time.Second))
	if len(writes) != 1 {
		t.Fatalf("rate-limited writes = %d, want 1", len(writes))
	}
	g.flush() // Closing fullscreen/minimized uses the last normal observation.
	g.flush()
	if len(writes) != 2 || writes[1] != last {
		t.Fatalf("close writes = %+v, want first and last normal rectangles", writes)
	}
}
