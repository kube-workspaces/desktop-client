package main

import (
	"flag"
	"testing"
)

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
