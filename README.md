# pll: exact pacing and clock following for Go

A Go library that paces work at an exact rate and follows another clock with a phase-locked loop.

## At a glance

| | |
| --- | --- |
| Version | v0.1.0 |
| License | [MIT](https://github.com/Version2beta/pll/blob/main/LICENSE) |
| Install | `go get github.com/version2beta/pll@v0.1.0` |
| Repository | [github.com/Version2beta/pll](https://github.com/Version2beta/pll) |
| API reference | [pkg.go.dev/github.com/version2beta/pll](https://pkg.go.dev/github.com/version2beta/pll) |
| Requires | Go 1.23 or newer; standard library only |

pll has two parts. Use either one alone, or link them.

- A **Pacer** divides time into evenly spaced, numbered slots at an exact rate and tells you which slots are due. Every slot's time is computed from its number with exact rational arithmetic, so it never drifts, even at rates like 30000/1001 per second.
- A **Follower** is a phase-locked loop. Feed it readings of another clock, such as a sound card, a sensor or a remote server, taken against your own. It learns how far apart the two clocks are and how fast the other one runs, filters out jitter and outliers, notices when the other clock jumps, and translates between the two.

Feeding a follower's trim to a pacer keeps your output in step with a device that runs on its own clock.

The package never reads a clock, starts a goroutine or touches your data. You pass the time in, as a `time.Duration` from an origin you choose, and the hot paths allocate nothing.

## Quick start

Pace 48,000 audio samples per second. Wake up on any schedule; `Due` hands back whatever has come due since the last call.

```go
rate, _ := pll.UnitsPerSecond(48_000, 1) // 48,000 units per second
p, _ := pll.NewPacer(rate)

for _, now := range []time.Duration{0, 10 * time.Millisecond, 25 * time.Millisecond} {
	span, _ := p.Due(now, 4096)
	fmt.Printf("at %v: %d due, slots %d to %d\n", now, span.Count, span.First, span.First+span.Count-1)
}
```

```text
at 0s: 1 due, slots 0 to 0
at 10ms: 480 due, slots 1 to 480
at 25ms: 720 due, slots 481 to 1200
```

In a real program `now` is `time.Since(start)`. A late wake-up loses nothing: the overdue slots all come back at once. `span.Time(i)` is the exact scheduled time of each slot, and `p.NextDue()` says when to wake up next.

## Examples

### A rate that is not a whole number

```go
rate, _ := pll.UnitsPerSecond(30000, 1001) // 29.97 per second
p, _ := pll.NewPacer(rate)
p.Claim(1_000_000)
span, _ := p.Claim(1)
fmt.Println(span.First, span.Time(0)) // 1000000 9h16m6.666666666s
```

Slot one million is exactly where it should be. Adding a rounded period a million times would have drifted.

### Variable-size units at a byte rate

Give each byte a slot and claim one slot per byte of each datagram.

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

### Nothing to send

When there is nothing to put in old slots, skip them and keep the newest.

```go
rate, _ := pll.UnitsPerSecond(1000, 1)
p, _ := pll.NewPacer(rate)
now := 2 * time.Second
skipped, _ := p.Skip(now, 10) // keep the newest 10 due slots
span, _ := p.Due(now, 10)
fmt.Println(skipped, span.First, span.Count) // 1991 1991 10
```

### Put another clock's readings on your timeline

A sensor counts microseconds on its own crystal, which runs 50 ppm fast.

```go
nominal, _ := pll.UnitsPerSecond(1_000_000, 1) // a microsecond counter
f, _ := pll.NewFollower(pll.DefaultFollowerConfig(nominal))

sensor := func(local time.Duration) int64 { // its crystal runs 50 ppm fast
	return int64(local.Seconds() * 1e6 * (1 + 50e-6))
}
for local := time.Duration(0); local <= time.Minute; local += 10 * time.Millisecond {
	f.Observe(local, sensor(local))
}
when, _ := f.Local(sensor(time.Minute + 500*time.Millisecond))
fmt.Printf("%.1f ppm, %v, %v\n", f.Offset()*1e6, f.State(time.Minute), when.Round(time.Microsecond))
// 50.0 ppm, locked, 1m0.5s
```

`Observe` reports what it did with each reading: anchored, accepted, rejected as an outlier, or stepped because the other clock jumped. `Local` and `Remote` translate in either direction.

### Keep a device with its own clock fed

The units a device has consumed are a reading of its clock. Follow it, and let the follower trim the pacer.

```go
rate, _ := pll.UnitsPerSecond(48000, 1)
p, _ := pll.NewPacer(rate)
f, _ := pll.NewFollower(pll.DefaultFollowerConfig(rate))

consumed := func(t time.Duration) int64 { // the device runs 40 ppm fast
	return int64(t.Seconds() * 48000 * (1 + 40e-6))
}
for t := time.Duration(0); t <= 5*time.Minute; t += 100 * time.Millisecond {
	f.Observe(t, consumed(t)) // units consumed: a reading of the device's clock
	p.SetTrim(f.Trim())
}
fmt.Printf("pacer trim %.1f ppm\n", float64(p.Trim())/1e6) // pacer trim 40.3 ppm
```

In the simulator, a linked pacer holds a device's queue within one unit for five minutes; unlinked, the queue drifts by 576 units.

## Choosing follower settings

The defaults suit readings that arrive with microseconds of jitter, such as a sound card's position. Readings that cross a network arrive with milliseconds of jitter, sometimes in bunches, and need a slower loop that looks at the least-delayed reading in each window.

| Setting | Low jitter (the defaults) | Network |
| --- | --- | --- |
| `Bandwidth` | 0.1 Hz | 0.005 Hz |
| `Window` | 0 (every reading) | 1 s |
| `LockThreshold` | 100 µs | 2 ms |
| `OutlierThreshold` | 5 ms | 20 ms |
| In the simulator | locks in 80 ms; 12 µs RMS error with 20 µs of jitter | locks in 18–92 s; 0.7 ms RMS error with 2 ms of jitter and batched delivery |

```go
cfg := pll.DefaultFollowerConfig(nominal)
cfg.Window = time.Second
cfg.Bandwidth = 0.005
cfg.LockThreshold = 2 * time.Millisecond
cfg.OutlierThreshold = 20 * time.Millisecond
f, err := pll.NewFollower(cfg)
```

`f.Jitter()` reports the spread of recent phase errors, which helps when choosing `OutlierThreshold`. The [specification](https://github.com/Version2beta/pll/blob/main/SPEC.md) gives the loop equations.

## Language notes

- Times are `time.Duration` values from an origin you choose. Pass `time.Since(start)`, or simulated time in tests.
- A `Pacer` or `Follower` is not safe for concurrent use. Keep one owner, or guard it yourself.
- Match errors with `errors.Is`: `ErrInvalidRate`, `ErrInvalidArgument`, `ErrOverflow`, `ErrOrder`, `ErrNotAnchored`. A bad follower setting returns a `*ConfigError` naming the field; use `errors.As`.
- `Span.All` returns an `iter.Seq2`, so `for slot, at := range span.All()` works.
- `Span.Time` panics on an index out of range, as slice indexing does.
- On an Apple M5, handing out one slot takes about 5 ns and handing out 64 with their times about 155 ns, with no allocations.

## How we know it's correct

- **The pacer is exact.** A deliberately naive model recomputes every slot time from scratch with `big.Rat`. A seeded randomized test and a fuzz target compare the pacer against it over operation sequences that include rates with 64-bit terms, extreme trims, slot numbers near 2^64 and times near the largest `time.Duration`. Invariants are checked after every operation.
- **The follower follows its equations.** A literal model of the loop equations checks every update. Invariants run after every reading on random and fuzzed sequences: the rate estimate stays in bounds, the mapping never runs backwards, and an update never moves the phase by more than the error that caused it.
- **The follower works on realistic clocks.** A deterministic simulator runs it against clocks that are fast, slow, drifting, changing rate or jumping, delivered with jitter, heavy-tailed delays, batching and outages. Each scenario has pass thresholds.
- **The tests catch real bugs.** Planted bugs, 8 in the pacer and 13 in the follower, were all caught.
- **CI** runs the tests with the race detector on Go 1.23 and the current release, and fuzzes each target for 30 seconds.

[TESTING.md](https://github.com/Version2beta/pll/blob/main/TESTING.md) says what each test checks and how to plant a bug yourself.

### Check it yourself

```sh
git clone https://github.com/Version2beta/pll && cd pll
make verify   # formatting, vet, tests, race detector
make fuzz     # 30 s per fuzz target
make sim      # the scenario table below
make bench
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

## How it compares

| Library | Language | Kind | Paces at a fixed rate | Follows another clock | Configured by |
| --- | --- | --- | --- | --- | --- |
| pll | Go | Pacer and type-II phase-locked loop | Yes, exact rational schedule | Yes, with windowing, outlier rejection, steps and holdover | Rate; bandwidth and damping |
| [golang.org/x/time/rate](https://pkg.go.dev/golang.org/x/time/rate) | Go | Token-bucket rate limiter | Allows events at an average rate, with bursts | No | Rate and burst size |
| [facebook/time servo](https://pkg.go.dev/github.com/facebook/time/servo) | Go | PI clock servo for PTP | No | Yes, from clock offsets, with a spike filter and holdover state | Proportional and integral gains |
| [PipeWire `spa_dll`](https://docs.pipewire.org/dll_8h_source.html) | C | Delay-locked loop, a header of about 60 lines | No | Returns a rate correction from each error | Bandwidth |
| [FFmpeg `timefilter`](https://ffbox0-bg.ffmpeg.org/doxygen/4.4/timefilter_8h_source.html) | C | Delay-locked loop for device timestamps | No | Filters a device clock against the system clock | Bandwidth in Hz |

## Colophon

- Design and copyright by M. Robert Martin
- Implemented with assistance from Claude Opus 5.5, Claude Haiku 5.5 and OpenAI GPT-6.1 Sol
- License: [MIT License](https://github.com/Version2beta/pll/blob/main/LICENSE)

This README is also pll's page on [version2beta.com](https://version2beta.com/tools/pll/).
