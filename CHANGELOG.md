# Changelog

## [Unreleased]

Ideas taken from Dossier, career-ops, Seekter, ApplyPilot, Simplify Copilot, JobSpy, freehire and
ats-scrapers (2026-10-27 study), reimplemented in Vani. Credits and licences:
[docs/ACKNOWLEDGEMENTS.md](docs/ACKNOWLEDGEMENTS.md). An earlier note said freehire's MIT adapters
were ported with notice. Source files do not carry an upstream copyright header.

### Added

- **Fresh install from a clone** (2026-11-15, issue #1). `scripts/bootstrap.sh` clones the public
  siblings (Manvi, DevCouncil, gusset, tessl) at the commits in `scripts/deps.lock`, writes
  `go.work`, builds `build/vani` and `build/rkb`, runs `vani init` and `config check`, and reports
  each optional layer as found, missing or not checked; it never changes an existing `go.work`,
  checkout or config, and refuses a `go.work` that still uses an outside `decide` checkout. The
  decision client moved in as the nested module `decide/` (`go.mod` replaces it with `./decide`,
  module path unchanged), and the knowledge-base tool as `rkb/`, without the owner's resumes,
  claims or real-data tests; owner-specific lint vocabulary, recurring-point mechanisms and parity
  baselines moved to `rkb` config keys (empty by default). rkb now layers one user config over its
  embedded default (the skill's, else `~/.config/rkb/config.json`, which works under Vani's
  minimal child environment), and a config that exists but cannot be read stops it instead of
  falling back. `rkb/examples` is an invented sample knowledge base a new user can copy, with a
  Vani–rkb contract test. `decide`, `rkb` and Vani all target Go 1.27.4. `scripts/test.sh` tests
  both nested modules; `.github/workflows/fresh-install.yml` runs the bootstrap and the Go gate on
  Linux and macOS. The README has a copyable prompt for setting Vani up with a coding agent.
- **`vani rescore --coverage-unknown`** (2026-11-15, the owner's "yes, do both"). A posting DevPrism
  found no must-have in keeps the `gap.coverage_unknown` marker and is ranked on the unknown-coverage
  prior, and plain `rescore` never asks about it again (it takes only Scored and Explained postings
  with no score). After a DevPrism upgrade reads more JDs (its heuristic parser learned `REQUIRED`,
  `What You Bring` and similar headings, and closes benefits and privacy footers), this mode asks
  DevPrism again about every scored, explained or digested posting with the marker and a description
  (`pipeline.CoverageRescorable`). One that now has must-haves gets the annotations a first score
  writes, in one event that keeps its status and explanation (reason `coverage_rescored`); one still
  read as having none is left as it was, with nothing written. Dropped, dead and duplicate postings
  are never asked (Rule 7). The counts come from the ledger: asked, measured, still unknown, failed
  and not reached (the `--limit`). It skips the default mode's 0-of-0 repair. Like plain `rescore`,
  it takes the ledger lock, so it is refused while the daemon runs.

- **Removing a board closes its postings** (2026-11-14, the owner's "yes, build it"; design
  decided by Fable). A board's postings closed only when its own listing left them out, and a
  removed board is never listed again, so they stayed open for good. `vani watchlist remove` now
  writes a tombstone (`<state>/watchlist/removed/`) and the next run closes the board's open
  postings (Dead, `died_from` their status); adding the board back revives them through its
  listing. `--keep-postings` opts out, and `--close-postings` closes a board already off the
  watchlist. Postings a still-watched board covers and ones first seen after the removal (ingested)
  stay open; a tombstone written after the run read its config waits for the next run. The run
  record names each board (`boards.removed`), and the closings are not counted as deaths.
- **Ledger group commit** (2026-11-14, the owner's "batch the ledger appends", design decided by
  Fable). A board's listing events are one `ledger.File.Group`: written and folded at once (the
  reconcile loop reads its own writes as before) and made durable by one fsync when the board is
  done, more only past `MaxBatchEvents` or `MaxBatchBytes`. A crash mid-board leaves a whole-line
  prefix that the next listing completes (`TestReconcileCrashPrefixConverges`); a failed fsync
  breaks the ledger, cuts the file to the last synced line and rebuilds memory to it, the board's
  summary is zero, and the run keeps the pending-reopen set unsaved so the next run applies it
  again. Runner stage events (between paid model calls) and the retriage loops keep one fsync per
  event: the retriage `reopen` contract reports only written keys, which a group's failed final
  sync would break. Measured with the real fsync, 5 interleaved rounds: a board of 500 changed
  postings 2.92 s -> 0.17 s (min; median 3.19 s -> 0.39 s); a 20k-record run with nothing
  changed 429 -> 398 ms (min; within noise). One fsync costs a median 7.9 ms on the owner's Mac.
- **Tests no longer wait on fsync or retry backoff.** Every production fsync goes through
  `atomicfile.Sync`/`SyncDir`; tests count, fail or skip them through test-only setters
  (`internal/atomicfile/synctest`, `atomicfile.RunWithoutSync`, kept real under `-test.bench`).
  Every package whose tests skip syncs (ledger, pipeline, marks, eval, inbox, rkbresume,
  resumestore, cmd/vani) is covered by sync-count tests and a real-fsync round trip, in its
  own package (ledger, marks, eval) or in the store it writes through (atomicfile.Write,
  ledger); manualtriage and the vector log gained sync-count tests. feedback, doctor and digest
  still sync for real in their tests and have no count test. Source adapters' tests
  answer `srcutil`'s retry waits at once (`srcutil.RunWithoutWaits`). Measured as one back-to-back pair
  (n=1, 2026-11-14, `go test ./... -count=1`, 0 failures in either; other work was running on
  the machine, and earlier unpaired runs put the before at 13 to 16 min and the after at 3 min 14 s
  to 3 min 44 s, so the direction is firm and the ratio is soft): the full suite
  went from 16 min 24 s to 3 min 44 s wall; `cmd/vani` from 961 s (over `go test`'s default
  10-minute timeout) to 205 s, goneparity from 649 s to 3 s, pipeline from 560 s to 68 s and
  ledger from 393 s to 80 s.
- **Round-2 measurements** (2026-11-14, decided by Fable). `vani relevance` adds a part-coverage
  table over the open pool: how often each Score part is evidence, how much of the match it
  decides (weight x confidence), its distinct values, and whether it is known nowhere or constant.
  It also adds a weight shadow: four alternative weightings (fit 0, coverage 0, a rebalanced
  split, match shrink 0.10) on the judged postings still open, measured on the age-free ranked
  score with a paired bootstrap of the AUC difference (`rankfit.PairedAUCDiff`) at a family-wise
  level. The rule that would refute a change is stated in the report: a variant is better only
  with every computable mode's interval above 0, and nothing changes a weight under 150 judged
  open postings. The shadow measures the ranked score, not Relevance, because Relevance keeps a
  fixed shrink that a MatchShrink variant cannot move. `vani status` and `vani semantic status`
  print a semantic-health line: on how many open postings the similarity is evidence (ok at 80%
  or more, warn at 50%, bad below), and why it is low, naming a profile change with the stored
  vectors a warm rescores from before the warm budget. `audit --json` reports each draw phase's
  wall time. The Mac app now gives a draw 60 seconds: its 30-second audit timeout was never used,
  so the draw ran on the jobs client's 30 seconds.
- **Audit benchmark fixes** (2026-11-14, decided by Fable after a benchmark on a copy of live
  state). The kill rule for the yes-seeking cells is reworked before any cell label exists: the
  old rule kept a cell at 4 yes anywhere from 20 to 39 judged, so a cell no better than random was
  kept about 39% of the time. Now a cell is turned off at its first 20 answers with 0 or 1 yes,
  and at its first 40 kept only with 5 or more yes and at least twice its comparator's yes rate
  (the random strata for the wants cell, the score deciles for the yes-neighbour cell, frozen at
  the 40th answer); a comparator with no answers cannot decide. Exact operating characteristics
  are pinned in `internal/eval/killrule_oc_test.go`: at a 9% base rate a cell equal to random is
  kept 8.0% of the time and one three times as good 85.3%. The teacher-rejects cell leaves the
  yes rule: at 20 answers its no rate is shown beside the top three deciles' and the owner
  decides. A cell that draws fewer than its quota is named (its pool is running out). The daily
  run and `vani semantic warm` now embed the owner's judged postings that have no vector (at most
  200 a run, outside `warm_per_run`), since the warm only reaches kept and marked postings; on the
  copy, 31 of 32 judged postings had none, and the taste column now reads all 32. Their texts are
  pinned (`semantic/labelled-texts.json`) above every Score in each warm's live set, so a vector
  log compaction never evicts them first. The taste column
  says why each judged posting lacks a vector, and dropped postings' evicted descriptions are read
  back before their vectors are looked up (one lookup, `cmd/vani/postingvectors.go`, also used by
  the cluster study). `vani relevance` prints an exact one-sided chance p (rank sums, ties as
  midranks) beside each part AUC and each seniority-shadow AUC, and how many rows were tested.
- **Every audit overlay on by default** (owner, 2026-11-13). `vani audit` now draws, beside the
  17 random places, 5 "teacher wants" and 2 "teacher rejects" disagreement picks, 3 yes-neighbour
  picks and 3 engineering picks unless told otherwise (`--disagree=0` and the like turn one off).
  A default overlay whose inputs do not exist yet (no teacher readings, no vector store, a taste
  column that does not pass) or whose cell the kill rule has turned off draws nothing and is
  named: an `overlay not drawn:` line, and `skipped` in `--json`. A default cell quota over the
  2x cap is trimmed with a note. The same flags given explicitly still fail closed, unless
  `--soft-overlays` makes them soft too. The random draw is unchanged. The Mac app's Audit tab
  shows vani's defaults on its steppers until one is set (a set 0 is kept as off), sends
  `--soft-overlays`, lists what was not drawn, and has a "Vani's Defaults" button. The Jobs tab's
  "Would you apply?" row now asks what kept a Maybe or a No from a yes, as the Audit card does.
- **Taste column, yes-neighbour cell, engineering group, doctor silver log** (Fable's third pass
  on the second enhancement review). `vani relevance` gains a taste column: each judged posting's
  nearness to the owner's other yes answers, from stored vectors, against the title part. `vani
  audit --yes-neighbours N` draws from the 50 unlabelled postings nearest those answers, and is
  refused unless the taste column beats the title part. `vani audit --engineering N` draws at
  random from engineering-titled postings. Both are overlays: the random draw is unchanged. Labels
  from the neighbour cell count as cell labels; engineering labels count in the auditor's
  evaluation and the yes yield but stay out of the relevance strata, drop recall, the reader table
  and the weight fit (`vani rankfit` prints how many). `vani doctor` appends its drop verdicts,
  without free text, to `silver_doctor.jsonl`, which `vani relevance` compares with the owner's
  answers once there are 10 yes and 30 no. No verdict is a label. Built off by default; the entry
  above turned the draws on (the silver log was on whenever `vani doctor` reviews drops). The Mac
  app's Audit tab has steppers for the neighbour and engineering picks, and after a Maybe or a No
  it asks what kept the posting from a yes (level, domain, location, employer type, other; one
  key each, Return for none), sent as `audit answer --reason`.
- **Disagreement cells and the yes yield** (`vani audit --disagree N [--teacher M]`,
  `internal/eval/yesyield.go`, `vani relevance`). The weekly draw can add N postings where the
  teacher's stored role_fit reading (default Sonnet) disagrees with the engine: wanted but dropped
  or in the lowest three score deciles, or rejected in the top decile. That is where a missed yes
  is likeliest. The cells sit over the random strata without changing them: the random picks
  and their p_select are the same with or without `--disagree`, and at least a third of the draw
  stays random. Labels drawn there are kept out of every estimate (relevance counts, readers, drop
  agreement, auditor gates, the weight fit); drop recall counts each as a known outcome, as it
  counts a mark. Postings in strata the auditor leaves out (work authorization, work mode, owner
  exclusions) are never read for a cell. `vani relevance` now shows the raw yes yield per stratum
  and cell, and names a yes-rich stratum only when its 95% lower bound clears the random strata's
  95% upper bound. A teacher log with no reading by `--teacher` of any candidate is refused (a
  misspelt model would otherwise look like no disagreement). Built off by default; the first entry
  above turned it on.
  After Fable's second review: each cell has its own quota (`--disagree` for the "teacher wants"
  cell, `--disagree-rejects` for the "teacher rejects" cell, which Fable kept at 0 unless asked: the
  teachers reject most of what they read, so it is a large slice of the top decile; its default is
  now 2). The 2x cap no
  longer counts closed places. The yes yield gains group rows (all deciles, top three deciles, all
  drop classes) and a kill rule per cell (at 20 judged: 0-1 yes turns it off, 4+ keeps it, 2-3 runs
  to 40; reworked on 2026-11-14, see "Audit benchmark fixes"). `vani teacher freeze` says how many exemplars came from the cells. The Mac app's Audit
  tab gains a "Disagreement picks" stepper (vani's default, 5, until set).
- **Drops the rules no longer make, by class** (`vani retriage --policy-drops`, its `classes`).
  A drop made under a rule that has since changed was never re-examined: two Databricks Staff
  roles the owner marked "maybe" were dropped on `seniority = staff_plus` (p 0.912 and 0.917, a
  rule the live config no longer has). `--policy-drops` lets such drops back through the pending
  reopen, and its reply now counts what it lets back (or, with `--dry-run`, would) by the audit's
  drop class. A separate `--stale-drops` report and `--rescue` were built on the rank branch and
  retired when main's `--policy-drops` landed: the rescue wrote `unsure` at once, so a role gone
  from its board would have reached scoring and the paid notes call, and it judged drops with a
  second copy of the policy check.
- **Readers and score parts against the owner** (`internal/eval/readeragree.go`,
  `internal/rankfit/partauc.go`, `vani relevance`). The report now sets every triage reader against
  the owner's would_apply labels: each engine backend from the ledger and each teacher model in
  `teacher_readings.jsonl`. It gives each reader's role_fit sensitivity and specificity (Wilson
  intervals) and a value-by-answer table for role_fit and seniority. It also gives each Score part's
  AUC on the judged postings, with a stratified bootstrap interval, over all judged postings and
  over the scored stratum. The parts are built as `vani rankfit` builds them. Counts only, no
  spend; a source that cannot be read is named, never shown as an empty table. This is stage 0 of
  the adaptive-ranking plan: it decides whether any model's reading may stand in for the owner's
  label.
- **Sensitivity of the relevance verdicts** (`internal/eval/labelmode.go`, `vani relevance`). The
  reader table and the part AUC are repeated under four readings of the labels: yes or maybe
  wanted, yes only, maybes left out, and engineering titles only (a word-list title check; a
  non-engineering word wins). A verdict that holds in one reading only shows as such. The reader
  table also gives the seniority each reader read on its mismatch_seniority readings.
- **The reason for a maybe or a no** (`vani audit answer --reason R`, label slot
  `would_apply_reason`). An answer that is not a yes may name the one factor that kept it from
  one, in the pointwise auditor's vocabulary (level, domain, location, employer_type, other), so
  the owner's reasons and the auditor's can be compared. The reason travels with the answer: a
  triage re-label keeps it, a new answer replaces it, and it is never a triage slot. A reason on a
  yes or a skip, on its own, or outside the vocabulary is refused. The Mac app asks for it after a
  Maybe or a No, on the Audit card and in the Jobs tab's "Would you apply?" row.
- **One-tap would you apply** (`vani audit answer --shown`, the Mac app's job detail). A posting
  answered from a surface takes the stratum and inclusion probability of its newest impression
  that carries them, else is recorded as a manual label; an impression log that cannot be read is
  an error. The job detail has a Would you apply? row that writes through it.
- **Seniority-gate relaxation, off by default** (`Weights.SpareSeniorityMismatch`,
  `vani rankfit --spare-seniority`). The triage rubric reads mismatch_seniority against an
  early-career engineer whatever the target summary says. A rank weights file may list seniority
  readings (`mid`, `senior`, `staff_plus`; never `manager`) at which such a reading is not a
  mismatch: no gate, fit part unknown. It only loosens. `vani relevance` shows a shadow of two
  variants: the AUC of the age-free relevance per label reading before and after, and each judged
  posting it moves with its percentile among open postings. On the live ledger (2026-11-12)
  neither variant changed any AUC; sparing staff_plus too moved one judged posting (84th
  percentile before and after) and 373 of 29,311 open postings.
- **Summary what-if** (`vani relevance --what-if-summary FILE`). This reports what a draft
  `target_summary` would do to the order of the judged postings. It swaps only the two Score parts
  that read the summary: keyword (the target terms) and semantic (the knowledge-base profile and its
  calibration anchors). The semantic part is rescored from the stored chunk vectors for both the
  live summary and the draft, in a temporary copy of the vector log that is removed afterwards.
  A posting not known in both arms is compared as unknown in both. The live arm is a null test
  against the scores the ranking reads. No model is asked and nothing is written. The triage
  readings, which the summary also steers, stay as they are, and the report says so. It lists every
  posting answered yes with its percentile among open postings before and after. A build without
  `-tags tessl` says the semantic part was not computed and compares the keyword part only.
- **Silver rescue rule (D6)** (`vani retriage --silver-drops [--rescue]`). This reports the model
  drops that the pairwise would_apply auditor keeps picking over their stratum. The bar is a score
  of at least `--min-score` over at least `--min-games` comparisons, with no answer of "neither". It
  also reports the rule's precision on gold labels made after the exemplar freeze. `--rescue`
  takes them, best first and at most `--max-per-day` per day, but only while the auditor's verdict
  on gold under today's question is go. A drop on a watched board waits as a pending reopen (class
  `silver`) for its board's next full listing, then goes to unsure if the exclusions and title
  rules still let the listed text through; any other goes to unsure at once. Pending marks count
  against the day's cap. It never rescues title drops, owner exclusions, or work authorization and
  work_mode drops. A rescued posting is scored like any uncertain one and the runner does not
  triage it again. Nothing runs unless the owner asks.
- **Silver-pretrained weight fit** (`vani rankfit --part weights --silver-prior`). The auditor's
  decisive answers fit Score's shares first, and the owner-labelled fit is pulled toward those
  shares instead of the hand weights (`rankfit.Options.Prior`). Answers about any posting the owner
  judged are left out. Only judgments made after the exemplar freeze are held out
  (`Options.TrainOnly`). The gold-only fit on the same split is reported beside it, and the gold
  gate (`Beats`) still decides whether anything is written.
- **Cluster strata study** (`vani export pooled-vectors`, `vani strata compare`,
  `docs/CLUSTER_STUDY.md`). An offline study of whether embedding clusters (Ojas e-means, fitted
  outside Vani by `scripts/cluster-study/vani_cluster.rs`) would be a better audit stratum than the
  job category. It reads the chunk-vector log read-only (`semantic.OpenVectorReader`) and measures
  the drop in within-stratum variance of Sonnet's role_fit reading against the categories and the
  audit's own strata (`eval.AuditStratumOf`). On a copy of live state, clusters were fitted on 8,420
  pooled postings and the variance measured on the 2,805 of them with both a Sonnet reading and a
  category. E-means at 8 cells cut it by 18.6% (median of 5 seeds, range 12.2–19.2%; highest
  bootstrap upper bound 24.9%), under the 25% bar, so clusters are not adopted. Lloyd's k-means did as well at 13 cells and above.
- **Pipeline doctor** (`cmd/vani/doctorcmd.go`, `internal/doctor`). `vani doctor` has Claude
  (`llm.model`, Opus) read per-board signals over the newest run records, with each board's raw
  `details` lines, the run logs' distinct trouble lines, and a seeded sample of dropped postings
  stratified by drop class. It reports which boards fail silently and which drops look wrong. It is
  report only (`<state dir>/doctor/`). `--dry-run --show` prints both requests and their worst-case
  cost, and a real run needs `--budget-usd` to cover that cost. Sponsorship rules, readings and
  drops never leave the machine. A failed part is written as failed and the command exits 1.
  [docs/RUNTIME.md](docs/RUNTIME.md) describes it.
- **Pipeline doctor in the Mac app.** The Pipeline tab has a Pipeline doctor panel: price a review
  (nothing sent), confirm a budget, run it, and read the report (boards failing silently and wrong
  drops first, with the model's evidence, next steps and fixes). The newest report shows on launch.
  `vani doctor --json` now carries the report (`result`), the drop sample's `seed` and `sent`, a
  partial review's reply keeps its report, and `vani doctor --last` prints the newest report.
- **Features, benchmarks, and credits.** The README leads with what Vani fetches
  and what it refuses to do. Board caps in that list match the adapter
  constants (Avature 400, IBM 500, D. E. Shaw 1,000, SmartRecruiters and
  Atlassian 3,000, among the others). [docs/BENCHMARKS.md](docs/BENCHMARKS.md)
  records a 2026-11-07 run of the pay, insight, and 20 MiB JSON benchmarks on
  an Apple M5 Pro, and points at older numbers that were not re-run.
  [docs/ACKNOWLEDGEMENTS.md](docs/ACKNOWLEDGEMENTS.md) names the projects whose
  patterns were reimplemented and the programs Vani actually runs, with the
  licence each public repository reported. `scripts/release.sh` still requires
  a private repository, so the README no longer says a release is a private
  GitHub release that the script can cut.
- **Public noncommercial terms.** [LICENSE](LICENSE) is the PolyForm Noncommercial License 1.0.0.
  [NOTICE](NOTICE) and [docs/LEGAL.md](docs/LEGAL.md) state the purpose (one reader for employer
  job listings that are split across applicant-tracking systems, to help people look for work,
  not to make money) and that a source is included or excluded when the author is notified by a
  GitHub issue. The license does not cover fetched job text or third-party dependencies.
- **US postings first** (`cmd/vani/advanceorder.go`, `cmd/vani/runoptions.go`,
  `internal/location/us.go`, `internal/config/config.go`). `vani run-once --us-first` and
  `vani run-request --us-first` advance (triage, score, explain) postings located in the United
  States before the rest for that run, and the config key `advance_us_first` (default false)
  does the same for every run, scheduled runs and the extension inbox poll included. It only
  orders: nothing is dropped or skipped, and a posting outside the US waits for a later run when
  the cap (`--advance N` or `advance_per_run`) is reached first. A title the title signals exclude
  still goes last, each group keeps the usual order (title rank, the knowledge-base order,
  freshness), and `--us-first` is refused with `--list-only`. The run record's `options` and a
  run request carry `"us_first": true`.

  `location.InUS` decides what is in the US: a state the location parser reads, a part naming the
  US anywhere, a state that is not last ("California - San Francisco"), a short list of US cities
  boards write bare ("San Francisco", "NYC"), or a remote scope of US, North America, the Americas
  or anywhere. "Remote" alone and an empty location are not in the US, and another country or a
  Canadian province code makes a place foreign ("Mississauga, ON, CA"). An advance run still
  spends from `decide.daily_usd`.
- **Triage chosen companies and places** (`cmd/vani/runscope.go`, `internal/jobview/scope.go`).
  `vani run-once` and `vani run-request` take `--company`, `--state`, `--area`, `--remote` and
  `--or-remote`, which limit the run's advance phase (triage, score, explain) to the postings at
  those companies and places. The rest are untouched and wait for a later run. The flags read
  as `jobs list` reads them, through the one rule both apply (`jobview.Scope`), so
  `jobs list --view pending` with the same flags previews the run exactly. A test holds the two
  in agreement over a grid of postings and scopes. The run record's `options`, `status --json`
  and `runs show` carry the scope. The daemon refuses a request whose scope is not in the
  flags' folded form. A scope, or a named company, with nothing waiting is a warning in the
  run's output. The inbox poll never reads the scope.
- **CLI-Anything conventions for agents** (`cmd/vani/jsoncli.go`,
  `skills/cli-anything-vani/SKILL.md`). The global `vani --json <command>` returns exactly one
  JSON value on stdout for every outcome. A failure of any kind is `{"error","code"}`, with
  `code` one of `usage`, `unsupported`, `busy` (another vani holds the ledger) or `runtime`,
  and exit codes are unchanged. It refuses
  commands with no JSON form, and those do not run.

  `vani --json help` lists every leaf command as `flag`, `always` or `none` (`none` with a
  reason). Tests hold that table to the usage text and to each command's flags.

  Every command has a JSON form except `daemon`, `label` and `launchd plist`. That includes
  `run-once`, which answers with the record the run wrote, as `runs show --json` gives it.
  A command that fails after producing data keeps the data beside `{"error","code"}`, and
  progress text goes to stderr under `--json`. `internal/eval`'s report types gained
  snake_case JSON tags for `eval --json` and `relevance --json`.

  A command's own `--json` now also returns that envelope for a runtime failure it used to
  report only on stderr. A usage error keeps exit 2 and empty stdout. `jobs` failures gain
  `code`, and so do `config motivation`, `config exclude`, `answers similar` and
  `jobs ingest`, which wrote `error` alone. `logos refresh` and `bench local` used to fail with
  exit 1 and a success-shaped reply; they now keep their counts beside `{"error","code"}`, and
  `bench local --json` is one line like every other reply. The global flag takes
  `--json=true` and `--json=false`. `jobs retriage --batch --json` stays a stream of JSON
  lines, and SKILL.md says so.
- **Export warns before writing roles again** (`internal/exportlog`, `cmd/vani/jobsexport.go`). Every
  `vani jobs export` records the keys it wrote in `<state dir>/export/exported.json`: the first and last
  export per key, and the last 50 exports by file. A later export holding any of them asks in a
  terminal. Anywhere else it refuses with code `already_exported` and writes nothing. To go ahead,
  pass `--already-exported skip`, which leaves them out (`--top N` then takes the next N not yet
  exported), or `include`. `--dry-run` writes nothing and reports `already_exported`,
  `last_exported_at` and `last_file`. The Mac app runs that dry run first, then asks "Skip Exported
  Roles", "Export Them Again" or "Cancel". A record that cannot be read stops the export before the
  workbook is written. If the record cannot be written afterwards, the export exits 1 and names the
  file it wrote. Scripts that export the same roles repeatedly now need `--already-exported include`.
- **Application stages after "applied"** (`internal/marks/stages.go`): `vani jobs mark KEY
  screening|interview|offer|rejected|withdrawn|unstage`, `jobs list --stage`, a furthest-stage that never
  falls, and the Mac app's Stage row. Stages live in `stages.jsonl` so an older vani reads every staged
  posting as applied; rank labels are unchanged.
- **"Quiet since applied" digest section**: applied, screening and interview postings with no change for
  10 days, longest first, capped with "and K more".
- **Repeat-application warning** (`internal/samecompany`): an application to the same company within 60
  days shows in the extension panel, `jobs list`/`show` and the Mac app; never blocks a fill.
- **Likely-repost note**: the same company and title relisted under a new posting 14-90 days later.
- **Recruitee (`rc`), Pinpoint (`pp`), BambooHR (`bh`) and Personio (`ps`) adapters**, each on its own
  `{token}.` host.
- **`vani watchlist discover`**: suggests boards from the freehire.me public API (opt-in host, at most 3
  paced pages) and prints the `watchlist add` lines; it never edits the watchlist.
- **Ashby application-form summary** (`posting.Form`, `internal/applyform`): knock-out questions
  (sponsorship options, location lists, required essays and uploads) shown beside triage; annotation only.
- **Spreadsheet export columns**: Stage, Furthest stage, Applied, Repeat apply and Repost.
- **TikTok (`tt`) adapter**: lifeattiktok.com's own search API, one POST a page with descriptions in the
  list, search terms and a country or city filter resolved through the site's city list.
- **Radancy TalentBrew (`tb`) adapter** (Intuit, Citi): the site's sitemap and each job page's JSON-LD,
  at most 150 job pages a board a run.
- **On-device field triage and `sameQuestion`** (`macos/Sources/vani-fm`, `internal/decide/fmbackend`,
  `macos/Sources/VaniApplyKit/SameQuestion.swift`, `0e094d8`, `c45f7fc`). Form field triage (`career.form_field`)
  queries Apple's on-device Foundation Models (`apple_fm` via `vani-fm`) first before falling back to OpenRouter
  and Claude. Free, fast, bounded at 2 helpers and 15 s per run budget. The helper also powers the extension's
  `sameQuestion` native op to determine if two question labels represent the same question on-device.
- **Knowledge-base fact and choice answering in Suggest** (`internal/draft/answer.go`, `cmd/vani/suggestcmd.go`,
  `c45f7fc`). Answers fact and single-choice questions from the career knowledge base in a single batched Claude
  turn per page (up to 15 fields). Strictly validated, cited, and grounded against seeker-owned evidence. Pinned
  sensitive categories (`draft.Pinned`: work authorization, self-identification, consent) never reach a model.
  Unbacked drafts get one repair turn naming unbacked tokens. Outcomes logged to `<state>/suggest/outcomes.jsonl`
  under cross-process filelock.
- **Extension fill accuracy, write verification, and undo** (`extension/src/`, `d7e3b5a`). Selects latest answers
  among direct claims rather than most-used; uses on-device `sameQuestion` when field ID and label conflict;
  unambiguous choice matching; reads back writes to verify DOM values against reactive frameworks with a typing
  retry; panel Undo Fill restores untouched fields; dated answers fill yellow past threshold.
- **Batch re-triage shortcut (⌘A)** (`macos/Sources/VaniApply/JobsView.swift`, `cmd/vani/jobsretriagebatch.go`,
  `899236b`). ⌘A in the Jobs list prompts to re-triage the top 100, 250, 500, or 1000 postings (or the whole list
  if shorter) in current list order; maximum batch size raised to 1000 in `maxRetriageBatch` and
  `RetriageBatchClient.maxKeys`.

### Changed

- **`decide` and `rkb` on `encoding/json/v2`** (2026-11-16). Every file and request body they write
  keeps its bytes: options pin sorted map keys, `null` for a nil list or map, rkb's literal `& < >`
  with escaped U+2028/U+2029 and carried-over escapes kept as written, and the escaped `& < >` that
  the Lappi request line and the Decisions API body (pinned by n8n-email-engine) have always had.
  Byte goldens written by the v1 build pin this (`rkb/testdata/json-contract`, the jev, Lappi and
  spend-file tests), and rkb run on the owner's bank, resumes and verify log gave byte-identical
  output. Deliberate changes: a field name in the wrong case (`"Text"` for `"text"`) in bank.json
  is now an error naming the right spelling (v1 read it and also carried it, so the next save wrote
  both); invalid UTF-8 in a bank, resume or verify log is refused (v1 replaced it with U+FFFD and
  the next save rewrote it); a new verify-log entry writes `& < >` literally like every other rkb
  write; the spend file refuses a miscased key. `decide/lappi/client.go` stays on v1 until
  DevCouncil's copy moves with it, and the drift check now compares imports, so a one-sided switch
  fails it. The root module is still on v1.
- **One bounded detail step** (`srcutil.PlanDetails`): Workday, SmartRecruiters, Oracle HCM, Rippling,
  Avature and TalentBrew reuse stored postings, re-read each once in 30 days, skip new rows the title
  rule drops, and spend at most a fixed number of detail requests a board a run. Rippling (every
  detail every run before) and Avature (Bloomberg: 19 min a day) are new to it; Oracle HCM now
  re-reads stored postings.
- **Eightfold** reads the host's robots.txt before its first request of either API. A robots.txt that
  cannot be read now fails the board.
- **Avature**: boards of up to 400 postings (was 200), and Bloomberg's list and detail layout.

- **Draft grounding**: spelled numbers, credentials and acronyms must occur in what the model was shown,
  and credentials and years of experience only in the seeker's own evidence, target text or notes (a
  posting, question, title or company no longer backs them). Refusals carry a `kind` that the extension
  and the Mac app word.
- **Extension**: an answer longer than a field's `maxlength` is left for the person rather than cut, and
  the pre-submit audit reports values over a limit.

### Fixed

- **A job's role fit is the reading its ranking uses** (2026-11-14, the owner's "fix the label").
  The row's `fit`/`fit_label`, `--fit`, the stats' fit and strong counts and the export's Role fit
  column showed triage's primary reading, while the ranking reads the owner's manual reading, or
  the escalation tier's reading of an unsure primary, first (`jobinsight.RankContext.SlotFor`). On
  the live ledger 1,225 of 30,746 open jobs were labelled "Strong fit", and the ranking read 550
  of those as strong. They now read the same reading (`jobview.FitOf`, filled by
  `jobview.FitRanker` on every request, since a manual reading changes without the ledger moving;
  `jobs serve` reads it from the kept `Blend`, which names the reading it rested on, so it costs
  no second `SlotFor` pass). All 30,746 rows matched their score's fit reading (2,942 manual,
  12,495 escalation, 15,309 primary). The ledger's triage annotations, which the drop policy and
  `vani relevance` read, are untouched, and the jobs JSON's `triage` map still shows them. The
  order is unchanged (the top 2,000 keys and priorities compared equal on the live ledger).
- **A run limited to some boards no longer throws away the other boards' pending reopens**
  (`--board`). The run cleared the pending reopens of every board outside its cut as if those
  boards had left the watchlist, and saved the set, so dropped postings `vani retriage` had let
  back on them stayed dropped. Its exclusion pass also left their loosened exclusion drops
  dropped. Both now judge against the config's whole watchlist
  (`TestRunCutKeepsOtherBoardsPendingReopens`, which fails on either old site).
- **The weight fit reads the scores the owner saw, not an empty record** (`internal/rankfit`,
  `Targets`). Audit cards and Mac app Jobs rows are logged without Score's parts. `Targets` took
  each posting's newest impression as its scores, and for an audited posting that is the
  scoreless audit card. `ItemFromScores` then refused it ("no recorded gate or fresh"), so
  `vani rankfit --part weights` could not have run on any audit label once past its threshold.
  It now takes the newest impression that recorded scores, else scores the posting now. A
  scoreless Jobs row still counts as a pass.
- **Semantic score cache keeps the browsable postings** (`internal/semantic`). The cache held
  20,000 scores and evicted the oldest first, but the owner's boards hold about 29,000 browsable
  postings. Each warm evicted live scores to make room for the ones it had just scored, so the idle
  warmer re-scored the same overflow forever (1.68 million scores and 0.69 million rescores over its
  windows, with 9,311 postings always unscored). A posting that lost its score ranked on the 0.4
  prior. The cache now evicts the way the chunk-vector log does: postings outside the live set
  first, then the lowest ranked, then the oldest; a posting the current warm scored is never
  evicted for room unless the live set ranks it. The cap is 80,000, and the 64 MiB bound now
  sheds entries in proportion to the excess instead of half the cache. A warm that still has to
  evict a browsable posting reports it (`scores_evicted_live`, and a line from the idle warmer)
  rather than doing it silently.
- **Ranking: a role's level is penalised once, and "Staff" must agree with the years asked**
  (`internal/jobinsight`, `internal/expyears`, `internal/pipeline`). A seniority rank penalty and a
  `mismatch_seniority` role-fit reading say the same thing, yet both multiplied the gate, so a
  senior role in the owner's area (Anthropic's TPU Performance Engineer, gate 0.24) sank below
  off-domain staff roles that neither touched. The gate now takes the stronger of the two;
  `mismatch_domain` and a seniority penalty still compound. A `staff_plus` reading whose posting
  asks fewer than 7 years (the largest lower bound of its "N years" figures) is ranked at the level
  those years imply (5 or more senior, otherwise mid), and a title read as staff abstains on such a
  posting, so `vani retriage --title-drops` reopens one dropped that way (Perplexity's "Member of
  Data Staff"). With no figure, or 7 or more years, the staff reading stands. On the owner's ledger,
  with `staff_plus` added to `rank_penalties`, Anthropic's top 30 went from 15 staff+ roles to none.
- **Extension scraper**: a page read link by link stopped at its first 100 job links and said
  nothing, so Anthropic's careers page (669 jobs on one page) gave 100, and **Crawl all pages**
  called that complete. It now reads up to 2000 distinct job links (`MAX_PAGE_JOB_LINKS`); a posting
  linked twice counts once, and links past the bound are named in the drawer, the popup and the
  crawl's last line, which then reports itself incomplete. The same holds for the rest of the scrape
  path: JSON-LD stopped at 200 postings per block (now 2000 across the page, the rest counted); a page
  whose JSON-LD marked up a few featured roles hid all its job cards (the link reader now runs when it
  finds more jobs than the structured readers did); a paged Greenhouse board says how many jobs it
  lists beyond the page, and a crawl that ends short of that count is incomplete; Workday answer rows
  past 100 are named instead of dropped. A TalentBrew search (Citi: 15 shown of 3116) states its
  count the same way.
- **Extension scraper, navigation read as jobs**: on Citi's search 11 of 26 scraped "jobs" were site
  links (Saved Jobs, programme pages, talent-network forms, the pager's Previous), and ServiceNow's
  SmartRecruiters page gave its skip link and "Show more jobs"; each was sent to Vani as a posting.
  Pager controls are never jobs; a board reader's link back to the listing that names no posting is
  left out; and when most job links name a posting (a number, a UUID or a job parameter), the link
  reader leaves out those that do not and says how many.
- **Extension Save sends every job**: **Save to Vani** in the drawer and the popup sent one message of
  at most 200 jobs, so Anthropic's 669 went as 200 with "469 not sent (at most 200 at a time)", and a
  page of 200 full postings could pass the Mac host's 1 MiB bound in that one message and be refused
  whole. Save now goes through the crawl's batched sender (`sendScrape` over `sendCrawl`: 200 jobs or
  800 KiB a batch, the search saved by the first), says "Sending 201–400 of 669…" while it runs, and
  is reported ok only when no batch failed. A message that never reached Vani reads "Could not reach
  Vani", never "Vani refused". Only jobs no batch can carry are named as left out. The popup must stay
  open until the last batch: closing it ends the batches after the one in flight.
- **Extension crawl of a cut Workday URL**: a page URL with more than 20 filters, more than 200 values
  in one filter, or a search over 200 characters was cut to fit in silence, so the crawl read a
  different listing from the page's and could call it complete. The cut is now named in the crawl's
  notes and the crawl is incomplete.
- **Extension typecheck**: the fill reason `not_run` (contact checks that could not run) had no words
  in the panel's reason table, so `bun run typecheck` failed on `main`.
- **Workday short listings are counted**: a query under the 2000-row window that ends before the
  board's own total is still kept, not failed, but the rows it counted and did not list are now
  recorded (`boards.short`, `items[].short` in the run record) and named per board by
  `vani runs show` ("Listed short: …", largest first). A short listing is reconciled as a full one
  whose absent postings are all treated as a mass absence (`Reconciler.ReconcileShort`): the
  postings it lists get their sightings, pending reopens and field updates, and the ones it leaves
  out are held for the mass hold (14 days from their own first miss) and never killed by the
  ordinary two-miss rule, so the gap is visible and a board that is short on every run still ages,
  after the hold. It is not a failure. The same count now covers a Workday split whose
  allowance let a gap pass, and the hundredth that SmartRecruiters, Oracle HCM and IBM listings
  may lose before they fail; a board that then fails is not counted, as nothing ages. The app's
  run panel shows a **Listed short** row.
- **Inbox fold line adds up**: "folded N batch(es), M job(s)" now splits M into new, already known,
  duplicate of a stored job, invalid and repeated, prints any remainder as unaccounted, and says
  the other records it marked as duplicates apart. `pipeline.Summary` gains `ListedDuplicate`;
  `Duplicate` keeps its meaning. The inbox status (`vani status`, the app's Inbox row) carries the
  same split, and each board's reconcile counts in the run record carry `listed_duplicate`.
- `stages.jsonl` refuses a symlinked leaf on read and after compaction, as `marks.jsonl` does.
- `BankStoreTests.testConcurrentWritersLoseNothing` no longer fails under heavy machine load.
- **Workday**: a posting whose detail has an empty jobReqId is keyed by the id its path and
  jobPostingId agree on (Visa), and a stub listing row with no path or title is left out up to the
  listing's shortfall (Mastercard, HPE).
- **TalentBrew**: a JSON-LD block with raw control characters in its strings is read (Citi).
- **Daemon binary watch on run requests** (`cmd/vani/runrequest.go`, `0f51295`): `serveRunRequest` inspects
  binary replacement via `binarywatch` before claiming or strictly decoding run requests, restarting cleanly
  so newer options (such as run scopes) are not dropped as corrupt.
- **Jibe canonical URL client domain** (`internal/sources/jibe/jibe.go`, `5351d15`): accepts client's own
  `{client}.jibeapply.com` as canonical while retaining the board's host (e.g. GitHub `www.github.careers`).

## [0.2.0] - 2026-10-27

Prepared on 2026-10-27. Race-tested code is `8ca5be682f45ec41b9d4c7b275bf8fae08f51dfb` (main at HEAD).
Encompasses the 156 commits since `0.1.0`, including new ATS source adapters, native Excel workbook export,
the Resume Studio engine and macOS UI, and the complete 2026-10-26 deep audit remediation backlog.

### Highlights

- **Ten new ATS and careers board adapters:**
  - **Gem (`gm`):** Reads `https://api.gem.com/job_board/v0/{board}/job_posts/`, single-call whole listing, description pay range parsing (`2a28893`).
  - **SmartRecruiters (`sr`):** Reads `https://api.smartrecruiters.com/v1/companies/{company}/postings`, detail requests capped at 300 per run, two-letter country ISO filtering (`716cdfb`).
  - **Oracle HCM / Recruiting Cloud (`oc`):** Candidate Experience API with pagination, keyword search, and configurable `max_postings` up to 10,000 (`f813573`).
  - **IBM Careers (`ib`):** Covers IBM (`ibm`) and Confluent (`confluent`) career boards (`30773a1`).
  - **Avature (`av`):** Careers portal HTML adapter (e.g. Two Sigma), host and URL validation (`1eb4f18`).
  - **D. E. Shaw (`ds`) and Atlassian (`at`):** Single-request zero-detail career endpoints (`ab12f42`, `5e34e81`).
  - **Rippling (`rp`):** Public board API at `https://api.rippling.com/platform/api/ats/v1/board/{slug}/jobs` with detail fetches (`7718564`).
  - **Workable (`wk`):** Account widget API at `https://apply.workable.com/api/v1/widget/accounts/{slug}` with v2 shortcode detail fetches (`7718564`).
  - **XML Job Feed (`fd`):** Direct https XML job feeds (e.g. Shopify), partner job ID keys, standard library `encoding/xml` with external entity loading disabled (`7718564`).
- **Excel Workbook Export (`internal/xlsx`, `cd82d32`):**
  - `vani jobs export`: writes selected jobs or the top N (default 100, up to 5,000) sorted by priority, relevance, salary, or date to an `.xlsx` workbook.
  - Supports `--keys-stdin` to export explicit selections from the UI or pipelines.
  - Standard library only (`archive/zip` + XML), zero third-party dependencies.
  - Inline string encoding (`<is><t>`) ensures formula injection (`=`) is treated strictly as plain text.
  - macOS Jobs toolbar gains an **Export to Excel** menu and multi-row selection context menu.
- **Resume Studio & Versioned Resumes:**
  - Storage engine (`internal/resumestore`, `c136c19`): per-job versioned directory (`resumes/<sha256(key)>/`), atomic metadata, PDFs, attempts log, and locks.
  - Model pin (`4b545be`): All resume tailoring turns run exclusively on Claude Opus 5.5 (`claude-opus-5-5`) at high effort with 1,024 reasoning headroom, regardless of `llm.model`.
  - CLI surface (`cmd/vani/jobsresume.go`): `build`, `render`, `show`, `use`, `revalidate`, `diff`, `current`, `coverage`, and `gaps`.
  - Keyword coverage & gaps analysis (`internal/rkbcoverage`, `4ba14c6`): matches resume bullets against job posting keywords, identifies gaps, and appends uncovered skills to `<state>/resumes/gaps.jsonl`.
  - macOS Resume Studio (`0c30569`, `ed31bd7`): Native per-job window (`WindowGroup(for: String.self)`) with outline editing, drag-and-drop bullet reordering, bullet swapping against pool, section movement, entry overrides, PDFKit fitted preview, isolated web view unfitted preview, and redrafting sheets.
  - Browser extension dual-path resume delivery (`ea054d1`): Keyed builds (`resumeJob`) for tracked jobs with versioning vs stateless (`resume`) for untracked pages.
- **Source Updates:**
  - Workday: Support 3-digit pod numbers in tenant hostnames (`b33c049`).
  - Ashby: Fallback to job-board GraphQL query when posting API is disabled (`cc895c7`).
  - Apple: Multi-word query quoting and row parsing without team suffix (`6fbda51`).

### Fixes & Hardening (2026-10-26 Audit Backlog)

- **Symlink & non-regular file defense (`b8dca2c`, `ba1cbe2`, `e59931c`, `84dd8b2`):** `readBoundedFile` and storage layers reject symlinks, FIFOs, sockets, and character devices for `last.json`, impressions logs, marks, and manual-triage readings.
- **Daemon PID & liveness validation (`0f3ac31c`):** Checks process visibility (`EPERM` / `ERROR_ACCESS_DENIED`), binary identity, and start time tolerance to prevent stale PIDs from spoofing an active daemon.
- **Binary replacement detection (`b3448a9`):** `binarywatch` monitors the executable using OS file identity (volume serial + file index on Windows; device, inode, mtime, size on Unix) and exits 75 between runs on replacement.
- **Panic recovery (`f89d650`):** Panics following `guardedRun` finalize the run record as failed, ensuring the pipeline cursor and ledger state cleanly advance.
- **Race-free run requests and history flush (`378c663`, `6a289e4`, `3057b10`):** Mutex lock taken before state inspection; TOCTOU checks prevent concurrent request races and data loss during history flush.
- **Scrubbing & privacy bounds (`dd3d15e`, `5fcf29f`):** Spaced phone numbers, mailboxes, and work-authorization boilerplate are purged from bullet text before embeddings/kbmatch and from redraft instructions.
- **macOS app robustness (`129cdd2`, `be7a171`, `6c1fbc0`, `86e7844`):** Bank answer counters capped at `Int32.max` before SQLite binding; 50MB file size cap verified before loading; bank edits and activity log updates run asynchronously off the main actor; schedule-agent pid wait reliability improved.
- **Extension resilience (`7dc8df5`, `3894b41`, `3bc0b49`, `49a7047`):** Reuses single native messaging port even when `connectNative` appears after import; frame probe timeouts spliced cleanly without poisoning native messaging; bank cache `fresh()` drops in-flight fetches and generation cache; script-bound answer import picker.

## [0.1.0] - 2026-10-21

Prepared on 2026-10-21. Not published. There is no previous tag, so this is the first release note. It names the product surface from commits whose subjects were read for this note. It is not a line-by-line history of every commit since `cbfe2caa94a655a18cc4433c7dd87070c7f2c2cc`.

The race-tested code is `aa8ed0cd35f3d283ec02be1a7acfcc2af1893eb0`. The commit that adds this file is documentation only.

### Highlights

- Vani starts as a listings-first career engine: a Go daemon, the macOS app Vani Apply, and the browser extension (`cbfe2caa94a655a18cc4433c7dd87070c7f2c2cc`).
- Source boards search more than one query, with verified site filters and four search-site ATS kinds (`a0bc4f6052dc03be2fc53a462274bd98381fefd2`).
- Jobs ingest keeps an inbox, a near-duplicate check, cross-source identity, and a Jibe source (`cfb7b0ac7ec0b373bfd78f99e264edaba0da6e0d`).
- Jobs insight adds a location filter, an extension apply broker, and shadow eval (`365a40537ab46c57a0c2fe047ce1fe2b4e14213b`).
- The extension counts fill, save, and scan in every frame and answers every message (`c2444cfd84bd7a6dde05061009fbfafd06a78a08`). Basics and question intents fill the first form (`1bbb13ff6174f17334a179966fa111b61369cc78`).
- Jobs and the digest rank by one Score. The legacy relevance and priority fields are gone (`b71f0ebf89bd84bfb8383a7cc1b9f09d1f1d8f4a`).
- An optional embedding match compares postings with the resume knowledge base (`ce06891745cd836d28437dccbfc4f320513818ca`). It links only with `-tags tessl` on Darwin/arm64 when the Tessl archive is present. Otherwise the build still ships and semantic matching reports that it did not run (`macos/scripts/bundle.sh`).
- The daemon slices `waitUntil`, so sleep and wake cannot skip the daily run (`4ad64d001f52c60228fa260a8e203e209a94340c`). The Mac app announces that run with a native notification that opens Jobs (`fe49bf11b199d6b62f179a9a5adb14b08b4b8a8f`).

### Fixes

- Safari reads an extension script with no charset as windows-1252. A literal combining mark in the résumé normalizer then throws "range out of order" and the background script never loads. The bundler writes every non-ASCII UTF-16 unit as `\uXXXX` (`205849ff0e726b4cf3733282ede2f1af07674978`).
- `attachResume` goes to the sender's tab. The extension typechecks from a pinned compiler. A sender policy refuses the wrong caller before work runs. Scraped links that are not `http` or `https` are dropped (`ff0ba3862bc4d0322e8f31748a2d44acf8cd6610`).
- The daemon exits when the ledger is poisoned, and a knowledge-base sync replay keeps its stamp (`b6dd36a2545cd4f78609be42615576cc9a2cdc57`).
- Fallback title keys keep C++, C#, and .NET apart (`7a6ee3e2da5a0fb9f5cbdc06724bd2009e510731`).
- Test only. Rankfit's race tests now finish inside Go's default 10 minute package deadline (`42c4f64bdeb4fc8e7ddcd62234492c49dba813bd`). On their own, before this change, they took 799.947s.
- Test only. Platt sample keys use an atomic counter, so parallel tests do not share one increment (`aa8ed0cd35f3d283ec02be1a7acfcc2af1893eb0`).
- Test only. The second-facet crawl allows 30s. A quiet run is about 2.7s; Bun's 5s default failed it while a Go race suite shared the machine (`1da07b080a6d4c68838b972dd580129e9d2c3cf0`).

### Hardening

- Append-only stores fsync the directory on create, guard a torn tail, cap note lines, bound a partial knowledge-base sync, refuse rekey on Windows, and bound the spool (`317beb38867f4fb3a54807909d59f51c6d8f85df`).
- The Go tree compiles for Windows and Linux, child processes share one environment list, and the vault allowlist is sealed (`333f62abdc3d64a4221c9cb642ac05edef32407b`). That commit says `GOOS=windows` and `GOOS=linux` `go build` and `go vet` passed and that nothing ran on those systems. This release preparation did not repeat those builds. Off macOS the vault fails with `ErrNoKeychain` before it runs `security`. There is no Windows or Linux app bundle.

### Breaking changes

- OpenRouter config keys were renamed (`cf5ef63cccd4186070b4950545e4048549faf896`). `jev_model`, `jev_key_env`, `daily_usd.jev`, `min_jev_prob`, and `decide.shadow` `jev` are now `openrouter_model`, `openrouter_key_env`, `daily_usd.openrouter`, `min_openrouter_prob`, and `openrouter`. Old configs and track files still load. A file that sets an old key and a new key to different values is refused. Save writes the new names. Ledger rows written earlier keep the name `jev`; `vani eval` and `vani label` map that name and say how many they mapped. The daily cap file stays `budget/jev.json` on purpose. README "Renamed keys (2026-10-19)" matches this commit and also records that `kbmatch.backend` `"jev"` became `"openrouter"` on 2026-10-20.

### Known issues

- Nothing publishes v0.1.0. There is no release workflow, no checksum, no SBOM, and no updater. Signing in `macos/scripts/bundle.sh` defaults to Apple Development with `--timestamp=none`, then `codesign --verify --deep --strict`. It does not notarize. The decision is GitPulse `ft-52b6e7d3f0950c24195d9c20c0a20351`.
- A clean clone cannot build. `go.work` is gitignored and required, `go.mod` replaces sibling checkouts, and `go.sum` is absent. The decision is GitPulse `ft-8d8ca2ea609144eb5240c2b743487d58`.
- `atomicfile.Write` replaces a symlinked path with a regular file. That bug is GitPulse `ft-22d7da5f2735997b8bbdb7934bc1345c`, still in progress, and this release does not change it.
- `CFBundleShortVersionString` and the extension manifest are `0.1.0`. `CFBundleVersion` is `1`. `extension/package.json` has no version field.
- Marketing version `0.1.0` is the extension manifest and the two short version strings above. There is no root `package.json`, no `Cargo.toml`, no `Cargo.lock`, and no `tauri.conf.json`.
