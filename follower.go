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
	// Set by the first reading, so it is valid once anchored.
	prevLocal  time.Duration
	prevRemote int64

	// Window (SPEC 2.4.3). Unused when cfg.Window is 0.
	winOpen    bool
	winStart   time.Duration
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
// the loop, its phase error. A reading out of order returns ErrOrder, and a
// reading whose loop update would move the anchor beyond the range of
// time.Duration returns ErrOverflow; either way nothing changes.
func (f *Follower) Observe(local time.Duration, remote int64) (Observation, error) {
	if !f.anchored {
		f.anchor(local, remote)
		f.prevLocal, f.prevRemote = local, remote
		return Observation{Status: StatusAnchored}, nil
	}
	if local < f.prevLocal || remote <= f.prevRemote {
		return Observation{}, ErrOrder
	}
	obs := Observation{Status: StatusPending}
	if f.cfg.Window == 0 {
		var err error
		obs, err = f.feed(local, remote)
		if err != nil {
			return Observation{}, err
		}
	} else if !f.winOpen || uint64(local)-uint64(f.winStart) >= uint64(f.cfg.Window) {
		// Ordered local times make this unsigned difference exact, even
		// when start + Window or the signed difference would overflow.
		if f.winOpen {
			// The mapping has not changed since the candidate arrived, so
			// feed computes the same phase error. Commit only on success.
			next := *f
			var err error
			obs, err = next.feed(f.candLocal, f.candRemote)
			if err != nil {
				return Observation{}, err
			}
			*f = next
		}
		f.winOpen = true
		f.winStart = local
		f.candLocal, f.candRemote = local, remote
		// Use the mapping after the flush for the new window's errors.
		f.candErr = f.phaseError(local, remote)
	} else if e := f.phaseError(local, remote); e < f.candErr {
		f.candLocal, f.candRemote, f.candErr = local, remote, e
	}
	f.prevLocal, f.prevRemote = local, remote
	return obs, nil
}

// anchor sets the mapping to remote at local time local, with rate offset 0
// (SPEC 2.4.2).
func (f *Follower) anchor(local time.Duration, remote int64) {
	f.anchored = true
	f.y = 0
	f.restart(local, remote)
}

// restart sets L0 = local and R0 = remote, and clears the lock, jitter and
// outlier run. The rate offset is left alone (SPEC 2.5.1 step, 2.4.2 anchor).
func (f *Follower) restart(local time.Duration, remote int64) {
	f.l0, f.l0frac = local, 0
	f.r0 = remote
	f.ref = local
	f.lastAcc = local
	f.accepted = 0
	f.lockRun = 0
	f.jitterM = 0
	f.runLen = 0
}

// feed handles one reading as a sample for the loop (SPEC 2.5.1). An outlier
// is rejected unless it completes a run of StepAfter agreeing outliers, which
// steps the clock. A sample that is not an outlier is accepted.
func (f *Follower) feed(l time.Duration, r int64) (Observation, error) {
	e := f.phaseError(l, r)
	obs := Observation{Error: roundDuration(e)}
	th := float64(f.cfg.OutlierThreshold)
	if math.Abs(e) > th {
		if f.runLen > 0 && math.Abs(e-f.runErr) <= th {
			f.runLen++
		} else {
			f.runLen, f.runErr = 1, e
		}
		if f.runLen >= f.cfg.StepAfter {
			f.restart(l, r)
			obs.Status = StatusStepped
		} else {
			obs.Status = StatusRejected
		}
		return obs, nil
	}
	f.runLen = 0
	if err := f.accept(l, r, e); err != nil {
		return Observation{}, err
	}
	obs.Status = StatusAccepted
	return obs, nil
}

// accept uses a non-outlier sample with phase error e: the loop update (SPEC
// 2.5.2), then lock (2.5.3) and jitter (2.5.4). It changes nothing when the
// new anchor does not fit in a time.Duration.
func (f *Follower) accept(l time.Duration, r int64, e float64) error {
	y, l0, frac, r0, ref := f.y, f.l0, f.l0frac, f.r0, f.ref
	if dt := nanosBetween(l, f.ref) / 1e9; dt > 0 {
		wn := f.wn
		x := wn * math.Min(dt, 1/(2*wn))
		kp := math.Min(1, 2*f.cfg.Damping*x)
		pred := f.predictFrom(r) // L̂_old(r) − L0, before y changes
		maxY := f.cfg.MaxOffsetPPM * 1e-6
		y = min(max(f.y-(x*x/dt)*e/1e9, -maxY), maxY)
		v := pred + kp*e
		ns := math.Round(v)
		var err error
		if l0, err = addNanos(f.l0, ns); err != nil {
			return err
		}
		frac = v - ns
		r0, ref = r, l
	}
	f.y, f.l0, f.l0frac, f.r0, f.ref = y, l0, frac, r0, ref

	if math.Abs(e) <= float64(f.cfg.LockThreshold) {
		f.lockRun = min(f.lockRun+1, f.cfg.LockCount)
	} else {
		f.lockRun = 0
	}
	e2 := e * e
	if f.accepted == 0 {
		f.jitterM = e2
	} else {
		f.jitterM += (e2 - f.jitterM) / 16
	}
	f.accepted = min(f.accepted+1, 2)
	f.lastAcc = l
	return nil
}

// phaseError returns the phase error e = l − L̂(r) of reading (l, r), in ns.
func (f *Follower) phaseError(l time.Duration, r int64) float64 {
	return nanosBetween(l, f.l0) - f.predictFrom(r)
}

// predictFrom returns L̂(r) − L0 in ns, including the fraction of L0.
func (f *Follower) predictFrom(r int64) float64 {
	var ticks float64
	if d, ok := subInt64(r, f.r0); ok {
		ticks = float64(d)
	} else {
		ticks = float64(r) - float64(f.r0)
	}
	return ticks*f.pn/(1+f.y) + f.l0frac
}

// nanosBetween returns a − b in ns as a float64, exact when it fits in int64.
func nanosBetween(a, b time.Duration) float64 {
	if d, ok := subInt64(int64(a), int64(b)); ok {
		return float64(d)
	}
	return float64(a) - float64(b)
}

// roundDuration rounds ns to the nearest nanosecond, saturating at the
// limits of time.Duration.
func roundDuration(ns float64) time.Duration {
	r := math.Round(ns)
	switch {
	case r >= 1<<63:
		return math.MaxInt64
	case r < -(1 << 63):
		return math.MinInt64
	}
	return time.Duration(r)
}

// Local maps remote ticks to local time, rounded to the nearest nanosecond.
func (f *Follower) Local(remote int64) (time.Duration, error) {
	if !f.anchored {
		return 0, ErrNotAnchored
	}
	// Exact when remote - r0 fits in int64; otherwise the float64 result
	// is approximate, but such a distance is far outside any real clock.
	var ticks float64
	if d, ok := subInt64(remote, f.r0); ok {
		ticks = float64(d)
	} else {
		ticks = float64(remote) - float64(f.r0)
	}
	ns := math.Round(ticks*f.pn/(1+f.y) + f.l0frac)
	return addNanos(f.l0, ns)
}

// Remote maps local time to remote ticks, rounded to the nearest tick.
func (f *Follower) Remote(local time.Duration) (int64, error) {
	if !f.anchored {
		return 0, ErrNotAnchored
	}
	var ns float64
	if d, ok := subInt64(int64(local), int64(f.l0)); ok {
		ns = float64(d)
	} else {
		ns = float64(local) - float64(f.l0)
	}
	ticks := math.Round((ns - f.l0frac) * (1 + f.y) / f.pn)
	if !(ticks >= -(1<<63) && ticks < 1<<63) {
		return 0, ErrOverflow
	}
	r, ok := addInt64(f.r0, int64(ticks))
	if !ok {
		return 0, ErrOverflow
	}
	return r, nil
}

// subInt64 returns a - b and whether it fits in int64.
func subInt64(a, b int64) (int64, bool) {
	d := a - b
	if (b > 0 && d > a) || (b < 0 && d < a) {
		return 0, false
	}
	return d, true
}

// addInt64 returns a + b and whether it fits in int64.
func addInt64(a, b int64) (int64, bool) {
	s := a + b
	if (b > 0 && s < a) || (b < 0 && s > a) {
		return 0, false
	}
	return s, true
}

// addNanos returns base + ns, where ns is a whole number of nanoseconds, or
// ErrOverflow when the sum or ns does not fit in a time.Duration.
func addNanos(base time.Duration, ns float64) (time.Duration, error) {
	if !(ns >= -(1<<63) && ns < 1<<63) {
		return 0, ErrOverflow
	}
	s, ok := addInt64(int64(base), int64(ns))
	if !ok {
		return 0, ErrOverflow
	}
	return time.Duration(s), nil
}

// Offset returns the estimated fractional rate offset y: the remote clock
// runs at nominal × (1 + y) as measured by the local clock.
func (f *Follower) Offset() float64 { return f.y }

// Trim returns round(y × 1e12), ready for Pacer.SetTrim.
func (f *Follower) Trim() int64 { return int64(math.Round(f.y * 1e12)) }

// Jitter returns the root mean square of accepted samples' phase errors,
// weighted 1/16 per sample, rounded to the nearest nanosecond. It is 0 until
// two samples have been accepted since the last anchor, step or Reset.
func (f *Follower) Jitter() time.Duration {
	if f.accepted < 2 {
		return 0
	}
	return roundDuration(math.Sqrt(f.jitterM))
}

// State returns StateHoldover when now is more than HoldoverAfter past the
// last accepted sample, and otherwise the lock state.
func (f *Follower) State(now time.Duration) State {
	if f.anchored && now > f.lastAcc {
		// now - lastAcc, exact as unsigned when now > lastAcc, even when the
		// signed difference would overflow.
		if uint64(now)-uint64(f.lastAcc) > uint64(f.cfg.HoldoverAfter) {
			return StateHoldover
		}
	}
	if f.lockRun >= f.cfg.LockCount {
		return StateLocked
	}
	return StateAcquiring
}

// Reset forgets every reading, the rate estimate included; the next reading
// anchors.
func (f *Follower) Reset() {
	// Keep only the validated configuration and its derived constants; every
	// other field is loop state, which Reset forgets (SPEC 2.6).
	*f = Follower{cfg: f.cfg, pn: f.pn, wn: f.wn}
}

// Observation reports what Observe did with a reading.
type Observation struct {
	Status Status
	Error  time.Duration // phase error of the sample fed to the loop, ns
}
