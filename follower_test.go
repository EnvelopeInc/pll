package pll_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/version2beta/pll"
)

func mustRate(t testing.TB, num, den uint64) pll.Rate {
	t.Helper()
	r, err := pll.UnitsPerSecond(num, den)
	if err != nil {
		t.Fatalf("UnitsPerSecond(%d, %d): %v", num, den, err)
	}
	return r
}

func mustFollower(t testing.TB, cfg pll.FollowerConfig) *pll.Follower {
	t.Helper()
	f, err := pll.NewFollower(cfg)
	if err != nil || f == nil {
		t.Fatalf("NewFollower(%+v) = (%v, %v), want a follower and no error", cfg, f, err)
	}
	return f
}

func observe(t testing.TB, f *pll.Follower, l time.Duration, r int64, want pll.Status) pll.Observation {
	t.Helper()
	obs, err := f.Observe(l, r)
	if err != nil {
		t.Fatalf("Observe(%v, %d): unexpected error %v", l, r, err)
	}
	if obs.Status != want {
		t.Fatalf("Observe(%v, %d) status = %v, want %v", l, r, obs.Status, want)
	}
	return obs
}

func wantLocal(t testing.TB, f *pll.Follower, r int64, want time.Duration, tol time.Duration) {
	t.Helper()
	got, err := f.Local(r)
	if err != nil {
		t.Fatalf("Local(%d): unexpected error %v", r, err)
	}
	if d := got - want; d > tol || d < -tol {
		t.Fatalf("Local(%d) = %d ns, want %d ns (±%d)", r, int64(got), int64(want), int64(tol))
	}
}

// perfect is a remote clock at 1000 ticks/s that agrees exactly with local time.
func perfect(t testing.TB) pll.FollowerConfig {
	return pll.DefaultFollowerConfig(mustRate(t, 1000, 1))
}

func TestDefaultFollowerConfig(t *testing.T) {
	r := mustRate(t, 48000, 1)
	c := pll.DefaultFollowerConfig(r)
	want := pll.FollowerConfig{
		Nominal: r, Bandwidth: 0.1, Damping: math.Sqrt2 / 2, MaxOffsetPPM: 500,
		Window: 0, OutlierThreshold: 5 * time.Millisecond, StepAfter: 3,
		LockThreshold: 100 * time.Microsecond, LockCount: 8, HoldoverAfter: 5 * time.Second,
	}
	if c != want {
		t.Errorf("DefaultFollowerConfig(48000/1) = %+v,\nwant %+v", c, want)
	}
}

func TestNewFollowerValidation(t *testing.T) {
	good := perfect(t)
	tests := []struct {
		field, reason string
		mod           func(*pll.FollowerConfig)
	}{
		{"Nominal", "must be a valid Rate", func(c *pll.FollowerConfig) { c.Nominal = pll.Rate{} }},
		{"Bandwidth", "must be finite and > 0", func(c *pll.FollowerConfig) { c.Bandwidth = 0 }},
		{"Bandwidth", "must be finite and > 0", func(c *pll.FollowerConfig) { c.Bandwidth = math.Inf(1) }},
		{"Bandwidth", "must be finite and > 0", func(c *pll.FollowerConfig) { c.Bandwidth = math.NaN() }},
		{"Damping", "must be finite and > 0", func(c *pll.FollowerConfig) { c.Damping = -1 }},
		{"MaxOffsetPPM", "must be finite, > 0 and < 1000000", func(c *pll.FollowerConfig) { c.MaxOffsetPPM = 0 }},
		{"MaxOffsetPPM", "must be finite, > 0 and < 1000000", func(c *pll.FollowerConfig) { c.MaxOffsetPPM = 1e6 }},
		{"Window", "must be >= 0", func(c *pll.FollowerConfig) { c.Window = -1 }},
		{"OutlierThreshold", "must be > 0", func(c *pll.FollowerConfig) { c.OutlierThreshold = 0 }},
		{"StepAfter", "must be >= 1", func(c *pll.FollowerConfig) { c.StepAfter = 0 }},
		{"LockThreshold", "must be >= 0", func(c *pll.FollowerConfig) { c.LockThreshold = -1 }},
		{"LockCount", "must be >= 1", func(c *pll.FollowerConfig) { c.LockCount = 0 }},
		{"HoldoverAfter", "must be > 0", func(c *pll.FollowerConfig) { c.HoldoverAfter = 0 }},
		// Two bad fields: the first in declaration order is reported.
		{"Bandwidth", "must be finite and > 0", func(c *pll.FollowerConfig) { c.Bandwidth = 0; c.LockCount = 0 }},
	}
	for _, tt := range tests {
		c := good
		tt.mod(&c)
		f, err := pll.NewFollower(c)
		var ce *pll.ConfigError
		if f != nil || !errors.As(err, &ce) {
			t.Errorf("bad %s: NewFollower = (%v, %v), want nil and a *ConfigError", tt.field, f, err)
			continue
		}
		if ce.Field != tt.field || ce.Reason != tt.reason {
			t.Errorf("bad %s: ConfigError{%q, %q}, want {%q, %q}", tt.field, ce.Field, ce.Reason, tt.field, tt.reason)
		}
		if want := "pll: FollowerConfig." + tt.field + " " + tt.reason; err.Error() != want {
			t.Errorf("Error() = %q, want %q", err.Error(), want)
		}
	}
	zeroWindowZeroLock := good
	zeroWindowZeroLock.Window, zeroWindowZeroLock.LockThreshold = 0, 0
	mustFollower(t, zeroWindowZeroLock)
}

func TestStatusStateStrings(t *testing.T) {
	for s, want := range map[pll.Status]string{
		pll.StatusAnchored: "anchored", pll.StatusAccepted: "accepted", pll.StatusPending: "pending",
		pll.StatusRejected: "rejected", pll.StatusStepped: "stepped", 0: "Status(0)", 9: "Status(9)",
	} {
		if s.String() != want {
			t.Errorf("Status(%d).String() = %q, want %q", s, s.String(), want)
		}
	}
	for s, want := range map[pll.State]string{
		pll.StateAcquiring: "acquiring", pll.StateLocked: "locked", pll.StateHoldover: "holdover", 7: "State(7)",
	} {
		if s.String() != want {
			t.Errorf("State(%d).String() = %q, want %q", s, s.String(), want)
		}
	}
}

func TestFollowerAnchors(t *testing.T) {
	f := mustFollower(t, perfect(t))
	if _, err := f.Local(0); !errors.Is(err, pll.ErrNotAnchored) {
		t.Fatalf("Local before any reading: error %v, want ErrNotAnchored", err)
	}
	if _, err := f.Remote(0); !errors.Is(err, pll.ErrNotAnchored) {
		t.Fatalf("Remote before any reading: error %v, want ErrNotAnchored", err)
	}
	if s := f.State(0); s != pll.StateAcquiring {
		t.Fatalf("State before any reading = %v, want acquiring", s)
	}
	obs := observe(t, f, 3*time.Second, 70_000, pll.StatusAnchored)
	if obs.Error != 0 {
		t.Errorf("anchoring Error = %v, want 0", obs.Error)
	}
	wantLocal(t, f, 70_000, 3*time.Second, 0)
	wantLocal(t, f, 71_000, 4*time.Second, 0) // 1000 ticks later, 1 s later
	if r, err := f.Remote(5 * time.Second); err != nil || r != 72_000 {
		t.Errorf("Remote(5s) = (%d, %v), want 72000", r, err)
	}
	if f.Offset() != 0 || f.Trim() != 0 || f.Jitter() != 0 {
		t.Errorf("after anchoring: Offset %v, Trim %d, Jitter %v; want all zero", f.Offset(), f.Trim(), f.Jitter())
	}
}

func TestFollowerOrder(t *testing.T) {
	f := mustFollower(t, perfect(t))
	observe(t, f, time.Second, 1000, pll.StatusAnchored)
	if _, err := f.Observe(time.Second-1, 2000); !errors.Is(err, pll.ErrOrder) {
		t.Errorf("local time going back: error %v, want ErrOrder", err)
	}
	if _, err := f.Observe(2*time.Second, 1000); !errors.Is(err, pll.ErrOrder) {
		t.Errorf("remote ticks not increasing: error %v, want ErrOrder", err)
	}
	// Rejected readings change nothing: the next good one is judged as usual.
	obs := observe(t, f, 2*time.Second, 2000, pll.StatusAccepted)
	if obs.Error != 0 {
		t.Errorf("after two rejected readings, Error = %v, want 0", obs.Error)
	}
	// Several readings may share a local time.
	observe(t, f, 2*time.Second, 2001, pll.StatusAccepted)
}

func TestPerfectClock(t *testing.T) {
	f := mustFollower(t, perfect(t))
	observe(t, f, 0, 0, pll.StatusAnchored)
	for k := 1; k <= 20; k++ {
		l := time.Duration(k) * 10 * time.Millisecond
		obs := observe(t, f, l, int64(10*k), pll.StatusAccepted)
		if obs.Error != 0 {
			t.Fatalf("reading %d: Error = %v, want 0", k, obs.Error)
		}
		want := pll.StateAcquiring
		if k >= 8 {
			want = pll.StateLocked // LockCount 8 consecutive accepted samples
		}
		if s := f.State(l); s != want {
			t.Fatalf("after %d accepted readings, State = %v, want %v", k, s, want)
		}
	}
	if f.Offset() != 0 {
		t.Errorf("Offset = %v, want exactly 0", f.Offset())
	}
	wantLocal(t, f, 1_000_000, 1000*time.Second, 0)
}

func TestLoopEquations(t *testing.T) {
	cfg := perfect(t)
	f := mustFollower(t, cfg)
	m := newLoopModel(cfg, true)
	readings := []struct {
		l time.Duration
		r int64
	}{
		{0, 0},
		{time.Second + 100*time.Microsecond, 1000}, // 100 µs late, after a long gap: capped gains
		{1100 * time.Millisecond, 1100},            // a short gap: the plain equations
		{1200*time.Millisecond - 30*time.Microsecond, 1200},
		{1250 * time.Millisecond, 1250},
		{1260 * time.Millisecond, 1260},
	}
	for i, rd := range readings {
		want := m.observe(rd.l, rd.r)
		status := pll.StatusAccepted
		if i == 0 {
			status = pll.StatusAnchored
		}
		obs := observe(t, f, rd.l, rd.r, status)
		if d := obs.Error - time.Duration(math.Round(want)); i > 0 && (d > 1 || d < -1) {
			t.Fatalf("reading %d: Error = %d ns, want %d ns", i, int64(obs.Error), int64(math.Round(want)))
		}
		if math.Abs(f.Offset()-m.y) > 1e-15 {
			t.Fatalf("reading %d: Offset = %.17g, want %.17g", i, f.Offset(), m.y)
		}
		for _, r := range []int64{rd.r, rd.r + 1, rd.r + 1000} {
			wantLocal(t, f, r, m.local(r), 1)
		}
	}
	if f.Offset() == 0 {
		t.Fatal("the loop never moved the rate estimate")
	}
}

func TestOffsetConverges(t *testing.T) {
	// A remote counter at 1 MHz nominal that runs 50 ppm fast, read every
	// 10 ms with no jitter.
	cfg := pll.DefaultFollowerConfig(mustRate(t, 1_000_000, 1))
	f := mustFollower(t, cfg)
	for k := 0; k <= 6000; k++ {
		l := time.Duration(k) * 10 * time.Millisecond
		r := int64(math.Floor(l.Seconds() * 1_000_000 * (1 + 50e-6)))
		if _, err := f.Observe(l, r); err != nil {
			t.Fatalf("Observe(%v, %d): %v", l, r, err)
		}
	}
	if d := math.Abs(f.Offset() - 50e-6); d > 0.01e-6 {
		t.Errorf("after 60 s Offset = %.6f ppm, want 50 ppm ±0.01", f.Offset()*1e6)
	}
	if s := f.State(60 * time.Second); s != pll.StateLocked {
		t.Errorf("State = %v, want locked", s)
	}
}

func TestOffsetClamp(t *testing.T) {
	cfg := pll.DefaultFollowerConfig(mustRate(t, 1_000_000, 1))
	f := mustFollower(t, cfg)
	maxSeen := 0.0
	for k := 0; k <= 3000; k++ {
		l := time.Duration(k) * 10 * time.Millisecond
		r := int64(math.Floor(l.Seconds() * 1_000_000 * (1 + 2000e-6))) // 2000 ppm fast
		obs, err := f.Observe(l, r)
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		if obs.Status == pll.StatusStepped || obs.Status == pll.StatusRejected {
			continue
		}
		maxSeen = math.Max(maxSeen, math.Abs(f.Offset()))
	}
	if maxSeen > cfg.MaxOffsetPPM*1e-6 {
		t.Errorf("Offset reached %.3f ppm, beyond MaxOffsetPPM 500", maxSeen*1e6)
	}
	if f.Offset() != cfg.MaxOffsetPPM*1e-6 {
		t.Errorf("Offset = %.6f ppm, want exactly 500 at the clamp", f.Offset()*1e6)
	}
}

func TestRemoteInverse(t *testing.T) {
	cfg := pll.DefaultFollowerConfig(mustRate(t, 48000, 1))
	f := mustFollower(t, cfg)
	for k := 0; k <= 500; k++ {
		l := time.Duration(k) * 10 * time.Millisecond
		r := int64(math.Floor((l.Seconds() + 2) * 48000 * (1 - 120e-6)))
		if _, err := f.Observe(l, r); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}
	for _, r := range []int64{96_000, 300_000, 5_000_000} {
		l, err := f.Local(r)
		if err != nil {
			t.Fatalf("Local(%d): %v", r, err)
		}
		back, err := f.Remote(l)
		if err != nil || back < r-1 || back > r+1 {
			t.Errorf("Remote(Local(%d)) = (%d, %v), want %d ±1", r, back, err, r)
		}
	}
}

func TestTrim(t *testing.T) {
	cfg := pll.DefaultFollowerConfig(mustRate(t, 1_000_000, 1))
	f := mustFollower(t, cfg)
	for k := 0; k <= 300; k++ {
		l := time.Duration(k) * 10 * time.Millisecond
		r := int64(math.Floor(l.Seconds() * 1_000_000 * (1 - 33e-6)))
		if _, err := f.Observe(l, r); err != nil {
			t.Fatalf("Observe: %v", err)
		}
		if want := int64(math.Round(f.Offset() * 1e12)); f.Trim() != want {
			t.Fatalf("Trim() = %d, want round(Offset × 1e12) = %d", f.Trim(), want)
		}
	}
	if f.Trim() >= 0 {
		t.Errorf("a slow remote clock gave Trim() = %d, want negative", f.Trim())
	}
}

func TestJitter(t *testing.T) {
	f := mustFollower(t, perfect(t))
	observe(t, f, 0, 0, pll.StatusAnchored)
	delays := []time.Duration{40, -30, 70, 0, -50, 20, 90, -10} // µs
	var errs []float64
	for k, d := range delays {
		l := time.Duration(k+1)*10*time.Millisecond + d*time.Microsecond
		obs := observe(t, f, l, int64(10*(k+1)), pll.StatusAccepted)
		errs = append(errs, float64(obs.Error))
		var want float64
		if len(errs) >= 2 {
			m := errs[0] * errs[0]
			for _, e := range errs[1:] {
				m += (e*e - m) / 16
			}
			want = math.Sqrt(m)
		}
		if got := float64(f.Jitter()); math.Abs(got-want) > 2 {
			t.Fatalf("after %d accepted samples Jitter = %v, want %.0f ns", len(errs), f.Jitter(), want)
		}
	}
	if f.Jitter() == 0 {
		t.Fatal("Jitter stayed 0")
	}
}

func TestWindow(t *testing.T) {
	cfg := perfect(t)
	cfg.Window = time.Second
	f := mustFollower(t, cfg)
	observe(t, f, 0, 0, pll.StatusAnchored)
	// One window: readings at 0.1 s … 0.9 s with delays; the least delayed
	// (smallest phase error) is the one at 0.5 s, 20 µs late.
	delays := map[int]time.Duration{1: 900, 2: 300, 3: 700, 4: 50, 5: 20, 6: 400, 7: 600, 8: 80, 9: 1000}
	for k := 1; k <= 9; k++ {
		l := time.Duration(k)*100*time.Millisecond + delays[k]*time.Microsecond
		observe(t, f, l, int64(100*k), pll.StatusPending)
	}
	// The first reading at or past the window's end feeds the best one.
	observe(t, f, 1100*time.Millisecond+500*time.Microsecond, 1100, pll.StatusPending) // window opened at 0.1009 s
	obs := observe(t, f, 1200*time.Millisecond+500*time.Microsecond, 1200, pll.StatusAccepted)
	if obs.Error != 20*time.Microsecond {
		t.Fatalf("window fed a sample with Error %v, want the 20µs one", obs.Error)
	}
	// That reading opened the next window.
	observe(t, f, 1500*time.Millisecond, 1500, pll.StatusPending)
}

func TestOutliers(t *testing.T) {
	f := mustFollower(t, perfect(t))
	observe(t, f, 0, 0, pll.StatusAnchored)
	for k := 1; k <= 10; k++ {
		observe(t, f, time.Duration(k)*10*time.Millisecond, int64(10*k), pll.StatusAccepted)
	}
	before := f.Offset()
	obs := observe(t, f, 110*time.Millisecond+50*time.Millisecond, 110, pll.StatusRejected)
	if obs.Error != 50*time.Millisecond {
		t.Errorf("rejected sample Error = %v, want 50ms", obs.Error)
	}
	if f.Offset() != before || f.State(160*time.Millisecond) != pll.StateLocked {
		t.Errorf("a rejected sample changed the loop: Offset %v → %v, State %v", before, f.Offset(), f.State(160*time.Millisecond))
	}
	// Two disagreeing outliers do not step, even with StepAfter 3.
	observe(t, f, 200*time.Millisecond+80*time.Millisecond, 200, pll.StatusRejected)
	observe(t, f, 350*time.Millisecond-60*time.Millisecond, 350, pll.StatusRejected)
	observe(t, f, 400*time.Millisecond, 400, pll.StatusAccepted)
}

func TestSteps(t *testing.T) {
	f := mustFollower(t, perfect(t))
	observe(t, f, 0, 0, pll.StatusAnchored)
	for k := 1; k <= 10; k++ {
		observe(t, f, time.Duration(k)*10*time.Millisecond, int64(10*k), pll.StatusAccepted)
	}
	before := f.Offset()
	// The remote clock jumps 2 s ahead: readings show 2000 more ticks.
	observe(t, f, 110*time.Millisecond, 2110, pll.StatusRejected)
	observe(t, f, 120*time.Millisecond, 2120, pll.StatusRejected)
	obs := observe(t, f, 130*time.Millisecond, 2130, pll.StatusStepped)
	if obs.Error != -2*time.Second {
		t.Errorf("stepping sample Error = %v, want -2s", obs.Error)
	}
	if f.Offset() != before {
		t.Errorf("a step changed Offset from %v to %v; it must keep the rate", before, f.Offset())
	}
	wantLocal(t, f, 2130, 130*time.Millisecond, 0)
	if s := f.State(130 * time.Millisecond); s != pll.StateAcquiring {
		t.Errorf("after a step State = %v, want acquiring", s)
	}
	if f.Jitter() != 0 {
		t.Errorf("after a step Jitter = %v, want 0", f.Jitter())
	}
	observe(t, f, 140*time.Millisecond, 2140, pll.StatusAccepted)

	// A step keeps a rate estimate that is not zero: a 1 MHz counter 100 ppm
	// fast, then a 1 s jump.
	h := mustFollower(t, pll.DefaultFollowerConfig(mustRate(t, 1_000_000, 1)))
	var l time.Duration
	var r int64
	for k := 0; k <= 300; k++ {
		l = time.Duration(k) * 10 * time.Millisecond
		r = int64(math.Floor(l.Seconds() * 1e6 * (1 + 100e-6)))
		if _, err := h.Observe(l, r); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}
	trained := h.Offset()
	if trained < 50e-6 {
		t.Fatalf("setup: Offset %.3f ppm after 3 s of a 100 ppm clock", trained*1e6)
	}
	for k := 1; k <= 3; k++ {
		l += 10 * time.Millisecond
		r = int64(math.Floor(l.Seconds()*1e6*(1+100e-6))) + 1_000_000
		obs, err := h.Observe(l, r)
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		if k == 3 && obs.Status != pll.StatusStepped {
			t.Fatalf("third reading after the jump: status %v, want stepped", obs.Status)
		}
	}
	if h.Offset() != trained {
		t.Errorf("a step changed Offset from %.6f to %.6f ppm; it must keep the rate", trained*1e6, h.Offset()*1e6)
	}

	// StepAfter 1 steps on the first outlier.
	cfg := perfect(t)
	cfg.StepAfter = 1
	g := mustFollower(t, cfg)
	observe(t, g, 0, 0, pll.StatusAnchored)
	observe(t, g, 10*time.Millisecond, 10, pll.StatusAccepted)
	observe(t, g, 20*time.Millisecond, 5020, pll.StatusStepped)
}

func TestLockAndHoldover(t *testing.T) {
	f := mustFollower(t, perfect(t))
	observe(t, f, 0, 0, pll.StatusAnchored)
	k := 0
	next := func(late time.Duration) pll.Observation {
		k++
		return observe(t, f, time.Duration(k)*10*time.Millisecond+late, int64(10*k), pll.StatusAccepted)
	}
	for i := 0; i < 8; i++ {
		next(0)
	}
	now := time.Duration(k) * 10 * time.Millisecond
	if s := f.State(now); s != pll.StateLocked {
		t.Fatalf("after 8 good samples State = %v, want locked", s)
	}
	next(400 * time.Microsecond) // accepted, but beyond LockThreshold
	if s := f.State(time.Duration(k) * 10 * time.Millisecond); s != pll.StateAcquiring {
		t.Fatalf("after a sample beyond LockThreshold State = %v, want acquiring", s)
	}
	last := time.Duration(k)*10*time.Millisecond + 400*time.Microsecond
	if s := f.State(last + 5*time.Second); s != pll.StateAcquiring {
		t.Errorf("exactly HoldoverAfter later State = %v, want acquiring", s)
	}
	if s := f.State(last + 5*time.Second + 1); s != pll.StateHoldover {
		t.Errorf("past HoldoverAfter State = %v, want holdover", s)
	}
}

func TestReset(t *testing.T) {
	f := mustFollower(t, perfect(t))
	for k := 0; k <= 50; k++ {
		l := time.Duration(k) * 10 * time.Millisecond
		if _, err := f.Observe(l, int64(math.Floor(l.Seconds()*1000*(1+300e-6)))+int64(k)); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}
	if f.Offset() == 0 {
		t.Fatal("setup: the loop never moved")
	}
	f.Reset()
	if _, err := f.Local(0); !errors.Is(err, pll.ErrNotAnchored) {
		t.Errorf("after Reset Local: error %v, want ErrNotAnchored", err)
	}
	if f.Offset() != 0 || f.Jitter() != 0 || f.State(time.Hour) != pll.StateAcquiring {
		t.Errorf("after Reset: Offset %v, Jitter %v, State %v; want 0, 0, acquiring", f.Offset(), f.Jitter(), f.State(time.Hour))
	}
	// Ordering starts over too: an earlier reading anchors.
	observe(t, f, 0, 0, pll.StatusAnchored)
}

func TestFollowerLargeTimes(t *testing.T) {
	// 400 days of local time is past 2^53 ns, where a float64 count of
	// nanoseconds can no longer hold every value. The mapping must not care.
	base := 400 * 24 * time.Hour
	f := mustFollower(t, perfect(t))
	observe(t, f, base+7, 1_000_000_000, pll.StatusAnchored)
	wantLocal(t, f, 1_000_000_000, base+7, 0)
	for k := 1; k <= 10; k++ {
		obs := observe(t, f, base+7+time.Duration(k)*10*time.Millisecond, 1_000_000_000+int64(10*k), pll.StatusAccepted)
		if obs.Error != 0 {
			t.Fatalf("reading %d: Error = %d ns, want 0", k, int64(obs.Error))
		}
	}
	wantLocal(t, f, 1_000_000_100, base+7+100*time.Millisecond, 0)
	// A 3 s jump, then a step anchors exactly on the stepping reading.
	l := base + 7 + 110*time.Millisecond + 3
	observe(t, f, l, 1_000_003_110, pll.StatusRejected)
	observe(t, f, l+10*time.Millisecond, 1_000_003_120, pll.StatusRejected)
	observe(t, f, l+20*time.Millisecond, 1_000_003_130, pll.StatusStepped)
	wantLocal(t, f, 1_000_003_130, l+20*time.Millisecond, 0)
	if r, err := f.Remote(l + 20*time.Millisecond); err != nil || r != 1_000_003_130 {
		t.Errorf("Remote at the step = (%d, %v), want 1000003130", r, err)
	}
}

func TestFollowerLongRun(t *testing.T) {
	// 1000 days of readings every 6 hours from a clock at 3 ticks/s that
	// agrees with local time. Tick r is at r × 10^9/3 ns, which leaves a third
	// of a nanosecond over for most r, and the run goes far past 2^53 ns. A
	// mapping that keeps its anchor near the latest reading stays within a
	// couple of nanoseconds; one that holds the whole elapsed time in a
	// float64 drifts by much more.
	f := mustFollower(t, pll.DefaultFollowerConfig(mustRate(t, 3, 1)))
	truth := func(r int64) time.Duration { // round(r × 1e9 / 3) + 7, exactly
		q, rem := r*1_000_000_000/3, r*1_000_000_000%3
		if rem == 2 {
			q++
		}
		return time.Duration(q) + 7
	}
	observe(t, f, truth(0), 0, pll.StatusAnchored)
	for k := int64(1); k <= 1000*4; k++ {
		r := k * 64_801 // a little over 6 hours of ticks, rarely a multiple of 3
		obs := observe(t, f, truth(r), r, pll.StatusAccepted)
		if obs.Error > 1 || obs.Error < -1 {
			t.Fatalf("reading %d: Error = %d ns, want within 1 ns", k, int64(obs.Error))
		}
		wantLocal(t, f, r, truth(r), 2)
		wantLocal(t, f, r+1, truth(r+1), 2)
	}
}
