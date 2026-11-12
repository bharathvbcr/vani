package freehire

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func titled(source, id, rawURL, company, title string) Job {
	j := job(source, id, rawURL, company)
	j.Title = title
	return j
}

// merge is Merge with no evidence function: every board's Evidence is zero.
func merge(hits []Hit) Merged { return Merge(hits, nil) }

// Within one search count, a board with more evidence outranks a board with
// more postings; the evidence function sees each result under the search that
// found it, and a board keeps its best.
func TestMergeRanksEvidenceBeforePostings(t *testing.T) {
	big := []Job{
		titled("greenhouse", "big:1", "https://boards.greenhouse.io/big/jobs/1", "Big", "Agent d'accueil"),
		titled("greenhouse", "big:2", "https://boards.greenhouse.io/big/jobs/2", "Big", "Agent de sécurité"),
		titled("greenhouse", "big:3", "https://boards.greenhouse.io/big/jobs/3", "Big", "Agent polyvalent"),
	}
	small := titled("greenhouse", "small:1", "https://boards.greenhouse.io/small/jobs/1", "Small", "Agent Inference Engineer")
	var calls []string
	ev := func(search string, j Job) int {
		calls = append(calls, search+"|"+j.Title)
		if strings.Contains(j.Title, "Inference") {
			return 1
		}
		return 0
	}
	m := Merge([]Hit{{Search: "Agent", Jobs: append(big, small)}}, ev)
	if len(m.Boards) != 2 || m.Boards[0].Board.Token != "small" || m.Boards[0].Evidence != 1 || m.Boards[1].Evidence != 0 {
		t.Errorf("boards %+v", m.Boards)
	}
	if !slices.Contains(calls, "Agent|Agent Inference Engineer") || len(calls) != 4 {
		t.Errorf("evidence asked %v", calls)
	}
	// Without evidence, postings decide.
	if m := merge([]Hit{{Search: "Agent", Jobs: append(big, small)}}); m.Boards[0].Board.Token != "big" {
		t.Errorf("no evidence: first %+v", m.Boards[0])
	}
}

func mergedIn(m Merged, ats posting.ATS, token string) (MergedBoard, bool) {
	for _, b := range m.Boards {
		if b.Board.ATS == ats && strings.EqualFold(b.Board.Token, token) {
			return b, true
		}
	}
	return MergedBoard{}, false
}

func TestMergeCountsAResultOnceAndCreditsEverySearch(t *testing.T) {
	acme1 := titled("greenhouse", "acme:1", "https://boards.greenhouse.io/acme/jobs/1", "Acme", "Inference Engineer")
	acme2 := titled("greenhouse", "acme:2", "https://boards.greenhouse.io/acme/jobs/2", "Acme", "LLM Engineer")
	beta := titled("lever", "b1", "https://jobs.lever.co/beta/b1", "Beta", "ML Inference Engineer")
	gamma1 := titled("greenhouse", "gamma:1", "https://boards.greenhouse.io/gamma/jobs/1", "Gamma", "Rust Engineer")
	gamma2 := titled("greenhouse", "gamma:2", "https://boards.greenhouse.io/gamma/jobs/2", "Gamma", "Rust Engineer")
	gamma3 := titled("greenhouse", "gamma:3", "https://boards.greenhouse.io/gamma/jobs/3", "Gamma", "Systems Engineer")
	wd := titled("workday", "w1", "https://r1rcm.wd1.myworkdayjobs.com/en-US/R1RCM/job/X/Inference-Engineer_R1", "R1", "Inference Engineer")
	jazz := titled("jazzhr", "j1", "https://acme.applytojob.com/apply/j1", "Jazz", "Inference Engineer")

	m := merge([]Hit{
		{Search: "Inference", Jobs: []Job{acme1, beta, wd, jazz}},
		{Search: "Rust", Jobs: []Job{acme1, gamma1, gamma2, gamma3, wd, jazz}},
		{Search: "LLM", Jobs: []Job{acme2}},
	})
	// acme1, wd and jazz were each found twice.
	if m.Repeated != 3 || m.Distinct != 8 {
		t.Errorf("distinct %d repeated %d, want 8 and 3", m.Distinct, m.Repeated)
	}
	acme, ok := mergedIn(m, posting.Greenhouse, "acme")
	if !ok || acme.Postings != 2 || !slices.Equal(acme.Searches, []string{"Inference", "Rust", "LLM"}) {
		t.Errorf("acme %+v", acme)
	}
	if !slices.Equal(acme.Titles, []string{"Inference Engineer", "LLM Engineer"}) || acme.Board.Company != "Acme" {
		t.Errorf("acme titles %v company %q", acme.Titles, acme.Board.Company)
	}
	// Most searches first: acme (3), then the Workday board (2), then gamma
	// (1 search, 3 postings) before beta (1 search, 1 posting).
	var order []string
	for _, b := range m.Boards {
		order = append(order, b.Board.Token)
	}
	if want := []string{"acme", "r1rcm/R1RCM", "gamma", "beta"}; !slices.Equal(order, want) {
		t.Errorf("order %v, want %v", order, want)
	}
	gamma, _ := mergedIn(m, posting.Greenhouse, "gamma")
	if gamma.Postings != 3 || len(gamma.Titles) != 2 {
		t.Errorf("gamma %+v (titles are distinct)", gamma)
	}
	// A Workday board carries every search that found it, so it lists them.
	w, ok := mergedIn(m, posting.Workday, "r1rcm/R1RCM")
	if !ok || w.Board.Search != "" || !slices.Equal(w.Board.Searches, []string{"Inference", "Rust"}) || w.Postings != 1 {
		t.Errorf("workday %+v", w)
	}
	// A greenhouse board takes no search.
	if acme.Board.Search != "" || acme.Board.Searches != nil {
		t.Errorf("greenhouse board carries searches: %+v", acme.Board)
	}
	// Every distinct result is on a board or in an unreadable group.
	total := 0
	for _, b := range m.Boards {
		total += b.Postings
	}
	for _, u := range m.Unreadable {
		total += u.Count
	}
	if total != m.Distinct || !slices.Contains(m.Unreadable, Unreadable{Source: "jazzhr", Reason: ReasonNoAdapter, Count: 1}) {
		t.Errorf("accounted %d of %d; unreadable %+v", total, m.Distinct, m.Unreadable)
	}
}

// One search on a Workday board is its Search; more than sources.MaxSearches
// are cut to the first ones, and the board stays valid.
func TestMergeWorkdaySearchBounds(t *testing.T) {
	wd := titled("workday", "w1", "https://r1rcm.wd1.myworkdayjobs.com/en-US/R1RCM/job/X/Y_R1", "R1", "Inference Engineer")
	m := merge([]Hit{{Search: "Inference", Jobs: []Job{wd}}})
	if len(m.Boards) != 1 || m.Boards[0].Board.Search != "Inference" || m.Boards[0].Board.Searches != nil {
		t.Fatalf("one search: %+v", m.Boards)
	}
	var hits []Hit
	for i := range sources.MaxSearches + 4 {
		hits = append(hits, Hit{Search: "term " + string(rune('a'+i)), Jobs: []Job{wd}})
	}
	m = merge(hits)
	b := m.Boards[0]
	if len(b.Searches) != sources.MaxSearches+4 || len(b.Board.Searches) != sources.MaxSearches || b.Board.Searches[0] != "term a" {
		t.Errorf("%d found-by, %d board searches: %v", len(b.Searches), len(b.Board.Searches), b.Board.Searches)
	}
	// A search a board may not carry (over sources.MaxSearchLen) leaves the
	// board without searches rather than refused.
	m = merge([]Hit{{Search: strings.Repeat("x", sources.MaxSearchLen+1), Jobs: []Job{wd}}})
	if len(m.Boards) != 1 || m.Boards[0].Board.Search != "" || len(m.Unreadable) != 0 {
		t.Errorf("long search: boards %+v unreadable %+v", m.Boards, m.Unreadable)
	}
}

func TestMergeOfNothing(t *testing.T) {
	for _, hits := range [][]Hit{nil, {}, {{Search: "x"}}, {{Search: "x", Jobs: []Job{}}}} {
		if m := merge(hits); len(m.Boards) != 0 || len(m.Unreadable) != 0 || m.Distinct != 0 || m.Repeated != 0 {
			t.Errorf("merge(%v) = %+v", hits, m)
		}
	}
}

// A result's title is third-party text: it is cleaned like the company name,
// so escape sequences, bidirectional overrides and newlines never reach a
// terminal.
func TestValidJobCleansTheTitle(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{
		"source": "greenhouse", "external_id": "acme:1", "url": "https://boards.greenhouse.io/acme/jobs/1",
		"company": "Acme", "title": "\x1b[31mInference‮\nEngineer​ " + strings.Repeat("y", 500),
	})
	j, ok := validJob(raw)
	if !ok {
		t.Fatal("refused")
	}
	if strings.ContainsAny(j.Title, "\x1b‮\n​") || len([]rune(j.Title)) > maxName || !strings.HasPrefix(j.Title, "[31mInference Engineer") {
		t.Errorf("title %q", j.Title)
	}
	raw, _ = json.Marshal(map[string]string{"source": "greenhouse", "external_id": "acme:1", "url": "https://boards.greenhouse.io/acme/jobs/1"})
	if j, ok := validJob(raw); !ok || j.Title != "" {
		t.Errorf("no title: %+v %v", j, ok)
	}
}
