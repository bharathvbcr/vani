package eval

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.001 }

func TestWilsonKnownIntervals(t *testing.T) {
	cases := []struct {
		k, n   int
		lo, hi float64
	}{
		{5, 10, 0.2366, 0.7634},
		{2, 4, 0.1500, 0.8500},
		{0, 5, 0, 0.4345},
		{3, 3, 0.4385, 1},
		{9, 10, 0.5958, 0.9821},
	}
	for _, c := range cases {
		lo, hi, ok := Wilson(c.k, c.n)
		if !ok || !near(lo, c.lo) || !near(hi, c.hi) {
			t.Errorf("Wilson(%d,%d) = %.4f %.4f %v, want %.4f %.4f", c.k, c.n, lo, hi, ok, c.lo, c.hi)
		}
	}
	for _, c := range [][2]int{{0, 0}, {1, 0}, {-1, 5}, {6, 5}} {
		if _, _, ok := Wilson(c[0], c[1]); ok {
			t.Errorf("Wilson(%d,%d) gave an interval", c[0], c[1])
		}
	}
}

func keysOf(prefix string, n int) []posting.Key {
	var out []posting.Key
	for i := 1; i <= n; i++ {
		out = append(out, posting.Key(fmt.Sprintf("%s%d", prefix, i)))
	}
	return out
}

func lab(m map[posting.Key]Entry, key, verdict, source string, p float64) {
	m[posting.Key(key)] = Entry{Key: posting.Key(key), Labels: map[string]string{WouldApply: verdict}, Source: source, PSelect: p}
}

func fitRec(key, reading, backend, p string) ledger.Record {
	ann := map[string]string{"triage.role_fit": reading, "triage.role_fit.reading": "answered", "triage.role_fit.backend": backend}
	if p != "" {
		ann["triage.role_fit.p"] = p
	}
	return mkRec(key, posting.Scored, ann)
}

func toyLabels() map[posting.Key]Entry {
	m := map[posting.Key]Entry{}
	lab(m, "n1", "yes", "decile:10", 0.5)
	lab(m, "n2", "no", "decile:10", 0.5)
	lab(m, "n3", "maybe", "decile:1", 0.5)
	lab(m, "n4", "skip", "decile:5", 0.5)
	lab(m, "n5", "no", "decile:1", 0.5)
	lab(m, "t1", "yes", "decile:10", 0.5)
	lab(m, "t2", "yes", "decile:10", 0.5)
	lab(m, "t3", "yes", SourceManual, 0)
	lab(m, "d1", "maybe", "dropped:title:role_fit", 0.3)
	lab(m, "d2", "no", "dropped:title:role_fit", 0.3)
	lab(m, "d3", "no", "dropped:title:role_fit", 0.3)
	lab(m, "d4", "no", "dropped:model:seniority", 1)
	m["old"] = Entry{Key: "old", Labels: map[string]string{"work_mode": "remote"}} // a triage label, not would_apply
	return m
}

func TestBuildRelevanceToyCounts(t *testing.T) {
	ords := []Ordering{{Name: "New", Keys: keysOf("n", 10)}, {Name: "Top-open", Keys: keysOf("t", 3)}}
	r := BuildRelevance(toyLabels(), nil, ords, []int{5, 10}, Names{})
	if r.Labels != 12 || r.Judged != 11 || r.Skipped != 1 || r.Manual != 1 {
		t.Fatalf("totals: %+v", r)
	}
	type pk struct {
		o string
		k int
	}
	want := map[pk][3]int{ // shown, wanted, judged
		{"New", 5}: {5, 2, 4}, {"New", 10}: {10, 2, 4},
		{"Top-open", 5}: {3, 3, 3}, {"Top-open", 10}: {3, 3, 3},
	}
	for _, row := range r.Precision {
		w := want[pk{row.Ordering, row.K}]
		if row.Shown != w[0] || row.Wanted != w[1] || row.N != w[2] {
			t.Errorf("%s@%d: shown %d wanted %d/%d, want %v", row.Ordering, row.K, row.Shown, row.Wanted, row.N, w)
		}
	}
	if len(r.Precision) != 4 {
		t.Errorf("%d precision rows, want 4", len(r.Precision))
	}
	// Deciles: highest first; the manual label and the skip are in no stratum.
	if len(r.Deciles) != 2 || r.Deciles[0].Source != "decile:10" || r.Deciles[0].Wanted != 3 || r.Deciles[0].N != 4 ||
		r.Deciles[1].Source != "decile:1" || r.Deciles[1].Wanted != 1 || r.Deciles[1].N != 2 {
		t.Errorf("deciles %+v", r.Deciles)
	}
	if len(r.Drops) != 2 || r.Drops[0].Source != "dropped:model:seniority" || r.Drops[0].Count != (Count{0, 1}) ||
		r.Drops[1].Source != "dropped:title:role_fit" || r.Drops[1].Count != (Count{1, 3}) {
		t.Errorf("drops %+v", r.Drops)
	}
	md := r.Markdown()
	for _, frag := range []string{
		"| New | 5 | 5 | 2/4 = 50% (95% CI 15-85%) |",
		"| Top-open | 5 | 3 | 3/3 = 100% (95% CI 44-100%) |",
		"| dropped:title:role_fit | 1/3 = 33%",
		"| dropped:model:seniority | 0/1 = 0% (95% CI 0-79%) |",
		"no data: no judged posting has a backend role_fit reading",
	} {
		if !strings.Contains(md, frag) {
			t.Errorf("report lacks %q:\n%s", frag, md)
		}
	}
}

func TestBuildRelevanceBackendReliability(t *testing.T) {
	m := map[posting.Key]Entry{}
	lab(m, "a", "yes", "decile:9", 0.5)
	lab(m, "b", "no", "decile:9", 0.5)
	lab(m, "c", "yes", "decile:9", 0.5)
	lab(m, "e", "yes", "decile:9", 0.5)
	lab(m, "f", "no", "decile:9", 0.5)
	recs := []ledger.Record{
		fitRec("a", "strong_fit", "d1-ultra", "0.9000"),  // p 0.9, wanted: (0.1)^2
		fitRec("b", "partial_fit", "d1-ultra", "0.8000"), // p 0.8, not wanted: 0.64
		fitRec("c", "adjacent", "d1-ultra", "0.9000"),    // not a positive reading: not scored
		fitRec("e", "strong_fit", "lappi", ""),           // no p: counted, not scored
		fitRec("f", "strong_fit", "d1-ultra", "1.7"),     // p out of range: not scored
		fitRec("zz", "strong_fit", "d1-ultra", "0.9"),    // not judged: ignored
	}
	r := BuildRelevance(m, recs, nil, nil, Names{})
	if len(r.Backends) != 2 {
		t.Fatalf("backends %+v", r.Backends)
	}
	d1, lp := r.Backends[0], r.Backends[1]
	if d1.Backend != "d1-ultra" || d1.Judged != 4 || d1.Positive != 3 || d1.Scored != 2 || d1.Count != (Count{1, 2}) {
		t.Errorf("d1-ultra %+v", d1)
	}
	if b, ok := d1.Brier(); !ok || !near(b, (0.01+0.64)/2) {
		t.Errorf("Brier %v %v, want 0.325", b, ok)
	}
	// 0.9 falls in the [0.8,1.0) bin with 0.8: two postings, one wanted.
	if bin := d1.Bins[4]; bin.N != 2 || bin.Wanted != 1 || !near(bin.SumP, 1.7) {
		t.Errorf("top bin %+v", bin)
	}
	if lp.Backend != "lappi" || lp.Judged != 1 || lp.Positive != 1 || lp.Scored != 0 {
		t.Errorf("lappi %+v", lp)
	}
	if _, ok := lp.Brier(); ok {
		t.Error("a backend with no p has no Brier score")
	}
	md := r.Markdown()
	if !strings.Contains(md, "| lappi | 1 | 1 | 0 | no data (n=0) | no data | no data |") || !strings.Contains(md, "| d1-ultra | 4 | 3 | 2 | 1/2 = 50%") || !strings.Contains(md, "0.325") {
		t.Errorf("backend table:\n%s", md)
	}
}

func TestBuildRelevanceLegacyBackendNameIsFiledUnderTheCurrentOne(t *testing.T) {
	m := map[posting.Key]Entry{}
	lab(m, "a", "yes", "decile:9", 0.5)
	r := BuildRelevance(m, []ledger.Record{fitRec("a", "strong_fit", LegacyOpenRouterName, "0.9")}, nil, nil, Names{OpenRouter: "d1-ultra"})
	if len(r.Backends) != 1 || r.Backends[0].Backend != "d1-ultra" {
		t.Errorf("%+v", r.Backends)
	}
}

// Nothing judged is "no data", never 0%, NaN or an empty interval.
func TestRelevanceZeroLabelsIsNoData(t *testing.T) {
	skip := map[posting.Key]Entry{}
	lab(skip, "s", "skip", "decile:3", 0.2)
	for name, labels := range map[string]map[posting.Key]Entry{
		"no labels":   nil,
		"only skip":   skip,
		"only triage": {"x": {Key: "x", Labels: map[string]string{"work_mode": "remote"}}},
	} {
		r := BuildRelevance(labels, nil, []Ordering{{Name: "New", Keys: keysOf("n", 3)}}, DefaultPrecisionKs, Names{})
		md := r.Markdown()
		if r.Judged != 0 || !strings.Contains(md, "no data") {
			t.Errorf("%s: %s", name, md)
		}
		for _, bad := range []string{"0%", "NaN", "Inf", "| New"} {
			if strings.Contains(md, bad) {
				t.Errorf("%s: report contains %q:\n%s", name, bad, md)
			}
		}
	}
	// An ordering with labels elsewhere but none in its top k says no data for it.
	m := map[posting.Key]Entry{}
	lab(m, "elsewhere", "yes", "decile:2", 0.1)
	md := BuildRelevance(m, nil, []Ordering{{Name: "New", Keys: keysOf("n", 3)}, {Name: "Empty"}}, []int{5}, Names{}).Markdown()
	if !strings.Contains(md, "| New | 5 | 3 | no data (n=0) |") || !strings.Contains(md, "| Empty | 5 | 0 | no data (n=0) |") {
		t.Errorf("empty top-k:\n%s", md)
	}
}

// The triage report and the held-out split are over triage labels only; the
// audit's would_apply entries must not move them.
func TestWouldApplyEntriesDoNotMoveTheTriageReport(t *testing.T) {
	s, _, _ := newStore(t)
	tri := map[string]string{"triage.work_mode": "remote", "triage.work_mode.reading": "answered", "triage.work_mode.backend": "d1-ultra"}
	recs := []ledger.Record{mkRec("a", posting.Triaged, tri), mkRec("b", posting.Triaged, tri)}
	if err := s.Put(Entry{Key: "a", Labels: map[string]string{"work_mode": "remote"}}); err != nil {
		t.Fatal(err)
	}
	before := BuildReport(recs, s, Names{})
	if err := s.Put(Entry{Key: "b", Labels: map[string]string{WouldApply: "yes"}, Source: "decile:1", PSelect: 0.5}); err != nil {
		t.Fatal(err)
	}
	after := BuildReport(recs, s, Names{})
	if before.LabelEntries != 1 || after.LabelEntries != 1 || after.Matched != 1 || after.NoTriage != 0 {
		t.Errorf("before %+v after %+v", before, after)
	}
	sp := Split{Name: "toy", Size: 1}
	_, h1, err := BuildHeldOut(recs, s, Names{}, sp)
	if err != nil || h1.Ranked != 1 {
		t.Errorf("held-out ranked %d err %v, want 1", h1.Ranked, err)
	}
}
