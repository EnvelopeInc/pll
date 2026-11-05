# pll

`github.com/EnvelopeInc/pll` is a Go package for generating pacing triggers at the rate of 188-byte MPEG-TS packets and adjusting trigger timing from external events.

Install it with:

```sh
go get github.com/EnvelopeInc/pll
```

## API and lifecycle

Create a PLL with `NewPLL(mbps, kp, ki, kd)`, where `mbps` is the target rate in decimal megabits per second and the integer PID gains are scaled by 100 (for example, `kp == 100` applies a proportional factor of 1). The constructor returns `*pll.PLL`; it panics if the rate is non-finite, non-positive, or would produce a period outside the valid `time.Ticker` range. The packet period is calculated from 188 bytes at the target bitrate and truncated to `time.Duration` nanoseconds.

`Start()` runs until `Stop()` is called. `StartContext(ctx)` runs until `ctx` is canceled or `Stop()` is called; passing a nil context panics. A PLL is one-shot: subsequent starts have no effect, including after it has stopped. `Stop()` is safe to call more than once, waits for the worker to exit, and prevents a not-yet-started PLL from starting. Stopping or context cancellation closes both channels.

Send one value on `EventCh` for each external timing event to be used for correction; the boolean value is ignored. Receive pacing signals from `TriggerCh` (each emitted value is `true`). Both channels are unbuffered. The worker may block while delivering a trigger until a receiver accepts it, but cancellation and `Stop()` can still end that wait. Stop all goroutines sending to `EventCh` before calling `Stop()` or canceling the `StartContext` context, because either shutdown path closes the channel. Do not send to either channel after shutdown.

## Timing characterization

Run the standalone standard-library harness with a target bitrate and bounded duration:

```sh
go run ./cmd/pll-timing -mbps 10 -duration 5s
```

The harness runs with zero PID gains and sends no `EventCh` inputs, so it characterizes baseline timer/`TriggerCh` delivery rather than correction from an external timing source. It reports average period/rate error and interval-jitter percentiles, where jitter is the absolute deviation from the observed mean period. Duration is limited to 100ms–30s and storage to one million samples (the run stops early if that sample limit is reached). Results characterize this PLL implementation on the current OS/hardware only; they are not wire-level proof, a hardware-independent measurement, or a cross-platform timing guarantee. Scheduler load, timer resolution, channel scheduling, and the receiving process affect these metrics. No hardware-specific tolerance is asserted by tests.

## Limitations

PID gain ranges are not validated. PID intermediates are computed safely and the resulting delay is clamped to `[0, period]`; the integral accumulator saturates at the `int64` limits rather than wrapping. This arithmetic protection does not establish that any particular gain values produce stable or useful control. An event received before the first ticker update is referenced to the worker's run start. Validate pacing with the target stream and workload; tests and the harness do not establish wire timing or MPEG-TS integration behavior.

## Development and releases

CI runs `go test -race ./...` and `go vet ./...` on Go 1.22.x and the stable Go release. Run the same checks locally. For versioning and publication instructions, see [RELEASING.md](RELEASING.md).
