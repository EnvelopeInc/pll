package pll

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Keep a bounded mailbox full with genuine, increasing samples followed by
// duplicates. The submitter intentionally does not share scheduler cancellation.
func sustainedReferenceTraffic(t *testing.T) (*PLL, *Synchronizer, *ReferenceQueue, func()) {
	t.Helper()
	p, _ := NewPLL(8_000_000_000, 1)
	var emissions []Emission
	for len(emissions) < 50_000 {
		batch, err := p.Advance(50*time.Microsecond, 50_000-uint64(len(emissions)), MaxBatchSize)
		if err != nil {
			t.Fatal(err)
		}
		emissions = append(emissions, batch.Emissions...)
	}
	s := testSync(t, p)
	queue, _ := NewReferenceQueue(4096)
	sample := func(i int) ReferenceSample {
		e := emissions[i]
		return ReferenceSample{Emission: e, ReferenceTime: e.Timestamp, ObservedAt: e.Timestamp + 1}
	}
	for i := 0; i < 4096; i++ {
		if err := queue.Submit(context.Background(), sample(i)); err != nil {
			t.Fatal(err)
		}
	}
	stop := make(chan struct{})
	var writers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			i := 4096
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := queue.Submit(context.Background(), sample(i)); err == nil {
					if i+1 < len(emissions) {
						i++
					}
				} else {
					runtime.Gosched()
				}
			}
		}()
	}

	return p, s, queue, func() { close(stop); writers.Wait() }
}

func TestRunSchedulerSustainedReferencesHonorCancellation(t *testing.T) {
	p, s, queue, stop := sustainedReferenceTraffic(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := RunScheduler(ctx, p, make(chan struct{}), func(context.Context, Emission) error { return nil }, RuntimeConfig{WakeInterval: time.Millisecond, MaxBurst: 1, MaxAvailable: 1, References: queue, Synchronizer: s, Origin: time.Now().Add(-time.Second)})
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	var err error
	timely := true
	select {
	case err = <-done:
	case <-time.After(200 * time.Millisecond):
		timely = false
	}
	stop()
	if !timely {
		select {
		case err = <-done:
		case <-time.After(time.Second):
			t.Fatal("scheduler did not stop after submitter joined")
		}
	}
	queue.Close()
	if !timely || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation timely=%t err=%v", timely, err)
	}
}

func TestRunSchedulerSustainedReferencesStillPace(t *testing.T) {
	p, s, queue, stop := sustainedReferenceTraffic(t)
	// A capacity-sized drain validates thousands of genuine samples under an
	// eight-writer flood. Allow race-instrumented drains and select turns time
	// to complete; this asserts eventual pacing, not a 150ms delivery bound.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	availability := make(chan struct{})
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case availability <- struct{}{}:
			}
		}
	}()
	var accepted atomic.Uint64
	done := make(chan error, 1)
	go func() {
		_, _, err := RunScheduler(ctx, p, availability, func(context.Context, Emission) error { accepted.Add(1); return nil }, RuntimeConfig{WakeInterval: 10 * time.Millisecond, MaxBurst: 1, MaxAvailable: 1, References: queue, Synchronizer: s, Origin: time.Now().Add(-time.Second)})
		done <- err
	}()
	<-ctx.Done()
	paced := accepted.Load() > 0
	stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler failed to stop")
	}
	<-producerDone
	queue.Close()
	if !paced {
		t.Fatal("continuous reference traffic prevented pacing before cancellation")
	}
}

func TestDrainReferenceBatchBoundsSustainedRefillAndPreservesNotification(t *testing.T) {
	q, _ := NewReferenceQueue(2)
	for i := 0; i < 2; i++ {
		if err := q.Submit(context.Background(), ReferenceSample{}); err != nil {
			t.Fatal(err)
		}
	}
	<-q.notify
	calls := 0
	closed, err := drainReferenceBatch(context.Background(), q, func(sample ReferenceSample) {
		calls++
		// Stop eventually even with a regressed unbounded drain, so failure joins.
		if calls < 6 {
			if err := q.Submit(context.Background(), sample); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case <-q.notify:
		default:
		} // consume edge; handler must restore level
	})
	if err != nil || closed || calls != 2 {
		t.Fatalf("calls=%d closed=%t err=%v", calls, closed, err)
	}
	select {
	case <-q.notify:
	default:
		t.Fatal("remaining references lost their notification")
	}
	q.Close()
}

func TestDrainReferenceBatchChecksCancellationDuringSustainedRefill(t *testing.T) {
	q, _ := NewReferenceQueue(2)
	for i := 0; i < 2; i++ {
		_ = q.Submit(context.Background(), ReferenceSample{})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	_, err := drainReferenceBatch(ctx, q, func(sample ReferenceSample) {
		calls++
		cancel()
		if calls < 6 {
			_ = q.Submit(context.Background(), sample)
		}
	})
	q.Close()
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestRunSchedulerContinuousValidReferencesDoNotPostponeWakeSlot(t *testing.T) {
	p, _ := NewPLL(8_000_000, 1)
	batch, err := p.Advance(256*time.Microsecond, 256, 256)
	if err != nil {
		t.Fatal(err)
	}
	s := testSync(t, p)
	q, _ := NewReferenceQueue(4)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	refsDone := make(chan struct{})
	go func() {
		defer close(refsDone)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for _, e := range batch.Emissions {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			_ = q.Submit(ctx, ReferenceSample{Emission: e, ReferenceTime: e.Timestamp, ObservedAt: e.Timestamp + 1})
		}
	}()
	availability := make(chan struct{})
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case availability <- struct{}{}:
			}
		}
	}()
	var accepted uint64
	stats := RuntimeStats{}
	_, _, runErr := RunScheduler(ctx, p, availability, func(context.Context, Emission) error { accepted++; return nil }, RuntimeConfig{WakeInterval: 20 * time.Millisecond, MaxBurst: 1, MaxAvailable: 1, References: q, Synchronizer: s, Origin: time.Now().Add(-time.Second), Stats: &stats})
	cancel()
	<-refsDone
	<-producerDone
	q.Close()
	if !errors.Is(runErr, context.DeadlineExceeded) || stats.ReferenceAccepted < 2 || accepted == 0 {
		t.Fatalf("accepted=%d references=%d err=%v", accepted, stats.ReferenceAccepted, runErr)
	}
}
