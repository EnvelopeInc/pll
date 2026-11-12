You are implementing one small task in the `pll` repository, a Go library.
This is one iteration of an unattended loop. You start with no memory of
earlier iterations; everything you need is in files.

## Do this, in order

1. Read `SPEC.md`, then the last 30 lines of `PROGRESS.md`.
2. Read the task below, the source files it touches (`rate.go`, `pacer.go`,
   `span.go`), and the test named in the task's test command. The test is
   the definition of done. `model_test.go` holds the reference model the
   proof tests compare against.
3. Implement only what this task needs. Keep every exported name, signature
   and doc comment as it is. You may add unexported fields, helpers and
   files. Code that already passes its tests stays as it is unless this task
   requires changing it.
4. Run the task's test command. Fix and repeat until it passes.
5. Run `gofmt -l .` and `go vet ./...`. Both must be clean.
6. Run `go test ./... -count=1` and compare with what failed before: you
   must not break a test that was passing. Tests for later tasks are
   expected to fail.
7. Append an entry of at most three lines to `PROGRESS.md`: what you did,
   what the next iteration should know, and anything in SPEC.md or a test
   that looks wrong.
8. Stop. Do not start the next task.

## Rules

- The test decides. If a test and SPEC.md disagree, make the test pass and
  record the disagreement in PROGRESS.md.
- Never edit: any `*_test.go` file, `doc.go`, `errors.go`, `SPEC.md`,
  `tasks.json`, `PROMPT.md`, `loop.sh`, `Makefile`, `go.mod`, `README.md`,
  `LICENSE`, `.gitignore`, or anything under `pi/` or `.github/`. The loop
  reverts such edits and counts the iteration as failed. Do not add test
  files of your own.
- Standard library only. Do not add dependencies.
- Do not run `git commit`, `git reset`, `git checkout`, `git restore`,
  `git stash`, `git clean` or `git push`. The loop commits for you when the
  test passes.
- Do not write code whose only purpose is to satisfy the test's literal
  inputs, such as returning fixed values for the test's cases. Implement
  the behavior SPEC.md and the doc comments describe.
- If you cannot make the test pass, leave the code compiling, say exactly
  what blocked you in PROGRESS.md, and stop. A clear report of failure is
  more useful than a guess.
- Say only what you did. Do not report tests as passing unless you ran them
  after your last edit and saw them pass.
