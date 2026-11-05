package pll

import (
	"context"
	"math"
	"sync"
	"time"
)

// PLL represents a Phase-Locked Loop
type PLL struct {
	EventCh   chan bool // Input event channel; stop senders before calling Stop.
	TriggerCh chan bool // PLL output signal, closed when the PLL stops.

	period time.Duration // Period of reference clock signal
	delay  time.Duration // number of nanoseconds to wait after receiving a ticker signal

	kp int // Proportional gain (over 100)
	ki int // Integral gain (over 100)
	kd int // Derivative gain (over 100)

	integral  int       // Integral term for PID controller
	lastTick  time.Time // last tick time
	lastDelta int       // Last delta (error) for PID controller

	mu      sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewPLL creates a PLL for the specified bitrate in Mbps and initializes it
// with the specified PID parameters. It panics if the bitrate is not finite and
// positive or produces a period that cannot be represented by a valid ticker.
func NewPLL(mbps float64, kp, ki, kd int) *PLL {
	const packetSize = 188 * 8 // packet size in bits
	if math.IsNaN(mbps) || math.IsInf(mbps, 0) || mbps <= 0 {
		panic("pll: bitrate must be a finite positive number of Mbps")
	}
	periodNanos := float64(packetSize) / (mbps * 1e6) * 1e9
	if math.IsInf(periodNanos, 0) || periodNanos < 1 || periodNanos >= float64(math.MaxInt64) {
		panic("pll: bitrate produces an invalid ticker period")
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

	ticker := time.NewTicker(pll.period)
	defer ticker.Stop()

	var triggerTimer *time.Timer
	defer func() {
		if triggerTimer != nil && !triggerTimer.Stop() {
			select {
			case <-triggerTimer.C:
			default:
			}
		}
	}()
	var trigger <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pll.lastTick = time.Now()
			if triggerTimer == nil {
				triggerTimer = time.NewTimer(pll.delay)
			} else {
				if !triggerTimer.Stop() {
					select {
					case <-triggerTimer.C:
					default:
					}
				}
				triggerTimer.Reset(pll.delay)
			}
			trigger = triggerTimer.C
		case <-trigger:
			select {
			case pll.TriggerCh <- true:
			case <-ctx.Done():
				return
			}
			trigger = nil
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
	porportional := int(delta) * pll.kp / 100
	pll.integral += int(delta) * pll.ki / 100
	derivative := (int(delta) - pll.lastDelta) * pll.kd / 100
	pll.lastDelta = int(delta)

	delay := pll.delay - time.Duration(porportional+pll.integral+derivative)
	if delay > pll.period {
		delay = pll.period
	}
	if delay < 0 {
		delay = 0
	}
	pll.delay = delay
}
