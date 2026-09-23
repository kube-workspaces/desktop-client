package main

import (
	"flag"
	"testing"
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
