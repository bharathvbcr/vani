package jobview

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// plainRanker hides countingRanker's Rank, Hits and Finish, so a selection
// takes the whole-Job path a plain Ranker takes.
type plainRanker struct{ c *countingRanker }

func (p plainRanker) Enrich(j *Job, r ledger.Record, kwLimit int) []jobinsight.Hit {
	return p.c.Enrich(j, r, kwLimit)
}

// Text a selection cleans and matches: mixed case, controls, newlines, runs of
// space, non-ASCII, empty, and longer than FieldRunes.
var candTexts = []string{"", "Acme", "ACME corp", "acme\u0000corp", "Ünïcode GmbH", "  spaced   out  ", "line\nbreak",
	"tab\there", strings.Repeat("long ", 80) + "tail", "Austin, TX", "Remote - US", "Berlin, Germany", "Platform​Team",
	"bad \xff\xfe utf8 Acme", "\xc3 cut"}

var candSlotValues = []string{"", "strong_fit", "weak_fit", "remote", "hybrid", "senior", "yes", "no", " padded ", "x\u0007y"}

func randomRecords(rng *rand.Rand, n int) ([]ledger.Record, map[posting.Key]marks.Entry) {
	statuses := []posting.Status{posting.Live, posting.Triaged, posting.Unsure, posting.Scored, posting.Digested, posting.Dead, posting.Dropped}
	pick := func(s []string) string { return s[rng.IntN(len(s))] }
	recs := make([]ledger.Record, 0, n)
	m := map[posting.Key]marks.Entry{}
	for i := range n {
		k := posting.Key(fmt.Sprintf("gh:acme:%03d", i))
		ann := map[string]string{}
		for _, slot := range digest.TriageSlots {
			if v := pick(candSlotValues); v != "" {
				ann[ledger.AnnTriagePrefix+slot] = v
			}
		}
		switch rng.IntN(3) {
		case 0:
			ann[ledger.AnnCategory] = pick([]string{"ml", "infra", " ", "data\nsci"})
		case 1:
			ann[ledger.AnnTriagePrefix+"category"] = pick([]string{"ml", "sales", ""})
		}
		recs = append(recs, ledger.Record{Key: k, Status: statuses[rng.IntN(len(statuses))], Annotations: ann,
			FirstSeen: time.Date(2026, 10, 1+rng.IntN(30), 0, 0, 0, 0, time.UTC),
			Posting: posting.Posting{Key: k, Company: pick(candTexts), Title: pick(candTexts),
				Location: pick(candTexts), Department: pick(candTexts), Remote: rng.IntN(2) == 0}})
		switch rng.IntN(5) {
		case 0:
			m[k] = marks.Entry{Mark: marks.Saved, Note: pick(candTexts), MarkedAt: time.Unix(int64(rng.IntN(1000)), 0)}
		case 1:
			m[k] = marks.Entry{Mark: marks.Applied, Stage: marks.StageNone, MarkedAt: time.Unix(int64(rng.IntN(1000)), 0)}
		case 2:
			m[k] = marks.Entry{Mark: marks.Hidden}
		}
	}
	return recs, m
}

// A selection over compact candidates (a PageRanker) answers what one over
// whole Jobs answers (a plain Ranker), for every view, order, query and
// filter that reads a static field, over adversarial text.
func TestCandSelectionMatchesWholeJobs(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	queries := []string{"", "acme", "ACME", "corp", "austin", "remote", "team", "long long", "\n", "ü", "zzz", "tail", "spaced   out"}
	for round := range 40 {
		recs, m := randomRecords(rng, 60)
		snap := ledger.Snapshot{Records: recs}
		for _, view := range Views {
			for _, by := range Sorts {
				o := Opts{View: view, Sort: by, Query: queries[rng.IntN(len(queries))]}
				switch rng.IntN(5) {
				case 0:
					o.Fits = map[string]bool{"strong_fit": true, "none": true}
				case 1:
					o.More.Companies = map[string]bool{CompanyKey("ACME corp"): true}
				case 2:
					o.More.ExcludeTerms = []string{"long"}
				case 3:
					o.Open = true
					o.More.Seniorities = map[string]bool{"senior": true}
				}
				a, b := o, o
				got := BuildList(snap, m, &a, newCountingRanker(), InsightInfo{}, 3, 7)
				want := BuildList(snap, m, &b, plainRanker{newCountingRanker()}, InsightInfo{}, 3, 7)
				gj, _ := json.Marshal(got)
				wj, _ := json.Marshal(want)
				if string(gj) != string(wj) {
					t.Fatalf("round %d %s/%s %+v: candidate list differs from whole jobs\ngot:  %.500s\nwant: %.500s", round, view, by, o, gj, wj)
				}
				c, d := o, o
				gs := StatsFor(snap, m, &c, newCountingRanker(), InsightInfo{}, nil, "USD")
				sel, _ := Select(snap, m, &d, plainRanker{newCountingRanker()}, 0)
				ws := StatsOf(sel, InsightInfo{}, nil)
				ws.Salary = PayStatsOf(sel, "USD")
				gj, _ = json.Marshal(gs)
				wj, _ = json.Marshal(ws)
				if string(gj) != string(wj) {
					t.Fatalf("round %d %s/%s %+v: candidate stats differ\ngot:  %.500s\nwant: %.500s", round, view, by, o, gj, wj)
				}
			}
		}
	}
}

// The query keeps exactly the jobs MatchesQuery keeps over their whole Job:
// an oracle the selection does not share (both arms above run the same
// query line).
func TestCandQueryMatchesWholeJobOracle(t *testing.T) {
	rng := rand.New(rand.NewPCG(13, 17))
	queries := []string{"acme", "ACME", "corp", "austin", "remote", "team", "long long", "ü", "tail", "spaced   out", "gmbh", "berlin"}
	for round := range 40 {
		recs, m := randomRecords(rng, 80)
		snap := ledger.Snapshot{Records: recs}
		for _, view := range []string{"all", "saved", ViewPending} {
			for _, q := range queries {
				all := Opts{View: view}
				every, _ := Select(snap, m, &all, newCountingRanker(), 0)
				want := map[posting.Key]bool{}
				for _, s := range every {
					if MatchesQuery(s.Job, strings.ToLower(q)) {
						want[s.Job.Key] = true
					}
				}
				o := Opts{View: view, Query: q}
				got, _ := Select(snap, m, &o, newCountingRanker(), 0)
				if len(got) != len(want) {
					t.Fatalf("round %d %s %q: %d selected, oracle %d", round, view, q, len(got), len(want))
				}
				for _, s := range got {
					if !want[s.Job.Key] {
						t.Fatalf("round %d %s %q: %s selected, oracle drops it", round, view, q, s.Job.Key)
					}
				}
			}
		}
	}
}

// SelectBriefs is Select's jobs as gaps reads them, without finishing any.
func TestSelectBriefsMatchSelect(t *testing.T) {
	rng := rand.New(rand.NewPCG(29, 31))
	recs, m := randomRecords(rng, 200)
	snap := ledger.Snapshot{Records: recs}
	for _, view := range Views {
		a, b := Opts{View: view, Query: "acme"}, Opts{View: view, Query: "acme"}
		c := newCountingRanker()
		briefs := SelectBriefs(snap, m, &a, c)
		if len(c.finished) != 0 {
			t.Fatalf("%s: SelectBriefs finished %d jobs", view, len(c.finished))
		}
		sel, _ := Select(snap, m, &b, plainRanker{newCountingRanker()}, 0)
		if len(briefs) != len(sel) {
			t.Fatalf("%s: %d briefs, %d selected", view, len(briefs), len(sel))
		}
		for i, s := range sel {
			got, _ := json.Marshal(briefs[i])
			want, _ := json.Marshal(Brief{Key: s.Job.Key, Company: s.Job.Company, Title: s.Job.Title,
				Priority: s.Job.Priority, Hits: s.Hits, Record: briefs[i].Record})
			if string(got) != string(want) || recs[briefs[i].Record].Key != s.Job.Key {
				t.Fatalf("%s #%d: brief %s, selected %s", view, i, got, want)
			}
		}
	}
}

// staticOf reads every triage slot and the category the way a Job's Triage
// and Category carry them, so a slot added to digest.TriageSlots cannot be
// filtered on a candidate and shown differently on its row.
func TestStaticOfRoundTripsThroughAJob(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 5))
	recs, m := randomRecords(rng, 300)
	for _, r := range recs {
		j := ToJob(r, m[r.Key])
		if got, want := candOfJob(&j, nil, 0).st, staticOf(r); got != want {
			t.Fatalf("%s: from the Job %+v, from the record %+v", r.Key, got, want)
		}
	}
}

// Hits is what Finish returns, for a ranker that implements both.
func TestPageRankerHitsAreFinishHits(t *testing.T) {
	c := newCountingRanker()
	for _, r := range pageSnap(5).Records {
		j := ToJob(r, marks.Entry{})
		got, _ := json.Marshal(c.Hits(r))
		want, _ := json.Marshal(EnrichWith(c, &j, r, 0))
		if string(got) != string(want) {
			t.Errorf("%s: Hits %s, Finish %s", r.Key, got, want)
		}
	}
}
