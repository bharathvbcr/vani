package eval

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The bound is the one-sided 95% exact lower limit of a binomial proportion.
// The reference figures were computed independently by bisection on the
// binomial tail (not from this code).
func TestLowerBound(t *testing.T) {
	for _, tc := range []struct {
		k, n int
		want float64
	}{
		{10, 20, 0.302}, {15, 20, 0.544}, {12, 20, 0.394}, {25, 50, 0.376},
		{35, 50, 0.576}, {5, 10, 0.222}, {8, 10, 0.493},
		{10, 10, math.Pow(0.05, 1.0/10)}, // all successes: alpha^(1/n)
	} {
		if got := lowerBound(tc.k, tc.n, 0.05); math.Abs(got-tc.want) > 0.001 {
			t.Errorf("lowerBound(%d, %d) = %.4f, want %.3f", tc.k, tc.n, got, tc.want)
		}
	}
	for _, tc := range [][2]int{{0, 20}, {-1, 20}, {5, 0}, {5, -3}, {21, 20}} {
		if got := lowerBound(tc[0], tc[1], 0.05); got != 0 {
			t.Errorf("lowerBound(%d, %d) = %v, want 0 for an impossible or empty count", tc[0], tc[1], got)
		}
	}
	// A larger sample with the same share gives a tighter, higher bound.
	if small, large := lowerBound(12, 20, 0.05), lowerBound(120, 200, 0.05); !(large > small) {
		t.Errorf("bound did not tighten with n: %v then %v", small, large)
	}
}

// shadowRec is a labeled work_mode posting the primary (jev) answered with got
// and a shadow backend recorded with the given reading and value.
func shadowRec(t *testing.T, s *Store, key, label, got, backend, reading, value string) ledger.Record {
	t.Helper()
	if err := s.Put(Entry{Key: posting.Key(key), Labels: map[string]string{"work_mode": label}}); err != nil {
		t.Fatal(err)
	}
	r := jevRec(key, "work_mode", got, "0.9000", "", true)
	r.Annotations["triage.work_mode.shadow_reading"] = reading
	r.Annotations["triage.work_mode.shadow_backend"] = backend
	if value != "" {
		r.Annotations["triage.work_mode.shadow"] = value
	}
	return r
}

// A shadow that was asked and unavailable raises "asked" without raising
// "answered", so its coverage is honest and the coverage rule applies.
func TestUnavailableShadowIsAskedNotAnswered(t *testing.T) {
	s, _, _ := newStore(t)
	var recs []ledger.Record
	for i := 0; i < 40; i++ {
		key := fmt.Sprintf("U%02d", i)
		if i < 10 {
			recs = append(recs, shadowRec(t, s, key, "remote", "remote", "local-m-combined", "unavailable", ""))
		} else {
			recs = append(recs, shadowRec(t, s, key, "remote", "remote", "local-m-combined", "answered", "remote"))
		}
	}
	rep := BuildReport(recs, s, Names{})
	if len(rep.Promotions) != 1 {
		t.Fatalf("promotions: %+v", rep.Promotions)
	}
	p := rep.Promotions[0]
	if p.Asked != 40 || p.ShadowUnavailable != 10 || p.ShadowAnswered != 30 || p.Paired != 30 || p.PrimaryAnswered != 40 {
		t.Fatalf("row: %+v", p)
	}
	if v := p.Verdict(); v != "not promotable: coverage 30/40 < primary 40/40" {
		t.Fatalf("verdict %q", v)
	}
	md := rep.Markdown()
	for _, frag := range []string{
		"| 100.0% (40/40) | 10 | 30 | 0 | 0 | not promotable: coverage 30/40 < primary 40/40 |",
		"shadow unavailable",
	} {
		if !strings.Contains(md, frag) {
			t.Errorf("markdown lacks %q\n%s", frag, md)
		}
	}
	// The unavailable postings are in the shadow row's denominator too.
	if len(rep.Shadow) != 1 || rep.Shadow[0].Labeled != 40 || rep.Shadow[0].ShadowAnswered != 30 {
		t.Fatalf("shadow rows: %+v", rep.Shadow)
	}
}

// Two candidate shadow models over the same primary are two rows in both
// tables, each needing its own labels; they never pool.
func TestShadowModelsDoNotPool(t *testing.T) {
	s, _, _ := newStore(t)
	var recs []ledger.Record
	for i := 0; i < 35; i++ {
		recs = append(recs, shadowRec(t, s, fmt.Sprintf("A%02d", i), "remote", "remote", "local-a-combined", "answered", "remote"))
		recs = append(recs, shadowRec(t, s, fmt.Sprintf("B%02d", i), "remote", "remote", "local-b-per_slot", "answered", "onsite"))
	}
	for i := 0; i < 5; i++ { // a third model with too few labels for a verdict of its own
		recs = append(recs, shadowRec(t, s, fmt.Sprintf("C%02d", i), "remote", "remote", "local-c-combined", "answered", "remote"))
	}
	rep := BuildReport(recs, s, Names{})
	if len(rep.Promotions) != 3 || len(rep.Shadow) != 3 {
		t.Fatalf("promotions %+v, shadow rows %+v", rep.Promotions, rep.Shadow)
	}
	want := []struct {
		shadow         string
		asked, correct int
	}{{"local-a-combined", 35, 35}, {"local-b-per_slot", 35, 0}, {"local-c-combined", 5, 5}}
	for i, w := range want {
		p, sh := rep.Promotions[i], rep.Shadow[i]
		if p.Shadow != w.shadow || p.Asked != w.asked || p.ShadowCorrect != w.correct {
			t.Errorf("promotion %d: %+v, want %+v", i, p, w)
		}
		if sh.Shadow != w.shadow || sh.Labeled != w.asked || sh.ShadowCorrect != w.correct {
			t.Errorf("shadow row %d: %+v, want %+v", i, sh, w)
		}
	}
	if v := rep.Promotions[2].Verdict(); !strings.HasPrefix(v, "insufficient labels (5/30)") {
		t.Errorf("third model verdict %q", v)
	}
}

// Paired counts use only postings both sides answered, and split the ones
// where exactly one matched the label.
func TestPairedCounts(t *testing.T) {
	s, _, _ := newStore(t)
	recs := []ledger.Record{
		shadowRec(t, s, "p1", "remote", "remote", "local-m-combined", "answered", "onsite"), // primary only
		shadowRec(t, s, "p2", "remote", "onsite", "local-m-combined", "answered", "remote"), // shadow only
		shadowRec(t, s, "p3", "remote", "remote", "local-m-combined", "answered", "remote"), // both right
		shadowRec(t, s, "p4", "remote", "onsite", "local-m-combined", "answered", "hybrid"), // both wrong
		shadowRec(t, s, "p5", "remote", "remote", "local-m-combined", "abstained", ""),      // shadow did not answer
		shadowRec(t, s, "p6", "remote", "remote", "local-m-combined", "unavailable", ""),    // shadow failed
	}
	rep := BuildReport(recs, s, Names{})
	if len(rep.Promotions) != 1 {
		t.Fatalf("promotions: %+v", rep.Promotions)
	}
	p := rep.Promotions[0]
	if p.Asked != 6 || p.Paired != 4 || p.PrimaryOnly != 1 || p.ShadowOnly != 1 || p.Discordant() != 2 ||
		p.ShadowAnswered != 4 || p.ShadowUnavailable != 1 || p.PrimaryAnswered != 6 {
		t.Fatalf("row: %+v", p)
	}
}

// Rows recorded under the pre-naming shadow backend are read as written and the
// report says what that name may pool.
func TestLegacyLocalShadowRowIsExplained(t *testing.T) {
	s, _, _ := newStore(t)
	var recs []ledger.Record
	for i := 0; i < 3; i++ {
		recs = append(recs, shadowRec(t, s, fmt.Sprintf("L%d", i), "remote", "remote", LegacyLocalShadow, "answered", "remote"))
	}
	md := BuildReport(recs, s, Names{}).Markdown()
	if !strings.Contains(md, `Shadow "local" is a row recorded before the model was named`) {
		t.Fatalf("no legacy note:\n%s", md)
	}
	recs = nil
	for i := 0; i < 3; i++ {
		recs = append(recs, shadowRec(t, s, fmt.Sprintf("N%d", i), "remote", "remote", "local-m-combined", "answered", "remote"))
	}
	if md := BuildReport(recs, s, Names{}).Markdown(); strings.Contains(md, "recorded before the model was named") {
		t.Fatalf("legacy note on a named shadow:\n%s", md)
	}
}
