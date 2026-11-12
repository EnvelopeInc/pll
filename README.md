# pll

`github.com/version2beta/pll` is a Go library for keeping time: it paces work at an exact rate, and follows another clock with a phase-locked loop.

<!-- site:skip -->
Also published at [version2beta.com](https://version2beta.com/tools/pll-go/).
<!-- /site:skip -->

## The need

Programs that produce or consume things at a steady rate run into the same two problems.

- **Keeping a steady pace on your own clock.** Sending samples, frames, packets or requests at a fixed rate sounds like a timer. But a timer that adds a rounded period on every tick drifts, a rate like 30000/1001 per second has no exact period in nanoseconds, and a program that wakes up late has to decide what happens to the slots it missed.
- **Keeping pace with someone else's clock.** A sound card, a sensor, a remote server or another program each runs on its own crystal, a little fast or slow, and its readings reach you late and unevenly. To put its timestamps on your timeline, or to keep a device with its own clock fed without overrunning or starving it, you have to estimate how its clock relates to yours, continuously, without being fooled by jitter.

pll has a part for each, and they work alone or together.

- A **Pacer** numbers evenly spaced slots at an exact rate and tells you which slots are due. Each slot's time is computed from its number with exact rational arithmetic, so it never drifts.
- A **Follower** is a phase-locked loop. Fed readings of another clock taken against your own, it estimates how far apart the two clocks are and how fast the other one runs. It filters out jitter, rejects outliers, notices when the other clock jumps, and translates times in both directions. Its trim can drive a Pacer.

The package never reads a clock, starts a goroutine or touches your data. You pass the time in, as a `time.Duration` from an origin you choose, and the hot paths allocate nothing.

## At a glance

| | |
|---|---|
| Version | v0.3.2 |
| Go | 1.23 or newer; standard library only |
| License | MIT |
| Install | `go get github.com/version2beta/pll@v0.3.2` |
| Repository | [github.com/version2beta/pll](https://github.com/version2beta/pll) |
| API reference | [pkg.go.dev/github.com/version2beta/pll](https://pkg.go.dev/github.com/version2beta/pll) |

## Quick start

Pace 48,000 samples per second. Wake up on any schedule; `Due` hands back whatever has come due since the last call.

```go
package main

import (
	"fmt"
	"time"

	"github.com/version2beta/pll"
)

func main() {
	rate, err := pll.UnitsPerSecond(48_000, 1)
	if err != nil {
		panic(err)
	}
	p, err := pll.NewPacer(rate)
	if err != nil {
		panic(err)
	}

	for _, now := range []time.Duration{0, 10 * time.Millisecond, 25 * time.Millisecond} {
		span, err := p.Due(now, 4096)
		if err != nil {
			panic(err)
		}
		fmt.Printf("at %v: %d due, slots %d to %d\n", now, span.Count, span.First, span.First+span.Count-1)
	}
}
```

```text
at 0s: 1 due, slots 0 to 0
at 10ms: 480 due, slots 1 to 480
at 25ms: 720 due, slots 481 to 1200
```

In a real program `now` is `time.Since(start)`. A late wake-up loses nothing: the overdue slots come back together. `span.Time(i)` is each slot's scheduled time, rounded down to whole nanoseconds, and `p.NextDue()` says when the next slot falls due.

## Examples

### A pacing loop

A Pacer has one owner. Here a goroutine owns it, wakes every 10 ms, and hands each batch of due slots to a consumer.

```go
// pace owns the Pacer: it wakes every 10 ms and sends whatever has come due.
func pace(ctx context.Context, p *pll.Pacer, out chan<- pll.Span) error {
	start := time.Now()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			span, err := p.Due(time.Since(start), 4096)
			if err != nil {
				return err
			}
			if span.Count > 0 {
				out <- span
			}
		}
	}
}

func main() {
	rate, _ := pll.UnitsPerSecond(48_000, 1)
	p, _ := pll.NewPacer(rate)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	spans := make(chan pll.Span)
	go func() {
		pace(ctx, p, spans)
		close(spans)
	}()

	var units uint64
	for span := range spans {
		units += span.Count // write span.Count units, stamped with span.Time(i)
	}
	fmt.Println("units in one second:", units) // about 48,000
}
```

A `Span` is a small value that carries its own timing, so it is safe to send to another goroutine.

### Variable-size units at a byte rate

Give each byte a slot, and claim one slot per byte of each datagram.

```go
rate, _ := pll.BitsPerSecond(50_000_000, 8) // 50 Mbit/s, one slot per byte
p, _ := pll.NewPacer(rate)
for _, size := range []uint64{1500, 400, 1500} {
	span, _ := p.Claim(size)
	fmt.Printf("%4d bytes: send at %v\n", size, span.Time(0))
}
```

```text
1500 bytes: send at 0s
 400 bytes: send at 240µs
1500 bytes: send at 304µs
```

### Keeping a device with its own clock fed

A device that consumes units on its own clock tells you how many it has consumed. That count is a reading of its clock. Follow it, and let the follower trim the pacer that feeds it.

```go
rate, _ := pll.UnitsPerSecond(48000, 1)
p, _ := pll.NewPacer(rate)
f, _ := pll.NewFollower(pll.DefaultFollowerConfig(rate))

consumed := func(t time.Duration) int64 { // the device runs 40 ppm fast
	return int64(t.Seconds() * 48000 * (1 + 40e-6))
}
for t := time.Duration(0); t <= 5*time.Minute; t += 100 * time.Millisecond {
	f.Observe(t, consumed(t))
	p.SetTrim(f.Trim())
}
fmt.Printf("pacer trim %.1f ppm\n", float64(p.Trim())/1e6) // pacer trim 40.3 ppm
```

In the simulator, a linked pacer holds the device's queue within one unit for five minutes; unlinked, the queue drifts by 576 units.

## Behavior worth knowing

### Rates

- `UnitsPerSecond(num, den)` is `num/den` units per second, reduced to lowest terms; `BitsPerSecond(bits, bitsPerUnit)` is the same with `bits/bitsPerUnit`. A zero term returns `ErrInvalidRate`.
- Any positive rate works. Above 10⁹ units per second, several slots share a nanosecond, and times are non-decreasing rather than strictly increasing.

### Slots

- Slot `s` is at exactly `s × den × 10⁹ / num` nanoseconds from the origin; `Span.Time` reports the floor. A slot is due when its exact time is at or before `now`, compared without rounding: a slot at 8/3 ns has time 2 ns but is due from 3 ns.
- The cursor marks the next slot not yet handed out or skipped. It never moves back, so an earlier `now` than before finds nothing new and is not an error.
- `Due(now, max)` hands out the oldest due slots, at most `max`. `Claim(n)` hands out the next `n` slots whether or not they are due. `Skip(now, keep)` passes over due slots, keeping the newest `keep`. `NextDue()` returns the cursor slot and the first whole nanosecond at which it is due.
- A `Span` covers slots `First` to `First+Count−1`. `Span.Time(i)` panics when `i >= Count`, as slice indexing does. `Span.All()` is an `iter.Seq2` over slots and times.

### Trim

- `SetTrim(ppt)` runs the pacer at nominal × (1 + ppt/10¹²) from the cursor on, for `ppt` strictly between −10¹² and 10¹².
- The new timing starts at the floor of the cursor slot's exact time, so the phase moves by less than 1 ns and times never decrease. Spans already handed out keep their times. Setting the trim the pacer already has changes nothing.

### The follower

- The first reading after `NewFollower` or `Reset` anchors the mapping. After that, readings must not go back in local time, and remote ticks must increase; otherwise `Observe` returns `ErrOrder` and changes nothing.
- `Observe` reports what it did with each reading: `StatusAnchored`, `StatusAccepted`, `StatusPending` (waiting in a window), `StatusRejected` (an outlier), or `StatusStepped` (enough agreeing outliers to show the other clock jumped; the mapping re-anchors and keeps its rate estimate).
- `State(now)` is `StateLocked` after `LockCount` consecutive samples within `LockThreshold`, `StateHoldover` when no sample has been accepted for `HoldoverAfter`, and `StateAcquiring` otherwise. In holdover the mapping keeps running at the last rate estimate.
- `Local` and `Remote` translate between remote ticks and local time. `Offset` is the estimated rate offset, `Trim` is that offset in parts per trillion for `Pacer.SetTrim`, and `Jitter` is a running RMS of recent phase errors.
- The defaults suit readings with microseconds of jitter. Readings that cross a network need a slower loop that looks at the least-delayed reading in each window:

| Setting | Low jitter (the defaults) | Network |
|---|---|---|
| `Bandwidth` | 0.1 Hz | 0.005 Hz |
| `Window` | 0 (every reading) | 1 s |
| `LockThreshold` | 100 µs | 2 ms |
| `OutlierThreshold` | 5 ms | 20 ms |
| In the simulator | Locks in 80 ms; 12 µs RMS error with 20 µs of jitter | Locks in 18–92 s; 0.7 ms RMS error with 2 ms of jitter and batched delivery |

The loop equations, and every rule above in full, are in the [specification](https://github.com/version2beta/pll/blob/main/SPEC.md).

### Limits and errors

- Errors are matched with `errors.Is`: `ErrInvalidRate`, `ErrInvalidArgument` (a negative time, a zero count, a trim out of range), `ErrOverflow` (a slot index past 2⁶⁴ − 2, or a time or mapping that does not fit in a `time.Duration`), `ErrOrder` and `ErrNotAnchored`. A call that returns an error changes nothing.
- An invalid follower setting returns a `*ConfigError` naming the first bad field; use `errors.As`.
- Pacer arithmetic is exact for every input, including 64-bit rate terms, slot numbers near 2⁶⁴ and times near the largest `time.Duration`. The follower's estimates are float64; its anchor is held as whole nanoseconds plus a fraction, so its precision does not depend on how long the program has run.
- On an Apple M5, handing out one slot takes about 5 ns, and 64 slots with their times about 155 ns. `Due`, `Claim`, `Skip`, `NextDue`, `Span.Time` and `Observe` do not allocate.

### Concurrency

- A `Pacer` or a `Follower` is not safe for concurrent use. Give each one owner, as in the pacing loop above, or guard it yourself.
- A `Span` is an immutable value and may be shared freely.

## How we know it's correct

- **A deliberately naive reference for the pacer.** It recomputes every slot time from scratch with `big.Rat`, straight from the formula, with no fast path.
- **Differential tests** run thousands of random operation sequences against that reference, including extreme rates, trims, slot numbers near 2⁶⁴ and times near the largest `time.Duration`, and require identical results. A fuzz target does the same with generated input, and invariants are checked after every operation.
- **A literal model of the follower's loop equations** checks every update, and invariant tests and a fuzz target check that the rate estimate stays in bounds, the mapping never runs backwards, and an update never moves the phase by more than the error that caused it.
- **A deterministic simulator** runs the follower against clocks that are fast, slow, drifting, changing rate or jumping, delivered with jitter, heavy-tailed delays, batching and outages, and holds each scenario to thresholds.
- **Planted bugs,** 8 in the pacer and 13 in the follower, were all caught. CI runs every test with the race detector on Go 1.23 and the current release, and fuzzes each target for 30 seconds.

[TESTING.md](https://github.com/version2beta/pll/blob/main/TESTING.md) describes each test file and the reasoning behind these checks.

### Check it yourself

1. Clone the repository and run the tests:
   ```sh
   git clone https://github.com/version2beta/pll.git
   cd pll
   make verify
   ```
2. Fuzz each target against its reference for 30 seconds, and print the simulator's results:
   ```sh
   make fuzz
   make sim
   ```
   ```text
           scenario  locked at  RMS error  max error  rate error  steps  rejected  end state
         sound-card       80ms     11.7µs     21.9µs   0.608 ppm      0         0     locked
     sensor-network    18.301s    667.4µs    987.1µs   7.029 ppm      1         2     locked
        rate-change       80ms     11.7µs     21.8µs   0.706 ppm      0         0     locked
             outage       80ms     11.6µs     22.3µs   2.246 ppm      0         0     locked
         clock-step       80ms     11.6µs     22.7µs   1.066 ppm      1         2     locked
      aging-crystal       80ms     11.7µs     25.3µs   1.903 ppm      0         0     locked
   ```
3. Plant a bug. In `pacer.go`, find the line in `ceilTime` that rounds a slot's time up to the next whole nanosecond:
   ```go
   if r != 0 {
   ```
   Change it to `if r > 1 {`, then run:
   ```sh
   go test -run TestDifferential
   ```
   The test fails immediately. It shows what the pacer returned, what the reference returned, and the operations that led there. Undo the change with `git checkout pacer.go`.

## How it compares

<!-- Re-verify these facts before each publish. -->

| | pll | [golang.org/x/time/rate](https://pkg.go.dev/golang.org/x/time/rate) | [go.uber.org/ratelimit](https://pkg.go.dev/go.uber.org/ratelimit) | [facebook/time servo](https://pkg.go.dev/github.com/facebook/time/servo) |
|---|---|---|---|---|
| Purpose | Exact pacing, and following another clock | Rate limiting | Rate limiting | Steering a clock's frequency from measured offsets, in Meta's PTP and NTP libraries |
| Method | Exact rational schedule; type-II phase-locked loop | Token bucket | Leaky bucket | PI servo |
| Rate | Exact fraction of units per second, with a trim | `float64` events per second, with a burst size | Events per period, with optional slack | Not applicable |
| Waiting | You wake up; `Due` says what is due | `Allow`, `Reserve` or a blocking `Wait` | `Take` sleeps until the next event is allowed | Not applicable |
| Time source | Passed in | `time.Now`, or a time passed to the `…At` methods | A `Clock`, replaceable for tests | Offsets passed in |
| Follows another clock | Yes, from readings, with windowing, outlier rejection, steps and holdover | No | No | Yes, from clock offsets, with a spike filter and holdover state |

Outside Go, delay-locked loops such as PipeWire's [`spa_dll`](https://docs.pipewire.org/dll_8h_source.html) and FFmpeg's [`timefilter`](https://ffbox0-bg.ffmpeg.org/doxygen/4.4/timefilter_8h_source.html) filter a device's clock against the system clock, configured by a single bandwidth.

**When to choose something else:**
- If you want to limit how often something happens, with bursts allowed, `x/time/rate` is the standard choice; pll spaces slots evenly and has no burst allowance.
- If you want a call that simply blocks until the next event is allowed, `uber-go/ratelimit` does that in one line; with pll you write the wake-up loop yourself.
- If you are disciplining a system clock to PTP or NTP, use a servo built for that, such as the one in `facebook/time`, which works with clock offsets and frequency adjustments rather than readings of another clock.

## Colophon

Design and copyright by M. Robert Martin.\
Implemented with assistance from Claude Opus 5.5, Claude Haiku 5.5 and OpenAI gpt-6.1-sol.\
License: MIT License.
