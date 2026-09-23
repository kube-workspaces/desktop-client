// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a strict vMAJOR.MINOR.PATCH release tag: the only shape the
// updater compares. Anything else (dev builds, dirty trees, bare shas,
// pre-release suffixes) is unversioned and never auto-offered an update.
type Version struct {
	Major, Minor, Patch int
}

// String renders the tag form, with the v prefix tags carry.
func (v Version) String() string {
	return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Compare orders two versions: -1, 0 or +1.
func (v Version) Compare(o Version) int {
	if v.Major != o.Major {
		return cmpInt(v.Major, o.Major)
	}
	if v.Minor != o.Minor {
		return cmpInt(v.Minor, o.Minor)
	}
	return cmpInt(v.Patch, o.Patch)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// Parse interprets a release tag. Only the exact vMAJOR.MINOR.PATCH shape
// parses; everything else reports an error and the caller treats the build
// as unversioned (see [IsUnversioned]).
func Parse(tag string) (Version, error) {
	s := strings.TrimSpace(tag)
	if !strings.HasPrefix(s, "v") {
		return Version{}, fmt.Errorf("update: version %q has no v prefix", tag)
	}
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("update: version %q is not vMAJOR.MINOR.PATCH", tag)
	}
	var v Version
	for i, p := range parts {
		if p == "" {
			return Version{}, fmt.Errorf("update: version %q has an empty component", tag)
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || strconv.Itoa(n) != p {
			return Version{}, fmt.Errorf("update: version %q has a non-numeric component %q", tag, p)
		}
		switch i {
		case 0:
			v.Major = n
		case 1:
			v.Minor = n
		case 2:
			v.Patch = n
		}
	}
	return v, nil
}

// IsUnversioned reports whether a stamped version string can never take part
// in update comparison: dev builds, dirty trees, commit-count describes like
// v0.1.1-3-gabcdef (or -dirty), and bare shas. An unversioned build never
// gets an automatic offer; an explicit `update --check` may still report
// what the latest release is.
func IsUnversioned(stamped string) bool {
	_, err := Parse(stamped)
	return err != nil
}

// Newer reports whether latest is strictly newer than current. Either side
// being unversioned means false: the updater only moves between two real
// releases, never away from a build it cannot place.
func Newer(current, latest string) bool {
	c, err := Parse(current)
	if err != nil {
		return false
	}
	l, err := Parse(latest)
	if err != nil {
		return false
	}
	return l.Compare(c) > 0
}
