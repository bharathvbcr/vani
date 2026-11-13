package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Manvi/manvi/prompt"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/decide/wouldapply"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/rankfit"
	"github.com/bharathvbcr/decide"
)

// The pairwise would_apply auditor, `vani teacher --task would_apply_pair`, and
// its three companions: `teacher freeze` (write the frozen exemplar set from the
// owner's labels), `teacher cap` (the auditor's own daily cap, which refuses
// until set) and `teacher report` (the auditor's AUC on the owner's later gold
// labels, beside the title part's).
//
// Its answers are silver labels in their own log (silver_pairs.jsonl). Nothing
// here writes an owner label, changes a ranking or drops a posting. What is
// sent: the pair prompt, the target summary, the frozen exemplar titles (job
// titles and companies the owner labelled) and two postings' title, location
// and description, all framed as untrusted content (internal/decide/wouldapply).
// No vault, header, mark, note or contact detail ever enters a prompt or a row.

const (
	// defaultTeacherPairs is --pairs when not given.
	defaultTeacherPairs = 50
	// pairCoverMates is how many stratum-mates each gold post-freeze posting is
	// compared with, so it has a score to evaluate.
	pairCoverMates = 3
)

// checkTeacherPairFlags refuses a flag that does not mean anything for the task
// asked, rather than ignoring it: a flag that quietly does nothing is how a run
// ends up not doing what its command line says.
func checkTeacherPairFlags(task string, given map[string]bool) error {
	if task == teacherTaskPair {
		for _, f := range []string{"of", "with-readings-of", "draw-out", "keys", "sample", "max"} {
			if given[f] {
				return usagef("teacher: --%s does not apply to --task %s, which draws pairs and not postings (--pairs bounds it)", f, teacherTaskPair)
			}
		}
		return nil
	}
	for _, f := range []string{"pairs", "both-orders"} {
		if given[f] {
			return usagef("teacher: --%s belongs to --task %s", f, teacherTaskPair)
		}
	}
	return nil
}

// silverPaths are the auditor's files: the frozen exemplars, the pair log and
// the cap.
func (e *env) silverPaths() (exemplars, pairs, capFile string) {
	return filepath.Join(e.stateDir, eval.ExemplarsFile), filepath.Join(e.stateDir, eval.PairsFile),
		filepath.Join(e.stateDir, "budget", eval.PairCapFile)
}

// labelsDir is where the owner's labels live by default.
func (e *env) labelsDir() string { return filepath.Join(e.stateDir, "eval", "labels") }

// teacherPairsJSON is what a would_apply_pair run says beyond the shared plan.
type teacherPairsJSON struct {
	// Frozen is true when the exemplar set was read from its frozen file;
	// Preview is true when none exists and a dry run priced the set a freeze would
	// write now (a real run refuses until one is frozen). Neither is set when
	// there were too few labels even for a preview.
	Frozen         bool   `json:"frozen"`
	Preview        bool   `json:"preview_not_frozen,omitempty"`
	ExemplarSHA256 string `json:"exemplar_sha256,omitempty"`
	FrozenAt       string `json:"frozen_at,omitempty"`
	Yes            int    `json:"exemplars_yes"`
	No             int    `json:"exemplars_no"`
	Seed           string `json:"seed"`
	BothOrders     bool   `json:"both_orders"`
	Pairs          int    `json:"pairs"`
	Questions      int    `json:"questions"`
	AlreadyAnswer  int    `json:"pairs_already_answered"`
	Strata         int    `json:"strata"`
	// Cover counts the gold post-freeze postings the plan was asked to compare;
	// CoverShort those that did not get their mates.
	Cover      int `json:"cover_postings"`
	CoverShort int `json:"cover_short"`
	// What was left out of the candidates, and why.
	NoText   int `json:"left_out_no_text"`
	Closed   int `json:"left_out_closed"`
	Excluded int `json:"left_out_sponsorship_work_mode_exclusion"`
	// StrataMembers is the candidate postings in each stratum a pair can be drawn
	// from (the strata left out are counted above, never named).
	StrataMembers map[string]int `json:"strata_candidates"`
	Exemplar      int            `json:"left_out_exemplars"`
	PromptHash    string         `json:"prompt_hash,omitempty"`
	FixtureSHA    string         `json:"fixture_sha"`
	CapUSD        float64        `json:"auditor_cap_usd"`
	SpentToday    float64        `json:"auditor_spent_today_usd"`
	CapIsNotSet   bool           `json:"auditor_cap_not_set,omitempty"`
}

// stratumLeftOut says why the auditor does not draw from a stratum. Closed
// postings measure liveness and the duplicate fold, not taste. The 2026-11-12
// plan (build/fable-plan-2026-11-12-ranking.md) leaves out sponsorship and
// work_mode drops; the sponsorship test is the doctor's (sponsorshipSlot, every
// slot or class about work authorization: sponsor, visa, citizenship, clearance
// and the like), so the two never disagree about what is about authorization.
// Owner exclusions are left out for the doctor's reason too: what one matched can
// say what the owner needs.
func stratumLeftOut(name string) (reason string, left bool) {
	switch {
	case strings.HasPrefix(name, eval.ClosedPrefix):
		return "closed", true
	case strings.HasPrefix(name, eval.DroppedPrefix+"exclude:"):
		return "owner_exclusion", true
	case strings.HasPrefix(name, eval.DroppedPrefix) && (sponsorshipSlot(name) || strings.HasSuffix(name, ":work_mode")):
		return "sponsorship_work_mode", true
	}
	return "", false
}

// withholdFromExemplars is eval.FreezeExemplars' withhold function: a label drawn
// from a stratum the auditor never sends (stratumLeftOut, the closed strata
// aside, which the freeze leaves out itself) is not shown to the model as an
// example of what the owner would not apply to.
func withholdFromExemplars(source string) bool {
	reason, left := stratumLeftOut(source)
	return left && reason != "closed"
}

// elided is p with everything the posting wrote replaced by a note, for the one
// question a dry run shows.
func elided(p posting.Posting) posting.Posting {
	return posting.Posting{Key: p.Key, Title: "[title elided]", Location: "[location elided]",
		Description: fmt.Sprintf("[%d bytes of description elided]", len(p.Description))}
}

// boundQuestion is a question whose context is as large as one may be, for the
// worst-case price of a run that has no exemplar set to render.
func boundQuestion() decide.Question {
	return decide.Question{
		Task: wouldapply.Task, Prompt: wouldapply.Prompt,
		Context: bytes.Repeat([]byte("x"), jdtriage.MaxContextBytes),
		Slots:   []decide.Slot{{Name: wouldapply.Slot, Options: append([]string(nil), wouldapply.Options[:]...)}},
	}
}

// pairExemplars is the exemplar set a run asks under. A real run needs the frozen
// file. A dry run without one prices the set a freeze would write now (preview)
// or, with too few labels for that, nothing (ok is false) and bounds the cost
// from the largest question instead; it says which, and never writes a file.
func (e *env) pairExemplars(dry bool, labels map[posting.Key]eval.Entry, recs []ledger.Record) (set eval.ExemplarSet, frozen, preview bool, err error) {
	path, _, _ := e.silverPaths()
	set, err = eval.ReadExemplars(path)
	switch {
	case err == nil:
		return set, true, false, nil
	case !errors.Is(err, eval.ErrNotFrozen):
		return eval.ExemplarSet{}, false, false, fmt.Errorf("teacher: %w", err)
	case !dry:
		return eval.ExemplarSet{}, false, false, fmt.Errorf("teacher: %w; the auditor reads only a frozen set, so a freeze fixes which labels count as evaluation data", err)
	}
	set, _, ferr := eval.FreezeExemplars(labels, recs, now(), withholdFromExemplars)
	if ferr != nil {
		return eval.ExemplarSet{}, false, false, nil
	}
	return set, false, true, nil
}

func (e *env) teacherPairs(o teacherOpts, jr *teacherReply) error {
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	model := o.model
	if model == "" {
		model = cfg.LLM.EffectiveTriageModel()
	}
	if cfg.LLM.Provider == "" || model == "" {
		return errors.New("teacher: no llm provider and model are configured")
	}
	summary, err := jdtriage.NormalizeSummary(cfg.TargetSummary)
	if err != nil {
		return err
	}
	prices, source, err := teacherPrices(cfg, model)
	if err != nil {
		return fmt.Errorf("teacher: %w", err)
	}
	ctx, cancel := signalContext()
	defer cancel()

	// The auditor's own cap comes first and refuses by default: a run with no cap
	// set, an unreadable cap file or a spent day stops here, before the labels,
	// the ledger, the provider or the key are touched. A dry run only reports it.
	_, pairsPath, capPath := e.silverPaths()
	w := &wired{cfg: cfg, stateDir: e.stateDir, warn: e.stderr}
	_ = w.budgetLedgerPath() // fixes w.day, the one UTC day this run's cap and ledger share
	day := w.day
	capFile := eval.OpenPairCap(capPath)
	capNano, spentNano, capErr := capFile.State(day)
	// refuseCap is the real run's cap check; it runs again once the run lock is
	// held, because the cap and its spend were read before a wait for another run.
	refuseCap := func() error {
		switch {
		case capErr != nil:
			return fmt.Errorf("teacher: %w", capErr)
		case capNano <= 0:
			return fmt.Errorf("teacher: %w", eval.ErrNoPairCap)
		case capNano-spentNano <= 0:
			return fmt.Errorf("teacher: today's would_apply auditor cap (%s) is already spent; nothing was asked", usd(capNano))
		}
		return nil
	}
	if !o.dry {
		if err := refuseCap(); err != nil {
			return err
		}
	}

	labels, bad, err := eval.LabelsAt(e.labelsDir())
	if err != nil {
		return fmt.Errorf("teacher: labels unreadable, refusing to plan (the evaluation set would be wrong): %w", err)
	}
	if bad > 0 {
		fmt.Fprintf(e.stderr, "vani: %d malformed label lines skipped\n", bad)
	}
	// A dry run writes nothing, so its jobs read keeps no ledger checkpoint (the
	// cache every other jobs read refreshes).
	e.noCheckpoint = o.dry
	b, err := e.browse()
	if err != nil {
		return err
	}
	// The posting text comes from a full ledger read: the jobs read leaves out the
	// text of dropped and closed postings, which the drop-class strata need.
	recs, err := e.ledgerRecords()
	if err != nil {
		return err
	}
	set, frozen, preview, err := e.pairExemplars(o.dry, labels, recs)
	if err != nil {
		return err
	}
	haveSet := frozen || preview
	ex := set.Exemplars()
	byKey := make(map[posting.Key]posting.Posting, len(recs))
	for _, r := range recs {
		p := r.Posting
		p.Key = r.Key
		byKey[r.Key] = p
	}

	// Candidates: every posting of a stratum the auditor draws from, with text.
	ac, err := b.auditCandidates(e.dropRules(), false)
	if err != nil {
		return err
	}
	stats := &teacherPairsJSON{Frozen: frozen, Preview: preview, FixtureSHA: wouldapply.Fixture(), BothOrders: o.bothOrders,
		CapUSD: float64(capNano) / 1e9, SpentToday: float64(spentNano) / 1e9, CapIsNotSet: capErr == nil && capNano <= 0,
		StrataMembers: map[string]int{}}
	exempt := set.Keys()
	var cands []eval.PairCandidate
	for stratum, keys := range eval.AuditStrata(ac, nil) {
		reason, left := stratumLeftOut(stratum)
		for _, k := range keys {
			p, ok := byKey[k]
			switch {
			case left && reason == "closed":
				stats.Closed++
			case left:
				stats.Excluded++
			case !ok || strings.TrimSpace(p.Description) == "":
				stats.NoText++
			case exempt[k]:
				stats.Exemplar++
			default:
				cands = append(cands, eval.PairCandidate{Posting: p, Stratum: stratum})
				stats.StrataMembers[stratum]++
			}
		}
	}
	var cover map[posting.Key]bool
	if frozen {
		gold, _ := eval.EvaluationLabels(labels, set, byKey)
		cover = make(map[posting.Key]bool, len(gold))
		for k := range gold {
			cover[k] = true
		}
	}

	var unlock func()
	if !o.dry {
		if unlock, err = eval.LockPairs(ctx, pairsPath, teacherLockWait); err != nil {
			return err
		}
		defer unlock()
		// The cap and its spend were read before a possible wait for another run
		// that spends from it: read them again now that this run holds the lock.
		capNano, spentNano, capErr = capFile.State(day)
		stats.CapUSD, stats.SpentToday, stats.CapIsNotSet = float64(capNano)/1e9, float64(spentNano)/1e9, capErr == nil && capNano <= 0
		if err := refuseCap(); err != nil {
			return err
		}
	}
	store, err := eval.OpenPairStore(pairsPath)
	if err != nil {
		return err
	}
	if store.Skipped > 0 {
		fmt.Fprintf(e.stderr, "vani: silver pairs: %d unreadable lines skipped (a torn tail or a hand edit)\n", store.Skipped)
	}

	var id eval.PairIdent
	if haveSet {
		ph, err := wouldapply.PromptHash(summary, ex)
		if err != nil {
			return fmt.Errorf("teacher: %w", err)
		}
		id = eval.PairIdent{Model: model, Fixture: stats.FixtureSHA, Prompt: ph, Exemplar: set.SHA256}
		stats.PromptHash, stats.ExemplarSHA256, stats.Yes, stats.No = ph, set.SHA256, len(set.Yes), len(set.No)
		stats.FrozenAt = set.FrozenAt.Format(time.RFC3339)
	}
	seed := eval.WeekSeed(now())
	if o.seedSet {
		seed = strconv.FormatUint(o.seed, 10)
	}
	stats.Seed = seed

	var works []eval.PairWork
	var qerr error
	asks, ps := eval.PlanPairs(cands, eval.PairPlanOpts{Seed: seed, N: o.pairs, BothOrders: o.bothOrders, Cover: cover, Mates: pairCoverMates},
		func(a eval.PlannedAsk) bool {
			if !haveSet { // nothing to render: the worst-case question stands in
				works = append(works, eval.PairWork{A: a.A, B: a.B, Stratum: a.Stratum, Q: boundQuestion()})
				return true
			}
			q, _, err := wouldapply.Question(a.A, a.B, summary, ex)
			if err != nil {
				qerr = errors.Join(qerr, err)
				return false
			}
			if store.Has(id, a.A.Key, a.B.Key, wouldapply.RenderSHA256(q)) {
				return false
			}
			works = append(works, eval.PairWork{A: a.A, B: a.B, Stratum: a.Stratum, Q: q})
			return true
		})
	if qerr != nil {
		return fmt.Errorf("teacher: building a pair question: %w", qerr)
	}
	stats.Pairs, stats.Questions, stats.AlreadyAnswer, stats.Strata, stats.Cover, stats.CoverShort = ps.Pairs, len(asks), ps.Stored, ps.Strata, ps.Cover, ps.CoverShort

	toks := make([]teacherPrompt, len(works))
	for i, w := range works {
		sys, user := llmbackend.Messages(w.Q)
		toks[i] = teacherPrompt{Estimate: prompt.EstimateTokens(sys + "\n" + user), Ceiling: teacherCeilingTokens(sys, user)}
	}
	llmCap := int64(cfg.Decide.DailyUSD.LLM * 1e9)
	jr.Plan = &teacherPlanJSON{Task: o.task, Model: model, Provider: cfg.LLM.Provider, Candidates: len(cands), NoText: stats.NoText,
		Already: ps.Stored, ToAsk: len(works), DailyCapUSD: cfg.Decide.DailyUSD.LLM}
	jr.Pairs = stats
	e.pairsReport(jr.Plan, stats, cfg, model, source, prices, toks, ps, len(cands), capNano, spentNano, capErr, set)
	if o.dry {
		e.pairsShowQuestion(works, summary, ex, haveSet)
		return nil
	}
	if len(works) == 0 {
		return nil
	}

	// One budget day for the whole run (see teacherRun): the cap file's day is the
	// llm ledger's, w.day.
	ledgerPath := w.budgetLedgerPath()
	d, err := teacherDecider(ctx, w, model)
	if err != nil {
		return err
	}
	spent := func(c context.Context) (int64, error) { return teacherSpent(c, ledgerPath) }
	committed, err := spent(ctx)
	if err != nil {
		return fmt.Errorf("teacher: reading today's llm spend: %w", err)
	}
	before, beforeErr := teacherSettled(ctx, ledgerPath)
	if beforeErr != nil {
		// The auditor's spend is recorded from this ledger; a run that could not
		// record it does not start.
		return fmt.Errorf("teacher: the llm budget ledger cannot be read, so the auditor's spend could not be recorded: %w", beforeErr)
	}
	limit := min(int64(o.budgetUSD*1e9), capNano-spentNano, llmCap-committed)
	if limit <= 0 {
		return fmt.Errorf("teacher: today's llm cap ($%.2f) is already spent; nothing was asked", cfg.Decide.DailyUSD.LLM)
	}
	fmt.Fprintf(e.stdout, "teacher: spending at most %s (the least of --budget-usd, the auditor's cap headroom %s and the $%.2f daily llm cap's headroom)\n",
		usd(limit), usd(capNano-spentNano), cfg.Decide.DailyUSD.LLM)
	// Every question's cost is written to the auditor's cap file as it is paid: the
	// llm ledger's settled spend plus the reservations it kept unproven, which
	// only grows (a question still in flight is counted when it settles, and a
	// reservation that settles lower is never given back). Another spender's cost
	// in the same window is counted against the auditor too, which errs toward
	// the cap.
	last := before.SpentNanoUSD + before.RetainedNanoUSD
	meter := func(c context.Context) error {
		s, err := teacherSettled(c, ledgerPath)
		if err != nil {
			return err
		}
		cur := s.SpentNanoUSD + s.RetainedNanoUSD
		delta := max(cur-last, 0)
		last = max(cur, last)
		return capFile.AddSpent(c, day, delta)
	}
	run := eval.PairRun{
		D: d, Ident: id, Store: store, Concurrency: o.conc, Timeout: llmbackend.MaxTimeout,
		LimitNano: limit, Spent: spent, Worst: func(q decide.Question) int64 { return teacherWorstNano(prices, q) },
		Meter: meter, Now: now,
	}
	res, err := run.Run(ctx, works)
	after, afterErr := teacherSettled(context.WithoutCancel(ctx), ledgerPath)
	jr.Run = e.teacherSummary(eval.TeacherResult{Considered: res.Considered, Skipped: res.Skipped, Asked: res.Asked, Stored: res.Stored,
		Unread: res.Unread, Unusable: res.Unusable, Stopped: res.Stopped, SpentNano: res.SpentNano},
		ps.Stored, len(works), before, beforeErr, after, afterErr, llmCap)
	jr.Run.LimitUSD = float64(limit) / 1e9
	if cn, sn, serr := capFile.State(day); serr == nil {
		stats.CapUSD, stats.SpentToday = float64(cn)/1e9, float64(sn)/1e9
		fmt.Fprintf(e.stdout, "teacher: the auditor's own cap: %s spent today of %s, %s left\n", usd(sn), usd(cn), usd(max(cn-sn, 0)))
	} else {
		fmt.Fprintf(e.stdout, "teacher: the auditor's cap file could not be read back (%v)\n", serr)
	}
	if res.Abstained > 0 {
		fmt.Fprintf(e.stdout, "teacher: %d of the stored answers are abstentions (the model said the pair was too thin to tell)\n", res.Abstained)
	}
	if err != nil {
		return fmt.Errorf("teacher: %w", err)
	}
	if res.Stopped != "" && res.Stopped != eval.TeacherStoppedAtLimit {
		return errors.New("teacher: " + res.Stopped)
	}
	return nil
}

// pairsReport prints what a would_apply_pair run would ask and cost, and fills
// plan's estimate. The prices and the worst case are the teacher's.
func (e *env) pairsReport(plan *teacherPlanJSON, st *teacherPairsJSON, cfg config.Config, model, source string, p budget.Prices,
	toks []teacherPrompt, ps eval.PlanStats, cands int, capNano, spentNano int64, capErr error, set eval.ExemplarSet) {
	switch {
	case st.Frozen:
		fmt.Fprintf(e.stdout, "teacher: exemplar set frozen %s, sha256 %s: %d yes titles and %d no titles\n", st.FrozenAt, set.SHA256[:12], st.Yes, st.No)
	case st.Preview:
		fmt.Fprintf(e.stdout, "teacher: NO FROZEN EXEMPLAR SET. Priced with the set a freeze would write now (sha256 %s: %d yes, %d no); nothing was written, and a real run refuses until `vani teacher freeze` is run\n",
			set.SHA256[:12], st.Yes, st.No)
	default:
		fmt.Fprintf(e.stdout, "teacher: NO FROZEN EXEMPLAR SET and too few labels to preview one (a freeze needs %d yes and %d no). The cost below is a bound from the largest question the context cap allows; a real run refuses until `vani teacher freeze` is run\n",
			eval.MinExemplarsPerClass, eval.MinExemplarsPerClass)
	}
	fmt.Fprintf(e.stdout, "teacher: %d pairs planned (%d questions%s) in %d strata from %d candidate postings, seed %s; %d pairs already answered under this model, question and exemplar set\n",
		st.Pairs, st.Questions, map[bool]string{true: ", both orders", false: ""}[st.BothOrders], st.Strata, cands, st.Seed, ps.Stored)
	fmt.Fprintf(e.stdout, "teacher: left out of the candidates: %d closed, %d sponsorship, work_mode or owner-exclusion drops, %d with no text, %d exemplar postings\n", st.Closed, st.Excluded, st.NoText, st.Exemplar)
	names := slices.Sorted(maps.Keys(st.StrataMembers))
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", n, st.StrataMembers[n]))
	}
	fmt.Fprintf(e.stdout, "teacher: candidates per stratum: %s\n", strings.Join(parts, ", "))
	if st.Frozen {
		fmt.Fprintf(e.stdout, "teacher: evaluation coverage: %d gold labels made after the freeze to be compared with %d mates each; %d got fewer\n", st.Cover, pairCoverMates, st.CoverShort)
	}
	fmt.Fprintf(e.stdout, "teacher: answers are silver labels: they go to %s, never to labels.jsonl, and change no ranking or drop\n", eval.PairsFile)
	switch {
	case capErr != nil:
		fmt.Fprintf(e.stdout, "teacher: the auditor's cap file cannot be read (%v); a real run would refuse\n", capErr)
	case capNano <= 0:
		fmt.Fprintf(e.stdout, "teacher: the auditor has NO daily cap, so a real run would refuse and spend nothing (set one with `vani teacher cap --usd N`)\n")
	default:
		fmt.Fprintf(e.stdout, "teacher: the auditor's daily cap is %s, %s spent today\n", usd(capNano), usd(spentNano))
	}
	if len(toks) == 0 {
		fmt.Fprintf(e.stdout, "teacher: nothing to ask\n")
		return
	}
	c := teacherCostOf(p, toks)
	plan.InputTokens = &teacherTokens{Mean: c.sum / len(toks), Min: c.lo, Max: c.hi, Total: c.sum}
	plan.PriceSource, plan.TypicalUSD, plan.MaxUSD = source, float64(c.typical)/1e9, float64(c.worst)/1e9
	fmt.Fprintf(e.stdout, "teacher: model %s (%s); each question carries the pair prompt, your target summary, the frozen exemplar titles and two postings framed as untrusted content\n", model, cfg.LLM.Provider)
	fmt.Fprintf(e.stdout, "teacher: input tokens per question (estimated with Manvi's prompt.EstimateTokens over the exact messages): mean %d, min %d, max %d, total %d\n", c.sum/len(toks), c.lo, c.hi, c.sum)
	fmt.Fprintf(e.stdout, "teacher: price from %s: input $%.4f and output $%.4f per million tokens\n", source, float64(p.Input)/1000, float64(p.Output)/1000)
	fmt.Fprintf(e.stdout, "teacher: %d questions: typically about %s (replies of %d tokens), at most %s counting every reply at its full budget and every prompt at its physical ceiling (one token per byte, plus %d for the envelope), the bound the spend gate reserves\n",
		len(toks), usd(c.typical), teacherTypicalOut, usd(c.worst), teacherPromptOverhead)
}

// pairsShowQuestion prints the first planned question as the model would get it,
// with each posting's text elided (the exemplar titles are shown as sent).
func (e *env) pairsShowQuestion(works []eval.PairWork, summary string, ex wouldapply.Exemplars, haveSet bool) {
	switch {
	case len(works) == 0:
		return
	case !haveSet:
		fmt.Fprintln(e.stdout, "teacher: no question is shown: there is no exemplar set to render it with")
		return
	}
	q, _, err := wouldapply.Question(elided(works[0].A), elided(works[0].B), summary, ex)
	if err != nil {
		fmt.Fprintf(e.stdout, "teacher: the example question could not be rendered: %v\n", err)
		return
	}
	sys, user := llmbackend.Messages(q)
	fmt.Fprintf(e.stdout, "teacher: one question as sent (the postings' text elided), system message then user message:\n---\n%s\n---\n%s---\n", sys, user)
}

// teacherFreezeReply is `vani teacher freeze --json`.
type teacherFreezeReply struct {
	Path     string           `json:"path"`
	FrozenAt string           `json:"frozen_at,omitempty"`
	SHA256   string           `json:"exemplar_sha256,omitempty"`
	Stats    eval.FreezeStats `json:"stats"`
	failure
}

// cmdTeacherFreeze writes the frozen exemplar set from the owner's labels. The
// freeze time is the line between the labels the auditor may have read and the
// labels it is evaluated on, so it is never repeated without --replace.
func (e *env) cmdTeacherFreeze(args []string) error {
	fs := e.newFlags("teacher freeze")
	replace := fs.Bool("replace", false, "freeze again: the old set is kept beside the new one under its own freeze time (answers asked under it stay tied to its hash); labels made after the new freeze are the new evaluation set")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout: where the set was written, its hash and what it rests on")
	if err := parse(fs, args); err != nil {
		return err
	}
	rep := &teacherFreezeReply{}
	err := func() error {
		path, _, _ := e.silverPaths()
		rep.Path = path
		labels, bad, err := eval.LabelsAt(e.labelsDir())
		if err != nil {
			return fmt.Errorf("labels unreadable, refusing to freeze: %w", err)
		}
		if bad > 0 {
			fmt.Fprintf(e.stderr, "vani: %d malformed label lines skipped\n", bad)
		}
		recs, err := e.ledgerRecords()
		if err != nil {
			return err
		}
		set, st, err := eval.FreezeExemplars(labels, recs, now(), withholdFromExemplars)
		rep.Stats = st
		if err != nil {
			return err
		}
		if err := eval.WriteExemplars(path, set, *replace); err != nil {
			return err
		}
		rep.FrozenAt, rep.SHA256 = set.FrozenAt.Format(time.RFC3339), set.SHA256
		return nil
	}()
	if err != nil {
		rep.fail(err)
	}
	return e.reply(*asJSON, rep, err, func() {
		st := rep.Stats
		fmt.Fprintf(e.stdout, "teacher freeze: wrote %s\n", rep.Path)
		fmt.Fprintf(e.stdout, "teacher freeze: frozen %s, sha256 %s: %d yes and %d no exemplars, of %d yes and %d no labels (left out: %d maybe, %d skip, %d closed-stratum; %d with no ledger record, %d repeated jobs)\n",
			rep.FrozenAt, rep.SHA256, st.YesKept, st.NoKept, st.YesLabels, st.NoLabels, st.Maybe, st.Skipped, st.Closed, st.NotInLedger, st.Repeats)
		fmt.Fprintln(e.stdout, "teacher freeze: labels you make from now on are the auditor's evaluation set; this file is never overwritten without --replace")
	})
}

// teacherCapReply is `vani teacher cap --json`.
type teacherCapReply struct {
	Set         bool    `json:"set"`
	CapUSD      float64 `json:"daily_cap_usd"`
	SpentUSD    float64 `json:"spent_today_usd"`
	HeadroomUSD float64 `json:"headroom_today_usd"`
	Day         string  `json:"day"`
	Path        string  `json:"path"`
	failure
}

// cmdTeacherCap shows or sets the auditor's own daily cap. A cap that was never
// set is zero and the auditor refuses to spend.
func (e *env) cmdTeacherCap(args []string) error {
	fs := e.newFlags("teacher cap")
	capUSD := fs.Float64("usd", 0, fmt.Sprintf("set the auditor's daily cap in dollars (0 refuses all spend; at most %g); without it the cap is shown", eval.MaxPairCapUSD))
	asJSON := e.jsonFlag(fs, "one JSON object on stdout: the cap, today's spend and the headroom")
	if err := parse(fs, args); err != nil {
		return err
	}
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == "usd" })
	_, _, capPath := e.silverPaths()
	rep := &teacherCapReply{Path: capPath, Day: eval.Day(budgetClock())}
	ctx, cancel := signalContext()
	defer cancel()
	c := eval.OpenPairCap(capPath)
	err := func() error {
		if set {
			if err := c.SetCap(ctx, *capUSD); err != nil {
				return usagef("teacher cap: %v", err)
			}
		}
		capNano, spent, err := c.State(rep.Day)
		if err != nil {
			return err
		}
		rep.Set, rep.CapUSD, rep.SpentUSD = capNano > 0, float64(capNano)/1e9, float64(spent)/1e9
		rep.HeadroomUSD = max(rep.CapUSD-rep.SpentUSD, 0)
		return nil
	}()
	if err != nil {
		rep.fail(err)
	}
	return e.reply(*asJSON, rep, err, func() {
		if !rep.Set {
			fmt.Fprintf(e.stdout, "teacher cap: no daily cap is set, so the would_apply auditor spends nothing (%s)\n", rep.Path)
			return
		}
		fmt.Fprintf(e.stdout, "teacher cap: $%.2f a day, $%.4f spent on %s, $%.4f of headroom (%s)\n", rep.CapUSD, rep.SpentUSD, rep.Day, rep.HeadroomUSD, rep.Path)
	})
}

// teacherReportReply is `vani teacher report --json`.
type teacherReportReply struct {
	Frozen         bool                   `json:"frozen"`
	ExemplarSHA256 string                 `json:"exemplar_sha256,omitempty"`
	FrozenAt       string                 `json:"frozen_at,omitempty"`
	Evaluation     *eval.EvalLabelStats   `json:"evaluation_labels,omitempty"`
	OtherExemplars int                    `json:"rows_under_another_exemplar_set"`
	Skipped        int                    `json:"unreadable_lines"`
	Auditors       []teacherAuditorReport `json:"auditors"`
	failure
}

// teacherAuditorReport is one model and question's answers and verdict.
type teacherAuditorReport struct {
	Model string `json:"model"`
	// Current is true when the question and prompt are today's (the fixture hash
	// and the prompt hash, which holds the target summary and the exemplars).
	Current    bool              `json:"current"`
	FixtureSHA string            `json:"fixture_sha"`
	PromptHash string            `json:"prompt_hash"`
	Pairs      eval.PairStats    `json:"pairs"`
	Scored     int               `json:"scored_postings"`
	AUC        rankfit.SilverAUC `json:"auc"`
}

// cmdTeacherReport scores the pair answers against the owner's gold labels made
// after the freeze, and sets the auditor's AUC beside the title part's on the
// same postings. It reads, it spends nothing, and below the plan's counts it
// says "insufficient" and shows no AUC.
func (e *env) cmdTeacherReport(args []string) error {
	fs := e.newFlags("teacher report")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout: the counts, the pair statistics and the AUC verdict per model")
	seed := fs.Uint64("seed", 1, "the bootstrap's seed")
	if err := parse(fs, args); err != nil {
		return err
	}
	rep := &teacherReportReply{Auditors: []teacherAuditorReport{}}
	err := e.silverReport(rep, *seed)
	if err != nil {
		rep.fail(err)
	}
	return e.reply(*asJSON, rep, err, func() {
		fmt.Fprintf(e.stdout, "## Pairwise auditor (silver labels)\n\nexemplar set frozen %s, sha256 %.12s. Labels made after that time are the evaluation set.\n", rep.FrozenAt, rep.ExemplarSHA256)
		if st := rep.Evaluation; st != nil {
			fmt.Fprintf(e.stdout, "evaluation labels: %d yes or no kept; left out: %d made before the freeze, %d relabels of an exemplar, %d repeats of an exemplar job, %d with no ledger record, %d maybe, %d skip, %d closed\n",
				st.Kept, st.BeforeFreeze, st.Exemplar, st.Repost, st.NotInLedger, st.Maybe, st.Skipped, st.Closed)
		}
		if rep.OtherExemplars > 0 {
			fmt.Fprintf(e.stdout, "%d pair rows were asked under another exemplar set and are not counted\n", rep.OtherExemplars)
		}
		if len(rep.Auditors) == 0 {
			fmt.Fprintln(e.stdout, "\nno pair answers under this exemplar set yet (`vani teacher --task would_apply_pair`)")
		}
		for _, a := range rep.Auditors {
			cur := "current question"
			if !a.Current {
				cur = "an earlier question or target summary"
			}
			fmt.Fprintf(e.stdout, "\n#### %s (%s)\n\n", a.Model, cur)
			p := a.Pairs
			fmt.Fprintf(e.stdout, "%d answers: %d usable, %d abstained, %d unusable; %.0f%% of the decisive ones said A (0.5 means position carried nothing; %d decisive), %.0f%% said neither; %d of %d pairs asked in both orders chose the same posting; %d postings have enough comparisons to score (%d games at least)\n",
				p.Rows, p.Usable, p.Abstained, p.Unusable, 100*p.ShareA, p.Decisive, 100*p.NeitherShare, p.Consistent, p.BothOrders, a.Scored, eval.MinPostingGames)
			fmt.Fprint(e.stdout, a.AUC.Markdown())
		}
	})
}

func (e *env) silverReport(rep *teacherReportReply, seed uint64) error {
	e.noCheckpoint = true // a report writes nothing, the ledger's read cache included
	exemplarsPath, pairsPath, _ := e.silverPaths()
	set, err := eval.ReadExemplars(exemplarsPath)
	if err != nil {
		return err
	}
	rep.Frozen, rep.ExemplarSHA256, rep.FrozenAt = true, set.SHA256, set.FrozenAt.Format(time.RFC3339)
	labels, bad, err := eval.LabelsAt(e.labelsDir())
	if err != nil {
		return fmt.Errorf("labels unreadable, refusing to report: %w", err)
	}
	if bad > 0 {
		fmt.Fprintf(e.stderr, "vani: %d malformed label lines skipped\n", bad)
	}
	store, err := eval.OpenPairStore(pairsPath)
	if err != nil {
		return err
	}
	rep.Skipped = store.Skipped
	b, err := e.browse()
	if err != nil {
		return err
	}
	byKey := make(map[posting.Key]posting.Posting, len(b.snap.Records))
	for _, r := range b.snap.Records {
		byKey[r.Key] = r.Posting
	}
	gold, st := eval.EvaluationLabels(labels, set, byKey)
	rep.Evaluation = &st
	base := jobinsight.DefaultWeights()
	items, err := e.judgedItems(labels, b.snap, base, func(k posting.Key) bool { _, ok := gold[k]; return ok })
	if err != nil {
		return err
	}

	// One report per model and question asked under this exemplar set.
	var idents []eval.PairIdent
	for _, r := range store.Rows() {
		id := eval.PairIdent{Model: r.Model, Fixture: r.FixtureSHA, Prompt: r.PromptHash, Exemplar: r.ExemplarSHA}
		switch {
		case id.Exemplar != set.SHA256:
			rep.OtherExemplars++
		case !slices.Contains(idents, id):
			idents = append(idents, id)
		}
	}
	slices.SortFunc(idents, func(a, c eval.PairIdent) int { return strings.Compare(a.Model+a.Prompt, c.Model+c.Prompt) })
	cfg, cfgErr := e.loadConfig()
	currentPrompt := ""
	if cfgErr == nil {
		if sum, err := jdtriage.NormalizeSummary(cfg.TargetSummary); err == nil {
			currentPrompt, _ = wouldapply.PromptHash(sum, set.Exemplars())
		}
	}
	for _, id := range idents {
		scores, ps := eval.ScorePairs(store.Rows(), id)
		keyed := make(map[string]float64, len(scores))
		for k, s := range scores {
			if s.Games >= eval.MinPostingGames {
				keyed[string(k)] = s.Score
			}
		}
		rep.Auditors = append(rep.Auditors, teacherAuditorReport{
			Model: id.Model, Current: id.Fixture == wouldapply.Fixture() && id.Prompt == currentPrompt,
			FixtureSHA: id.Fixture, PromptHash: id.Prompt, Pairs: ps, Scored: len(keyed),
			AUC: rankfit.BuildSilverAUC(items, keyed, base, rankfit.DefaultPartAUCResamples, seed),
		})
	}
	return nil
}
