package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

var bfT0 = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

func bfKey(i int) posting.Key {
	k, err := posting.GreenhouseKey("acme", fmt.Sprint(1000+i))
	if err != nil {
		panic(err)
	}
	return k
}

// bfPrimary is the annotations a triage leaves for the slots it read with Liquid D
// at the probabilities in ps (every slot of the fixture is read; others at 0.95).
func bfPrimary(ps map[string]float64) map[string]string {
	a := decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{}}
	for _, s := range jdtriage.Slots {
		p := 0.95
		if v, ok := ps[s.Name]; ok {
			p = v
		}
		a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: s.Options[0].Value, Jev: &decide.JevCalib{Probability: p}, Detail: "backend=d1-ultra"}
	}
	ann := map[string]string{"triage.backend": "d1-ultra"}
	for _, s := range jdtriage.Slots {
		sr := a.Slots[s.Name]
		ann["triage."+s.Name] = sr.Value
		ann["triage."+s.Name+".reading"] = "answered"
		ann["triage."+s.Name+".backend"] = "d1-ultra"
		ann["triage."+s.Name+".p"] = fmt.Sprintf("%.4f", sr.Jev.Probability)
	}
	return ann
}

// bfSeed appends a posting at status st with the annotations ann (and a rank, so
// the priority order has something to read).
func bfSeed(t *testing.T, l ledger.Ledger, i int, st posting.Status, ann map[string]string, coverage string) posting.Key {
	t.Helper()
	k := bfKey(i)
	p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Acme", Title: fmt.Sprintf("Engineer %d", i),
		Description: "Build and operate large-scale inference systems. Fully remote within the US.", PostedAt: bfT0.Add(-time.Hour)}
	a := maps.Clone(ann)
	if coverage != "" {
		a[ledger.AnnCoverage], a[ledger.AnnRank] = coverage, coverage
	}
	if err := l.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: bfT0.Add(-time.Duration(i) * time.Minute), Key: k, Status: st, Posting: &p, Annotations: a}); err != nil {
		t.Fatal(err)
	}
	return k
}

func bfLedger(t *testing.T) *ledger.File {
	t.Helper()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func bfRunner(l ledger.Ledger) *pipeline.Runner {
	return &pipeline.Runner{Ledger: l, Now: func() time.Time { return bfT0 }}
}

func asked(f *escFake) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.asked {
		out = append(out, strings.Join(a, ","))
	}
	return out
}

// answerWith is an escFake reply that answers every slot asked with value(slot).
func answerWith(value func(slot decide.Slot) string) func(decide.Question) (decide.Answer, error) {
	return func(q decide.Question) (decide.Answer, error) {
		a := decide.Answer{Backend: escalateName, Slots: map[string]decide.SlotReading{}}
		for _, s := range q.Slots {
			a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: value(s)}
		}
		return a, nil
	}
}

func lastOption(s decide.Slot) string { return s.Options[len(s.Options)-1] }

func escalatorFor(t *testing.T, f *escFake, belowP float64, maxPerRun int) *escalator {
	t.Helper()
	e, _, _ := testEscalator(t, &primaryStub{}, f, belowP, maxPerRun)
	return e
}

func itemKeys(items []backfillItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = string(it.key)
	}
	return out
}

// Only a wanted slot the primary read under below_p, that the tier has not read, is
// a candidate; the order is the caller's.
func TestBackfillItemsPicksOnlyUnsureUnescalatedSlots(t *testing.T) {
	want := map[string]bool{"role_fit": true, "work_mode": true}
	withShadow := func(slot, reading, backend string) map[string]string {
		a := bfPrimary(map[string]float64{"role_fit": 0.3})
		a["triage."+slot+".shadow_reading"], a["triage."+slot+".shadow_backend"] = reading, backend
		return a
	}
	nan := bfPrimary(nil)
	nan["triage.role_fit.p"] = "NaN"
	noP := bfPrimary(map[string]float64{"role_fit": 0.3})
	delete(noP, "triage.role_fit.p")
	unanswered := bfPrimary(map[string]float64{"role_fit": 0.3})
	unanswered["triage.role_fit.reading"] = "unavailable"
	atBelow := bfPrimary(map[string]float64{"role_fit": 0.5})
	unwanted := bfPrimary(map[string]float64{"seniority": 0.2})
	both := bfPrimary(map[string]float64{"role_fit": 0.1, "work_mode": 0.4, "seniority": 0.1})

	cases := []struct {
		name string
		ann  map[string]string
		want string // slots, comma separated; "" = not a candidate
	}{
		{"unsure fit", bfPrimary(map[string]float64{"role_fit": 0.3}), "role_fit"},
		{"two wanted slots", both, "role_fit,work_mode"},
		{"confident", bfPrimary(nil), ""},
		{"exactly below_p", atBelow, ""},
		{"unwanted slot only", unwanted, ""},
		{"NaN p", nan, ""},
		{"no p (not Liquid D's reading)", noP, ""},
		{"primary not an answer", unanswered, ""},
		{"tier already answered", withShadow("role_fit", "answered", "escalate"), ""},
		{"tier failed", withShadow("role_fit", "failed", "escalate"), ""},
		{"tier outage is asked again", withShadow("role_fit", "unavailable", "escalate"), "role_fit"},
		{"another shadow's outage", withShadow("role_fit", "unavailable", "lappi"), ""},
		{"another shadow answered", withShadow("role_fit", "answered", "lappi"), ""},
	}
	var recs []ledger.Record
	for i, c := range cases {
		recs = append(recs, ledger.Record{Key: bfKey(i), Status: posting.Explained, Annotations: c.ann})
	}
	items := backfillItems(recs, 0.5, want)
	got := map[string]string{}
	for _, it := range items {
		names := make([]string, len(it.slots))
		for i, s := range it.slots {
			names[i] = s.Name
			if len(s.Options) < 2 || it.asked[s.Name] == "" {
				t.Errorf("%s: slot %s carries no options or no primary value", it.key, s.Name)
			}
		}
		got[string(it.key)] = strings.Join(names, ",")
	}
	for i, c := range cases {
		if got[string(bfKey(i))] != c.want {
			t.Errorf("%s: asked %q, want %q", c.name, got[string(bfKey(i))], c.want)
		}
	}
	// The order is kept.
	if keys := itemKeys(items); !slices.IsSortedFunc(keys, func(a, b string) int { return strings.Compare(a, b) }) {
		t.Errorf("order not kept: %v", keys)
	}
}

func seedUnsure(t *testing.T, l ledger.Ledger, n int) []posting.Key {
	var keys []posting.Key
	for i := 0; i < n; i++ {
		keys = append(keys, bfSeed(t, l, i, posting.Explained, bfPrimary(map[string]float64{"role_fit": 0.3}), fmt.Sprint(90-i)))
	}
	return keys
}

func itemsOf(l ledger.Ledger) []backfillItem {
	recs := l.All()
	slices.SortFunc(recs, func(a, b ledger.Record) int { return strings.Compare(string(a.Key), string(b.Key)) })
	return backfillItems(recs, 0.5, map[string]bool{"role_fit": true})
}

// The pass asks the tier about the unsure slots only, records the readings beside
// the primary's with a triage's annotations, leaves the primary's reading and the
// status alone, and a second pass asks nothing new.
func TestBackfillPassRecordsOnceAndIsIdempotent(t *testing.T) {
	l := bfLedger(t)
	keys := seedUnsure(t, l, 5)
	before := map[posting.Key]ledger.Record{}
	for _, k := range keys {
		r, _ := l.Get(k)
		before[k] = r
	}
	f := &escFake{reply: answerWith(lastOption)}
	e := escalatorFor(t, f, 0.5, 100)
	rep := backfillPass(context.Background(), bfRunner(l), e, itemsOf(l), "", 2)
	if rep.Candidates != 5 || rep.Slots != 5 || rep.Answered != 5 || rep.Recorded != 5 || rep.Stopped != "" || rep.Failed != 0 || rep.Deferred != 0 {
		t.Fatalf("report %+v", rep)
	}
	if f.calls.Load() != 5 || !slices.Equal(slices.Compact(asked(f)), []string{"role_fit"}) {
		t.Fatalf("asked %v in %d call(s)", asked(f), f.calls.Load())
	}
	for _, k := range keys {
		got, _ := l.Get(k)
		if got.Status != posting.Explained {
			t.Errorf("%s: status %s", k, got.Status)
		}
		for key, v := range before[k].Annotations {
			if got.Annotations[key] != v {
				t.Errorf("%s: %s moved from %q to %q", k, key, v, got.Annotations[key])
			}
		}
		for key, v := range map[string]string{
			"triage.role_fit.shadow": "mismatch_seniority" /* the last option */, "triage.role_fit.shadow_reading": "answered",
			"triage.role_fit.shadow_backend": escalateName, "triage.role_fit.disagree": "true",
		} {
			if got.Annotations[key] != v {
				t.Errorf("%s: %s = %q, want %q", k, key, got.Annotations[key], v)
			}
		}
	}
	// Idempotent: nothing is a candidate any more, and a pass over nothing asks nothing.
	if items := itemsOf(l); len(items) != 0 {
		t.Fatalf("still candidates after the pass: %v", itemKeys(items))
	}
	rep2 := backfillPass(context.Background(), bfRunner(l), escalatorFor(t, f, 0.5, 100), itemsOf(l), "", 2)
	if rep2 != (escalateBackfillReport{}) || f.calls.Load() != 5 {
		t.Fatalf("second pass %+v, %d calls", rep2, f.calls.Load())
	}
}

// max_per_run is shared with the triage: what the run's triage spent is not
// available to the backfill, the best postings go first, the rest are deferred.
func TestBackfillPassRespectsMaxPerRunSharedWithTheTriage(t *testing.T) {
	l := bfLedger(t)
	keys := seedUnsure(t, l, 5) // coverage 90, 89, ...: keys[0] is the best
	f := &escFake{reply: answerWith(lastOption)}
	e := escalatorFor(t, f, 0.5, 3)
	e.used.Add(1) // the run's triage already escalated one slot
	rep := backfillPass(context.Background(), bfRunner(l), e, itemsOf(l), "", 1)
	if rep.Slots != 2 || rep.Recorded != 2 || rep.Deferred != 3 || rep.Stopped != backfillStopCap {
		t.Fatalf("report %+v", rep)
	}
	if f.calls.Load() != 2 || e.Escalated() != 3 {
		t.Fatalf("%d call(s), escalated %d", f.calls.Load(), e.Escalated())
	}
	for i, k := range keys {
		r, _ := l.Get(k)
		if got := r.Annotations["triage.role_fit.shadow_reading"] != ""; got != (i < 2) {
			t.Errorf("posting %d recorded=%v", i, got)
		}
	}
	// A spent budget asks nothing at all.
	e2 := escalatorFor(t, f, 0.5, 1)
	e2.used.Add(1)
	if rep := backfillPass(context.Background(), bfRunner(l), e2, itemsOf(l), "", 1); rep.Slots != 0 || rep.Stopped != backfillStopCap || rep.Deferred != 3 || f.calls.Load() != 2 {
		t.Fatalf("spent budget: %+v, %d calls", rep, f.calls.Load())
	}
}

// A posting with two slots to escalate and one slot of budget is asked about the
// first (fixture order); the other is deferred.
func TestBackfillPassSplitsAPostingAtTheCap(t *testing.T) {
	l := bfLedger(t)
	k := bfSeed(t, l, 1, posting.Explained, bfPrimary(map[string]float64{"role_fit": 0.2, "work_mode": 0.2}), "50")
	f := &escFake{reply: answerWith(lastOption)}
	recs := l.All()
	items := backfillItems(recs, 0.5, map[string]bool{"role_fit": true, "work_mode": true})
	rep := backfillPass(context.Background(), bfRunner(l), escalatorFor(t, f, 0.5, 1), items, "", 1)
	if rep.Slots != 1 || rep.Deferred != 1 || rep.Recorded != 1 || !slices.Equal(asked(f), []string{"role_fit"}) {
		t.Fatalf("report %+v asked %v", rep, asked(f))
	}
	r, _ := l.Get(k)
	if r.Annotations["triage.work_mode.shadow_reading"] != "" {
		t.Fatalf("work_mode was recorded: %v", r.Annotations)
	}
}

// The budget gate: when the llm daily cap refuses, the tier reads as unavailable.
// Nothing is recorded for those slots, the pass stops after a streak of them
// instead of hammering the provider, and a later run asks exactly what is left.
func TestBackfillPassStopsOnAnOutageAndALaterRunFinishesTheJob(t *testing.T) {
	l := bfLedger(t)
	seedUnsure(t, l, 30)
	var served atomic.Int64
	budgetLeft := int64(4)
	f := &escFake{reply: func(q decide.Question) (decide.Answer, error) {
		a := decide.Answer{Backend: escalateName, Slots: map[string]decide.SlotReading{}}
		for _, s := range q.Slots {
			if served.Add(1) <= budgetLeft {
				a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: lastOption(s)}
			} else {
				a.Slots[s.Name] = decide.SlotReading{Reading: decide.Unavailable, Detail: "budget"}
			}
		}
		return a, nil
	}}
	rep := backfillPass(context.Background(), bfRunner(l), escalatorFor(t, f, 0.5, 100), itemsOf(l), "", 1)
	if rep.Recorded != 4 || rep.Answered != 4 || rep.Unavailable != pipeline.MaxConsecutiveUnread || rep.Stopped != backfillStopUnavailable {
		t.Fatalf("report %+v", rep)
	}
	if got := f.calls.Load(); got > 4+pipeline.MaxConsecutiveUnread {
		t.Fatalf("%d calls: the pass did not stop after the streak", got)
	}
	if want := 30 - 4 - pipeline.MaxConsecutiveUnread; rep.Deferred != want {
		t.Fatalf("deferred %d, want %d", rep.Deferred, want)
	}
	// Outage readings left no annotation: those postings are still candidates.
	left := itemsOf(l)
	if len(left) != 26 {
		t.Fatalf("%d candidates left, want 26 (the 4 recorded are done)", len(left))
	}
	// The cap is back: the next run asks only what is left, each once.
	budgetLeft = 1 << 30
	before := f.calls.Load()
	rep2 := backfillPass(context.Background(), bfRunner(l), escalatorFor(t, f, 0.5, 100), left, "", 3)
	if rep2.Recorded != 26 || f.calls.Load()-before != 26 || len(itemsOf(l)) != 0 {
		t.Fatalf("second run %+v, %d new calls", rep2, f.calls.Load()-before)
	}
}

// A refused slot in the middle of answers does not trip the streak.
func TestBackfillPassStreakResetsOnAReading(t *testing.T) {
	l := bfLedger(t)
	seedUnsure(t, l, 3*pipeline.MaxConsecutiveUnread)
	var n atomic.Int64
	f := &escFake{reply: func(q decide.Question) (decide.Answer, error) {
		a := decide.Answer{Slots: map[string]decide.SlotReading{}}
		for _, s := range q.Slots {
			if n.Add(1)%2 == 0 {
				a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: lastOption(s)}
			} else {
				a.Slots[s.Name] = decide.SlotReading{Reading: decide.Unavailable}
			}
		}
		return a, nil
	}}
	rep := backfillPass(context.Background(), bfRunner(l), escalatorFor(t, f, 0.5, 1000), itemsOf(l), "", 1)
	if rep.Stopped != "" || rep.Slots != 3*pipeline.MaxConsecutiveUnread || rep.Recorded != 3*pipeline.MaxConsecutiveUnread/2 {
		t.Fatalf("report %+v", rep)
	}
}

// failingLedger refuses every Append, as a ledger that could not write does.
type failingLedger struct{ ledger.Ledger }

func (failingLedger) Append(context.Context, ledger.Event) error { return errors.New("disk full") }

// A reading that cannot be written stops the pass and is counted; nothing else is
// asked meanwhile.
func TestBackfillPassStopsWhenTheLedgerCannotWrite(t *testing.T) {
	l := bfLedger(t)
	seedUnsure(t, l, 6)
	f := &escFake{reply: answerWith(lastOption)}
	run := bfRunner(failingLedger{l})
	rep := backfillPass(context.Background(), run, escalatorFor(t, f, 0.5, 100), itemsOf(l), "", 1)
	if rep.Stopped != backfillStopLedger || rep.Failed != 1 || rep.Recorded != 0 || f.calls.Load() != 1 || !strings.Contains(rep.Note, "disk full") {
		t.Fatalf("report %+v after %d call(s)", rep, f.calls.Load())
	}
	if len(itemsOf(l)) != 6 {
		t.Fatal("a failed write left a trace")
	}
}

// A cancelled run and an expired pass ask nothing more and say why.
func TestBackfillPassCancelAndTimeout(t *testing.T) {
	l := bfLedger(t)
	seedUnsure(t, l, 4)
	f := &escFake{reply: answerWith(lastOption)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep := backfillPass(ctx, bfRunner(l), escalatorFor(t, f, 0.5, 100), itemsOf(l), "", 2)
	if f.calls.Load() != 0 || rep.Stopped != backfillStopCancelled || rep.Deferred != 4 {
		t.Fatalf("cancelled: %+v, %d calls", rep, f.calls.Load())
	}
	old := escalateBackfillTimeout
	escalateBackfillTimeout = 30 * time.Millisecond
	t.Cleanup(func() { escalateBackfillTimeout = old })
	slow := &escFake{reply: func(q decide.Question) (decide.Answer, error) {
		time.Sleep(60 * time.Millisecond)
		return answerWith(lastOption)(q)
	}}
	rep = backfillPass(context.Background(), bfRunner(l), escalatorFor(t, slow, 0.5, 100), itemsOf(l), "", 1)
	if rep.Stopped != backfillStopTimeout || slow.calls.Load() >= 4 {
		t.Fatalf("timeout: %+v after %d call(s)", rep, slow.calls.Load())
	}
}

// An unusable target summary builds no question: nothing is asked or recorded.
func TestBackfillPassWithAnUnusableSummaryAsksNothing(t *testing.T) {
	l := bfLedger(t)
	seedUnsure(t, l, 2)
	f := &escFake{reply: answerWith(lastOption)}
	rep := backfillPass(context.Background(), bfRunner(l), escalatorFor(t, f, 0.5, 100), itemsOf(l), strings.Repeat("x", jdtriage.MaxTargetSummaryBytes+1), 1)
	if f.calls.Load() != 0 || rep.Failed != 2 || rep.Recorded != 0 || rep.Note == "" {
		t.Fatalf("report %+v, %d calls", rep, f.calls.Load())
	}
}

// The pass is bounded by its workers and its escalator, and safe under -race: many
// postings, several workers, the cap exact, each posting asked once.
func TestBackfillPassConcurrentWorkersAskEachSlotOnce(t *testing.T) {
	l := bfLedger(t)
	seedUnsure(t, l, 60)
	f := &escFake{reply: answerWith(lastOption)}
	e := escalatorFor(t, f, 0.5, 40)
	rep := backfillPass(context.Background(), bfRunner(l), e, itemsOf(l), "", 8)
	if rep.Slots != 40 || rep.Recorded != 40 || rep.Deferred != 20 || f.calls.Load() != 40 || e.Escalated() != 40 {
		t.Fatalf("report %+v, %d calls", rep, f.calls.Load())
	}
	if n := len(itemsOf(l)); n != 20 {
		t.Fatalf("%d candidates left, want 20", n)
	}
}

// The report is counts and a bounded reason: no posting key, title or reading.
func TestBackfillReportHoldsNoPostingText(t *testing.T) {
	l := bfLedger(t)
	seedUnsure(t, l, 2)
	f := &escFake{reply: answerWith(lastOption)}
	rep := backfillPass(context.Background(), bfRunner(l), escalatorFor(t, f, 0.5, 100), itemsOf(l), "", 1)
	b, _ := json.Marshal(rep)
	for _, bad := range []string{"Engineer", "acme", "gh:", "mismatch_seniority"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("report %s contains %q", b, bad)
		}
	}
}

// --- the stage on the wired app ---

func bfWired(t *testing.T, l *ledger.File, mut func(*config.Config)) (*wired, *pipeline.Runner, *escFake, *escalator) {
	t.Helper()
	cfg := config.Default()
	cfg.Decide.Escalate = &config.Escalate{Enabled: true}
	if mut != nil {
		mut(&cfg)
	}
	f := &escFake{reply: answerWith(lastOption)}
	e := escalatorFor(t, f, 0.5, 100)
	w := &wired{cfg: cfg, stateDir: t.TempDir(), led: l, warn: &strings.Builder{}}
	r := bfRunner(l)
	r.Triager = &jdtriage.Triager{D: e}
	return w, r, f, e
}

// The stage asks about browsable, kept postings only: not a live, closed or dropped
// one, and not a confident or an already escalated one. Everything it records goes
// through the run's runner, and what it did reaches the run record.
func TestEscalateBackfillStageAsksOnlyBrowsableUnsureUnescalated(t *testing.T) {
	l := bfLedger(t)
	unsure := bfPrimary(map[string]float64{"role_fit": 0.3})
	kept := map[string]posting.Key{
		"explained": bfSeed(t, l, 1, posting.Explained, unsure, "70"),
		"digested":  bfSeed(t, l, 2, posting.Digested, unsure, "70"),
		"triaged":   bfSeed(t, l, 3, posting.Triaged, unsure, ""),
		"unsure":    bfSeed(t, l, 4, posting.Unsure, unsure, ""),
	}
	not := map[string]posting.Key{
		"live":      bfSeed(t, l, 5, posting.Live, unsure, ""),
		"dead":      bfSeed(t, l, 6, posting.Dead, unsure, "70"),
		"dropped":   bfSeed(t, l, 7, posting.Dropped, unsure, ""),
		"confident": bfSeed(t, l, 8, posting.Explained, bfPrimary(nil), "70"),
	}
	done := maps.Clone(unsure)
	done["triage.role_fit.shadow_reading"], done["triage.role_fit.shadow_backend"], done["triage.role_fit.shadow"] = "answered", "escalate", "adjacent"
	not["escalated"] = bfSeed(t, l, 9, posting.Explained, done, "70")
	// A closed posting the owner marked is still closed: it is not written.
	not["marked dead"] = bfSeed(t, l, 10, posting.Dead, unsure, "70")
	w, r, f, _ := bfWired(t, l, nil)
	if _, err := marks.SetMark(marks.Dir(w.stateDir), not["marked dead"], marks.Saved, bfT0); err != nil {
		t.Fatal(err)
	}
	snap := map[posting.Key]ledger.Record{}
	for _, k := range not {
		snap[k], _ = l.Get(k)
	}

	w.escalateBackfill(context.Background(), r)
	rep := w.runReport().EscalateBackfill
	if rep == nil || rep.Candidates != len(kept) || rep.Slots != len(kept) || rep.Recorded != len(kept) || rep.Stopped != "" {
		t.Fatalf("run record %+v", rep)
	}
	if f.calls.Load() != int64(len(kept)) {
		t.Fatalf("%d calls", f.calls.Load())
	}
	for name, k := range kept {
		if got, _ := l.Get(k); got.Annotations["triage.role_fit.shadow_backend"] != "escalate" {
			t.Errorf("%s was not recorded", name)
		}
	}
	for name, k := range not {
		got, _ := l.Get(k)
		if !maps.Equal(got.Annotations, snap[k].Annotations) || !got.LastEvent.Equal(snap[k].LastEvent) {
			t.Errorf("%s was written", name)
		}
	}
	// Reported through the run record, as JSON under escalate_backfill.
	b, _ := json.Marshal(w.runReport())
	if !strings.Contains(string(b), `"escalate_backfill":{"candidates":4,"slots":4,"answered":4,"recorded":4}`) {
		t.Fatalf("run detail %s", b)
	}
	// A second run asks nothing.
	w.escalateBackfill(context.Background(), r)
	if f.calls.Load() != int64(len(kept)) {
		t.Fatalf("a second stage asked again: %d calls", f.calls.Load())
	}
}

// The posting the ranking reads as the best fit goes first: with room for one slot
// the better of two unsure postings is asked, in the order the semantic warm uses.
func TestEscalateBackfillStageAsksTheBestPostingFirst(t *testing.T) {
	l := bfLedger(t)
	unsure := bfPrimary(map[string]float64{"role_fit": 0.3})
	low := bfSeed(t, l, 1, posting.Explained, unsure, "10")
	high := bfSeed(t, l, 2, posting.Explained, unsure, "95")
	w, r, f, e := bfWired(t, l, nil)
	e.maxRun = 1
	w.escalateBackfill(context.Background(), r)
	if f.calls.Load() != 1 {
		t.Fatalf("%d calls", f.calls.Load())
	}
	if h, _ := l.Get(high); h.Annotations["triage.role_fit.shadow_reading"] != "answered" {
		t.Fatal("the better posting was not asked")
	}
	if lw, _ := l.Get(low); lw.Annotations["triage.role_fit.shadow_reading"] != "" {
		t.Fatal("the worse posting was asked first")
	}
	if rep := w.runReport().EscalateBackfill; rep == nil || rep.Deferred != 1 || rep.Stopped != backfillStopCap {
		t.Fatalf("run record %+v", rep)
	}
}

// Slots the ranking does not read are not backfilled; a drop rule's slot is.
func TestEscalateBackfillStageAsksTheSlotsTheRankingReads(t *testing.T) {
	l := bfLedger(t)
	bfSeed(t, l, 1, posting.Explained, bfPrimary(map[string]float64{"role_fit": 0.3, "work_mode": 0.3, "seniority": 0.3}), "50")
	w, r, f, _ := bfWired(t, l, func(c *config.Config) {
		c.DropRules = []config.DropRule{{Slot: "work_mode", Values: []string{"onsite"}, MinOpenRouterProb: 0.9}}
	})
	w.escalateBackfill(context.Background(), r)
	if got := asked(f); len(got) != 1 {
		t.Fatalf("asked %v, want one question", got)
	}
	if got := asked(f)[0]; got != "role_fit,work_mode" {
		t.Fatalf("asked slots %q, want role_fit and the drop rule's work_mode, not seniority", got)
	}
}

// Off by config, or on with no tier this run, it is not a pass: nothing runs, and
// the second case says so in the record.
func TestEscalateBackfillStageNotRun(t *testing.T) {
	l := bfLedger(t)
	bfSeed(t, l, 1, posting.Explained, bfPrimary(map[string]float64{"role_fit": 0.3}), "50")

	w, r, f, _ := bfWired(t, l, func(c *config.Config) { c.Decide.Escalate = nil })
	w.escalateBackfill(context.Background(), r)
	if w.runReport().EscalateBackfill != nil || f.calls.Load() != 0 {
		t.Fatalf("decide.escalate off: %+v, %d calls", w.runReport().EscalateBackfill, f.calls.Load())
	}
	w, r, f, _ = bfWired(t, l, func(c *config.Config) { c.Decide.Escalate = &config.Escalate{Enabled: false} })
	w.escalateBackfill(context.Background(), r)
	if w.runReport().EscalateBackfill != nil || f.calls.Load() != 0 {
		t.Fatal("decide.escalate disabled still ran")
	}

	// On, but the run built no escalator (llm disabled): the record says it did not run.
	w, r, f, _ = bfWired(t, l, nil)
	r.Triager = &jdtriage.Triager{D: &primaryStub{}}
	w.escalateBackfill(context.Background(), r)
	if rep := w.runReport().EscalateBackfill; rep == nil || !strings.Contains(rep.Note, "not run") || rep.Slots != 0 || f.calls.Load() != 0 {
		t.Fatalf("no tier: %+v", rep)
	}
	// On, but no ledger: nothing is asked.
	w, r, f, _ = bfWired(t, l, nil)
	w.led = nil
	w.escalateBackfill(context.Background(), r)
	if rep := w.runReport().EscalateBackfill; rep == nil || !strings.Contains(rep.Note, "no ledger") || f.calls.Load() != 0 {
		t.Fatalf("no ledger: %+v", rep)
	}
	// A nil runner is no tier.
	if escalatorOf(nil) != nil {
		t.Fatal("a nil runner has an escalator")
	}
}

// Unreadable marks are said, not silently skipped; kept postings are still read.
func TestEscalateBackfillStageSaysWhenTheMarksCouldNotBeRead(t *testing.T) {
	l := bfLedger(t)
	bfSeed(t, l, 1, posting.Explained, bfPrimary(map[string]float64{"role_fit": 0.3}), "50")
	w, r, f, _ := bfWired(t, l, nil)
	// A regular file where the marks directory belongs cannot be read as one.
	dir := marks.Dir(w.stateDir)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := marks.ReadMarks(dir); err == nil {
		t.Fatal("setup: the marks are still readable")
	}
	w.escalateBackfill(context.Background(), r)
	rep := w.runReport().EscalateBackfill
	if rep == nil || !strings.Contains(rep.Note, "marks could not be read") || rep.Recorded != 1 || f.calls.Load() != 1 {
		t.Fatalf("report %+v", rep)
	}
}

// Only the daemon's run can backfill: the stage needs the ledger the run holds, and
// a second opener of that ledger is refused, so a backfill cannot run beside it.
func TestBackfillNeedsTheLedgerLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := ledger.Open(dir); !errors.Is(err, ledger.ErrLocked) {
		t.Fatalf("a second holder of the ledger: %v", err)
	}
	if _, err := wireApp(anthropicConfig(t), filepath.Dir(dir), nil); !errors.Is(err, ledger.ErrLocked) {
		t.Fatalf("wireApp over a held ledger: %v", err)
	}
}
