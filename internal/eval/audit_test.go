package eval

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func scoredPool(n int) []AuditCandidate {
	var out []AuditCandidate
	for i := 0; i < n; i++ {
		out = append(out, AuditCandidate{Key: posting.Key(fmt.Sprintf("gh:s:%03d", i)), Score: float64(i)})
	}
	return out
}

func droppedPool(sizes map[string]int) []AuditCandidate {
	var out []AuditCandidate
	for class, n := range sizes {
		for i := 0; i < n; i++ {
			out = append(out, AuditCandidate{Key: posting.Key(fmt.Sprintf("gh:d:%s:%03d", class, i)), Dropped: true, Class: class})
		}
	}
	return out
}

func srcCounts(p AuditPlan) map[string]int {
	m := map[string]int{}
	for _, pk := range p.Picks {
		m[pk.Source]++
	}
	return m
}

func pickKeys(p AuditPlan) []string {
	var out []string
	for _, pk := range p.Picks {
		out = append(out, string(pk.Key)+"@"+pk.Source)
	}
	return out
}

func TestAuditSampleIsDeterministicBySeed(t *testing.T) {
	cands := append(scoredPool(200), droppedPool(map[string]int{"title:role_fit": 60, "model:role_fit": 40, "model:seniority": 25})...)
	a := AuditSample(cands, nil, "2026-W43", 5, 10)
	b := AuditSample(cands, nil, "2026-W43", 5, 10)
	if strings.Join(pickKeys(a), ",") != strings.Join(pickKeys(b), ",") {
		t.Fatalf("same seed, different picks:\n%v\n%v", pickKeys(a), pickKeys(b))
	}
	// Input order is not an input.
	rev := make([]AuditCandidate, len(cands))
	for i, c := range cands {
		rev[len(cands)-1-i] = c
	}
	if c := AuditSample(rev, nil, "2026-W43", 5, 10); strings.Join(pickKeys(a), ",") != strings.Join(pickKeys(c), ",") {
		t.Fatalf("candidate order changed the draw")
	}
	other := AuditSample(cands, nil, "2026-W44", 5, 10)
	if strings.Join(pickKeys(a), ",") == strings.Join(pickKeys(other), ",") {
		t.Errorf("a new seed drew the identical 15")
	}
}

func TestAuditSampleStratumCounts(t *testing.T) {
	cands := append(scoredPool(200), droppedPool(map[string]int{"title:role_fit": 60, "model:role_fit": 40, "model:seniority": 25})...)
	p := AuditSample(cands, nil, "s", 5, 10)
	if len(p.Picks) != 15 {
		t.Fatalf("%d picks, want 15: %v", len(p.Picks), pickKeys(p))
	}
	deciles, drops := map[string]int{}, map[string]int{}
	for _, pk := range p.Picks {
		switch {
		case strings.HasPrefix(pk.Source, "decile:"):
			deciles[pk.Source]++
		case strings.HasPrefix(pk.Source, "dropped:"):
			drops[pk.Source]++
		default:
			t.Errorf("source %q", pk.Source)
		}
	}
	nd, nx := 0, 0
	for _, v := range deciles {
		nd += v
		if v != 1 {
			t.Errorf("a decile gave %d picks, want 1 (5 of 10 deciles)", v)
		}
	}
	for _, v := range drops {
		nx += v
	}
	if nd != 5 || len(deciles) != 5 || nx != 10 || len(drops) != 3 {
		t.Fatalf("deciles %v drops %v", deciles, drops)
	}
	// 10 over 3 classes: 3,3,3 plus one extra, never 0 for a class.
	for c, v := range drops {
		if v < 3 || v > 4 {
			t.Errorf("%s got %d, want 3 or 4", c, v)
		}
	}
	// Source is a valid label source.
	for _, pk := range p.Picks {
		if !validSource(pk.Source) || !(pk.PSelect > 0 && pk.PSelect <= 1) {
			t.Errorf("pick %+v is not storable", pk)
		}
	}
}

func TestAuditSampleSmallAndEmptyStrata(t *testing.T) {
	if p := AuditSample(nil, nil, "s", 5, 10); len(p.Picks) != 0 || len(p.Strata) != 0 {
		t.Errorf("empty pool: %+v", p)
	}
	// 3 scored postings fill three deciles with one member each: all taken, p=1.
	p := AuditSample(scoredPool(3), nil, "s", 5, 10)
	if len(p.Picks) != 3 {
		t.Fatalf("3 scored: %v", pickKeys(p))
	}
	for _, pk := range p.Picks {
		if pk.PSelect != 1 {
			t.Errorf("a stratum taken whole has p_select 1, got %v", pk.PSelect)
		}
	}
	// No dropped postings: the dropped quota is not lent to the scored side
	// (that would break the inclusion probabilities).
	if p := AuditSample(scoredPool(100), nil, "s", 5, 10); len(p.Picks) != 5 {
		t.Errorf("no dropped pool: %d picks", len(p.Picks))
	}
	// Only dropped postings.
	p = AuditSample(droppedPool(map[string]int{"title:role_fit": 2}), nil, "s", 5, 10)
	if len(p.Picks) != 2 {
		t.Errorf("dropped-only: %v", pickKeys(p))
	}
	// Zero or negative quotas draw nothing.
	if p := AuditSample(scoredPool(50), nil, "s", 0, -3); len(p.Picks) != 0 {
		t.Errorf("zero quota drew %v", pickKeys(p))
	}
	// A candidate with no key, or a hostile class, never produces an unstorable pick.
	hostile := []AuditCandidate{{Key: "", Score: 1}, {Key: "gh:x:1", Dropped: true, Class: "a b\n" + strings.Repeat("x", 100)}}
	for _, pk := range AuditSample(hostile, nil, "s", 5, 10).Picks {
		if pk.Key == "" || !validSource(pk.Source) {
			t.Errorf("unstorable pick %+v", pk)
		}
	}
}

func TestAuditSampleNeverRepeatsLabelledOrDuplicates(t *testing.T) {
	cands := append(scoredPool(40), droppedPool(map[string]int{"title:role_fit": 12})...)
	cands = append(cands, cands[:10]...) // duplicate rows
	labeled := map[posting.Key]Entry{}
	first := AuditSample(cands, nil, "s", 5, 10)
	for _, pk := range first.Picks {
		labeled[pk.Key] = Entry{Key: pk.Key}
	}
	// Label half of everything else too.
	for i, c := range cands {
		if i%2 == 0 {
			labeled[c.Key] = Entry{Key: c.Key}
		}
	}
	for _, seed := range []string{"s", "t", "u", "v"} {
		seen := map[posting.Key]bool{}
		for _, pk := range AuditSample(cands, labeled, seed, 5, 10).Picks {
			if _, done := labeled[pk.Key]; done {
				t.Fatalf("seed %s re-drew labelled %s", seed, pk.Key)
			}
			if seen[pk.Key] {
				t.Fatalf("seed %s drew %s twice", seed, pk.Key)
			}
			seen[pk.Key] = true
		}
	}
	// Everything labelled: nothing to draw, no error, no p_select division by zero.
	all := map[posting.Key]Entry{}
	for _, c := range cands {
		all[c.Key] = Entry{Key: c.Key}
	}
	if p := AuditSample(cands, all, "s", 5, 10); len(p.Picks) != 0 {
		t.Errorf("all labelled: %v", pickKeys(p))
	}
}

// The recorded p_select is the probability over the seed, not a label: the
// empirical inclusion frequency of each posting converges to it.
func TestAuditSamplePSelectIsTheInclusionProbability(t *testing.T) {
	cands := append(scoredPool(20), droppedPool(map[string]int{"title:role_fit": 5, "model:role_fit": 1, "model:seniority": 2})...)
	const seeds = 6000
	freq := map[posting.Key]int{}
	psel := map[posting.Key]float64{}
	for i := 0; i < seeds; i++ {
		for _, pk := range AuditSample(cands, nil, fmt.Sprintf("seed-%d", i), 5, 4).Picks {
			freq[pk.Key]++
			if old, ok := psel[pk.Key]; ok && old != pk.PSelect {
				t.Fatalf("p_select of %s changed across seeds: %v vs %v", pk.Key, old, pk.PSelect)
			}
			psel[pk.Key] = pk.PSelect
		}
	}
	var keys []string
	for k := range psel {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	for _, k := range keys {
		got := float64(freq[posting.Key(k)]) / seeds
		want := psel[posting.Key(k)]
		if math.Abs(got-want) > 0.04 {
			t.Errorf("%s: drawn in %.3f of seeds, p_select says %.3f", k, got, want)
		}
	}
	// Every posting of the pool can be drawn (p_select > 0 for all, none starved).
	if len(keys) != len(cands) {
		t.Errorf("%d of %d postings ever drawn", len(keys), len(cands))
	}
}

func TestAuditPlanReportsStrata(t *testing.T) {
	cands := append(scoredPool(100), droppedPool(map[string]int{"title:role_fit": 8, "model:role_fit": 2})...)
	p := AuditSample(cands, nil, "s", 5, 10)
	got := map[string]AuditStratum{}
	for _, st := range p.Strata {
		got[st.Source] = st
	}
	if st := got["dropped:model:role_fit"]; st.Pool != 2 || st.Taken < 1 {
		t.Errorf("model:role_fit stratum %+v", st)
	}
	if st := got["dropped:title:role_fit"]; st.Pool != 8 || st.Taken < 1 {
		t.Errorf("title:role_fit stratum %+v", st)
	}
	total := 0
	for _, st := range p.Strata {
		total += st.Taken
	}
	if total != len(p.Picks) {
		t.Errorf("strata account for %d of %d picks", total, len(p.Picks))
	}
	if c := srcCounts(p); len(c) == 0 {
		t.Error("no picks")
	}
}

func TestWeekSeedAndInterleaved(t *testing.T) {
	if got := WeekSeed(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)); got != "2026-W43" {
		t.Errorf("WeekSeed = %q", got)
	}
	// ISO year boundary: 2027-01-01 is in week 53 of 2026.
	if got := WeekSeed(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)); got != "2026-W53" {
		t.Errorf("WeekSeed at the year boundary = %q", got)
	}
	cands := append(scoredPool(100), droppedPool(map[string]int{"title:role_fit": 30, "model:role_fit": 30})...)
	p := AuditSample(cands, nil, "s", 5, 10)
	a, b := p.Interleaved("s"), p.Interleaved("s")
	if len(a) != len(p.Picks) || fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("interleave not stable or lost picks: %d of %d", len(a), len(p.Picks))
	}
	// Same set, and the 5 scored are not all ahead of the 10 dropped.
	seen := map[posting.Key]bool{}
	for _, pk := range a {
		seen[pk.Key] = true
	}
	for _, pk := range p.Picks {
		if !seen[pk.Key] {
			t.Errorf("%s lost", pk.Key)
		}
	}
	lead := 0
	for _, pk := range a[:5] {
		if strings.HasPrefix(pk.Source, "decile:") {
			lead++
		}
	}
	if lead == 5 {
		t.Errorf("order still groups strata: %v", a[:5])
	}
}
