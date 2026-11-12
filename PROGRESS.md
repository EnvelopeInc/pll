# Progress

One entry per loop iteration, newest last. Three lines at most: what you
did, what you learned that the next iteration needs, anything in SPEC.md or
a test that looks wrong.

01: rate.go: UnitsPerSecond validates and reduces by gcd; BitsPerSecond delegates; Num/Den/String implemented. TestUnitsPerSecond, TestBitsPerSecond, TestRateString pass; gofmt and vet clean.
Next: pacer.go and span.go are still stubs (all Pacer/Span/Example/Fuzz/Alloc tests fail as expected). Rate has unexported gcd(a,b) in rate.go for reuse.
SPEC.md and rate_test.go: no disagreement found for this task.
