# pll — stage 1 build kit

Exact pacing for Go: divide time into evenly spaced, numbered slots at an
exact rate and say which are due. This repository is the stage 1 build kit.
The binding behavior is in `SPEC.md`. The code is stubs: 21 tests fail on
purpose, and `loop.sh` drives a coding agent through `tasks.json` until they
pass. This README is replaced by the library README before release.

## What is here

| Path | What it is | Written by |
| --- | --- | --- |
| `SPEC.md` | Binding behavior for stage 1: `Rate`, `Pacer`, `Span` | strong model |
| `doc.go`, `errors.go` | Package doc and errors, complete | strong model |
| `rate.go`, `pacer.go`, `span.go` | Exported API and doc comments; bodies are stubs | the loop |
| `*_test.go` | The definition of done, including a naive `big.Rat` reference model, a randomized differential test, a fuzz target and allocation checks | strong model |
| `tasks.json` | 21 tasks in order, each with one test command | strong model |
| `PROMPT.md` | What the agent is told every iteration | strong model |
| `loop.sh` | The loop: picks the task, runs the agent, judges, commits | strong model |
| `pi/loop-guard.ts` | Pi extension that blocks edits to the files above during the loop | strong model |
| `Makefile` | `verify`, `fuzz`, `bench` | strong model |
| `.github/workflows/ci.yml` | Tests on Go 1.23 and stable with the race detector; 30 s of fuzzing | strong model |

## Apply it

The kit replaces every file in the repository; the old code goes. Your
checkout is assumed to be `~/Development/pll`; adjust the paths if not.

```sh
cd ~/Development/pll
git config user.name "M. Robert Martin"
git remote set-url origin git@github.com:version2beta/pll.git
git switch main && git pull --ff-only
git switch -c loop
git rm -rq .
cd .. && unzip -o ~/Downloads/pll-stage1-kit.zip && cd pll
git add -A && git commit -m "pll: stage 1 build kit"
go test ./... 2>&1 | tail -3     # fails: that is the starting point
```

## Run the loop

```sh
pi --help                        # check the flags used in loop.sh
./loop.sh
```

Before the first unattended run:

1. Check the three agent command lines at the top of `loop.sh` against your
   Pi: the print flag, `--provider`, `--model`, `--thinking`, `-e`, and the
   model ids `claude-haiku-5-5` and `gpt-6.1-sol`. Override them with the
   `CODER`, `ESCALATE` and `ESCALATE_MAX` environment variables.
2. Run it in a container that holds only the model keys. The last task runs
   the race detector, which needs cgo and a C compiler; the official Go
   images have both.
3. Set spend limits at the providers. The loop caps iterations
   (`MAX_ITER`, default 60), attempts per task (`MAX_ATTEMPTS`, default 4)
   and time per iteration (`ITER_TIMEOUT`, default 20m), not dollars.

How it escalates: a task's first attempt goes to `CODER`. Any later attempt,
and every attempt at a task marked `"tier": "hard"`, goes to `ESCALATE`. The
last attempt goes to `ESCALATE_MAX`. After `MAX_ATTEMPTS` failures on one
task the loop writes a `STOP` file naming it and exits.

How it judges: the agent never marks its own work. After each iteration the
loop reverts any change to a protected path, runs the task's test, every
earlier task's test and `go vet`, and commits only if all pass. When every
task passes it runs `make verify`.

## After the loop

```sh
git log --oneline main..loop     # one commit per task, plus failed-attempt notes
tail -40 PROGRESS.md
make verify && make fuzz && make bench
```

Send the diff and `PROGRESS.md` back for review before merging into `main`.
Stage 2 (the `Follower`, the simulator and the link) arrives as an overlay on
top of this build.
