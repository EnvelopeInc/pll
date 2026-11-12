# Changelog

One entry per released version, newest first.

## v0.3.1 — 2026-11-12

Documentation only. The README now says that `Span.Time` reports a slot's scheduled time rounded down to whole nanoseconds, as its reference section always did.

## v0.3.0 — 2026-11-12

The first release under `github.com/version2beta/pll`. Version numbers below v0.3.0 belong to an earlier, withdrawn API and do not build under this module path.

- `Rate`: an exact number of units per second, from `UnitsPerSecond` or `BitsPerSecond`.
- `Pacer`: evenly spaced, numbered slots at an exact rate, with `Due`, `Claim`, `Skip`, `NextDue` and a trim in parts per trillion. Times never drift and the hot paths do not allocate.
- `Span`: a run of handed-out slots with their exact times, and an iterator over them.
- `Follower`: a type-II phase-locked loop configured by bandwidth and damping, with a lower-envelope window for jittery delivery, outlier rejection, steps when the other clock jumps, lock and holdover states, and a running jitter estimate. Its `Trim` drives a `Pacer`.
- `cmd/pllsim`: a deterministic simulator that runs the follower against realistic clocks and prints what it measured.
