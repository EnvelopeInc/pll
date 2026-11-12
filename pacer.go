package pll

import (
	"math"
	"math/big"
	"math/bits"
	"time"
)

// Pacer divides time into evenly spaced slots numbered 0, 1, 2, … at an exact
// rate and hands them out in order. Slot 0 is due at time zero. A cursor marks
// the next slot not yet handed out or skipped; it never moves back.
//
// Times are durations from an origin the caller chooses; the Pacer never reads
// a clock. A Pacer is not safe for concurrent use. Construct it with NewPacer;
// the zero Pacer is not usable.
type Pacer struct {
	cursor uint64   // next slot not yet handed out or skipped
	rate   Rate     // nominal rate, unaffected by trim
	seg    *segment // current timing; immutable, shared with issued Spans
}

// segment is one stretch of uniform timing. Slot s >= start is at exactly
// anchor + (s-start) × pn/pd nanoseconds, where pn/pd is the period
// den×1e21 / (num×(1e12+trim)) reduced to lowest terms. A segment is never
// modified after it is made, so Spans may share it.
type segment struct {
	start  uint64 // slot S
	anchor int64  // integer nanoseconds A
	trim   int64  // parts per trillion q

	// pn and pd hold the period when both fit in a uint64 (small is true).
	// Otherwise bigPn and bigPd hold it and the big.Int path is used.
	small        bool
	pn, pd       uint64
	bigPn, bigPd *big.Int
}

// tenTo21 is 10^21, the scale that turns a rate into nanoseconds per slot.
var tenTo21 = new(big.Int).Exp(big.NewInt(10), big.NewInt(21), nil)

// newSegment makes the segment for rate num/den with the given start slot,
// anchor and trim. It requires num > 0, den > 0 and 1e12+trim > 0.
func newSegment(num, den, start uint64, anchor, trim int64) *segment {
	n := new(big.Int).Mul(new(big.Int).SetUint64(den), tenTo21)
	d := new(big.Int).Mul(new(big.Int).SetUint64(num), big.NewInt(1_000_000_000_000+trim))
	g := new(big.Int).GCD(nil, nil, n, d)
	n.Quo(n, g)
	d.Quo(d, g)

	seg := &segment{start: start, anchor: anchor, trim: trim}
	if n.IsUint64() && d.IsUint64() {
		seg.small = true
		seg.pn, seg.pd = n.Uint64(), d.Uint64()
	} else {
		seg.bigPn, seg.bigPd = n, d
	}
	return seg
}

// floorTime returns floor(T(s)) for slot s >= g.start, the time in nanoseconds
// of slot s under this segment. It returns false when that time does not fit
// in an int64. The anchor is never negative, because it is the floor of a time
// that is not negative.
func (g *segment) floorTime(s uint64) (int64, bool) {
	k := s - g.start
	if !g.small {
		t := new(big.Int).SetUint64(k)
		t.Mul(t, g.bigPn)
		t.Quo(t, g.bigPd) // k and the period are not negative, so Quo is floor
		t.Add(t, big.NewInt(g.anchor))
		if !t.IsInt64() {
			return 0, false
		}
		return t.Int64(), true
	}
	hi, lo := bits.Mul64(k, g.pn)
	if hi >= g.pd {
		return 0, false // the quotient is at least 2^64, far beyond an int64
	}
	q, _ := bits.Div64(hi, lo, g.pd)
	if q > uint64(math.MaxInt64-g.anchor) {
		return 0, false
	}
	return int64(q) + g.anchor, true
}

// ceilTime returns ceil(T(s)) for slot s >= g.start: the first whole
// nanosecond at or after which slot s is due. It returns false when that time
// does not fit in an int64.
func (g *segment) ceilTime(s uint64) (int64, bool) {
	k := s - g.start
	if !g.small {
		t := new(big.Int).SetUint64(k)
		t.Mul(t, g.bigPn)
		q, r := new(big.Int).QuoRem(t, g.bigPd, new(big.Int))
		if r.Sign() != 0 {
			q.Add(q, big.NewInt(1))
		}
		q.Add(q, big.NewInt(g.anchor))
		if !q.IsInt64() {
			return 0, false
		}
		return q.Int64(), true
	}
	hi, lo := bits.Mul64(k, g.pn)
	if hi >= g.pd {
		return 0, false // the quotient is at least 2^64, far beyond an int64
	}
	q, r := bits.Div64(hi, lo, g.pd)
	if r != 0 {
		if q == math.MaxUint64 {
			return 0, false
		}
		q++
	}
	if q > uint64(math.MaxInt64-g.anchor) {
		return 0, false
	}
	return int64(q) + g.anchor, true
}

// latestDue returns the largest slot s >= g.start whose exact time is at or
// before now, capped at math.MaxUint64. It returns false when even slot
// g.start is not yet due. now must not be negative.
func (g *segment) latestDue(now int64) (uint64, bool) {
	if now < g.anchor {
		return 0, false
	}
	d := uint64(now - g.anchor) // not negative, and fits in a uint64
	var k uint64                // slots after g.start that are due
	if g.small {
		// k = floor(d × pd / pn). When the high word reaches pn the quotient
		// is at least 2^64, so the slot is beyond the cap.
		hi, lo := bits.Mul64(d, g.pd)
		if hi >= g.pn {
			return math.MaxUint64, true
		}
		k, _ = bits.Div64(hi, lo, g.pn)
	} else {
		t := new(big.Int).SetUint64(d)
		t.Mul(t, g.bigPd)
		t.Quo(t, g.bigPn) // d and the period are not negative, so Quo is floor
		if !t.IsUint64() {
			return math.MaxUint64, true
		}
		k = t.Uint64()
	}
	if k > math.MaxUint64-g.start {
		return math.MaxUint64, true
	}
	return g.start + k, true
}

// NewPacer returns a Pacer for rate r with no trim. An invalid Rate returns
// nil and ErrInvalidRate.
func NewPacer(r Rate) (*Pacer, error) {
	if r.num == 0 || r.den == 0 {
		return nil, ErrInvalidRate
	}
	return &Pacer{rate: r, seg: newSegment(r.num, r.den, 0, 0, 0)}, nil
}

// Due hands out the oldest slots whose exact time is at or before now, at most
// max of them, and moves the cursor past them. When nothing is due it returns
// a Span with Count 0 and First at the cursor. A now earlier than before is
// not an error; it finds nothing new.
//
// Errors: ErrInvalidArgument when now < 0 or max is 0; ErrOverflow when the
// cursor has reached math.MaxUint64 and that slot is due. On error nothing
// changes.
func (p *Pacer) Due(now time.Duration, max uint64) (Span, error) {
	if now < 0 || max == 0 {
		return Span{}, ErrInvalidArgument
	}
	last, ok := p.seg.latestDue(int64(now))
	if !ok || last < p.cursor {
		return Span{First: p.cursor, Count: 0, seg: p.seg}, nil
	}
	if p.cursor == math.MaxUint64 {
		return Span{}, ErrOverflow // slot MaxUint64 is due and cannot be handed out
	}
	if last == math.MaxUint64 {
		last-- // MaxUint64 is never handed out
	}
	n := last - p.cursor + 1
	if n > max {
		n = max
	}
	s := Span{First: p.cursor, Count: n, seg: p.seg}
	p.cursor += n
	return s, nil
}

// Claim hands out the next n slots whether or not they are due, and moves the
// cursor past them.
//
// Errors: ErrInvalidArgument when n is 0; ErrOverflow when the span would
// reach slot math.MaxUint64 or its last slot's time would exceed the largest
// time.Duration. On error nothing changes.
func (p *Pacer) Claim(n uint64) (Span, error) {
	if n == 0 {
		return Span{}, ErrInvalidArgument
	}
	if n > math.MaxUint64-p.cursor {
		return Span{}, ErrOverflow
	}
	if _, ok := p.seg.floorTime(p.cursor + n - 1); !ok {
		return Span{}, ErrOverflow
	}
	s := Span{First: p.cursor, Count: n, seg: p.seg}
	p.cursor += n
	return s, nil
}

// Skip moves the cursor past the slots that are due at now without handing
// them out, except the newest keep of them, and returns how many it skipped.
// Use it when there is nothing to put in old slots: Skip(now, k) followed by
// Due(now, k) hands out the newest k due slots.
//
// Errors: ErrInvalidArgument when now < 0; ErrOverflow when the cursor has
// reached math.MaxUint64 and that slot is due. On error nothing changes.
func (p *Pacer) Skip(now time.Duration, keep uint64) (uint64, error) {
	if now < 0 {
		return 0, ErrInvalidArgument
	}
	last, ok := p.seg.latestDue(int64(now))
	if !ok || last < p.cursor {
		return 0, nil // nothing due
	}
	if p.cursor == math.MaxUint64 {
		return 0, ErrOverflow // slot MaxUint64 is due and cannot be passed over
	}
	if last == math.MaxUint64 {
		last-- // MaxUint64 is never handed out, so it is never counted
	}
	d := last - p.cursor + 1 // slots Due could hand out at now
	n := d - min(d, keep)
	p.cursor += n
	return n, nil
}

// NextDue returns the cursor slot and the earliest whole nanosecond at which
// it is due: the ceiling of its exact time. A Due call at that time or later
// hands it out.
//
// Errors: ErrOverflow when the cursor has reached math.MaxUint64 or that time
// exceeds the largest time.Duration; slot and at are then zero.
func (p *Pacer) NextDue() (slot uint64, at time.Duration, err error) {
	if p.cursor == math.MaxUint64 {
		return 0, 0, ErrOverflow
	}
	t, ok := p.seg.ceilTime(p.cursor)
	if !ok {
		return 0, 0, ErrOverflow
	}
	return p.cursor, time.Duration(t), nil
}

// SetTrim sets the rate to nominal × (1 + ppt/1e12) for the cursor slot and
// every slot after it. Spans already handed out keep their times.
//
// The new timing starts at the floor of the cursor slot's exact time under the
// old timing, so the phase moves by less than 1 ns and times never decrease.
// Setting the trim it already has changes nothing.
//
// Errors: ErrInvalidArgument when ppt <= -1e12 or ppt >= 1e12; ErrOverflow
// when the cursor slot's time exceeds the largest time.Duration. On error
// nothing changes.
func (p *Pacer) SetTrim(ppt int64) error {
	if ppt <= -1_000_000_000_000 || ppt >= 1_000_000_000_000 {
		return ErrInvalidArgument
	}
	if ppt == p.seg.trim {
		return nil
	}
	anchor, ok := p.seg.floorTime(p.cursor)
	if !ok {
		return ErrOverflow
	}
	p.seg = newSegment(p.rate.num, p.rate.den, p.cursor, anchor, ppt)
	return nil
}

// Trim returns the current trim in parts per trillion.
func (p *Pacer) Trim() int64 { return p.seg.trim }
