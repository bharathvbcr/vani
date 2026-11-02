# Vani deep audit — 2026-11-01

This file replaces the 2026-10-26 audit that was at this path. That text is still in git history: `git show 1f7fe30:AUDIT_REPORT.md` (the last commit that changed this file; `main` at filing is `62a71a8`). The 2026-10-20 audit is `git show 9b4af91:AUDIT_REPORT.md`. The 2026-10-26 header says its 18 confirmed findings were implemented, regression-tested, and merged at `8ca5be6` on 2026-10-27. Those cards are still in `review`. "Claimed fixed" is not a 2026-11-01 verification. This audit marks a criterion met only where the cited lines were re-read. Nothing was moved to `done`.

No source, config, or dependency was changed. The only writes are GitPulse board writes and this file. `.gitignore` was already dirty and was not edited.

## How this audit was run

The harness has no model selector. Slice audits and the final grouping pass were general code-reader subagents. The parent re-read every cited line that a card now depends on. That re-read is the verification pass. DevMap and GitPulse Insights were named first in every slice prompt.

DevMap `devmap status --json` at the start: generation 2719, schema 29 current, source and analyzer freshness true, query ready, 20631 nodes, 83833 edges, pending 0, and **degraded**. Reason: partial call extraction, 0 parse failures, 4 HTML files pattern-recovered and call-blind, 12 Swift files partial-parse (declarations and calls inside ERROR regions are absent), including `macos/Sources/VaniApplyKit/Jobs.swift`. Go net resolution was about 670/1000, TypeScript about 411/1000, Swift about 477/1000. Directory targets `internal/pipeline` and `internal/egress` are not indexed. `cmd/` is not indexed. Empty or truncated graph answers were treated as unknown. Dead symbols: `walk_incomplete`, 200 shown of 282, zero at confidence 0.9 or above. Do not read that as "no dead code".

GitPulse Insights at the start: one worktree (this checkout, `main` at `62a71a8`), no live Claude or Codex session, no overlapping dirty source files. The only dirty path was `.gitignore`. In-flight risk on the filed findings: none. The two `in_progress` cards were not opened and not edited:

- `ft-7099bd3ba7e0b8f2f9622204b98f2c85` — Lappi v2 caller side
- `ft-8d8ca2ea609144eb5240c2b743487d58` — a clean clone of Vani cannot build

The board was re-pulled immediately before filing: 51 tasks, `has_more` false. Eleven new cards were then filed. No card was deleted or merged.

Tests were not run. Windows and Linux were not executed. Swift findings that DevMap only partially parses were confirmed by reading the source.

Slices audited: ATS sources (the post-2026-10-26 adapters, with an Avature body-cap finding), the extension fill and native-port path, the CLI run-record and run-request path, and the Mac Jobs export / same-question / schedule-test path. A second wave of auditors was not launched. The post-2026-10-26 delta is largely those four slices. Not re-audited, so not cleared: pipeline internals beyond the Digested reconcile spot-check, the egress SSRF card, kbsync, eval and rankfit, ledger durability, vault, semantic, employer and lca. Older `review` cards were not all re-verified.

## Severity × dimension

Counts are this audit's confirmed concerns after the source re-read. Rejected items are not in the table. Each concern is in one cell. A fold keeps the card's existing severity. The cell uses the new finding's severity.

| dimension | critical | high | medium | low | total |
|---|---|---|---|---|---|
| modular architecture | 0 | 0 | 0 | 0 | 0 |
| strict typing | 0 | 0 | 0 | 0 | 0 |
| defensive validation at trust boundaries | 0 | 1 | 0 | 1 | 2 |
| async error boundaries | 0 | 2 | 1 | 0 | 3 |
| deterministic testability | 0 | 0 | 0 | 1 | 1 |
| performance and benchmarks | 0 | 0 | 1 | 0 | 1 |
| correctness (Windows / macOS / Linux) | 0 | 0 | 2 | 3 | 5 |
| security | 0 | 1 | 0 | 0 | 1 |
| **total** | **0** | **4** | **4** | **5** | **13** |

High: fill readback (defensive validation), fill reentrancy and the job-call timeout (async), same-question pre-model skip (security). Medium: a claimed run request deleted on a failed link or a lost cancel (async), `run-once --json` exiting 0 when the finished record fails to save (correctness), Mac export ignoring hide-duplicates and clearing its lock before the save panel (correctness), export building the whole ledger for `already=ask` or `skip` (performance). Low: Avature reading up to 64 MiB before the 4 MiB check (defensive validation), CLI skill exit and `pending.us` wording folded onto the honesty card (correctness), Mac export summary omitting forgotten keys (correctness), a future applied time counted inside 60 days (correctness), the sibling SIGTERM test still racing the redirect (testability).

## Top 10 risks

1. **sameQuestion starts the helper for a label suggest would skip** (`ft-d3a66dc661884b666d61770cc4547221`). `SameQuestion.swift` puts both page labels into the vani-fm question and starts the helper with no `Guard` or `Sensitive` check. Suggest skips the model only when `Guard` is non-empty and `NeverDrafted` is non-empty. Work-authorization wording and the other Guard categories take that path. The reply is closed. Chrome and Safari both route here. Do not extend the skip to the broader sensitive category.
2. **Fill reports a success the control did not keep** (`ft-e1a4b3969c4ceae0175732570bd615f9`). The combobox judge treats a substring either way as a match, so a short token such as `us` matches a longer place token that merely contains those letters. `united states` does not contain `us` as a substring. Single-select `setSelect` and single `setGroup` return success without reading the control back. Multi-select already checks `shownAll`.
3. **A second fill clears the first fill's undo** (`ft-6bda9125003d1b0481ebee5d12c91fd9`). `FRAME_REPLY_MS` rejects the sender promise and does not cancel the handler. `beginFill` clears `lastFill` with no in-flight guard. The shortcut sends `fill` with no lock. Popup `busy()` covers only that one click.
4. **jobStart and jobResult are clamped to 5 seconds, and a timed-out write poisons the port** (`ft-0d14521b80329970c9227988ae1677a1`). `JOB_CALL_MS` is 10 seconds. `callDeadline` clamps it to `callTimeoutMs` (5 seconds). A timeout after `postMessage` poisons the connection. The host `callTimeout` is the same 5 seconds for start, result, identity, and reachable. The model does not block `start()`. The hang is the XPC or launchd hop. The host change is a separate start/result timeout, not a higher timeout for every XPC call.
5. **`run-once --json` can exit 0 while `last.json` is still the running record** (`ft-ae4f5feb67f75c7fdffcb0e172087ae2`). The running record is saved first. If the finished write fails, the command replies with a nil error. `omitempty` hides the failure. After the process exits, a later read can show interrupted because the pid is gone. The JSON already said success.
6. **A claimed run request can be deleted instead of put back** (`ft-5acb43c086f0fa5bb8b310c810ffb9fe`). The claim is always removed on the way out. Requeue uses `os.Link`. Any link error other than `ErrExist` returns, and the defer deletes the only copy (exFAT and some SMB shares). A signal that wins the cancel over a later config restart makes `dailyJob` return nil, so the claim is not requeued. A missing binary after `replaced()` is false, or a strict decode, can drop a still-valid claim. A genuinely corrupt request must still be dropped. A finished run must still not be requeued.
7. **Mac Jobs export writes the wrong rows and can start twice** (`ft-5ea4279b29486b93e6036f1274e2fb70`). Select-all uses the on-screen list, including hide-duplicates. Export uses `page.total`. `checkExport` clears `exporting` before the alert and the save panel return, so a second export can write a second file.
8. **Jobs export builds the whole ledger for `already=ask` or `skip`** (`ft-ec65683692bd5840c16a6371f8683e33`). With a non-empty export log, `BuildList` is called with `math.MaxInt` and then the first page is kept. Mac `exportCheck` omits `already`, so the default ask path is the one that matters. The cost was not measured. This card carries its own benchmark. No separate benchmark card.
9. **The CLI skill still mis-states two exits and `pending.us`** (`ft-debfd7bf523d636982476a176a6feb0c`, new criteria). `exitCode` maps `errConfigRestart` to 75 for any command, while the skill says 75 is daemon-only. `run-once` can hit that error with a nil daemon cancel and should exit 1. The daemon still exits 75. `run-request --json` turns a usage error into a plain error on purpose, because exit 2 means the binary is too old to the Mac app. Do not change that exit. `--company`, `--state`, and `--area` do not scope `pending.us`. That count is every awaiting posting located in the US.
10. **Avature listing reads up to 64 MiB and only then applies the 4 MiB cap** (`ft-79b7caa89cbf5abf3d09a8ae8b94d228`). `GetBody` uses the shared max. The adapter rejects afterward. A list error does not reconcile. The two remaining low findings are the Mac summary omitting forgotten export keys (`ft-1437c26b3015cf8aae566347870db37d`) and a future applied timestamp counting as inside 60 days (`ft-660c51c132695b9a363f675b1f8cfab7`).

## New cards

All new cards are `bug`, status `backlog`, label `audit-2026-11-01`.

| id | title | priority | severity | wave |
|---|---|---|---|---|
| `ft-e1a4b3969c4ceae0175732570bd615f9` | Fill must not report a success the control did not keep | 1 | high | 1 |
| `ft-0d14521b80329970c9227988ae1677a1` | jobStart and jobResult must use the job budget, not the 5s short-op clamp | 1 | high | 1 |
| `ft-ae4f5feb67f75c7fdffcb0e172087ae2` | run-once --json must not exit 0 when the finished run record fails to save | 1 | medium | 1 |
| `ft-d3a66dc661884b666d61770cc4547221` | sameQuestion must use the suggest pre-model skip before starting the helper | 1 | high | 1 |
| `ft-6bda9125003d1b0481ebee5d12c91fd9` | A second fill must not run during the first or clear its undo | 1 | high | 2 |
| `ft-5acb43c086f0fa5bb8b310c810ffb9fe` | A claimed run request is put back unless it finished or is genuinely corrupt | 2 | medium | 2 |
| `ft-5ea4279b29486b93e6036f1274e2fb70` | Mac Jobs export must write the on-screen list and stay single-flight through the save panel | 2 | medium | 2 |
| `ft-ec65683692bd5840c16a6371f8683e33` | Jobs export must not build the whole ledger for already=ask or skip | 2 | medium | 2 |
| `ft-debfd7bf523d636982476a176a6feb0c` | fold, see below | 2 | low | 3 |
| `ft-8f7be1583ae5e08890b97a5b4f4acab7` | fold, see below | 2 | low | 3 |
| `ft-79b7caa89cbf5abf3d09a8ae8b94d228` | Avature list must apply the 4 MiB cap before the read | 3 | low | 3 |
| `ft-1437c26b3015cf8aae566347870db37d` | Mac Jobs export summary must include forgotten | 3 | low | 3 |
| `ft-660c51c132695b9a363f675b1f8cfab7` | A future applied timestamp is outside the 60-day repeat-apply window | 3 | low | 4 |

Stable keys are `gp-audit-2026-11-01-*` as filed. The board item ids above are what `gitpulse_list_tasks` returns.

## Folded cards

Both stayed `review`. Priority and severity were not lowered. Titles were not changed.

- `ft-debfd7bf523d636982476a176a6feb0c` (revision 6). Title unchanged. Labels kept, plus `audit-2026-11-01`. Planned files now also include `cmd/vani/main.go` and `skills/cli-anything-vani/SKILL.md`. Three criteria added: run-once exits 1 rather than 75 when the daemon cancel is nil; the skill states the deliberate `run-request --json` usage-error mapping; the skill stops saying company, state, and area scope `pending.us`. The four older criteria were not re-read and were not marked met. Do not edit `internal/jobview/select.go`. Do not change the run-request exit. Do not edit `app.go` on this card.
- `ft-8f7be1583ae5e08890b97a5b4f4acab7` (revision 4). Title unchanged. Label `audit-2026-11-01` added (the card had none). The three existing criteria are marked `[MET — verified in source]` for the named test's empty-file window. Tests were not re-run. A fourth criterion was added: `testADaemonThatIgnoresSigtermIsKilledAfterTheGrace` must not proceed when the pid file appears if the script echoes the pid before the trap. Production `ScheduleAgent.stop` is unchanged. `ScheduleAgent.swift` stays on the plan so that constraint stays visible.

`related_tasks_exist` fired while filing because new titles share ordinary words (`must`, `run`, `list`, `apply`) and the shared `audit-2026-11-01` label. Each refusal was read. None of those cards were the same concern. Each new card was retried with `reviewed_related` naming every listed id. Nothing was folded across those refusals except the two folds above, which were decided before filing.

## Verified-stale cards

Still `review`. Not moved to `done`. Tests were not re-run on 2026-11-01.

- `ft-0f3ac31c525eeb33741f407caf0bb11c` (revision 5). Criteria 1–3 marked `[MET — verified in source]`. `daemonIsLive` requires an identity, this binary, and a kernel start not after `StartedAt` plus one second. `scheduleKeeping`, `requestRun`, and `archiveInterruptedRun` follow that check. `pidAlive` remains an existence check for `runState`. Criterion 4 was not marked met: the test files were seen, and not every assertion line was re-read. A Windows `GetExitCodeProcess` error after a successful open still returns true from `processExists`. That is deliberate and was not added as a criterion.
- `ft-94364021c96bc6f7427b6c0af5cce7ec` (revision 4). All three criteria marked met in source. `popup.html` has no inline `onclick`. `popup.ts` binds the import button. The binding test exists and was not re-run. Not folded into `ft-edfabd86`.
- `ft-f2c485911ee04f3e4f6b8ff03710cd57` (revision 4). All three criteria marked met in source. `fresh()` bumps `generation` and clears `inflight` before `request()`. The out-of-order test file exists and was not re-run.
- `ft-02a32a35fe28ae1664551c412941a273` and `ft-b31f157835e8b1fd943c80a825286ba2`. Criteria already say met. This audit's earlier re-read still matches: subprocess pipes are drained on their own threads and stop sends SIGTERM then SIGKILL; Digested postings are aged because `closed()` is Dead, Duplicate, or Dropped only, and retriage appends the key only after the ledger append. Tests were not re-run. These two cards were not rewritten in this filing pass.
- `ft-dcd139bfca29862ce97d8ade909d3b79`. `requestRun` takes the live-daemon check and the file lock, then writes only if the request is still absent. That covers the two-success race. `optionsMatch` not comparing companies, states, and US-first was not re-read. The card was not marked met and was not rewritten.
- `ft-e97abbbf30ed5b366274a269a2b658ed`. Page JSON-LD selection is URL-matched, including talentbrew. Not reopened for the duplicate `maxGone` helpers. Not rewritten.

## Build order

No shared planned files inside a wave. Folds are content updates already applied. Their file ownership still sequences the build.

Wave 1:

- `ft-e1a4b3969c4ceae0175732570bd615f9` — `extension/src/fill.ts`
- `ft-0d14521b80329970c9227988ae1677a1` — `extension/src/nativeport.ts`, `extension/src/background.ts`, `macos/Sources/VaniApplyKit/Agent.swift`
- `ft-ae4f5feb67f75c7fdffcb0e172087ae2` — `cmd/vani/app.go`, `cmd/vani/main.go`, `cmd/vani/jsoncli.go`
- `ft-d3a66dc661884b666d61770cc4547221` — `macos/Sources/VaniApplyKit/SameQuestion.swift`

Wave 2, after wave 1 because `fill.ts`, `background.ts`, and `app.go` overlap:

- `ft-6bda9125003d1b0481ebee5d12c91fd9` — `frames.ts`, `content.ts`, `background.ts`, `popup.ts`, `fill.ts`
- `ft-5acb43c086f0fa5bb8b310c810ffb9fe` — `cmd/vani/runrequest.go`, `cmd/vani/app.go`
- `ft-5ea4279b29486b93e6036f1274e2fb70` — `JobsView.swift`, `JobsModel.swift`
- `ft-ec65683692bd5840c16a6371f8683e33` — `cmd/vani/jobsexport.go`, including the benchmark

Wave 3, after wave 1 because `main.go` overlaps the run-record card:

- `ft-debfd7bf523d636982476a176a6feb0c` — `cmd/vani/main.go` and `skills/cli-anything-vani/SKILL.md` for the new criteria. Do not edit `select.go` or `app.go` here.
- `ft-8f7be1583ae5e08890b97a5b4f4acab7` — the schedule-agent test only. Production stop stays as it is.
- `ft-79b7caa89cbf5abf3d09a8ae8b94d228` — `internal/sources/avature/avature.go`
- `ft-1437c26b3015cf8aae566347870db37d` — `macos/Sources/VaniApplyKit/Jobs.swift`

Wave 4:

- `ft-660c51c132695b9a363f675b1f8cfab7` — `internal/samecompany/index.go`. Do not edit `quiet.go`.

The export scan card's own benchmark is the baseline. It was not folded into `ft-2a0aff5427a893bd2d218b958dbf01ec` or `ft-d30b8cdcf63c8f4bcf772dd775455dc8`.

## Rejected findings

Not filed.

- Duplicate `maxGone` helpers in `apple.go` and `eightfold.go`. The formulas match `srcutil.MaxGone`. `docs/FOLLOWUPS.md` already records the drift. `ft-e97abbbf` criterion 7 already covers the shared helper. No current numeric mismatch.
- `advance_us_first` does not drop or double-count. Company and place run flags are enforced.
- The page JSON-LD first-`JobPosting` bug is fixed. Talentbrew is URL-matched. A schema.org type-URI miss was not filed.
- A label in the broader sensitive category still reaches the field model. `Guard` does not include that pattern. The suggest skip is `Guard` non-empty and `NeverDrafted` non-empty, so the `&&` is the documented split, not a bug. The draft is still blocked afterwards by `Sensitive`. Do not file a change that stops sending those labels.
- "Reached furthest" is shown only when the stage is terminal. That gate is explicit in the UI. The store keeps the furthest mark across rejected and withdrawn. Not a defect.
- Windows `processExists` returns true when `GetExitCodeProcess` fails after a successful open. The comment says the process is there because it opened. `daemonIsLive` does not treat that as this daemon.
- Documented `docs/FOLLOWUPS.md` limits, left as written: SmartRecruiters robots, `joburl.IDParams` case, Eightfold and Apple not on `PlanDetails`, IBM / DE Shaw / Atlassian one-request lists, TikTok near `MaxPostings`, the Bloomberg pay line, Workday stub rows, autofill same-question green-from-12-pairs, Safari `sameQuestion` not exercised in Safari, an edited draft staying yellow, and migration crash re-merge.
- A successful requeue does not double-run. `run-request --json` success means queued. `clearClaimedRequest` at daemon start drops a leftover claim by design.
- No `http.Get` or `http.DefaultClient` under `internal/sources`. A failed `List` does not reconcile.

## Decisions made without asking

- Roles were briefs. There is no model picker, so "Sonnet", "Opus", and "Fable" were the parent plus code-reader subagents. The parent did the line re-read.
- Code-reader subagents cannot write files. Slice reports exist only in their final messages.
- A second auditor wave was not launched. The uncovered subsystems are named above.
- Combobox substring matches and the unread single-select / `setGroup` path are one card. Multi-select already reads the selection back and must not regress.
- The three run-request loss paths (link error, signal winning the cancel, disappearing binary after `replaced()` is false) are one card.
- Hide-duplicates and the export flag clearing before the save panel are one card.
- The host timeout for `jobStart` and `jobResult` is a planned edit on the timeout card, as a separate start/result timeout. The shared 5 second identity and reachable timeout stays.
- No separate benchmark card. The export-scan card carries the benchmark.
- `ft-0f3ac31c` criteria 1–3 were marked met in source even though that rewrite sits outside the build waves. Criterion 4 was left unmarked.
- `ft-02a32a35` and `ft-b31f1578` were not rewritten. Their criteria already say met, and this filing pass did not add a note.
- `ft-dcd139bf` was not marked met as a whole.
- The four older honesty criteria on `ft-debfd7bf` were not marked met.
- `related_tasks_exist` matches on shared title words plus the audit label were treated as separate work and filed with `reviewed_related`.
- The broader-category skip, the furthest-stage display, the duplicate `maxGone` helpers, and the Windows exit-code residual were not filed.
- The two prior off-board security items were not re-filed.

The grouping pass returned no disagreement with this set.

## Owner decisions needed before building

Recorded here. Not filed as cards. Not decided by this audit.

- The broader sensitive category may still reach the field model, on-device first and the cloud fallback if it abstains. The same-question card must not extend the pre-model skip to that category. The essay-plus-consent exception stays.
- Two items were left off the board in the prior audit and stay off: an XPC team check, and the API key in the daemon environment. Re-file either only with a new product decision.
- Show "Reached furthest" before a terminal stage only if that display is wanted. The current gate is intentional.
- The 2026-10-21 decision on the honesty card stands: `track use` refuses unless `--allow-unchecked`. This audit did not reopen it.

## Stop

No building was started. Waiting for a go-ahead.
