// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package reconnect implements the retry pacing a long-lived client uses when
// its connection drops.
//
// The policy is capped exponential backoff with full jitter. Jitter is not
// cosmetic: when a platform restarts its API every connected client is
// disconnected at the same instant, and a fleet retrying on the same
// deterministic schedule turns one outage into a self-inflicted thundering
// herd that keeps the API from coming back. Full jitter — a uniform sample
// from [0, computed] rather than a fixed fraction around it — is the variant
// that spreads the load best while still bounding the wait, and it is the
// cheapest to reason about.
//
// The package deliberately depends on nothing outside the standard library,
// and the source of randomness is injectable, so the schedule can be unit
// tested exactly rather than statistically.
package reconnect

import (
	"math"
	"math/rand/v2"
	"time"
)

// Defaults used for any zero field of a [Policy]. They are separate constants
// because each one is a decision rather than a taste:
//
//   - one second is long enough that an instant retry of a connection that
//     failed for a real reason does not simply fail again, and short enough
//     that a blip is invisible to the user;
//   - thirty seconds bounds how long a session sits dark after the platform
//     comes back, which is the number a user actually feels;
//   - doubling reaches the cap in five attempts, so the total delay before a
//     session is deemed hopeless grows linearly rather than explosively.
const (
	DefaultInitial    = time.Second
	DefaultMax        = 30 * time.Second
	DefaultMultiplier = 2.0
)

// Policy describes a retry schedule.
//
// The zero value is usable and equivalent to [Default]: every field falls back
// to its documented default, so a caller can override one number without
// having to restate the rest.
type Policy struct {
	// Initial is the delay after the first failed attempt. Zero or negative
	// means [DefaultInitial].
	Initial time.Duration

	// Max caps the delay however many attempts have failed. Zero or negative
	// means [DefaultMax]. A Max below Initial simply pins every delay to Max.
	Max time.Duration

	// Multiplier is the growth factor between attempts. A value below 1 means
	// [DefaultMultiplier]; exactly 1 gives a constant delay, which is a
	// legitimate policy for a resource that is busy rather than broken.
	Multiplier float64

	// MaxAttempts bounds how many attempts may fail before the caller should
	// give up. Zero or negative means unlimited, which is the right default
	// for an interactive session: only the user knows when to stop waiting.
	MaxAttempts int

	// Jitter enables full jitter, spreading retries uniformly over
	// [0, computed]. See the package comment for why this matters.
	Jitter bool

	// Rand supplies jitter samples in [0, 1). Nil means [math/rand/v2.Float64].
	// It exists so tests can pin the schedule; production code should leave it
	// nil.
	Rand func() float64
}

// Default returns the policy an interactive session should use: one second
// initial delay, thirty second cap, doubling, unlimited attempts, jitter on.
func Default() Policy {
	return Policy{
		Initial:     DefaultInitial,
		Max:         DefaultMax,
		Multiplier:  DefaultMultiplier,
		MaxAttempts: 0,
		Jitter:      true,
	}
}

// normalized returns p with every unset field replaced by its default, so the
// rest of the package can compute without re-checking for zero values.
func (p Policy) normalized() Policy {
	if p.Initial <= 0 {
		p.Initial = DefaultInitial
	}
	if p.Max <= 0 {
		p.Max = DefaultMax
	}
	if p.Multiplier < 1 {
		p.Multiplier = DefaultMultiplier
	}
	return p
}

// Backoff returns how long to wait after attempt failures.
//
// Attempts are 1-based: Backoff(1) is the delay after the first failure, and
// the delay grows by Multiplier for each subsequent attempt until it reaches
// Max. Values below 1 are treated as 1, so a caller that has not yet counted
// an attempt still gets a sane delay rather than zero.
//
// With Jitter disabled the result is exactly Initial*Multiplier^(attempt-1)
// capped at Max, which is what makes the schedule testable. With Jitter
// enabled the result is a uniform sample from [0, that value).
func (p Policy) Backoff(attempt int) time.Duration {
	p = p.normalized()
	if attempt < 1 {
		attempt = 1
	}

	// The exponential is computed in float64 rather than by repeated
	// multiplication of a Duration: an int64 nanosecond count overflows into
	// nonsense (including negative delays) after about 60 doublings, whereas a
	// float64 saturates to +Inf and is caught by the cap below.
	d := float64(p.Initial) * math.Pow(p.Multiplier, float64(attempt-1))
	if max := float64(p.Max); d > max || math.IsInf(d, 1) || math.IsNaN(d) {
		d = max
	}

	if p.Jitter {
		d *= clampUnit(p.random())
	}
	if d < 0 {
		d = 0
	}
	return time.Duration(d)
}

// Exhausted reports whether attempt failures have used up the budget, i.e.
// whether the caller should stop retrying and report the last error.
//
// It is always false for an unlimited policy.
func (p Policy) Exhausted(attempt int) bool {
	return p.MaxAttempts > 0 && attempt >= p.MaxAttempts
}

// random draws one jitter sample.
func (p Policy) random() float64 {
	if p.Rand != nil {
		return p.Rand()
	}
	return rand.Float64() //nolint:gosec // retry jitter is not a security decision
}

// clampUnit forces a sample into [0, 1). A caller-supplied Rand is not trusted
// to honour the range, and an out-of-range sample must not be able to produce
// a negative delay or one above the cap.
func clampUnit(f float64) float64 {
	switch {
	case math.IsNaN(f), f < 0:
		return 0
	case f >= 1:
		return math.Nextafter(1, 0)
	default:
		return f
	}
}
