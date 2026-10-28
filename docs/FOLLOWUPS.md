# Follow-ups

```mermaid
flowchart TD
    subgraph CoreVani["Vani Core (apps/Vani)"]
        VANI_RUN["Pipeline Runner"]
        VANI_EGRESS["internal/egress\n(SSRF Guard & CONNECT Proxy)"]
        VANI_DECIDE["internal/decide/jdtriage\n(4-slot JD Triage Fixture)"]
        VANI_KB["cmd/vani kb sync\n(rkb bank -> DevPrism)"]
        VANI_APPLY["Vani Apply (Extension & Mac App)\n(CAS Store & Native Messaging)"]
    end

    subgraph ManviIntegration["Manvi Ecosystem (devtools/Manvi)"]
        MANVI_EGRESS["Shared Egress Policy\n[Follow-up: Export Vetting + Dialer]"]
        MANVI_LLM["LLM Adapters (Gemini/Claude)\n[Follow-up: Route through egress]"]
        MANVI_PIN["go.work Local Checkout Pin\n[Follow-up: Version lockfile]"]
    end

    subgraph DecideEcosystem["Decide Module (devtools/decide)"]
        DECIDE_ROUTER["Decision Router & Spend Caps"]
        DECIDE_JEV["decide/jev\n[Follow-up: Multi-slot & n8n support]"]
        DECIDE_LAPPI["decide/lappi\n[Follow-up: Unify DevCouncil copy]"]
    end

    subgraph DevCouncil["DevCouncil (devtools/devcouncil)"]
        DC_LAPPI["devcouncil/lappi\n[Follow-up: Replace with decide/lappi]"]
    end

    subgraph DevPrism["DevPrism Service"]
        DP_MCP["DevPrism --mcp\n(resume_gap_analysis & career_upsert_block)"]
    end

    subgraph ExternalServices["External Automation & Agents"]
        N8N["n8n-email-engine\n[Follow-up: Ledger event feed]"]
        LAPPI_AGENT["Lappi Daemon (qd.sock)\n[Follow-up: Train 4 triage families]"]
    end

    VANI_EGRESS -.->|Proposed Convergence| MANVI_EGRESS
    VANI_RUN --> MANVI_LLM
    VANI_DECIDE --> DECIDE_ROUTER
    DECIDE_ROUTER --> DECIDE_JEV & DECIDE_LAPPI
    DECIDE_LAPPI -.->|Unify Dependency| DC_LAPPI
    DECIDE_LAPPI --> LAPPI_AGENT
    VANI_KB --> DP_MCP
    N8N -.->|vani.event lines| VANI_RUN
```

## Career pipeline on rkb (2026-10-24): what is left

Resumes, answer drafts, the semantic profile and DevPrism now take only `kbsync.Eligible` bullets
(docs/RUNTIME.md, "The knowledge base" and "Building a resume"). Installed 2026-10-24; the
`kb sync --replace` ran (backup `state/kbsync/backups/career-20261024-143402.db`, 43 blocks; DevPrism
now holds 19).

- **The approved pool is small: 17 entries, 37 bullets** (of 82 entries, 382 bullets), and 31 of
  them are the base resume's. About 70 listed bullets fail only because they have no round-2 reader
  pass on their current wording (rkb's two-round gate, lint.go:449-476). Running rkb's round-2
  readers on the CV and tailored selections widens the tailoring choice and DevPrism's ranking
  evidence. An owner decision, not code: keep two rounds everywhere, or relax drafts and ranking to
  one reviewed pass while resumes keep two.
- **Forced rescore.** `vani rescore` scores only postings with no rank, so the ~6,000 postings
  ranked before the replace keep coverage from the old DevPrism blocks. A `--all` (bounded, DevPrism
  only) would re-rank them.
- **rkb fit under load.** At a load average of 119 (other sessions compiling) rkb's Chrome fit
  outlived its 100 s and `vani resume` failed with `timeout`. A load-aware or longer fit bound needs
  the 360 s total re-budgeted.
- **The family rule has not run live.** Built after the last successful live run (v3, which showed
  two framings of the tessl work); the run after it timed out under load.
- **Pre-existing:** `TestDailyRunRereadsTheWatchlist` and `TestRunOnceOverridesReachTheAppAndTheRecord`
  fail on clean `e9710ea` too, and a `cmd/vani` test run leaves `cmd/vani/runs/inbox.json` behind
  (also on clean HEAD).
- **rkb is not under version control** (`career/skills/resume-knowledge-base`); Vani mirrors its
  review hash and selection defaults (eligible.go) and holds them to it with the real-bank oracle.
- **Docs merge:** README.md, docs/RUNTIME.md and this file are also edited in `tmp/wt-jibe`
  (`sources-live-audit`).

## Remaining after the final landing (2026-10-20)

The ranking and filtering audit and its status per finding are in
[RANKING_PLAN.md](RANKING_PLAN.md). What is done there is not repeated here; this is what is left.

- **The semantic floor before Liquid D** (Phase 3). Pick it from the live distribution once the
  owner has seen it. Without a local model, a posting under the floor would wait for the paid
  call, so the floor only defers.
- **Semantic calibration, `internal/semantic` Version 4 → 5 (2026-10-24).** The ceiling anchor now
  averages its best third of sentences (`anchorDepth`: `round(n*8/24)`, at least 1: 2 of the owner's
  6) instead of all of them, the same top-third statistic a posting's raw score is (best 8 of the
  24-chunk cap). Measured with the real model on the owner's live profile (variant V5, 34 saved, 12
  hidden, 300 random new postings): ceiling 0.758 → 0.807, saved jobs at 0.95 or more 82% → 8.8%, 85%
  of saved inside (0.6, 0.95), saved median 0.823 against 0.601 for the population; the order of
  postings and the ranking AUC (saved over population 0.849) are unchanged, since only the scale
  moved. Floor, knee 0.8 and tanh are unchanged; the off-domain anchors are read in full as before.
  `ChunkVersion` stays 3, so the bump rescores from the vector log and embeds no posting. Still
  open: the floor (0.655 here; pooling or a top-third floor were measured and rejected or deferred
  as a separate decision) and that the saved set is 34 jobs (bootstrap 90% interval for the share
  inside (0.6, 0.95): 74% to 94%). `tmp/kbaudit/calib/` holds the harness and `full3.out`.
- **Calibration (≥100 judged labels) and the weight fit (≥150).** Nothing to build until the weekly
  `vani audit` has produced them (about 150 a month at the planned cadence). Both must refuse below
  their threshold with a "could not run" result, never a quiet fit.
- **Owner config, not code:** a location `exclude` (RANKING_PLAN F3), `advance_per_run` 750 (the
  default stays 300), the cloud shadow (README, "Cloud shadow"), and the Engineering Manager
  question (TRACKS.md).
- **DevPrism upstream:** report `coveragePercentage: null` for a JD with no must-have skills
  (`jd.rs:559`, `gap.rs:246` under `/Users/bharath/Code/devtools/DevPrism`). Vani already reads
  both the old `0` and the new `null` as "no must-haves" and nothing else as a measurement
  (`internal/resume/tools.go`), so this is not blocking.
- **Jobs-tab impressions carry no scores.** `vani impressions log` takes keys and positions, so a
  fitter that wants a Jobs row's Score parts joins the key to the ledger at read time. Digest rows
  carry their parts as shown. A failed report is shown on the Jobs tab and the Audit tab
  (`impressionsError`); it still carries no scores.
- **The Mac app's UI-automation tests are skipped without accessibility trust** (the test runner
  must be trusted); `swift test` reports them as skipped, not passed. Run them once on a trusted
  session before a release that touches the Jobs tab.
- **The extension keeps a second scorer for postings Vani has not read** (`extension/src/relevance.ts`,
  a heuristic with a generic profile). It shows Vani's own row when the posting is in the ledger and
  a pill labelled "Estimate" otherwise (extension README); the two can differ. Retiring the estimate
  means asking Vani to score a page it has not ingested, which is a contract change.
- **`internal/decide/kbmatch/jev.go`** holds the option descriptions and could be renamed
  `descriptions.go` the next time that package is touched.

## Needs the owner's go-ahead

- **Converge egress with Manvi.** [`internal/egress`](../internal/egress) re-implements manvi/fetch's rules because
  manvi/fetch is GET-only, reduces bodies to prose and caps them for model context, so it cannot
  carry ATS JSON or Workday POSTs. Fix: Manvi exports its vetting + pinned dialer as a reusable
  policy; Vani wraps it and deletes its copy. A Manvi change plus a Jarvis pin bump.
- **LLM traffic bypasses Vani's egress.** Manvi's Gemini/Anthropic adapters open their own
  connections to their fixed vendor endpoints. The model cannot choose the destination, so this
  is not an SSRF path, but it is a second outbound path. Resolved by the same Manvi egress export.
- **n8n-email-engine `vani.event` lines** feeding the ledger (alerts from email). A change in
  another repo.
- **DevCouncil's `devcouncil/lappi` onto `decide/lappi`.** The client and request code now live in
  `~/Code/devtools/decide/lappi` (moved, made generic: `NewRequest`, no defect-class constants);
  DevCouncil still has its own copy, so there are two until this lands;
  `decide/lappi/drift_test.go` fails if their code (comments aside) diverges, and skips with a
  message when DevCouncil is not checked out beside `decide`. The change: DevCouncil
  requires `github.com/bharathvbcr/decide` and keeps only its defect-class request, `asker.go`,
  `record.go` and `diffsplit.go`. Blast radius: replace directives in a dependency are ignored, so
  every module that builds DevCouncil through a `replace` (Manvi, Jarvis, Vani's workspace) needs
  its own `require` + `replace` for `decide`, or `decide` must be published. That touches Manvi's
  `go.mod` and `module-pins.txt` and Jarvis's `go.mod` and `upstream.lock.json`.
- **n8n-email-engine onto `decide/jev`.** The email engine's inline Jev client has no context, no
  cancellation and no spend cap. Moving it needs two additions to `decide/jev`: a configurable
  endpoint (with the email engine's key-host and loopback-http rules) and a caller-shaped `state`
  (it sends `{from, subject, snippet, bulk_mail}`, not `{"text": ...}`).
- **Lappi training (approved, deferred).** Lappi-decision's decision pool carries one choice slot
  per example, so the triage question is split for Lappi into four single-slot families
  (`career.jd_role_fit`, `career.jd_sponsorship`, `career.jd_work_mode`, `career.jd_seniority`;
  fixture v2). Vani's Lappi backend routes each slot to its family; the Liquid decision model
  (over OpenRouter) and the LLM still get all four slots in one request. The generators are being written in Lappi-decision; the training run
  waits for the owner.
- **Title drops count toward `advance_per_run`.** A title drop asks no backend, so leaving it out
  of the cap would let a run triage more postings for the same spend. The owner kept it counted
  when approving `title_signals.read` (2026-10-19). Changing that needs their go-ahead.

## Audit 2026-10-19: criteria not built as written

Each was decided on evidence; the cards' other criteria were built and tested.

- **Card 13: marks writes still read the store first.** The criterion asked `marks.write` to
  append without re-reading earlier lines. The read is what refuses to append to a corrupt store,
  and it folds the entry a write returns, so it stays. Its cost is bounded instead: a write
  compacts the store once it passes 1 MiB with more than half its lines superseded, or when its
  line would pass the 8 MiB cap (`internal/marks`). Interleaved A/B, min of 5, per mark: 1 MiB of
  history 16.6 -> 12.3 ms, 4 MiB 46.2 -> 12.4 ms, 7 MiB 77.9 -> 12.9 ms. If the live marks alone
  grow past a few MiB, the next step is a faster line decoder, not dropping the check.
- **Card 12: only the reasoning headroom is clamped.** `llmbackend.Shape` keeps the headroom from
  lifting `MaxTokens` past the model's output ceiling. A request already over the ceiling is not
  lowered: Manvi refuses it before sending, which is louder than a silently smaller budget.
- **Card 12: no untrusted-content frame to move the reasons facts across.** The
  `career.jd_reasons` question goes only to the Liquid decision model through the OpenRouter
  client `decide/jev` (`cmd/vani/reasonscmd.go`), whose state carries no
  BEGIN/END UNTRUSTED markers. The context is now capped at `jdtriage.MaxContextBytes` with the
  facts kept; only the coverage percentage is the pipeline's own fact, and the missing
  requirement names are posting text and are labelled so.
- **Card 12: no shadow over-count found.** Unsampled questions write no shadow keys, and the live
  ledger had 2,176 records with shadow keys, none left over from an earlier triage. The one path
  that could leave one (a dead posting with no `died_from` coming back live) is closed: a triage
  now replaces every earlier `triage.*` key.
- **Card 11: the Mac app's models were not moved into `VaniApplyKit`.** They are already testable
  through `@testable import VaniApply` (`VaniApplyUITests`), and moving `AppModel` would link
  SafariServices and ServiceManagement into the Safari appex and the native host. `JobsModel`
  gained unit tests that need no accessibility trust; `AppModel` has none, since its init reaches
  the App Group container, SMAppService and a timer. Splitting those behind injected seams is the
  route to testing it.
- **Card 6: eval labels do not follow a re-keyed posting.** `vani migrate rekey-fallback` moves
  ledger events and marks. Labels (`labels.jsonl`, keyed by posting) can live in the repository's
  `eval/jd_triage` rather than the state directory, so the migration cannot find them all. No
  fallback-keyed posting exists today, so none is labelled; if one ever is, re-point its label by
  the old -> new map in `ledger/rekey-fallback-title.json`.
- **`TestJobsInsightScalesTo5000Postings` has a tight bound under load.** It ran 5.7-6.5 s under
  `-race` on a quiet machine and passed its 10 s bound, but crossed it twice while a Swift build
  ran alongside. Not a code fault; raise the bound or skip it under `-race` if it recurs.
  (Superseded 2026-10-21: ft-2673557b made the test count allocations, 4.00x for 4x postings,
  instead of wall time, `cmd/vani/jobsscale_test.go`.)

## Audit 2026-10-20: decisions and deviations the audit found unrecorded (recorded 2026-10-21)

Each is a decision or a deviation from a card's criterion that lived only in a card log or a code
comment, so the next audit would report it as a bug. Line numbers are on main at `2fcac90`, except where an entry below was re-checked on `0c27a13` (2026-10-21).
"Fable delegated" is the owner's delegation to the Fable advisor on 2026-10-21.

- **A4-3, page half: page boards fail on http links off the default port, by design (2026-10-20,
  ft-e97abbbf criterion 3, amended).** `upgradeHTTP` (`internal/sources/page/page.go:446-457`)
  rewrites an http link on port 80 or no port to https and leaves any other port alone; a posting
  the operator's pattern selects but that is not https then fails the board in `fetchableURL`
  (`page.go:414-418`, `:334`) instead of being fetched somewhere else or skipped (a skip would mark
  it dead). Pinned by `internal/sources/page/page_test.go:305-309`. The https form of another port
  is a guess.
- **ft-bcf3eb75 criterion 4: kbsync saves every 16 blocks and once at the end (2026-10-20; amended
  and DECIDED 2026-10-21, Fable delegated, decision 2).** At most `ceil(n/16)+1` writes per pass,
  not one per block: `flushEvery = 16` (`internal/kbsync/state.go:96-99`), the batch flush at
  `internal/kbsync/apply.go:150-151` and the final flush on every return (`apply.go:79`, which does
  nothing when nothing is unsaved, so an exact multiple of 16 saves `n/16` times). A crash between
  saves loses at most 15 records, whose blocks are sent again unchanged (state never runs ahead of
  DevPrism). `internal/kbsync/batch_test.go:48` pins 3 saves for 40 blocks.
- **ft-1045267a D17: the llama-server device is fixed to Metal, not configurable (Fable D17;
  decided before the 2026-10-20 audit, date not recorded; first written here 2026-10-21).** `internal/localserver/localserver.go:71-81`
  passes `--device MTL0 -ngl all` always. The owner's rule of 2026-10-18 is that the served path is
  Metal and CPU is never a fallback; a configurable device was declined on that ground. Without
  MTL0 llama.cpp exits at once ("invalid device") and the run goes on without the shadow, rather than
  measuring a different numeric path.
- **ft-1045267a D18: the native host does not check the extension id (Fable D18; decided before
  the 2026-10-20 audit, date not recorded; first written here 2026-10-21).** `macos/Sources/vani-apply-host/main.swift:20-29` requires only a
  `chrome-extension://` prefix. The boundary is Chrome's `allowed_origins` in the manifest that
  `ChromeConnector` writes; the argument comes from whoever launched the process, so an exact-id
  match in the host would add no protection (a same-user process can pass any id), and the id the
  user chose is in the app's defaults, which the host cannot read.
- **B2-6: the native host is a same-user deputy; a parent-process code-signature check and
  `#if DEBUG` gating of `--store-dir`, `--vani`, `--anthropic-account` and `--no-keychain` are
  DECLINED (audit 2026-10-20; decided 2026-10-21, Fable delegated).** The host runs as the user
  (`main.swift:20-29`) and the flags (`main.swift:31-58`) grant nothing a same-user process does
  not already have. Security impact: no change. Revisit if the host gains a capability the same
  user cannot exercise directly, for example a Keychain ACL bound to the host's signature.
- **A2-4 residual: a well-formed empty listing during an ATS outage ages the board; no floor
  (audit 2026-10-20; DECLINED 2026-10-21, Fable delegated).** The reasons and the contract are in
  "Known limits" under "Absence ages every open posting" (the Residual bullet).
- **ft-3189102e criterion 2: `COUNTRY_SECOND_LEVELS` is 14 second levels, so `a.me.uk` and `b.me.uk`
  collide (2026-10-20).** `extension/src/normalize.ts:124-141` (`ac co com edu go gob gov ltd mil ne
  net or org plc`); `siteKey` keeps the last two labels for any other suffix, so `me.uk` is one key
  for every `x.me.uk` (and `ltd.uk` is covered, `me.uk` is not). It is not a full public-suffix
  list. Per-tenant ATS subdomains collapse by design: `acme.greenhouse.io` and `boards.greenhouse.io`
  are the key `greenhouse.io` (the comment's own example), so a field signature is per ATS, not per
  tenant. A public-suffix list would be a new dependency and needs the owner's go-ahead.
- **A2-7: `rekey` refuses on Windows (decision 9; 2026-10-21, Opus build session).** A platform
  limit, not a defect: `rekey.Run` returns `ErrUnsupported` on `GOOS=windows` before writing
  anything (`internal/rekey/rekey.go:101-102`, `:118-121`), because the ledger stays open (locked)
  while its events file is replaced and Windows cannot rename over an open file.

### Built-session additions (2026-10-21, Opus build session; owner or Fable delegated where named)

- **ft-3d83fba9 criterion 2 was BUILT, not declined; the size measured was synthetic.** The
  decision-3 rule (build if `BenchmarkLoad` is at least 100 ms) fired: G15 measured 0.832 s
  interleaved min (1.222 s block min, n = 5) on a synthetic 200k-employer x 3 FY index, because no
  production LCA index exists in the live state (`internal/lca/load_bench_test.go:34`). Result:
  `LoadView` 19 ms against 947 ms for a fresh-process `Load`, 57 allocs against 4.80M
  (`internal/lca/view.go:288`, `view_bench_test.go:21`). There is therefore no decline to record.
- **ft-3d83fba9 criterion 5: `srcutil.GetJSON` streaming DECLINED (decision 3; 2026-10-21, Fable
  delegated).** Measured (G15 `BenchmarkGetJSON20MiB`, synthetic 20 MiB body, fake `Doer`,
  `MaxBody` 64 MiB, interleaved min): 76.8 MB/op, 22.1k allocs/op, 26.3 ms.
  The gain would be memory only; bodies are capped at 64 MiB (`internal/sources/srcutil/srcutil.go:31`);
  boards run serially; `json.Unmarshal` already rejects trailing data (`srcutil.go:169`); the body is
  read once with `io.ReadAll(io.LimitReader(...))` (`srcutil.go:243`). Reopen trigger: a board body
  at or above 32 MiB (half the cap).
- **ft-3d83fba9 criterion 4: `ledger.Record` contract delta (Fable, delegated; 2026-10-21).**
  `Record.BodyEvicted` (`json:"-"`, `internal/ledger/types.go:26-47`) is true when a Dead, Dropped or
  Duplicate record's `Description` was dropped from memory. `File.All()` returns those records with
  an empty `Description` and reads nothing from disk (`internal/ledger/file.go:576-583`); a caller
  that needs the text uses `ledger.Full` (`internal/ledger/evict.go:171-174`), `File.Get`
  (`file.go:560`) or `File.Hydrate` (`evict.go:149`), which fail with `ErrBodyUnavailable`
  (`evict.go:50`) rather than return another body. Heap after `Open` of a 20k-record, 80%-dead
  synthetic ledger fell from 100.7 MB to 34.3 MB (34%); `Open` ns/op rose 9.1% (148 to 162 ms, min of
  5, loaded), accepted by Fable (once per process, 66 MB saved). The one consumer that needed text,
  `warmSemantic`, hydrates only owner-marked closed records (`cmd/vani/semanticwire.go:188-193`,
  `cmd/vani/app.go:310`); the `vani semantic warm` CLI does not yet (ft-0aa92138). The single-row
  `jobs` commands no longer read every body: see the next entry (`ReadSnapshotScoped`, on main).
- **ft-2673557b `jobs find` / `jobs mark` under 250 ms: criterion 2 met in the cleaner measurement,
  not re-verified on a quiet machine (2026-10-21; supersedes the first, partial entry).** Two merges.
  First (75732b2): `newRowInsight` learns boilerplate only from the row's own company
  (`cmd/vani/jobsinsight.go:284`); the regression was commit 3255839 putting the full-ledger learn
  on every `find`. At load 28-33, `mark` 2193 to 244 ms and `find` 2094 to 255 ms; allocations 14.13M
  to 524k and 14.28M to 670k. The rest was `ledger.ReadSnapshot` I/O. Second (0c27a13, commits
  1f306aa, a810bd4, aefe615): `ledger.ReadSnapshotScoped` (`internal/ledger/lean.go:250`) cuts the
  description out of each line before decoding for every record outside the row's company, then
  hydrates the row's company by offset with a byte compare (a mismatch is `ErrBodyUnavailable`,
  `lean.go:217-225`). `jobs show/mark/note/find` read through it (`rowScope`, `rowRecord`,
  `cmd/vani/jobscmd.go:430,447`; `jobsStateFor` at `:458`; callers `jobscmd.go:300,380`,
  `jobsfind.go:84`), and a record whose body was skipped is refused, never emitted. Fail-closed cut
  (`stripDescription`, `lean.go:57`): only compact lines with exactly one posting and one
  `description` key, no escaped or non-ASCII keys; the description's escapes are validated so
  `ReadSnapshot` and `ReadSnapshotScoped` agree on a corrupt line; a canonical line never falls
  back to the full decode. Rows are byte-identical to the full-read path
  (`TestScopedReadRowsAreByteIdenticalToTheFullPath`). Fuzz: 10.7M + 4.0M execs, then 2.1M + 1.3M
  after the fix, 0 crashers. **Numbers, synthetic 10k postings of about 6 KiB, interleaved min of 5,
  Apple M5 Pro, measured on a loaded machine (load average about 17-20):** `mark` 136 to 90 ms,
  `find` 130 to 94 ms; B/op 111 to 55 MB (`mark`), 119 to 62 MB (`find`); allocations unchanged.
  After the fail-closed fix, at load 80-89: `mark` 205 ms, `find` 283 ms, which is not comparable
  (the machine was saturated). So both are under 250 ms only in the cleaner run; wall-clock was never
  taken on a quiet machine, and no rerun is recorded. State the criterion as met under load, not as
  a quiet-machine figure.
- **ft-b6b00b08 A2-3: marks line cap raised 8 to 16 KiB; a downgrade limit (Fable decision; 2026-10-21).**
  `maxLineBytes = 16 << 10` (`internal/marks/marks.go:77`; the downgrade warning is at `:74-76`).
  `marks.jsonl` written at or after this change may hold lines up to 16 KiB; an older binary with
  the 8 KiB cap returns `ErrCorrupt` on such a line, so do not run an older `vani` against a store a
  newer one has written escape-heavy notes into. Not a migration: no bytes are rewritten.
- **ft-22d7da5f: atomic writes and symlinks (Opus design decision, accepted 2026-10-21; merged
  2fcac90).** Directory components resolve, so a state directory that is itself a symlink works
  (iCloud, an external disk). A program-named leaf is never written through a symlink:
  `atomicfile.Write` replaces the link with a regular file, as before
  (`internal/atomicfile/atomicfile.go:28-37`), `OpenAppend` refuses a linked leaf with
  `ErrLinkedLeaf` and creates or removes nothing (`atomicfile.go:85-116`, error at `:18`), and a
  directory, FIFO, socket or device leaf is `ErrNotRegular` (`internal/atomicfile/resolve.go:10-36`).
  Only the operator-chosen config path follows its link, resolved once and used for both the lock and
  the write. Security impact: no widening against the pre-change code; a planted link in a state
  directory still cannot move a write outside it (the first version of this fix could, through the
  eval text cache and the logo cache; tests assert outside files stay untouched). Consequence: an
  append-only file (ledger, marks) that is itself a symlink now fails loud instead of starting an
  empty history; replace it with the file, or link its directory.
  The refusal is wired at both append owners: `ledger.Open` opens its events log with
  `atomicfile.OpenAppend` (`internal/ledger/file.go:100`) and `marks` its store
  (`internal/marks/marks.go:447`), so a linked leaf returns `ErrLinkedLeaf` wrapped, with no empty log
  created and no history replaced. Pinned by `internal/ledger/symlink_test.go:30,40`,
  `internal/marks/symlink_test.go:29` and `internal/atomicfile/symlink_test.go:47,69` (a dangling link
  too).
- **ft-60d1dc8e: TypeScript 7.0.2 is not "no transitive packages" (accepted by Fable; 2026-10-21).**
  The exact pin (`extension/package.json:8`) is the native compiler: `typescript` plus 20 optional
  `@typescript/typescript-<platform>` packages, 21 entries with sha512 hashes in `extension/bun.lock`
  (`typescript` at line 52). Opus verified that all 21 share one maintainer set. The typecheck gate
  requires `tsc --version` to equal `Version <pin>` and never reports ok without running tsc
  (`extension/scripts/typecheck.sh:26-29`).
- **ft-c567ded5: rankfit floors (Fable kept the constants, decision 4; 2026-10-21).**
  `MinHeldOutPairs` 200 and `MinScoredStratumPairs` 50 (`internal/rankfit/gain.go:15,21`), which needs
  at least 13 scored held-out items, about 200-250 labels, so the first fit at 150 is often refused
  by design (fail closed); `HeldOutAllowedSkips` 3 of a 30-item split, so 27 (`internal/eval/heldout.go:43`);
  `MinInclusionProbability` 1e-9 (`internal/eval/store.go:76`; below about 1e-154 the weights overflowed
  to NaN). The reasoning is in `docs/RANKING_PLAN.md` ("Rank learning", the `Beats()` rules, lines
  203-219); not repeated here.
- **ft-a1bb3507: the fieldtriage guard is a first line, not a proof (Opus decision; 2026-10-21).**
  Known misses: a digit or punctuation inserted inside a word ("c1tizen", "c.i.t.i.z.e.n") and a word
  written wholly in a non-Latin lookalike script. Fail-safe false pins, kept on purpose (they become
  a manual answer, never a draft): "military-grade", "Asian market", and any label containing
  "criminal". The backstop is after the model: `suggest.Run` drafts only an `essay` reading of a label
  `fieldtriage.Sensitive` does not flag (`internal/suggest/suggest.go:158`,
  `internal/decide/fieldtriage/fieldtriage.go:135-146`), and a word mixing Latin with another
  alphabet is pinned `self_identification` (`fieldtriage.go:153-157`, `fold.go:171`). Not a personal-data
  leak: only the job site's label text leaves.
- **ft-a1bb3507 follow-up: the fieldtriage guard speedup is on main, not pending (939ec8e, merge
  68dba78; 2026-10-21).** `folds` returns the four readings (invisible character as nothing or as a
  space, tag joining or separating) once each: identical readings are dropped, so a plain label is
  scanned by every pattern once, not four times (`internal/decide/fieldtriage/fold.go:36-59`).
  `Sensitive` folds once and shares the result with `guardForms`
  (`fieldtriage.go:135-146`, `:150-152`). The commit message's figures: `Guard` on 64 KB 224 to 50
  ms, under `-race` 6.1 to 1.6 s (3-5x). The linearity tests no longer use a wall-clock bound: they
  assert that allocated bytes grow at most 12x for an 8x input (`assertLinear`,
  `internal/decide/fieldtriage/guard_test.go:115-131`) and keep a 2 s cap on a label at the
  production 1,000-rune size (`guard_test.go:142`). An earlier note that this fix was unmerged was
  wrong. Not re-measured here: the speedup figures are the commit's own.
- **ft-c567ded5 follow-up: the rankfit objective is faster, bit for bit (merge e8f7726, commit
  4ac9260; 2026-10-21).** `softplusSigmoid` takes both the loss term and its gradient weight from one
  `exp` (`internal/rankfit/platt.go:209`, used at `internal/rankfit/fit.go:382`), and the
  per-pair weights are computed once into `m.pw` (`fit.go:322-323`, read at `:381`). Loss and gradient
  are bit-identical to the separate functions (`internal/rankfit/objective_parity_test.go:82,115`;
  `FitResult` hashes unchanged on 6 seeds, per the commit). The null-world test went 68.8 to 6.3 s,
  and under `-race` 144 to 31.5 s (figures from the build session that landed it; not re-run for
  this entry). Separately, the null-world tests run in parallel: commit 42c4f64, from another
  session, which gave the package `worldWith` (`internal/rankfit/fit_test.go:29`) so a test that
  wants another label model no longer assigns the shared `plantedShares`, and took
  `go test -race ./internal/rankfit` from 799.9 s (past Go's 10 minute default) to 167.6 s, a figure
  taken before the objective speedup. The GOMAXPROCS determinism check stays sequential, 8 repeats
  rather than 50.
- **ft-8cd27d93 B1-2: a `kbmatch` run that waits more than 10 s for another run exits busy
  (2026-10-21; design note from the build, kept).** The whole run holds the store lock
  (`kbmatchLockWait = 10 * time.Second`, `cmd/vani/kbmatchcmd.go:64`; `OpenLocked` at `:308`; `ErrBusy`
  at `internal/decide/kbmatch/store.go:39`), so the loser asks nothing that day and says so. Fail-safe by design.
- **ft-84d4fe25 A1-1: the DevPrism binary and tools hash are pinned for the process lifetime
  (Fable, delegated; 2026-10-21).** The daemon reloads the whole config per run, except the schedule
  and `devprism.binary` / `devprism.tools_hash`, which its scorer was started with: a change logs
  "applies after the daemon restarts" and the running value is kept (`cmd/vani/app.go:216-218`, table
  at `:232-246`). Restart the daemon to apply a DevPrism change.
- **ft-bafd7500 (Mac app), two accepted limits (2026-10-21).** `BlockingWork`
  (`macos/Sources/VaniApplyKit/BlockingWork.swift:13`) runs on a GCD concurrent queue whose width the
  system caps at about 90-100 threads: a 1000-call burst peaked +19 threads, and a short call queued
  behind 300 one-second calls waited about 3 s. `BankStamp` (size, nanosecond mtime, inode;
  `macos/Sources/VaniApplyKit/BankStore.swift:103-106`) cannot see an in-place same-size edit that
  restores the mtime (`cp -p`, `rsync --inplace -t`); `BankStore` writes replace the file by rename,
  so only a hand edit does this.
- **Process: no card filed through GitPulse can reach "done" from an agent session (2026-10-21;
  a tooling gap, not a Vani defect).** GitPulse gates "done" on a Manvi verification record, but
  Manvi's task store does not know GitPulse task ids (`devcouncil_verify_task`: "no task is checked
  out", and "no verification record exists for this task: Manvi's work.verify.task has not judged
  it"), so every card of the 2026-10-20 audit sits in `review`. The `review` column is not
  unfinished work: read each card's log for what was built and measured, and do not re-report it.

## Title prefilter (built 2026-10-19; live since the 2026-10-20 ranking audit, per RANKING_PLAN)

- **Enabling it: install the binary first.** `Parse` refuses unknown fields, so a
  `title_signals.read` in `config.json` makes every older `vani` fail to load the config. That
  includes `run-once`, the daemon and the Mac app's calls. The order: merge, rebuild and install
  `vani`, then run `vani prefilter` (it should print "off"). Next, add the `read` entries
  (docs/TRACKS.md shows them) and run `vani prefilter` again to see the count and false drops. A
  track's decider can carry them instead; `vani track use` copies them.
- **Measured precision (dry run on a 10:58 copy of the live ledger, with the approved lists).**
  1,313 of 2,663 live postings would be dropped. d1-ultra had read role_fit on 270 of the
  postings it drops: mismatch_domain 232, mismatch_seniority 21, adjacent 17, strong_fit and
  partial_fit 0. `Principal` (43 live) and `Distinguished` have no model reading in the ledger.
  They are kept on what the words mean, unmeasured. Re-run `vani prefilter` after a change to the
  lists; any strong_fit or partial_fit it lists is a false drop.
- **Reversal.** `vani retriage --title-drops` returns the title drops that the current rules no
  longer drop.
- **The track deciders have no `read` entries yet.** `drafts/deciders` is personal and outside git,
  and this session's write gate could not edit it. If the owner runs `vani track use ai-engineer`
  or `ml-inference-systems` without them, the config's `read` entries are cleared, since
  `title_signals` is copied as is. To fix, paste the `read` block from TRACKS.md into each file's
  `title_signals` and run `vani track show <id>`, which checks it.
- **Outage detection.** A title drop is no backend reading. It does not reset the unread
  breaker, and it does not stop a phase in which no backend read anything from failing
  (`pipeline.AdvancePlan.TitleDrop`).

## Run speed (applied 2026-10-19, measured on fakes only)

- **Why:** a run advanced postings one at a time and waited for the d3-mini shadow on each:
  about 4.5 s per posting on 2026-10-19 against about 1 s on 2026-10-18 without it.
- **Owner's choices:** option A, `decide.shadow_every` = 12; `decide.Router.ShadowSample` added
  in the decide module; up to 3 calls past the OpenRouter daily cap with 4 workers is acceptable
  (`DailyCap.Allow` and `Charge` are separate steps; `TestAdvanceConcurrentSpendCapHolds`).
- **What changed:**
  - `advance_concurrency` (default 4): `pipeline.Advance` starts postings in rank order and counts
    results on one goroutine. A stop starts nothing new and waits for the postings in flight.
    The unread streak can admit up to workers-1 extra attempts.
  - `decide.shadow_every` (default 12): hash sample by posting key (`cmd/vani/shadowsample.go`).
    A local shadow allows server `parallel` + 1 calls, and a sampled posting is skipped while
    that is full. Unsampled and skipped postings carry no shadow keys, which `vani eval` reads as
    not asked. `TestGatedShadowChangesNoDecision` checks that statuses and triage annotations
    match a run with no shadow.
  - Ordering: title rank, then freshness, then ledger order (`cmd/vani/advanceorder.go`).
- **BenchmarkAdvance** (fakes at 1/100 of measured latencies: d1-ultra triage 0.65 s, d3-mini
  3.2 s at parallel 1, notes 0.35 s, scoring 0.02 s assumed; 96 postings; M5 Pro; real s/posting):

  | run | s/posting | shadowed / busy-skipped per 96 |
  |---|---|---|
  | before: 1 worker, shadow every posting | 3.76-3.82 | 96 / - |
  | before: 1 worker, no shadow | 1.17-1.25 | - |
  | 4 workers, no shadow | 0.30-0.43 | - |
  | 4 workers, shadow every posting | 3.29-3.88 | 96 / - |
  | 4 workers, one in 12, 1 slot | 0.32 | 5 / 2 |
  | 4 workers, one in 12, 2 slots (shipped) | 0.34-0.35 | 7 / 0 |
  | 4 workers, one in 4, 2 slots | 0.48-0.49 | 14 / 10 |

  The 1-slot row is why the limit is parallel + 1: at about 80% shadow load a no-queue slot loses
  a large share of the sample (Erlang-B), and a waiting call costs about 0.01 s per posting. The
  sample is 7 postings per run, so the busy-skip rate is a small-sample figure.
- **Not verified:** no live run (not to be run against the live config by this work); the
  live d1-ultra and llama-server latencies under concurrency, and DevPrism's scoring time.

## Answer and job dedup, fill speed (applied 2026-10-19, uncommitted in the main checkout)

- **What changed:**
  - **Bank store** (`BankStore.swift`): bank.json is the source of truth, and SQLite is resynced
    to the same revision. Before this, a missed mirror write followed by a capture rewrote
    bank.json from stale SQLite rows.
  - **Capture** (`conceptKey`/`findOwner` in `extension/src/bank.ts`, `Concept.swift`): a
    capture lands in the existing answer for the same question, with the new wording added.
    Both languages replay `test/vectors/concept.json`.
  - **Answers page:** "Find duplicates" merges in one validated `store.update`.
  - **Jobs** (`reconcile_identity.go`, `neardup.go`): before triage, Reconcile marks Duplicate
    for the same job-id URL, for the same company/title/location on two ATSes, and for a
    near-identical JD at the same place. A different place is only `dup.sibling_of`, and a
    partial match is only `dup.candidate_of`.
  - **Fill:** match index, MutationObserver waits (every document and shadow root the scan
    walked), a 2 s bank memo, and up to 3 rounds for dependent fields.
  - **Revive** (`reconcile_revive.go`): a Duplicate that is still listed comes back Live when its
    survivor is Dead or gone, and it then demotes any live twin it outranks. A weak posting is
    never made a duplicate of a Dead ATS record (`atsFor`).
- **Applied on the owner's go-ahead ("keep persistent connection; Batched job-ledger; Sharing
  triage results"):**
  - **Persistent port** (`extension/src/nativeport.ts`): bank ops go over one `connectNative`
    port, matched to replies in FIFO order. A timeout poisons the port, so the next call
    reconnects. Slow ops (draft, jobFind, jobMark, jobsIngest) and Safari keep one process per
    message. Host time per call drops from 6-8 ms to 0.06-0.11 ms.
  - **Host idle exit:** the host no longer exits on idle while a frame is waiting on stdin.
  - **`Ledger.AppendBatch`:** one fsync per batch, all-or-nothing on validation. 1,000 events
    take 4.85 ms instead of 4,569 ms. Near-dup's first pass takes 174 ms instead of about 3 s.
  - **Keep triage across a near-identical edit** (`editkeep.go`): kept when shingle Jaccard is
    at least 0.90, with the same role title and place, and no changed shingle that holds an
    authorization, work-mode or seniority term. At most 3 kept edits in a row, then triage is
    asked again.
  - **Sibling triage copying: not built.** On the bench ledger, 0 of 201 siblings had a twin
    dropped on a JD-content slot. Asking d1 only the location slots saves under 2% of a call.
- **Embedding layer,** on branch `claude/wizardly-curie-dbba78` (`56fced3`, `dfde033`, `17e877b`;
  cut from rank-relevance):
  - **Code:** the vector cache, `vani answers similar --json`, and a lexical veto (a one-word
    swap or a contrast word stays "possible").
  - **Cutoffs:** "likely" at 0.95 and "possible" at 0.88, measured on 106 labelled pairs with no
    false merge at "likely".
  - **Adapter:** the near-dup cosine adapter embeds the title too.
  - **Merged** on `claude/wonderful-hawking-3e840a`: rank-relevance, then that branch, then main,
    then the wiring. `runOnce` passes `nearDupSimilarity` (`cmd/vani/semanticwire.go`) as
    `NearDupOptions.Similarity`. With semantic matching off, a model that will not load, or a
    build without `-tags tessl`, it is nil and the stage runs on shingles alone
    (`neardupsim_test.go`). Until that branch reaches main, the Answers page says
    similar-wording grouping is unavailable.
  - **Done (branch `claude/neardup-sim-budget`, on top of `claude/wonderful-hawking-3e840a`):** a
    slow cosine (a cold vector cache) no longer spends the stage's 2 s deadline. The readings
    share a budget of their own, `NearDupSimilarityShare` (half) of the deadline, and a breaker
    stops them after `NearDupSimilarityBreaker` (3) errors in a row. Pairs past either are left
    without a reading and counted (`SimilaritySkipped`, `SimilarityBudgetSpent`,
    `SimilarityBreakerOpen`); the shingle pass finishes, with the outcomes of a run without the
    cosine (`neardup_simbudget_test.go`). A cut from the stage's own deadline is `Partial` as
    before, and `PartialInSimilarity` says whether it came during a reading.
  - **Open:** the budget protects the shingle pass only from the cosine's share. A shingle pass
    that needs more than the other half of the deadline can still be cut during a reading
    (`PartialInSimilarity`). How long the shingle pass alone takes on a large ledger is not
    measured yet.
  - **Open:** a daily run loads the model up to three times: near-dup, the warm before the advance
    phase, and the warm before the digest. Each load takes 1-2.5 s.
  - **Open:** the cosine only reads pairs the shingles already found (overlap at least 0.60). A
    reworded re-post under that overlap gets no annotation.
- **Near-dup audit** (build/rsi-bench-state ledger copy, 2,639 live records):
  - Before the fix, 70 shingle Duplicates were written, 43 of them with different raw titles.
    Most were different jobs on one templated JD:
    - seniority ("Staff" vs "Senior Staff");
    - bracketed team or term ("(Fintech)"/"(Healthtech)", "(Summer 2027)"/"(Winter 2027)");
    - remote roles in other places, because every remote posting compared equal.
  - Now a Duplicate needs the same `roleTitle` and the same places, remote included. The run gives
    8 Duplicates, and all 8 are the same job. There are 160 candidates and 201 siblings, and a
    rerun writes nothing.
  - Tests: `TestNearDupDifferentRolesOnOneTemplateAreNeverDuplicates` and
    `TestNearDupRemoteRolesInDifferentPlacesAreSiblings`. Both failed when the fix was mutated
    back to the old rule.
- **Known limits:**
  - A Mac-app edit can take up to 2 s to reach a fill (the memo window). The persistent port
    removed per-call spawn cost, so this memo is now the main source of staleness. A host push
    was designed and deferred: about 60 lines of Swift, and the `notify` key is reserved.
  - Whether an idle open port keeps the MV3 worker alive in current Chrome is not tested.
    Correctness does not depend on it, because a closed port reconnects.
  - A write sent within microseconds of the host's idle exit is rejected loudly; a read is
    retried.
  - The Answers merge sheet and the Jobs duplicate badges were not checked on screen. They
    compile and their logic is tested. The app was launched against a synthetic bank
    (`tmp/ui-store`), but the session could not bring it into the user's full-screen Space.
  - Fixed and checked at runtime: the app's Safari state check logged "data race detected" at
    launch. SafariServices declares that completion handler `NS_SWIFT_UI_ACTOR`, so a closure
    literal passed to it was inferred `@MainActor` but was called on a background queue.
    `AppModel.safariState` and `AppModel.showSafariSettings` now pass a plain `@Sendable` value
    instead. The startup log went from 2 warnings to 0, which checks `safariState` only.
    `showSafariSettings`'s handler is not main-actor in the SDK, so its change is precautionary
    and was not exercised at runtime. There is no unit test; the evidence is the relaunch log.
- **Leftovers to remove by hand** (deleting needed a confirmation this session could not show).
  All are under the gitignored `tmp/`:
  - `tmp/neardupbench/` and `tmp/batchbench/` (Go programs marked `//go:build ignore`).
  - `tmp/neardupbench-scratch*/`, `tmp/batchbench-scratch/`, `tmp/benchstore/` and
    `tmp/ui-store/`.
  - `tmp/uitest/`, `tmp/nmping.ts`, `tmp/dupaudit.ts`, `tmp/siblingmeasure.ts`,
    `tmp/portbench.ts` and `tmp/mutate.sh`.
  - Branch `embed-similar`, which has no unique commits; it is an ancestor of rank-relevance.

## Triage backlog and Workday coverage (2026-10-19, branch no-gaps)

- **Why NVIDIA, Adobe and Broadcom got no triage.** The 09:57 run in `.state` predates `58e8f9c`
  (freshness order, 11:07), so it advanced in `ledger.All()` key order: every `wd:` key sorts last,
  and the cap and the cancel (13:36) came before any of their 127 postings. The bundled app helper
  (17:36) has the freshness order. No code change was needed for this part.
- **Undated postings sorted last forever (fixed).** Greenhouse `first_published` was read only from
  `365a405` (07:46), and an unchanged listing wrote no event, so 1,051 Databricks, Scale AI and
  Together AI postings stayed undated and sorted after every dated posting. Reconcile now writes a
  `re-dated` event (status and annotations kept; `Summary.Redated`) when a same-content posting
  stored undated is listed with a date. A stored date is never moved, and a dropped posting gets no
  event. Checked on a copy of the live ledger with live listings: 1,051 undated live postings → 10
  (Databricks 861, Scale AI 179, Together AI 78 re-dated). Tests: `redate_test.go`; the parity test
  now varies `PostedAt` and requires the re-dated path.
- **Title rank was off.** `titleRank` reads only `title_signals.include`/`exclude`, and the live
  config had only `read`. The owner approved an include list (LLM, AI Engineer, Applied AI, Machine
  Learning, ML, Inference, GPU, CUDA, Performance, Agent, GenAI, Backend, Systems, New College Grad,
  Intern), now in `.state/config.json`. Both the bundled helper and this branch's `vani config check`
  accept it.
- **Workday per-board cap (owner's choice: a higher per-board cap; "increase the cap if needed").**
  `Board.max_postings`, Workday only, 1..`sources.MaxPostingsCeiling` (10,000); zero is
  `workday.MaxPostings` (5,000). It is the board's listing limit: a board whose own count (its
  total, or its facets past the 2,000 window) is over it fails before paging, never truncated.
  Workday stops counting one query at 2,000: a larger board reports `total: 2000`, and offsets 2000
  and 2020 return the first page again (checked live on NVIDIA). The adapter
  (`internal/sources/workday/crawl.go`) fails a listing that serves its first page again or a page
  with nothing new, and lists a larger board by splitting it on one of its facets, as
  `extension/src/crawler.ts` does. Cost: listing is a request per 20 postings. Detail requests are
  bounded apart from it at `workday.MaxDetails` (300) per board per run. Stored postings are reused
  (a thirtieth re-read a day), new ones the title rule drops are not fetched, and the rest wait for
  the next run. NVIDIA "LLM" lists 431, "inference" 443, "deep learning" 1,008, "TensorRT" 136, "new
  college grad" 73 (live totals, 2026-10-19).
- **Open:**
  - The config loader refuses unknown fields, so `max_postings` cannot go into the live config
    until a build with this branch is installed in `Vani Apply.app/Contents/Helpers/vani`. The
    bundled 17:36 helper would reject the whole config.
  - Eightfold (Microsoft, Qualcomm) has the same 200 cap and per-posting detail request;
    `max_postings` is refused there until its paging limits are checked.
  - Intel's board listed 35 postings through this adapter at 21:00 but none during the 09:57 run.
    The cause went to stderr only; the per-board run record (`claude/focused-diffie-81a9b8`) keeps it
    from the next run on.
  - d1-ultra answered 44 HTTP 429s, 30 timeouts and 28 HTTP 502s in that run. Jev already retries
    with backoff and honours Retry-After; nothing here paces the four workers together.
  - **NVIDIA's board after install (measured 2026-10-19, listings only, no details).** Distinct
    postings as terms are added: "new college grad" 74, + "LLM" 497, + "inference" 716, +
    "TensorRT" 729, + "CUDA" 984, + "deep learning" 1,477. Recommended: `search` "new college grad",
    `searches` ["LLM", "inference", "TensorRT", "CUDA"], `max_postings` 1200 (984 now, about 22%
    headroom). This was measured before the facet split. With it
    (`claude/relaxed-burnell-e60996`), the whole board (about 2,750, no search) fits the default
    limit of 5,000. It lists in 16 queries and about 150 requests, and spends at most 300 detail
    requests a run. Tested on a fake NVIDIA-shaped board only, not run live from the daemon.
  - **At merge, with the uncommitted jobsIngest work in the main checkout:** the `re-dated` case
    goes once into the shared `reconcile(..., full bool)` loop that `Reconcile` and
    `ReconcilePartial` both call, before `default`. Add a `reconcile_partial_test.go` case: posted
    date missing, then supplied, gives `re-dated`. The Workday save guards must compare against the
    board's effective cap: in Go, `workday.CheckSize` (`claude/relaxed-burnell-e60996`, which replaces
    the main checkout's uncommitted one) does, by `capFor`. `extension/src/crawlrun.ts`
    (`DAEMON_WORKDAY_MAX = 200`) must become 5,000 (`workday.MaxPostings`), since a board saved from
    the extension carries no `max_postings`.
    **Done at the merge (`2841b02`, 2026-10-19).** The case is in the shared loop, and the test is
    `TestReconcilePartialRedatesAnUndatedScrape` (`redate_test.go`). The main checkout's
    uncommitted `CheckSize` gave way to the branch's `checksize.go`. `DAEMON_WORKDAY_MAX` is 5,000.
  - **Workday detail reuse:** a stored posting is re-read only on its turn (once in 30 days, when the
    run's 300 detail requests are not all spent on new rows), so an edited description can go
    unseen for up to a month. New postings the title rule drops are never fetched or stored, so
    `vani prefilter` and the ledger do not count them. A board fails rather than defer rows when
    a stored posting's URL does not match its listed path.

## Shadow evaluation (applied 2026-10-19, not yet measured on the live ledger)

- **Three gaps in what `vani eval` could tell you, fixed on the owner's go-ahead ("apply all",
  T = 0.4).** Found by a read-only audit and re-verified in code. Use `vani bench local` to compare
  candidate models offline; this section is about the live shadow's record. Context and the 382/382
  reproduction: "d1-3B against your own labels" under Unverified.
  - **Run output.** `runOnce` prints `vani: shadow "<name>": N call(s) or slot(s) failed, timed out
    or came back unusable this run` after the advance phase when `Router.ShadowErrors()` is nonzero
    (`cmd/vani/shadowerrors.go`). The count is per run and includes slots the shadow left out.
  - **1. A failed shadow is recorded.** `decide/router.go` `compareShadow` now sets every slot of
    `RouterAnswer.Shadow` to `Unavailable` (detail `auth`, `deadline` or `error`, tagged
    `;backend=<shadow>`) when the shadow errors, panics or times out, so `Shadow` non-nil means
    "was asked". `OnDisagree` fires only for a shadow that read the slot (`Answered` or
    `Abstained`): an outage, a refusal or a `Failed` reading is not a disagreement (the last is a
    narrowing: a shadow value outside the slot's options used to fire it). No new annotation keys:
    `triage.<slot>.shadow_reading=unavailable` and `shadow_backend` are the existing ones,
    `pipeline.Unread` ignores them, and nothing validates the value set. `vani eval` now counts
    those postings as asked, shows them in a "shadow unavailable" column, and the existing
    no-worse-coverage rule applies to them. Tests: `decide/router_test.go`
    (`TestFailedShadowIsRecordedUnavailable`: error, auth, panic, timeout; `OnDisagree` silent, one
    `ShadowErrors` per failed call; `TestOnDisagreeNeedsAShadowThatRead`; the two old assertions
    that `Shadow` stays nil were rewritten), `pipeline/triage_annotations_test.go`,
    `eval/paired_test.go`. Failures from runs before this change are not in the ledger and cannot
    be recovered; the report says so.
  - **2. The shadow is named after its model.** `config.Local.BackendName()` gives
    `local-<model>-<combined|per_slot>`, where `<model>` is the GGUF file name without `.gguf` when
    Vani starts the server (the file is what differs between quantisations) or `decide.local.model`
    otherwise, normalised like `config.BackendName`. It is `shadow_backend` in the ledger and keys
    the eval, so two models, or one model asked two ways, are separate Promotion and Shadow rows,
    each needing its own 30 labels (the Shadow table was keyed by slot only and pooled them too).
    **Limit: 32 bytes** (`jev.MaxNameBytes`); `config.Validate` rejects a longer name instead of
    truncating. `local-vllm-d3-mini-q8_0-combined` is exactly 32, so a longer GGUF file name
    (a version suffix, say) fails `vani config check`; shorten the file name. An OpenRouter model
    named `local-...` is refused. `vani bench` names its backend `local-bench`. Rows recorded
    before this stay `local` (append-only): the 382 d1-3B Q8_0 combined postings; the report labels
    that row as recorded before the model was named. `benchCases` skips any primary that is a local
    shadow (`config.IsLocalShadow`). Alternative not taken: separate `shadow_model` /
    `shadow_questions` keys (no length limit, two more contract keys).
  - **3. The verdict is paired.** `eval/report.go` `Promotion.Verdict`: the 30-label floor and the
    no-worse-coverage rule are unchanged, then a comparison on the postings both answered (at least
    30), counting those where exactly one matched the label (`PrimaryOnly`, `ShadowOnly`). It needs
    at least 10 such postings (`MinDiscordant`) and a one-sided 95% exact (Clopper-Pearson) lower
    bound on `ShadowOnly / (PrimaryOnly + ShadowOnly)` of at least `T = 0.4`
    (`PromotionBound`), computed with stdlib `math`. Reference bounds, computed independently of
    the code and checked in `TestLowerBound`: 15 of 20 gives 0.544 (promotable), 12 of 20 gives
    0.394, 10 of 20 gives 0.302, 25 of 50 gives 0.376 (all not), 10 of 10 gives 0.741. Consequence
    to know: a shadow that matches the primary on nearly everything has few disagreements and
    reads "insufficient disagreements", never "promotable"; and a tie on 20 disagreements no
    longer promotes.
  - **Verification (2026-10-19).** `go build ./...`, `go vet ./...` and
    `go test -count=1 ./cmd/... ./internal/...` in Vani pass, and
    `go test -count=1 github.com/bharathvbcr/decide/...` passes (decide, jev, lappi; the router
    tests also under `-race`). Not done: a live `vani eval` over the real ledger, a live run with a
    failing shadow, and `vani config check` against the live config (its GGUF file name must fit
    the 32-byte limit above).

## Structural

- **Decide module location.** `decide` is local-only (no remote, nothing committed); Vani reaches
  it by `replace => ../../devtools/decide`.
- **Manvi pin.** `go.work` uses the local Manvi checkout (main at ccbe4ed on 2026-10-18), while
  Jarvis pins 5dbf47c. Add a lock file and a check.
- **`vani status --json` re-reads decide's spend file.** `decide.DailyCap` has no reader (only
  `Allow` and `Charge`), so `readOpenRouterSpend` (`cmd/vani/statuscmd.go`) parses the
  `{"version":1,"days":{day:{key:usd}}}` format itself. A test writes the file with the real
  `DailyCap`, so a format change fails there. The fix is a `DailyCap.Spent(key)` accessor in
  `decide`, then delete the parser.
- **Skipped daily runs are history, not last.json** (2026-10-23). A slot more than 6 h late writes
  `runs/history/<id>.json` with outcome `skipped`, the slot time and how late it was. `runs/last.json`
  stays the last run that started. `vani status` adds `last_skip` when that slot is newer than the
  last real run, and the Pipeline tab says "A daily slot was skipped" instead of "No run in over a day".
- **`internal/vault` is built but not wired (left as is, Fable D21, 2026-10-19).** Nothing imports
  it. It implements `resume.HeaderSource` (`internal/resume/tools.go`), and
  `internal/rule7/boundary_test.go` already gates it as a package the decision code may not import.
  `resume.Client.Synthesize` was removed 2026-10-23 with the DevPrism resume path (`vani resume`
  now builds with rkb, whose header comes from the bank), so `HeaderSource` has no caller left
  but the vault itself: either delete the vault and `Header`/`HeaderSource` together, or keep
  them for a header Vani would supply. It brings personal data into the resume path and needs the
  owner's go-ahead. Until then it is kept: its allowlist is sealed (`Allowed()` returns a copy) and
  it refuses with `ErrNoKeychain` off macOS.
- **`vani resume` on rkb (2026-10-23).** Built (docs/RUNTIME.md, "Building a resume"). Open:
  - `kb.vm_report` is not a config key: the validator report is guessed from `kb.code_root`
    (`resumeVMReport`). Without one, figures with no manual verification are dropped as unverified.
  - Done (verified 2026-10-25): `JobKind.resume.usesModel` is true and its timeout is 375 s
    (`Agent.swift`); the extension's `OWN_TEXT` lists `kb`, `lint`, `fit` (`resume.ts:256`); the
    untrusted-marker regexp has one production owner (`llmbackend/unmark.go:16`, which
    `rkbresume` calls).
  - The agent's PATH is the system folders: `pdftotext` is found in Homebrew's prefixes by
    `rkbresume`; Chrome by rkb's own list.
  - Copies to fold: `rkbresume.Runner` (stdout and exit code) could replace `kbsync.Runner`. (The
    claim-token regexp is folded: `kbsync.TokenRe` is the one copy.)
  - An education entry with org and dates is a job to `kbsync.KindOf`, so if one ever gains an
    eligible bullet it becomes placeable in the work history (`rkbresume.canHold`). Today no
    education bullet is eligible.
  - Resume Studio (2026-10-25): per-job tailored resumes. The CLI (`vani jobs resume build|render|
    show|use|revalidate`), the store and the owner's decisions are in docs/RUNTIME.md, "Resume
    Studio". Still open: the pool is about the base resume until rkb round-2 review runs (`vani kb
    sync --dry-run`: 17 entries / 36 slots; the career/ session's handoff); the Swift app has the
    client, model, card, bind-and-log and the Studio window (macos/README.md, "Resume Studio"; what
    only the running app can confirm is under its "Not verified here"), and the extension's keyed path, the
    agent's `resumeJob` and header removal step 2 are done; the Studio's preview cannot select a
    bullet by clicking it (that needs scripts in the page, which stay off); a build
    that falls back to base is kept but never current over a real version; marks' sent version and
    `vani migrate rekey-*` moving the resume folder (keyed by the key's hash); the app and CLI
    binaries must be rebuilt and reinstalled together before the first `use` (marks with
    `resume_version` read as corrupt to an older binary, and now records of schema 2, which
    carry the redraft's instruction, scope and locks). The redraft (`build --from/--scope/--lock/--instruction`),
    `diff`, `current`, the orphan-aware retry and the per-version figure check are in RUNTIME.md; the
    app has the Redraft sheet, the versions and coverage panels, the career-session hand-off, the
    quit prompt and the explicit pool rules (macos/README.md; what only the running app confirms is
    listed there); fit orphans reach the app only through the fit note's text (`(orphan b1, ...)`), not a
    field of the version;
    the estimate types of `jobs resume build --dry-run` and `jobs retriage` are one (`llmestimate.go`).
    Open: `jobs resume show` spends most of its 0.6 s reading the job's keywords (`jobs show` alone is
    0.4 to 0.5 s), not rkb (docs/RUNTIME.md, "Figures").
- **Jev naming left after the Liquid D rename (2026-10-20, branch `liquid-d-rename`).** The decision
  model is Liquid D (`liquid/d1-ultra` over OpenRouter). `config.Default()` (what `vani init`
  writes) now names it and, by the owner's call, no llm: tests that need one set `testLLM`. And
  `kbmatch.backend` `"jev"` is now `"openrouter"`: the old value still loads, `vani` warns about it,
  `--backend jev` still works, and saving writes `"openrouter"`. Stored kbmatch answers keep the
  backend they recorded, so old ones say `"jev"`. A `KBMatch` built in Go with `"jev"` and saved
  as it is still writes `"jev"`. No live path does that, since loading and the flag both
  normalise it. Three names stay on purpose:
  - `<state dir>/budget/jev.json` and its spend key `jev`. `decide.DailyCap` re-reads the file on
    every call, reads a missing file as nothing spent, and takes no cross-process lock. So no
    rename is safe while a daemon or an older binary still holds the old path: it would start the
    day at zero, a second full cap. `TestOpenRouterCapFileKeepsItsName` and the two spend tests in
    `cmd/vani/openrouter_budget_test.go` pin the name. Renaming it first needs a flock in
    `DailyCap`, then a migration that merges the two files' days.
  - `decide/jev`, `jev.Config` and the `.Jev` reading fields. These are the OpenRouter Decisions
    API client, and renaming them is a cross-repo contract change.
  - `eval.LegacyOpenRouterName` and the `jev_*` config aliases, which exist for old data.
  The file name `internal/decide/kbmatch/jev.go` could become `descriptions.go` the next time that
  package is touched.
- **`JobsClient.call` and `PipelineClient.status` are the same twelve lines** (run vani, bound,
  decode `{"error"}`). Extract one `VaniJSONCall` in `VaniApplyKit` when `Jobs.swift` is next
  touched.

## Unverified (Phase 0)

- **Picked notes have not run live (2026-10-18).** Notes are now reasons that d1-ultra picks from
  `<state dir>/reasons.json` (`internal/decide/jdreasons`), replacing a Claude turn per posting.
  The live catalog (14 fit, 14 gap, 13 check) was drafted in a Claude Code session on 2026-10-18
  and installed with `vani reasons import`, because `vani reasons draft` needs Claude and the
  Keychain key is refused until `ANTHROPIC_WORKSPACE_ID` is set. Its `drafted_by` says so. Not
  measured: how well d1-ultra picks among up to 16 options per category, and what
  a three-question call costs. The design gives one pick per category; a yes/no question per reason
  (several scored picks per category, about 15 times the questions per call) was not built.
- **`typesafe/jev-1.13` not tested, and not in use (owner's call, 2026-10-19).** The owner runs
  `liquid/d1-ultra` (below), so no four-question request was ever sent to jev-1.13. It stays
  `decide/jev`'s `DefaultModel` and the model in `TestRequestShapeGolden`
  (`decide/jev/jev_test.go:86`). A config that leaves `decide.jev_model` empty, or sets jev-1.13,
  runs untested against the four-slot question. (2026-10-20: `vani init` now writes
  `liquid/d1-ultra` as `decide.openrouter_model`, and Vani always passes its model, so the decide
  module's default reaches no Vani call; whether to change that shared default is the owner's.)
- **Lappi does not serve the triage families; nothing to calibrate yet.** Checked 2026-10-18,
  read-only. Lappi-decision has no `career.jd_triage` family. It registers the four single-slot
  families (`crates/qd-prep/src/synth_career.rs:57-66`), and its generator is written. All four are
  `task_not_trained` in `docs/caller-contract.md` (rows at lines 85-88) and stated zero-train in
  `data/decisions/v6-allocation.json` (`stated_zero_train_families`: "no pool is built or trained. No
  R2 cap has been ruled"). There is no train cap, so no calibration entry exists. The export
  calibration gate refuses a family without one (`crates/qd-export/tests/calibration_gate.rs`).
  Both copies of the client require and parse `score` (the margin) and `conformal_set`
  (`decide/lappi/client.go:125, 442-487`; DevCouncil `devcouncil/lappi/client.go:124-125, 441-476`), so
  the calibration fields are supported on the client side. The fixture copies match
  (sha256 `0cf04ad7...`, both sides). Rechecked: no `qd` on PATH, `~/.cargo/bin` or
  `/opt/homebrew/bin`; no `~/Library/Caches/qd/` (the default socket dir); no launchd agent; no `qd`
  process. Nothing reads Lappi until an R2 cap is ruled and the four families are pooled, trained,
  calibrated and served.
- **d1-3B against your own labels.** `.state/config.json` runs d1 (`decide.local.server`) as the
  shadow. The ledger holds d1 shadow readings for 382 postings that d1-ultra read (all four
  slots answered by both). The verdict is `vani label` then `vani eval`, and it needs at least 30
  labeled postings that d1 was asked about. Neither the 12-slot synthetic check below nor the
  agreement numbers that follow are evidence for promotion.
  - **Agreement with d1-ultra on real postings (2026-10-19, `vani bench local`, 382 postings, no
    labels).** `vani bench local` replays postings the primary read through a local model with the
    run's exact request. It reproduced the recorded shadow 382/382 on every slot, so the request
    matches production. d1-3B Q8_0 agreed with d1-ultra on:

    | slot | agreement |
    |---|---|
    | sponsorship | 346/382 (90.6%) |
    | role_fit | 236/382 (61.8%) |
    | work_mode | 233/382 (61.0%) |
    | seniority | 202/382 (52.9%) |

    Latency was p50 735 ms and p95 956 ms per posting (four combined questions, Metal), and the
    server started in 5.0 s. The synthetic 0.3 s does not hold on real posting lengths.
    - Where d1-3B disagrees:
      - It reads senior roles as mid (88 times).
      - On role_fit, the costly direction is mismatch_seniority → strong_fit (21 times).
      - It answers not_stated where d1-ultra read onsite (78) or a sponsorship stance (35). Those
        misses are conservative.
    - Where d1-ultra was confident (p ≥ 0.9), d1-3B agreed on role_fit 77/78 and on seniority
      43/46. Most disagreement is on postings d1-ultra itself was unsure of (role_fit p < 0.5:
      58/172 agree).
    - Agreement is not accuracy. Either side can be wrong.
    - The JSON report is `build/bench-d1-3b.json` (gitignored).
  - **vllm-d3-mini against the same 382 postings (2026-10-19, `vani bench local --batch 8192
    --parallel 1`).** The model is `ggml-org/vllm-d3-mini-GGUF` `vllm-d3-mini-Q8_0.gguf`
    (5,157,835,200 bytes, sha256 `214d44cc…793f93d`, matching Hugging Face). It is 4B, built on
    Qwen3.5-4B, and Apache-2.0. The owner approved the download. It is at
    `build/models/ggml-org-vllm-d3-mini-GGUF/` (gitignored) because the workspace gate refuses
    writes outside the repo. llama.cpp 0.7.0 served it over `/v1/systemone`, with no failed
    posting.

    | slot | d1-3B | d3-mini |
    |---|---|---|
    | role_fit | 236 (61.8%) | 296 (77.5%) |
    | seniority | 202 (52.9%) | 303 (79.3%) |
    | sponsorship | 346 (90.6%) | 336 (88.0%) |
    | work_mode | 233 (61.0%) | 218 (57.1%) |

    Each cell is agreement with d1-ultra out of 382.

    - **Latency:** d3-mini runs at p50 3.2 s and p95 4.5 s per posting, against d1-3B's
      0.74 s and 0.96 s, so about 4.4× slower. The flags also differ: d1-3B ran with `-np` auto
      and the default batch, d3-mini with `-np 1 -b/-ub 8192`.
    - **The costly error mostly goes:**
      - role_fit mismatch_seniority → strong_fit falls from 21 to 6.
      - seniority senior → mid falls from 88 to 27.
    - **Its own probability separates its errors from cloud agreement.** With local p ≥ 0.5:
      - role_fit agreed 148/148;
      - sponsorship 298/299;
      - seniority 176/185;
      - work_mode 108/112.

      Below 0.5, role_fit agreed 148/234.
    - **Where it trails:** its losses on work_mode and sponsorship are not_stated answers where
      d1-ultra read a stance: onsite → not_stated 92, hybrid → not_stated 45, sponsors_visa →
      not_stated 28. These misses are conservative.
    - Report: `build/bench-d3-mini.json`. Agreement is not accuracy. `vani label` and
      `vani eval` decide, and d3 is a new candidate under Lappi-decision's pre-registration,
      which names d1-3B only.
    - **d3-mini is the run's shadow since 2026-10-19 (owner's go-ahead).**
      - `decide.local.server` gained `batch_size` and `parallel`, and the run passes them to
        llama-server (`TestLocalServerGetsBatchAndParallel`).
      - `.state/config.json` now runs `vllm-d3-mini-Q8_0.gguf` with `batch_size` 8192 and
        `parallel` 1, and passes `vani config check`. Its deprecated `jev_*` keys were renamed to
        `openrouter_*` in the same edit.
      - A bare `vani bench local -n 20` started exactly that server, on Metal, and answered 20 of
        20 postings across four companies (p50 2.9 s).
      - Shadow readings recorded before the switch are d1-3B's. Until the per-model shadow id
        lands, `vani eval` pools them with d3's.
      - The model file lives in the gitignored `build/models/`. Moving it to
        `~/Code/research/models/` means updating `model_path`.
    - `vani bench local -n N` with N below the population now takes postings round-robin across
      companies (`TestBenchCasesSpreadAcrossCompanies`). It used to take the first N in key
      order, which is one company's.
    - Insight cost: one `vani jobs list` (keywords for every browsable posting, blurb learning,
      scores, sort) took 0.48 s for 5,000 postings of 6 KiB (`BenchmarkJobsInsight5000`,
      `TestJobsInsightScalesTo5000Postings`).
    - `ggml-org/vllm-d3-flash-GGUF` (9B, about 9.5 GB at Q8_0) is the stretch candidate. It
      has not been downloaded.
  - **RSI-Jev v6.0 and decider-2b on the same 382 postings (2026-10-19, `vani bench local
    --endpoint`).** Neither loads in llama.cpp, so each ran behind its own `/v1/systemone`
    server on loopback, measured with the run's exact request. The population is the ledger as
    it stood before 09:00 (`build/rsi-bench-state/`, events before 2026-10-19T09:00), which
    gives the same 382 eligible postings, all `jev` primary, as the d1-3B and d3-mini runs. The
    bench does not print keys, so the identical set is inferred, not checked key by key.

    | model | answered | role_fit | seniority | sponsorship | work_mode | p50 / p95 per posting |
    |---|---|---|---|---|---|---|
    | d3-mini (above) | 382/382 | 77.5% | 79.3% | 88.0% | 57.1% | 3.2 / 4.5 s |
    | d1-3B (above) | 382/382 | 61.8% | 52.9% | 90.6% | 61.0% | 0.74 / 0.96 s |
    | RSI-Jev v6.0-VL 4B, `--effort auto`, fp32 MPS | 192 of 212 tried (partial) | 69.8% | 59.4% | 97.9% | 42.7% | 26.9 / 60.0 s |
    | decider-2b v11, fp16 MPS, `DECIDER_SHARED=0` | 381/382 | 16.5% | 62.5% | 86.4% | 29.4% | 7.8 / 16.3 s |

    Each cell is agreement with d1-ultra; agreement is not accuracy.
    - **decider-2b is broken for this task:** role_fit calls almost everything strong_fit
      (mismatch_domain → strong_fit 106, adjacent → strong_fit 81), and work_mode answers
      not_stated where d1-ultra read onsite (111) or hybrid (95). Its JevArena lead over Lappi
      does not carry over. Report `build/bench-decider-2b.json`.
    - **RSI-Jev is a partial run, on a different subset.** 20 postings passed the bench's 60 s cap,
      then memguard stopped the server (its osascript notice at 11:54:19), so its rows are the 192
      answered among the first 212 postings, not the 382. Its latency is transformers' reference
      DeltaNet path on MPS (fla is CUDA-only), not the model; its card's 23-40 ms is a GB10 with fla.
      It leads on sponsorship and trails on work_mode (hybrid → remote 34, onsite → not_stated 38).
      Report `build/bench-rsi-jev-v6.json`. The same-subset runs of the others were stopped on the
      owner's word (machine under load), so the subset comparison was not made.
    - **decider-ai 1.8.2's shared-prefix path grows memory on MPS:** footprint 6 → 22 GB over about
      110 postings, then an MPS OOM in `shared_prefix.py:205` under
      `PYTORCH_MPS_HIGH_WATERMARK_RATIO=0.4`; uncapped, memguard stopped it at 12:00:17. With
      `DECIDER_SHARED=0` it stayed at 10-20 GB for all 382, at about 7× the latency of the shared
      path (p50 1.1-1.9 s on the postings it reached).
    - The owner chose cloud models for now (2026-10-19). The RSI-Jev venv is
      `research/external/RSI-Jev-v6.0-src/.venv` (gitignored there).
  - The owner decided on 2026-10-18 to port d1 natively to tessl only if it beats Jev. Jev runs
    only with `OPENROUTER_API_KEY` exported, and its `daily_usd` cap is 0.5. (Superseded: the
    cloud model is Liquid d1-ultra, its key also comes from the Keychain, and the live cap is
    $20; see RUNTIME.md.) The promotion table has
    one row per primary backend (d1 vs `jev`, d1 vs `llm`), each needing its own 30 labels. A day
    where the LLM answered after Jev's cap therefore never counts as Jev's baseline.
  - The primary is now `liquid/d1-ultra` (`decide.openrouter_model`, the owner's choice). Since
    2026-10-19 it answers as `d1-ultra` in annotations and eval rows. The 382 earlier rows name it
    `jev`; `vani eval` reads those as `d1-ultra` (primary only, never a shadow), so the numbers do
    not move (`internal/eval/names_test.go`). "Beats Jev" reads as local d1-3B against cloud
    d1-ultra. Lappi-decision's pre-registration amendment 2 records this rename (draft, for the
    owner's review).
  - **The first live runs read nothing (2026-10-18, about 22:10).** `OPENROUTER_API_KEY` was unset,
    so d1-ultra was off. Anthropic refused every call with HTTP 400, "This API key is not scoped to
    a workspace", because `ANTHROPIC_WORKSPACE_ID` was unset; Vani sends `anthropic-workspace-id`
    only when it is (`wire.go`, as Manvi's CLI does). All 300 postings per run stayed Live, and no
    shadow reading was recorded. Since then, the advance phase stops after 20 unread postings in a
    row, or when none of the postings it tried were read, and the run fails. A shell loop then
    stops, and no empty digest is written. Unread postings are summarised in one line, and a
    provider error that differs only in its request id is printed once.
  - `vani label` queues, in order: disagreements, then postings d1 was asked about, then postings
    triaged before the shadow existed. The 604 postings triaged on 2026-10-18 have no d1 reading and
    are not re-asked: `vani retriage` only returns postings that got no reading at all.
  - The qd.sock scoping session recommends about 80 labels: 50 to fit calibration and 30 held out.
    Labelling disagreements first means precision is measured on harder cases than a random sample,
    but the shadow and the primary are scored on the same postings.
  - **Held-out verdict in Vani (2026-10-19, needs the owner's approval before merge).**
    `vani eval --held-out d1-vani-jdtriage-2026-10-18` prints the usual report unchanged, then a
    Promotion table over the pre-registration's held-out postings only. The split ranks every
    labelled key by sha256(salt || 0x00 || key) ascending and holds out the first 30
    (`internal/eval/heldout.go`, `Splits`). It uses the same Promotion arithmetic and the same
    `jev` alias as the all-labels table. Salt and size come from the registered name and are not
    flags. The rule is tested against a digest computed with `shasum`, and the code is tested to
    reproduce the all-labels rows when every posting is held out (`heldout_test.go`). If any label
    line is unreadable, the held-out run refuses, because a missing key could move the split.
    - **Owner decision: what counts as "labelled".** The flag ranks every distinct key in
      `labels.jsonl` (latest line per key). That includes a key whose labels are all `skip` and a
      key with no ledger record. This keeps the split a function of the label file alone. If such a
      key ranks in the first 30, it takes a held-out place but adds nothing to the asked count. The
      registration's rule text does not say whether such keys count. Excluding all-skip keys is a
      one-line filter in `BuildHeldOut`, and it must be decided before any label is read.
    - The flag does not print the labels.jsonl sha256 that the registration's ledger row needs. Run
      `shasum -a 256` on the file at the verdict run.
- **whisper.cpp after the llama.cpp 0.7.0 upgrade:** `whisper-cli --help` runs; no transcription
  was made, because no whisper model is on this Mac.

## Verified (Phase 0)

- **Jobs tab controls reach the model (2026-10-19).** `macos/Tests/VaniApplyUITests` drives the
  real `JobsView` and reads `JobsModel` back. The test bundle re-runs itself as a host process
  (`JobsTabHost`) showing `NavigationStack { JobsView() }` over a fake `vani` with four made-up
  roles; it never starts the app, so no Login Items agent is registered.
  - The driver uses the accessibility API on the host's pid: AXPress on the toolbar Sort and
    Filter menus and their items. Clicks are mouse events the host sends its own window at a
    control's accessibility frame; nothing goes to the window server, so the cursor is not used.
  - The host's app is never activated, so its window is never key; `allowsWindowActivationEvents`
    (macOS 15) lets a first click through. It needs an accessibility-trusted runner, as this Mac's
    is; otherwise, and before macOS 15, the tests skip.
  - The scatter's dots are found from Swift Charts' accessibility bins ("0 to 50", "50 to 100"):
    the bin from 0 gives the plot's edge and width on the square-root age axis, and every other
    bin must agree within 1.5 pt. Gamma and alpha are clicked; a plot read upside down picks delta.
  - On main `fed3ad5` all five pass. Each mutation below fails only its own test:
    - Sort `Picker` bound to `.constant(jobs.sort)`: "the model never reached sort fresh … sort: "default"".
    - Filter fit toggle `set: { _ in }`: "never reached fits [strong_fit] … fits: []".
    - Fit row `.onTapGesture` removed: the click test fails ("fits: []"). The VoiceOver test still passes.
    - Scatter overlay `.onTapGesture` removed: "never reached selection fx:gamma:3 … selection: nil".
    - Fit row `.accessibilityAction` removed: only the VoiceOver test fails ("fits [adjacent]").
  - XCUITest cannot run under SwiftPM ("Device is not configured for UI testing - use of
    XCUIApplication is not supported … in a unit test bundle"). In-process driving still fails as
    before: the process's own accessibility tree is a bare application element, and clicks never
    reach a tap gesture.
  - One run in about 25 once saw the host's accessibility tree empty for 10 s in the Sort test
    (cause unknown). An unreadable children list now prints its AXError in the failure.
- **Lever EU host (2026-10-19).** Vani's real `lever` source and `egress` client
  (`build/phase0-devprism/leverprobe`), free unauthenticated GETs:
  - `quantinuum`, `olx` and `kwalee` (all listed on `jobs.eu.lever.co`) on `api.eu.lever.co`
    returned 121, 55 and 17 postings, all with `jobs.eu.lever.co` posting URLs. On `api.lever.co`
    every one was `board not found`.
  - `spotify` is the reverse: 82 postings on the US host, `board not found` on the EU host.
  - So a board's `region` must match where the company is hosted; the wrong host fails loudly as
    not-found, never empty. Egress's DNS vetting and pinned TLS accept the EU host.
- **DevPrism `--mcp` (2026-10-18, [PHASE0-devprism.md](PHASE0-devprism.md)).** `devprism.binary`
  can be set to `/Applications/DevPrism.app/Contents/MacOS/claude-prism-desktop` with
  `devprism.tools_hash` `1349173b7e4784bf59a8f478d6a4d63fe4f3479e332dba946ad5f09767ecab39`.
  - Every `--mcp` start opens the real `career.db` and runs schema init plus two idempotent
    migrations. On today's db they change nothing: a copy was byte-identical after a session.
  - Only `HOME` selects the data dir.
  - The three tools Vani calls match its shapes on the installed build, and a real `kb sync` against
    a copy added 81 blocks, then reran as 81 unchanged.
  - Ranking input is bounded: every source caps a description at 64 KiB (`srcutil.MaxDescription`;
    ashby, greenhouse, lever, workday, page and its JSON-LD path), far under DevPrism's 1 MiB
    `jd_text` refusal.
  - The installed build (2026-08-22 13:14) is older than the DevPrism checkout. Inferred from
    `git log`: it was built from `24ecbc8` (2026-08-21), the last commit before it. Four later commits
    touch `career_db` or the MCP tools: `777f94b`, `52869d3`, `51b213f`, `90b910c`. Recompute the
    hash after any DevPrism update.
  - **Owner decision before the first real sync: the duplicated work history.** Back up
    `career.db` first. 5 of rkb's 8 `work` blocks duplicate 4 existing DevPrism `experience` blocks
    (same title and org, different ids). Those 4 blocks hold 8 bullets and 4 facts that rkb does
    not carry. Another 5 title matches are at a different org and are not duplicates. The id pairs
    are in `build/phase0-devprism/duplicate-work-blocks.tsv` (gitignored; ids only, plus
    bullet/fact counts). Either delete the 4 old blocks in DevPrism after moving anything worth
    keeping into rkb, or keep both and accept that `resume_gap_analysis` counts evidence from
    either copy.
- **Postings scored while `devprism.binary` was empty (2026-10-19): `vani rescore`.** Those runs
  wrote `scored` with reason "scoring disabled" and no `rank` or `gap.coverage_pct`. Setting the
  binary later did not revisit them, so in the live top 20 of `jobs list --sort priority` every
  posting took the 0.4 coverage prior.
  - What it does: `vani rescore` scores `scored`/`explained` records that have no `rank`, in place.
    The record keeps its status and explanation. It goes through the run's `pipeline.Advance` (`baseAdvancePlan(kindRescore, …)`), so the cap,
    concurrency, cancellation and 20-failure streak are the run's. It calls DevPrism only, never a
    model. `--dry-run` reads a snapshot without the lock. See README "Use".
  - Why a missing `rank` stands in for the reason: the folded record keeps no event reasons. Every
    real score writes `rank`, and a copy of the live ledger showed the two agree.
  - Dry run on a copy of the live ledger, taken 2026-10-19: **1,631 would be rescored** (1,581
    explained, 50 scored), 0 without a description, and 51 digested left alone. The live `.state`
    was not written.
  - **Owner decisions:**
    - Run `vani rescore` against `.state`. Stop the daemon first, since the command needs the
      ledger lock.
    - Whether the 51 digested postings should be scored too. They would keep `digested`.
    - Whether to re-pick the 1,581 kept explanations. They were picked without the coverage, and
      re-picking costs an OpenRouter call each.
    - Whether the daily run should also do this. That would be one `pipeline.Advance` call with `AdvancePlan.Select = rescorable`
      call in `runOnce` after the advance phase. It was left out: it would compete with the live
      backlog (1,186 `live` that day) for `advance_per_run`. The class only arises from an empty
      binary; a DevPrism that is down leaves postings `triaged` and the run retries them.
- **Decisions API, four slots in one request (2026-10-19 02:18 UTC, `liquid/d1-ultra`).** The owner
  approved one paid call, sent by `build/phase0-devprism/jevone`: Vani's real `jdtriage.Question` for a
  synthetic posting, `decide/jev` with `MaxAttempts: 1`, through `egress`.
  - Result: 1 HTTP request, status 200, 755 ms. All four slots answered (`role_fit`, `seniority`,
    `sponsorship`, `work_mode`), each `{"type": "choice", "choice", "probabilities", "confidence"}`.
    `state: {"text": ...}` was accepted. The response names the resolved model
    (`liquid/d1-ultra-20261015`) and `"provider": "Liquid"`.
  - **`usage.cost` is USD**: `{"input_tokens": 664, "output_tokens": 0, "cost": 0.000027888}`, which
    is exactly 664 tokens at the published $0.042 per million input tokens. `decide/jev` charged that
    value as `cost_usd`.
  - Readings: senior, sponsors_visa and remote, each correct for the posting. `role_fit` was
    `mismatch_seniority` (0.93), correct against the default early-career target.
  - `decide/jev` records `confidence` (for example 0.913) as the slot probability, not the chosen
    option's entry in `probabilities` (0.931). That is by design: it prefers `confidence` and falls
    back to `probabilities[choice]` (`decide/jev/jev.go:454-456`), as the email engine does.
  - Set as Vani's decision model on 2026-10-19: `.state/config.json` `decide.jev_model` is now
    `liquid/d1-ultra` (was `typesafe/jev-1.13`). `vani config check` passes. Key env
    `OPENROUTER_API_KEY`; cap `decide.daily_usd.jev` 0.5, about 17,900 postings a day at this call's
    cost.
- **Local d1-3B (Q8_0) on llama.cpp 0.7.0 (2026-10-18).** llama.cpp 0.6.0 refuses the model
  (`unsupported decision model type: lfm2-d1`). The model is at
  `~/Code/research/models/LiquidAI-d1-3B-GGUF/`, sha256 `2f0942d5…eaba77d`.
  - `localserver` starts it in about 2 s and stops it cleanly. A real `vani run-once`, run without
    keys, started and stopped it before failing at "no decision backend". No process was left behind.
  - The check used three synthetic postings and 12 slots, through `decide/jev` (`Name: local`) over
    `egress.Loopback`, with the configured target summary rendered as in a real run.
    - The `combined` question scored 10/12, at about 0.3 s per posting on Metal.
    - Both misses had low probability: a `role_fit` at p 0.22, and a `seniority` at p 0.37 on a mid
      sales role.
    - Without the target summary, `per_slot` scored 9/12 and `combined` 10/12.
  - **CPU (`-ngl 0`) and Metal gave the same choice on all 12 slots.** Probabilities differed by at
    most 0.03, and CPU took about 4 s per posting.
  - A low-probability answer is still read as `answered` (the Jev wire shape has no abstain). Only a
    drop rule's `min_openrouter_prob` turns probability into action, and only for the configured
    OpenRouter model's name (`DropPolicy.calibrated`, `internal/pipeline/runner.go`); `local` is
    only ever the shadow and never drops.
  - **Prompt variant measured:** each option goes out with its fixture description
    (`jdtriage.Descriptions()`), and the target summary is in the state. The options are in
    alphabetical order, not fixture order: `decide/jev` builds `criteria` as a Go map, and
    encoding/json sorts map keys (`decide/jev/jev.go`, `body`). Cloud and local get the same order,
    so the comparison stays fair. The
    qd.sock scoping session found that the bare option strings change 3 of 12 choices and that a
    reordering can move a probability by up to 0.36. A shadow verdict therefore holds only for this
    variant; an adapter that serves d1 has to send the same descriptions in the same order.
  - **Metal only.** `localserver` passes `--device MTL0 -ngl all`, following the owner's rule
    (2026-10-18) that on-device decider runtimes use Metal by default and never fall back to CPU.
    Measured: an absent device makes llama-server exit 1 at startup ("invalid device: MTL9"), so
    without Metal the run carries on without the shadow. The test fake reports unhealthy unless it
    was started Metal-only. A real start took 3.0 s and gave the same 10/12.
  - **What the verdict covers.** The served runtime for d1 is to be a native tessl/qd-metal port
    (Lappi-decision `docs/third-party-decider-runtime-2026-10-18.md` §10/§13), not llama.cpp.
    llama.cpp is the eval path and the port's parity oracle on the same GGUF weights. So a
    promotable verdict from `vani eval` holds for the port only once the port passes parity with
    llama.cpp on the same prompt variant.
  - **Slots and context:** `localserver` leaves `-np` on auto. On 0.7.0 that gives
    `n_slots = 4, n_ctx_slot = 8192, kv_unified = true`, so the four combined questions run in one
    pass over a shared 8192-token pool. A maximum-size posting (past the 24 KiB cap, with the
    summary) answered in 2.1 s without a context error. Do not add `-np 4` alone. Measured with
    `-c 8192 -np 4`, the log shows `n_ctx_slot = 2048, kv_unified = 'false'`, and the same posting
    (5073 tokens) was refused with HTTP 400 ("exceeds the available context size (2048 tokens)").

## Known limits

- **Absence ages every open posting, digested ones included (2026-10-19).** `Reconcile`
  (`internal/pipeline/reconcile.go`) counts a miss for every posting on the board that is not
  dead, duplicate or dropped. Two misses in a row kill it. When the same content is listed again,
  the posting goes back to the status it died in (`died_from`), so a digested posting is not
  triaged or sent in a digest a second time. Dead records written before `died_from` existed come
  back as live. Absence is only as good as the listing:
  - By the `sources.Source` contract, an empty listing means the board has no postings, so every
    posting on it dies after two runs. Each adapter must therefore fail on a response it cannot
    read, never return an empty list for one. Greenhouse, Lever, Ashby, Amazon, Apple, Google,
    Eightfold and page boards already did. Workday now fails on a reply with no `jobPostings`
    array.
  - Workday stops at the first empty page even when the first page's `total` promised more
    (`TestEmptyPageStopsLoopWhenTotalOverstates`). If the service returns an empty page part-way
    through by mistake, the postings it did not reach count as missed. This was an earlier choice,
    kept because a live `total` that overstates would otherwise fail the board on every run.
  - Jibe (`jb`, AMD) fails the board when a page adds nothing before `totalCount` is reached,
    as Eightfold does. Live on 2026-10-19 the unsearched AMD listing read 1278 of 1278 in 13
    pages, so an overstated total has not been seen.
  - **Residual (audit A2-4, 2026-10-20; floor DECLINED 2026-10-21, decision delegated to Fable):** a
    well-formed empty listing during an ATS outage still ages the board. It is treated as aged,
    not suspect, and there is no floor ("never age a board to zero in one run"). Unreadable replies are
    already refused by each adapter (above); `Dead` needs `MissesToDead` (2) consecutive misses
    (`internal/pipeline/reconcile.go:31`, applied at `:258-264`); `died_from` restores the posting when
    its content is listed again (`reconcile.go:191-195`); and a floor would hide a board that was
    genuinely emptied. Revisit if an adapter is seen returning a well-formed empty list while the
    service is down. Also recorded in "Audit 2026-10-19: criteria not built as written".

- **Jibe remote and pay rules are read from one tenant (2026-10-20).** The AMD payload has no
  remote field and no populated `salary_*_value`. `internal/sources/jibe` now marks a posting
  remote when `location_name`'s last segment is `Home Office` or `Home Office_…` (33 of 1286
  live rows), and leads its description with a pay line from `tags2`/`tags3` (1283 of 1286 rows,
  all `"USD $178,500.00/Yr."`-shaped). Both are AMD's conventions; `tags2`/`tags3` are custom
  tags another Jibe tenant may use for something else. The pay parse only accepts that exact
  money shape in one currency, so a different use leaves no pay line rather than a wrong one.
  Not read yet: `employment_type` (29 `TEMPORARY`, 6 `PART_TIME`) and `tags1` (Yes/No, meaning
  unknown).
  - A second tenant, PepsiCo (`www.pepsicojobs.com`, 431 rows of one search), confirms neither
    rule misfires: its `tags2` is a date and `tags3` a job family (0 pay lines; its pay is in
    the description, which `ParsePay` reads), and its `location_name` is dash-separated
    (`Neu-Isenburg Home Office-DEU`), so its home-office roles are missed, never mis-flagged.
    Its rows have no `client_code`, and 190 of 431 ids are Paradox ids (`P1-6635769-1`), which
    the digits-only id check refused, failing the whole board; ids are now any
    `[A-Za-z0-9_-]` segment up to 64 characters, de-duplicated case-insensitively as keys are.
    Its `tags1` holds recruiter names: personal data, never to be read.

- **Live audit of the search-site sources (2026-10-20).** Fixed on branch `sources-live-audit`:
  - Workday: `jobPostingInfo.additionalLocations` was not read, so multi-location postings
    (9 of 20 NVIDIA, 7 of 20 Intel listing rows) kept their primary place only, and NVIDIA
    JR2021784's only remote place (`US, CA, Remote`) was lost; neither tenant sends
    `remoteType`. `Location` now joins them (at most 20). `timeType` now leads as a role
    line (below). Unread: the `workerSubType` listing facet (fixed-term intern/contract).
  - Apple: a multi-location posting has one Pay & Benefits footer per location with different
    ranges (200688031: $175,000–$308,500 San Diego/Seattle, $184,700–$324,800 Cupertino), and
    the adapter read the first footer only. It now leads with a pay line holding the envelope
    of the US footers' base-pay sentences (the body is unchanged). Metro locations with no
    city (`San Francisco Bay Area`) keep their name. Unread: `jobsData.type`, `PIPE` on 12 of
    15 live details including recent ones; "evergreen pipeline" is an inferred meaning, so it
    is not put in front of the decision model.
  - Eightfold: Microsoft marks remote roles only in `efcustomTextWorkSite`
    (`0 days / week in-office – remote`) while `workLocationOption` says `onsite`; both are
    now read. Qualcomm's `locationFlexibility` (`flexible_within_country`) is unread, meaning
    unverified. Qualcomm's search answered 429 with no `Retry-After` on 4 of 6 requests after
    an unspaced burst; Eightfold is now paced (below), at a spacing that is a guess because
    the tolerated rate was never measured. Eightfold's and Apple's own `MaxPostings` are 200
    (Amazon 500, Google 20 per term; `srcutil.MaxPostings` 5000 is only the shared default),
    so broad searches (Microsoft "machine learning" 338, Qualcomm 365, Apple "LLM" 362) fail
    those boards by design and need a narrower search or location filter.
  - Google: a remote role names only states in its locations; field 18 opens with
    `Remote location(s):` (3 of 3 `has_remote=true` results, 0 of 92 others) and now sets
    `Remote`. `has_remote` is not a board filter key (a contract change); fields 20 (1–4,
    likely level) and 11 are unread.
  - `jobinsight.ParsePay`: (1) a not-pay word in a finished sentence before a range no longer
    vetoes it. Every Google posting's "…relevant education or training.\nUS: $188000 -
    $274000 (USD)" read as no pay (0 of 16; 95 of 95 after); a pay word there still names a
    lone amount. (2) A code before a symbol (`CAD $114,400 - CAD $203,900`, Microsoft
    Montreal) read as USD; the code now wins. (3) Every reader of a posting's pay (digest,
    `vani jobs`) goes through `jobinsight.PostingPay`, which gives a bare `$` the currency of
    the posting's places when they all name one dollar country (Canada, Australia, New
    Zealand, Singapore); an explicit code still wins. A province alone ("Markham, ON") or a
    mixed remote scope stays USD. (4) TWD, MYR, CNY, KRW, RON, RSD and SAR are read: AMD's
    1242 pay lines went from 1068 read to 1241. The Armenian dram is left out (its code is
    AMD's name). The Mac app's currency picker is pinned to `PayCurrencies` by
    `TestSwiftPayCurrenciesMatch`.
  - Amazon: no change. Pay is in `preferred_qualifications` text and `ParsePay` read 338 of
    338; `is_intern`, `is_manager` and `university_job` were null in all 768 rows, so the
    description's role flags never fire. `optional_search_labels`' `amazon-cleared-jobs`
    (14 of 768) now leads as "security clearance" in the role line.
  - Shared source plumbing: `srcutil.Pacer` spaces one board's requests within a total wait
    budget (Jibe 5 s / 15 min from AMD's Crawl-delay; Eightfold 2 s / 15 min). `GetJSON`
    retried a 429/503 without `Retry-After` after 5 s, 15 s and 30 s, then failed the board.
    (Superseded on 2026-10-23 when this work was merged onto main: main's jittered exponential
    backoff with per-request, per-board and per-run retry budgets, its `rate_limited` board
    outcome and the host throttle won, and that fixed schedule was dropped. The `Pacer` stays:
    it spaces a board's own requests and sits above egress's one-second per-host floor, which
    a paced wait already spans, so the two never add.) Adapters' structured role facts lead as one `srcutil.RoleLine` in one employment-type
    wording (Jibe `employment_type`, Workday `timeType`, Eightfold role/employment/work site,
    Amazon schedule and clearance), stripped by `SourceText` like pay lines, so a board
    changing only a role field re-renders a stored posting rather than re-triaging it (the
    one-time move of Eightfold's and Amazon's old lead lines cost nothing: the ledger was
    empty). Every API adapter's title goes through `srcutil.Title` (entities, whitespace,
    300-byte cap).
  - Stored postings: reconcile now refreshes a stored posting whose listing changed only in
    location, remote flag, department, URL, apply URL or company (event reason `refreshed`;
    status and annotations kept). An empty listed field never blanks a stored one, and a
    partial (extension) listing may set the remote flag but never clear it. A title-only
    change is still `Unchanged`: triage, the title drop rules and fallback keys read titles,
    so refreshing one under a standing triage would be wrong, and re-triaging title edits is
    a spending decision left open. The same rules now govern the re-rendered, reappeared and
    re-dated paths, which before stored the listing wholesale (blanking stored fields and
    moving a stored posted date). The run record's per-board counts now carry `refreshed`,
    `kept`, `redated` and `revived`.

- **~~A run without a decision backend lists its boards (2026-10-20).~~ Not kept (2026-10-23).**
  Main's later design won on the merge: `--list-only` needs no backend and succeeds, and a run
  that advances is refused before it folds the spool or lists any board
  (`TestFullRunWithoutBackendStillRefusedAfterListOnly`, `TestRunOnceCLIListOnlyWithoutBackend`).
  The advance stop `no_backend` and its notice were removed. Reverting that is the owner's call;
  the snapshot commit holds the old code. What it said: before, `run-once` and the
  daemon failed before reading any board when no backend could be built (no API key), which
  left the Jobs list empty. Now they list the boards and run the engine's own stages (drop
  rules, near-duplicates), record the postings live, then fail with advance stop
  `no_backend`, so the run never records as finished; `--list-only` passes. The app's notice
  says the postings are in Jobs. The inbox poll (advance only) still refuses without a
  backend.
- **The CLI's default config and state are not the app's (2026-10-20).** The app runs `vani`
  on `.state/config.json` and `.state/state` (its `draft-settings.json`); a `vani` started
  without `VANI_CONFIG`/`VANI_STATE_DIR` uses `~/Library/Application Support`, a near-default
  pair nothing reads. Resolved the same day: `vani` now refuses to run when the app's settings
  name another pair than the one it defaulted to, and `vani status` says where each path came
  from; every `vani` reads the OpenRouter key from the Keychain item `openrouter-api-key` when
  the variable is unset (`VANI_NO_KEYCHAIN=1` opts out). The owner moved `.state/` to the
  default location the same day, so the app and a bare `vani` now share one pair. Still open:
  Claude keys for scheduled runs, signing `vani`
  so the Keychain grant survives rebuilds, and installing a daemon (the schedule never fires
  without one; since 2026-10-23 the app's Run on schedule can keep it, below). `docs/RUNTIME.md`
  maps every entry point to its config, state and keys.
  - **Merged with main's schedule agent (2026-10-23).** `vani`'s own Keychain read and the app's
    schedule agent read the same item, so they were made one policy: the environment wins; a
    `vani` the app starts with the decision key gets `VANI_NO_KEYCHAIN=1` (the app is the one
    reader for it, and a refused or unanswered read is not asked again by another binary); a
    `vani daemon` started any other way (the plist) reads the item itself once, at its start,
    bounded to 60 s, and announces the result in `daemon.json` (`source: "keychain"`, or the
    reason there is no key in `reason` and `keychain`). The schedule hint and `vani status`
    say this. Not run against the real Keychain: the tests replace the read.
- **`cmd/vani/jobscmd.go:102` still names `jobinsight.ParsePay`** in a comment; it reads
  `PostingPay` now. Left because that file was mid-edit in another session.

- **Workday serves at most 2000 rows of one query, then page 1 again (seen live, NVIDIA,
  2026-10-19).** `nvidia/NVIDIAExternalCareerSite` answers `total: 2000` while its facets count
  2753 (`timeType` and `workerSubType` agree). Offsets 1980 and 1990 return the last rows; every
  offset from 2000 to 6000 returns page 1 again (`JR2026658` first) with HTTP 200. Before the merge
  `2841b02` the Go adapter was safe only because `offset >= total` stopped it at the capped total,
  and its `MaxPostings = 200` made NVIDIA fail as `ErrTooManyPostings` on every run. Inside a
  facet-filtered query, the applied
  facet's own counts span the whole board (its other options), so they never size that query.
  - **The extension's crawl handles it** (`extension/src/crawler.ts`): it pages to the window,
    detects the wrap, and splits a larger board by a facet whose buckets sum to the board's count.
    Live: 2753 of 2753 listed in 16 queries and 150 requests, about 80 s.
  - **Save is now refused, listing is still open:** `vani jobs ingest` with `save_search` no longer
    saves a Workday search the adapter cannot list. `saveSearch` (`cmd/vani/jobsingest.go`) calls
    `workday.CheckSize`, one limit-1 request per search term, and answers status `invalid` when a
    term is over `MaxPostings`, or when the check could not run. The jobs are still queued. This
    adds no field to the ingest JSON. It checks each term alone, not their union, so a board of
    several small terms can still fail in `List`. `vani watchlist add --ats wd` has no such check.
    The saved board carries only `q`: URL facets are not applied by the adapter, so the reason says
    to narrow by search terms.
  - **Go side, merged (`2841b02`, `internal/sources/workday/crawl.go`):** the adapter pages to the
    window, fails on a wrap or a repeated page, and splits a bigger board by a facet. A board's
    listing limit is `max_postings`, defaulting to `workday.MaxPostings` (5,000), with a ceiling of
    `sources.MaxPostingsCeiling` (10,000). It makes at most 300 detail requests per board per run.
    Stored postings are reused, and new ones the title rule drops are not fetched. Tested on a fake
    NVIDIA-shaped board only; **not yet run live from the daemon.** The installed app's bundled
    `vani` predates it.
  - **Fixed 2026-10-22, wrap at the total:** `salesforce/External_Career_Site` "machine learning"
    counts 40 on its first page and 0 on every later one, and answers offset 40 with page 1 again.
    The walk paged until a short page, so a total that is a multiple of 20 sent it to offset 40 and
    refused a complete listing. It now treats the wrap as the end once every job the first page
    counts has been read (when the total is a positive integer under the window and at least the
    page's rows). It still asks for the page past the total, one extra request, so a first total
    that understates the board cannot cut the listing short and age unread postings. A wrap
    before the total still fails the board, and so does a wrap with no usable total. A later
    page's total is ignored. A board that shrinks onto a full page mid-walk fails that run.
  - **Open, ingest bound:** a crawled job's description reaches Vani only as the 4000-byte
    `snippet` (`docs/EXTENSION_INGEST.md`). The crawl sends the part that describes the role, not
    the company boilerplate, but triage and scoring see that excerpt, not the whole posting.
  - **Run as a loaded extension (2026-10-19, headed Chrome, throwaway profile):**
    - **Worked:** the drawer's "Crawl all pages → Vani", progress, Stop, NVIDIA (135 jobs, all
      descriptions) and a Greenhouse pager (14 pages, 667 jobs) all worked through the native host.
    - **Refused:** the installed app's bundled `vani` (built 20:38) has no `jobs ingest`, so it
      refused every batch. A `vani` built from this tree queued the same 135-job batch.
    - **Not tried:** saving a daily search.
  - **Saving from the extension:** a Workday board over 5,000 jobs (`crawlrun.ts`
    `DAEMON_WORKDAY_MAX`, matching `workday.MaxPostings`) is not saved as a daily search. A board
    saved from the extension has no `max_postings` of its own.

- **Salesforce is left off the watchlist (owner's call via Fable, D15, 2026-10-19).** Its
  Workday board is `salesforce/External_Career_Site` on `salesforce.wd12.myworkdayjobs.com`.
  Search totals from one limit-1 cxs search per term, through Vani's egress on 2026-10-19:
  "early career" 148, "LLM" 236, "AI engineer" 886, "Agentforce" 1,557. "early career" is the only
  term seen under the 200-posting cap. "LLM inference", "machine learning engineer" and "new grad"
  got HTTP 429 at Vani's 1 s per-host pace and again 8 s apart, so their totals are unknown. A
  148-posting board needs one detail GET per posting on top of the search pages, so at that rate a
  run would most likely stop on 429 part-way (`srcutil` gives up after 3 attempts) and leave the
  board unchanged. To revisit: probe again from a quieter moment or IP, and add the board with
  `--search "early career"` only if a whole listing completes.

- **Pay is read from text, by fixed rules (`internal/jobinsight/pay.go`).** Display and browse
  filters only; no engine rule reads it.
  - Lever and Ashby now carry their pay into the description (owner's call, 2026-10-19). That
    changed every Lever and Ashby posting's description hash. On the first run after it,
    `Reconcile` (`internal/pipeline/reconcile.go`, the `DescriptionHash() != hash` case) marks each
    one Live with reason `changed` and clears its triage, score, rank and explanation. Each is then
    re-triaged, re-scored and re-explained (decision and LLM spend, under the daily caps), and every
    one not hidden in Jobs is listed in the next digest again. Marks and notes are kept (they live
    in `internal/marks`). A posting dropped before is re-triaged too, and may come back.
  - Greenhouse's job-board API returns structured pay (`pay_input_ranges`) only from the
    single-job endpoint with `pay_transparency=true`, not from the list Vani reads
    (docs.greenhouse.io/job-board.html, checked 2026-10-19). Fetching it would be one request per
    posting, so Greenhouse pay is whatever the posting's own text says. Workday and page boards are
    the same.
  - A bare "$" reads as USD. A Canadian or Australian posting that writes "$" without "CAD"/"AUD"
    near the amount is read as USD and compared as USD.
  - No exchange rates: `--min-salary` leaves out a job that states pay only in another currency.
  - Several ranges in one currency (location bands) become one envelope, so `--min-salary` keeps a
    role if any band reaches the floor. A range that would stretch the envelope past 3× (an intern
    band, a variable-pay range) is left out and the earlier ranges stand.
  - Bounds: 128 KiB of text, 8 KiB of text around currency signs, 4,000 amounts. Measured on this
    Mac (`BenchmarkParsePay`, min of 3): about 0.12 ms for a 64 KiB posting with one range, and
    about 2.5 ms for 64 KiB dense with currency signs (the window cap's worst case).

- **llama-server on a Unix socket leaves the socket file behind** (llama.cpp 0.7.0, `--host
  <path>.sock`). It does so even after a clean SIGTERM (exit 0), and the next start on that path
  fails with `couldn't bind HTTP server socket`. Vani's `localserver` uses a TCP loopback port, so it
  is unaffected. Any socket adapter (the Lappi-decision qd.sock plan) has to remove a stale socket
  itself after checking that nothing is listening. Serving over the socket works: health, then
  `/v1/systemone`. The socket was created `srwxr-xr-x` under the default umask. On macOS, connecting
  needs write permission, so only the owner should be able to connect. That is inferred, not tested
  as another user; keep the socket in a 0700 directory anyway.

- **Lightpanda proxy token is in argv.** The per-run token rides in `--http-proxy` and
  `--proxy-bearer-token`, so another process of the same user can read it with `ps` while a
  board's browser runs. Bounded: the proxy listens on loopback only, the token is fresh per run and
  dies with the proxy, and it only forwards to allowlisted hosts on 443. `lightpanda help` lists
  proxy credentials as flags only (`--http-proxy`, `--proxy-bearer-token`; checked 2026-10-18).
- **One `lightpanda mcp` process per board** (since 2026-10-19; was one `lightpanda fetch` per
  page). `page.MCPBrowser` drives it over stdio JSON-RPC (`goto`, then `html`); its RobotStore is
  shared by the board's pages, so a board asks its host for robots.txt twice per run (Vani's
  Crawl-delay read, once per run, and lightpanda's, once per board) instead of N+2. Measured on
  1.1.0-nightly.10455; the proposal, with the failure-mode table and the `serve`/batched-`fetch`
  alternatives, is `long-lived-lightpanda-proposal.md` in the side-session notes of 2026-10-19.
  What remains:
  - A hung page cannot be abandoned inside lightpanda (it ignores `notifications/cancelled` and
    answers one request at a time), so a load that outlives `lightpandaTimeout` kills the process;
    the next load starts another, which reads robots.txt again. At most 3 restarts per board.
  - Within a board the process keeps cookies between pages (each `fetch` started cookie-less).
    Nothing crosses boards: the board's end resets the process.
  - The page proxy's tunnel lifetime is `page.BoardTimeout`, since one CONNECT now serves a whole
    board. Its per-tunnel byte cap (64 MiB each way) can still be reached by a board with many large
    pages; lightpanda then re-sends the request in flight on a new tunnel, unpaced (measured for a
    tunnel cut mid-request). Raise `MaxBytes` for the page proxy if a live board hits it.
  - Not measured: memory over a 52-page board in one process; redirects (whether robots.txt is
    checked per hop); a tunnel cut while a large body is streaming.
- **Lightpanda has no `structured_data` dump mode;** JSON-LD is parsed from the `html` tool's
  document (the mcp `structuredData` tool exists but is unused). Telemetry is on by default and is
  disabled through `LIGHTPANDA_DISABLE_TELEMETRY=1`.
- **`kb sync --confirm`: built 2026-10-19 on the owner's "Build confirm flow" (audit card 12),
  tested on fakes only, not run against DevPrism.** Without the flag nothing changed: an overwrite
  DevPrism asks about is reported as `needs_confirmation` and skipped. With it, each block DevPrism
  answers `inputRequired` for in that run, and only such a block, has its loss read from
  DevPrism's `requestState` (the dropped bullet and fact ids and the rewritten locked bullets,
  `tools_career.rs:945-952`), printed with the old text, and is confirmed by resending the same
  block bytes with that `requestState` and `{confirm:true}` (`internal/kbsync/confirm.go`).
  `--dry-run --confirm` is refused. The flag is the consent; there is no per-block prompt.
  - The confirmation has to happen in the same run: DevPrism keeps the token in its process's
    memory for 5 minutes, single use, bound to `blockId:sha1(block)` (`elicitation.rs:39`,
    `145-166`; `tools_career.rs:144-148`).
  - DevPrism does not bind the token to the block it replaces (`tools_career.rs:879-911`), so a
    block edited in DevPrism after the run read it would lose the edit unseen. Vani re-reads the
    block just before confirming and reports `stale` if it changed. A window remains between that
    re-read and DevPrism's write. Closing it is a DevPrism change: put a digest of the prior block in
    the subject. Not done; DevPrism was read, not changed.
  - A `requestState` Vani cannot read (not base64 or JSON, a list missing, nothing named, over
    64 KiB) is not confirmed: what would be lost could not be shown.
  - A refusal (`-32002`, e.g. DevPrism restarted between the two calls) is reported as
    `confirm_rejected` and the sync goes on; DevPrism down or timing out stops it with the error.
  - The fakes mirror DevPrism's checks from its source; the wire shape (`requestState` field names,
    base64 of JSON) has not been checked against a running DevPrism.
  - Tests: `internal/kbsync/confirm_test.go`, `cmd/vani/kb_test.go` (`TestKBSync*Confirm*`).
- **kbsync shared bullets: fixed in code, not yet run against the real career.db.** 14 rkb
  bullets are listed by more than one entry. Before the fix, every copy kept the bare rkb bullet
  id. DevPrism keys embeddings by `(owner_id, model)` with the bullet id as `owner_id`, so the
  copies shared one embedding row. A search hit then resolved to an arbitrary parent block
  (`career_db/vectors.rs:629-635`, `LIMIT 1`). Deleting one parent also dropped the embedding the
  other still used (`career_db/mod.rs:526-556`).

  Now every bullet id, shared or not, is `<entry id>/<bullet id>` (`scopedBulletID` in
  `internal/kbsync/blocks.go`).
  - Why every bullet: an id does not change when a second entry starts listing a bullet.
  - Why `/`: it is outside rkb's id alphabet `[A-Za-z0-9._:-]`, so a scoped id never collides
    with a bare id.
  - DevPrism accepts it: `Bullet.id` is an unvalidated `String` (`career_db/mod.rs:49-60`).
    `career_upsert_block` caps only the whole block, at 1 MiB, and every DevPrism lookup compares
    the whole id.
  - Refused: an entry with an empty id, or any id containing `/`, is blocked, never sent.
  - Hashes: `hashVersion` is now 2, so every hash changed. State written before the fix reads as
    "update", never "unchanged".

  **Limit left: a block already synced with bare ids.**
  - `buildUpdate` renames each bare-id bullet to its scoped id. That keeps DevPrism's fields on the
    bullet and does not duplicate its text.
  - DevPrism's overwrite gate counts the vanished bare id as a dropped bullet. It answers
    `inputRequired`, which `kb sync` reports as `needs_confirmation`.
  - A plain sync never confirms. It records no hash, and it asks again on every run.
  - Ways out: `vani kb sync --confirm` prints the dropped bare ids and confirms the rename (see
    "kb sync --confirm" below), or the owner deletes that block in DevPrism and the next sync adds
    it fresh with scoped ids, because `Plan` adds any block DevPrism lacks.
  - No real sync has run, so only the Phase 0 copy of career.db has bare ids.
  - Tests: `internal/kbsync/scoped_test.go`.
- **`kb sync` sends the approved pool only, and `--replace` retires the old blocks (built 2026-10-23;
  fakes and a copy only, the real sync is the lead's step).** Two problems with the first design:
  it sent every bank entry (superseded wordings, unreviewed drafts, 11 CAUTION claims in synced
  bullets), and it never deleted, so DevPrism's 43 blocks from the 2026-08-22 markdown import
  (LiquiTask "Deprecated", WisDev, Chronicle, filler) kept feeding `resume_gap_analysis`, which
  Vani ranks postings with. Now:
  - `kbsync.ResolvePool` (`internal/kbsync/pool.go`) cuts the bank to `kbsync.Eligible`'s verdict
    over `kb.selections` (`eligible.go` owns the rule). An entry no selection lists is not synced;
    a listed entry with no eligible bullet is withheld with a reason; a listed degree with no
    bullets syncs as a bullet-less block. `kb.selections` empty refuses the run. The block hash
    covers the bullet list, so a block whose bullets all pass hashes as before (no `hashVersion`
    bump, no re-send); one that loses a bullet updates, and `buildUpdate` now drops an rkb bullet
    (id under the block's `entry/` prefix) that rkb no longer lists, which DevPrism gates behind a
    confirmation. DevPrism's own bullets stay. `Plan.Cautioned` is computed over what is sent and is
    empty for a pool.
  - `--replace` (`internal/kbsync/replace.go`, `backup.go`, `cmd/vani/kbcmd.go`): orphans are the
    DevPrism blocks the pool does not produce. Only kinds the sync writes (`experience`, `work`,
    `project`, `education`) are deleted; the 2 `publication` blocks have no rkb source to re-add them
    and are spared unless `--delete-all-kinds` (or named with `--keep`). Order: copy `career.db`,
    start DevPrism, check the copy holds exactly the ids DevPrism lists, upsert the pool, delete
    only if every pool block landed (`Result.Landed`), then `career_delete_block` per orphan through
    the same confirmation round trip as upserts (`confirm.go`, now shared); stops at the first
    DevPrism error; a block already gone is not an error; a block changed since the plan is `stale`
    and kept. Guards: at most `--max-delete` (default 100, ceiling 500), and nothing is deleted when
    the pool has no block to sync.
  - **Backup is Vani's.** DevPrism writes no `career-db-backups/`: `rg -uu "career-db-backups"` over
    the DevPrism checkout finds nothing (the only backup code is `plugins/path_guard.rs` for project
    files), and `career_delete_block` removes the row and embeddings with nothing kept
    (`tools_career.rs:1044-1060`). The five files there are not from DevPrism's code. `sqlite3
    .backup` takes a consistent snapshot of a WAL database with a writer present
    (`TestBackupIncludesUncheckpointedWAL`); the copy is read back (`quick_check` ok, ids listed).
    Needs a `sqlite3` binary (macOS ships `/usr/bin/sqlite3`).
  - Not changed, worth knowing: Vani sends `kind:"work"` (`blocks.go:44`); DevPrism's canonical kind
    is `experience` (`src/lib/resume-sections.ts`). Ranking normalizes it (`career_match/selection.rs:93`),
    the UI's kind list may not.
  - Tests: `internal/kbsync/{pool,replace,backup}_test.go`, `cmd/vani/{kbpool,kbreplace}_test.go`.
- **kbsync tagging: built, smoke-tested on a copy, real sync still held by the owner.** DevPrism
  scores synced blocks without embeddings (skills, persona, recency, seniority), so rkb blocks were
  never picked. `hashVersion` 3 now sends skills from the stack, persona `ai`, plain titles/orgs and
  ISO dates, skips rkb variants, and unions skills/personas on update (`internal/kbsync/tags.go`,
  `markdown.go`). On a copy of career.db, Build picked rkb blocks for all three smoke postings, and
  no `](`, `*` or `[` reached the PDFs. Open, the owner's call:
  - **Duplicates now print.** DevPrism's own 43 blocks and rkb's copies of the same work both get
    picked: M.S. Biomedical printed twice (`edu_8eb29acf142a` and `master-of-science-in`), and
    Research Tools still leads with the deprecated LiquiTask (`pro_154b7ff50102`). rkb also has two
    entries for one project (`agent-evaluation-harness-manvi`, `agent-harness-evaluation`), not
    linked by `origin`, so the variant rule does not catch them. Ways out: retire the old DevPrism
    blocks rkb now covers, and merge the two rkb entries.
  - Work entries have no `stack`, so rkb work blocks carry no skills.
  - Bullets: ResolveText strips `**bold**` (as rkb's text renderer does), though DevPrism would
    render it as bold.
  - 18 bullets exist only in the 9 variants and are not synced.
  - Tests: `internal/kbsync/tags_test.go`; goldens in `internal/kbsync/testdata`.
- **Ranking and KB matching (branch `rank-relevance`, 2026-10-19), open items:**
  - Live since 2026-10-19 (main `ab1d4d5`, installed app): the config has `kb.bank`,
    `target_summary`, `semantic.snapshot` and, approved by the owner, `rank_penalties`
    `[{"slot": "seniority", "values": ["senior"]}]` (backup `.state/config.json.bak-rank-relevance`).
    `vani semantic warm` scored all 1,682 triaged postings in 2 min 44 s, none failed.
  - The senior penalty is mild by design: it scales with the reading's confidence, and the live
    senior readings imply about 0.17-0.24 (gates 0.82-0.87). Without the embedding part it takes
    senior readings in the top 20 from 13 to 4 (rankeval real-ledger test; proxy NDCG@20 0.792 →
    0.924, the proxy does not read seniority). With embeddings on, `vani jobs` still lists 7 in
    the top 20: those roles match the knowledge base well. Owner's decision: leave it as is and
    measure again on or after 2026-10-26 (baseline 7 of 20). Count senior readings in
    `vani jobs list --sort priority --limit 20 --json` (`.jobs[].triage.seniority`), run with
    `VANI_CONFIG` and `VANI_STATE_DIR` set to the live `.state` paths. A `title_signals.read`
    for "Senior"/"Sr." is the other lever.
  - ~~Postings scored while `devprism.binary` was empty lack coverage in the live top 20~~ —
    resolved: on 2026-10-20 a fold of the live ledger found 2,287 of 2,301 digested postings with
    coverage and rank (`vani rescore` exists for the rest).
  - The Mac app does not show `score_factors` or `semantic.evidence`, and never passes
    `--half-life` (Score's default is 10 days).
  - Resolved by policy (2026-10-20, docs/RANKING_PLAN.md): at half-life 10 an excellent
    21-day-old posting still ranks below a mediocre same-day one by priority ("what is new"), and
    the digest's Top open section ranks by Relevance ("best open role"), where it leads.
    `TestRTSharpRecencyBuriesExcellent` asserts that and no longer skips.
  - Closed on this branch: displayed `relevance` rests on the known parts (`relevance_known`);
    `vani jobs mark`/`note` rank with the marks as written; the digest ranks with the same drop-rule
    penalties, rank penalties and title signals as `vani jobs` (`cmd/vani/rankconfig.go`); the
    semantic warm runs before triage too; the score file is keyed by model and the warm holds an
    inter-process lock; an escalation shadow's disagreement does not lower confidence (only a
    Lappi-style shadow's does, as DisagreeConf was tuned on those pairs).
  - Checked 2026-10-19: a `-tags tessl` binary signed as bundle.sh signs the helper (hardened
    runtime, Apple Development, identifier dev.vbcr.vani.cli, no entitlements) runs EmbeddingGemma 2
    on Metal with the same results as unsigned. The full app bundle itself was not rebuilt.
  - Tessl's archive pins gusset `984209b`, while the Go side of gusset is the live
    `devtools/gusset` checkout; tessl's frame-version check fails the load on drift.
  - `extension/src/relevance.ts` still scores pages with its own in-page algorithm, not Score.
  - A generic text-vector cache in `internal/semantic` (for answer dedup and near-duplicate
    postings) belongs to the "Embedding unification" session. Until then scores are keyed by
    posting key and text hash: the same JD under several keys is embedded once per key, and a KB
    edit re-embeds every posting (posting vectors are not kept).
  - Similarity breaks title-rank ties in `advance` order but drops nothing, so it saves triage
    tokens only through ordering under `advance_per_run`.
  - An undated posting from its board's first crawl has unknown age. It no longer takes the floor
    (0.25) for good: Score reads it as first seen `SeenDays` ago plus `UnknownAgeDays` (1.5
    half-lives when unset), so freshness is about 0.5 on the day it is seen and falls from there
    (2026-10-20, owner's request: a strong Google posting scored 19 for relevance 75.9, now 39.1).
    This retires the invariant "a known age never scores below an unknown one"; `rankeval` does
    not model `SeenKnown`, so its 60%-undated world (NDCG@10 about 0.45) was not re-measured. The
    live ledger's dated postings average freshness 0.34, so the prior leans toward surfacing: 5 of
    the live top 20 are now undated, 0 before. Google's adapter has read `posted_at` since
    2026-10-19 10:58, after its first crawl; `reconcile.go:219` backfills it on the next crawl, which
    makes these postings dated.
  - Meaning leads, keywords tie-break (2026-10-20, owner's request): `DefaultWeights` now has
    Semantic 0.35 (was 0.25) and Keyword 0.03 (was 0.10); fit, coverage, title and employer are
    unchanged. A keyword-rich posting distant in meaning (semantic .55) used to outrank a close
    one (.80) with no keywords. rankeval's NDCG@10 is flat across these weights (0.7341 → 0.7346),
    so this rests on the principle. Live, the top 15 all read semantic 0.72 or more (69 of 1,682
    postings are at 0.95 or more), so among the best the embedding barely separates them and fit and
    freshness decide the order.
  - Age-neutral employers and a sharper knowledge-base match (2026-10-20, owner's request: tune for
    their work and knowledge base; Google has an application cap, so its date matters little).
    - `age_neutral_companies` (config) → `RankContext.AgeNeutral` → `Inputs.AgeNeutral`. An
      undated posting of such an employer that Vani has seen is held at `NeutralFresh` 0.78 and
      does not age; the fresh factor reports `known: false, note: "age_neutral"`. A date the
      board gives is used as for anyone, and with nothing seen at all the floor stays (missing
      input is never a bonus). The list is the owner's to fill: no employer is built in. On a copy
      of the live ledger with `["Google"]`, the Google "Software Engineer III, AI/ML" posting goes
      from priority 40.9 to 61 (rank 6 → 1) and "TPU Efficiency" from 35.2 to 53.2. A flat 0.78
      puts them above the live dated postings' average freshness of 0.34 on purpose.
    - Not done: `advance` ordering (cmd/vani/advanceorder.go) builds its own `RankContext` with no
      config, so it still ages these postings; it only orders what triage reads first.
    - `internal/semantic` Version 2 → 3. (1) `genericLine` drops degree, soft-skill, years-only and
      work-authorization lines from the match (the posting keeps them). Against the real bank and
      model a bare "Software Engineer" with four such lines scored 0.80 beside 0.81 for an
      LLM-inference posting; with the filter and the discount below it is 0.62 beside 0.83
      (`generic_real_test.go`). That test first asserted a 0.25 margin, then "thin", and now asks for
      0.15 (measured 0.21): the posting is a bare title (weight 2) plus a team-practice line, which no
      rule separates from work, so it keeps two high chunks; the test was lowered to what the design
      can deliver, not tuned until green. (2) Fewer than 8 distinct chunks discounts the score by
      (n/(n+2))/(8/10); full evidence is unchanged. (3) Above 0.8 the score bends toward 1 (tanh)
      instead of clipping. Re-scoring all 1,682 live postings in a throwaway copy (2 minutes):
      postings at exactly 1.0 went 37 → 0, at 0.95 or more 69 → 40, Spearman against the old scores
      0.995, and the top 25 by semantic overlap in 16 of 25 (the old top was a 37-way tie at 1.0).
      `ledger_real_test.go` repeats this against any ledger.
    - Limits found: the embedding cannot tell a company's domain from the job's function (Baseten
      "Product Marketing Manager, Post-Training" reads 0.69, a sales role at an AI company 0.49); the
      gate and role fit do that work. A team-practice line such as "use AI-assisted coding and agentic
      code review" matches an agent project at 0.80 whatever the job, and no rule separates it from
      work; the Amazon "Fees Tech" posting keeps semantic 0.84 on two lines like it. A line repeated
      across one employer's postings would find them, which needs a persistent line-frequency table.
    - The cached scores from Version 2 are ignored until `vani semantic warm` reruns, and every
      posting takes the 0.40 prior until then. `warm_per_run` defaults to 1,000, so run
      `vani semantic warm --max 5000` once after installing the new binary (about 2 minutes for
      1,682 postings). The installed app's `vani` still runs the old ranking until it is rebuilt.
    - `genericLine` leaves out "fast-paced" on purpose: "Work in a fast-paced environment shipping
      CUDA kernels" is a work line (`generic_test.go`).
    - Built after the owner's go-ahead (2026-10-20): `internal/decide/kbmatch` and `vani kbmatch`.
      A Vani-local `career.kb_match` task (like `jdreasons`: no change to the shared `decide`
      module, the pinned `jdtriage/fixture.json` or Lappi), answered by the llm backend by
      default, or by the Liquid decision model (`kbmatch.backend: "liquid"`; the earlier value
      `"jev"` still loads with a deprecation warning) when asked. The decision model is not
      the default because `role_fit` is already its read and this one should be independent.
      Answers live in `kbmatch.json` beside the ledger, not as ledger annotations, so a run needs
      no ledger lock, `internal/pipeline` is untouched and the daemon is unaffected. Score gains a
      seventh additive part (`Weights.KBMatch` 0.15) that is left out entirely when a posting has
      no answer, so an unasked posting's score, relevance and factor list are byte-identical to
      before. The 15% is an unmeasured choice: rankeval has no judged worlds, only the invariant
      that closer options never score lower and the part never gates.
    - Added the same day (owner: "can the embedding model and the decision model be used; a
      cheaper model like sonnet or haiku"):
      - `kbmatch.model` (and `--model`) picks the llm model on its own. It is not
        `llm.triage_model`: that setting also moves the triage LLM backend and escalation
        (`wire.go`, `escalate.go`), so setting it to haiku would have changed `role_fit`'s
        last-resort reader, which nobody asked for. `kbmatch.model` falls back to
        `llm.triage_model`, then `llm.model`. The dry run prices the chosen model at Anthropic's
        published rates (`anthropic.PriceOf`), as the daily cap meters it; the configured prices
        apply only to the configured model and to a model the table does not know.
      - `kbmatch.backend: "liquid"` (or `--backend liquid`; `jev` is the deprecated name) asks the decision model over OpenRouter
        with option descriptions and the same question text, from the OpenRouter cap. Its
        probability is kept. Tested over a local server, not against OpenRouter.
      - The embedding model picks, per posting, the eight knowledge-base lines closest to it
        (`semantic.Matcher.Nearest`: best cosine of each profile line over the posting's chunks,
        stack lines and titles left out, near-duplicates collapsed) and the question carries a
        compact digest (1.3 KB on the live bank, was 6.1 KB) plus those lines. Against the real
        bank and model the closest line to an inference posting is 0.80 and to a nursing posting
        0.68, with at most two of five lines shared (`nearest_real_test.go`). A failure to load
        the model, or on one posting, sends the full digest instead and says so. Whether the
        focused question yields better answers than the full digest is unmeasured: no model was
        called. The answer's identity does not depend on the picked lines (it is keyed to the
        bank, target and posting text), so the Jobs list needs no embedding model to find it; the
        entry records `mode` and `backend` for audit.
    - Not exercised: any provider call. The tests use a fake `decide.Decider` and a local
      server standing in for the decision model. This environment holds neither `ANTHROPIC_API_KEY` nor
      `OPENROUTER_API_KEY`, so `vani kbmatch run` stopped at "no credential for anthropic" on the
      first real attempt, before any request; the prompt's wording (`kbmatch.Prompt`) and the
      reply parse against a real model are unverified. First real run: `vani kbmatch run
      --dry-run`, then `--top 5`, then read the `kb_match` factor on those jobs.
    - Cost (dry run on a copy of the live ledger; 643 of 1,682 postings are not mismatch
      readings; at most 144,000 input tokens for the top 40, 3,600 a posting before the
      embedding model shortens it), typical / at most counting every reply at its full
      1,280-token budget: top 40 on haiku-5-5 $0.02 / $0.04, sonnet-5-5 $0.35 / $0.80,
      opus-5-5 $0.70 / $1.60; all 500 on haiku $0.23 / $0.51, on sonnet $4.54 / $10.19.
    - Hazard: the config loader rejects unknown keys, so a `vani` built before a key existed
      fails to read a config that carries it. `vani jobs` does not fail: it drops the
      target-keyword marking, the semantic scores, the title signals and the drop rules and puts
      the reason in `insight.warning` (checked: an older build lists priorities with no semantic
      match); `run-once` and `daemon` refuse to start. State as of 2026-10-20: the live config
      carries `age_neutral_companies: ["Google"]`. The repo-root `vani` and `build/vani` accept
      it (main merged it); `build/vani-fix` and the app's installed helper (`Vani Apply.app`,
      built before that merge) do not, so reinstall the app before relying on the key there.
      The `kbmatch` key is not in main yet: leave it out of the live config until this branch is
      merged and every `vani` that reads the config is rebuilt. `vani kbmatch run --model
      claude-haiku-5-5` needs no config key.
    - Not built: a daily stage. The command is run by hand; wiring it into the pipeline would
      touch `internal/pipeline` and needs its own decision about when it may spend. The batch is
      not concurrent (one question at a time; the time per question is unmeasured).
  - Lappi's calibrated score is not used for confidence (an answered Lappi reading counts as
    `AnsweredConf` 0.7): Lappi's score is a top-two margin, not a probability, and decide keeps
    the two apart on purpose.
- **Employer lists need a yearly re-import** (IRS eo1-eo4 and IPEDS HD2024 imported 2026-10-18 into
  `.state/state`, 1.6M names). Recheck the IPEDS codes in each year's dictionary (docs/EMPLOYER.md).
  Runs read the derived `index.view` (0.09 s / 69 MB a lookup, was 3 s / 490 MB). Import still loads
  the full index: re-importing eo3 into it takes about 6.3 s and 770 MB at peak (was 6.6 s / 1.0 GB).
- **LCA imports are one file per fiscal year.** Quarterly files are cumulative, so importing Q2
  and Q4 of one year double-counts. Re-importing identical bytes replaces the earlier import
  (keyed by the file's sha256).
- `decide.DailyCap` is safe within one process only; the daemon and a manual `run-once` at the
  same moment could under-count a decision-model (OpenRouter) charge.
- DevPrism reports no aggregate score; the rank is must-have coverage until a better definition
  exists.
- **Vani Apply Mac app, unverified on a live system** (`macos/README.md`):
  - Safari loading the hand-assembled appex: `pluginkit -m -p com.apple.Safari.web-extension` after
    the app is first opened.
  - Whether a development-signed extension needs "Allow unsigned extensions".
  - Signed host/appex opening the App Group container without a prompt.
  - A live put/get from each browser.
  - Safari's native message size limit (undocumented; the bank is capped at 900 KB for Chrome's
    1 MB).
- **Safari and promise-returning `chrome.*`:** `background.ts` awaits `chrome.tabs.query`,
  `chrome.tabs.sendMessage`, `chrome.scripting.executeScript` and `chrome.storage.local.*`. Apple
  documents the `chrome.*` alias but not that these return promises. If the live Safari test shows
  them returning undefined, add a `dist-safari` prelude in `build.ts` that aliases `chrome` to
  `browser`. The native transport already prefers `browser.runtime.sendNativeMessage`.
- **Safari booleans:** the store requires a real boolean for `captureSelfId` and `review`. If Safari's
  bridge delivers JS `true` as a plain NSNumber, every Safari write turns self-ID capture off. The live
  check: a Safari put with `captureSelfId: true` must read back true.
- **Mac app deletions do not win over a browser offline copy:** answers written to
  `chrome.storage.local` during a host outage are merged back by `updatedAt`, so an answer deleted
  in the app meanwhile can return. There are no tombstones yet.
- **Claude wiring (2026-10-18), not yet exercised live:**
  - **No real Opus call yet.** Agent shells cannot read the Keychain item `anthropic-api-key`, so
    no paid call has been made. The first one is a Draft click in Vani Apply, after setting Settings ▸
    Claude drafting to `.state/config.json` and `.state/state`. Its acceptance check: a draft returns
    and `.state/state/budget/llm-<day>.json` records the charge.
  - **The pipeline still needs the key in its environment.** `run-once` and `daemon` have no
    launcher that injects it (the `manvi` shell function covers only `manvi`). Running them needs
    `ANTHROPIC_API_KEY` exported for that command, or a `vani` shell function like the `manvi` one.
    A launchd agent would need its own injection.
  - **First-run cost.** With no decision model and no title prefilter, every live posting goes to Opus for
    triage, at roughly $0.01 to $0.03 each. On 9 boards the $10/day cap runs out on day one. Postings
    the cap refuses now stay `live` and are retried the next day (`ErrTriageUnavailable`). Before this
    change they became `unsure` and were never triaged. The title prefilter (`title_signals.read`,
    built 2026-10-19, see "Title prefilter") stretches the budget once it is enabled.
  - **First `run-once` (2026-10-18 21:15) used a stale `build/vani`,** built before the
    pricedProvider fix. Every LLM call was refused locally, so $0 was spent. 300 postings went
    live → unsure → scored on that non-answer. `vani retriage` (new; `--dry-run` lists) returned
    them to live. It is the repair for any future outage that predates `ErrTriageUnavailable`.
    The pipeline also caps each run at 300 postings, so one run is at most 300 triages plus their
    notes.
  - **Second `run-once` (21:20): Anthropic refused every request with a 400** (300 refused
    attempts, $0). `vani draft` showed why: the owner's key is not scoped to a workspace, so each
    request must carry `anthropic-workspace-id`. Vani built the adapter with `anthropic.New`,
    ignoring Manvi's `ANTHROPIC_WORKSPACE_ID` (and `ANTHROPIC_BASE_URL`). `provider()` now uses
    `NewForWorkspace` as Manvi's CLI does, and Vani Apply has a Settings ▸ Claude drafting
    workspace field that it passes to `vani draft`. Either export `ANTHROPIC_WORKSPACE_ID` with
    the key, or replace the Keychain key with one created inside a workspace.
  - **Postings awaiting triage were invisible (2026-10-19).** The app's ledger held 3,027 postings,
    but only the 283 that reached a digest were listed. All 656 Anthropic postings and 1,983
    others were still `live`, because each run triages at most 300 and no view lists untriaged
    postings. The LLM budget file showed 1,098 refused attempts, but no record of why: only the LLM
    backend's errors were printed (to stderr), and the run kept none of them. Now:
    `advance_per_run` sets the cap (default 300, at most 5000). The run record and
    `status --json` carry the `advance` report and `backend_errors` from every decision backend
    (OpenRouter, LLM, Lappi, local, and the notes picker). `jobs list`/`stats` report `pending`,
    and the digest and the Jobs tab footer show the count. Draining 2,639 postings at 300 a run
    takes 9 runs. Raising the cap is the owner's spend decision.
  - **A board that failed to list was invisible (2026-10-19).** Intel's board (`wd intel/External`
    on intel.wd1.myworkdayjobs.com, search "LLM") answered total=35 when probed live, yet the
    ledger held no `wd:intel` record and the run record did not say why. A board whose listing fails
    (one bad Workday detail fails the whole board, by design: a partial listing would age healthy
    postings as absent) only reached stderr. Now the run record and `status --json` carry `boards`:
    the configured, read, failed, skipped and not-reached counts, the postings listed, and one entry
    per board (ATS, token, company, outcome, listed count, reconcile counts, and a one-line error
    of at most 300 runes for a failed or skipped board). The entries are bounded at 100 boards and
    32 KiB; failed and skipped boards are kept before read ones, and the cut is counted
    (`unrecorded`). The totals are always exact. A cancelled run keeps the boards read before it and
    the one being listed (failed), and counts the rest as not reached. The record reader's bound went
    from 64 KiB to 256 KiB. A worst-case record, with every bound full of six-byte-escaped text, is
    85 KB (`TestWorstCaseRunRecordIsReadable` holds it under half the bound); full backend errors
    plus full board entries would have passed 64 KiB. The Mac Pipeline tab shows the
    boards that were not read, and a run that succeeded with one is a warning, not healthy. Also
    fixed: `cleanText` (behind `oneLine`) wrote a pending space or newline after its bound check,
    so every bounded line could keep one rune more than its limit. Not done:
    - **Why Intel failed is inferred, not known.** Four live probes through the real adapter and
      egress client listed 35 postings with no error (36–40 s each). In the 09:57 run the ledger
      shows NVIDIA reconciled at 09:59:52 and Adobe at 10:01:12. Adobe's 47 details at Broadcom's
      rate (7 in 9 s) take about 55 s, so Intel ran for roughly 20–30 s before it ended with no
      records. That fits a transient detail failure partway through, which fails the whole board
      by design. The next failure's `boards` entry will carry the cause. If it recurs, a bounded
      per-posting retry or skip is a separate decision, because absence tracking must stay honest.
    - **An older installed `vani` reading a new record over 64 KiB reports the last run as unknown.**
      Rebuild the installed binary with the app.
  - **Safari can't draft.** Its sandboxed appex cannot start `vani`, so the draft op answers
    `unsupported`. Fixing that means XPC to the app, or a request queue in the group folder that the
    app serves.
  - **Mark Applied from the extension (2026-10-19).** The extension offers to mark a tracked job
    Applied after a final Submit, or from the popup on any page (`jobFind`/`jobMark` native ops,
    `vani jobs find --url`; `extension/README.md`), in Chrome (native host) and Safari (agent XPC).
    Done: gh_jid and other job-naming parameters, Greenhouse embeds, apply sub-pages, frames,
    offers kept in `chrome.storage.session`, a toolbar badge, the Jobs tab reloading when the app
    comes to the front. Checked end to end with the built `vani-apply-host` and a real `vani` against
    a seeded ledger (finds, mark, refusals, no shell). Not done or not checked:
    - **Not run in a live browser.** Loading the unpacked extension in Chrome, the Safari appex, and
      the agent over a real launchd Mach service (Login Items) were not exercised; the XPC calls were
      tested over an in-process connection and the extension through `bun test` and `test/dom.html`.
    - **Submit is not watched on hosts the manifest does not list** (a company careers page). Widening
      the content-script hosts widens what the extension can read, which is the owner's call; the popup
      row covers those pages on demand.
    - **A fixed set of job-naming parameters** (`internal/joburl.IDParams`, mirrored in
      `extension/src/applied.ts`). A board that tells jobs apart by another parameter is "ambiguous" or
      unmatched, never guessed; add the name to both lists and the shared vectors.
    - **Form submitted by script with no Submit button** gives no offer; the popup row still works.
    - **A frame the top frame cannot reach** (no content script there) shows the question inside the
      frame, which may be small.
  - **Vani Apply launches anthropic drafts through `anthropic-account` (2026-10-19).** The app and
    the Chrome host no longer read the Keychain or keep a workspace id. When the config's
    `llm.provider` is anthropic they start `anthropic-account exec [--account NAME] -- vani …`, and
    the account comes from a Settings picker fed by `anthropic-account list --json`. That replaces
    the workspace field above, and it means a Keychain prompt is now the launcher's (`security`, up
    to 2 min). Two things are open: it has not been run live against the API, and it is not
    installed in `~/.local/bin` yet. Vani still ignores `ANTHROPIC_API_KEY_FALLBACK`, because
    `cmd/vani/wire.go` builds the adapter without arming the fallback.
  - **The host's Keychain read has no timeout.** An unanswered macOS prompt leaves a Chrome draft
    waiting until the extension's own timeout fires. On the anthropic path the read is now the
    launcher's, inside the draft's own 150 s ceiling.

## Skill gaps: `vani jobs gaps` (built 2026-10-19, branch `jobs-gaps`)

- **Not measured on live data.** The live ledger was empty and the config had no
  `target_summary` when it was built, so it has run only on test fixtures and a seeded scratch
  ledger. After the next `run-once` with DevPrism on, run `vani jobs gaps --view all` and check
  that the top rows are skills you actually lack.
- **Keyword gaps need a target summary.** Without one, only DevPrism's gaps are counted and the
  command says so. With a one-line summary, most job keywords count as "not named", which is why
  DevPrism's gaps rank first (by `missing_weight`) and keyword-only gaps follow.
- **DevPrism phrases that are not vocabulary terms are not counted.** `gap.missing` is free text;
  only what `internal/jobinsight/vocab.txt` recognizes aggregates across roles. A requirement
  DevPrism names in words the vocabulary lacks ("on-call rotation experience") is left out, never
  guessed; adding the term to the vocabulary brings it in.
- **The Mac app does not show it yet.** `jobs gaps --json` is the contract (flat: `roles`,
  `with_missing`, `target_known`, `total_gaps`, `gaps[]` with `term`, `cat`, `roles`, `missing`,
  `weight`, `missing_weight`, `companies`, `examples[]`). A "Skills to build" panel in the Jobs
  tab's Insights would read it the way the charts read `jobs stats --json`.
- **Merged into main** (2026-10-19). The only conflict was the `cmdJobs` usage string, which both
  sides rewrote; it now reads `"jobs: expected list, stats, gaps, folders, show, find, ingest, mark
  or note"`.

## Ranking measurement (branch ranking-measure, 2026-10-20)

- Plan, findings and phase status: `docs/RANKING_PLAN.md`.
- Built: `internal/feedback` (impressions and audit judgments beside the ledger), the digest's
  Top open and Explore sections (`digest.top_open`, `digest.explore`), `vani audit` and the
  age-neutral advance order. The recency red-team case is policy now, not a skip.
- Open: the Mac app's New / Top open sections and an audit sheet wait on the uncommitted
  JobsView/JobsModel work in the main checkout. Fail-open and notes-gate counts are on the run
  record and the Pipeline tab (2026-10-23). The semantic floor and the agreement drop rule stay
  Phase 3. The Go Platt and weight fitter is the
  Phase 4-5 session's; they read `<state>/feedback/judgments.jsonl` and must refuse below 100 and
  150 usable judgments.
- An app that calls `vani jobs` does not log impressions yet: only the digest does. Adding a
  `vani audit shown` (or similar) for the app's visible rows comes with the app work.

## Rank learning: `vani rankfit` (built 2026-10-20, branch `claude/admiring-einstein-72e43c`, on `ranking-measure`)

docs/RANKING_PLAN.md Phases 4–5, machinery only: nothing has been fitted on live data (0 audit
judgments, 29 saves, 0 hides on 2026-10-20). The branch is rebased onto `ranking-measure` and
merges after it.

- **Merge with the exclude-config work.** `internal/config/config.go` gains `rank_weights_file`
  (one field after `age_neutral_companies`, and one entry in the absolute-path check). The main
  checkout has another session's uncommitted exclude work in that file: merge by hand, not by
  overwriting either side.
- **Labels.** The weight fit reads `internal/feedback`: `feedback.Latest` judgments (yes above
  maybe above no; skip labels nothing; the threshold is `feedback.WeightFitMin` on usable ones),
  then marks (saved/applied yes, hidden no), then impressions (shown and left unmarked is a no;
  never shown is no label). Only judgments are held out and scored. Platt reads `vani label`'s
  slot labels (`internal/eval`, read only), threshold `feedback.CalibrationMin` distinct postings
  per backend. An audit judgment of a dropped posting is not used as a slot label: "would not
  apply" does not say the reading was right.
- **Recorded scores, one approximation.** A judged or shown posting is fitted on the factors
  feedback recorded then, which is what the owner saw (`rankfit.ItemFromScores`). Those hold
  known parts only, so an unknown role fit takes the prior. ScoreWith gives an option that is set
  but unanswered its ordinal value instead; the record cannot carry that
  (`TestItemFromScoresMatchesItemFrom` pins the one case). A mark nothing showed is scored now.
  Recorded factors were computed under the calibration of their day, not this run's.
- **Shown-but-unmarked negatives are many.** The digest logs every role it lists, so passed
  postings will outnumber saves. Only held-out audit judgments decide whether the file is
  written, so a mark-driven fit that does not order judgments better is refused. If passed
  postings swamp the saves, weight them down or keep only those shown above a saved one.
- **Per-slot calibration (owner decision).** Calibration pools every triage slot of a backend.
  `vani label` asks every slot, so calibrating per (backend, slot) would cost no extra labels and
  would keep easy slots from lifting role_fit's confidence. The change: `Slot.Name` from
  `SlotOf`, calibration keyed `backend/slot`, and `PlattSamples` grouped the same way.
- **The company factor is left out of the fit.** It is learned from the same marks the fit
  trains on, so including it would reward the fit for reading its own labels. Accuracy is
  reported on `log match + log gate + log fresh`, for the fitted and the hand shares alike; the
  live Score still multiplies the company factor.
- **Gate and freshness are not fitted, only diagnosed.** `Weights` has no exponent for them;
  the file carries the seven shares and the calibration. `rankfit` also fits free gate and
  freshness exponents and prints them: far from 1, they say the gate or freshness constants
  (`Mismatch`, `UnsureGate`, `FreshFloor`, `HalfLifeDays`), not the shares, are what is off.
- **Calibration is keyed by the backend as recorded.** Legacy `jev` rows calibrate separately
  from `d1-ultra` rows (no `eval.Names` mapping at score time), so each needs its own 100
  judged postings. kb_match readings carry no backend and stay uncalibrated.
- **`advance` order ignores the file.** `cmd/vani/advanceorder.go` builds its own context from
  run options, not the config's rank weights; it still ranks by the hand weights. `vani audit`
  scores with the file (its context is `vani jobs`'s).
- **`--dry-run` exits 0 even when it says "would refuse".** It is a count, not a readiness probe;
  read its "would run" lines, or run without it.
- **A weight fit that does not beat the hand weights is not written** (exit 1). Phase 5's exit
  also asks that the labelled set replace the synthetic rankeval worlds: not done.
- **Not run on the live state.** `vani rankfit --dry-run` against `.state/state` was not run
  from this branch.

## Board expansion and the triage backlog (2026-10-22)

The owner added 60 boards (79 in all). A list-only run put 8,525 untriaged postings in the ledger,
and the owner read the empty Jobs tab as a sync failure. Five branches merged on main (`61b7397`,
`6af2490`, `c65bc60`, `dde9125`, `9218e2a`):
- the Eightfold v2 fallback (Netflix)
- Workday `wdN.myworkdaysite.com` hosts (Snap)
- Ashby `board_name` (Hippocratic AI)
- a 1 MiB icon cap (OpenAI)
- list-only runs that need no backend
- `jobs list --view pending` and the Awaiting triage tab
- the `schedule not kept` warning in `status` and the Pipeline tab

Verified on merged main: `go test ./...` (60 packages ok), `swift test` (564, 0 failures), and
`bun test extension/test/` (1192, typecheck ok). A list-only run of Snap, Hippocratic and Netflix
against a scratch state dir (`tmp/e2e-newsources`) listed 28, 50 and 77 postings with no key.

Left open:
- **Contract change.** `sources.Board` gained `board_name` (Ashby only). Any vani built before
  `6af2490` refuses a config that uses it, and it refuses `myworkdaysite.com` hosts too. Rebuild
  every installed vani (the app's helper, any daemon) before adding those boards.
- **Extension.** The extension does not recognise `myworkdaysite.com` pages as Workday
  (`crawler.ts` `WD_HOST`, `scraper.ts`, `portal.ts`). Only `applied.ts` `postingKey` folds them.
- **`jobs find --url` on a live posting returns no `row`.** `extension/src/jobrow.ts`
  `parseFindRow` treats that as "no row". This was read in the code, not run in a browser.
- **Swift decoding.** Fixed (2026-10-22). `Job.untriaged` and `Job.prefilterDrop` are `Lenient<>`
  behind the same optional properties (`untriagedWire`, `prefilterDropWire`): a wrong-typed value
  reads as nil (a triaged row) and the list survives.
- **Speed.** The custom `Job` JSON encoder on the list path is not benchmarked.
- **v2 and pcsx ids are inferred, not verified, to be the same.** No host serves both APIs. Keys
  stay `ef:netflix.com:{id}`. Checked again on 2026-10-22 (Microsoft v2: 403 "Not authorized for
  PCSX"; Qualcomm and Microsoft pcsx: 200; Netflix v2: 200) and still not verifiable directly. What
  was verified is that each API's id is the id in its own site's public job URL: Netflix v2 row
  `id` 790316208720 = `canonicalPositionUrl` `/careers/job/790316208720` = the page's `og:url`
  and its embedded `pid`; Qualcomm pcsx row `id` 446720207388 = `positionUrl` = the job page's
  canonical URL and JSON-LD `url`; Microsoft pcsx `positionUrl` is `/careers/job/{id}` the same
  way. The job-URL id is one platform id per posting, so a Netflix v2 to pcsx switch would very
  likely keep keys, but no host answers both APIs to compare, so that stays inferred. If Netflix
  ever enables pcsx, compare one `id` per API before trusting existing keys.
- **Robots parsing was duplicated (fixed).** `page.parseAllowed` repeated `parseCrawlDelay`'s robots.txt
  group state machine. Both now read `parseRobotsGroups` (`internal/sources/page/robotsgroups.go`), one
  RFC 9309 group reader. `TestRobotsParsersMatchLegacy` runs both over 3,000 generated and 27
  hand-written files against the old functions, kept as a test oracle (`robots_oracle_test.go`).
- **Netflix costs one extra request per run.** The 403 pcsx probe runs every time; the result is
  not cached.
- **List-only still starts the local llama.cpp server.** Fixed (2026-10-22). `startRun` starts it
  only when the pass advances (`advance` true); `runnerFor` then neither warns that the local model
  is disabled nor that the shadow is unavailable on a pass that asks nothing. Inbox polls still
  advance, so they start it as before. Test: `cmd/vani/listonlylocal_test.go`.
- **Non-ASCII board names (closed 2026-10-22).** `sources.BoardNameProblem` now takes Unicode
  letters and digits (so `Zürich Labs`, `东京 科技`), still refusing controls, format characters
  (zero-width joiner, bidirectional overrides), non-U+0020 spaces, punctuation (`. / ? # % :`,
  fullwidth lookalikes included), invalid UTF-8, and names over 64 runes or 192 bytes. Combining
  marks are refused, because the standard library has no NFC and refusing marks is how the accepted
  set avoids one letter written two ways; the cost is that a script needing marks (Devanagari,
  Thai) cannot be a board name until `golang.org/x/text/unicode/norm` is an approved dependency.
  Lookalike letters are accepted on purpose: the name only ever reaches one fixed host's path.
  Open: `jobs ingest` derives the token for a spaced name as the lower-cased, hyphenated name, so
  a pasted `jobs.ashbyhq.com/Zürich%20Labs` page gets the token `zürich-labs`, a non-ASCII key
  part (keys accept it; the Mac app's key check refuses only whitespace and controls). Give it an
  ASCII slug with `watchlist add --token` if that matters. (A non-ASCII token was already possible
  before this change for an unspaced name such as `/データ`: `sources.TokenProblem` refuses only
  `:`, and the request path is percent-escaped.)
- **~~The schedule is not kept.~~ Fixed (2026-10-23, branch `worktree-agent-ac2e315dd6ab5a56f`),
  once the owner turns it on.** `vani launchd plist` still puts no key in an agent's environment,
  so the app now keeps the schedule itself: Pipeline ▸ **Run on schedule** registers a login item
  (`vani-schedule-agent`, `macos/README.md` item 6a) that runs `vani daemon` with the decision key
  read from the Keychain into the child's environment only. To enable: install the new app, open
  the Pipeline tab, turn the switch on, approve "Vani Apply" in System Settings ▸ General ▸ Login
  Items & Extensions; the warning goes once a daemon announces itself. Left open:
  - **Not run live.** Registration, the approval step, the first Keychain read from the new helper
    (it is a different signed identifier, `dev.vbcr.vani.schedule.agent`, from the app that made the
    item, so macOS may ask "Allow / Always Allow" once; unanswered for 30 s the daemon starts with the
    model off and `status` says so, then turn the switch off and on) and a real daily run from it were not
    exercised here: tests inject the key and never register anything. Only the supervisor against the
    real `build/vani daemon` was run.
  - **Two ways a model-less daemon stays model-less.** A key added after a refused or unanswered read is
    not picked up until the switch is turned off and on (a retry would prompt again); and `daemon.model`
    is read at the daemon's start, so a config edit that adds an OpenRouter model to a running daemon is
    not reflected until it restarts.
  - **An updated app does not replace the running daemon** (fixed 2026-10-24: the daemon records its
    executable's inode, size and mtime at start and exits 75 between runs when the file at that path
    differs, `cmd/vani/binarywatch.go`). Where it logs `could not identify its own binary`, turn the switch
    off and on (or Register Again) after reinstalling.
  - **`daemon.json` gained `model`.** An older installed `vani` reading it rejects the file as corrupt
    (its reader is strict) and reports no daemon; the new reader tolerates unknown fields. Rebuild every
    installed `vani` together.
  - **The anthropic provider's Keychain prompt is the launcher's** (`anthropic-account exec`) and is not
    bounded by the helper; a daemon that waits on it has not announced itself yet.
- **Unsupported job sites.** Meta, Uber, Groq (Gem), Hugging Face (Workable), Modular (no board),
  Arm (iCIMS), Synopsys (Radancy), Aurora (custom), Two Sigma (Avature) and Citadel (custom).
- **An unnamed Swift test failure under load.** It was seen twice on 2026-10-22/23. The first time,
  the logo-cap agent's `swift test` ran beside a full Go run. The second time, the final-main
  `swift test` (642 tests, "1 failure (0 unexpected)") ran beside `bun test`. Neither run kept the
  failing line, and a rerun on an idle machine was clean. The next time it appears, keep the full
  output (`swift test ... 2>&1 | tee tmp/swift-test.log`) and grep it for `error:`, so the test can
  be named.
- **Monograms.** Hudson River Trading and Marvell have no logo: their sites answer 403 to every
  request. Reachable PNGs found 2026-10-22 on Wikimedia Commons, both fetched by `vani logos
  refresh --force` against a scratch state (fetched 2, failed 0): HRT
  `https://upload.wikimedia.org/wikipedia/commons/3/3b/HRT_Primary_Logo_-_Full_Color.png`
  (721x420, 9,943 bytes, CC BY-SA 4.0, credited to hudsonrivertrading.com) and Marvell
  `https://upload.wikimedia.org/wikipedia/commons/thumb/8/83/Marvell_Logo.svg/330px-Marvell_Logo.svg.png`
  (330x95, 3,277 bytes, a render of the SVG). Both applied to the live config on 2026-10-22
  (`watchlist domain` and `watchlist icon`; `logos refresh` fetched 2, failed 0). The Marvell mark is black on transparent, so it may vanish on a dark
  background; Marvell's own Workday page offers only an SVG (`/MarvellCareers/assets/logo`).
- **Microsoft Eightfold 429 is unexplained, and cleared by itself.** Re-probed 2026-10-23 00:16
  UTC with 5 requests to Microsoft hosts: `apply.careers.microsoft.com` robots.txt 200 (allows
  `/api/pcsx` and `/api/apply`), pcsx search 200 with 10 positions, so the 429 was a time-limited
  throttle, not a block of this IP. There is no other official endpoint to fall back to:
  `jobs.careers.microsoft.com` answers 301 to `apply.careers.microsoft.com/`, and
  `gcsservices.careers.microsoft.com` (the retired jobs.careers search API) fails TLS verification
  from here (its certificate does not name the host; not retried with verification off). v2 on the
  Microsoft host is 403 "Not authorized for PCSX", so v2 is no fallback either. No code change.
  The original note: on 2026-10-22 `apply.careers.microsoft.com` answered every
  pcsx search with 429, no `Retry-After`, an HTML 22-byte body, minutes after the last run. Whether it is a
  burst limit or a sustained edge block is not known; backoff (about 2-16 s, one board budget of 2 min) cannot
  outwait the second. The board now ends `rate_limited` and is retried next run. Pacing stays egress's 1 s per
  host; no `Crawl-delay` is in that robots.txt. Every srcutil source now retries up to 5 times, not 3.
  The cause is still unknown; what was missing (no run cap, no memory of a throttled host) is fixed
  (2026-10-22, branch `worktree-agent-a8080e8b372021eb2`):
  - **Run cap (fixed).** `listBoards` wraps the run in `srcutil.WithRunRetryBudget` (10 min,
    `RunRetryBudget`). Budgets nest: every wait is charged to the board's 2 min and the run's 10 min
    together, or to neither. Once the run's is spent a request that would wait gives up at once
    (`the run's retry budget is spent`): a 429 as `rate_limited`, a 503 as the plain status.
  - **Host memory in a run (fixed).** `srcutil.HostThrottle`, on the context beside the budgets: a host whose
    request ended 429 after its retries is in a probe state. The next request to it (the next board's)
    is sent once, with no retries; a 429 again blocks the host for the rest of the run (no request sent,
    error `host rate limited earlier this run; probed once`); a 200 reopens it. Requests that arrive while the
    probe is out wait for it (ending with their context); a request already retrying stops retrying once
    another finds the host throttled.
  - **Host memory across runs (fixed).** The throttle is seeded from, and written back to,
    `<state>/hosts/cooldown.json` (`srcutil.LoadCooldowns`/`SaveCooldowns`, written with `atomicfile`):
    `{"version":1,"hosts":{host:{"runs":N,"last":RFC3339}}}`. The next run sends one probe, no retries; a
    429 ends the host for the run and counts `N+1` (error `host in cooldown after N+1 rate-limited runs;
    probed once`), any 200 deletes the entry. Backoff: after the second consecutive rate-limited run a
    host is left alone, with no request, for 15 min, doubling per run to 6 h (`not probed until ...`);
    the first rate-limited run earns no window, and 6 h means a daily run always probes. A host no
    board reached keeps its entry; entries older than 30 days are dropped. Bounds: file read at most
    64 KiB (more is ignored with a warning), 64 hosts (most recent kept), count capped at 99, host
    names validated, a bad entry dropped and counted in a warning, a `last` in the future read as a window
    just over (so it is probed), an unusable or unsavable file a warning and never a failed run. A
    cooldown only changes how soon a board fails: the board is never reconciled, so no posting is aged,
    marked missed or dead. No run-record field or outcome was added (the board's `error` carries the
    message), so `Pipeline.swift` needs no change.
  - **Not covered.** Only requests through `srcutil` are remembered: the careers-page source (lightpanda) and
    `egress` pacing are not, and requests made outside the listing phase (pay look-ups, `jobs find`)
    carry no throttle. After a host answers 200 it is open again, so a host that serves page 1 and
    429s page 2 gets the usual retries on page 2 (bursty, not blocked).
- **An interrupted run never reaches the run history.** Fixed (2026-10-22). `dailyJob` calls `archiveInterruptedRun` (`cmd/vani/runhistory.go`) after the run holds the ledger and before it replaces `last.json`: a `running` record is written to `runs/history/<id>.json` with outcome `interrupted` (a new accepted outcome; no end time, since none is known), once and never over an existing entry. The ledger lock is the proof that no other run is live, so the record's pid is not asked (a reused pid, or this process's own pid after a failed final write, must not hide the run); the one check kept is that `daemon.json` does not name a live other process holding that pid. An unreadable `last.json` is reported on stderr and replaced; nothing is archived from it. An older vani reading the history shows an `interrupted` file as a problem row (it does not know the outcome). A run that died is archived at the next run's start, not before: until then it is `last.json` and `status` reports it as interrupted, as it did.
- **Nothing ties a log to its run.** Fixed (2026-10-22). The app starts `run-once` with `VANI_RUN_LOG=<state>/runs/manual.log` (an environment variable, so an older vani ignores it); `useRunLog` validates it (`checkRunLog`: absolute, no `..`, no control characters, a regular file or a new one inside the resolved `<state>/runs`) and `last.json` records it as `log`, next to a new `id` field (the run id). A refused path is said on stderr and not recorded; the run is unaffected. When the app rotates `manual.log` it finds the newest run in `last.json` and the history (`RunLogOwner`) that recorded this file and, if the log was last written during that run (60 s slack), names the rotated file `history/manual-<run id>.log` (else the old `manual-<time>.log`, never over an existing file). The newest claimant, not `last.json`'s run: a scheduled run the daemon made in between records no log. `runs failures` reports `history/manual-<id>.log`, else the recorded file unless a later run recorded the same path ("Log: not kept"); a run with no recorded log still lists the logs that exist. Left: daemon runs record no log (`vani.err.log` holds every run it made, so the report still lists it); a refused run (ledger locked) writes no record, so a following rotation can name its refusal text after the previous run only if that run's name is free and its mtime fits, which is rare and not guarded further.

## Scraped cards with glued titles (2026-10-24)

- **A generic listing card that is one `<a>` gave a title with its badge and location glued on.** Fixed in the extension, not the daemon (`extension/src/card.ts`, used by `scrapeGeneric` in `scraper.ts`). The 40 `pg:revolutpeople-com:*` postings ("Academy LeaderFeaturedOffice: New York") share one `first_seen` to the microsecond and carry `Description == Title`: one extension batch (`inbox.Batch.Postings` stores `Snippet` as `Description`), not the daemon's page source, whose every run of that board fails `RobotsBlocked` (the site's robots.txt, honoured on purpose) and whose `build` takes the description from the posting page. `scrapeGeneric` read `a.textContent`, which the DOM concatenates with nothing between sibling elements. The title is now the card's own headline or first run of text, `data-variant="status"` and badge/chip/pill elements are left out, and a `data-testid`/class `location` row becomes the posting's location ("Austin; Barcelona": its `|` separator elements become "; "). The snippet is what the card says beyond its title, empty when it says nothing: a card has no description, and handing the title back as one made the knowledge-base match read a one-line "evidence" and still mark the posting Thin. A card with no text outside badges and location is skipped; every walk is bounded (600 nodes per pass, depth 24) and fails closed. Tests: `extension/test/scraper.card.test.ts` over `extension/test/fixtures/revolutpeople-biorce-2026-10.html` (a trimmed capture: 68 cards, inline styles dropped).
- **Existing rows are corrected by re-saving, not by the daemon.** Keys are `pg:{token}:{sha256(canonical URL)[:24]}` (`posting.PageKey`), so the fix moves no key and makes no duplicate. A re-scrape sends the same URLs; `ReconcilePartial` finds each known key, the description hash differs (old text was the glued title, new is empty), the source text differs, so the posting takes the clean title and location, its triage/score annotations are cleared (`Changed`, status back to live) and it is triaged again. Needs: rebuild the extension (`bun run extension/build.ts`; `dist-*` are ignored), reload it, open the board's careers page and Save to Vani. Nothing re-reads the board otherwise: the daemon cannot (RobotsBlocked). A title-only change is not applied by an ordinary re-list (`refreshFields` keeps the stored title), which is why the empty snippet matters here.
- **Left: no description for generic boards.** The extension fetches posting pages only for Workday (`crawler.ts`); a generic listing card holds no description, so these postings stay Thin until a bounded detail fetch is built for them. Not built here (a new fan-out against a site whose robots.txt blocks the daemon).
- **Left: company "We have 68 open positions".** `extractCompanyName` falls through to the page's `<h1>` on a page with no site name; the saved board's company and every ingested posting's company carry it (also seen: "Join our team", title "Back to Careers"). `refreshFields` does refresh a company, so correcting the extraction corrects stored rows on the next save.
- **Left: `internal/sources/page/scan.go` takes the whole anchor text** (`htmltext.Text` of the `<a>`) as the third-tier title fallback (`page.build`: JSON-LD, then `<title>`, then anchor text). It has the same shape; it has not produced a bad row because the posting page's `<title>` comes first. Revisit if a board's pages share one `<title>` (this site's is "Careers home").

## Manual Sonnet reading (`vani jobs retriage`, 2026-10-24)

- **Built.** `internal/manualtriage` (append-only JSONL under `<state>/retriage/`, keyed by posting key and the hash of the text the model read; last wins; torn tail skipped; corrupt store fails closed), `RankContext.Manual` consumed in the one slot owner `SlotFor` (now `SlotFor(r ledger.Record, slot)`), the command in `cmd/vani/jobsretriage.go`, `manual_triage` on every job reply, and the app's Re-triage action. Display and ranking only (Rule 7).
- **Left: the replay/lock code in `internal/manualtriage` repeats `internal/marks`'.** Same shape (fold, torn-tail rule, flock with an in-process turn). `marks` is pinned by fifteen test files, so it was not refactored. The follow-up is one shared append-only-fold helper both call.
- **Left: only `role_fit` and the rank-penalty slots order the list.** The other slots (seniority, sponsorship, work mode) are stored and shown, and feed the ranking only where a `rank_penalties` rule names them. The list row's fit chip and the `--fit` filters still read the run's triage map (`Triage`), as they do for an escalated reading; the detail shows the manual reading beside them.
- **Left: the cap check before asking is advice.** `--dry-run` says `cap_reached` when what is left of the day's llm cap is under the call's reserved maximum; the metered provider is the gate (`cap_reached` is also its refusal). The reservation Manvi makes may differ from the estimate.
- **Left: `advanceorder.go` builds its own `RankContext` without the manual lookup.** It orders which unread postings the daemon triages first; a manual reading exists only for triaged postings, so it has nothing to say there.

## Application stages and "Quiet since applied" (2026-10-27)

- **Built.** `vani jobs mark KEY screening|interview|offer|rejected|withdrawn|unstage` sets what happened after applying; `jobs list --stage` filters; the digest lists applied, screening and interview postings with no change for `digest.QuietDays` (10) days. Stages live in `<state>/marks/stages.jsonl` (`internal/marks/stages.go`), not in `marks.jsonl`: an older vani refuses a `marks.jsonl` line with a mark, version or field it does not know (`compat_test.go`), so the mark stays `applied` at every stage and every older reader, the rank labels included, sees what it always saw. `Read` merges the stages in; `ReadMarks` does not open the file.
- **Behaviour of an older binary.** It never opens `stages.jsonl`: an applied posting at any stage reads as applied. If it applies the posting again, the applied time moves and the stage line stops naming it, so the posting reads as applied with no stage until the stage is set again. Rekey by an older binary leaves the stage under the old key. No older binary loses or misreads a mark.
- **Left: `cmd/vani/jobsexport.go`** (untracked in the main checkout, another session's) should carry `stage`, `furthest_stage` and `stage_at` once it lands; they are on every job reply (`jobview.Job`).
- **Left: the row badge** in the Jobs list still shows only the mark (applied); the stage is in the detail pane's Stage row and the row's context menu.
