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
| 6 Lappi v2 | **Vani side built and tested; the model is being trained in Lappi-decision.** The swap is in Vani already: Lappi runs as the shadow, `vani eval --window` gives the paired per-slot verdict, and `decide.lappi.primary` / `primary_slots` promotes a slot so Lappi answers it first, with no fall back to d1-ultra and an outage breaker. Tests passed in the full run at 88e88be (2026-11-13): `cmd/vani/wire_test.go` `TestLappiShadowThenPrimary`, `cmd/vani/primaryslots_test.go`, `primaryslots_outage_test.go`, `primaryslots_attack_test.go`, `primaryslots_shadow_record_test.go`, `internal/eval` `TestPromotionVerdicts`, `TestSlotPromotableInBothWindowsIsPromotable`. The promotion stays a config edit made after `vani eval --window` says promotable: a promoted slot gets no new shadow row (`TestAttackPromotionReportHasNoSelfPairedRowForPromotedSlot`), so a standing check would turn against a legitimate promotion; a check at the moment of promotion is an owner decision, not built. **Unblock chain, in Lappi-decision** ("Lappi V2 job model training plan" lane, `HANDOFF/job-model-j3-2026-11-12.md`): J3 is done (pool of 4,703 train lines, decidability report; role_fit strong_fit is undecidable everywhere); J4 (features, head arms) waits on owner question q1 (unredacted text for head arms, default no) and q2 (train the single-reader pools, default not trained); then train, calibrate and serve; then Vani runs it as the shadow until windows close with a promotable verdict; then the owner sets `primary_slots`. Checked 2026-11-13, read-only. | `cmd/vani/wire.go`, `internal/eval/windows.go`, `internal/config/config.go` |
| Adaptive stage 0 (2026-11-12) | **done in code.** `vani relevance` sets every reader (engine backends, teacher models) against the owner's would_apply, and each Score part's AUC. On the live 32 labels (3 yes, 8 maybe, 21 no), no reader called any wanted posting strong_fit or partial_fit: d1-ultra 0/10, Haiku 0/10, Sonnet 0/9. The pool-wide rate of those readings is low, though: about 6% of the ledger's role_fit events and 4% of teacher rows (counted 2026-11-12). So 0/10 is near what chance gives. It says the role_fit question rarely answers "fit" at all, which points at its rubric and `target_summary` before the models' taste. Either way, no model reading may stand in for a label yet. Part AUC, read only where a part was known: title alone orders the owner's taste in the score deciles (0.67, 0.56-0.83); fit, coverage, semantic and keyword are unclear at this n, and kb_match and employer were never known on a judged posting. The plan and its later stages are `build/fable-plan-2026-11-12-ranking.md` (not tracked). | `internal/eval/readeragree.go`, `internal/rankfit/partauc.go` |
| Adaptive stage 1 (2026-11-12) | **done in code.** The reader table and part AUC repeat under four label readings (yes or maybe, yes only, maybes left out, engineering titles only). Live: with the maybes left out, match x gate orders the 3 yes postings above the no's (AUC 0.94-1.00); among engineering titles only, fit reads backwards (0.19, 0.00-0.46) and title forwards (0.75). The 8 maybes are the noise, and 5 of them are non-engineering titles. | `internal/eval/labelmode.go`, `cmd/vani/sensitivity.go` |
| Adaptive stage 3 (2026-11-12) | **built, off by default.** `Weights.SpareSeniorityMismatch` (`vani rankfit --spare-seniority`) lifts the gate and the fit penalty of a mismatch_seniority reading at listed seniority levels. The rubric's "early-career" anchor is in the sha-pinned fixture, so it is relaxed here rather than in the prompt. Shadow on the live ledger: sparing mid and senior moves no judged posting and 345 open ones; adding staff_plus moves one judged posting (84th percentile before and after) and 373 open ones; no AUC changes. Promotion is the owner's. | `internal/jobinsight/blend.go`, `cmd/vani/seniorityshadow.go` |
| Adaptive stage 4 (2026-11-12) | **built; the draft summary measured.** `vani relevance --what-if-summary FILE` swaps the keyword and semantic parts only (semantic rescored from stored vectors in a temporary copy; the 31 judged postings without vectors embedded there locally; null test against the live scores: 7,793 compared, 0 apart). Live, with the draft in `build/target-summary-draft-2026-11-12.txt`: no AUC moves in any label reading (0.72 / 0.90 / 0.63, the what-if's own base arm with the judged postings embedded and the semantic part compared only where known in both, not the live ranking's AUC); the three yes postings move at most one percentile point (2 of 3 had a known semantic part). The draft adds the terms SRE and security engineering, and the two no's that rise most are an SRE role (36 to 39) and a security role (71 to 73). The triage readings, the summary's main lever, are not measured here. | `cmd/vani/summarywhatif.go` |
| Adaptive stage 7 (2026-11-12) | **built, spends nothing until the owner sets its cap.** The pairwise would_apply auditor: `vani teacher --task would_apply_pair` asks which of two postings of one audit stratum the owner would rather apply to, given a frozen set of the owner's labelled titles (`vani teacher freeze`). Silver labels in `silver_pairs.jsonl`, never in `labels.jsonl`; no ranking or drop changes. `vani teacher report` gives its AUC on gold labels made after the freeze beside the title part's: go at ≥10 yes and ≥30 no, lower bound above 0.5 and point AUC above the title part's, else "insufficient" or no-go. | `internal/decide/wouldapplypair`, `internal/eval` (`pairs.go`, `pairplan.go`, `pairscore.go`, `exemplars.go`, `pairscap.go`), `internal/rankfit/silver.go`, `docs/RUNTIME.md` |
| Adaptive stage 2 (2026-11-12; reworked 2026-11-13) | **folded into main's `--policy-drops`; not run on live.** The branch's `--stale-drops` report found, on the live ledger (2026-11-12): 33,928 dropped, 4,025 stale (3,996 title drops and 29 model drops, all `seniority=staff_plus` at p ≥ 0.9 under a rule removed 2026-11-12, the two Databricks maybes among them), 3 not judged. When main gained `retriage --policy-drops` (the pending reopen: a watched board's drop waits for its next listing), the report and its `--rescue` were retired: the rescue wrote unsure at once, past that gate, and judged drops with a second copy of the policy check. `--policy-drops --dry-run` now counts what it would let back by drop class. Running it is the owner's call. | `cmd/vani/retriagecmd.go` (`classes`), main's `internal/pipeline/retriage.go` |
| Adaptive stage 8 (2026-11-12) | **built, off until the owner runs it; waits on silver answers.** The rescue rule D6 (`vani retriage --silver-drops`): model drops the auditor keeps picking (score, comparisons, no neither) go to unsure within a daily cap, only while the auditor's verdict on gold is go; a watched board's drop waits as a pending `silver` reopen for its board's next listing, as every reopen of a closed posting does; title, exclusion and work authorization or work_mode drops never. The silver pretrain (`vani rankfit --part weights --silver-prior`): the auditor's answers fit a prior the gold fit is pulled toward (`Options.Prior`); answers about owner-judged postings are dropped; only post-freeze judgments are held out (`Options.TrainOnly`); `Beats` still decides. Neither can run on live until the auditor has a cap, a freeze and answers. | `internal/pipeline/silverrescue.go`, `internal/rankfit/silverprior.go`, `cmd/vani/silverrescue.go`, `cmd/vani/rankfitsilver.go` |
| Adaptive stage 6 (2026-11-13) | **measured; rejected.** Would Ojas e-means clusters of posting embeddings make a better audit stratum than the 8 job categories? Bar (Fable plan, decision 5): a 25% cut in within-stratum variance of Sonnet's role_fit at the category's cell count. On a copy of live state (fit on 8,420 pooled postings; variance measured on the 2,805 with a Sonnet reading and a category): e-means at k=8 cut 18.6% (5 seeds, range 12.2–19.2%; permutation null about ±0.4%), Lloyd 13.7%; at k≥13 Lloyd matched or beat e-means. Clusters are not adopted; the study stays as a rerunnable tool. The fit binary still needs an ojas branch from an ojas-rooted session. | `docs/CLUSTER_STUDY.md`, `internal/clusterstudy`, `internal/semantic/vectorexport.go`, `cmd/vani/clusterstudycmd.go`, `scripts/cluster-study/vani_cluster.rs` |
| Adaptive stage 5 (2026-11-13) | **built; on by default since 2026-11-13 (owner); not run on live.** One-tap labels: `vani audit answer --reason` records what kept a maybe or a no from a yes (level, domain, location, employer_type, other). Disagreement cells (Fable decision 3): `vani audit --disagree N [--teacher M]` adds N picks where the teacher's (default Sonnet) stored role_fit disagrees with the engine: wanted but dropped or in score deciles 1–3, or rejected in decile 10. The cells are an overlay: the random draw (picks and p_select) is the same with or without them, at least a third of the draw stays random, and a teacher log that cannot be read is an error. Postings in strata the auditor leaves out (work authorization, work mode, owner exclusions) and closed ones are never read. Their labels are kept out of every estimate (relevance counts, reader table, drop agreement, auditor gates, the weight fit, the shadow and what-if); drop recall counts each as a known outcome, as a mark. Yes-rich strata: `vani relevance` gains a raw yes-yield table per stratum and cell, naming a row only when its Wilson lower bound clears the random strata's upper bound. Fable's second review (`build/fable-review-2026-11-13-second.md`) kept the overlay, recall-as-mark, the Sonnet default and cell exemplars, and changed four things, all built: each cell has its own quota (`--disagree` for "wants", `--disagree-rejects` for "rejects", 0 by default at the time, no lending); the 2x cap counts scored and dropped places only, not closed; the yes yield gains group rows (all deciles, top three deciles, all drop classes) and the kill rule (at 20 judged a cell with 0-1 yes is turned off, 4+ kept, 2-3 runs to 40; reworked 2026-11-14, see the audit benchmark row); the freeze counts exemplars from the cells. The Mac app's Audit tab has a "Disagreement picks" stepper that sends `--disagree`. Measured on a copy of live state (2026-11-13, seed 2026-W46): the wants cell holds 46 postings under Sonnet and 50 under Haiku, the rejects cell 209 and 288. Not run on live. On by default (owner, 2026-11-13): `vani audit` draws 5 teacher-wants picks (Fable's suggested start) and 2 teacher-rejects picks unless told otherwise; a default cell with no teacher readings, or one the kill rule has turned off, is skipped and named rather than an error (an explicit quota still fails closed); a default over the 2x cap is trimmed with a note (`cmd/vani/auditdefaults_test.go`). The Mac app's Audit card asks for the reason after a Maybe or a No (keys 1-5, Return for none, Escape to take the verdict back). | `internal/eval/audit.go`, `internal/eval/yesyield.go`, `internal/eval/droprecall.go`, `cmd/vani/auditcmd.go`, `cmd/vani/relevancecmd.go` |
| Second enhancement review, items 3-5 (2026-11-13) | **built; on by default since 2026-11-13 (owner): 3 yes-neighbour and 3 engineering picks a week, the neighbour cell skipped and named until the taste column passes and a vector store exists; not run on live.** Decided in Fable's third pass (appended to `build/fable-review-2026-11-13-second.md`). *Taste column:* `vani relevance` sets each judged posting's highest cosine to another yes posting (leave-one-out, stored pooled vectors, nothing embedded) against the title part's AUC on the same postings; insufficient below 5 yes. *Yes-neighbour cell:* `vani audit --yes-neighbours N` draws uniformly from the 50 unlabelled postings nearest the owner's yes answers made before the seed's week, filled round-robin by anchor, without reposts of labelled postings or postings the overlays may not draw; refused unless the taste column beats the title part; an unreadable vector store is an error. Its labels are cell labels (SelectedOnReading) and it is under the kill rule. *Engineering group:* `vani audit --engineering N` draws at random from engineering-titled postings (`eval.TitleIsEngineering`, the title rule `vani relevance` scopes with, not the jobcategory classifier), kept and dropped, as an overlay drawn after the random strata and before the cells. Its labels count in the auditor's evaluation (`vani auditor report` and the pairwise report print how many came from it) and the yes yield, and are kept out of the relevance strata, drop recall, the reader table and the weight fit (counted, until the fit's weights are post-stratified, enhancement 4). The 2x cap counts the neighbour cell with the disagreement cells; engineering places count on neither side. *Doctor silver log:* each drops-part verdict goes to `silver_doctor.jsonl` (no free text, idempotent on run and key, no rows from left-out strata), compared with owner answers in `vani relevance` per prompt hash, "insufficient" below 10 yes and 30 no. Never a label, a mark or a known outcome. The Mac app's Audit tab has "Near your yeses" and "Engineering picks" steppers beside "Disagreement picks" (vani's defaults until one is set, a set 0 kept as off; the first two share vani's cap; the app sends `--soft-overlays` and lists what was not drawn). The Jobs tab's "Would you apply?" row asks for the reason after a Maybe or a No. | `internal/rankfit/taste.go`, `internal/rankfit/neighbours.go`, `cmd/vani/neighbours.go`, `internal/eval/audit.go`, `internal/doctor/silver.go`, `cmd/vani/doctorsilver.go` |
| Audit benchmark (2026-11-14) | **measured on a copy of live state; four fixes built (Fable's decisions); not run on live.** Labels: 32 (3 yes, 8 maybe, 21 no), unchanged since 2026-11-11, all from random strata. *Real labels:* on the copy as it stood, the live ranking's AUC was 0.63 yes-or-maybe vs no (exact one-sided p 0.13), 0.92 yes vs no (p 0.008). A warm by the old binary on a fresh copy (refreshing the stale scores, embedding nothing new) leaves it at 0.63 (p 0.125); with the judged postings embedded as well it reads 0.72 (p 0.020) and 0.90 (p 0.011), because 13 judged postings, not 8, now carry a semantic part most of the pool lacks. That is a change in what is measured, not evidence the ranking improved. Read with care (Fable): at most 12 of the 21 no's are scored postings and 17 labels are drops, so the high yes-vs-no AUC mostly restates the gate (drops hold 0 yes of 17); ordering within the scored postings is unmeasured (within the deciles no part clears chance: title 0.67, p 0.19). Engineering-titled judged postings: 3 yes of 10 against 0 of 22 (Fisher one-sided p 0.024), post hoc and confounded with being kept. Teachers read 0 of 9 wanted postings as a fit, so the wants cell is expected to be turned off at its first look. *Draw:* 12 seeds on the copy, default against every overlay at 0: 0 invariant failures (random picks and p_select identical, random share at least 0.63); 17 random + 5 wants + 2 rejects + 3 engineering a week, the neighbour cell skipped and named every time. *Gates:* overlay labels do not count toward the weight fit's 150 (cell and engineering labels are kept out), so it is 8 weeks away at 15 answered random cards a week with or without them; the pairwise auditor's gate (10 yes, 30 no after the freeze) is 7 weeks at the median (4-16) on random cards, and between 4 and 6 with the engineering group (plan on 7, Fable). *Fixes:* (1) the kill rule (above) is reworked: first look at 20 answers (0-1 yes turns the cell off; no early keep), second at 40 (keep with 5+ yes and at least 2x the comparator's rate, comparator frozen at the 40th answer; random strata for the wants cell, the deciles for the neighbour cell; no comparator answers cannot decide); the old rule kept a cell equal to random about 39% of the time, the new one 8.0% at a 9% base rate and keeps a cell three times as good 85.3% (`internal/eval/killrule_oc_test.go`, exact sums); the rejects cell leaves the yes rule and is the owner's call at 20 answers, its no rate beside the top three deciles'; a cell drawing under its quota is named (its pool is running out). (2) The daily run and `vani semantic warm` embed the owner's judged postings without a vector (`cmd/vani/labelvectors.go`; at most 200 a run). It runs there rather than at the start of each audit draw, as Fable suggested, because the Mac app gives a draw 30 seconds and a draw already takes about 18 on live-sized state; labels answered this week are embedded by the next daily run, before the next week's anchors are taken. Each run writes the judged postings' text hashes to `semantic/labelled-texts.json`, and every warm that names a live set for the vector log (the daily run's, the one before the advance phase, the idle warm) ranks them above any Score (`pinLabelled`), so no compaction evicts them first: those live sets hold browsable postings only, which leaves out a judged posting that was dropped. On the copy all 31 were embedded and the taste column reads 32 of 32 (still insufficient: 3 yes, needs 5). The taste column says why a judged posting has no vector, and evicted descriptions are read back before a lookup (`cmd/vani/postingvectors.go`). (3) `vani relevance` prints the exact p beside each part AUC (known only) and each seniority-shadow AUC (`rankfit.AUCPValue`, normal approximation over 100 pooled values). (4) Not built, by decision: engineering labels join the weight fit only with enhancement 4's post-stratification, weight `1 / (n_s/N_s + eng_i * n_e/N_e)` over every engineering-titled label with the Kish effective n printed beside the 150 (gain about a week and a half). The benchmark program (exact nulls, the simulation, the 12-seed draw check) is kept in `build/audit-bench/`, untracked. | `internal/eval/yesyield.go`, `internal/eval/killrule_oc_test.go`, `cmd/vani/auditcmd.go` (`shortCells`), `cmd/vani/labelvectors.go`, `cmd/vani/postingvectors.go`, `cmd/vani/taste.go`, `internal/rankfit/partauc.go` (`AUCPValue`), `cmd/vani/seniorityshadow.go` |
| Round-2 gaps (2026-11-14) | **measured on copies of live state; three reports built (Fable's decisions); the vector-log cap is held; not run on live.** These pool-wide figures over all 30,923 open postings supersede the earlier top-5000 ones and that review's coverage 47 / title 24 / fit 22 / keyword 7 split. *Part coverage* (`vani relevance`, weight x confidence over the open pool): on the copy as it stood, semantic was known on 0 postings, because all 20,000 cached scores were for an earlier knowledge-base profile. Of the carried share, coverage decided 51%, title 30%, fit 11% and keyword 9%. One warm by the old binary (117 s: 8,287 rescored from stored vectors, 1,000 embedded) brought semantic to 9,060 known (29%), and the split became coverage 39%, semantic 23%, title 23%, fit 8%, keyword 7%. Fit is read on 98.4% of open postings but 23,797 of those readings are confident mismatches, which the gate takes (77% of the open pool); the rest average confidence 0.58. Employer and kb_match are known nowhere. The displayed relevance rests on a median 0.32 of the additive weight (10th percentile 0.13). The 512 MiB vector log is at its bound: the warm left 6,673 postings after compacting twice. *Weight shadow:* only 11 judged postings are still open (the rest of the 32 are drops), so all four variants (fit 0, coverage 0, a rebalanced split, match shrink 0.10) read within noise (paired intervals up to -0.32 to +0.43). The rule that would refute a change is printed with the report: better only with every computable mode's paired interval above 0 at a family-wise 0.05, never under 150 judged open postings; picks drawn on a reading or a title are left out. It measures the ranked score less freshness, because the displayed Relevance keeps a fixed shrink (0.10) that a MatchShrink variant cannot move. *Semantic health:* `vani status` and `vani semantic status` print how many open postings carry a usable similarity (ok at 80%, warn at 50%, bad below) and the cause: a profile change with the vectors a warm can rescore from, then the warm budget. Its count matches the coverage table's (9,060 of 30,923 on the warmed copy). It costs `vani status` about 0.3-0.8 s and is not in the polled `status --json`. *Audit:* `audit --json` reports each phase's wall time. On the copy the 12.2 s draw was ledger 3.8, candidates 4.5, overlays 3.4 and draw 0.4. The Mac app's 30-second audit timeout was never used, since the draw ran on the jobs client's 30 s; a draw now gets at least 60 s. *Drop recall at the current n* was already printed (one-sided 95% bound per class and the labels a 5% bound needs: 45% to 100% now, 54 to 59 more labels each), so nothing was built for it. *Vector log (held):* an int8 log failed the parity bar against the 7,604 stored postings, so the log stays f32. The real-data test measured max score difference 0.055 against a bar of 0.005, Kendall tau 0.993 against 0.995, and the top evidence chunk unchanged for 91% against 99%. Raising the cap to 2.5 GiB (about 38,000 postings), with the score-cache read bound at 256 MiB, landed from branch `rank/vector-cap` in commit `4451350`: a binary with the older 512 MiB cap compacts a larger log to 448 MiB on open (`needsCompaction`), so the Mac app's bundled `vani` should stay rebuilt at `>= 4451350`. | `cmd/vani/weightshadow.go`, `internal/rankfit/paired.go`, `cmd/vani/semantichealth.go`, `cmd/vani/auditcmd.go` (`phaseClock`), `macos/Sources/VaniApplyKit/Audit.swift` (`drawTimeout`) |

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
| F6 | Drops had no recall measure. Now: the dropped stratum in the label queue and the weekly `vani audit`, `vani relevance` (precision with Wilson intervals, drop-candidate agreement per shadow), `decide.shadow_mode: drop_candidates`. `vani relevance` also estimates recall: the share of wanted (and of yes-only) postings dropped, overall and per class, post-stratified by the cell each posting sits in now (kept, or a drop class): the cell's rate among its labels times its pool today, Jeffreys intervals combined by seeded draws. It does not weight by p_select, which is relative to the week's unlabelled pool and falls as the ledger grows (decile labels 0.0017 on 10-22, 0.00019 on 11-10); `rankfit`'s 1/p_select weights carry that skew and are a follow-up. It says "insufficient" until every cell has 3 judged labels, treats a cell with none as unknown, and gives each drop class's exact 95% upper bound and the labels a 5% bound needs. Exclusion drops are their own class (`exclude:<field>`), dead and duplicate postings their own audit strata (`--closed`, 2 a week, kept out of `rankfit`), and a teacher proxy counts dropped postings the llm tier reads as a fit. | built; waiting on labels |

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
(`decide/jev`, the OpenRouter Decisions API protocol) and in two file/value names kept on
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
[`RUNTIME.md#cloud-shadow`](RUNTIME.md#cloud-shadow).

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
  held-out accuracy, because the audit draws 10 dropped for 5 scored. A `p_select` outside [1e-9, 1] on
  a sampled label is refused. Held-out accuracy is also printed per stratum (scored, dropped, mixed),
  and when the held-out set holds sampled postings, at least `MinScoredStratumPairs` (50) pairs must
  be between two scored-stratum postings: the ranking orders scored postings, so a held-out set of
  mostly dropped ones cannot carry the claim. Marks, impressions and manual labels carry no
  inclusion probability and weigh 1.
- The scored-stratum floor (`MinScoredStratumPairs` = 50) needs at least 13 scored held-out items
  (13 items make 78 pairs at most; 10 make 45), which in practice means roughly 200-250 labels, so
  the first fit at 150 labels will often be refused by design (fail-closed). A `p_select` under
  `eval.MinInclusionProbability` (1e-9) is refused too: its weight would overflow.
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
