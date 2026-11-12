# Progress

One entry per loop iteration, newest last. Three lines at most: what you
did, what you learned that the next iteration needs, anything in SPEC.md or
a test that looks wrong.

01: rate.go: UnitsPerSecond validates and reduces by gcd; BitsPerSecond delegates; Num/Den/String implemented. TestUnitsPerSecond, TestBitsPerSecond, TestRateString pass; gofmt and vet clean.
Next: pacer.go and span.go are still stubs (all Pacer/Span/Example/Fuzz/Alloc tests fail as expected). Rate has unexported gcd(a,b) in rate.go for reuse.
SPEC.md and rate_test.go: no disagreement found for this task.
04: pacer.go: Pacer{cursor, seg *segment}; segment holds start S, anchor A, trim q and reduced period pn/pd (uint64 fast path, math/big fallback), built once by newSegment. NewPacer and Trim implemented; TestNewPacer passes; gofmt, vet clean; TestUnitsPerSecond, TestBitsPerSecond, TestRateString still pass; other tests fail as expected (stubs).
Next: Span needs an unexported *segment and start slot logic; SetTrim must call newSegment with A=floor(T_old(cursor)). Segment time evaluation is not yet exercised by any test.
SPEC.md and pacer_test.go: no disagreement found for this task.
05: pacer.go: Claim implemented (n=0 and cursor+n overflow checks; last slot's time checked via new segment.floorTime, fast uint64 path + big fallback). span.go: Span gained unexported seg *segment. TestClaim passes; gofmt, vet clean; TestUnitsPerSecond/TestBitsPerSecond/TestRateString/TestNewPacer still pass.
Next: Span.Time (task 06) should use segment.floorTime(First+i) and panic when i >= Count. ExamplePacer_Claim fails only because Span.Time is still a stub returning 0.
SPEC.md and pacer_test.go: no disagreement found for this task.
06: span.go: Time checks i >= Count, uses the captured segment.floorTime(First+i), and panics on overflow; TestSpanTimeNominal, TestSpanTimePanics, TestClaim and ExamplePacer_Claim pass; gofmt and vet clean.
Next: All/Due/Skip/NextDue/SetTrim remain stubs; full-suite failing test/subtest names fell from 31 to 28 with no new failures (baseline and after logs: /tmp/pll-task06.jYEwvh).
SPEC.md and tests: no disagreement found for this task.
08: span.go: All yields (First+i, Time(i)) for i < Count and stops when yield returns false; TestSpanAll and ExampleSpan_All pass; gofmt, vet clean; TestUnitsPerSecond, TestBitsPerSecond, TestRateString, TestNewPacer, TestClaim, TestSpanTime* still pass; remaining failures are only Due/Skip/NextDue/SetTrim-dependent tests (stubs).
Next: Due, Skip, NextDue and SetTrim are still stubs; they need the largest-due-slot helper (segment.floorTime's inverse) and the ceil-time computation.
SPEC.md and pacer_test.go: no disagreement found for this task.
09: pacer.go: NextDue returns cursor and ceil(T(cursor)) via new segment.ceilTime (uint64 fast path with bits.Div64 plus remainder round-up, big.Int fallback); ErrOverflow at MaxUint64 or unfit time. TestNextDue passes; gofmt, vet clean; other tests still fail only in Due/SetTrim/Skip stubs (TestOverflow, TestBoundaries, TestDifferential, TestDue*, TestSkip, TestSetTrim*, Fuzz, Alloc, Example*).
Next: Due (task 10) should reuse the largest-due-slot logic: S + floor((now-A)*pd/pn) capped at MaxUint64; ceilTime and floorTime are the model for the new helper.
SPEC.md and pacer_test.go: no disagreement found for this task.
10: pacer.go: Due implemented via new segment.latestDue (largest due slot = start + floor((now-anchor)*pd/pn), uint64 fast path with bits.Div64, big.Int fallback, capped at MaxUint64); overflow when cursor is MaxUint64 and due. TestDue, TestDueLimits, ExamplePacer_Due pass; gofmt, vet clean; no previously passing test regressed (12 -> 18 passing).
Next: Skip (task 11) can reuse latestDue: D = latest - cursor + 1 when latest >= cursor; Skip/SetTrim/Overflow/Boundaries/Differential/Fuzz still fail as stubs.
SPEC.md and pacer_test.go: no disagreement found for this task.
12: pacer.go: Skip implemented, reusing segment.latestDue with the same MaxUint64-1 cap as Due; keeps newest `keep` due slots; ErrInvalidArgument for now<0, ErrOverflow when cursor is MaxUint64 and due. TestSkip passes; gofmt, vet clean; no regressions (only SetTrim/Overflow/Boundaries/Differential/Fuzz/Alloc/Example failures remain).
Next: SetTrim (task 13) is still a stub and is the cause of TestSetTrim, TestSetTrimKeepsSpans, ExamplePacer_SetTrim, and the SetTrim-based TestBoundaries/TestOverflow failures.
SPEC.md and pacer_test.go: no disagreement found for this task.
