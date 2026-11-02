package pipeline

import (
	"context"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Progress is counts on the dispatch goroutine: once before any posting, then
// once per posting started, with the same limit and eligible. The func takes
// no posting key.
func TestAdvanceProgressCountsWithoutAKey(t *testing.T) {
	recs := []ledger.Record{
		{Key: "gh:a:1", Status: posting.Live, Posting: posting.Posting{Key: "gh:a:1", Title: "Engineer"}},
		{Key: "gh:a:2", Status: posting.Live, Posting: posting.Posting{Key: "gh:a:2", Title: "Engineer"}},
	}
	var got [][3]int
	plan := AdvancePlan{
		Limit: 2, Workers: 1,
		Progress: func(attempted, limit, eligible int) {
			got = append(got, [3]int{attempted, limit, eligible})
		},
	}
	deps := AdvanceDeps{
		Records: recs,
		Advance: func(context.Context, posting.Key) error { return nil },
	}
	rep, err := Advance(context.Background(), plan, deps)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Attempted != 2 || rep.Eligible != 2 {
		t.Fatalf("report %+v", rep)
	}
	want := [][3]int{{0, 2, 2}, {1, 2, 2}, {2, 2, 2}}
	if len(got) != len(want) {
		t.Fatalf("calls %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call %d: got %v want %v", i, got[i], want[i])
		}
	}
}

func TestAdvanceProgressIsNotCalledWhenThePlanIsRefused(t *testing.T) {
	called := false
	_, err := Advance(context.Background(), AdvancePlan{
		Limit: 0, Workers: 1,
		Progress: func(int, int, int) { called = true },
	}, AdvanceDeps{Advance: func(context.Context, posting.Key) error { return nil }})
	if err == nil || called {
		t.Fatalf("err %v called %v", err, called)
	}
}
