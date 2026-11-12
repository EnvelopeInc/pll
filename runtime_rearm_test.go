package pll

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunSchedulerRearmsFromQueuedReferenceBeforeFirstBatch(t *testing.T) {
	p, _ := NewPLL(8_000, 1) // nominal 1ms slots
	batch, err := p.Advance(time.Millisecond, 2, 2)
	if err != nil || len(batch.Emissions) != 2 {
		t.Fatalf("preassign reference emissions: batch=%+v err=%v", batch, err)
	}
	zero := time.Duration(0)
	syncer, err := NewSynchronizer(p, SyncConfig{
		ReferenceTimeAtVirtualZero: &zero, Kp: 1, Ki: .01,
		MaxCorrectionPPM: 2000, SlewLimitPPMPerSecond: 1_000_000,
		MaxSampleAge: time.Second, MaxReferenceInterval: time.Second,
		MaxPhaseResidual: 10 * time.Millisecond, LockSamples: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	queue, _ := NewReferenceQueue(4)
	if err = queue.Submit(context.Background(), ReferenceSample{Emission: batch.Emissions[0], ReferenceTime: 0, ObservedAt: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if err = queue.Submit(context.Background(), ReferenceSample{Emission: batch.Emissions[1], ReferenceTime: 3 * time.Millisecond, ObservedAt: 2 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	origin := time.Now().Add(-20 * time.Millisecond)
	availability := make(chan struct{})
	go func() { availability <- struct{}{} }()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var emission Emission
	var acceptedAt time.Duration
	stats := RuntimeStats{}
	accepted, pending, runErr := RunScheduler(ctx, p, availability, func(_ context.Context, e Emission) error {
		emission = e
		acceptedAt = time.Since(origin)
		cancel()
		return nil
	}, RuntimeConfig{WakeInterval: 50 * time.Millisecond, MaxBurst: 1, MaxAvailable: 1, References: queue, Synchronizer: syncer, Origin: origin, Stats: &stats})
	queue.Close()
	if !errors.Is(runErr, context.Canceled) || accepted != 1 || pending != 0 {
		t.Fatalf("accepted=%d pending=%d err=%v", accepted, pending, runErr)
	}
	if stats.ReferenceAccepted != 2 {
		t.Fatalf("references not processed before timer rearm: %+v", stats)
	}
	// The correction moves aligned slot 50 from 50ms to approximately 50.096ms.
	// An emission of old-boundary slot 50 at 50ms must not be accepted.
	firstCorrectedBoundary, err := p.ScheduledTime(50)
	if err != nil {
		t.Fatal(err)
	}
	if emission.Slot < 50 || acceptedAt < firstCorrectedBoundary || acceptedAt > firstCorrectedBoundary+5*time.Millisecond {
		t.Fatalf("first accepted unit preceded corrected batch boundary: slot=%d accepted_at=%s boundary=%s", emission.Slot, acceptedAt, firstCorrectedBoundary)
	}
	if got, err := p.ScheduledTime(emission.Slot); err != nil || acceptedAt < got {
		t.Fatalf("accepted before corrected scheduled slot: accepted=%s scheduled=%s err=%v", acceptedAt, got, err)
	}
}
