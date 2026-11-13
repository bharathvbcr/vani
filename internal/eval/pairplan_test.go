package eval

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// planCands makes n candidates per stratum for each stratum name.
func planCands(strata map[string]int) []PairCandidate {
	var out []PairCandidate
	for s, n := range strata {
		for i := range n {
			k := fmt.Sprintf("%s#%d", s, i)
			out = append(out, PairCandidate{Posting: posting.Posting{Key: posting.Key(k), Title: k, Description: "d"}, Stratum: s})
		}
	}
	return out
}

func allPending(PlannedAsk) bool { return true }

func TestPlanPairsStaysWithinOneStratumAndIsBounded(t *testing.T) {
	cands := planCands(map[string]int{"decile:1": 10, "decile:2": 10, "dropped:title:role_fit": 7})
	asks, st := PlanPairs(cands, PairPlanOpts{Seed: "s", N: 9}, allPending)
	if len(asks) != 9 || st.Pairs != 9 {
		t.Fatalf("%d asks, %d pairs, want 9", len(asks), st.Pairs)
	}
	stratumOf := map[posting.Key]string{}
	for _, c := range cands {
		stratumOf[c.Posting.Key] = c.Stratum
	}
	seen := map[[2]posting.Key]bool{}
	used := map[posting.Key]int{}
	for _, a := range asks {
		if a.A.Key == a.B.Key {
			t.Fatalf("a pair of one posting: %v", a.A.Key)
		}
		if stratumOf[a.A.Key] != a.Stratum || stratumOf[a.B.Key] != a.Stratum {
			t.Errorf("pair %s/%s crosses strata (%s, %s, %s)", a.A.Key, a.B.Key, stratumOf[a.A.Key], stratumOf[a.B.Key], a.Stratum)
		}
		lo, hi := PairKeyOf(a.A.Key, a.B.Key)
		if seen[[2]posting.Key{lo, hi}] {
			t.Errorf("pair %s/%s planned twice", lo, hi)
		}
		seen[[2]posting.Key{lo, hi}] = true
		used[a.A.Key]++
		used[a.B.Key]++
	}
	for k, n := range used {
		if n > 1 {
			t.Errorf("%s is in %d pairs of a stratum round-robin", k, n)
		}
	}
	// Round-robin: nine pairs over three strata are three each.
	per := map[string]int{}
	for _, a := range asks {
		per[a.Stratum]++
	}
	if per["decile:1"] != 3 || per["decile:2"] != 3 || per["dropped:title:role_fit"] != 3 {
		t.Errorf("pairs per stratum %v", per)
	}
}

func TestPlanPairsIsDeterministicAndSeedDependent(t *testing.T) {
	cands := planCands(map[string]int{"decile:1": 20, "decile:2": 20})
	key := func(asks []PlannedAsk) (s string) {
		for _, a := range asks {
			s += string(a.A.Key) + ">" + string(a.B.Key) + ";"
		}
		return s
	}
	a1, _ := PlanPairs(cands, PairPlanOpts{Seed: "s", N: 8}, allPending)
	rev := append([]PairCandidate(nil), cands...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	a2, _ := PlanPairs(rev, PairPlanOpts{Seed: "s", N: 8}, allPending)
	if key(a1) != key(a2) {
		t.Error("the plan depends on the order the candidates came in")
	}
	a3, _ := PlanPairs(cands, PairPlanOpts{Seed: "another", N: 8}, allPending)
	if key(a1) == key(a3) {
		t.Error("a different seed drew the same pairs")
	}
}

func TestPlanPairsOrderIsBalancedAndBothOrdersAreOpposites(t *testing.T) {
	cands := planCands(map[string]int{"decile:1": 400})
	asks, _ := PlanPairs(cands, PairPlanOpts{Seed: "s", N: 200}, allPending)
	forward := 0
	for _, a := range asks {
		if a.A.Key < a.B.Key {
			forward++
		}
	}
	if forward < 70 || forward > 130 {
		t.Errorf("%d of 200 pairs were shown in key order: the A/B order leans (position bias would be confounded with the key)", forward)
	}
	both, st := PlanPairs(cands, PairPlanOpts{Seed: "s", N: 5, BothOrders: true}, allPending)
	if len(both) != 10 || st.Pairs != 5 {
		t.Fatalf("%d asks, %d pairs, want 10 and 5", len(both), st.Pairs)
	}
	for i := 0; i < len(both); i += 2 {
		if both[i].A.Key != both[i+1].B.Key || both[i].B.Key != both[i+1].A.Key {
			t.Errorf("asks %d and %d are not one pair in opposite orders", i, i+1)
		}
	}
}

func TestPlanPairsSkipsWhatIsStoredAndResumes(t *testing.T) {
	cands := planCands(map[string]int{"decile:1": 12})
	first, _ := PlanPairs(cands, PairPlanOpts{Seed: "s", N: 2}, allPending)
	stored := map[[2]posting.Key]bool{}
	for _, a := range first {
		lo, hi := PairKeyOf(a.A.Key, a.B.Key)
		stored[[2]posting.Key{lo, hi}] = true
	}
	next, st := PlanPairs(cands, PairPlanOpts{Seed: "s", N: 2}, func(a PlannedAsk) bool {
		lo, hi := PairKeyOf(a.A.Key, a.B.Key)
		return !stored[[2]posting.Key{lo, hi}]
	})
	if len(next) != 2 || st.Stored != 2 {
		t.Fatalf("%d asks, %d stored passed over", len(next), st.Stored)
	}
	for _, a := range next {
		lo, hi := PairKeyOf(a.A.Key, a.B.Key)
		if stored[[2]posting.Key{lo, hi}] {
			t.Errorf("a stored pair %s/%s was planned again", lo, hi)
		}
	}
	// Everything stored: nothing is planned and the plan terminates.
	none, st := PlanPairs(cands, PairPlanOpts{Seed: "s", N: 50}, func(PlannedAsk) bool { return false })
	if len(none) != 0 || st.Pairs != 0 {
		t.Errorf("planned %d pairs with all stored", len(none))
	}
}

func TestPlanPairsEdgeCases(t *testing.T) {
	if asks, _ := PlanPairs(nil, PairPlanOpts{Seed: "s", N: 5}, allPending); len(asks) != 0 {
		t.Error("pairs from no candidates")
	}
	if asks, _ := PlanPairs(planCands(map[string]int{"decile:1": 6}), PairPlanOpts{Seed: "s", N: 0}, allPending); len(asks) != 0 {
		t.Error("pairs with N = 0")
	}
	// A stratum of one cannot make a pair.
	if asks, _ := PlanPairs(planCands(map[string]int{"decile:1": 1, "decile:2": 1}), PairPlanOpts{Seed: "s", N: 5}, allPending); len(asks) != 0 {
		t.Errorf("%d pairs from singleton strata", len(asks))
	}
	// Duplicate keys, empty keys and empty strata are ignored.
	cands := planCands(map[string]int{"decile:1": 4})
	cands = append(cands, cands[0], PairCandidate{Posting: posting.Posting{Key: ""}, Stratum: "decile:1"},
		PairCandidate{Posting: posting.Posting{Key: "nostratum"}, Stratum: ""})
	asks, _ := PlanPairs(cands, PairPlanOpts{Seed: "s", N: 10}, allPending)
	if len(asks) != 2 {
		t.Errorf("%d pairs from 4 distinct postings, want 2", len(asks))
	}
	// Oversized N is bounded by what exists.
	asks, _ = PlanPairs(planCands(map[string]int{"decile:1": 9}), PairPlanOpts{Seed: "s", N: 1 << 30}, allPending)
	if len(asks) != 4 {
		t.Errorf("%d pairs from 9 postings, want 4 disjoint ones", len(asks))
	}
}

func TestPlanPairsCoversTheGoldPostingsWithLabelBlindMates(t *testing.T) {
	cands := planCands(map[string]int{"decile:1": 40, "decile:2": 40})
	gold := map[posting.Key]bool{"decile:1#3": true, "decile:2#7": true, "decile:2#9": true, "not-a-candidate": true}
	asks, st := PlanPairs(cands, PairPlanOpts{Seed: "s", N: 50, Cover: gold, Mates: 3}, allPending)
	count := map[posting.Key]int{}
	for _, a := range asks {
		count[a.A.Key]++
		count[a.B.Key]++
		if a.A.Key[:8] != a.B.Key[:8] {
			t.Errorf("a cover pair crosses strata: %s %s", a.A.Key, a.B.Key)
		}
	}
	for _, k := range []posting.Key{"decile:1#3", "decile:2#7", "decile:2#9"} {
		if count[k] < 3 {
			t.Errorf("%s is in %d pairs, want at least its 3 mates", k, count[k])
		}
	}
	if st.Cover != 4 || st.CoverShort != 1 {
		t.Errorf("cover %d short %d, want 4 and 1 (one is not a candidate)", st.Cover, st.CoverShort)
	}

	// The mates do not depend on anything about the label: the same seed and keys
	// plan the same mates whichever posting is the "yes".
	other, _ := PlanPairs(cands, PairPlanOpts{Seed: "s", N: 50, Cover: gold, Mates: 3}, allPending)
	if len(other) != len(asks) {
		t.Error("coverage is not deterministic")
	}

	// With the plan too small for the mates, a cover stays within N and is reported short.
	small, st2 := PlanPairs(cands, PairPlanOpts{Seed: "s", N: 2, Cover: gold, Mates: 3}, allPending)
	if len(small) > 2 || st2.CoverShort < 2 {
		t.Errorf("%d asks (N 2), %d short", len(small), st2.CoverShort)
	}
}

func TestAuditStrataAgreesWithTheAuditDraw(t *testing.T) {
	var cands []AuditCandidate
	for i := range 40 {
		cands = append(cands, AuditCandidate{Key: posting.Key(fmt.Sprintf("kept%d", i)), Score: float64(i)})
	}
	for i := range 6 {
		cands = append(cands, AuditCandidate{Key: posting.Key(fmt.Sprintf("drop%d", i)), Dropped: true, Class: []string{"title:role_fit", "model:seniority"}[i%2]})
	}
	cands = append(cands, AuditCandidate{Key: "dead1", Closed: true, Class: "dead"}, AuditCandidate{Key: "kept0"})
	strata := AuditStrata(cands, nil)
	plan := AuditDraw(cands, nil, "seed", AuditQuotas{Scored: 40, Dropped: 6, Closed: 1})
	if len(plan.Picks) != 47 {
		t.Fatalf("the draw took %d, want every posting", len(plan.Picks))
	}
	got := map[posting.Key]string{}
	n := 0
	for s, ks := range strata {
		for _, k := range ks {
			got[k] = s
			n++
		}
	}
	if n != 47 {
		t.Errorf("%d members in the strata, want 47 (a key listed twice counts once)", n)
	}
	for _, p := range plan.Picks {
		if got[p.Key] != p.Source {
			t.Errorf("%s: strata say %q, the draw says %q", p.Key, got[p.Key], p.Source)
		}
	}
	// A labelled posting leaves the strata, as it leaves the draw.
	labeled := map[posting.Key]Entry{"kept5": {Key: "kept5"}}
	for _, ks := range AuditStrata(cands, labeled) {
		for _, k := range ks {
			if k == "kept5" {
				t.Error("a labelled posting is in a stratum")
			}
		}
	}
}

func TestPairCapRefusesByDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "budget", PairCapFile)
	c := OpenPairCap(path)
	capNano, spent, err := c.State("2026-11-12")
	if err != nil || capNano != 0 || spent != 0 {
		t.Fatalf("no file: cap %d spent %d err %v, want zero (refuse)", capNano, spent, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err == nil {
		t.Error("reading the cap created its directory")
	}
	if err := c.SetCap(context.Background(), 2.5); err != nil {
		t.Fatal(err)
	}
	if capNano, _, _ := c.State("2026-11-12"); capNano != 2_500_000_000 {
		t.Errorf("cap %d", capNano)
	}
	if err := c.AddSpent(context.Background(), "2026-11-12", 700); err != nil {
		t.Fatal(err)
	}
	if err := c.AddSpent(context.Background(), "2026-11-12", 300); err != nil {
		t.Fatal(err)
	}
	if _, spent, _ := c.State("2026-11-12"); spent != 1000 {
		t.Errorf("spent %d, want 1000", spent)
	}
	if _, spent, _ := c.State("2026-11-13"); spent != 0 {
		t.Error("a new UTC day starts with yesterday's spend")
	}
	// Setting a new cap keeps the spend record; zero refuses everything again.
	if err := c.SetCap(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if capNano, spent, _ := c.State("2026-11-12"); capNano != 0 || spent != 1000 {
		t.Errorf("after a zero cap: cap %d spent %d", capNano, spent)
	}
}

func TestPairCapRejectsBadValuesAndBadFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), PairCapFile)
	c := OpenPairCap(path)
	for _, usd := range []float64{-1, math.NaN(), math.Inf(1), MaxPairCapUSD + 0.01} {
		if err := c.SetCap(context.Background(), usd); err == nil {
			t.Errorf("a cap of %v was set", usd)
		}
	}
	if err := c.AddSpent(context.Background(), "2026-11-12", -5); err == nil {
		t.Error("a negative spend was recorded")
	}
	for name, content := range map[string]string{
		"corrupt":        "{",
		"unknown field":  `{"version":1,"daily_usd":1,"spent_nano":{},"extra":1}`,
		"trailing":       `{"version":1,"daily_usd":1,"spent_nano":{}} {}`,
		"version":        `{"version":2,"daily_usd":1,"spent_nano":{}}`,
		"negative cap":   `{"version":1,"daily_usd":-1,"spent_nano":{}}`,
		"huge cap":       `{"version":1,"daily_usd":1000,"spent_nano":{}}`,
		"negative spend": `{"version":1,"daily_usd":1,"spent_nano":{"2026-11-12":-3}}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		// An unreadable cap is an error and not a zero spend: a run must stop on it.
		if _, _, err := c.State("2026-11-12"); err == nil {
			t.Errorf("%s: the cap file was read as valid", name)
		}
		if err := c.AddSpent(context.Background(), "2026-11-12", 5); err == nil {
			t.Errorf("%s: spend was recorded over a file that cannot be read", name)
		}
	}
}

func TestPairCapKeepsABoundedHistory(t *testing.T) {
	c := OpenPairCap(filepath.Join(t.TempDir(), PairCapFile))
	if err := c.SetCap(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 50 {
		if err := c.AddSpent(context.Background(), Day(day.AddDate(0, 0, i)), 1); err != nil {
			t.Fatal(err)
		}
	}
	s, err := c.load()
	if err != nil || len(s.SpentNano) != pairCapKeepDays {
		t.Errorf("%d days kept (err %v), want %d", len(s.SpentNano), err, pairCapKeepDays)
	}
}
