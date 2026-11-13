package eightfold

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// storedPosting is what an earlier run stored for position pid of domain.
func storedPosting(t *testing.T, domain, pid string) posting.Posting {
	t.Helper()
	key, err := posting.EightfoldKey(domain, pid)
	if err != nil {
		t.Fatal(err)
	}
	return posting.Posting{Key: key, ATS: posting.Eightfold, Company: "Microsoft", Title: "Stored " + pid,
		Description: "stored text", URL: "https://" + host + "/careers/job/" + pid, SeenAt: fixedNow.Add(-48 * time.Hour)}
}

// turnSplit is ids 0..n-1 split by whether fixedNow's day is their refresh turn.
func turnSplit(n int) (onTurn, offTurn []string) {
	day := srcutil.Day(fixedNow)
	for i := range n {
		if srcutil.RefreshTurn(id(i), day) {
			onTurn = append(onTurn, id(i))
		} else {
			offTurn = append(offTurn, id(i))
		}
	}
	return onTurn, offTurn
}

// detailCounter wraps s's doer and records the position ids whose detail was
// asked for.
func detailCounter(s *source) *[]string {
	var asked []string
	inner := s.d
	s.d = srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if u, err := url.Parse(r.URL); err == nil && u.Path == "/api/pcsx/position_details" {
			asked = append(asked, u.Query().Get("position_id"))
		}
		return inner.(*srctest.Doer).Handler(r)
	})
	return &asked
}

// bounded is a goneSite of n positions with the bounded detail step on: stored
// returns the postings for the board, stats collects each report.
func bounded(t *testing.T, n int, replies map[string]srctest.Reply, stored []posting.Posting, drop func(posting.Posting) bool) (*source, *[]string, *[]Stats) {
	t.Helper()
	s := goneSite(t, n, replies)
	asked := detailCounter(s)
	var stats []Stats
	s.opt = &Options{
		Stored:     func(sources.Board) []posting.Posting { return stored },
		DropListed: drop,
		Report:     func(_ sources.Board, st Stats) { stats = append(stats, st) },
	}
	return s, asked, &stats
}

func accounted(st Stats) bool {
	return st.Reused+st.Fetched+st.Refreshed+st.Prefiltered+st.Deferred+st.Gone == st.Listed
}

// With every position stored, a run asks for no detail but those whose refresh
// turn it is: the stored postings come back as stored, seen now, in listing
// order, and the ones on their turn are read again.
func TestStoredPositionsAreReused(t *testing.T) {
	const n = 40
	onTurn, offTurn := turnSplit(n)
	if len(onTurn) == 0 || len(offTurn) == 0 {
		t.Fatalf("fixture: %d on turn, %d off: pick another n", len(onTurn), len(offTurn))
	}
	var stored []posting.Posting
	for i := range n {
		stored = append(stored, storedPosting(t, "microsoft.com", id(i)))
	}
	s, asked, stats := bounded(t, n, nil, stored, nil)
	got, err := s.List(context.Background(), board("t"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*asked, onTurn) {
		t.Errorf("details asked %v, want only the refresh turn %v", *asked, onTurn)
	}
	if len(got) != n {
		t.Fatalf("got %d postings, want %d", len(got), n)
	}
	for i, p := range got {
		pid := id(i)
		if string(p.Key) != "ef:microsoft.com:"+pid || !p.SeenAt.Equal(fixedNow) {
			t.Errorf("posting %d: key %s seen %s", i, p.Key, p.SeenAt)
		}
		refreshed := slices.Contains(onTurn, pid)
		if stale := p.Title == "Stored "+pid && p.Description == "stored text"; stale == refreshed {
			t.Errorf("posting %s: refreshed=%v but title %q", pid, refreshed, p.Title)
		}
	}
	st := (*stats)[0]
	if len(*stats) != 1 || st.Reused != len(offTurn) || st.Refreshed != len(onTurn) || st.Fetched != 0 || !accounted(st) {
		t.Errorf("stats %+v", st)
	}
}

// New positions are fetched, stored ones off their turn are not, and the
// stored posting of another board with the same position id is never used:
// keys are built from this board's domain.
func TestNewPositionsAreFetchedAndOtherBoardsNeverMatch(t *testing.T) {
	const n = 30
	_, offTurn := turnSplit(n)
	stored := []posting.Posting{storedPosting(t, "microsoft.com", offTurn[0]), storedPosting(t, "microsoft.com", offTurn[1])}
	// Qualcomm's posting for the same id, handed back by a confused store.
	stored = append(stored, storedPosting(t, "qualcomm.com", offTurn[2]))
	s, asked, stats := bounded(t, n, nil, stored, nil)
	got, err := s.List(context.Background(), board("t"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n || len(*asked) != n-2 {
		t.Fatalf("%d postings, %d details asked; want %d and %d", len(got), len(*asked), n, n-2)
	}
	if slices.Contains(*asked, offTurn[0]) || slices.Contains(*asked, offTurn[1]) || !slices.Contains(*asked, offTurn[2]) {
		t.Errorf("asked %v: the two stored positions must be reused and the other board's must be fetched", *asked)
	}
	for _, p := range got {
		if strings.HasPrefix(string(p.Key), "ef:qualcomm.com:") {
			t.Errorf("another board's posting came back: %s", p.Key)
		}
	}
	if st := (*stats)[0]; st.Reused != 2 || st.Fetched != n-2 || !accounted(st) {
		t.Errorf("stats %+v", st)
	}
}

// DropListed judges a new position from its search row and spends no request
// on one it drops; a stored position is reused whatever its title says.
func TestDropListedSkipsNewPositionsOnly(t *testing.T) {
	const n = 12
	_, offTurn := turnSplit(n)
	newDropped, storedDropped := "", offTurn[0]
	for i := range n {
		if pid := id(i); pid != storedDropped && !slices.Contains(offTurn[:1], pid) {
			newDropped = pid
			break
		}
	}
	drop := func(p posting.Posting) bool {
		if p.Company != "Microsoft" || p.ATS != posting.Eightfold || p.Description != "" {
			t.Errorf("DropListed saw more than the search row: %+v", p)
		}
		return p.Title == "Role "+newDropped || p.Title == "Role "+storedDropped
	}
	s, asked, stats := bounded(t, n, nil, []posting.Posting{storedPosting(t, "microsoft.com", storedDropped)}, drop)
	got, err := s.List(context.Background(), board("t"))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(*asked, newDropped) || slices.Contains(*asked, storedDropped) {
		t.Errorf("asked %v: neither the dropped new position nor the stored one needs a detail", *asked)
	}
	keys := map[string]bool{}
	for _, p := range got {
		keys[string(p.Key)] = true
	}
	if keys["ef:microsoft.com:"+newDropped] || !keys["ef:microsoft.com:"+storedDropped] || len(got) != n-1 {
		t.Errorf("returned %d postings; dropped new present=%v, stored present=%v", len(got),
			keys["ef:microsoft.com:"+newDropped], keys["ef:microsoft.com:"+storedDropped])
	}
	if st := (*stats)[0]; st.Prefiltered != 1 || st.Reused != 1 || st.Fetched != n-2 || !accounted(st) {
		t.Errorf("stats %+v", st)
	}
}

// Gone is bounded by the details fetched, not the positions listed: one new
// position closed among reused ones is only that position, and more gone than
// a tenth of what was fetched is the detail endpoint failing.
func TestGoneIsBoundedByTheFetchedDetails(t *testing.T) {
	const n = 40
	_, offTurn := turnSplit(n)
	// The first four off-turn ids are new; the rest of them are stored and
	// reused. The on-turn ids are new too: nothing stored is re-read.
	var stored []posting.Posting
	for _, pid := range offTurn[4:] {
		stored = append(stored, storedPosting(t, "microsoft.com", pid))
	}
	var newIDs []string
	for i := range n {
		if pid := id(i); !slices.Contains(offTurn[4:], pid) {
			newIDs = append(newIDs, pid)
		}
	}
	if len(newIDs) < 4 || srcutil.MaxGone(len(newIDs)) != 1 {
		t.Fatalf("fixture: %d new ids, MaxGone %d; the cases below need at least 4 and exactly 1", len(newIDs), srcutil.MaxGone(len(newIDs)))
	}

	// One fetched position gone: the board stands, without it.
	s, _, stats := bounded(t, n, map[string]srctest.Reply{newIDs[0]: notFound()}, stored, nil)
	got, err := s.List(context.Background(), board("t"))
	if err != nil || len(got) != n-1 {
		t.Fatalf("one gone among %d fetched: %d postings, %v", len(newIDs), len(got), err)
	}
	if st := (*stats)[0]; st.Gone != 1 || !accounted(st) {
		t.Errorf("stats %+v", st)
	}

	// Two gone of a handful fetched is over the tenth (at least one) MaxGone
	// allows, reused positions or not.
	replies := map[string]srctest.Reply{newIDs[0]: notFound(), newIDs[1]: notFound()}
	s, _, _ = bounded(t, n, replies, stored, nil)
	if _, err := s.List(context.Background(), board("t")); err == nil || !strings.Contains(err.Error(), "detail endpoint is not answering") {
		t.Errorf("two gone of %d fetched: %v", len(newIDs), err)
	}

	// Nothing stored and the only position gone: the endpoint, not a closing.
	s, _, _ = bounded(t, 1, map[string]srctest.Reply{id(0): notFound()}, nil, nil)
	if _, err := s.List(context.Background(), board("t")); err == nil {
		t.Error("the only position gone with nothing reused passed")
	}
}

// A store that answers nothing, or postings of other ATSs, changes nothing:
// every detail is asked for, as without the bounded step.
func TestEmptyOrForeignStoreAsksForEveryDetail(t *testing.T) {
	const n = 5
	foreign := posting.Posting{Key: "wd:microsoft.com:" + posting.Key(id(0)), ATS: posting.Workday, Title: "Not this"}
	for name, stored := range map[string][]posting.Posting{"nil": nil, "foreign": {foreign}} {
		s, asked, stats := bounded(t, n, nil, stored, nil)
		got, err := s.List(context.Background(), board("t"))
		if err != nil || len(got) != n || len(*asked) != n {
			t.Errorf("%s: %d postings, %d details, %v", name, len(got), len(*asked), err)
		}
		if st := (*stats)[0]; st.Fetched != n || st.Reused != 0 || !accounted(st) {
			t.Errorf("%s: stats %+v", name, st)
		}
	}
}
