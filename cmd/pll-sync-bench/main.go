// Command pll-sync-bench exercises the real scheduler with a simulated external
// reference. This is not evidence of a physical clock, transport, or hardware.
package main

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/EnvelopeInc/pll"
)

const (
	rate       = int64(19_392_000)
	packetSize = int64(188)
)

type testCase struct {
	name string
	ppm  int64
	gap  bool
}

// referenceTime indexes an independent rational oscillator model. Positive ppm
// means a faster clock, so its period is nominal period divided by frequency ratio.
func referenceTime(slot uint64, ppm int64) time.Duration {
	numerator := new(big.Int).SetUint64(slot)
	numerator.Mul(numerator, big.NewInt(packetSize*8*1_000_000_000))
	numerator.Mul(numerator, big.NewInt(1_000_000))
	denominator := big.NewInt(rate)
	denominator.Mul(denominator, big.NewInt(1_000_000+ppm))
	numerator.Add(numerator, new(big.Int).Quo(new(big.Int).Set(denominator), big.NewInt(2)))
	numerator.Quo(numerator, denominator)
	return time.Duration(numerator.Int64())
}

func run(tc testCase, load bool) error {
	p, err := pll.NewPLL(rate, packetSize)
	if err != nil {
		return err
	}
	zero := time.Duration(0)
	maxAge := 120 * time.Millisecond
	maxInterval := time.Second
	if tc.gap {
		maxInterval = 250 * time.Millisecond
	}
	syncer, err := pll.NewSynchronizer(p, pll.SyncConfig{
		ReferenceTimeAtVirtualZero: &zero, Kp: 5, Ki: 6.25,
		MaxCorrectionPPM: 1000, SlewLimitPPMPerSecond: 2000,
		MaxSampleAge: maxAge, MaxReferenceInterval: maxInterval,
		MaxPhaseResidual: time.Second, LockTolerance: 20 * time.Microsecond, LockSamples: 4,
	})
	if err != nil {
		return fmt.Errorf("create synchronizer: %w", err)
	}
	queue, err := pll.NewReferenceQueue(1024)
	if err != nil {
		return err
	}
	availability := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 2100*time.Millisecond)
	defer cancel()
	var loadWG sync.WaitGroup
	var loadWork atomic.Uint64
	if load {
		for i := 0; i < 2; i++ {
			loadWG.Add(1)
			go func() {
				defer loadWG.Done()
				for {
					select {
					case <-ctx.Done():
						return
					default:
						var work uint64
						for n := uint64(0); n < 10000; n++ {
							work = work*1664525 + n + 1013904223
						}
						loadWork.Add(work)
					}
				}
			}()
		}
	}
	start := time.Now()
	var mu sync.Mutex
	type sample struct{ at, phase time.Duration }
	type scheduledSlot struct {
		slot      uint64
		timestamp time.Duration
	}
	var phases []sample
	var callbacks []time.Duration
	var scheduledSlots []scheduledSlot
	var acceptedRefs, rejectedSubmit, unitsWithoutReference, recoveryRefsSubmitted uint64
	var nextReferenceEligible time.Duration
	var holdoverAt, reacquiringAt, relockedAt, lockAt time.Duration
	var holdoverSamples, reacquiringSamples uint64
	trimMin, trimMax := 0.0, 0.0
	if tc.name != "free-run" {
		trimMin, trimMax = math.Inf(1), math.Inf(-1)
	}
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		ticker := time.NewTicker(50 * time.Microsecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			select {
			case availability <- struct{}{}:
			case <-ctx.Done():
				return
			}
		}
	}()
	stats := pll.RuntimeStats{}
	config := pll.RuntimeConfig{WakeInterval: 10 * time.Millisecond, MaxBurst: 1024, MaxAvailable: 4096, Origin: start, Stats: &stats, MeasurePhases: true}
	if tc.name != "free-run" {
		config.References, config.Synchronizer = queue, syncer
	}
	accepted, pending, runErr := pll.RunScheduler(ctx, p, availability, func(_ context.Context, e pll.Emission) error {
		observed := time.Since(start) // actual callback acceptance time, separate from virtual timestamp
		mu.Lock()
		defer mu.Unlock()
		callbacks = append(callbacks, observed)
		scheduledSlots = append(scheduledSlots, scheduledSlot{slot: e.Slot, timestamp: e.Timestamp})
		external := referenceTime(e.Slot, tc.ppm)
		phases = append(phases, sample{at: observed, phase: external - e.Timestamp})
		if tc.name == "free-run" {
			return nil
		}
		switch syncer.Status(observed) {
		case pll.SyncHoldover:
			holdoverSamples++
			if holdoverAt == 0 {
				holdoverAt = observed
			}
		case pll.SyncReacquiring:
			reacquiringSamples++
			if holdoverAt != 0 && reacquiringAt == 0 {
				reacquiringAt = observed
			}
		case pll.SyncLocked:
			if lockAt == 0 {
				lockAt = observed
			}
			if reacquiringAt != 0 && relockedAt == 0 {
				relockedAt = observed
			}
		}
		trim := syncer.Trim() * 1e6
		if trim < trimMin {
			trimMin = trim
		}
		if trim > trimMax {
			trimMax = trim
		}
		if tc.gap && observed >= 700*time.Millisecond && observed < 1100*time.Millisecond {
			unitsWithoutReference++ // accepted units deliberately receive no external reference
			return nil
		}
		if observed < nextReferenceEligible {
			return nil
		}
		// Model a 100Hz source from local monotonic observation time. Emission
		// timestamps are virtual schedule times and never pace reference input.
		nextReferenceEligible = observed + 10*time.Millisecond
		if submitErr := queue.Submit(context.Background(), pll.ReferenceSample{Emission: e, ReferenceTime: external, ObservedAt: observed}); submitErr != nil {
			rejectedSubmit++
		} else {
			acceptedRefs++
			if tc.gap && observed >= 1100*time.Millisecond {
				recoveryRefsSubmitted++
			}
		}
		return nil
	}, config)
	cancel()
	<-producerDone // all callback and availability senders have stopped before caller-owned close
	loadWG.Wait()
	queue.Close()
	if runErr != nil && runErr != context.DeadlineExceeded && runErr != context.Canceled {
		return runErr
	}
	mu.Lock()
	defer mu.Unlock()
	rollingPhase := func(window time.Duration) time.Duration {
		cut := time.Since(start) - window
		var max time.Duration
		for _, s := range phases {
			if s.at >= cut {
				v := s.phase
				if v < 0 {
					v = -v
				}
				if v > max {
					max = v
				}
			}
		}
		return max
	}
	scheduledRate := math.NaN()
	if len(scheduledSlots) >= 2 {
		first, last := scheduledSlots[0], scheduledSlots[len(scheduledSlots)-1]
		if last.timestamp > first.timestamp {
			scheduledRate = float64(last.slot-first.slot) / (float64(last.timestamp-first.timestamp) / float64(time.Second))
		}
	}
	targetRate := float64(rate) / float64(packetSize*8) * (1 + float64(tc.ppm)/1e6)
	type rateSummary struct {
		min, mean, p95, max float64
		samples             int
	}
	rollingRateError := func(window, duration time.Duration) rateSummary {
		values := make([]float64, 0, len(callbacks))
		left, right := 0, 0
		const sampleInterval = 10 * time.Millisecond
		// Uniform time endpoints avoid weighting dense callback bursts more heavily.
		for end := window; end <= duration; end += sampleInterval {
			for right < len(callbacks) && callbacks[right] <= end {
				right++
			}
			cut := end - window
			for left < right && callbacks[left] <= cut {
				left++
			}
			actual := float64(right-left) / window.Seconds()
			values = append(values, (actual/targetRate-1)*1e6)
		}
		if len(values) == 0 {
			return rateSummary{min: math.NaN(), mean: math.NaN(), p95: math.NaN(), max: math.NaN()}
		}
		sort.Float64s(values)
		sum := 0.0
		for _, v := range values {
			sum += v
		}
		p95 := int(math.Ceil(.95*float64(len(values)))) - 1
		return rateSummary{min: values[0], mean: sum / float64(len(values)), p95: values[p95], max: values[len(values)-1], samples: len(values)}
	}
	elapsed := time.Since(start)
	ref100, ref500 := rollingRateError(100*time.Millisecond, elapsed), rollingRateError(500*time.Millisecond, elapsed)
	var holdoverDuration, reacquisitionDuration time.Duration
	if reacquiringAt > holdoverAt && holdoverAt != 0 {
		holdoverDuration = reacquiringAt - holdoverAt
	}
	if relockedAt > reacquiringAt && reacquiringAt != 0 {
		reacquisitionDuration = relockedAt - reacquiringAt
	}
	fmt.Printf("case=%s reference=simulated_external load=%t duration=%s accepted=%d accepted_rate=%.0f/s scheduled_slot_rate=%.2f/s rate_error_100ms_ppm[min/mean/p95/max]=[%.1f/%.1f/%.1f/%.1f](n=%d) rate_error_500ms_ppm[min/mean/p95/max]=[%.1f/%.1f/%.1f/%.1f](n=%d) target_rate=%.1f/s pending_units=%d ref_pending=%d ref_submitted=%d ref_observations_accepted=%d ref_submit_rejected=%d rejected_observations=%v units_without_reference=%d phase_abs_max_100ms=%s phase_abs_max_500ms=%s trim_final_ppm=%.3f trim_range_ppm=[%.3f,%.3f] lock_time=%s holdover_samples=%d holdover_to_reacquiring=%s reacquiring_samples=%d reacquiring_to_locked=%s relock_at=%s recovery_refs_submitted=%d final_state=%d timer_wakes=%d max_burst=%d\n",
		tc.name, load, elapsed.Round(time.Millisecond), accepted, float64(accepted)/elapsed.Seconds(), scheduledRate, ref100.min, ref100.mean, ref100.p95, ref100.max, ref100.samples, ref500.min, ref500.mean, ref500.p95, ref500.max, ref500.samples, targetRate, pending, stats.ReferencePending, acceptedRefs, stats.ReferenceAccepted, rejectedSubmit, deref(stats.ReferenceRejected), unitsWithoutReference, rollingPhase(100*time.Millisecond), rollingPhase(500*time.Millisecond), syncer.Trim()*1e6, trimMin, trimMax, formatDuration(lockAt), holdoverSamples, formatDuration(holdoverDuration), reacquiringSamples, formatDuration(reacquisitionDuration), formatDuration(relockedAt), recoveryRefsSubmitted, syncer.State(), stats.TimerWakeCount, stats.MaxBurstObserved)
	return nil
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "not_observed"
	}
	return d.String()
}

func deref(p *map[string]uint64) map[string]uint64 {
	if p == nil {
		return map[string]uint64{}
	}
	return *p
}

func main() {
	cases := []testCase{{"free-run", 0, false}, {"nominal-ref", 0, false}, {"plus-200ppm", 200, false}, {"minus-200ppm", -200, false}, {"interruption-recovery", 100, true}}
	for _, tc := range cases {
		for _, load := range []bool{false, true} {
			if err := run(tc, load); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
	}
}
