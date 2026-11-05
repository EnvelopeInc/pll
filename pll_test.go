package pll

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewPLLRejectsInvalidBitrate(t *testing.T) {
	for _, mbps := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1), math.SmallestNonzeroFloat64, 1e12} {
		t.Run(fmt.Sprint(mbps), func(t *testing.T) {
			assert.Panics(t, func() { NewPLL(mbps, 0, 0, 0) })
		})
	}
}

func TestStartContextRejectsNilWithoutLocking(t *testing.T) {
	pll := NewPLL(10, 0, 0, 0)
	assert.Panics(t, func() { pll.StartContext(nil) })

	stopped := make(chan struct{})
	go func() {
		pll.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("nil context left the PLL locked")
	}
}

func TestStartStopLifecycle(t *testing.T) {
	pll := NewPLL(10, 0, 0, 0)
	pll.Start()
	pll.Start() // Starting twice must not launch a second worker.

	// Leave TriggerCh unread so the worker encounters output backpressure.
	time.Sleep(20 * time.Millisecond)
	stopped := make(chan struct{})
	go func() {
		pll.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked with an unread TriggerCh")
	}
	pll.Stop() // Stopping twice must be safe.
	if _, ok := <-pll.TriggerCh; ok {
		t.Fatal("TriggerCh should be closed after Stop")
	}
	if _, ok := <-pll.EventCh; ok {
		t.Fatal("EventCh should be closed after Stop")
	}
	pll.Start() // A stopped PLL cannot be restarted.
}

func TestStartContextCancellation(t *testing.T) {
	pll := NewPLL(10, 0, 0, 0)
	ctx, cancel := context.WithCancel(context.Background())
	pll.StartContext(ctx)
	cancel()

	select {
	case <-pll.done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after context cancellation")
	}
	pll.Stop()
	if _, ok := <-pll.TriggerCh; ok {
		t.Fatal("TriggerCh should be closed after cancellation")
	}
}

func TestStopBeforeStart(t *testing.T) {
	pll := NewPLL(10, 0, 0, 0)
	pll.Stop()
	pll.Stop()
	pll.Start()
	if _, ok := <-pll.TriggerCh; ok {
		t.Fatal("stopping before start should close TriggerCh")
	}
}

func TestPIDController(t *testing.T) {
	t.Run("positive delta decreases delay", func(t *testing.T) {
		pll := &PLL{kp: 100, period: 10 * time.Millisecond, delay: 10 * time.Millisecond}
		pll.pidController(time.Millisecond)
		assert.Equal(t, 9*time.Millisecond, pll.delay)
	})

	t.Run("negative delta increases delay", func(t *testing.T) {
		pll := &PLL{kp: 100, period: 10 * time.Millisecond, delay: 8 * time.Millisecond}
		pll.pidController(-time.Millisecond)
		assert.Equal(t, 9*time.Millisecond, pll.delay)
	})

	t.Run("integral and derivative updates are included", func(t *testing.T) {
		pll := &PLL{kp: 100, ki: 100, kd: 100, period: 10 * time.Millisecond, delay: 8 * time.Millisecond, lastDelta: int64(time.Millisecond)}
		pll.pidController(2 * time.Millisecond)
		// P=2ms, I=2ms, D=1ms.
		assert.Equal(t, 3*time.Millisecond, pll.delay)
		assert.Equal(t, int64(2*time.Millisecond), pll.integral)
		assert.Equal(t, int64(2*time.Millisecond), pll.lastDelta)
	})

	t.Run("clamps at both bounds", func(t *testing.T) {
		pll := &PLL{kp: 1000, period: 10 * time.Millisecond, delay: 10 * time.Millisecond}
		pll.pidController(time.Second)
		assert.Zero(t, pll.delay)

		pll = &PLL{kp: -1000, period: 10 * time.Millisecond, delay: 0}
		pll.pidController(time.Second)
		assert.Equal(t, 10*time.Millisecond, pll.delay)
	})

	t.Run("extreme durations and gains remain bounded", func(t *testing.T) {
		pll := &PLL{kp: math.MaxInt, ki: math.MinInt, kd: math.MaxInt, period: time.Duration(math.MaxInt64), delay: time.Duration(math.MaxInt64), lastDelta: math.MinInt64}
		pll.pidController(time.Duration(math.MaxInt64))
		assert.GreaterOrEqual(t, pll.delay, time.Duration(0))
		assert.LessOrEqual(t, pll.delay, pll.period)
		assert.Equal(t, int64(math.MaxInt64), pll.lastDelta)

		pll.pidController(time.Duration(math.MinInt64))
		assert.GreaterOrEqual(t, pll.delay, time.Duration(0))
		assert.LessOrEqual(t, pll.delay, pll.period)
	})

	t.Run("integral saturates instead of wrapping", func(t *testing.T) {
		pll := &PLL{ki: math.MaxInt, period: time.Duration(math.MaxInt64), delay: time.Duration(math.MaxInt64)}
		pll.pidController(time.Duration(math.MaxInt64))
		assert.Equal(t, int64(math.MaxInt64), pll.integral)

		pll = &PLL{ki: math.MinInt, period: time.Duration(math.MaxInt64), delay: time.Duration(math.MaxInt64)}
		pll.pidController(time.Duration(math.MaxInt64))
		assert.Equal(t, int64(math.MinInt64), pll.integral)
	})
}

func TestTriggerUsesOnePeriodDeadline(t *testing.T) {
	pll := NewPLL(0.01504, 0, 0, 0) // 100ms per 188-byte packet.
	pll.Start()
	defer pll.Stop()

	started := time.Now()
	select {
	case _, ok := <-pll.TriggerCh:
		if !ok {
			t.Fatal("TriggerCh closed before first trigger")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not deliver first trigger")
	}
	elapsed := time.Since(started)
	if elapsed < 75*time.Millisecond || elapsed >= 175*time.Millisecond {
		t.Fatalf("first trigger should follow one 100ms deadline, got %v", elapsed)
	}
}

func TestEventChLateCorrectionChangesSubsequentTriggerTiming(t *testing.T) {
	const period = 300 * time.Millisecond
	pll := NewPLL(1504.0/(period.Seconds()*1e6), 100, 0, 0)
	pll.Start()
	defer pll.Stop()

	receive := func() time.Time {
		t.Helper()
		select {
		case _, ok := <-pll.TriggerCh:
			if !ok {
				t.Fatal("TriggerCh closed before expected trigger")
			}
			return time.Now()
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for trigger")
			return time.Time{}
		}
	}

	_ = receive() // first trigger
	// Leave trigger 2 unread beyond its next reference tick. The delayed receive
	// makes this event late relative to the worker's reference, producing a
	// positive PID correction that shortens a later interval.
	time.Sleep(2*period + 60*time.Millisecond)
	abortEvent := make(chan struct{})
	eventSent := make(chan struct{})
	senderDone := make(chan struct{})
	go func() {
		defer close(senderDone)
		select {
		case pll.EventCh <- true: // pending while the worker is blocked delivering trigger 2
			close(eventSent)
		case <-abortEvent:
		}
	}()
	defer func() {
		close(abortEvent)
		<-senderDone
	}()
	_ = receive() // trigger 2; accepting it lets the pending event be processed
	select {
	case <-eventSent:
	case <-time.After(time.Second):
		t.Fatal("worker did not receive the correction event")
	}
	_ = receive() // trigger 3; the previously scheduled interval is unchanged
	third := time.Now()
	_ = receive() // trigger 4 uses the corrected delay
	fourth := time.Now()
	if interval := fourth.Sub(third); interval >= period-20*time.Millisecond {
		t.Fatalf("late EventCh correction should shorten a subsequent trigger interval; got %v, period %v", interval, period)
	}
}

func TestEventBeforeFirstDeadlineUsesRunStartReference(t *testing.T) {
	pll := NewPLL(0.0015, 100, 0, 0) // approximately one-second period, preventing a deadline race
	ctx, cancel := context.WithCancel(context.Background())
	pll.StartContext(ctx)
	select {
	case pll.EventCh <- true:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not receive startup event")
	}
	cancel()
	select {
	case <-pll.done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	if pll.delay != pll.period {
		t.Fatalf("startup event should be referenced to run start and leave delay clamped at period: got %v, period %v", pll.delay, pll.period)
	}
}
