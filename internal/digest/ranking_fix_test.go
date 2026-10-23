package digest

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func inCompany(r ledger.Record, company string) ledger.Record {
	r.Posting.Company = company
	return r
}

// Equal priority breaks on relevance, not on the key: the one tie-break every
// list shares (jobinsight.ComparePriority).
func TestDigestTieBreaksOnRelevance(t *testing.T) {
	type cand struct {
		fit string
		age int
		res jobinsight.Result
	}
	build := func(key, fitv string, age int) ledger.Record {
		return aged(rec(key, posting.Explained, "Role "+key, fit(fitv)), age)
	}
	score := func(r ledger.Record) jobinsight.Result { return newScorer(nil, nil, day, Options{}).score(r) }
	var a, b *cand
	fits := []string{"strong_fit", "partial_fit", "adjacent"}
search:
	for _, fa := range fits {
		for ageA := 0; ageA < 40; ageA++ {
			ca := cand{fa, ageA, score(build("x", fa, ageA))}
			for _, fb := range fits {
				for ageB := 0; ageB < 40; ageB++ {
					cb := cand{fb, ageB, score(build("y", fb, ageB))}
					if ca.res.Score == cb.res.Score && ca.res.Relevance != cb.res.Relevance {
						a, b = &ca, &cb
						break search
					}
				}
			}
		}
	}
	if a == nil {
		t.Fatal("no fixture with equal priority and different relevance found")
	}
	hi, lo := a, b
	if lo.res.Relevance > hi.res.Relevance {
		hi, lo = lo, hi
	}
	// The key order opposes the relevance order, so a key-only tie-break fails.
	hiRec := build("gh:a:z", hi.fit, hi.age)
	loRec := build("gh:a:a", lo.fit, lo.age)
	hiRec.Posting.Title, loRec.Posting.Title = "High relevance", "Low relevance"
	for _, recs := range [][]ledger.Record{{hiRec, loRec}, {loRec, hiRec}} {
		_, keys := Render(recs, nil, day, Counts{})
		if want := []posting.Key{"gh:a:z", "gh:a:a"}; !slices.Equal(keys, want) {
			t.Fatalf("keys %v, want %v (equal priority %.1f, relevance %.1f over %.1f)", keys, want, hi.res.Score, hi.res.Relevance, lo.res.Relevance)
		}
	}
}

// The digest ages postings by the half-life it is given, as `vani jobs` does.
func TestDigestUsesGivenHalfLife(t *testing.T) {
	old := aged(rec("gh:a:old", posting.Explained, "Excellent but older", fit("strong_fit")), 21)
	fresh := aged(rec("gh:a:fresh", posting.Explained, "Mediocre and new", fit("adjacent")), 0)
	recs := []ledger.Record{old, fresh}
	first := func(o Options) posting.Key {
		_, keys := RenderWith(recs, nil, day, Counts{}, o)
		return keys[0]
	}
	if got := first(Options{}); got != "gh:a:fresh" {
		t.Fatalf("default half-life: %s leads, want the fresh one", got)
	}
	if got := first(Options{HalfLifeDays: 365}); got != "gh:a:old" {
		t.Errorf("half-life 365: %s leads, want the excellent older one", got)
	}
	if got := first(Options{HalfLifeDays: 2}); got != "gh:a:fresh" {
		t.Errorf("half-life 2: %s leads, want the fresh one", got)
	}
	// An unusable half-life falls back to the default rather than ranking by NaN.
	for _, h := range []float64{-5, 0, 1e9} {
		if got := first(Options{HalfLifeDays: h}); got != "gh:a:fresh" {
			t.Errorf("half-life %v: %s leads, want the default order", h, got)
		}
	}
}

// At most three roles of one company lead the ranked section, and exact
// company + title duplicates are one row carrying a similar count; every
// collapsed role is still returned as new, so it is marked Digested.
func TestDigestDiversity(t *testing.T) {
	var recs []ledger.Record
	for i := 0; i < 5; i++ {
		k := fmt.Sprintf("gh:big:%d", i)
		r := aged(rec(k, posting.Explained, fmt.Sprintf("Big role %d", i), fit("strong_fit")), 1)
		recs = append(recs, inCompany(r, "BigCo"))
	}
	for i := 0; i < 3; i++ {
		k := fmt.Sprintf("gh:small:%d", i)
		r := aged(rec(k, posting.Explained, fmt.Sprintf("Small role %d", i), fit("partial_fit")), 1)
		recs = append(recs, inCompany(r, fmt.Sprintf("Small%d", i)))
	}
	md, keys := Render(recs, nil, day, Counts{})
	if len(keys) != 8 {
		t.Fatalf("every role is still new: %v", keys)
	}
	big := 0
	for _, k := range keys[:6] {
		if strings.HasPrefix(string(k), "gh:big:") {
			big++
		}
	}
	if big > 3 {
		t.Errorf("%d BigCo roles in the first six: %v", big, keys)
	}
	if !strings.Contains(md, "## Ranked roles (8)") {
		t.Errorf("heading must count every role:\n%s", md)
	}

	dup := []ledger.Record{
		aged(rec("gh:a:1", posting.Explained, "ML  Engineer", fit("strong_fit")), 1),
		aged(rec("gh:a:2", posting.Explained, "ml engineer!", fit("strong_fit")), 1),
		aged(rec("gh:a:3", posting.Explained, "Data Engineer", fit("strong_fit")), 1),
	}
	md, keys = Render(dup, nil, day, Counts{})
	if len(keys) != 3 {
		t.Fatalf("collapsed roles must still be returned as new: %v", keys)
	}
	if n := strings.Count(md, "\n### "); n != 2 {
		t.Errorf("%d role rows, want 2 (the duplicates collapse):\n%s", n, md)
	}
	if !strings.Contains(md, "Similar: 1 more") {
		t.Errorf("the collapsed row must carry a similar count:\n%s", md)
	}
}

func TestTopOpenDiversity(t *testing.T) {
	var recs []ledger.Record
	for i := 0; i < 6; i++ {
		r := aged(rec(fmt.Sprintf("gh:big:%d", i), posting.Digested, fmt.Sprintf("Big role %d", i), fit("strong_fit")), 3)
		recs = append(recs, inCompany(r, "BigCo"))
	}
	for i := 0; i < 3; i++ {
		r := aged(rec(fmt.Sprintf("gh:small:%d", i), posting.Digested, fmt.Sprintf("Small role %d", i), fit("partial_fit")), 3)
		recs = append(recs, inCompany(r, fmt.Sprintf("Small%d", i)))
	}
	_, _, shown := RenderShown(recs, nil, day, Counts{}, Options{TopOpen: 6})
	got := sectionKeys(shown, SectionTopOpen)
	if len(got) != 6 {
		t.Fatalf("top open %v", got)
	}
	big := 0
	for _, k := range got {
		if strings.HasPrefix(k, "gh:big:") {
			big++
		}
	}
	if big > 3 {
		t.Errorf("%d BigCo roles in top open %v", big, got)
	}
}
