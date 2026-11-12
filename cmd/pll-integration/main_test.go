package main

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/EnvelopeInc/pll"
)

func TestBoundedQueueCapacityFIFOAndHighWater(t *testing.T) {
	q := newBoundedQueue(2)
	for i := uint64(1); i <= 2; i++ {
		if _, err := q.Enqueue(context.Background(), item{emission: pll.Emission{Slot: i}}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := q.Enqueue(ctx, item{}); err == nil {
		t.Fatal("enqueue after cancellation succeeded")
	}
	for i := uint64(1); i <= 2; i++ {
		v, ok := q.Dequeue()
		if !ok || v.emission.Slot != i {
			t.Fatalf("got %+v ok=%t want=%d", v, ok, i)
		}
	}
	if q.HighWater() != 2 {
		t.Fatalf("high water=%d", q.HighWater())
	}
	q.Close()
	if _, ok := q.Dequeue(); ok {
		t.Fatal("dequeue after drained close succeeded")
	}
}

func TestBoundedQueueEnqueueTimestampIsAssignedAtInsertion(t *testing.T) {
	q := newBoundedQueue(1)
	if _, err := q.Enqueue(context.Background(), item{}); err != nil {
		t.Fatal(err)
	}
	attemptAt := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := q.Enqueue(context.Background(), item{emission: pll.Emission{Slot: 2}, enqueued: attemptAt})
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		q.mu.Lock()
		waiting, size := q.waiting, q.size
		q.mu.Unlock()
		if waiting > 0 {
			if size != 1 {
				t.Fatalf("waiting enqueue observed with occupancy=%d", size)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("enqueue did not report waiting for full queue")
		}
		runtime.Gosched()
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("first item missing")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	second, ok := q.Dequeue()
	if !ok {
		t.Fatal("second item missing")
	}
	if !second.enqueued.After(attemptAt) {
		t.Fatalf("timestamp %s was not assigned at successful insertion after attempt %s", second.enqueued, attemptAt)
	}
}

func TestContentionWorkerSelectionTracksParallelism(t *testing.T) {
	for _, tc := range []struct{ procs, limit, moderate, substantial int }{
		{10, 10, 2, 9}, {4, 4, 1, 3}, {10, 6, 1, 5}, {1, 1, 1, 1},
	} {
		moderate, substantial := contentionWorkers(tc.procs, tc.limit)
		if moderate != tc.moderate || substantial != tc.substantial {
			t.Fatalf("GOMAXPROCS=%d limit=%d workers=(%d,%d), want (%d,%d)", tc.procs, tc.limit, moderate, substantial, tc.moderate, tc.substantial)
		}
	}
}

func TestQuotaBoundaryUnitsUsesCumulativeRationalRate(t *testing.T) {
	if got := quotaBoundaryUnits(1, 0); got != 1290 {
		t.Fatalf("first quota=%d", got)
	}
	if got := quotaBoundaryUnits(2, 0); got != 2579 {
		t.Fatalf("cumulative quota=%d", got)
	}
	if quotaBoundaryUnits(10, 100) <= quotaBoundaryUnits(10, -100) {
		t.Fatal("positive ppm quota did not exceed negative ppm quota")
	}
	if got := quotaAtDuration(10*time.Second, 0); got != 128937 {
		t.Fatalf("10s expected count=%d; must include slot zero", got)
	}
}

func TestSourcePhaseAccumulatorMatchesExactDeadlines(t *testing.T) {
	for _, ppm := range []int64{-100, 0, 100} {
		clock := newSourceClock(ppm)
		for n := uint64(0); n < 100_000; n++ {
			if got, want := clock.Deadline(), sourceDeadline(n, ppm); got != want {
				t.Fatalf("ppm=%d slot=%d deadline=%s want=%s", ppm, n, got, want)
			}
			clock.Advance()
		}
	}
}

func TestSourceDeadlineAndIndependentOscillator(t *testing.T) {
	if got := sourceDeadline(1289, 0); got <= 99*time.Millisecond || got >= 101*time.Millisecond {
		t.Fatalf("deadline=%s", got)
	}
	if oscillator(10_000, 50, 0) >= oscillator(10_000, -50, 0) {
		t.Fatal("faster oscillator should reach slot earlier")
	}
}

func TestWindowErrorsUsesFixedGridAndHalfOpenWindow(t *testing.T) {
	times := []time.Duration{0, 10 * time.Millisecond, 20 * time.Millisecond, 100 * time.Millisecond, 110 * time.Millisecond}
	got := windowErrors(times, 100*time.Millisecond, 110*time.Millisecond)
	if len(got) != 2 {
		t.Fatalf("windows=%d values=%v", len(got), got)
	}
	wantFirst := (3.0 - float64(rate)*.1/float64(bits)) / (float64(rate) * .1 / float64(bits)) * 100
	if got[0] != wantFirst {
		t.Fatalf("first-window error=%v want=%v", got[0], wantFirst)
	}
}

func TestMeasureDeficitFindsRecovery(t *testing.T) {
	unitPeriod := time.Duration(bits) * time.Second / time.Duration(rate)
	times := []time.Duration{0, unitPeriod * 3, unitPeriod*3 + 1, unitPeriod*3 + 2, unitPeriod * 4, unitPeriod * 5}
	d := measureDeficit(times, unitPeriod*6)
	if d.peak < 1 || !d.recovered || d.recoveredAt < unitPeriod*3 || d.recoveredAt > unitPeriod*4 {
		t.Fatalf("deficit=%+v", d)
	}
}
