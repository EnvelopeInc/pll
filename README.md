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

## Limitations

The package does not validate PID gain ranges or protect PID arithmetic from integer overflow. Select gains conservatively and validate pacing with the target stream and workload; the existing tests do not establish measured timing accuracy or MPEG-TS integration behavior.

## Development and releases

CI runs `go test -race ./...` and `go vet ./...` on Go 1.22.x and the stable Go release. Run the same checks locally. For versioning and publication instructions, see [RELEASING.md](RELEASING.md).
