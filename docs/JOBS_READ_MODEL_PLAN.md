# Plan: a read model that keeps the Jobs tab fast at any ledger size

Status: phases 1b–6 built on 2026-11-11, plus serve's kept state ("Follow-up: what serve keeps"),
phases A–D and phase E, the kept read ("E: the kept read").
The speed and memory targets are not met (see the phase results and docs/BENCHMARKS.md). Written
as a proposal; the long-term plan, which accepts a major refactor of how state is read and cached.

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

Heap in use after a list and a forced GC, with the Reader and the facts store held: 360 MiB. That
is a floor. Serve's RSS between requests, with 176–760 MB allocated per request, is larger and
was not measured.

**These miss two Phase 6 targets.** A warm view change takes about 1.2–1.6 s of CPU, not under
200 ms. Resident memory is at least 360 MiB of heap, not under 300 MB. Serve takes away the process start, the
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

### Follow-up: what serve keeps (2026-11-11)

The ranked selection itself cannot be cached as sketched above. A job's age is whole days counted
from its own instant, so no clock bucket answers as the command does at the same moment. The
score also reads the semantic and kbmatch stores, manual readings, the weights file and
`--half-life`, and the marks reach it through the company blurbs and counts. What serve keeps is
only what is computed from inputs that did not change, each kept under a key that names all of
them:

- `samecompany.Groups`, the company identity and role groups behind the company-history notes,
  under the Reader's generation (bumped, under one lock with the snapshot it labels, when
  `Advance` reports a change) and the watchlist domains (`Companies.Key`). The marks and the clock
  are paired with it per request (`Groups.Index`). It copies the fields it reads, so it never
  holds a request's records or their descriptions.
- `semantic.Lookups`, the decoded score file, while its size and modification time are unchanged.
  The file is looked at before it is read.
- `rankMemo`: each title's rank under the title signals, each title's keyword hits, and each
  location's places. Each map is keyed by the text its function reads and is emptied past 2^18
  entries.

`jobview.Sort` now sorts a permutation, and `Select` sizes its slice once. Both help the command
too.

Tests: `TestJobsServeSecondRequestAnswersAsTheCommand` changes one input between two requests
(a mark, the clock +1 day and past the apply window, an appended relisting, a retitled posting,
the watchlist domains, the title signals) and compares every reply with a fresh command's. It also
checks that each step changed the replies, and that the groups were rebuilt exactly when the
records or domains changed. `TestJobsServeSeesARewrittenScoreFile` covers a warm between requests.
With any one key part removed (generation, domains, signals, the score file's stat), a parity
comparison fails. The live-copy parity dump is byte-identical for the old command, the new command
and serve's warm pass.

Result (BENCHMARKS.md, 2026-11-11): warm requests use 37–49% less CPU (`--view new` 1.49 s →
0.76 s) and allocate about half as much. What serve keeps costs memory: heap in use after a list
rose from 360 MiB to 450–456 MiB. Peak RSS, measured for the first time, is about 1 GB, set by the
heavier requests' garbage, and did not fall. **Both targets are still missed.** What is left is per request over every posting
in the view: `ToJob`, the score and the sort. The next step is planned below ("Plan: page-only
rows and a two-stage score").

## Plan: page-only rows and a two-stage score (proposed 2026-11-11; A–D built and measured)

### Where a warm request's CPU goes now

From one profile of eight warm `--view new` requests on the live copy (`bbb0442`, load about 80),
per request, on the request's own goroutine. These are inferred from one profile under load, not
a benchmark:

| Part | CPU | What it does |
|---|---|---|
| read and insight build | ~120 ms | advance and snapshot the Reader, read marks and config, look up stored facts, learn the company blurbs, open the stores |
| `Select`, own loop | ~110 ms | view counts, filters, a 40k-entry `Selected` slice |
| `Rank` → `ScoreOf` | ~115 ms | `InputsFor` and `ScoreWith` for every job in the view |
| `ToJob` | ~90 ms | a full `Job` (cleaned text, triage map, URLs) for every job in the view |
| `Sort` | ~60 ms | stable sort of 40k jobs |
| page finish, diversify, encode | ~70 ms | 1,000 rows: keywords, reasons, resume, company notes, row, JSON |
| **total on the goroutine** | **~565 ms** | the garbage collector adds roughly 250 ms more |

**Ceiling of caching the score and the row alone** (inferred): it removes `Rank` and `ToJob`, about
200 ms plus their garbage, which leaves about 0.5 s of CPU per view. That is not 200 ms. The plan
below goes further: it stops building a `Job` for anything but the page.

### What a score reads, and what may be kept

`Score = 100 × match × gate × fresh × company`. Only `fresh` (the clock, `--half-life`, a
posting's dates, `age_neutral_companies`) and `company` (the marks on the company's other
postings) change from one request to the next while nothing else changes. `match`, `gate`, the
displayed relevance and its basis are a blend of inputs that change only when a file changes:

| Input | Comes from | Key part when kept | Test step that changes it |
|---|---|---|---|
| titles, annotations, statuses, first-seen, dates | the ledger | Reader generation | append a posting; retitle one; add an annotation |
| statuses `hideExcluded` sets | config `exclude` and the marks (a marked posting is never hidden) | computed per request (see C) | add an exclusion; mark an excluded posting |
| company blurbs (keyword part) | keyword hits of the `all` view, whose members depend on statuses and marks | a hash of the learned blurb map, learned per request (~20 ms) | mark a closed posting so it joins `all` |
| title signals, drop rules, rank penalties, target summary, `decide.escalate.below_p`, age-neutral set | config | a fingerprint of those fields | change each |
| fitted weights | `rank_weights_file` | its path, size and modification time | rewrite it |
| semantic scores | `<state>/semantic/semantic-scores.json`, read under the knowledge-base profile | the `semantic.Lookups` key (profile hash, file size and modification time) | warm between requests; edit the bank |
| kbmatch readings | `<state>/kbmatch.json`, matched under the knowledge-base profile and the target summary | the file's size and modification time, the profile's digest, the summary | record a reading; edit the bank |
| manual readings | `retriage/readings.jsonl` | its size and modification time | `vani jobs retriage` between requests |
| vocabulary | embedded | its fingerprint | (fixed per binary) |

What stays per request: the marks (mark fields, stage, note, company counts, view membership), the
clock and `--half-life` (`fresh`, `age_days`), every flag (view, query, filters, sort, `--currency`,
`--limit`), and the aggregation in `stats` and `gaps`. `MatchesQuery` reads the mark's note as well
as the title, company, location and team, so the query test is a kept lowered text plus the note
per request. Phase B must show that this equals lowering the joined fields (test it with fuzzed
Unicode, invalid UTF-8 included).

### Phases

**A. One scorer in two stages (`internal/jobinsight`).** `ScoreWith` becomes
`Blend(in, w)`, which computes the parts, `match`, `gate`, relevance and basis, followed by
`Blend.Close(age, company, halfLife)`, which computes `fresh`, `company`, `Score`, `Relevance` and the factor list.
`ScoreWith` stays as their composition, so the digest, the daily run and `rankfit` are unchanged.
The factor list is built only in `Close`, so a caller that needs only the order skips it.
- Gate: a copy of the pre-split `ScoreWith` kept in a `_test.go` as the oracle. Compare every
  `Result` field and every factor by `math.Float64bits` over seeded random inputs, the edge values
  (zero, negative and huge weights, NaN, unknown parts) and, env-gated, every posting on the live
  copy. The arm64 compiler may fuse a multiply and an add within one function. Moving code across
  a call can change a last bit, and that bit reaches the factor JSON. Plus the digest, `rankfit` and
  scoring tests. No speed target: this phase is the seam.

**B. Page-only jobs, one path for the command and serve (`internal/jobview`).** `Select`, `Sort`,
`Diversify`, `StatsOf`, `PayStatsOf` and the place counts work on a compact candidate: record index,
the sort keys (priority, relevance, fresh date, pay key in `--currency`, lowered company, key, mark
time), and the fields the filters, stats and diversification read (triage slots, category, company
key, title, places, remote, closed, age, hits). `ToJob` and `Finish` run only for the page
`BuildList` returns (≤ `--limit`, which the app sends as 1000). This extends the existing
`Rank`/`Finish` seam rather than adding a second one.
- Gate: the live parity dump is byte-identical (old command, new command, serve's warm pass).
  An interleaved CPU A/B of both the command and serve. The heap floor after a list is measured
  and is no higher than 456 MiB.

**C. Serve keeps each posting's blend and static fields.** Per Reader generation and under the key
parts in the table, serve keeps each posting's `Blend` and the candidate's fields that read no
marks. A request then closes the score (`fresh`, `company`), applies its marks and statuses,
selects, sorts and builds its page. `hideExcluded` is answered per request from a kept per-posting
"an exclusion matches" bit under the exclusions' fingerprint, with the marks applied on top.
- Gate: `TestJobsServeSecondRequestAnswersAsTheCommand` gains a step for every row of the table;
  with any one key part removed, a parity comparison fails; the live parity dump; the A/B; a
  memory budget the owner sets (the kept blends and fields are estimated at 10–30 MiB, unmeasured).

**D. Memory, a separate track.** Peak RSS follows the collector's target (`GOGC=50`: about 15%
lower peak for about 20% more CPU; BENCHMARKS.md). Options: a memory limit for serve
(`debug.SetMemoryLimit`), a lower `GOGC` for serve, or a smaller live heap (Phase B's smaller
garbage, a leaner kept score file). Each is measured with `/usr/bin/time -l` on the warm table.

**Phase A results (9965dcd).** `BlendOf` then `Close` (or `Order`, without the factor list) holds
to a verbatim copy of the old scorer bit for bit over 200k seeded cases. The live parity dump is
unchanged.

**Phase B results (measured 2026-11-11, load average 40–66).** A selection works on a compact
candidate (`internal/jobview/cand.go`). `BuildList` builds and finishes a `Job` only for its page.
`jobs stats` (`StatsFor`) and `jobs gaps` (`SelectBriefs`) build none. `Select` still returns
every job whole, for audit. `Rank` orders with `Blend.Order`, and `Finish` adds the factor list
for the jobs shown. The live parity dump is byte-identical for 9965dcd, the new command and
serve's warm pass. Interleaved A/B against 9965dcd, 3 rounds each:

| | 9965dcd | Phase B |
|---|---|---|
| serve, warm `--view new`, CPU per request | 792–807 ms | 438–592 ms |
| serve, warm `--view all --sort company` | 743–760 ms | 506–544 ms |
| serve, warm `--sort salary` | 718–737 ms | 563–570 ms |
| serve, warm `stats --view new` | 967–1079 ms | 377–498 ms |
| serve, heap in use after a list | 467–474 MiB | 453–458 MiB |
| serve, peak RSS of the bench process | 1.01–1.07 GB | 0.93–0.96 GB |
| command `jobs list --view new`, user CPU | 1.68–2.33 s | 1.44–2.04 s |
| command `--sort salary`, user CPU | 1.93–2.03 s | 1.76–1.79 s |

The plan's heap gate was "no higher than 456 MiB", from an earlier measurement. The new arm read
453–458 MiB here, while the old arm read 467–474 MiB in the same runs. The command gains less,
because a cold command's time is mostly its read.

**Phase C results (measured 2026-11-11, load average 40–60).** A profile after B put
`InputsFor` at ~70 ms, `staticOf` at ~50 ms and the where census at ~45 ms of a warm
`--view new` request. Phase C is three steps:

- **C1:** the census without per-job maps, and sort and diversify over a permutation. No cache.
- **C2:** `jobview.KeptRows`, each record's static row fields per generation.
- **C3:** `cmd/vani/jobsblends.go`, each posting's `Blend` under a key built from what the
  request resolved: the generation, config read or not, weights, rules, escalation, title
  signals, target, vocabulary, semantic matcher, kbmatch stat and digest, and manual readings.
  Per-posting guards cover status, stored facts and the company blurb.

`InputsFor` is now `BlendFor`'s inputs plus `ClosingFor`, and is held to a pinned copy. Each key
part, removed alone, fails a parity test. The status guard has no reachable list case today,
since `hideExcluded` only drops postings, so it fails a mechanism test instead. The live parity
dump is unchanged (`75bf02f2…`). Interleaved A/B against b9036fd, 3 rounds:

| | b9036fd (B) | Phase C |
|---|---|---|
| serve, warm `--view new` | 587–686 ms | 395–441 ms |
| serve, warm `--view all --sort company` | 562–600 ms | 345–372 ms |
| serve, warm `--sort salary` | 575–636 ms | 317–402 ms |
| serve, warm `--q engineer` | 530–569 ms | 403–431 ms |
| serve, warm `stats --view new` | 441–460 ms | 287–321 ms |
| serve, heap in use after a list | 462 MiB | 495–501 MiB |
| serve, peak RSS of the bench process | 0.91–0.95 GB | 0.96–0.98 GB |

The kept rows and Blends cost 33–39 MiB of heap. That is over the plan's 10–30 MiB estimate. The
gate was a budget for the owner to set, which is still open. The 200 ms target is still missed: about 400 ms of CPU per warm view, under load.

**Phase D results.** Measured settings: no limit, `GOMEMLIMIT` at 1 GiB, 800 MiB and 650 MiB,
and `GOGC=50` (tables in BENCHMARKS.md, n=2 per arm).

- **Warm requests:** 800 MiB took the peak from 0.91–0.95 GB to 0.84–0.85 GB, with no CPU
  difference those runs could show.
- **A cold first list** (every posting's facts derived): it runs above 800 MiB, and the limit cost
  it about a third more CPU.

So serve sets its limit only after its first view, when `GOMEMLIMIT` is unset. The limit is the
larger of 800 MiB and 1.6 times the live heap measured then. A tighter cap is the owner's call
(question 2): 650 MiB reached 0.73 GB for about a third more CPU, and `GOGC=50` reached 0.76 GB
for about 8% more.

**Expected after A–C (inferred before measuring; superseded by the table above):** about 250 ms on the goroutine. The read and
insight build (~120 ms) and the 1,000-row page (~70 ms) stay, so roughly 300 ms of CPU per warm
view. Short of 200 ms.

### E: the kept read (2026-11-11, chosen by the owner: "Reuse the read between requests")

A warm `--view new` cost about 406 ms of CPU in this process and about 61 ms more in a child, the
`rkb claims export` run that builds the knowledge-base profile; the bench did not count the
child before (`child-cpu-ms/op` now does). A warm-only profile (30 requests, live-state clone) put
the read and insight build at about 120 ms of it: learn ~24 ms, the profile ~19 ms besides rkb,
the manual readings ~18 ms and their fingerprint ~10 ms, the facts lookups and prune ~20 ms, the
config ~5 ms (twice), the snapshot ~7 ms. `BuildList` (selection, places, sort, the page) was
~138 ms.

Built, in two commits:

- *Once per request* (`9457f8a`): the semantic and kbmatch lookups share one profile (one rkb
  run; `onceProfile`), and the read's config is the ranking's. On the live state there is no
  kbmatch store, so the first saves nothing there yet; the second saves one config parse.
- *Kept between requests* (`cmd/vani/jobsreadkeep.go`), each under a key naming what it read:
  - the text facts map: the generation, the marks the text scope reads (those on closed, dropped
    and duplicate postings, `scopeReadsMark`; saving or hiding a live posting keeps the map) and
    the facts store's edit count (`jobfacts.Store.Changes`), sampled before the lookups and again
    after the read's own prune and save. Kept only when the read loaded no text, its save
    succeeded and the store holds nothing unsaved, so a reuse skips no hydration, derivation or
    save.
  - what learn learned (company blurbs, keyword hits): the kept facts map itself, the generation,
    the postings in view "all" after the exclusions, the vocabulary. Kept only when learning noted
    no posting missing.
  - the manual readings and their fingerprint: a stat of `retriage/readings.jsonl` taken before
    it is read (device, inode, size, mode, modification and change time; `fileStamp`). The store
    appends or renames a new file over the old, and either changes the stamp; a rewrite in place
    at the same size and modification time still changes the change time. Without a change time
    (not darwin or linux) nothing is kept.
  Set hashes use a per-process random seed (`hash/maphash`), so posting keys cannot be chosen to
  collide.

Not kept, and why: the weights file (none in the live config: nothing to save); the marks (3 ms,
and the keys above are computed from them); the config (5 ms, but `config.Config` holds maps and
slices that request code could change); the profile (its rkb run reads files Vani cannot list,
the reason the resume pool is not cached either; the owner chose to keep running it, decision 3
below).

Tests: `TestJobsServeKeepsTheReadAndAnswersAsTheCommand` (a live posting saved and hidden, which
rebuild nothing; a dropped posting, a mark bringing it into scope, cleared and marked again with
one request between; an exclusion, one that reads descriptions; a reading appended, rewritten in
place at the same size and time, replaced by a rename, deleted; every reply the command's, and
the keep counters per step), `TestJobsServeKeepsNoFactsMapWithAnUnsavedStore`,
`TestJobsServeUnreadableReadingsWarnEveryRequest`, `TestJobsServeReadKeepConcurrent` (`-race`),
`TestFileStampSeesEveryChange`, `TestLearnKeepsNothingNotedMissing`,
`TestStoreChangesCountsEdits`.

Mutation runs, one change each: the stamp's change time, the facts key's marks, its generation,
its edit count in the lookup, the marks narrowed back to every mark, the learn key's view set and
facts identity, learn's missing check, the readings stamp, the whole-text rule, and the save
checks (failed save and unsaved edits, removed together) each fail a test. Five survive, each
covered by another guard: the learn key's generation (its facts identity carries one); the
keep-time edit count (counts only grow, so the lookup never matches a map kept after an edit; the
check only avoids keeping a dead entry); the failed-save and unsaved-edit checks alone (a failed
save leaves the store dirty, so each covers the other); and "the read loaded no text" (a read
that loaded text derived and filed it, moving the edit count, except for a record no event gave a
posting, whose hydration loads nothing). The vocabulary in the keys is fixed per process and not
mutated.

Left as it was: `kbStat`, the kbmatch store's identity for the kept Blends, is still size and
modification time only, not `fileStamp`.

Result (BENCHMARKS.md, 3 interleaved rounds, live-state clone): a warm `--view new` went from
364–400 ms to 227–258 ms of CPU in this process, `--view saved` from 171–245 ms to 53–60 ms, and
every other warm request by 70–135 ms; rkb's ~60 ms of child CPU is unchanged. Every keep hit on
every warm request (30 hits, 1 miss each). Heap in use after a list stayed at about 490–505 MiB;
max RSS fell from 0.95–0.97 GB to 0.90–0.93 GB. **The 200 ms target is still missed:** about
0.23–0.26 s here plus about 0.06 s in rkb. What is left is `BuildList` (~138 ms in the profile:
selection, the places count, the sort, the page) and the profile (rkb and the bank parse).

### Open questions for the owner

1. **The 200 ms target.** After A–E, the levers left are the owner's choice:
   - *A smaller page:* the app asks for fewer than 1,000 rows and pages through the rest.
   - *`BuildList`:* the selection, the places count, the sort and the page (~138 ms in the
     profile).
   - Or the target moves to about 300 ms.
2. **Memory.** Accept about 450–500 MiB of heap and a ~1 GB peak, or cap serve (D) at the cost of
   CPU.
3. **rkb per request (decided 2026-11-11: keep running it).** Every view runs `rkb claims export`
   for the knowledge-base profile, about 60 ms of child CPU and 70 ms of wall time. Its output
   depends on files Vani does not name (the claims file and verification log rkb's config lists,
   rkb's own binary and config), so the owner chose to keep running it, which is exact, over
   keying a kept profile on file stats (exact only while that list of files is complete) or
   having rkb report the files it read. That cost stays in every view's time.

Language: Go throughout. This is the existing ranking and view code. No kernel here is CPU-bound
enough to justify a Rust core behind gusset (each step is map lookups and small arithmetic per
posting).

The hardest question is Phase A's: proving the two-stage score bit-identical on arm64. If the
compiler fuses operations differently across the split, the fix is to make each stage's
arithmetic explicit and unfused, without changing a single rounding.

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
5. **Swift client and fallback (Layer 4). Done.** As built, `JobsServeClient`
   (`macos/Sources/VaniApplyKit/JobsServe.swift`) runs its own `Process` rather than
   `VaniProcess.launch`, because a `LaunchedJob` reads one reply and exits, and serve does neither.
   It shares `VaniProcess.environment`, `decodeReply` and the reply size cap with the command path.
   The app's `JobsClient` asks serve for `list`, `stats`, `gaps` and `show`, and runs the command
   whenever serve cannot answer. Serve restarts once, 2 s after it dies. Row patching after a write
   and the stale `JobsModel` comment came with the Swift quick win (merge `6951e10`).
6. **Prove it. Done; the targets are not met.** `docs/BENCHMARKS.md` ("2026-11-11, the Jobs read
   path at the live scale") gives the results at the live scale.
   - Cold `jobs list` dropped from 8.4 s to 2.1 s of user CPU (`--view new`). This is interleaved
     A/B, min of 5, against a build from before Phase 1b.
   - Serve's first reply takes 2.4–2.5 s of CPU.
   - Serve's warm replies take 0.34–1.6 s of CPU.
   - Serve holds at least 360 MiB of heap; its RSS was not measured.
   - Serve's byte-equality with the command is tested on fixtures, including concurrent requests
     under `-race`. The live parity dump covers the command path.

   Neither the 200 ms warm target nor the 300 MB resident target is met. What remains is the
   per-request ranking (Phase 4 results). `docs/RUNTIME.md` documents the read path, `jobs serve`
   and the two `readmodel` files. The live parity dump at the end is byte-identical to the Phase 1b
   dump.

   Lines from Phase 1b to Phase 5, without the merged Swift quick win:
   - code: about +2,520 / −140;
   - tests: about +2,740;
   - docs: +480.

   Nothing was deleted outright. The removed lines are the old full-text fold and per-call
   derivation in the jobs path, replaced in place.
F. **The app keeps its page vani's own (Swift only). Done.** It closes the gaps the Phase 1 patch
   left, now that a warm list is cheap.
   - **After a write.** A mark, stage or note still patches the row at once. Once the writes stop
     (`JobsModel.reconcileDelay`, 0.5 s), one quiet list (no progress shown) replaces the page with
     vani's. That restores what a patch cannot redo:
     - the row's own ranking;
     - the company factor on its company's other rows;
     - rows past the limit;
     - exclusions on a cleared mark.

     A quiet list started before a later write is stopped, so it never lands over that write's
     patch. A list the owner starts after a write takes the waiting reconcile's place. A failed
     reconcile shows its error and keeps the patched page.
   - **On activation.** Coming to the front re-lists every time. The Phase 1 file stamp
     (`JobsListInputs`, five files) is deleted. It could not be complete: a list also reads the
     semantic, kbmatch and resume stores, logos, and the rkb profile, whose inputs Vani cannot name.
   - **Tests.** `JobsListCostTests` covers both, against the fake `vani`. A mutation run put back
     six old behaviours one at a time. Five failed tests. The sixth removed a condition that could
     never be false, which is now gone.

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
