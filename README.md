# pll — stage 2 build kit

Exact pacing for Go, and now a phase-locked loop that follows a clock other
than your own. This repository is the stage 2 build kit, applied on top of
the stage 1 build. The binding behavior is in `SPEC.md`, part 2. The
`Follower` is stubs: 26 new tests fail on purpose, and `loop.sh` drives a
coding agent through them. This README is replaced by the library README
before release.

## What the overlay adds

| Path | What it is | Written by |
| --- | --- | --- |
| `SPEC.md` | Part 1 unchanged; part 2 is the follower | strong model |
| `follower_errors.go`, `status.go`, `doc.go` | Errors, `Status` and `State`, package doc; complete | strong model |
| `follower.go` | Exported API and doc comments; bodies are stubs | the loop |
| `internal/sim/` | Deterministic simulator: remote clocks, delivery, a device sink, late wakeups; complete | strong model |
| `cmd/pllsim/` | Prints what every scenario measures; complete | strong model |
| `follower*_test.go`, `example_follower_test.go` | Unit tests, a literal loop model, a differential test, invariants, a fuzz target, scenario thresholds, allocation checks | strong model |
| `testdata/fuzz/FuzzFollower/` | A regression seed the fuzzer found in the reference | strong model |
| `tasks.json` | Stage 1's 20 tasks, 26 new ones, the whole-tree check last | strong model |
| `PROMPT.md`, `loop.sh`, `pi/loop-guard.ts`, `Makefile`, CI | Updated for the new protected paths and the second fuzz target | strong model |

## Apply it

```sh
cd ~/Development/pll
git switch main && git pull --ff-only
git switch -c loop2
rm -rf .loop                     # stage 1's loop state
cd .. && unzip -o ~/Downloads/pll-stage2-kit.zip && cd pll
git add -A && git commit -m "pll: stage 2 build kit"
go test ./... 2>&1 | tail -3     # fails: the follower is stubs
```

## Run the loop

```sh
./loop.sh
```

The model commands, caps and escalation are as in stage 1. The loop starts
at task 21; tasks 1–20 already pass and are re-checked after every iteration.

## After the loop

```sh
make verify && make fuzz && make bench
make sim                         # the scenario table
git push -u origin loop2
```

Then send `make sim` output and the end of `.loop/loop.log` back for review
before opening the pull request.
