# pll specification

This file is binding for the build loop. Where it and a test disagree, the
test decides; record the disagreement in PROGRESS.md.

The design document behind it lives outside the repository. This file covers
**stage 1**: `Rate`, `Pacer` and `Span`. Stage 2 adds the `Follower` in new
files and a new numbered part.

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
| `doc.go`, `errors.go` | Complete; do not change |
| `rate.go`, `pacer.go`, `span.go` | Exported API fixed; bodies and unexported fields are yours |
| `*_test.go` | The definition of done; do not change |

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
