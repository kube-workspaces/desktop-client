// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"testing"
)

func TestParseAcceptsStrictTags(t *testing.T) {
	v, err := Parse("v0.1.1")
	if err != nil {
		t.Fatalf("Parse(v0.1.1): %v", err)
	}
	if v.Major != 0 || v.Minor != 1 || v.Patch != 1 {
		t.Fatalf("Parse(v0.1.1) = %+v", v)
	}
	if v.String() != "v0.1.1" {
		t.Fatalf("String() = %q", v.String())
	}
}

func TestParseRejectsEverythingElse(t *testing.T) {
	for _, tag := range []string{
		"", "dev", "0.1.1", "v0.1", "v0.1.1.2",
		"v0.1.1-3-gabcdef", "v0.1.1-dirty", "v1.2.3-rc1",
		"v01.2.3", "v1.2.x", "v1..3", "v1.2.3 ",
		"abcdef1234", "v0.1.1-3-gabcdef-dirty",
	} {
		if tag == "v1.2.3 " {
			// Trailing space is trimmed, so this one parses — it is
			// asserted separately below.
			continue
		}
		if _, err := Parse(tag); err == nil {
			t.Fatalf("Parse(%q) succeeded, want an error", tag)
		}
		if !IsUnversioned(tag) {
			t.Fatalf("IsUnversioned(%q) = false, want true", tag)
		}
	}
	if _, err := Parse("v1.2.3 "); err != nil {
		t.Fatalf("Parse tolerates surrounding whitespace, got: %v", err)
	}
}

func TestNewerOrdersReleases(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.1.0", "v0.1.1", true},
		{"v0.1.1", "v0.1.1", false},
		{"v0.2.0", "v0.1.9", false},
		{"v0.9.9", "v1.0.0", true},
		{"v1.10.0", "v1.9.9", false},
		// Unversioned current build now offers upgrade to latest release.
		{"dev", "v0.1.1", true},
		{"v0.1.0", "dev", false},
		{"v0.1.0", "v0.1.2-rc1", false},
	}
	for _, c := range cases {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Fatalf("Newer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}
