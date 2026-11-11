package jobview

import (
	"fmt"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// countingRanker scores by key order and counts what each job was asked for.
type countingRanker struct {
	ranked, finished, enriched map[posting.Key]int
	payAsked                   int
}

func newCountingRanker() *countingRanker {
	return &countingRanker{ranked: map[posting.Key]int{}, finished: map[posting.Key]int{}, enriched: map[posting.Key]int{}}
}

func (c *countingRanker) Enrich(j *Job, r ledger.Record, kwLimit int) []jobinsight.Hit {
	c.enriched[r.Key]++
	return EnrichWith(c, j, r, kwLimit)
}

func (c *countingRanker) Rank(rk *Ranked, r ledger.Record, withPay bool) {
	c.ranked[r.Key]++
	if withPay {
		c.payAsked++
		rk.PayRead = true
	}
	if Untriaged(r) {
		return // as insight.Rank: no score without a triage reading
	}
	var n int
	fmt.Sscanf(string(r.Key)[len("gh:acme:"):], "%d", &n)
	rk.Priority, rk.Relevance = float64(n), float64(n)
}

func (c *countingRanker) Hits(ledger.Record) []jobinsight.Hit { return []jobinsight.Hit{{Term: "go"}} }

func (c *countingRanker) Finish(j *Job, r ledger.Record, _ int, _ bool) []jobinsight.Hit {
	c.finished[r.Key]++
	j.Logo = "logo"
	return c.Hits(r)
}

func pageSnap(n int) ledger.Snapshot {
	var snap ledger.Snapshot
	for i := range n {
		snap.Records = append(snap.Records, rec(fmt.Sprintf("gh:acme:%03d", i), posting.Digested))
	}
	return snap
}

// A list ranks every job in its view but finishes for display only the page
// it returns: on a large ledger the display work (keywords, semantic match,
// manual reading, pay, row) is the most of a list's cost, and all but a page
// of it was thrown away.
func TestBuildListFinishesOnlyThePage(t *testing.T) {
	snap := pageSnap(50)
	c := newCountingRanker()
	o := Opts{View: "all", Sort: "priority"}
	l := BuildList(snap, nil, &o, c, InsightInfo{}, 0, 5)
	if l.Total != 50 || l.Shown != 5 {
		t.Fatalf("total %d shown %d, want 50 and 5", l.Total, l.Shown)
	}
	if len(c.enriched) != 0 {
		t.Errorf("Enrich called %d times; a PageRanker list should rank and finish", len(c.enriched))
	}
	if len(c.ranked) != 50 {
		t.Errorf("ranked %d jobs, want every one of 50", len(c.ranked))
	}
	if len(c.finished) != 5 {
		t.Errorf("finished %d jobs, want the page of 5", len(c.finished))
	}
	for _, j := range l.Jobs {
		if c.finished[j.Key] != 1 || j.Logo != "logo" {
			t.Errorf("%s shown but finished %d times (logo %q)", j.Key, c.finished[j.Key], j.Logo)
		}
	}
	// Highest priority first: the page is the top of the order, not the first records read.
	if l.Jobs[0].Key != "gh:acme:049" {
		t.Errorf("first row %s, want gh:acme:049", l.Jobs[0].Key)
	}
	if c.payAsked != 0 {
		t.Errorf("pay asked for %d jobs; nothing in a priority order reads it", c.payAsked)
	}
}

// Pay is ranked for every job when the order or a filter reads it.
func TestBuildListRanksPayWhenRead(t *testing.T) {
	for _, o := range []Opts{
		{View: "all", Sort: "salary"},
		{View: "all", Sort: "priority", More: Filter{PayListed: true}},
		{View: "all", Sort: "priority", More: Filter{MinPay: 100_000, Currency: "USD"}},
	} {
		c := newCountingRanker()
		BuildList(pageSnap(20), nil, &o, c, InsightInfo{}, 0, 5)
		if c.payAsked != 20 {
			t.Errorf("%+v: pay ranked for %d of 20 jobs", o, c.payAsked)
		}
	}
}

// Select's callers (gaps, audit) read every selected job whole, with its hits
// and pay: each is ranked with pay and finished exactly once, as Enrich would.
func TestSelectReturnsEveryJobWhole(t *testing.T) {
	c := newCountingRanker()
	o := Opts{View: "all"}
	sel, _ := Select(pageSnap(10), nil, &o, c, 0)
	if len(sel) != 10 || len(c.ranked) != 10 || len(c.finished) != 10 || c.payAsked != 10 {
		t.Fatalf("selected %d, ranked %d, finished %d, paid %d; want 10 each", len(sel), len(c.ranked), len(c.finished), c.payAsked)
	}
	for _, s := range sel {
		if len(s.Hits) == 0 || s.Job.Logo != "logo" || c.finished[s.Job.Key] != 1 {
			t.Errorf("%s: hits %d logo %q finished %d", s.Job.Key, len(s.Hits), s.Job.Logo, c.finished[s.Job.Key])
		}
	}
}

// Stats read every selected job's hits without finishing any job.
func TestStatsForFinishesNothing(t *testing.T) {
	c := newCountingRanker()
	o := Opts{View: "all"}
	st := StatsFor(pageSnap(10), nil, &o, c, InsightInfo{}, nil, "USD")
	if st.Total != 10 || len(c.finished) != 0 || len(c.enriched) != 0 {
		t.Fatalf("total %d, finished %d, enriched %d; want 10, 0, 0", st.Total, len(c.finished), len(c.enriched))
	}
	if len(st.Keywords) != 1 || st.Keywords[0].N != 10 {
		t.Errorf("keywords %+v, want go counted for all 10", st.Keywords)
	}
}

// An empty view finishes nothing and still answers.
func TestBuildListEmptyFinishesNothing(t *testing.T) {
	c := newCountingRanker()
	o := Opts{View: "saved"}
	l := BuildList(pageSnap(10), nil, &o, c, InsightInfo{}, 0, 5)
	if l.Total != 0 || len(l.Jobs) != 0 || len(c.finished) != 0 {
		t.Errorf("total %d jobs %d finished %d", l.Total, len(l.Jobs), len(c.finished))
	}
}
