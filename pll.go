package pll

import (
	"context"
	"math"
	"math/big"
	"sync"
	"time"
)

// PLL represents a Phase-Locked Loop
type PLL struct {
	EventCh   chan bool // Input event channel; stop senders before calling Stop.
	TriggerCh chan bool // PLL output signal, closed when the PLL stops.

	period time.Duration // Period of reference clock signal
	delay  time.Duration // interval between scheduled trigger deadlines

	kp int // Proportional gain (over 100)
	ki int // Integral gain (over 100)
	kd int // Derivative gain (over 100)

	integral  int64     // Integral term for PID controller
	lastTick  time.Time // last tick time
	lastDelta int64     // Last delta (error) for PID controller

	mu      sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewPLL creates a PLL for the specified bitrate in Mbps and initializes it
// with the specified PID parameters. It panics if the bitrate is not finite and
// positive or produces a period outside the positive, representable time.Duration range accepted by time.NewTimer.
func NewPLL(mbps float64, kp, ki, kd int) *PLL {
	const packetSize = 188 * 8 // packet size in bits
	if math.IsNaN(mbps) || math.IsInf(mbps, 0) || mbps <= 0 {
		panic("pll: bitrate must be a finite positive number of Mbps")
	}
	periodNanos := float64(packetSize) / (mbps * 1e6) * 1e9
	if math.IsInf(periodNanos, 0) || periodNanos < 1 || periodNanos >= float64(math.MaxInt64) {
		panic("pll: bitrate produces a period outside the valid time.Timer range")
	}
	period := time.Duration(periodNanos)
	return &PLL{
		EventCh:   make(chan bool),
		TriggerCh: make(chan bool),
		period:    period,
		delay:     period, // start with the period as the delay
		done:      make(chan struct{}),
		kp:        kp,
		ki:        ki,
		kd:        kd,
	}
}

// Start begins the operation of the PLL. It is equivalent to StartContext with
// context.Background(). A PLL can be started only once.
func (pll *PLL) Start() {
	pll.StartContext(context.Background())
}

// StartContext begins the operation of the PLL until ctx is canceled. A PLL
// can be started only once; subsequent calls after it has started or stopped
// have no effect. ctx must not be nil.
func (pll *PLL) StartContext(ctx context.Context) {
	if ctx == nil {
		panic("pll: nil context")
	}
	pll.mu.Lock()
	if pll.started || pll.stopped {
		pll.mu.Unlock()
		return
	}
	pll.started = true
	workerCtx, cancel := context.WithCancel(ctx)
	pll.cancel = cancel
	pll.mu.Unlock()
	go pll.run(workerCtx)
}

func (pll *PLL) run(ctx context.Context) {
	defer func() {
		pll.mu.Lock()
		pll.stopped = true
		pll.mu.Unlock()
		close(pll.EventCh)
		close(pll.TriggerCh)
		close(pll.done)
	}()

	pll.lastTick = time.Now() // Events before the first deadline use run start as their reference.
	deadline := pll.lastTick.Add(pll.period)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			select {
			case pll.TriggerCh <- true:
			case <-ctx.Done():
				return
			}
			pll.lastTick = deadline
			interval := pll.delay
			if interval <= 0 {
				interval = pll.period
			}
			deadline = deadline.Add(interval)
			// Do not emit a burst to catch up after a delayed receiver or scheduler.
			if !deadline.After(time.Now()) {
				deadline = time.Now().Add(interval)
			}
			timer.Reset(time.Until(deadline))
		case _, ok := <-pll.EventCh:
			if !ok {
				return
			}
			delta := time.Since(pll.lastTick.Add(pll.period))
			pll.pidController(delta)
		}
	}
}

// Stop stops the PLL and waits for its worker to exit. It is safe to call more
// than once. Stopping before Start prevents the PLL from being started later.
func (pll *PLL) Stop() {
	pll.mu.Lock()
	if !pll.started {
		if !pll.stopped {
			pll.stopped = true
			close(pll.EventCh)
			close(pll.TriggerCh)
			close(pll.done)
		}
		pll.mu.Unlock()
		return
	}
	cancel := pll.cancel
	done := pll.done
	pll.mu.Unlock()
	cancel()
	<-done
}

// pidController implements a PID controller for adjusting the PLL output signal
// all math is integer math here
// we are adjusting the delay to get event to happen near the next tick
func (pll *PLL) pidController(delta time.Duration) {
	deltaNanos := int64(delta)
	proportional := scaledGain(deltaNanos, pll.kp)
	pll.integral = saturatingAdd(pll.integral, scaledGain(deltaNanos, pll.ki))
	derivative := scaledGain(saturatingSub(deltaNanos, pll.lastDelta), pll.kd)
	pll.lastDelta = deltaNanos

	// Use wide intermediates so arbitrary int gains cannot overflow before the
	// output is bounded to the valid delay interval.
	adjustment := new(big.Int).SetInt64(proportional)
	adjustment.Add(adjustment, new(big.Int).SetInt64(pll.integral))
	adjustment.Add(adjustment, new(big.Int).SetInt64(derivative))
	delay := new(big.Int).Sub(big.NewInt(int64(pll.delay)), adjustment)
	if delay.Sign() < 0 {
		pll.delay = 0
	} else if delay.Cmp(big.NewInt(int64(pll.period))) > 0 {
		pll.delay = pll.period
	} else {
		pll.delay = time.Duration(delay.Int64())
	}
}

func scaledGain(value int64, gain int) int64 {
	product := new(big.Int).Mul(big.NewInt(value), big.NewInt(int64(gain)))
	product.Quo(product, big.NewInt(100))
	if product.IsInt64() {
		return product.Int64()
	}
	if product.Sign() < 0 {
		return math.MinInt64
	}
	return math.MaxInt64
}

func saturatingAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}

func saturatingSub(a, b int64) int64 {
	if b > 0 && a < math.MinInt64+b {
		return math.MinInt64
	}
	if b < 0 && a > math.MaxInt64+b {
		return math.MaxInt64
	}
	return a - b
}
