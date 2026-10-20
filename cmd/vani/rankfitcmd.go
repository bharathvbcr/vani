package main

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"path/filepath"
	"slices"
	"strings"

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
var errRankfitNoGain = errors.New("rankfit: the fitted shares do not beat the hand weights on the held-out judged pairs; nothing written (--part platt keeps a calibration alone)")

// cmdRankfit learns Score's constants from judged labels (internal/rankfit):
// per-backend Platt calibration from the eval labels, and the shares from
// would-apply judgements and marks. Below a threshold it refuses, exits
// non-zero and writes nothing; a refusal reads "could not run", never as a
// result. --dry-run prints the counts and what would run.
func (e *env) cmdRankfit(args []string) error {
	fs := e.newFlags("rankfit")
	labels := fs.String("labels", "", "label directory (default: state dir)")
	out := fs.String("out", "", "file to write (default: <state dir>/rank/weights.json)")
	part := fs.String("part", "all", "what to fit: "+strings.Join(rankfitParts, ", "))
	seed := fs.Uint64("seed", 1, "seed for the held-out split and the pair sample")
	lambda := fs.Float64("lambda", rankfit.DefaultLambda, "L2 pull of each log-share toward the hand weights, in training items")
	holdout := fs.Int("holdout", rankfit.DefaultHoldoutPct, "percent of judged labels held out to evaluate the fit (1-90)")
	maxPairs := fs.Int("max-pairs", rankfit.DefaultMaxPairs, "training pairs above which they are sampled")
	dry := fs.Bool("dry-run", false, "print the label counts and what would run; fit and write nothing")
	if err := parse(fs, args); err != nil {
		return err
	}
	if !slices.Contains(rankfitParts, *part) {
		return usagef("rankfit: --part must be one of %s", strings.Join(rankfitParts, ", "))
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

	targets := rankfit.Targets(lab, mk)
	rep := rankfit.Report{Labels: len(lab)}
	for _, t := range targets {
		switch {
		case t.Judged:
			rep.Judged++
		case t.Positive:
			rep.MarkedPos++
		default:
			rep.MarkedNeg++
		}
	}
	samples, missing := rankfit.PlattSamples(snap.Records, lab)
	w := e.stdout
	fmt.Fprintf(w, "labels: %d eval entries (%d not in the ledger); %d would-apply judgements; marks: %d positive, %d negative\n",
		rep.Labels, missing, rep.Judged, rep.MarkedPos, rep.MarkedNeg)

	doPlatt, doWeights := *part != "weights", *part != "platt"
	var notRun []string
	if doPlatt {
		usable := 0
		for _, b := range slices.Sorted(maps.Keys(samples)) {
			n := rankfit.PlattUsable(samples[b])
			usable = max(usable, n)
			verdict := "would run"
			if n < rankfit.MinPlattLabels {
				verdict = fmt.Sprintf("would refuse: %d of %d", n, rankfit.MinPlattLabels)
			}
			fmt.Fprintf(w, "platt %s: %d judged readings (%s)\n", b, n, verdict)
		}
		if usable < rankfit.MinPlattLabels {
			notRun = append(notRun, (&rankfit.NotRunError{Part: "platt", Have: usable, Need: rankfit.MinPlattLabels,
				What: "judged readings for at least one backend"}).Error())
		}
	}
	if doWeights {
		verdict := "would run"
		if rep.Judged < rankfit.MinJudgedLabels {
			verdict = fmt.Sprintf("would refuse: %d of %d", rep.Judged, rankfit.MinJudgedLabels)
		}
		fmt.Fprintf(w, "weight fit: %d judged would-apply labels (%s)\n", rep.Judged, verdict)
		if rep.Judged < rankfit.MinJudgedLabels {
			notRun = append(notRun, (&rankfit.NotRunError{Part: "weight fit", Have: rep.Judged, Need: rankfit.MinJudgedLabels,
				What: "judged would-apply labels"}).Error())
		}
	}
	if *dry {
		fmt.Fprintln(w, "dry run: nothing fitted or written")
		return nil
	}
	if len(notRun) > 0 {
		return fmt.Errorf("rankfit %s; nothing written", strings.Join(notRun, "; "))
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
		res, err := e.rankfitWeights(snap, me, targets, base, rankfit.Options{Lambda: *lambda, Seed: *seed, HoldoutPct: *holdout, MaxPairs: *maxPairs}, w)
		if err != nil {
			return err
		}
		rep.Weights = &res
		if !res.Beats() {
			return errRankfitNoGain
		}
		f.Shares = &res.Shares
	}
	if err := rankfit.Write(path, f); err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote %s\nto rank by it, set rank_weights_file to that path in the config\n", path)
	return nil
}

// rankfitWeights builds each labelled posting's Score parts under base, as
// `vani jobs` would rank it (the same insight), fits the shares and prints
// the comparison with the hand weights.
func (e *env) rankfitWeights(snap ledger.Snapshot, me map[posting.Key]marks.Entry, targets map[posting.Key]rankfit.Target,
	base jobinsight.Weights, opt rankfit.Options, w io.Writer) (rankfit.FitResult, error) {
	in, err := e.newInsight(jobinsight.DefaultScoreHalfLifeDays, snap, me)
	if err != nil {
		return rankfit.FitResult{}, err
	}
	byKey := make(map[posting.Key]ledger.Record, len(snap.Records))
	for _, r := range snap.Records {
		byKey[r.Key] = r
	}
	var items []rankfit.Item
	absent := 0
	for _, k := range slices.Sorted(maps.Keys(targets)) {
		r, ok := byKey[k]
		if !ok {
			absent++
			continue
		}
		t := targets[k]
		it, err := rankfit.ItemFrom(string(k), jobinsight.ScoreWith(jobinsight.InputsFor(r, in.rc), base), t.Positive, t.Judged)
		if err != nil {
			return rankfit.FitResult{}, err
		}
		items = append(items, it)
	}
	if absent > 0 {
		fmt.Fprintf(w, "weight fit: %d labelled postings are not in the ledger and are left out\n", absent)
	}
	res, err := rankfit.Fit(items, base, opt)
	if err != nil {
		return res, fmt.Errorf("rankfit %w; nothing written", err)
	}
	fmt.Fprintf(w, "weight fit: %d training items, %d of %d pairs used; %d held-out items, %d pairs\n",
		res.TrainItems, res.TrainPairs, res.TrainPairsTotal, res.HeldOutItems, res.HeldOutPairs)
	fmt.Fprintf(w, "held-out pairwise accuracy: fitted %.4f, hand %.4f\n", res.FitAccuracy, res.HandAccuracy)
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
