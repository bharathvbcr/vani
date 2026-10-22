package jobview

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func rec(key string, st posting.Status) ledger.Record {
	k := posting.Key(key)
	return ledger.Record{Key: k, Status: st, Posting: posting.Posting{Key: k, Company: "Acme", Title: "Engineer"}}
}

// The pending view is exactly the unmarked live postings, and no ranked view
// lists a live one that is not marked.
func TestInViewPendingIsUnmarkedLiveOnly(t *testing.T) {
	all := []posting.Status{posting.Discovered, posting.Live, posting.Triaged, posting.Unsure, posting.Scored,
		posting.Explained, posting.Digested, posting.Dead, posting.Duplicate, posting.Dropped}
	for _, st := range all {
		for _, m := range []marks.Mark{marks.None, marks.Saved, marks.Applied, marks.Hidden} {
			r := rec("gh:a:1", st)
			want := st == posting.Live && m == marks.None
			if got := InView(ViewPending, r, m); got != want {
				t.Errorf("InView(pending, %s, %q) = %v, want %v", st, m, got, want)
			}
			if st == posting.Live && m == marks.None && (InView("new", r, m) || InView("all", r, m)) {
				t.Errorf("an untriaged posting is in a ranked view")
			}
			if InView(ViewPending, r, m) && (InView("saved", r, m) || InView("hidden", r, m) || InView("applied", r, m)) {
				t.Errorf("(%s, %q) is in pending and a mark view", st, m)
			}
		}
	}
}

// Pending.Total is the size of the pending view, so the footer count and the
// tab count cannot disagree; marked postings are in their mark view instead.
func TestPendingOfAgreesWithTheView(t *testing.T) {
	snap := ledger.Snapshot{Records: []ledger.Record{
		rec("gh:a:1", posting.Live), rec("gh:a:2", posting.Live), rec("gh:a:3", posting.Live),
		rec("gh:a:4", posting.Digested), rec("gh:a:5", posting.Dead), rec("gh:a:6", posting.Dropped),
	}}
	m := map[posting.Key]marks.Entry{"gh:a:2": {Mark: marks.Saved}, "gh:a:4": {Mark: marks.Hidden}}
	p := PendingOf(snap, m, "")
	counts := 0
	for _, r := range snap.Records {
		if InView(ViewPending, r, m[r.Key].Mark) {
			counts++
		}
	}
	if p.Total != counts || p.Total != 2 || p.Matching != 2 {
		t.Fatalf("pending %+v, view holds %d", p, counts)
	}
	if empty := PendingOf(ledger.Snapshot{}, nil, ""); empty.Total != 0 || empty.Companies == nil {
		t.Fatalf("empty ledger: %+v", empty)
	}
}

func TestEffectiveSortForPending(t *testing.T) {
	for _, c := range []struct{ view, by, want string }{
		{ViewPending, "default", "fresh"}, {ViewPending, "priority", "fresh"}, {ViewPending, "relevance", "fresh"},
		{ViewPending, "fresh", "fresh"}, {ViewPending, "company", "company"}, {ViewPending, "salary", "salary"},
		{"new", "default", "default"}, {"new", "priority", "priority"}, {"saved", "relevance", "relevance"},
	} {
		if got := EffectiveSort(c.view, c.by); got != c.want {
			t.Errorf("EffectiveSort(%s, %s) = %s, want %s", c.view, c.by, got, c.want)
		}
	}
}

// Dated postings lead, newest first; undated ones follow grouped by company
// (then key), whatever order was asked for.
func TestSortPendingNewestFirstThenCompany(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	mk := func(key, company string, fresh time.Time) Selected {
		return Selected{Job: Job{Key: posting.Key(key), Company: company, FreshAt: fresh, Untriaged: true}}
	}
	for _, by := range []string{"default", "priority", "relevance", "fresh"} {
		sel := []Selected{
			mk("gh:b:1", "beta", time.Time{}), mk("gh:a:2", "Acme", time.Time{}), mk("gh:c:1", "Cee", now.Add(-48*time.Hour)),
			mk("gh:a:1", "Acme", time.Time{}), mk("gh:d:1", "Dee", now.Add(-time.Hour)),
		}
		Sort(sel, ViewPending, by)
		var got []string
		for _, s := range sel {
			got = append(got, string(s.Job.Key))
		}
		if want := "gh:d:1 gh:c:1 gh:a:1 gh:a:2 gh:b:1"; strings.Join(got, " ") != want {
			t.Errorf("--sort %s: %q, want %q", by, strings.Join(got, " "), want)
		}
	}
}

// An untriaged job's wire shape carries no relevance or priority, where a
// triaged one writes them even when they are 0; every other field is written
// the same, and the untriaged job decodes back into a Job.
func TestJobMarshalOmitsScoresOnlyWhenUntriaged(t *testing.T) {
	triaged := Job{Key: "gh:a:1", Company: "A&B <x>", Title: "Eng", Status: posting.Digested}
	b, err := json.Marshal(triaged)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"relevance":0`) || !strings.Contains(string(b), `"priority":0`) {
		t.Fatalf("a triaged job must keep its zero scores: %s", b)
	}
	if strings.Contains(string(b), "untriaged") || strings.Contains(string(b), "prefilter_drop") {
		t.Fatalf("a triaged job carries untriaged fields: %s", b)
	}
	type plain Job
	ref, _ := json.Marshal(plain(triaged))
	var ra, rb map[string]any
	if json.Unmarshal(b, &ra) != nil || json.Unmarshal(ref, &rb) != nil || fmt.Sprint(ra) != fmt.Sprint(rb) {
		t.Fatalf("triaged encoding changed:\n%s\n%s", b, ref)
	}

	un := Job{Key: "gh:a:2", Company: "A&B", Title: "Eng", Status: posting.Live, Untriaged: true, PrefilterDrop: "title",
		Relevance: 12, Priority: 7, RelevanceKnown: 0.5}
	b, err = marshalNoEscape(un)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, banned := range []string{`"relevance`, `"priority"`, "score_factors", `"row"`} {
		if strings.Contains(s, banned) {
			t.Fatalf("untriaged job carries %s: %s", banned, s)
		}
	}
	if !strings.Contains(s, `"untriaged":true`) || !strings.Contains(s, `"prefilter_drop":"title"`) || !strings.Contains(s, `"company":"A&B"`) {
		t.Fatalf("untriaged job lost fields (or escaped HTML): %s", s)
	}
	var back Job
	if err := json.Unmarshal(b, &back); err != nil || back.Key != un.Key || !back.Untriaged || back.Relevance != 0 {
		t.Fatalf("round trip %+v: %v", back, err)
	}
}

// ToJob flags a posting by its status, so a saved or hidden live one stays
// unranked in its own view.
func TestToJobFlagsLiveByStatus(t *testing.T) {
	for _, st := range []posting.Status{posting.Live, posting.Triaged, posting.Digested, posting.Dead, posting.Dropped} {
		for _, m := range []marks.Mark{marks.None, marks.Saved} {
			if got := ToJob(rec("gh:a:1", st), marks.Entry{Mark: m}).Untriaged; got != (st == posting.Live) {
				t.Errorf("Untriaged(%s, %q) = %v", st, m, got)
			}
		}
	}
}

// 10,000 live records: Select bounds nothing by itself, BuildList's limit does.
func TestBuildListPendingBoundsTheReply(t *testing.T) {
	var snap ledger.Snapshot
	for i := 0; i < 10000; i++ {
		snap.Records = append(snap.Records, rec(fmt.Sprintf("gh:c%d:%d", i%50, i), posting.Live))
	}
	snap.Records = append(snap.Records, rec("gh:z:dead", posting.Dead), rec("gh:z:drop", posting.Dropped))
	o := &Opts{View: ViewPending, Sort: "default"}
	l := BuildList(snap, nil, o, noScore{}, InsightInfo{}, 8, 100)
	if l.Total != 10000 || l.Shown != 100 || len(l.Jobs) != 100 || l.Counts[ViewPending] != 10000 || l.Sort != "fresh" {
		t.Fatalf("total %d shown %d counts %v sort %s", l.Total, l.Shown, l.Counts, l.Sort)
	}
}

// marshalNoEscape encodes as `vani jobs --json` does (jobsReply).
func marshalNoEscape(v any) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	err := enc.Encode(v)
	return []byte(b.String()), err
}

type noScore struct{}

func (noScore) Enrich(*Job, ledger.Record, int) []jobinsight.Hit { return nil }
