# Vani build report — 2026-10-21

Backlog worked in AUDIT_REPORT.md's wave order (waves 1–2 complete, wave 3 partly started). Every card below went
through the same loop: an Opus brief, a Sonnet build in its own worktree, and a separate Sonnet adversarial pass. Any
break went back for a fix. Opus reviewed every diff, and Fable made the judgement calls.

**Nothing is merged to `main`.** Merging needs your approval. Every result lives on a worktree branch under
`.claude/worktrees/`, listed with its final commit. Done is evidence-gated by Manvi, so every card sits in **review**.
None was moved to done.

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
