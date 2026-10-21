package rankfit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Attack 4: where PSelect comes from. A would_apply row with no source (a legacy
// row) is refused by the store on write and counted malformed on read, so it
// never reaches the fit as a silent weight 1; a manual row is the only
// unweighted judgment.
func TestAttackLegacyRowWithoutSourceIsNotSilentlyWeightOne(t *testing.T) {
	dir := t.TempDir()
	s, err := eval.NewStore(filepath.Join(dir, "labels"), filepath.Join(dir, "text"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(eval.Entry{Key: "gh:a:1", Labels: map[string]string{eval.WouldApply: "yes"}}); err == nil {
		t.Error("the store accepted a would_apply label with no source")
	}
	line := `{"v":1,"key":"gh:a:2","labels":{"would_apply":"yes"},"labeled_at":"2026-10-01T00:00:00Z"}` + "\n"
	f, err := os.OpenFile(filepath.Join(dir, "labels", eval.LabelsFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
	f.Close()
	got, bad, err := s.Labels()
	if err != nil {
		t.Fatal(err)
	}
	if bad != 1 || len(got) != 0 {
		t.Errorf("legacy row: bad %d, labels %v", bad, got)
	}
}

// Attack 4: manual and audit judgments mixed in one fit: manual weighs 1, the
// audit's 1/p; the result is finite and deterministic, and the audit stratum
// counts in the strata while the manual one is "unstratified".
func TestAttackMixedManualAndAuditItems(t *testing.T) {
	items := world(21, 900, 600)
	for i := range items {
		switch {
		case i%3 == 0:
			items[i].Source = eval.SourceManual
		case i%3 == 1:
			items[i].Source, items[i].PSelect = "decile:4", 0.2
		default:
			items[i].Source, items[i].PSelect = "dropped:title:role_fit", 0.9
		}
	}
	a, err := Fit(items, jobinsight.DefaultWeights(), Options{Seed: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !resultFinite(a) {
		t.Fatalf("non-finite: %+v", a)
	}
	names := map[string]bool{}
	for _, s := range a.Strata {
		names[s.Stratum] = true
	}
	for _, want := range []string{StratumScored, StratumDropped, "unstratified", "mixed"} {
		if !names[want] {
			t.Errorf("missing stratum %q in %+v", want, a.Strata)
		}
	}
	// A manual item weighs 1 and a p 0.2 one 5: weight() says so.
	if w := items[0].weight(); w != 1 {
		t.Errorf("manual weight %v", w)
	}
	if w := items[1].weight(); w != 5 {
		t.Errorf("audit weight %v", w)
	}
}

// Targets keeps a would_apply label's source and p exactly as read.
func TestAttackTargetsRefusesManualWithP(t *testing.T) {
	labels := map[posting.Key]eval.Entry{"gh:a:x": {Labels: map[string]string{eval.WouldApply: "yes"}, Source: eval.SourceManual, PSelect: 0.5}}
	if _, _, err := Targets(labels, nil, nil); err == nil {
		t.Error("a manual label carrying p_select was accepted")
	}
}
