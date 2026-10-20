package digest

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func fit(v string) map[string]string {
	return map[string]string{ledger.AnnRank: "60", ledger.AnnCoverage: "60",
		"triage.role_fit": v, "triage.role_fit.reading": "answered", "triage.role_fit.p": "0.9"}
}

func aged(r ledger.Record, days int) ledger.Record {
	r.FirstSeen = day.Add(-time.Duration(days) * 24 * time.Hour)
	r.Posting.PostedAt = r.FirstSeen
	return r
}

func sectionKeys(shown []Shown, section string) []string {
	var out []string
	for _, s := range shown {
		if s.Section == section {
			out = append(out, string(s.Key))
		}
	}
	return out
}

// Top open answers "what is the best open role" with age left out: an
// excellent 21-day-old role leads a mediocre fresh one there, though the
// age-weighted priority still puts the fresh one first among new roles. This
// is the split the red-team case TestRTSharpRecencyBuriesExcellent asks for.
func TestTopOpenRanksByRelevanceNotAge(t *testing.T) {
	old := aged(rec("gh:a:old", posting.Digested, "Excellent but older", fit("strong_fit")), 21)
	fresh := aged(rec("gh:a:fresh", posting.Digested, "Mediocre and new", fit("adjacent")), 0)
	hidden := aged(rec("gh:a:hid", posting.Digested, "Hidden", fit("strong_fit")), 1)
	saved := aged(rec("gh:a:sav", posting.Digested, "Saved", fit("strong_fit")), 1)
	dead := aged(rec("gh:a:dead", posting.Dead, "Dead", fit("strong_fit")), 1)
	m := map[posting.Key]marks.Mark{"gh:a:hid": marks.Hidden, "gh:a:sav": marks.Saved}
	recs := []ledger.Record{fresh, old, hidden, saved, dead}

	md, keys, shown := RenderShown(recs, m, day, Counts{}, Options{TopOpen: 5})
	if len(keys) != 0 {
		t.Fatalf("Top open roles must not be returned as new keys (they would be re-marked Digested): %v", keys)
	}
	if got := sectionKeys(shown, SectionTopOpen); !slices.Equal(got, []string{"gh:a:old", "gh:a:fresh"}) {
		t.Fatalf("top open %v, want old then fresh", got)
	}
	res := func(k string) jobinsight.Result {
		for _, s := range shown {
			if string(s.Key) == k {
				return s.Result
			}
		}
		t.Fatalf("%s not shown", k)
		return jobinsight.Result{}
	}
	if o, f := res("gh:a:old"), res("gh:a:fresh"); !(o.Score < f.Score && o.Relevance > f.Relevance) {
		t.Fatalf("the case is not the recency trade-off: old %.1f/%.1f, fresh %.1f/%.1f", o.Score, o.Relevance, f.Score, f.Relevance)
	}
	if !strings.Contains(md, "## Top open (2)") || !strings.Contains(md, "key `gh:a:old`") {
		t.Fatalf("digest:\n%s", md)
	}
	if !strings.Contains(md, "No new roles today.") {
		t.Fatal("a day with only Top open roles still says nothing is new")
	}
}

func TestTopOpenAndExploreOffByDefault(t *testing.T) {
	recs := []ledger.Record{rec("gh:a:1", posting.Digested, "Old", fit("strong_fit")), rec("gh:a:2", posting.Dropped, "Gone", nil)}
	md, _, shown := RenderShown(recs, nil, day, Counts{}, Options{})
	if strings.Contains(md, "Top open") || strings.Contains(md, "Explore") || len(shown) != 0 {
		t.Fatalf("sections without options:\n%s\n%v", md, shown)
	}
}

func TestTopOpenBounds(t *testing.T) {
	var recs []ledger.Record
	for i := 0; i < MaxTopOpen+20; i++ {
		recs = append(recs, rec(fmt.Sprintf("gh:a:%03d", i), posting.Digested, "Role", fit("strong_fit")))
	}
	_, _, shown := RenderShown(recs, nil, day, Counts{}, Options{TopOpen: 10_000})
	if n := len(sectionKeys(shown, SectionTopOpen)); n != MaxTopOpen {
		t.Fatalf("top open listed %d, cap %d", n, MaxTopOpen)
	}
	_, _, shown = RenderShown(recs, nil, day, Counts{}, Options{TopOpen: -3, ExploreBelowFold: -1, ExploreDropped: -1})
	if len(shown) != 0 {
		t.Fatalf("negative counts listed %v", shown)
	}
}

func dropped(key, backend, slot, value string) ledger.Record {
	return rec(key, posting.Dropped, "Dropped "+key, map[string]string{
		"triage.backend": backend, "triage." + slot: value, "triage." + slot + ".reading": "answered"})
}

var rules = []jobinsight.DropRule{{Slot: "role_fit", Values: []string{"mismatch_domain"}}, {Slot: "seniority", Values: []string{"staff_plus", "manager"}}}

func TestExplorePicksBothPoolsDeterministically(t *testing.T) {
	var recs []ledger.Record
	for i := 0; i < 30; i++ {
		recs = append(recs, rec(fmt.Sprintf("gh:f:%02d", i), posting.Digested, "Below", fit("adjacent")))
		recs = append(recs, dropped(fmt.Sprintf("gh:d:%02d", i), "title", "role_fit", "mismatch_domain"))
	}
	m := map[posting.Key]marks.Mark{"gh:f:00": marks.Saved, "gh:d:00": marks.Hidden}
	for i := 1; i < 30; i++ { // a saved dropped role is listed under Saved, never explored
		m[posting.Key(fmt.Sprintf("gh:d:%02d", i))] = marks.Saved
	}
	m["gh:d:29"] = marks.None
	o := Options{Rules: rules, ExploreBelowFold: 2, ExploreDropped: 1}
	md, keys, shown := RenderShown(recs, m, day, Counts{}, o)
	ex := sectionKeys(shown, SectionExplore)
	if len(keys) != 0 || len(ex) != 3 {
		t.Fatalf("keys %v explore %v", keys, ex)
	}
	if !strings.HasPrefix(ex[0], "gh:f:") || !strings.HasPrefix(ex[1], "gh:f:") || ex[2] != "gh:d:29" {
		t.Fatalf("explore %v: want two below-fold then the one unmarked dropped role", ex)
	}
	for _, k := range ex {
		if k == "gh:f:00" || k == "gh:d:00" {
			t.Fatalf("explore picked a marked role %s", k)
		}
	}
	for _, s := range shown {
		if strings.HasPrefix(string(s.Key), "gh:d:") && s.DropClass != "title:role_fit" {
			t.Fatalf("drop class %q", s.DropClass)
		}
	}
	if !strings.Contains(md, "Dropped: title") {
		t.Fatalf("dropped role shows no reason:\n%s", md)
	}
	// The same day picks the same roles; another day picks others.
	_, _, again := RenderShown(recs, m, day, Counts{}, o)
	if !slices.Equal(sectionKeys(again, SectionExplore), ex) {
		t.Fatal("explore is not reproducible within a day")
	}
	differs := false
	for d := 1; d <= 5 && !differs; d++ {
		_, _, other := RenderShown(recs, m, day.AddDate(0, 0, d), Counts{}, o)
		differs = !slices.Equal(sectionKeys(other, SectionExplore), ex)
	}
	if !differs {
		t.Fatal("explore picked the same roles five days running")
	}
}

// Explore never repeats a Top open role: those are already in front of the owner.
func TestExploreSkipsTopOpen(t *testing.T) {
	recs := []ledger.Record{rec("gh:a:1", posting.Digested, "Only", fit("strong_fit"))}
	_, _, shown := RenderShown(recs, nil, day, Counts{}, Options{TopOpen: 1, ExploreBelowFold: 1})
	if len(shown) != 1 || shown[0].Section != SectionTopOpen {
		t.Fatalf("shown %+v", shown)
	}
}

func TestDropClass(t *testing.T) {
	cases := []struct {
		r    ledger.Record
		want string
	}{
		{dropped("k", "title", "role_fit", "mismatch_domain"), "title:role_fit"},
		{dropped("k", "liquid/d1-ultra", "seniority", "staff_plus"), "liquid/d1-ultra:seniority"},
		{dropped("k", "", "seniority", "manager"), "unknown:seniority"},
		{dropped("k", "title", "work_mode", "onsite"), "title:other"},
		{dropped("k", "x`y\n<b>", "role_fit", "mismatch_domain"), "xyb:role_fit"},
		{rec("k", posting.Digested, "Kept", fit("mismatch_domain")), ""},
	}
	for _, c := range cases {
		if got := DropClass(c.r, rules); got != c.want {
			t.Errorf("DropClass = %q, want %q", got, c.want)
		}
	}
}

func TestShownPositionsFollowReadingOrder(t *testing.T) {
	recs := []ledger.Record{
		rec("gh:n:1", posting.Explained, "New", fit("strong_fit")),
		rec("gh:o:1", posting.Digested, "Open", fit("strong_fit")),
		rec("gh:s:1", posting.Digested, "Saved", fit("partial_fit")),
	}
	m := map[posting.Key]marks.Mark{"gh:s:1": marks.Saved}
	_, keys, shown := RenderShown(recs, m, day, Counts{}, Options{TopOpen: 3})
	if !slices.Equal(keys, []posting.Key{"gh:n:1"}) {
		t.Fatalf("keys %v", keys)
	}
	want := []string{SectionRanked, SectionTopOpen, SectionSaved}
	for i, s := range shown {
		if s.Position != i+1 || s.Section != want[i] {
			t.Fatalf("shown %+v", shown)
		}
	}
}

// A key in the digest's code span reads back exactly, so `vani audit judge`
// can take it; a hostile key cannot break out of the span or the line.
func TestKeyCodeSpan(t *testing.T) {
	if got := codeSpan("gh:acme:123_4"); got != "gh:acme:123_4" {
		t.Fatalf("plain key changed: %q", got)
	}
	if got := codeSpan("a`b\nc\td"); got != "abcd" {
		t.Fatalf("hostile key: %q", got)
	}
	if got := codeSpan(strings.Repeat("k", maxFieldRunes+9)); len(got) != maxFieldRunes {
		t.Fatalf("long key kept %d runes", len(got))
	}
}

// WriteShown reports what the written file lists; a run that leaves today's
// digest alone shows nothing, so no impression is logged twice.
func TestWriteShownOnlyForAWrittenFile(t *testing.T) {
	dir := t.TempDir()
	recs := []ledger.Record{rec("gh:o:1", posting.Digested, "Open", fit("strong_fit"))}
	o := Options{TopOpen: 3}
	path, keys, shown, err := WriteShown(dir, day, recs, nil, Counts{}, o)
	if err != nil || len(keys) != 0 || len(shown) != 1 {
		t.Fatalf("first write: %s %v %v %v", path, keys, shown, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "digests", "2026-10-18.md")); err != nil {
		t.Fatal(err)
	}
	path2, keys2, shown2, err := WriteShown(dir, day, recs, nil, Counts{}, o)
	if err != nil || path2 != path || keys2 != nil || shown2 != nil {
		t.Fatalf("second write: %s %v %v %v", path2, keys2, shown2, err)
	}
	if _, _, _, err := WriteShown("", day, recs, nil, Counts{}, o); err == nil {
		t.Fatal("empty state dir accepted")
	}
}
