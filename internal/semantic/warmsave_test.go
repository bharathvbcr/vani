package semantic

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// saveCounter records every score save of a Matcher: how many, and the bytes
// each one wrote (the score file's size right after the save).
type saveCounter struct {
	saves int
	bytes int64
	onNth map[int]func()
}

func (c *saveCounter) attach(m *Matcher) {
	m.saveTick = func(n int64) {
		c.saves++
		c.bytes += n
		if f := c.onNth[c.saves]; f != nil {
			f()
		}
	}
}

// prefillScores writes n synthetic entries to the score cache of m's dir, the
// way a long-lived cache looks to the next Warm.
func prefillScores(t *testing.T, m *Matcher, n int) {
	t.Helper()
	m.mu.Lock()
	for i := 0; i < n; i++ {
		m.sf.Entries[fmt.Sprintf("old%06d", i)] = &entry{TextHash: fmt.Sprintf("%032x", i), Raw: 0.5, Score: 0.5, Seq: m.sf.NextSeq}
		m.sf.NextSeq++
	}
	m.mu.Unlock()
	if err := m.save(&Report{}, nil); err != nil {
		t.Fatal(err)
	}
}

// TestWarmSaveCountsAreBounded measures what a Warm writes to the score cache,
// by counts (the host is too loaded for wall time): a cache pre-filled with
// 20000 entries (about 2.2 MB here; 22.4 MB live, so scale bytes by about 10) and a warm of 1200 postings
// (about 150 embed groups) under the fake embedder.
//
// Measured before the change (saving every 8 groups): 25 saves, 54,946,821
// bytes written. After (saving every warmSaveEvery = 500 scored postings, and
// at the end): 3 saves, 6,954,559 bytes written.
func TestWarmSaveCountsAreBounded(t *testing.T) {
	const prefill, postings = 20000, 1200
	m, _ := newMatcher(t, newSpy())
	prefillScores(t, m, prefill)
	var c saveCounter
	c.attach(m)
	rep, err := m.Warm(context.Background(), vecDocs(postings), 5000)
	if err != nil || rep.Scored != postings {
		t.Fatalf("%+v %v", rep, err)
	}
	t.Logf("warm of %d postings over %d cached entries: %d saves, %d bytes written", postings, prefill, c.saves, c.bytes)
	if want := postings/warmSaveEvery + 1; c.saves > want {
		t.Errorf("%d saves for %d postings, want at most %d (one per %d scored, and the last)", c.saves, postings, want, warmSaveEvery)
	}
	if c.saves < 1 {
		t.Errorf("a warm that scored postings must save")
	}
	if m2, err := NewMatcher(newSpy(), synthProfile(t), m.dir); err != nil || m2.Len() != prefill+postings {
		t.Errorf("the saved cache holds %d entries, want %d (%v)", m2.Len(), prefill+postings, err)
	}
}

// A warm with fewer postings than the interval saves exactly once, and a warm
// that scores nothing does not save.
func TestWarmSaveSmallAndEmpty(t *testing.T) {
	m, _ := newMatcher(t, newSpy())
	var c saveCounter
	c.attach(m)
	if _, err := m.Warm(context.Background(), nil, 10); err != nil || c.saves != 0 {
		t.Fatalf("empty warm: %d saves, %v", c.saves, err)
	}
	if rep, err := m.Warm(context.Background(), vecDocs(40), 100); err != nil || rep.Scored != 40 || c.saves != 1 {
		t.Fatalf("%+v %d saves, %v", rep, c.saves, err)
	}
	// Everything cached now: nothing scored, nothing saved.
	if _, err := m.Warm(context.Background(), vecDocs(40), 100); err != nil || c.saves != 1 {
		t.Fatalf("cached warm saved again: %d saves, %v", c.saves, err)
	}
}

// A cancelled warm still saves what it scored, even when no interval save has
// happened yet.
func TestWarmCancelSavesScoredBetweenIntervals(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = 1 << 20
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sp := newSpy()
	sp.hook = func(_ context.Context, _ []string, _ Kind, call int) ([][]float32, error, bool) {
		if call == 6 {
			cancel()
		}
		return nil, nil, false
	}
	m.e = sp
	var c saveCounter
	c.attach(m)
	rep, err := m.Warm(ctx, vecDocs(300), 1000)
	if !errors.Is(err, context.Canceled) || rep.Scored == 0 || rep.Scored == 300 {
		t.Fatalf("%+v %v", rep, err)
	}
	if c.saves != 1 {
		t.Errorf("%d saves, want the one on the way out", c.saves)
	}
	m2, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil || m2.Len() != rep.Scored {
		t.Errorf("saved %d, scored %d (%v)", m2.Len(), rep.Scored, err)
	}
}

// A crash between saves loses at most the postings scored since the last save
// (under the interval plus one embed group), and a later Warm rebuilds them
// from the vector log with no embedding.
func TestWarmCrashBetweenSavesLosesBoundedAndRecovers(t *testing.T) {
	const every, postings = 20, 100
	docs := vecDocs(postings)
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = every
	var c saveCounter
	c.attach(m)
	scoresPath := filepath.Join(dir, scoresFileName)
	var snap []byte
	c.onNth = map[int]func(){2: func() {
		b, err := os.ReadFile(scoresPath)
		if err != nil {
			t.Fatal(err)
		}
		snap = b
	}}
	if rep, err := m.Warm(context.Background(), docs, 1000); err != nil || rep.Scored != postings {
		t.Fatalf("%+v %v", rep, err)
	}
	if snap == nil {
		t.Fatalf("only %d saves", c.saves)
	}
	// The process dies after the second save: the score file is as it was then.
	if err := os.WriteFile(scoresPath, snap, 0o600); err != nil {
		t.Fatal(err)
	}
	m2, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	lost := postings - m2.Len()
	if lost <= 0 || lost > every+MaxBatch {
		t.Fatalf("lost %d scores, want 1..%d", lost, every+MaxBatch)
	}
	sp := newSpy()
	m2.e = sp
	rep, err := m2.Warm(context.Background(), docs, 1000)
	if err != nil || rep.Rescored != lost || rep.Scored != 0 || rep.Cached != postings-lost {
		t.Fatalf("recovery: %+v %v", rep, err)
	}
	if n := sp.count(); n != 0 {
		t.Errorf("recovery embedded %d times, want 0", n)
	}
	assertSame(t, matchesOf(t, m2, docs), oracle(t, synthProfile(t), docs))
}
