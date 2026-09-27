package main

import (
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

func TestUIScaleFlagValidation(t *testing.T) {
	for _, tc := range []struct {
		in      float64
		wantErr bool
	}{
		{0, false},
		{1, false},
		{1.25, false},
		{2, false},
		{3, false},
		{-1, true},
		{0.5, true},
		{3.5, true},
	} {
		err := checkUIScale(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("checkUIScale(%v) err = %v, wantErr %v", tc.in, err, tc.wantErr)
		}
	}
}

// TestSeriesRow pins the probe's per-second row: rates over the tick, raw
// rect counts, and decode share — the columns a sustained full-motion run
// is read from.
func TestSeriesRow(t *testing.T) {
	prev := rfb.Stats{}
	cur := rfb.Stats{
		Updates:    30,
		Rects:      120,
		BytesRead:  1_048_576,
		DecodeTime: 150 * time.Millisecond,
	}
	row := seriesRow(time.Second, prev, cur)
	for _, want := range []string{"t+1s", "30.0/s", "120", "1.0 MiB/s", "15.0%"} {
		if !strings.Contains(row, want) {
			t.Errorf("series row %q does not contain %q", row, want)
		}
	}

	// A stalled tick rates nothing and divides by nothing.
	if row := seriesRow(0, cur, cur); !strings.Contains(row, "0.0/s") {
		t.Errorf("stalled series row %q has no zero rate", row)
	}
}

func TestAdaptiveQualityFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{nil, true},
		{[]string{"--quality=8"}, false},
		{[]string{"--quality=-1"}, false},
		{[]string{"--compress=3"}, false},
		{[]string{"--adaptive-quality=false"}, false},
		{[]string{"--quality=3", "--adaptive-quality=true"}, true},
		{[]string{"--adaptive-quality=true", "--quality=3"}, true},
	} {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.Int("quality", 8, "")
		fs.Int("compress", -1, "")
		enabled := fs.Bool("adaptive-quality", true, "")
		if err := fs.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		if got := adaptiveQualityEnabled(fs, *enabled); got != tc.want {
			t.Errorf("%v: adaptive = %v, want %v", tc.args, got, tc.want)
		}
	}
}
