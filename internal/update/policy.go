// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"time"
)

// checkInterval is how often the shell phones home: at most once per 24 h.
// The timestamp lives in the settings file, so reinstalls and clock jumps
// are the only ways to beat it, and neither matters.
const checkInterval = 24 * time.Hour

// Policy is everything that can switch the automatic check off, in decision
// order. The zero value checks: nothing disables anything by default.
type Policy struct {
	// Managed means a no-auto-update sentinel file sits in the config
	// directory: a packaged or admin-managed install that owns updating
	// itself. This disables everything, including explicit `update --check`.
	Managed bool
	// EnvDisabled means KUBE_WORKSPACES_NO_UPDATE is set. It stops the
	// automatic check only; an explicit `update` command still runs.
	EnvDisabled bool
	// AutoUpdate is the user's own setting: nil (never touched) means on.
	AutoUpdate *bool
}

// DisabledFor reasons about an explicit user request (`update` on the CLI,
// "Check now" in the shell). Only a managed install refuses those.
func (p Policy) DisabledFor(reason string) (string, bool) {
	if p.Managed {
		return "automatic updates are disabled for this installation" + reason, true
	}
	return "", false
}

// AutoDisabled reasons about the background check the shell runs on its own.
// Any one switch stops the phone-home; the user can still check manually
// (unless managed).
func (p Policy) AutoDisabled() (string, bool) {
	if s, off := p.DisabledFor(""); off {
		return s, true
	}
	if p.EnvDisabled {
		return "KUBE_WORKSPACES_NO_UPDATE is set", true
	}
	if p.AutoUpdate != nil && !*p.AutoUpdate {
		return "automatic updates are switched off in Settings", true
	}
	return "", false
}

// ShouldCheck reports whether the startup check is due: automatic checks
// enabled and no successful check within the interval. A zero lastCheck
// means never, which is always due.
func (p Policy) ShouldCheck(lastCheck time.Time, now time.Time) bool {
	if _, off := p.AutoDisabled(); off {
		return false
	}
	if lastCheck.IsZero() {
		return true
	}
	return !now.Before(lastCheck.Add(checkInterval))
}
