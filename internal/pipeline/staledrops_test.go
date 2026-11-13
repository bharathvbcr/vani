package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

func classByDropper(r ledger.Record) string { return dropperOf(r) + ":cls" }

// managerOnly is the policy of the live config after "staff_plus" left its
// seniority rule: the same reader and backend, a narrower rule.
func managerOnly(p DropPolicy) DropPolicy {
	p.Rules = []DropRule{{Slot: "seniority", Values: []string{"manager"}, MinOpenRouterProb: 0.9}}
	return p
}

// modelDropped advances a posting whose title no rule reads to a model drop on
// seniority = staff_plus, p 0.9124, with the other readings the Databricks
// postings carried.
func modelDropped(t *testing.T) (*Runner, *ledger.File, posting.Key) {
	t.Helper()
	r, ft, l, k := titleRunner(t, "Software Engineer", titleRules)
	ft.ans = decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{
		"seniority":   jev("staff_plus", 0.9124),
		"role_fit":    jev("mismatch_seniority", 0.5958),
		"sponsorship": jev("not_stated", 0.8847),
	}}
	if err := r.Advance(ctx, k); err != nil || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("setup: %v, status %s", err, rec(t, l, k).Status)
	}
	return r, l, k
}

// A drop made under a rule that has since changed is stale; the same policy
// still drops it. The report names the readings it was made on, no posting text.
func TestStaleDropsFollowTheRules(t *testing.T) {
	r, l, k := modelDropped(t)

	rep, err := StaleDrops(ctx, l, r.Policy, classByDropper)
	if err != nil || rep.Dropped != 1 || rep.StillDropped != 1 || rep.Stale != 0 || rep.NotJudged != 0 {
		t.Fatalf("unchanged rules: %+v (%v)", rep, err)
	}

	rep, err = StaleDrops(ctx, l, managerOnly(r.Policy), classByDropper)
	if err != nil || rep.Stale != 1 || rep.StillDropped != 0 || len(rep.Items) != 1 {
		t.Fatalf("staff_plus rule removed: %+v (%v)", rep, err)
	}
	it := rep.Items[0]
	if it.Key != k || it.Class != "model:cls" || it.By != DropByModel || it.Today != "triaged" {
		t.Errorf("item %+v", it)
	}
	if len(rep.Classes) != 1 || rep.Classes[0] != (StaleClass{Class: "model:cls", Count: 1}) {
		t.Errorf("classes %+v", rep.Classes)
	}
	got := map[string]StaleReading{}
	for _, rd := range it.Recorded {
		got[rd.Slot] = rd
	}
	if s := got["seniority"]; s.Value != "staff_plus" || s.Backend != "d1-ultra" || s.P == nil || *s.P != 0.9124 {
		t.Errorf("seniority reading %+v", s)
	}
	if s, ok := got["sponsorship"]; !ok || s.Value != "" {
		t.Errorf("sponsorship value must be left out: %+v", s)
	}

	// A threshold raised over the recorded p is the same story.
	hi := r.Policy
	hi.Rules = []DropRule{{Slot: "seniority", Values: []string{"staff_plus"}, MinOpenRouterProb: 0.95}}
	if rep, _ := StaleDrops(ctx, l, hi, classByDropper); rep.Stale != 1 {
		t.Errorf("raised threshold: %+v", rep)
	}
	// No drop rule at all: nothing today drops it.
	if rep, _ := StaleDrops(ctx, l, DropPolicy{OpenRouter: "d1-ultra"}, classByDropper); rep.Stale != 1 {
		t.Errorf("no rules: %+v", rep)
	}
	// A nil class function classes a drop by what dropped it.
	if rep, _ := StaleDrops(ctx, l, managerOnly(r.Policy), nil); rep.Items[0].Class != "model:other" {
		t.Errorf("nil class: %+v", rep.Items)
	}
	if rec(t, l, k).Status != posting.Dropped {
		t.Fatal("the report wrote")
	}
}

// The report carries no title, company, location or description.
func TestStaleReportHoldsNoPostingText(t *testing.T) {
	r, l, k := modelDropped(t)
	p := rec(t, l, k).Posting
	rep, err := StaleDrops(ctx, l, managerOnly(r.Policy), classByDropper)
	if err != nil || rep.Stale != 1 {
		t.Fatalf("%+v (%v)", rep, err)
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{p.Title, p.Company, p.Location, p.Description, "not_stated"} {
		if text != "" && strings.Contains(string(b), text) {
			t.Errorf("report holds %q: %s", text, b)
		}
	}
}

// The rescue returns a stale model drop to Unsure, keeps what the model read,
// records why, is a no-op the second time, and a dry run writes nothing.
func TestRescueStaleDrops(t *testing.T) {
	r, l, k := modelDropped(t)
	today := managerOnly(r.Policy)
	at := t0.Add(2 * time.Hour)

	out, err := RescueStaleDrops(ctx, l, at, today, true)
	if err != nil || len(out.Rescued) != 1 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("dry run: %+v (%v), status %s", out, err, rec(t, l, k).Status)
	}
	out, err = RescueStaleDrops(ctx, l, at, today, false)
	if err != nil || len(out.Rescued) != 1 || out.Rescued[0] != k {
		t.Fatalf("rescue: %+v (%v)", out, err)
	}
	got := rec(t, l, k)
	if got.Status != posting.Unsure || got.Annotations["triage.seniority"] != "staff_plus" ||
		got.Annotations["triage.seniority.p"] != "0.9124" || !strings.Contains(got.Annotations[ledger.AnnTriageUnsure], "earlier rule") {
		t.Fatalf("after rescue: %s %v", got.Status, got.Annotations)
	}
	if got.Posting.Description == "" {
		t.Error("the description was lost")
	}

	again, err := RescueStaleDrops(ctx, l, at.Add(time.Hour), today, false)
	if err != nil || len(again.Rescued) != 0 || rec(t, l, k).LastEvent != got.LastEvent {
		t.Fatalf("second run: %+v (%v)", again, err)
	}
	if rep, _ := StaleDrops(ctx, l, today, classByDropper); rep.Dropped != 0 {
		t.Errorf("rescued record still reported: %+v", rep)
	}

	// The rescued posting goes on through the pipeline as an uncertain one.
	r.Policy = today
	if err := r.Advance(ctx, k); err != nil || rec(t, l, k).Status != posting.Explained {
		t.Fatalf("advance after rescue: %v, status %s", err, rec(t, l, k).Status)
	}
}

// A drop today's policy still makes is never touched, however the rescue is asked.
func TestRescueRefusesAStillDroppedPosting(t *testing.T) {
	r, l, k := modelDropped(t)
	out, err := RescueStaleDrops(ctx, l, t0.Add(time.Hour), r.Policy, false)
	if err != nil || len(out.Rescued) != 0 || out.StillDropped != 1 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("%+v (%v), status %s", out, err, rec(t, l, k).Status)
	}
}

// A stale title or exclusion drop is reported, and the rescue leaves it to the
// mechanism that returns it to Live (a backend has to read it).
func TestStaleTitleAndExclusionDrops(t *testing.T) {
	r, _, l, k := titleRunner(t, "Staff Software Engineer", titleRules)
	if err := r.Advance(ctx, k); err != nil || !TitleDropped(rec(t, l, k)) {
		t.Fatalf("setup: %v", err)
	}
	if rep, _ := StaleDrops(ctx, l, r.Policy, classByDropper); rep.StillDropped != 1 || rep.Stale != 0 {
		t.Fatalf("same rules: %+v", rep)
	}
	kept := managerOnly(r.Policy)
	rep, err := StaleDrops(ctx, l, kept, classByDropper)
	if err != nil || rep.Stale != 1 || rep.Items[0].By != DropByTitle || rep.Items[0].Today != "live" || rep.Items[0].Class != "title:cls" {
		t.Fatalf("title rules changed: %+v (%v)", rep, err)
	}
	out, err := RescueStaleDrops(ctx, l, t0.Add(time.Hour), kept, false)
	if err != nil || len(out.Rescued) != 0 || out.LeftToOther != 1 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("rescue of a title drop: %+v (%v)", out, err)
	}

	// An exclusion drop: stale once the exclusions are gone, still dropped while they match.
	l2 := openLedger(t)
	p := ghPost(t, "7", "jd text")
	p.Title = "Software Engineer Intern"
	if _, err := Reconcile(ctx, l2, board, []posting.Posting{p}, t0); err != nil {
		t.Fatal(err)
	}
	excl := DropPolicy{OpenRouter: "d1-ultra", Exclude: func(p posting.Posting) (string, bool) {
		return "title matches Intern", strings.Contains(p.Title, "Intern")
	}}
	r2 := &Runner{Ledger: l2, Triager: &fakeTriager{}, Scorer: fakeScorer{}, Explainer: fakeExplainer{text: "n"}, Policy: excl,
		Now: func() time.Time { return t0.Add(time.Minute) }}
	if err := r2.Advance(ctx, p.Key); err != nil || !Excluded(rec(t, l2, p.Key)) {
		t.Fatalf("exclusion setup: %v", err)
	}
	if rep, _ := StaleDrops(ctx, l2, excl, classByDropper); rep.StillDropped != 1 {
		t.Fatalf("exclusion still matches: %+v", rep)
	}
	rep, _ = StaleDrops(ctx, l2, DropPolicy{OpenRouter: "d1-ultra"}, classByDropper)
	if rep.Stale != 1 || rep.Items[0].By != DropByExclude {
		t.Fatalf("exclusion lifted: %+v", rep)
	}
}

// drop appends a Dropped event with the given annotations to a posting.
func drop(t *testing.T, l ledger.Ledger, id string, ann map[string]string) posting.Key {
	t.Helper()
	p := ghPost(t, id, "jd text")
	if _, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0); err != nil {
		t.Fatal(err)
	}
	ev := ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Minute), Key: p.Key, Status: posting.Dropped, Annotations: ann}
	if err := l.Append(ctx, ev); err != nil {
		t.Fatal(err)
	}
	return p.Key
}

// Model drops lead the list, so a cap on it cuts title drops, not the few
// model drops a rescue acts on.
func TestStaleItemsListModelDropsFirst(t *testing.T) {
	r, l, k := modelDropped(t) // key "gh:acme:1"
	// A title drop whose key sorts before the model drop's.
	p := ghPost(t, "0", "jd text")
	if _, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0); err != nil {
		t.Fatal(err)
	}
	ev := ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Minute), Key: p.Key, Status: posting.Dropped,
		Annotations: map[string]string{"triage.backend": "title", "triage.seniority": "staff_plus", "triage.seniority.reading": "answered"}}
	if err := l.Append(ctx, ev); err != nil {
		t.Fatal(err)
	}
	rep, err := StaleDrops(ctx, l, managerOnly(r.Policy), classByDropper)
	if err != nil || len(rep.Items) != 2 || rep.Items[0].Key != k || rep.Items[1].Key != p.Key {
		t.Fatalf("%+v (%v)", rep.Items, err)
	}
}

// A drop that cannot be re-judged is counted apart, never as stale: no
// readings, a backend today's policy does not calibrate, a probability that is
// missing or not a number, or nothing saying who dropped it.
func TestStaleDropsCountWhatCannotBeJudged(t *testing.T) {
	l := openLedger(t)
	read := func(backend, p string) map[string]string {
		a := map[string]string{"triage.backend": backend, "triage.seniority": "staff_plus",
			"triage.seniority.reading": "answered", "triage.seniority.backend": backend}
		if p != "" {
			a["triage.seniority.p"] = p
		}
		return a
	}
	drop(t, l, "1", map[string]string{"triage.backend": "d1-ultra"})     // no readings
	drop(t, l, "2", read("old-model", "0.97"))                           // another backend's drop
	drop(t, l, "3", read("d1-ultra", ""))                                // no probability
	drop(t, l, "4", read("d1-ultra", "NaN"))                             // not a number
	drop(t, l, "5", read("d1-ultra", "-0.2"))                            // out of range
	drop(t, l, "6", map[string]string{"triage.seniority": "staff_plus"}) // nobody recorded
	today := DropPolicy{OpenRouter: "d1-ultra", Rules: []DropRule{{Slot: "seniority", Values: []string{"staff_plus"}, MinOpenRouterProb: 0.9}}}

	rep, err := StaleDrops(ctx, l, today, classByDropper)
	if err != nil || rep.Dropped != 6 || rep.NotJudged != 6 || rep.Stale != 0 || rep.StillDropped != 0 {
		t.Fatalf("%+v (%v)", rep, err)
	}
	for _, why := range []string{notJudgedNoReadings, notJudgedUncalib, notJudgedNoP, notJudgedNoDropper} {
		if rep.NotJudgedWhy[why] == 0 {
			t.Errorf("no count for %q: %v", why, rep.NotJudgedWhy)
		}
	}
	if rep.NotJudgedWhy[notJudgedNoP] != 3 {
		t.Errorf("missing, NaN and out-of-range probabilities: %v", rep.NotJudgedWhy)
	}
	out, err := RescueStaleDrops(ctx, l, t0.Add(time.Hour), today, false)
	if err != nil || len(out.Rescued) != 0 || out.NotJudged != 6 {
		t.Fatalf("rescue: %+v (%v)", out, err)
	}
	for _, r := range l.All() {
		if r.Status != posting.Dropped {
			t.Errorf("%s was written: %s", r.Key, r.Status)
		}
	}
}

// With drop-on-probability switched off for the rule, a recorded drop is stale
// (the rule changed), not unjudgeable.
func TestStaleDropsRuleSwitchedOff(t *testing.T) {
	r, l, _ := modelDropped(t)
	off := r.Policy
	off.Rules = []DropRule{{Slot: "seniority", Values: []string{"staff_plus"}}}
	if rep, _ := StaleDrops(ctx, l, off, classByDropper); rep.Stale != 1 || rep.NotJudged != 0 {
		t.Fatalf("%+v", rep)
	}
}

// A policy that names no OpenRouter backend while a rule drops on a probability
// cannot judge any model drop: it says so instead of calling them all stale.
func TestStaleDropsRefuseAnUnjudgeablePolicy(t *testing.T) {
	r, l, k := modelDropped(t)
	bad := r.Policy
	bad.OpenRouter = ""
	if _, err := StaleDrops(ctx, l, bad, classByDropper); !errors.Is(err, ErrPolicyUnjudgeable) {
		t.Errorf("report: %v", err)
	}
	if _, err := RescueStaleDrops(ctx, l, t0, bad, false); !errors.Is(err, ErrPolicyUnjudgeable) {
		t.Errorf("rescue: %v", err)
	}
	if rec(t, l, k).Status != posting.Dropped {
		t.Fatal("an unjudgeable policy wrote")
	}
}

// evictedLedger answers every record without its description, as a ledger whose
// bodies cannot be read back does.
type evictedLedger struct{ ledger.Ledger }

func evict(r ledger.Record) ledger.Record { r.Posting.Description, r.BodyEvicted = "", true; return r }

func (e evictedLedger) All() []ledger.Record {
	var out []ledger.Record
	for _, r := range e.Ledger.All() {
		out = append(out, evict(r))
	}
	return out
}

func (e evictedLedger) Get(k posting.Key) (ledger.Record, bool) {
	r, ok := e.Ledger.Get(k)
	return evict(r), ok
}

// A policy that reads the description (exclusions, title readings) cannot judge
// a drop whose body is gone; a policy that does not can judge it, but the
// rescue still leaves it, because reopening it needs the body.
func TestStaleDropsWithoutTheBody(t *testing.T) {
	r, l, k := modelDropped(t)
	ev := evictedLedger{l}

	rep, err := StaleDrops(ctx, ev, managerOnly(r.Policy), classByDropper) // r.Policy reads titles
	if err != nil || rep.NotJudged != 1 || rep.NotJudgedWhy[notJudgedNoBody] != 1 || rep.Stale != 0 {
		t.Fatalf("prefiltering policy: %+v (%v)", rep, err)
	}
	plain := DropPolicy{OpenRouter: "d1-ultra", Rules: []DropRule{{Slot: "seniority", Values: []string{"manager"}, MinOpenRouterProb: 0.9}}}
	if rep, _ := StaleDrops(ctx, ev, plain, classByDropper); rep.Stale != 1 {
		t.Fatalf("model-only policy needs no body: %+v", rep)
	}
	out, err := RescueStaleDrops(ctx, ev, t0.Add(time.Hour), plain, false)
	if err != nil || len(out.Rescued) != 0 || out.NotJudged != 1 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("rescue without a body: %+v (%v)", out, err)
	}
}

func TestStaleDropsStopOnCancel(t *testing.T) {
	r, l, k := modelDropped(t)
	c, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := StaleDrops(c, l, managerOnly(r.Policy), classByDropper); !errors.Is(err, context.Canceled) {
		t.Errorf("report: %v", err)
	}
	if _, err := RescueStaleDrops(c, l, t0, managerOnly(r.Policy), false); !errors.Is(err, context.Canceled) {
		t.Errorf("rescue: %v", err)
	}
	if rec(t, l, k).Status != posting.Dropped {
		t.Fatal("a cancelled rescue wrote")
	}
}

// An empty ledger, and one with nothing dropped, report nothing; the lists are
// empty, not null.
func TestStaleDropsEmpty(t *testing.T) {
	l := openLedger(t)
	rep, err := StaleDrops(ctx, l, DropPolicy{}, nil)
	if err != nil || rep.Dropped != 0 || rep.Items == nil || rep.Classes == nil || rep.NotJudgedWhy == nil {
		t.Fatalf("%+v (%v)", rep, err)
	}
	if out, err := RescueStaleDrops(ctx, l, t0, DropPolicy{}, false); err != nil || len(out.Rescued) != 0 {
		t.Fatalf("%+v (%v)", out, err)
	}
}
