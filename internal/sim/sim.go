// sim.go is complete; do not change it.

// Package sim is a deterministic simulator for exercising pll: remote clocks
// that are offset, fast or slow, drifting, changing rate or jumping; a delivery
// link that adds jitter, heavy-tailed delays, batching and outages; a device
// that drains a buffer at its own rate; and a caller that wakes up late.
//
// Nothing here reads a real clock or sleeps. The same seed gives the same
// result on every machine.
package sim

import (
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/version2beta/pll"
)

// Change sets the remote clock's rate error to PPM from local time At on.
type Change struct {
	At  time.Duration
	PPM float64
}

// Step makes the remote clock jump by Jump at local time At.
type Step struct {
	At   time.Duration
	Jump time.Duration
}

// Clock is a remote clock as seen from true local time.
type Clock struct {
	Nominal         uint64        // ticks per second
	Offset          time.Duration // remote minus local at local time 0
	PPM             float64       // rate error from time 0, parts per million
	Changes         []Change      // later rate errors, in time order
	DriftPPMPerHour float64       // added to the rate error, growing linearly
	Steps           []Step        // phase jumps, in time order
}

// RateAt returns the clock's true fractional rate offset at local time t.
func (c Clock) RateAt(t time.Duration) float64 {
	ppm := c.PPM
	for _, ch := range c.Changes {
		if t >= ch.At {
			ppm = ch.PPM
		}
	}
	return ppm*1e-6 + c.DriftPPMPerHour*1e-6*t.Hours()
}

// Seconds returns the remote clock's reading at local time t, in seconds.
func (c Clock) Seconds(t time.Duration) float64 {
	ts := t.Seconds()
	s := ts + c.Offset.Seconds()
	prev, ppm := 0.0, c.PPM
	for _, ch := range c.Changes {
		at := ch.At.Seconds()
		if at >= ts {
			break
		}
		s += ppm * 1e-6 * (at - prev)
		prev, ppm = at, ch.PPM
	}
	s += ppm * 1e-6 * (ts - prev)
	s += 0.5 * c.DriftPPMPerHour * 1e-6 / 3600 * ts * ts
	for _, st := range c.Steps {
		if t >= st.At {
			s += st.Jump.Seconds()
		}
	}
	return s
}

// Ticks returns the remote clock's reading at local time t, in whole ticks.
func (c Clock) Ticks(t time.Duration) int64 {
	return int64(math.Floor(c.Seconds(t) * float64(c.Nominal)))
}

// Span is a half-open stretch of local time.
type Span struct{ From, To time.Duration }

// Link takes readings of a clock and delivers them late.
type Link struct {
	Interval  time.Duration // a reading every Interval of local time
	Base      time.Duration // the smallest delivery delay
	Jitter    time.Duration // plus a uniform delay in [0, Jitter)
	SpikeProb float64       // with this probability, plus an exponential delay
	SpikeMean time.Duration // with this mean
	Batch     int           // readings arrive in groups of Batch, together
	Outages   []Span        // no readings are taken during these
}

// Reading is one remote clock reading: taken at true local time Taken,
// delivered at local time Arrived.
type Reading struct {
	Taken   time.Duration
	Arrived time.Duration
	Ticks   int64
}

// Readings returns the readings of c over link l during d, in delivery order.
// Deliveries are first in, first out, and remote ticks strictly increase.
func Readings(c Clock, l Link, d time.Duration, seed uint64) []Reading {
	rng := rand.New(rand.NewPCG(seed, 0x5eed))
	var out []Reading
	var lastTicks int64 = math.MinInt64
	var lastArrived time.Duration
	for t := time.Duration(0); t < d; t += l.Interval {
		skip := false
		for _, o := range l.Outages {
			if t >= o.From && t < o.To {
				skip = true
			}
		}
		delay := l.Base
		if l.Jitter > 0 {
			delay += time.Duration(rng.Int64N(int64(l.Jitter)))
		}
		if l.SpikeProb > 0 && rng.Float64() < l.SpikeProb {
			delay += time.Duration(rng.ExpFloat64() * float64(l.SpikeMean))
		}
		if skip {
			continue
		}
		ticks := c.Ticks(t)
		if ticks <= lastTicks {
			continue // the clock has not advanced a whole tick
		}
		arrived := max(t+delay, lastArrived)
		out = append(out, Reading{Taken: t, Arrived: arrived, Ticks: ticks})
		lastTicks, lastArrived = ticks, arrived
	}
	if l.Batch > 1 {
		for i := 0; i < len(out); i += l.Batch {
			j := min(i+l.Batch, len(out)) - 1
			for k := i; k < j; k++ {
				out[k].Arrived = out[j].Arrived
			}
		}
	}
	return out
}

// Scenario is one follower test: a clock, a link, a duration and a config.
type Scenario struct {
	Name     string
	Clock    Clock
	Link     Link
	Duration time.Duration
	Settle   time.Duration // steady-state measurements start here
	Config   func(pll.FollowerConfig) pll.FollowerConfig
}

// Result is what a scenario measured.
type Result struct {
	LockTime    time.Duration // first time State was locked; -1 if never
	RMSError    time.Duration // RMS mapping error after Settle
	MaxError    time.Duration // largest mapping error after Settle
	EndRatePPM  float64       // |Offset - true rate| at the end, ppm
	MaxRatePPM  float64       // largest |Offset - true rate| after Settle, ppm
	FitRatePPM  float64       // |least-squares rate of the last readings - true rate|, ppm
	Anchored    int
	Accepted    int
	Pending     int
	Rejected    int
	Stepped     int
	EndState    pll.State
	EndJitter   time.Duration
	FirstStepAt time.Duration // -1 if none
	Errors      int           // Observe calls that returned an error
	Readings    int
	RelockAfter time.Duration // after the last outage or step: time until locked again; -1 if never
}

// Run feeds the scenario's readings to a Follower configured from
// DefaultFollowerConfig and Scenario.Config, and measures it.
//
// The mapping error of a reading is Local(Ticks) - (Taken + Link.Base): how
// far the follower's idea of when the remote showed Ticks is from the truth,
// allowing for the smallest delivery delay, which no follower can see.
func Run(s Scenario, seed uint64) (Result, error) {
	rate, err := pll.UnitsPerSecond(s.Clock.Nominal, 1)
	if err != nil {
		return Result{}, err
	}
	cfg := pll.DefaultFollowerConfig(rate)
	if s.Config != nil {
		cfg = s.Config(cfg)
	}
	f, err := pll.NewFollower(cfg)
	if err != nil {
		return Result{}, err
	}
	rs := Readings(s.Clock, s.Link, s.Duration, seed)
	res := Result{LockTime: -1, FirstStepAt: -1, RelockAfter: -1, Readings: len(rs)}

	var disturb time.Duration = -1 // the last outage end or step, for RelockAfter
	for _, o := range s.Link.Outages {
		disturb = max(disturb, o.To)
	}
	for _, st := range s.Clock.Steps {
		disturb = max(disturb, st.At)
	}

	var sumSq float64
	var n int
	for _, r := range rs {
		obs, err := f.Observe(r.Arrived, r.Ticks)
		if err != nil {
			res.Errors++
			continue
		}
		switch obs.Status {
		case pll.StatusAnchored:
			res.Anchored++
		case pll.StatusAccepted:
			res.Accepted++
		case pll.StatusPending:
			res.Pending++
		case pll.StatusRejected:
			res.Rejected++
		case pll.StatusStepped:
			res.Stepped++
			if res.FirstStepAt < 0 {
				res.FirstStepAt = r.Arrived
			}
		}
		state := f.State(r.Arrived)
		if state == pll.StateLocked && res.LockTime < 0 {
			res.LockTime = r.Arrived
		}
		if disturb >= 0 && r.Arrived >= disturb && state == pll.StateLocked && res.RelockAfter < 0 {
			res.RelockAfter = r.Arrived - disturb
		}
		if r.Taken >= s.Settle {
			if local, err := f.Local(r.Ticks); err == nil {
				e := float64(local - (r.Taken + s.Link.Base))
				sumSq += e * e
				n++
				if a := time.Duration(math.Abs(e)); a > res.MaxError {
					res.MaxError = a
				}
			}
			d := math.Abs(f.Offset()-s.Clock.RateAt(r.Taken)) * 1e6
			res.MaxRatePPM = max(res.MaxRatePPM, d)
		}
	}
	if n > 0 {
		res.RMSError = time.Duration(math.Sqrt(sumSq / float64(n)))
	}
	if len(rs) > 0 {
		last := rs[len(rs)-1]
		res.EndRatePPM = math.Abs(f.Offset()-s.Clock.RateAt(last.Taken)) * 1e6
		res.EndState = f.State(last.Arrived)
		res.EndJitter = f.Jitter()
		res.FitRatePPM = math.Abs(fitRate(rs, s.Clock.Nominal, s.Settle)-s.Clock.RateAt(last.Taken)) * 1e6
	}
	return res, nil
}

// fitRate is an independent estimate of the remote rate offset: the slope of
// a least-squares line through (arrival time, remote seconds) for the readings
// taken after settle, minus one.
func fitRate(rs []Reading, nominal uint64, settle time.Duration) float64 {
	var xs, ys []float64
	for _, r := range rs {
		if r.Taken >= settle {
			xs = append(xs, r.Arrived.Seconds())
			ys = append(ys, float64(r.Ticks)/float64(nominal))
		}
	}
	if len(xs) < 2 {
		return math.NaN()
	}
	var mx, my float64
	for i := range xs {
		mx += xs[i]
		my += ys[i]
	}
	mx /= float64(len(xs))
	my /= float64(len(xs))
	var sxy, sxx float64
	for i := range xs {
		sxy += (xs[i] - mx) * (ys[i] - my)
		sxx += (xs[i] - mx) * (xs[i] - mx)
	}
	return sxy/sxx - 1
}

// Sink is a device with its own clock that drains a buffer.
type Sink struct {
	Nominal  uint64        // units per second the device is meant to take
	PPM      float64       // how fast its clock really runs
	Lead     time.Duration // the device starts consuming this long after the producer
	Wake     time.Duration // the producer wakes this often
	Observe  time.Duration // and reads the device's queue this often
	Duration time.Duration
	Settle   time.Duration
	Link     bool // feed the follower's trim to the pacer
	Config   func(pll.FollowerConfig) pll.FollowerConfig
}

// SinkResult is what a sink run measured.
type SinkResult struct {
	MinQueue, MaxQueue int64 // queue range after Settle, units
	Underruns          int   // observations that found the queue empty
	EndTrim            int64
	EndRatePPM         float64 // |follower offset - device rate| at the end, ppm
}

// RunSink drives a Pacer that produces units for a device consuming them at
// its own rate. Every Observe interval the follower reads how many units the
// device has consumed, which is a reading of the device's clock; with Link
// set, the pacer's trim follows the follower.
func RunSink(s Sink) (SinkResult, error) {
	rate, err := pll.UnitsPerSecond(s.Nominal, 1)
	if err != nil {
		return SinkResult{}, err
	}
	p, err := pll.NewPacer(rate)
	if err != nil {
		return SinkResult{}, err
	}
	cfg := pll.DefaultFollowerConfig(rate)
	if s.Config != nil {
		cfg = s.Config(cfg)
	}
	f, err := pll.NewFollower(cfg)
	if err != nil {
		return SinkResult{}, err
	}
	var produced uint64
	consumed := func(t time.Duration) uint64 {
		if t <= s.Lead {
			return 0
		}
		return uint64((t - s.Lead).Seconds() * float64(s.Nominal) * (1 + s.PPM*1e-6))
	}
	res := SinkResult{MinQueue: math.MaxInt64, MaxQueue: math.MinInt64}
	var lastObs time.Duration = -s.Observe
	var lastConsumed int64 = -1
	for t := time.Duration(0); t <= s.Duration; t += s.Wake {
		sp, err := p.Due(t, math.MaxUint64)
		if err != nil {
			return res, err
		}
		produced += sp.Count
		if t-lastObs >= s.Observe {
			lastObs = t
			c := consumed(t)
			q := int64(produced) - int64(c)
			if q <= 0 {
				res.Underruns++
			}
			if t >= s.Settle {
				res.MinQueue = min(res.MinQueue, q)
				res.MaxQueue = max(res.MaxQueue, q)
			}
			if int64(c) > lastConsumed {
				if _, err := f.Observe(t, int64(c)); err != nil {
					return res, err
				}
				lastConsumed = int64(c)
				if s.Link {
					if err := p.SetTrim(f.Trim()); err != nil {
						return res, err
					}
				}
			}
		}
	}
	res.EndTrim = p.Trim()
	res.EndRatePPM = math.Abs(f.Offset()-s.PPM*1e-6) * 1e6
	return res, nil
}

// LateResult is what a late-wakeup run measured.
type LateResult struct {
	HandedOut uint64 // slots handed out by Due
	Expected  uint64 // slots due by the end
	Gaps      int    // spans that did not start where the previous one ended
	Early     int    // slots handed out before their time
}

// RunLateWakeups drives a Pacer from a caller that means to wake every Wake
// but wakes up to MaxLate late, and checks that every due slot is handed out
// exactly once and never early.
func RunLateWakeups(rate pll.Rate, wake, maxLate, d time.Duration, seed uint64) (LateResult, error) {
	p, err := pll.NewPacer(rate)
	if err != nil {
		return LateResult{}, err
	}
	rng := rand.New(rand.NewPCG(seed, 0x1a7e))
	var res LateResult
	var next uint64
	var wakes []time.Duration
	for t := time.Duration(0); t <= d; t += wake {
		wakes = append(wakes, t+time.Duration(rng.Int64N(int64(maxLate)+1)))
	}
	sort.Slice(wakes, func(i, j int) bool { return wakes[i] < wakes[j] })
	for _, now := range wakes {
		sp, err := p.Due(now, 1<<20)
		if err != nil {
			return res, err
		}
		if sp.First != next {
			res.Gaps++
		}
		for i := uint64(0); i < sp.Count; i++ {
			if sp.Time(i) > now {
				res.Early++
			}
		}
		next = sp.First + sp.Count
		res.HandedOut += sp.Count
	}
	slot, _, err := p.NextDue()
	if err != nil {
		return res, err
	}
	res.Expected = slot
	return res, nil
}
