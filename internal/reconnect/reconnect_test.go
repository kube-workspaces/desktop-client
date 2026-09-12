// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package reconnect

import (
	"math"
	"testing"
	"time"
)

func TestPolicyBackoffSequence(t *testing.T) {
	tests := []struct {
		name   string
		policy Policy
		want   []time.Duration
	}{
		{
			name:   "zero value uses defaults",
			policy: Policy{},
			want: []time.Duration{
				time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
				16 * time.Second, 30 * time.Second, 30 * time.Second,
			},
		},
		{
			name:   "default policy without jitter",
			policy: func() Policy { p := Default(); p.Jitter = false; return p }(),
			want: []time.Duration{
				time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
				16 * time.Second, 30 * time.Second, 30 * time.Second,
			},
		},
		{
			name:   "custom initial and multiplier",
			policy: Policy{Initial: 100 * time.Millisecond, Max: time.Second, Multiplier: 3},
			want: []time.Duration{
				100 * time.Millisecond, 300 * time.Millisecond,
				900 * time.Millisecond, time.Second, time.Second,
			},
		},
		{
			name:   "multiplier of one is a constant delay",
			policy: Policy{Initial: 250 * time.Millisecond, Max: time.Minute, Multiplier: 1},
			want: []time.Duration{
				250 * time.Millisecond, 250 * time.Millisecond, 250 * time.Millisecond,
			},
		},
		{
			name:   "max below initial pins every delay to max",
			policy: Policy{Initial: 10 * time.Second, Max: time.Second, Multiplier: 2},
			want:   []time.Duration{time.Second, time.Second, time.Second},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i, want := range tt.want {
				attempt := i + 1
				if got := tt.policy.Backoff(attempt); got != want {
					t.Errorf("Backoff(%d) = %v, want %v", attempt, got, want)
				}
			}
		})
	}
}

func TestPolicyBackoffAttemptFloor(t *testing.T) {
	p := Policy{Initial: time.Second, Max: time.Minute, Multiplier: 2}
	for _, attempt := range []int{math.MinInt, -1, 0, 1} {
		if got := p.Backoff(attempt); got != time.Second {
			t.Errorf("Backoff(%d) = %v, want %v", attempt, got, time.Second)
		}
	}
}

// A huge attempt count must saturate at Max rather than overflow into a
// negative or absurd duration, which is what naive int64 doubling does.
func TestPolicyBackoffDoesNotOverflow(t *testing.T) {
	p := Policy{Initial: time.Second, Max: 30 * time.Second, Multiplier: 2}
	for _, attempt := range []int{100, 1000, math.MaxInt32} {
		got := p.Backoff(attempt)
		if got != 30*time.Second {
			t.Errorf("Backoff(%d) = %v, want %v", attempt, got, 30*time.Second)
		}
	}
}

func TestPolicyBackoffJitterBounds(t *testing.T) {
	// Bounds, not values: the point of jitter is that the value is not
	// predictable, so the test asserts the contract instead.
	p := Default()
	uncapped := Policy{Initial: p.Initial, Max: p.Max, Multiplier: p.Multiplier}

	for attempt := 1; attempt <= 12; attempt++ {
		ceiling := uncapped.Backoff(attempt)
		for i := 0; i < 200; i++ {
			got := p.Backoff(attempt)
			if got < 0 || got >= ceiling {
				t.Fatalf("Backoff(%d) = %v, want in [0, %v)", attempt, got, ceiling)
			}
		}
	}
}

func TestPolicyBackoffJitterIsDeterministicWithInjectedRand(t *testing.T) {
	samples := []float64{0, 0.5, 0.25, 1, -1, math.NaN()}
	i := 0
	p := Policy{
		Initial:    time.Second,
		Max:        4 * time.Second,
		Multiplier: 2,
		Jitter:     true,
		Rand: func() float64 {
			f := samples[i]
			i++
			return f
		},
	}

	tests := []struct {
		attempt int
		want    time.Duration
		desc    string
	}{
		{1, 0, "sample 0 yields no delay at all"},
		{2, time.Second, "sample 0.5 halves the 2s step"},
		{3, time.Second, "sample 0.25 quarters the 4s step"},
		// An out-of-range sample from a caller-supplied Rand must not be able
		// to exceed the cap or go negative.
		{4, 4*time.Second - 1, "sample 1 is clamped just below the ceiling"},
		{5, 0, "negative sample is clamped to zero"},
		{6, 0, "NaN sample is clamped to zero"},
	}
	for _, tt := range tests {
		if got := p.Backoff(tt.attempt); got != tt.want {
			t.Errorf("%s: Backoff(%d) = %v, want %v", tt.desc, tt.attempt, got, tt.want)
		}
	}
}

func TestPolicyExhausted(t *testing.T) {
	tests := []struct {
		name        string
		maxAttempts int
		attempt     int
		want        bool
	}{
		{"unlimited by default", 0, 1_000_000, false},
		{"negative is unlimited", -1, 5, false},
		{"single attempt not yet made", 1, 0, false},
		{"single attempt used", 1, 1, true},
		{"below limit", 5, 4, false},
		{"at limit", 5, 5, true},
		{"past limit", 5, 6, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Policy{MaxAttempts: tt.maxAttempts}
			if got := p.Exhausted(tt.attempt); got != tt.want {
				t.Errorf("Exhausted(%d) with MaxAttempts=%d = %v, want %v",
					tt.attempt, tt.maxAttempts, got, tt.want)
			}
		})
	}
}

func TestDefault(t *testing.T) {
	p := Default()
	switch {
	case p.Initial != DefaultInitial:
		t.Errorf("Initial = %v, want %v", p.Initial, DefaultInitial)
	case p.Max != DefaultMax:
		t.Errorf("Max = %v, want %v", p.Max, DefaultMax)
	case p.Multiplier != DefaultMultiplier:
		t.Errorf("Multiplier = %v, want %v", p.Multiplier, DefaultMultiplier)
	case p.MaxAttempts != 0:
		t.Errorf("MaxAttempts = %d, want 0 (unlimited)", p.MaxAttempts)
	case !p.Jitter:
		t.Error("Jitter = false, want true")
	}
}

// The zero Policy must behave exactly like Default with jitter off, so that
// callers can set a single field without inheriting a broken schedule.
func TestZeroPolicyNormalizesToDefaults(t *testing.T) {
	got := Policy{}.normalized()
	switch {
	case got.Initial != DefaultInitial:
		t.Errorf("Initial = %v, want %v", got.Initial, DefaultInitial)
	case got.Max != DefaultMax:
		t.Errorf("Max = %v, want %v", got.Max, DefaultMax)
	case got.Multiplier != DefaultMultiplier:
		t.Errorf("Multiplier = %v, want %v", got.Multiplier, DefaultMultiplier)
	case got.Jitter:
		t.Error("Jitter = true, want false")
	}
}
