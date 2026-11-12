package doctor

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

func ip(n int) *int { return &n }

// runs is one board's history: a listed count per run, -1 for a failed run.
func runs(board string, listed ...int) []BoardRun {
	out := make([]BoardRun, len(listed))
	for i, n := range listed {
		r := BoardRun{RunID: fmt.Sprintf("r%02d", i), At: t0.Add(time.Duration(i) * time.Hour), Board: board, Company: "Co"}
		if n < 0 {
			r.Outcome, r.Error = OutcomeFailed, "context deadline exceeded"
		} else {
			r.Outcome, r.Listed = OutcomeRead, ip(n)
		}
		out[i] = r
	}
	return out
}

func signalOf(t *testing.T, got []BoardSignal, board string) BoardSignal {
	t.Helper()
	for _, s := range got {
		if s.Board == board {
			return s
		}
	}
	t.Fatalf("no signal for %s in %v", board, got)
	return BoardSignal{}
}

func TestSignalsNameTheSilentShapesSeenLive(t *testing.T) {
	var h []BoardRun
	h = append(h, runs("ab:graphite", 0, 0, 0, 0, 0)...)      // every read empty
	h = append(h, runs("wd:snapchat/snap", 46, 46, 0, 44)...) // one empty read between full ones
	h = append(h, runs("tb:citi", 1777, 1769, 1768, 700)...)  // latest under half the median
	h = append(h, runs("ef:microsoft.com", 141, -1, -1, -1)...)
	h = append(h, runs("gh:anthropic", 670, 671, 669)...) // healthy: no signal
	got := Signals(h, []string{"gh:anthropic", "ab:graphite", "sr:never"})

	if s := signalOf(t, got, "ab:graphite"); !slices.Equal(s.Signals, []string{SigZeroEveryRun}) || s.ZeroReads != 5 {
		t.Errorf("graphite %+v", s)
	}
	snap := signalOf(t, got, "wd:snapchat/snap")
	if !slices.Contains(snap.Signals, SigZeroAfterNonzero) || slices.Contains(snap.Signals, SigZeroEveryRun) {
		t.Errorf("snap %+v", snap)
	}
	if fmt.Sprint(snap.Series) != "[46 46 0 44]" || snap.Median != 44 {
		t.Errorf("snap series %v median %d", snap.Series, snap.Median)
	}
	if s := signalOf(t, got, "tb:citi"); !slices.Contains(s.Signals, SigSharpDrop) {
		t.Errorf("citi %+v", s)
	}
	ms := signalOf(t, got, "ef:microsoft.com")
	if !slices.Contains(ms.Signals, SigChronicFailure) || !slices.Contains(ms.Signals, SigFailingNow) || ms.LastError == "" {
		t.Errorf("microsoft %+v", ms)
	}
	if fmt.Sprint(ms.Series) != "[141 -1 -1 -1]" {
		t.Errorf("microsoft series %v", ms.Series)
	}
	if s := signalOf(t, got, "sr:never"); !slices.Equal(s.Signals, []string{SigNeverRecorded}) {
		t.Errorf("never recorded %+v", s)
	}
	for _, s := range got {
		if s.Board == "gh:anthropic" {
			t.Errorf("a healthy board was signalled: %+v", s)
		}
	}
	// Sorted by board, so two calls on one history print the same report.
	if !slices.IsSortedFunc(got, func(a, b BoardSignal) int { return strings.Compare(a.Board, b.Board) }) {
		t.Error("not sorted by board")
	}
}

func TestMassMissedIsAgainstThePreviousListing(t *testing.T) {
	h := runs("wd:x/y", 40, 40, 41)
	h[2].Missed = 25 // more than half of the previous 40 missed in one run
	if s := signalOf(t, Signals(h, nil), "wd:x/y"); !slices.Contains(s.Signals, SigMassMissed) || s.MaxMissed != 25 {
		t.Fatalf("%+v", s)
	}
	h[2].Missed = 4 // under the floor: ordinary churn
	for _, s := range Signals(h, nil) {
		t.Fatalf("churn was signalled: %+v", s)
	}
}

// Adversarial histories: nothing, too few reads to judge, out-of-order input,
// a small board, duplicates, and an empty board name.
func TestSignalsEdges(t *testing.T) {
	if got := Signals(nil, nil); len(got) != 0 {
		t.Fatalf("empty history: %v", got)
	}
	// Two empty reads are too few to call a board empty.
	if got := Signals(runs("ab:new", 0, 0), nil); len(got) != 0 {
		t.Fatalf("two reads judged: %v", got)
	}
	// A small board halving is not a sharp drop (median under the floor).
	if got := Signals(runs("ab:small", 6, 6, 6, 2), nil); len(got) != 0 {
		t.Fatalf("small board judged: %v", got)
	}
	// Input order is not an input.
	h := runs("wd:snapchat/snap", 46, 46, 0, 44)
	rev := slices.Clone(h)
	slices.Reverse(rev)
	if a, b := Signals(h, nil), Signals(rev, nil); fmt.Sprint(a[0].Signals, a[0].Series, *a[0].Latest) != fmt.Sprint(b[0].Signals, b[0].Series, *b[0].Latest) {
		t.Fatal("history order changed the signals")
	}
	// A run with no board name is ignored; a duplicated watchlist entry counts once.
	got := Signals(append(runs("", 0, 0, 0), runs("ab:z", 1, 1, 1)...), []string{"sr:a", "sr:a"})
	if len(got) != 1 || got[0].Board != "sr:a" {
		t.Fatalf("%v", got)
	}
	// The series keeps the newest maxSeries values.
	long := make([]int, maxSeries+10)
	long[len(long)-1] = 0
	for i := range long[:len(long)-1] {
		long[i] = 100
	}
	s := signalOf(t, Signals(runs("gh:long", long...), nil), "gh:long")
	if len(s.Series) != maxSeries || s.Series[len(s.Series)-1] != 0 {
		t.Fatalf("series %v", s.Series)
	}
	// A long error is cut to one bounded line.
	h = runs("gh:err", -1, -1)
	h[1].Error = strings.Repeat("timeout\n", 500)
	if s := signalOf(t, Signals(h, nil), "gh:err"); len([]rune(s.LastError)) > maxErrorRunes || strings.Contains(s.LastError, "\n") {
		t.Fatalf("error %q", s.LastError)
	}
}

func TestRunTrendIsOldestFirstAndOmitsZeros(t *testing.T) {
	got := RunTrend([]RunSummary{
		{At: t0.Add(time.Hour), Configured: 467, Read: 464, Failed: 3, Unrecorded: 60, FailOpen: map[string]int{"coverage_unknown": 213, "other": 0}},
		{At: t0, Configured: 451, Read: 400},
	})
	if len(got) != 2 || !strings.HasPrefix(got[0], "2026-11-06T12:00Z configured=451") ||
		!strings.Contains(got[1], "unrecorded=60") || !strings.Contains(got[1], "fail_open[coverage_unknown=213]") {
		t.Fatalf("%q", got)
	}
}
