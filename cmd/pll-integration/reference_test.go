package main

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/EnvelopeInc/pll"
)

func TestReferenceTickerRunsWithoutSchedulerEmissionsAndJoins(t *testing.T) {
	refs, _ := pll.NewReferenceQueue(1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan referenceSourceStats, 1)
	go func() { done <- runReference(ctx, time.Now(), make(chan emissionSnapshot, 1), refs) }()
	select {
	case stats := <-done:
		if stats.ticks == 0 || stats.unassociated != stats.ticks || stats.submitted != 0 {
			t.Fatalf("reference without scheduler: %+v", stats)
		}
	case <-time.After(time.Second):
		t.Fatal("reference source did not join on cancellation")
	}
	refs.Close()
}

func TestReferenceAssociationPreservesGenuineEmissionAndBoundsAge(t *testing.T) {
	p, _ := pll.NewPLL(rate, unitBytes)
	zero := time.Duration(0)
	syncer, err := pll.NewSynchronizer(p, pll.SyncConfig{ReferenceTimeAtVirtualZero: &zero, Kp: 5, Ki: 6.25, MaxCorrectionPPM: 1000, SlewLimitPPMPerSecond: 2000, MaxSampleAge: time.Second, MaxReferenceInterval: 250 * time.Millisecond, MaxPhaseResidual: time.Second, LockTolerance: 20 * time.Microsecond, LockSamples: 4})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := p.Advance(0, 1, 1)
	if err != nil || len(batch.Emissions) != 1 {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	snapshot := emissionSnapshot{emission: batch.Emissions[0], at: 0}
	tick := makeReferenceTick(10 * time.Millisecond)
	sample, ok := associateReference(tick, snapshot)
	if !ok || sample.Emission != snapshot.emission {
		t.Fatal("genuine snapshot was not retained")
	}
	if err := syncer.Observe(tick.at, sample); err != nil {
		t.Fatalf("private provenance lost: %v", err)
	}
	if got, want := sample.ReferenceTime, oscillator(0, 50, -2*time.Microsecond); math.Abs(float64(got-want)) > 1 {
		t.Fatalf("independent tick projection=%s want=%s", got, want)
	}
	snapshot.at = tick.at + 1
	if _, ok := associateReference(tick, snapshot); ok {
		t.Fatal("future snapshot associated")
	}
	snapshot.at = tick.at - associationMaxAge - 1
	if _, ok := associateReference(tick, snapshot); ok {
		t.Fatal("stale snapshot associated")
	}
}

func TestReferenceMailboxReplacesAndSourceCountsInterruptionAndDuplicates(t *testing.T) {
	mailbox := make(chan emissionSnapshot, 1)
	publishSnapshot(mailbox, emissionSnapshot{emission: pll.Emission{Slot: 1}})
	publishSnapshot(mailbox, emissionSnapshot{emission: pll.Emission{Slot: 2}})
	if len(mailbox) != 1 {
		t.Fatal("mailbox not bounded")
	}
	refs, _ := pll.NewReferenceQueue(1)
	origin := time.Now()
	ticks := make(chan time.Time, 4)
	for _, at := range []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 4 * time.Second, 6 * time.Second} {
		ticks <- origin.Add(at)
	}
	close(ticks)
	stats := collectReferences(context.Background(), origin, ticks, mailbox, refs)
	if stats.ticks != 4 || stats.submitted != 1 || stats.interrupted != 1 || stats.unassociated != 2 || len(stats.rejected) != 0 {
		t.Fatalf("stats=%+v", stats)
	}
	// A second source cannot enqueue into the full bounded reference queue.
	publishSnapshot(mailbox, emissionSnapshot{emission: pll.Emission{Slot: 3}})
	ticks = make(chan time.Time, 1)
	ticks <- origin.Add(10 * time.Millisecond)
	close(ticks)
	stats = collectReferences(context.Background(), origin, ticks, mailbox, refs)
	if stats.rejected[pll.ErrReferenceQueueFull.Error()] != 1 {
		t.Fatalf("stats=%+v", stats)
	}
	refs.Close()
}

func TestReferenceTelemetryObservedTransitionsAndTimeWeightedTrim(t *testing.T) {
	m := newReferenceTelemetry(10 * time.Second)
	for _, v := range []struct {
		at    time.Duration
		state pll.SyncState
		trim  float64
	}{
		{0, pll.SyncAcquiring, 0}, {time.Second, pll.SyncLocked, -50},
		{5 * time.Second, pll.SyncHoldover, -50}, {6 * time.Second, pll.SyncReacquiring, -40},
		{7 * time.Second, pll.SyncLocked, -48}, {9 * time.Second, pll.SyncLocked, -50},
		{9500 * time.Millisecond, pll.SyncLocked, -52}, {10 * time.Second, pll.SyncLocked, -54},
	} {
		m.observe(v.at, v.state, v.trim)
	}
	if len(m.transitions) != 5 || m.durations != [4]time.Duration{time.Second, 7 * time.Second, time.Second, time.Second} {
		t.Fatalf("transitions=%v durations=%v", m.transitions, m.durations)
	}
	if m.finalTrim != -54 || m.minTrim != -54 || m.maxTrim != 0 || m.steadyDuration != time.Second || m.steadyIntegral != -51 {
		t.Fatalf("telemetry=%+v", m)
	}
	summary := m.summary()
	for _, want := range []string{"locked@1s", "holdover@5s", "reacquiring@6s", "locked@7s", "trim_final_ppm=-54.000", "mean_ppm=-51.000"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("missing %q in %s", want, summary)
		}
	}
}
