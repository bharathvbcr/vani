package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

var pfAt = time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

func pfPost(i int) posting.Posting {
	return posting.Posting{ATS: posting.Workday, Company: "Acme", Title: fmt.Sprintf("Intern %03d", i), Location: "Austin, TX"}
}

func TestPrefilterSampleKeepsTheSmallestHashesWhateverTheOrder(t *testing.T) {
	const week = "2026-W46"
	fwd, rev := NewPrefilterSample(10), NewPrefilterSample(10)
	for i := 0; i < 200; i++ {
		fwd.Note(pfPost(i), "exclude_title", week, pfAt)
	}
	for i := 199; i >= 0; i-- {
		rev.Note(pfPost(i), "exclude_title", week, pfAt)
	}
	a, seenA := fwd.Week(week)
	b, seenB := rev.Week(week)
	if len(a) != 10 || len(b) != 10 {
		t.Fatalf("held %d and %d, want the cap of 10", len(a), len(b))
	}
	if seenA != 200 || seenB != 200 {
		t.Errorf("seen %d and %d, want 200 each", seenA, seenB)
	}
	for i := range a {
		if a[i].Key != b[i].Key {
			t.Fatalf("the order the rows came in changed the sample: %v vs %v", a, b)
		}
	}
	// The kept rows are the 10 smallest hashes of all 200.
	var keys []posting.Key
	for i := 0; i < 200; i++ {
		keys = append(keys, PrefilterKey(pfPost(i)))
	}
	sort.Slice(keys, func(i, j int) bool { return lessWeekHash(weekHash(week, keys[i]), weekHash(week, keys[j])) })
	want := map[posting.Key]bool{}
	for _, k := range keys[:10] {
		want[k] = true
	}
	for _, r := range a {
		if !want[r.Key] {
			t.Errorf("held %s, which is not among the 10 smallest hashes", r.Key)
		}
	}
}

func TestPrefilterSampleTakesARowListedEveryDayOnce(t *testing.T) {
	s := NewPrefilterSample(5)
	for day := 0; day < 7; day++ {
		s.Note(pfPost(1), "exclude_title", "2026-W46", pfAt.AddDate(0, 0, day))
	}
	rows, seen := s.Week("2026-W46")
	if len(rows) != 1 || seen != 7 {
		t.Fatalf("rows %d seen %d, want 1 row of 7 reports", len(rows), seen)
	}
	// Another week is a draw of its own.
	s.Note(pfPost(1), "exclude_title", "2026-W47", pfAt)
	if r, _ := s.Week("2026-W47"); len(r) != 1 {
		t.Errorf("a new week holds %d rows", len(r))
	}
	if all := s.Rows(); len(all) != 1 {
		t.Errorf("a key held in two weeks is one audit candidate, got %d", len(all))
	}
}

func TestPrefilterSampleIgnoresAndCleansWhatItCannotUse(t *testing.T) {
	s := NewPrefilterSample(5)
	s.Note(posting.Posting{ATS: posting.Workday}, "exclude_title", "2026-W46", pfAt) // nothing to judge
	s.Note(pfPost(1), "", "2026-W46", pfAt)                                          // no rule
	s.Note(pfPost(1), "exclude_title", "", pfAt)                                     // no week
	s.Note(pfPost(2), "Exclude Title!!", "2026-W46", pfAt)                           // rule is made a safe token
	s.Note(posting.Posting{ATS: posting.Workday, Company: "A\x1b[31mcme", Title: "T\n\t x" + strings.Repeat("é", 400)}, "exclude_title", "2026-W46", pfAt)
	var nilSample *PrefilterSample
	nilSample.Note(pfPost(3), "exclude_title", "2026-W46", pfAt) // a nil sample counts nothing, and does not panic
	rows, _ := s.Week("2026-W46")
	if len(rows) != 2 {
		t.Fatalf("held %d rows: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if strings.ContainsAny(r.Company+r.Title, "\x1b\n\t") || len(r.Title) > maxPrefilterField {
			t.Errorf("unclean row %+v", r)
		}
		if r.Rule != "excludetitle" && r.Rule != "exclude_title" {
			t.Errorf("rule %q", r.Rule)
		}
	}
}

func TestPrefilterSampleSaveLoadRoundTripAndBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit", "prefilter-sample.json")
	s := NewPrefilterSample(3)
	for w := 40; w < 52; w++ { // twelve weeks: only the newest PrefilterWeeksKept stay
		for i := 0; i < 10; i++ {
			s.Note(pfPost(i), "exclude_location", fmt.Sprintf("2026-W%02d", w), pfAt)
		}
	}
	if err := s.Save(path, pfAt); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPrefilterSample(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.weeks) != PrefilterWeeksKept {
		t.Errorf("kept %d weeks, want %d", len(got.weeks), PrefilterWeeksKept)
	}
	if _, old := got.weeks["2026-W40"]; old {
		t.Error("the oldest week survived")
	}
	rows, seen := got.Week("2026-W51")
	if len(rows) != 3 || seen != 10 {
		t.Errorf("newest week: %d rows seen %d, want 3 of 10", len(rows), seen)
	}
	if got.Changed() {
		t.Error("a freshly loaded sample reports a change")
	}
	// No description or other text field exists in the file.
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "description") {
		t.Errorf("the sample file mentions a description:\n%s", b)
	}
	// A lower cap on the next load trims on the next save.
	small, err := LoadPrefilterSample(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := small.Save(path, pfAt); err != nil {
		t.Fatal(err)
	}
	again, _ := LoadPrefilterSample(path, 1)
	if r, _ := again.Week("2026-W51"); len(r) != 1 {
		t.Errorf("a cap of 1 kept %d", len(r))
	}
	// The cap itself is bounded.
	if NewPrefilterSample(1<<30).perWeek != MaxPrefilterSamplePerWeek || NewPrefilterSample(0).perWeek != DefaultPrefilterSamplePerWeek {
		t.Error("the per-week cap is not bounded or defaulted")
	}
}

func TestLoadPrefilterSampleRefusesWhatItCannotReadAndNeverTakesItForEmpty(t *testing.T) {
	dir := t.TempDir()
	row := `{"key":"x:00000000000000000000aa01","ats":"wd","title":"T","rule":"exclude_title","at":"2026-11-10T09:00:00Z"}`
	for name, body := range map[string]string{
		"malformed":     `{"v":1,`,
		"version":       `{"v":2,"weeks":{}}`,
		"unknown field": `{"v":1,"weeks":{},"x":1}`,
		"trailing":      `{"v":1,"weeks":{}} {}`,
		"no key":        `{"v":1,"weeks":{"2026-W46":{"seen":1,"rows":[{"key":"","ats":"wd","title":"T","rule":"exclude_title","at":"2026-11-10T09:00:00Z"}]}}}`,
		"bad ats key":   `{"v":1,"weeks":{"2026-W46":{"seen":1,"rows":[{"key":"zz:1","ats":"wd","title":"T","rule":"exclude_title","at":"2026-11-10T09:00:00Z"}]}}}`,
		"unsafe rule":   `{"v":1,"weeks":{"2026-W46":{"seen":1,"rows":[{"key":"x:00000000000000000000aa01","ats":"wd","title":"T","rule":"A b","at":"2026-11-10T09:00:00Z"}]}}}`,
		"no time":       `{"v":1,"weeks":{"2026-W46":{"seen":1,"rows":[{"key":"x:00000000000000000000aa01","ats":"wd","title":"T","rule":"exclude_title"}]}}}`,
		"duplicate key": `{"v":1,"weeks":{"2026-W46":{"seen":2,"rows":[` + row + `,` + row + `]}}}`,
		"negative seen": `{"v":1,"weeks":{"2026-W46":{"seen":-1,"rows":[]}}}`,
		"null week":     `{"v":1,"weeks":{"2026-W46":null}}`,
		"long title":    `{"v":1,"weeks":{"2026-W46":{"seen":1,"rows":[{"key":"x:00000000000000000000aa01","ats":"wd","title":"` + strings.Repeat("a", 201) + `","rule":"exclude_title","at":"2026-11-10T09:00:00Z"}]}}}`,
		"too many weeks": func() string {
			var b strings.Builder
			b.WriteString(`{"v":1,"weeks":{`)
			for i := 0; i < 4*PrefilterWeeksKept+1; i++ {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `"2026-W%02d":{"seen":0,"rows":[]}`, i+1)
			}
			b.WriteString(`}}`)
			return b.String()
		}(),
		"oversized": `{"v":1,"weeks":{},"pad":"` + strings.Repeat("a", maxPrefilterSampleFile) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".json")
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if s, err := LoadPrefilterSample(p, 50); err == nil {
				t.Fatalf("loaded %+v", s)
			}
		})
	}
	s, err := LoadPrefilterSample(filepath.Join(dir, "none.json"), 50)
	if err != nil || s == nil || len(s.Rows()) != 0 {
		t.Errorf("a missing file is an empty sample: %v %v", s, err)
	}
}

func TestPrefilterSampleIsSafeForConcurrentNotes(t *testing.T) {
	s := NewPrefilterSample(20)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				s.Note(pfPost(i), "exclude_title", "2026-W46", pfAt)
			}
		}()
	}
	wg.Wait()
	rows, seen := s.Week("2026-W46")
	if len(rows) != 20 || seen != 8*300 {
		t.Errorf("rows %d seen %d", len(rows), seen)
	}
	single := NewPrefilterSample(20)
	for i := 0; i < 300; i++ {
		single.Note(pfPost(i), "exclude_title", "2026-W46", pfAt)
	}
	want, _ := single.Week("2026-W46")
	for i := range want {
		if want[i].Key != rows[i].Key {
			t.Fatal("concurrent notes chose other rows than one goroutine did")
		}
	}
}

func TestPrefilterKeyIsStableAndNeverALedgerFallbackKey(t *testing.T) {
	p := pfPost(1)
	k := PrefilterKey(p)
	if k != PrefilterKey(posting.Posting{ATS: posting.Workday, Company: " ACME ", Title: "intern  001", Location: "austin, tx"}) {
		t.Error("case and spacing split one row into two keys")
	}
	if k == PrefilterKey(pfPost(2)) || k == PrefilterKey(posting.Posting{ATS: posting.Rippling, Company: "Acme", Title: p.Title, Location: p.Location}) {
		t.Error("two rows share a key")
	}
	fb, err := posting.FallbackKey(p.Company, p.Title, p.Location)
	if err != nil {
		t.Fatal(err)
	}
	if k == fb || k.ATS() != posting.Other {
		t.Errorf("key %s (fallback %s, ats %q)", k, fb, k.ATS())
	}
}

func TestPrefilterRuleNamesTheRuleNeverItsValue(t *testing.T) {
	for reason, want := range map[string]string{
		`excluded: title matches "intern"`:                       "exclude_title",
		`excluded: company is "Acme"`:                            "exclude_company",
		`excluded: location matches "India"`:                     "exclude_location",
		`excluded: location is outside only_locations`:           "exclude_location",
		`excluded: location is outside only_locations (unrecognised location)`: "exclude_location_unrecognised",
		`excluded: location matches "unrecognised location"`:                   "exclude_location",
		`excluded: description matches an excluded phrase`:       "exclude_description",
		`excluded: something new`:                                "exclude_other",
		`slot "role_fit" = "mismatch_domain": title matched "x"`: "read_role_fit",
		`slot "seniority" = "staff_plus": title matched "Staff"`: "read_seniority",
		``:        "other",
		`unknown`: "other",
	} {
		if got := PrefilterRule(reason); got != want {
			t.Errorf("PrefilterRule(%q) = %q, want %q", reason, got, want)
		}
	}
	if strings.Contains(PrefilterRule(`excluded: title matches "visa sponsorship"`), "visa") {
		t.Error("a rule's value reached the class")
	}
}

func TestListedDropsRecordedTellsRecWhatItDropsAndJudgesAsListedDropsDoes(t *testing.T) {
	pol := DropPolicy{Exclude: func(p posting.Posting) (string, bool) {
		if strings.Contains(strings.ToLower(p.Title), "intern") {
			return `title matches "intern"`, true
		}
		if p.Location == "Lyon" {
			return "location is outside only_locations", true
		}
		return "", false
	}}
	var mu sync.Mutex
	got := map[string]string{}
	drop := pol.ListedDropsRecorded(func(p posting.Posting, rule string) {
		mu.Lock()
		got[p.Title] = rule
		mu.Unlock()
	})
	plain := pol.ListedDrops()
	for _, p := range []posting.Posting{
		{Title: "Software Intern"}, {Title: "Engineer", Location: "Lyon"}, {Title: "Engineer", Location: "Austin"}, {},
	} {
		if drop(p) != plain(p) {
			t.Errorf("%+v: recorded %v, plain %v", p, drop(p), plain(p))
		}
	}
	if len(got) != 2 || got["Software Intern"] != "exclude_title" || got["Engineer"] != "exclude_location" {
		t.Errorf("recorded %v", got)
	}
	if (DropPolicy{}).ListedDropsRecorded(func(posting.Posting, string) {}) != nil {
		t.Error("a policy that prefilters nothing must give nil, as ListedDrops does (adapters test for nil)")
	}
	if drop := pol.ListedDropsRecorded(nil); drop == nil || !drop(posting.Posting{Title: "Intern"}) {
		t.Error("a nil recorder must still drop")
	}
}
