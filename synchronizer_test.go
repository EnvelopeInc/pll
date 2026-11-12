package pll

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"testing"
	"time"
)

func testSync(t *testing.T, p *PLL) *Synchronizer {
	t.Helper()
	zero := time.Duration(0)
	s, err := NewSynchronizer(p, SyncConfig{ReferenceTimeAtVirtualZero: &zero, Kp: 1, Ki: 0.01, MaxCorrectionPPM: 1000, SlewLimitPPMPerSecond: 1000, MaxSampleAge: time.Hour, MaxReferenceInterval: time.Hour, MaxPhaseResidual: time.Second, LockTolerance: time.Millisecond, LockSamples: 2})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func emissionAt(t *testing.T, p *PLL, now time.Duration) Emission {
	t.Helper()
	b, e := p.Advance(now, 1, 1)
	if e != nil || len(b.Emissions) != 1 {
		t.Fatalf("Advance: %+v %v", b, e)
	}
	return b.Emissions[0]
}
func TestSynchronizationCorrectsScheduledAndAdvanceButNotSnapshots(t *testing.T) {
	p, e := NewPLL(8_000_000, 1)
	if e != nil {
		t.Fatal(e)
	}
	s := testSync(t, p)
	first := emissionAt(t, p, 0)
	if err := s.Observe(time.Second, ReferenceSample{Emission: first, ReferenceTime: first.Timestamp, ObservedAt: time.Second}); err != nil {
		t.Fatal(err)
	}
	second := emissionAt(t, p, time.Microsecond)
	before := second.Timestamp
	if err := s.Observe(2*time.Second, ReferenceSample{Emission: second, ReferenceTime: second.Timestamp + time.Millisecond, ObservedAt: 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if s.Trim() <= 0 {
		t.Fatalf("positive phase error did not lengthen period: %g", s.Trim())
	}
	if second.Timestamp != before {
		t.Fatal("emission snapshot mutated")
	}
	start := p.syncClock.start
	target := start + 1000
	q := big.NewInt(p.syncClock.q)
	baseNum := new(big.Int).Mul(new(big.Int).SetUint64(p.clock.bitsPerUnit), big.NewInt(1_000_000_000))
	anchorNum := new(big.Int).Mul(new(big.Int).SetUint64(start), baseNum)
	anchorDen := new(big.Int).SetUint64(p.clock.rate)
	stepNum := new(big.Int).Mul(new(big.Int).SetUint64(target-start), baseNum)
	stepNum.Mul(stepNum, new(big.Int).Add(big.NewInt(correctionDenominator), q))
	oracleNum := new(big.Int).Add(new(big.Int).Mul(anchorNum, big.NewInt(correctionDenominator)), stepNum)
	oracleDen := new(big.Int).Mul(anchorDen, big.NewInt(correctionDenominator))
	oracle := new(big.Int).Quo(oracleNum, oracleDen)
	got, err := p.ScheduledTime(target)
	if err != nil || !oracle.IsInt64() || int64(got) != oracle.Int64() {
		t.Fatalf("corrected schedule=%s err=%v oracle=%s", got, err, oracle)
	}
	if _, err := p.ScheduledTime(0); !errors.Is(err, ErrHistoricalSchedule) {
		t.Fatalf("historical query error=%v", err)
	}
	v, _ := p.VirtualTime(10_000_000)
	scheduled, err := p.ScheduledTime(10_000_000)
	if err != nil || scheduled <= v {
		t.Fatalf("scheduled=%s nominal=%s err=%v", scheduled, v, err)
	}
	batch, err := p.Advance(10*time.Millisecond, 1, 1)
	if err != nil || len(batch.Emissions) != 1 {
		t.Fatalf("corrected Advance: %+v %v", batch, err)
	}
	nominal, _ := p.VirtualTime(batch.Emissions[0].Slot)
	if batch.Emissions[0].Timestamp == nominal {
		t.Fatal("corrected due emission retained nominal timestamp")
	}
}
func TestCorrectedLatestDueUsesExactRationalBoundary(t *testing.T) {
	p, _ := NewPLL(3_000_000_000, 1)
	p.syncClock = &correctedClock{start: 0, anchor: new(big.Rat), period: big.NewRat(8, 3)}
	stamp, err := p.ScheduledTime(1)
	if err != nil || stamp != 2*time.Nanosecond {
		t.Fatalf("slot 1 timestamp=%s err=%v", stamp, err)
	}
	latest, err := p.latestDue(2 * time.Nanosecond)
	if err != nil || latest != 0 {
		t.Fatalf("slot 1 became due at 2ns: latest=%d err=%v", latest, err)
	}
	latest, err = p.latestDue(3 * time.Nanosecond)
	if err != nil || latest != 1 {
		t.Fatalf("slot 1 not due at 3ns: latest=%d err=%v", latest, err)
	}
	p.syncClock = &correctedClock{start: 1, anchor: big.NewRat(8, 3), period: big.NewRat(8, 3)}
	p.nextSlot = 1
	latest, err = p.latestDue(2)
	if err != nil || latest >= p.syncClock.start {
		t.Fatalf("pre-anchor result=%d err=%v", latest, err)
	}
	batch, err := p.Advance(2, 1, 1)
	if err != nil || len(batch.Emissions) != 0 {
		t.Fatalf("fractional anchor emitted early: batch=%+v err=%v", batch, err)
	}
	batch, err = p.Advance(3, 1, 1)
	if err != nil || len(batch.Emissions) != 1 || batch.Emissions[0].Slot != 1 {
		t.Fatalf("fractional anchor not due at 3ns: batch=%+v err=%v", batch, err)
	}
}

func TestSynchronizationRejectsForgedForeignAndOutOfOrder(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	s := testSync(t, p)
	em := emissionAt(t, p, 0)
	bad := em
	bad.Timestamp++
	if err := s.Observe(time.Second, ReferenceSample{Emission: bad, ReferenceTime: 0, ObservedAt: 0}); !errors.Is(err, ErrInvalidEmission) {
		t.Fatalf("modified emission: %v", err)
	}
	other, _ := NewPLL(8_000_000, 1)
	foreign := emissionAt(t, other, 0)
	if err := s.Observe(time.Second, ReferenceSample{Emission: foreign, ReferenceTime: 0, ObservedAt: 0}); !errors.Is(err, ErrInvalidEmission) {
		t.Fatalf("foreign emission: %v", err)
	}
	forgedSlot := em
	forgedSlot.Slot = p.nextSlot
	forgedSlot.assignedSlot = p.nextSlot
	forgedSlot.Timestamp, _ = p.ScheduledTime(p.nextSlot)
	forgedSlot.assignedTimestamp = forgedSlot.Timestamp
	if err := s.Observe(time.Second, ReferenceSample{Emission: forgedSlot, ReferenceTime: 0, ObservedAt: 0}); !errors.Is(err, ErrUnassignedEmission) {
		t.Fatalf("unassigned slot: %v", err)
	}
	if err := s.Observe(time.Second, ReferenceSample{Emission: em, ReferenceTime: 0, ObservedAt: 0}); err != nil {
		t.Fatal(err)
	}
	if err := s.Observe(time.Second, ReferenceSample{Emission: em, ReferenceTime: 1, ObservedAt: 0}); !errors.Is(err, ErrReferenceOrder) {
		t.Fatalf("duplicate: %v", err)
	}
}
func TestEmissionWholeValueCopyPreservesProvenanceButJSONDoesNot(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	s := testSync(t, p)
	emission := emissionAt(t, p, 0)
	encoded, err := json.Marshal(emission)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Emission
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.UnitIndex != emission.UnitIndex || decoded.Slot != emission.Slot || decoded.Timestamp != emission.Timestamp {
		t.Fatalf("exported fields did not round trip: got %+v want %+v", decoded, emission)
	}
	if err := s.Observe(0, ReferenceSample{Emission: decoded}); !errors.Is(err, ErrInvalidEmission) {
		t.Fatalf("decoded emission accepted as a reference: %v", err)
	}
	copied := emission
	if err := s.Observe(0, ReferenceSample{Emission: copied}); err != nil {
		t.Fatalf("whole-value copy lost provenance: %v", err)
	}
}

func TestSynchronizationEpochResetAndStaleness(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	s := testSync(t, p)
	a := emissionAt(t, p, 0)
	if err := s.Observe(time.Second, ReferenceSample{Emission: a, ReferenceTime: 0, ObservedAt: 0, Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	b := emissionAt(t, p, time.Microsecond)
	if err := s.Observe(2*time.Second, ReferenceSample{Emission: b, ReferenceTime: time.Microsecond, ObservedAt: 0, Epoch: 2}); !errors.Is(err, ErrEpochChange) {
		t.Fatalf("epoch change: %v", err)
	}
	if err := s.ResetEpoch(2, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Observe(2*time.Second, ReferenceSample{Emission: b, ReferenceTime: time.Microsecond, ObservedAt: 0, Epoch: 2}); err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	if err := s.Observe(2*time.Hour, ReferenceSample{Emission: b, ReferenceTime: 2 * time.Microsecond, ObservedAt: 0, Epoch: 2}); !errors.Is(err, ErrStaleReference) {
		t.Fatalf("stale: %v", err)
	}
}
func TestLockRequiresConsecutiveLowResidualSamplesAndStatusHoldover(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	zero := time.Duration(0)
	cfg := SyncConfig{ReferenceTimeAtVirtualZero: &zero, Kp: 1e-6, Ki: 1e-6, MaxCorrectionPPM: 100, SlewLimitPPMPerSecond: 100, MaxSampleAge: time.Second, MaxReferenceInterval: time.Hour, MaxPhaseResidual: time.Second, LockTolerance: time.Nanosecond, LockSamples: 3}
	s, err := NewSynchronizer(p, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		em := emissionAt(t, p, time.Duration(i+1)*time.Microsecond)
		if err := s.Observe(time.Duration(i+1)*time.Second, ReferenceSample{Emission: em, ReferenceTime: em.Timestamp, ObservedAt: time.Duration(i+1) * time.Second}); err != nil {
			t.Fatal(err)
		}
		want := SyncAcquiring
		if i == 2 {
			want = SyncLocked
		}
		if s.State() != want {
			t.Fatalf("sample %d state=%v want=%v", i, s.State(), want)
		}
	}
	trim := s.Trim()
	if s.Status(5*time.Second) != SyncHoldover || s.Trim() != trim {
		t.Fatalf("stale status changed mapping/state: %v trim=%g", s.Status(5*time.Second), s.Trim())
	}
}

func TestSynchronizerRequiresPositivePIStageGains(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	zero := time.Duration(0)
	base := SyncConfig{ReferenceTimeAtVirtualZero: &zero, Kp: 1, Ki: 1, MaxCorrectionPPM: 100, SlewLimitPPMPerSecond: 100, MaxSampleAge: time.Second, MaxReferenceInterval: time.Second, MaxPhaseResidual: time.Second, LockTolerance: time.Millisecond, LockSamples: 2}
	for _, tc := range []struct {
		name string
		kp   float64
		ki   float64
	}{{"zero proportional gain", 0, 1}, {"zero integral gain", 1, 0}} {
		cfg := base
		cfg.Kp, cfg.Ki = tc.kp, tc.ki
		if _, err := NewSynchronizer(p, cfg); !errors.Is(err, ErrInvalidSyncConfig) {
			t.Errorf("%s: got %v, want invalid config", tc.name, err)
		}
	}
}

func TestSynchronizerCopiesReferenceEpochConfig(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	offset := time.Duration(0)
	s, err := NewSynchronizer(p, SyncConfig{ReferenceTimeAtVirtualZero: &offset, Kp: 1, Ki: 0.01, MaxCorrectionPPM: 100, SlewLimitPPMPerSecond: 100, MaxSampleAge: time.Second, MaxReferenceInterval: time.Second, MaxPhaseResidual: time.Second, LockTolerance: time.Millisecond, LockSamples: 2})
	if err != nil {
		t.Fatal(err)
	}
	offset = 5 * time.Second
	first := emissionAt(t, p, 0)
	if err := s.Observe(0, ReferenceSample{Emission: first, ReferenceTime: first.Timestamp, ObservedAt: 0}); err != nil {
		t.Fatalf("caller mutation changed configured epoch: %v", err)
	}
	second := emissionAt(t, p, time.Microsecond)
	if err := s.Observe(time.Microsecond, ReferenceSample{Emission: second, ReferenceTime: second.Timestamp, ObservedAt: time.Microsecond}); err != nil {
		t.Fatalf("copied epoch did not remain stable: %v", err)
	}
	if s.Trim() != 0 {
		t.Fatalf("mutated config changed trim: %g", s.Trim())
	}
}

func TestLockToleranceHandlesMinimumDurationWithoutOverflow(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	zero := time.Duration(0)
	s, err := NewSynchronizer(p, SyncConfig{ReferenceTimeAtVirtualZero: &zero, Kp: 1, Ki: 1, MaxCorrectionPPM: 100, SlewLimitPPMPerSecond: 100, MaxSampleAge: time.Second, MaxReferenceInterval: time.Second, MaxPhaseResidual: time.Duration(math.MaxInt64), LockTolerance: time.Duration(math.MaxInt64), LockSamples: 1})
	if err != nil {
		t.Fatal(err)
	}
	em := emissionAt(t, p, 0)
	if err := s.Observe(0, ReferenceSample{Emission: em, ReferenceTime: time.Duration(math.MinInt64), ObservedAt: 0}); !errors.Is(err, ErrPhaseResidual) {
		t.Fatalf("MinInt64 residual was not rejected safely: %v", err)
	}
	if s.State() == SyncLocked {
		t.Fatal("MinInt64 phase residual incorrectly locked")
	}
}

func TestLockDoesNotConvergeWithResidualOutsideTolerance(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	s := testSync(t, p)
	for i := 0; i < 4; i++ {
		em := emissionAt(t, p, time.Duration(i+1)*10*time.Millisecond)
		if err := s.Observe(time.Duration(i+1)*time.Second, ReferenceSample{Emission: em, ReferenceTime: em.Timestamp + 2*time.Millisecond, ObservedAt: time.Duration(i+1) * time.Second}); err != nil {
			t.Fatal(err)
		}
	}
	if s.State() == SyncLocked {
		t.Fatal("locked despite residual outside tolerance")
	}
}

func TestNegativePhaseLengthensNoPeriodAndHoldoverRetainsTrim(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	s := testSync(t, p)
	a := emissionAt(t, p, 0)
	if err := s.Observe(time.Second, ReferenceSample{Emission: a, ReferenceTime: 0, ObservedAt: 0}); err != nil {
		t.Fatal(err)
	}
	b := emissionAt(t, p, time.Second)
	ref := b.Timestamp - 100*time.Nanosecond
	if err := s.Observe(2*time.Second, ReferenceSample{Emission: b, ReferenceTime: ref, ObservedAt: time.Second}); err != nil {
		t.Fatal(err)
	}
	if s.Trim() >= 0 {
		t.Fatalf("negative phase did not shorten period: %g", s.Trim())
	}
	trim := s.Trim()
	s.Holdover()
	if s.State() != SyncHoldover || s.Trim() != trim {
		t.Fatalf("holdover changed trim/state: %g %v", s.Trim(), s.State())
	}
}

func TestSaturationFreezesIntegrator(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	zero := time.Duration(0)
	cfg := SyncConfig{ReferenceTimeAtVirtualZero: &zero, Kp: 100, Ki: 1e8, MaxCorrectionPPM: 100, SlewLimitPPMPerSecond: 100, MaxSampleAge: time.Hour, MaxReferenceInterval: time.Hour, MaxPhaseResidual: time.Second, LockTolerance: time.Millisecond, LockSamples: 2}
	s, err := NewSynchronizer(p, cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := emissionAt(t, p, 0)
	if err := s.Observe(time.Second, ReferenceSample{Emission: a, ReferenceTime: 0, ObservedAt: 0}); err != nil {
		t.Fatal(err)
	}
	b := emissionAt(t, p, time.Second)
	if err := s.Observe(2*time.Second, ReferenceSample{Emission: b, ReferenceTime: b.Timestamp + 500*time.Millisecond, ObservedAt: time.Second}); err != nil {
		t.Fatal(err)
	}
	if s.Trim() != 100e-6 || s.integral != 0 {
		t.Fatalf("saturation trim=%g integral=%g", s.Trim(), s.integral)
	}
}

func TestSlewSaturationFreezesIntegralAndAllowsUnwind(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	zero := time.Duration(0)
	cfg := SyncConfig{ReferenceTimeAtVirtualZero: &zero, Kp: 1, Ki: 1e8, MaxCorrectionPPM: 1000, SlewLimitPPMPerSecond: 1, MaxSampleAge: time.Hour, MaxReferenceInterval: time.Hour, MaxPhaseResidual: time.Second, LockTolerance: time.Millisecond, LockSamples: 2}
	s, err := NewSynchronizer(p, cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := emissionAt(t, p, 0)
	if err := s.Observe(time.Second, ReferenceSample{Emission: a, ReferenceTime: a.Timestamp, ObservedAt: 0}); err != nil {
		t.Fatal(err)
	}
	b := emissionAt(t, p, time.Second)
	if err := s.Observe(2*time.Second, ReferenceSample{Emission: b, ReferenceTime: b.Timestamp + 100*time.Millisecond, ObservedAt: time.Second}); err != nil {
		t.Fatal(err)
	}
	if s.integral != 0 || s.Trim() <= 0 || s.Trim() > 2e-6 {
		t.Fatalf("slew saturation trim=%g integral=%g", s.Trim(), s.integral)
	}
	trimBeforeUnwind := s.Trim()
	c := emissionAt(t, p, 2*time.Second)
	if err := s.Observe(3*time.Second, ReferenceSample{Emission: c, ReferenceTime: c.Timestamp - 50*time.Millisecond, ObservedAt: 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if s.Trim() >= trimBeforeUnwind {
		t.Fatalf("trim did not unwind: before=%g after=%g", trimBeforeUnwind, s.Trim())
	}
}

func TestMinimumPeriodClampDoesNotWindIntegrator(t *testing.T) {
	p, err := NewPLL(8_000_000_000, 1) // exactly 1ns nominal period
	if err != nil {
		t.Fatal(err)
	}
	zero := time.Duration(0)
	s, err := NewSynchronizer(p, SyncConfig{ReferenceTimeAtVirtualZero: &zero, Kp: 1, Ki: 1e9, MaxCorrectionPPM: 100, SlewLimitPPMPerSecond: 100, MaxSampleAge: time.Hour, MaxReferenceInterval: time.Hour, MaxPhaseResidual: time.Second, LockTolerance: time.Nanosecond, LockSamples: 2})
	if err != nil {
		t.Fatal(err)
	}
	a := emissionAt(t, p, 0)
	if err := s.Observe(time.Second, ReferenceSample{Emission: a, ReferenceTime: 0, ObservedAt: time.Second}); err != nil {
		t.Fatal(err)
	}
	b := emissionAt(t, p, 2*time.Nanosecond)
	if err := s.Observe(2*time.Second, ReferenceSample{Emission: b, ReferenceTime: time.Nanosecond, ObservedAt: 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if s.Trim() != 0 || s.integral != 0 {
		t.Fatalf("minimum-period saturation wound integrator: trim=%g integral=%g", s.Trim(), s.integral)
	}
	if p.syncClock != nil && p.syncClock.period.Cmp(big.NewRat(1, 1)) < 0 {
		t.Fatalf("period below one nanosecond: %s", p.syncClock.period)
	}
}

func TestCorrectionAnchorsAtPendingCursor(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	s := testSync(t, p)
	batch, err := p.Advance(10*time.Microsecond, 10, 1)
	if err != nil || !batch.MoreDue {
		t.Fatalf("first pending batch: %+v %v", batch, err)
	}
	a := batch.Emissions[0]
	if err := s.Observe(time.Second, ReferenceSample{Emission: a, ReferenceTime: a.Timestamp, ObservedAt: 0}); err != nil {
		t.Fatal(err)
	}
	batch, err = p.Advance(10*time.Microsecond, 9, 1)
	if err != nil || !batch.MoreDue {
		t.Fatalf("second pending batch: %+v %v", batch, err)
	}
	b := batch.Emissions[0]
	anchorSlot := p.nextSlot
	if err := s.Observe(2*time.Second, ReferenceSample{Emission: b, ReferenceTime: b.Timestamp + time.Millisecond, ObservedAt: time.Second}); err != nil {
		t.Fatal(err)
	}
	if p.syncClock.start != anchorSlot {
		t.Fatalf("segment starts %d, want cursor %d", p.syncClock.start, anchorSlot)
	}
	want, _ := p.VirtualTime(anchorSlot)
	got, err := p.ScheduledTime(anchorSlot)
	if err != nil || got != want {
		t.Fatalf("reanchor changed cursor time: %s want %s err=%v", got, want, err)
	}
}

func TestRelativeEpochAnchorsAgainAfterReset(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	cfg := SyncConfig{FirstSampleAnchoring: true, Kp: 1, Ki: 0.01, MaxCorrectionPPM: 100, SlewLimitPPMPerSecond: 100, MaxSampleAge: time.Hour, MaxReferenceInterval: time.Hour, MaxPhaseResidual: time.Second, LockTolerance: time.Millisecond, LockSamples: 2}
	s, err := NewSynchronizer(p, cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := emissionAt(t, p, 0)
	if err := s.Observe(time.Second, ReferenceSample{Emission: a, ReferenceTime: 10 * time.Second, ObservedAt: 0, Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	b := emissionAt(t, p, time.Microsecond)
	if err := s.Observe(2*time.Second, ReferenceSample{Emission: b, ReferenceTime: 10*time.Second + b.Timestamp, ObservedAt: time.Second, Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetEpoch(2); err != nil {
		t.Fatal(err)
	}
	c := emissionAt(t, p, 2*time.Microsecond)
	if err := s.Observe(3*time.Second, ReferenceSample{Emission: c, ReferenceTime: 100*time.Second + c.Timestamp, ObservedAt: 0, Epoch: 2}); err != nil {
		t.Fatalf("relative reacquisition did not re-anchor: %v", err)
	}
}

func TestNominalScheduledTimeAndAdvanceRemainExactWithoutSynchronizer(t *testing.T) {
	p, _ := NewPLL(3_000_000_000, 1)
	for i := uint64(0); i < 100; i++ {
		a, _ := p.VirtualTime(i)
		b, e := p.ScheduledTime(i)
		if e != nil || a != b {
			t.Fatalf("slot %d: %v %v %v", i, a, b, e)
		}
	}
	b, e := p.Advance(10*time.Nanosecond, 20, 20)
	if e != nil {
		t.Fatal(e)
	}
	for _, em := range b.Emissions {
		want, _ := p.VirtualTime(em.Slot)
		if em.Timestamp != want {
			t.Fatalf("slot %d timestamp=%s want=%s", em.Slot, em.Timestamp, want)
		}
	}
}

// referencePeriodOracle independently defines the synthetic reference period
// from a signed frequency offset. Positive ppm means more cycles per second,
// hence a shorter period. The nominal one-byte period is 1000ns.
func referencePeriodOracle(ppm int64) *big.Rat {
	return big.NewRat(1_000_000_000, 1_000_000+ppm)
}

func referenceTimeForSlot(ppm int64, slot uint64) time.Duration {
	r := new(big.Rat).Mul(referencePeriodOracle(ppm), new(big.Rat).SetUint64(slot))
	return time.Duration(new(big.Int).Quo(r.Num(), r.Denom()).Int64())
}

func syncSimulation(t *testing.T, ppm int64) (*PLL, *Synchronizer, time.Duration) {
	t.Helper()
	p, err := NewPLL(8_000_000, 1)
	if err != nil {
		t.Fatal(err)
	}
	zero := time.Duration(0)
	cfg := SyncConfig{ReferenceTimeAtVirtualZero: &zero, Kp: 10, Ki: 100, MaxCorrectionPPM: 500, SlewLimitPPMPerSecond: 1000, MaxSampleAge: time.Second, MaxReferenceInterval: time.Second, MaxPhaseResidual: time.Second, LockTolerance: 2 * time.Microsecond, LockSamples: 3}
	s, err := NewSynchronizer(p, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p, s, time.Duration(10_000_000) // 10ms update cadence
}

func runReferenceFrequencySimulation(t *testing.T, ppm int64) (*PLL, *Synchronizer, time.Duration, float64) {
	t.Helper()
	p, s, cadence := syncSimulation(t, ppm)
	recentMin, recentMax := math.Inf(1), math.Inf(-1)
	for i := int64(0); i <= 250; i++ {
		now := time.Duration(i) * cadence
		batch, err := p.Advance(now, 1, 1)
		if err != nil || len(batch.Emissions) != 1 {
			t.Fatalf("simulation Advance step %d: batch=%+v err=%v", i, batch, err)
		}
		em := batch.Emissions[0]
		ref := referenceTimeForSlot(ppm, em.Slot)
		if err := s.Observe(now, ReferenceSample{Emission: em, ReferenceTime: ref, ObservedAt: now}); err != nil {
			t.Fatalf("simulation Observe step %d: %v", i, err)
		}
		if i >= 231 {
			recentMin = math.Min(recentMin, s.Trim()*1e6)
			recentMax = math.Max(recentMax, s.Trim()*1e6)
		}
	}
	return p, s, cadence, recentMax - recentMin
}

func countDueThrough(t *testing.T, p PLL, now time.Duration) uint64 {
	t.Helper()
	var total uint64
	for i := 0; i < 1_000_000; i++ {
		b, err := p.Advance(now, ^uint64(0), MaxBatchSize)
		if err != nil {
			t.Fatal(err)
		}
		total += uint64(len(b.Emissions))
		if !b.MoreDue {
			return total
		}
	}
	t.Fatal("due-count simulation did not drain")
	return 0
}

func independentReferenceDueCount(t *testing.T, now time.Duration, firstSlot uint64, ppm int64) uint64 {
	t.Helper()
	elapsedSlots := new(big.Rat).Quo(new(big.Rat).SetInt64(int64(now)), referencePeriodOracle(ppm))
	latest := new(big.Int).Quo(elapsedSlots.Num(), elapsedSlots.Denom())
	if !latest.IsUint64() {
		t.Fatalf("reference due-slot index overflows uint64: %s", latest)
	}
	if latest.Uint64() < firstSlot {
		return 0
	}
	return latest.Uint64() - firstSlot + 1
}

func absCountDifference(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

func TestClosedLoopReferenceFrequencyOffsetsConvergeAndChangeAdvanceRate(t *testing.T) {
	for _, ppm := range []int64{-200, 200} {
		t.Run(map[bool]string{true: "positive", false: "negative"}[ppm > 0], func(t *testing.T) {
			p, s, cadence, recentSpreadPPM := runReferenceFrequencySimulation(t, ppm)
			expectedPeriodTrimPPM := -float64(ppm) * 1_000_000 / float64(1_000_000+ppm)
			if math.Abs(s.Trim()*1e6-expectedPeriodTrimPPM) > 30 {
				t.Fatalf("period trim %.2fppm did not converge toward reference frequency %dppm (expected trim %.2fppm)", s.Trim()*1e6, ppm, expectedPeriodTrimPPM)
			}
			if recentSpreadPPM > 30 {
				t.Fatalf("late-run trim unstable: spread %.2fppm", recentSpreadPPM)
			}
			// Compare opportunities over the same fixed one-second horizon from
			// the same unassigned cursor, not merely the controller's trim value.
			cursor := p.nextSlot
			now := 250*cadence + time.Second
			corrected := *p
			corrected.nextSlot, corrected.unitCount = cursor, 0
			nominal := corrected
			nominal.syncClock = nil
			correctedCount := countDueThrough(t, corrected, now)
			nominalCount := countDueThrough(t, nominal, now)
			wantReferenceCount := independentReferenceDueCount(t, now, cursor, ppm)
			if diff := absCountDifference(correctedCount, wantReferenceCount); diff > 100 {
				t.Fatalf("Advance count=%d diverges from independent reference count=%d by %d", correctedCount, wantReferenceCount, diff)
			}
			if ppm > 0 && correctedCount <= nominalCount {
				t.Fatalf("positive frequency offset did not increase Advance opportunities: corrected=%d nominal=%d", correctedCount, nominalCount)
			}
			if ppm < 0 && correctedCount >= nominalCount {
				t.Fatalf("negative frequency offset did not decrease Advance opportunities: corrected=%d nominal=%d", correctedCount, nominalCount)
			}
			if absCountDifference(correctedCount, nominalCount) < 100 {
				t.Fatalf("Advance rate difference too small: corrected=%d nominal=%d", correctedCount, nominalCount)
			}
			b, err := p.Advance(now, 1, 1)
			if err != nil || len(b.Emissions) != 1 {
				t.Fatalf("corrected timestamp Advance: %+v %v", b, err)
			}
			want, err := p.ScheduledTime(b.Emissions[0].Slot)
			if err != nil || b.Emissions[0].Timestamp != want {
				t.Fatalf("emission timestamp=%s scheduled=%s err=%v", b.Emissions[0].Timestamp, want, err)
			}
		})
	}
}

func TestJitterBoundAndDelayedFreshObservationEquivalence(t *testing.T) {
	makePair := func() (*PLL, *Synchronizer) {
		p, _ := NewPLL(8_000_000, 1)
		zero := time.Duration(0)
		s, err := NewSynchronizer(p, SyncConfig{ReferenceTimeAtVirtualZero: &zero, Kp: 1, Ki: 1000, MaxCorrectionPPM: 500, SlewLimitPPMPerSecond: 1000, MaxSampleAge: time.Second, MaxReferenceInterval: time.Second, MaxPhaseResidual: time.Second, LockTolerance: time.Microsecond, LockSamples: 3})
		if err != nil {
			t.Fatal(err)
		}
		return p, s
	}
	p1, s1 := makePair()
	p2, s2 := makePair()
	maxNoiseTrim := 0.0
	for i := 0; i < 40; i++ {
		now := time.Duration(i+1) * 10 * time.Millisecond
		e1, err := p1.Advance(now, 1, 1)
		if err != nil || len(e1.Emissions) != 1 {
			t.Fatalf("immediate Advance: %+v %v", e1, err)
		}
		e2, err := p2.Advance(now, 1, 1)
		if err != nil || len(e2.Emissions) != 1 {
			t.Fatalf("delayed Advance: %+v %v", e2, err)
		}
		if e1.Emissions[0].Slot != e2.Emissions[0].Slot {
			t.Fatal("paired schedules diverged before observation")
		}
		ref := referenceTimeForSlot(0, e1.Emissions[0].Slot)
		jitter := time.Duration(0)
		if i%2 == 0 {
			jitter = 100 * time.Nanosecond
		} else {
			jitter = -100 * time.Nanosecond
		}
		ref += jitter
		if err := s1.Observe(now, ReferenceSample{Emission: e1.Emissions[0], ReferenceTime: ref, ObservedAt: now}); err != nil {
			t.Fatal(err)
		}
		delayedNow := now + 100*time.Millisecond
		if err := s2.Observe(delayedNow, ReferenceSample{Emission: e2.Emissions[0], ReferenceTime: ref, ObservedAt: delayedNow}); err != nil {
			t.Fatal(err)
		}
		if s1.Trim() != s2.Trim() || s1.integral != s2.integral {
			t.Fatalf("observation latency changed result: trim %.12g vs %.12g", s1.Trim(), s2.Trim())
		}
		if math.Abs(s1.Trim()) > 500e-6 {
			t.Fatalf("jitter caused unbounded trim: %g", s1.Trim())
		}
		if math.Abs(s1.Trim()) > maxNoiseTrim {
			maxNoiseTrim = math.Abs(s1.Trim())
		}
	}
	if maxNoiseTrim > 100e-6 {
		t.Fatalf("alternating 100ns jitter not bounded/rejected: max trim=%g", maxNoiseTrim)
	}
}

func TestExtendedCorrectedRationalScheduleAgainstOracle(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	start := uint64(73)
	anchor := big.NewRat(123456789, 7)
	q := int64(1234567)
	period := new(big.Rat).Mul(big.NewRat(1000, 1), big.NewRat(correctionDenominator+q, correctionDenominator))
	p.syncClock = &correctedClock{start: start, anchor: new(big.Rat).Set(anchor), period: period, q: q}
	previous := time.Duration(-1)
	for i := uint64(0); i < 250_000; i++ {
		slot := start + i
		exact := new(big.Rat).Add(new(big.Rat).Set(anchor), new(big.Rat).Mul(new(big.Rat).SetUint64(i), period))
		wantInt := new(big.Int).Quo(exact.Num(), exact.Denom())
		got, err := p.ScheduledTime(slot)
		if err != nil || !wantInt.IsInt64() || got != time.Duration(wantInt.Int64()) {
			t.Fatalf("slot %d got=%d want=%s err=%v", slot, got, wantInt, err)
		}
		if got <= previous {
			t.Fatalf("schedule not monotonic at slot %d: %d <= %d", slot, got, previous)
		}
		previous = got
	}
}

func TestLongReferenceGapReacquiresWithoutTrimOrPhaseStep(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	zero := time.Duration(0)
	s, err := NewSynchronizer(p, SyncConfig{ReferenceTimeAtVirtualZero: &zero, Kp: 1, Ki: 10, MaxCorrectionPPM: 500, SlewLimitPPMPerSecond: 100, MaxSampleAge: time.Second, MaxReferenceInterval: 20 * time.Millisecond, MaxPhaseResidual: time.Second, LockTolerance: time.Millisecond, LockSamples: 3})
	if err != nil {
		t.Fatal(err)
	}
	a := emissionAt(t, p, 0)
	if err := s.Observe(time.Second, ReferenceSample{Emission: a, ReferenceTime: 0, ObservedAt: time.Second}); err != nil {
		t.Fatal(err)
	}
	b := emissionAt(t, p, time.Millisecond)
	jump := b
	if err := s.Observe(2*time.Second, ReferenceSample{Emission: jump, ReferenceTime: jump.Timestamp + 2*time.Second, ObservedAt: 2 * time.Second}); !errors.Is(err, ErrPhaseResidual) {
		t.Fatalf("long-gap phase discontinuity hidden by rebase: %v", err)
	}
	if s.lastSlot != a.Slot || s.lastReference != 0 {
		t.Fatal("rejected phase discontinuity changed sample history")
	}
	before, err := p.ScheduledTime(p.nextSlot)
	if err != nil {
		t.Fatal(err)
	}
	trim := s.Trim()
	refB := b.Timestamp + 100*time.Millisecond
	if err := s.Observe(2*time.Second, ReferenceSample{Emission: b, ReferenceTime: refB, ObservedAt: 2 * time.Second}); err != nil {
		t.Fatalf("valid long-gap sample rejected: %v", err)
	}
	after, err := p.ScheduledTime(p.nextSlot)
	if err != nil {
		t.Fatal(err)
	}
	if s.State() != SyncReacquiring || s.Trim() != trim || before != after || s.integral != 0 {
		t.Fatalf("gap changed mapping/controller: state=%v trim=%g integral=%g before=%d after=%d", s.State(), s.Trim(), s.integral, before, after)
	}
	c := emissionAt(t, p, 2*time.Millisecond)
	refC := c.Timestamp + 101*time.Millisecond
	if err := s.Observe(3*time.Second, ReferenceSample{Emission: c, ReferenceTime: refC, ObservedAt: 3 * time.Second}); err != nil {
		t.Fatalf("post-gap reacquisition sample rejected: %v", err)
	}
	if s.lastSlot != c.Slot || s.lastReference != refC {
		t.Fatal("post-gap sample did not rebase controller history")
	}
}

func TestNoReferenceAdvanceMatchesIndependentNominalPLL(t *testing.T) {
	p1, _ := NewPLL(8_000_000, 1)
	p2, _ := NewPLL(8_000_000, 1)
	for _, now := range []time.Duration{0, time.Microsecond, 10 * time.Millisecond, time.Second} {
		b1, e1 := p1.Advance(now, 100, 100)
		b2, e2 := p2.Advance(now, 100, 100)
		if e1 != nil || e2 != nil || len(b1.Emissions) != len(b2.Emissions) {
			t.Fatalf("nominal Advances differ: %v %v", e1, e2)
		}
		for i := range b1.Emissions {
			if b1.Emissions[i].Slot != b2.Emissions[i].Slot || b1.Emissions[i].Timestamp != b2.Emissions[i].Timestamp || b1.Emissions[i].UnitIndex != b2.Emissions[i].UnitIndex {
				t.Fatalf("nominal emission %d differs", i)
			}
			want, err := p1.VirtualTime(b1.Emissions[i].Slot)
			if err != nil || b1.Emissions[i].Timestamp != want {
				t.Fatalf("unreferenced emission differs from exact nominal schedule: got=%s want=%s err=%v", b1.Emissions[i].Timestamp, want, err)
			}
		}
	}
}

func TestLockCounterSaturatesAtRequiredSamples(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	s := testSync(t, p)
	s.cfg.LockSamples = ^uint32(0)
	s.lockCount = s.cfg.LockSamples - 1
	for i := 0; i < 3; i++ {
		now := time.Duration(i) * time.Microsecond
		e := emissionAt(t, p, now)
		if err := s.Observe(now, ReferenceSample{Emission: e, ReferenceTime: e.Timestamp, ObservedAt: now}); err != nil {
			t.Fatal(err)
		}
		if s.lockCount != s.cfg.LockSamples || s.State() != SyncLocked {
			t.Fatalf("sample %d: lockCount=%d state=%v; want saturated count and locked", i, s.lockCount, s.State())
		}
	}
}
