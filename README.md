# pll

`github.com/EnvelopeInc/pll` generates transport-agnostic trigger signals at intervals derived from a target MPEG-TS bitrate, assuming 188-byte packets. External timing events can correct the schedule. It does not read, write, or parse packets, perform transport I/O, or guarantee wire-level timing.

Requires Go 1.22.2 or newer.

## Install

```sh
go get github.com/EnvelopeInc/pll@v0.1.0
```

## Example

```go
package main

import (
	"fmt"

	"github.com/EnvelopeInc/pll"
)

func main() {
	p := pll.NewPLL(10, 0, 0, 0) // 10 Mbps; zero PID gains
	p.Start()
	fmt.Println(<-p.TriggerCh)
	p.Stop()
}
```

## API and behavior

- `NewPLL(mbps, kp, ki, kd)` takes a target decimal megabit-per-second rate. Each PID gain is an integer scaled by 100 (`100` means a factor of 1). It panics for a non-finite or non-positive rate, or when the derived packet period is outside the positive `time.Duration` range accepted by timers. The period uses 188-byte packets and is truncated to nanoseconds.
- `Start()` runs the worker; `StartContext(ctx)` runs it until context cancellation or `Stop()`. A nil context panics. A PLL is one-shot: later starts do nothing, even after shutdown. `Stop()` is idempotent, waits for an active worker to exit, and prevents a not-yet-started PLL from starting.
- `TriggerCh` emits unbuffered `true` signals. The worker can wait for a receiver, but shutdown can interrupt that wait. `EventCh` is an unbuffered input; send one value per external event to use for correction. The bool value is ignored. The first deadline is one packet period after worker start; event timing is compared with that deadline.
- The PLL owns and closes both channels on shutdown; callers must not close them. Stop event-sender goroutines before calling `Stop()` or canceling the context, since either shutdown path closes `EventCh`. Do not send after shutdown.

## Timing characterization and limitations

The repository includes a bounded, standard-library timing harness:

```sh
go run ./cmd/pll-timing -mbps 10 -duration 5s
```

It uses zero PID gains and sends no `EventCh` inputs. It timestamps receipt from `TriggerCh` and reports average period/rate error and interval-jitter percentiles. Jitter is the absolute deviation from the observed mean period. Duration is limited to 100ms–30s and storage to one million samples, so a run may stop early at that sample limit. These measurements characterize trigger-channel delivery on the current host only; they do not measure packet transmission or wire timing and are not cross-platform guarantees. Timer resolution, OS scheduling, receiver scheduling, and workload affect results. The worker schedules against absolute deadlines but skips missed intervals instead of emitting catch-up bursts. Tests do not assert a hardware-specific timing tolerance.

PID gains are not range-validated. Intermediate arithmetic avoids integer overflow; the integral accumulator saturates at the `int64` bounds and the computed delay is clamped to `[0, period]`. This does not guarantee stable or useful control for any gain choice. Validate behavior on the target host and workload.

## Contributing

Format Go changes with `gofmt`. Before opening a pull request, run:

```sh
go test -race ./...
go vet ./...
```

Pull requests should explain the user-visible change, include focused tests for behavior changes, and report relevant validation. Keep documentation aligned with the API and its timing limitations. CI runs the race tests and vet on Go 1.22.x and the current stable Go version.
