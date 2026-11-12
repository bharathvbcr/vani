package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// staffRules are the live rules before the owner kept Staff roles (2026-11-12).
var staffRules = []DropRule{
	{Slot: "seniority", Values: []string{"staff_plus", "manager"}, MinOpenRouterProb: 0.9},
	{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9},
}

// keptStaff is staffRules with staff_plus taken out of the seniority rule.
var keptStaff = []DropRule{
	{Slot: "seniority", Values: []string{"manager"}, MinOpenRouterProb: 0.9},
	{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9},
}

func staffAns(p float64) decide.Answer {
	return decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{
		"seniority": jev("staff_plus", p), "role_fit": jev("strong_fit", 0.95)}}
}

// modelDrop lists posting id on l and advances it under rules with ans as the
// backend's answer; it must end Dropped. It returns the runner (its Triager
// counts calls) and the key.
func modelDrop(t *testing.T, l ledger.Ledger, id string, ans decide.Answer, rules []DropRule) (*Runner, *fakeTriager, posting.Key) {
	t.Helper()
	p := ghPost(t, id, "jd text "+id)
	p.Title = "Software Engineer " + id
	if _, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0); err != nil {
		t.Fatal(err)
	}
	ft := &fakeTriager{ans: ans}
	r := &Runner{Ledger: l, Triager: ft, Scorer: fakeScorer{}, Explainer: fakeExplainer{text: "n"},
		Policy: DropPolicy{Rules: rules, OpenRouter: "d1-ultra"}, Badger: fakeBadger{"Acme": "LCA: 3 filings"},
		Now: func() time.Time { return t0.Add(time.Minute) }, Funnel: Funnel{ModelID: reviewModelID}}
	if err := r.Advance(ctx, p.Key); err != nil {
		t.Fatal(err)
	}
	if got := rec(t, l, p.Key); got.Status != posting.Dropped {
		t.Fatalf("setup: %s is %s, want dropped (%v)", p.Key, got.Status, got.Annotations)
	}
	return r, ft, p.Key
}

func pol(rules []DropRule) DropPolicy { return DropPolicy{Rules: rules, OpenRouter: "d1-ultra"} }

// The bug this file exists for: the owner takes staff_plus out of the drop
// rule, and a posting the decision model dropped as staff_plus stays dropped
// forever, because only title drops and exclusions had a way back. Its stored
// reading is still valid, so it comes back on that reading (triaged, not
// Live), and the next run scores it without asking a backend again.
func TestRetriagePolicyDropsReturnsAModelDropTheRulesNoLongerDrop(t *testing.T) {
	l := openLedger(t)
	r, ft, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)

	// Same rules: nothing comes back.
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(staffRules), reviewScope(), false)
	if err != nil || got.Returned() != 0 {
		t.Fatalf("unchanged rules returned %+v (%v)", got, err)
	}

	// Dry run under the new rules: reported, nothing written.
	got, err = RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), reviewScope(), true)
	if err != nil || !slices.Equal(got.Model, []posting.Key{k}) || len(got.Title) != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("dry run: %+v (%v), status %s", got, err, rec(t, l, k).Status)
	}

	got, err = RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), reviewScope(), false)
	if err != nil || !slices.Equal(got.Model, []posting.Key{k}) {
		t.Fatalf("retriage: %+v (%v)", got, err)
	}
	after := rec(t, l, k)
	if after.Status != posting.Triaged {
		t.Fatalf("status %s, want triaged on its stored reading", after.Status)
	}
	for key, want := range map[string]string{
		"triage.backend": "d1-ultra", "triage.seniority": "staff_plus", "triage.seniority.reading": "answered",
		"triage.seniority.p": "0.9500", "triage.role_fit": "strong_fit", ledger.AnnLCA: "LCA: 3 filings",
	} {
		if after.Annotations[key] != want {
			t.Errorf("%s = %q, want %q (the reading and badges are kept)", key, after.Annotations[key], want)
		}
	}
	if after.Annotations[ledger.AnnTriageUnsure] != "" {
		t.Errorf("a triaged reopen carries triage.unsure %q", after.Annotations[ledger.AnnTriageUnsure])
	}

	// The next run goes on from triage: scored and explained, no backend asked.
	r.Policy = pol(keptStaff)
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	if ft.calls != 1 || rec(t, l, k).Status != posting.Explained {
		t.Fatalf("re-advance: %d backend calls (want the original 1), status %s", ft.calls, rec(t, l, k).Status)
	}

	// Run again: nothing left to return, and the explained posting is untouched.
	if got, err := RetriagePolicyDrops(ctx, l, t0.Add(2*time.Hour), pol(keptStaff), reviewScope(), false); err != nil || got.Returned() != 0 {
		t.Fatalf("second retriage returned %+v (%v)", got, err)
	}
}

func TestRetriagePolicyDropsThresholds(t *testing.T) {
	for name, c := range map[string]struct {
		p         float64
		threshold float64
		reopened  bool
	}{
		"threshold raised past the reading": {p: 0.92, threshold: 0.95, reopened: true},
		"threshold raised, reading clears":  {p: 0.97, threshold: 0.95, reopened: false},
		"threshold lowered":                 {p: 0.92, threshold: 0.8, reopened: false},
		"rule switched off with 0":          {p: 0.99, threshold: 0, reopened: true},
	} {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			_, _, k := modelDrop(t, l, "1", staffAns(c.p), staffRules)
			rules := slices.Clone(staffRules)
			rules[0].MinOpenRouterProb = c.threshold
			got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(rules), reviewScope(), false)
			if err != nil {
				t.Fatal(err)
			}
			if reopened := slices.Contains(got.Model, k); reopened != c.reopened {
				t.Fatalf("reopened %v, want %v (%+v)", reopened, c.reopened, got)
			}
			want := posting.Dropped
			if c.reopened {
				want = posting.Triaged
			}
			if st := rec(t, l, k).Status; st != want {
				t.Fatalf("status %s, want %s", st, want)
			}
		})
	}
}

// A slot whose rule is removed no longer drops; a remaining rule the reading
// never answered makes it unsure, as a fresh triage would, and it is kept.
func TestRetriagePolicyDropsSlotRemovedAndUnsure(t *testing.T) {
	l := openLedger(t)
	_, _, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
	onlyRoleFit := []DropRule{staffRules[1]}
	if got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(onlyRoleFit), reviewScope(), false); err != nil || !slices.Equal(got.Model, []posting.Key{k}) {
		t.Fatalf("slot removed: %+v (%v)", got, err)
	}
	if st := rec(t, l, k).Status; st != posting.Triaged {
		t.Fatalf("status %s", st)
	}

	l2 := openLedger(t)
	_, _, k2 := modelDrop(t, l2, "2", staffAns(0.95), staffRules)
	// The owner now rules on a slot this reading never had.
	newSlot := []DropRule{{Slot: "sponsorship", Values: []string{"no"}, MinOpenRouterProb: 0.9}}
	if _, err := RetriagePolicyDrops(ctx, l2, t0.Add(time.Hour), pol(newSlot), reviewScope(), false); err != nil {
		t.Fatal(err)
	}
	got := rec(t, l2, k2)
	if got.Status != posting.Unsure || !strings.Contains(got.Annotations[ledger.AnnTriageUnsure], "sponsorship") {
		t.Fatalf("status %s, unsure %q; want unsure naming the missing slot", got.Status, got.Annotations[ledger.AnnTriageUnsure])
	}
}

// Another rule still drops the reading: it stays dropped.
func TestRetriagePolicyDropsStillDroppedByAnotherRule(t *testing.T) {
	l := openLedger(t)
	ans := staffAns(0.95)
	ans.Slots["role_fit"] = jev("mismatch_domain", 0.96)
	_, _, k := modelDrop(t, l, "1", ans, staffRules)
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), reviewScope(), false)
	if err != nil || got.Returned() != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("returned %+v (%v), status %s", got, err, rec(t, l, k).Status)
	}
}

func TestRetriagePolicyDropsLappiBackend(t *testing.T) {
	lappiAns := decide.Answer{Backend: "lappi", Slots: map[string]decide.SlotReading{
		"seniority": {Reading: decide.Answered, Value: "staff_plus", Lappi: &decide.LappiCalib{Score: 0.7}},
		"role_fit":  {Reading: decide.Answered, Value: "strong_fit", Lappi: &decide.LappiCalib{Score: 0.7}},
	}}
	rules := []DropRule{{Slot: "seniority", Values: []string{"staff_plus"}, MinLappiScore: 0.5}}
	l := openLedger(t)
	_, _, k := modelDrop(t, l, "1", lappiAns, rules)
	if got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(rules), reviewScope(), false); err != nil || got.Returned() != 0 {
		t.Fatalf("unchanged: %+v (%v)", got, err)
	}
	raised := slices.Clone(rules)
	raised[0].MinLappiScore = 0.8
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(raised), reviewScope(), false)
	if err != nil || !slices.Equal(got.Model, []posting.Key{k}) || rec(t, l, k).Status != posting.Triaged {
		t.Fatalf("raised lappi threshold: %+v (%v), status %s", got, err, rec(t, l, k).Status)
	}
}

// A reading the current policy cannot judge is left dropped and counted: one
// from a backend it does not trust (the decision model's old name, or no
// OpenRouter model configured), one with no calibration recorded, or a
// backend that never carries one (a hand-asked Claude reading). Reopening
// those would undo every drop the moment the model is renamed.
func TestRetriagePolicyDropsLeavesUnjudgeableReadings(t *testing.T) {
	for name, c := range map[string]struct {
		mutate func(map[string]string)
		policy DropPolicy
	}{
		"old backend name": {mutate: func(a map[string]string) {
			a["triage.backend"], a["triage.seniority.backend"], a["triage.role_fit.backend"] = "jev", "jev", "jev"
		}, policy: pol(keptStaff)},
		"no OpenRouter model configured": {mutate: func(map[string]string) {}, policy: DropPolicy{Rules: keptStaff}},
		"no calibration recorded": {mutate: func(a map[string]string) {
			a["triage.seniority.p"], a["triage.role_fit.p"] = "", ""
		}, policy: pol(keptStaff)},
		"hand-asked reading": {mutate: func(a map[string]string) {
			a["triage.backend"], a["triage.seniority.backend"], a["triage.role_fit.backend"] = "manual", "manual", "manual"
			a["triage.seniority.p"], a["triage.role_fit.p"] = "", ""
		}, policy: pol(keptStaff)},
	} {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			_, _, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
			ann := map[string]string{}
			c.mutate(ann)
			if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(2 * time.Minute), Key: k,
				Status: posting.Dropped, Annotations: ann, Reason: "test: rewrite the reading"}); err != nil {
				t.Fatal(err)
			}
			got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), c.policy, reviewScope(), false)
			if err != nil || got.Returned() != 0 || got.Unjudged != 1 || rec(t, l, k).Status != posting.Dropped {
				t.Fatalf("got %+v (%v), status %s; want left dropped and counted unjudged", got, err, rec(t, l, k).Status)
			}
		})
	}
}

// A copied reading is judged like an asked one, and keeps the key it was
// copied from.
func TestRetriagePolicyDropsInheritedReading(t *testing.T) {
	l := openLedger(t)
	a, b := dupPost(t, "1", "Remote", longDesc), dupPost(t, "2", "Remote", longDesc)
	reconcileFunnel(t, l, a, b)
	tr := &seqTriager{answers: []decide.Answer{staffAns(0.95)}}
	r := newRunner(l, tr, Funnel{Cache: NewReadCache(), ModelID: "m1"})
	r.Policy = pol(staffRules)
	for _, k := range []posting.Key{a.Key, b.Key} {
		if err := r.Advance(ctx, k); err != nil || rec(t, l, k).Status != posting.Dropped {
			t.Fatalf("setup %s: %v %s", k, err, rec(t, l, k).Status)
		}
	}
	if rec(t, l, b.Key).Annotations[AnnTriageInheritedFrom] != string(a.Key) {
		t.Fatal("setup: b did not inherit")
	}
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), reviewScope(), false)
	if err != nil || len(got.Model) != 2 {
		t.Fatalf("returned %+v (%v), want both", got, err)
	}
	if rb := rec(t, l, b.Key); rb.Status != posting.Triaged || rb.Annotations[AnnTriageInheritedFrom] != string(a.Key) {
		t.Fatalf("copy: %s %v", rb.Status, rb.Annotations)
	}
	if tr.calls != 1 {
		t.Fatalf("%d backend calls", tr.calls)
	}
}

// Title drops and model drops go through one selector: a title drop comes
// back Live for the backends to read (it carries no backend reading), a model
// drop comes back on its reading. RetriageTitleDrops is the title class alone.
func TestRetriagePolicyDropsTitleAndModelTogether(t *testing.T) {
	r, _, l, kt := titleRunner(t, "Staff Software Engineer", titleRules)
	if err := r.Advance(ctx, kt); err != nil || rec(t, l, kt).Status != posting.Dropped {
		t.Fatalf("title setup: %v", err)
	}
	_, _, km := modelDrop(t, l, "2", staffAns(0.95), titleRules)
	kept := r.Policy
	kept.Rules = keptStaff

	if keys, err := RetriageTitleDrops(ctx, l, t0.Add(time.Hour), kept, reviewScope(), true); err != nil || !slices.Equal(keys, []posting.Key{kt}) {
		t.Fatalf("title drops alone: %v (%v)", keys, err)
	}
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), kept, reviewScope(), false)
	if err != nil || !slices.Equal(got.Title, []posting.Key{kt}) || !slices.Equal(got.Model, []posting.Key{km}) {
		t.Fatalf("got %+v (%v)", got, err)
	}
	if rec(t, l, kt).Status != posting.Live || rec(t, l, kt).Annotations["triage.backend"] != "" {
		t.Fatalf("title drop: %s %v", rec(t, l, kt).Status, rec(t, l, kt).Annotations)
	}
	if rec(t, l, km).Status != posting.Triaged {
		t.Fatalf("model drop: %s", rec(t, l, km).Status)
	}
}

// What the current exclusions or title rules drop stays dropped: a fresh
// triage would drop it again before asking anyone. An exclusion drop is
// ApplyExclusions' to reopen, never this one's.
func TestRetriagePolicyDropsRespectsPrefilters(t *testing.T) {
	l := openLedger(t)
	_, _, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
	p := pol(keptStaff)
	p.Exclude = func(posting.Posting) (string, bool) { return "company is \"Acme\"", true }
	if got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), p, reviewScope(), false); err != nil || got.Returned() != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("excluded now: %+v (%v)", got, err)
	}
	p = pol(keptStaff)
	p.ReadTitle = readsOn(map[string]TitleReading{"Software": {Slot: "seniority", Value: "manager"}})
	if got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), p, reviewScope(), false); err != nil || got.Returned() != 0 {
		t.Fatalf("title rule drops it now: %+v (%v)", got, err)
	}

	// An exclusion drop is never touched here, whatever the exclusions say now.
	l2 := openLedger(t)
	q := ghPost(t, "9", "jd")
	if _, err := Reconcile(ctx, l2, board, []posting.Posting{q}, t0); err != nil {
		t.Fatal(err)
	}
	ex := pol(staffRules)
	ex.Exclude = func(posting.Posting) (string, bool) { return "company is \"Acme\"", true }
	rx := &Runner{Ledger: l2, Triager: &fakeTriager{}, Scorer: fakeScorer{}, Explainer: fakeExplainer{text: "n"}, Policy: ex,
		Now: func() time.Time { return t0.Add(time.Minute) }}
	if err := rx.Advance(ctx, q.Key); err != nil || rec(t, l2, q.Key).Status != posting.Dropped {
		t.Fatalf("exclusion setup: %v", err)
	}
	if got, err := RetriagePolicyDrops(ctx, l2, t0.Add(time.Hour), pol(keptStaff), reviewScope(), false); err != nil || got.Returned() != 0 || got.Unjudged != 0 {
		t.Fatalf("exclusion drop: %+v (%v)", got, err)
	}
}

func TestRetriagePolicyDropsEmptyLedger(t *testing.T) {
	got, err := RetriagePolicyDrops(ctx, openLedger(t), t0, pol(keptStaff), reviewScope(), false)
	if err != nil || got.Returned() != 0 || got.Unjudged != 0 {
		t.Fatalf("%+v (%v)", got, err)
	}
}

// batchCounter counts AppendBatch calls and their sizes, and can cancel the
// context after the first batch lands.
type batchCounter struct {
	*ledger.File
	sizes       []int
	cancelAfter context.CancelFunc
}

func (b *batchCounter) AppendBatch(ctx context.Context, evs []ledger.Event) error {
	if err := b.File.AppendBatch(ctx, evs); err != nil {
		return err
	}
	b.sizes = append(b.sizes, len(evs))
	if b.cancelAfter != nil {
		b.cancelAfter()
	}
	return nil
}

// manyDrops writes n model drops of staff_plus readings straight to l, in one
// batch per MaxBatchEvents, without running the pipeline n times.
func manyDrops(t *testing.T, l ledger.Ledger, n int) []posting.Key {
	t.Helper()
	ps := make([]posting.Posting, n)
	for i := range ps {
		ps[i] = ghPost(t, fmt.Sprint(i+1), "jd")
	}
	if _, err := Reconcile(ctx, l, board, ps, t0); err != nil {
		t.Fatal(err)
	}
	ann := triageAnnotations(staffAns(0.95))
	ann[AnnTriageModel] = reviewModelID
	evs := make([]ledger.Event, n)
	keys := make([]posting.Key, n)
	for i, p := range ps {
		evs[i] = ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Minute), Key: p.Key, Status: posting.Dropped, Annotations: ann, Reason: "dropped"}
		keys[i] = p.Key
	}
	if _, err := appendBatched(ctx, l, evs); err != nil {
		t.Fatal(err)
	}
	return keys
}

// More reopenings than one ledger batch holds are written in bounded batches,
// never one oversized batch and never one fsync per posting.
func TestRetriagePolicyDropsBatchesWithinLedgerBounds(t *testing.T) {
	if testing.Short() {
		t.Skip("writes over 4096 events")
	}
	f := openLedger(t)
	n := ledger.MaxBatchEvents + 3
	keys := manyDrops(t, f, n)
	bc := &batchCounter{File: f}
	got, err := RetriagePolicyDrops(ctx, bc, t0.Add(time.Hour), pol(keptStaff), reviewScope(), false)
	if err != nil || len(got.Model) != n {
		t.Fatalf("returned %d of %d (%v)", len(got.Model), n, err)
	}
	if len(bc.sizes) < 2 || slices.Max(bc.sizes) > ledger.MaxBatchEvents {
		t.Fatalf("batches %v, want at least 2, none over %d", bc.sizes, ledger.MaxBatchEvents)
	}
	for _, k := range []posting.Key{keys[0], keys[n-1]} {
		if st := rec(t, f, k).Status; st != posting.Triaged {
			t.Fatalf("%s is %s", k, st)
		}
	}
}

// Cancelled after the first batch: what landed is reported, the rest stays
// dropped, and the error says why.
func TestRetriagePolicyDropsCancelMidBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("writes over 4096 events")
	}
	f := openLedger(t)
	n := ledger.MaxBatchEvents + 3
	keys := manyDrops(t, f, n)
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	bc := &batchCounter{File: f, cancelAfter: cancel}
	got, err := RetriagePolicyDrops(c, bc, t0.Add(time.Hour), pol(keptStaff), reviewScope(), false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want canceled", err)
	}
	if len(bc.sizes) != 1 || len(got.Model) != bc.sizes[0] || len(got.Model) >= n {
		t.Fatalf("reported %d, batches %v", len(got.Model), bc.sizes)
	}
	written := map[posting.Key]bool{}
	for _, k := range got.Model {
		written[k] = true
	}
	dropped := 0
	for _, k := range keys {
		st := rec(t, f, k).Status
		if written[k] != (st == posting.Triaged) {
			t.Fatalf("%s is %s, reported written %v", k, st, written[k])
		}
		if st == posting.Dropped {
			dropped++
		}
	}
	if dropped != n-len(got.Model) {
		t.Fatalf("%d left dropped, want %d", dropped, n-len(got.Model))
	}
}

func TestRetriagePolicyDropsCancelledBeforeStart(t *testing.T) {
	l := openLedger(t)
	_, _, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
	c, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := RetriagePolicyDrops(c, l, t0, pol(keptStaff), reviewScope(), false); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if rec(t, l, k).Status != posting.Dropped {
		t.Fatal("cancelled retriage wrote")
	}
}
