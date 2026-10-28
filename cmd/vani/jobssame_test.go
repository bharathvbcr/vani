package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/samecompany"
)

// sameSeed is one posting for the company-history tests: where and when it was
// first seen, and the page it is on.
type sameSeed struct {
	key, company, title, location, url string
	firstSeen                          time.Time
	status                             posting.Status
}

func seedSame(t *testing.T, stateDir string, seeds ...sameSeed) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	for _, s := range seeds {
		st := s.status
		if st == "" {
			st = posting.Digested
		}
		p := posting.Posting{Key: posting.Key(s.key), ATS: "gh", Company: s.company, Title: s.title, Location: s.location, URL: s.url,
			Description: "Build things."}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: s.firstSeen, Key: p.Key, Status: st, Posting: &p}); err != nil {
			t.Fatal(err)
		}
	}
}

func markAppliedAt(t *testing.T, stateDir, key string, at time.Time) {
	t.Helper()
	if _, err := marks.SetMark(marks.Dir(stateDir), posting.Key(key), marks.Applied, at); err != nil {
		t.Fatal(err)
	}
}

func sameDaysAgo(n int) time.Time { return jobsT0.Add(-time.Duration(n) * 24 * time.Hour) }

const sameTarget = "https://boards.greenhouse.io/acme/jobs/3"

func TestJobsFindWarnsAboutARepeatApplication(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedSame(t, state,
		sameSeed{key: "gh:acme:1", company: "Acme", title: "Backend Engineer", firstSeen: sameDaysAgo(40)},
		sameSeed{key: "ab:acme:2", company: "ACME, Inc.", title: "Staff Data Engineer", firstSeen: sameDaysAgo(30)},
		sameSeed{key: "gh:acme:3", company: "Acme", title: "Platform Engineer", url: sameTarget, firstSeen: sameDaysAgo(2)},
		sameSeed{key: "gh:zenith:1", company: "Zenith", title: "Elsewhere", firstSeen: sameDaysAgo(30)},
	)
	markAppliedAt(t, state, "gh:acme:1", sameDaysAgo(20))
	markAppliedAt(t, state, "ab:acme:2", sameDaysAgo(5))
	markAppliedAt(t, state, "gh:zenith:1", sameDaysAgo(1))

	out := findJSON(t, sameTarget)
	if out.Job == nil || out.Job.RepeatApply == nil {
		t.Fatalf("no warning: %+v", out.Job)
	}
	w := out.Job.RepeatApply
	if w.Count != 2 || w.WindowDays != samecompany.ApplyWindowDays || w.LatestKey != "ab:acme:2" || w.LatestTitle != "Staff Data Engineer" || !w.LatestAt.Equal(sameDaysAgo(5)) {
		t.Fatalf("warning = %+v", w)
	}
	for _, want := range []string{"2 other roles", "Acme", "Staff Data Engineer", sameDaysAgo(5).Format(time.DateOnly), "60 days"} {
		if !strings.Contains(w.Text, want) {
			t.Errorf("text %q lacks %q", w.Text, want)
		}
	}
	// A note, never a block: the same reply still names the job and its row.
	if out.Job.Key != "gh:acme:3" || out.Job.Row == nil {
		t.Fatalf("the warning replaced the job: %+v", out.Job)
	}
}

func TestJobsFindRepeatApplyWindowBoundaries(t *testing.T) {
	cases := []struct {
		name string
		ago  time.Duration
		want bool
	}{
		{"exactly the window", 60 * 24 * time.Hour, true},
		{"a second past", 60*24*time.Hour + time.Second, false},
		{"61 days", 61 * 24 * time.Hour, false},
		{"59 days", 59 * 24 * time.Hour, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, state := setup(t)
			fixedNow(t)
			seedSame(t, state,
				sameSeed{key: "gh:acme:1", company: "Acme", title: "A", firstSeen: sameDaysAgo(200)},
				sameSeed{key: "gh:acme:3", company: "Acme", title: "B", url: sameTarget, firstSeen: sameDaysAgo(1)},
			)
			markAppliedAt(t, state, "gh:acme:1", jobsT0.Add(-c.ago))
			out := findJSON(t, sameTarget)
			if got := out.Job != nil && out.Job.RepeatApply != nil; got != c.want {
				t.Fatalf("warned = %v, want %v: %+v", got, c.want, out.Job)
			}
		})
	}
}

func TestJobsFindStaysQuietWithoutAPriorApplication(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedSame(t, state,
		sameSeed{key: "gh:acme:1", company: "Acme", title: "A", firstSeen: sameDaysAgo(10)},
		sameSeed{key: "gh:acme:3", company: "Acme", title: "B", url: sameTarget, firstSeen: sameDaysAgo(1)},
	)
	mustCode(t, 0, "jobs", "mark", "gh:acme:1", "saved") // saved is not applied
	o, _ := mustCode(t, 0, "jobs", "find", "--json", "--url", sameTarget)
	if strings.Contains(o, "repeat_apply") {
		t.Fatalf("a warning with nothing applied: %s", o)
	}
}

func TestJobsFindDoesNotWarnAboutThePostingAlreadyApplied(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedSame(t, state,
		sameSeed{key: "gh:acme:1", company: "Acme", title: "A", firstSeen: sameDaysAgo(10)},
		sameSeed{key: "gh:acme:3", company: "Acme", title: "B", url: sameTarget, firstSeen: sameDaysAgo(1)},
	)
	markAppliedAt(t, state, "gh:acme:1", sameDaysAgo(3))
	markAppliedAt(t, state, "gh:acme:3", sameDaysAgo(1))
	o, _ := mustCode(t, 0, "jobs", "find", "--json", "--url", sameTarget)
	if strings.Contains(o, "repeat_apply") {
		t.Fatalf("an applied posting was warned about: %s", o)
	}
}

func TestJobsFindUnknownPageHasNoWarning(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedSame(t, state, sameSeed{key: "gh:acme:1", company: "Acme", title: "A", url: sameTarget, firstSeen: sameDaysAgo(10)})
	markAppliedAt(t, state, "gh:acme:1", sameDaysAgo(3))
	o, _ := mustCode(t, 0, "jobs", "find", "--json", "--url", "https://boards.greenhouse.io/acme/jobs/404")
	if strings.Contains(o, "repeat_apply") || strings.Contains(o, `"job"`) {
		t.Fatalf("a page Vani does not track got a job or a warning: %s", o)
	}
}

func TestJobsFindEmptyCompanyIsNeverWarned(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedSame(t, state,
		sameSeed{key: "gh:a:1", company: "", title: "A", firstSeen: sameDaysAgo(10)},
		sameSeed{key: "gh:b:3", company: "", title: "B", url: sameTarget, firstSeen: sameDaysAgo(1)},
	)
	markAppliedAt(t, state, "gh:a:1", sameDaysAgo(3))
	o, _ := mustCode(t, 0, "jobs", "find", "--json", "--url", sameTarget)
	if strings.Contains(o, "repeat_apply") {
		t.Fatalf("two postings with no company were one company: %s", o)
	}
}

// An extension built before this field existed reads the reply by name and
// ignores what it does not know: every key it relied on is still there, and
// the reply without a warning is the same object it always was.
func TestJobsFindReplyIsAdditiveForOldExtensions(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedSame(t, state,
		sameSeed{key: "gh:acme:1", company: "Acme", title: "A", firstSeen: sameDaysAgo(10)},
		sameSeed{key: "gh:acme:3", company: "Acme", title: "B", url: sameTarget, firstSeen: sameDaysAgo(1)},
	)
	keysOf := func(o string) (top, job []string) {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(o), &m); err != nil {
			t.Fatal(err)
		}
		var j map[string]json.RawMessage
		if err := json.Unmarshal(m["job"], &j); err != nil {
			t.Fatal(err)
		}
		for k := range m {
			top = append(top, k)
		}
		for k := range j {
			job = append(job, k)
		}
		return top, job
	}
	quiet, _ := mustCode(t, 0, "jobs", "find", "--json", "--url", sameTarget)
	markAppliedAt(t, state, "gh:acme:1", sameDaysAgo(3))
	warned, _ := mustCode(t, 0, "jobs", "find", "--json", "--url", sameTarget)

	qTop, qJob := keysOf(quiet)
	wTop, wJob := keysOf(warned)
	if strings.Join(sortedCopy(qTop), ",") != strings.Join(sortedCopy(wTop), ",") {
		t.Fatalf("the top-level keys changed: %v vs %v", qTop, wTop)
	}
	extra := map[string]bool{}
	for _, k := range wJob {
		extra[k] = true
	}
	for _, k := range qJob {
		delete(extra, k)
	}
	if len(extra) != 1 || !extra["repeat_apply"] {
		t.Fatalf("the warning must be the one added job key, got %v", extra)
	}
	// Decoding with the reply's pre-warning shape (an old client) still works.
	var old struct {
		Matches int `json:"matches"`
		Job     *struct {
			Key       string       `json:"key"`
			Company   string       `json:"company"`
			Title     string       `json:"title"`
			Mark      marks.Mark   `json:"mark"`
			MatchedOn string       `json:"matched_on"`
			Row       *jobview.Row `json:"row"`
		} `json:"job"`
	}
	if err := json.Unmarshal([]byte(warned), &old); err != nil || old.Job == nil || old.Job.Key != "gh:acme:3" || old.Job.Row == nil {
		t.Fatalf("old-shape decode: %v %+v", err, old)
	}
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestJobsListShowsTheRepeatApplyNote(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedSame(t, state,
		sameSeed{key: "gh:acme:1", company: "Acme", title: "A", firstSeen: sameDaysAgo(10)},
		sameSeed{key: "gh:acme:3", company: "Acme", title: "B", firstSeen: sameDaysAgo(1)},
		sameSeed{key: "gh:zenith:1", company: "Zenith", title: "C", firstSeen: sameDaysAgo(1)},
	)
	markAppliedAt(t, state, "gh:acme:1", sameDaysAgo(3))
	list := listJSON(t, "--view", "all")
	byKey := map[posting.Key]jobview.Job{}
	for _, j := range list.Jobs {
		byKey[j.Key] = j
	}
	if byKey["gh:acme:3"].RepeatApply == nil || byKey["gh:acme:3"].RepeatApply.Count != 1 {
		t.Fatalf("the unapplied Acme row has no note: %+v", byKey["gh:acme:3"].RepeatApply)
	}
	if byKey["gh:acme:1"].RepeatApply != nil || byKey["gh:zenith:1"].RepeatApply != nil {
		t.Fatalf("the applied row or another company's row has a note: %+v %+v", byKey["gh:acme:1"].RepeatApply, byKey["gh:zenith:1"].RepeatApply)
	}
	o, _ := mustCode(t, 0, "jobs", "list", "--view", "all")
	if !strings.Contains(o, "already applied to 1 other role(s) here in 60d") {
		t.Fatalf("the text list does not show the note:\n%s", o)
	}
}

func TestJobsListFlagsALikelyRepost(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedSame(t, state,
		sameSeed{key: "gh:acme:1", company: "Acme", title: "Backend Engineer", location: "Austin, TX", firstSeen: sameDaysAgo(30), status: posting.Dead},
		sameSeed{key: "gh:acme:2", company: "Acme", title: "Backend Engineer (Remote)", location: "Austin, TX", firstSeen: sameDaysAgo(2)},
		// A sibling: the same role listed for another city at the same time as gh:acme:2.
		sameSeed{key: "gh:acme:4", company: "Acme", title: "Backend Engineer", location: "Denver, CO", firstSeen: sameDaysAgo(1)},
	)
	list := listJSON(t, "--view", "all")
	byKey := map[posting.Key]jobview.Job{}
	for _, j := range list.Jobs {
		byKey[j.Key] = j
	}
	rp := byKey["gh:acme:2"].Repost
	if rp == nil || rp.OfKey != "gh:acme:1" || rp.Title != "Backend Engineer" || !rp.FirstSeen.Equal(sameDaysAgo(30)) {
		t.Fatalf("the relisted role is not flagged: %+v", rp)
	}
	if want := "likely repost of Backend Engineer first seen " + sameDaysAgo(30).Format(time.DateOnly); rp.Text != want {
		t.Fatalf("text %q, want %q", rp.Text, want)
	}
	if byKey["gh:acme:1"].Repost != nil || byKey["gh:acme:4"].Repost != nil {
		t.Fatalf("the original or a sibling was flagged: %+v %+v", byKey["gh:acme:1"].Repost, byKey["gh:acme:4"].Repost)
	}
	o, _ := mustCode(t, 0, "jobs", "list", "--view", "all")
	if !strings.Contains(o, "likely repost of Backend Engineer first seen "+sameDaysAgo(30).Format(time.DateOnly)) {
		t.Fatalf("the text list does not show the repost:\n%s", o)
	}
	// Nothing is dropped or blocked by it: the flagged posting is listed and keeps its status.
	if byKey["gh:acme:2"].Status != posting.Digested {
		t.Fatalf("the flag changed the status: %s", byKey["gh:acme:2"].Status)
	}
}

func TestJobsShowAndFindEnrichmentCarryTheSameNotesAsTheList(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedSame(t, state,
		sameSeed{key: "gh:acme:1", company: "Acme", title: "Backend Engineer", firstSeen: sameDaysAgo(40)},
		sameSeed{key: "gh:acme:3", company: "Acme", title: "Backend Engineer", url: sameTarget, firstSeen: sameDaysAgo(2)},
	)
	markAppliedAt(t, state, "gh:acme:1", sameDaysAgo(3))
	var listed jobview.Job
	for _, j := range listJSON(t, "--view", "all").Jobs {
		if j.Key == "gh:acme:3" {
			listed = j
		}
	}
	o, _ := mustCode(t, 0, "jobs", "show", "--json", "gh:acme:3")
	var shown jobview.Job
	if err := json.Unmarshal([]byte(o), &shown); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal([]any{listed.RepeatApply, listed.Repost})
	b, _ := json.Marshal([]any{shown.RepeatApply, shown.Repost})
	if string(a) != string(b) || listed.RepeatApply == nil || listed.Repost == nil {
		t.Fatalf("list %s vs show %s", a, b)
	}
	if f := findJSON(t, sameTarget); f.Job == nil || f.Job.RepeatApply == nil || *f.Job.RepeatApply != *listed.RepeatApply {
		t.Fatalf("find differs from the list: %+v", f.Job)
	}
}

// A hostile posting title is cleaned before it is quoted in a note.
func TestSameNotesCleanUntrustedTitles(t *testing.T) {
	n := jobview.NewRepeatApplyNote("Acme\x1b[31m", 1, 60, "gh:a:1", "Evil\n\x1b[2JTitle "+strings.Repeat("x", 500), jobsT0)
	for _, s := range []string{n.Text, n.LatestTitle} {
		if strings.ContainsAny(s, "\x1b\n") {
			t.Fatalf("control characters survived: %q", s)
		}
	}
	if len([]rune(n.LatestTitle)) > 121 {
		t.Fatalf("title is %d runes", len([]rune(n.LatestTitle)))
	}
	r := jobview.NewRepostNote("gh:a:1", "", jobsT0)
	if !strings.Contains(r.Text, "(untitled)") {
		t.Fatalf("empty title: %q", r.Text)
	}
}

// With the company-history index built once per run, the insight pass over n
// postings costs a bounded number of allocations each, and 4x the postings costs
// about 4x: a per-row scan of the ledger would be 16x.
func TestSameCompanyIndexScalesLinearly(t *testing.T) {
	if testing.Short() {
		t.Skip("scale check")
	}
	v, err := jobinsight.Default()
	if err != nil {
		t.Fatal(err)
	}
	build := func(n int) ledger.Snapshot {
		var snap ledger.Snapshot
		for i := range n {
			k := posting.Key(fmt.Sprintf("gh:co%02d:%05d", i%40, i))
			snap.Records = append(snap.Records, ledger.Record{
				Key: k, Status: posting.Digested, FirstSeen: jobsT0.Add(-time.Duration(i%120) * 24 * time.Hour),
				Posting: posting.Posting{Key: k, Company: fmt.Sprintf("Company %02d", i%40), Title: fmt.Sprintf("Engineer %d", i%5),
					Location: "Austin, TX", Description: "Build things."},
			})
		}
		return snap
	}
	pass := func(snap ledger.Snapshot) (flagged int, mallocs uint64) {
		in := &insight{vocab: v, now: jobsT0, halfLife: jobinsight.DefaultScoreHalfLifeDays}
		in.same = newSameCompany(config.Config{}, nil, snap, nil, jobsT0)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for _, r := range snap.Records {
			j := jobview.ToJob(r, marks.Entry{})
			in.annotateSame(&j, r)
			if j.Repost != nil {
				flagged++
			}
		}
		runtime.ReadMemStats(&after)
		return flagged, after.Mallocs - before.Mallocs
	}
	pass(build(50))
	const small, large = 2000, 8000
	fSmall, aSmall := pass(build(small))
	fLarge, aLarge := pass(build(large))
	if fSmall == 0 || fLarge == 0 {
		t.Fatalf("nothing flagged (%d, %d): the pass measured nothing", fSmall, fLarge)
	}
	if aLarge > aSmall*5 {
		t.Fatalf("4x the postings made %d allocations against %d: not linear", aLarge, aSmall)
	}
}
