package main

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

var (
	crawl1 = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC) // every board's first crawl
	crawl2 = time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
)

func rec(key string, firstSeen, posted time.Time, title string) ledger.Record {
	return ledger.Record{Key: posting.Key(key), Status: posting.Live, FirstSeen: firstSeen,
		Posting: posting.Posting{Key: posting.Key(key), Title: title, PostedAt: posted}}
}

func days(n int) time.Time { return crawl1.AddDate(0, 0, -n) }

func keysOf(recs []ledger.Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = string(r.Key)
	}
	return out
}

func ordered(recs []ledger.Record, rank func(posting.Posting) int) []string {
	todo := slices.Clone(recs)
	orderAdvance(todo, recs, rank)
	return keysOf(todo)
}

func TestOrderAdvanceFreshestFirst(t *testing.T) {
	t.Run("freshness beats ledger order", func(t *testing.T) {
		recs := []ledger.Record{
			rec("ab:a:1", crawl1, days(30), ""),
			rec("ab:a:2", crawl1, days(1), ""),
			rec("ab:b:1", crawl1, days(7), ""),
		}
		if got, want := ordered(recs, nil), []string{"ab:a:2", "ab:b:1", "ab:a:1"}; !slices.Equal(got, want) {
			t.Fatalf("order %v, want %v", got, want)
		}
	})

	t.Run("title rank beats freshness", func(t *testing.T) {
		recs := []ledger.Record{
			rec("ab:a:1", crawl1, days(30), "LLM Inference Engineer"),
			rec("ab:a:2", crawl1, days(1), "Sales Engineer"),
			rec("ab:a:3", crawl1, days(2), "Software Engineer"),
		}
		w := &wired{cfg: config.Config{TitleSignals: config.TitleSignals{Include: []string{"Inference"}, Exclude: []string{"Sales"}}}}
		if got, want := ordered(recs, w.titleRank()), []string{"ab:a:1", "ab:a:3", "ab:a:2"}; !slices.Equal(got, want) {
			t.Fatalf("order %v, want %v", got, want)
		}
	})

	t.Run("no date in the first crawl sorts after the dated, not first", func(t *testing.T) {
		recs := []ledger.Record{
			rec("gh:a:1", crawl1, time.Time{}, ""), // first-seen is the crawl itself: no age known
			rec("ab:b:1", crawl1, days(60), ""),
			rec("gh:a:2", crawl1, time.Time{}, ""),
			rec("ab:b:2", crawl1, days(3), ""),
		}
		if got, want := ordered(recs, nil), []string{"ab:b:2", "ab:b:1", "gh:a:1", "gh:a:2"}; !slices.Equal(got, want) {
			t.Fatalf("order %v, want %v", got, want)
		}
	})

	t.Run("no date but listed after the first crawl: first-seen stands in", func(t *testing.T) {
		recs := []ledger.Record{
			rec("gh:a:1", crawl1, time.Time{}, ""),
			rec("ab:b:1", crawl1, days(2), ""),
			rec("gh:a:9", crawl2, time.Time{}, ""), // new on the second crawl
		}
		if got, want := ordered(recs, nil), []string{"gh:a:9", "ab:b:1", "gh:a:1"}; !slices.Equal(got, want) {
			t.Fatalf("order %v, want %v", got, want)
		}
	})

	t.Run("a board's first crawl is its own", func(t *testing.T) {
		// Board b was added on the second crawl: its undated postings are its
		// first crawl, of unknown age, not new.
		recs := []ledger.Record{
			rec("gh:a:1", crawl1, time.Time{}, ""),
			rec("gh:a:2", crawl2, time.Time{}, ""),
			rec("gh:b:1", crawl2, time.Time{}, ""),
			rec("ab:c:1", crawl1, days(1), ""),
		}
		if got, want := ordered(recs, nil), []string{"gh:a:2", "ab:c:1", "gh:a:1", "gh:b:1"}; !slices.Equal(got, want) {
			t.Fatalf("order %v, want %v", got, want)
		}
	})

	t.Run("an ineligible record still dates its board's first crawl", func(t *testing.T) {
		dead := rec("gh:a:0", crawl1, time.Time{}, "")
		dead.Status = posting.Dead
		live := rec("gh:a:5", crawl2, time.Time{}, "")
		older := rec("ab:c:1", crawl1, days(1), "")
		todo := []ledger.Record{older, live}
		orderAdvance(todo, []ledger.Record{dead, older, live}, nil)
		if got := keysOf(todo); !slices.Equal(got, []string{"gh:a:5", "ab:c:1"}) {
			t.Fatalf("order %v: the dead record's crawl was ignored", got)
		}
	})

	t.Run("a future posted date counts as first-seen", func(t *testing.T) {
		recs := []ledger.Record{
			rec("ab:a:1", crawl1, crawl2.AddDate(1, 0, 0), ""), // a year ahead: clamped to crawl1
			rec("ab:a:9", crawl2, days(0).Add(12*time.Hour), ""),
		}
		if got, want := ordered(recs, nil), []string{"ab:a:9", "ab:a:1"}; !slices.Equal(got, want) {
			t.Fatalf("order %v, want %v", got, want)
		}
	})

	t.Run("ties keep ledger order", func(t *testing.T) {
		recs := []ledger.Record{
			rec("ab:a:3", crawl1, days(5), ""),
			rec("ab:a:1", crawl1, days(5), ""),
			rec("gh:z:2", crawl1, time.Time{}, ""),
			rec("gh:z:1", crawl1, time.Time{}, ""),
		}
		if got := ordered(recs, nil); !slices.Equal(got, keysOf(recs)) {
			t.Fatalf("order %v, want ledger order %v", got, keysOf(recs))
		}
	})
}

// The cap spends itself on the freshest postings; the older ones wait.
func TestAdvanceCapSpendsOnTheFreshest(t *testing.T) {
	var recs []ledger.Record
	for i, age := range []int{40, 2, 90, 0, 15, 1} {
		recs = append(recs, rec("ab:a:"+strings.Repeat("x", i+1), crawl1, days(age), ""))
	}
	var mu sync.Mutex
	var got []string
	rep, err := advanceAll(context.Background(), recs, nil, 3, 1, nil, func(_ context.Context, k posting.Key) error {
		mu.Lock()
		got = append(got, string(k))
		mu.Unlock()
		return nil
	}, func(string, ...any) {})
	if err != nil || rep.Attempted != 3 || rep.Deferred != 3 {
		t.Fatalf("err %v report %+v", err, rep)
	}
	if want := []string{"ab:a:xxxx", "ab:a:xxxxxx", "ab:a:xx"}; !slices.Equal(got, want) {
		t.Fatalf("advanced %v, want the three freshest %v", got, want)
	}
}
