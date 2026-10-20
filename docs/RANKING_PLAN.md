# Ranking and filtering: audit and plan (2026-10-20)

Draft for the owner to decide on. Sources: four read-only audits (ranking, filter funnel, labels,
model research) and an independent review. Claims are marked **verified** (re-checked by me in code
or in `.state`), **auditor-read** (one subagent read the code; not re-checked), **inferred**, or
**unverified**. The live state is `.state/state`. The Application Support state dir is a 1,286-record
stub with no triage.

## Summary

- **Nothing measures ranking against your judgement.** `rankeval` worlds are synthetic and use the
  same `quality × recency` form as `Score` (`rankeval/world_test.go`), so NDCG barely moves when the
  weights change (0.7341 → 0.7346). The real-ledger proxy scores against the model's own `role_fit`
  reading. (verified)
- **There is no training signal yet.** The live state has 29 saved, 0 applied and 0 hidden. All 29
  saves came from one 14-minute session, picked from the top of the current ranking (mean semantic
  0.887 against 0.651 overall). Nothing is logged about what was shown, so an unmarked posting does
  not count as a negative. There are 3 days of history and no apply outcomes. (verified)
- **TimesFM and Google's TabFM are not the right tools.** TimesFM forecasts time series and has no
  ranking objective; 3.0's weights are non-commercial. TabFM 1.0 (Google, June 2026) does in-context
  classification and regression. Its weights are non-commercial, it needs a Python runtime, and it
  still needs the labels we lack. When labels exist, the right model is a regularised pairwise-
  logistic fit of the existing `Score` parts. It would be trained offline and shipped as a JSON
  `Weights` vector, which `ScoreWith` already accepts. (research; licence texts not fully read)
- **The first thing to build is measurement**: impression logging, exploration slots and a weekly
  audit sample. Weight tuning comes after.

## Ranking findings

`priority = 100 · match · gate · fresh · company` (`internal/jobinsight/score.go`).

| # | Finding | Status |
|---|---|---|
| R1 | ~~The digest sorting by rank-presence before Score is a bug~~ — **withdrawn**. It is deliberate: `RenderWith` heads unscored postings "Not ranked" so that a skipped DevPrism stage cannot pass for a ranking (`digest.go:212-214`). Only 14 digested postings are affected. | verified |
| R2 | ~~The live top 20 lack coverage~~ — **stale**. A fold of `.state/state/ledger/events.jsonl` finds 2,287 of 2,301 digested postings with coverage and rank, and only 1 strong/partial fit without. The FOLLOWUPS note (line ~988) needs updating. | verified (the fold assumes annotations merge across events) |
| R3 | One number answers two questions: *what is new* and *what is the best open posting*. Freshness spans 0.25–1.0. An excellent 21-day-old posting scores 31.5, below a mediocre same-day one at 37.6. The red-team test for this is skipped as a known failure. `Result.Relevance` already exists as the age-free number but no surface sections on it. | verified |
| R4 | Thin evidence reads as strong. Semantic alone at 0.9 gives match ≈0.79, against ≈0.855 with every part known at 0.9. `MatchShrink` is 0.10. | inferred (hand arithmetic) |
| R5 | Confidence is asymmetric. A calibrated Liquid D p of 0.44 is trusted less than an LLM answer that carries no p (`AnsweredConf` 0.70). A p near 0 still counts as known. | verified |
| R6 | `semantic` (0.35) and `kb_match` (0.15) both measure closeness to the KB. kb_match's question is built from the 8 embedding-nearest lines. It is asked only for top candidates, which biases the selection, and its 0.15 weight was never measured. Live answers: 0. | verified |
| R7 | `advance` order runs Score with only the semantic input and ignores `age_neutral_companies`. `extension/src/relevance.ts` is a separate heuristic with a hardcoded generic profile. | verified |
| R8 | The default sort breaks ties on `Rank`, which counts coverage a second time. | verified |

## Filter-funnel findings

Free stages run first: the title `read` lists and the `exclude` block, which is not set live. Then
the paid router: Liquid D (`liquid/d1-ultra` on OpenRouter) ($20/day cap), then the Claude fallback. Until 2026-10-20 `triage_model` was
unset, so the fallback was **Opus** ($10/day cap); it is now Haiku. No shadow is configured live (the auditor's "d3-mini 1 in 12" was the default, not the live config). Only Liquid D
at p≥0.9 (or Lappi) can drop a posting.

| # | Finding | Status |
|---|---|---|
| F1 | ~~The domain title read needs AI/ML `unless` terms~~ — **refuted by the ledger**. Every live domain title-drop whose title has an AI/ML/Data term is a product, program, marketing, finance or design role, about 30 in all. Adding the terms would send them to paid triage. The real gap is in the staff_plus read: its `unless` spares "Senior/Staff" but not the spelled variants "Sr / Staff", "Staff/Senior" or "Senior / Staff" (3 senior-open ML roles dropped). Title drops by slot: role_fit 319, seniority 296. Model drops: role_fit 354, seniority 160. Workday listing drops are not stored, and title-drop precision still can't be refreshed (selection bias). | verified (ledger fold) |
| F2 | The Opus fallback pays for readings that can never drop a posting. `calibrated` admits only the OpenRouter model and Lappi. The live config has `"model": "claude-opus-5-5"` and no `triage_model`. | verified (`runner.go:500-515`, `.state/config.json:221`) |
| F3 | No location exclude is set live (there is no `exclude` key in `.state/config.json`). **Another session has uncommitted exclude-config work** (`internal/config/exclude.go`, `pipeline/exclude.go`, `cmd/vani/configexclude.go`). Coordinate with it; don't duplicate it. | verified |
| F4 | Notes (one Liquid D call) run for every survivor, with no relevance gate. `advance_per_run` 3000 is above the backlog, so only the dollar caps limit spend. | verified |
| F5 | Fail-open keeps (unreadable location, empty description) are not counted, so "could not check" looks the same as "passed". | verified |
| F6 | Drops have no recall measure. `eval` measures reading precision only, and there are 0 live labels. | verified |

## Target architecture until Lappi v2: a cost-ordered cascade

Every stage either annotates or orders. Only engine rules keyed on annotations drop (Rule 7). A
cheap stage decides whether to spend on a dear one, and no posting reaches a paid stage with a free
signal missing.

| Tier | Model | Job | Can drop? |
|---|---|---|---|
| 0 | Rules (title read/exclude, location exclude from the in-flight work) | Free pre-filter | Yes, as an engine rule |
| 1 | DevPrism | Coverage for every kept posting | No |
| 2 | EmbeddingGemma (Tessl) | Semantic score, near-dup, kb_match focus lines | No |
| 3 | Liquid D `d1-ultra` (OpenRouter) | Triage slots, only above a semantic+coverage floor | Yes, p≥0.9 |
| 3b | Liquid D `d3-mini` (local, free) | Reads the sub-floor pool fully, so nothing goes unread | No |
| 4 | Claude Haiku (`triage_model`) | Escalation when Liquid D p is low or Liquid D and d3-mini disagree | No (confidence only) |
| 5 | Claude Sonnet | kb_match for top-K, notes above a relevance gate | No |

### Model access today (verified 2026-10-20)

The decision model is **Liquid D**. "Jev" survives only as the name of the wire client
(`devtools/decide/jev`, the OpenRouter Decisions API protocol). Config keys moved off "jev" names
on 2026-10-19 (`internal/config/legacy.go`). Each model family reaches Vani by a different path:

| Model | Path into Vani | Live? |
|---|---|---|
| Liquid D `d1-ultra` | OpenRouter, through `decide/jev` (`decide.openrouter_model`) | yes, the primary |
| Liquid D local (`d1-3B` / `d3-mini`) | llama.cpp `/v1/systemone` (`decide.local`), shadow only | **no**: there is no `shadow`, `local` or `escalate` key in `.state/config.json`. Tier 3b needs this configured. |
| Lappi | local socket (`decide.lappi`) | no, not served |
| EmbeddingGemma 2 | Tessl in-process (`-tags tessl`) | yes |
| Claude | Manvi `llm` provider (`llm.model` Opus, `llm.triage_model` Haiku) | yes |
| Claude in Jarvis | not wired into Vani; it appears only in a pricing comment (`config.go:407`) | — |

Leftover "Jev" naming in Vani that users see:
- `config.Default()` still seeds `typesafe/jev-1.13` and Gemini (`config.go:526-530`).
- `kbmatch.backend` takes the value `"jev"` (`config.go:369`).
- The OpenRouter cap file is `budget/jev.json` (`wire.go:49`).
- Many comments.

`decide/jev` is the protocol client's name and should keep it.

How models agree:
- Use multi-model agreement as a **confidence modifier**.
- A drop rule may tighten to "two backends agree at p≥X". That is stricter than today and stays
  inside Rule 7.
- When backends disagree, the posting goes to `unsure` and is kept.
- Don't seek agreement on notes or kb_match: no decision rides on notes, and top-K selection makes
  kb_match agreement uninformative.

Cost levers. These magnitudes are estimates to measure, not promises:
- Opus → Haiku fallback is about 10–20× cheaper per read.
- A semantic floor before Liquid D saves roughly the share of postings below it. Pick the floor from the
  live distribution.
- A relevance gate on notes removes most of the per-survivor notes spend.

## Training: when and what

- **Now: nothing.** Any fit today would learn what the ranker already showed.
- **At ≥150 judged labels:** a pairwise-logistic fit over the Score parts plus fresh and gate, with
  an L2 prior centred on today's weights. Python is allowed only as the offline trainer and oracle;
  inference is Go reading JSON. Platt scaling per backend replaces the 0.70 constant.
- **Lappi v2 drop-in:** keep the `Slot` contract and the `Inputs` fields unchanged, and keep the
  `jdtriage/fixture.json` sha pin. Lappi becomes another router backend, scored by the same
  `Weights`.
- **TimesFM's only legitimate use** is forecasting posting volume per employer, once months of
  history exist. It is optional and low priority.

## Do now (no labels needed)

"Measure first" is about not tuning *weights* blind. It does not apply to these:

- **F2 — done 2026-10-20.** `llm.triage_model` is `claude-haiku-5-5` in `.state/config.json`
  (backup at `.state/config.json.bak-ranking-plan`), and `vani config check` passes. Manvi's table
  prices Haiku, so the daily cap meters it at Haiku rates. Escalation is off in the live config, so
  only the triage fallback changes. The installed app picks this up on its next run.
- **R1 — withdrawn** (see the table above).
- **F1 — replaced:** add the "Sr / Staff", "Staff/Senior", "Senior / Staff" spellings to the
  staff_plus `unless` list (the owner decides), then run `vani retriage --title-drops`.
- **Docs:** update the stale FOLLOWUPS coverage note (R2) and the "prefilter not yet enabled" line
  (FOLLOWUPS.md:121).

## Phases

1. **Measurement foundation (code). Do this first.**
   - Build:
     - An impression log: posting, surface, position, and the Score factors at the time.
     - 2–3 exploration slots per digest, drawn from below-the-fold postings and from **dropped**
       ones, and labelled as exploration.
     - A weekly audit command: 20 postings stratified by score decile and drop reason, judged in the
       `eval` label format.
     - Fail-open counters.
   - Exit: 7 days of impressions and the first audit batch judged.
2. **Surface consistency (code, small).**
   - Add "New" and "Top open" sections keyed on `Relevance` to the digest and the app.
   - Pass config to `advance` order.
   - Exit: the app and the digest show the same top 20, and the recency red-team case is either
     un-skipped or restated as policy.
3. **Cascade and spend (owner config + code). Starts after the exclude-config work lands.**
   - Set `triage_model` to Haiku.
   - Add the semantic+coverage floor before Liquid D, with d3-mini reading the sub-floor pool.
   - Gate notes on relevance.
   - Lower `advance_per_run`.
   - Exit: daily spend down ≥40%, with audit-sample precision unchanged.
4. **Calibration (needs ≥100 labels).**
   - Per-backend Platt scaling.
   - The agreement rule for drops.
   - AI/ML terms in the title `unless` list.
   - Store Workday listing drops.
   - Exit: drop recall measured on the exploration pool, and title-drop precision refreshed.
5. **Weight fit (needs ≥150 labels).** Exit: pairwise accuracy on held-out audit labels beats the
   hand weights, and the labelled set replaces the synthetic rankeval worlds.
6. **Lappi v2 swap** when it is served. Exit: shadow agreement with Liquid D within the `fitConfusion`
   band before promotion.

## Owner decisions

Decided 2026-10-20:
- **Fitting target:** both signals. Save/Hide marks give volume. The weekly audit's "would apply"
  label is ground truth for evaluation and breaks ties.
- **Do now:** triage fallback → Haiku, the digest comparator fix, the AI/ML `unless` terms, and
  Phase 1 measurement.
- **Recency:** split the surfaces into "New" (priority, recency-weighted) and "Top open" (age-free
  `Relevance`).
- **Plan home:** this file, linked from FOLLOWUPS.

Still open:

1. **What is the fitting target?** "Saved", or an explicit "would apply" audit label? Apply →
   interview is the outcome that matters, but it is sparse and lagged by weeks. This is the hardest
   open question: every later weight, floor and calibration optimises whichever proxy you pick.
2. How many exploration slots per digest, including dropped postings?
3. Recency: keep half-life 10 and floor 0.25 inside one score, or split the surfaces into "New" and
   "Top open"?
4. Which AI/ML terms go in the title `unless` list. Should Engineering Manager be kept?
5. The `triage_model` choice (Haiku?), and the semantic floor once you've seen the distribution.
6. Should thin evidence be shrunk harder (`MatchShrink` 0.10 → ~0.3)?
