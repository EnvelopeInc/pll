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
	t.Run("A positive delta should decrease the delay", func(t *testing.T) {
		period := 10 * time.Millisecond
		delay := 10 * time.Millisecond
		pll := &PLL{
			kp:     1,
			ki:     1,
			kd:     1,
			period: period,
			delay:  delay,
		}

		delta := 5 * time.Millisecond
		pll.pidController(delta)
		assert.Less(t, pll.delay, delay)
	})

	t.Run("A negative delta should increase the delay", func(t *testing.T) {
		period := 10 * time.Millisecond
		delay := 8 * time.Millisecond
		pll := &PLL{
			kp:     1,
			ki:     1,
			kd:     1,
			period: period,
			delay:  delay,
		}

		delta := -5 * time.Millisecond
		pll.pidController(delta)
		assert.Greater(t, pll.delay, delay)
	})
}
