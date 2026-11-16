#!/bin/bash
# One local entry point for every Vani test gate, and what scripts/release.sh calls. Hosted CI
# (.github/workflows/fresh-install.yml) runs only the go suite, from a fresh clone set up by
# scripts/bootstrap.sh, on Linux and macOS; the Swift suite needs the owner's signing identity, so swift,
# bun and types run here, on this Mac.
#
#   scripts/test.sh [--suite go,swift,bun,types] [--bg] [--strict] [--fresh] [--race] [--keep N]
#   scripts/test.sh status          # summary of the newest run (works while it is still going)
#
# Suites run concurrently, each logging to <run>/<suite>.log; the summary prints one line per suite:
#   pass | FAIL | pass (N SKIPPED: why) -- a skipped check never reads as a pass; --strict makes it a failure.
#   go     go test ./... (cmd/vani, the long pole, split into shards; cached unless --fresh)
#   swift  build once, then every test except the three window-driving classes in parallel, then those
#          three alone, serially. Their host window is transparent and never frontmost (JobsTabHost).
#   bun    bun test --parallel over extension/test, then a serial re-run of any file that failed
#   types  extension/scripts/typecheck.sh (bun test does not type-check)
#
# --bg runs the whole thing detached at background priority (taskpolicy -b: throttled CPU and disk, so the
#   machine stays responsive), returns at once, and never posts a notification or opens a window.
#   Read it with `scripts/test.sh status`; the exit code of the run is in <run>/exit.
#
# Logs: tmp/test-runs/<timestamp>/ (gitignored); `tmp/test-runs/latest` points at the newest; --keep N
# (default 5) prunes older runs. Exit status: 0 only when every selected suite passed (and, with --strict,
# none skipped).
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RUNS="${VANI_TEST_RUNS:-$ROOT/tmp/test-runs}"
export VANI_NO_KEYCHAIN=1   # a `vani` that builds a decision backend would otherwise read the owner's key

SUITES="go,swift,bun,types"
BG=0
STRICT=0
RACE=0
FRESH=0
KEEP=5
ACTION=run
RUN_DIR=""

die() { echo "test.sh: $*" >&2; exit 2; }

while [ $# -gt 0 ]; do
  case "$1" in
    status) ACTION=status ;;
    --suite) shift; [ $# -gt 0 ] || die "--suite needs a list"; SUITES="$1" ;;
    --bg) BG=1 ;;
    --strict) STRICT=1 ;;
    --race) RACE=1 ;;
    --fresh) FRESH=1 ;;
    --keep) shift; [ $# -gt 0 ] || die "--keep needs a number"; KEEP="$1" ;;
    --run-dir) shift; RUN_DIR="$1" ;;   # internal: the detached child of --bg
    *) die "unknown argument $1 (usage: scripts/test.sh [status] [--suite go,swift,bun,types] [--bg] [--strict] [--race] [--keep N])" ;;
  esac
  shift
done
case "$KEEP" in ''|*[!0-9]*) die "--keep needs a number" ;; esac
for s in ${SUITES//,/ }; do
  case "$s" in go|swift|bun|types) ;; *) die "unknown suite '$s' (go, swift, bun, types)" ;; esac
done

summary() { # print <run>/<suite>.result lines in a fixed order
  local d="$1" s
  for s in go swift bun types; do
    [ -f "$d/$s.result" ] && printf '%-6s %s\n' "$s" "$(cat "$d/$s.result")"
  done
}

if [ "$ACTION" = status ]; then
  [ -e "$RUNS/latest" ] || die "no run yet under $RUNS"
  d="$(cd "$RUNS/latest" && pwd -P)"
  echo "run: $d"
  summary "$d"
  if [ -f "$d/exit" ]; then echo "finished, exit $(cat "$d/exit")"; else echo "still running (or interrupted: no exit file)"; fi
  exit 0
fi

mkdir -p "$RUNS"
if [ -z "$RUN_DIR" ]; then
  RUN_DIR="$RUNS/$(date +%Y%m%d-%H%M%S)-$$"
  mkdir -p "$RUN_DIR"
  ln -sfn "$RUN_DIR" "$RUNS/latest"
  # prune: keep the newest $KEEP run directories
  # (newest first by modification time: two runs in one second share a name prefix, so names do not order them)
  ls -1dt "$RUNS"/[0-9]*/ 2>/dev/null | tail -n +"$((KEEP + 1))" | while read -r old; do rm -rf "$old"; done
  if [ "$BG" = 1 ]; then
    args=(--suite "$SUITES" --keep "$KEEP" --run-dir "$RUN_DIR")
    [ "$STRICT" = 1 ] && args+=(--strict)
    [ "$RACE" = 1 ] && args+=(--race)
    [ "$FRESH" = 1 ] && args+=(--fresh)
    nohup taskpolicy -b "$0" "${args[@]}" > "$RUN_DIR/driver.log" 2>&1 < /dev/null &
    echo "running in the background (pid $!), logs in $RUN_DIR"
    echo "check with: $ROOT/scripts/test.sh status"
    exit 0
  fi
fi
mkdir -p "$RUN_DIR"

now() { date +%s; }

# finish <suite> <status text> <exit 0|1> <start>
finish() {
  echo "$2 ($(( $(now) - $4 )) s)" > "$RUN_DIR/$1.result"
  echo "$3" > "$RUN_DIR/$1.rc"
}

run_types() {
  local t0; t0=$(now)
  if sh "$ROOT/extension/scripts/typecheck.sh" > "$RUN_DIR/types.log" 2>&1; then
    finish types pass 0 "$t0"
  else
    finish types "FAIL (see types.log)" 1 "$t0"
  fi
}

run_bun() {
  local t0 log="$RUN_DIR/bun.log" ext="$ROOT/extension"; t0=$(now)
  bun test --parallel --cwd "$ext" > "$log" 2>&1
  local rc=$?
  if [ $rc -ne 0 ]; then
    # Files that failed under N workers get one serial re-run: a failure there is real, one that vanishes is
    # a load-dependent timing edge, and both are reported (never retried into a pass in silence).
    local failed; failed=$(grep -E '^\(fail\)' "$log" | wc -l | tr -d ' ')
    local files; files=$(grep -E '^test/.*\.ts:$' "$log" | tr -d ':' | sort -u)
    echo "--- serial re-run of files with output ---" >> "$log"
    bun test --cwd "$ext" $files >> "$log" 2>&1 && \
      { finish bun "pass after serial re-run: $failed failure(s) under --parallel did not reproduce serially (load-dependent; see bun.log)" 0 "$t0"; return; }
    finish bun "FAIL ($failed failing under --parallel; serial re-run also failed; see bun.log)" 1 "$t0"
    return
  fi
  local n; n=$(grep -E '^Ran [0-9]+ tests' "$log" | head -1)
  finish bun "pass ($n)" 0 "$t0"
}

# `swift test --parallel` exits non-zero on a failure but prints a skipped test exactly like a passed one (checked
# with a planted XCTSkip: neither stdout nor --xunit-output marks it), so it cannot be allowed to run a test that
# may skip: a contract test that found no Go vani would read as green. Those classes -- every XCTestCase class in a
# file that throws XCTSkip, plus the window-driving ones (they skip unless the runner is trusted for
# accessibility) -- run in a second, serial pass that prints each skip, and the summary counts them.
WINDOW_TESTS='JobsGapsPanelTests|JobsControlsTests|JobsBackToTopTests|JobsTabInvisibleTests|JobsTabHost'

skip_capable_classes() { # <Tests dir>: the window classes plus every XCTestCase class in a file with XCTSkip
  { echo "$WINDOW_TESTS" | tr '|' '\n'
    grep -rl 'XCTSkip' "$1" --include='*.swift' | while read -r f; do
      grep -hoE 'class [A-Za-z0-9_]+ *: *XCTestCase' "$f" | sed -E 's/class ([A-Za-z0-9_]+).*/\1/'
    done
  } | sort -u | paste -sd'|' -
}

run_swift() {
  local t0 pkg="$ROOT/macos" log="$RUN_DIR/swift.log"; t0=$(now)
  swift build --package-path "$pkg" --build-tests > "$RUN_DIR/swift-build.log" 2>&1 \
    || { finish swift "FAIL (build; see swift-build.log)" 1 "$t0"; return; }
  local serial; serial=$(skip_capable_classes "$pkg/Tests")
  swift test --skip-build --parallel --package-path "$pkg" --skip "$serial" > "$log" 2>&1
  local rc1=$?
  swift test --skip-build --package-path "$pkg" --filter "$serial" > "$RUN_DIR/swift-serial.log" 2>&1
  local rc2=$?
  local skipped; skipped=$(grep -c "' skipped (" "$RUN_DIR/swift-serial.log")
  local ran; ran=$(grep -Eo 'Executed [0-9]+ tests' "$RUN_DIR/swift-serial.log" | tail -1)
  # each distinct skip reason, with its count, goes to swift-skips.txt and the summary
  grep -o 'Test skipped - .*' "$RUN_DIR/swift-serial.log" | sed -E 's/^Test skipped - //' | cut -c1-110 | sort | uniq -c | sort -rn > "$RUN_DIR/swift-skips.txt"
  if [ $rc1 -ne 0 ] || [ $rc2 -ne 0 ]; then
    finish swift "FAIL (parallel rc=$rc1, serial rc=$rc2; see swift.log, swift-serial.log)" 1 "$t0"
  elif [ "$skipped" -gt 0 ]; then
    finish swift "pass, but ${skipped} tests SKIPPED, not run (reasons in swift-skips.txt: $(head -3 "$RUN_DIR/swift-skips.txt" | sed -E 's/^ *([0-9]+) /\1x /' | paste -sd';' -))" 0 "$t0"
    echo "$skipped" > "$RUN_DIR/swift.skipped"
  else
    finish swift "pass (${ran:-serial pass ran})" 0 "$t0"
  fi
}

# cmd/vani holds ~1,800 tests that run one after another in a single process (80+ s on its own, over half of
# `go test ./...`'s wall time). Its compiled test binary is split into GO_SHARDS processes by test name; the
# tests keep their t.TempDir()/t.Setenv isolation, so processes do not share state.
#
# Each shard is its own `go test -run`, so Go's result cache applies per shard and a re-run with unchanged code
# replays in seconds. Concurrency is capped on purpose: past ~8 packages at once Manvi's locked-memory credential
# store panics ("could not acquire lock ... cannot allocate memory", seen at -p 18), a load-dependent failure
# that unbounded parallelism makes more likely. --fresh adds -count=1 (no cache; scripts/release.sh uses it).
GO_SHARDS="${VANI_TEST_GO_SHARDS:-4}"
GO_P="${VANI_TEST_GO_P:-6}"
GO_NESTED="${VANI_TEST_GO_NESTED:-decide rkb}"   # nested modules tested on their own (see run_go); the variable is a test seam

run_go() {
  local t0 log="$RUN_DIR/go.log"; t0=$(now)
  local flags=(-timeout 15m)   # never empty: bash 3.2 under set -u treats an empty array as unbound
  [ "$FRESH" = 1 ] && flags+=(-count=1)
  [ "$RACE" = 1 ] && flags+=(-race)
  # decide lives in this repo. A go.work that still `use`s an outside copy wins over go.mod's replace, so
  # the root module would be tested against that copy while the nested run below tests this one.
  local decide_dir
  if ! decide_dir=$(go -C "$ROOT" list -m -f '{{.Dir}}' github.com/bharathvbcr/decide 2>&1); then
    echo "go list could not resolve decide: $decide_dir" > "$log"
    finish go "FAIL (decide does not resolve; see go.log)" 1 "$t0"; return
  fi
  if [ "$(cd "$decide_dir" 2>/dev/null && pwd -P)" != "$(cd "$ROOT/decide" && pwd -P)" ]; then
    echo "decide resolves to $decide_dir, not $ROOT/decide: in go.work, replace the use line for that directory with ./decide" > "$log"
    finish go "FAIL (decide resolves outside this repo; see go.log)" 1 "$t0"; return
  fi
  local others; others=$(go -C "$ROOT" list ./... | grep -v '/cmd/vani$')
  # shellcheck disable=SC2086
  go -C "$ROOT" test -p "$GO_P" "${flags[@]}" $others > "$log" 2>&1 &
  local others_pid=$!
  local rc_cmd=0 names total i
  names=$(go -C "$ROOT" test -list '.*' ./cmd/vani 2>"$RUN_DIR/go-cmd-list.err" | grep -E '^(Test|Example|Fuzz)')
  total=$(printf '%s\n' "$names" | grep -c .)
  if [ "$total" -eq 0 ]; then
    rc_cmd=1
    echo "cmd/vani: listed no tests (see go-cmd-list.err)" >> "$log"
  else
    for ((i = 0; i < GO_SHARDS; i++)); do
      # round-robin by name order: neighbouring tests (one file's) land in different shards
      local re; re=$(printf '%s\n' "$names" | awk -v n="$GO_SHARDS" -v k="$i" 'NR % n == k { printf "%s%s", sep, $0; sep = "|" }')
      [ -n "$re" ] || continue
      go -C "$ROOT" test -v "${flags[@]}" -run "^($re)\$" ./cmd/vani > "$RUN_DIR/go-cmd-$i.log" 2>&1 &
      echo $! >> "$RUN_DIR/go-cmd.pids"
    done
    local p
    while read -r p; do wait "$p" || rc_cmd=1; done < "$RUN_DIR/go-cmd.pids"
    # every listed test must be reported by some shard: a shard that ran nothing is not a pass
    local ran; ran=$(cat "$RUN_DIR"/go-cmd-[0-9]*.log | grep -Ec '^--- (PASS|FAIL|SKIP): ' || true)
    echo "cmd/vani: $total tests listed, $ran reported by $GO_SHARDS shards" >> "$log"
    [ "$ran" -ge "$total" ] || { echo "cmd/vani: only $ran of $total tests reported" >> "$log"; rc_cmd=1; }
  fi
  wait "$others_pid"; local rc_others=$?
  # Nested modules in this repo (stdlib only) are outside the root module's ./..., so each is tested on its
  # own, without the workspace. A missing one is a failure, not a skip.
  local rc_nested=0 m
  for m in $GO_NESTED; do
    if [ ! -f "$ROOT/$m/go.mod" ]; then
      echo "$m: no go.mod" >> "$log"; rc_nested=1; continue
    fi
    GOWORK=off go -C "$ROOT/$m" test "${flags[@]}" ./... >> "$log" 2>&1 || rc_nested=1
  done
  if [ "$rc_cmd" -eq 0 ] && [ "$rc_others" -eq 0 ] && [ "$rc_nested" -eq 0 ]; then
    finish go "pass ($(grep -c '^ok' "$log") packages + cmd/vani: $total tests in $GO_SHARDS shards)" 0 "$t0"
  else
    finish go "FAIL (other packages rc=$rc_others, cmd/vani rc=$rc_cmd, nested modules rc=$rc_nested; see go.log, go-cmd-*.log)" 1 "$t0"
  fi
}

pids=()
for s in ${SUITES//,/ }; do
  "run_$s" &
  pids+=($!)
done
for p in "${pids[@]}"; do wait "$p" || true; done

rc=0
for s in ${SUITES//,/ }; do
  [ "$(cat "$RUN_DIR/$s.rc" 2>/dev/null || echo 1)" = 0 ] || rc=1
  [ -f "$RUN_DIR/$s.result" ] || echo "did not finish" > "$RUN_DIR/$s.result"
done
if [ "$STRICT" = 1 ]; then
  for s in swift; do [ -f "$RUN_DIR/$s.skipped" ] && { echo "strict: $s had skipped tests" >&2; rc=1; }; done
fi
summary "$RUN_DIR"
echo "logs: $RUN_DIR"
echo "$rc" > "$RUN_DIR/exit"
exit "$rc"
