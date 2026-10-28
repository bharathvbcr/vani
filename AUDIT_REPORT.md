# Vani deep audit — 2026-10-26

> **Remediated & Merged 2026-10-27:** All 18 confirmed findings (19 tasks) from this audit were implemented,
> regression-tested with adversarial test suites, reviewed in `BUILD_REPORT.md`, and merged into `main` at `8ca5be6`.

Repo `/Users/bharath/Code/apps/Vani`, `main` @ `70fc33e`. No source, config, or dependency was changed. The only writes are GitPulse board writes and this file. The 2026-10-20 audit that was at this path is committed at `9b4af91` (`git show 9b4af91:AUDIT_REPORT.md`).

The board was re-read immediately before filing (35 open, `has_more` false) and again after (50 open, `has_more` false). Nothing was moved to done. The two in-progress cards were not edited.

## Headline

- Eight read-only slice audits, then a line-by-line check of every cited site. Confirmed findings were grouped into **15 new cards** and **4 folds**. No critical finding.
- Highest new risks: a reused daemon pid looks alive, the first JSON-LD `JobPosting` wins, a bank counter above `Int32.max` traps after `bank.json` is written, and an idle pause closes Tessl while `EmbedTexts` can still be in the binding.
- Two security items were left off the board on purpose (XPC team check, API key in the daemon environment). They are owner decisions below.
- No new benchmark card. The one performance finding without a Swift measurement was folded onto `ft-bafd7500` as a measurement criterion.

## Severity × dimension

Counts are this audit's confirmed concerns after verification. Rejected items are not in the table. Each concern is in one cell. A fold that already had a higher severity keeps that severity on the card; the cell uses the new finding's severity.

| dimension | critical | high | medium | low | total |
|---|---|---|---|---|---|
| modular architecture | 0 | 0 | 0 | 0 | 0 |
| strict typing | 0 | 1 | 0 | 0 | 1 |
| defensive validation at trust boundaries | 0 | 1 | 1 | 0 | 2 |
| async error boundaries | 0 | 1 | 0 | 1 | 2 |
| deterministic testability | 0 | 0 | 0 | 0 | 0 |
| performance and benchmarks | 0 | 0 | 1 | 0 | 1 |
| correctness (Windows / macOS / Linux) | 0 | 1 | 6 | 1 | 8 |
| security | 0 | 0 | 4 | 0 | 4 |
| **total** | **0** | **4** | **12** | **2** | **18** |

High: daemon pid identity (correctness), JSON-LD node choice (defensive validation), Int32 bind (strict typing), idle embed lifetime and deadline backoff (async). Medium security: symlink leaf bypasses, spaced contact in an instruction, phone-shaped bullet text into kbmatch, `last.json` FIFO/symlink read. Medium correctness: popup import, run history, concurrent run request, rekey metadata, Windows resume fit, bank memo generation. Medium defensive: bank file read before the 50 MB cap. Medium performance: edit and activity log still on the main actor. Low: panic after `guardedRun` (async), Windows binary watch (correctness).

## Top 10 risks

1. **Daemon pid identity** (`ft-0f3ac31c`). `EPERM` and `ERROR_ACCESS_DENIED` count as alive, and a same-user pid that is not this binary also counts. Status says the schedule is kept and omits Unkept. A running `last.json` with that pid is not archived. `requestRun` queues a request the other process never reads. The lie lasts until a new `vani daemon` overwrites `daemon.json` (`cmd/vani/main.go:552`) without asking `liveDaemon`. The other process does not run the request.
2. **First JSON-LD JobPosting wins** (`ft-e97abbbf`, new criteria). `findTyped` returns the first `JobPosting` and does not walk nested objects other than `@graph`. An expired sibling drops the page; a different job's title can be stored under this URL. Two full-board misses then age the posting Dead.
3. **Int32 trap after the JSON write** (`ft-d07a7b65`). `BankValidator.count` allows up to `9_007_199_254_740_991`. `sqlite3_bind_int(Int32(uses/fills))` traps above `Int32.max`. `bank.json` is already written; `do/catch` does not catch a trap, so later saves die on the mirror. A counter that only increments from zero does not get there. A webpage path was not shown.
4. **Close during EmbedTexts** (`ft-9b6a727a`). `callEmbed` returns on `ctx.Done` without joining. `window` then closes the model, and only then signals `Pause`. The comment says the pause wait covers Tessl finishing. A crash was not observed.
5. **A chunk deadline is treated as a failed model** (same card). `ensureProfile` failing inside `idleChunkTime` (2 minutes) with zero scores counts toward `idleBadChunks` (3) and can back off for up to 4 hours. Scores stay absent. Whether this owner's bank exceeds 2 minutes was not measured.
6. **Spaced phone and email in an instruction** (`ft-c6f80c88`). `phoneMaxSeparator` is 2, so `555 - 123 - 4567` is not a number. `emailLike` requires non-space bytes beside `@`. The text is sent to the model and stored on the version record. `2019-2024` and `2019–2024` must still pass.
7. **Import never opens the picker** (`ft-94364021`). The Import button is an inline `onclick` inside the label. Manifest V3 drops it. Export is bound from script and works.
8. **Three leaves still `os.Open` a symlink** (`ft-22d7da5f`, new criteria). Impressions can `Truncate` the target. Marks and manual-triage can `WriteAt` a swapped leaf. A symlinked `readings.jsonl` is applied as manual rank context. Same-user. The 2026-10-21 design (replace or refuse the leaf; do not write through it) stays.
9. **`last.json` follows a symlink and blocks on a FIFO** (`ft-f2cc5108`). `readRunRecordFile` and the daily snapshot use `os.Open` / `os.ReadFile`. `readBoundedFile` already refuses a non-regular file. Same-user stall or a parse of the symlink target.
10. **Bullet text still carries phone-shaped runs and work-authorization phrases** (`ft-948c9eb6`). `cleanBulletText` strips claim tokens, markdown, URLs, and emails. Those two shapes are embedded and copied into the kbmatch question. Structured contact fields are not read. Organisation names stay, as the kbmatch comment says.

Next in line, not in the ten: a failed history append drops the finished run record (`ft-17728c6f`); two concurrent `requestRun` calls can both succeed (`ft-dcd139bf`).

## New cards

All `kind: bug`, status `backlog`, label `audit-2026-10-26`. Priority 0 is urgent and 3 is low.

| item_id | title | severity | priority | wave |
|---|---|---|---|---|
| `ft-0f3ac31c525eeb33741f407caf0bb11c` | EPERM and ERROR_ACCESS_DENIED leave a foreign pid looking alive | high | 1 | A |
| `ft-d07a7b653eaab4c8e2d490e3a4b6d6ab` | SQLite Int32 bind traps on oversized uses and fills counters | high | 1 | A |
| `ft-9b6a727a79db1269ffdb98722f6c0c36` | Idle pause Closes Tessl while EmbedTexts is still inside the binding | high | 1 | A |
| `ft-94364021c96bc6f7427b6c0af5cce7ec` | Popup import control inline onclick never reaches the picker | medium | 1 | B |
| `ft-c6f80c883e69566801e309d55d20b351` | checkInstruction skips a spaced handset and a spaced mailbox | medium | 1 | B |
| `ft-f2cc510849955a5c723c438dc3967775` | Opening last.json follows a symlink and blocks on a FIFO | medium | 2 | B |
| `ft-17728c6f60b7012ddc69531f679e98f0` | History append failure discards the finished bookkeeping | medium | 2 | C |
| `ft-dcd139bfca29862ce97d8ade909d3b79` | Concurrent requestRun writers both return success | medium | 2 | C |
| `ft-0c907f4e66a33511458a7833c560a47a` | Rekey renames a damaged resume record that readMeta rejects | medium | 2 | C |
| `ft-b1d3e1badb3eab6c80ed6cf04e92e043` | procgroup_other and bare pdftotext miss Win32 | medium | 3 | C |
| `ft-2a67d27fa725e7fdf0321ddb0717c4fc` | Data(contentsOf:) precedes the fifty-megabyte validator cap | medium | 3 | D |
| `ft-948c9eb6753acd2a05c29175ad1e8b41` | Bullet prose embeds nanp-shaped groups and work-authorization phrases | medium | 2 | D |
| `ft-f2c485911ee04f3e4f6b8ff03710cd57` | fresh() loses the generation race to an older get() | medium | 2 | D |
| `ft-b7dacb8fdc271946432973e83c24bf49` | Identical size and mtime hide a replaced Windows executable | low | 3 | D |
| `ft-10c5bf90ab4e6ee33fbacd36d430baea` | FRAME_PROBE_MS abandons the native port before callTimeoutMs | low | 3 | E |

`ft-0f3ac31c` was filed with `reviewed_related` naming `ft-bafd7500` (shared title word `error`, plus the audit label just added to that card). `ft-f2cc5108` was filed with `reviewed_related` naming `ft-e97abbbf` (shared title word `json`). Both were read first. They are different work.

## Folded cards

Each fold sent the existing title, description, and criteria plus the new section. Status moved from `review` to `backlog` because the new criteria are unmet. Priority and severity were not lowered. Logs were left in place.

| item_id | what was added | severity | priority | revision |
|---|---|---|---|---|
| `ft-e97abbbf30ed5b366274a269a2b658ed` | JSON-LD: pick the node whose url/sameAs is this page; walk `mainEntity`; keep the single-expired skip | high (unchanged) | 1 | 9 |
| `ft-22d7da5f2735997b8bbdb7934bc1345c` | Impressions, marks reopen, and `readings.jsonl` still follow a leaf | medium (unchanged) | 2 | 4 |
| `ft-84d4fe2545e64bdaa9678e18b45a5e6d` | A panic in `RunReport` after `guardedRun` leaves `last.json` running and the cursor advances | medium (unchanged) | 1 | 7 |
| `ft-bafd750094720ac26a73280954b5c717` | `store.update` and `recentActivity` still run on the main actor, with a measurement criterion | **medium, was low** | **2, was 3** | 5 |

On `ft-bafd7500`, criterion B2-2 is prefixed `[MET — verified in source]` for the off-main tick read at `AppModel.swift:465-473`. The 50-tick counter test was not re-run. B2-3, B2-4, B2-5, and B2-10 were not re-read and were not marked met.

On `ft-22d7da5f`, criteria 1–5 were not marked met. Their text still says a write through a symlink updates the target. The 2026-10-21 build did the opposite for program-named leaves: `Write` replaces the link, `OpenAppend` returns `ErrLinkedLeaf`. That design stays. The new criteria are the callers that bypass it.

On `ft-e97abbbf`, criterion 7 (Eightfold / Apple max-gone) was not marked met. The sources reader cited `eightfold.go:283-297` and `apple.go:135-149`. Those lines were not re-read in the parent pass.

## Verified-stale cards

"Stale" here means the card's log says the work is built and merged, and the card is still `review` because Manvi has not verified it. None of these were moved to done. Criteria were not bulk-prefixed `[MET]` unless this pass re-read the lines.

| item_id | what the log says | what this pass did |
|---|---|---|
| `ft-e97abbbf` | c1–c6 met; c3 partial on purpose (http off the default port); c7 met in the log | Folded. c7 checkbox left as written |
| `ft-84d4fe25` | lifecycle criteria met, merged `9f065bc` | Panic-inside-`guardedRun` re-read (`app.go:199-208`). Tail panic is the new criterion. Checkboxes left as written |
| `ft-22d7da5f` | c1–c5 met under the replace/refuse design, merged `2fcac90` | Did not rewrite the follow-the-link checkbox text |
| `ft-bafd7500` | B2-* met, merged `163e15e` | Only the tick-read half of B2-2 re-read |
| `ft-c9ff7380` | wait-until slices met | c3 `pmset` is the owner's harness. Not run |
| `ft-d74590ed` | CONNECT / NAT64 | No new SSRF hole found. Not re-marked |
| `ft-3189102e` | c3 C++ / C# / .NET distinct | c2 `me.uk` / `myworkdayjobs` still the recorded partial |
| `ft-3d83fba9` | c2/c4 log says built | Checkbox text may still say not met. Not marked. No stale LCA/employer view found |
| `ft-da6154b8` | FOLLOWUPS bookkeeping | `docs/FOLLOWUPS.md` still says `myworkdaysite.com` is unread; `extension` crawler code accepts it. Docs drift, not re-filed as a bug |
| `ft-6dc4d776` | UTF-8 chunking | U+FFFD rescan of the live bank is still an owner step. The bank was not read |
| `ft-1045267a` | portability | c5 device flag and c7 extension-id / parent signature stay declined |
| `ft-b6b00b08` | storage gaps | Windows rekey refusal is already this card. The new rekey card is the metadata check |
| `ft-a1bb3507` | prompt bounds | Digit-inside-word stays here. Bullet phones are the new card |
| `ft-8d8ca2ea` | clean clone cannot build | **in progress**. Not edited. Owner decision stands: pin published tags, no vendoring |
| `ft-7099bd3b` | Lappi v2 caller | **in progress**. Not edited. Fixture sha stays |

Many other `review` cards from 2026-10-19 and 2026-10-20 have the same shape (log says merged, Manvi has no verification record, often because no API key). They were not re-opened and not completed.

## Build waves

No two cards in one wave edit the same file. Shared files are sequenced: `runrequest.go` and `runhistory.go` A then C, `app.go` B then C then E, `BankStore.swift` A then D, `AppModel.swift` D then E.

**Wave A.** `ft-0f3ac31c` (pid files, `schedulekept.go`, `runrequest.go`, `runhistory.go`). `ft-e97abbbf` (`jsonld.go`, `page.go`). `ft-d07a7b65` (bank validator, SQLite mirror, `BankStore.swift`). `ft-9b6a727a` (`semantic.go`, `idlewarm.go`, `matcher.go`, `tessl.go`).

**Wave B.** `ft-22d7da5f` (`feedback.go`, `marks.go`, `manualtriage.go`). `ft-94364021` (`popup.html`, `popup.ts`). `ft-c6f80c88` (`redraft.go`). `ft-f2cc5108` (`runrecord.go`, `app.go`).

**Wave C.** `ft-17728c6f` (`app.go`, `runhistory.go`). `ft-dcd139bf` (`runrequest.go`). `ft-0c907f4e` (`rekey.go`). `ft-b1d3e1ba` (`procgroup_other.go`, `build.go`).

**Wave D.** `ft-2a67d27f` (`BankStore.swift`, `DraftSettings.swift`, `AppModel.swift`, `SQLiteBankStore.swift`). `ft-948c9eb6` (`text.go`, `kbmatch.go`). `ft-f2c48591` (`bankcache.ts`). `ft-b7dacb8f` (`binarywatch_windows.go`, `binarywatch.go`).

**Wave E.** `ft-bafd7500` (`AppModel.swift`). `ft-84d4fe25` (`daemon.go`, and `app.go` if the tail is folded into `guardedRun`). `ft-10c5bf90` (`background.ts`, `nativeport.ts`).

## Rejected findings

- **MAC-4.** XPC accepts any binary signed by team `HAUHVD8M37` (`Agent.swift` client requirement, set from `main.swift`). Same class as declined D18. Not filed.
- **MAC-5.** `OPENROUTER_API_KEY` is copied into the daemon child on purpose (`ScheduleAgent`, `withDecisionKey: true`). A same-user process can already read a same-user environment. Not filed.
- **atomicfile.Write replacing a program-named symlink.** Reviewed design from 2026-10-21. Not reverted. Callers that still follow the leaf were filed on that card.
- **Date-only `validThrough` as UTC end of day.** Pinned by `TestExpiredPostingsAreSkipped`.
- **Ticker sleep delaying polls.** It delays. It does not skip the daily slot. The slot skip was `ft-c9ff7380` and is a separate, already-built wait.
- **Empty ATS listing aging, Windows `SyncDir` no-op, `MinOpenRouterProb` 0 never drops.** Known or by design.
- **`javascript:` next controls.** Clicked on purpose.
- **SQL quote-doubling in the label delete.** The key is already constrained. Not a finding.
- **Prototype pollution, personal data in extension logs.** Not found.
- **Config writers bypassing `UpdateWithDeprecations`.** Not found. Production writer is `cmd/vani/main.go` via that function.
- **New vault-allowlist hole, D17, D18.** Not found, or already declined.
- **Digit-inside-word evasion.** Stays on `ft-a1bb3507`.
- **GetJSON streaming.** Declined unless a board body hits 32 MiB.
- **Employer / LCA hash-bound drift.** Not found. Both readers cap at `1<<30`.
- **Unsure reading used as a drop.** Not found.
- **PS-1 "the other process executes the run" and "the daily slot never fires".** Narrowed. The request sits. A new daemon overwrites the announcement.
- **SD-2 "this bank exceeds 2 minutes".** Not measured.
- **SD-1 "a crash was observed".** Not observed. The defect is the overlap.
- **Auditor priorities 4, 28, 48, 55, 62.** Not the board's 0–3 scale. Clamped.

## Decisions made without asking

- The spawn tool has no model argument. Slice audits ran as read-only code-reader subagents. This session verified the cited lines and did the grouping. That is the Sonnet / Opus / Fable split the request named, on the models this session can actually start.
- New cards are `backlog`, not `ready`. Building waits for a go-ahead.
- Label on every new card and every fold: `audit-2026-10-26`.
- Folds moved `review` → `backlog` because the added criteria are unmet. That is not a lower priority or severity.
- `ft-bafd7500` raised from priority 3 / low to priority 2 / medium. The measurement criterion is the benchmark. No second benchmark card. `ft-d30b8cdc` and `ft-2a0aff54` were not reopened.
- Only B2-2 was prefixed met, and only for the lines re-read. The follow-the-link checkboxes on `ft-22d7da5f` were not marked met. Criterion 7 on `ft-e97abbbf` was not marked met.
- `ft-7099bd3b` and `ft-8d8ca2ea` were not edited. No card was deleted or merged.
- Idle warm lives in `cmd/vani/idlewarm.go`, not under `internal/semantic`. The card cites the file that is in the tree.
- `jdtriage/fixture.json` was not touched. `go.mod` was not touched.

## Owner decisions needed before building

These are not ready work. Building the other cards does not depend on them.

1. **XPC peer check (MAC-4).** Today any other binary you signed with team `HAUHVD8M37` can connect. Tightening it to this app's identity is the same product choice as declined D18. Say if you want a card.
2. **API key in the daemon child (MAC-5).** The schedule agent passes `OPENROUTER_API_KEY` because the daemon reads it from the environment. A different channel (a pipe, a one-shot file) is a product choice. The current path is tested.
3. **`ft-c9ff7380` criterion 3.** The `pmset` sleep check is yours to run. It was not run.
4. **`ft-52b6e7d3`.** v0.1.0 still needs you to decide signing, publish, and rollback, and to push.
5. **`ft-8d8ca2ea` (in progress).** A clean clone cannot build until sibling repos are tagged. The recorded decision is published versions, no vendoring. Do not edit `go.mod` from this audit.
6. **`ft-6dc4d776`.** Whether a past bank was split on a UTF-8 boundary is unknown. Do not install over `/Applications/Vani Apply.app` and do not read the live bank to find out, unless you ask for that.
7. **`ft-7099bd3b` (in progress).** Lappi MinHash, `shadow_every`, the decide identity pin, the fixture bump, and the label set stay on that card. Do not change `internal/decide/jdtriage/fixture.json` without a retrain.
8. **D17 and D18** on `ft-1045267a` stay declined.

## Map notes

DevMap at the start of the audit: generation 2612, schema 27, queryable, not degraded. Dead-symbol rows were truncated at confidence 0.4 and were not treated as dead. HTML is call-blind, so empty caller lists there are unknown. Package-level dependency queries are not a file target. GitPulse insights on the later slices: one worktree (`main`), no other live session, no overlapping dirty files. The collisions facet timed out on two early slices; those empty overlap lists were not treated as clean. Insights were not re-pulled before filing because this pass did not edit source.

## Stop

No code was changed. Say when to start a wave.
