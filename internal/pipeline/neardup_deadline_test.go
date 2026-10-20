package pipeline

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A demoted posting is no survivor, whichever end the decisions are reached
// from: three copies are two duplicates of the first, not a chain.
func TestNearDupCopiesAllPointAtTheEarliest(t *testing.T) {
	l := openLedger(t)
	jd := words("duty", 100)
	a := ndAdd(t, l, "1", "Acme", "Engineer", "Remote", jd, t0)
	b := ndAdd(t, l, "2", "Acme", "Engineer", "Remote", jd, t0.Add(time.Minute))
	c := ndAdd(t, l, "3", "Acme", "Engineer", "Remote", jd, t0.Add(2*time.Minute))
	rep := annotate(t, l, t1, NearDupOptions{})
	if rep.Duplicates != 2 || rep.Examined != 3 {
		t.Fatalf("report %+v", rep)
	}
	wantDuplicate(t, l, b, a)
	wantDuplicate(t, l, c, a)
	wantLive(t, l, a)
}

// Under a deadline the newest postings are the ones decided, and the
// decisions made are the complete ones a full run makes.
func TestNearDupDeadlineCostsTheOldestPostings(t *testing.T) {
	l := openLedger(t)
	jd2 := words("task", 100)
	old := ndAdd(t, l, "1", "Acme", "Engineer", "Remote", words("duty", 100), t0)
	older := ndAdd(t, l, "2", "Acme", "Engineer", "Remote", shares(80, 100), t0.Add(time.Minute)) // a candidate of old
	first := ndAdd(t, l, "3", "Globex", "Designer", "Remote", jd2, t0.Add(2*time.Minute))
	newest := ndAdd(t, l, "4", "Globex", "Designer", "Remote", jd2, t0.Add(3*time.Minute)) // a duplicate of first
	// The similarity reading of `older`'s candidate pair ignores its budget and
	// outruns the stage's deadline: the one cut the budget cannot prevent.
	const deadline = 200 * time.Millisecond
	rep := annotate(t, l, t1, NearDupOptions{
		Deadline: deadline,
		Similarity: func(ctx context.Context, a, b string) (float64, bool, error) {
			time.Sleep(deadline + 50*time.Millisecond)
			return 0, false, ctx.Err()
		},
	})
	// Decided: newest and first, and old, which `older` needed before it was cut.
	if !rep.Partial || !rep.PartialInSimilarity || rep.Examined != 3 || rep.Records != 4 || rep.Written != 1 ||
		rep.SimilarityErrors != 0 || rep.SimilaritySkipped != 0 {
		t.Fatalf("report %+v", rep)
	}
	wantDuplicate(t, l, newest, first)
	if a := rec(t, l, older).Annotations; a[AnnDupCandidateOf] != "" {
		t.Fatalf("a posting the deadline cut was annotated from part of its pairs: %v", a)
	}
	wantLive(t, l, old, first)

	// The next run, with time, decides the rest.
	rep = annotate(t, l, t1, NearDupOptions{})
	if rep.Partial || rep.Written != 1 || rec(t, l, older).Annotations[AnnDupCandidateOf] != string(old) {
		t.Fatalf("second run %+v", rep)
	}
}

// slowLedger is a fixed set of records whose Append takes a while, as an
// fsync does.
type slowLedger struct {
	recs  []ledger.Record
	delay time.Duration
	mu    sync.Mutex
	n     int
}

func (s *slowLedger) Append(ctx context.Context, e ledger.Event) error {
	return s.AppendBatch(ctx, []ledger.Event{e})
}

// AppendBatch delays once per batch, as one fsync does, and counts events.
func (s *slowLedger) AppendBatch(ctx context.Context, evs []ledger.Event) error {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	s.n += len(evs)
	s.mu.Unlock()
	return nil
}
func (s *slowLedger) Get(k posting.Key) (ledger.Record, bool) { return ledger.Record{}, false }
func (s *slowLedger) All() []ledger.Record                    { return s.recs }
func (s *slowLedger) Close() error                            { return nil }

// Writing the decided events is not charged to the comparing deadline.
func TestNearDupWritesAreNotCountedAgainstTheDeadline(t *testing.T) {
	const pairs = 200
	jd := words("duty", 100)
	// The writes are one batch, so one delay: make it outlast the deadline.
	l := &slowLedger{delay: NearDupDeadline + 100*time.Millisecond}
	for i := range pairs {
		for j := range 2 {
			k, err := posting.GreenhouseKey("acme", fmt.Sprintf("%d-%d", i, j))
			if err != nil {
				t.Fatal(err)
			}
			l.recs = append(l.recs, ledger.Record{Key: k, Status: posting.Live, Annotations: map[string]string{},
				FirstSeen: t0.Add(time.Duration(2*i+j) * time.Second),
				Posting:   posting.Posting{Key: k, Company: fmt.Sprintf("Co%d", i), Title: "Engineer", Location: "Remote", Description: jd}})
		}
	}
	start := time.Now()
	rep, err := AnnotateNearDuplicates(ctx, l, t1, NearDupOptions{})
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Partial || rep.Examined != 2*pairs || rep.Written != pairs || l.n != pairs {
		t.Fatalf("report %+v, %d appended", rep, l.n)
	}
	if took <= NearDupDeadline {
		t.Fatalf("the writes took %v: the test no longer outlasts the deadline", took)
	}
}

func TestNearDupCancelDuringWritesIsAnError(t *testing.T) {
	jd := words("duty", 100)
	l := &slowLedger{delay: time.Hour}
	for j := range 2 {
		k, _ := posting.GreenhouseKey("acme", fmt.Sprint(j))
		l.recs = append(l.recs, ledger.Record{Key: k, Status: posting.Live, Annotations: map[string]string{},
			FirstSeen: t0.Add(time.Duration(j) * time.Second),
			Posting:   posting.Posting{Key: k, Company: "Acme", Title: "Engineer", Location: "Remote", Description: jd}})
	}
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := AnnotateNearDuplicates(cctx, l, t1, NearDupOptions{}); err == nil {
		t.Fatal("a cancelled write phase must be an error, not a clean run")
	}
}
