package pipeline

import (
	"slices"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/decide"
)

// Re-review findings on the policy reopen (5a9970b, 2026-11-12). Each test
// here failed on 5a9970b.

// inboxBoard is the board the inbox drain folds a scraped page under
// (cmd/vani drainInbox): a careers-page board no watchlist names.
var inboxBoard = sources.Board{ATS: posting.Page, Token: "ext", Company: "Acme"}

// inboxTitleDrop lists a scraped page posting titled title on l through the
// inbox's partial fold and drops it on that title.
func inboxTitleDrop(t *testing.T, l ledger.Ledger, title string) (*Runner, posting.Key) {
	t.Helper()
	k, err := posting.PageKey(inboxBoard.Token, "https://acme.example/jobs/1")
	if err != nil {
		t.Fatal(err)
	}
	p := posting.Posting{Key: k, ATS: posting.Page, Company: "Acme", Title: title, Location: "Remote", Description: "jd text"}
	if _, err := NewReconciler(l).ReconcilePartial(ctx, inboxBoard, []posting.Posting{p}, t0); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Ledger: l, Triager: &fakeTriager{ans: decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{
		"seniority": jev("mid", 0.95), "role_fit": jev("strong_fit", 0.95)}}}, Scorer: fakeScorer{},
		Explainer: fakeExplainer{text: "n"}, Policy: DropPolicy{Rules: titleRules, OpenRouter: "d1-ultra",
			ReadTitle: readsOn(map[string]TitleReading{"Staff": {Slot: "seniority", Value: "staff_plus"}})},
		Now: func() time.Time { return t0.Add(time.Minute) }}
	if err := r.Advance(ctx, k); err != nil || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("setup: %v %s", err, rec(t, l, k).Status)
	}
	return r, k
}

// watchNothing is the scope of a run whose watchlist names none of the test
// boards (the inbox's page token is never on it).
func watchNothing() ReopenScope {
	sc := reviewScope()
	sc.Watched = func(posting.Key) bool { return false }
	return sc
}

// Finding A: a page posting the inbox folded (ReconcilePartial, no absence
// pass) sits on no watched board, so 5a9970b counted its title drop stale
// and never let it back. A live inbox posting is never aged either, so the
// watchlist test says nothing about it: before f254c4d it was reopened, and
// it is again.
func TestRetriageTitleDropsReopensAnInboxPosting(t *testing.T) {
	l := openLedger(t)
	r, k := inboxTitleDrop(t, l, "Staff Software Engineer")
	kept := r.Policy
	kept.Rules = keptStaff
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), kept, func() ReopenScope { s := watchNothing(); s.TitleOnly = true; return s }(), false)
	if err != nil || !slices.Equal(got.Title, []posting.Key{k}) || got.Stale != 0 {
		t.Fatalf("inbox title drop: %+v (%v), want it back and nothing stale", got, err)
	}
	if st := rec(t, l, k).Status; st != posting.Live {
		t.Fatalf("status %s, want live", st)
	}
}

// Finding B: the exclusions were re-checked before a model drop came back,
// never before a title drop. A title drop that an exclusion now matches went
// back to Live, and the next run dropped it again as an exclusion.
func TestRetriageTitleDropsRespectsExclusions(t *testing.T) {
	r, _, l, k := titleRunner(t, "Staff Software Engineer", titleRules)
	if err := r.Advance(ctx, k); err != nil || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("setup: %v %s", err, rec(t, l, k).Status)
	}
	kept := r.Policy
	kept.Rules = keptStaff
	kept.Exclude = func(posting.Posting) (string, bool) { return "company is \"Acme\"", true }
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), kept, reviewScope(), false)
	if err != nil || got.Returned() != 0 {
		t.Fatalf("returned %+v (%v), want nothing: an exclusion holds it", got, err)
	}
	if st := rec(t, l, k).Status; st != posting.Dropped {
		t.Fatalf("status %s, want dropped", st)
	}
}

// Finding C: the watch check ran before the prefilter, so a drop the
// exclusions still hold, on an unwatched board, was counted stale: "the rules
// would let it back" when they would not.
func TestRetriageStaleCountsOnlyDropsTheRulesWouldLetBack(t *testing.T) {
	l := openLedger(t)
	_, _, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
	p := pol(keptStaff)
	p.Exclude = func(posting.Posting) (string, bool) { return "company is \"Acme\"", true }
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), p, watchNothing(), false)
	if err != nil || got.Stale != 0 || got.Returned() != 0 {
		t.Fatalf("got %+v (%v), want nothing stale: the exclusion still drops %s", got, err, k)
	}
}

// Finding H: a model drop's reading is bound to the text it read
// (triage.*_sha256). A dropped posting that reappears after a miss takes the
// listing's new title or location and keeps its old reading, so a reopen
// would put back a reading of other text. It goes back to Live for a fresh
// read instead.
func TestRetriageModelDropWhoseTextMovedGoesBackToLive(t *testing.T) {
	l := openLedger(t)
	_, _, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
	if rec(t, l, k).Annotations[ledger.AnnTriageTitleSHA256] == "" {
		t.Fatal("setup: the reading carries no title hash")
	}
	rewriteReading(t, l, k, map[string]string{ledger.AnnTriageTitleSHA256: posting.RawSHA256("Senior Software Engineer 1")})
	if _, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), reviewScope(), false); err != nil {
		t.Fatal(err)
	}
	after := rec(t, l, k)
	if after.Status != posting.Live || after.Annotations["triage.seniority"] != "" {
		t.Fatalf("status %s, triage.seniority %q: want live with the stale reading forgotten", after.Status, after.Annotations["triage.seniority"])
	}
}
