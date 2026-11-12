package eval

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func dropPool(classes map[string]int) []AuditCandidate {
	var cs []AuditCandidate
	for c, n := range classes {
		for i := range n {
			cs = append(cs, AuditCandidate{Key: posting.Key(fmt.Sprintf("gh:%s:%03d", c, i)), Dropped: true, Class: c})
		}
	}
	return cs
}

func sourcesOf(p AuditPlan) []string {
	var out []string
	for _, pk := range p.Picks {
		out = append(out, pk.Source)
	}
	slices.Sort(out)
	return out
}

// A duplicate is closed under a method (dup.method): a fold by shingle overlap
// and one with no method are different mechanisms, so a wrong one of either
// must show up in its own stratum, not in a pooled "duplicate" one.
func TestAuditClosedDuplicatesSplitByMethod(t *testing.T) {
	cands := []AuditCandidate{
		{Key: "gh:a:1", Closed: true, Class: "duplicate:shingle"},
		{Key: "gh:a:2", Closed: true, Class: "duplicate:shingle"},
		{Key: "gh:b:1", Closed: true, Class: "duplicate:other"},
		{Key: "gh:c:1", Closed: true, Class: "dead"},
	}
	for want, c := range map[string]AuditCandidate{
		"closed:duplicate:shingle": cands[0],
		"closed:duplicate:other":   cands[2],
		"closed:dead":              cands[3],
	} {
		if got := AuditCell(c); got != want {
			t.Errorf("AuditCell(%q) = %q, want %q", c.Class, got, want)
		}
	}
	// The weekly budget is unchanged: three strata, still two places.
	plan := AuditDraw(cands, nil, "2026-W46", AuditQuotas{Closed: 2})
	if len(plan.Picks) != 2 || len(plan.Strata) != 3 {
		t.Fatalf("picks %d strata %d: %+v", len(plan.Picks), len(plan.Strata), plan)
	}
	for _, p := range plan.Picks {
		if !validSource(p.Source) {
			t.Errorf("invalid source %q", p.Source)
		}
	}
	// A bare "duplicate" (a label written before the split) keeps its stratum,
	// and junk in a method cannot make an invalid or empty stratum name.
	if got := AuditCell(AuditCandidate{Closed: true, Class: "duplicate"}); got != "closed:duplicate" {
		t.Errorf("bare duplicate: %q", got)
	}
	for _, class := range []string{"duplicate:", ":", "duplicate:Bad Method!", "::x"} {
		if got := AuditCell(AuditCandidate{Closed: true, Class: class}); !validSource(got) || !strings.HasPrefix(got, ClosedPrefix) || strings.HasSuffix(got, ":") {
			t.Errorf("class %q -> %q", class, got)
		}
	}
}

// With places for every non-empty class, each gets at least one, however
// lopsided the pool (a one-member class beside a huge one).
func TestAuditDropClassesAllServedWhenSlotsAllow(t *testing.T) {
	pool := dropPool(map[string]int{"model:a": 500, "model:b": 1, "exclude:title": 2, "model:c": 40, "title:x": 1})
	for _, seed := range []string{"2026-W44", "2026-W45", "x", ""} {
		plan := AuditDraw(pool, nil, seed, AuditQuotas{Dropped: 10})
		got := map[string]int{}
		for _, p := range plan.Picks {
			got[p.Source]++
		}
		for _, c := range []string{"model:a", "model:b", "exclude:title", "model:c", "title:x"} {
			if got[DroppedPrefix+c] < 1 {
				t.Errorf("seed %q: class %s unsampled: %v", seed, c, got)
			}
		}
		if len(plan.Picks) != 10 {
			t.Errorf("seed %q: %d picks, want 10", seed, len(plan.Picks))
		}
	}
	// Exactly as many places as classes: one each.
	plan := AuditDraw(pool, nil, "2026-W44", AuditQuotas{Dropped: 5})
	if got := sourcesOf(plan); len(got) != 5 || len(slices.Compact(slices.Clone(got))) != 5 {
		t.Errorf("slots == classes: %v", got)
	}
}

// More non-empty classes than places: which ones are served rotates with the
// ISO week so that every class is covered in ceil(classes/slots) successive
// weeks, including across a year boundary.
func TestAuditDropClassesRotateWhenClassesExceedSlots(t *testing.T) {
	pool := dropPool(map[string]int{"c1": 30, "c2": 1, "c3": 5, "c4": 2, "c5": 9, "c6": 1, "c7": 12})
	week := func(start time.Time, i int) string { return WeekSeed(start.AddDate(0, 0, 7*i)) }
	for _, start := range []time.Time{
		time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 12, 14, 0, 0, 0, 0, time.UTC), // runs through 2026-W53 into 2027
		time.Date(2027, 12, 20, 0, 0, 0, 0, time.UTC), // a 52-week year
	} {
		for from := range 7 {
			seen := map[string]bool{}
			for i := range 3 { // ceil(7/3)
				plan := AuditDraw(pool, nil, week(start, from+i), AuditQuotas{Dropped: 3})
				if len(plan.Picks) != 3 {
					t.Fatalf("%d picks", len(plan.Picks))
				}
				for _, p := range plan.Picks {
					seen[p.Source] = true
				}
			}
			if len(seen) != 7 {
				t.Errorf("from %s: three weeks covered %d of 7 classes: %v", week(start, from), len(seen), seen)
			}
		}
	}
	// A partial round serves each class with probability slots/classes.
	plan := AuditDraw(pool, nil, "2026-W44", AuditQuotas{Dropped: 3})
	for _, p := range plan.Picks {
		n := map[string]int{"c1": 30, "c2": 1, "c3": 5, "c4": 2, "c5": 9, "c6": 1, "c7": 12}[strings.TrimPrefix(p.Source, DroppedPrefix)]
		if want := 3.0 / 7 / float64(n); p.PSelect != want {
			t.Errorf("%s p_select %v, want %v", p.Source, p.PSelect, want)
		}
	}
}

// Rotation changes nothing about determinism: the same seed draws the same
// postings in any input order, and edge pools do not panic.
func TestAuditDrawRotationIsDeterministicAndSafe(t *testing.T) {
	pool := dropPool(map[string]int{"c1": 4, "c2": 4, "c3": 4, "c4": 4})
	rev := slices.Clone(pool)
	slices.Reverse(rev)
	for _, seed := range []string{"2026-W44", "2026-W53", "2027-W01", "weird", "2026-W99", "0000-W00", ""} {
		a := AuditDraw(pool, nil, seed, AuditQuotas{Dropped: 3})
		b := AuditDraw(rev, nil, seed, AuditQuotas{Dropped: 3})
		if !reflect.DeepEqual(a, b) {
			t.Errorf("seed %q: input order changed the draw:\n%+v\n%+v", seed, a, b)
		}
		if len(a.Picks) != 3 {
			t.Errorf("seed %q: %d picks", seed, len(a.Picks))
		}
	}
	if p := AuditDraw(nil, nil, "2026-W44", AuditQuotas{Dropped: 3, Closed: 2}); len(p.Picks) != 0 || len(p.Strata) != 0 {
		t.Errorf("empty pool: %+v", p)
	}
	// Every class labelled already: nothing left to draw.
	labeled := map[posting.Key]Entry{}
	for _, c := range dropPool(map[string]int{"c1": 4}) {
		labeled[c.Key] = Entry{}
	}
	if p := AuditDraw(dropPool(map[string]int{"c1": 4}), labeled, "2026-W44", AuditQuotas{Dropped: 3}); len(p.Picks) != 0 {
		t.Errorf("all labelled: %+v", p)
	}
	// One class, many places: drawn short, not padded.
	if p := AuditDraw(dropPool(map[string]int{"c1": 2}), nil, "2026-W44", AuditQuotas{Dropped: 9}); len(p.Picks) != 2 {
		t.Errorf("short pool: %+v", p)
	}
}
