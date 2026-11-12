package pll_test

// Proofs for the Follower: a differential test against the literal loop model,
// a fuzz target with invariant checks after every reading, an allocation
// check, and the simulator scenarios with their pass thresholds.

import (
	"encoding/binary"
	"errors"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/version2beta/pll"
	"github.com/version2beta/pll/internal/sim"
)

// TestFollowerDifferential compares the Follower with the literal model on
// noisy readings where no window, outlier, step or clamp applies.
func TestFollowerDifferential(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261112, 2))
	for run := 0; run < 60; run++ {
		nominal := []uint64{48000, 1_000_000, 90_000, 1000}[run%4]
		cfg := pll.DefaultFollowerConfig(mustRate(t, nominal, 1))
		cfg.Bandwidth = []float64{0.1, 0.02, 1, 3}[rng.IntN(4)]
		cfg.Damping = []float64{math.Sqrt2 / 2, 1, 0.3, 2}[rng.IntN(4)]
		cfg.OutlierThreshold = time.Hour
		cfg.MaxOffsetPPM = 999_999
		f := mustFollower(t, cfg)
		m := newLoopModel(cfg, false)

		ppm := rng.Float64()*400 - 200
		interval := []time.Duration{time.Millisecond, 10 * time.Millisecond, 250 * time.Millisecond, 3 * time.Second}[rng.IntN(4)]
		jitter := []time.Duration{0, 10 * time.Microsecond, time.Millisecond}[rng.IntN(3)]
		var lastL time.Duration = -1
		var lastR int64 = math.MinInt64
		for k := 0; k < 400; k++ {
			taken := time.Duration(k) * interval
			l := taken
			if jitter > 0 {
				l += time.Duration(rng.Int64N(int64(jitter)))
			}
			l = max(l, lastL)
			r := int64(math.Floor((taken.Seconds() + 1) * float64(nominal) * (1 + ppm*1e-6)))
			if r <= lastR {
				continue
			}
			lastL, lastR = l, r
			want := m.observe(l, r)
			obs, err := f.Observe(l, r)
			if err != nil {
				t.Fatalf("run %d reading %d: Observe(%v, %d): %v", run, k, l, r, err)
			}
			if k > 0 && obs.Status != pll.StatusAccepted {
				t.Fatalf("run %d reading %d: status %v, want accepted", run, k, obs.Status)
			}
			if d := float64(obs.Error) - math.Round(want); k > 0 && math.Abs(d) > 1 {
				t.Fatalf("run %d reading %d: Error %d ns, model %.1f ns", run, k, int64(obs.Error), want)
			}
			if math.Abs(f.Offset()-m.y) > 1e-12 {
				t.Fatalf("run %d reading %d: Offset %.15g, model %.15g", run, k, f.Offset(), m.y)
			}
			got, err := f.Local(r + int64(nominal))
			if err != nil {
				t.Fatalf("Local: %v", err)
			}
			if d := got - m.local(r+int64(nominal)); d > 1 || d < -1 {
				t.Fatalf("run %d reading %d: Local %d, model %d", run, k, int64(got), int64(m.local(r+int64(nominal))))
			}
		}
	}
}

// followerInvariants feeds readings and checks, after every one, the
// invariants from SPEC.md.
func followerInvariants(t *testing.T, cfg pll.FollowerConfig, readings [][2]int64) {
	t.Helper()
	f, err := pll.NewFollower(cfg)
	if err != nil {
		t.Fatalf("NewFollower: %v", err)
	}
	maxY := cfg.MaxOffsetPPM * 1e-6
	var lastL, lastR int64
	have := false
	anchored := false
	for i, rd := range readings {
		l, r := time.Duration(rd[0]), rd[1]
		var before time.Duration
		var beforeOK bool
		if anchored {
			if v, err := f.Local(r); err == nil {
				before, beforeOK = v, true
			}
		}
		obs, err := f.Observe(l, r)
		outOfOrder := have && (int64(l) < lastL || r <= lastR)
		if outOfOrder {
			if !errors.Is(err, pll.ErrOrder) {
				t.Fatalf("reading %d (%d, %d) is out of order: error %v, want ErrOrder", i, l, r, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("reading %d (%d, %d): unexpected error %v", i, l, r, err)
		}
		have, lastL, lastR = true, int64(l), r
		if obs.Status < pll.StatusAnchored || obs.Status > pll.StatusStepped {
			t.Fatalf("reading %d: invalid status %d", i, obs.Status)
		}
		if !anchored && obs.Status != pll.StatusAnchored {
			t.Fatalf("reading %d: first reading has status %v, want anchored", i, obs.Status)
		}
		if anchored && obs.Status == pll.StatusAnchored {
			t.Fatalf("reading %d: anchored twice without Reset", i)
		}
		anchored = true
		y := f.Offset()
		if math.IsNaN(y) || math.Abs(y) > maxY {
			t.Fatalf("reading %d: Offset %v outside ±%v", i, y, maxY)
		}
		if j := f.Jitter(); j < 0 {
			t.Fatalf("reading %d: Jitter %v < 0", i, j)
		}
		after, aerr := f.Local(r)
		switch {
		case obs.Status == pll.StatusStepped && cfg.Window == 0:
			if aerr != nil || after != l {
				t.Fatalf("reading %d: after a step Local(r) = (%v, %v), want %v", i, after, aerr, l)
			}
		case obs.Status == pll.StatusAccepted && cfg.Window == 0 && beforeOK && aerr == nil:
			e := obs.Error
			if e < 0 {
				e = -e
			}
			move := after - before
			if move < 0 {
				move = -move
			}
			if move > e+2 {
				t.Fatalf("reading %d: one update moved the phase by %v, more than |e| = %v", i, move, e)
			}
		case obs.Status == pll.StatusRejected || obs.Status == pll.StatusPending:
			if beforeOK && (aerr != nil || after != before) {
				t.Fatalf("reading %d: a %v reading changed the mapping from %v to %v", i, obs.Status, before, after)
			}
		}
		// Local never decreases as remote increases.
		step := int64(cfg.Nominal.Num()/cfg.Nominal.Den()/100) + 1
		a, e1 := f.Local(r)
		b, e2 := f.Local(r + step)
		if e1 == nil && e2 == nil && b < a {
			t.Fatalf("reading %d: Local(%d) = %v > Local(%d) = %v", i, r, a, r+step, b)
		}
	}
}

// FuzzFollower decodes a config and a reading sequence from bytes.
func FuzzFollower(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20})
	f.Add([]byte{255, 3, 7, 1, 200, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 9, 9, 9, 9})
	f.Add([]byte{17, 0, 1, 99, 3, 255, 255, 255, 255, 255, 255, 255, 127, 0, 0, 0, 0, 0, 0, 0, 128})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 4 {
			return
		}
		nominal := []uint64{1000, 48000, 1_000_000, 1_000_000_000}[data[0]%4]
		cfg := pll.DefaultFollowerConfig(mustRate(t, nominal, 1))
		cfg.Bandwidth = []float64{0.1, 0.005, 2, 40}[data[1]%4]
		cfg.Damping = []float64{math.Sqrt2 / 2, 0.1, 1, 5}[(data[1]>>2)%4]
		cfg.StepAfter = int(data[2]%4) + 1
		cfg.LockCount = int(data[2]>>2%8) + 1
		if data[3]&1 != 0 {
			cfg.Window = time.Duration(data[3]) * 10 * time.Millisecond
		}
		cfg.OutlierThreshold = []time.Duration{5 * time.Millisecond, time.Microsecond, time.Second, time.Hour}[(data[3]>>1)%4]
		data = data[4:]
		var readings [][2]int64
		var l, r int64
		for len(data) >= 2 && len(readings) < 64 {
			mode := data[0]
			var buf [8]byte
			n := copy(buf[:], data[1:])
			data = data[1+n:]
			v := binary.LittleEndian.Uint64(buf[:])
			switch mode % 4 {
			case 0: // a plausible reading: forward in time, a little noise
				dl := int64(v%50_000_000) + 1
				l += dl
				r += int64(float64(dl)*float64(nominal)/1e9*(1+float64(int8(mode))*1e-5)) + 1
			case 1: // a jump in remote ticks
				l += int64(v % 1_000_000_000)
				r += int64(v>>20) + 1
			case 2: // anything, including out of order
				l = int64(v) >> (mode % 40)
				r = int64(v>>8) - int64(v&0xff)
			default: // same local time, next tick
				r++
			}
			readings = append(readings, [2]int64{l, r})
		}
		followerInvariants(t, cfg, readings)
	})
}

func TestFollowerAllocs(t *testing.T) {
	for _, window := range []time.Duration{0, 100 * time.Millisecond} {
		cfg := pll.DefaultFollowerConfig(mustRate(t, 48000, 1))
		cfg.Window = window
		f := mustFollower(t, cfg)
		var l time.Duration
		var r int64
		for i := 0; i < 200; i++ {
			l += 10 * time.Millisecond
			r += 480
			if _, err := f.Observe(l, r); err != nil {
				t.Fatalf("Observe: %v", err)
			}
		}
		if s := f.State(l); s != pll.StateLocked {
			t.Fatalf("window %v: setup State = %v, want locked; the follower must work before allocations matter", window, s)
		}
		var sink time.Duration
		allocs := testing.AllocsPerRun(200, func() {
			l += 10 * time.Millisecond
			r += 480
			obs, _ := f.Observe(l, r)
			sink += obs.Error
			v, _ := f.Local(r + 480)
			sink += v
			_, _ = f.Remote(l)
			sink += f.Jitter() + time.Duration(f.Trim())
			_ = f.State(l)
		})
		if allocs != 0 {
			t.Errorf("window %v: %v allocations per reading, want 0", window, allocs)
		}
		_ = sink
	}
}

// TestFollowerInvariants runs the invariant checks on seeded random readings,
// so they run on every test, not only while fuzzing.
func TestFollowerInvariants(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 7))
	for run := 0; run < 200; run++ {
		nominal := []uint64{1000, 48000, 1_000_000}[run%3]
		cfg := pll.DefaultFollowerConfig(mustRate(t, nominal, 1))
		cfg.Window = time.Duration(run%3) * 200 * time.Millisecond
		cfg.StepAfter = run%4 + 1
		var readings [][2]int64
		var l, r int64
		for k := 0; k < 300; k++ {
			l += rng.Int64N(20_000_000) + 1
			r += int64(float64(l-readings0(readings))*float64(nominal)/1e9) + 1
			if rng.IntN(40) == 0 {
				r += rng.Int64N(int64(nominal) * 3)
			}
			if rng.IntN(60) == 0 {
				l -= rng.Int64N(5_000_000)
			}
			readings = append(readings, [2]int64{l, r})
		}
		followerInvariants(t, cfg, readings)
		if t.Failed() {
			return
		}
	}
}

func readings0(rs [][2]int64) int64 {
	if len(rs) == 0 {
		return 0
	}
	return rs[len(rs)-1][0]
}

// scenarioBounds are the pass thresholds for each simulator scenario: about
// twice the worst result a reference implementation measured over the seeds
// used here.
var scenarioBounds = map[string]struct {
	lockBy           time.Duration // locked by this time
	rms, maxErr      time.Duration
	maxRate, endRate float64 // ppm
	steps            int     // exact number of steps
	relockWithin     time.Duration
}{
	"sound-card":     {lockBy: 200 * time.Millisecond, rms: 20 * time.Microsecond, maxErr: 45 * time.Microsecond, maxRate: 2, endRate: 1},
	"sensor-network": {lockBy: 3 * time.Minute, rms: 1500 * time.Microsecond, maxErr: 2500 * time.Microsecond, maxRate: 15, endRate: 10, steps: 1},
	"rate-change":    {lockBy: 200 * time.Millisecond, rms: 20 * time.Microsecond, maxErr: 45 * time.Microsecond, maxRate: 2, endRate: 1},
	"outage":         {lockBy: 200 * time.Millisecond, rms: 20 * time.Microsecond, maxErr: 60 * time.Microsecond, maxRate: 5, endRate: 1.5, relockWithin: time.Second},
	"clock-step":     {lockBy: 200 * time.Millisecond, rms: 20 * time.Microsecond, maxErr: 50 * time.Microsecond, maxRate: 3, endRate: 1, steps: 1, relockWithin: time.Second},
	"aging-crystal":  {lockBy: 200 * time.Millisecond, rms: 20 * time.Microsecond, maxErr: 50 * time.Microsecond, maxRate: 4, endRate: 1.5},
}

func TestScenarios(t *testing.T) {
	for _, sc := range sim.Scenarios() {
		b, ok := scenarioBounds[sc.Name]
		if !ok {
			t.Fatalf("no bounds for scenario %q", sc.Name)
		}
		for seed := uint64(1); seed <= 3; seed++ {
			r, err := sim.Run(sc, seed)
			if err != nil {
				t.Fatalf("%s seed %d: %v", sc.Name, seed, err)
			}
			fail := func(format string, args ...any) {
				t.Helper()
				t.Errorf("%s seed %d: "+format, append([]any{sc.Name, seed}, args...)...)
			}
			if r.Errors != 0 {
				fail("%d readings returned errors", r.Errors)
			}
			if r.LockTime < 0 || r.LockTime > b.lockBy {
				fail("locked at %v, want by %v", r.LockTime, b.lockBy)
			}
			if r.EndState != pll.StateLocked {
				fail("ended %v, want locked", r.EndState)
			}
			if r.RMSError > b.rms || r.MaxError > b.maxErr {
				fail("mapping error RMS %v max %v, want at most %v and %v", r.RMSError, r.MaxError, b.rms, b.maxErr)
			}
			if r.MaxRatePPM > b.maxRate || r.EndRatePPM > b.endRate {
				fail("rate error max %.3f end %.3f ppm, want at most %.3f and %.3f", r.MaxRatePPM, r.EndRatePPM, b.maxRate, b.endRate)
			}
			if r.Stepped != b.steps {
				fail("%d steps, want %d", r.Stepped, b.steps)
			}
			if b.relockWithin > 0 && (r.RelockAfter < 0 || r.RelockAfter > b.relockWithin) {
				fail("relocked %v after the disturbance, want within %v", r.RelockAfter, b.relockWithin)
			}
			if sc.Name == "sound-card" && r.FitRatePPM > 0.1 {
				fail("the independent least-squares fit is %.3f ppm off; the scenario itself is wrong", r.FitRatePPM)
			}
		}
	}
}

func TestDeviceSink(t *testing.T) {
	linked, err := sim.RunSink(sim.DeviceSink(true))
	if err != nil {
		t.Fatalf("linked: %v", err)
	}
	if linked.Underruns != 0 || linked.MaxQueue-linked.MinQueue > 10 {
		t.Errorf("linked: queue %d to %d with %d underruns; want a band of at most 10 units and none",
			linked.MinQueue, linked.MaxQueue, linked.Underruns)
	}
	if d := float64(linked.EndTrim)/1e6 - 40; math.Abs(d) > 1 {
		t.Errorf("linked: pacer trim %d ppt, want about 40 ppm", linked.EndTrim)
	}
	free, err := sim.RunSink(sim.DeviceSink(false))
	if err != nil {
		t.Fatalf("unlinked: %v", err)
	}
	if free.MaxQueue-free.MinQueue < 400 {
		t.Errorf("unlinked: queue band %d units; the device should have drained it by about 576", free.MaxQueue-free.MinQueue)
	}
}

func TestLateWakeups(t *testing.T) {
	for _, rc := range [][2]uint64{{48000, 1}, {30000, 1001}, {6_250_000, 1}} {
		r, err := sim.RunLateWakeups(mustRate(t, rc[0], rc[1]), 10*time.Millisecond, 20*time.Millisecond, 20*time.Second, 1)
		if err != nil {
			t.Fatalf("%d/%d: %v", rc[0], rc[1], err)
		}
		if r.Gaps != 0 || r.Early != 0 || r.HandedOut != r.Expected || r.HandedOut == 0 {
			t.Errorf("%d/%d: %+v; want every due slot exactly once, none early", rc[0], rc[1], r)
		}
	}
}
