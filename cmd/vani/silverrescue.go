package main

import (
	"errors"
	"fmt"
	"slices"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/rankfit"
)

// `vani retriage --silver-drops`: the per-posting rescue rule (D6). It reports
// the model drops the pairwise would_apply auditor keeps picking over their
// stratum-mates, and how many of them the owner has answered yes since the
// exemplar freeze (the rule's precision on gold). With --rescue it returns them
// to unsure, at most --max-per-day a day, and only while `vani teacher report`
// says "go" for the auditor under today's question. It asks no model.

// Defaults of the rule's flags.
const (
	defaultSilverMinScore  = 0.7
	defaultSilverMinGames  = 4
	defaultSilverMaxPerDay = 10
)

// silverDropsReply is `vani retriage --silver-drops --json`.
type silverDropsReply struct {
	SilverDrops bool    `json:"silver_drops"`
	DryRun      bool    `json:"dry_run"`
	Rescue      bool    `json:"rescue"`
	MinScore    float64 `json:"min_score"`
	MinGames    int     `json:"min_games"`
	MaxPerDay   int     `json:"max_per_day"`
	// Auditor is the model whose picks were read, Verdict its AUC verdict on
	// gold (rankfit.SilverGo and the rest); empty when no current answers.
	Auditor string `json:"auditor"`
	Verdict string `json:"verdict"`
	// The rescue's outcome (candidates, what was or would be rescued, the cap).
	pipeline.SilverRescue
	// GoldYes and GoldNo count the candidates the owner answered after the
	// exemplar freeze (relabels and repeats of an exemplar left out): the
	// rule's precision on gold is GoldYes / (GoldYes + GoldNo), with its
	// Wilson interval when there is any.
	GoldYes   int     `json:"gold_yes"`
	GoldNo    int     `json:"gold_no"`
	PrecLo    float64 `json:"precision_lo,omitempty"`
	PrecHi    float64 `json:"precision_hi,omitempty"`
	Unjudged  int     `json:"unjudged"`
	ScoreNote string  `json:"score_note"`
	failure
}

const silverScoreNote = "the auditor's score is relative to the posting's stratum: a dropped stratum's best is not by itself a wanted posting, which is why the rescue waits for the auditor's verdict on gold"

// retriageSilver is `vani retriage --silver-drops`.
func (e *env) retriageSilver(cfg config.Config, rule pipeline.SilverRule, dry, rescue, asJSON bool) error {
	reply := &silverDropsReply{SilverDrops: true, DryRun: dry, Rescue: rescue,
		MinScore: rule.MinScore, MinGames: rule.MinGames, MaxPerDay: rule.MaxPerDay, ScoreNote: silverScoreNote,
		SilverRescue: pipeline.SilverRescue{Candidates: []pipeline.SilverCandidate{}, Rescued: []posting.Key{}, Left: map[string]int{}}}
	err := e.silverRescue(cfg, rule, dry, rescue, reply)
	if err != nil {
		reply.fail(err)
	}
	return e.reply(asJSON, reply, err, func() { e.printSilver(reply) })
}

func (e *env) silverRescue(cfg config.Config, rule pipeline.SilverRule, dry, rescue bool, reply *silverDropsReply) error {
	if err := rule.Check(); err != nil {
		return usageError{"retriage --silver-drops: " + err.Error()}
	}
	// The auditor's verdict on gold, and which answers are today's.
	rep := &teacherReportReply{Auditors: []teacherAuditorReport{}}
	if err := e.silverReport(rep, 1); err != nil {
		return fmt.Errorf("retriage --silver-drops reads the auditor's report: %w", err)
	}
	var pick *teacherAuditorReport
	for i := range rep.Auditors {
		a := &rep.Auditors[i]
		if !a.Current {
			continue
		}
		if pick == nil || (a.AUC.Status == rankfit.SilverGo && pick.AUC.Status != rankfit.SilverGo) {
			pick = a
		}
	}
	if pick == nil {
		return errors.New("retriage --silver-drops: no auditor answers under today's question and exemplar set (`vani teacher --task would_apply_pair`)")
	}
	reply.Auditor, reply.Verdict = pick.Model, pick.AUC.Status
	if rescue && !dry && pick.AUC.Status != rankfit.SilverGo {
		return fmt.Errorf("retriage --silver-drops --rescue: the auditor's verdict on gold is %q, and a rescue needs %q (`vani teacher report`)", pick.AUC.Status, rankfit.SilverGo)
	}
	_, pairsPath, _ := e.silverPaths()
	store, err := eval.OpenPairStore(pairsPath)
	if err != nil {
		return err
	}
	scores, _ := eval.ScorePairs(store.Rows(), eval.PairIdent{Model: pick.Model, Fixture: pick.FixtureSHA, Prompt: pick.PromptHash, Exemplar: rep.ExemplarSHA256})
	picks := make(map[posting.Key]pipeline.SilverPick, len(scores))
	for k, s := range scores {
		picks[k] = pipeline.SilverPick{Score: s.Score, Games: s.Games, Picked: s.Picked, Neither: s.Neither}
	}

	var l ledger.Ledger
	if rescue && !dry {
		app, err := buildApp(cfg, e.stateDir, e.stderr)
		if err != nil {
			return err
		}
		defer app.close()
		l = app.Ledger
	} else {
		snap, err := e.readLedgerSnapshot("retriage")
		if err != nil {
			return err
		}
		l = snap
	}
	rules := evalDropRules(cfg)
	protected := func(r ledger.Record) bool {
		_, left := stratumLeftOut(eval.DroppedPrefix + eval.DropClass(r, rules))
		return left
	}
	ctx, cancel := signalContext()
	defer cancel()
	out, err := pipeline.RescueSilverDrops(ctx, l, now(), picks, rule, protected, dry || !rescue)
	if err != nil {
		return err
	}
	if !rescue {
		out.Rescued = []posting.Key{} // a report rescues nothing; Candidates says what would qualify
	}
	reply.SilverRescue = out

	// Precision on gold: the candidates the owner answered after the freeze.
	labels, _, err := eval.LabelsAt(e.labelsDir())
	if err != nil {
		return fmt.Errorf("labels unreadable: %w", err)
	}
	exemplarsPath, _, _ := e.silverPaths()
	set, err := eval.ReadExemplars(exemplarsPath)
	if err != nil {
		return err
	}
	byKey := make(map[posting.Key]posting.Posting)
	for _, r := range l.All() {
		byKey[r.Key] = r.Posting
	}
	gold, _ := eval.EvaluationLabels(labels, set, byKey)
	for _, c := range out.Candidates {
		switch gold[c.Key] {
		case "yes":
			reply.GoldYes++
		case "no":
			reply.GoldNo++
		default:
			reply.Unjudged++
		}
	}
	if lo, hi, ok := eval.Wilson(reply.GoldYes, reply.GoldYes+reply.GoldNo); ok {
		reply.PrecLo, reply.PrecHi = lo, hi
	}
	return nil
}

func (e *env) printSilver(r *silverDropsReply) {
	w := e.stdout
	if r.Error != "" {
		return
	}
	fmt.Fprintf(w, "## Silver rescue (D6)\n\nauditor %s, verdict on gold: %s. Rule: score at least %.2f over at least %d comparisons, no answer of neither; model drops only (title, exclusion, work authorization and work_mode drops never); at most %d a day.\n", r.Auditor, r.Verdict, r.MinScore, r.MinGames, r.MaxPerDay)
	fmt.Fprintf(w, "note: %s\n\n", r.ScoreNote)
	fmt.Fprintf(w, "candidates: %d; rescued earlier today: %d; left in today's cap: %d\n", len(r.Candidates), r.Today, r.CapLeft)
	if len(r.Left) > 0 {
		keys := make([]string, 0, len(r.Left))
		for k := range r.Left {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "passed over: %d %s\n", r.Left[k], k)
		}
	}
	fmt.Fprintf(w, "precision on gold (candidates answered after the freeze): %d yes, %d no, %d not answered", r.GoldYes, r.GoldNo, r.Unjudged)
	if r.GoldYes+r.GoldNo > 0 {
		fmt.Fprintf(w, "; yes share 95%% %.2f-%.2f", r.PrecLo, r.PrecHi)
	}
	fmt.Fprintln(w)
	switch {
	case !r.Rescue:
		fmt.Fprintln(w, "report only: --rescue returns the candidates to unsure within the cap")
	case r.DryRun:
		fmt.Fprintf(w, "dry run: %d would be returned to unsure\n", len(r.Rescued))
	default:
		fmt.Fprintf(w, "returned to unsure: %d\n", len(r.Rescued))
	}
	for i, c := range r.Candidates {
		if i == maxStaleItemsText {
			fmt.Fprintf(w, "(%d more)\n", len(r.Candidates)-i)
			break
		}
		fmt.Fprintf(w, "  %s score %.2f over %d\n", c.Key, c.Score, c.Games)
	}
}
