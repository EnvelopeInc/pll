package pll

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"time"
)

// RuntimeConfig bounds scheduler work and queued availability.
type RuntimeConfig struct {
	WakeInterval  time.Duration
	References    *ReferenceQueue
	Synchronizer  *Synchronizer
	Origin        time.Time
	MaxBurst      uint64
	MaxAvailable  uint64
	MeasurePhases bool
	Stats         *RuntimeStats
}

// RuntimeStats accumulates phase counts and monotonic wall-clock durations for
// one RunScheduler call; durations are not CPU time.
type RuntimeStats struct {
	TimerWakeCount         uint64
	TimerWakeLateness      time.Duration
	AvailabilityEventCount uint64
	AvailabilityHandleTime time.Duration
	AdvanceCallCount       uint64
	AdvanceTime            time.Duration
	AcceptanceCallCount    uint64
	AcceptanceTime         time.Duration
	ReferenceAccepted      uint64
	ReferenceRejected      *map[string]uint64
	ReferencePending       uint64
	MaxBurstObserved       uint64
}

var ErrInvalidRuntimeConfig = errors.New("pll: invalid runtime configuration")
var ErrReferenceQueueClosed = errors.New("pll: reference queue closed")
var ErrReferenceQueueFull = errors.New("pll: reference queue full")

// ErrReferenceQueueBusy reports that Submit could not acquire the queue mutex immediately.
var ErrReferenceQueueBusy = errors.New("pll: reference queue busy")

// ReferenceQueue is a bounded, caller-owned reference mailbox with nonblocking
// Submit. Close and scheduler-side drains may wait for its mutex. Do not copy it.
type ReferenceQueue struct {
	mu       sync.Mutex
	samples  []ReferenceSample
	head     int
	size     int
	capacity int
	closed   bool
	notify   chan struct{}
}

func NewReferenceQueue(capacity int) (*ReferenceQueue, error) {
	if capacity < 1 || capacity > 4096 {
		return nil, ErrInvalidRuntimeConfig
	}
	return &ReferenceQueue{samples: make([]ReferenceSample, capacity), capacity: capacity, notify: make(chan struct{}, 1)}, nil
}

// Submit queues one sample without waiting for the queue mutex. It returns
// ErrReferenceQueueBusy if another operation currently owns the queue. The
// caller owns queue closure. Any error means the sample was not enqueued;
// cancellation is checked before busy, and closed is checked before full once
// the mutex is acquired. A nil context returns context.Canceled.
func (q *ReferenceQueue) Submit(ctx context.Context, sample ReferenceSample) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !q.mu.TryLock() {
		return ErrReferenceQueueBusy
	}
	defer q.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if q.closed {
		return ErrReferenceQueueClosed
	}
	if q.size == q.capacity {
		return ErrReferenceQueueFull
	}
	tail := (q.head + q.size) % q.capacity
	q.samples[tail] = sample
	q.size++
	select {
	case q.notify <- struct{}{}:
	default:
	}
	return nil
}

// Close marks the queue closed; callers must stop submitters before invoking it.
func (q *ReferenceQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
}
func (q *ReferenceQueue) pop() (ReferenceSample, bool, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.size == 0 {
		return ReferenceSample{}, false, q.closed
	}
	s := q.samples[q.head]
	q.samples[q.head] = ReferenceSample{}
	q.head = (q.head + 1) % q.capacity
	q.size--
	return s, true, q.closed
}
func (q *ReferenceQueue) counts() (int, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.size, q.closed
}

// drainReferenceBatch bounds work even when submitters refill every popped slot.
// Notification is level-triggered again when work remains after this batch.
func drainReferenceBatch(ctx context.Context, q *ReferenceQueue, consume func(ReferenceSample)) (bool, error) {
	for n := 0; n < q.capacity; n++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		sample, ok, closed := q.pop()
		if !ok {
			return closed, nil
		}
		consume(sample)
	}
	pending, closed := q.counts()
	if pending > 0 || closed {
		select {
		case q.notify <- struct{}{}:
		default:
		}
	}
	return pending == 0 && closed, nil
}

func resetTimer(timer *time.Timer, deadline time.Time) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	d := time.Until(deadline)
	if d < 0 {
		d = 0
	}
	timer.Reset(d)
}

// correctedDeadline preserves wake batching while targeting a future corrected
// slot boundary rather than waking once per unit.
func correctedDeadline(p *PLL, origin, wallNow time.Time, interval time.Duration) (time.Time, uint64) {
	now := time.Since(origin)
	due, err := p.latestDue(now)
	if err != nil {
		return wallNow.Add(interval), p.nextSlot
	}
	period := periodRat(p.clock)
	spanRat := new(big.Rat).Quo(new(big.Rat).SetInt64(int64(interval)), period)
	span := uint64(1)
	if spanRat.Sign() > 0 {
		n := new(big.Int).Quo(spanRat.Num(), spanRat.Denom())
		if n.IsUint64() && n.Uint64() > 0 {
			span = n.Uint64()
		}
	}
	cursor := p.nextSlot
	base := due + 1
	if base < cursor {
		base = cursor
	}
	rem := base % span
	if rem != 0 {
		if base > ^uint64(0)-(span-rem) {
			return wallNow.Add(interval), p.nextSlot
		}
		base += span - rem
	}
	scheduled, err := p.ScheduledTime(base)
	if err != nil {
		return wallNow.Add(interval), p.nextSlot
	}
	target := origin.Add(scheduled)
	if !target.After(wallNow) {
		return wallNow.Add(interval), p.nextSlot
	}
	return target, base
}

// RunScheduler synchronously schedules caller-owned units. The caller owns
// availability, an unbuffered event stream (one event per available unit), and
// closes it after all senders stop to signal no more units. The scheduler never
// closes it and drains received pending units before normal return. Cancellation
// or errors may leave pending units. Producers should
// select on ctx when sends can block. accept must synchronously accept an
// emission; it should honor ctx if it can block. A callback error is terminal: Advance has already
// consumed that attempted slot, and the stateful PLL must not be resumed.
// Emission timestamps are virtual schedule times, not acceptance times.
// Returns accepted and still-pending unit counts. The caller owns external
// buffering and synchronization of pll. Pending includes a failed callback
// attempt but excludes availability events not received. Callbacks may read
// synchronizer status but must not reenter Advance or mutate synchronization.
func RunScheduler(ctx context.Context, p *PLL, availability <-chan struct{}, accept func(context.Context, Emission) error, config RuntimeConfig) (uint64, uint64, error) {
	if ctx == nil || p == nil || availability == nil || cap(availability) != 0 || accept == nil || config.WakeInterval <= 0 || config.MaxBurst == 0 || config.MaxBurst > MaxBatchSize || config.MaxAvailable == 0 || (config.References == nil) != (config.Synchronizer == nil) || (config.References != nil && config.Origin.IsZero()) || (config.Synchronizer != nil && config.Synchronizer.pll != p) {
		return 0, 0, ErrInvalidRuntimeConfig
	}
	measure := config.MeasurePhases && config.Stats != nil
	if config.Stats != nil {
		*config.Stats = RuntimeStats{}
	}
	stats := config.Stats
	if config.References != nil && stats != nil {
		defer func() { n, _ := config.References.counts(); stats.ReferencePending = uint64(n) }()
	}
	origin := time.Now()
	if config.Synchronizer != nil {
		origin = config.Origin
	}
	deadline := origin.Add(config.WakeInterval)
	var deadlineSlot uint64
	if config.Synchronizer != nil {
		deadline, deadlineSlot = correctedDeadline(p, origin, time.Now(), config.WakeInterval)
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	var pending, accepted, wake uint64
	closed := false
	referencesEnabled := config.References != nil
	for {
		if err := ctx.Err(); err != nil {
			return accepted, pending, err
		}
		if closed && pending == 0 {
			return accepted, 0, nil
		}
		input := availability
		if pending >= config.MaxAvailable {
			input = nil
		}
		var refNotify <-chan struct{}
		if referencesEnabled {
			refNotify = config.References.notify
		}
		select {
		case <-ctx.Done():
			return accepted, pending, ctx.Err()
		case <-refNotify:
			referenceUpdated := false
			queueClosed, drainErr := drainReferenceBatch(ctx, config.References, func(sample ReferenceSample) {
				if err := config.Synchronizer.Observe(time.Since(origin), sample); err == nil {
					referenceUpdated = true
					if stats != nil {
						stats.ReferenceAccepted++
					}
				} else if stats != nil {
					if stats.ReferenceRejected == nil {
						counts := make(map[string]uint64)
						stats.ReferenceRejected = &counts
					}
					(*stats.ReferenceRejected)[err.Error()]++
				}
			})
			if drainErr != nil {
				return accepted, pending, drainErr
			}
			if queueClosed {
				config.Synchronizer.Holdover()
				referencesEnabled = false
			}
			if stats != nil {
				n, _ := config.References.counts()
				stats.ReferencePending = uint64(n)
			}
			if referenceUpdated {
				// Keep this wake's slot fixed. Selecting a new future slot on every
				// reference can postpone pacing forever under continuous valid traffic.
				if scheduled, err := p.ScheduledTime(deadlineSlot); err == nil {
					target := origin.Add(scheduled)
					if !target.Equal(deadline) {
						deadline = target
						resetTimer(timer, deadline)
					}
				}
			}

		case _, ok := <-input:
			handled := time.Time{}
			if measure {
				handled = time.Now()
			}
			if !ok {
				closed = true
				availability = nil
			} else {
				pending++
				if measure {
					stats.AvailabilityEventCount++
				}
			}
			if measure {
				stats.AvailabilityHandleTime += time.Since(handled)
			}
		case <-timer.C:
			if measure {
				now := time.Now()
				stats.TimerWakeCount++
				stats.TimerWakeLateness += now.Sub(deadline)
			}
			elapsed := time.Since(origin)
			if pending == 0 {
				began := time.Time{}
				if measure {
					began = time.Now()
					stats.AdvanceCallCount++
				}
				if _, err := p.Advance(elapsed, 0, 1); err != nil {
					return accepted, pending, err
				}
				if measure {
					stats.AdvanceTime += time.Since(began)
				}
			} else {
				var burstCount uint64
				for burst := uint64(0); burst < config.MaxBurst && pending > 0; burst++ {
					began := time.Time{}
					if measure {
						began = time.Now()
						stats.AdvanceCallCount++
					}
					batch, err := p.Advance(elapsed, pending, 1)
					if measure {
						stats.AdvanceTime += time.Since(began)
					}
					if err != nil {
						return accepted, pending, err
					}
					if len(batch.Emissions) == 0 {
						break
					}
					acceptBegan := time.Time{}
					if measure {
						acceptBegan = time.Now()
						stats.AcceptanceCallCount++
					}
					if err := accept(ctx, batch.Emissions[0]); err != nil {
						if measure {
							stats.AcceptanceTime += time.Since(acceptBegan)
						}
						return accepted, pending, err
					}
					if measure {
						stats.AcceptanceTime += time.Since(acceptBegan)
					}
					pending--
					accepted++
					burstCount++
				}
				if measure && burstCount > stats.MaxBurstObserved {
					stats.MaxBurstObserved = burstCount
				}
			}
			if wake == ^uint64(0) {
				return accepted, pending, ErrTimeOverflow
			}
			wake++
			// Advance the absolute phase, skipping missed timer deadlines.
			maxIntervals := uint64((time.Duration(1<<63 - 1)) / config.WakeInterval)
			if wake > maxIntervals {
				return accepted, pending, ErrTimeOverflow
			}
			deadline = origin.Add(time.Duration(wake) * config.WakeInterval)
			if config.Synchronizer != nil {
				deadline, deadlineSlot = correctedDeadline(p, origin, time.Now(), config.WakeInterval)
			} else {
				now := time.Now()
				if !deadline.After(now) {
					missed := uint64(now.Sub(origin)/config.WakeInterval) + 1
					if missed > maxIntervals {
						return accepted, pending, ErrTimeOverflow
					}
					wake = missed
					deadline = origin.Add(time.Duration(wake) * config.WakeInterval)
				}
			}
			resetTimer(timer, deadline)
		}
	}
}
