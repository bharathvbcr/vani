# Vani deep audit — 2026-10-20

Repo `/Users/bharath/Code/apps/Vani`, `main` @ `d5c4097`, clean worktree. Read-only audit: no source, config or
dependency was changed. The only writes were GitPulse board writes and this file (uncommitted). This file replaces
the 2026-10-19 audit report that was committed at this path; that version is intact in git
(`git show 365a405:AUDIT_REPORT.md`), so restore it or keep both under different names.

Pipeline: six Sonnet auditors (≤4 at once, two waves) → three Opus verifiers → Fable advisor (grouping, priority,
fold-vs-new, waves) → board filing. Working files (findings, verifier reports, Fable plan) are in the session
scratchpad: `…/scratchpad/audit/{A1,A2,A4,B1,B2,B3}-*.md`, `VERIFY-1.md`, `VERIFY-3.md`, `FABLE.md`, and the sibling
dir `…-Vani-d56b4dec…/scratchpad/audit/{A3-decide-semantic-eval,VERIFY-2}.md`.

Slices: A1 cmd/vani run/daemon wiring · A2 pipeline/posting/ledger/marks/digest/jobview/jobinsight · A3
decide/semantic/rankfit/eval/kbsync/resume · A4 egress/sources/config/vault/lca/employer · B1 other cmd/vani commands ·
B2 macOS Swift · B3 browser extension.

## Headline

- **59 raw findings → 58 distinct** (A1-6 = B1-1). After Opus verification: **2 high, 8 medium, 43 low, 0 critical**;
  4 rejected, 1 turned into an owner question (A2-4).
- **One data-loss bug (B2-1):** the Mac host splits the answer bank into 500,000-byte chunks at raw byte offsets, so a
  multi-byte character on a boundary becomes U+FFFD. The JSON stays valid, so nobody notices, and the extension writes
  the damaged bank back on the next edit. Reproduced with `swift -e`. This applies to banks over 750 KB with
  non-ASCII text. **Whether it has already happened to the live bank is unknown.** The audit did not read the bank
  (personal data); see owner decision 1.
- **One product-loop bug (A1-5):** the daemon's scheduler sleeps on a timer that doesn't advance while the Mac sleeps.
  A laptop asleep across 08:30 for more than about 6 h skips the daily run as "missed", every day. Verified from Go
  runtime source; not reproduced on hardware.
- **Board:** 13 new cards and 2 folds. Every one of the 13 `review` cards from audit-2026-10-19 got a verification
  section, with each criterion marked MET, NOT MET or declined. Two review cards had unmet criteria and moved to
  backlog: ft-3d83 had three unmet and unrecorded; ft-e97a's detail-404 fix was only half done. Nothing was moved to
  done. The board now holds 27 cards.

## Severity × dimension (final, after verification; rejected excluded)

| dimension | critical | high | medium | low | total |
|---|---|---|---|---|---|
| modular architecture | 0 | 0 | 0 | 0 | 0 |
| strict typing | 0 | 0 | 1 (B3-4) | 0 | 1 |
| defensive validation at trust boundaries | 0 | 0 | 1 (A4-1) | 8 | 9 |
| async / error boundaries | 0 | 0 | 2 (A1-2, A1-3) | 10 | 12 |
| deterministic testability | 0 | 0 | 0 | 0 | 0 |
| performance & benchmarks | 0 | 0 | 1 (A2-1) | 6 | 7 |
| correctness (macOS/Linux/Windows) | 0 | 2 (A1-5, B2-1) | 3 (A1-1, B1-1, B3-1) | 14 | 19 |
| security | 0 | 0 | 0 | 5 | 5 |
| **total** | **0** | **2** | **8** | **43** | **53** |

Lows by dimension: validation A2-3, A2-10, A3-4, A4-2, B1-3, B2-5, B2-7, B2-8; async/error A1-4, A1-9, A1-10, A3-6,
A3-8, B1-7, B1-8, B2-4, B2-9, B2-10; performance A2-2, A4-4, A4-5, B1-6, B2-2, B2-3; correctness A1-8, A2-5, A2-6, A2-7,
A2-8, A2-9, A3-1, A3-2, A3-3, A3-9, A4-3 (page half), A4-6, B1-2, B1-4; security A3-5, A3-10, B1-5, B3-2, B3-3. No
security finding is a page-reachable exposure or a personal-data leak once verified: A3-5's guard gaps leak no
applicant data, B3-2 isn't reachable by web pages, and B1-3 is CLI-only. Testability gaps show up inside the cards as
missing tests (e.g. the B3-1 route, ASCII-only protocol tests). No architecture finding met the evidence bar.

## Top 10 risks (Fable ranking)

1. **B2-1** — silent, persistent corruption of the answer bank (>750 KB, non-ASCII); it may already have happened. → ft-6dc4d776
2. **A1-5** — a sleeping Mac never gets its scheduled run, so the core loop is off on a normal laptop. → ft-c9ff7380
3. **B3-1** — the resume PDF (whose file name carries the owner's name) can be attached in whichever ATS tab is
   active, and the owner is told it was *not* attached. This is live on the fallback path. → ft-60d1dc8e
4. **A1-2** — one DevPrism death makes every daily run fail until a human restarts the daemon. → ft-84d4fe25
5. **A1-1** — exclusions saved in Mac Settings are ignored by the daemon until a restart nobody is told to do. → ft-84d4fe25
6. **B1-1 / A1-6** — Settings edits and extension save_search can erase each other with exit 0, across a 10 s network window. → ft-b756b6a3
7. **A4-1** — one malformed `allowed_hosts` entry stops every run while `config check` prints ok. → ft-b756b6a3
8. **ft-3d83** — a "review" card with three criteria unmet and unrecorded; ledger resident memory grows with every key ever seen (A2-1). → ft-3d83 (reopened)
9. **B3-4** — there is no in-repo typecheck, HEAD already fails tsc, and the mapped-type guard that should have
   caught B3-1 can't run. → ft-60d1dc8e
10. **Process: the wt-jibe lane** — 8 of 15 cards wait on a 92-file uncommitted branch that also carries pre-split
    copies of code these cards change (wire.go lazyScorer, the srcutil retry path).

## Cards filed

### New (13) — label `audit-2026-10-20`

| group | item_id | title (short) | prio | sev | status | wave | findings |
|---|---|---|---|---|---|---|---|
| G1 | ft-c9ff73806ffa9c5ed8e80b913c135b31 | Daemon scheduler waits in wall-time slices | 1 | high | ready | 1 | A1-5 |
| G8 | ft-6dc4d776b2b04ffbdf04e58c4a315bf6 | Mac native protocol integrity (UTF-8 chunks, patch, listAnswers, draft settings) | 1 | high | ready | 1 | B2-1, B2-7, B2-8, B2-9 |
| G6 | ft-c567ded58985474e7c2e77fecf1b874f | Rankfit/eval verdicts statistically honest | 2 | low | ready | 1 | A3-1, A3-2, A3-3, A3-9 |
| G15 | ft-d30b8cdcf63c8f4bcf772dd775455dc8 | Benchmark baselines for the audit's perf findings | 2 | low | ready | 1 | B1-6; baselines for A2-1, A4-4, A4-5 |
| G10 | ft-60d1dc8eb4d3065fbc0afda7a1767388 | Extension routing & trust (attachResume, tsc gate, sender policy, URL schemes) | 1 | medium | ready | 2 | B3-1, B3-4, B3-2, B3-3 |
| G5 | ft-b6b00b08e8b50cd9613fd149c17698ec | Storage write-path gaps beside ft-bcf3 | 3 | low | ready | 2 | A2-8, A3-6, A2-3, A3-8, A2-7, A2-10 |
| G9 | ft-bafd750094720ac26a73280954b5c717 | Mac app main-thread & subprocess boundaries | 3 | low | ready | 2 | B2-2, B2-3, B2-10, B2-4, B2-5 |
| G2 | ft-84d4fe2545e64bdaa9678e18b45a5e6d | Daemon run lifecycle (config reload, DevPrism restart, panics, poison, warnings, batched marks) | 1 | medium | backlog | 3 | A1-1, A1-2, A1-3, A1-4, A1-9, A1-10, A2-2 |
| G7 | ft-a1bb3507a791c0bd7301ada1eb94ec56 | Decision-prompt bounds & sensitive-field guard | 2 | low | backlog | 3 | A3-4, A3-5, A3-10 |
| G11 | ft-debfd7bf523d636982476a176a6feb0c | CLI & digest honesty | 2 | low | backlog | 3 | B1-3, A2-9, B1-8, A2-6 |
| G3 | ft-b756b6a3f9d316b68f53b5445feee841 | Config file boundary (locked Update, Validate↔egress, URL scrub) | 2 | medium | backlog | 4 | B1-1(=A1-6), A4-1, A4-2, B1-5, B1-7 |
| G12 | ft-8cd27d934e1620b035a892bdaaa6357f | Store write & lock correctness in commands | 3 | low | backlog | 4 | A2-5, B1-4, B1-2, A1-8 |
| G14 | ft-da6154b825e659d68aa9d6870b564ec5 | FOLLOWUPS bookkeeping for unrecorded declines | 3 | low | backlog | 5 | A4-3 record, ft-bcf3 c4, D17/D18, A2-4 residual, B2-6, ft-3189 c2 |

### Folded into existing cards (2)

| group | item_id | what was added | status change |
|---|---|---|---|
| G4 | ft-3d83fba9b9d4b87feb9160056d072624 | Criteria 2, 4, 5 marked NOT MET (not recorded as declined); amended criteria tied to G15 numbers; Fable's build/decline per criterion; findings A2-1, A4-4, A4-5 | review → **backlog** (wave 4) |
| G13 | ft-e97abbbf30ed5b366274a269a2b658ed | New criterion 7: Eightfold/Apple detail 404s bounded like Workday (A4-6); criterion 3 amended (page http:port refusal is deliberate, to be recorded) | review → **backlog** (wave 3) |

Priority and severity were kept on both; nothing was lowered.

## Verified-stale / verification sections on review cards

Every review card now carries a "Verification 2026-10-20" section, with each criterion prefixed by its verdict.

| card | result |
|---|---|
| ft-02a32a35 subprocess deadlocks | 4/4 MET (Swift tests 6/6; Go resume tests pass). Residual SIGKILL/exit bound → G9 |
| ft-b31f1578 reconcile Digested→Dead | 3/3 MET (tests pass). reconcile.go is dirty in wt-jibe; re-verify after it lands |
| ft-edfabd86 extension frames | 5/5 MET. **Stale log claim:** "tsc is clean" is false at HEAD (attachResume landed after) → G10 |
| ft-5d181a02 drop rules / breaker / lazyScorer | 3/3 MET. Adjacent gap, a DevPrism dying mid-life → G2 |
| ft-d74590ed egress CONNECT/SSRF | 5/5 MET (c1 "in effect": Go net/http already takes the CONNECT target from the request line) |
| ft-3189102e siteKey / title norm | c1 MET; c2 PARTIAL (14 second levels only; limitation to record); c3 not re-verified |
| ft-e97abbbf sources resilience | c1,2,4,5,6 MET; c3 PARTIAL by design; class gap folded as c7 → **backlog** |
| ft-bcf3eb75 storage atomicity | c1–3 MET; **c4 NOT MET as written** (every 16 blocks + final; better than written; amendment pending) |
| ft-1045267a portability / vault / host | c1–4, 6 MET; c5 PARTIAL (device declined D17); c7 NOT MET, declined D18 (reasoning verified; recorded only in code) |
| ft-3d83fba9 LCA view / memory | c1, c3 MET; **c2, c4, c5 NOT MET, not recorded** → **backlog** |
| ft-ade10aab Mac models / polling / RO CLI | c1 declined (recorded); c2–5 MET (c2 purpose half-met: bank.json tick → G9) |
| ft-df45cd8e prompt framing / clamp / kbsync | c1, c2 PARTIAL (declined parts recorded); c3–5 MET; fixture sha pin MET |
| ft-2a0aff54 benchmarks / marks | c1, c3, c5 MET; c4 declined (recorded); c2 not re-verified |
| ft-7099bd3b (owner-written backlog) | **not touched.** Recommendation only (A3-3 vs its criterion 6), see owner decision 11 |

## Build order (Fable): waves of ≤4, no shared files inside a wave

| wave | cards | gate |
|---|---|---|
| 1 | G1 ft-c9ff7380 · G15 ft-d30b8cdc · G8 ft-6dc4d776 · G6 ft-c567ded5 | now: no wt-jibe file, file-disjoint |
| 2 | G10 ft-60d1dc8e · G5 ft-b6b00b08 · G9 ft-bafd7500 | after wave 1 (G10 and G9 share store.ts/BankStore.swift with G8) |
| 3 | G2 ft-84d4fe25 · G7 ft-a1bb3507 · G13 ft-e97abbbf · G11 ft-debfd7bf | **after wt-jibe lands** |
| 4 | G3 ft-b756b6a3 · G4 ft-3d83fba9 · G12 ft-8cd27d93 | after wave 3 |
| 5 | G14 ft-da6154b8 (docs/FOLLOWUPS.md only) | last, once the G4/G13 decisions are final |

Fable showed the shared-file check per wave in FABLE.md §4: wire.go, app.go and main.go are touched only by G2 in
wave 3; config.go only by G3 in wave 4; ledger/file.go by G5 (wave 2) and G4 (wave 4). If wt-jibe stalls, G7 and the
ledger/lca half of G4 can start in worktrees and rebase.

## Rejected findings

| id | reason |
|---|---|
| A1-7 OpenRouter spend cap overshoot | By design and recorded (decide/spend.go:40-44; FOLLOWUPS:472-477). Belongs in the decide repo if pursued. |
| A3-7 drop class names the slot | Recorded owner design (eval/queue.go:20-25; RANKING_PLAN.md:182-183): names backend and slot, never a value; slot names are the pinned public contract. |
| A4-7 proxy token in lightpanda argv | Already a recorded known limit (FOLLOWUPS:929-933). |
| A4-3 Workday half ("404 not logged") | Wrong: runwire.go:162-167 wires Report to warnf; workday.go:200-206 reports Gone:N. The page half is kept (G13/G14). |
| B2-6 native host origin check | Not a defect: the D18 reasoning holds (argv comes from the launcher; Chrome enforces allowed_origins). A parent-signature check is a new optional control → owner decision 7. |
| A2-4 empty listing ages the board | Downgraded to an owner question: the contract is recorded (FOLLOWUPS:822-832) and page.go refuses an empty match. Residual → owner decision 6 / G14. |
| Relabels (kept as findings) | B2-2, A1-8, A1-2 are adjacent gaps, not stale claims, because the cited criteria are MET as worded. A3-1's Platt sub-claim was dropped. A3-5 was rejected *as a personal-data leak*; its regex gaps, false UI text and attestation draft are kept. |

## Decisions made without asking (logged)

1. **Scope of "audit":** main @ d5c4097 only. wt-jibe's 92 uncommitted files, including the new `cmd/vani/keychain.go` and
   `internal/vault/modelkey.go`, were not audited; findings in those files are tagged in-flight.
2. **B2-1 data loss did not stop the run.** The stop rule covers data loss; I read it as "stop before an action of mine
   could cause or expose data loss". Filing a card is non-destructive, and the bug is live either way, so I filed it
   at p1/high and put the only decision it needs (repairing already-damaged banks) at the top of the owner list.
3. **Overwrite safety:** I tested overwrite on one card (ft-5d181a02) and re-read it before touching the rest. Logs,
   column and position were preserved, so the other 12 overwrites went ahead.
4. **ft-3d83 and ft-e97a moved review → backlog.** Unmet or newly added criteria scheduled for waves 3–4 shouldn't sit
   in "review" (the claimed-done column). No card was moved to done; done needs Manvi verification.
5. **Criterion marks:** existing criteria text was kept verbatim with a `[MET — verified in source]`, `[PARTIAL …]`,
   `[NOT MET …]` or `[not re-verified]` prefix. Amendments were appended after "— amended 2026-10-20:", not substituted.
6. **related_tasks_exist refusals:** every listed card had been read in full this session, and Fable had already
   judged each group separate work, so I retried with `reviewed_related` instead of folding. The new cards name the
   related cards in their descriptions.
7. **New cards' status:** waves 1–2 are `ready` (no in-flight files); waves 3–5 are `backlog` (blocked on wt-jibe).
8. **Fable's design calls, adopted:** A1-1 reloads config per run; A2-7 makes rekey refuse on Windows; B1-3 makes `track
   use` refuse unless `--allow-unchecked`; G11 runs at p2 (Opus had p3); the ft-3d83 criteria are built or declined
   depending on the G15 numbers.
9. **ft-7099 (owner-written)** was not modified.
10. **Tool gaps worked around:** GitPulse `active_changes` was rate-limited until the third retry (the in-flight list
    is in the scratchpad's INFLIGHT.md). Most DevMap neighbor and impact walks came back `walk_incomplete`, and DevMap
    doesn't resolve `chrome.*` receivers or prove Swift's confident tier. Many caller claims therefore rest on `rg`
    (some with `-uu`) and are labelled that way in the findings files. The shell gate blocked `git -C`,
    out-of-repo `go run` and tsc, so A2-3 and A2-7 were settled from stdlib source rather than executed, and tsc
    wasn't run.

## Owner decisions — decided by Fable under delegation (2026-10-21)

The owner delegated these to Fable ("For decision ask fable"). Full reasoning:
`…/scratchpad/audit/FABLE-DECISIONS.md`. Each decision is appended to its card under "### Owner decision (Fable,
delegated 2026-10-21)", and criteria were replaced where noted. **One item stays with the owner:** carrying out any
restore or repair of the live bank (part of decision 1).

| # | decision | cards updated |
|---|---|---|
| 1 | B2-1: a read-only U+FFFD scan is step 0 of G8, run before any edit and again after the fix. It outputs counts, sizes and field keys only, never answer text, and a scan that could not run is logged as such. Correction: quarantined banks are sibling `bank.corrupt-*.json` files, not a directory. If damage is found, the builder stops and you repair (by hand in the app by default, with a Time Machine copy as reference); nothing is ever auto-rewritten. **Doing the restore or repair is yours, not delegated.** | ft-6dc4d776 (criterion 5 replaced) |
| 2 | ft-bcf3 c4 amended to "at most ceil(n/16)+1 writes per pass"; no rebuild; recorded via G14 | ft-bcf3eb75 (c4 replaced), ft-da6154b8 |
| 3 | ft-3d83: build c4; build c2 only if G15 measures BenchmarkLoad ≥100 ms (the builder applies this without asking); decline c5, reopening if any board body reaches 32 MiB | ft-3d83fba9 (c5 replaced) |
| 4 | TypeScript devDependency **approved**: exact pin read from the registry at build time, `"private": true`, devDependency only, committed `bun.lock`, `--frozen-lockfile`, the installed `tsc` (never `bunx`), `node_modules` gitignored, nothing shipped in the bundle. Security impact: build-time only; no access widened. | ft-60d1dc8e (c2 replaced) |
| 5 | Delete the three unsent message types, their handlers and the now-callerless `store.queryCandidates`/`store.listAnswers` (keep the `patchAnswers` function). G8 clamps the native ops, and a follow-up card deletes them after G10. | ft-60d1dc8e (c3 replaced), ft-6dc4d776 |
| 6 | A2-4 empty-listing floor declined; recorded via G14 | ft-da6154b8 |
| 7 | B2-6 signature check and `#if DEBUG` gating declined; security impact: none; revisit trigger named; recorded via G14 | ft-da6154b8 |
| 8 | A1-1: reload config per run confirmed | ft-84d4fe25 |
| 9 | A2-7: rekey refuses on Windows confirmed; recorded via G14 | ft-b6b00b08, ft-da6154b8 |
| 10 | B1-3: `--allow-unchecked` refusal confirmed; the words file is not required | ft-debfd7bf |
| 11 | ft-7099 criterion 6 **not amended**; the pre-registered 30-item held-out split is a Lappi-decision matter, so A3-3 stays on G6. A note only was appended to your card; its criteria are unchanged (re-read to confirm). | ft-7099bd3b (note), ft-c567ded5 |
| 12 | G13 criterion 7 stays on ft-e97a in wave 3; it is not handed to wt-jibe. It may start in a worktree if wt-jibe hasn't landed when wave 2 ends. | ft-e97abbbf |

Board writes on 2026-10-21: 12 card overwrites, each re-pulled first with revisions unchanged. Log, column and
position were preserved.

## Owner decisions as originally listed (2026-10-20, superseded by the table above)

1. **B2-1 recovery (G8, before it lands):** run a read-only scan of the live `bank.json` and the quarantine dir for
   U+FFFD. If it finds damage, choose between restoring from a pre-corruption copy, hand-fixing the affected answers,
   or accepting the loss. The fix itself needs no decision.
2. **ft-bcf3 criterion 4:** amend it to "no per-block save; at most ceil(n/16)+1 writes per pass" and record it
   (recommended), or rebuild the batching as a single write.
3. **ft-3d83 criteria 2/4/5:** build c4 (in-memory eviction); build c2 only if G15 measures LCA Load above 100 ms;
   decline and record c5 (recommended). The alternative for c4 is events.jsonl compaction, which is larger and
   rewrites the on-disk history.
4. **TypeScript devDependency (G10):** `bun add -d typescript` creates extension/package.json and a lockfile. This is
   a new dependency (recommended: add it). The alternative is a script that never reports "ok" when tsc is absent.
5. **Delete the three unsent extension message types** (queryCandidates, listAnswers, patchAnswers-as-message). The
   evidence is rg, not the code graph. Recommended: delete in the extension; clamp, don't delete, the native op.
6. **A2-4 residual:** should a board with ≥M open records that suddenly lists 0 be treated as suspect rather than
   aged? Recommended: decline and record.
7. **B2-6:** add a parent-process code-signature check to vani-apply-host and compile its test flags only under
   `#if DEBUG`? Recommended: decline for now and record.
8. **A1-1 (G2):** object now if you'd rather "document that the daemon needs a restart" than reload config per run.
9. **A2-7 (G5):** object now if you'd rather support rekey on Windows than refuse there.
10. **B1-3 (G11):** object now if you'd rather require the words file than add a `--allow-unchecked` refusal.
11. **ft-7099 criterion 6 (your card):** consider sizing the held-out split above MinPromotionLabels plus allowed
    skips. If you do, drop A3-3 from G6.
12. **G13 placement:** keep criterion 7 on ft-e97a (done), or hand it to the wt-jibe session, which is already editing
    eightfold.go and apple.go.

No Opus/Fable disagreement changes scope. Fable raised G11's priority and noted ft-ade10 c2's purpose clause; the
work is the same either way.

Stopped here, awaiting your go-ahead to build.
