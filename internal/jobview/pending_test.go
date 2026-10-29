package jobview

import (
	"encoding/json"
	"fmt"
	"reflect"
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

func recAt(key, company, loc string, st posting.Status) ledger.Record {
	k := posting.Key(key)
	return ledger.Record{Key: k, Status: st, Posting: posting.Posting{Key: k, Company: company, Title: "Engineer", Location: loc}}
}

// Pending.US counts the pending postings whose board location is in the US,
// read from the formats boards write; Companies carry their own US share.
func TestPendingOfCountsUS(t *testing.T) {
	snap := ledger.Snapshot{Records: []ledger.Record{
		recAt("gh:acme:1", "Acme", "San Francisco", posting.Live),
		recAt("gh:acme:2", "Acme", "US, CA, Santa Clara", posting.Live),
		recAt("gh:acme:3", "Acme", "Toronto, ON", posting.Live),
		recAt("gh:acme:4", "Acme", "", posting.Live),
		recAt("gh:beta:1", "Beta", "Remote - US", posting.Live),
		recAt("gh:beta:2", "Beta", "Remote", posting.Live),
		recAt("gh:beta:3", "Beta", "London, United Kingdom", posting.Live),
		// Not pending: triaged, marked, dead or dropped postings are not counted
		// whatever their location.
		recAt("gh:acme:5", "Acme", "Austin, TX", posting.Digested),
		recAt("gh:acme:6", "Acme", "Austin, TX", posting.Live),
		recAt("gh:acme:7", "Acme", "Austin, TX", posting.Dead),
		recAt("gh:acme:8", "Acme", "Austin, TX", posting.Dropped),
	}}
	m := map[posting.Key]marks.Entry{"gh:acme:6": {Mark: marks.Saved}}
	p := PendingOf(snap, m, "")
	want := Pending{Total: 7, Matching: 7, US: 3, Companies: []PendingCompany{{Company: "Acme", N: 4, US: 2}, {Company: "Beta", N: 3, US: 1}}}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("pending %+v, want %+v", p, want)
	}
	for _, c := range p.Companies {
		if c.US > c.N {
			t.Errorf("%s: %d in the US of %d", c.Company, c.US, c.N)
		}
	}
}

// A query narrows Matching and leaves Total, US and the company counts alone:
// US sizes the whole backlog a run would triage.
func TestPendingOfUSIgnoresTheQuery(t *testing.T) {
	snap := ledger.Snapshot{Records: []ledger.Record{
		recAt("gh:acme:1", "Acme", "Seattle, WA", posting.Live),
		recAt("gh:beta:1", "Beta", "New York, NY", posting.Live),
		recAt("gh:beta:2", "Beta", "Paris, France", posting.Live),
	}}
	all, q := PendingOf(snap, nil, ""), PendingOf(snap, nil, "beta")
	if q.Matching != 2 || all.Matching != 3 {
		t.Fatalf("matching: %d with the query, %d without", q.Matching, all.Matching)
	}
	q.Matching, all.Matching = 0, 0
	if !reflect.DeepEqual(q, all) || q.US != 2 || q.Total != 3 {
		t.Fatalf("query changed the counts: %+v vs %+v", q, all)
	}
	if none := PendingOf(snap, nil, "nosuchcompany"); none.Matching != 0 || none.US != 2 {
		t.Fatalf("a query that names nothing: %+v", none)
	}
}

// Nothing in the US, or nothing pending: 0, rendered as 0 and not left out.
func TestPendingOfUSZero(t *testing.T) {
	for name, snap := range map[string]ledger.Snapshot{
		"empty":              {},
		"no records pending": {Records: []ledger.Record{recAt("gh:a:1", "Acme", "Austin, TX", posting.Digested)}},
		"no US":              {Records: []ledger.Record{recAt("gh:a:1", "Acme", "Berlin, Germany", posting.Live), recAt("gh:a:2", "Acme", "Toronto, ON", posting.Live), recAt("gh:a:3", "Acme", "", posting.Live)}},
	} {
		p := PendingOf(snap, nil, "")
		if p.US != 0 {
			t.Errorf("%s: US %d", name, p.US)
		}
		for _, c := range p.Companies {
			if c.US != 0 {
				t.Errorf("%s: %s US %d", name, c.Company, c.US)
			}
		}
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"us":0`) {
			t.Errorf("%s: zero US left out of %s", name, b)
		}
	}
}

// The wire shape: the old keys in their old order, then us, on the summary and
// on every company entry.
func TestPendingJSONShapeCarriesUS(t *testing.T) {
	snap := ledger.Snapshot{Records: []ledger.Record{
		recAt("gh:acme:1", "Acme", "Austin, TX", posting.Live), recAt("gh:acme:2", "Acme", "Pune, India", posting.Live),
	}}
	b, err := json.Marshal(PendingOf(snap, nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"total":2,"matching":2,"companies":[{"company":"Acme","n":2,"us":1}],"us":1}`; string(b) != want {
		t.Fatalf("shape %s, want %s", b, want)
	}
}

// The company list stays bounded at MaxPendingCompanies, and US still counts
// the postings of the companies it cut: the list is a sample, US is the size.
func TestPendingOfUSCountsPastTheCompanyBound(t *testing.T) {
	var recs []ledger.Record
	us := 0
	for i := 0; i < MaxPendingCompanies+5; i++ {
		for j := 0; j <= i; j++ {
			recs = append(recs, recAt(fmt.Sprintf("gh:c%02d:%d", i, j), fmt.Sprintf("C%02d", i), "Boston, MA", posting.Live))
			us++
		}
	}
	p := PendingOf(ledger.Snapshot{Records: recs}, nil, "")
	if len(p.Companies) != MaxPendingCompanies || p.Total != len(recs) || p.US != us {
		t.Fatalf("%d companies, total %d, US %d of %d", len(p.Companies), p.Total, p.US, us)
	}
	sum := 0
	for _, c := range p.Companies {
		sum += c.US
	}
	if sum >= p.US {
		t.Fatalf("the bounded list sums to %d of %d: nothing was cut", sum, p.US)
	}
}

// 20,000 pending postings, oversized and malformed locations among them: US is
// counted without a panic, and stays within [0, Total].
func TestPendingOfUSAdversarialLocations(t *testing.T) {
	long := strings.Repeat("San Francisco, ", 5000)
	locs := []string{"", ",", ",,,", "   ", "\x00", "Remote -", "us", "USA", long, strings.Repeat("x", 1<<16), "💥, 💥", "Remote - US" + strings.Repeat(", Canada", 1000)}
	var recs []ledger.Record
	for i := 0; i < 20000; i++ {
		recs = append(recs, recAt(fmt.Sprintf("gh:c%d:%d", i%7, i), fmt.Sprintf("C%d", i%7), locs[i%len(locs)], posting.Live))
	}
	p := PendingOf(ledger.Snapshot{Records: recs}, nil, "")
	if p.Total != 20000 || p.US < 0 || p.US > p.Total {
		t.Fatalf("total %d US %d", p.Total, p.US)
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
