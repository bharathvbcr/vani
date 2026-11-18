package pipeline

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Attack tests for the newest-posting exemption (card
// ft-ae057be0ac19ae51ab9d56474ff7ee7c, commit d3f2c5f).

// eventLedger is a fixed set of records that captures what is appended, so a
// run can be checked for writes, and whose All may repeat a key.
type eventLedger struct {
	recs []ledger.Record
	mu   sync.Mutex
	evs  []ledger.Event
}

func (e *eventLedger) Append(ctx context.Context, ev ledger.Event) error {
	return e.AppendBatch(ctx, []ledger.Event{ev})
}
func (e *eventLedger) AppendBatch(ctx context.Context, evs []ledger.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	e.evs = append(e.evs, evs...)
	e.mu.Unlock()
	return nil
}
func (e *eventLedger) Get(k posting.Key) (ledger.Record, bool) { return ledger.Record{}, false }
func (e *eventLedger) All() []ledger.Record                    { return e.recs }
func (e *eventLedger) Close() error                            { return nil }

func atkRecord(t *testing.T, id, company, title, desc string, at time.Time) ledger.Record {
	t.Helper()
	k, err := posting.GreenhouseKey("acme", id)
	if err != nil {
		t.Fatal(err)
	}
	return ledger.Record{Key: k, Status: posting.Live, FirstSeen: at,
		Posting: posting.Posting{Key: k, ATS: posting.Greenhouse, Company: company, Title: title, Location: "Remote", Description: desc}}
}

func atkRun(t *testing.T, l ledger.Ledger, opt NearDupOptions) NearDupReport {
	t.Helper()
	return annotate(t, l, t1, opt)
}

// Hitting the candidate cap under a spent deadline: the newest posting's
// first candidate has none of its own and is decided, the second has one whose
// status is unknown, so the newest is left undecided: nothing is written,
// only those two descriptions are read however many older postings there are,
// and no similarity reading is made.
func TestNearDupAttackNewestAtTheCandidateCapIsBoundedWork(t *testing.T) {
	l := openLedger(t)
	const older = NearDupMaxCandidates + 10
	var keys []posting.Key
	for i := range older {
		keys = append(keys, ndAdd(t, l, fmt.Sprintf("o%03d", i), "Acme", "Engineer", "Remote", words("duty", 100), t0.Add(time.Duration(i)*time.Second)))
	}
	newest := ndAdd(t, l, "zz", "Acme", "Engineer", "Remote", shares(80, 100), t0.Add(time.Hour)) // a candidate of every older one
	calls := 0
	before := l.All()
	rep := atkRun(t, l, NearDupOptions{
		Deadline: time.Nanosecond,
		Similarity: func(ctx context.Context, a, b string) (float64, bool, error) {
			calls++
			return 0.5, true, nil
		},
	})
	// Examined is the oldest posting (a leaf). Shingled is the descriptions read: none beyond the first pairs.
	if rep.Records != older+1 || rep.Examined != 1 || rep.Compared != 0 || rep.Shingled > 2 ||
		rep.Candidates != 0 || calls != 0 || !rep.Partial || rep.Written != 0 {
		t.Fatalf("report %+v, similarity calls %d", rep, calls)
	}
	if got := rec(t, l, newest).Annotations; len(got) != 0 {
		t.Fatalf("the undecided newest posting was annotated: %v", got)
	}
	for _, k := range keys {
		if !reflect.DeepEqual(rec(t, l, k), before[indexOfKey(before, k)]) {
			t.Fatalf("an older posting %s was written", k)
		}
	}
}

func indexOfKey(rs []ledger.Record, k posting.Key) int {
	for i, r := range rs {
		if r.Key == k {
			return i
		}
	}
	return -1
}

// A long chain of copies, each one's candidates undecided because the deadline
// cut them: the newest does not recurse into them (at most two descriptions
// are read), is left undecided rather than guessed, nothing is written, and
// nothing panics on the nil outcomes.
func TestNearDupAttackNewestDoesNotRecurseIntoCutCandidates(t *testing.T) {
	l := openLedger(t)
	jd := words("duty", 100)
	const n = 300
	var keys []posting.Key
	for i := range n {
		keys = append(keys, ndAdd(t, l, fmt.Sprintf("c%03d", i), "Acme", "Engineer", "Remote", jd, t0.Add(time.Duration(i)*time.Second)))
	}
	rep := atkRun(t, l, NearDupOptions{Deadline: time.Nanosecond})
	if !rep.Partial || rep.Examined != 1 || rep.Duplicates != 0 || rep.Written != 0 || rep.Shingled > 2 {
		t.Fatalf("report %+v", rep)
	}
	wantLive(t, l, keys...)
	for _, k := range keys[:n-1] {
		if a := rec(t, l, k).Annotations; len(a) != 0 {
			t.Fatalf("%s annotated by a cut run: %v", k, a)
		}
	}
}

// Identical copies with identical FirstSeen (the key order breaks the ties):
// a full run makes the greatest key a duplicate of the least, and a spent
// deadline writes nothing rather than a guess.
func TestNearDupAttackIdenticalFirstSeenTiesBreakByKey(t *testing.T) {
	build := func() (ledger.Ledger, []posting.Key) {
		l := openLedger(t)
		var ks []posting.Key
		for _, id := range []string{"9", "10", "11", "2", "30"} { // not numeric order
			ks = append(ks, ndAdd(t, l, id, "Acme", "Engineer", "Remote", words("duty", 100), t0))
		}
		return l, ks
	}
	cut, _ := build()
	full, _ := build()
	cr := atkRun(t, cut, NearDupOptions{Deadline: time.Nanosecond})
	fr := atkRun(t, full, NearDupOptions{})
	if !cr.Partial || cr.Examined != 1 || cr.Written != 0 || fr.Partial || fr.Duplicates != 4 {
		t.Fatalf("cut %+v full %+v", cr, fr)
	}
	// The key sorting last is the newest: in both runs it is a duplicate of the key sorting first.
	var newest, first posting.Key
	for _, r := range full.All() {
		if newest == "" || r.Key > newest {
			newest = r.Key
		}
		if first == "" || r.Key < first {
			first = r.Key
		}
	}
	wantLive(t, cut, newest, first)
	wantDuplicate(t, full, newest, first)
}

// Postings with no company or title are not compared at all: the newest one
// that is, is the one decided, and a newest whose title is only a bracket
// (no words left to match) is decided with nothing to compare.
func TestNearDupAttackNewestWithEmptyOrWordlessTitle(t *testing.T) {
	l := openLedger(t)
	jd := words("duty", 100)
	a := ndAdd(t, l, "1", "Acme", "Engineer", "Remote", jd, t0)
	b := ndAdd(t, l, "2", "Acme", "Engineer", "Remote", jd, t0.Add(time.Minute))
	ndAdd(t, l, "3", "Acme", "", "Remote", jd, t0.Add(2*time.Minute))     // no title: never a record
	ndAdd(t, l, "4", "", "Engineer", "Remote", jd, t0.Add(3*time.Minute)) // no company
	rep := atkRun(t, l, NearDupOptions{Deadline: time.Nanosecond})
	if rep.Records != 2 || rep.Examined != 2 || rep.Duplicates != 1 || !rep.Partial {
		t.Fatalf("report %+v", rep)
	}
	wantDuplicate(t, l, b, a)

	l = openLedger(t)
	ndAdd(t, l, "1", "Acme", "Engineer", "Remote", jd, t0)
	w := ndAdd(t, l, "2", "Acme", "(Remote)", "Remote", jd, t0.Add(time.Minute)) // a title with no words
	rep = atkRun(t, l, NearDupOptions{Deadline: time.Nanosecond})
	if rep.Records != 2 || rep.Examined != 1 || rep.Compared != 0 || rep.Duplicates != 0 || rep.Written != 0 || !rep.Partial {
		t.Fatalf("wordless report %+v", rep)
	}
	wantLive(t, l, w)
}

// A key repeated in the ledger's listing (a corrupt or hand-built ledger): no
// panic, and a bounded run that writes each key at most once.
func TestNearDupAttackRepeatedKeyInTheListing(t *testing.T) {
	jd := words("duty", 100)
	first := atkRecord(t, "1", "Acme", "Engineer", jd, t0)
	newest := atkRecord(t, "2", "Acme", "Engineer", jd, t0.Add(time.Minute))
	l := &eventLedger{recs: []ledger.Record{first, newest, newest, newest}}
	rep, err := AnnotateNearDuplicates(ctx, l, t1, NearDupOptions{Deadline: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Examined != 1 || !rep.Partial {
		t.Fatalf("report %+v", rep)
	}
	seen := map[posting.Key]int{}
	for _, e := range l.evs {
		seen[e.Key]++
	}
	for k, c := range seen {
		if c > 1 {
			t.Fatalf("key %s written %d times: %v", k, c, l.evs)
		}
	}
}

// Cancelled before the run, during the newest's similarity reading (the reader
// honours the context), and during it with a reader that ignores the context:
// each is the caller's error and nothing is written.
func TestNearDupAttackCancellation(t *testing.T) {
	jd := words("duty", 100)
	mk := func() (*eventLedger, ledger.Record) {
		a := atkRecord(t, "1", "Acme", "Engineer", jd, t0)
		b := atkRecord(t, "2", "Acme", "Engineer", shares(80, 100), t0.Add(time.Minute))
		return &eventLedger{recs: []ledger.Record{a, b}}, b
	}
	t.Run("before", func(t *testing.T) {
		l, _ := mk()
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := AnnotateNearDuplicates(cctx, l, t1, NearDupOptions{Deadline: time.Nanosecond}); err == nil || len(l.evs) != 0 {
			t.Fatalf("err %v, writes %v", err, l.evs)
		}
	})
	for _, tc := range []struct {
		name  string
		after bool // the reader ignores the context
	}{{"during similarity, honoured", false}, {"during similarity, ignored", true}} {
		t.Run(tc.name, func(t *testing.T) {
			l, _ := mk()
			cctx, cancel := context.WithCancel(ctx)
			defer cancel()
			calls := 0
			_, err := AnnotateNearDuplicates(cctx, l, t1, NearDupOptions{
				Deadline: 10 * time.Second,
				Similarity: func(rc context.Context, a, b string) (float64, bool, error) {
					calls++
					cancel()
					if tc.after {
						return 0.5, true, nil
					}
					<-rc.Done()
					return 0, false, rc.Err()
				},
			})
			if err == nil || len(l.evs) != 0 || calls != 1 {
				t.Fatalf("err %v, writes %v, calls %d", err, l.evs, calls)
			}
		})
	}
}

// Cancelled while a big ledger is being pre-warmed (the cancel lands at some
// point of the run): whichever way the race goes, a returned error means no
// writes, and no run writes for a posting twice.
func TestNearDupAttackCancelDuringPrewarm(t *testing.T) {
	var recs []ledger.Record
	for i := range 400 {
		recs = append(recs, atkRecord(t, fmt.Sprintf("p%04d", i), "Acme", "Engineer", words("duty", 400), t0.Add(time.Duration(i)*time.Second)))
	}
	for _, delay := range []time.Duration{0, 200 * time.Microsecond, 2 * time.Millisecond, 10 * time.Millisecond} {
		l := &eventLedger{recs: recs}
		cctx, cancel := context.WithCancel(ctx)
		timer := time.AfterFunc(delay, cancel)
		_, err := AnnotateNearDuplicates(cctx, l, t1, NearDupOptions{Deadline: 2 * time.Second})
		timer.Stop()
		cancel()
		if err != nil && len(l.evs) != 0 && cctx.Err() != nil {
			// A write batch that completed before the cancel is acknowledged
			// work; the error says only that the run did not finish. The cancel
			// came before the write phase when no event is present.
			seen := map[posting.Key]bool{}
			for _, e := range l.evs {
				if seen[e.Key] {
					t.Fatalf("delay %v: key %s written twice", delay, e.Key)
				}
				seen[e.Key] = true
			}
		}
	}
}

// The similarity budget: a spent deadline leaves it nothing (the reader is
// never called); a deadline that runs out before the newest's reading (a slow
// step before it) still bounds the reading by the budget, not by the stage.
func TestNearDupAttackSimilarityBudgetBoundsTheNewest(t *testing.T) {
	l := openLedger(t)
	a := ndAdd(t, l, "1", "Acme", "Engineer", "Remote", words("duty", 100), t0)
	b := ndAdd(t, l, "2", "Acme", "Engineer", "Remote", shares(80, 100), t0.Add(time.Minute))
	const deadline = 400 * time.Millisecond
	budget := time.Duration(float64(deadline) * NearDupSimilarityShare)
	var remaining time.Duration
	calls := 0
	start := time.Now()
	rep := atkRun(t, l, NearDupOptions{
		Deadline: deadline,
		// Runs in the newest's decision before its reading, and spends the whole stage deadline.
		Protected: func(k posting.Key) bool {
			if k == b {
				time.Sleep(deadline + 50*time.Millisecond)
			}
			return false
		},
		Similarity: func(rc context.Context, x, y string) (float64, bool, error) {
			calls++
			dl, ok := rc.Deadline()
			if !ok {
				t.Error("the reading has no deadline")
			}
			remaining = time.Until(dl)
			<-rc.Done() // a reader that only stops when told to
			return 0, false, rc.Err()
		},
	})
	elapsed := time.Since(start)
	if calls != 1 || remaining > budget || rep.SimilaritySkipped != 1 || !rep.SimilarityBudgetSpent || rep.Examined < 1 {
		t.Fatalf("report %+v, calls %d, reading allowed %v of budget %v", rep, calls, remaining, budget)
	}
	// Stalled for the deadline plus one budget, with slack for a loaded host.
	if limit := deadline + 50*time.Millisecond + budget + 400*time.Millisecond; elapsed > limit {
		t.Fatalf("run took %v, want under %v", elapsed, limit)
	}
	if got := rec(t, l, b).Annotations[AnnDupCandidateOf]; got != string(a) {
		t.Fatalf("candidate of %q, want %q", got, a)
	}
	if rep.SimilarityTook > budget+400*time.Millisecond {
		t.Fatalf("readings took %v of a %v budget", rep.SimilarityTook, budget)
	}
}
