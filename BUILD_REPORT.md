# Vani build report — 2026-10-27

Audit-bug backlog from `AUDIT_REPORT.md` (2026-10-26). Primary `main` is still `70fc33ec6603885f49020f7dc95504bfc23dc784`. The only dirty files there are unstaged `AUDIT_REPORT.md` and this report. No commit from this run is on primary `main`. Nothing was pushed, force-pushed, or merged.

Done was not requested on any card. `gitpulse_complete_task` to `done` needs a Manvi verification record for the worktree head, and every review move in this run came back with `verification: null`. The cards below are in **review**.

The fixes were stacked on one clone, `/private/tmp/vani-integrate`, which is not a linked worktree of primary `main`. Its head is `7dc8df50b322ab96fd451373566640e90836418a`. `go.mod` and `go.work` are unchanged from `70fc33e`. That clone is where the suites below ran. It was not merged.

## How the work ran

The spawn tool has no model argument. `grok-4.7` `code-editor` stood in for the implementer and for a separate adversarial pass. This orchestrator wrote each implementation brief and reviewed each diff.

Isolated clones are their own `main`, not linked worktrees of the primary checkout. GitPulse insights and collision scans of `/Users/bharath/Code/apps/Vani` see one worktree. They do not see `~/.grok/worktrees` or `/private/tmp` clones. DevMap and GitPulse return `REPOSITORY_TRUST_REQUIRED` on those clones. Caller questions were asked on the primary index. The latest status is generation 2614, fresh, not degraded, 86600 edges. A truncated or `walk_incomplete` answer was treated as unknown. The ledger facet was stale (no catch-up newer than 600 seconds). Collisions were ok: 0 overlapping files, 1 worktree scanned.

Isolated Go builds used a temporary `go.work` outside the repo (`use` of the clone plus the local Manvi and `decide` checkouts, with the existing Tessl replace). The repo `go.mod` and `go.work` were not edited. `/tmp` is a symlink to `/private/tmp`; a `go.work` `use` line has to name the real path. `VANI_NO_KEYCHAIN=1` was set on `vani` test runs.

## Tasks in review from this run

Board status below was read with `gitpulse_list_tasks` on 2026-10-27. Revisions are the board revisions after the review move. Heads live only on the isolated clone named in the card log.

| Id | Rev | What landed |
| --- | --- | --- |
| `ft-e97abbbf30ed5b366274a269a2b658ed` | 13 | JSON-LD: only the new SE-1 criteria, plus a 2026-10-27 re-read of criterion 7. Eightfold and Apple still bound a detail 404 by `max(1, n/10)`. |
| `ft-9b6a727a79db1269ffdb98722f6c0c36` | 3 | Idle embed: the window waits out an abandoned Embed, bounded by the Tessl call timeout, before Close. A chunk that only misses its own ensureProfile deadline does not count as a bad chunk. |
| `ft-d07a7b653eaab4c8e2d490e3a4b6d6ab` | 3 | Int32: oversized counters are refused. `count()` keeps `0..Int32.max` and returns 0 above that. `Dedup.maxCount` is `Int(Int32.max)` so a saturated merge stays a value `count()` keeps. |
| `ft-0f3ac31c525eeb33741f407caf0bb11c` | 4 | Pid identity: EPERM and ERROR_ACCESS_DENIED mean the process is not visible. A live daemon also has to be this binary, with a start at or before `StartedAt` plus one second. The frozen-clock skew is captured once in the test. |
| `ft-94364021c96bc6f7427b6c0af5cce7ec` | 3 | Popup: the import control no longer depends on an inline `onclick`. |
| `ft-c6f80c883e69566801e309d55d20b351` | 4 | Contact gate: a spaced handset and a spaced mailbox are refused. A year span is two 1900–2099 groups joined only by `-` or `–`. |
| `ft-22d7da5f2735997b8bbdb7934bc1345c` | 7 | Symlink leaf: directory components still resolve. Program-named leaves are not written through. Criteria 1–5 were restated to that contract. The card stays in review. |
| `ft-f2cc510849955a5c723c438dc3967775` | 4 | last.json: `readRunRecordFile` and the daily snapshot use `readBoundedFile`. A missing file is still no record. A symlink, FIFO, directory, socket, or character device is not opened. |
| `ft-0c907f4e66a33511458a7833c560a47a` | 3 | Rekey: `loadRecord` reads meta before any rename. Damage leaves the old directory in place. |
| `ft-dcd139bfca29862ce97d8ade909d3b79` | 3 | requestRun: the lock is taken after the first read says a write is needed, then the file is read again. A lock failure is an error. |
| `ft-b1d3e1badb3eab6c80ed6cf04e92e043` | 3 | Win32: a Windows job object via `syscall.NewLazyDLL("kernel32.dll")`. `.exe` is executable without a Unix mode bit. A directory is not. |
| `ft-17728c6f60b7012ddc69531f679e98f0` | 3 | History: `flushFinishedLast` Lstats `last.json` before opening it. A non-regular leaf is not kept and does not block the bookkeeping write. |
| `ft-2a67d27fa725e7fdf0321ddb0717c4fc` | 3 | Read cap: stat before `Data(contentsOf:)`. The 50_000_000 byte cap is unchanged. A non-regular leaf is never opened. |
| `ft-f2c485911ee04f3e4f6b8ff03710cd57` | 3 | Bank cache: `fresh()` bumps the generation and drops the inflight fetch before requesting again. An older get must not `hold`. |
| `ft-948c9eb6753acd2a05c29175ad1e8b41` | 3 | Bullets: phone and work-authorization prose is stripped in `internal/semantic`. `redraft.go` was not edited. |
| `ft-b7dacb8fdc271946432973e83c24bf49` | 3 | Binary watch: Windows file identity is the volume serial and the file index, read only after `os.Stat` says the file is regular. The handle is closed on every path that opened one. |
| `ft-bafd750094720ac26a73280954b5c717` | 7 | MAC-2 only: activity refresh and `change` do their disk work on `BlockingWork`, then publish on the main actor. `reload()` still reads the bank on the main actor, on purpose. |
| `ft-10c5bf90ab4e6ee33fbacd36d430baea` | 4 | Frame probe: one timer. A queued timeout is spliced out and does not poison. A written timeout poisons that connection. `jobsIngest` stays at 40000 ms. A module imported before `connectNative` exists still uses one port once Chrome gains it. |
| `ft-84d4fe2545e64bdaa9678e18b45a5e6d` | 9 | PS-4 only: a panic after `guardedRun` still finalises the run as failed. A panic whose ledger is intact is not `ErrFatal`, so the daemon keeps scheduling. The 2026-10-21 lifecycle criteria were already merged and were not reopened. |
| `ft-8f7be1583ae5e08890b97a5b4f4acab7` | 3 | SIGTERM test only. The pid file is published by rename after the trap is installed. An empty file does not satisfy the wait. `ScheduleAgent.swift` was not edited. |

No task from this run is in `done`.

## Verified this continuation

These two were still open when this continuation started. Both are now in review.

**SIGTERM** `ft-8f7be1583ae5e08890b97a5b4f4acab7`, revision 3, sequence 27322. Head `0a02af04bd0bda7708c66637160b1e399aaf1215` on the schedule-agent worktree. The diff from the parent of `e47a734` is only `macos/Tests/VaniApplyKitTests/ScheduleAgentTests.swift` (+22/−2). Parent re-run: `testStopEndsTheDaemonWithSigtermAndWaitsForIt` passed in 0.407 seconds; `testThePidWaitRejectsEmptyPartialAndNonPositiveText` passed in 0.000 seconds. The 0.25 second cleanup assertion is still there. The full Swift suite on the integrate clone later passed; see below. DevMap impact of `ScheduleAgent.swift` was truncated (60 of 312 shown, `walk_incomplete`), which is why production `stop()` stayed out of scope.

**last.json** `ft-f2cc510849955a5c723c438dc3967775`, now revision 4, sequence 27479. The earlier review move was revision 3, sequence 27324. Production `e59931cb7994f82f67cc2b7b89f0730f768780cd`. Adversary tests only `47febfa12b9e1eaefb34abd6f53961c49d954efe` (`cmd/vani/lastjson_adversary_unix_test.go`, +742). Parent filter `TestLastJSON|TestRunRecord|TestHistory|TestPanicAfter|TestConfig` passed in 1.535 seconds. Parent `go test ./cmd/vani/` with no `-run` passed in 119.383 seconds (`GOWORK=/tmp/vani-lastjson-go.work`, `VANI_NO_KEYCHAIN=1`). A missing file is still `(nil, nil)`. Any other read error is returned. `restoreRecordAfterRestart` is unchanged: a nil error writes the prior bytes back, `os.ErrNotExist` removes the record this job wrote, and any other error logs and does not write or remove. `flushFinishedLast` and `finishDailyRun` were not edited. The check-then-open window inside `readBoundedFile` is the existing one and was accepted. `mknod` is not permitted here. `TestLastJSONAdversaryDeviceNode` on integrate commit `ba1cbe2328f5622e8b351c2ee6dac4ea76f25799` stats `/dev/null` and `/dev/zero`, requires a character device, and refuses them through `readBoundedFile` before `os.Open`. The filter passed in 0.171 seconds. The error string is the proof the node was not opened; a time bound would not be, because reading `/dev/null` returns EOF at once.

## Evidence recorded earlier in this same run

These timings were parent runs before this continuation. They were not repeated just now.

- JSON-LD full `go test ./...` exited 0 in 649.24 seconds.
- Idle full `go test ./...` exited 0 in 640.85 seconds.
- Pid: full `./...` failed five frozen-clock tests; after `c460940` only `./cmd/vani/` was re-run, and it passed in 133.800 seconds. Those commits are in the integrate clone, whose full `./...` is below.
- Frame probe, isolated: `bun test` of `nativeport.test.ts` and `frameprobe.test.ts` — 38 pass, 0 fail, 485 expect(), 6.46 seconds. Typecheck passed. The full extension suite then failed four frameprobe tests. That failure is fixed below.
- PS-4: filter passed (`cmd/vani` 0.962 seconds, `daemon` 0.172 seconds). Full packages with no `-run` passed (`cmd/vani` 132.718 seconds, `daemon` 0.230 seconds).
- MAC-2: Swift filter `AppModelTickTests|BankStoreReadCapTests|ImportBankReadCapTests|AppModelAdversarialTests` — 30 UI tests and 25 BankStoreReadCap tests, 0 failures. The 11 unhandled `runnotice` fixture warnings are pre-existing. Measured main-actor gaps, all under 0.25 seconds: edit/refresh 0.015160 seconds, two-edit 0.015122 seconds, unicode path 0.015144 seconds.

## Integrate clone

`/private/tmp/vani-integrate` was cloned from the last.json worktree and every other fix was cherry-picked with no conflicts. Head `7dc8df50b322ab96fd451373566640e90836418a` (`extension: use one native port when connectNative appears after import`), on `ba1cbe2328f5622e8b351c2ee6dac4ea76f25799`. The working tree was clean. `git diff 70fc33e..HEAD -- go.mod go.work` is empty. `go vet ./...` exited 0 in 4.57 seconds (`GOWORK=/tmp/vani-integrate-go.work`, `VANI_NO_KEYCHAIN=1`).

`go test -count=1 -timeout 20m ./...` exited 0 in 350.71 seconds. No package printed `FAIL`. `cmd/vani` took 348.152 seconds. `internal/sources/eightfold` took 0.228 seconds and `internal/sources/apple` took 0.344 seconds. Two packages have no test files (`internal/sources/srctest`, `internal/testutil/symlinktest`). This run is at `ba1cbe2`, the device-node commit. The later commit is TypeScript only.

`swift test --package-path /private/tmp/vani-integrate/macos` exited 0 in 416.14 seconds. VaniApplyUITests executed 173 tests, 12 skipped, 0 failures, in 75.307 seconds. VaniApplyKitTests executed 942 tests, 12 skipped, 0 failures, in 204.753 seconds. The 11 unhandled `runnotice` fixture warnings are the pre-existing ones. The skipped tests are what Swift reported; this log does not say why each one skipped.

`bun test /private/tmp/vani-integrate/extension/test` first exited 1: four tests in `frameprobe.test.ts` expected a port and saw none. `nativeTransport` is chosen when `store.ts` is first evaluated. Another file imports it before frameprobe's `beforeAll` installs `chrome.runtime.connectNative`, so the shared module stayed on `spawnTransport`. DevMap impact of `nativeTransport` on the primary index (root `/Users/bharath/Code/apps/Vani`, generation 2614) named direct callers `background.ts::native` and `store.ts::createStore`. The walk stopped at depth 3 (`walk_incomplete`), so that list is a lower bound. Six `m.nativeTransport` sites in `store.test.ts` are unresolved namesakes, not edges. `frameprobe.test.ts` was not in the affected-test list because it imports through a query string.

The fix keeps `nativeTransport === spawnTransport` when the module loads with no `connectNative`, and when Safari's `sendNativeMessage` wins. `background.native` calls `currentNativeTransport()` on each call. One port is cached per `connectNative` function. A different function does not reuse that port. `createStore` still captures the load-time binding. After that commit, `bun test` of `extension/test` was 1384 pass, 0 fail, 16694 expect(), 23.03 seconds, and `sh extension/scripts/typecheck.sh` printed `typecheck: ok`. An isolated `background.test.ts` run before the edit, with `connectNative` present at import, was 59 pass, 0 fail, 315 expect(), 2.42 seconds. A throwing `connectNative` falls through to `sendNativeMessage` during backoff, and `resumeOpen` is not a port op, so those stubs stayed.

## Benchmarks

No touched path in this run had a committed benchmark that was re-run, so there is no before/after number to report and no invented delta.

These are not hot paths, and no bench was added: pid, JSON-LD, Int32, idle, popup, contact, symlink, rekey, requestRun, Win32, history, read-cap, bank cache, bullets, binary watch, frame probe, last.json, SIGTERM.

MAC-2 has no Swift bench. The number for that card is the main-actor gap above. The 2026-10-21 daemon A2-2 bench was not re-claimed for the PS-4 residual.

## Deferred, blocked, or left alone

- `ft-7099bd3ba7e0b8f2f9622204b98f2c85` (Lappi v2, revision 5) is `in_progress`. It was not touched. `internal/decide/jdtriage/fixture.json` was not edited.
- `ft-8d8ca2ea609144eb5240c2b743487d58` (clean clone cannot build, revision 2) is `in_progress`. Owner decision stands: published versions, no vendoring, do not edit `go.mod`.
- Inbox, backlog, and ready were empty when listed this turn (0 tasks each).
- The review column also holds the 2026-10-19 and 2026-10-20 cards from the earlier build. This run did not reopen them.
- Windows tests were not executed. On the integrate tree, `GOOS=windows GOARCH=amd64 go test -c` succeeded for `./internal/rkbexec` and `./internal/rkbresume` (compile only, 79.87 seconds for that chain, together with the Linux compile below). `GOOS=windows GOARCH=amd64 go test -c ./cmd/vani` exited 1. The linker errors are `syscall.Mkfifo` in `adv_reload_pins_test.go:27` and `exportattack_test.go:163`, and `syscall.Kill` in `kbmatchlockadv_test.go:83`, `kbmatchlockadv_test.go:140`, and `pidalive_adversary_test.go:362`. Those tests were not changed. This is not a pass.
- Linux `GOOS=linux GOARCH=amd64 go test -c ./cmd/vani` exited 0 on the same chain. The tests were not run. Real Tessl/Metal tests were not run. Lightpanda integration tests were not run. Darwin `kill(1, 0)` returned EPERM, which the pid card treats as "not visible."
- `dev verify ft-f2cc510849955a5c723c438dc3967775 --json --mode off` returned `ok: false`, `gate_mode: off`, `processed_tasks: 0`, `verified_tasks: 0`, and `error: task ft-f2cc510849955a5c723c438dc3967775 not found`. Gate mode is off (`verification_origin` default, no `.devcouncil/config.yaml`). That is not a pass. Changes are not on primary `main`.

## New tasks

- Filed and now in review: `ft-8f7be1583ae5e08890b97a5b4f4acab7`, the schedule-agent SIGTERM test race. It was not folded into the Int32 card.
- A windows/386 job-struct mismatch was not filed. `procgroup_windows.go` returns early unless `unsafe.Sizeof(uintptr(0)) == 8`, and the struct uses `uintptr`, so it tracks pointer width. The repo does not ship windows/386. That is a skipped check, not a proven ABI bug.

## Fable decisions

- JSON-LD scope is the three new SE-1 criteria. Prefix URL is not a match. An empty page URL matches nothing. Depth cap stays `maxJSONLDDepth` (6). `@context` is skipped.
- Int32 refuses; it does not clamp to `Int32.max`. The log line is fixed and contains no answer value, key, or counter. JSON stays the source of truth until a later ordinary save. `Dedup.maxCount` moving to `Int(Int32.max)` was accepted.
- Pid slack stays 1 second. File mtime is not the baseline. A start far before `StartedAt` is still live. The frozen clock captures skew once and does not change production `daemonIsLive`.
- Idle `callEmbed` stays non-join. No Metal. If the join bound expires inside Embed, Close is skipped, the fixed log line is written, and the window still ends.
- The 2026-10-21 symlink design stays: leaves are not written through. Criteria 1–5 were overwritten on 2026-10-27 (revision 7) to that contract. The old write-through sentences are not what the card requires.
- Contact `phoneMaxSeparator` stays 3 on that worktree. `555 --- 4567` stays allowed. Seven or more Unicode decimal digits are refused. Circled digits and superscripts are not a phone.
- Bullets are separate from the contact gate. `555---4567` is a phone. `555 --- 4567` is not. A following year span joined by one space is removed with the phone. `redraft.go` stays untouched.
- Rekey does not change its signature. Windows rekey refusal stays on `ft-b6b00b08e8b50cd9613fd149c17698ec`.
- requestRun still replaces a symlink leaf. The TOCTOU fix is the lock and the re-read.
- History and last.json share `readBoundedFile` for the read. A stable FIFO or symlink must not block. A race that swaps a regular file for a FIFO after `Lstat` was not given a new lock.
- Frame probe: deadline is never a field of the host message. `0` is a real shorter deadline. `resumeInfo` is a port op, so 2000 ms is its only timer. `bankcache.ts` was not edited. The load-time export stays `spawnTransport` itself when there is no `connectNative`. `background.native` looks again so a late `connectNative` still uses one port.
- MAC-2 does not add a semaphore. `reload()` stays on the main actor. A cancelled task may still finish disk work already inside `BlockingWork`.
- PS-4 does not recover a panic inside the recover's own `saveRunRecord`. `runJob` is that backstop, and the error it records is non-fatal even if the panic value is `ErrFatal`. The cursor still advances.
- SIGTERM is test-only. Production `ScheduleAgent.swift` stays as it is while DevMap impact is truncated.
- Wave E daemon work is the PS-4 residual only. The 2026-10-21 decision that process-lifetime fields stay fixed for the life of the process was not reopened.

## Owner decisions still pending

These were not built.

1. XPC peer check (MAC-4 / declined D18): any binary signed by team HAUHVD8M37 can connect.
2. `OPENROUTER_API_KEY` in the daemon child (MAC-5). The current path is tested.
3. `ft-c9ff73806ffa9c5ed8e80b913c135b31` criterion 3, the `pmset` sleep check. The Mac was not put to sleep.
4. `ft-52b6e7d3f0950c24195d9c20c0a20351`, v0.1.0 signing, publish, and rollback.
5. `ft-8d8ca2ea609144eb5240c2b743487d58`, clean clone: published versions, no vendoring, do not edit `go.mod`. The card is `in_progress`.
6. `ft-6dc4d776b2b04ffbdf04e58c4a315bf6`: do not install over `/Applications/Vani Apply.app`, and do not read the live bank unless asked.
7. `ft-7099bd3ba7e0b8f2f9622204b98f2c85`: Lappi MinHash, `shadow_every`, decide identity pin, fixture bump, and the label set. The card is `in_progress`. `fixture.json` was not changed.
8. D17 and D18 on `ft-1045267a269b4250341c5d5d69acf653` stay declined.
9. Daemon reload-per-run was already decided on 2026-10-21: process-lifetime fields stay fixed. That question was not asked again.

## Definition of Done, still open

Every acceptance criterion that this run took on has a regression test, and each fix went through a separate adversarial pass before it was moved to review. The symlink card's criteria 1–5 now say what the code does. The integrate clone's Go suite, Swift suite, and extension suite passed, as recorded above. `go vet ./...` passed. That is the combined tree, not primary `main`.

These are still open, and they are why no card was moved to done:

- Manvi's verification is `null` on every review move. `dev verify` does not know GitPulse `ft-` ids, and gate mode is off.
- Primary `main` was not merged. The user did not say merge.
- Windows and Linux tests were compiled, not executed. `./cmd/vani` does not link for Windows because existing tests call `syscall.Mkfifo` and `syscall.Kill`.
- Real Tessl/Metal tests and Lightpanda integration tests were not run.
- The owner decisions in the list above were not built.
- `ft-7099bd3ba7e0b8f2f9622204b98f2c85` and `ft-8d8ca2ea609144eb5240c2b743487d58` stay `in_progress` and were not touched.

Opus-stand-in review approved the diffs that are in review above. `grok-4.7` `code-editor` stood in for the implementer and the adversary, because the spawn tool has no model argument.

## History — 2026-10-21

The report below is the 2026-10-21 session log, unchanged.

# Vani build report — 2026-10-21

## Current status (end of session, main @ 7e9ce14, local, not pushed)

**Every audit card the session could build is built, verified and merged to `main`, on your instructions ("merge all",
"build on main anyway", "leave no gaps").** The history below is the log of how it got there; its "nothing is merged"
and "merge-approval ask" sections describe the morning state.

- **On main:**
  - Audit waves 1–5: G1, G15, G8, G6, G5, G10, G9, G4, G13, G2, G7, G11, G3, G12 and G14.
  - Follow-ups:
    - ft-5ddec968 (native op deletion) and ft-fa1f7de7 (notify flake).
    - ft-2673557b (jobs mark/find, now with a scoped ledger read).
    - ft-0aa92138 (no gap; regression tests).
    - ft-a343da41 (loose ends).
    - ft-22d7da5f (symlink-safe atomic writes).
    - The fieldtriage guard speedup and the rankfit objective speedup.
  - Each passed an adversarial pass, and every break went back for a fix. That covers everything except the
    test-only, deletion-only and behaviour-preserving cards, which are stated on their cards.
- **Last full checks on main:**
  - Go: `go vet ./...` clean and `go test ./...` with no FAIL, at 0c27a13 with only docs after it.
  - Swift: `swift test` with 544 run, 7 skipped and 0 failed, at 52f218b. Later merges touched no Swift.
  - Extension: typecheck gate ok and `bun test` with 1,191 pass and 0 fail, at e5a0ab0. Later merges touched no
    extension code.
- **Board:** every built card is in **review**. Done is gated on a Manvi verification record, and Manvi's task store
  doesn't know GitPulse task ids (`manvi tool devcouncil_get_task` returns "no task ft-…"). No card filed through
  GitPulse can reach done from an agent session. That is a GitPulse↔Manvi integration gap, recorded in
  docs/FOLLOWUPS.md.
- **Branches and worktrees:** all agent branches were merged or patch-equivalent and are deleted, except four whose
  worktrees are harness-locked (all merged; remove them with `git worktree remove -f -f`). Some half-deleted
  directories remain under `.claude/worktrees/`, held by Finder `.DS_Store`.

### ft-7099 (Lappi v2 caller side): first wave merged (main f1f3319)
- **P1, held-out export:** `vani export heldout` exports keys and text hashes, with normalisation that matches Lappi's Python and a locked re-export after each label write. It writes no MinHash signature yet; that waits on your x/crypto OK and Lappi's format.
- **P2, Haiku teacher, and the run:** `vani teacher` was run on your instruction on 2026-10-21. It asked 3,668 postings and stored all 3,668 readings, with none unusable. Spend was **$1.60 settled** (Manvi ledger delta), against an estimate of $1.02 and a ceiling of $6.34.
- **P3, label windows:** label windows with frozen labels, and a two-window per-slot promotion verdict. `vani label` puts teacher disagreements first.
- **P4, per-slot routing:** `decide.lappi.primary_slots` routes Lappi per slot. A Lappi outage trips the circuit breaker and is reported.
- **Race suite:** `cmd/vani`'s race suite went from 519 s to 284 s, so `go test -race ./...` fits the default 10-minute timeout again.
- **Still open on ft-7099:**
  - Your ~230 labels and `decide.shadow_every: 1`.
  - The decide-module release-identity change, which Fable left for you.
  - The MinHash format with Lappi.
  - The fixture bump, blocked on Lappi cards.

### Still open: needs you, or can't be done from here
1. **ft-8d8ca2ea, "A clean clone of Vani cannot build".** `go.mod` replaces `decide` and `tessl` with relative
   sibling paths, and Manvi needs the ignored `go.work`. The repo rules forbid editing `go.mod`/`go.work` without
   you, so choose pinning, vendoring, a committed workspace, or a named path layout. Every agent worktree today hit
   this.
2. **ft-52b6e7d3, "Decide how v0.1.0 is signed, published, and rolled back".** This is a release decision.
3. **G1 hardware check.** Run a `pmset` sleep check across a scheduled minute. I didn't, because sleeping the Mac
   suspends the live sessions.
4. **G8 install and rescan.** Install the built `macos/build/Vani Apply.app`, which replaces
   `/Applications/Vani Apply.app` and re-registers its agent and Chrome link. Then run
   `python3 -I tmp/scan/fffd_scan.py "$HOME/Library/Group Containers/HAUHVD8M37.dev.vbcr.vani/apply"`. Both scans so
   far show 0 U+FFFD.
5. **ft-2673557b on a quiet machine.** `jobs mark`/`find` measured 90/94 ms min at load 17–20, under the 250 ms target.
   The machine never went quiet (load 17–89 all day), so the number carries a load caveat.
6. **wt-jibe** (`tmp/wt-jibe`, 92 uncommitted files, idle since 2026-10-20). It will conflict on everything built on
   main today: config, daemon wiring, digest, sources, fieldtriage, kbmatch and FOLLOWUPS. G3's criterion 6 (its
   config writers) can only be closed when it lands.
7. **Manvi↔GitPulse verification gap** (above). Until it's resolved, cards stay in review.
8. **Cleanup.** Remove the four locked worktrees and the half-deleted `.claude/worktrees/agent-*` directories, which
   needs `rm -rf`; the gate holds it for me.

---

# Session log

Backlog worked in AUDIT_REPORT.md's wave order. Every card below went through the same loop: an Opus brief, a Sonnet
build in its own worktree, and a separate Sonnet adversarial pass. Any break went back for a fix. Opus reviewed every
diff, and Fable made the judgement calls.

**(Morning state)** Nothing merged to `main` yet. Merging needed your approval. Every result lived on a worktree
branch under `.claude/worktrees/`, listed with its final commit. Done is evidence-gated by Manvi, so every card sat in
**review**.

## Merge-approval ask (Fable's order)

| # | card | branch @ final sha | stacks on |
|---|---|---|---|
| 1 | G1 ft-c9ff7380 daemon wall-time slices | `worktree-agent-a2d21fa6e46124f3d` @ 38ab5d8 | main |
| 2 | G15 ft-d30b8cdc benchmark baselines | `worktree-agent-a7911199a957dea7b` @ 86550e5 | main |
| 3 | G8 ft-6dc4d776 Mac native protocol integrity | `worktree-agent-a0b66855e0104df3d` @ 4cf62a2 | main |
| 4 | G6 ft-c567ded5 rankfit/eval statistics | `worktree-agent-a75e03a26ebee0875` @ b6a2a42 | main |
| 5 | G5 ft-b6b00b08 storage write paths | `worktree-agent-a7b7e43519f0d2eb5` @ 1758dd8 | main |
| 6 | G10 ft-60d1dc8e extension routing & trust | `worktree-agent-a274e71c19b8fdfca` @ 59f9662 | G8 |
| 7 | G9 ft-bafd7500 Mac main-thread & subprocess | `worktree-agent-a73463e4ef0f1cebb` @ 8ce3e2c | G8 |
| 8 | G4 ft-3d83fba9 LCA view + ledger eviction | `worktree-agent-a16233ba51e8fd080` @ f8999ba | G5 (includes G15's bench commit) |
| 9 | G13 ft-e97abbbf Eightfold/Apple detail 404 bound | `worktree-agent-a4588d976d23b410b` @ c644ec4 | main (rebase against wt-jibe when it lands) |

**Blocker on #8 (Fable):** `cmd/vani/app.go:217` passes `Ledger.All()` to `warmSemantic`. With eviction in place,
closed jobs would lose their semantic score. The fix is a one-line `ledger.Full` wrap, but `app.go` is uncommitted in
wt-jibe, so it can't land from here. It is recorded as a criterion on ft-84d4fe25 (G2), and it must land before or
together with G4.

**Overlap:** #2 and #8 both carry the bench commit (86550e5, and its cherry-pick 6ebb9b1). Merge #2 first; the
cherry-pick then applies as empty.

## Cards moved to review

Each card log carries the full per-criterion evidence. Every fix has a test that failed on the code before the fix;
the exceptions are named below.

- **G1 ft-c9ff7380:** `waitUntil` waits at most 30 s per timer, re-reads the wall clock, and re-checks after arming.
  - The adversarial race hunt found a real lost wakeup: 1 in 500 runs before the fix, 0 in 1,500 after.
  - **Pending you:** criterion 3, the `pmset` sleep check on hardware. It wasn't run because sleeping this Mac would
    suspend six live sessions.
- **G15 ft-d30b8cdc:** baselines only; see "Benchmarks" below.
- **G8 ft-6dc4d776:** UTF-8-safe chunking (one owner of the boundaries), plus the extension's `totalBytes` check.
  - Patches are validated whole before anything is applied, paging is clamped, and a corrupt draft-settings file now
    fails loudly.
  - Adversarial fixes:
    - Message integers are parsed strictly, so a fractional `baseRev` no longer truncates.
    - A dangling symlink no longer reads as "missing".
    - The client bounds `chunkCount`.
  - Live-bank U+FFFD baseline scan: **0**. bank.json is 9,230 B with 26 answers, and the SQLite mirror has 26 rows.
  - **Pending:** the post-install rescan, which needs the merged host installed.
- **G6 ft-c567ded5:** `Beats()` now needs at least 200 held-out pairs and a lower gain bound above 0, from an
  item-level bootstrap.
  - Fits are weighted by inverse inclusion probability (Horvitz-Thompson, 1/PSelect).
  - The held-out floor is 27/30, and the no-answers shadow row is kept.
  - Null world: 5 of 20 seeds "won" before the fix, 0 of 20 after. Interval coverage was 94/100.
  - Adversarial fixes: a p_select floor of 1e-9 (below about 1e-154 the weights overflowed to NaN), and correct
    split advice at 0 asked.
- **G5 ft-b6b00b08:** six durability gaps closed.
  - Parent-dir fsync on first create.
  - Torn-tail guard, plus a safe cut-back on a failed write.
  - Note budget handled by raising the marks line cap from 8 to 16 KiB.
  - kbsync counts the accepted block before the flush.
  - rekey refuses on Windows.
  - Inbox spool cap.
  - Adversarial fixes: TOCTOU-safe `SyncNew`, a `TornTail` that reports I/O errors, and no truncating of another
    appender's row.
- **G10 ft-60d1dc8e:** attachResume goes to the sender's tab, pinned to the offering page's origin, and the frame
  registry is cleared on navigation.
  - Typecheck gate on an exact-pinned TypeScript 7.0.2, with a sender-policy table.
  - The three unsent message types are deleted.
  - Scraped URLs are scheme-filtered.
  - The gate found 8 type errors the audit didn't know about. One was the popup warning icon reading a field that
    doesn't exist, which is now fixed: a visible behaviour change.
  - Adversarial fixes:
    - Bidi characters are refused in resume names.
    - The bank bound now counts UTF-8 bytes.
    - The gate can no longer print "ok" for an empty or fake tsc.
    - The gate's environment-variable overrides are removed.
- **G9 ft-bafd7500:** a stamp-gated tick that reads off the main actor, and cached bytes and settings.
  - Blocking calls now run through `BlockingWork` (a dedicated queue) instead of the cooperative pool.
  - Child processes are killed with SIGTERM then SIGKILL.
  - Replies decode version first.
  - The B2-10 test first passed on the old code. It was rewritten until it discriminates: the probe waits 5.5 s on
    the old pattern and 52 µs with the fix.
  - Adversarial fixes: the fractional-version message, and the exact-ceiling truncation flag.
- **G4 ft-3d83fba9:**
  - **c2 (LCA view):** built, because the decision-3 rule fired. It loads in 19 ms against 947 ms for a full load,
    and survived 133k fuzz executions and a cross-process race.
  - **c4 (ledger eviction):** heap after Open is 34% of the old heap.
  - **c4 adversarial fixes:** invalid-UTF-8 round-trip, and an unreadable body no longer stalls the exclusions pass.
  - **c5:** declined, as decided.

- **G13 ft-e97abbbf** (started under decision 12, because wt-jibe hasn't landed):
  - Branch `worktree-agent-a4588d976d23b410b` @ c644ec4, on main.
  - Criterion 7: Eightfold and Apple detail 404s are now bounded like Workday's. Its test failed on the old code.
  - The adversarial pass found no breaks; the cross-adapter parity table is identical.
  - Criterion 3 is only a FOLLOWUPS record, which goes through G14.
  - **Merge note:** this overlaps wt-jibe's uncommitted eightfold.go and apple.go, so expect a rebase when wt-jibe
    lands. It is item 9 in the merge ask.

## Merged to main (owner instruction, 2026-10-21)

All nine branches above are merged (`9b4af91`..`4ce02f4`); main is local and not pushed.
- **Merges:** `internal/eval/store.go` (G6 + G5) and `cmd/vani/runwire.go` (G4 + G13) auto-merged.
- **G4 gap:** G4 was merged on your instruction, overriding Fable's merge gate. The app.go:217 gap is still open and tracked on
  ft-84d4fe25.
- **Checks after merging:**
  - Go: build and vet clean, `go test ./...` with no FAIL.
  - Swift: 557 run, 7 skipped, 0 failed.
  - Extension: typecheck ok, and `bun test` shows 1,186 pass and 0 fail.
- **Board:** a request to move the seven cards with no open owner check to done was held by Manvi ("no verification record exists
  for this task: Manvi's work.verify.task has not judged it"), so they stay in review. G1 and G8 stay in review for
  the owner checks.

## Wave 3b — built off merged main 4ce02f4 (not merged)

| card | branch @ sha | status |
|---|---|---|
| ft-5ddec968 delete native listAnswers/queryCandidates | `worktree-agent-a3300d1f70cebfdcf` @ 862947f | review |
| ft-fa1f7de7 notify late-lock flake | `worktree-agent-a649f8a7e5d9446fb` @ da0e4d3 | review |
| G12 ft-8cd27d93 (A2-5, B1-4, A1-8 only) | `worktree-agent-a0db7f71af6c87972` @ d50eb0a | in progress (B1-2 waits for wt-jibe) |

- **ft-5ddec968:**
  - Deleted 567 lines and added 7. No callers remain, checked two ways: `rg -uu` and DevMap.
  - `repairMirror` and `readMirrorOrJSON` were only reachable from the deleted ops.
  - Swift: 543 run, 0 failed. Extension: 1,186 pass, typecheck ok.
  - It had no separate adversarial pass: the change only deletes code, and its attack surface is "anything still
    calls it", which the two checks covered.
- **ft-fa1f7de7 (test-only):**
  - The fake app's lock attempt collided with `appHolds`' probe lock.
  - It failed 2 of 500 runs under load before the fix and passed 1,000 of 1,000 after.
- **G12:**
  - **A2-5:** a re-opened job now forgets its stale category.
  - **A1-8:** `retriage --dry-run` reads a snapshot and no longer takes the ledger lock.
  - **B1-4:** an audit answer now merges with existing slot labels.
  - **Adversarial pass:** it found the root cause behind B1-4. The eval label store had no read-modify-write across
    processes; eight concurrent `audit answer` calls all "won". `Store.Update` now does that read-modify-write under a
    flock on `labels.lock`, and `Put` goes through it. On-disk format is unchanged.
- **New flake folded into ft-2673557b:** `cmd/vani TestJobsInsightScalesTo5000Postings` fails under `-race` with heavy
  parallel load.

## Wave 3b merged; branches cleaned (2026-10-21)

- **Merged:** the three wave-3b branches (`3b363f8`, `5a8cd87`, `e5a0ab0`).
- **Checks after merging:**
  - Go: no FAIL.
  - Swift: 543 run, 0 failed.
  - Extension: typecheck ok, and `bun test` shows 1,191 pass and 0 fail.
- **Branches:** all agent branches are deleted. 27 were merged; the other 5 were patch-equivalent per `git cherry`.
- **Worktrees:** all agent worktrees are unregistered. Nine directories are left half-deleted (Finder `.DS_Store`). Removing them needs `rm -rf`, which the gate holds.

## Wave 3 — built off main e5a0ab0 on the owner's decision to build despite wt-jibe (not merged)

| card | branch @ sha | status |
|---|---|---|
| G2 ft-84d4fe25 daemon run lifecycle (+ G4 app.go:217 gap) | `worktree-agent-a985d9bc1b3106024` @ 329dcb5 | review |
| G7 ft-a1bb3507 prompt bounds + sensitive-field guard | `worktree-agent-ab77b5e2b6c8febd1` @ b5c86f6 | review |
| G11 ft-debfd7bf CLI/digest honesty | `worktree-agent-a9dcbc14b3f0729a5` @ 3c8e067 | review |
| ft-2673557b jobs mark/find perf | `worktree-agent-ab93511f45d208708` @ 8fbd552 | review (`find` <250 ms needs a quiet-machine rerun) |

- **G2:**
  - Config reload is now owned in one place, and process-lifetime fields (schedule, DevPrism binary and tools hash) are pinned with a note.
  - A dead DevPrism is replaced, at most once per window.
  - Panic recovery is bounded, and its text is length-capped.
  - A poisoned inbox poll stops the daemon.
  - A fatal error under cancellation is recorded as failed.
  - `publishDigest` drops from 2,000 fsyncs to 1.
  - Owner-marked closed records get their text back for the daily semantic warm-up.
  - Adversarial fixes: a FIFO config path no longer hangs the run; a DevPrism binary change now logs a note; batches split by bytes as well as count; an unreadable body is counted instead of skipped silently.
- **G7:**
  - Title, location and context are bounded in the kbmatch prompt.
  - The guard covers more phrasings and normalises labels structurally.
  - A word mixing alphabets now fails closed.
  - A model reading of a sensitive field never becomes a draft.
  - The adversarial pass first found 30 sensitive labels being drafted; all 30 are now caught.
  - Known misses: digits inserted inside a word ("c1tizen"), and a fully non-Latin lookalike word. Some false pins also happen (for example "military-grade"); they fail safe as a manual answer.
- **G11:**
  - `track show` no longer prints a false "check: ok", and `track use` refuses unless `--allow-unchecked`.
  - The rescore count is now honest.
  - `semantic warm` warns when the marks store is unreadable.
  - The digest has a "Coverage unknown" section.
  - Adversarial fixes: an empty words file no longer counts as a passed check; a posting dropped mid-pass is no longer counted as rescored.
  - The coverage-unknown check now has one owner, `ledger.CoverageUnknown`.
- **ft-2673557b:**
  - A single-row lookup used to re-learn the whole ledger (introduced by commit 3255839). It now learns only that company's postings, and parity tests prove the rows are byte-identical.
  - At 10k synthetic postings, min of 5 at load average 28–33: `jobs mark` went from 2,193 to 244 ms and `jobs find` from 2,094 to 255 ms. Allocations per call fell from 14.1M to 0.52–0.67M.
  - The scale test now counts allocations, and the adversarial pass tightened its bound to at most 4.1x.
- **Overlap at merge:** G2 and G11 both touch `internal/pipeline`, and G11 ran gofmt on `runner.go`. Expect a mechanical conflict there.
- **Manvi:** `devcouncil_verify_task` can't judge any of these yet. It says "no task is checked out; call devcouncil_checkout_task first".
- **Filed:** ft-0aa92138. The `vani semantic warm` CLI path doesn't hydrate marked closed records yet; build it after G11 merges.

## Deferred or blocked (and why)

| card | reason |
|---|---|
| G2 ft-84d4fe25 | wire.go, app.go and main.go are uncommitted in wt-jibe. Fable: building wiring twice is pure conflict tax. Now also carries the app.go:217 fix that G4 depends on. |
| G7 ft-a1bb3507 | fieldtriage and kbmatch are dirty in wt-jibe. Fable: wait until the stall is confirmed, because behaviour code rebased onto 92 unknown edits risks silent semantic merges. |
| G11 ft-debfd7bf | digest.go is dirty in wt-jibe. |
| G3 ft-b756b6a3, G12 ft-8cd27d93 | wave 4, after wave 3. |
| G14 ft-da6154b8 | wave 5. docs/FOLLOWUPS.md is dirty in wt-jibe, and the card now also carries this session's records. |
| ft-2673557b (new) | jobsinsight.go is dirty in wt-jibe. |
| ft-7099bd3b | your own card; not touched. |

**wt-jibe** (`tmp/wt-jibe`, branch `sources-live-audit`) has 92 uncommitted files and no live session. Its last edit
was 2026-10-20 at about 11:19. Fable classes it as *stall-suspected*; only you can confirm it is dead.

## Benchmarks

All data is synthetic, on an Apple M5 Pro (18 cores), go1.27.2. Load average was 7–21 throughout from other
sessions, so the minimum of each set of runs is quoted. None of these is presented as a speedup claim unless it is
labelled as a before/after pair.

| benchmark (scale) | baseline min | after min | note |
|---|---|---|---|
| ledger Open20k (20k records, 80% dead) heap-inuse delta | 100.7 MB | 34.3 MB | G4 c4. Target was ≤40%; got 34%. |
| ledger Open20k ns/op | 148 ms | 162 ms | +9.1%. Fable accepted it with justification: it runs once per process and saves 66 MB. |
| ledger Append1000 / AppendBatch1000 | 3.237 s / 4.48 ms | 3.349 s / 4.09 ms (G5); 3.056 s / 4.30 ms (G4) | both within run spread |
| pipeline ReconcileRun | 371.5 ms | 338.5 ms | G4 |
| lca Load vs LoadView (200k employers × 3 FY) | 947 ms, 4.80M allocs | 19 ms, 57 allocs | G4 c2. Index hashing is about 13 ms of that. |
| srcutil GetJSON (20 MiB body) | 26.3 ms, 76.8 MB/op | — | baseline only. Under the 32 MiB reopen trigger, so c5 stays declined. |
| jobs mark / jobs find (10k postings) | 1.09 s / 1.16 s | — | over 1 s, so fix card ft-2673557b was filed |

`jobs find` was 0.23 s on the stale base 0dbee8a and is about 1.2 s on main. The cause hasn't been profiled; it is
noted on ft-2673557b.

## New tasks filed

- **ft-2673557b:** jobs mark and find take over 1 s at 10k postings (backlog).
- **ft-fa1f7de7:** `internal/notify` `TestViaAppWaitsForALateLock` flakes under load. It is a wall-clock 30 ms race,
  passes 20/20 in isolation, and predates this work.
- **ft-5ddec968:** delete the native listAnswers and queryCandidates ops. G10 filed it after removing the extension
  side.
- Folded into existing cards:
  - **ft-84d4fe25 (G2):** a new criterion for the `app.go:217` `ledger.Full` wrap.
  - **ft-da6154b8 (G14):** seven new FOLLOWUPS records.

## Fable's decisions (this session)

1. **Wave-2 sequencing:** stack G10 and G9 on G8, build G5 off main, and use the fourth slot for G4's ledger and
   LCA half rather than G7.
2. **G1 pmset check:** left to you. A check that wasn't run is never reported as passing.
3. **Deferrals:** G14 and G7 are deferred this session.
4. **G6 constants:** kept at 200 / 50 / 3. The scored-stratum floor is near-unreachable at 150 labels by design
   (fail-closed), and that is documented in RANKING_PLAN.md.
5. **G5 marks line cap 8→16 KiB:** accepted; it is not a migration. The downgrade limit is recorded, and an
   invariant test pins it.
6. **G10 TypeScript 7.0.2:** accepted. It ships as `typescript` plus 20 optional platform packages, not the predicted
   single package. Opus verified that all 21 share one maintainer set and that the lockfile has 21 sha512 hashes.
   This is disclosed to you on the card.
7. **G4 c4:**
   - **Contract delta:** approved under delegation. `Record` gains `BodyEvicted`, and `All()` returns closed
     records without their description.
   - **Open +9%:** accepted.
   - **Merge gate:** the app.go:217 fix must land first.
8. **wt-jibe:** stall-suspected, not confirmed. G13 starts now; G2, G7, G11 and ft-2673557b wait. Stop and report
   after that.

## Owner decisions still pending

1. **Merge approval** for the nine branches above, in the order given, landing as stacks.
2. **wt-jibe:** land it, hand it over, or declare it dead. This unblocks G2, G7, G11, G14 and ft-2673557b, and the
   G4 merge through the app.go:217 fix.
3. **G1 criterion 3:** run a `pmset` sleep check across a scheduled minute and log it.
4. **G8:** install the fixed host, then rescan the bank for U+FFFD. The baseline is 0, so no repair decision is
   expected.
5. **Disclosures you may override:**
   - TypeScript 7 package shape (G10).
   - Marks downgrade limit (G5).
   - `ledger.Record` contract delta (G4).
   - Popup warning-icon behaviour change (G10).
   - Emoji ZWJ sequences now refused in resume file names (G10).
6. **Cleanup:** `.claude/worktrees/agent-a057e4d0143a0373f/benchbin/` couldn't be removed, because the gate holds
   `rm -r`. About 27 agent worktrees remain; remove them after merging.

## Process notes (verified)

- **Stale worktree base.** `Agent` worktree isolation cuts from `origin/main` (0dbee8a), which is 259 commits behind
  local `main` (d5c4097). The G1 and G15 numbers were first measured on the stale base and then redone on main.
  Every later worktree fast-forwarded first.
- **Held `git reset --hard`.** The blast-radius gate holds it for your approval. G5 lost about 40 minutes to it.
  `git merge --ff-only` is the non-destructive route that was used instead.
- **Worktree `go.work`.** `go.mod`'s relative `replace` lines for `decide` and `tessl` don't resolve at worktree
  depth. Each worktree used an uncommitted `go.work` with absolute replaces, and `go.mod` was never edited.
- **Tool gaps.**
  - GitPulse was rate-limited for most of the morning.
  - DevMap had no index inside agent worktrees. Agents fell back to `rg`, sometimes `-uu`, and said so.
  - `dev verify` was not run: DevCouncil gate mode is off, and the shell gate blocks git and go runs aimed at another
    worktree's path.
