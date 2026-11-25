package dice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/rowstest"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func storedOf(t *testing.T, gs ...string) func(sources.Board) []posting.Posting {
	return func(sources.Board) []posting.Posting {
		var out []posting.Posting
		for _, g := range gs {
			out = append(out, storedPosting(t, g))
		}
		return out
	}
}

func TestPrefiltered(t *testing.T) {
	gs := guids(0, 4)
	f := newFake(t, map[string][]string{"ml": gs})
	var st Stats
	drop := func(p posting.Posting) bool {
		if p.Company != "Example Corp" || p.Location != "Austin, Texas, USA" || p.Description != "" {
			t.Errorf("DropListed saw %+v", p)
		}
		return strings.HasSuffix(p.Title, gs[1][:8])
	}
	got, err := src(f.doer(), &Options{DropListed: drop, Report: func(_ sources.Board, x Stats) { st = x }}).List(context.Background(), diceBoard("ml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || st.Prefiltered != 1 || st.Fetched != 3 || slices.Contains(f.detailIDs(), gs[1]) {
		t.Fatalf("got %d, stats %+v, details %v", len(got), st, f.detailIDs())
	}
}

func TestStoredReusedWithoutCall(t *testing.T) {
	gs := notOnTurn(0, 5)
	f := newFake(t, map[string][]string{"ml": gs})
	var st Stats
	got, err := src(f.doer(), &Options{Stored: storedOf(t, gs...), Report: func(_ sources.Board, x Stats) { st = x }}).List(context.Background(), diceBoard("ml"))
	if err != nil {
		t.Fatal(err)
	}
	if f.count("get_job_details") != 0 || st.Reused != 5 || len(got) != 5 {
		t.Fatalf("%d detail calls, stats %+v", f.count("get_job_details"), st)
	}
	for i, p := range got {
		if p.Description != "stored text "+gs[i] || !p.SeenAt.Equal(fixedNow) {
			t.Fatalf("reused posting %+v", p)
		}
	}
}

func TestRefreshTurnRereads(t *testing.T) {
	// Some stored row's turn comes today: it is re-read though unchanged.
	day := srcutil.Day(fixedNow)
	var on string
	for i := 0; on == ""; i++ {
		if g := guidOf(i); srcutil.RefreshTurn(g, day) {
			on = g
		}
	}
	f := newFake(t, map[string][]string{"ml": {on}})
	var st Stats
	got, err := src(f.doer(), &Options{Stored: storedOf(t, on), Report: func(_ sources.Board, x Stats) { st = x }}).List(context.Background(), diceBoard("ml"))
	if err != nil || st.Refreshed != 1 || !strings.Contains(got[0].Description, "About role") {
		t.Fatalf("got %+v, stats %+v, %v", got, st, err)
	}
}

// A moved modifiedDate alone is no reason to call get_job_details: the
// ledger keeps a posting's first UpdatedAt when its text is unchanged, so a
// moved date would read as changed on every later run and spend a detail call
// each time. Stored postings are re-read on their refresh turn only.
func TestModifiedDateAloneDoesNotRefetch(t *testing.T) {
	gs := notOnTurn(0, 3)
	f := newFake(t, map[string][]string{"ml": gs})
	f.edit = func(g string, row map[string]any) {
		if g == gs[1] {
			row["modifiedDate"] = "2026-11-24T08:00:00Z"
		}
	}
	var st Stats
	for run := range 2 {
		got, err := src(f.doer(), &Options{Stored: storedOf(t, gs...), Report: func(_ sources.Board, x Stats) { st = x }}).List(context.Background(), diceBoard("ml"))
		if err != nil {
			t.Fatal(err)
		}
		if ids := f.detailIDs(); len(ids) != 0 || st.Refreshed != 0 || st.Reused != 3 || len(got) != 3 {
			t.Fatalf("run %d: details %v, stats %+v", run, ids, st)
		}
	}
}

// A stored posting on its refresh turn is re-read.
func TestRefreshTurnRereadsAStoredPosting(t *testing.T) {
	gs := append(onTurn(0, 1), notOnTurn(0, 2)...)
	f := newFake(t, map[string][]string{"ml": gs})
	var st Stats
	got, err := src(f.doer(), &Options{Stored: storedOf(t, gs...), Report: func(_ sources.Board, x Stats) { st = x }}).List(context.Background(), diceBoard("ml"))
	if err != nil {
		t.Fatal(err)
	}
	if ids := f.detailIDs(); len(ids) != 1 || ids[0] != gs[0] || st.Refreshed != 1 || st.Reused != 2 || !strings.Contains(got[0].Description, "About role") {
		t.Fatalf("details %v, stats %+v", ids, st)
	}
}

func TestStoredTransientMissReturnsStored(t *testing.T) {
	gs := append(onTurn(0, 1), notOnTurn(0, 2)...)
	f := newFake(t, map[string][]string{"ml": gs})
	f.detail = func(string) (answer, bool) { return answer{raw: &srctest.Reply{Err: timeoutErr{}}}, true }
	var st Stats
	got, err := src(f.doer(), &Options{Stored: storedOf(t, gs...), Report: func(_ sources.Board, x Stats) { st = x }}).List(context.Background(), diceBoard("ml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Description != "stored text "+gs[0] || st.Unanswered != 1 || st.Reused != 2 {
		t.Fatalf("got %+v, stats %+v", got, st)
	}
}

func TestNewTransientMissNotReturned(t *testing.T) {
	gs := guids(0, 10)
	f := newFake(t, map[string][]string{"ml": gs})
	f.detail = func(g string) (answer, bool) {
		if g == gs[4] {
			return answer{raw: &srctest.Reply{Err: timeoutErr{}}}, true
		}
		return answer{}, false
	}
	var st Stats
	got, err := src(f.doer(), &Options{Report: func(_ sources.Board, x Stats) { st = x }}).List(context.Background(), diceBoard("ml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 9 || st.Unanswered != 1 || st.Fetched != 9 {
		t.Fatalf("got %d, stats %+v", len(got), st)
	}
	for _, p := range got {
		if p.Key == key(t, gs[4]) {
			t.Fatal("a new row whose detail missed was returned")
		}
	}
	// New(d) cannot tell a missed new row from a stored one: it fails.
	if _, err := src(f.doer(), nil).List(context.Background(), diceBoard("ml")); err == nil {
		t.Fatal("New listed a board with a missed detail")
	}
}

func TestJobNotFoundIsGone(t *testing.T) {
	gs := guids(0, 10)
	f := newFake(t, map[string][]string{"ml": gs})
	gone := map[string]bool{gs[2]: true}
	f.detail = func(g string) (answer, bool) {
		if gone[g] {
			return answer{capture: "notfound.sse"}, true
		}
		return answer{}, false
	}
	var st Stats
	got, err := src(f.doer(), &Options{Report: func(_ sources.Board, x Stats) { st = x }}).List(context.Background(), diceBoard("ml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 9 || st.Gone != 1 {
		t.Fatalf("got %d, stats %+v", len(got), st)
	}
	// Two of ten is more than MaxGone(10): the detail tool is failing.
	gone[gs[7]] = true
	n := 0
	_, err = src(f.doer(), &Options{Report: func(_ sources.Board, x Stats) { st = x; n++ }}).List(context.Background(), diceBoard("ml"))
	if err == nil || !strings.Contains(err.Error(), "gone") || errors.Is(err, srcutil.ErrBoardNotFound) {
		t.Fatalf("err = %v", err)
	}
	if n != 1 || st.Gone != 2 {
		t.Fatalf("reported %d times, stats %+v", n, st)
	}
}

func TestOtherDetailToolErrorFails(t *testing.T) {
	f := newFake(t, map[string][]string{"ml": guids(0, 3)})
	f.detail = func(string) (answer, bool) {
		return answer{toolErr: "Failed to retrieve job details: upstream 500"}, true
	}
	if _, err := src(f.doer(), &Options{}).List(context.Background(), diceBoard("ml")); err == nil || errors.Is(err, srcutil.ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
	f.detail = func(string) (answer, bool) { return answer{sc: map[string]any{"skills": []any{}}}, true }
	if _, err := src(f.doer(), &Options{}).List(context.Background(), diceBoard("ml")); !errors.Is(err, ErrShape) {
		t.Fatalf("a detail with no description: err = %v", err)
	}
}

func TestNewWithoutStoredOverCapFails(t *testing.T) {
	f := newFake(t, map[string][]string{"ml": guids(0, MaxDetails+1)})
	_, err := src(f.doer(), nil).List(context.Background(), diceBoard("ml"))
	if !errors.Is(err, srcutil.ErrTooManyPostings) || f.count("get_job_details") != 0 {
		t.Fatalf("err = %v after %d detail calls", err, f.count("get_job_details"))
	}
	// At the cap it lists.
	f = newFake(t, map[string][]string{"ml": guids(0, MaxDetails)})
	if got, err := src(f.doer(), nil).List(context.Background(), diceBoard("ml")); err != nil || len(got) != MaxDetails {
		t.Fatalf("got %d, %v", len(got), err)
	}
}

func TestStoredFromAnotherBoardIgnored(t *testing.T) {
	gs := notOnTurn(0, 2)
	f := newFake(t, map[string][]string{"ml": gs})
	other := func(sources.Board) []posting.Posting {
		p := storedPosting(t, gs[0])
		p.Key = posting.Key("hm:himalayas:x:" + gs[0])
		return []posting.Posting{p}
	}
	got, err := src(f.doer(), &Options{Stored: other}).List(context.Background(), diceBoard("ml"))
	if err != nil || len(got) != 2 || f.count("get_job_details") != 2 {
		t.Fatalf("got %d, %v, %d details", len(got), err, f.count("get_job_details"))
	}
}

func TestDirectApplyDetailsPageAccepted(t *testing.T) {
	gs := guids(0, 2)
	f := newFake(t, map[string][]string{"applied AI": gs})
	f.edit = func(g string, row map[string]any) {
		if g == gs[1] {
			row["detailsPageUrl"] = "https://www.dice.com/direct-apply/" + g + "?utm_source=vani-test&utm_medium=mcp"
		}
	}
	got, err := src(f.doer(), nil).List(context.Background(), diceBoard("applied AI"))
	if err != nil {
		t.Fatalf("direct-apply row rejected: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d postings, want 2", len(got))
	}
	if got[1].URL != "https://www.dice.com/job-detail/"+gs[1] {
		t.Fatalf("URL = %q, want canonical job-detail page", got[1].URL)
	}
}

// listPayload runs one listing of the rows in payload (a JSON array) with a
// detail answering each, for rowstest.
func listPayload(t *testing.T) func(ctx context.Context, payload string) ([]posting.Posting, error) {
	return func(ctx context.Context, payload string) ([]posting.Posting, error) {
		var rows []any
		if err := json.Unmarshal([]byte(payload), &rows); err != nil {
			t.Fatal(err)
		}
		f := newFake(t, nil)
		f.search = func(map[string]any) (answer, bool) {
			return answer{sc: map[string]any{"data": rows, "metadata": map[string]any{"page": 1, "pageSize": 100, "total": len(rows), "totalPages": 1}}}, true
		}
		return src(f.doer(), &Options{}).List(ctx, diceBoard("ml"))
	}
}

func TestBadRows(t *testing.T) {
	// marker is replaced by the row's index as twelve digits, so every bad
	// row with a UUID guid has its own (a repeated guid is dropped as a
	// repeat, not counted bad).
	const marker = "BADROWIDX"
	bad := func(edit func(map[string]any)) string {
		r := rowObject("00000000-0000-4000-8000-" + marker)
		edit(r)
		raw, _ := json.Marshal(r)
		return string(raw)
	}
	rowstest.Check(t, rowstest.Case{
		List: listPayload(t),
		Payload: func(n int, badRows map[int]string) string {
			var parts []string
			for i := 0; i < n; i++ {
				if r, ok := badRows[i]; ok {
					parts = append(parts, strings.ReplaceAll(r, marker, fmt.Sprintf("%012d", i)))
					continue
				}
				raw, _ := json.Marshal(rowObject(guidOf(i)))
				parts = append(parts, string(raw))
			}
			return "[" + strings.Join(parts, ",") + "]"
		},
		BadRows: map[string]string{
			"guid not a UUID":  bad(func(r map[string]any) { r["guid"] = "job-" + marker }),
			"null guid":        bad(func(r map[string]any) { r["guid"] = nil }),
			"empty title":      bad(func(r map[string]any) { r["title"] = "  " }),
			"null company":     bad(func(r map[string]any) { r["companyName"] = nil }),
			"empty company":    bad(func(r map[string]any) { r["companyName"] = "\u00a0" }),
			"bad postedDate":   bad(func(r map[string]any) { r["postedDate"] = "yesterday" }),
			"bad modifiedDate": bad(func(r map[string]any) { r["modifiedDate"] = "2026-13-01" }),
			"foreign page":           bad(func(r map[string]any) { r["detailsPageUrl"] = "https://evil.example/job-detail/x" }),
			"direct-apply wrong id":  bad(func(r map[string]any) { r["detailsPageUrl"] = "https://www.dice.com/direct-apply/00000000-0000-4000-8000-ffffffffffff" }),
			"script page":            bad(func(r map[string]any) { r["detailsPageUrl"] = "javascript:alert(1)" }),
		},
	})
}
