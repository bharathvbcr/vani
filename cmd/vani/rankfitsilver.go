package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/rankfit"
)

// `vani rankfit --part weights --silver-prior`: the silver pretrain (stage 8).
// The pairwise auditor's decisive answers under today's question fit Score's
// shares first (rankfit.FitSilverPrior), and the owner-labelled fit is pulled
// toward them instead of the hand weights. The gold gate does not move: the
// fit is measured against the hand weights on held-out owner judgments, and
// only Beats writes. Held out are only the judgments the auditor cannot have
// learned from (made after the exemplar freeze, not an exemplar or a repeat of
// one); the gold-only fit on the same split is reported beside it.

// silverPriorFit is the prior and what the comparison needs.
type silverPriorFit struct {
	Prior     rankfit.SilverPrior `json:"prior"`
	Auditor   string              `json:"auditor"`
	Absent    int                 `json:"absent"` // pairs naming a posting the ledger no longer has, left out
	TrainOnly func(string) bool   `json:"-"`
	// GoldOnly is the fit without the prior on the same split and seed.
	GoldOnly *rankfit.FitResult `json:"gold_only,omitempty"`
}

// rankfitSilverPrior fits the prior from the auditor's answers.
func (e *env) rankfitSilverPrior(snap ledger.Snapshot, me map[posting.Key]marks.Entry, lab map[posting.Key]eval.Entry,
	base jobinsight.Weights, lambda float64, w io.Writer) (*silverPriorFit, error) {
	rep := &teacherReportReply{Auditors: []teacherAuditorReport{}}
	if err := e.silverReport(rep, 1); err != nil {
		return nil, fmt.Errorf("rankfit --silver-prior reads the auditor's answers: %w", err)
	}
	var pick *teacherAuditorReport
	for i := range rep.Auditors {
		if a := &rep.Auditors[i]; a.Current && (pick == nil || a.Pairs.Decisive > pick.Pairs.Decisive) {
			pick = a
		}
	}
	if pick == nil {
		return nil, errors.New("rankfit --silver-prior: no auditor answers under today's question and exemplar set")
	}
	exemplarsPath, pairsPath, _ := e.silverPaths()
	store, err := eval.OpenPairStore(pairsPath)
	if err != nil {
		return nil, err
	}
	set, err := eval.ReadExemplars(exemplarsPath)
	if err != nil {
		return nil, err
	}
	decisive := eval.DecisivePairs(store.Rows(), eval.PairIdent{Model: pick.Model, Fixture: pick.FixtureSHA, Prompt: pick.PromptHash, Exemplar: rep.ExemplarSHA256})

	// Any posting the owner answered, or that the auditor was shown as an
	// exemplar, is gold: no silver pair about it reaches the prior.
	judged := func(k string) bool {
		e, ok := lab[posting.Key(k)]
		return ok && e.Labels[eval.WouldApply] != ""
	}
	targets := map[posting.Key]rankfit.Target{}
	for _, d := range decisive {
		for _, k := range []posting.Key{d.Win, d.Lose} {
			if !judged(string(k)) {
				targets[k] = rankfit.Target{}
			}
		}
	}
	items, _, _, err := e.rankfitItems(snap, me, targets, base)
	if err != nil {
		return nil, fmt.Errorf("rankfit --silver-prior: score the auditor's postings: %w", err)
	}
	have := make(map[string]bool, len(items))
	for _, it := range items {
		have[it.Key] = true
	}
	out := &silverPriorFit{Auditor: pick.Model}
	pairs := make([]rankfit.SilverPair, 0, len(decisive))
	for _, d := range decisive {
		win, lose := string(d.Win), string(d.Lose)
		if !judged(win) && !judged(lose) && (!have[win] || !have[lose]) {
			out.Absent++
			continue
		}
		pairs = append(pairs, rankfit.SilverPair{Win: win, Lose: lose})
	}
	prior, err := rankfit.FitSilverPrior(items, pairs, judged, base, lambda)
	out.Prior = prior
	if err != nil {
		return out, fmt.Errorf("rankfit --silver-prior %w; nothing written", err)
	}
	byKey := make(map[posting.Key]posting.Posting, len(snap.Records))
	for _, r := range snap.Records {
		byKey[r.Key] = r.Posting
	}
	gold, _ := eval.EvaluationLabels(lab, set, byKey)
	out.TrainOnly = func(k string) bool { _, ok := gold[posting.Key(k)]; return !ok }

	fmt.Fprintf(w, "silver prior (%s): %d decisive answers used, %d about owner-judged postings left out, %d naming postings no longer in the ledger; %d postings\n",
		pick.Model, prior.Pairs, prior.Excluded, out.Absent, prior.Items)
	hand, ps := rankfit.SharesOf(base).Array(), prior.Shares.Array()
	for i, n := range rankfit.PartNames {
		fmt.Fprintf(w, "  %-9s hand %.4f  prior %.4f\n", n, hand[i], ps[i])
	}
	if !prior.Converged {
		fmt.Fprintf(w, "warning: the prior's optimizer stopped at its iteration cap (%d)\n", prior.Iterations)
	}
	fmt.Fprintf(w, "held out: only the %d owner judgments made after the exemplar freeze\n", len(gold))
	return out, nil
}
