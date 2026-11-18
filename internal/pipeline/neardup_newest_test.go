package pipeline

import (
	"context"
	"testing"
	"time"
)

// A deadline that has run out before decide starts (a stall, here a 1ns
// budget) must still leave the newest posting decided; older ones are cut and
// the run is Partial. Card ft-ae057be0ac19ae51ab9d56474ff7ee7c.
func TestNearDupNewestIsDecidedWhenTheDeadlineIsAlreadySpent(t *testing.T) {
	l := openLedger(t)
	jd, jd2 := words("duty", 100), words("task", 100)
	o1 := ndAdd(t, l, "1", "Acme", "Engineer", "Remote", jd, t0)
	o2 := ndAdd(t, l, "2", "Acme", "Engineer", "Remote", jd, t0.Add(time.Minute)) // a duplicate of o1, older than the newest pair
	x := ndAdd(t, l, "3", "Globex", "Designer", "Remote", jd2, t0.Add(2*time.Minute))
	y := ndAdd(t, l, "4", "Globex", "Designer", "Remote", jd2, t0.Add(3*time.Minute)) // newest: a duplicate of x
	rep := annotate(t, l, t1, NearDupOptions{Deadline: time.Nanosecond})
	if !rep.Partial || rep.Examined != 1 || rep.Duplicates != 1 || rep.Compared != 1 || rep.Written != 1 || rep.Records != 4 {
		t.Fatalf("report %+v", rep)
	}
	wantDuplicate(t, l, y, x)
	wantLive(t, l, o1, o2, x) // older postings keep what they had: the deadline cut them
}

// The similarity step belongs to the newest posting's decision: it is reached
// past a spent deadline, bounded by the similarity budget (nothing left, so
// the pair is counted as skipped and the reader is not called).
func TestNearDupNewestSimilarityIsBoundedWhenTheDeadlineIsSpent(t *testing.T) {
	l := openLedger(t)
	a := ndAdd(t, l, "1", "Acme", "Engineer", "Remote", words("duty", 100), t0)
	b := ndAdd(t, l, "2", "Acme", "Engineer", "Remote", shares(80, 100), t0.Add(time.Minute)) // a candidate of a
	calls := 0
	rep := annotate(t, l, t1, NearDupOptions{
		Deadline: time.Nanosecond,
		Similarity: func(ctx context.Context, x, y string) (float64, bool, error) {
			calls++
			return 0.5, true, nil
		},
	})
	if rep.Examined != 1 || rep.Candidates != 1 || rep.SimilaritySkipped != 1 || calls != 0 || !rep.Partial {
		t.Fatalf("report %+v, similarity calls %d", rep, calls)
	}
	if got := rec(t, l, b).Annotations[AnnDupCandidateOf]; got != string(a) {
		t.Fatalf("newest candidate annotation %q, want %q", got, a)
	}
}

// One posting: nothing older to cut, so a spent deadline is not Partial.
// No postings: nothing to decide, and no panic.
func TestNearDupSpentDeadlineWithOneOrNoPostings(t *testing.T) {
	l := openLedger(t)
	rep := annotate(t, l, t1, NearDupOptions{Deadline: time.Nanosecond})
	if rep.Records != 0 || rep.Examined != 0 || rep.Partial {
		t.Fatalf("empty report %+v", rep)
	}
	ndAdd(t, l, "1", "Acme", "Engineer", "Remote", words("duty", 100), t0)
	rep = annotate(t, l, t1, NearDupOptions{Deadline: time.Nanosecond})
	if rep.Records != 1 || rep.Examined != 1 || rep.Partial {
		t.Fatalf("single report %+v", rep)
	}
}

// The exemption is from the deadline only: a cancelled caller still stops the
// run, newest posting included.
func TestNearDupCancelledCallerStopsTheNewestToo(t *testing.T) {
	l := openLedger(t)
	jd := words("duty", 100)
	ndAdd(t, l, "1", "Acme", "Engineer", "Remote", jd, t0)
	y := ndAdd(t, l, "2", "Acme", "Engineer", "Remote", jd, t0.Add(time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AnnotateNearDuplicates(ctx, l, t1, NearDupOptions{Deadline: time.Nanosecond}); err == nil {
		t.Fatal("a cancelled context was not an error")
	}
	wantLive(t, l, y)
}
