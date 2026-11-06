package samecompany

import (
	"fmt"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

var t0 = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

func rec(key, company, title, loc string, first time.Time) ledger.Record {
	return ledger.Record{Key: posting.Key(key), Status: posting.Triaged, FirstSeen: first,
		Posting: posting.Posting{Key: posting.Key(key), Company: company, Title: title, Location: loc}}
}

func appliedAt(key string, at time.Time) map[posting.Key]marks.Entry {
	return map[posting.Key]marks.Entry{posting.Key(key): {Key: posting.Key(key), Mark: marks.Applied, MarkedAt: at}}
}

func merge(ms ...map[posting.Key]marks.Entry) map[posting.Key]marks.Entry {
	out := map[posting.Key]marks.Entry{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func TestRepeatApplyWindowBoundaries(t *testing.T) {
	cases := []struct {
		name string
		ago  time.Duration
		want bool
	}{
		{"just now", 0, true},
		{"one day short of the window", days(ApplyWindowDays - 1), true},
		{"exactly the window", days(ApplyWindowDays), true},
		{"a second past the window", days(ApplyWindowDays) + time.Second, false},
		{"a day past the window", days(ApplyWindowDays + 1), false},
		{"a mark dated in the future (clock skew)", -time.Hour, false},
		{"one nanosecond in the future", -time.Nanosecond, false},
		{"a mark one year in the future", -days(365), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recs := []ledger.Record{
				rec("gh:acme:1", "Acme", "Backend Engineer", "", t0.Add(-days(200))),
				rec("gh:acme:2", "Acme", "Data Engineer", "", t0.Add(-days(1))),
			}
			ix := New(recs, appliedAt("gh:acme:1", t0.Add(-c.ago)), nil, t0)
			a, ok := ix.RepeatApply("gh:acme:2", "Acme")
			if ok != c.want {
				t.Fatalf("ok = %v, want %v (%+v)", ok, c.want, a)
			}
			if ok && (a.Count != 1 || a.Latest.Key != "gh:acme:1" || a.Latest.Title != "Backend Engineer") {
				t.Fatalf("answer = %+v", a)
			}
		})
	}
}

func TestRepeatApplyFutureMarkDoesNotReplaceARecentOne(t *testing.T) {
	recs := []ledger.Record{
		rec("gh:acme:future", "Acme", "Future Role", "", t0.Add(-days(10))),
		rec("gh:acme:recent", "Acme", "Recent Role", "", t0.Add(-days(10))),
		rec("gh:acme:target", "Acme", "Target", "", t0),
	}
	m := merge(
		appliedAt("gh:acme:future", t0.Add(days(365))),
		appliedAt("gh:acme:recent", t0.Add(-days(2))),
	)
	a, ok := New(recs, m, nil, t0).RepeatApply("gh:acme:target", "Acme")
	if !ok || a.Count != 1 || a.Latest.Key != "gh:acme:recent" || a.Latest.Title != "Recent Role" || !a.Latest.At.Equal(t0.Add(-days(2))) {
		t.Fatalf("answer = %+v ok=%v", a, ok)
	}
}

func TestRepeatApplyZeroTimeMarkDoesNotCount(t *testing.T) {
	recs := []ledger.Record{
		rec("gh:acme:1", "Acme", "Backend Engineer", "", t0.Add(-days(200))),
		rec("gh:acme:2", "Acme", "Data Engineer", "", t0),
	}
	a, ok := New(recs, appliedAt("gh:acme:1", time.Time{}), nil, t0).RepeatApply("gh:acme:2", "Acme")
	if ok {
		t.Fatalf("a zero applied time counted: %+v", a)
	}
}

func TestRepeatApplyFutureMarkAloneDoesNotWarn(t *testing.T) {
	recs := []ledger.Record{
		rec("gh:acme:1", "Acme", "Future Role", "", t0.Add(-days(3))),
		rec("gh:acme:2", "Acme", "Target", "", t0),
	}
	a, ok := New(recs, appliedAt("gh:acme:1", t0.Add(time.Nanosecond)), nil, t0).RepeatApply("gh:acme:2", "Acme")
	if ok {
		t.Fatalf("a future mark alone warned: %+v", a)
	}
}

func TestRepeatApplyAlreadyAppliedStaysSilentWithAFutureSibling(t *testing.T) {
	recs := []ledger.Record{
		rec("gh:acme:1", "Acme", "Target", "", t0),
		rec("gh:acme:2", "Acme", "Future Role", "", t0),
	}
	m := merge(
		appliedAt("gh:acme:1", t0.Add(-days(1))),
		appliedAt("gh:acme:2", t0.Add(time.Hour)),
	)
	if a, ok := New(recs, m, nil, t0).RepeatApply("gh:acme:1", "Acme"); ok {
		t.Fatalf("an applied posting was warned: %+v", a)
	}
}

func TestRepeatApplyNoPriorApplication(t *testing.T) {
	recs := []ledger.Record{
		rec("gh:acme:1", "Acme", "A", "", t0), rec("gh:acme:2", "Acme", "B", "", t0),
	}
	saved := map[posting.Key]marks.Entry{"gh:acme:1": {Key: "gh:acme:1", Mark: marks.Saved, MarkedAt: t0}}
	for name, m := range map[string]map[posting.Key]marks.Entry{"no marks": nil, "only saved": saved} {
		if a, ok := New(recs, m, nil, t0).RepeatApply("gh:acme:2", "Acme"); ok {
			t.Errorf("%s: warned %+v", name, a)
		}
	}
}

func TestRepeatApplyCountsAndNamesTheMostRecent(t *testing.T) {
	recs := []ledger.Record{
		rec("gh:acme:1", "Acme", "Old Role", "", t0),
		rec("ab:acme:2", "Acme Inc.", "Newest Role", "", t0),
		rec("lv:acme:3", "ACME", "Middle Role", "", t0),
		rec("gh:acme:4", "Acme", "Target", "", t0),
		rec("gh:other:1", "Other", "Elsewhere", "", t0),
	}
	m := merge(
		appliedAt("gh:acme:1", t0.Add(-days(50))),
		appliedAt("ab:acme:2", t0.Add(-days(2))),
		appliedAt("lv:acme:3", t0.Add(-days(10))),
		appliedAt("gh:other:1", t0.Add(-days(1))),
	)
	a, ok := New(recs, m, nil, t0).RepeatApply("gh:acme:4", "Acme")
	if !ok || a.Count != 3 || a.Latest.Key != "ab:acme:2" || a.Latest.Title != "Newest Role" || !a.Latest.At.Equal(t0.Add(-days(2))) {
		t.Fatalf("answer = %+v ok=%v", a, ok)
	}
}

func TestRepeatApplyDoesNotCountThePostingItself(t *testing.T) {
	recs := []ledger.Record{rec("gh:acme:1", "Acme", "A", "", t0), rec("gh:acme:2", "Acme", "B", "", t0)}
	m := merge(appliedAt("gh:acme:1", t0.Add(-days(1))))
	ix := New(recs, m, nil, t0)
	if a, ok := ix.RepeatApply("gh:acme:1", "Acme"); ok {
		t.Fatalf("a posting already applied to is warned about itself: %+v", a)
	}
	// With another applied posting, the applied one is still not warned: there is
	// no application left to make.
	m = merge(m, appliedAt("gh:acme:2", t0.Add(-days(3))))
	ix = New(recs, m, nil, t0)
	if _, ok := ix.RepeatApply("gh:acme:1", "Acme"); ok {
		t.Fatal("an applied posting was warned")
	}
}

// Marks hold one value per posting, so hiding an applied posting replaces the
// Applied mark and the application is no longer counted. This pins that
// current behaviour; counting later stages, or keeping an applied history, is
// the stages work's change (AppliedAt).
func TestRepeatApplyForgetsAnApplicationHiddenAfterwards(t *testing.T) {
	recs := []ledger.Record{rec("gh:acme:1", "Acme", "A", "", t0), rec("gh:acme:2", "Acme", "B", "", t0)}
	hidden := map[posting.Key]marks.Entry{"gh:acme:1": {Key: "gh:acme:1", Mark: marks.Hidden, MarkedAt: t0.Add(-days(1))}}
	if _, ok := New(recs, hidden, nil, t0).RepeatApply("gh:acme:2", "Acme"); ok {
		t.Fatal("a hidden posting counted as applied")
	}
}

func TestAppliedAtIsTheOneDefinitionOfApplied(t *testing.T) {
	at := t0.Add(-time.Hour)
	for mark, want := range map[marks.Mark]bool{marks.Applied: true, marks.Saved: false, marks.Hidden: false, marks.None: false} {
		got, ok := AppliedAt(marks.Entry{Mark: mark, MarkedAt: at})
		if ok != want || (ok && !got.Equal(at)) {
			t.Errorf("%q: (%v, %v), want ok=%v", mark, got, ok, want)
		}
	}
	if _, ok := AppliedAt(marks.Entry{Mark: marks.Applied}); ok {
		t.Error("an applied mark with no time counted")
	}
}

func TestSameCompanyAcrossBoardsAndNameVariants(t *testing.T) {
	variants := []string{
		"Acme", "ACME", "  acme  ", "Acme, Inc.", "Acme Inc", "Acme LLC", "Acme Corporation", "acme.",
		"ＡＣＭＥ", // full-width letters
	}
	for _, v := range variants {
		recs := []ledger.Record{
			rec("ab:acme:1", v, "Applied Role", "", t0),
			rec("gh:acme:2", "Acme", "Target", "", t0),
		}
		if _, ok := New(recs, appliedAt("ab:acme:1", t0.Add(-days(5))), nil, t0).RepeatApply("gh:acme:2", "Acme"); !ok {
			t.Errorf("%q was not the same company as Acme", v)
		}
	}
	for _, other := range []string{"Acme Labs", "Acmeco", "Acm", "Zenith"} {
		recs := []ledger.Record{
			rec("ab:x:1", other, "Applied Role", "", t0),
			rec("gh:acme:2", "Acme", "Target", "", t0),
		}
		if a, ok := New(recs, appliedAt("ab:x:1", t0.Add(-days(5))), nil, t0).RepeatApply("gh:acme:2", "Acme"); ok {
			t.Errorf("%q matched Acme: %+v", other, a)
		}
	}
}

func TestNameFoldsUnicode(t *testing.T) {
	same := [][2]string{
		{"Zoë Labs", "Zoe Labs"},
		{"Zoë Labs", "Zoë Labs"}, // decomposed and composed
		{"Café Corp", "CAFE corp."},
		{"Müller GmbH", "Muller"},
		{"Ｇｏｏｇｌｅ ＬＬＣ", "google"},
		{"Łódź Systems Ltd", "lodz systems"},
	}
	for _, p := range same {
		if a, b := Name(p[0]), Name(p[1]); a != b || a == "" {
			t.Errorf("Name(%q) = %q, Name(%q) = %q; want equal and non-empty", p[0], a, p[1], b)
		}
	}
	if Name("日本 株式会社") == Name("韓国 株式会社") {
		t.Error("distinct non-Latin names folded together")
	}
}

func TestEmptyCompanyMatchesNothing(t *testing.T) {
	for _, blank := range []string{"", "   ", "...", "Inc."} {
		recs := []ledger.Record{
			rec("pg:a:1", blank, "Applied Role", "", t0),
			rec("pg:b:2", blank, "Target", "", t0),
		}
		ix := New(recs, appliedAt("pg:a:1", t0.Add(-days(1))), nil, t0)
		// "Inc." folds to "inc", a real (if odd) name: only the truly empty ones
		// must be inert.
		_, ok := ix.RepeatApply("pg:b:2", blank)
		if wantInert := Name(blank) == ""; wantInert && ok {
			t.Errorf("company %q matched another empty company", blank)
		}
	}
}

func TestRepeatApplyForAnUnknownPosting(t *testing.T) {
	recs := []ledger.Record{rec("gh:acme:1", "Acme", "A", "", t0)}
	ix := New(recs, appliedAt("gh:acme:1", t0.Add(-days(1))), nil, t0)
	if a, ok := ix.RepeatApply("gh:ghost:9", "Nobody"); ok {
		t.Fatalf("unknown company warned: %+v", a)
	}
	if _, ok := (*Index)(nil).RepeatApply("gh:acme:2", "Acme"); ok {
		t.Fatal("a nil index warned")
	}
	if _, ok := New(nil, nil, nil, t0).RepeatApply("gh:acme:2", "Acme"); ok {
		t.Fatal("an empty ledger warned")
	}
}

func TestAnAppliedMarkWithNoRecordIsIgnored(t *testing.T) {
	recs := []ledger.Record{rec("gh:acme:2", "Acme", "B", "", t0)}
	if a, ok := New(recs, appliedAt("gh:acme:1", t0.Add(-days(1))), nil, t0).RepeatApply("gh:acme:2", "Acme"); ok {
		t.Fatalf("a mark for a posting the ledger lacks counted: %+v", a)
	}
}

func TestBoardsSharingADomainAreOneCompany(t *testing.T) {
	boards := []sources.Board{
		{ATS: posting.Greenhouse, Token: "acme", Company: "Acme", Domain: "www.acme.example"},
		{ATS: posting.Ashby, Token: "acme-labs", Company: "Acme Labs", Domain: "WWW.ACME.EXAMPLE"},
		{ATS: posting.Lever, Token: "other", Company: "Other", Domain: "other.example"},
		{ATS: posting.Lever, Token: "nodomain", Company: "Plain"},
	}
	co := NewCompanies(boards)
	recs := []ledger.Record{
		rec("ab:acme-labs:1", "Acme Labs", "Applied Role", "", t0), // another name, but its board shares the domain
		rec("gh:acme:2", "Acme", "Target", "", t0),
		rec("lv:other:3", "Other", "Elsewhere", "", t0),
	}
	m := appliedAt("ab:acme-labs:1", t0.Add(-days(3)))
	if a, ok := New(recs, m, co, t0).RepeatApply("gh:acme:2", "Acme"); !ok || a.Count != 1 {
		t.Fatalf("boards on one domain were not one company: %+v ok=%v", a, ok)
	}
	if _, ok := New(recs, m, nil, t0).RepeatApply("gh:acme:2", "Acme"); ok {
		t.Fatal("without the watchlist, differently named boards were joined")
	}
	if _, ok := New(recs, m, co, t0).RepeatApply("lv:other:3", "Other"); ok {
		t.Fatal("a board on another domain was joined")
	}
	// A posting the extension saved has no board key; its name finds the domain.
	pg := append(recs, rec("pg:saved:9", "Acme Labs", "Saved Page", "", t0))
	if a, ok := New(pg, m, co, t0).RepeatApply("pg:saved:9", "Acme Labs"); !ok || a.Count != 1 {
		t.Fatalf("a name on the domain's board was not joined: %+v ok=%v", a, ok)
	}
}

func TestANameTwoDomainsClaimIsNotJoinedToEither(t *testing.T) {
	co := NewCompanies([]sources.Board{
		{ATS: posting.Greenhouse, Token: "a", Company: "Same Name", Domain: "a.example"},
		{ATS: posting.Greenhouse, Token: "b", Company: "Same Name", Domain: "b.example"},
	})
	if got := co.domainOfName(Name("Same Name")); got != "" {
		t.Fatalf("an ambiguous name resolved to %q", got)
	}
	recs := []ledger.Record{
		rec("pg:x:1", "Same Name", "Applied", "", t0),
		rec("gh:a:2", "Same Name", "Target", "", t0),
	}
	if _, ok := New(recs, appliedAt("pg:x:1", t0.Add(-days(1))), co, t0).RepeatApply("gh:a:2", "Same Name"); ok {
		t.Fatal("a posting on board a was joined to a name-only posting through an ambiguous name")
	}
}

// --- reposts ---

func TestRepostEdges(t *testing.T) {
	cases := []struct {
		name string
		gap  time.Duration
		want bool
	}{
		{"same day", 0, false},
		{"a day short of the minimum", days(RepostMinGapDays - 1), false},
		{"a second short of the minimum", days(RepostMinGapDays) - time.Second, false},
		{"exactly the minimum", days(RepostMinGapDays), true},
		{"a second past the minimum", days(RepostMinGapDays) + time.Second, true},
		{"between", days(40), true},
		{"exactly the maximum", days(RepostMaxGapDays), true},
		{"a second past the maximum", days(RepostMaxGapDays) + time.Second, false},
		{"a day past the maximum", days(RepostMaxGapDays + 1), false},
		{"the later posting listed first", -days(30), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recs := []ledger.Record{
				rec("gh:acme:1", "Acme", "Backend Engineer", "Austin, TX", t0.Add(-c.gap)),
				rec("gh:acme:2", "Acme", "Backend Engineer", "Austin, TX", t0),
			}
			p, ok := New(recs, nil, nil, t0).Repost(recs[1])
			if ok != c.want {
				t.Fatalf("ok = %v, want %v (%+v)", ok, c.want, p)
			}
			if ok && (p.Key != "gh:acme:1" || p.Title != "Backend Engineer" || !p.At.Equal(t0.Add(-c.gap))) {
				t.Fatalf("prior = %+v", p)
			}
		})
	}
}

func TestRepostSiblingsListedTogetherAreNotReposts(t *testing.T) {
	// One role per city, all first seen within the span of each other.
	recs := []ledger.Record{
		rec("gh:acme:1", "Acme", "Backend Engineer", "Austin, TX", t0.Add(-days(3))),
		rec("gh:acme:2", "Acme", "Backend Engineer", "", t0.Add(-days(2))),
		rec("gh:acme:3", "Acme", "Backend Engineer", "Remote", t0.Add(-days(1))),
		rec("gh:acme:4", "Acme", "Backend Engineer", "Austin, TX", t0),
	}
	ix := New(recs, nil, nil, t0)
	for _, r := range recs {
		if p, ok := ix.Repost(r); ok {
			t.Errorf("%s flagged as a repost of %+v", r.Key, p)
		}
	}
}

func TestRepostLocationCompatibility(t *testing.T) {
	cases := []struct {
		name    string
		old, nw string
		oldRem  bool
		newRem  bool
		want    bool
	}{
		{"equal", "Austin, TX", "Austin, TX", false, false, true},
		{"equal with other spelling", "Austin, TX", "Austin, Texas", false, false, true},
		{"old names none", "", "Austin, TX", false, false, true},
		{"new names none", "Austin, TX", "", false, false, true},
		{"both none", "", "", false, false, true},
		{"different cities", "Austin, TX", "Denver, CO", false, false, false},
		{"remote vs a city", "Remote", "Denver, CO", true, false, false},
		{"remote vs remote", "Remote", "Remote", true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := rec("gh:acme:1", "Acme", "Backend Engineer", c.old, t0.Add(-days(30)))
			a.Posting.Remote = c.oldRem
			b := rec("gh:acme:2", "Acme", "Backend Engineer", c.nw, t0)
			b.Posting.Remote = c.newRem
			if _, ok := New([]ledger.Record{a, b}, nil, nil, t0).Repost(b); ok != c.want {
				t.Fatalf("ok = %v, want %v", ok, c.want)
			}
		})
	}
}

func TestRepostTitleVariants(t *testing.T) {
	cases := []struct {
		name    string
		old, nw string
		want    bool
	}{
		{"identical", "Backend Engineer", "Backend Engineer", true},
		{"case and punctuation", "Backend Engineer", "BACKEND  engineer!", true},
		{"a work-mode tag", "Backend Engineer", "Backend Engineer (Remote)", true},
		{"a trailing work-mode", "Backend Engineer", "Backend Engineer - Hybrid", true},
		{"a requisition number", "Backend Engineer", "Backend Engineer R-20431", true},
		{"a seniority word", "Backend Engineer", "Senior Backend Engineer", false},
		{"another role", "Backend Engineer", "Frontend Engineer", false},
		{"a bracketed qualifier", "Backend Engineer (Payments)", "Backend Engineer", false},
		{"empty", "", "", false},
		{"only a requisition", "R-20431", "R-20432", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := rec("gh:acme:1", "Acme", c.old, "", t0.Add(-days(30)))
			b := rec("gh:acme:2", "Acme", c.nw, "", t0)
			if _, ok := New([]ledger.Record{a, b}, nil, nil, t0).Repost(b); ok != c.want {
				t.Fatalf("ok = %v, want %v", ok, c.want)
			}
		})
	}
}

func TestRepostNeedsTheSameCompany(t *testing.T) {
	a := rec("gh:acme:1", "Acme", "Backend Engineer", "", t0.Add(-days(30)))
	b := rec("gh:zenith:2", "Zenith", "Backend Engineer", "", t0)
	if _, ok := New([]ledger.Record{a, b}, nil, nil, t0).Repost(b); ok {
		t.Fatal("another company's role was a repost")
	}
	// Another board, the same company name.
	c := rec("ab:acme:3", "Acme, Inc.", "Backend Engineer", "", t0)
	if p, ok := New([]ledger.Record{a, c}, nil, nil, t0).Repost(c); !ok || p.Key != "gh:acme:1" {
		t.Fatalf("a relisting on another board of the company was missed: %+v ok=%v", p, ok)
	}
	// An empty company is no company.
	e1 := rec("pg:a:1", "", "Backend Engineer", "", t0.Add(-days(30)))
	e2 := rec("pg:b:2", "", "Backend Engineer", "", t0)
	if _, ok := New([]ledger.Record{e1, e2}, nil, nil, t0).Repost(e2); ok {
		t.Fatal("two postings with no company were one company's repost")
	}
}

func TestRepostSameKeyIsNotARepost(t *testing.T) {
	// The ledger holds one record per key; a key listed again is the same posting
	// (dedup folds it). Even a duplicated record in the input is not its own repost.
	a := rec("gh:acme:1", "Acme", "Backend Engineer", "", t0.Add(-days(30)))
	b := rec("gh:acme:1", "Acme", "Backend Engineer", "", t0)
	ix := New([]ledger.Record{a, b}, nil, nil, t0)
	if p, ok := ix.Repost(b); ok {
		t.Fatalf("a key was a repost of itself: %+v", p)
	}
	if p, ok := ix.Repost(a); ok {
		t.Fatalf("a key was a repost of itself: %+v", p)
	}
}

func TestRepostNamesTheMostRecentEarlierListing(t *testing.T) {
	recs := []ledger.Record{
		rec("gh:acme:1", "Acme", "Backend Engineer", "", t0.Add(-days(80))),
		rec("gh:acme:2", "Acme", "Backend Engineer", "", t0.Add(-days(40))),
		rec("gh:acme:3", "Acme", "Backend Engineer", "", t0.Add(-days(5))), // inside the minimum gap
		rec("gh:acme:4", "Acme", "Backend Engineer", "", t0),
	}
	p, ok := New(recs, nil, nil, t0).Repost(recs[3])
	if !ok || p.Key != "gh:acme:2" {
		t.Fatalf("prior = %+v ok=%v, want gh:acme:2", p, ok)
	}
}

func TestRepostIgnoresUnknownFirstSeen(t *testing.T) {
	a := rec("gh:acme:1", "Acme", "Backend Engineer", "", time.Time{})
	b := rec("gh:acme:2", "Acme", "Backend Engineer", "", t0)
	ix := New([]ledger.Record{a, b}, nil, nil, t0)
	if p, ok := ix.Repost(b); ok {
		t.Fatalf("an undated earlier posting counted: %+v", p)
	}
	if p, ok := ix.Repost(a); ok {
		t.Fatalf("an undated posting was flagged: %+v", p)
	}
}

func TestRepostCountsDeadAndDroppedEarlierListings(t *testing.T) {
	// The usual repost: the old listing closed, a new one opened.
	for _, st := range []posting.Status{posting.Dead, posting.Dropped, posting.Duplicate, posting.Digested} {
		a := rec("gh:acme:1", "Acme", "Backend Engineer", "", t0.Add(-days(30)))
		a.Status = st
		b := rec("gh:acme:2", "Acme", "Backend Engineer", "", t0)
		if _, ok := New([]ledger.Record{a, b}, nil, nil, t0).Repost(b); !ok {
			t.Errorf("an earlier %s listing was not counted", st)
		}
	}
}

func TestIndexIsSafeForConcurrentUse(t *testing.T) {
	var recs []ledger.Record
	for i := 0; i < 200; i++ {
		recs = append(recs, rec(fmt.Sprintf("gh:acme:%d", i), "Acme", fmt.Sprintf("Role %d", i%7), "", t0.Add(-days(i%100))))
	}
	ix := New(recs, appliedAt("gh:acme:3", t0.Add(-days(2))), nil, t0)
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for _, r := range recs {
				ix.Repost(r)
				ix.RepeatApply(r.Key, r.Posting.Company)
			}
		}()
	}
	for g := 0; g < 8; g++ {
		<-done
	}
}

// The index is built once per run, so a list of n rows costs about n lookups,
// not n*n comparisons. One company with every posting under one title and place
// is the worst case for a scan-based lookup.
func TestRepostLookupIsBoundedAtScale(t *testing.T) {
	const n = 60000
	recs := make([]ledger.Record, n)
	for i := range recs {
		recs[i] = rec(fmt.Sprintf("gh:big:%d", i), "Big Co", "Software Engineer", fmt.Sprintf("City %d", i%50), t0.Add(-time.Duration(i)*time.Minute))
	}
	start := time.Now()
	ix := New(recs, nil, nil, t0)
	flagged := 0
	for _, r := range recs {
		if _, ok := ix.Repost(r); ok {
			flagged++
		}
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("%d lookups took %v", n, d)
	}
	// Everything is first seen within 60000 minutes (~41 days) of the rest, so
	// the postings more than 14 days after another of their city are flagged.
	if flagged == 0 {
		t.Fatal("nothing was flagged: the scale case measured nothing")
	}
}
