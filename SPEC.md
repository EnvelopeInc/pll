# pll specification

This file is binding for the build loop. Where it and a test disagree, the
test decides; record the disagreement in PROGRESS.md.

The design document behind it lives outside the repository. Part 1 covers
`Rate`, `Pacer` and `Span`, built in stage 1. Part 2 covers the `Follower`,
built in stage 2.

## Part 1. The pacer

### 1.1 Scope

Package `pll`, module `github.com/version2beta/pll`, Go 1.23 or newer,
standard library only.

The package keeps time; it never touches payloads. It never reads a clock,
starts a goroutine, sleeps, or does I/O. Every call that depends on the
present takes `now time.Duration`, elapsed from an origin the caller chooses.
Values are not safe for concurrent use.

Files:

| File | State |
| --- | --- |
| `doc.go`, `errors.go`, `follower_errors.go`, `status.go` | Complete; do not change |
| `internal/sim/`, `cmd/pllsim/` | Complete; do not change |
| `rate.go`, `pacer.go`, `span.go` | Built in stage 1; change only if a part 2 task needs it |
| `follower.go` | Exported API fixed; bodies and unexported fields are yours |
| `*_test.go`, `testdata/` | The definition of done; do not change |

### 1.2 Rate

A `Rate` is an exact number of units per second, `num/den`, stored reduced to
lowest terms (divide both by their greatest common divisor).

- `UnitsPerSecond(num, den)`: returns the reduced rate. If `num` or `den` is
  0, returns the zero `Rate` and `ErrInvalidRate`.
- `BitsPerSecond(bits, bitsPerUnit)`: identical to
  `UnitsPerSecond(bits, bitsPerUnit)`.
- `Num()`, `Den()`: the reduced numerator and denominator.
- `String()`: `"48000 units/s"` when the denominator is 1,
  `"30000/1001 units/s"` otherwise, and `"invalid rate"` for the zero `Rate`.

### 1.3 Slots and timing

A `Pacer` numbers slots 0, 1, 2, … and keeps a **cursor**: the next slot not
yet handed out or skipped. The cursor starts at 0 and never decreases. Slot
`math.MaxUint64` is never handed out, so a span's `First+Count` never
overflows.

Timing is held as a **segment**: a start slot `S`, an integer anchor `A` in
nanoseconds, and a trim `q` in parts per trillion. The **exact time** of slot
`s ≥ S` is the rational number

    T(s) = A + (s − S) × den × 10^21 / (num × (10^12 + q))   nanoseconds

A new pacer has the segment `S = 0, A = 0, q = 0`, so without trim slot `s` is
at exactly `s × den × 10^9 / num` ns.

- The **time** of a slot, as `Span.Time` reports it, is `floor(T(s))`.
- A slot is **due at** `now` when `T(s) ≤ now`, compared exactly. A slot at
  8/3 ns has time 2 ns but is first due at 3 ns.
- A time **fits** when it is ≤ `math.MaxInt64` (all times here are ≥ 0).

### 1.4 Pacer

`NewPacer(r)`: a pacer at rate `r`, cursor 0, no trim. A zero numerator or
denominator in `r` returns `nil, ErrInvalidRate`.

`Due(now, max) (Span, error)`:

1. `now < 0` or `max == 0`: `ErrInvalidArgument`.
2. Let `L` be the largest slot that is due at `now`. If no slot `≥ cursor` is
   due, return `Span{First: cursor, Count: 0}` and no error.
3. If the cursor is `math.MaxUint64` (and so that slot is due):
   `ErrOverflow`.
4. Otherwise hand out slots `cursor … min(L, MaxUint64−1)`, at most `max` of
   them, oldest first, and move the cursor past them.

An earlier `now` than a previous call is not an error; it finds nothing new.

`Claim(n) (Span, error)`: hand out the next `n` slots whether or not they are
due. `n == 0`: `ErrInvalidArgument`. `n > MaxUint64 − cursor`, or the last
slot's time does not fit: `ErrOverflow`.

`Skip(now, keep) (uint64, error)`: let `D` be the number of slots that `Due`
could hand out at `now` (ignoring `max`). Move the cursor past
`D − min(D, keep)` of them, the oldest, and return that number. `now < 0`:
`ErrInvalidArgument`. Cursor at `MaxUint64` with that slot due: `ErrOverflow`.
`Skip(now, k)` then `Due(now, k)` hands out the newest `k` due slots.

`NextDue() (slot, at, err)`: the cursor slot and `ceil(T(cursor))`, the first
whole nanosecond at which it is due. `ErrOverflow` (with `slot` and `at` 0)
when the cursor is `MaxUint64` or that time does not fit.

`SetTrim(ppt) error`: run at `nominal × (1 + ppt/10^12)` from the cursor on.

1. `ppt ≤ −10^12` or `ppt ≥ 10^12`: `ErrInvalidArgument`.
2. `ppt` equal to the current trim: nothing changes (no re-anchoring).
3. Otherwise the new segment is `S = cursor`, `A = floor(T_old(cursor))`,
   `q = ppt`. If that `A` does not fit: `ErrOverflow`.

Anchoring at the floor moves the phase by less than 1 ns, keeps times
non-decreasing, and keeps every anchor an integer, so the arithmetic never
accumulates growing fractions.

`Trim()`: the current trim.

Every call that returns an error changes nothing.

### 1.5 Span

`Span{First, Count}` covers slots `First … First+Count−1`. It carries the
segment it was issued under: a later `SetTrim` never changes its times.

- `Time(i)`: `floor(T(First+i))` as a `time.Duration`. Panics when
  `i >= Count`, with a message like an index out of range.
- `All()`: an `iter.Seq2[uint64, time.Duration]` yielding `(slot, time)` for
  each slot in order; it stops when the consumer stops.
- The zero `Span` has `Count 0`: `Time` always panics and `All` yields nothing.

### 1.6 Arithmetic

Results must be exact for every input, including rates with 64-bit numerators
and denominators, trims at ±(10^12 − 1), slot indexes near `2^64` and times
near `math.MaxInt64`. Products such as `(s − S) × den × 10^21` exceed 128
bits, so:

- Keep the segment's period as a reduced fraction `pn/pd`
  (`den × 10^21 / (num × (10^12 + q))` divided by its gcd), computed once when
  the segment is made, with `math/big`.
- Common path: when `pn` and `pd` fit in `uint64`, compute
  `floor(k × pn / pd)` with `math/bits.Mul64` and `bits.Div64`, which is exact
  when the high word is below the divisor.
- Otherwise fall back to `math/big`.
- The largest due slot: `S + floor((now − A) × pd / pn)` when `now ≥ A`, by
  the same two paths; cap at `math.MaxUint64`.

The model in `model_test.go` recomputes everything with `big.Rat` from the
formula in 1.3; the tests compare against it.

### 1.7 Allocation

For everyday rates (num and den below 2^32) with or without a trim of ±50 ppm,
`Due`, `Claim`, `Skip`, `NextDue`, `Span.Time` must not allocate. Hold the
segment as an immutable value made once per `NewPacer` or `SetTrim`, and let
spans share it.

### 1.8 Errors

`errors.go` defines them; return these values (callers use `errors.Is`).

| Error | When |
| --- | --- |
| `ErrInvalidRate` | Zero numerator or denominator, including the zero `Rate` |
| `ErrInvalidArgument` | `now < 0`, `max` or `n` is 0, or a trim out of range |
| `ErrOverflow` | A slot index or slot time that does not fit, as above |

## Part 2. The follower

### 2.1 What it does

A `Follower` is a second-order (type-II) phase-locked loop. It reads a remote
clock as integer ticks at a nominal rate (`FollowerConfig.Nominal`, a `Rate`
in ticks per second) and learns the mapping between remote ticks and local
time from readings: "the remote clock showed `remote` ticks at local time
`local`". The caller unwraps counters that wrap. Local times may be any
`time.Duration`, negative included.

The nominal tick period in nanoseconds is `Pn = Den × 10^9 / Num` (float64).

### 2.2 Configuration

`DefaultFollowerConfig(nominal)` returns exactly:

| Field | Default |
| --- | --- |
| `Nominal` | the argument |
| `Bandwidth` | 0.1 (Hz) |
| `Damping` | `math.Sqrt2 / 2` |
| `MaxOffsetPPM` | 500 |
| `Window` | 0 |
| `OutlierThreshold` | 5 ms |
| `StepAfter` | 3 |
| `LockThreshold` | 100 µs |
| `LockCount` | 8 |
| `HoldoverAfter` | 5 s |

`NewFollower(cfg)` checks the fields in this order and returns `nil` and a
`*ConfigError{Field, Reason}` for the first that fails:

| Field | Reason (exact text) | Fails when |
| --- | --- | --- |
| `Nominal` | `must be a valid Rate` | numerator or denominator is 0 |
| `Bandwidth` | `must be finite and > 0` | NaN, ±Inf or ≤ 0 |
| `Damping` | `must be finite and > 0` | NaN, ±Inf or ≤ 0 |
| `MaxOffsetPPM` | `must be finite, > 0 and < 1000000` | NaN, ±Inf, ≤ 0 or ≥ 10^6 |
| `Window` | `must be >= 0` | < 0 |
| `OutlierThreshold` | `must be > 0` | ≤ 0 |
| `StepAfter` | `must be >= 1` | < 1 |
| `LockThreshold` | `must be >= 0` | < 0 |
| `LockCount` | `must be >= 1` | < 1 |
| `HoldoverAfter` | `must be > 0` | ≤ 0 |

`ConfigError.Error()` is `"pll: FollowerConfig.<Field> <Reason>"`.

### 2.3 State

The loop holds an **anchor** (remote ticks `R0` at local time `L0`) and a
fractional **rate offset** `y`: the remote clock runs at nominal × (1 + y)
as measured locally. It predicts the local time of remote reading `r` as

    L̂(r) = L0 + (r − R0) × Pn / (1 + y)

Hold `L0` as integer nanoseconds plus a float64 fraction, and rebase it at
every update, so that precision does not depend on how large local time is.
Compute `r − R0` exactly when it fits in `int64`.

The **phase error** of reading `(l, r)` is `e = l − L̂(r)` in nanoseconds,
positive when the reading arrived later than predicted.

### 2.4 Observe

`Observe(local, remote) (Observation, error)`:

1. **Order.** After the first reading since `NewFollower` or `Reset`, a
   reading with `local` earlier than the previous reading's, or `remote` not
   greater than the previous reading's, returns `ErrOrder` and changes
   nothing. Readings may share a local time. Previous means the last reading
   that did not return an error, whatever its status.
2. **Anchoring.** The first reading since `NewFollower` or `Reset` sets
   `L0 = local`, `R0 = remote`, and returns `StatusAnchored` with `Error` 0.
   The rate offset is 0 after `NewFollower` and `Reset`.
3. **Window.** With `Window == 0`, every later reading is fed to the loop
   (2.5) and `Observe` returns the result. With `Window > 0`:
    - If no window is open, the reading opens one starting at its `local`,
      becomes the window's candidate, and returns `StatusPending`.
    - If `local < start + Window`, the reading replaces the candidate when its
      phase error is smaller (strictly), and returns `StatusPending`.
    - Otherwise the candidate is fed to the loop, `Observe` returns that
      result, and then this reading opens the next window as its candidate.
   Phase errors for the window are computed when each reading arrives; the
   loop does not change while a window is open. Delays only make arrivals
   later, so the reading with the smallest error is the least delayed.

A pending reading has `Error` 0. `Observation.Error` is the phase error of
the sample fed to the loop, rounded to the nearest nanosecond.

### 2.5 Feeding a sample

For a sample `(l, r)` with phase error `e`:

1. **Outliers.** If `|e| > OutlierThreshold`, the sample is an outlier. A run
   of consecutive outliers continues when `|e − e_first| ≤ OutlierThreshold`,
   where `e_first` is the error of the run's first outlier; otherwise this
   outlier starts a new run of length 1. When the run reaches `StepAfter`,
   **step**: `L0 = l`, `R0 = r`, keep `y`, clear lock and jitter, end the run,
   and return `StatusStepped`. Otherwise return `StatusRejected` and change
   nothing else. A sample that is not an outlier ends any run.
2. **Loop update.** Let `Δt` be the local seconds from the last anchor, step
   or loop update to `l`. If `Δt > 0`, with `ωn = 2π × Bandwidth`:

        x  = ωn × min(Δt, 1/(2 ωn))
        kp = min(1, 2 × Damping × x)
        y  ← clamp(y − (x² / Δt) × e / 10^9,  ±MaxOffsetPPM × 10^−6)
        L0 ← L̂_old(r) + kp × e,   R0 ← r

   computing `L̂_old(r)` before changing `y`. Capping `Δt` at `1/(2 ωn)`
   keeps the loop stable however sparse the samples; for `Δt ≤ 1/(2 ωn)` these
   are the plain equations `kp = 2ζ ωn Δt`, `Δy = −ωn² Δt e`. If `Δt == 0`,
   the loop does not change.
3. **Lock.** If `|e| ≤ LockThreshold`, add 1 to the lock run (stopping at
   `LockCount`); otherwise set it to 0. The state is `StateLocked` when the run
   has reached `LockCount`, otherwise `StateAcquiring`.
4. **Jitter.** On the first accepted sample since the last anchor, step or
   `Reset`, `m = e²`; on each later one, `m ← m + (e² − m)/16`.
5. Record `l` as the time of the last accepted sample, and return
   `StatusAccepted`.

Anchoring and stepping also count as the last accepted sample for holdover,
and set the state to `StateAcquiring`.

### 2.6 Reading the follower

- `Local(remote)`: `L̂(remote)` rounded to the nearest nanosecond.
  `ErrNotAnchored` before the first reading; `ErrOverflow` if it does not fit.
- `Remote(local)`: `R0 + round((local − L0) × (1 + y) / Pn)`, the inverse.
  Same errors.
- `Offset()`: `y`. `Trim()`: `round(y × 10^12)`, ready for `Pacer.SetTrim`.
- `Jitter()`: `round(√m)` nanoseconds once two samples have been accepted
  since the last anchor, step or `Reset`; 0 before.
- `State(now)`: `StateHoldover` when anchored and `now − last accepted >
  HoldoverAfter`; otherwise the lock state. `StateAcquiring` before anchoring.
- `Reset()`: forget everything, the rate offset and the reading order
  included; the next reading anchors.

`Status` and `State` with their `String` methods are in `status.go`;
`ErrOrder`, `ErrNotAnchored` and `ConfigError` in `follower_errors.go`.

### 2.7 Allocation

`Observe`, `Local`, `Remote`, `Jitter`, `Trim` and `State` must not allocate.

### 2.8 The simulator

`internal/sim` drives the follower through scenarios: remote clocks that are
offset, fast or slow, drifting, changing rate or jumping; delivery with
jitter, heavy-tailed delays, batching and outages; a device that drains a
buffer at its own rate; and a caller that wakes late. `cmd/pllsim` prints the
results. The scenario tests hold the follower to thresholds about twice what a
reference implementation measured.
