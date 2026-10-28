package smartrecruiters

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func goneSet(a *api, k int) {
	a.bad = map[string]srctest.Reply{}
	for i := 0; i < k; i++ {
		a.bad[idOf(i)] = notFound()
	}
}

// reports collects what a source's Report was given.
type reports struct {
	mu    sync.Mutex
	calls []Stats
}

func (r *reports) add(_ sources.Board, st Stats) {
	r.mu.Lock()
	r.calls = append(r.calls, st)
	r.mu.Unlock()
}

func TestGoneDetailsAreLeftOutUnderTheBound(t *testing.T) {
	for _, n := range []int{1, 2, 9, 10, 11, 19, 20, 21, 100} {
		bound := max(1, n/10)
		for _, k := range []int{1, bound, bound + 1, n} {
			if k > n || k < 1 {
				continue
			}
			a := board(n)
			goneSet(a, k)
			rep := &reports{}
			got, err := src(t, a.doer(), &Options{Report: rep.add}).List(context.Background(), srBoard())
			wantFail := k > bound || k == n
			if wantFail {
				if err == nil {
					t.Errorf("n=%d gone=%d: %d postings, want a failure", n, k, len(got))
				} else if errors.Is(err, srcutil.ErrBoardNotFound) {
					t.Errorf("n=%d gone=%d: detail 404s read as board-not-found: %v", n, k, err)
				}
			} else if err != nil || len(got) != n-k {
				t.Errorf("n=%d gone=%d: %d postings, %v; want %d, nil", n, k, len(got), err, n-k)
			}
			if len(rep.calls) != 1 || rep.calls[0].Listed != n {
				t.Errorf("n=%d gone=%d: reports %+v, want one with listed=%d", n, k, rep.calls, n)
			} else if !wantFail && rep.calls[0].Gone != k {
				t.Errorf("n=%d gone=%d: report gone = %d", n, k, rep.calls[0].Gone)
			}
		}
	}
}

func TestGoneStoredRowIsLeftOutSoItAges(t *testing.T) {
	// A stored posting whose re-read answers 404 closed: it must not be returned.
	a := board(20)
	a.bad = map[string]srctest.Reply{idOf(3): notFound()}
	stored := []posting.Posting{storedPosting(idOf(3), stamp)}
	s := src(t, a.doer(), &Options{Stored: func(sources.Board) []posting.Posting { return stored }})
	// Make id 3 due for a re-read: its stamp differs from the stored one.
	stored[0].PostedAt = stored[0].PostedAt.AddDate(0, 0, -1)
	got, err := s.List(context.Background(), srBoard())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if p.Key == stored[0].Key {
			t.Fatal("a posting whose detail is gone was returned")
		}
	}
	if len(got) != 19 {
		t.Fatalf("%d postings, want 19", len(got))
	}
}

func TestOtherDetailFailuresAreNeverGone(t *testing.T) {
	for name, rep := range map[string]srctest.Reply{
		"410":       {Status: 410, Body: []byte("{}")},
		"403":       {Status: 403, Body: []byte("{}")},
		"429":       {Status: 429, Header: http.Header{"Retry-After": {"0"}}},
		"500":       {Status: 500, Body: []byte("{}")},
		"malformed": okReply("{not json <html>"),
		"oversized": okReply(strings.Repeat("x", detailMaxBody+1)),
		"htmlpage":  okReply("<html><body>404 Page Not Found</body></html>"),
		"null":      okReply("null"),
		"empty":     okReply(""),
	} {
		a := board(20)
		a.bad = map[string]srctest.Reply{idOf(5): rep}
		reps := &reports{}
		got, err := src(t, a.doer(), &Options{Report: reps.add}).List(context.Background(), srBoard())
		if err == nil {
			t.Errorf("%s: %d postings, want the board to fail", name, len(got))
			continue
		}
		if errors.Is(err, srcutil.ErrBoardNotFound) || strings.Contains(err.Error(), "are gone") {
			t.Errorf("%s: read as gone/not-found: %v", name, err)
		}
		if len(reps.calls) != 1 || reps.calls[0].Gone != 0 {
			t.Errorf("%s: reports %+v, want one with no gone", name, reps.calls)
		}
	}
}

func TestDetailThatIsAnotherPostingFails(t *testing.T) {
	a := board(3)
	a.detail = func(id string, d map[string]any) {
		if id == idOf(1) {
			d["id"] = idOf(2)
		}
	}
	_, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
	if !errors.Is(err, ErrShape) {
		t.Fatalf("err = %v, want ErrShape", err)
	}
}

func TestDetailWithNoNameFallsBackToTheRowAndEmptyBothFails(t *testing.T) {
	a := board(1)
	a.detail = func(_ string, d map[string]any) { d["name"] = "  " }
	got, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
	if err != nil || len(got) != 1 || got[0].Title != "Role "+idOf(0) {
		t.Fatalf("%v, %v: the listing's title stands in for a blank one", got, err)
	}
	a = board(0)
	a.list = func(string, int) (srctest.Reply, bool) {
		return okReply(strings.Replace(page(1, []string{idOf(0)}), `"name":"Role `+idOf(0)+`"`, `"name":""`, 1)), true
	}
	a.detail = func(_ string, d map[string]any) { d["name"] = "" }
	if _, err := src(t, a.doer(), nil).List(context.Background(), srBoard()); !errors.Is(err, ErrShape) {
		t.Fatalf("err = %v, want ErrShape for a posting with no title anywhere", err)
	}
}

func TestPostingURLsMustBeSmartRecruitersOwnPages(t *testing.T) {
	good := "https://jobs.smartrecruiters.com/" + company + "/" + idOf(0) + "-role"
	for name, mut := range map[string]func(d map[string]any){
		"other host": func(d map[string]any) { d["postingUrl"] = "https://evil.example/" + company + "/" + idOf(0) },
		"careers host": func(d map[string]any) {
			d["postingUrl"] = "https://careers.smartrecruiters.com/" + company + "/" + idOf(0)
		},
		"userinfo": func(d map[string]any) {
			d["postingUrl"] = "https://jobs.smartrecruiters.com@evil.example/" + company + "/" + idOf(0)
		},
		"http":          func(d map[string]any) { d["postingUrl"] = "http://jobs.smartrecruiters.com/" + company + "/" + idOf(0) },
		"javascript":    func(d map[string]any) { d["postingUrl"] = "javascript:alert(1)" },
		"other company": func(d map[string]any) { d["postingUrl"] = "https://jobs.smartrecruiters.com/Other/" + idOf(0) },
		"other posting": func(d map[string]any) {
			d["postingUrl"] = "https://jobs.smartrecruiters.com/" + company + "/" + idOf(1)
		},
		"id prefix only": func(d map[string]any) { d["postingUrl"] = good[:len(good)-5] + "9-role" },
		"extra segment":  func(d map[string]any) { d["postingUrl"] = good + "/x" },
		"no path":        func(d map[string]any) { d["postingUrl"] = "https://jobs.smartrecruiters.com/" },
		"empty":          func(d map[string]any) { d["postingUrl"] = "" },
		"missing":        func(d map[string]any) { delete(d, "postingUrl") },
		"port": func(d map[string]any) {
			d["postingUrl"] = "https://jobs.smartrecruiters.com:8443/" + company + "/" + idOf(0)
		},
		"apply other host": func(d map[string]any) { d["applyUrl"] = "https://evil.example/apply" },
		"apply javascript": func(d map[string]any) { d["applyUrl"] = "javascript:alert(1)" },
		"apply other post": func(d map[string]any) {
			d["applyUrl"] = "https://jobs.smartrecruiters.com/" + company + "/" + idOf(1) + "?oga=true"
		},
	} {
		a := board(1)
		a.detail = func(_ string, d map[string]any) { mut(d) }
		got, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
		if err == nil {
			t.Errorf("%s: %d postings with URL %q, want an error", name, len(got), got[0].URL)
		}
	}
	// The company in the URL is the detail's own identifier, in any case.
	a := board(1)
	a.detail = func(id string, d map[string]any) {
		d["company"] = map[string]any{"identifier": "ServiceNow-EU", "name": "ServiceNow"}
		d["postingUrl"] = "https://jobs.smartrecruiters.com/servicenow-eu/" + id
		d["applyUrl"] = "https://jobs.smartrecruiters.com/servicenow-eu/" + id + "?oga=true"
	}
	if got, err := src(t, a.doer(), nil).List(context.Background(), srBoard()); err != nil || len(got) != 1 {
		t.Fatalf("a company identifier that differs from the token: %v, %v", got, err)
	}
}

func TestAnAbsentApplyURLIsLeftEmpty(t *testing.T) {
	a := board(1)
	a.detail = func(_ string, d map[string]any) { delete(d, "applyUrl") }
	got, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
	if err != nil || got[0].ApplyURL != "" {
		t.Fatalf("%v, %v", got, err)
	}
}

func TestDetailRequestsGoToTheListedIDOnly(t *testing.T) {
	a := board(3)
	d := a.doer()
	if _, err := src(t, d, nil).List(context.Background(), srBoard()); err != nil {
		t.Fatal(err)
	}
	for _, r := range d.Requests() {
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL, "https://api.smartrecruiters.com/v1/companies/ServiceNow/postings") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
	}
	want := []string{idOf(0), idOf(1), idOf(2)}
	if got := a.fetchedIDs(); !slices.Equal(got, want) {
		t.Fatalf("details %v, want %v", got, want)
	}
}

// ---- the bounded detail step ----

func storedPosting(id, released string) posting.Posting {
	key, _ := posting.SmartRecruitersKey(company, id)
	p := posting.Posting{
		Key: key, ATS: posting.SmartRecruiters, Company: "ServiceNow", Title: "Stored " + id,
		URL: "https://jobs.smartrecruiters.com/" + company + "/" + id, Description: "stored text",
	}
	if released != "" {
		p.PostedAt, _ = parseStamp(released)
	}
	return p
}

// idOnTurn is an id whose scheduled re-read is (want) or is not on the day of
// fixedNow, so a test can choose which it gets.
func idOnTurn(want bool) string {
	day := int(fixedNow.Unix() / 86400)
	for i := 0; ; i++ {
		if id := idOf(i); refreshTurn(id, day) == want {
			return id
		}
	}
}

func TestStoredPostingIsReusedWithoutARequest(t *testing.T) {
	id := idOnTurn(false)
	a := board(0)
	a.ids = func(string) []string { return []string{id} }
	st := storedPosting(id, stamp)
	var stats Stats
	s := src(t, a.doer(), &Options{
		Stored: func(sources.Board) []posting.Posting { return []posting.Posting{st} },
		Report: func(_ sources.Board, s Stats) { stats = s },
	})
	got, err := s.List(context.Background(), srBoard())
	if err != nil || len(got) != 1 {
		t.Fatalf("%v, %v", got, err)
	}
	if got[0].Description != "stored text" || !got[0].SeenAt.Equal(fixedNow) {
		t.Errorf("reused posting %+v: want the stored text with SeenAt now", got[0])
	}
	if len(a.fetchedIDs()) != 0 {
		t.Fatalf("a stored posting cost a detail request: %v", a.fetchedIDs())
	}
	if stats.Reused != 1 || stats.Fetched != 0 || stats.Refreshed != 0 {
		t.Fatalf("stats %+v", stats)
	}
}

func TestStoredPostingIsReReadWhenItsReleasedDateMoved(t *testing.T) {
	id := idOnTurn(false)
	a := board(0)
	a.ids = func(string) []string { return []string{id} }
	st := storedPosting(id, "2026-09-01T10:00:00.000Z") // the listing says stamp
	var stats Stats
	s := src(t, a.doer(), &Options{
		Stored: func(sources.Board) []posting.Posting { return []posting.Posting{st} },
		Report: func(_ sources.Board, s Stats) { stats = s },
	})
	got, err := s.List(context.Background(), srBoard())
	if err != nil || len(got) != 1 {
		t.Fatalf("%v, %v", got, err)
	}
	if got[0].Description == "stored text" || got[0].Title != "Role "+id {
		t.Errorf("a re-released posting was returned as stored: %+v", got[0])
	}
	if !slices.Equal(a.fetchedIDs(), []string{id}) || stats.Refreshed != 1 || stats.Reused != 0 {
		t.Fatalf("fetched %v stats %+v", a.fetchedIDs(), stats)
	}
}

func TestStoredPostingIsReReadOnItsTurn(t *testing.T) {
	id := idOnTurn(true)
	a := board(0)
	a.ids = func(string) []string { return []string{id} }
	st := storedPosting(id, stamp)
	s := src(t, a.doer(), &Options{Stored: func(sources.Board) []posting.Posting { return []posting.Posting{st} }})
	got, err := s.List(context.Background(), srBoard())
	if err != nil || len(got) != 1 || got[0].Description == "stored text" {
		t.Fatalf("%v, %v: a posting on its turn must be re-read", got, err)
	}
	if !slices.Equal(a.fetchedIDs(), []string{id}) {
		t.Fatalf("fetched %v", a.fetchedIDs())
	}
}

func TestEveryStoredPostingIsReReadWithinRefreshDays(t *testing.T) {
	// Over refreshDays consecutive days every id has exactly one turn.
	ids := []string{idOf(0), idOf(1), idOf(2), idOf(77), "12345", "999999999999999"}
	for _, id := range ids {
		turns := 0
		for day := 20000; day < 20000+refreshDays; day++ {
			if refreshTurn(id, day) {
				turns++
			}
		}
		if turns != 1 {
			t.Errorf("id %s has %d turns in %d days, want 1", id, turns, refreshDays)
		}
	}
}

func TestStoredPostingNotListedIsNotReturned(t *testing.T) {
	// A stored posting the listing no longer holds is absent: the reconciler
	// ages it. Returning it would keep a closed posting alive.
	a := board(2)
	gone := storedPosting(idOf(99), stamp)
	s := src(t, a.doer(), &Options{Stored: func(sources.Board) []posting.Posting { return []posting.Posting{gone} }})
	got, err := s.List(context.Background(), srBoard())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if p.Key == gone.Key {
			t.Fatal("a stored posting absent from the listing was returned")
		}
	}
	if len(got) != 2 {
		t.Fatalf("%d postings, want 2", len(got))
	}
}

func TestAnotherBoardsStoredPostingIsNeverReused(t *testing.T) {
	id := idOnTurn(false)
	a := board(0)
	a.ids = func(string) []string { return []string{id} }
	other := storedPosting(id, stamp)
	other.Key = posting.Key("sr:othercompany:" + id)
	s := src(t, a.doer(), &Options{Stored: func(sources.Board) []posting.Posting { return []posting.Posting{other} }})
	got, err := s.List(context.Background(), srBoard())
	if err != nil || len(got) != 1 || got[0].Description == "stored text" {
		t.Fatalf("%v, %v", got, err)
	}
	if !slices.Equal(a.fetchedIDs(), []string{id}) {
		t.Fatalf("fetched %v: a row with no stored posting of its own must be fetched", a.fetchedIDs())
	}
}

func TestNewRowsPastMaxDetailsAreDeferredNotReturned(t *testing.T) {
	n := MaxDetails + 50
	a := board(n)
	var stats Stats
	s := src(t, a.doer(), &Options{Report: func(_ sources.Board, s Stats) { stats = s }})
	got, err := s.List(context.Background(), srBoard())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxDetails || len(a.fetchedIDs()) != MaxDetails {
		t.Fatalf("%d postings, %d detail requests, want %d of each", len(got), len(a.fetchedIDs()), MaxDetails)
	}
	if stats.Listed != n || stats.Fetched != MaxDetails || stats.Deferred != 50 {
		t.Fatalf("stats %+v", stats)
	}
	// The first MaxDetails rows are the ones fetched, in listing order.
	if got[0].Key != posting.Key("sr:servicenow:"+idOf(0)) || got[MaxDetails-1].Key != posting.Key("sr:servicenow:"+idOf(MaxDetails-1)) {
		t.Fatalf("deferred the wrong rows: first %q last %q", got[0].Key, got[MaxDetails-1].Key)
	}
}

func TestStoredRowsPastTheCapAreStillReturned(t *testing.T) {
	// 350 rows, the first 200 stored (and not due): 150 new rows are fetched, none
	// deferred, and every stored row is still returned, so none ages.
	day := int(fixedNow.Unix() / 86400)
	var stored []posting.Posting
	var ids []string
	for i := 0; len(ids) < 350; i++ {
		id := idOf(i)
		if len(stored) < 200 && !refreshTurn(id, day) {
			stored = append(stored, storedPosting(id, stamp))
		}
		ids = append(ids, id)
	}
	a := board(0)
	a.ids = func(string) []string { return ids }
	s := src(t, a.doer(), &Options{Stored: func(sources.Board) []posting.Posting { return stored }})
	got, err := s.List(context.Background(), srBoard())
	if err != nil {
		t.Fatal(err)
	}
	have := map[posting.Key]bool{}
	for _, p := range got {
		have[p.Key] = true
	}
	for _, p := range stored {
		if !have[p.Key] {
			t.Fatalf("stored posting %s missing from the listing result: it would age", p.Key)
		}
	}
	if len(a.fetchedIDs()) > MaxDetails {
		t.Fatalf("%d detail requests, over %d", len(a.fetchedIDs()), MaxDetails)
	}
}

func TestScheduledReReadsYieldToNewRowsAtTheCap(t *testing.T) {
	// MaxDetails new rows fill the budget: a due stored row is reused instead.
	id := idOnTurn(true)
	ids := []string{id}
	for i := 0; len(ids) < MaxDetails+1; i++ {
		if x := idOf(1000 + i); x != id {
			ids = append(ids, x)
		}
	}
	a := board(0)
	a.ids = func(string) []string { return ids }
	st := storedPosting(id, stamp)
	var stats Stats
	s := src(t, a.doer(), &Options{
		Stored: func(sources.Board) []posting.Posting { return []posting.Posting{st} },
		Report: func(_ sources.Board, s Stats) { stats = s },
	})
	got, err := s.List(context.Background(), srBoard())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.fetchedIDs()) != MaxDetails || slices.Contains(a.fetchedIDs(), id) {
		t.Fatalf("%d details, stored row fetched=%v", len(a.fetchedIDs()), slices.Contains(a.fetchedIDs(), id))
	}
	if got[0].Key != st.Key || got[0].Description != "stored text" || stats.Reused != 1 || stats.Refreshed != 0 {
		t.Fatalf("first %+v stats %+v", got[0], stats)
	}
}

func TestNewFailsABoardThatNeedsMoreThanMaxDetails(t *testing.T) {
	a := board(MaxDetails + 1)
	d := a.doer()
	_, err := src(t, d, nil).List(context.Background(), srBoard())
	if !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("err = %v, want ErrTooManyPostings", err)
	}
	if len(a.fetchedIDs()) != 0 {
		t.Fatalf("%d details fetched before the board was refused", len(a.fetchedIDs()))
	}
	// At the limit it works.
	b := board(MaxDetails)
	if got, err := src(t, b.doer(), nil).List(context.Background(), srBoard()); err != nil || len(got) != MaxDetails {
		t.Fatalf("%d postings, %v", len(got), err)
	}
}

func TestDropListedRowsAreNeverFetchedOrReturned(t *testing.T) {
	a := board(6)
	var judged []posting.Posting
	dropOdd := func(p posting.Posting) bool {
		judged = append(judged, p)
		return strings.HasSuffix(p.Title, "1") || strings.HasSuffix(p.Title, "3") || strings.HasSuffix(p.Title, "5")
	}
	var stats Stats
	s := src(t, a.doer(), &Options{DropListed: dropOdd, Report: func(_ sources.Board, s Stats) { stats = s }})
	got, err := s.List(context.Background(), srBoard())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || len(a.fetchedIDs()) != 3 {
		t.Fatalf("%d postings, %d details; want 3 of each", len(got), len(a.fetchedIDs()))
	}
	if stats.Prefiltered != 3 || stats.Fetched != 3 || stats.Listed != 6 {
		t.Fatalf("stats %+v", stats)
	}
	// DropListed judges what the listing row says: title, company, location.
	p := judged[0]
	if p.Title != "Role "+idOf(0) || p.Company != "ServiceNow" || p.Location != "Austin, Texas, United States" || p.ATS != posting.SmartRecruiters || p.Description != "" {
		t.Fatalf("judged %+v", p)
	}
}

func TestDropListedNeverDropsAStoredPosting(t *testing.T) {
	id := idOnTurn(false)
	a := board(0)
	a.ids = func(string) []string { return []string{id} }
	st := storedPosting(id, stamp)
	s := src(t, a.doer(), &Options{
		Stored:     func(sources.Board) []posting.Posting { return []posting.Posting{st} },
		DropListed: func(posting.Posting) bool { return true },
	})
	got, err := s.List(context.Background(), srBoard())
	if err != nil || len(got) != 1 {
		t.Fatalf("%v, %v: a stored row is returned whatever DropListed says, or it would age", got, err)
	}
}

func TestReportIsCalledOnceEvenWhenTheBoardFails(t *testing.T) {
	a := board(10)
	a.bad = map[string]srctest.Reply{idOf(4): {Status: 500, Body: []byte("{}")}}
	rep := &reports{}
	if _, err := src(t, a.doer(), &Options{Report: rep.add}).List(context.Background(), srBoard()); err == nil {
		t.Fatal("want an error")
	}
	if len(rep.calls) != 1 || rep.calls[0].Listed != 10 {
		t.Fatalf("reports %+v", rep.calls)
	}
	// A listing that fails reports nothing: there is no board count to report.
	b := board(0)
	b.list = func(string, int) (srctest.Reply, bool) { return srctest.Reply{Status: 500}, true }
	rep = &reports{}
	if _, err := src(t, b.doer(), &Options{Report: rep.add}).List(context.Background(), srBoard()); err == nil {
		t.Fatal("want an error")
	}
	if len(rep.calls) != 0 {
		t.Fatalf("reports %+v after a failed listing", rep.calls)
	}
}

func TestStoredIsReadOncePerBoardAndGetsTheBoard(t *testing.T) {
	a := board(2)
	var calls []sources.Board
	s := src(t, a.doer(), &Options{Stored: func(b sources.Board) []posting.Posting { calls = append(calls, b); return nil }})
	if _, err := s.List(context.Background(), srBoard()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Token != company {
		t.Fatalf("Stored called %d times with %+v", len(calls), calls)
	}
}

// ---- description ----

func TestDescriptionIsBoundedAndKeepsTheJobFirst(t *testing.T) {
	huge := "<p>" + strings.Repeat("company boilerplate é ", 30000) + "</p>"
	a := board(1)
	a.detail = func(_ string, d map[string]any) {
		d["jobAd"] = map[string]any{"sections": map[string]any{
			"companyDescription":    map[string]any{"title": "Company Description", "text": huge},
			"jobDescription":        map[string]any{"title": "Job Description", "text": "<p>Build things.</p><script>alert(1)</script>"},
			"qualifications":        map[string]any{"title": "Qualifications", "text": "<ul><li>Go</li><li>Rust</li></ul>"},
			"additionalInformation": map[string]any{"title": "Additional Information", "text": huge},
			"videos":                map[string]any{"title": "Videos", "text": "<p>ignored</p>"},
		}}
	}
	got, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
	if err != nil {
		t.Fatal(err)
	}
	desc := got[0].Description
	if len(desc) > srcutil.MaxDescription || !utf8.ValidString(desc) {
		t.Fatalf("description %d bytes valid=%v", len(desc), utf8.ValidString(desc))
	}
	if !strings.Contains(desc, "Job Description\nBuild things.") || !strings.Contains(desc, "Qualifications\n") || !strings.Contains(desc, "Go") {
		t.Fatalf("job sections lost to the boilerplate: %.300q", desc)
	}
	if strings.Contains(desc, "alert(1)") || strings.Contains(desc, "<") || strings.Contains(desc, "ignored") {
		t.Fatalf("script, markup or an unknown section reached the description: %.300q", desc)
	}
	if jd, ai := strings.Index(desc, "Job Description"), strings.Index(desc, "Additional Information"); jd > ai {
		t.Fatalf("job description after additional information")
	}
}

func TestDescriptionOfAPostingWithNoSections(t *testing.T) {
	a := board(1)
	a.detail = func(_ string, d map[string]any) { d["jobAd"] = map[string]any{} }
	got, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
	if err != nil || len(got) != 1 {
		t.Fatalf("%v, %v", got, err)
	}
	if got[0].Description != "Role: full-time\n\n" && got[0].Description != "Role: full-time" {
		t.Fatalf("description %q, want only the role line", got[0].Description)
	}
}

func TestHostileTextStaysText(t *testing.T) {
	a := board(1)
	a.detail = func(_ string, d map[string]any) {
		d["name"] = "Engineer &lt;script&gt;    x\n\ty"
		d["department"] = map[string]any{"label": "  Eng\u0000ineering "}
		d["jobAd"] = map[string]any{"sections": map[string]any{
			"jobDescription": map[string]any{"title": "Job Description", "text": "<p>&lt;b&gt;not bold&lt;/b&gt; $(rm -rf /) `id` ; ../..</p>"},
		}}
	}
	got, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Title != "Engineer <script> x y" {
		t.Errorf("title %q", got[0].Title)
	}
	if strings.ContainsRune(got[0].Department, 0) {
		t.Errorf("department kept a control character: %q", got[0].Department)
	}
	if !strings.Contains(got[0].Description, "<b>not bold</b> $(rm -rf /) `id` ; ../..") {
		t.Errorf("description %q: escaped markup must stay literal text", got[0].Description)
	}
}

func TestOneListingPageIsOneRequestAndDetailsFollowInOrder(t *testing.T) {
	a := board(4)
	d := a.doer()
	if _, err := src(t, d, nil).List(context.Background(), srBoard()); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, u := range d.URLs() {
		if strings.Contains(u, "?") {
			kinds = append(kinds, "list")
		} else {
			kinds = append(kinds, "detail")
		}
	}
	if got := strings.Join(kinds, ","); got != fmt.Sprint("list,detail,detail,detail,detail") {
		t.Fatalf("request order %s", got)
	}
}
