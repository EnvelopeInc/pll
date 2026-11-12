package pll

import (
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"
)

func TestRequiredRateSchedulesAgainstIndependentValues(t *testing.T) {
	cases := []struct {
		name string
		rate int64
		size int64
		want map[uint64]int64
	}{
		{
			name: "19.392 Mbps, 188-byte units", rate: 19_392_000, size: 188,
			want: map[uint64]int64{0: 0, 1: 77_557, 2: 155_115, 3: 232_673, 1_000_000: 77_557_755_775, 1_000_000_000: 77_557_755_775_577},
		},
		{
			name: "40 Mbps, 188-byte units", rate: 40_000_000, size: 188,
			want: map[uint64]int64{0: 0, 1: 37_600, 2: 75_200, 3: 112_800, 1_000_000: 37_600_000_000, 1_000_000_000: 37_600_000_000_000},
		},
		{
			name: "100 Mbps, 188-byte units", rate: 100_000_000, size: 188,
			want: map[uint64]int64{0: 0, 1: 15_040, 2: 30_080, 3: 45_120, 1_000_000: 15_040_000_000, 1_000_000_000: 15_040_000_000_000},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewPLL(tc.rate, tc.size)
			if err != nil {
				t.Fatal(err)
			}
			for slot, want := range tc.want {
				got, err := p.VirtualTime(slot)
				if err != nil || got != time.Duration(want) {
					t.Errorf("VirtualTime(%d) = %s, %v; want %dns", slot, got, err, want)
				}
			}
		})
	}
}

func TestIndependentOracleForMultipleUnitSizesAndLongCounts(t *testing.T) {
	for _, tc := range []struct{ rate, size int64 }{
		{1_000_000, 188}, {10_000_000, 192}, {27_000_000, 204},
		{44_100, 1}, {48_000, 7}, {1_000_000_007, 188}, {40_000_000, 256},
	} {
		p, err := NewPLL(tc.rate, tc.size)
		if err != nil {
			t.Fatalf("NewPLL(%d,%d): %v", tc.rate, tc.size, err)
		}
		for _, slot := range []uint64{0, 1, 2, 999_999, 1_000_000_000} {
			// Independent big-integer oracle for floor(slot * bytes * 8 * 1e9 / rate).
			n := new(big.Int).SetUint64(slot)
			n.Mul(n, big.NewInt(tc.size))
			n.Mul(n, big.NewInt(8))
			n.Mul(n, big.NewInt(1_000_000_000))
			n.Quo(n, big.NewInt(tc.rate))
			if !n.IsInt64() {
				continue
			}
			got, err := p.VirtualTime(slot)
			if err != nil || got != time.Duration(n.Int64()) {
				t.Fatalf("rate=%d size=%d slot=%d got=%s,%v want=%dns", tc.rate, tc.size, slot, got, err, n.Int64())
			}
		}
	}
}

func TestFractionalNanosecondPeriodsDoNotAccumulateDrift(t *testing.T) {
	p, err := NewPLL(3_000_000_000, 1) // 8/3 ns per unit
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{0, 2, 5, 8, 10, 13, 16, 18, 21}
	for slot, ns := range want {
		got, err := p.VirtualTime(uint64(slot))
		if err != nil || got != time.Duration(ns) {
			t.Fatalf("VirtualTime(%d) = %s,%v; want %dns", slot, got, err, ns)
		}
		if slot > 0 {
			previous, _ := p.VirtualTime(uint64(slot - 1))
			if got <= previous {
				t.Fatalf("timestamps not monotonic at slot %d: %s <= %s", slot, got, previous)
			}
		}
	}
}

func TestAdvanceNominalRationalDeadlineIsNotFlooredTimestamp(t *testing.T) {
	p, _ := NewPLL(3_000_000_000, 1) // slot 1 is exactly 8/3ns
	stamp, err := p.VirtualTime(1)
	if err != nil || stamp != 2*time.Nanosecond {
		t.Fatalf("slot 1 timestamp=%s err=%v", stamp, err)
	}
	for _, tc := range []struct {
		now  time.Duration
		slot uint64
		want int
	}{
		{0, 0, 1},
		{2 * time.Nanosecond, 0, 0},
		{3 * time.Nanosecond, 1, 1},
		{0, 0, 0}, // decreasing now cannot rewind the cursor
	} {
		batch, err := p.Advance(tc.now, 1, 1)
		if err != nil || len(batch.Emissions) != tc.want || batch.MoreDue {
			t.Fatalf("now=%s batch=%+v err=%v", tc.now, batch, err)
		}
		if tc.want != 0 && batch.Emissions[0].Slot != tc.slot {
			t.Fatalf("now=%s slot=%d want=%d", tc.now, batch.Emissions[0].Slot, tc.slot)
		}
	}
}

func TestAdvanceBatchLimitDrainAndSlots(t *testing.T) {
	p, err := NewPLL(8_000_000_000, 1)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := p.Advance(5*time.Nanosecond, 6, 2)
	if err != nil || len(batch.Emissions) != 2 || !batch.MoreDue {
		t.Fatalf("first: %+v %v", batch, err)
	}
	for i, e := range batch.Emissions {
		if e.UnitIndex != uint64(i) || e.Slot != uint64(i) || e.Timestamp != time.Duration(i)*time.Nanosecond {
			t.Fatalf("emission: %+v", e)
		}
	}
	batch, err = p.Advance(5*time.Nanosecond, 4, 2)
	if err != nil || len(batch.Emissions) != 2 || !batch.MoreDue {
		t.Fatalf("second: %+v %v", batch, err)
	}
	batch, err = p.Advance(5*time.Nanosecond, 2, 2)
	if err != nil || len(batch.Emissions) != 2 || batch.MoreDue {
		t.Fatalf("third: %+v %v", batch, err)
	}
	if batch.Emissions[0].UnitIndex != 4 || batch.Emissions[1].UnitIndex != 5 {
		t.Fatalf("indices: %+v", batch.Emissions)
	}
}

func TestAdvanceUnderrunSkipsEarlySlotsAndNoAvailabilitySkipsAll(t *testing.T) {
	p, _ := NewPLL(8_000_000_000, 1)
	batch, err := p.Advance(10*time.Nanosecond, 0, 4)
	if err != nil || len(batch.Emissions) != 0 {
		t.Fatalf("zero availability: %+v %v", batch, err)
	}
	batch, err = p.Advance(12*time.Nanosecond, 2, 4)
	if err != nil || len(batch.Emissions) != 2 {
		t.Fatalf("available after skipped slots: %+v %v", batch, err)
	}
	if batch.Emissions[0].Slot != 11 || batch.Emissions[1].Slot != 12 || batch.Emissions[0].UnitIndex != 0 {
		t.Fatalf("slots/indices after no availability: %+v", batch.Emissions)
	}
	batch, err = p.Advance(13*time.Nanosecond, 8, 4)
	if err != nil || len(batch.Emissions) != 1 || batch.Emissions[0].Slot != 13 || batch.Emissions[0].UnitIndex != 2 {
		t.Fatalf("post underrun: %+v %v", batch, err)
	}
}

func TestAdvancePartialAvailabilityUsesLatestDueSlots(t *testing.T) {
	p, _ := NewPLL(8_000_000_000, 1)
	batch, err := p.Advance(12*time.Nanosecond, 2, 1)
	if err != nil || len(batch.Emissions) != 1 || !batch.MoreDue {
		t.Fatalf("first partial batch: %+v %v", batch, err)
	}
	if e := batch.Emissions[0]; e.UnitIndex != 0 || e.Slot != 11 || e.Timestamp != 11*time.Nanosecond {
		t.Fatalf("first partial emission: %+v", e)
	}
	batch, err = p.Advance(12*time.Nanosecond, 1, 1)
	if err != nil || len(batch.Emissions) != 1 || batch.MoreDue {
		t.Fatalf("drained partial batch: %+v %v", batch, err)
	}
	if e := batch.Emissions[0]; e.UnitIndex != 1 || e.Slot != 12 || e.Timestamp != 12*time.Nanosecond {
		t.Fatalf("second partial emission: %+v", e)
	}
}

func TestAdvanceRejectsInvalidInputsWithoutMutation(t *testing.T) {
	p, _ := NewPLL(8_000_000_000, 1)
	for _, max := range []uint64{0, MaxBatchSize + 1} {
		if _, err := p.Advance(0, 1, max); !errors.Is(err, ErrInvalidBatchSize) {
			t.Fatalf("max=%d: %v", max, err)
		}
	}
	if _, err := p.Advance(-1, 1, 1); !errors.Is(err, ErrInvalidNow) {
		t.Fatal(err)
	}
	batch, err := p.Advance(0, 1, 1)
	if err != nil || len(batch.Emissions) != 1 || batch.Emissions[0].Slot != 0 {
		t.Fatalf("state after invalid calls: %+v %v", batch, err)
	}
}

func TestAdvanceUnitIndexOverflowDoesNotMutatePacer(t *testing.T) {
	p, _ := NewPLL(8_000_000_000, 1)
	p.unitCount = ^uint64(0)
	if _, err := p.Advance(0, 1, 1); !errors.Is(err, ErrUnitIndexOverflow) {
		t.Fatalf("unit index overflow: %v", err)
	}
	if p.nextSlot != 0 || p.unitCount != ^uint64(0) {
		t.Fatalf("overflow mutated state: nextSlot=%d unitCount=%d", p.nextSlot, p.unitCount)
	}
}

func TestMotivatingRateEmissionCountsAcrossWindowsAndJitter(t *testing.T) {
	cases := []struct {
		rate       int64
		window     time.Duration
		wantCount  uint64
		lastSlot   uint64
		lastTimeNS int64
	}{
		{19_392_000, 100 * time.Millisecond, 1_290, 1_289, 99_971_947},
		{19_392_000, 500 * time.Millisecond, 6_447, 6_446, 499_937_293},
		{100_000_000, 100 * time.Millisecond, 6_649, 6_648, 99_985_920},
		{100_000_000, 500 * time.Millisecond, 33_245, 33_244, 499_989_760},
	}
	for _, tc := range cases {
		for _, jitter := range []time.Duration{100 * time.Microsecond, time.Millisecond, 5 * time.Millisecond, 37 * time.Millisecond} {
			t.Run(fmt.Sprintf("rate=%d/window=%s/jitter=%s", tc.rate, tc.window, jitter), func(t *testing.T) {
				p, err := NewPLL(tc.rate, 188)
				if err != nil {
					t.Fatal(err)
				}
				available := uint64(1_000_000) // enough input units; the pacer must not invent more
				var emitted uint64
				drainAt := func(now time.Duration) {
					t.Helper()
					for {
						batch, err := p.Advance(now, available, MaxBatchSize)
						if err != nil {
							t.Fatal(err)
						}
						for _, e := range batch.Emissions {
							if e.UnitIndex != emitted || e.Slot != emitted {
								t.Fatalf("emission order at %d: %+v", emitted, e)
							}
							want := independentTimestamp(tc.rate, 188, emitted)
							if e.Timestamp != want {
								t.Fatalf("timestamp at unit %d: got %s want %s", emitted, e.Timestamp, want)
							}
							emitted++
						}
						available -= uint64(len(batch.Emissions))
						if !batch.MoreDue {
							return
						}
					}
				}

				for now := time.Duration(0); ; {
					drainAt(now)
					if now == tc.window {
						break
					}
					now += jitter
					if now > tc.window {
						now = tc.window
					}
				}
				if emitted != tc.wantCount {
					t.Fatalf("rate=%d window=%s jitter=%s emitted=%d want=%d", tc.rate, tc.window, jitter, emitted, tc.wantCount)
				}
				if emitted == 0 || emitted-1 != tc.lastSlot {
					t.Fatalf("last slot=%d want=%d", emitted-1, tc.lastSlot)
				}
				last, err := p.VirtualTime(tc.lastSlot)
				if err != nil || last != time.Duration(tc.lastTimeNS) {
					t.Fatalf("last virtual timestamp=%s,%v want=%dns", last, err, tc.lastTimeNS)
				}
			})
		}
	}
}

func independentTimestamp(rate, size int64, slot uint64) time.Duration {
	n := new(big.Int).SetUint64(slot)
	n.Mul(n, big.NewInt(size))
	n.Mul(n, big.NewInt(8))
	n.Mul(n, big.NewInt(1_000_000_000))
	n.Quo(n, big.NewInt(rate))
	return time.Duration(n.Int64())
}

func TestProducerFasterAndSlowerThanNominal(t *testing.T) {
	t.Run("faster producer remains caller-owned after due slots", func(t *testing.T) {
		p, _ := NewPLL(8_000_000_000, 1) // one scheduled unit per nanosecond
		available := uint64(20)
		var emissions []Emission
		for {
			batch, err := p.Advance(5*time.Nanosecond, available, 2)
			if err != nil {
				t.Fatal(err)
			}
			emissions = append(emissions, batch.Emissions...)
			available -= uint64(len(batch.Emissions))
			if !batch.MoreDue {
				break
			}
		}
		if len(emissions) != 6 || available != 14 {
			t.Fatalf("emissions=%d remaining=%d; want 6 due and 14 still caller-owned", len(emissions), available)
		}
		for i, e := range emissions {
			if e.UnitIndex != uint64(i) || e.Slot != uint64(i) || e.Timestamp != time.Duration(i)*time.Nanosecond {
				t.Fatalf("emission %d: %+v", i, e)
			}
		}
		batch, err := p.Advance(5*time.Nanosecond, available, 2)
		if err != nil || len(batch.Emissions) != 0 {
			t.Fatalf("same-time excess units should wait: %+v %v", batch, err)
		}
		batch, err = p.Advance(6*time.Nanosecond, available, 2)
		if err != nil || len(batch.Emissions) != 1 || batch.Emissions[0].Slot != 6 || batch.Emissions[0].UnitIndex != 6 {
			t.Fatalf("next-rate opportunity: %+v %v", batch, err)
		}
	})

	t.Run("slower producer leaves slot gaps but unit indices stay sequential", func(t *testing.T) {
		p, _ := NewPLL(8_000_000_000, 1)
		var unitIndex uint64
		for _, tc := range []struct {
			now       time.Duration
			available uint64
			firstSlot uint64
			count     uint64
		}{
			{10 * time.Nanosecond, 3, 8, 3},
			{20 * time.Nanosecond, 2, 19, 2},
		} {
			batch, err := p.Advance(tc.now, tc.available, 4)
			if err != nil || uint64(len(batch.Emissions)) != tc.count || batch.MoreDue {
				t.Fatalf("now=%s batch=%+v err=%v", tc.now, batch, err)
			}
			for i, e := range batch.Emissions {
				wantSlot := tc.firstSlot + uint64(i)
				if e.UnitIndex != unitIndex || e.Slot != wantSlot || e.Timestamp != time.Duration(wantSlot)*time.Nanosecond {
					t.Fatalf("emission: %+v; want unit=%d slot=%d", e, unitIndex, wantSlot)
				}
				unitIndex++
			}
		}
	})
}

func TestNewPLLRejectsInvalidAndUnrepresentableConfiguration(t *testing.T) {
	for _, tc := range []struct{ rate, size int64 }{
		{0, 1}, {-1, 1}, {1, 0}, {1, -1},
		{int64(^uint64(0) >> 1), int64(^uint64(0) >> 1)}, // unit bit count overflow
		{8_000_000_001, 1},            // sub-nanosecond period
		{1, int64(^uint64(0)>>1) / 8}, // period exceeds time.Duration
	} {
		if p, err := NewPLL(tc.rate, tc.size); err == nil || p != nil {
			t.Errorf("NewPLL(%d,%d) = %v,%v", tc.rate, tc.size, p, err)
		}
	}
	if p, err := NewPLL(1_000_000, 188, nil); err == nil || p != nil {
		t.Errorf("nil option: %v,%v", p, err)
	}
}

func TestNewPLLPropagatesOptionError(t *testing.T) {
	want := errors.New("option failure")
	p, err := NewPLL(1_000_000, 188, func(*options) error { return want })
	if p != nil || !errors.Is(err, want) {
		t.Fatalf("NewPLL with failing option = %v,%v; want nil and %v", p, err, want)
	}
}

func TestVirtualTimeReportsDurationOverflow(t *testing.T) {
	p, err := NewPLL(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	slot := uint64(int64(^uint64(0)>>1)/8_000_000_000) + 1
	if _, err := p.VirtualTime(slot); !errors.Is(err, ErrTimeOverflow) {
		t.Fatalf("VirtualTime(%d) error = %v; want %v", slot, err, ErrTimeOverflow)
	}
}
