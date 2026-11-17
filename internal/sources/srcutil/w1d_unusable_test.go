package srcutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// A stored row whose text is unusable is always read again, never reused,
// deferred or dropped, whatever its refresh turn, the limit or the new rows.
func TestW1dPlanDetailsUnusable(t *testing.T) {
	const day = 100
	var off, on string // ids not on / on their refresh turn on day
	for i := 0; off == "" || on == ""; i++ {
		id := string(rune('a'+i%26)) + string(rune('a'+i/26%26))
		if RefreshTurn(id, day) && on == "" {
			on = id
		} else if !RefreshTurn(id, day) && off == "" {
			off = id
		}
	}
	tests := []struct {
		name  string
		rows  []DetailRow
		limit int
		fetch []int
		reuse []int
		newN  int
		unus  int
		defrd int
	}{
		{"alone, off turn", []DetailRow{{ID: off, Stored: true, Unusable: true}}, 5, []int{0}, nil, 0, 1, 0},
		{"alone, on turn", []DetailRow{{ID: on, Stored: true, Unusable: true}}, 5, []int{0}, nil, 0, 1, 0},
		{"zero limit still reads it", []DetailRow{{ID: off, Stored: true, Unusable: true}, {ID: "n"}}, 0, []int{0}, nil, 0, 1, 1},
		{"takes room from new rows", []DetailRow{{ID: "n1"}, {ID: "n2"}, {ID: off, Stored: true, Unusable: true}}, 2, []int{0, 2}, nil, 1, 1, 1},
		{"more unusable than the limit", []DetailRow{{ID: "n"}, {ID: "u1", Stored: true, Unusable: true}, {ID: "u2", Stored: true, Unusable: true}, {ID: off, Stored: true}}, 1, []int{1, 2}, []int{3}, 0, 2, 1},
		{"dropped listing does not drop it", []DetailRow{{ID: off, Stored: true, Unusable: true, Drop: true}}, 5, []int{0}, nil, 0, 1, 0},
		{"changed rows get what is left", []DetailRow{{ID: "u", Stored: true, Unusable: true}, {ID: "c", Stored: true, Changed: true}}, 1, []int{0}, []int{1}, 0, 1, 0},
		{"unusable but not stored is a new row", []DetailRow{{ID: "n", Unusable: true}}, 5, []int{0}, nil, 1, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pl := PlanDetails(tc.rows, day, tc.limit)
			if !slices.Equal(pl.Fetch, tc.fetch) || !slices.Equal(pl.Reuse, tc.reuse) || pl.New != tc.newN || pl.Unusable != tc.unus || pl.Deferred != tc.defrd {
				t.Fatalf("plan %+v, want fetch %v reuse %v new %d unusable %d deferred %d", pl, tc.fetch, tc.reuse, tc.newN, tc.unus, tc.defrd)
			}
			if len(pl.Fetch)+len(pl.Reuse)+pl.Deferred+pl.Prefiltered != len(tc.rows) {
				t.Fatalf("plan %+v does not place every row exactly once", pl)
			}
		})
	}
}

// A transient miss on an unusable stored row fails the board even where the same
// miss on a new row, or a stored one, is absorbed; the refusal is the miss
// itself, not "too many misses".
func TestW1dDetailMissesNeverAbsorbAnUnusableRow(t *testing.T) {
	rows := []DetailRow{{ID: "n"}, {ID: "u", Stored: true, Unusable: true}}
	for i := range 10 {
		rows = append(rows, DetailRow{ID: string(rune('a' + i)), Stored: true})
	}
	pl := PlanDetails(rows, 100, 5)
	if pl.New != 1 || pl.Unusable != 1 {
		t.Fatalf("plan %+v", pl)
	}
	miss := fmt.Errorf("detail: %w", io.ErrUnexpectedEOF)
	m := NewDetailMisses(pl, true)
	if !m.Take(context.Background(), 0, miss) {
		t.Fatal("a new row's miss was not absorbed")
	}
	if m.Take(context.Background(), 1, miss) {
		t.Fatal("an unusable stored row's miss was absorbed")
	}
	if err := m.Refusal(context.Background(), 1, miss); !errors.Is(err, miss) || errors.Is(err, ErrTooManyMisses) {
		t.Fatalf("refusal = %v, want the miss itself", err)
	}
}

// Marked reads only the explicit mark: a nil mark marks nothing, and the key is
// all it is asked, so a posting with genuinely empty text is not unusable unless
// the mark says so.
func TestW1dMarkedIsAnExplicitMarkOnly(t *testing.T) {
	k, other := posting.Key("gh:a:1"), posting.Key("gh:a:2")
	if Marked(nil, k) {
		t.Fatal("a nil mark marked a key")
	}
	mark := func(x posting.Key) bool { return x == k }
	if !Marked(mark, k) || Marked(mark, other) {
		t.Fatal("Marked does not follow the mark")
	}
}

// A stored row with no Unusable mark is reused on a turn it is not due, whatever
// its text is: the planner never sees the text, so a posting whose description
// is genuinely empty is not re-read for it.
func TestW1dPlanDetailsUnmarkedStoredRowIsReused(t *testing.T) {
	const day = 100
	id := ""
	for i := 0; id == ""; i++ {
		if c := fmt.Sprintf("s%d", i); !RefreshTurn(c, day) {
			id = c
		}
	}
	pl := PlanDetails([]DetailRow{{ID: id, Stored: true}}, day, 5)
	if len(pl.Fetch) != 0 || !slices.Equal(pl.Reuse, []int{0}) || pl.Unusable != 0 {
		t.Fatalf("plan %+v, want the stored row reused", pl)
	}
}
