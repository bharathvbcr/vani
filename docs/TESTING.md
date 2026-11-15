# Testing and the gate

`scripts/test.sh` is the one entry point: Go, Swift, the extension's Bun tests and its type-check, run
concurrently. It is what `scripts/release.sh` calls (with `--fresh --ui`). There is no hosted CI and none is
planned here: `go.work` and `go.mod` point at sibling checkouts by absolute path (Manvi, decide, tessl,
DevCouncil, gusset), and the Swift suite and release need this Mac's signing identity. A hosted runner could not
build the repo as it stands.

```sh
scripts/test.sh                     # all four suites; Go cached; no window, no focus taken
scripts/test.sh --bg                # the same, detached; returns at once (not throttled)
scripts/test.sh status              # the newest run, also while it is still going
scripts/test.sh --suite go,bun      # a subset
scripts/test.sh --fresh --ui        # what a release runs: no Go cache, Jobs tab UI tests included
scripts/test.sh --strict            # a skipped, not-run or retried-to-green test fails the run
```

Logs are in `tmp/test-runs/<timestamp>/` (gitignored, newest five kept; `latest` points at the newest). The
summary names each suite's state: `pass`, `FAIL`, or `pass, but N tests SKIPPED / NOT RUN: why`.

**Skips are surfaced for Swift only.** A Go `t.Skip` and a Bun skip are not counted by the runner. For Swift, a
check that did not run does not print as a plain pass.

## Measured (2026-11-15, 18 cores, this Mac, other agents' builds sharing it)

Every number is wall time on a machine that was not idle: other sessions' Rust builds held the load average
between 70 and 105 for the last hours of the work (it was lower for the earlier runs). Treat them as a range.

| | before | now |
|---|---|---|
| Go, `go test ./...` | 99–166 s across 6 uncached runs (default `-p`: 104–153 s, `-p 8`: 99 s) | alone, through the runner: 70 s (n=1); inside the combined run, uncached (`--fresh`): 98–116 s (n=5) |
| Go, nothing changed | 11 s (`go test ./...`, all cached) | 25–30 s through the runner (3 of 4 `cmd/vani` shards cache; see Still open) |
| Swift | 302 s wall: 1,107 Kit tests in 215 s plus 219 UI-bundle model tests in 83 s | about 90–110 s wall with the build, parallel plus a serial pass |
| Bun (extension) | 50 s, 1,809 tests | 17 s, same 1,809 (`--parallel`) |
| All four | about 505 s one after another | cached: 90–111 s (n=10); `--fresh`: 131–151 s (n=3) |

Stability, as evidence and not a claim: of the combined runs after the fixes below, 5 of 5 passed (cached), 3 of
3 `--fresh` passed, one `--fresh --ui` passed. Before the SIGPIPE fix 1 of 5 hit the crash. A `--bg` run passed
Swift, Bun and types and failed Go once on a transient (below). The runs of 70–105 load average are the ones where
`--bg` under `taskpolicy -b` (644 s) and `nice -n 10` (457 s) failed on timing in every suite; those two results
are confounded by that load, so they do not say which throttle is bad, only that a throttle was not worth it.
`--bg` therefore does not throttle.

What did not help, so nobody retries it: a RAM disk for `TMPDIR` (89 s against 84 s for `cmd/vani`, and the run
failed), and more Go parallelism: `-p 18` panicked once in Manvi's locked-memory credential store
(`could not acquire lock ... cannot allocate memory`). Total Go concurrency is capped (`VANI_TEST_GO_P`, default
6; `VANI_TEST_GO_SHARDS`, default 4).

## What the runner does, and why

- **Go.** `cmd/vani` holds about 1,780 tests that run one after another in one process. Its test names are listed
  and split into shards, each its own `go test -run`, so Go's result cache applies per shard. After the run every
  listed test must have been reported by some shard, or the suite fails.
- **Swift.** `swift test --parallel` exits non-zero on a failure but prints a skipped test like a passed one
  (checked with a planted `XCTSkip`: neither stdout nor `--xunit-output` marks it). So every class that has a
  superclass, in a file that throws `XCTSkip` (15 now, including `ResumeStudioViewsTests`, which inherits from
  `StudioFakeCase`), runs in a second, serial pass, and each skip and its reason is counted.
  `scripts/test.sh --print-skip-classes` lists them; `TestTestScriptDerivesEverySkipCapableClass` pins that.
- **Bun.** `bun test --parallel` (workers, each with a fresh global). A file that fails under load gets one serial
  re-run; if it passes there the summary says "did not reproduce serially", writes `bun.rerun`, and `--strict`
  and `release.sh` surface it, so it is a retry to green that is visible, never silent.
- **Types.** `extension/scripts/typecheck.sh`; `bun test` does not type-check.

## The Jobs tab UI tests

`macos/Tests/VaniApplyUITests` has three classes that drive a second process (`JobsTabHost`) through the
accessibility API: `JobsGapsPanelTests`, `JobsControlsTests`, `JobsBackToTopTests`. Their windows are now fully
transparent (and kept out of window cycling and Spaces), so nothing is drawn. But macOS makes the host the active
app: in the last 12 runs of the focus test it did every time; earlier in the same session the same test passed in
1 of 1, 6 of 10 and 7 of 10 runs with different code and machine state, and one `fresh --ui` run late in the work
passed it, so the rate depends on the machine's state and is not a constant. Ordering the windows at the back,
and giving activation back from `didBecomeActive` plus a 50 ms timer (35–43 yields in 3 s, still frontmost,
flapping focus), did not stop it; those comparisons are confounded by the changing state, so they show that the
yield does not reliably work and nothing finer.

So a default run, and `--bg`, do not run these tests (`13 UI tests NOT RUN`), and `--ui` does.
`JobsTabInvisibleTests` asserts the transparency (no accessibility trust needed) and ends in a skip, naming it,
when macOS has made the host frontmost. `VANI_UI_HOST_VISIBLE=1` shows the windows, to watch a run.

**Not verified: the three AX classes have never passed with the transparent windows.** This shell's runner is not
trusted for accessibility, so they skip, and `release.sh` now runs them (`--ui`). Before the next release, run
`scripts/test.sh --suite swift --ui` once from a terminal that has Accessibility access and read the result.

## Failures found on the way, and fixed

- `g10-adversarial.test.ts` and `popup-import-binding.test.ts` failed on clean `main` (stale test fixtures: the
  cover messages gained a required `url`, and the popup's export handler became `async`). Tests updated.
- `scraper.card.test.ts` "669 job cards" takes about 7 s alone against Bun's 5 s default, so a loaded machine
  failed it. It now has an explicit 30 s timeout, like the other heavy tests.
- `ResumeStoreTests.testPreviewCopyRefusesAPathAndCapsTheCopies` counted copies in the machine-wide temp
  directory, so parallel test processes pushed it to 9 against a cap of 8 (failed in 2 of 5 parallel runs).
  `ResumeStore.previewCopy` takes an optional `in:` root; the test uses its own.
- `swift test` crashed intermittently with SIGPIPE (signal 13) when a child exited before reading its stdin
  (`HostProcessTests`, `OnDeviceDeciderTests`, `JobsTabSession.send`). Those pipes now set `F_SETNOSIGPIPE`, as
  the app's own do. `testAHostThatExitsWithoutReadingItsInputIsAStatusNotASigpipe` kills the test process on the
  old code, every time.
- `TestNothingButTheAuditorReadsItsLog` walked the whole repo root, `.devcouncil/` and `.state/` included. It now
  skips dot and underscore directories (no tracked non-test Go file is in one). This did not by itself make shard 0
  cacheable (below).

## Still open

- **`cmd/vani` shard 0 does not replay from cache in the runner** (about 11 s each run), although the same
  `go test` replays standalone. Something in it reads state that changes while the other shards and packages
  run; not found.
- **A transient `go list` failure.** One `--bg` run failed with `no Go files in .../Vani ... [setup failed]`: a `.`
  entry in `go list ./...`, once in about 14 runs, cause not found. The runner reported it as a FAIL; rerunning
  passed.
- **One unread parallel-pass failure.** In the second batch of five combined Swift runs, run 4 had
  `parallel rc=1, serial rc=1`. Its log was pruned (newest five runs are kept) before it was read, so what failed
  is unknown. The `ResumeStore` and SIGPIPE fixes came after.
- **`ScheduleAgentTests` has timing assertions** (`testStopSignalsTheChildWhileAWatchCheckIsStuck`,
  `testStopEndsTheDaemonWithSigtermAndWaitsForIt`) that failed twice while other work shared the machine, and the
  5 s typecheck-gate tests in Bun fail when the machine is starved. A run on a saturated machine can fail on
  timing whatever the runner does. Not changed.
- **`bun test` on its own** had 8 failures in its first serial run on this machine (3 stale expectations, now
  fixed, and 5 that did not recur in later serial or parallel runs). It was not re-run serially on an idle machine,
  because the machine was never idle; `--parallel` and the serial re-run in the runner were.
- **Manvi's locked-memory credential store panics when many tests hold secrets at once** (seen once, at `-p 18`).
  That is in the Manvi repo; the cap above avoids it, it does not fix it.
- **The UI host takes focus** (above).
