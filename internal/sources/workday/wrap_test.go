package workday

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// Workday answers an offset at or past a query's last row with page 1 again,
// and reports total on the first page only (0 on the rest): seen on
// salesforce/External_Career_Site, search "machine learning", 2026-10-22,
// total 40: offset 0 total 40, offset 20 total 0, offset 40 total 40 and page
// 1's rows. The listing was complete at 40; the walk asked for offset 40
// anyway and refused the run. These tests hold the live shape.

// liveBoard is an n-job board that answers as the live service does.
func liveBoard(n int) *fakeBoard {
	fb := nvidiaBoard(n)
	fb.params = nil
	fb.wrapPastEnd = true
	fb.totalAt = func(offset, real int) (string, bool) {
		if offset == 0 {
			return fmt.Sprint(real), true
		}
		return "0", true
	}
	return fb
}

func listedOnce(t *testing.T, rows []listedJob, want int) {
	t.Helper()
	if len(rows) != want {
		t.Fatalf("listed %d of %d", len(rows), want)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.ExternalPath] {
			t.Fatalf("%s listed twice", r.ExternalPath)
		}
		seen[r.ExternalPath] = true
	}
}

// A total that is an exact multiple of the page size ends on a full page; the
// next page is Workday's wrap, which ends a listing that has read every counted
// job. The walk asks for that one page (never assumes it) and no further.
func TestListingEndingOnAFullPageEndsOnTheWrap(t *testing.T) {
	for _, n := range []int{1, 19, 20, 21, 39, 40, 41, 60, 100, 1980, 1999} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			fb := liveBoard(n)
			rows, w, err := listAll(t, fb, board)
			if err != nil {
				t.Fatal(err)
			}
			listedOnce(t, rows, n)
			pages := (n + pageSize - 1) / pageSize
			if n%pageSize == 0 {
				pages++ // the page past the total, which wraps
			}
			if w.requests != pages || fb.posts != pages {
				t.Errorf("%d requests (%d posted) for %d pages; offsets %v", w.requests, fb.posts, pages, fb.offsets)
			}
			for _, o := range fb.offsets {
				if o > n || (o == n && n%pageSize != 0) {
					t.Errorf("asked for offset %d on a board of %d", o, n)
				}
			}
		})
	}
}

// The owner's board, through List: 40 jobs, total 40 on the first page and 0
// on the second, page 1 again at offset 40.
func TestListWithTotalOnFirstPageOnly(t *testing.T) {
	var offsets []int
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "POST" && r.URL == jobsURL {
			var req struct{ Offset int }
			if err := json.Unmarshal(r.Body, &req); err != nil {
				return srctest.Reply{Status: 400}, true
			}
			offsets = append(offsets, req.Offset)
			start := req.Offset
			total := 0
			if start == 0 {
				total = 40
			}
			if start >= 40 {
				start, total = 0, 40
			}
			var rows []string
			for i := start; i < start+20; i++ {
				rows = append(rows, fmt.Sprintf(`{"title":"T%d","externalPath":"/job/X/T%d_JR%d","locationsText":"L","postedOn":"Posted Today"}`, i, i, i))
			}
			return ok(fmt.Sprintf(`{"total":%d,"jobPostings":[%s]}`, total, strings.Join(rows, ","))), true
		}
		if r.Method == "GET" && strings.HasPrefix(r.URL, siteURL+"/job/X/T") {
			id := r.URL[strings.LastIndex(r.URL, "_JR")+3:]
			return ok(fmt.Sprintf(detailTmpl, id, id)), true
		}
		return srctest.Reply{}, false
	})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 40 {
		t.Fatalf("got %d postings, %v", len(got), err)
	}
	if fmt.Sprint(offsets) != "[0 20 40]" {
		t.Errorf("offsets %v, want [0 20 40] (offset 40 is the wrap that ends it)", offsets)
	}
}

// The wrap check keeps its job: a wrap before the first page's total is a
// listing that lost rows, and fails the board.
func TestWrapBeforeTheTotalStillFails(t *testing.T) {
	for name, tc := range map[string]struct {
		fb      func() *fakeBoard
		wantMsg string
	}{
		"wraps at 60 of 100": {fb: func() *fakeBoard {
			fb := liveBoard(100)
			fb.wrapAt = 60
			return fb
		}, wantMsg: "short of the 100"},
		"wraps at 20 of 100": {fb: func() *fakeBoard {
			fb := liveBoard(100)
			fb.wrapAt = 20
			return fb
		}, wantMsg: "short of the 100"},
		// The total claims more than the board holds, and the board wraps rather
		// than running dry: the jobs the total counts cannot be accounted for.
		"total overstates, board wraps": {fb: func() *fakeBoard {
			fb := liveBoard(40)
			fb.totalAt = func(offset, real int) (string, bool) { return fmt.Sprint(real + 20), offset == 0 }
			return fb
		}, wantMsg: "short of the 60"},
	} {
		t.Run(name, func(t *testing.T) {
			rows, _, err := listAll(t, tc.fb(), board)
			if !errors.Is(err, errIncomplete) || rows != nil || !strings.Contains(err.Error(), "first page again") || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("err = %v with %d rows", err, len(rows))
			}
		})
	}
}

// Without a total that can be believed the wrap cannot be told from a lost
// listing, so it fails the board whenever it comes before a short page.
func TestWrapWithoutAUsableTotalStillFails(t *testing.T) {
	for name, total := range map[string]func(offset, real int) (string, bool){
		"missing":               func(int, int) (string, bool) { return "", false },
		"zero beside rows":      func(int, int) (string, bool) { return "0", true },
		"null":                  func(int, int) (string, bool) { return "null", true },
		"negative":              func(int, int) (string, bool) { return "-40", true },
		"a string":              func(int, int) (string, bool) { return `"40"`, true },
		"fractional":            func(int, int) (string, bool) { return "40.5", true },
		"under the page's rows": func(int, int) (string, bool) { return "7", true },
	} {
		t.Run(name, func(t *testing.T) {
			fb := liveBoard(40)
			fb.totalAt = total
			rows, _, err := listAll(t, fb, board)
			if !errors.Is(err, errIncomplete) || rows != nil || !strings.Contains(err.Error(), "first page again") {
				t.Fatalf("err = %v with %d rows", err, len(rows))
			}
		})
	}
	// The same totals on a board that runs dry on a short page list in full.
	for name, total := range map[string]func(offset, real int) (string, bool){
		"missing":          func(int, int) (string, bool) { return "", false },
		"zero beside rows": func(int, int) (string, bool) { return "0", true },
		"under the rows":   func(int, int) (string, bool) { return "7", true },
	} {
		t.Run("short page, total "+name, func(t *testing.T) {
			fb := liveBoard(45)
			fb.totalAt = total
			rows, _, err := listAll(t, fb, board)
			if err != nil {
				t.Fatal(err)
			}
			listedOnce(t, rows, 45)
		})
	}
}

// The first page's total is the one believed. A later page that reports another
// number (a board that grew or shrank mid-walk) neither stops the walk early nor
// extends it: the walk goes to the first total, and the wrap check judges the
// pages it asks for.
func TestTotalChangingMidWalk(t *testing.T) {
	later := func(claim func(real int) int) func(offset, real int) (string, bool) {
		return func(offset, real int) (string, bool) {
			if offset == 0 {
				return fmt.Sprint(real), true
			}
			return fmt.Sprint(claim(real)), true
		}
	}
	t.Run("later pages claim fewer", func(t *testing.T) {
		fb := liveBoard(60)
		fb.totalAt = later(func(real int) int { return real - 40 })
		rows, _, err := listAll(t, fb, board)
		if err != nil {
			t.Fatal(err)
		}
		listedOnce(t, rows, 60)
	})
	t.Run("later pages claim more", func(t *testing.T) {
		fb := liveBoard(40)
		fb.totalAt = later(func(real int) int { return real + 200 })
		rows, _, err := listAll(t, fb, board)
		if err != nil {
			t.Fatal(err)
		}
		listedOnce(t, rows, 40)
		if fmt.Sprint(fb.offsets) != "[0 20 40]" {
			t.Errorf("offsets %v, want [0 20 40]", fb.offsets)
		}
	})
	t.Run("board shrinks to a short last page", func(t *testing.T) {
		fb := liveBoard(50)
		fb.totalAt = func(offset, _ int) (string, bool) { // counted 60 when the walk began
			if offset == 0 {
				return "60", true
			}
			return "0", true
		}
		rows, _, err := listAll(t, fb, board)
		if err != nil {
			t.Fatal(err)
		}
		listedOnce(t, rows, 50) // the short page at offset 40 ends it
	})
	t.Run("board shrinks onto a full page", func(t *testing.T) {
		// 60 counted, 40 left: offset 40 wraps before the count is reached. The
		// walk cannot tell it from a lost listing and fails, loudly, and the
		// next run lists the 40.
		fb := liveBoard(40)
		fb.totalAt = func(offset, _ int) (string, bool) {
			if offset == 0 {
				return "60", true
			}
			return "0", true
		}
		if _, _, err := listAll(t, fb, board); !errors.Is(err, errIncomplete) {
			t.Fatalf("err = %v", err)
		}
	})
}

// A first-page total that understates the board never ends the listing on its
// own: only the wrap does, so rows past the total are still read, and a board
// is never cut short (its unread postings aged as absent) on a low count.
func TestUnderstatedTotalStillListsEveryRow(t *testing.T) {
	for _, claim := range []string{"20", "40", "100"} {
		t.Run("claims "+claim, func(t *testing.T) {
			fb := nvidiaBoard(300) // runs dry on an empty page, never wraps
			fb.params = nil
			fb.totalAt = func(offset, _ int) (string, bool) { return claim, offset == 0 }
			rows, _, err := listAll(t, fb, board)
			if err != nil {
				t.Fatal(err)
			}
			listedOnce(t, rows, 300)
		})
	}
	fb := nvidiaBoard(300)
	fb.params = nil
	fb.totalAt = func(offset, _ int) (string, bool) { return "7", offset == 0 }
	rows, _, err := listAll(t, fb, board)
	if err != nil {
		t.Fatal(err)
	}
	listedOnce(t, rows, 300)
}

// Empty and near-empty boards: one request, no more.
func TestEmptyAndTinyListingsAskForOnePage(t *testing.T) {
	for name, tc := range map[string]struct {
		n     int
		total func(offset, real int) (string, bool)
	}{
		"empty":                {n: 0},
		"empty, total claims":  {n: 0, total: func(int, int) (string, bool) { return "100", true }},
		"empty, total missing": {n: 0, total: func(int, int) (string, bool) { return "", false }},
		"one job":              {n: 1},
	} {
		t.Run(name, func(t *testing.T) {
			fb := liveBoard(tc.n)
			if tc.total != nil {
				fb.totalAt = tc.total
			}
			rows, w, err := listAll(t, fb, board)
			if err != nil {
				t.Fatal(err)
			}
			listedOnce(t, rows, tc.n)
			if w.requests != 1 {
				t.Errorf("%d requests, offsets %v", w.requests, fb.offsets)
			}
		})
	}
}

// The request bound still applies to a walk that knows its total.
func TestKnownTotalWalkStaysWithinTheRequestBound(t *testing.T) {
	fb := liveBoard(100)
	s, _ := fakeSource(fb, nil)
	w := newWalker(s, jobsURL, 100)
	w.requests = listRequests(100) - 2 // room for the first page and one more
	_, err := w.walk(context.Background(), map[string][]string{}, 0)
	if !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("err = %v, want the request bound", err)
	}
	if fb.posts != 2 {
		t.Errorf("%d requests made, want 2", fb.posts)
	}
}

// A total the walk believes never takes it past the window: the board that
// claims 1999 and holds more is read to 1999 only if it answers; one that wraps
// first fails.
func TestKnownTotalNeverPassesTheWindow(t *testing.T) {
	fb := liveBoard(1999)
	rows, w, err := listAll(t, fb, board)
	if err != nil {
		t.Fatal(err)
	}
	listedOnce(t, rows, 1999)
	if w.requests > window/pageSize {
		t.Errorf("%d requests, window allows %d", w.requests, window/pageSize)
	}
	fb = liveBoard(1999)
	fb.wrapAt = 1000
	if _, _, err := listAll(t, fb, board); !errors.Is(err, errIncomplete) {
		t.Fatalf("a board wrapping at 1000 of 1999: err = %v", err)
	}
}

// Every search term is walked to its own first-page total and the union listed
// once. "senior" matches exactly one page (20 of 30): its next page is the wrap,
// which ends that term cleanly.
func TestSearchTermsEachStopAtTheirOwnTotal(t *testing.T) {
	fb := liveBoard(30)
	b := board
	b.Searches = []string{"senior", "engineer 1"}
	rows, w, err := listAll(t, fb, b)
	if err != nil {
		t.Fatal(err)
	}
	union, wantPages := 0, 0
	for _, term := range b.Searches {
		hits := 0
		for _, j := range fb.jobs {
			if strings.Contains(strings.ToLower(j.title), term) {
				hits++
			}
		}
		wantPages += max(1, (hits+pageSize-1)/pageSize)
		if hits > 0 && hits%pageSize == 0 {
			wantPages++ // the page past the total, which wraps
		}
	}
	for _, j := range fb.jobs {
		l := strings.ToLower(j.title)
		if strings.Contains(l, "senior") || strings.Contains(l, "engineer 1") {
			union++
		}
	}
	listedOnce(t, rows, union)
	if w.requests != wantPages {
		t.Errorf("%d requests, want %d (offsets %v)", w.requests, wantPages, fb.offsets)
	}
}
