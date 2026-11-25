package dice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/mcphttp"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// TestCapturedReplies lists the live search capture of 2026-11-24 (three rows,
// its counts trimmed to them) with the live detail capture answering each row.
func TestCapturedReplies(t *testing.T) {
	f := newFake(t, nil)
	f.search = func(map[string]any) (answer, bool) { return answer{capture: "search.sse"}, true }
	f.detail = func(string) (answer, bool) { return answer{capture: "detail.sse"}, true }
	got, err := src(f.doer(), nil).List(context.Background(), diceBoard("machine learning engineer"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d postings, want 3", len(got))
	}
	p := got[0]
	const guid = "46f0d513-ff59-4049-b4ab-9b45d8002fbe"
	want := posting.Posting{
		Key: "dc:dice:" + guid, ATS: posting.Dice, Company: "DCM Infotech Limited", Title: "Wireless Software Engineer",
		Location: "Austin, Texas, USA", URL: "https://www.dice.com/job-detail/" + guid, ApplyURL: "https://www.dice.com/job-detail/" + guid,
		PostedAt:  time.Date(2026, 11, 17, 16, 48, 52, 0, time.UTC),
		UpdatedAt: time.Date(2026, 11, 24, 23, 17, 35, 0, time.UTC),
		SeenAt:    fixedNow,
	}
	desc := p.Description
	p.Description = ""
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("posting =\n%+v\nwant\n%+v", p, want)
	}
	// "Depends on Experience" is no range: no pay line.
	if !strings.HasPrefix(desc, "Role: contract, on-site\n\nTitle: Wireless Software Engineer") {
		t.Fatalf("description starts %q", desc[:min(len(desc), 120)])
	}
	if !strings.Contains(desc, "\n\nSkills\n4G LTE, 5G NR, 5G-Advanced technologies, C/C++, Linux, PHY, Python, software development, wireless communication") {
		t.Fatalf("description has no sorted skills section: %q", desc[max(0, len(desc)-300):])
	}
	if strings.Contains(desc, "<") || strings.Contains(desc, "&bull;") {
		t.Fatal("description kept HTML")
	}
	if got[1].Description[:len("Role: contract, third party, on-site\n\n")] != "Role: contract, third party, on-site\n\n" {
		t.Fatalf("second row's role line: %q", got[1].Description[:60])
	}
	if !strings.HasPrefix(got[2].Description, "Pay range: USD 105050 – 161800 per year\nRole: full-time, on-site\n\n") {
		t.Fatalf("third row's lead: %q", got[2].Description[:80])
	}
	for _, p := range got {
		if strings.Contains(p.URL, "utm_") || strings.Contains(p.ApplyURL, "?") {
			t.Fatalf("URL kept the query: %s %s", p.URL, p.ApplyURL)
		}
	}
	// The detail asked for is the row's guid, never its id.
	if ids := f.detailIDs(); ids[0] != guid {
		t.Fatalf("detail ids = %v", ids)
	}
}

func TestSearchArguments(t *testing.T) {
	f := newFake(t, map[string][]string{"golang": guids(0, 2)})
	if _, err := src(f.doer(), nil).List(context.Background(), diceBoard("golang")); err != nil {
		t.Fatal(err)
	}
	calls := f.toolCalls()
	want := map[string]any{"keyword": "golang", "jobs_per_page": float64(100), "page_number": float64(1), "sort": "datePosted"}
	if calls[0].Name != "search_jobs" || !reflect.DeepEqual(calls[0].Args, want) {
		t.Fatalf("first call = %+v", calls[0])
	}
}

func TestPagingToTotalPagesNotPast(t *testing.T) {
	f := newFake(t, map[string][]string{"ml": guids(0, 250)})
	var st Stats
	s := src(f.doer(), &Options{Report: func(_ sources.Board, x Stats) { st = x }})
	got, err := s.List(context.Background(), diceBoard("ml"))
	if err != nil {
		t.Fatal(err)
	}
	if pages := f.pagesAsked("ml"); !reflect.DeepEqual(pages, []int{1, 2, 3}) {
		t.Fatalf("pages asked = %v, want 1..3 and never past totalPages", pages)
	}
	// 250 new rows, 120 details: the rest deferred, not returned.
	if len(got) != MaxDetails || st.Listed != 250 || st.Fetched != MaxDetails || st.Deferred != 130 || st.Requests != 3 || st.Matched != 250 {
		t.Fatalf("got %d postings, stats %+v", len(got), st)
	}
	if f.count("get_job_details") != MaxDetails {
		t.Fatalf("detail calls = %d", f.count("get_job_details"))
	}
	if st.Windowed {
		t.Fatal("a board without posted_within_days reported a windowed listing")
	}
}

func TestStopsAtTotalPagesShortOfTotal(t *testing.T) {
	// The server counts 250 jobs but says the search has 2 pages: the walk
	// stops there, and 50 missing is more than it may be short.
	f := newFake(t, map[string][]string{"ml": guids(0, 250)})
	f.pages = func(string, int) int { return 2 }
	_, err := src(f.doer(), &Options{}).List(context.Background(), diceBoard("ml"))
	if !errors.Is(err, errIncomplete) {
		t.Fatalf("err = %v, want errIncomplete", err)
	}
	if pages := f.pagesAsked("ml"); !reflect.DeepEqual(pages, []int{1, 2}) {
		t.Fatalf("pages asked = %v", pages)
	}
	// One short of the count is a posting closing mid-walk, and is kept.
	f2 := newFake(t, map[string][]string{"ml": guids(0, 199)})
	f2.total = func(string, int) int { return 200 }
	f2.pages = func(string, int) int { return 2 }
	got, err := src(f2.doer(), &Options{}).List(context.Background(), diceBoard("ml"))
	if err != nil || len(got) != MaxDetails {
		t.Fatalf("got %d postings, %v", len(got), err)
	}
}

func TestOverCapFailsBeforePage2(t *testing.T) {
	f := newFake(t, map[string][]string{"engineer": guids(0, 150)})
	f.total = func(string, int) int { return MaxPostings + 1 }
	reported := false
	_, err := src(f.doer(), &Options{Report: func(sources.Board, Stats) { reported = true }}).List(context.Background(), diceBoard("engineer"))
	if !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("err = %v, want ErrTooManyPostings", err)
	}
	for _, w := range []string{"location", "workplace_type", "posted_within_days"} {
		if !strings.Contains(err.Error(), w) {
			t.Fatalf("error %q does not say to narrow with %s", err, w)
		}
	}
	if calls := f.toolCalls(); len(calls) != 1 {
		t.Fatalf("calls = %+v, want page 1 only", calls)
	}
	if reported {
		t.Fatal("a board that failed while listing reported stats")
	}
}

func TestMultiTermUnion(t *testing.T) {
	f := newFake(t, map[string][]string{"go": guids(0, 10), "rust": guids(5, 15)})
	var st Stats
	got, err := src(f.doer(), &Options{Report: func(_ sources.Board, x Stats) { st = x }}).List(context.Background(), diceBoard("go", "rust", "GO"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 15 || st.Listed != 15 || st.Matched != 20 || st.Requests != 2 || st.Fetched != 15 {
		t.Fatalf("got %d postings, stats %+v", len(got), st)
	}
	seen := map[posting.Key]bool{}
	for _, p := range got {
		if seen[p.Key] {
			t.Fatalf("posting %s twice", p.Key)
		}
		seen[p.Key] = true
	}
	if f.count("get_job_details") != 15 {
		t.Fatalf("a job in both terms was read twice: %d detail calls", f.count("get_job_details"))
	}
}

func TestUnionOverCap(t *testing.T) {
	f := newFake(t, map[string][]string{"a": guids(0, 600), "b": guids(600, 1200)})
	_, err := src(f.doer(), &Options{}).List(context.Background(), diceBoard("a", "b"))
	if !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("err = %v", err)
	}
	if f.count("get_job_details") != 0 {
		t.Fatal("details were read for a board over its cap")
	}
}

func TestDuplicateRowsAcrossPages(t *testing.T) {
	// Page 2 repeats page 1's last row (a posting arrived mid-walk) and the
	// walk still reaches the count.
	all := guids(0, 150)
	f := newFake(t, map[string][]string{"ml": all})
	f.search = func(a map[string]any) (answer, bool) {
		if a["page_number"].(float64) != 2 {
			return answer{}, false
		}
		rows := []any{rowObject(all[99])}
		for _, g := range all[100:] {
			rows = append(rows, rowObject(g))
		}
		return answer{sc: map[string]any{"data": rows, "metadata": map[string]any{"page": 2, "pageSize": 100, "total": 150, "totalPages": 2}}}, true
	}
	got, err := src(f.doer(), &Options{}).List(context.Background(), diceBoard("ml"))
	if err != nil || len(got) != MaxDetails {
		t.Fatalf("got %d, %v", len(got), err)
	}
}

func TestEmptySearch(t *testing.T) {
	f := newFake(t, map[string][]string{})
	got, err := src(f.doer(), nil).List(context.Background(), diceBoard("nothing matches"))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %d, %v", len(got), err)
	}
}

func TestRemoteRowWithNullLocation(t *testing.T) {
	g := guidOf(0)
	f := newFake(t, map[string][]string{"ml": {g}})
	f.edit = func(_ string, row map[string]any) {
		row["jobLocation"] = nil
		row["workplaceTypes"] = []any{"Remote"}
		row["isRemote"] = true
		row["willingToSponsor"] = true
		row["salary"] = nil
		row["employmentType"] = "Full-time, Contract"
	}
	got, err := src(f.doer(), nil).List(context.Background(), diceBoard("ml"))
	if err != nil {
		t.Fatal(err)
	}
	p := got[0]
	if p.Location != "" || !p.Remote {
		t.Fatalf("location %q remote %v", p.Location, p.Remote)
	}
	if !strings.HasPrefix(p.Description, "Role: contract, full-time, remote, sponsorship offered\n\n") {
		t.Fatalf("description %q", p.Description)
	}
	// isRemote alone (workplaceTypes null) is remote too.
	f.edit = func(_ string, row map[string]any) {
		row["jobLocation"] = nil
		row["workplaceTypes"] = nil
		row["isRemote"] = true
	}
	got, err = src(f.doer(), nil).List(context.Background(), diceBoard("ml"))
	if err != nil || !got[0].Remote {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestRateLimitedSearch(t *testing.T) {
	f := newFake(t, map[string][]string{"ml": guids(0, 3)})
	f.search = func(map[string]any) (answer, bool) { return answer{capture: "ratelimited.sse"}, true }
	_, err := src(f.doer(), nil).List(context.Background(), diceBoard("ml"))
	var rl *srcutil.RateLimitedError
	if !errors.As(err, &rl) || !errors.Is(err, srcutil.ErrRateLimited) {
		t.Fatalf("err = %v, want a *srcutil.RateLimitedError", err)
	}
	if rl.Host != "mcp.dice.com" || rl.Method != "POST" || rl.Path != "/mcp" || rl.Attempts != 1 || !strings.Contains(rl.Why, "429") {
		t.Fatalf("rate limit = %+v", rl)
	}
	if rl.Status == nil || rl.Status.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %v", rl.Status)
	}
}

func TestRateLimitedDetail(t *testing.T) {
	f := newFake(t, map[string][]string{"ml": guids(0, 3)})
	f.detail = func(string) (answer, bool) {
		return answer{toolErr: "Error calling tool 'get_job_details': Too Many Requests"}, true
	}
	var st Stats
	n := 0
	_, err := src(f.doer(), &Options{Report: func(_ sources.Board, x Stats) { st = x; n++ }}).List(context.Background(), diceBoard("ml"))
	if !errors.Is(err, srcutil.ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
	if n != 1 || st.Listed != 3 {
		t.Fatalf("report %d times, stats %+v", n, st)
	}
	if f.count("get_job_details") != 1 {
		t.Fatalf("detail calls after a rate limit: %d", f.count("get_job_details"))
	}
}

func TestValidationToolErrorIsPlain(t *testing.T) {
	f := newFake(t, nil)
	f.search = func(map[string]any) (answer, bool) { return answer{capture: "validation.sse"}, true }
	_, err := src(f.doer(), nil).List(context.Background(), diceBoard("ml"))
	var te *mcphttp.ToolError
	if !errors.As(err, &te) || errors.Is(err, srcutil.ErrRateLimited) {
		t.Fatalf("err = %v, want a plain tool error", err)
	}
	if strings.ContainsAny(te.Text, "\n") {
		t.Fatal("tool text was not cleaned")
	}
}

func TestEndpoint404IsNotBoardNotFound(t *testing.T) {
	f := newFake(t, nil)
	f.init = &srctest.Reply{Status: http.StatusNotFound, Body: []byte("not found")}
	_, err := src(f.doer(), nil).List(context.Background(), diceBoard("ml"))
	if !errors.Is(err, mcphttp.ErrEndpointGone) || errors.Is(err, srcutil.ErrBoardNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestCanceledContext(t *testing.T) {
	f := newFake(t, map[string][]string{"ml": guids(0, 3)})
	d := f.doer()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := src(d, nil).List(ctx, diceBoard("ml"))
	if !errors.Is(err, context.Canceled) || len(d.Requests()) != 0 {
		t.Fatalf("err = %v, %d requests", err, len(d.Requests()))
	}
	// Canceled between the listing and the details.
	ctx, cancel = context.WithCancel(context.Background())
	f.search = func(map[string]any) (answer, bool) { cancel(); return answer{}, false }
	if _, err := src(f.doer(), &Options{}).List(ctx, diceBoard("ml")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestSearchTimeoutFailsBoard(t *testing.T) {
	f := newFake(t, nil)
	f.search = func(map[string]any) (answer, bool) { return answer{raw: &srctest.Reply{Err: timeoutErr{}}}, true }
	if got, err := src(f.doer(), &Options{}).List(context.Background(), diceBoard("ml")); err == nil {
		t.Fatalf("a search that timed out listed %d postings", len(got))
	}
}

func TestMalformedSearchReplies(t *testing.T) {
	ok := guids(0, 3)
	rows := func(gs []string) []any {
		var out []any
		for _, g := range gs {
			out = append(out, rowObject(g))
		}
		return out
	}
	meta := func(page, total, pages int) map[string]any {
		return map[string]any{"page": page, "pageSize": 100, "total": total, "totalPages": pages}
	}
	for name, sc := range map[string]any{
		"no metadata":        map[string]any{"data": rows(ok)},
		"no data":            map[string]any{"metadata": meta(1, 3, 1)},
		"null data":          map[string]any{"data": nil, "metadata": meta(1, 3, 1)},
		"no total":           map[string]any{"data": rows(ok), "metadata": map[string]any{"page": 1, "pageSize": 100, "totalPages": 1}},
		"wrong page echoed":  map[string]any{"data": rows(ok), "metadata": meta(2, 3, 1)},
		"other page size":    map[string]any{"data": rows(ok), "metadata": map[string]any{"page": 1, "pageSize": 5, "total": 3, "totalPages": 1}},
		"total below rows":   map[string]any{"data": rows(ok), "metadata": meta(1, 2, 1)},
		"negative total":     map[string]any{"data": rows(ok), "metadata": meta(1, -1, 1)},
		"empty page counted": map[string]any{"data": []any{}, "metadata": meta(1, 3, 1)},
		"too many rows":      map[string]any{"data": rows(guids(0, 101)), "metadata": meta(1, 101, 2)},
		"data not a list":    map[string]any{"data": "rows", "metadata": meta(1, 3, 1)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, nil)
			f.search = func(map[string]any) (answer, bool) { return answer{sc: sc}, true }
			if got, err := src(f.doer(), &Options{}).List(context.Background(), diceBoard("ml")); err == nil {
				t.Fatalf("listed %d postings", len(got))
			}
		})
	}
	t.Run("page adds nothing", func(t *testing.T) {
		f := newFake(t, nil)
		f.search = func(a map[string]any) (answer, bool) {
			// Every page serves page 1's rows again.
			return answer{sc: map[string]any{"data": rows(guids(0, 100)), "metadata": meta(int(a["page_number"].(float64)), 300, 3)}}, true
		}
		_, err := src(f.doer(), &Options{}).List(context.Background(), diceBoard("ml"))
		if !errors.Is(err, ErrShape) || f.count("search_jobs") != 2 {
			t.Fatalf("err = %v after %d calls", err, f.count("search_jobs"))
		}
	})
	t.Run("oversized reply", func(t *testing.T) {
		f := newFake(t, nil)
		big := srctest.Reply{Status: 200, Body: []byte("event: message\ndata: {\"x\":\"" + strings.Repeat("a", mcphttp.MaxReply) + "\"}\n\n"),
			Header: http.Header{"Content-Type": {"text/event-stream"}}}
		f.search = func(map[string]any) (answer, bool) { return answer{raw: &big}, true }
		if _, err := src(f.doer(), &Options{}).List(context.Background(), diceBoard("ml")); !errors.Is(err, srcutil.ErrBodyTooLarge) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("not json", func(t *testing.T) {
		f := newFake(t, nil)
		bad := srctest.Reply{Status: 200, Body: []byte("<html>gateway</html>"), Header: http.Header{"Content-Type": {"text/html"}}}
		f.search = func(map[string]any) (answer, bool) { return answer{raw: &bad}, true }
		if _, err := src(f.doer(), &Options{}).List(context.Background(), diceBoard("ml")); !errors.Is(err, mcphttp.ErrProtocol) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestBoardChecks(t *testing.T) {
	for name, b := range map[string]sources.Board{
		"other token": {ATS: posting.Dice, Token: "dice2", Search: "ml"},
		"other ats":   {ATS: posting.Himalayas, Token: Token, Search: "ml"},
		"no search":   {ATS: posting.Dice, Token: Token},
		"host":        {ATS: posting.Dice, Token: Token, Search: "ml", Host: "mcp.dice.com"},
		"max":         {ATS: posting.Dice, Token: Token, Search: "ml", MaxPostings: 50},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, nil)
			d := f.doer()
			if _, err := src(d, nil).List(context.Background(), b); err == nil || len(d.Requests()) != 0 {
				t.Fatalf("err = %v after %d requests", err, len(d.Requests()))
			}
		})
	}
}

func TestNewWithNeedsStored(t *testing.T) {
	if _, err := NewWith(srctest.NewFunc(nil), Options{}); err == nil {
		t.Fatal("NewWith accepted no Stored")
	}
	s, err := NewWith(srctest.NewFunc(nil), Options{Stored: func(sources.Board) []posting.Posting { return nil }})
	if err != nil || s.ATS() != posting.Dice || New(nil).ATS() != posting.Dice {
		t.Fatalf("%v", err)
	}
}

func TestPacing(t *testing.T) {
	f := newFake(t, map[string][]string{"a": guids(0, 150), "b": guids(140, 145)})
	var waits []time.Duration
	wait := func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return ctx.Err() }
	if _, err := src(f.doer(), &Options{Wait: wait}).List(context.Background(), diceBoard("a", "b")); err != nil {
		t.Fatal(err)
	}
	calls := len(f.toolCalls())
	if calls != 3+MaxDetails || len(waits) != calls-1 {
		t.Fatalf("%d calls, %d waits; want a wait before every call but the first", calls, len(waits))
	}
	for _, w := range waits {
		if w < time.Second {
			t.Fatalf("wait %s, want at least a second", w)
		}
	}
}

func TestDeterministicAcrossRuns(t *testing.T) {
	f := newFake(t, map[string][]string{"ml": guids(0, 5)})
	f.edit = func(_ string, row map[string]any) {
		row["workplaceTypes"] = []any{"Remote", "Hybrid"}
		row["employmentType"] = "Third Party, Contract"
	}
	f.detail = func(guid string) (answer, bool) {
		d := detailObject(guid)
		d["skills"] = []any{map[string]any{"name": "zeta"}, map[string]any{"name": "Alpha"}, map[string]any{"name": "alpha"}}
		return answer{sc: d}, true
	}
	first, err := src(f.doer(), nil).List(context.Background(), diceBoard("ml"))
	if err != nil {
		t.Fatal(err)
	}
	// A day later, Dice lists the same rows with their sets in another order.
	f.edit = func(_ string, row map[string]any) {
		row["workplaceTypes"] = []any{"Hybrid", "Remote"}
		row["employmentType"] = "Contract, Third Party"
	}
	f.detail = func(guid string) (answer, bool) {
		d := detailObject(guid)
		d["skills"] = []any{map[string]any{"name": "alpha"}, map[string]any{"name": "zeta"}, map[string]any{"name": "Alpha"}}
		return answer{sc: d}, true
	}
	s := src(f.doer(), nil)
	s.now = func() time.Time { return fixedNow.Add(36 * time.Hour) }
	second, err := s.List(context.Background(), diceBoard("ml"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range first {
		a, b := first[i], second[i]
		a.SeenAt, b.SeenAt = time.Time{}, time.Time{}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("run 1:\n%+v\nrun 2:\n%+v", a, b)
		}
	}
	if !strings.HasPrefix(first[0].Description, "Pay range: USD 120000 – 150000\nRole: contract, third party, hybrid, remote\n\n") {
		t.Fatalf("description %q", first[0].Description)
	}
	if !strings.HasSuffix(first[0].Description, "Skills\nAlpha, zeta") {
		t.Fatalf("description %q", first[0].Description)
	}
}

func TestToolArgumentsAreJSON(t *testing.T) {
	// The detail call names the guid as job_id and nothing else.
	f := newFake(t, map[string][]string{"ml": guids(0, 1)})
	if _, err := src(f.doer(), nil).List(context.Background(), diceBoard("ml")); err != nil {
		t.Fatal(err)
	}
	c := f.toolCalls()[1]
	raw, _ := json.Marshal(c.Args)
	if c.Name != "get_job_details" || string(raw) != `{"job_id":"`+guidOf(0)+`"}` {
		t.Fatalf("detail call %s %s", c.Name, raw)
	}
}

// A later page that counts fewer postings than page 1 promised, or none, does
// not end the walk as if the listing were whole: the rows page 1 counted and
// no page served are a truncated listing, and the board fails rather than
// aging them.
func TestALaterPageCannotShrinkTheListing(t *testing.T) {
	f := newFake(t, map[string][]string{"ml": guids(0, 250)})
	f.search = func(args map[string]any) (answer, bool) {
		if int(args["page_number"].(float64)) < 2 {
			return answer{}, false
		}
		return answer{sc: map[string]any{
			"data":     []any{},
			"metadata": map[string]any{"page": 2, "pageSize": 100, "total": 0, "totalPages": 0},
		}}, true
	}
	got, err := src(f.doer(), &Options{}).List(context.Background(), diceBoard("ml"))
	if err == nil {
		t.Fatalf("got %d postings and no error; a page counting 0 after page 1's 250 ended the walk", len(got))
	}
}
