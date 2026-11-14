package pipeline

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A staff-titled drop whose description asks for fewer than staff years is
// not a staff reading (expyears.BelowStaff). The dropped record's body is
// evicted from l.All(), so the title verdict must read the full body: judged
// on an empty description it stays dropped for ever.
func TestRetriageTitleDropJudgesTheDescriptionOfAnEvictedBody(t *testing.T) {
	l := openLedger(t)
	p := ghPost(t, "1", "We ask for 3+ years of experience building services.")
	p.Title = "Staff Software Engineer"
	if _, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Minute), Key: p.Key, Status: posting.Dropped,
		Annotations: map[string]string{"triage.backend": TitleBackend}, Reason: "title"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range l.All() {
		if r.Key == p.Key && !r.BodyEvicted {
			t.Fatal("setup: the dropped record's body is not evicted from All()")
		}
	}
	policy := DropPolicy{Rules: titleRules, OpenRouter: "d1-ultra", ReadTitle: readsOn(map[string]TitleReading{
		"Staff": {Slot: "seniority", Value: "staff_plus"}})}

	keys, err := RetriageTitleDrops(ctx, l, t0.Add(time.Hour), policy, reviewScope(), false)
	if err != nil || len(keys) != 1 || keys[0] != p.Key {
		t.Fatalf("returned %v (%v): a staff title whose description asks 3 years was kept dropped", keys, err)
	}
	if got := rec(t, l, p.Key); got.Status == posting.Dropped {
		t.Fatalf("still dropped: %+v", got)
	}
}

// The same title with a description at staff level stays dropped, and a
// dropped record whose body cannot be read back is not judged on nothing.
func TestRetriageTitleDropStaffYearsStayDropped(t *testing.T) {
	l := openLedger(t)
	p := ghPost(t, "2", "You bring 10+ years of experience.")
	p.Title = "Staff Software Engineer"
	if _, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Minute), Key: p.Key, Status: posting.Dropped,
		Annotations: map[string]string{"triage.backend": TitleBackend}, Reason: "title"}); err != nil {
		t.Fatal(err)
	}
	policy := DropPolicy{Rules: titleRules, OpenRouter: "d1-ultra", ReadTitle: readsOn(map[string]TitleReading{
		"Staff": {Slot: "seniority", Value: "staff_plus"}})}
	keys, err := RetriageTitleDrops(ctx, l, t0.Add(time.Hour), policy, reviewScope(), false)
	if err != nil || len(keys) != 0 || rec(t, l, p.Key).Status != posting.Dropped {
		t.Fatalf("a 10-year staff title was returned: %v (%v)", keys, err)
	}
}
