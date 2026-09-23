// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"testing"
	"time"
)

func boolPtr(b bool) *bool { return &b }

func TestAutoCheckNeedsNothingByDefault(t *testing.T) {
	var p Policy
	if s, off := p.AutoDisabled(); off {
		t.Fatalf("zero Policy disables the check: %s", s)
	}
	if !p.ShouldCheck(time.Time{}, time.Now()) {
		t.Fatal("a client that never checked is due")
	}
}

func TestEverySwitchStopsTheBackgroundCheck(t *testing.T) {
	cases := []struct {
		name   string
		policy Policy
	}{
		{"managed", Policy{Managed: true}},
		{"env", Policy{EnvDisabled: true}},
		{"setting", Policy{AutoUpdate: boolPtr(false)}},
		{"all", Policy{Managed: true, EnvDisabled: true, AutoUpdate: boolPtr(false)}},
	}
	for _, c := range cases {
		if s, off := c.policy.AutoDisabled(); !off {
			t.Fatalf("%s: background check allowed", c.name)
		} else if s == "" {
			t.Fatalf("%s: no reason given", c.name)
		}
		if c.policy.ShouldCheck(time.Time{}, time.Now()) {
			t.Fatalf("%s: due despite being disabled", c.name)
		}
	}
}

func TestExplicitOptOutBeatsTheDefault(t *testing.T) {
	p := Policy{AutoUpdate: boolPtr(true)}
	if _, off := p.AutoDisabled(); off {
		t.Fatal("an explicit true disables the check")
	}
}

func TestManagedRefusesExplicitChecksToo(t *testing.T) {
	p := Policy{Managed: true}
	if _, off := p.DisabledFor(""); !off {
		t.Fatal("a managed install allowed an explicit check")
	}
	p = Policy{EnvDisabled: true, AutoUpdate: boolPtr(false)}
	if _, off := p.DisabledFor(""); off {
		t.Fatal("env/setting switches must not block an explicit `update` command")
	}
}

func TestCheckCadenceIs24Hours(t *testing.T) {
	now := time.Now()
	var p Policy
	if !p.ShouldCheck(now.Add(-25*time.Hour), now) {
		t.Fatal("a check older than 24 h is due")
	}
	if p.ShouldCheck(now.Add(-23*time.Hour), now) {
		t.Fatal("a check newer than 24 h is not due")
	}
	if p.ShouldCheck(now.Add(time.Hour), now) {
		t.Fatal("a check timestamped in the future is not due")
	}
}
