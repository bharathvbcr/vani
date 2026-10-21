package pipeline

import (
	"reflect"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// Every status a retriage can reopen drops the derived category, through both
// reopen paths; a record the match leaves alone keeps it.
func TestReopenForgetsCategoryFromEveryRetriageableStatus(t *testing.T) {
	l := openLedger(t)
	unread := map[string]string{ledger.AnnCategory: "backend", "triage.category": "backend", "triage.category.reading": "unavailable", ledger.AnnRank: "3", ledger.AnnLCA: "badge"}
	real := map[string]string{ledger.AnnCategory: "data", "triage.category": "data", "triage.category.reading": "answered"}
	var want []posting.Key
	for i, st := range []posting.Status{posting.Unsure, posting.Triaged, posting.Scored, posting.Explained} {
		p := ghPost(t, string(rune('a'+i)), "d")
		if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: p.Key, Status: st, Posting: &p, Annotations: unread}); err != nil {
			t.Fatal(err)
		}
		want = append(want, p.Key)
	}
	keep := ghPost(t, "z", "d")
	if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: keep.Key, Status: posting.Scored, Posting: &keep, Annotations: real}); err != nil {
		t.Fatal(err)
	}
	keys, err := RetriageUnread(ctx, l, t0.Add(time.Hour), false)
	if err != nil || len(keys) != len(want) {
		t.Fatalf("retriage = %v, %v", keys, err)
	}
	for _, k := range want {
		r := rec(t, l, k)
		if _, ok := r.Annotations[ledger.AnnCategory]; ok || r.Status != posting.Live {
			t.Errorf("%s: status %s annotations %v", k, r.Status, r.Annotations)
		}
		if r.Annotations[ledger.AnnLCA] != "badge" {
			t.Errorf("%s lost an unrelated annotation", k)
		}
	}
	if r := rec(t, l, keep.Key); r.Annotations[ledger.AnnCategory] != "data" || r.Status != posting.Scored {
		t.Errorf("a record with a real reading was touched: %+v", r)
	}
}

// A dropped-on-title record that carries a category (an older run left it) is
// reopened by RetriageTitleDrops without it.
func TestRetriageTitleDropsForgetsCategory(t *testing.T) {
	l := openLedger(t)
	p := ghPost(t, "1", "d")
	ann := map[string]string{ledger.AnnCategory: "backend", ledger.AnnTriagePrefix + "backend": TitleBackend}
	if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: p.Key, Status: posting.Dropped, Posting: &p, Annotations: ann}); err != nil {
		t.Fatal(err)
	}
	keys, err := RetriageTitleDrops(ctx, l, t0.Add(time.Hour), DropPolicy{}, false)
	if err != nil || len(keys) != 1 {
		t.Fatalf("= %v, %v", keys, err)
	}
	if v, ok := rec(t, l, p.Key).Annotations[ledger.AnnCategory]; ok {
		t.Errorf("category %q survived", v)
	}
}

// Old ledgers replay unchanged: a reopen event written before the category was
// in the forget set still leaves the category on replay (history is not
// rewritten), a new one removes it, and a cold reopen of the file folds to
// exactly the records the live ledger holds.
func TestCategoryReplayFoldsIdentically(t *testing.T) {
	dir := t.TempDir()
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := ghPost(t, "1", "d")
	ann := map[string]string{ledger.AnnCategory: "backend", "triage.category": "backend", "triage.category.reading": "unavailable"}
	if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: p.Key, Status: posting.Unsure, Posting: &p, Annotations: ann}); err != nil {
		t.Fatal(err)
	}
	legacy := ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Minute), Key: p.Key, Status: posting.Live,
		Annotations: map[string]string{ledger.AnnRank: "", "triage.category": "", "triage.category.reading": ""}, Reason: "retriage: legacy"}
	if err := l.Append(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if got := rec(t, l, p.Key).Annotations[ledger.AnnCategory]; got != "backend" {
		t.Fatalf("legacy reopen replay changed history: category %q", got)
	}
	cur := rec(t, l, p.Key)
	if err := l.Append(ctx, reopenEvent(cur, t0.Add(time.Hour), "retriage: new")); err != nil {
		t.Fatal(err)
	}
	live := l.All()
	if _, ok := live[0].Annotations[ledger.AnnCategory]; ok {
		t.Fatalf("new reopen left the category: %v", live[0].Annotations)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l2, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if cold := l2.All(); !reflect.DeepEqual(cold, live) {
		t.Errorf("cold replay differs:\n%+v\n%+v", cold, live)
	}
}

// A description change reopens a Scored record through Reconcile; the next
// triage with no category answer leaves none, and one with a category sets it.
func TestReconcileThenAdvanceCategory(t *testing.T) {
	noCat := decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": jev("remote", 0.99)}}
	r, l, k := runnerWith(t, noCat, nil)
	r.SkipScoring = true
	if err := l.Append(ctx, ledger.Event{V: 1, At: t0.Add(time.Second), Key: k, Status: posting.Scored,
		Annotations: map[string]string{ledger.AnnCategory: "old"}}); err != nil {
		t.Fatal(err)
	}
	p := ghPost(t, "1", "a different jd text")
	if _, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	if v, ok := rec(t, l, k).Annotations[ledger.AnnCategory]; ok {
		t.Errorf("category %q survived a description change and a categoryless triage", v)
	}
}
