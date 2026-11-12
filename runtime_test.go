package pll

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunSchedulerAcceptanceCloseAndVirtualTimes(t *testing.T) {
	p, _ := NewPLL(8_000, 1)
	availability := make(chan struct{})
	go func() {
		for i := 0; i < 3; i++ {
			availability <- struct{}{}
		}
		close(availability)
	}()
	var got []Emission
	a, pending, err := RunScheduler(context.Background(), p, availability, func(_ context.Context, e Emission) error { got = append(got, e); return nil }, RuntimeConfig{WakeInterval: time.Millisecond, MaxBurst: 2, MaxAvailable: 3})
	if err != nil || a != 3 || pending != 0 || len(got) != 3 {
		t.Fatalf("a=%d pending=%d got=%d err=%v", a, pending, len(got), err)
	}
	for i, e := range got {
		want, _ := p.VirtualTime(e.Slot)
		if e.Timestamp != want || (i > 0 && e.Timestamp <= got[i-1].Timestamp) {
			t.Fatalf("timestamps: %+v", got)
		}
	}
}

func TestRunSchedulerRejectsBufferedAvailability(t *testing.T) {
	p, _ := NewPLL(8_000, 1)
	_, _, err := RunScheduler(context.Background(), p, make(chan struct{}, 1), func(context.Context, Emission) error { return nil }, RuntimeConfig{WakeInterval: time.Millisecond, MaxBurst: 1, MaxAvailable: 1})
	if !errors.Is(err, ErrInvalidRuntimeConfig) {
		t.Fatalf("got %v", err)
	}
}

func TestRunSchedulerCancellationAndCallbackError(t *testing.T) {
	p, _ := NewPLL(8_000, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a, pending, err := RunScheduler(ctx, p, make(chan struct{}), func(context.Context, Emission) error { return nil }, RuntimeConfig{WakeInterval: time.Millisecond, MaxBurst: 1, MaxAvailable: 1})
	if !errors.Is(err, context.Canceled) || a != 0 || pending != 0 {
		t.Fatalf("%d %d %v", a, pending, err)
	}
	p, _ = NewPLL(8_000, 1)
	ch := make(chan struct{})
	go func() { ch <- struct{}{} }()
	want := errors.New("reject")
	a, pending, err = RunScheduler(context.Background(), p, ch, func(context.Context, Emission) error { return want }, RuntimeConfig{WakeInterval: time.Millisecond, MaxBurst: 1, MaxAvailable: 1})
	if !errors.Is(err, want) || a != 0 || pending != 1 {
		t.Fatalf("%d %d %v", a, pending, err)
	}
}

func TestRunSchedulerAggregateBurstAndDelayedWake(t *testing.T) {
	p, _ := NewPLL(1_000_000_000, 1)
	ch := make(chan struct{})
	go func() {
		for i := 0; i < 8; i++ {
			ch <- struct{}{}
		}
		close(ch)
	}()
	var calls int
	started := time.Now()
	a, pending, err := RunScheduler(context.Background(), p, ch, func(context.Context, Emission) error { calls++; return nil }, RuntimeConfig{WakeInterval: 30 * time.Millisecond, MaxBurst: 2, MaxAvailable: 8})
	if err != nil || calls != 8 || a != 8 || pending != 0 {
		t.Fatalf("calls=%d accepted=%d pending=%d err=%v", calls, a, pending, err)
	}
	if time.Since(started) < 30*time.Millisecond {
		t.Fatal("closed input was not drained on due timer wakes")
	}
}

func TestRunSchedulerSlowCallbackHonorsCancellationAndBackpressure(t *testing.T) {
	p, _ := NewPLL(8_000, 1)
	ch := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	secondSent := make(chan struct{})
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		select {
		case ch <- struct{}{}:
		case <-ctx.Done():
			return
		}
		select {
		case ch <- struct{}{}:
			close(secondSent)
		case <-ctx.Done():
		}
	}()
	a, pending, err := RunScheduler(ctx, p, ch, func(ctx context.Context, _ Emission) error { <-ctx.Done(); return ctx.Err() }, RuntimeConfig{WakeInterval: time.Millisecond, MaxBurst: 1, MaxAvailable: 1})
	if !errors.Is(err, context.DeadlineExceeded) || a != 0 || pending != 1 {
		t.Fatalf("accepted=%d pending=%d err=%v", a, pending, err)
	}
	select {
	case <-secondSent:
		t.Fatal("producer send was not backpressured")
	default:
	}
	<-producerDone
}

func TestRunSchedulerSlowConsumerEventuallyAcceptsDueUnits(t *testing.T) {
	p, _ := NewPLL(8_000, 1)
	availability := make(chan struct{})
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for i := 0; i < 4; i++ {
			availability <- struct{}{}
		}
		close(availability)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var emissions []Emission
	var acceptedAt []time.Time
	accepted, pending, err := RunScheduler(ctx, p, availability, func(ctx context.Context, e Emission) error {
		timer := time.NewTimer(2 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
		emissions = append(emissions, e)
		acceptedAt = append(acceptedAt, time.Now())
		return nil
	}, RuntimeConfig{WakeInterval: time.Millisecond, MaxBurst: 2, MaxAvailable: 4})
	<-producerDone
	if err != nil || accepted != 4 || pending != 0 || len(emissions) != 4 {
		t.Fatalf("accepted=%d pending=%d emissions=%d err=%v", accepted, pending, len(emissions), err)
	}
	for i, e := range emissions {
		if e.UnitIndex != uint64(i) {
			t.Fatalf("unit index %d: %+v", i, e)
		}
		want, err := p.VirtualTime(e.Slot)
		if err != nil || e.Timestamp != want {
			t.Fatalf("virtual timestamp at emission %d: %+v err=%v want=%s", i, e, err, want)
		}
		if i > 0 && acceptedAt[i].Sub(acceptedAt[i-1]) < time.Millisecond {
			t.Fatalf("slow consumer accepted too quickly: %v", acceptedAt[i].Sub(acceptedAt[i-1]))
		}
	}
}

func TestRunSchedulerAvailabilityCapBackpressure(t *testing.T) {
	p, _ := NewPLL(8_000, 1)
	ch := make(chan struct{})
	sent := make(chan struct{})
	go func() { ch <- struct{}{}; ch <- struct{}{}; close(sent); close(ch) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, pending, err := RunScheduler(ctx, p, ch, func(context.Context, Emission) error { return nil }, RuntimeConfig{WakeInterval: time.Millisecond, MaxBurst: 1, MaxAvailable: 1})
	if err != nil || a != 2 || pending != 0 {
		t.Fatalf("%d %d %v", a, pending, err)
	}
	select {
	case <-sent:
	default:
		t.Fatal("producer did not complete")
	}
}

func TestReferenceQueueBoundsCloseAndCancellation(t *testing.T) {
	q, err := NewReferenceQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	sample := ReferenceSample{}
	if err := q.Submit(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	if err := q.Submit(context.Background(), sample); !errors.Is(err, ErrReferenceQueueFull) {
		t.Fatalf("full: %v", err)
	}
	q.Close()
	if err := q.Submit(context.Background(), sample); !errors.Is(err, ErrReferenceQueueClosed) {
		t.Fatalf("closed: %v", err)
	}
	q2, _ := NewReferenceQueue(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q2.Submit(ctx, sample); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
}

func TestReferenceQueueRingWrapsInFIFOOrder(t *testing.T) {
	q, err := NewReferenceQueue(2)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []ReferenceSample{{Epoch: 1}, {Epoch: 2}} {
		if err := q.Submit(context.Background(), sample); err != nil {
			t.Fatal(err)
		}
	}
	first, ok, _ := q.pop()
	if !ok || first.Epoch != 1 {
		t.Fatalf("first=%+v ok=%t", first, ok)
	}
	if err := q.Submit(context.Background(), ReferenceSample{Epoch: 3}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []uint64{2, 3} {
		got, ok, _ := q.pop()
		if !ok || got.Epoch != want {
			t.Fatalf("got=%+v ok=%t want epoch %d", got, ok, want)
		}
	}
}

func TestReferenceQueueSubmitReturnsWhenMutexContended(t *testing.T) {
	q, err := NewReferenceQueue(2)
	if err != nil {
		t.Fatal(err)
	}
	q.mu.Lock()
	started := time.Now()
	err = q.Submit(context.Background(), ReferenceSample{})
	elapsed := time.Since(started)
	q.mu.Unlock()
	if !errors.Is(err, ErrReferenceQueueBusy) {
		t.Fatalf("got %v, want ErrReferenceQueueBusy", err)
	}
	if elapsed > 20*time.Millisecond {
		t.Fatalf("Submit waited under mutex contention: %s", elapsed)
	}
}

func TestRunSchedulerReferenceQueueCallbackAndCloseFreeRun(t *testing.T) {
	p, _ := NewPLL(8_000, 1)
	zero := time.Duration(0)
	syncer, err := NewSynchronizer(p, SyncConfig{ReferenceTimeAtVirtualZero: &zero, Kp: 1, Ki: .01, MaxCorrectionPPM: 1000, SlewLimitPPMPerSecond: 1000, MaxSampleAge: time.Second, MaxReferenceInterval: time.Second, MaxPhaseResidual: time.Second, LockSamples: 1})
	if err != nil {
		t.Fatal(err)
	}
	q, _ := NewReferenceQueue(4)
	availability := make(chan struct{})
	go func() {
		for i := 0; i < 3; i++ {
			availability <- struct{}{}
		}
		close(availability)
	}()
	origin := time.Now()
	submitted := false
	accepted, pending, err := RunScheduler(context.Background(), p, availability, func(_ context.Context, e Emission) error {
		if !submitted {
			submitted = true
			if err := q.Submit(context.Background(), ReferenceSample{Emission: e, ReferenceTime: e.Timestamp, ObservedAt: time.Since(origin)}); err != nil {
				return err
			}
		}
		return nil
	}, RuntimeConfig{WakeInterval: time.Millisecond, MaxBurst: 2, MaxAvailable: 3, References: q, Synchronizer: syncer, Origin: origin})
	q.Close()
	if err != nil || accepted != 3 || pending != 0 || !submitted {
		t.Fatalf("accepted=%d pending=%d submitted=%t err=%v", accepted, pending, submitted, err)
	}
	if syncer.State() == SyncAcquiring {
		t.Fatal("reference was not processed")
	}
}

func TestRunSchedulerQueueCloseWhileRunningEntersHoldoverAndKeepsPacing(t *testing.T) {
	p, err := NewPLL(8_000, 1)
	if err != nil {
		t.Fatal(err)
	}
	zero := time.Duration(0)
	syncer, err := NewSynchronizer(p, SyncConfig{
		ReferenceTimeAtVirtualZero: &zero, Kp: 1, Ki: .01,
		MaxCorrectionPPM: 1000, SlewLimitPPMPerSecond: 1000,
		MaxSampleAge: time.Second, MaxReferenceInterval: time.Second,
		MaxPhaseResidual: time.Second, LockTolerance: time.Millisecond, LockSamples: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	queue, err := NewReferenceQueue(4)
	if err != nil {
		t.Fatal(err)
	}
	availability := make(chan struct{})
	queueClosed := make(chan struct{})
	observedState := make(chan SyncState, 1)
	producerDone := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		defer close(producerDone)
		defer close(availability)
		select {
		case availability <- struct{}{}:
		case <-ctx.Done():
			return
		}
		select {
		case <-queueClosed:
		case <-ctx.Done():
			return
		}
		for {
			select {
			case availability <- struct{}{}:
			case <-ctx.Done():
				return
			}
			select {
			case state := <-observedState:
				if state == SyncHoldover {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	origin := time.Now()
	var accepted int
	count, pending, runErr := RunScheduler(ctx, p, availability, func(_ context.Context, e Emission) error {
		accepted++
		if accepted == 1 {
			if err := queue.Submit(context.Background(), ReferenceSample{Emission: e, ReferenceTime: e.Timestamp, ObservedAt: time.Since(origin)}); err != nil {
				return err
			}
			// No further submission can occur after this callback, so queue
			// closure is owned here and observed before the producer resumes.
			queue.Close()
			close(queueClosed)
			return nil
		}
		observedState <- syncer.State()
		return nil
	}, RuntimeConfig{
		WakeInterval: time.Millisecond, MaxBurst: 1, MaxAvailable: 1,
		References: queue, Synchronizer: syncer, Origin: origin,
	})
	cancel()
	<-producerDone
	queue.Close()
	if runErr != nil || count < 2 || pending != 0 || accepted != int(count) {
		t.Fatalf("accepted=%d callback_count=%d pending=%d err=%v", count, accepted, pending, runErr)
	}
	if syncer.State() != SyncHoldover {
		t.Fatalf("state after queue closure=%v, want holdover", syncer.State())
	}
}

func TestRunSchedulerPhaseStats(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		p, _ := NewPLL(8_000, 1)
		ch := make(chan struct{})
		go func() { ch <- struct{}{}; close(ch) }()
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		stats := RuntimeStats{}
		a, pending, err := RunScheduler(ctx, p, ch, func(context.Context, Emission) error { return nil }, RuntimeConfig{WakeInterval: time.Millisecond, MaxBurst: 1, MaxAvailable: 1, MeasurePhases: enabled, Stats: &stats})
		cancel()
		if err != nil || a != 1 || pending != 0 {
			t.Fatalf("enabled=%t accepted=%d pending=%d err=%v", enabled, a, pending, err)
		}
		if enabled {
			if stats.AvailabilityEventCount != 1 || stats.TimerWakeCount == 0 || stats.AdvanceCallCount == 0 || stats.AcceptanceCallCount != 1 || stats.AdvanceTime < 0 || stats.AcceptanceTime < 0 {
				t.Fatalf("phase metrics not populated: %+v", stats)
			}
		} else if stats != (RuntimeStats{}) {
			t.Fatalf("disabled metrics populated: %+v", stats)
		}
	}
}
