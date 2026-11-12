package pll_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/version2beta/pll"
)

const ms = time.Millisecond

func mustPacer(t *testing.T, num, den uint64) *pll.Pacer {
	t.Helper()
	r, err := pll.UnitsPerSecond(num, den)
	if err != nil {
		t.Fatalf("UnitsPerSecond(%d, %d): %v", num, den, err)
	}
	p, err := pll.NewPacer(r)
	if err != nil || p == nil {
		t.Fatalf("NewPacer(%v) = (%v, %v), want a pacer and no error", r, p, err)
	}
	return p
}

func wantSpan(t *testing.T, what string, s pll.Span, err error, first, count uint64) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error %v", what, err)
	}
	if s.First != first || s.Count != count {
		t.Fatalf("%s = span First=%d Count=%d, want First=%d Count=%d", what, s.First, s.Count, first, count)
	}
}

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: error %v, want %v", what, err, want)
	}
}

func wantTimes(t *testing.T, what string, s pll.Span, want []time.Duration) {
	t.Helper()
	if s.Count != uint64(len(want)) {
		t.Fatalf("%s: span has %d slots, want %d", what, s.Count, len(want))
	}
	for i, w := range want {
		if got := s.Time(uint64(i)); got != w {
			t.Errorf("%s: Time(%d) (slot %d) = %d ns, want %d ns", what, i, s.First+uint64(i), int64(got), int64(w))
		}
	}
}

func wantNext(t *testing.T, p *pll.Pacer, slot uint64, at time.Duration) {
	t.Helper()
	gs, gat, err := p.NextDue()
	if err != nil || gs != slot || gat != at {
		t.Fatalf("NextDue() = (%d, %d, %v), want (%d, %d, nil)", gs, int64(gat), err, slot, int64(at))
	}
}

func TestNewPacer(t *testing.T) {
	p, err := pll.NewPacer(pll.Rate{})
	if !errors.Is(err, pll.ErrInvalidRate) || p != nil {
		t.Errorf("NewPacer(zero Rate) = (%v, %v), want (nil, ErrInvalidRate)", p, err)
	}
	p = mustPacer(t, 48000, 1)
	if got := p.Trim(); got != 0 {
		t.Errorf("a new pacer's Trim() = %d, want 0", got)
	}
}

func TestClaim(t *testing.T) {
	p := mustPacer(t, 3, 8)
	s, err := p.Claim(1)
	wantSpan(t, "Claim(1)", s, err, 0, 1)
	s, err = p.Claim(4)
	wantSpan(t, "then Claim(4)", s, err, 1, 4)
	_, err = p.Claim(0)
	wantErr(t, "Claim(0)", err, pll.ErrInvalidArgument)
	s, err = p.Claim(2)
	wantSpan(t, "Claim(2) after a failed Claim(0)", s, err, 5, 2)

	q := mustPacer(t, 1_000_000_000, 1)
	s, err = q.Claim(1_000_000)
	wantSpan(t, "Claim(1000000) at 1e9 units/s", s, err, 0, 1_000_000)
}

func TestSpanTimeNominal(t *testing.T) {
	// 3e8 units/s: a period of 10/3 ns.
	p := mustPacer(t, 300_000_000, 1)
	s, _ := p.Claim(5)
	wantTimes(t, "3e8 units/s, slots 0-4", s, []time.Duration{0, 3, 6, 10, 13})
	s, _ = p.Claim(2)
	wantTimes(t, "3e8 units/s, slots 5-6", s, []time.Duration{16, 20})

	// 30000/1001 units/s: a period of 33366666.67 ns, never accumulated.
	p = mustPacer(t, 30000, 1001)
	s, _ = p.Claim(4)
	wantTimes(t, "30000/1001 units/s", s, []time.Duration{0, 33_366_666, 66_733_333, 100_100_000})

	// 2e9 units/s: two slots share each nanosecond.
	p = mustPacer(t, 2_000_000_000, 1)
	s, _ = p.Claim(5)
	wantTimes(t, "2e9 units/s", s, []time.Duration{0, 0, 1, 1, 2})

	// 3/8 units/s: a period of 2666666666.67 ns.
	p = mustPacer(t, 3, 8)
	s, _ = p.Claim(3)
	wantTimes(t, "3/8 units/s", s, []time.Duration{0, 2_666_666_666, 5_333_333_333})
}

func TestSpanTimePanics(t *testing.T) {
	panics := func(what string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic", what)
			}
		}()
		f()
	}
	p := mustPacer(t, 1000, 1)
	s, err := p.Claim(2)
	wantSpan(t, "Claim(2)", s, err, 0, 2)
	if got := s.Time(1); got != time.Millisecond {
		t.Fatalf("Time(1) = %v, want 1ms", got)
	}
	panics("Time(Count)", func() { s.Time(2) })
	panics("Time(MaxUint64)", func() { s.Time(math.MaxUint64) })
	panics("zero Span Time(0)", func() { pll.Span{}.Time(0) })
	empty, _ := p.Due(0, 1) // nothing new is due at 0
	panics("Time(0) on an empty span from Due", func() { empty.Time(0) })
}

func TestSpanAll(t *testing.T) {
	p := mustPacer(t, 300_000_000, 1)
	s, _ := p.Claim(5)
	var slots []uint64
	var times []time.Duration
	for slot, at := range s.All() {
		slots = append(slots, slot)
		times = append(times, at)
	}
	wantSlots := []uint64{0, 1, 2, 3, 4}
	wantT := []time.Duration{0, 3, 6, 10, 13}
	if len(slots) != len(wantSlots) {
		t.Fatalf("All() yielded %d slots, want %d", len(slots), len(wantSlots))
	}
	for i := range wantSlots {
		if slots[i] != wantSlots[i] || times[i] != wantT[i] {
			t.Errorf("All() item %d = (%d, %d), want (%d, %d)", i, slots[i], times[i], wantSlots[i], wantT[i])
		}
	}
	n := 0
	for range s.All() {
		n++
		if n == 2 {
			break
		}
	}
	if n != 2 {
		t.Errorf("breaking out of All() after 2 items ran %d items", n)
	}
	for slot := range (pll.Span{}).All() {
		t.Errorf("the zero Span yielded slot %d, want nothing", slot)
	}
}

func TestNextDue(t *testing.T) {
	p := mustPacer(t, 300_000_000, 1)
	wantNext(t, p, 0, 0)
	p.Claim(1)
	wantNext(t, p, 1, 4) // exact 10/3 ns: due from 4 ns, the ceiling
	p.Claim(1)
	wantNext(t, p, 2, 7)
	p.Claim(1)
	wantNext(t, p, 3, 10)

	q := mustPacer(t, 3, 8)
	q.Claim(1)
	wantNext(t, q, 1, 2_666_666_667)
}

func TestDue(t *testing.T) {
	p := mustPacer(t, 300_000_000, 1) // slot k at 10k/3 ns
	s, err := p.Due(0, 10)
	wantSpan(t, "Due(0, 10)", s, err, 0, 1)
	s, err = p.Due(3, 10)
	wantSpan(t, "Due(3, 10): slot 1 is at 10/3 ns, not yet due", s, err, 1, 0)
	s, err = p.Due(4, 10)
	wantSpan(t, "Due(4, 10)", s, err, 1, 1)
	s, err = p.Due(100, 1000)
	wantSpan(t, "Due(100, 1000): slots 2-30", s, err, 2, 29)
	s, err = p.Due(100, 1000)
	wantSpan(t, "Due(100, 1000) again", s, err, 31, 0)

	q := mustPacer(t, 1000, 1)
	s, err = q.Due(ms-1, 10)
	wantSpan(t, "Due(1ms-1ns, 10) at 1000 units/s", s, err, 0, 1)
	s, err = q.Due(ms, 10)
	wantSpan(t, "Due(1ms, 10)", s, err, 1, 1)
}

func TestDueLimits(t *testing.T) {
	p := mustPacer(t, 1000, 1)
	s, err := p.Due(10*ms, 4)
	wantSpan(t, "Due(10ms, 4)", s, err, 0, 4)
	s, err = p.Due(10*ms, 4)
	wantSpan(t, "Due(10ms, 4) again", s, err, 4, 4)
	s, err = p.Due(10*ms, 4)
	wantSpan(t, "Due(10ms, 4) a third time", s, err, 8, 3)
	s, err = p.Due(5*ms, 4)
	wantSpan(t, "Due(5ms, 4): an earlier now never rewinds", s, err, 11, 0)
	_, err = p.Due(-1, 4)
	wantErr(t, "Due(-1, 4)", err, pll.ErrInvalidArgument)
	_, err = p.Due(20*ms, 0)
	wantErr(t, "Due(20ms, 0)", err, pll.ErrInvalidArgument)
	s, err = p.Due(11*ms, 100)
	wantSpan(t, "Due(11ms, 100) after two failed calls", s, err, 11, 1)
}

func TestSkip(t *testing.T) {
	p := mustPacer(t, 1000, 1)
	n, err := p.Skip(10*ms, 3)
	if err != nil || n != 8 {
		t.Fatalf("Skip(10ms, 3) = (%d, %v), want (8, nil): 11 slots due, keep the newest 3", n, err)
	}
	s, err := p.Due(10*ms, 100)
	wantSpan(t, "Due(10ms, 100) after the skip", s, err, 8, 3)
	if n, err = p.Skip(10*ms, 0); err != nil || n != 0 {
		t.Fatalf("Skip(10ms, 0) with nothing due = (%d, %v), want (0, nil)", n, err)
	}
	if n, err = p.Skip(15*ms, 10); err != nil || n != 0 {
		t.Fatalf("Skip(15ms, 10) with 5 due = (%d, %v), want (0, nil)", n, err)
	}
	if n, err = p.Skip(15*ms, 0); err != nil || n != 5 {
		t.Fatalf("Skip(15ms, 0) = (%d, %v), want (5, nil)", n, err)
	}
	wantNext(t, p, 16, 16*ms)
	_, err = p.Skip(-1, 0)
	wantErr(t, "Skip(-1, 0)", err, pll.ErrInvalidArgument)
	wantNext(t, p, 16, 16*ms)
}

func TestSetTrim(t *testing.T) {
	p := mustPacer(t, 1000, 1)
	for _, bad := range []int64{1_000_000_000_000, -1_000_000_000_000, math.MaxInt64, math.MinInt64} {
		wantErr(t, "SetTrim out of range", p.SetTrim(bad), pll.ErrInvalidArgument)
		if p.Trim() != 0 {
			t.Fatalf("after a rejected SetTrim(%d), Trim() = %d, want 0", bad, p.Trim())
		}
	}

	// 10% faster from slot 1000 on: the period becomes 1e6/1.1 ns.
	p.Claim(1000)
	if err := p.SetTrim(100_000_000_000); err != nil {
		t.Fatalf("SetTrim(1e11): %v", err)
	}
	if p.Trim() != 100_000_000_000 {
		t.Fatalf("Trim() = %d, want 100000000000", p.Trim())
	}
	wantNext(t, p, 1000, time.Second)
	s, _ := p.Claim(12)
	if s.Time(0) != time.Second || s.Time(1) != 1_000_909_090 || s.Time(11) != 1_010_000_000 {
		t.Errorf("after SetTrim(1e11): times %d, %d, %d; want 1000000000, 1000909090, 1010000000",
			s.Time(0), s.Time(1), s.Time(11))
	}

	// Half speed from the start: a 2 ms period.
	q := mustPacer(t, 1000, 1)
	if err := q.SetTrim(-500_000_000_000); err != nil {
		t.Fatalf("SetTrim(-5e11): %v", err)
	}
	s, _ = q.Claim(3)
	wantTimes(t, "after SetTrim(-5e11)", s, []time.Duration{0, 2 * ms, 4 * ms})

	// The new timing starts at the floor of the cursor slot's exact time.
	r := mustPacer(t, 3, 1) // slot 1 is at 333333333.33 ns
	r.Claim(1)
	if err := r.SetTrim(1); err != nil {
		t.Fatalf("SetTrim(1): %v", err)
	}
	wantNext(t, r, 1, 333_333_333)

	// Setting the trim it already has changes nothing: no re-anchoring.
	u := mustPacer(t, 3, 1)
	u.Claim(1)
	if err := u.SetTrim(0); err != nil {
		t.Fatalf("SetTrim(0): %v", err)
	}
	s, _ = u.Claim(3)
	if s.Time(2) != time.Second {
		t.Errorf("SetTrim(0) on an untrimmed pacer moved slot 3 to %d ns; want exactly 1s", s.Time(2))
	}
}

func TestSetTrimKeepsSpans(t *testing.T) {
	p := mustPacer(t, 1000, 1)
	first, _ := p.Claim(5)
	if err := p.SetTrim(200_000_000_000); err != nil {
		t.Fatalf("SetTrim(2e11): %v", err)
	}
	second, _ := p.Claim(3)
	if err := p.SetTrim(-300_000_000_000); err != nil {
		t.Fatalf("SetTrim(-3e11): %v", err)
	}
	p.Claim(2)
	wantTimes(t, "a span issued before two trims", first, []time.Duration{0, ms, 2 * ms, 3 * ms, 4 * ms})
	// 1.2× from slot 5 at 5 ms: a period of 833333.33 ns.
	wantTimes(t, "a span issued between the trims", second, []time.Duration{5 * ms, 5_833_333, 6_666_666})
}

func TestOverflow(t *testing.T) {
	// 1 unit/s: slot k is at k seconds; the largest time.Duration is
	// 9223372036.854775807 s, so slot 9223372036 is the last that fits.
	p := mustPacer(t, 1, 1)
	s, err := p.Claim(9_223_372_037)
	wantSpan(t, "Claim(9223372037) at 1 unit/s", s, err, 0, 9_223_372_037)
	if got := s.Time(9_223_372_036); got != 9_223_372_036*time.Second {
		t.Fatalf("last slot time = %d, want %d", int64(got), int64(9_223_372_036*time.Second))
	}
	_, err = p.Claim(1)
	wantErr(t, "Claim(1) past the largest time", err, pll.ErrOverflow)
	_, _, err = p.NextDue()
	wantErr(t, "NextDue() past the largest time", err, pll.ErrOverflow)
	s, err = p.Due(math.MaxInt64, 1)
	wantSpan(t, "Due(MaxInt64, 1): the next slot is never due", s, err, 9_223_372_037, 0)
	wantErr(t, "SetTrim(1) when the cursor slot's time does not fit", p.SetTrim(1), pll.ErrOverflow)
	if p.Trim() != 0 {
		t.Fatalf("Trim() = %d after a failed SetTrim, want 0", p.Trim())
	}

	// math.MaxUint64 units/s: every slot index is reachable within a second.
	q := mustPacer(t, math.MaxUint64, 1)
	s, err = q.Due(time.Second, math.MaxUint64)
	wantSpan(t, "Due(1s, MaxUint64) at MaxUint64 units/s", s, err, 0, math.MaxUint64)
	_, err = q.Due(time.Second, 1)
	wantErr(t, "Due once slot MaxUint64 is due", err, pll.ErrOverflow)
	_, err = q.Skip(time.Second, 0)
	wantErr(t, "Skip once slot MaxUint64 is due", err, pll.ErrOverflow)
	_, err = q.Claim(1)
	wantErr(t, "Claim(1) at slot MaxUint64", err, pll.ErrOverflow)
	_, _, err = q.NextDue()
	wantErr(t, "NextDue() at slot MaxUint64", err, pll.ErrOverflow)

	r := mustPacer(t, math.MaxUint64, 1)
	_, err = r.Claim(math.MaxUint64)
	if err != nil {
		t.Fatalf("Claim(MaxUint64) on a fresh pacer: %v", err)
	}
	s, err = r.Due(0, 1)
	wantSpan(t, "Due(0, 1) when slot MaxUint64 is not yet due", s, err, math.MaxUint64, 0)
}
