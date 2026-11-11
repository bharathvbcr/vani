# Plan: a read model that keeps the Jobs tab fast at any ledger size

Status: proposal, 2026-11-11. Nothing here is built. This is the long-term plan, and it accepts a
major refactor of how state is read and cached.

## Why it is slow

Every view, sort, filter or query change in the Jobs tab, every mark/stage/note write, and every
app activation spawns a fresh `vani jobs list` (`macos/Sources/VaniApply/JobsModel.swift:258`).
With Insights open it also spawns `jobs stats` and `jobs gaps`. On the live state (62,734
postings, `ledger/events.jsonl` = 625 MB, 270k lines):

| measurement | value | label |
|---|---|---|
| `jobs list --view new --limit 1000` | 15–23 s wall, ~8 s user CPU, ~1.3 GB max RSS | verified (2 runs; daemon running concurrently) |
| `jobs list --view all --sort company` | 38 s wall, 8.3 s CPU | verified; the extra wall time is contention, not sorting |
| fold only (`jobs folders`) | 3.65 s wall, 2.4 s CPU, 912 MB | measured by the Fable review |
| `learn` keyword pass | `BenchmarkJobsInsight5000` 1.58 s / 591 MB per op; linear, so ~5–8 s at 26k | benchmark verified; extrapolation inferred |

The root cause is structural, not a slow loop. **The only stored form of state is the
append-only event log, and every reader rebuilds everything from it on every call.**

1. `ledger.ReadSnapshot` re-folds all 270k events with every description in memory
   (`internal/ledger/file.go:298`). The 36k dropped, dead and duplicate postings cost as much to
   read as the ones a view shows.
2. Keyword hits are recomputed from the raw description on every read
   (`cmd/vani/jobsinsight.go:349-370`), although they only change when the posting's text does.
3. Nothing stays resident between interactions, so each sort or filter pays steps 1 and 2 again.
4. The app multiplies that: it starts up to three such processes per change, re-lists after
   writes that already return the updated row, and never stops a superseded list.

The log grows with every run, so all of these costs grow with it.

## Phase 0 results (2026-11-11, measured)

`BenchmarkLiveReadModel` (`cmd/vani/jobsreadmodel_bench_test.go`) on the live state, read-only.
Apple M5 Pro, `-benchtime=3x -count=3`: three results per row, each the mean of 3 iterations;
the median result is shown. The daemon may have been
running, so read wall time as an upper bound.

| piece | per call | allocated | stays per request in serve? |
|---|---|---|---|
| fold (`ReadSnapshot` + marks + exclusions) | 2.37 s | 1.70 GB | no: checkpoint + tail |
| `newInsight` full | 2.92 s | 3.02 GB | partly |
| ↳ tokenizing only (`learn`) | 2.56 s | 2.92 GB | no: hits index |
| ↳ everything except tokenizing | 0.21 s | 0.10 GB | yes (upper bound; its file reads get cached) |
| `Boilerplate` from cached hits | 7 ms | 2.6 MB | yes |
| `NewRankContext` over 62k | 4.5 ms | 0.9 MB | yes |
| `newSameCompany` | <1 µs | 0 | yes |
| **`BuildList` new/default, insight prebuilt** | **2.52 s** | **0.85 GB** | **yes, and this breaks the 200 ms target** |
| `BuildList` all/default, all/company | 2.7–4.1 s, 2.4–3.1 s | 0.83 GB | yes |
| JSON encode of the 1,000-row reply | 16 ms | 31 MB | yes |
| marks read, config load | 0.2 ms, 4 ms | small | yes |
| checkpoint candidate: gob decode of 62k lean records | 168 ms | 537 MB (not interned) | cold start only |

**Verdict: no-go for Layer 3 as first drafted.** A resident serve that caches only the fold and
the keyword hits would still spend about 2.5 s per sort or filter in `BuildList`.

A CPU profile of `BuildList` (5 iterations, 14.4 s in total) shows `Enrich` at 73% of it. Most of
that is work that depends only on a posting's text, redone for every in-view row (about 26k) on
every call, although only 1,000 rows are returned:

| hot spot | share of `BuildList` CPU | what it does |
|---|---|---|
| `semantic.TextHash` (`internal/semantic/semantic.go:179`) | 22% | `fmt.Fprintf` copies the whole description into a format buffer just to hash it |
| `parsePay` (via `PostingPay`) | 13% | regexes over the full description |
| `manualReadingSet.state` → `StateOf` | 11% | re-renders the `jdtriage` prompt (1.4 GB allocated) to check whether a manual reading is stale, once per slot per row |
| `ToJob` (`CleanText`, map access) | 14% | text cleanup for display |

The large `memmove`/`memclr` time comes from the same allocations. The Fable review had called
per-row `Enrich` minor; the measurement says otherwise.

What this changes:

- **Layer 2 covers all text-derived facts, not just hits.** These are the posting text
  fingerprint (one hash shared by the semantic lookup and the manual-reading staleness check), the
  parsed pay, and the cleaned display text. They are now measured as hot, which was the condition
  for adding them.
- **`Select` becomes two-phase.** Compute only the sort and filter keys for every in-view row, then
  enrich display fields for the rows actually returned.
- **New Phase 1b (CLI hot-path fixes, no contract change, byte-identical output):** hash by writing
  straight into the hasher instead of `fmt.Fprintf`; compute a record's manual-reading state once
  per row instead of once per slot. These help every reader today, before any refactor.
- Gob decode allocates 537 MB for the lean records. The checkpoint format needs interning or a
  string table, and the 300 MB memory target is unverified until it is measured as retained heap
  rather than allocations.

## Phase 1b results (2026-11-11, measured)

Changes, all on the CLI read path, with byte-identical output:

- `semantic.TextHash` feeds the hash through a pooled 4 KiB buffer instead of `fmt.Fprintf`. The
  hash bytes are unchanged (`TestTextHashMatchesReference` compares it with the old code). At 6 KB
  it takes 2.25 µs instead of 2.5 µs; at 64 KB, 23 µs instead of 38 µs; allocation is 192 B per
  call instead of the size of the text.
- `manualtriage.TextMemo` and `StateWith`: a reading's text hash is computed once per posting
  rather than once per triage slot plus once for the row. A memo entry is reused only while the
  title, location and description are equal.
- `insight.semanticMatch`: the rank and the row share one lookup per posting, so the text is hashed
  once rather than twice. It has the same equality check.
- `jobview.PageRanker` (`Rank`/`Finish`): `BuildList` ranks every job in the view (place, age,
  score, and pay only when the salary sort or a pay filter reads it) and finishes only the page it
  returns. `stats`, `gaps` and `show` still enrich every job whole.

Parity: the live parity dump (`TestLiveJobsParityDump`, 26 calls, 49.6 MB of output over a
copy-on-write snapshot of the live state, clock pinned) is byte-identical before and after. The
baseline was identical across two runs. `TestPagedListMatchesWholeEnrich` compares the paged list
with whole `Enrich` over every view and sort and the pay, relevance, age, place and query filters.
A deliberate mutation (pay left unset in `Finish`) fails it, whereas the existing golden test still
passed under that mutation.

Timing: interleaved A/B, real binaries (old one built with `go build -overlay` from the HEAD
sources), same state copy, minimum of 5 runs per arm, Apple M5 Pro, other sessions running:

| `jobs list` | before | after | change |
|---|---|---|---|
| `--view new` | 6.82 s | 5.25 s | −23% |
| `--view all --sort company` | 7.67 s | 5.54 s | −28% |
| `--view new --sort salary` | 7.19 s | 7.15 s | none (pay is parsed for every row) |
| `--view saved` | 4.26 s | 4.32 s | none (this is the fold + keyword floor) |

Peak RSS stayed at 1.3–1.4 GB in both arms, so memory did not move. What remains is the roughly
4.3 s floor that every call pays, which is the fold plus the keyword pass (Phase 0 timed them at
2.4 s and 2.6 s in a separate run), plus pay parsing for the salary sort. Those are Layers 1 and 2: the checkpoint and the stored text-derived
facts. Phase 1b cannot remove them.

## Phase 2 results (2026-11-11, measured)

What was built (Layer 1), and where it differs from the draft below:

- `ledger.Reader` (`internal/ledger/reader.go`): `OpenReader`, `Advance`, `Snapshot`, `Body`,
  `Hydrate`, `BodyFP`, `Info`. `ReadSnapshotScoped` is now `OpenReader` + `Snapshot` + `Hydrate`.
  `Advance` folds only the new lines. It refolds from scratch when the file was replaced
  (device/inode), shrank, or its last folded line changed (a rollback followed by a longer
  append), and after a failed fold.
- Each posting line's description is fingerprinted (`BodyFingerprint`, SHA-256 of the raw JSON
  string contents, 16 bytes) as it is folded, whether or not it is kept in memory. Phase 3 keys
  stored facts on it.
- Checkpoint: `<state>/readmodel/ledger.ckpt`, gob, interned on load. It is trusted only when its
  format and schema, file identity, covered size, the SHA-256 of the last covered line and 32
  sampled lines all match the events file; otherwise the reader folds in full and `Info` says why.
  `internal/rekey` writes the events file by rename, so a rekeyed log has a new inode and is never
  read through an old checkpoint.
- **No `vani ledger checkpoint` command.** Any reader that folded at least 8 MiB of tail saves a
  new checkpoint, under a try-lock of its own (a reader that finds it held skips the save). That
  covers the case the command was for (scheduling off, nothing else takes the ledger lock) without
  a new entry point.
- The Jobs reads (`jobsStateFor`) load descriptions only for records a view reads the text of
  (`jobsTextScope`: everything except unmarked closed, dropped and duplicate postings).

Parity: the live parity dump is byte-identical to the Phase 1b dump, both cold (no checkpoint) and
warm (a checkpoint saved by the cold run).

Timing: interleaved A/B, real binaries (Phase 1b built with `go build -overlay` from the HEAD
sources), same state copy with a checkpoint, 7 runs per arm. Other sessions kept the load
average near 86 throughout, so wall time ranged 5–48 s for the same command and is not reported.
User CPU time is the measure; its spread within an arm was about 10%, and in every row the slowest
Phase 2 run used less CPU than the fastest Phase 1b run.

| `jobs list` | Phase 1b user CPU (min) | Phase 2 user CPU (min) | change |
|---|---|---|---|
| `--view new` | 6.31 s | 5.23 s | −17% |
| `--view all --sort company` | 5.85 s | 4.86 s | −17% |
| `--view new --sort salary` | 7.31 s | 6.44 s | −12% |
| `--view saved` | 5.03 s | 4.17 s | −17% |

Peak RSS fell from 1.27–1.33 GB to 0.92–1.07 GB. The fold is no longer the floor: what remains is
reading back about 50k descriptions for the text scope and tokenizing them (`learn`), which is
Phase 3.

## Phase 3 results (2026-11-11, measured)

What was built (Layer 2), as `internal/jobfacts` rather than the drafted `internal/jobindex`:

- **Every fact a Jobs view derives from a posting's text**, found by reading the code for every
  description reader on the list path, not only the profiled ones: keyword hits
  (`Vocab.Extract`), the semantic text hash, the manual-reading text hash, stated pay, and the
  posting half of the knowledge-base match identity (`kbmatch.TextPrefix`, a saved SHA-256 state
  that `kbmatch.TextSHA256Of` finishes with the summary and digest). The last one was not in the
  plan. The live state has no kbmatch store, so the live parity dump could not have caught a
  view that still hashed an empty description for it. `TestTextPrefixResumesToTheStoredIdentity`
  pins the old hash as an oracle, so stored answers keep matching.
- Each posting's facts are filed under its key with the `Input` they were derived from (the
  description's `BodyFingerprint`, title, location, remote). A fact is used only while that input
  is the posting's now, and only from a store whose `Version` is this build's (the vocabulary's
  hash and every deriver's answers over a fixed probe).
- `jobsListRead` (list, stats, gaps) loads a description only for an in-scope posting with no
  current fact, derives and saves facts for those, and prunes the store to the postings a view
  reads. When the config's exclusions read descriptions (`exclude.descriptions`), it loads every
  in-scope description as before, because the rule must see the text.
- Every deriver site asks one accessor (`insight.storedFacts`). A record that reaches a deriver
  with neither its text nor a fact fails the command (`textFacts.err`) instead of being answered
  for an empty description.
- The exclusion and prefilter guards now skip a bodiless posting only when a rule reads
  descriptions (`config.Exclude.ReadsDescriptions`); `TestExcludeReadsDescriptions` checks that
  every other rule answers the same with and without the text.

Store: `<state>/readmodel/jobfacts`, gob, written atomically under a try-lock. On the live state
it is 13.8 MB for 26,358 postings and opens in 44–62 ms (77 MB allocated), where the Hydrate of
the text scope and `learn` it replaces cost 0.6–1.5 s and 2.7 s (2.9 GB allocated).

Parity: the live dump is byte-identical to the Phase 1b dump with no store (the first command
derived 26,358 facts, every later one served them), with a full store (all served), and with a
third of the entries removed (17,572 served, 8,786 derived). `TestJobsViewFromFactsMatchesText`
compares the facts read with the text read over every view, sort, stats and pay filter, cold,
warm and after a posting changed. Deliberate mutations (stored pay ignored, stored hits ignored,
either guard reverted) each fail at least one test.

Timing: interleaved A/B of the Phase 2 and Phase 3 binaries over the same state copy with a full
store, 7 runs per arm, user CPU (the load average was again 60–230 from other work, so wall time
is not reported). In every row the slowest Phase 3 run used less than a third of the CPU of the
fastest Phase 2 run.

| `jobs list` | Phase 2 user CPU (min) | Phase 3 user CPU (min) | change |
|---|---|---|---|
| `--view new` | 5.39 s | 2.04 s | −62% |
| `--view all --sort company` | 5.06 s | 1.57 s | −69% |
| `--view new --sort salary` | 6.40 s | 1.72 s | −73% |
| `--view saved` | 4.40 s | 0.99 s | −78% |

Peak RSS fell from 0.81–1.16 GB to 0.52–0.67 GB. Across Phases 1b–3, `--view new` went from 6.8 s
wall (Phase 1b's baseline, a quieter machine) to about 2 s of CPU. What is left per call is the
process start, the checkpoint load (76 MB of gob for 62k lean records) and the ranking itself;
the first two are what a resident serve keeps.

## Phase 4 results (2026-11-11)

What was built (Layer 3, read ops):

- `vani jobs serve` (`cmd/vani/jobsserve.go`). Manvi's serve contract, copied: NDJSON, `{id, op,
  params}` in and exactly one `{id, ok, result|error}` out per request, errors as values, unknown
  fields ignored, major-only `protocol` 1, `hello` reports the ops and each op's deadline.
- Ops `list`, `stats`, `gaps`, `show`. `params.args` are the command's own flags, and the result
  is byte for byte what `vani jobs <op> --json` prints (`TestJobsServeAnswersAsTheCommand`), so
  the app decodes one set of types.
- Resident between requests: the ledger `Reader`, advanced before each read, and the facts store.
  Marks, config, the clock and every other store are read per request, as the command reads them
  (`TestJobsServeReadsTheLatestState`).
- Bounds: 64 KiB request lines (a longer one is refused with its id, and the session lives on),
  4 requests computing at once (past it `E_BUSY`, retryable), a 30 s deadline per request
  (`E_TIMEOUT`, retryable). The drafted 10 s was too short for the first list after a vocabulary
  change, which derives every posting's facts. A newer `list`, `stats` or `gaps` answers the older
  one `E_SUPERSEDED` at once; its computation is not interrupted (the list path takes no context)
  and still counts against the 4. A panic is `E_INTERNAL`. Closing stdin exits once every
  accepted request is answered.

Measured (`BenchmarkLiveServe`, `cmd/vani/jobsserve_bench_test.go`). The state was a copy of the
live state, 62k records, `-benchtime=5x -count=3`. The machine's load average was about 86 from
other sessions, so the table gives CPU time per request (getrusage), not wall time:

| Request | CPU per request (3 counts) | Allocated per request |
|---|---|---|
| first list, `--view new` (Reader from the checkpoint, facts store opened) | 2.39–2.52 s | 1.60 GB |
| warm `--view new` | 1.54–1.58 s | 702 MB |
| warm `--view all --sort company` | 1.16–1.42 s | 554 MB |
| warm `--view new --sort salary` | 1.23–1.45 s | 587 MB |
| warm `--view saved` | 0.34–0.41 s | 176 MB |
| warm `--view new --q engineer` | 1.34–1.52 s | 600 MB |
| warm `stats --view new` | 1.80–1.92 s | 760 MB |

Heap in use after a list, with the Reader and the facts store held: 360 MiB.

**These miss two Phase 6 targets.** A warm view change takes about 1.2–1.6 s of CPU, not under
200 ms. Resident heap is 360 MiB, not under 300 MB. Serve takes away the process start, the
checkpoint load and the facts derivation. It does not take away the per-request ranking.
A CPU profile of warm `--view new` puts the time in these places:

- `jobview.Select`'s allocation and slice growth: 27%.
- `samecompany`'s `groupsOf`, `roleTitle` and `looseLocation`: 15%.
- `location.Parse`, called twice per row: 10–14%.
- The semantic scores file, decoded again on each request: 4%.
- The rest is GC and memclr.

Each of these is a cost per request over every posting. A resident view
would have to cache the ranked selection, keyed on marks, config and the clock bucket. That is
follow-up work, not part of this phase.

## Target architecture

Four layers. Each one owns one thing, and each makes the layers above it cheap.

```
 Swift app (JobsModel)  ── NDJSON over stdin/stdout ──▶  vani jobs serve  (resident, app-owned)
                                                            │ per request: marks, config, now()
                                                            │ → Boilerplate / RankContext / Select / Sort
                                                            ▼
                                     hits index: keyword hits keyed by (key, body fp, vocab version)
                                                            ▼
                                     ledger Reader: checkpoint + tail of events.jsonl,
                                     descriptions read from the log by offset when needed
```

**Language:** Go throughout. It is service and orchestration work, and the ranking code is already
Go. If Phase 0 shows tokenization is still the bottleneck after caching, the tokenizer is a
candidate for a Rust core behind gusset. That is not assumed here.

### Layer 1: ledger `Reader` and checkpoint (contract change; needs the owner's OK)

The event log stays the source of truth and the only copy of each description. Readers stop
depending on reading all of it.

- **One reader, not a second one beside it.** A new `Reader` becomes the only read path.
  `ReadSnapshot` and `ReadSnapshotScoped` turn into thin wrappers over it, and the duplicated replay
  plumbing is deleted:
  ```go
  func OpenReader(dir string) (*Reader, error)          // checkpoint + tail, no lock
  func (r *Reader) Advance() (changed bool, err error)  // new lines; refold on shrink/inode/rollback/corrupt
  func (r *Reader) Snapshot() Snapshot                  // lean records (BodyEvicted set)
  func (r *Reader) Body(k posting.Key) (string, error)  // read the description from events.jsonl by offset
  ```
- **Descriptions stay in the log.** The lean records carry each posting line's `(offset, length)`.
  `Body` reads that range back, as `leanState.note`/`lean.read` and `File.Hydrate` already do. No
  separate body store, no migration and no duplicated descriptions on disk. A separate body store
  is only worth adding if the owner chooses to archive old events (open question 2).
- **Checkpoint.** `ledger/checkpoint.bin` holds the lean records, their body offsets, and the
  covered prefix of `events.jsonl` (offset, size, inode, and a hash of the last line it covers).
  The format is versioned, written to a temp file, fsynced, then renamed. Strings are interned on
  load, as replay's `interner` does today, because a decoded checkpoint otherwise holds a separate
  copy of every repeated company and annotation value and can use more memory than a replay.
- **Who writes checkpoints.** (As built: any reader that folded at least 8 MiB of tail, under its
  own try-lock; see "Phase 2 results". The draft had the lock holder and a `vani ledger checkpoint`
  command write them.) With scheduling off, nothing else takes the ledger lock for days, so a
  checkpoint written only by the lock holder would go stale. The checkpoint only ever holds a fold
  of complete lines, so writing it needs its own try-lock, not the ledger lock.
- **When the checkpoint is not trusted.** If it is missing, corrupt, its inode differs, the file is
  shorter than its offset, or its last-line hash no longer matches, readers do a full fold and the
  reply says so. `internal/rekey` rewrites `events.jsonl` and also removes the checkpoint.
  `internal/ledger` and `internal/rekey` are the only non-test Go files that read the log (checked
  with `rg -uu`).

Gain: every reader (`jobs list`, `show`, `find`, `export`, the extension, the CLI) drops from about
2.4 s CPU and 0.9 GB to loading the checkpoint plus a short tail. The load time is an estimate until
Phase 0 measures it.

### Layer 2: hits index (new `internal/jobindex`)

- Stores keyword hits per posting, keyed by `(key, body fp, vocab version)`. A vocab change bumps
  the version and invalidates every entry. The store has the same checkpoint-plus-append shape as
  Layer 1.
- **Who persists it.** The index can be rebuilt from scratch, so it has its own try-lock rather
  than the ledger lock. Any process that computes a missing entry (the lock holder after an append
  batch, serve, a cold CLI read) appends it if it gets the try-lock, and skips if not. Without this,
  a vocab bump with no daemon running would leave every cold read recomputing 26k hits.
- **Scope (set by Phase 0):** keyword hits, the posting text fingerprint, the manual-reading
  staleness state's text input, parsed pay, and cleaned display text. Location parsing (1.5%) and
  category stay computed per request unless a later profile shows them to be hot.
- Anything that depends on time, marks or config stays per request: `Boilerplate` (it depends on
  the marks-driven in-view set), `RankContext` and score decay (`now()`), the exclusion overlay,
  `Select`, `Sort`.

Gain: removes the ~5–8 s `learn` tokenization from every reader. Rows come out byte-identical,
because the same `vocab.Extract` runs, just at a different time.

### Layer 3: resident query service (`vani jobs serve`)

- **Owned by the app, not by the daemon.** The daemon only runs when scheduling is on, holds the
  decision key, and is busy for minutes during a run. Serving queries from it would make the Jobs
  tab depend on all three. Serve takes no ledger lock and needs no key.
- **Wire:** a copy of Manvi's `manvi/serve/protocol.go` contract. NDJSON, `{id, op, params}` and
  `{id, ok, result|error}`, one response per request, errors as values, a major-only
  `ProtocolVersion`, unknown fields ignored. Ops: `hello`, `list`, `stats`, `gaps`, `show`, and in
  v2 `mark`, `note`, `stage`. Results are the existing `jobview` types.
- **Bounds:** request lines are capped at 64 KB and larger ones refused. At most 4 requests are
  outstanding. A newer `list` cancels the older one through its `context`. Each request has a 10 s
  deadline. The process exits on stdin EOF.
- **Freshness per request:** `Reader.Advance()`, re-read marks (9.7 KB) and config, and check the
  mtime and size of the semantic scores (12 MB), resume store, kbmatch and logos. Then rebuild the
  per-request layer over a shallow copy of the records. The resident snapshot is never mutated;
  today's `hideExcluded` (`cmd/vani/jobscmd.go:505`) edits records in place, and it reads
  descriptions for description rules, so it uses `Reader.Body` and a match cache keyed by a
  fingerprint of `cfg.Exclude`.
- **Writes in v2:** serve calls the same `marks` package as the CLI and updates its own state at
  once, so a mark needs no re-list. `marks` writers already take an exclusive flock
  (`internal/marks/marks.go:13`, verified), so serve and the extension's `jobs mark` can both
  write.

### Layer 4: app

- `JobsServeClient` in `VaniApplyKit` uses the existing `VaniProcess.launch` / `LaunchedJob`
  lifecycle, one serve per app instance, and matches replies by `id`.
- If serve crashes, times out or reports an unknown version, the app falls back to transient
  `vani jobs list` (same types) and restarts serve once with backoff. Once Layers 1 and 2 exist,
  the fallback is fast too.
- Patch the row in place after a write. Insights' `stats` and `gaps` go to the same process.
- Fix the stale "a tenth of a second" comment in `JobsModel`.

## Invariants (each one becomes a test)

1. **Parity.** For `list`, `stats`, `gaps`, `show`, `find` and `export`, across a matrix of view,
   sort, filter, query and half-life, the new path's JSON equals today's byte for byte. This is
   checked on a fixture ledger and on a real-shaped synthetic one, before and after `Advance`. The
   model is `TestRowInsightParity`.
2. **Torn tail.** Reading resumes from the last complete-line offset. A line torn last time is read
   once it is complete.
3. **Writer rollback.** `Append`/`AppendBatch` truncate back to `goodSize` on failure
   (`file.go:444-547`). The reader remembers a hash of the last complete line it consumed, and
   `Advance` re-checks that range first; on a mismatch, a shorter file or a new inode it refolds
   fully. Tests: (a) append, roll back, `Advance`; (b) append, roll back, append a longer line,
   `Advance`. Both must equal a fresh fold. Case (b) leaves the file longer than the reader's
   offset, so a size check alone misses it.
4. **Checkpoint safety.** A stale, partial or corrupt checkpoint is never trusted. The reader does a
   full fold and the reply says so (a check that could not run never looks like a pass).
5. **Lock-free reads.** Readers never take the ledger lock and never write the ledger. Checkpoints
   and the hits index are written under their own try-locks, by temp file and rename or by append.
6. **Untrusted input.** Descriptions are only tokenized and hashed. Nothing from them reaches a
   shell or an unframed prompt, and no links in them are followed.
7. **No personal data** in any log line, checkpoint or index entry.
8. **Every bound is enforced and tested:** line size, outstanding requests, deadline, body read
   size, checkpoint size.

## Phases

Each phase ships on its own and is useful by itself.

0. **Measure (go/no-go). Done; see "Phase 0 results".** `RankContext` (4.5 ms) and `Boilerplate`
   (7 ms) are fine. `BuildList` (2.5 s) is not, which added Phase 1b and widened Layer 2.
1. **App quick win (Swift only).** Patch rows in place after writes, cancel superseded list
   processes, and only re-list on activation when the ledger or marks files changed. This is
   independent of everything else and touches no Go file. (Running in its own session.)
1b. **CLI hot-path fixes (Go, no contract change). Done; see "Phase 1b results".** `TextHash`
   without `fmt`; the manual-reading text hash and the semantic match remembered per posting; and a
   two-phase list (`jobview.PageRanker`: rank every row, finish only the returned page).
2. **Ledger `Reader` and checkpoint (Layer 1). Done; see "Phase 2 results".** No
   `vani ledger checkpoint` command: readers save the checkpoint themselves. Rekey needs no
   invalidation step (it replaces the file).
3. **Text facts store (Layer 2). Done; see "Phase 3 results".** Built as `internal/jobfacts`, not
   `internal/jobindex`: it holds every text-derived fact a view reads, not only hits.
4. **`vani jobs serve` (Layer 3). Read ops done; see "Phase 4 results".** Writes (`mark`, `note`,
   `stage`) still go through the command.
5. **Swift client and fallback (Layer 4).**
6. **Prove it.** A committed benchmark in `docs/BENCHMARKS.md`: interleaved A/B runs, min-of-N,
   old vs new at the live scale, for cold CLI read, serve's first reply and serve's warm reply.
   Update `docs/RUNTIME.md` with the new entry point and files. Report lines removed alongside
   lines added.

Targets, to be confirmed by Phase 0: cold CLI `jobs list` under 1 s; serve's warm sort, filter and
view changes under 200 ms; serve's resident memory under 300 MB.

## Rejected alternatives

- **SQLite (or another embedded DB).** It would give indexes and queries, but it needs a new
  dependency, which the repo forbids without asking. Lean records for 62k postings fit in memory,
  and ranking is Go code rather than SQL. Worth reconsidering only at millions of postings.
- **A separate body store.** It duplicates the log's descriptions (about 500 MB) and adds a
  migration. It is only needed if old events get archived.
- **The daemon serves queries.** See Layer 3: it is optional, holds the decision key, and is busy
  during runs.
- **Only a resident serve, with no storage change.** This was the first draft. Warm queries become
  fast, but every cold start, the CLI and the extension still pay the full fold, and that cost keeps
  growing with the log.
- **A smaller page or a trimmed JSON payload.** The cost scales with the ledger, not with the rows
  returned.

## Open questions for the owner

1. ~~May `internal/ledger` gain the `Reader`, the checkpoint and `vani ledger checkpoint`?~~
   **Answered 2026-11-11:** the owner asked for all phases to be built, which includes this
   contract change.
2. Should old events eventually be archived out of `events.jsonl` (for example in yearly segments)?
   That would bring back a separate body store; otherwise the checkpoint is the whole speed-up.
3. Is a resident helper of up to about 300 MB acceptable while the app is open, or should serve exit
   after some idle time?

## Context and gaps

- `gitpulse_insights` (2026-11-11): 2 worktrees, no collisions. Main has 31 uncommitted files from
  another session (sources, posting, config, PipelineView). They are outside this plan's path and
  are not to be touched. The worktree `claude/teacher-kbmatch-keys-746931` changes kbmatch, which
  is one of serve's cached inputs.
- "No serve mode exists" was checked with `rg` over non-test files in `cmd/vani`, not through
  DevMap.
- Phase 0 measured CPU time and allocations, not retained heap, so serve's resident memory is
  still unverified. `BuildList` ran slower under the profiler (5.5 s/op) than without it (2.5 s);
  the profile's shares are used, not its absolute times.
