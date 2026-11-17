package workday

import (
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// countDetails wraps h and counts every detail GET by URL.
func countDetails(h func(srctest.Request) (srctest.Reply, bool)) (func(srctest.Request) (srctest.Reply, bool), func() map[string]int) {
	var mu sync.Mutex
	n := map[string]int{}
	return func(r srctest.Request) (srctest.Reply, bool) {
			if r.Method == "GET" && strings.HasPrefix(r.URL, siteURL) {
				mu.Lock()
				n[r.URL]++
				mu.Unlock()
			}
			return h(r)
		}, func() map[string]int {
			mu.Lock()
			defer mu.Unlock()
			out := map[string]int{}
			for k, v := range n {
				out[k] = v
			}
			return out
		}
}

// The strict companion of TestW1dadvUnusableStoredPostingIsReadAgainAndNeverDropped:
// the unusable posting's own detail URL is requested exactly once, its listed
// text is the one that detail read, and Refreshed is exactly the unusable row
// plus the other stored rows whose refresh turn it is (so a posting counted as
// refreshed without a request, or a refreshed count inflated by another row,
// fails).
func TestW1dadv2UnusableRowIsRequestedExactlyOnce(t *testing.T) {
	fb := missBoard(30)
	const unreadable = 5
	h, counts := countDetails(fb.handle)
	var st Stats
	s := missSource(h, &Options{
		Stored:   func(sources.Board) []posting.Posting { return w1dadvUnreadableStored(fb, unreadable) },
		Unusable: w1dadvMarked(fb, unreadable),
		Report:   func(_ sources.Board, x Stats) { st = x },
	})
	got, err := s.List(missNoRetry(), board)
	if err != nil {
		t.Fatal(err)
	}
	wantRefreshed := 1
	for i := range fb.jobs {
		if i != unreadable && srcutil.RefreshTurn(fb.jobs[i].path, srcutil.Day(fixedNow)) {
			wantRefreshed++
		}
	}
	esc, _ := escapePath(fb.jobs[unreadable].path)
	if c := counts()[siteURL+esc]; c != 1 {
		t.Fatalf("the unusable posting's detail was requested %d times, want 1 (%v)", c, counts())
	}
	for _, p := range got {
		if strings.HasSuffix(string(p.Key), strings.ToLower("JR"+strconv.Itoa(fb.jobs[unreadable].req))) && (p.Description == "" || p.Description == "stored text") {
			t.Fatalf("listed with text %q", p.Description)
		}
	}
	if st.Fetched != 0 {
		t.Fatalf("stats %+v", st)
	}
	t.Logf("stats %+v, expected refreshed by the turn rule %d, detail GETs %d", st, wantRefreshed, len(counts()))
	if st.Refreshed != wantRefreshed {
		t.Fatalf("Refreshed = %d, want %d (the unusable row plus the stored rows whose turn it is); stats %+v", st.Refreshed, wantRefreshed, st)
	}
}

// "Always read, uncapped by the limit": a board whose every stored posting is
// unusable (an events.jsonl truncated under a large board) spends a detail
// request on every one of them in a single run, past MaxDetails, the bound the
// adapter documents ("at most MaxDetails detail requests go out per board").
func TestW1dadv2ManyUnusableRowsStayWithinMaxDetails(t *testing.T) {
	n := MaxDetails + 200
	fb := missBoard(n)
	h, counts := countDetails(fb.handle)
	keys := map[posting.Key]bool{}
	var stored []posting.Posting
	for i := range n {
		sp := storedJobs(fb, i)
		sp[0].Description = ""
		keys[sp[0].Key] = true
		stored = append(stored, sp...)
	}
	var st Stats
	s := missSource(h, &Options{
		Stored:   func(sources.Board) []posting.Posting { return stored },
		Unusable: func(k posting.Key) bool { return keys[k] },
		Report:   func(_ sources.Board, x Stats) { st = x },
	})
	_, err := s.List(missNoRetry(), board)
	total := 0
	for _, c := range counts() {
		total += c
	}
	t.Logf("List error %v; detail GETs %d; stats %+v", err, total, st)
	if total > MaxDetails {
		t.Fatalf("%d detail requests in one run for %d unusable stored postings, over MaxDetails=%d", total, n, MaxDetails)
	}
}
