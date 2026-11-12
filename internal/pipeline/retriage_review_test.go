package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/jobcategory"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// Review findings on the policy reopen (2026-11-12): each test here failed on
// 9384225, the commit that introduced RetriagePolicyDrops.

// reviewModelID is the model id the test runners record readings under
// (modelDrop, the inherited-reading runner) and reviewScope judges against.
const reviewModelID = "m1"

// reviewScope is the scope a run with today's question and every test board
// watched would pass: the shipped slot options, the test model id.
func reviewScope() ReopenScope {
	opts := jdtriage.SlotValues()
	opts[jobcategory.Slot] = jobcategory.OptionValues()
	return ReopenScope{ModelID: reviewModelID, Options: opts, Watched: func(posting.Key) bool { return true }}
}

// rewriteReading replaces k's stored triage annotations with ann, keeping it
// dropped, as a later model or question would have written them.
func rewriteReading(t *testing.T, l ledger.Ledger, k posting.Key, ann map[string]string) {
	t.Helper()
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(2 * time.Minute), Key: k,
		Status: posting.Dropped, Annotations: ann, Reason: "test: rewrite the reading"}); err != nil {
		t.Fatal(err)
	}
}

// Finding 1: a reading was judged when any one slot carried a calibration the
// policy can read. Here the drop was made on seniority by the decision model
// under its old name; the category slot carries a Lappi score. After the owner
// switches the OpenRouter model, the seniority reading cannot be judged, so
// the posting must stay dropped and be counted, not reopened because the
// Lappi slot happened to be readable.
func TestRetriagePolicyDropsMixedBackendIsUnjudged(t *testing.T) {
	l := openLedger(t)
	ans := decide.Answer{Backend: "d1-ultra+lappi", Slots: map[string]decide.SlotReading{
		"seniority": {Reading: decide.Answered, Value: "staff_plus", Detail: "backend=d1-ultra", Jev: &decide.JevCalib{Probability: 0.95}},
		"role_fit":  {Reading: decide.Answered, Value: "strong_fit", Detail: "backend=d1-ultra", Jev: &decide.JevCalib{Probability: 0.95}},
		"category":  {Reading: decide.Answered, Value: jobcategory.AIEngineer, Detail: "backend=lappi", Lappi: &decide.LappiCalib{Score: 0.7}},
	}}
	_, _, k := modelDrop(t, l, "1", ans, staffRules)
	// The model id is held equal on purpose: the slot check alone must catch
	// it, whatever the id says.
	renamed := DropPolicy{Rules: staffRules, OpenRouter: "d2-ultra"}
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), renamed, reviewScope(), false)
	if err != nil || got.Returned() != 0 || got.Unjudged != 1 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("got %+v (%v), status %s; want left dropped and counted unjudged", got, err, rec(t, l, k).Status)
	}
	// Under the model it was made with, the same mixed reading is judged: the
	// Lappi slot is readable too, and staff_plus no longer drops.
	got, err = RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), reviewScope(), false)
	if err != nil || !slices.Equal(got.Model, []posting.Key{k}) {
		t.Fatalf("same model: %+v (%v)", got, err)
	}
}

// Finding 2: the model id a reading was made under was never compared. A
// reading made under another model id (or none recorded) is not the current
// question's answer, so it is not judged: counted, left dropped. A stored
// value that is no longer a legal option for its slot (a renamed option) is
// unjudged too, not "no rule matches".
func TestRetriagePolicyDropsReadingFromAnotherQuestionIsUnjudged(t *testing.T) {
	// renamed is today's question after staff_plus became staff_or_above.
	renamed := reviewScope()
	renamed.Options["seniority"] = []string{"intern_or_new_grad", "mid", "senior", "staff_or_above", "manager"}
	for name, c := range map[string]struct {
		ann   map[string]string
		rules []DropRule
		scope ReopenScope
	}{
		"another model id":  {ann: map[string]string{AnnTriageModel: "or=old-model;ctx=000000000000"}, rules: keptStaff, scope: reviewScope()},
		"no model id":       {ann: map[string]string{AnnTriageModel: ""}, rules: keptStaff, scope: reviewScope()},
		"no current id":     {ann: map[string]string{}, rules: keptStaff, scope: ReopenScope{Options: reviewScope().Options, Watched: reviewScope().Watched}},
		"no options given":  {ann: map[string]string{}, rules: keptStaff, scope: ReopenScope{ModelID: reviewModelID, Watched: reviewScope().Watched}},
		"renamed option":    {ann: map[string]string{}, rules: renamedStaff, scope: renamed},
		"slot not in today": {ann: map[string]string{"triage.level": "senior", "triage.level.reading": "answered"}, rules: keptStaff, scope: reviewScope()},
	} {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			_, _, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
			rewriteReading(t, l, k, c.ann)
			got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(c.rules), c.scope, false)
			if err != nil || got.Returned() != 0 || got.Unjudged != 1 || rec(t, l, k).Status != posting.Dropped {
				t.Fatalf("got %+v (%v), status %s; want left dropped and counted unjudged", got, err, rec(t, l, k).Status)
			}
		})
	}
}

// renamedStaff is the seniority rule after a question that renamed staff_plus
// to staff_or_above.
var renamedStaff = []DropRule{
	{Slot: "seniority", Values: []string{"staff_or_above", "manager"}, MinOpenRouterProb: 0.9},
	{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9},
}

// Finding 3: a drop on a board that is no longer watched never ages (a
// dropped posting is closed, and no listing of the board runs the absence
// pass), so reopening it would leave it advanceable for good. It stays
// dropped and is counted stale, for a model drop and a title drop alike.
func TestRetriagePolicyDropsUnwatchedBoardIsStale(t *testing.T) {
	r, _, l, kt := titleRunner(t, "Staff Software Engineer", titleRules)
	if err := r.Advance(ctx, kt); err != nil || rec(t, l, kt).Status != posting.Dropped {
		t.Fatalf("title setup: %v", err)
	}
	_, _, km := modelDrop(t, l, "2", staffAns(0.95), titleRules)
	kept := r.Policy
	kept.Rules = keptStaff
	sc := reviewScope()
	sc.Watched = func(posting.Key) bool { return false }

	keys, err := RetriageTitleDrops(ctx, l, t0.Add(time.Hour), kept, sc, false)
	if err != nil || len(keys) != 0 {
		t.Fatalf("title drops on an unwatched board: %v (%v)", keys, err)
	}
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), kept, sc, false)
	if err != nil || got.Returned() != 0 || got.Stale != 2 {
		t.Fatalf("got %+v (%v); want both left dropped and counted stale", got, err)
	}
	for _, k := range []posting.Key{kt, km} {
		if st := rec(t, l, k).Status; st != posting.Dropped {
			t.Fatalf("%s is %s", k, st)
		}
	}

	// A watched board reopens both, and a nil Watched (no watchlist given)
	// reopens nothing: a check that could not run is not a pass.
	sc.Watched = nil
	if got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), kept, sc, true); err != nil || got.Returned() != 0 || got.Stale != 2 {
		t.Fatalf("nil Watched: %+v (%v)", got, err)
	}
	got, err = RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), kept, reviewScope(), false)
	if err != nil || !slices.Equal(got.Title, []posting.Key{kt}) || !slices.Equal(got.Model, []posting.Key{km}) || got.Stale != 0 {
		t.Fatalf("watched: %+v (%v)", got, err)
	}
}

// unreadableBody is a ledger whose stored body for bad cannot be read back:
// Get returns it still evicted (as File.Get does when the read fails), and a
// write that would reopen it is refused the way File.prepare refuses it.
type unreadableBody struct {
	*ledger.File
	bad posting.Key
}

func (u unreadableBody) Get(k posting.Key) (ledger.Record, bool) {
	r, ok := u.File.Get(k)
	if ok && k == u.bad {
		r.Posting.Description, r.BodyEvicted = "", true
	}
	return r, ok
}

func (u unreadableBody) refuse(evs []ledger.Event) error {
	for _, e := range evs {
		if e.Key == u.bad {
			return fmt.Errorf("ledger: reopening %s: %w", e.Key, ledger.ErrBodyUnavailable)
		}
	}
	return nil
}

func (u unreadableBody) AppendBatch(ctx context.Context, evs []ledger.Event) error {
	if err := u.refuse(evs); err != nil {
		return err
	}
	return u.File.AppendBatch(ctx, evs)
}

func (u unreadableBody) Append(ctx context.Context, e ledger.Event) error {
	if err := u.refuse([]ledger.Event{e}); err != nil {
		return err
	}
	return u.File.Append(ctx, e)
}

// Finding 4: one record whose evicted body cannot be read back was classed
// for reopening on an empty description (so a description exclusion could not
// match it), and its event then refused the whole batch, so every rerun
// stopped at it. It is skipped and counted unreadable; the others land; a dry
// run reports the same classes.
func TestRetriagePolicyDropsUnreadableBodyIsSkipped(t *testing.T) {
	f := openLedger(t)
	_, _, k1 := modelDrop(t, f, "1", staffAns(0.95), staffRules)
	_, _, k2 := modelDrop(t, f, "2", staffAns(0.95), staffRules)
	u := unreadableBody{File: f, bad: k1}
	for _, dry := range []bool{true, false} {
		got, err := RetriagePolicyDrops(ctx, u, t0.Add(time.Hour), pol(keptStaff), reviewScope(), dry)
		if err != nil || !slices.Equal(got.Model, []posting.Key{k2}) || got.Unreadable != 1 {
			t.Fatalf("dry %v: %+v (%v); want k2 returned, k1 counted unreadable", dry, got, err)
		}
	}
	if rec(t, f, k1).Status != posting.Dropped || rec(t, f, k2).Status != posting.Triaged {
		t.Fatalf("k1 %s, k2 %s", rec(t, f, k1).Status, rec(t, f, k2).Status)
	}
}

// failingWrites refuses every batch, and every single append after the first
// ok of them, with an error that is not about a body.
type failingWrites struct {
	*ledger.File
	ok int
}

var errDiskFull = errors.New("test: disk full")

func (w *failingWrites) AppendBatch(context.Context, []ledger.Event) error { return errDiskFull }

func (w *failingWrites) Append(ctx context.Context, e ledger.Event) error {
	if w.ok == 0 {
		return errDiskFull
	}
	w.ok--
	return w.File.Append(ctx, e)
}

// Finding 5: before the batched reopen, a failure part way left the earlier
// reopenings written. A refused batch now falls back to one event at a time,
// so the ones written before the failure stay written and are reported, and
// the error says why it stopped.
func TestRetriageTitleDropsKeepsWhatLandedBeforeAFailure(t *testing.T) {
	r, _, l, k1 := titleRunner(t, "Staff Software Engineer", titleRules)
	// One full listing of all three, so none is aged absent by another's.
	p1 := ghPost(t, "1", "jd text")
	p1.Title = "Staff Software Engineer"
	listing := []posting.Posting{p1}
	ks := []posting.Key{k1}
	for _, id := range []string{"2", "3"} {
		p := ghPost(t, id, "jd text "+id)
		p.Title = "Staff Engineer " + id
		listing = append(listing, p)
		ks = append(ks, p.Key)
	}
	if _, err := Reconcile(ctx, l, board, listing, t0); err != nil {
		t.Fatal(err)
	}
	for _, k := range ks {
		if err := r.Advance(ctx, k); err != nil || rec(t, l, k).Status != posting.Dropped {
			t.Fatalf("setup %s: %v", k, err)
		}
	}
	kept := r.Policy
	kept.Rules = keptStaff
	kept.ReadTitle = nil
	w := &failingWrites{File: l, ok: 1}
	keys, err := RetriageTitleDrops(ctx, w, t0.Add(time.Hour), kept, reviewScope(), false)
	if !errors.Is(err, errDiskFull) || len(keys) != 1 {
		t.Fatalf("keys %v, err %v; want the first written and reported, then the error", keys, err)
	}
	if rec(t, l, keys[0]).Status != posting.Live {
		t.Fatalf("%s is %s, reported written", keys[0], rec(t, l, keys[0]).Status)
	}
	live := 0
	for _, k := range ks {
		if rec(t, l, k).Status == posting.Live {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("%d live, want exactly the reported one", live)
	}
}
