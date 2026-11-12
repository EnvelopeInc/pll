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
13: pacer.go: SetTrim validates bounds, returns early for unchanged trim, checks floor(cursor time) before replacing the immutable segment, and uses the retained nominal Rate; issued spans stay unchanged. TestSetTrim, gofmt, vet and full suite pass.
Next: task 13 complete; no previously passing tests regressed (20 passing/17 failing test/subtest results -> 37 passing/0 failing); baseline/final logs and comparison: /tmp/pll-task13.Gq52FQ.
SPEC.md and tests: no disagreement found for this task.
21: follower.go: DefaultFollowerConfig returns the SPEC 2.2 defaults (adds math import); TestDefaultFollowerConfig passes; gofmt, vet clean; full suite 39 pass/30 fail -> 40 pass/29 fail, no regressions.
Next: NewFollower validation (2.2 table, first failing field in order, *ConfigError); Nominal is stored unchecked here, so NewFollower must check it with Rate.Num/Den.
SPEC.md and follower_test.go: no disagreement found for this task.
22: follower.go: NewFollower validates via FollowerConfig.validate in SPEC 2.2 order (*ConfigError); Follower now holds cfg, pn, wn and unexported loop/order/window/outlier/lock/jitter state fields (SPEC 2.3-2.5); other methods still stubs. TestNewFollowerValidation passes; gofmt, vet clean; full suite 40 pass/29 fail -> 41 pass/28 fail, no pass->fail (baseline from HEAD follower.go in /tmp/base22).
Next: Observe/Local/Remote (task 23+) should use the anchor fields l0+l0frac, r0, y, ref, hasPrev/prevLocal/prevRemote; the struct fields are a proposal, rename or drop any the later tasks do not need.
SPEC.md and follower_test.go: no disagreement found for this task.
23: follower.go: Observe anchors on the first reading (anchor helper sets l0/r0/y=0/ref/lastAcc, returns StatusAnchored); Local/Remote implemented per 2.6 with ErrNotAnchored and ErrOverflow, plus subInt64/addInt64/addNanos helpers. TestFollowerAnchors and TestRemoteInverse pass; gofmt, vet clean; no pass->fail vs HEAD (baseline /tmp/base23; 43 PASS lines now).
Next: Observe still returns Observation{}, nil for readings after the anchor (the ordering, window and loop paths are unbuilt); Reset is still a stub, so task 36 must clear all fields including hasPrev and anchored.
SPEC.md and follower_test.go: no disagreement found for this task.
24: follower.go: Observe checks order against the last successful reading (ErrOrder, no state change); the anchor path is now restart()-based; the sample path implements SPEC 2.5 in full (outlier run and step, loop update with capped gains, lock, jitter, lastAcc) via feed/accept/phaseError/predictFrom; hasPrev field dropped. TestFollowerOrder passes; gofmt, vet clean; no pass->fail vs pre-edit copy (43 -> 52 PASS; also newly pass TestFollowerInvariants, LargeTimes, LongRun, FuzzFollower).
Next: Window (2.4.3) is unbuilt, so Window>0 currently feeds every reading; Offset/Trim/Jitter/State are still stubs, which is why TestPerfectClock, TestLoopEquations, TestSteps, TestOutliers fail. Observe can now return ErrOverflow (accept, when a new L0 does not fit); SPEC 2.4 lists only ErrOrder, so record that as a contract gap.
SPEC.md and follower_test.go: no disagreement found for this task beyond that ErrOverflow gap.
