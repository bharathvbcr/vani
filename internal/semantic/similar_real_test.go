//go:build tessl && darwin && arm64

package semantic

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// embedGemmaSnapshot is the model the cutoffs were measured on.
func embedGemmaSnapshot(t *testing.T) (string, bool) {
	t.Helper()
	if s := os.Getenv("VANI_TESSL_SNAPSHOT"); s != "" {
		return s, true
	}
	home, err := os.UserHomeDir()
	if err == nil {
		m, _ := filepath.Glob(filepath.Join(home, ".cache", "huggingface", "hub", "models--google--embeddinggemma-2", "snapshots", "*"))
		if len(m) == 1 {
			return m[0], true
		}
	}
	return "", false
}

// openCalibrationModel opens the real model. Without the snapshot the test is
// skipped, and says so; under VANI_EMBED_REQUIRE=1 that is a failure, so a CI
// that is meant to measure cannot pass by not measuring.
func openCalibrationModel(t *testing.T) *Tessl {
	t.Helper()
	snap, ok := embedGemmaSnapshot(t)
	if !ok {
		if os.Getenv("VANI_EMBED_REQUIRE") == "1" {
			t.Fatal("VANI_EMBED_REQUIRE=1 but no embeddinggemma-2 snapshot: set VANI_TESSL_SNAPSHOT")
		}
		t.Skip("SKIPPED, NOT MEASURED: no embeddinggemma-2 snapshot (set VANI_TESSL_SNAPSHOT); the cutoffs were not checked")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	start := time.Now()
	e, err := OpenTessl(ctx, TesslConfig{Snapshot: snap})
	if err != nil {
		if os.Getenv("VANI_EMBED_REQUIRE") == "1" {
			t.Fatalf("open: %v", err)
		}
		t.Skipf("SKIPPED, NOT MEASURED: the model did not open: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	t.Logf("model %s opened in %v", e.Model(), time.Since(start).Round(time.Millisecond))
	return e
}

// What was measured on embeddinggemma-2 with the fixture, recorded so a model,
// prompt or fixture change that moves them fails loudly instead of drifting.
// Set from the "calibration" output of TestRealLabelCalibration.
const (
	measuredMaxNeg = 0.9646 // highest different-question cosine in the fixture
	measuredMinPos = 0.8682 // lowest same-question cosine in the fixture
)

func TestRealLabelCalibration(t *testing.T) {
	e := openCalibrationModel(t)
	pairs := loadLabelPairs(t)
	var texts []string
	at := map[string]int{}
	for _, p := range pairs {
		for _, s := range []string{p.A, p.B} {
			if _, ok := at[s]; !ok {
				at[s] = len(texts)
				texts = append(texts, s)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var rep Report
	start := time.Now()
	rows, err := embedDistinct(ctx, e, nil, texts, &rep)
	if err != nil {
		t.Fatalf("embed: %v (%+v)", err, rep)
	}
	t.Logf("embedded %d distinct labels in %v", len(texts), time.Since(start).Round(time.Millisecond))
	cos := make([]float64, len(pairs))
	for i, p := range pairs {
		cos[i] = dot(rows[at[p.A]], rows[at[p.B]])
	}

	all := calibrate(pairs, cos, 0.01)
	var devPairs, holdPairs []labelPair
	var devCos, holdCos []float64
	for i, p := range pairs {
		if isHoldout(p) {
			holdPairs, holdCos = append(holdPairs, p), append(holdCos, cos[i])
		} else {
			devPairs, devCos = append(devPairs, p), append(devCos, cos[i])
		}
	}
	dev, hold := calibrate(devPairs, devCos, 0.01), calibrate(holdPairs, holdCos, 0.01)

	var b strings.Builder
	fmt.Fprintf(&b, "\ncalibration on %d pairs (%d same, %d different)\n", len(pairs), all.PosCount, all.NegCount)
	fmt.Fprintf(&b, "  highest different-question cos %.4f, lowest same-question cos %.4f\n", all.MaxNeg, all.MinPos)
	fmt.Fprintf(&b, "  zero-false-merge cutoff %.4f keeps %.1f%% of same-question pairs\n", all.ZeroFPCut, 100*all.ZeroFPRecall)
	fmt.Fprintf(&b, "  dev split (%d pairs): max neg %.4f, zero-FP cut %.4f, recall %.1f%%\n", len(devPairs), dev.MaxNeg, dev.ZeroFPCut, 100*dev.ZeroFPRecall)
	fmt.Fprintf(&b, "  holdout   (%d pairs): max neg %.4f, zero-FP cut %.4f, recall %.1f%%\n", len(holdPairs), hold.MaxNeg, hold.ZeroFPCut, 100*hold.ZeroFPRecall)
	fmt.Fprintf(&b, "  cutoff  precision  recall  falseMerges  (of %d different-question pairs)\n", all.NegCount)
	for _, pt := range all.Points {
		if pt.TP+pt.FP == 0 {
			continue
		}
		fmt.Fprintf(&b, "  %.2f    %6.1f%%   %5.1f%%   %3d (%.0f%%)\n", pt.Cut, 100*pt.Precision, 100*pt.Recall, pt.FP, pt.FalseMergesPerNegPct)
	}
	type scored struct {
		cos float64
		p   labelPair
	}
	var neg, pos []scored
	for i, p := range pairs {
		if p.Same {
			pos = append(pos, scored{cos[i], p})
		} else {
			neg = append(neg, scored{cos[i], p})
		}
	}
	sort.Slice(neg, func(i, j int) bool { return neg[i].cos > neg[j].cos })
	sort.Slice(pos, func(i, j int) bool { return pos[i].cos < pos[j].cos })
	fmt.Fprintf(&b, "  ten highest different-question pairs:\n")
	for _, s := range neg[:min(10, len(neg))] {
		fmt.Fprintf(&b, "    %.4f  %q ~ %q\n", s.cos, s.p.A, s.p.B)
	}
	fmt.Fprintf(&b, "  ten lowest same-question pairs:\n")
	for _, s := range pos[:min(10, len(pos))] {
		fmt.Fprintf(&b, "    %.4f  %q ~ %q\n", s.cos, s.p.A, s.p.B)
	}
	t.Log(b.String())

	if math.Abs(all.MaxNeg-measuredMaxNeg) > 0.01 {
		t.Errorf("highest different-question cosine is %.4f, recorded %.4f: re-measure and re-set the cutoffs", all.MaxNeg, measuredMaxNeg)
	}
	if math.Abs(all.MinPos-measuredMinPos) > 0.01 {
		t.Errorf("lowest same-question cosine is %.4f, recorded %.4f", all.MinPos, measuredMinPos)
	}

	// The two cutoffs, against both halves.
	for name, c := range map[string]struct {
		pairs []labelPair
		cos   []float64
	}{"all": {pairs, cos}, "dev": {devPairs, devCos}, "holdout": {holdPairs, holdCos}} {
		for i, p := range c.pairs {
			if !p.Same && TierPair(p.A, p.B, c.cos[i]) == "likely" {
				t.Errorf("%s: LIKELY (%.3f) merges %q with %q at %.4f", name, LikelyCos, p.A, p.B, c.cos[i])
			}
		}
	}
	var likelyTP, possibleTP, possibleFP int
	for i, p := range pairs {
		if p.Same && TierPair(p.A, p.B, cos[i]) == "likely" {
			likelyTP++
		}
		if cos[i] >= PossibleCos {
			if p.Same {
				possibleTP++
			} else {
				possibleFP++
			}
		}
	}
	t.Logf("LIKELY %.3f: %d/%d same-question pairs, 0 false merges; POSSIBLE %.3f: %d/%d same-question pairs, %d false merges",
		LikelyCos, likelyTP, all.PosCount, PossibleCos, possibleTP, all.PosCount, possibleFP)
	// With the lexical veto: a vetoed pair can only be "possible", so the "likely"
	// tier is what is left. This is informational for choosing LikelyCos; the
	// veto's words were read off these same negatives, so it is not held out.
	var kept []labelPair
	var keptCos []float64
	vetoedPos, vetoedNeg := 0, 0
	for i, p := range pairs {
		if LexicalVeto(p.A, p.B) != "" {
			if p.Same {
				vetoedPos++
			} else {
				vetoedNeg++
			}
			continue
		}
		kept, keptCos = append(kept, p), append(keptCos, cos[i])
	}
	withVeto := calibrate(kept, keptCos, 0.01)
	var vb strings.Builder
	fmt.Fprintf(&vb, "\nwith the lexical veto (%d same-question and %d different-question pairs vetoed to possible)\n", vetoedPos, vetoedNeg)
	fmt.Fprintf(&vb, "  highest unvetoed different-question cos %.4f; zero-false-merge cutoff %.4f would keep %d of %d same-question pairs\n",
		withVeto.MaxNeg, withVeto.ZeroFPCut, int(math.Round(withVeto.ZeroFPRecall*float64(withVeto.PosCount))), all.PosCount)
	fmt.Fprintf(&vb, "  cutoff  likely-tier precision  recall(of %d)  falseMerges\n", all.PosCount)
	for _, pt := range withVeto.Points {
		if pt.TP+pt.FP == 0 || pt.Cut < 0.90 {
			continue
		}
		fmt.Fprintf(&vb, "  %.2f    %6.1f%%              %5.1f%%        %d\n", pt.Cut, 100*pt.Precision, 100*float64(pt.TP)/float64(all.PosCount), pt.FP)
	}
	t.Log(vb.String())
	var tierLikelyTP, tierLikelyFP int
	for i, p := range pairs {
		if TierPair(p.A, p.B, cos[i]) == "likely" {
			if p.Same {
				tierLikelyTP++
			} else {
				tierLikelyFP++
			}
		}
	}
	t.Logf("TierPair at LIKELY %.2f with the veto: %d of %d same-question pairs, %d false merges", LikelyCos, tierLikelyTP, all.PosCount, tierLikelyFP)
	if tierLikelyFP != 0 {
		t.Errorf("%d false merges at the likely tier", tierLikelyFP)
	}

	// The tiers still do what the doc on LikelyCos says they do.
	if likelyTP < 15 || possibleTP < 50 {
		t.Errorf("tiers kept %d (likely) and %d (possible) same-question pairs, measured 15 and 50 of %d", likelyTP, possibleTP, all.PosCount)
	}
	// What a bank-sized list looks like: every pair among the fixture's distinct
	// labels, unlabelled (some of these really are the same question).
	var allLikely, allPossible int
	for i := range rows {
		for j := i + 1; j < len(rows); j++ {
			switch TierPair(texts[i], texts[j], dot(rows[i], rows[j])) {
			case "likely":
				allLikely++
				allPossible++
			case "possible":
				allPossible++
			}
		}
	}
	t.Logf("all %d pairs of the %d distinct labels: %d at LIKELY, %d at POSSIBLE or above", len(rows)*(len(rows)-1)/2, len(rows), allLikely, allPossible)
}

// TestRealSimilarPairsEndToEnd runs the whole pass on the real model, through a
// vector cache: labels that share a concept come out as likely or possible
// pairs, the first-name/last-name trap does not come out as likely, and a second
// pass embeds nothing.
func TestRealSimilarPairsEndToEnd(t *testing.T) {
	e := openCalibrationModel(t)
	texts := []string{"LinkedIn profile URL", "LinkedIn", "First name", "Last name", "Years of experience", "Phone number", "Email address", "GitHub URL"}
	c, err := OpenVectorCache(filepath.Join(t.TempDir(), "semantic"), 0)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	ps, rep, err := SimilarPairs(context.Background(), e, c, texts, PossibleCos)
	if err != nil {
		t.Fatalf("%v %+v", err, rep)
	}
	t.Logf("%d pairs at %.3f in %v: %+v", len(ps), PossibleCos, time.Since(start).Round(time.Millisecond), ps)
	found := false
	for _, p := range ps {
		a, b := texts[p.A], texts[p.B]
		if (a == "LinkedIn profile URL" && b == "LinkedIn") || (a == "LinkedIn" && b == "LinkedIn profile URL") {
			found = true
		}
		if TierPair(a, b, p.Cos) == "likely" && strings.Contains(a, "name") && strings.Contains(b, "name") {
			t.Errorf("first/last name came out LIKELY: %v", p)
		}
	}
	if !found {
		t.Errorf("LinkedIn ~ LinkedIn profile URL missing at POSSIBLE: %v", ps)
	}
	_, rep2, err := SimilarPairs(context.Background(), e, c, texts, PossibleCos)
	if err != nil || rep2.Scored != 0 || rep2.Cached != len(texts) {
		t.Errorf("second pass: %+v %v", rep2, err)
	}
}
