#!/usr/bin/env bash
# The build loop. Each iteration: find the first task whose test fails, hand
# that one task to a coding agent in a fresh context, then decide for
# ourselves whether it is done by running the test. The agent never marks
# its own work.
#
# Run it on a branch, in a container that holds nothing but the model keys:
#
#   git switch -c loop && ./loop.sh
#
# Stop it by creating a file named STOP. It also stops by itself when every
# task passes, when one task has failed MAX_ATTEMPTS times, or after
# MAX_ITER iterations.
#
# Models. Attempt 1 goes to CODER. Every later attempt, and any task marked
# "tier": "hard", goes to ESCALATE. The last attempt goes to ESCALATE_MAX.
# Check these command lines against `pi --help` and `pi --list-models`
# before the first run; the flags and model ids are the part of this kit
# that was not tested.

set -u
cd "$(dirname "$0")"

MAX_ITER=${MAX_ITER:-60}
MAX_ATTEMPTS=${MAX_ATTEMPTS:-4}
ITER_TIMEOUT=${ITER_TIMEOUT:-20m}
GUARD=${GUARD:-"-e pi/loop-guard.ts"}

CODER=${CODER:-"pi -p --provider anthropic --model claude-haiku-5-5 --thinking high $GUARD"}
ESCALATE=${ESCALATE:-"pi -p --provider openai-codex --model gpt-6.1-sol --thinking high $GUARD"}
ESCALATE_MAX=${ESCALATE_MAX:-"pi -p --provider openai-codex --model gpt-6.1-sol --thinking max $GUARD"}

# Paths the agent must not change. Anything matching is reverted.
PROTECTED='(_test\.go$|^pi/|^\.github/|^SPEC\.md$|^tasks\.json$|^PROMPT\.md$|^loop\.sh$|^Makefile$|^go\.mod$|^go\.sum$|^doc\.go$|^errors\.go$|^README\.md$|^LICENSE$|^\.gitignore$)'

STATE=.loop
mkdir -p "$STATE/attempts" "$STATE/log"

say() { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "$STATE/loop.log"; }

for tool in jq git go timeout; do
  command -v "$tool" >/dev/null || { echo "loop.sh needs $tool"; exit 2; }
done
git rev-parse --git-dir >/dev/null 2>&1 || { echo "not a git repository"; exit 2; }
if [ -n "$(git status --porcelain)" ]; then
  echo "working tree is not clean; commit or stash first"; exit 2
fi
case "$(git branch --show-current)" in
  main|master) echo "refusing to run on $(git branch --show-current); use a branch"; exit 2 ;;
esac

ntasks=$(jq '.tasks | length' tasks.json)

# run_test <index>: run one task's test; output to $STATE/test.out.
run_test() {
  local cmd
  cmd=$(jq -r ".tasks[$1].test" tasks.json)
  timeout 5m bash -c "$cmd" >"$STATE/test.out" 2>&1
}

# first_failing: index of the first task whose test fails, or empty.
first_failing() {
  local i
  for ((i = 0; i < ntasks; i++)); do
    run_test "$i" || { echo "$i"; return; }
  done
}

revert_all() {
  git checkout -q -- . 2>/dev/null
  git clean -fdq 2>/dev/null
}

iter=0
while [ "$iter" -lt "$MAX_ITER" ]; do
  [ -e STOP ] && { say "STOP file found; stopping"; exit 0; }
  iter=$((iter + 1))

  idx=$(first_failing)
  if [ -z "$idx" ]; then
    say "every task passes"
    make verify >"$STATE/verify.out" 2>&1 && say "make verify: ok" || say "make verify FAILED, see $STATE/verify.out"
    exit 0
  fi
  run_test "$idx"; cp "$STATE/test.out" "$STATE/before.out"

  id=$(jq -r ".tasks[$idx].id" tasks.json)
  title=$(jq -r ".tasks[$idx].title" tasks.json)
  tier=$(jq -r ".tasks[$idx].tier" tasks.json)
  attempts=$(cat "$STATE/attempts/$id" 2>/dev/null || echo 0)

  if [ "$attempts" -ge "$MAX_ATTEMPTS" ]; then
    say "task $id failed $attempts times; stopping for a human"
    echo "stuck on task $id: $title" >STOP
    exit 3
  fi

  if [ "$attempts" -ge $((MAX_ATTEMPTS - 1)) ]; then
    agent=$ESCALATE_MAX; who=escalate-max
  elif [ "$attempts" -ge 1 ] || [ "$tier" = hard ]; then
    agent=$ESCALATE; who=escalate
  else
    agent=$CODER; who=coder
  fi

  {
    cat PROMPT.md
    printf '\n## The task\n\n```json\n'
    jq ".tasks[$idx]" tasks.json
    printf '```\n\n## The test currently fails like this\n\n```text\n'
    tail -n 60 "$STATE/before.out"
    printf '```\n'
    if [ "$attempts" -gt 0 ] && [ -s "$STATE/last-attempt.diff" ]; then
      printf '\n## A previous attempt at this task failed\n\n'
      printf 'It was discarded. This is what it changed and how the test failed afterwards. Do not repeat it.\n\n```diff\n'
      head -n 300 "$STATE/last-attempt.diff"
      printf '```\n\n```text\n'
      tail -n 60 "$STATE/last-attempt.out"
      printf '```\n'
    fi
  } >"$STATE/prompt.txt"

  say "iter $iter: task $id ($title), attempt $((attempts + 1)), $who"
  log="$STATE/log/$(printf '%03d' "$iter")-task$id-$who.log"
  # shellcheck disable=SC2086
  timeout "$ITER_TIMEOUT" $agent "$(cat "$STATE/prompt.txt")" >"$log" 2>&1
  rc=$?
  [ "$rc" -eq 124 ] && say "  agent timed out after $ITER_TIMEOUT"

  # Guard: undo any change to a protected path.
  violated=$(git status --porcelain -uall | cut -c4- | sed 's/.* -> //' | grep -E "$PROTECTED" || true)
  if [ -n "$violated" ]; then
    say "  protected paths were changed and have been reverted:"
    printf '%s\n' "$violated" | sed 's/^/      /' | tee -a "$STATE/loop.log"
    printf '%s\n' "$violated" | while IFS= read -r p; do
      git checkout -q -- "$p" 2>/dev/null || rm -f -- "$p"
    done
  fi

  # Judge: the task's own test, every earlier task, and a clean vet.
  ok=1
  run_test "$idx" || ok=0
  cp "$STATE/test.out" "$STATE/after.out"
  if [ "$ok" -eq 1 ]; then
    for ((j = 0; j < idx; j++)); do
      run_test "$j" || { ok=0; say "  broke earlier task $(jq -r ".tasks[$j].id" tasks.json)"; cp "$STATE/test.out" "$STATE/after.out"; break; }
    done
  fi
  if [ "$ok" -eq 1 ] && ! go vet ./... >"$STATE/vet.out" 2>&1; then
    ok=0; say "  go vet failed"; cp "$STATE/vet.out" "$STATE/after.out"
  fi
  [ -n "$violated" ] && ok=0

  if [ "$ok" -eq 1 ]; then
    changed_go=$(git ls-files -m -o --exclude-standard -- '*.go')
    # shellcheck disable=SC2086
    [ -n "$changed_go" ] && gofmt -w $changed_go 2>/dev/null
    git add -A
    git commit -qm "task $id: $title" -m "loop iteration $iter, $who"
    rm -f "$STATE/attempts/$id" "$STATE/last-attempt.diff" "$STATE/last-attempt.out"
    say "  passed and committed"
  else
    git add -A -N . 2>/dev/null
    git diff -- . ':!PROGRESS.md' >"$STATE/last-attempt.diff"
    cp "$STATE/after.out" "$STATE/last-attempt.out"
    note=$(git diff -- PROGRESS.md | grep '^+[^+]' | cut -c2- || true)
    git reset -q
    revert_all
    if [ -n "$note" ]; then
      # Keep what the agent said about why it failed.
      printf '%s\n' "$note" >>PROGRESS.md
      git add PROGRESS.md && git commit -qm "task $id: failed attempt $((attempts + 1)) notes"
    fi
    echo $((attempts + 1)) >"$STATE/attempts/$id"
    say "  failed; changes discarded"
  fi
done

say "reached MAX_ITER=$MAX_ITER"
exit 4
