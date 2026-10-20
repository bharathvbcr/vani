# Ranking and filtering: audit and plan (2026-10-20)

Sources: four read-only audits (ranking, filter funnel, labels, model research) and an independent
review. Claims are marked **verified** (re-checked in code or in `.state`), **auditor-read** (one
subagent read the code; not re-checked), **inferred**, or **unverified**. The live state is
`.state/state`. The Application Support state dir is a 1,286-record stub with no triage.

## Status (2026-10-20)

| Phase | State | Where |
|---|---|---|
| Do now: Haiku triage fallback | **done** (live config) | `.state/config.json` `llm.triage_model`, backup `.state/config.json.bak-ranking-plan` |
| Do now: Staff spellings | **done** (live config); 5 title drops returned to live by `vani retriage --title-drops` | `title_signals.read` staff_plus `unless` |
| 1 Measurement | **built** on branch `ranking-measure`. Exit (7 days of impressions, first audit batch judged) is owner-time. | `internal/feedback`, `vani audit`, digest Explore |
| 2 Surfaces | **built for the digest and advance order.** The Mac app's sections wait on the uncommitted JobsView/JobsModel work in the main checkout. | digest Top open, `advanceorder.go`, rankeval |
| 3 Cascade | **proposed session**, gated on the exclude-config work and the Liquid D rename landing | — |
| 4–5 Calibration, weight fit | **proposed session** (Go fitter and loader that refuse below 100/150 judgments) | — |
| 6 Lappi v2 | nothing to build until Lappi is served | — |

Fail-open counters (Phase 1) belong to the exclude-config feature, which owns the location and
description checks they count, so they moved to the Phase 3 session.

## Summary

- **Nothing measured ranking against the owner's judgement.** `rankeval` worlds are synthetic and use
  the same `quality × recency` form as `Score` (`rankeval/world_test.go`), so NDCG barely moves when
  the weights change (0.7341 → 0.7346). The real-ledger proxy scores against the model's own
  `role_fit` reading. (verified) Phase 1 adds the measurement.
- **There is no training signal yet.** The live state has 29 saved, 0 applied and 0 hidden. All 29
  saves came from one 14-minute session, picked from the top of the current ranking (mean semantic
  0.887 against 0.651 overall). Until Phase 1 nothing recorded what was shown, so an unmarked posting
  did not count as a negative. There are 3 days of history and no apply outcomes. (verified)
- **TimesFM and Google's TabFM are not the right tools.** TimesFM forecasts time series and has no
  ranking objective; 3.0's weights are non-commercial. TabFM 1.0 (Google, June 2026) does in-context
  classification and regression. Its weights are non-commercial, it needs a Python runtime, and it
  still needs the labels we lack. When labels exist, the right model is a regularised pairwise-
  logistic fit of the existing `Score` parts, in Go, shipped as a weights file. (research; licence
  texts not fully read)

## Ranking findings

`priority = 100 · match · gate · fresh · company` (`internal/jobinsight/score.go`).

| # | Finding | Status |
|---|---|---|
| R1 | ~~The digest sorting by rank-presence before Score is a bug~~ — **withdrawn**. It is deliberate: the digest heads unscored postings "Not ranked" so that a skipped DevPrism stage cannot pass for a ranking. Only 14 digested postings are affected. | verified |
| R2 | ~~The live top 20 lack coverage~~ — **stale**. A fold of the live ledger finds 2,287 of 2,301 digested postings with coverage and rank. | verified |
| R3 | One number answered two questions: *what is new* and *what is the best open posting*. An excellent 21-day-old posting scores 33.3, below a mediocre same-day one at 35.4. **Resolved:** the digest's "Top open" section ranks by age-free `Relevance`; the red-team case now asserts the excellent posting leads there. | done |
| R4 | Thin evidence reads as strong. Semantic alone at 0.9 gives match ≈0.79, against ≈0.855 with every part known at 0.9. `MatchShrink` is 0.10. | inferred; open (owner) |
| R5 | Confidence is asymmetric. A calibrated Liquid D p of 0.44 is trusted less than an LLM answer that carries no p (`AnsweredConf` 0.70). A p near 0 still counts as known. | verified; Phase 4 |
| R6 | `semantic` (0.35) and `kb_match` (0.15) both measure closeness to the KB. kb_match is asked only for top candidates, and its 0.15 weight was never measured. Live answers: 0. | verified; measure with audits |
| R7 | ~~`advance` order ignores `age_neutral_companies`~~ — **fixed**. `extension/src/relevance.ts` is still a separate heuristic with a hardcoded generic profile. | done / open |
| R8 | The default sort breaks ties on `Rank`, which counts coverage a second time. | verified; minor |

## Filter-funnel findings

Free stages run first: the title `read` lists, then the `exclude` block (not set live). Then the
paid router: Liquid D (`liquid/d1-ultra` on OpenRouter, $20/day cap), then the Claude fallback.
That fallback was Opus until 2026-10-20 and is Haiku now ($10/day cap). No shadow is configured
live. Only Liquid D at p≥0.9 (or Lappi) can drop a posting.

| # | Finding | Status |
|---|---|---|
| F1 | ~~The domain title read needs AI/ML `unless` terms~~ — **refuted by the ledger**. Every live domain title-drop with an AI/ML/Data term is a product, program, marketing, finance or design role. The real gap was the staff_plus read sparing "Senior/Staff" but not "Sr / Staff", "Staff/Senior" and similar. **Fixed** in the live config; 5 postings returned. Title drops by slot: role_fit 319, seniority 296; model drops: role_fit 354, seniority 160. | done |
| F2 | ~~The Opus fallback pays for readings that can never drop~~ — **fixed**: `llm.triage_model` is Haiku. | done |
| F3 | No location exclude is set live. The exclude-config work is in flight in the main checkout. | Phase 3 |
| F4 | Notes (one Liquid D call) run for every survivor, with no relevance gate. `advance_per_run` 3000 is above the backlog. | Phase 3 |
| F5 | Fail-open keeps (unreadable location, empty description) are not counted. | Phase 3 |
| F6 | Drops had no recall measure. **Now:** each digest shows one random dropped posting, and `vani audit` samples dropped postings by class, so `vani audit stats` reports false drops per class. | built |

## Target architecture until Lappi v2: a cost-ordered cascade

Every stage either annotates or orders. Only engine rules keyed on annotations drop (Rule 7). A
cheap stage decides whether to spend on a dear one, and no posting reaches a paid stage with a free
signal missing.

| Tier | Model | Job | Can drop? |
|---|---|---|---|
| 0 | Rules (title read/exclude, location exclude) | Free pre-filter | Yes, as an engine rule |
| 1 | DevPrism | Coverage for every kept posting | No |
| 2 | EmbeddingGemma (Tessl) | Semantic score, near-dup, kb_match focus lines | No |
| 3 | Liquid D `d1-ultra` (OpenRouter) | Triage slots, first for postings above a semantic+coverage floor | Yes, p≥0.9 |
| 3b | Liquid D local (`decide.local`) | Reads the below-floor pool for free when configured; otherwise a daily quota keeps it moving | No |
| 4 | Claude Haiku (`triage_model`) | Fallback and escalation when Liquid D p is low | No (confidence only) |
| 5 | Claude Sonnet | kb_match for top-K, notes above a relevance gate | No |

The floor defers; it never holds a posting forever (Rule 7).

### Model access today (verified 2026-10-20)

The decision model is **Liquid D**. "Jev" survives as the name of the wire client
(`devtools/decide/jev`, the OpenRouter Decisions API protocol). A separate session renames the
user-facing leftovers (`config.Default()`, `kbmatch.backend: "jev"`, `budget/jev.json`).

| Model | Path into Vani | Live? |
|---|---|---|
| Liquid D `d1-ultra` | OpenRouter, through `decide/jev` (`decide.openrouter_model`) | yes, the primary |
| Liquid D local (`d1-3B` / `d3-mini`) | llama.cpp `/v1/systemone` (`decide.local`), shadow only | **no** (no `shadow`, `local` or `escalate` key live) |
| Lappi | local socket (`decide.lappi`) | no, not served |
| EmbeddingGemma 2 | Tessl in-process (`-tags tessl`) | yes |
| Claude | Manvi `llm` provider (`llm.model` Opus, `llm.triage_model` Haiku) | yes |
| Claude in Jarvis | not wired into Vani | — |

How models agree:
- Use multi-model agreement as a **confidence modifier**.
- A drop rule may tighten to "two backends agree at p≥X". That is stricter than today and stays
  inside Rule 7.
- When backends disagree, the posting goes to `unsure` and is kept.
- Don't seek agreement on notes or kb_match.

## Measurement (Phase 1, built)

- **Impressions:** `<state>/feedback/impressions.jsonl`. Every role a written digest lists gets a
  line with its key, section, position, priority, relevance, the known Score parts and, for an
  Explore pick of a dropped role, its drop class. A line never holds a title, text or note. A failure
  to record is printed and kept in the run record as `impressions_error`.
- **Digest sections:**
  - **Top open:** `digest.top_open`, default 10. The best roles listed before and still open, by
    Relevance.
  - **Explore:** `digest.explore`, default `{"below_fold": 2, "dropped": 1}`. Picked by a hash of
    the date. The dropped pick shows why it was dropped.
  - Neither section changes a posting's status. `0` turns a section off.
- **Audit:** `vani audit next|run|judge|stats`.
  - The weekly batch (20) leads with the digest's unjudged Explore picks, then about 70% kept
    postings across priority deciles and 30% dropped postings across drop classes. It is stable
    within an ISO week.
  - Verdicts are yes, maybe, no or skip, kept in `<state>/feedback/judgments.jsonl` with the scores
    as judged.
  - `stats` reports would-apply by decile, false drops by class (unmeasured below 5 judged), and how
    many more judgments calibration (100) and the weight fit (150) need.
  - A drop class names the backend and slot only (`title:role_fit`), never the rule's value, which
    can reveal work authorization.
- **Cadence:** 5 minutes a day on the Explore picks, 15 minutes a week on `vani audit run`. That is
  about 150 judgments a month, so the weight fit can run around late November 2026.

## Training: when and what

- **Now: nothing.** Any fit today would learn what the ranker already showed.
- **At ≥100 usable judgments:** per-backend Platt scaling replaces `AnsweredConf` and raw p.
- **At ≥150:** a pairwise-logistic fit over the Score parts, with an L2 prior centred on today's
  weights, in Go. Both refuse below threshold with a "could not run" result.
- **Lappi v2 drop-in:** keep the `Slot` contract, the `Inputs` fields and the
  `jdtriage/fixture.json` sha pin. Lappi becomes another router backend.
- **TimesFM's only legitimate use** is forecasting posting volume per employer, once months of
  history exist. Optional and low priority.

## Owner decisions

Decided 2026-10-20:
- **Fitting target:** both signals. Save/Hide marks give volume; the audit's would-apply label is
  ground truth and breaks ties.
- **Recency:** split into "New" (priority, recency-weighted) and "Top open" (age-free Relevance).
- **Exploration:** 2 below-fold + 1 dropped a day, and Top open 10. These are the recommended
  defaults, since the question went unanswered; change them under `digest` in the config.
- **Audit surface:** CLI first (`vani audit`); the Mac app comes after its Jobs work lands.
- **Triage fallback:** Haiku.
- **Title rules:** no AI/ML `unless` terms (refuted by data); Staff spellings added.

Still open:
1. Should thin evidence be shrunk harder (`MatchShrink` 0.10 → ~0.3)? Measure with the audit first.
2. Should Engineering Manager roles be kept (TRACKS.md: remove `manager` from the drop rule)?
3. The semantic floor before Liquid D, once the Phase 3 session shows the live distribution.
