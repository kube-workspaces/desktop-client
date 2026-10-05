// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package media

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// A library that cannot possibly exist must fail with the operating system's
// own reason, not just the name: "no such file" and "present, dependency
// missing" need different fixes and used to be indistinguishable.
func TestLoadFailureCarriesTheOSError(t *testing.T) {
	_, err := load("libkw-not-a-real-library.so.9999")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("load = %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "libkw-not-a-real-library.so.9999") {
		t.Errorf("load = %v, want the attempted name", err)
	}
	// The sentinel names the library, the OS error explains it. Without a
	// trailing cause the message is just a filename.
	tail := err.Error()[strings.LastIndex(err.Error(), ": ")+2:]
	if strings.Contains(tail, "could not load") || tail == "" {
		t.Errorf("load = %v, want an OS-supplied reason after the library name", err)
	}
}

// Every candidate spelling is tried, so a platform shipping one of two names
// still works and the error still names the whole set.
func TestLoadTriesEveryCandidateAndNamesThemAll(t *testing.T) {
	_, err := load("libkw-missing-a.so.9999", "libkw-missing-b.so.9999")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("load = %v, want ErrUnavailable", err)
	}
	for _, name := range []string{"libkw-missing-a.so.9999", "libkw-missing-b.so.9999"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("load = %v, want %s named", err, name)
		}
	}
}

// ProbeVideo must be safe to call from every session opener at once: the shell
// consults it while choosing a transport, and Tier 1 may be probed again by a
// reconnect.
func TestProbeVideoIsConcurrencySafe(t *testing.T) {
	videoLatched.Store(false)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = ProbeVideo()
		}()
	}
	wg.Wait()
}

// A successful probe latches; a failed one does not. Latching a failure would
// downgrade Tier 1 for the whole run over something as transient as a scanner
// holding a freshly written DLL, which is the failure this exists to survive.
func TestProbeVideoLatchesOnlySuccess(t *testing.T) {
	videoLatched.Store(false)
	// A decoder that cannot be constructed must not be reported as available,
	// and must not poison later probes in a way tests cannot observe.
	if err := ProbeVideo(); err != nil && !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ProbeVideo = %v, want nil or ErrUnavailable", err)
	}
	if videoLatched.Load() && probeWouldFail() {
		t.Error("ProbeVideo latched a failure")
	}
}

// probeWouldFail reports whether this build genuinely cannot load the pinned
// libraries, which is the only state in which latching would be wrong.
func probeWouldFail() bool {
	if _, err := load(codecNames()...); err != nil {
		return true
	}
	return false
}
