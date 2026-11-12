// Command pll-integration measures a simulated producer/scheduler/queue/consumer
// path. It makes no physical transport or hardware claims.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"math/big"
	"os"
	"runtime"
	"runtime/metrics"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/EnvelopeInc/pll"
)

const (
	rate      = int64(19_392_000)
	unitBytes = int64(188)
	bits      = unitBytes * 8
)

type scenario struct {
	name                          string
	ppm                           int64
	burst, slow, stall, reference bool
	cpuWorkers, gomaxprocs        int
}
type item struct {
	emission pll.Emission
	enqueued time.Time
}
type boundedQueue struct {
	mu                sync.Mutex
	items             []item
	head, size, high  int
	waiting           int
	closed            bool
	notEmpty, notFull chan struct{}
}

func newBoundedQueue(capacity int) *boundedQueue {
	return &boundedQueue{items: make([]item, capacity), notEmpty: make(chan struct{}, 1), notFull: make(chan struct{}, 1)}
}
func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
func (q *boundedQueue) Enqueue(ctx context.Context, v item) (time.Duration, error) {
	began := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return time.Since(began), err
		}
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return time.Since(began), context.Canceled
		}
		if q.size < len(q.items) {
			tail := (q.head + q.size) % len(q.items)
			v.enqueued = time.Now()
			q.items[tail] = v
			q.size++
			if q.size > q.high {
				q.high = q.size
			}
			q.mu.Unlock()
			signal(q.notEmpty)
			return time.Since(began), nil
		}
		q.waiting++
		q.mu.Unlock()
		var waitErr error
		select {
		case <-ctx.Done():
			waitErr = ctx.Err()
		case <-q.notFull:
		}
		q.mu.Lock()
		q.waiting--
		q.mu.Unlock()
		if waitErr != nil {
			return time.Since(began), waitErr
		}
	}
}
func (q *boundedQueue) Dequeue() (item, bool) {
	for {
		q.mu.Lock()
		if q.size > 0 {
			v := q.items[q.head]
			q.items[q.head] = item{}
			q.head = (q.head + 1) % len(q.items)
			q.size--
			q.mu.Unlock()
			signal(q.notFull)
			return v, true
		}
		if q.closed {
			q.mu.Unlock()
			return item{}, false
		}
		q.mu.Unlock()
		<-q.notEmpty
	}
}
func (q *boundedQueue) Close()         { q.mu.Lock(); q.closed = true; q.mu.Unlock(); signal(q.notEmpty) }
func (q *boundedQueue) HighWater() int { q.mu.Lock(); defer q.mu.Unlock(); return q.high }
func (q *boundedQueue) Waiting() int   { q.mu.Lock(); defer q.mu.Unlock(); return q.waiting }

type accepted struct {
	emission     pll.Emission
	enqueued, at time.Time
}
type producerStats struct {
	offered, sent, blocked uint64
	wait                   time.Duration
}
type result struct {
	origin            time.Time
	runDuration       time.Duration
	deliveries        []accepted
	producer          producerStats
	queueHigh         int
	enqueueWait       time.Duration
	scheduler         pll.RuntimeStats
	skipped           uint64
	refPhase          []int64
	refSource         referenceSourceStats
	refTelemetry      referenceTelemetry
	refStates         map[pll.SyncState]uint64
	refFinalState     string
	cpuWorkers        int
	gomaxprocs        int
	schedulerAccepted uint64
	schedulerPending  uint64
	err               error
}

func main() {
	seconds := flag.Int("seconds", 10, "seconds per scenario [10,12]; 9 scenarios, total <=108s")
	capacity := flag.Int("queue", 64, "consumer queue capacity [1,4096]")
	gomaxprocs := runtime.GOMAXPROCS(0)
	workerLimit := flag.Int("cpu-worker-limit", gomaxprocs, "maximum contention workers; defaults to GOMAXPROCS")
	flag.Parse()
	if *seconds < 10 || *seconds > 12 || *capacity < 1 || *capacity > 4096 || *workerLimit < 1 || *workerLimit > gomaxprocs {
		fmt.Fprintf(os.Stderr, "seconds must be 10..12, queue 1..4096, cpu-worker-limit 1..GOMAXPROCS(%d)\n", gomaxprocs)
		os.Exit(2)
	}
	moderate, substantial := contentionWorkers(gomaxprocs, *workerLimit)
	cases := []scenario{{name: "continuous-nominal"}, {name: "100ms-burst-quota", burst: true}, {name: "producer-plus-100ppm", ppm: 100}, {name: "producer-minus-100ppm", ppm: -100}, {name: "consumer-delay", slow: true}, {name: "consumer-stall-backpressure", stall: true}, {name: "reference-jitter-interruption-recovery", reference: true}, {name: "moderate-cpu-contention", cpuWorkers: moderate, gomaxprocs: gomaxprocs}, {name: "substantial-cpu-contention", cpuWorkers: substantial, gomaxprocs: gomaxprocs}}
	for _, sc := range cases {
		sc.gomaxprocs = gomaxprocs
		r, d, cpu, alloc := run(sc, time.Duration(*seconds)*time.Second, *capacity)
		report(sc, r, d, cpu, alloc)
	}
}
func contentionWorkers(gomaxprocs, limit int) (int, int) {
	if gomaxprocs < 1 {
		gomaxprocs = 1
	}
	effective := gomaxprocs
	if limit > 0 && limit < effective {
		effective = limit
	}
	moderate := maxInt(1, effective/4)
	substantial := maxInt(1, effective-1)
	return moderate, substantial
}
func run(sc scenario, duration time.Duration, capacity int) (result, time.Duration, string, uint64) {
	p, err := pll.NewPLL(rate, unitBytes)
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	origin := time.Now()
	avail := make(chan struct{})
	q := newBoundedQueue(capacity)
	var ps producerStats
	var pg sync.WaitGroup
	pg.Add(1)
	go func() { defer pg.Done(); defer close(avail); produce(ctx, avail, origin, duration, sc, &ps) }()
	var refs *pll.ReferenceQueue
	var syncer *pll.Synchronizer
	if sc.reference {
		refs, err = pll.NewReferenceQueue(1024)
		if err != nil {
			panic(err)
		}
		zero := time.Duration(0)
		syncer, err = pll.NewSynchronizer(p, pll.SyncConfig{ReferenceTimeAtVirtualZero: &zero, Kp: 5, Ki: 6.25, MaxCorrectionPPM: 1000, SlewLimitPPMPerSecond: 2000, MaxSampleAge: time.Second, MaxReferenceInterval: 250 * time.Millisecond, MaxPhaseResidual: time.Second, LockTolerance: 20 * time.Microsecond, LockSamples: 4})
		if err != nil {
			panic(err)
		}
	}
	var out []accepted
	var consume sync.WaitGroup
	consume.Add(1)
	go func() {
		defer consume.Done()
		stalled := false
		for {
			v, ok := q.Dequeue()
			if !ok {
				return
			}
			if sc.stall && !stalled && time.Since(origin) >= 3*time.Second {
				stalled = true
				time.Sleep(time.Second)
			}
			if sc.slow {
				time.Sleep(2 * time.Millisecond)
			}
			out = append(out, accepted{v.emission, v.enqueued, time.Now()})
		}
	}()
	loadCtx, stopLoad := context.WithCancel(context.Background())
	var loads sync.WaitGroup
	var work atomic.Uint64
	for i := 0; i < sc.cpuWorkers; i++ {
		loads.Add(1)
		go func() {
			defer loads.Done()
			x := uint64(1)
			for {
				select {
				case <-loadCtx.Done():
					return
				default:
					for n := 0; n < 10000; n++ {
						x = x*2862933555777941757 + 3037000493
					}
					work.Add(x)
				}
			}
		}()
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	cpu0, cpuOK := cpuSeconds()
	stats := pll.RuntimeStats{}
	var enqueueWait atomic.Int64
	res := result{origin: origin, runDuration: duration, refStates: map[pll.SyncState]uint64{}, refTelemetry: newReferenceTelemetry(duration)}
	mailbox := make(chan emissionSnapshot, 1)
	var referenceWG sync.WaitGroup
	if sc.reference {
		res.refTelemetry.observe(0, syncer.Status(0), syncer.Trim()*1e6)
		referenceWG.Add(1)
		go func() {
			defer referenceWG.Done()
			res.refSource = runReference(ctx, origin, mailbox, refs)
		}()
	}
	measurementOrigin := origin
	if !sc.reference {
		measurementOrigin = time.Now()
	}
	res.origin = measurementOrigin
	startAt := measurementOrigin
	schedulerAccepted, schedulerPending, runErr := pll.RunScheduler(ctx, p, avail, func(ctx context.Context, e pll.Emission) error {
		now := time.Now()
		elapsed := now.Sub(origin)
		if sc.reference {
			state := syncer.Status(elapsed)
			res.refStates[state]++
			res.refTelemetry.observe(elapsed, state, syncer.Trim()*1e6)
			publishSnapshot(mailbox, emissionSnapshot{emission: e, at: elapsed})
		}
		wait, err := q.Enqueue(ctx, item{emission: e})
		enqueueWait.Add(int64(wait))
		return err
	}, pll.RuntimeConfig{WakeInterval: time.Millisecond, MaxBurst: pll.MaxBatchSize, MaxAvailable: 4096, Stats: &stats, MeasurePhases: true, References: refs, Synchronizer: syncer, Origin: origin})
	measurementElapsed := time.Since(startAt)
	finalSyncState := "not_enabled"
	if syncer != nil {
		// Scheduler has returned: ownership is now safely back on this goroutine.
		res.refTelemetry.observe(time.Since(origin), syncer.Status(time.Since(origin)), syncer.Trim()*1e6)
		finalSyncState = stateName(res.refTelemetry.lastState)
	}
	cpu1, cpuOK1 := cpuSeconds()
	runtime.ReadMemStats(&after)
	cancel()
	referenceWG.Wait()
	if refs != nil {
		refs.Close()
		res.refPhase = res.refSource.phase
	}
	pg.Wait()
	q.Close()
	consume.Wait()
	stopLoad()
	loads.Wait()
	elapsed := time.Since(measurementOrigin)
	res.deliveries = out
	res.producer = ps
	res.queueHigh = q.HighWater()
	res.enqueueWait = time.Duration(enqueueWait.Load())
	res.scheduler = stats
	res.err = runErr
	res.schedulerAccepted = schedulerAccepted
	res.schedulerPending = schedulerPending
	res.refFinalState = finalSyncState
	res.cpuWorkers = sc.cpuWorkers
	res.gomaxprocs = sc.gomaxprocs
	for i, v := range out {
		if i == 0 {
			res.skipped += v.emission.Slot
		} else if v.emission.Slot > out[i-1].emission.Slot+1 {
			res.skipped += v.emission.Slot - out[i-1].emission.Slot - 1
		}
	}
	cpu := "unsupported"
	if cpuOK && cpuOK1 {
		cpu = fmt.Sprintf("%.1f%%", 100*(cpu1-cpu0)/measurementElapsed.Seconds())
	}
	return res, elapsed, cpu, after.TotalAlloc - before.TotalAlloc
}

type sourceClock struct{ quotient, remainder, denominator, deadline, remainderAccumulator int64 }

func newSourceClock(ppm int64) sourceClock {
	numerator := bits * 1_000_000_000 * 1_000_000
	denominator := rate * (1_000_000 + ppm)
	return sourceClock{quotient: numerator / denominator, remainder: numerator % denominator, denominator: denominator}
}
func (c *sourceClock) Deadline() time.Duration { return time.Duration(c.deadline) }
func (c *sourceClock) Advance() {
	c.deadline += c.quotient
	c.remainderAccumulator += c.remainder
	if c.remainderAccumulator >= c.denominator {
		c.deadline++
		c.remainderAccumulator -= c.denominator
	}
}

func produce(ctx context.Context, ch chan<- struct{}, origin time.Time, duration time.Duration, sc scenario, s *producerStats) {
	if sc.burst {
		var sent uint64
		for boundary := int64(0); ; boundary++ {
			timer := time.NewTimer(time.Until(origin.Add(time.Duration(boundary) * 100 * time.Millisecond)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if time.Since(origin) >= duration {
				return
			}
			next := quotaBoundaryUnits(boundary+1, sc.ppm)
			for sent < next {
				if !offer(ctx, ch, s) {
					return
				}
				sent++
			}
		}
	}
	clock := newSourceClock(sc.ppm)
	timer := time.NewTimer(time.Until(origin.Add(clock.Deadline())))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if time.Since(origin) >= duration {
			return
		}
		if !offer(ctx, ch, s) {
			return
		}
		clock.Advance()
		delay := time.Until(origin.Add(clock.Deadline()))
		if delay < 0 {
			delay = 0
		}
		timer.Reset(delay)
	}
}
func quotaAtDuration(d time.Duration, ppm int64) uint64 {
	if d <= 0 {
		return 0
	}
	n := new(big.Int).Mul(big.NewInt(int64(d)), big.NewInt(rate*(1_000_000+ppm)))
	den := new(big.Int).Mul(big.NewInt(bits*1_000_000_000), big.NewInt(1_000_000))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(n, den, rem)
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Uint64()
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func quotaBoundaryUnits(boundary, ppm int64) uint64 {
	return quotaAtDuration(time.Duration(boundary)*100*time.Millisecond, ppm)
}
func sourceDeadline(n uint64, ppm int64) time.Duration {
	num := new(big.Int).SetUint64(n)
	num.Mul(num, big.NewInt(bits*1_000_000_000*1_000_000))
	den := big.NewInt(rate * (1_000_000 + ppm))
	num.Quo(num, den)
	return time.Duration(num.Int64())
}
func offer(ctx context.Context, ch chan<- struct{}, s *producerStats) bool {
	s.offered++
	t := time.Now()
	select {
	case ch <- struct{}{}:
		s.sent++
	case <-ctx.Done():
		s.blocked++
		s.wait += time.Since(t)
		return false
	}
	w := time.Since(t)
	s.wait += w
	if w > time.Millisecond {
		s.blocked++
	}
	return true
}
func oscillator(slot uint64, ppm int64, j time.Duration) time.Duration {
	n := new(big.Int).SetUint64(slot)
	n.Mul(n, big.NewInt(bits*1_000_000_000*1_000_000))
	den := big.NewInt(rate * (1_000_000 + ppm))
	n.Quo(n, den)
	return time.Duration(n.Int64()) + j
}
func report(sc scenario, r result, elapsed time.Duration, cpu string, alloc uint64) {
	ts := make([]time.Duration, len(r.deliveries))
	enq := make([]time.Duration, len(ts))
	slots := make([]uint64, len(ts))
	lateness := make([]int64, len(ts))
	if len(ts) > 0 {
		for i, v := range r.deliveries {
			ts[i] = v.at.Sub(r.origin)
			enq[i] = v.enqueued.Sub(r.origin)
			slots[i] = v.emission.Slot
			lateness[i] = int64(ts[i] - v.emission.Timestamp)
		}
	}
	bps := float64(len(ts)) * float64(bits) / elapsed.Seconds()
	nominal := !sc.reference
	virtual := "corrected_or_not_applicable"
	if nominal {
		if exact(r.deliveries) {
			virtual = "all_emissions_equal_PLL_VirtualTime"
		} else {
			virtual = "mismatch"
		}
	}
	fmt.Printf("scenario=%s duration=%s accepted=%d bitrate_bps=%.0f virtual_time=%s producer_ppm=%d producer_offered=%d producer_sent=%d producer_expected_including_slot0=%d producer_shortfall=%d producer_blocked=%d producer_wait=%s delivery_slot_gap_proxy=%d scheduler_wakes=%d wake_lateness=%s max_burst=%d availability_events=%d scheduler_accepted=%d scheduler_pending_on_return=%d queue_highwater=%d enqueue_wait_total=%s consumer_enqueue_to_accept_ns=%s virtual_to_accept_lateness_ns=%s delivery_intervals_ns=%s max_delivery_burst_1ms=%d windows_100ms=%s windows_500ms=%s deficit_recovery={%s} cpu_workers=%d gomaxprocs=%d go_runtime_process_cpu_estimate_pct=%s go_process_alloc_delta_bytes=%d scheduler_error=%v\n", sc.name, elapsed, len(ts), bps, virtual, sc.ppm, r.producer.offered, r.producer.sent, quotaAtDuration(r.runDuration, sc.ppm), maxInt(0, int(quotaAtDuration(r.runDuration, sc.ppm))-int(r.producer.sent)), r.producer.blocked, r.producer.wait, r.skipped, r.scheduler.TimerWakeCount, r.scheduler.TimerWakeLateness, r.scheduler.MaxBurstObserved, r.scheduler.AvailabilityEventCount, r.schedulerAccepted, r.schedulerPending, r.queueHigh, r.enqueueWait, intervalDist(enqueueDelay(enq, ts)), intervalDist(lateness), intervalDist(intervals(ts)), maxBurst(ts, time.Millisecond), windowDist(ts, 100*time.Millisecond, elapsed), windowDist(ts, 500*time.Millisecond, elapsed), deficit(ts, elapsed), r.cpuWorkers, r.gomaxprocs, cpu, alloc, r.err)
	if sc.reference {
		fmt.Printf("reference=independent_monotonic_ticker ideal-slot-association-only drift_ppm=50 deterministic_jitter=±3us gap=4s..6s phase_ns=%s trim_range_ppm=[%.3f,%.3f] callback_status_observations(not durations)=%s final_synchronizer_state_at_scheduler_stop=%s accepted=%d synchronizer_rejected=%v pending=%d\n", intDist(r.refPhase), r.refTelemetry.minTrim, r.refTelemetry.maxTrim, stateCounts(r.refStates), r.refFinalState, r.scheduler.ReferenceAccepted, r.scheduler.ReferenceRejected, r.scheduler.ReferencePending)
		fmt.Printf("reference_ticks=%d interrupted_ticks=%d unassociated_ticks=%d submitted=%d reference_submit_rejected=%v association_max_age=%s mailbox_capacity=1 %s\n", r.refSource.ticks, r.refSource.interrupted, r.refSource.unassociated, r.refSource.submitted, r.refSource.rejected, associationMaxAge, r.refTelemetry.summary())
	}
}
func exact(ds []accepted) bool {
	p, _ := pll.NewPLL(rate, unitBytes)
	for _, d := range ds {
		v, e := p.VirtualTime(d.emission.Slot)
		if e != nil || v != d.emission.Timestamp {
			return false
		}
	}
	return true
}
func intervals(v []time.Duration) []int64 {
	x := make([]int64, 0, len(v))
	for i := 1; i < len(v); i++ {
		x = append(x, int64(v[i]-v[i-1]))
	}
	return x
}
func enqueueDelay(enq, acc []time.Duration) []int64 {
	x := make([]int64, 0, len(enq))
	for i := range enq {
		x = append(x, int64(acc[i]-enq[i]))
	}
	return x
}
func intervalDist(x []int64) string {
	if len(x) == 0 {
		return "n=0"
	}
	sort.Slice(x, func(i, j int) bool { return x[i] < x[j] })
	q := func(f float64) int64 { return x[int(float64(len(x)-1)*f)] }
	return fmt.Sprintf("n=%d,min=%d,p50=%d,p95=%d,p99=%d,max=%d", len(x), x[0], q(.5), q(.95), q(.99), x[len(x)-1])
}
func intDist(x []int64) string { return intervalDist(x) }
func windowErrors(ts []time.Duration, w, elapsed time.Duration) []float64 {
	if elapsed < w {
		return nil
	}
	values := make([]float64, 0, int(elapsed/(10*time.Millisecond)))
	left, right := 0, 0
	target := float64(rate) * w.Seconds() / float64(bits)
	for end := w; end <= elapsed; end += 10 * time.Millisecond {
		cut := end - w
		for right < len(ts) && ts[right] <= end {
			right++
		}
		for left < right && ts[left] <= cut {
			left++
		}
		values = append(values, (float64(right-left)-target)/target*100)
	}
	return values
}
func windowDist(ts []time.Duration, w, elapsed time.Duration) string {
	values := windowErrors(ts, w, elapsed)
	if len(values) == 0 {
		return "n=0"
	}
	sort.Float64s(values)
	return fmt.Sprintf("n=%d,min=%.1f,p50=%.1f,p95=%.1f,max=%.1f,worst_abs_pct=%.1f", len(values), values[0], values[len(values)/2], values[int(.95*float64(len(values)-1))], values[len(values)-1], math.Max(math.Abs(values[0]), math.Abs(values[len(values)-1])))
}
func maxBurst(ts []time.Duration, w time.Duration) int {
	best, l := 0, 0
	for r := range ts {
		for l <= r && ts[r]-ts[l] > w {
			l++
		}
		if r-l+1 > best {
			best = r - l + 1
		}
	}
	return best
}

type deficitResult struct {
	peak, final         float64
	peakAt, recoveredAt time.Duration
	recovered           bool
}

func measureDeficit(ts []time.Duration, elapsed time.Duration) deficitResult {
	unitRate := float64(rate) / float64(bits)
	expected := func(t time.Duration) float64 { return math.Floor(unitRate*t.Seconds()) + 1 }
	r := deficitResult{final: math.Max(0, expected(elapsed)-float64(len(ts)))}
	peakIndex := -1
	for i, t := range ts {
		d := expected(t) - float64(i+1)
		if peakIndex < 0 || d > r.peak {
			r.peak = d
			r.peakAt = t
			peakIndex = i
		}
	}
	if d := expected(elapsed) - float64(len(ts)); peakIndex < 0 || d > r.peak {
		r.peak = d
		r.peakAt = elapsed
		peakIndex = len(ts)
	}
	if r.peak > 0 {
		for i := peakIndex + 1; i < len(ts); i++ {
			if expected(ts[i])-float64(i+1) <= 0 {
				r.recovered = true
				r.recoveredAt = ts[i]
				break
			}
		}
	}
	return r
}
func deficit(ts []time.Duration, elapsed time.Duration) string {
	d := measureDeficit(ts, elapsed)
	return fmt.Sprintf("peak_units=%.1f@%s,final_units=%.1f,recovered=%t,recovered_at=%s", d.peak, d.peakAt, d.final, d.recovered, d.recoveredAt)
}
func stateCounts(m map[pll.SyncState]uint64) string {
	return fmt.Sprintf("acquiring:%d,locked:%d,holdover:%d,reacquiring:%d", m[pll.SyncAcquiring], m[pll.SyncLocked], m[pll.SyncHoldover], m[pll.SyncReacquiring])
}
func stateName(s pll.SyncState) string {
	return [...]string{"acquiring", "locked", "holdover", "reacquiring"}[s]
}
func cpuSeconds() (float64, bool) {
	s := []metrics.Sample{{Name: "/cpu/classes/total:cpu-seconds"}, {Name: "/cpu/classes/idle:cpu-seconds"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindFloat64 || s[1].Value.Kind() != metrics.KindFloat64 {
		return 0, false
	}
	return math.Max(0, s[0].Value.Float64()-s[1].Value.Float64()), true
}
