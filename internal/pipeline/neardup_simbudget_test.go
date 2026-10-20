package pipeline

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// ndMixed fills l with one duplicate pair, one sibling pair and n candidate
// pairs, and returns the later posting of each candidate pair.
func ndMixed(t *testing.T, l ledger.Ledger, n int) []posting.Key {
	t.Helper()
	jd := words("duty", 100)
	ndAdd(t, l, "d1", "Acme", "Engineer", "Remote", jd, t0)
	ndAdd(t, l, "d2", "Acme", "Engineer", "Remote", jd, t0.Add(time.Minute))
	ndAdd(t, l, "s1", "Initech", "Analyst", "New York, NY", jd, t0.Add(2*time.Minute))
	ndAdd(t, l, "s2", "Initech", "Analyst", "Seattle, WA", jd, t0.Add(3*time.Minute))
	var cands []posting.Key
	for i := range n {
		co := fmt.Sprintf("Co%d", i)
		at := t0.Add(time.Duration(10+2*i) * time.Minute)
		ndAdd(t, l, fmt.Sprintf("c%da", i), co, "Designer", "Remote", words("duty", 100), at)
		cands = append(cands, ndAdd(t, l, fmt.Sprintf("c%db", i), co, "Designer", "Remote", shares(80, 100), at.Add(time.Minute)))
	}
	return cands
}

// ndOutcomes is every record's status and annotations, the cosine aside.
func ndOutcomes(l ledger.Ledger) map[posting.Key]string {
	out := map[posting.Key]string{}
	for _, r := range l.All() {
		ann := maps.Clone(r.Annotations)
		delete(ann, AnnDupCosine)
		out[r.Key] = fmt.Sprintf("%s %v", r.Status, ann)
	}
	return out
}

// ndCosines counts the records that carry a cosine.
func ndCosines(l ledger.Ledger) int {
	n := 0
	for _, r := range l.All() {
		if r.Annotations[AnnDupCosine] != "" {
			n++
		}
	}
	return n
}

// ndBaseline is the stage over ndMixed(n) with no similarity.
func ndBaseline(t *testing.T, n int) (NearDupReport, map[posting.Key]string) {
	t.Helper()
	l := openLedger(t)
	ndMixed(t, l, n)
	rep := annotate(t, l, t1, NearDupOptions{})
	if rep.Partial || rep.Duplicates != 1 || rep.Siblings != 1 || rep.Candidates != n {
		t.Fatalf("baseline %+v", rep)
	}
	return rep, ndOutcomes(l)
}

func wantSameShingleOutcomes(t *testing.T, l ledger.Ledger, rep, base NearDupReport, baseOut map[posting.Key]string) {
	t.Helper()
	if rep.Partial || rep.PartialInSimilarity || rep.Examined != rep.Records || rep.Records != base.Records ||
		rep.Duplicates != base.Duplicates || rep.Siblings != base.Siblings || rep.Candidates != base.Candidates ||
		rep.Compared != base.Compared {
		t.Fatalf("report %+v, without a similarity %+v", rep, base)
	}
	if got := ndOutcomes(l); !maps.Equal(got, baseOut) {
		t.Fatalf("shingle outcomes differ from a run without a similarity:\n got %v\nwant %v", got, baseOut)
	}
}

// A similarity slower than its budget costs readings, never the shingle pass:
// the stage is whole, and every status and shingle annotation is the one a
// run without a similarity writes.
func TestNearDupSlowSimilarityLeavesTheShinglePassWhole(t *testing.T) {
	const (
		n        = 6
		deadline = 400 * time.Millisecond
		budget   = time.Duration(float64(deadline) * NearDupSimilarityShare)
	)
	base, baseOut := ndBaseline(t, n)
	honours := func(ctx context.Context) (float64, bool, error) {
		<-ctx.Done()
		return 0, false, ctx.Err()
	}
	for _, tc := range []struct {
		name       string
		fast       int // readings answered at once before the slow one
		slow       func(ctx context.Context) (float64, bool, error)
		calls      int
		cosines    int
		wantBudget bool
	}{
		{"sleeps until its ctx ends", 0, honours, 1, 0, true},
		{"readings, then one that sleeps", 2, honours, 3, 2, true},
		{"ignores its ctx and returns an error late", 0, func(ctx context.Context) (float64, bool, error) {
			time.Sleep(budget + 50*time.Millisecond)
			return 0, false, errors.New("timed out")
		}, 1, 0, true},
		// A reading that arrives after its budget was still taken, and is kept.
		{"ignores its ctx and returns a reading late", 0, func(ctx context.Context) (float64, bool, error) {
			time.Sleep(budget + 50*time.Millisecond)
			return 0.9, true, nil
		}, 1, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := openLedger(t)
			ndMixed(t, l, n)
			calls := 0
			rep := annotate(t, l, t1, NearDupOptions{Deadline: deadline,
				Similarity: func(ctx context.Context, a, b string) (float64, bool, error) {
					calls++
					dl, ok := ctx.Deadline()
					if !ok || time.Until(dl) > budget {
						t.Errorf("reading %d: ctx deadline %v (set %v) is not inside the %v budget", calls, time.Until(dl), ok, budget)
					}
					if calls <= tc.fast {
						return 0.9, true, nil
					}
					return tc.slow(ctx)
				}})
			wantSameShingleOutcomes(t, l, rep, base, baseOut)
			if calls != tc.calls || ndCosines(l) != tc.cosines || rep.SimilaritySkipped != n-tc.cosines ||
				rep.SimilarityErrors != 0 || rep.SimilarityBudgetSpent != tc.wantBudget || rep.SimilarityBreakerOpen {
				t.Fatalf("%d calls, %d cosines, report %+v", calls, ndCosines(l), rep)
			}
		})
	}
}

// Errors in a row stop the readings; a reading resets the run, and a pair
// with no data (a blank description) says nothing about the similarity.
func TestNearDupSimilarityBreaker(t *testing.T) {
	const n = 8
	base, baseOut := ndBaseline(t, n)
	down := errors.New("embedder down")
	for _, tc := range []struct {
		name                          string
		answer                        func(call int) (float64, bool, error)
		calls, errs, skipped, cosines int
		open                          bool
	}{
		{"errors in a row", func(int) (float64, bool, error) { return 0, false, down },
			NearDupSimilarityBreaker, NearDupSimilarityBreaker, n - NearDupSimilarityBreaker, 0, true},
		{"a reading resets the run", func(call int) (float64, bool, error) {
			if call == NearDupSimilarityBreaker {
				return 0.95, true, nil
			}
			return 0, false, down
		}, 2 * NearDupSimilarityBreaker, 2*NearDupSimilarityBreaker - 1, n - 2*NearDupSimilarityBreaker, 1, true},
		{"no data does not trip it", func(int) (float64, bool, error) { return 0.99, false, nil },
			n, n, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := openLedger(t)
			ndMixed(t, l, n)
			calls := 0
			rep := annotate(t, l, t1, NearDupOptions{Similarity: func(context.Context, string, string) (float64, bool, error) {
				calls++
				return tc.answer(calls)
			}})
			wantSameShingleOutcomes(t, l, rep, base, baseOut)
			if calls != tc.calls || rep.SimilarityErrors != tc.errs || rep.SimilaritySkipped != tc.skipped ||
				ndCosines(l) != tc.cosines || rep.SimilarityBreakerOpen != tc.open || rep.SimilarityBudgetSpent {
				t.Fatalf("%d calls, %d cosines, report %+v", calls, ndCosines(l), rep)
			}
		})
	}
}

// A pair the budget did not reach this run has no reading, so last run's
// cosine is forgotten rather than shown as this run's.
func TestNearDupSkippedReadingForgetsTheOldCosine(t *testing.T) {
	l := openLedger(t)
	cands := ndMixed(t, l, 1)
	annotate(t, l, t1, NearDupOptions{Similarity: func(context.Context, string, string) (float64, bool, error) {
		return 0.97, true, nil
	}})
	if got := rec(t, l, cands[0]).Annotations[AnnDupCosine]; got != "0.970" {
		t.Fatalf("first run cosine %q", got)
	}
	rep := annotate(t, l, t1, NearDupOptions{Deadline: 100 * time.Millisecond,
		Similarity: func(ctx context.Context, a, b string) (float64, bool, error) {
			<-ctx.Done()
			return 0, false, ctx.Err()
		}})
	a := rec(t, l, cands[0]).Annotations
	if rep.Partial || rep.SimilaritySkipped != 1 || rep.Written != 1 || a[AnnDupCosine] != "" || a[AnnDupCandidateOf] == "" {
		t.Fatalf("report %+v, annotations %v", rep, a)
	}
}
