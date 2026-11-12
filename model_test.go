package pll_test

// A deliberately naive reference model of the Pacer, and the harness that
// drives the real Pacer and the model side by side.
//
// The model recomputes every slot time from scratch with big.Rat, straight
// from the formulas in SPEC.md. It has no fast path and no reduced fractions,
// so a bug in the Pacer's arithmetic shows up as a disagreement.

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/version2beta/pll"
)

const trimScale = 1_000_000_000_000

var (
	bigMaxInt64 = new(big.Int).SetInt64(math.MaxInt64)
	tenTo21     = new(big.Int).Exp(big.NewInt(10), big.NewInt(21), nil)
)

// modelSeg is one stretch of uniform timing: slot s >= start is at exactly
// anchor + (s-start) * den*1e21 / (num*(1e12+trim)) nanoseconds.
type modelSeg struct {
	num, den uint64
	start    uint64
	anchor   *big.Int
	trim     int64
}

func (g modelSeg) exact(s uint64) *big.Rat {
	if s < g.start {
		panic("model: slot before its segment")
	}
	k := new(big.Rat).SetInt(new(big.Int).SetUint64(s - g.start))
	p := new(big.Rat).SetFrac(
		new(big.Int).Mul(new(big.Int).SetUint64(g.den), tenTo21),
		new(big.Int).Mul(new(big.Int).SetUint64(g.num), big.NewInt(trimScale+g.trim)),
	)
	k.Mul(k, p)
	return k.Add(k, new(big.Rat).SetInt(g.anchor))
}

func floorRat(r *big.Rat) *big.Int {
	q := new(big.Int)
	m := new(big.Int)
	q.DivMod(r.Num(), r.Denom(), m) // Euclidean; r >= 0 here, so this is floor
	return q
}

func ceilRat(r *big.Rat) *big.Int {
	q := floorRat(r)
	if new(big.Rat).SetInt(q).Cmp(r) != 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

func fitsDuration(x *big.Int) bool { return x.Sign() >= 0 && x.Cmp(bigMaxInt64) <= 0 }

func (g modelSeg) floor(s uint64) *big.Int { return floorRat(g.exact(s)) }

// latest is the largest slot (capped at MaxUint64) whose exact time is <= now,
// and false when even the start slot is not due.
func (g modelSeg) latest(now int64) (uint64, bool) {
	n := new(big.Rat).SetInt64(now)
	a := new(big.Rat).SetInt(g.anchor)
	if n.Cmp(a) < 0 {
		return 0, false
	}
	p := new(big.Rat).SetFrac(
		new(big.Int).Mul(new(big.Int).SetUint64(g.den), tenTo21),
		new(big.Int).Mul(new(big.Int).SetUint64(g.num), big.NewInt(trimScale+g.trim)),
	)
	x := floorRat(new(big.Rat).Quo(new(big.Rat).Sub(n, a), p))
	x.Add(x, new(big.Int).SetUint64(g.start))
	if !x.IsUint64() {
		return math.MaxUint64, true
	}
	l := x.Uint64()
	// Sanity: the model checks its own answer the slow way.
	if g.exact(l).Cmp(n) > 0 || (l < math.MaxUint64 && g.exact(l+1).Cmp(n) <= 0) {
		panic("model: latest is wrong")
	}
	return l, true
}

type modelSpan struct {
	first, count uint64
	seg          modelSeg
}

type model struct {
	cursor uint64
	seg    modelSeg
}

func newModel(num, den uint64) *model {
	return &model{seg: modelSeg{num: num, den: den, anchor: new(big.Int)}}
}

func (m *model) due(now int64, max uint64) (modelSpan, error) {
	if now < 0 || max == 0 {
		return modelSpan{}, pll.ErrInvalidArgument
	}
	l, any := m.seg.latest(now)
	if !any || l < m.cursor {
		return modelSpan{first: m.cursor, seg: m.seg}, nil
	}
	if m.cursor == math.MaxUint64 {
		return modelSpan{}, pll.ErrOverflow
	}
	last := l
	if last == math.MaxUint64 {
		last--
	}
	n := last - m.cursor + 1
	if n > max {
		n = max
	}
	s := modelSpan{first: m.cursor, count: n, seg: m.seg}
	m.cursor += n
	return s, nil
}

func (m *model) claim(n uint64) (modelSpan, error) {
	if n == 0 {
		return modelSpan{}, pll.ErrInvalidArgument
	}
	if n > math.MaxUint64-m.cursor {
		return modelSpan{}, pll.ErrOverflow
	}
	if !fitsDuration(m.seg.floor(m.cursor + n - 1)) {
		return modelSpan{}, pll.ErrOverflow
	}
	s := modelSpan{first: m.cursor, count: n, seg: m.seg}
	m.cursor += n
	return s, nil
}

func (m *model) skip(now int64, keep uint64) (uint64, error) {
	if now < 0 {
		return 0, pll.ErrInvalidArgument
	}
	l, any := m.seg.latest(now)
	if !any || l < m.cursor {
		return 0, nil
	}
	if m.cursor == math.MaxUint64 {
		return 0, pll.ErrOverflow
	}
	last := l
	if last == math.MaxUint64 {
		last--
	}
	due := last - m.cursor + 1
	var skipped uint64
	if due > keep {
		skipped = due - keep
	}
	m.cursor += skipped
	return skipped, nil
}

func (m *model) nextDue() (uint64, int64, error) {
	if m.cursor == math.MaxUint64 {
		return 0, 0, pll.ErrOverflow
	}
	c := ceilRat(m.seg.exact(m.cursor))
	if !fitsDuration(c) {
		return 0, 0, pll.ErrOverflow
	}
	return m.cursor, c.Int64(), nil
}

func (m *model) setTrim(ppt int64) error {
	if ppt <= -trimScale || ppt >= trimScale {
		return pll.ErrInvalidArgument
	}
	if ppt == m.seg.trim {
		return nil
	}
	a := m.seg.floor(m.cursor)
	if !fitsDuration(a) {
		return pll.ErrOverflow
	}
	m.seg = modelSeg{num: m.seg.num, den: m.seg.den, start: m.cursor, anchor: a, trim: ppt}
	return nil
}

// --- the harness ---

type opKind int

const (
	opDue opKind = iota
	opClaim
	opSkip
	opNextDue
	opSetTrim
	numOpKinds
)

type op struct {
	kind opKind
	now  int64
	n    uint64 // max for Due, n for Claim, keep for Skip
	trim int64
}

func (o op) String() string {
	switch o.kind {
	case opDue:
		return fmt.Sprintf("Due(%d, %d)", o.now, o.n)
	case opClaim:
		return fmt.Sprintf("Claim(%d)", o.n)
	case opSkip:
		return fmt.Sprintf("Skip(%d, %d)", o.now, o.n)
	case opNextDue:
		return "NextDue()"
	default:
		return fmt.Sprintf("SetTrim(%d)", o.trim)
	}
}

// issued remembers a span and the times it showed when issued, so later
// operations can check that its times never change.
type issued struct {
	span  pll.Span
	times map[uint64]time.Duration
}

// sampleIndexes picks which slots of a span to compare: all of a short span,
// otherwise the ends and a few in between.
func sampleIndexes(count uint64) []uint64 {
	if count == 0 {
		return nil
	}
	if count <= 8 {
		idx := make([]uint64, count)
		for i := range idx {
			idx[i] = uint64(i)
		}
		return idx
	}
	return []uint64{0, 1, count / 3, count / 2, count - 2, count - 1}
}

// runOps drives a Pacer and the model through ops, comparing every result and
// checking the invariants after every operation. It stops at the first
// failure and reports the operations that led to it.
func runOps(t *testing.T, num, den uint64, ops []op) {
	t.Helper()
	r, err := pll.UnitsPerSecond(num, den)
	if err != nil {
		t.Fatalf("UnitsPerSecond(%d, %d): unexpected error %v", num, den, err)
	}
	p, err := pll.NewPacer(r)
	if err != nil || p == nil {
		t.Fatalf("NewPacer(%v): got (%v, %v), want a pacer and no error", r, p, err)
	}
	m := newModel(num, den)

	var (
		trail     []string
		nextFirst uint64        // invariant: where the next span must start
		lastTime  time.Duration // invariant: time of the last slot handed out
		haveTime  bool
		kept      []issued
	)
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("rate %d/%d after %v:\n"+format, append([]any{num, den, trail}, args...)...)
	}
	sameErr := func(what string, got, want error) bool {
		t.Helper()
		if (got == nil) != (want == nil) || (want != nil && !errors.Is(got, want)) {
			fail("%s: error %v, want %v", what, got, want)
		}
		return want == nil
	}
	checkSpan := func(what string, got pll.Span, want modelSpan, now int64, isDue bool) {
		t.Helper()
		if got.First != want.first || got.Count != want.count {
			fail("%s: span First=%d Count=%d, want First=%d Count=%d", what, got.First, got.Count, want.first, want.count)
		}
		if got.First != nextFirst {
			fail("%s: span starts at %d, but the cursor was at %d (spans must be contiguous)", what, got.First, nextFirst)
		}
		rec := issued{span: got, times: map[uint64]time.Duration{}}
		prev := time.Duration(-1)
		for _, i := range sampleIndexes(got.Count) {
			wantT := want.seg.floor(want.first + i)
			if !fitsDuration(wantT) {
				fail("%s: model says slot %d does not fit, yet a span holds it", what, want.first+i)
			}
			gotT := got.Time(i)
			if int64(gotT) != wantT.Int64() {
				fail("%s: Time(%d) (slot %d) = %d, want %d", what, i, got.First+i, int64(gotT), wantT.Int64())
			}
			if gotT < prev {
				fail("%s: times decrease inside the span at index %d", what, i)
			}
			prev = gotT
			rec.times[i] = gotT
		}
		if got.Count > 0 {
			if haveTime && got.Time(0) < lastTime {
				fail("%s: first time %d is before the previous slot's time %d", what, got.Time(0), lastTime)
			}
			if isDue && int64(got.Time(got.Count-1)) > now {
				fail("%s: handed out slot %d at %d, after now=%d", what, got.First+got.Count-1, got.Time(got.Count-1), now)
			}
			lastTime, haveTime = got.Time(got.Count-1), true
			kept = append(kept, rec)
			if len(kept) > 16 {
				kept = kept[1:]
			}
		}
		nextFirst = got.First + got.Count
	}

	for _, o := range ops {
		trail = append(trail, o.String())
		if len(trail) > 12 {
			trail = trail[1:]
		}
		switch o.kind {
		case opDue:
			got, gerr := p.Due(time.Duration(o.now), o.n)
			want, werr := m.due(o.now, o.n)
			if sameErr(o.String(), gerr, werr) {
				checkSpan(o.String(), got, want, o.now, true)
			}
		case opClaim:
			got, gerr := p.Claim(o.n)
			want, werr := m.claim(o.n)
			if sameErr(o.String(), gerr, werr) {
				checkSpan(o.String(), got, want, 0, false)
			}
		case opSkip:
			got, gerr := p.Skip(time.Duration(o.now), o.n)
			want, werr := m.skip(o.now, o.n)
			if sameErr(o.String(), gerr, werr) {
				if got != want {
					fail("%s: skipped %d, want %d", o, got, want)
				}
				nextFirst += got
			}
		case opNextDue:
			gs, gat, gerr := p.NextDue()
			ws, wat, werr := m.nextDue()
			if sameErr(o.String(), gerr, werr) {
				if gs != ws || int64(gat) != wat {
					fail("%s: got (%d, %d), want (%d, %d)", o, gs, int64(gat), ws, wat)
				}
				if gs != nextFirst {
					fail("%s: cursor %d, but spans and skips put it at %d", o, gs, nextFirst)
				}
			}
		case opSetTrim:
			gerr := p.SetTrim(o.trim)
			werr := m.setTrim(o.trim)
			sameErr(o.String(), gerr, werr)
			if p.Trim() != m.seg.trim {
				fail("%s: Trim() = %d, want %d", o, p.Trim(), m.seg.trim)
			}
		}
		// Invariant: spans already handed out never change.
		for _, k := range kept {
			for i, want := range k.times {
				if got := k.span.Time(i); got != want {
					fail("span starting at %d: Time(%d) changed from %d to %d", k.span.First, i, want, got)
				}
			}
		}
	}
}
