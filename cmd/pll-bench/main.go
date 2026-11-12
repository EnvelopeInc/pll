package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"math/big"
	"runtime"
	"runtime/metrics"
	"sort"
	"sync"
	"time"

	"github.com/EnvelopeInc/pll"
)

const unitBits = 188 * 8

func main() {
	duration := flag.Duration("duration", 2*time.Second, "duration per case (maximum 10s)")
	workers := flag.Int("cpu-workers", runtime.NumCPU(), "CPU load workers in loaded mode (1 through runtime.NumCPU)")
	rateFilter := flag.Int64("rate", 0, "run only this rate in bps (0 runs all rates)")
	measurePhases := flag.Bool("measure-phases", false, "collect scheduler phase metrics")
	flag.Parse()
	if *duration < 100*time.Millisecond || *duration > 10*time.Second || *duration%(100*time.Millisecond) != 0 || *workers < 1 || *workers > runtime.NumCPU() {
		panic("duration must be a 100ms multiple in [100ms,10s], cpu-workers must be within [1,runtime.NumCPU()]")
	}
	rates := []int64{19_392_000, 40_000_000, 100_000_000}
	if *rateFilter != 0 {
		rates = []int64{*rateFilter}
	}
	for _, rate := range rates {
		for _, loaded := range []bool{false, true} {
			run(rate, *duration, *workers, loaded, *measurePhases)
		}
	}
}

func run(rate int64, duration time.Duration, workers int, loaded, measurePhases bool) {
	mode := "idle"
	if loaded {
		mode = "loaded"
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := make(chan struct{})
	var wg sync.WaitGroup
	if loaded {
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				x := uint64(1)
				for {
					select {
					case <-stop:
						return
					default:
						x = x*2862933555777941757 + 3037000493
					}
				}
			}()
		}
	}
	p, err := pll.NewPLL(rate, 188)
	if err != nil {
		panic(err)
	}
	ch := make(chan struct{})
	done := make(chan struct{})
	producerStart := make(chan time.Time, 1)
	stats := producerStats{}
	go produce(ctx, ch, rate, duration, producerStart, &stats, done)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	cpu0, cpuOK := cpuSeconds()
	start := time.Now()
	endTimer := time.AfterFunc(duration, cancel)
	producerStart <- start
	var actual []time.Time
	var virtual []time.Duration
	var lateness []time.Duration
	var slots []uint64
	var phases pll.RuntimeStats
	accepted, pending, err := pll.RunScheduler(ctx, p, ch, func(_ context.Context, e pll.Emission) error {
		acceptedAt := time.Now()
		actual = append(actual, acceptedAt)
		virtual = append(virtual, e.Timestamp)
		lateness = append(lateness, acceptedAt.Sub(start)-e.Timestamp)
		slots = append(slots, e.Slot)
		return nil
	}, pll.RuntimeConfig{WakeInterval: 10 * time.Millisecond, MaxBurst: 1024, MaxAvailable: 4096, MeasurePhases: measurePhases, Stats: &phases})
	elapsed := time.Since(start)
	// Sample runtime/process counters at the measurement boundary, before
	// producer and CPU-load teardown adds unrelated work.
	cpu1, cpuOK1 := cpuSeconds()
	runtime.ReadMemStats(&after)
	endTimer.Stop()
	cancel()
	<-done
	close(stop)
	wg.Wait()
	if err != nil && err != context.DeadlineExceeded && err != context.Canceled {
		fmt.Printf("rate=%d mode=%s scheduler_error=%v\n", rate, mode, err)
		return
	}
	cpu := "unsupported"
	if cpuOK && cpuOK1 {
		cpu = fmt.Sprintf("%.1f%%", 100*(cpu1-cpu0)/elapsed.Seconds())
	}
	bps := float64(accepted) * unitBits / elapsed.Seconds()
	def := deficitSummary(actual, start, elapsed, rate)
	skipCount := skippedSlots(slots)
	phaseText := "disabled"
	if measurePhases {
		phaseText = fmt.Sprintf("timer_wakes=%d,timer_lateness_total=%s,availability_events=%d,availability_handle_total=%s,advance_calls=%d,advance_total=%s,accept_calls=%d,accept_total=%s", phases.TimerWakeCount, phases.TimerWakeLateness, phases.AvailabilityEventCount, phases.AvailabilityHandleTime, phases.AdvanceCallCount, phases.AdvanceTime, phases.AcceptanceCallCount, phases.AcceptanceTime)
	}
	fmt.Printf("rate=%d mode=%s phases={%s} accepted=%d units_per_sec=%.0f achieved_bps=%.0f error_pct=%.2f elapsed=%s pending_returned=%d generated=%d offered=%d sent=%d producer_send_wait_total=%s producer_blocked_count=%d producer_blocked_duration=%s cpu=%s alloc_bytes=%d mallocs=%d rolling100ms={%s} rolling500ms={%s} inter_delivery_ns={%s} max_actual_burst=%d(threshold=1ms) skipped_slots=%d deficit_units={max=%.0f,at=%s,final=%.0f,recovered=%t,recovered_at=%s} actual_span=%s actual_units_per_sec=%.0f virtual_span=%s virtual_units_per_sec=%.0f virtual_to_actual_lateness_ns={%s} underflow=%t input_backpressure=%t\n", rate, mode, phaseText, accepted, float64(accepted)/elapsed.Seconds(), bps, (bps-float64(rate))/float64(rate)*100, elapsed, pending, stats.generated, stats.offered, stats.sent, stats.sendWaitTotal, stats.blockedCount, stats.blockedDuration, cpu, after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs, rolling(actual, start, elapsed, rate, 100*time.Millisecond), rolling(actual, start, elapsed, rate, 500*time.Millisecond), intervals(actual), maxBurst(actual, time.Millisecond), skipCount, def.max, def.at, def.final, def.recovered, def.recoveredAt, timestampSpan(actual), timestampRate(actual), durationSpan(virtual), durationRate(virtual), intervalsDuration(lateness), skipCount > 0, stats.blockedCount > 0 || pending > 0)
}

type producerStats struct {
	generated, offered, sent, blockedCount uint64
	blockedDuration, sendWaitTotal         time.Duration
}

// The first 100ms quota is released at run start (including slot zero). Later
// bursts follow absolute 100ms boundaries; delayed wakes release every due batch.
func produce(ctx context.Context, ch chan struct{}, rate int64, duration time.Duration, started <-chan time.Time, stats *producerStats, done chan struct{}) {
	defer close(done)
	defer close(ch)
	const quantum = 100 * time.Millisecond
	origin := <-started
	den := int64(unitBits) * int64(time.Second)
	stats.generated++
	stats.offered++
	began := time.Now()
	select {
	case ch <- struct{}{}:
		stats.sent++
	case <-ctx.Done():
		wait := time.Since(began)
		stats.sendWaitTotal += wait
		if wait > time.Millisecond {
			stats.blockedDuration += wait
			stats.blockedCount++
		}
		return
	}
	wait := time.Since(began)
	stats.sendWaitTotal += wait
	if wait > time.Millisecond {
		stats.blockedDuration += wait
		stats.blockedCount++
	}
	batches := int64(duration / quantum)
	for batch := int64(1); batch <= batches; batch++ {
		if batch > 1 {
			deadline := origin.Add(time.Duration(batch-1) * quantum)
			timer := time.NewTimer(time.Until(deadline))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		n := batchUnits(rate, batch, den, quantum)
		stats.generated += uint64(n)
		for i := int64(0); i < n; i++ {
			stats.offered++
			began := time.Now()
			select {
			case ch <- struct{}{}:
				stats.sent++
			case <-ctx.Done():
				wait := time.Since(began)
				stats.sendWaitTotal += wait
				if wait > time.Millisecond {
					stats.blockedDuration += wait
					stats.blockedCount++
				}
				return
			}
			wait := time.Since(began)
			stats.sendWaitTotal += wait
			if wait > time.Millisecond {
				stats.blockedDuration += wait
				stats.blockedCount++
			}
		}
	}
	end := origin.Add(duration)
	timer := time.NewTimer(time.Until(end))
	select {
	case <-ctx.Done():
		timer.Stop()
	case <-timer.C:
	}
}
func batchUnits(rate, batch, den int64, q time.Duration) int64 {
	// Compute cumulative quotas exactly before differencing; the intermediate
	// product can overflow int64 even for a supported PLL rate and a 100ms batch.
	numerator := new(big.Int).Mul(big.NewInt(rate), big.NewInt(int64(q)))
	cumulative := func(n int64) *big.Int {
		return new(big.Int).Quo(new(big.Int).Mul(numerator, big.NewInt(n)), big.NewInt(den))
	}
	return new(big.Int).Sub(cumulative(batch), cumulative(batch-1)).Int64()
}
func cpuSeconds() (float64, bool) {
	s := []metrics.Sample{
		{Name: "/cpu/classes/total:cpu-seconds"},
		{Name: "/cpu/classes/idle:cpu-seconds"},
	}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindFloat64 || s[1].Value.Kind() != metrics.KindFloat64 {
		return 0, false
	}
	// The total metric is available CPU time, not consumed CPU time. Subtract
	// estimated idle time to approximate process CPU use.
	used := s[0].Value.Float64() - s[1].Value.Float64()
	if used < 0 {
		used = 0
	}
	return used, true
}
func rolling(ts []time.Time, start time.Time, elapsed time.Duration, rate int64, width time.Duration) string {
	values := rollingErrors(ts, start, elapsed, rate, width)
	if len(values) == 0 {
		return "no_complete_windows"
	}
	sort.Float64s(values)
	mean := 0.0
	for _, v := range values {
		mean += v
	}
	mean /= float64(len(values))
	q := func(p float64) float64 { return values[int(float64(len(values)-1)*p)] }
	return fmt.Sprintf("complete_sliding_windows=%d,min=%.1f,mean=%.1f,p95=%.1f,max=%.1f", len(values), values[0], mean, q(.95), values[len(values)-1])
}
func rollingErrors(ts []time.Time, start time.Time, elapsed time.Duration, rate int64, width time.Duration) []float64 {
	if width <= 0 || elapsed < width {
		return nil
	}
	target := float64(rate) * width.Seconds() / unitBits
	values := make([]float64, 0)
	left := 0
	for right, t := range ts {
		for left <= right && t.Sub(ts[left]) >= width {
			left++
		}
		age := t.Sub(start)
		if age >= width && age <= elapsed {
			count := right - left + 1
			values = append(values, (float64(count)-target)/target*100)
		}
	}
	return values
}
func intervalsDuration(ts []time.Duration) string {
	if len(ts) < 2 {
		return "insufficient_samples"
	}
	v := make([]int64, len(ts))
	for i := range ts {
		v[i] = int64(ts[i])
	}
	return durationValues(v)
}
func durationValues(v []int64) string {
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	q := func(p float64) int64 { return v[int(float64(len(v)-1)*p)] }
	return fmt.Sprintf("n=%d,min=%d,p50=%d,p95=%d,p99=%d,max=%d", len(v), v[0], q(.5), q(.95), q(.99), v[len(v)-1])
}
func intervalValues(v []int64) string {
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	q := func(p float64) int64 { return v[int(float64(len(v)-1)*p)] }
	return fmt.Sprintf("n=%d,min=%d,p50=%d,p95=%d,p99=%d,max=%d", len(v), v[0], q(.5), q(.95), q(.99), v[len(v)-1])
}
func intervals(ts []time.Time) string {
	if len(ts) < 2 {
		return "insufficient_samples"
	}
	v := make([]int64, len(ts)-1)
	for i := 1; i < len(ts); i++ {
		v[i-1] = ts[i].Sub(ts[i-1]).Nanoseconds()
	}
	return intervalValues(v)
}
func maxBurst(ts []time.Time, w time.Duration) int {
	best, l := 0, 0
	for r := range ts {
		for ts[r].Sub(ts[l]) > w {
			l++
		}
		if r-l+1 > best {
			best = r - l + 1
		}
	}
	return best
}

type deficitResult struct {
	max, final      float64
	at, recoveredAt time.Duration
	recovered       bool
}

func deficitSummary(ts []time.Time, start time.Time, elapsed time.Duration, rate int64) deficitResult {
	due := func(at time.Duration) float64 {
		return math.Floor(float64(rate)*at.Seconds()/unitBits) + 1
	}
	r := deficitResult{final: due(elapsed) - float64(len(ts))}
	values := make([]float64, 0, len(ts)+1)
	times := make([]time.Duration, 0, len(ts)+1)
	for i, t := range ts {
		at := t.Sub(start)
		values = append(values, due(at)-float64(i+1))
		times = append(times, at)
	}
	values = append(values, r.final)
	times = append(times, elapsed)
	peak := -1
	for i, v := range values {
		if peak < 0 || v > r.max {
			r.max = v
			r.at = times[i]
			peak = i
		}
	}
	if r.max > 0 {
		for i := peak + 1; i < len(values); i++ {
			if values[i] <= 0 {
				r.recovered = true
				r.recoveredAt = times[i]
				break
			}
		}
	}
	return r
}
func skippedSlots(slots []uint64) uint64 {
	var skipped uint64
	if len(slots) > 0 {
		skipped = slots[0]
	}
	for i := 1; i < len(slots); i++ {
		if slots[i] > slots[i-1]+1 {
			skipped += slots[i] - slots[i-1] - 1
		}
	}
	return skipped
}
func timestampSpan(ts []time.Time) time.Duration {
	if len(ts) < 2 {
		return 0
	}
	return ts[len(ts)-1].Sub(ts[0])
}
func timestampRate(ts []time.Time) float64 {
	span := timestampSpan(ts)
	if len(ts) < 2 || span <= 0 {
		return 0
	}
	return float64(len(ts)-1) / span.Seconds()
}
func durationSpan(ts []time.Duration) time.Duration {
	if len(ts) < 2 {
		return 0
	}
	return ts[len(ts)-1] - ts[0]
}
func durationRate(ts []time.Duration) float64 {
	span := durationSpan(ts)
	if len(ts) < 2 || span <= 0 {
		return 0
	}
	return float64(len(ts)-1) / span.Seconds()
}
