package main

import (
	"strings"
	"testing"
	"time"
)

func TestBatchUnitsExactCumulativeCount(t *testing.T) {
	const rate int64 = 19_392_000
	den := int64(unitBits) * int64(time.Second)
	var total int64
	for batch := int64(1); batch <= 20; batch++ {
		total += batchUnits(rate, batch, den, 100*time.Millisecond)
	}
	want := rate*int64(2*time.Second)/den + 1
	if total+1 != want {
		t.Fatalf("units=%d want=%d", total, want)
	}
}

func TestRollingErrorsUsesSlidingCompleteWindows(t *testing.T) {
	start := time.Now()
	ts := []time.Time{start, start.Add(100 * time.Millisecond), start.Add(200 * time.Millisecond)}
	got := rollingErrors(ts, start, 200*time.Millisecond, int64(unitBits*10), 100*time.Millisecond)
	if len(got) != 2 || got[0] != 0 || got[1] != 0 {
		t.Fatalf("sliding errors=%v", got)
	}
}

func TestLatenessSummaryUsesSamplesNotAdjacentDifferences(t *testing.T) {
	got := intervalsDuration([]time.Duration{-20, -10, 0, 10, 30})
	for _, want := range []string{"min=-20", "p50=0", "p95=10", "max=30"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q missing %q", got, want)
		}
	}
}

func TestProducerTotalIncludesSlotZero(t *testing.T) {
	for _, rate := range []int64{19_392_000, 40_000_000, 100_000_000} {
		den := int64(unitBits) * int64(time.Second)
		total := int64(1)
		for batch := int64(1); batch <= 20; batch++ {
			total += batchUnits(rate, batch, den, 100*time.Millisecond)
		}
		want := rate*int64(2*time.Second)/den + 1
		if total != want {
			t.Errorf("rate=%d total=%d want=%d", rate, total, want)
		}
	}
}

func TestDeficitSummaryRecoveryAndNoDeficit(t *testing.T) {
	start := time.Now()
	recoveredTimes := make([]time.Time, 11)
	for i := range recoveredTimes {
		recoveredTimes[i] = start.Add(time.Second)
	}
	recovered := deficitSummary(recoveredTimes, start, time.Second, int64(unitBits*10))
	if recovered.max != 10 || !recovered.recovered || recovered.recoveredAt != time.Second {
		t.Fatalf("recovered deficit=%+v", recovered)
	}
	steady := deficitSummary([]time.Time{start, start.Add(time.Second)}, start, time.Second, int64(unitBits))
	if steady.max != 0 || steady.recovered {
		t.Fatalf("no-deficit summary=%+v", steady)
	}
}

func TestBatchUnitsHighRateDoesNotOverflow(t *testing.T) {
	const rate int64 = 100_000_000_000
	const den int64 = 1504_000_000_000
	var total int64
	for batch := int64(1); batch <= 100; batch++ {
		got := batchUnits(rate, batch, den, 100*time.Millisecond)
		want := (rate/10*batch)/1504 - (rate/10*(batch-1))/1504
		if got != want {
			t.Fatalf("batch=%d quota=%d want=%d", batch, got, want)
		}
		total += got
	}
	if total != rate*10/1504 {
		t.Fatalf("total=%d", total)
	}
}
