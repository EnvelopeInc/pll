package pll

import (
	"iter"
	"time"
)

// Span is a run of consecutive slots handed out by a Pacer: slots First
// through First+Count-1. It carries the timing it was issued under, so a later
// SetTrim never changes its times. A Span is a small value; copy it freely.
//
// Add the unexported fields that snapshot needs. Handing out a Span must not
// allocate: share immutable timing data rather than copying it per call.
type Span struct {
	First, Count uint64

	seg *segment // timing the span was issued under; nil only for the zero Span
}

// Time returns the time of slot First+i: the floor of its exact time, in
// nanoseconds from the origin. It panics if i >= Count, like an index out of
// range.
func (s Span) Time(i uint64) time.Duration {
	return 0
}

// All yields each slot in the span with its time, in order.
func (s Span) All() iter.Seq2[uint64, time.Duration] {
	return func(yield func(uint64, time.Duration) bool) {}
}
