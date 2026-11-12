package pll

import (
	"context"
	"errors"
	"testing"
	"time"
)

func runtimeTestSync(t *testing.T, p *PLL) *Synchronizer {
	t.Helper()
	zero := time.Duration(0)
	s, err := NewSynchronizer(p, SyncConfig{ReferenceTimeAtVirtualZero: &zero, Kp: 1, Ki: .1, MaxCorrectionPPM: 1000, SlewLimitPPMPerSecond: 1000, MaxSampleAge: time.Second, MaxReferenceInterval: time.Second, MaxPhaseResidual: time.Second, LockSamples: 1})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRunSchedulerClosedReferencesFreeRun(t *testing.T) {
	p, _ := NewPLL(8_000, 1)
	s := runtimeTestSync(t, p)
	q, _ := NewReferenceQueue(2)
	q.Close()
	ch := make(chan struct{})
	go func() { ch <- struct{}{}; close(ch) }()
	origin := time.Now()
	accepted, pending, err := RunScheduler(context.Background(), p, ch, func(context.Context, Emission) error { return nil }, RuntimeConfig{WakeInterval: time.Millisecond, MaxBurst: 1, MaxAvailable: 1, References: q, Synchronizer: s, Origin: origin})
	if err != nil || accepted != 1 || pending != 0 {
		t.Fatalf("%d %d %v", accepted, pending, err)
	}
	if s.State() != SyncAcquiring {
		t.Fatalf("unexpected state: %v", s.State())
	}
}

func TestRunSchedulerInvalidReferenceDoesNotStopPacing(t *testing.T) {
	p, _ := NewPLL(8_000, 1)
	s := runtimeTestSync(t, p)
	q, _ := NewReferenceQueue(4)
	ch := make(chan struct{})
	go func() { ch <- struct{}{} }()
	origin := time.Now()
	stats := RuntimeStats{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, err := RunScheduler(ctx, p, ch, func(_ context.Context, e Emission) error {
		return q.Submit(context.Background(), ReferenceSample{Emission: Emission{}, ReferenceTime: e.Timestamp, ObservedAt: time.Since(origin)})
	}, RuntimeConfig{WakeInterval: time.Millisecond, MaxBurst: 1, MaxAvailable: 1, References: q, Synchronizer: s, Origin: origin, Stats: &stats})
	q.Close()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("scheduler stopped before cancellation: %v", err)
	}
	if stats.ReferenceRejected == nil || (*stats.ReferenceRejected)[ErrInvalidEmission.Error()] != 1 {
		t.Fatalf("rejections: %+v", stats.ReferenceRejected)
	}
}

func TestRunSchedulerCancellationDuringReferenceWait(t *testing.T) {
	p, _ := NewPLL(8_000, 1)
	s := runtimeTestSync(t, p)
	q, _ := NewReferenceQueue(1)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	origin := time.Now()
	_, _, err := RunScheduler(ctx, p, make(chan struct{}), func(context.Context, Emission) error { return nil }, RuntimeConfig{WakeInterval: time.Second, MaxBurst: 1, MaxAvailable: 1, References: q, Synchronizer: s, Origin: origin})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}
