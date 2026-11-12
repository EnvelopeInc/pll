package pll

import (
	"math"
	"time"
)

// FollowerConfig configures a Follower. Start from DefaultFollowerConfig and
// change what you need; NewFollower checks every field.
type FollowerConfig struct {
	// Nominal is the remote clock's nominal tick rate, in ticks per second.
	Nominal Rate
	// Bandwidth is the loop's natural frequency in Hz. Lower filters more
	// jitter and settles more slowly.
	Bandwidth float64
	// Damping is the loop's damping ratio.
	Damping float64
	// MaxOffsetPPM bounds the rate offset the loop will estimate, in parts
	// per million.
	MaxOffsetPPM float64
	// Window groups readings into windows of this much local time and feeds
	// the loop only the earliest-looking reading of each. 0 feeds every reading.
	Window time.Duration
	// OutlierThreshold: a sample whose phase error exceeds this is rejected.
	OutlierThreshold time.Duration
	// StepAfter is how many consecutive, agreeing rejections force a phase step.
	StepAfter int
	// LockThreshold is the largest phase error that counts toward lock.
	LockThreshold time.Duration
	// LockCount is how many consecutive good samples declare lock.
	LockCount int
	// HoldoverAfter is how long without an accepted sample before State
	// reports holdover.
	HoldoverAfter time.Duration
}

// DefaultFollowerConfig returns the defaults for a remote clock with the given
// nominal tick rate: Bandwidth 0.1 Hz, Damping √2/2, MaxOffsetPPM 500, Window 0,
// OutlierThreshold 5 ms, StepAfter 3, LockThreshold 100 µs, LockCount 8,
// HoldoverAfter 5 s.
func DefaultFollowerConfig(nominal Rate) FollowerConfig {
	return FollowerConfig{
		Nominal:          nominal,
		Bandwidth:        0.1,
		Damping:          math.Sqrt2 / 2,
		MaxOffsetPPM:     500,
		Window:           0,
		OutlierThreshold: 5 * time.Millisecond,
		StepAfter:        3,
		LockThreshold:    100 * time.Microsecond,
		LockCount:        8,
		HoldoverAfter:    5 * time.Second,
	}
}

// Follower is a second-order (type-II) phase-locked loop that tracks a remote
// clock against the local clock from (local time, remote ticks) readings. It
// is not safe for concurrent use. Construct it with NewFollower.
//
// The fields are yours to choose. SPEC.md, part 2, says what state the loop
// keeps; Observe must not allocate.
type Follower struct {
	cfg FollowerConfig // validated configuration
	pn  float64        // nominal tick period, ns: Den × 1e9 / Num
	wn  float64        // loop natural frequency, rad/s: 2π × Bandwidth

	// Mapping: remote R0 ticks at local time l0 + l0frac ns (SPEC 2.3).
	anchored bool
	l0       time.Duration
	l0frac   float64
	r0       int64
	y        float64       // fractional rate offset
	ref      time.Duration // local time of the last anchor, step or loop update

	// Reading order (SPEC 2.4.1): the last reading that returned no error.
	hasPrev    bool
	prevLocal  time.Duration
	prevRemote int64

	// Window (SPEC 2.4.3). Unused when cfg.Window is 0.
	winOpen    bool
	winStart   time.Duration
	winCand    bool // whether a candidate is held
	candLocal  time.Duration
	candRemote int64
	candErr    float64 // phase error of the candidate, ns

	// Outlier run (SPEC 2.5.1).
	runLen int
	runErr float64 // error of the run's first outlier, ns

	// Lock and jitter (SPEC 2.5.3, 2.5.4).
	lockRun  int
	lastAcc  time.Duration // local time of the last accepted sample, or anchor/step
	accepted int           // accepted samples since the last anchor, step or Reset, capped at 2
	jitterM  float64       // weighted mean of squared phase errors, ns²
}

// NewFollower returns a Follower with no readings. An invalid field returns
// nil and a *ConfigError naming the first bad field in declaration order.
func NewFollower(cfg FollowerConfig) (*Follower, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Follower{
		cfg: cfg,
		pn:  float64(cfg.Nominal.Den()) * 1e9 / float64(cfg.Nominal.Num()),
		wn:  2 * math.Pi * cfg.Bandwidth,
	}, nil
}

// validate checks the fields in SPEC 2.2 order and returns a *ConfigError for
// the first one that fails.
func (c FollowerConfig) validate() error {
	switch {
	case c.Nominal.Num() == 0 || c.Nominal.Den() == 0:
		return &ConfigError{"Nominal", "must be a valid Rate"}
	case !(c.Bandwidth > 0) || math.IsInf(c.Bandwidth, 0):
		return &ConfigError{"Bandwidth", "must be finite and > 0"}
	case !(c.Damping > 0) || math.IsInf(c.Damping, 0):
		return &ConfigError{"Damping", "must be finite and > 0"}
	case !(c.MaxOffsetPPM > 0 && c.MaxOffsetPPM < 1e6):
		return &ConfigError{"MaxOffsetPPM", "must be finite, > 0 and < 1000000"}
	case c.Window < 0:
		return &ConfigError{"Window", "must be >= 0"}
	case c.OutlierThreshold <= 0:
		return &ConfigError{"OutlierThreshold", "must be > 0"}
	case c.StepAfter < 1:
		return &ConfigError{"StepAfter", "must be >= 1"}
	case c.LockThreshold < 0:
		return &ConfigError{"LockThreshold", "must be >= 0"}
	case c.LockCount < 1:
		return &ConfigError{"LockCount", "must be >= 1"}
	case c.HoldoverAfter <= 0:
		return &ConfigError{"HoldoverAfter", "must be > 0"}
	}
	return nil
}

// Observe feeds one reading: the remote clock showed remote ticks at local
// time local. It returns what happened to the reading and, for a sample fed to
// the loop, its phase error. A reading out of order returns ErrOrder and
// changes nothing.
func (f *Follower) Observe(local time.Duration, remote int64) (Observation, error) {
	return Observation{}, nil
}

// Local maps remote ticks to local time, rounded to the nearest nanosecond.
func (f *Follower) Local(remote int64) (time.Duration, error) {
	return 0, nil
}

// Remote maps local time to remote ticks, rounded to the nearest tick.
func (f *Follower) Remote(local time.Duration) (int64, error) {
	return 0, nil
}

// Offset returns the estimated fractional rate offset y: the remote clock
// runs at nominal × (1 + y) as measured by the local clock.
func (f *Follower) Offset() float64 { return 0 }

// Trim returns round(y × 1e12), ready for Pacer.SetTrim.
func (f *Follower) Trim() int64 { return 0 }

// Jitter returns the root mean square of accepted samples' phase errors,
// weighted 1/16 per sample, rounded to the nearest nanosecond. It is 0 until
// two samples have been accepted since the last anchor, step or Reset.
func (f *Follower) Jitter() time.Duration {
	return 0
}

// State returns StateHoldover when now is more than HoldoverAfter past the
// last accepted sample, and otherwise the lock state.
func (f *Follower) State(now time.Duration) State {
	return StateAcquiring
}

// Reset forgets every reading, the rate estimate included; the next reading
// anchors.
func (f *Follower) Reset() {
}

// Observation reports what Observe did with a reading.
type Observation struct {
	Status Status
	Error  time.Duration // phase error of the sample fed to the loop, ns
}
