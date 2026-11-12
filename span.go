package pll

import (
	"fmt"
	"iter"
	"time"
)

// Span is a run of consecutive slots handed out by a Pacer: slots First
// through First+Count-1. It carries the timing it was issued under, so a later
// SetTrim never changes its times. A Span is a small value; copy it freely.
type Span struct {
	First, Count uint64

	seg *segment // timing the span was issued under; nil only for the zero Span
}

// Time returns the time of slot First+i: the floor of its exact time, in
// nanoseconds from the origin. It panics if i >= Count, like an index out of
// range.
func (s Span) Time(i uint64) time.Duration {
	if i >= s.Count {
		panic(fmt.Sprintf("pll: Span.Time index %d out of range [0:%d]", i, s.Count))
	}
	t, ok := s.seg.floorTime(s.First + i)
	if !ok {
		panic(ErrOverflow)
	}
	return time.Duration(t)
}

// All yields each slot in the span with its time, in order.
func (s Span) All() iter.Seq2[uint64, time.Duration] {
	return func(yield func(uint64, time.Duration) bool) {
		for i := uint64(0); i < s.Count; i++ {
			if !yield(s.First+i, s.Time(i)) {
				return
			}
		}
	}
}
