# Ranking and filtering: audit and plan (2026-10-20)

Sources: four read-only audits (ranking, filter funnel, labels, model research) and an independent
review. Claims are marked **verified** (re-checked in code or in `.state`), **auditor-read** (one
subagent read the code; not re-checked), **inferred**, or **unverified**. The live state is
`.state/state`. The Application Support state dir is a 1,286-record stub with no triage. Statements
about the live config (`.state/config.json`) were reported by the sessions that changed it and were
not re-read here; the repo carries no copy of it.

## Status (after the final landing, 2026-10-20)

| Phase | State | Where |
|---|---|---|
| Do now: Haiku triage fallback | **done** (live config) | `llm.triage_model` |
| Do now: Staff spellings in the staff_plus `unless` list | **done** (live config, reported); 5 title drops returned by `vani retriage --title-drops` | `title_signals.read` |
| 1 Measurement | **done in code.** Impressions (`vani impressions log`, the digest, audit cards, the Jobs tab), Explore picks, the weekly `vani audit`, `vani relevance`. Exit (7 days of impressions, the first batch judged) is owner time. | `internal/feedback`, `internal/eval`, `cmd/vani/auditcmd.go` |
| 2 Surfaces | **done for the digest, advance order and Jobs row contract** (Top open, one tie-break, age-neutral advance order). The recency red-team case is restated as policy. | `internal/digest`, `internal/jobview`, `advanceorder.go` |
| 3 Cascade and spend | **done except the semantic floor.** Triage fallback Haiku, notes gate (floor 32) and notes cap, thin-description skip, reading inheritance, fail-open counters, exclusions. The floor before Liquid D is open (see owner decisions). | `internal/config/funnel.go`, `internal/pipeline` |
| 4 Calibration | **fitter built, waits on labels** (needs ≥100 per backend): `vani rankfit --part platt` refuses below it | `internal/rankfit`, `rank_weights_file` |
| 5 Weight fit | **fitter built, waits on labels** (needs ≥150 would_apply answers): `vani rankfit --part weights` refuses below it, and writes only past the held-out gain interval | `internal/rankfit`, `rank_weights_file` |
| 6 Lappi v2 | nothing to build until Lappi is served | — |

Findings N1 to N10 and D1 to D11 of the later audit, and the advance-caller differences, are below.

## Summary

- **Nothing measured ranking against the owner's judgement.** `rankeval` worlds are synthetic and use
  the same `quality × recency` form as `Score` (`rankeval/world_test.go`), so NDCG barely moves when
  the weights change (0.7341 → 0.7346). The real-ledger proxy scores against the model's own
  `role_fit` reading. (verified) The would_apply labels and impressions are the measurement; they
  now accrue.
- **There is no training signal yet.** The live state has 29 saved, 0 applied and 0 hidden. All 29
  saves came from one 14-minute session, picked from the top of the current ranking (mean semantic
  0.887 against 0.651 overall). Until impressions landed nothing recorded what was shown, so an
  unmarked posting did not count as a negative. There are 3 days of history and no apply outcomes.
  (verified)
- **TimesFM and Google's TabFM are not the right tools.** TimesFM forecasts time series and has no
  ranking objective; 3.0's weights are non-commercial. TabFM 1.0 (Google, June 2026) does in-context
  classification and regression. Its weights are non-commercial, it needs a Python runtime, and it
  still needs the labels we lack. When labels exist, the right model is a regularised pairwise-
  logistic fit of the existing `Score` parts, in Go, shipped as a weights file. (research; licence
  texts not fully read)

## Ranking findings

`priority = 100 · match · gate · fresh · company` (`internal/jobinsight/score.go`).

| # | Finding | State |
|---|---|---|
| R1 | ~~The digest sorting by rank-presence before Score is a bug~~ — **withdrawn**. It is deliberate: the digest heads unscored postings "Not ranked" so that a skipped DevPrism stage cannot pass for a ranking. Only 14 digested postings were affected. | withdrawn (verified) |
| R2 | ~~The live top 20 lack coverage~~ — **stale**. A fold of the live ledger found 2,287 of 2,301 digested postings with coverage and rank. | stale (verified) |
| R3 | One number answered two questions: *what is new* and *what is the best open posting*. **Resolved:** the digest's "Top open" ranks by the age-free `Relevance` (`9efed0d`, `4bcbd30`); the red-team case asserts the excellent old posting leads there and is restated as policy. | **done** |
| R4 | Thin evidence read as strong. **Resolved in `Score`:** `MatchShrink` 0.30 and confidence-scaled part weights (`c56df41`). The displayed relevance keeps `relevanceShrink` 0.10 (owner decision). | **done** |
| R5 | Confidence was asymmetric: a calibrated Liquid D p of 0.44 was trusted less than an LLM answer with no p. Confidence now scales a part's weight (`c56df41`). Per-backend Platt scaling replaces the 0.70 constant at ≥100 labels. | partly done; calibration open (Phase 4) |
| R6 | `semantic` (0.35) and `kb_match` (0.15) both measure closeness to the KB, and kb_match's 0.15 was never measured. | open: measure with audit labels |
| R7 | `advance` order ignored `age_neutral_companies` and the config (`4bcbd30`, `b4518a6`: fixed). `extension/src/relevance.ts` is still a separate heuristic; the extension README says its number is an estimate and the Vani row is the ranking. | advance done; extension is a documented estimate |
| R8 | The default sort broke ties on `Rank`, which counts coverage a second time. One shared tie-break now serves the digest, Jobs and advance (`0cc2745`, `b4518a6`). | **done** |

## Filter-funnel findings

Free stages run first: the title `read` lists, then the `exclude` block (landed, `d2f1724`; not set
live). Then the paid router: Liquid D (`liquid/d1-ultra` on OpenRouter), then the Claude fallback.
That fallback was Opus until 2026-10-20 and is Haiku now. No shadow is configured live. Only Liquid D
at p≥0.9 (or Lappi) can drop a posting.

| # | Finding | State |
|---|---|---|
| F1 | ~~The domain title read needs AI/ML `unless` terms~~ — **refuted by the ledger**: every live domain title-drop with an AI/ML/Data term is a product, program, marketing, finance or design role. The real gap was the staff_plus read sparing "Senior/Staff" but not "Sr / Staff", "Staff/Senior" and similar; fixed in the live config, 5 postings returned. Title drops by slot: role_fit 319, seniority 296; model drops: role_fit 354, seniority 160. Workday listing drops are not stored. | done (live config); AI/ML terms stay out |
| F2 | ~~The Opus fallback pays for readings that can never drop~~ — `llm.triage_model` is Haiku. | **done** |
| F3 | No location exclude is set live. The exclude block (title, company, location, description) landed in `d2f1724`, with the Mac app's editor (`be85a51`). Setting it is the owner's call. | built; owner config |
| F4 | Notes ran for every survivor. Now gated on the age-free relevance: floor 32 (the integrated ranking's p78 of the live ledger, `7716b4d`), its own daily cap `funnel.notes_daily_usd`. `advance_per_run`: owner decision 750 (the code default is 300). | **done** |
| F5 | Fail-open keeps were not counted. Counters per cause (`602cdaf`), named in the digest footer (`9efed0d`); the exclusion stage counts a posting a location rule could not read as `location_unreadable`; the inbox poll fills the same report (`966c0db`). | **done** |
| F6 | Drops had no recall measure. Now: the dropped stratum in the label queue and the weekly `vani audit`, `vani relevance` (precision with Wilson intervals, drop-candidate agreement per shadow), `decide.shadow_mode: drop_candidates`. Recall needs labels. | built; waiting on labels |

## Ranking audit (N) and funnel audit (D)

"Where" is a file or commit on this branch that I found; a row says "reported" where I could not find the code.

| # | Finding | State | Where |
|---|---|---|---|
| N1 | Confidence interpolated a fit's value but never reduced its weight (62% of readings had p<0.5; a strong_fit at p=0.21 ranked #4). | done | confidence scales a part's weight, `internal/jobinsight/score.go` (`c56df41`) |
| N2 | A kept mismatch_domain was penalised three times (fit part, gate, drop-rule penalty). | done | gate only: `Mismatch` and `PenaltyStrength` act on the gate, `score.go` (`c56df41`) |
| N3 | Coverage exactly 0 on 22%: DevPrism's 0 of 0. | Vani side done; DevPrism fix open | `gap.coverage_unknown`, `vani rescore` repair (`a67bbae`, `e1238b3`), null accepted only with no must-haves (`internal/resume/tools.go`); DevPrism task `dp-gap-nice-only-must-haves` |
| N4 | Digest tie-break by ATS key; the app's top roles tied by list order. | done | one Relevance, Priority, Key tie-break (`0cc2745`, `b4518a6`) |
| N5 | The digest ignored the configured half-life. | done | digest options carry it (`0cc2745`, `7446092`; `Options.HalfLifeDays`) |
| N5b | Keyword hits (weight 0.03) are absent in the digest. | open, minor | `digest.go` `sortDigest` comment: that part takes its prior for every role |
| N6 | The company factor penalised a whole company for hides (to 0.67) and the 1.1 cap clipped. | done | upward-only from saves and applies, `CompanyMin` 1.0, `CompanyMax` 1.1, `score.go`, `inputs.go` |
| N7 | Employer preference weight 0.05 is effectively a tie-break. | open | needs labels (Phase 4) |
| N8 | The extension's separate "x% Match", good_fit label, crawler scoring location text. | done | Vani's row when available, a labelled estimate otherwise (`extension/src/jobrow.ts`), `rankListing` scores the title only (`relevance.ts`) |
| N9 | Near-duplicates and company concentration in the top 100. | done | `Diversify`, at most 3 per company in the top 50, exact company and title folded with a similar count (`internal/jobinsight/order.go`) |
| N10 | Notify does not use the score. | no action | — |
| D1 | The llm fallback answers only unanswered slots and can never drop (F2). | by design | Haiku `llm.triage_model` set |
| D2 | Notes ran for every scored posting. | done | relevance floor 32 and its own cap (`internal/config/funnel.go`, `7716b4d`, `b8bee70`) |
| D3 | Semantic never gated paid calls; 750 warmed against 3000 advanced. | done | `advance_per_run` 750 in the live config (reported), semantic advance order, the tail reported as deferred |
| D4 | DevPrism ran after triage; Gap and Score both. | done | DevPrism before triage, `funnel.score_after_triage` restores the old order; coverage is not a gate (`602cdaf`) |
| D5 | No in-chain escalation or agreement. | measurement only | `decide.shadow_mode: drop_candidates` with the cloud Haiku shadow, the agreement report in `vani relevance` (`fa84e4b`); a rule change waits for labels |
| D6 | A drop proceeds despite shadow disagreement. | open by decision | measure first; reported in `vani relevance` |
| D7 | Low-p answers become Triaged, not Unsure. | by decision | handled in ranking by the confidence weight, not by status |
| D8 | Fail-open keeps uncounted; no empty-description gate. | done | counters `empty_description`, `title_unread`, `location_unreadable`, `prescore_failed`, `coverage_unknown` (`internal/pipeline/failopen.go`), digest footer, inbox poll too (`966c0db`), `min_description_runes` 40 |
| D9 | Near-duplicate siblings paid their own triage. | done | reading inheritance, `triage.inherited_from`, never across locations for a drop; eval and bench leave inherited readings out (`internal/pipeline/readcache.go`, `57b128a`) |
| D10 | No triage-reading cache by content. | done | cache on (desc_hash, title, location, model), `readcache.go` |
| D11 | Notes and triage shared one cap. | done | `budget/jev-notes.json` beside `budget/jev.json` (`funnelwire.go`) |

### Advance-caller differences (`advancePlan`, `cmd/vani/runwire.go`)

| # | Difference | State | `AdvancePlan` field |
|---|---|---|---|
| 1 | The inbox poll ignores the tuned cap override. | deliberate | `Limit` (run: the override, else `advance_per_run`; inbox: `advance_per_run`) |
| 2 | The inbox poll skips the semantic order and warm. | deliberate (user-submitted, arrival order) | `Order` (run: title rank then semantic; inbox: title rank) |
| 3 | The inbox poll dropped the funnel and fail-open counts and shadow warnings. | fixed (`966c0db`) | `FillFunnel` true for run and inbox |
| 4 | Rescore sorts by freshness only. | deliberate | `Select` and `QuietCap`, with its own order |
| 5 | `--list-only` skips the advance phase only in the run. | deliberate | `Skip` (run only) |

## Target architecture until Lappi v2: a cost-ordered cascade

Every stage either annotates or orders. Only engine rules keyed on annotations drop (Rule 7). A
cheap stage decides whether to spend on a dear one, and no posting reaches a paid stage with a free
signal missing.

| Tier | Model | Job | Can drop? |
|---|---|---|---|
| 0 | Rules (title read/exclude, location exclude) | Free pre-filter | Yes, as an engine rule |
| 1 | DevPrism | Coverage for every kept posting | No |
| 2 | EmbeddingGemma (Tessl) | Semantic score, near-dup, kb_match focus lines | No |
| 3 | Liquid D `d1-ultra` (OpenRouter) | Triage slots | Yes, p≥0.9 |
| 3b | Liquid D local (`decide.local`) | Optional shadow only. **Not planned**: the owner chose cloud-only for now | No |
| 4 | Claude Haiku (`triage_model`) | Fallback and escalation when Liquid D p is low; shadow on drop candidates | No (confidence only) |
| 5 | Claude Sonnet | kb_match for top-K, notes above a relevance gate | No |

### Model access today

The decision model is **Liquid D**. "Jev" survives as the name of the wire client
(`devtools/decide/jev`, the OpenRouter Decisions API protocol) and in two file/value names kept on
purpose: the daily cap files `budget/jev.json` and `budget/jev-notes.json`, and their spend key
`jev`. The cap takes no lock across processes, so renaming the file would let a daemon still on the
old path start the day at zero (pinned by `cmd/vani/openrouter_budget_test.go`). Config keys and
`kbmatch.backend` moved to `openrouter` names with the old ones still loading (`ca41802`,
`internal/config/legacy.go`). `config.Default()` seeds `liquid/d1-ultra` and no paid language model.

| Model | Path into Vani | Live? |
|---|---|---|
| Liquid D `d1-ultra` | OpenRouter, through `decide/jev` (`decide.openrouter_model`) | yes, the primary |
| Liquid D local (`d1-3B` / `d3-mini`) | llama.cpp `/v1/systemone` (`decide.local`), shadow only | no, and not planned |
| Lappi | local socket (`decide.lappi`) | no, not served |
| EmbeddingGemma 2 | Tessl in-process (`-tags tessl`) | yes |
| Claude | Manvi `llm` provider (`llm.model` Opus, `llm.triage_model` Haiku) | yes |
| Claude in Jarvis | not wired into Vani | — |

The recommended cloud setup is a Claude Haiku as the shadow on drop candidates; the snippet is in
the README ("Cloud shadow").

How models agree:
- Use multi-model agreement as a **confidence modifier**.
- A drop rule may tighten to "two backends agree at p≥X". That is stricter than today and stays
  inside Rule 7.
- When backends disagree, the posting goes to `unsure` and is kept.
- Don't seek agreement on notes or kb_match.

## Measurement (Phase 1)

- **Impressions:** `<state>/feedback/impressions.jsonl` (`internal/feedback`). One line per posting
  shown: surface (`digest`, `jobs`, `audit`), section, 1-based position, a `slate_id`, and for the
  digest the priority, relevance, known Score parts and, for an Explore pick, its stratum, p_select
  and drop class. A line never holds a title, company, text or note.
  - A key is logged once per (slate, surface). The digest's slate is its date, an audit session's
    is the draw's seed, a Jobs refresh's is the view and the local day. Re-reporting a slate adds
    nothing.
  - The log is bounded: at 16 MiB it rotates to `impressions.1.jsonl` (replacing the one before),
    so it holds two generations at most.
  - The digest records what it wrote (a failure is printed and kept in the run record as
    `impressions_error`); the terminal audit records the cards it showed; the Mac app calls
    `vani impressions log` for the top 20 Jobs rows after each refresh and for each Audit card.
    `audit --json` and `--list` write nothing.
- **Digest sections:** **Top open** (`digest.top_open`, default 20: the best roles listed before and
  still open, by Relevance) and **Explore** (`digest.explore_below_fold` default 2,
  `digest.explore_dropped` default 1: picked by a hash of the date; each row carries its stratum and
  p_select). Neither changes a posting's status; `0` turns one off. Explore and the audit draw take
  unmarked roles only: a marked role is already judged.
- **Audit:** `vani audit` (terminal) or the Mac app's Audit tab: the weekly draw (15 by default,
  stratified over priority deciles and drop classes, stable within an ISO week) asks one question,
  would you apply (yes / maybe / no / skip), and writes a `would_apply` label with the stratum and
  the inclusion probability. `vani audit answer` records one verdict. A drop class names the backend
  and slot only (`title:role_fit`), never a rule's value.
- **Report:** `vani relevance` reports would_apply rates with 95% Wilson intervals, precision at K,
  and the shadow's agreement on drop candidates. Nothing there is weighted by p_select; the labelled
  set is a stratified sample. (The weight fit below does weight by it.)
- **Held-out promotion floors:** the registered split holds out 30 postings, equal to
  `MinPromotionLabels`, so a single skip would have made the held-out Promotion table
  "insufficient" for good. Its asked and paired floors are the split size less
  `HeldOutAllowedSkips` (3), so 27 of 30; the discordant floor (10) and the exact lower bound are
  not scaled, and the all-labels table keeps its 30. A row that is still insufficient says why and
  what would clear it (more labelled postings, or the split size the current labels would satisfy).
  The split size is pre-registered and unchanged. A shadow that was asked and answered nothing keeps
  its row in the shadow table, shown as "no answers (N unavailable)" with precision n/a.
- **Cadence (weekly audit):** 5 minutes a day on the Explore picks, 15 minutes a week on the audit.
  That is about 150 labels a month, so the weight fit can run around late November 2026 (it needs
  the held-out pairs and scored-stratum pairs below before it can write a file, so likely later).

## Weight-fit gate

`vani rankfit --part weights` writes a weights file only when `FitResult.Beats()` holds:

- **n:** at least `MinHeldOutPairs` (200) held-out judged pairs, printed with the run.
- **Interval:** a 95% percentile bootstrap of the accuracy gain (fitted minus hand), over the
  held-out **items** (`BootstrapResamples` = 1000, fixed seed from `--seed`). Items, not pairs,
  are resampled because pairs share postings and are correlated; a pair-level interval would be too
  narrow. The lower end must be above 0. A bare `fitted > hand` was true in 5 of 20 null worlds
  (labels drawn from the hand weights themselves).
- **Strata:** each judgment is weighted by 1/`p_select` (Horvitz-Thompson), in the fit and in the
  held-out accuracy, because the audit draws 10 dropped for 5 scored. A `p_select` outside (0, 1] on
  a sampled label is refused. Held-out accuracy is also printed per stratum (scored, dropped, mixed),
  and when the held-out set holds sampled postings, at least `MinScoredStratumPairs` (50) pairs must
  be between two scored-stratum postings: the ranking orders scored postings, so a held-out set of
  mostly dropped ones cannot carry the claim. Marks, impressions and manual labels carry no
  inclusion probability and weigh 1.
- More than `MaxHeldOutPairs` (2,000,000) held-out pairs is refused (lower `--holdout`), not
  sampled. Otherwise `vani rankfit` returns "no gain", names the reason and writes nothing.

## Training: when and what

- **Now: nothing.** Any fit today would learn what the ranker already showed.
- **At ≥100 usable labels:** per-backend Platt scaling replaces `AnsweredConf` and raw p.
- **At ≥150:** a pairwise-logistic fit over the Score parts, with an L2 prior centred on today's
  weights, in Go. Both refuse below threshold with a "could not run" result.
- **Lappi v2 drop-in:** keep the `Slot` contract, the `Inputs` fields and the
  `jdtriage/fixture.json` sha pin (a change means a retrain). Lappi becomes another router backend.
- **TimesFM's only legitimate use** is forecasting posting volume per employer, once months of
  history exist. Optional and low priority.

## Phases

1. **Measurement foundation: done.** Impressions, Explore, the weekly audit, fail-open counters.
2. **Surface consistency: done.** Top open, one tie-break, advance order reads the config.
3. **Cascade and spend: done except the floor.** Haiku, notes gate and cap, thin-description skip,
   reading inheritance, exclusions. The semantic floor before Liquid D is open.
4. **Calibration (needs ≥100 labels): open.** Per-backend Platt scaling (`vani rankfit --part platt`
   is built and refuses below the threshold), the agreement rule for drops, store Workday listing
   drops. Exit: drop recall measured on the exploration pool.
5. **Weight fit (needs ≥150 labels): open.** `vani rankfit --part weights` is built: it fits Score's
   shares from `vani audit answer`'s would_apply labels, marks and impressions, and writes a file
   only when the fit clearly beats the hand weights on held-out pairs (see "Weight-fit gate" below);
   `rank_weights_file` applies it. Exit:
   pairwise accuracy on held-out audit labels beats the hand weights, and the labelled set replaces
   the synthetic rankeval worlds.
6. **Lappi v2 swap** when it is served. Exit: shadow agreement with Liquid D within the
   `fitConfusion` band before promotion.

## Owner decisions

Decided 2026-10-20:
- **Fitting target:** both signals. Save/Hide marks give volume; the audit's would-apply label is
  ground truth and breaks ties.
- **Recency:** split into "New" (priority, recency-weighted) and "Top open" (age-free Relevance).
- **Exploration:** 2 below-fold + 1 dropped a day, Top open 20; change them under `digest`.
- **Triage fallback:** Haiku. **Cloud only:** no local decision model for now.
- **`relevanceShrink` stays 0.10** (the displayed relevance); `Score`'s `MatchShrink` is 0.30.
- **Budget file names kept:** `budget/jev.json`, `budget/jev-notes.json`, spend key `jev`.
- **Notes floor 32** (the integrated ranking's p78) and **`advance_per_run` 750**.
- **Advance callers:** the daily run, the inbox poll and rescore differ in five ways
  (characterized in `0454c4c`, named fields of `advancePlan`). #3 was a bug and is fixed (the inbox
  poll fills the funnel report and surfaces shadow warnings, `966c0db`); #1, #2, #4 and #5 are
  deliberate.
- **Coverage 0 of 0 is unknown:** Vani records it as `gap.coverage_unknown` and `vani rescore`
  repairs old rows without a DevPrism call. Vani accepts both DevPrism's old `0` and its coming
  `null` coverage for a JD with no must-have skills, and only then (a null beside must-haves is
  malformed). DevPrism's upstream fix is pending at `/Users/bharath/Code/devtools/DevPrism`
  `jd.rs:559` and `gap.rs:246`.
- **Weekly audit:** the audit is a weekly habit, 15 minutes.
- **Title rules:** no AI/ML `unless` terms (refuted by data); Staff spellings added.

Still open:
1. The semantic floor before Liquid D, once the live distribution is seen.
2. Should Engineering Manager roles be kept (TRACKS.md: remove `manager` from the drop rule)?
3. Whether to set a location `exclude` live (F3), and which entries.
