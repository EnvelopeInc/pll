package pll

import "time"

// Pacer divides time into evenly spaced slots numbered 0, 1, 2, … at an exact
// rate and hands them out in order. Slot 0 is due at time zero. A cursor marks
// the next slot not yet handed out or skipped; it never moves back.
//
// Times are durations from an origin the caller chooses; the Pacer never reads
// a clock. A Pacer is not safe for concurrent use. Construct it with NewPacer;
// the zero Pacer is not usable.
type Pacer struct {
	// The fields are yours to choose.
}

// NewPacer returns a Pacer for rate r with no trim. An invalid Rate returns
// nil and ErrInvalidRate.
func NewPacer(r Rate) (*Pacer, error) {
	return nil, nil
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
	return Span{}, nil
}

// Claim hands out the next n slots whether or not they are due, and moves the
// cursor past them.
//
// Errors: ErrInvalidArgument when n is 0; ErrOverflow when the span would
// reach slot math.MaxUint64 or its last slot's time would exceed the largest
// time.Duration. On error nothing changes.
func (p *Pacer) Claim(n uint64) (Span, error) {
	return Span{}, nil
}

// Skip moves the cursor past the slots that are due at now without handing
// them out, except the newest keep of them, and returns how many it skipped.
// Use it when there is nothing to put in old slots: Skip(now, k) followed by
// Due(now, k) hands out the newest k due slots.
//
// Errors: ErrInvalidArgument when now < 0; ErrOverflow when the cursor has
// reached math.MaxUint64 and that slot is due. On error nothing changes.
func (p *Pacer) Skip(now time.Duration, keep uint64) (uint64, error) {
	return 0, nil
}

// NextDue returns the cursor slot and the earliest whole nanosecond at which
// it is due: the ceiling of its exact time. A Due call at that time or later
// hands it out.
//
// Errors: ErrOverflow when the cursor has reached math.MaxUint64 or that time
// exceeds the largest time.Duration; slot and at are then zero.
func (p *Pacer) NextDue() (slot uint64, at time.Duration, err error) {
	return 0, 0, nil
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
	return nil
}

// Trim returns the current trim in parts per trillion.
func (p *Pacer) Trim() int64 { return 0 }
