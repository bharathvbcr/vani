package semantic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const deferChunk = 16

// warmChunks warms docs in chunks of deferChunk the way the idle warm does.
func warmChunks(t *testing.T, m *Matcher, docs []Doc, opt WarmOptions) {
	t.Helper()
	for i := 0; i < len(docs); i += deferChunk {
		c := docs[i:min(i+deferChunk, len(docs))]
		if rep, err := m.WarmWith(context.Background(), c, len(c), opt); err != nil || rep.Scored != len(c) {
			t.Fatalf("chunk at %d: %+v %v", i, rep, err)
		}
	}
}

// TestIdleChunkedWarmSaveCounts is the count the idle warm is about: 1200
// postings warmed as 75 chunks of 16 over a cache of 20000 entries (about 2.2
// MB here; 22.4 MB live, so scale bytes by about 10).
//
// Measured with one Warm per chunk (what the idle warm did on 65199eb, where
// each call saves once at its end): 75 saves, 163,009,094 bytes written.
// Deferred across chunks (every 500 scored, and the flush): see the log line.
func TestIdleChunkedWarmSaveCounts(t *testing.T) {
	const prefill, postings = 20000, 1200
	docs := vecDocs(postings)
	for _, tc := range []struct {
		name  string
		opt   WarmOptions
		flush bool
		max   int
	}{
		{"per-chunk saves", WarmOptions{}, false, 75},
		{"deferred", WarmOptions{DeferSave: true}, true, postings/warmSaveEvery + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, dir := newMatcher(t, newSpy())
			prefillScores(t, m, prefill)
			var c saveCounter
			c.attach(m)
			warmChunks(t, m, docs, tc.opt)
			if tc.flush {
				if err := m.FlushWarm(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("%s: %d saves, %d bytes written", tc.name, c.saves, c.bytes)
			if c.saves > tc.max {
				t.Errorf("%d saves, want at most %d", c.saves, tc.max)
			}
			if m2, err := NewMatcher(newSpy(), synthProfile(t), dir); err != nil || m2.Len() != prefill+postings {
				t.Errorf("saved cache holds %d entries, want %d (%v)", m2.Len(), prefill+postings, err)
			}
		})
	}
}

// Deferred chunks that end cleanly write nothing until the interval or the
// flush; the flush is a no-op when nothing is held.
func TestDeferSaveHoldsUntilIntervalOrFlush(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = 50
	var c saveCounter
	c.attach(m)
	if err := m.FlushWarm(context.Background()); err != nil || c.saves != 0 {
		t.Fatalf("empty flush: %d saves, %v", c.saves, err)
	}
	docs := vecDocs(64)
	warmChunks(t, m, docs[:32], WarmOptions{DeferSave: true})
	if c.saves != 0 {
		t.Fatalf("%d saves under the interval", c.saves)
	}
	if _, err := os.Stat(filepath.Join(dir, scoresFileName)); err == nil {
		t.Fatalf("score file written before any save")
	}
	warmChunks(t, m, docs[32:], WarmOptions{DeferSave: true})
	if c.saves != 1 {
		t.Fatalf("%d saves after crossing the interval, want 1", c.saves)
	}
	if err := m.FlushWarm(context.Background()); err != nil || c.saves != 2 {
		t.Fatalf("flush: %d saves, %v", c.saves, err)
	}
	if err := m.FlushWarm(context.Background()); err != nil || c.saves != 2 {
		t.Fatalf("second flush saved again: %d saves, %v", c.saves, err)
	}
	if m2, err := NewMatcher(newSpy(), synthProfile(t), dir); err != nil || m2.Len() != 64 {
		t.Fatalf("saved %d (%v)", m2.Len(), err)
	}
}

// A crash between deferred saves loses at most the interval plus one chunk of
// scores, and the next warm rebuilds them from the vector log with no
// embedding.
func TestDeferSaveCrashLosesBoundedAndRecovers(t *testing.T) {
	const every, postings = 40, 200
	docs := vecDocs(postings)
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = every
	// The process dies after the last chunk, before any flush: the score file is
	// as the last interval save left it.
	warmChunks(t, m, docs, WarmOptions{DeferSave: true})
	m2, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	lost := postings - m2.Len()
	if lost <= 0 || lost > every+deferChunk {
		t.Fatalf("lost %d scores, want 1..%d", lost, every+deferChunk)
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

// A chunk cut short by a cancel saves what it scored and what the earlier
// deferred chunks still held.
func TestDeferSaveCancelSavesEverythingScored(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = 1 << 20
	docs := vecDocs(200)
	warmChunks(t, m, docs[:48], WarmOptions{DeferSave: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sp := newSpy()
	sp.hook = func(_ context.Context, _ []string, _ Kind, call int) ([][]float32, error, bool) {
		if call == 3 {
			cancel()
		}
		return nil, nil, false
	}
	m.e = sp
	var c saveCounter
	c.attach(m)
	rep, err := m.WarmWith(ctx, docs[48:], 1000, WarmOptions{DeferSave: true})
	if !errors.Is(err, context.Canceled) || rep.Scored == 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if c.saves != 1 {
		t.Errorf("%d saves, want the one on the way out", c.saves)
	}
	if m2, err := NewMatcher(newSpy(), synthProfile(t), dir); err != nil || m2.Len() != 48+rep.Scored {
		t.Errorf("saved %d, scored %d (%v)", m2.Len(), 48+rep.Scored, err)
	}
	if m.dirtyLen() != 0 {
		t.Errorf("%d scores still held after the save", m.dirtyLen())
	}
}

// Another process saving between deferred chunks loses nothing: its scores
// survive the next reload and the flush, and so do the deferred ones.
func TestDeferSaveKeepsWhatAnotherWriterSaved(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = 1 << 20
	docs := vecDocs(120)
	warmChunks(t, m, docs[:32], WarmOptions{DeferSave: true})
	other, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep, err := other.Warm(context.Background(), docs[32:64], 100); err != nil || rep.Scored != 32 {
		t.Fatalf("%+v %v", rep, err)
	}
	// A deferred chunk reloads the file: the other's 32 arrive, ours stay held.
	warmChunks(t, m, docs[64:96], WarmOptions{DeferSave: true})
	for i, d := range docs[:96] {
		if _, ok := m.Lookup(d.Key, TextHash(d.Title, d.Description)); !ok {
			t.Fatalf("posting %d missing in memory after the reload", i)
		}
	}
	// The other writes again just before the flush.
	if rep, err := other.Warm(context.Background(), docs[96:], 100); err != nil || rep.Scored != 24 {
		t.Fatalf("%+v %v", rep, err)
	}
	if err := m.FlushWarm(context.Background()); err != nil {
		t.Fatal(err)
	}
	m2, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil || m2.Len() != 120 {
		t.Fatalf("saved %d, want 120 (%v)", m2.Len(), err)
	}
	assertSame(t, matchesOf(t, m2, docs), oracle(t, synthProfile(t), docs))
}

// A flush that cannot take the lock reports it and keeps the scores held for
// the next try; a later flush saves them.
func TestFlushWarmLockFailureKeepsScores(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = 1 << 20
	warmChunks(t, m, vecDocs(32), WarmOptions{DeferSave: true})
	unlock, err := lockCache(context.Background(), dir, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.FlushWarm(ctx); err == nil {
		t.Fatal("flush under a held lock and a dead context succeeded")
	}
	unlock()
	if m.dirtyLen() != 32 {
		t.Fatalf("%d scores held, want 32", m.dirtyLen())
	}
	if err := m.FlushWarm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m2, err := NewMatcher(newSpy(), synthProfile(t), dir); err != nil || m2.Len() != 32 {
		t.Fatalf("saved %d (%v)", m2.Len(), err)
	}
}
