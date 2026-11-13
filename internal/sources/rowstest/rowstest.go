// Package rowstest is the shared contract test for an adapter's bad-row
// handling (srcutil.Rows): one row that cannot become a posting is skipped and
// counted when the caller can hold a short listing, fails the board when it
// cannot, and fails it as before once more than a tenth of the rows are bad.
package rowstest

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// Index in a bad row is replaced by the row's position, for an adapter that
// drops a repeated id from one listing: two bad rows must not look alike.
const Index = "%IDX%"

// Case describes one adapter.
type Case struct {
	// List runs the adapter's List over a payload, with ctx.
	List func(ctx context.Context, payload string) ([]posting.Posting, error)
	// Payload is a listing of n rows in which the row at each index of bad is
	// replaced by the given raw row. Good rows must carry distinct ids.
	Payload func(n int, bad map[int]string) string
	// BadRows are rows the adapter refuses (each one alone is one bad row).
	BadRows map[string]string
}

type log struct {
	calls, skipped int
	first          string
}

func (l *log) ctx() context.Context {
	return srcutil.WithBadRowSink(context.Background(), func(_ sources.Board, n int, first string) {
		l.calls++
		l.skipped += n
		l.first = first
	})
}

// Check runs the contract.
func Check(t *testing.T, c Case) {
	t.Helper()
	names := make([]string, 0, len(c.BadRows))
	for name := range c.BadRows {
		names = append(names, name)
	}
	sort.Strings(names)
	at := func(name string, i int) string {
		return strings.ReplaceAll(c.BadRows[name], Index, strconv.Itoa(i))
	}
	for _, name := range names {
		raw := at(name, 3)
		t.Run("skips "+name, func(t *testing.T) {
			var l log
			got, err := c.List(l.ctx(), c.Payload(12, map[int]string{3: raw}))
			if err != nil {
				t.Fatalf("one bad row failed the board: %v", err)
			}
			if len(got) != 11 || l.calls != 1 || l.skipped != 1 || l.first == "" {
				t.Fatalf("got %d postings, sink %+v; want the 11 good rows and one skipped row reported once", len(got), l)
			}
		})
		t.Run("fails without a sink "+name, func(t *testing.T) {
			if got, err := c.List(context.Background(), c.Payload(12, map[int]string{3: raw})); err == nil {
				t.Fatalf("a caller that cannot hold a short listing got %d postings", len(got))
			}
		})
	}
	t.Run("bound", func(t *testing.T) {
		for _, bc := range []struct {
			name string
			n    int
			bad  []int
			ok   bool
		}{
			{"one of one", 1, []int{0}, true},
			{"two of twelve", 12, []int{3, 4}, false},
			{"two of twenty", 20, []int{0, 19}, true},
			{"three of twenty", 20, []int{0, 1, 19}, false},
		} {
			bad := map[int]string{}
			for _, i := range bc.bad {
				bad[i] = at(names[0], i)
			}
			var l log
			got, err := c.List(l.ctx(), c.Payload(bc.n, bad))
			if bc.ok {
				if err != nil || len(got) != bc.n-len(bc.bad) || l.skipped != len(bc.bad) {
					t.Errorf("%s: got %d postings, %v, sink %+v", bc.name, len(got), err, l)
				}
				continue
			}
			if err == nil || l.calls != 0 {
				t.Errorf("%s: want the board to fail with no report, got %d postings, %v, sink %+v", bc.name, len(got), err, l)
			}
		}
	})
	t.Run("clean listing reports nothing", func(t *testing.T) {
		var l log
		if got, err := c.List(l.ctx(), c.Payload(12, nil)); err != nil || len(got) != 12 || l.calls != 0 {
			t.Fatalf("got %d postings, %v, sink %+v", len(got), err, l)
		}
	})
}
