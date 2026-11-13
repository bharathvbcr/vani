package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/rankfit"
)

// rankfitParts are what `vani rankfit --part` can fit.
var rankfitParts = []string{"all", "platt", "weights"}

// errRankfitNoGain is a weight fit that ran but did not beat the hand weights
// on the held-out pairs: nothing is written.
var errRankfitNoGain = errors.New("rankfit: the fitted shares do not clearly beat the hand weights on the held-out judged pairs (too few pairs, or the gain interval does not clear 0); nothing written (--part platt keeps a calibration alone)")

// cmdRankfit learns Score's constants from judged labels (internal/rankfit):
// per-backend Platt calibration from the eval slot labels, and the shares from
// the audit's judgments (internal/feedback), marks and impressions. Below a threshold it refuses, exits
// non-zero and writes nothing; a refusal reads "could not run", never as a
// result. --dry-run prints the counts and what would run.
func (e *env) cmdRankfit(args []string) error {
	fs := e.newFlags("rankfit")
	labels := fs.String("labels", "", "label directory (default: state dir)")
	out := fs.String("out", "", "file to write (default: <state dir>/rank/weights.json)")
	part := fs.String("part", "all", "what to fit: "+strings.Join(rankfitParts, ", "))
	seed := fs.Uint64("seed", 1, "seed for the held-out split and the pair sample")
	lambda := fs.Float64("lambda", rankfit.DefaultLambda, "L2 pull of each log-share toward the hand weights, in training items")
	holdout := fs.Int("holdout", rankfit.DefaultHoldoutPct, "percent of audit judgments held out to evaluate the fit (1-90)")
	maxPairs := fs.Int("max-pairs", rankfit.DefaultMaxPairs, "training pairs above which they are sampled")
	dry := fs.Bool("dry-run", false, "print the label counts and what would run; fit and write nothing")
	silverPrior := fs.Bool("silver-prior", false, "with the weight fit: pull it toward shares fitted to the would_apply auditor's answers instead of the hand weights; held out are only owner judgments made after the exemplar freeze, and the gold gate decides as before")
	spare := fs.String("spare-seniority", "", "set the seniority readings (comma list, e.g. mid,senior) at which a mismatch_seniority role_fit is not a mismatch, in the weights file, and fit nothing; \"none\" clears it")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout: the counts, what ran or would, and the fit")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *spare != "" {
		path := *out
		if path == "" {
			path = filepath.Join(e.stateDir, "rank", "weights.json")
		}
		return e.rankfitSpareSeniority(path, *spare, *dry, *asJSON)
	}
	if !slices.Contains(rankfitParts, *part) {
		return usagef("rankfit: --part must be one of %s", strings.Join(rankfitParts, ", "))
	}
	if *silverPrior && *part == "platt" {
		return usagef("rankfit: --silver-prior is for the weight fit")
	}
	if !(*lambda > 0) || *lambda > 1e6 {
		return usagef("rankfit: --lambda must be more than 0 and at most 1e6")
	}
	if *holdout < 1 || *holdout > 90 {
		return usagef("rankfit: --holdout must be within 1..90")
	}
	if *maxPairs < 1 || *maxPairs > rankfit.MaxPairsLimit {
		return usagef("rankfit: --max-pairs must be within 1..%d", rankfit.MaxPairsLimit)
	}
	path := *out
	if path == "" {
		path = filepath.Join(e.stateDir, "rank", "weights.json")
	}

	s, err := e.openEvalStore(*labels)
	if err != nil {
		return err
	}
	lab, bad, err := s.Labels()
	if err != nil {
		return fmt.Errorf("rankfit could not run: labels unreadable: %w", err)
	}
	if bad > 0 {
		fmt.Fprintf(e.stderr, "vani: %d malformed label lines skipped\n", bad)
	}
	recs, err := e.ledgerRecords()
	if err != nil {
		return err
	}
	snap := ledger.Snapshot{Records: recs}
	me, err := marks.Read(marks.Dir(e.stateDir))
	if err != nil {
		return fmt.Errorf("rankfit could not run: read marks: %w", err)
	}
	mk := make(map[posting.Key]marks.Mark, len(me))
	for k, en := range me {
		mk[k] = en.Mark
	}

	shown, err := feedback.ReadImpressions(e.stateDir)
	if err != nil {
		return fmt.Errorf("rankfit could not run: read impressions: %w", err)
	}
	if shown.Skipped > 0 || shown.Capped > 0 {
		fmt.Fprintf(e.stderr, "vani: impressions: %d unreadable lines skipped, %d older lines past the read cap left out\n", shown.Skipped, shown.Capped)
	}

	targets, tc, err := rankfit.Targets(lab, shown.Rows, mk)
	if err != nil {
		return fmt.Errorf("rankfit could not run: %w", err)
	}
	rep := rankfit.Report{Labels: len(lab), Targets: tc}
	samples, missing := rankfit.PlattSamples(snap.Records, lab)
	jr := &rankfitReply{DryRun: *dry, Part: *part, Path: path, SlotLabelsMissing: missing, PlattUsable: map[string]int{}, Report: &rep}
	w := e.stdout
	if *asJSON {
		w = io.Discard // the reply carries what the text says
	}
	fmt.Fprintf(w, "slot labels: %d eval entries (%d not in the ledger)\n", rep.Labels, missing)
	fmt.Fprintf(w, "audit judgments: %d yes, %d maybe, %d no (%d skipped); marks: %d saved, %d hidden; shown and left unmarked: %d\n",
		tc.Yes, tc.Maybe, tc.No, tc.Skipped, tc.Saved, tc.Hidden, tc.Passed)
	if tc.Closed > 0 {
		fmt.Fprintf(w, "audit judgments of closed postings (dead or duplicate) left out of the fit: %d\n", tc.Closed)
	}
	if tc.Selected > 0 {
		fmt.Fprintf(w, "audit judgments from the disagreement or yes-neighbour cells left out of the fit: %d\n", tc.Selected)
	}
	if tc.Engineering > 0 {
		fmt.Fprintf(w, "audit judgments from the engineering group left out of the fit (until its weights are post-stratified): %d\n", tc.Engineering)
	}

	doPlatt, doWeights := *part != "weights", *part != "platt"
	var notRun []string
	if doPlatt {
		usable := 0
		for _, b := range slices.Sorted(maps.Keys(samples)) {
			n := rankfit.PlattUsable(samples[b])
			jr.PlattUsable[b] = n
			usable = max(usable, n)
			verdict := "would run"
			if n < rankfit.MinPlattPostings {
				verdict = fmt.Sprintf("would refuse: %d of %d", n, rankfit.MinPlattPostings)
			}
			fmt.Fprintf(w, "platt %s: %d judged postings (%s)\n", b, n, verdict)
		}
		if usable < rankfit.MinPlattPostings {
			notRun = append(notRun, (&rankfit.NotRunError{Part: "platt", Have: usable, Need: rankfit.MinPlattPostings,
				What: "judged postings for at least one backend"}).Error())
		}
	}
	if doWeights {
		verdict := "would run"
		if tc.Judged() < rankfit.MinJudged {
			verdict = fmt.Sprintf("would refuse: %d of %d", tc.Judged(), rankfit.MinJudged)
		}
		fmt.Fprintf(w, "weight fit: %d usable audit judgments (%s)\n", tc.Judged(), verdict)
		if tc.Judged() < rankfit.MinJudged {
			notRun = append(notRun, (&rankfit.NotRunError{Part: "weight fit", Have: tc.Judged(), Need: rankfit.MinJudged,
				What: "usable audit judgments"}).Error())
		}
	}
	jr.WeightJudged, jr.NotRun = tc.Judged(), nonNil(notRun)
	if *dry {
		fmt.Fprintln(w, "dry run: nothing fitted or written")
		return e.reply(*asJSON, jr, nil, func() {})
	}
	if len(notRun) > 0 {
		return e.reply(*asJSON, jr, fmt.Errorf("rankfit %s; nothing written", strings.Join(notRun, "; ")), nil)
	}

	base := jobinsight.DefaultWeights()
	f := rankfit.File{V: rankfit.FileVersion, FittedAt: now().UTC(), Report: &rep}
	if doPlatt {
		rep.Platt, rep.PlattRefused = map[string]rankfit.PlattFit{}, map[string]int{}
		f.Calibration = map[string]jobinsight.Calibration{}
		for _, b := range slices.Sorted(maps.Keys(samples)) {
			fit, err := rankfit.FitPlatt(b, samples[b], rankfit.DefaultPlattRidge)
			var nr *rankfit.NotRunError
			switch {
			case errors.As(err, &nr):
				rep.PlattRefused[b] = fit.N
				continue
			case err != nil:
				return err
			}
			rep.Platt[b], f.Calibration[b] = fit, fit.Calibration
			if c := fit.Calibration; c.UsesP {
				fmt.Fprintf(w, "platt %s: %d readings, %d right; conf = sigmoid(%.4f*logit(p) + %.4f)\n", b, fit.N, fit.Correct, c.A, c.B)
			} else {
				fmt.Fprintf(w, "platt %s: %d readings, %d right; no p, so conf = sigmoid(%.4f) = %.4f\n", b, fit.N, fit.Correct, c.B, 1/(1+math.Exp(-c.B)))
			}
		}
		base = f.Apply(base)
	}
	if doWeights {
		opt := rankfit.Options{Lambda: *lambda, Seed: *seed, HoldoutPct: *holdout, MaxPairs: *maxPairs}
		if *silverPrior {
			sp, err := e.rankfitSilverPrior(snap, me, lab, base, *lambda, w)
			if sp != nil {
				jr.SilverPrior = sp
			}
			if err != nil {
				return e.reply(*asJSON, jr, err, nil)
			}
			opt.TrainOnly = sp.TrainOnly
			fmt.Fprintln(w, "gold-only fit on the same split, for comparison:")
			gold, err := e.rankfitWeights(snap, me, targets, base, opt, w)
			if err != nil {
				return err
			}
			sp.GoldOnly = &gold
			opt.Prior = &sp.Prior.Shares
			fmt.Fprintln(w, "fit pulled toward the silver prior (this one is gated and written):")
		}
		res, err := e.rankfitWeights(snap, me, targets, base, opt, w)
		if err != nil {
			return err
		}
		rep.Weights = &res
		if !res.Beats() {
			fmt.Fprintf(w, "no gain: %s\n", res.NoGainReason())
			return e.reply(*asJSON, jr, errRankfitNoGain, nil)
		}
		f.Shares = &res.Shares
	}
	if err := rankfit.Write(path, f); err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote %s\nto rank by it, set rank_weights_file to that path in the config\n", path)
	jr.Written = true
	return e.reply(*asJSON, jr, nil, func() {})
}

// rankfitSpareSeniority sets the weights file's spared seniority levels
// (`vani rankfit --spare-seniority`): read the file at path if there is one
// (a file that cannot be read is an error, never overwritten), set the list,
// keep everything else, write it back validated. "none" clears the list; a
// file left with nothing in it is refused rather than written empty.
func (e *env) rankfitSpareSeniority(path, list string, dry, asJSON bool) error {
	var levels []string
	if strings.TrimSpace(list) != "none" {
		for _, v := range strings.Split(list, ",") {
			if v = strings.TrimSpace(v); v != "" {
				levels = append(levels, v)
			}
		}
		if len(levels) == 0 {
			return usagef("rankfit: --spare-seniority wants a comma list of seniority readings, or none")
		}
	}
	f := rankfit.File{V: rankfit.FileVersion, FittedAt: now().UTC()}
	if _, err := os.Stat(path); err == nil {
		if f, err = rankfit.Load(path); err != nil {
			return fmt.Errorf("rankfit: %s exists and cannot be read, so it is left as it is: %w", path, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("rankfit: %w", err)
	}
	f.SpareSeniorityMismatch = levels
	if err := f.Validate(); err != nil {
		return usagef("%v", err)
	}
	reply := struct {
		Path    string   `json:"path"`
		Spared  []string `json:"spared"`
		DryRun  bool     `json:"dry_run"`
		Written bool     `json:"written"`
	}{Path: path, Spared: nonNil(levels), DryRun: dry}
	if !dry {
		if err := rankfit.Write(path, f); err != nil {
			return err
		}
		reply.Written = true
	}
	return e.reply(asJSON, reply, nil, func() {
		switch {
		case dry:
			fmt.Fprintf(e.stdout, "dry run: would set spared seniority to %v in %s\n", nonNil(levels), path)
		case len(levels) == 0:
			fmt.Fprintf(e.stdout, "cleared spared seniority in %s\n", path)
		default:
			fmt.Fprintf(e.stdout, "spared seniority %v written to %s\nto rank by it, set rank_weights_file to that path in the config\n", levels, path)
		}
	})
}

// rankfitReply is `vani rankfit --json`: the label counts, each part that would
// run or was refused, and the fit as the weights file records it. A refusal
// or a fit that does not beat the hand weights carries the failure, and
// nothing is written. Report.Targets has the weights file's field names.
type rankfitReply struct {
	DryRun bool   `json:"dry_run"`
	Part   string `json:"part"`
	Path   string `json:"path"`
	// SlotLabelsMissing counts eval entries with no ledger record.
	SlotLabelsMissing int `json:"slot_labels_missing"`
	// PlattUsable is each backend's judged postings, when platt is fitted.
	PlattUsable  map[string]int  `json:"platt_usable"`
	WeightJudged int             `json:"weight_judged"`
	NotRun       []string        `json:"not_run"`
	Report       *rankfit.Report `json:"report"`
	Written      bool            `json:"written"`
	// SilverPrior is the prior and the gold-only comparison, with --silver-prior.
	SilverPrior *silverPriorFit `json:"silver_prior,omitempty"`
	failure
}

// rankfitItems builds each target's Score parts, the one way the fit and the
// relevance report's part AUC both read them: as feedback recorded them when
// the posting was judged or shown, else scored now under base. recorded counts
// the first kind; absent counts targets with no ledger record (left out).
func (e *env) rankfitItems(snap ledger.Snapshot, me map[posting.Key]marks.Entry, targets map[posting.Key]rankfit.Target,
	base jobinsight.Weights) (items []rankfit.Item, recorded, absent int, err error) {
	var in *insight
	byKey := make(map[posting.Key]ledger.Record, len(snap.Records))
	for _, r := range snap.Records {
		byKey[r.Key] = r
	}
	for _, k := range slices.Sorted(maps.Keys(targets)) {
		t := targets[k]
		if t.Scores != nil {
			it, err := rankfit.ItemFromScores(string(k), *t.Scores, base, t)
			if err != nil {
				return nil, 0, 0, err
			}
			items, recorded = append(items, it), recorded+1
			continue
		}
		r, ok := byKey[k]
		if !ok {
			absent++
			continue
		}
		if in == nil {
			if in, err = e.newInsight(jobinsight.DefaultScoreHalfLifeDays, snap, me); err != nil {
				return nil, 0, 0, err
			}
		}
		it, err := rankfit.ItemFrom(string(k), jobinsight.ScoreWith(jobinsight.InputsFor(r, in.rc), base), t)
		if err != nil {
			return nil, 0, 0, err
		}
		items = append(items, it)
	}
	return items, recorded, absent, nil
}

// rankfitWeights builds each labelled posting's Score parts: as feedback
// recorded them when it was judged or shown, which is what the owner saw;
// else scored now under base, as `vani jobs` would rank it (the same insight).
// It fits the shares and prints the comparison with the hand weights.
func (e *env) rankfitWeights(snap ledger.Snapshot, me map[posting.Key]marks.Entry, targets map[posting.Key]rankfit.Target,
	base jobinsight.Weights, opt rankfit.Options, w io.Writer) (rankfit.FitResult, error) {
	items, recorded, absent, err := e.rankfitItems(snap, me, targets, base)
	if err != nil {
		return rankfit.FitResult{}, err
	}
	fmt.Fprintf(w, "weight fit: %d labelled postings with the scores recorded when judged or shown, %d scored now, %d not in the ledger and left out\n",
		recorded, len(items)-recorded, absent)
	res, err := rankfit.Fit(items, base, opt)
	if err != nil {
		return res, fmt.Errorf("rankfit %w; nothing written", err)
	}
	fmt.Fprintf(w, "weight fit: %d training items, %d of %d pairs used; %d held-out items, %d pairs\n",
		res.TrainItems, res.TrainPairs, res.TrainPairsTotal, res.HeldOutItems, res.HeldOutPairs)
	fmt.Fprintf(w, "held-out pairwise accuracy: fitted %.4f, hand %.4f (pairs weighted by 1/p_select); n = %d held-out pairs\n",
		res.FitAccuracy, res.HandAccuracy, res.HeldOutPairs)
	fmt.Fprintf(w, "gain interval (95%%, %d item resamples): [%+.4f, %+.4f]; a gain needs n >= %d and the lower end above 0\n",
		res.Resamples, res.GainLo, res.GainHi, rankfit.MinHeldOutPairs)
	for _, s := range res.Strata {
		fmt.Fprintf(w, "  stratum %-12s %6d pairs: fitted %.4f, hand %.4f\n", s.Stratum, s.Pairs, s.FitAccuracy, s.HandAccuracy)
	}
	if res.Stratified {
		fmt.Fprintf(w, "scored-stratum pairs: %d (a gain needs %d)\n", res.ScoredPairs, rankfit.MinScoredStratumPairs)
	}
	hand, fit := res.Hand.Array(), res.Shares.Array()
	for i, n := range rankfit.PartNames {
		fmt.Fprintf(w, "  %-9s hand %.4f  fitted %.4f\n", n, hand[i], fit[i])
	}
	fmt.Fprintf(w, "diagnostic exponents with gate and freshness free: gate %.3f, fresh %.3f (1 is what Score uses)\n", res.GateExp, res.FreshExp)
	if !res.Converged {
		fmt.Fprintf(w, "warning: the optimizer stopped at its iteration cap (%d)\n", res.Iterations)
	}
	if !res.DiagConverged {
		fmt.Fprintln(w, "warning: the diagnostic fit did not converge; ignore its exponents")
	}
	return res, nil
}
