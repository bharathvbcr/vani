package semantic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// breakScoresFile makes the next score save fail: a directory sits where the
// atomic rename must land.
func breakScoresFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

// Attack: the final save fails after mid-loop saves succeeded. The error must
// reach the caller as ErrCache, Scored stays the full count, and the entries
// already saved mid-loop are still on disk.
func TestAttackWarmFinalSaveFailsAfterMidSaves(t *testing.T) {
	const every, postings = 20, 100
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = every
	path := filepath.Join(dir, scoresFileName)
	var c saveCounter
	c.attach(m)
	var midBytes int64
	c.onNth = map[int]func(){}
	// Break the path right after the last mid-loop save we can predict: save 2
	// leaves a tail unsaved only when the tail is shorter than a full interval,
	// so break after save 2 and let the next (final or mid) one fail.
	c.onNth[2] = func() {
		if fi, err := os.Stat(path); err == nil {
			midBytes = fi.Size()
		}
		os.Remove(path)
		_ = os.Mkdir(path, 0o700)
	}
	rep, err := m.Warm(context.Background(), vecDocs(postings), 1000)
	if !errors.Is(err, ErrCache) {
		t.Fatalf("want ErrCache from the failing save, got %v (rep %+v)", err, rep)
	}
	if midBytes == 0 {
		t.Fatalf("hook did not fire (%d saves)", c.saves)
	}
	if got := rep.Scored + rep.Skipped + rep.Failed; got != postings {
		t.Errorf("accounting: scored %d + skipped %d + failed %d = %d, want %d", rep.Scored, rep.Skipped, rep.Failed, got, postings)
	}
	if m.Len() == 0 {
		t.Errorf("in-memory scores were dropped on a save failure")
	}
}

// Attack: a mid-loop save fails. Warm returns at once; the accounting must
// still add up to the postings handed in.
func TestAttackWarmMidLoopSaveFailureAccounting(t *testing.T) {
	const every, postings = 20, 100
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = every
	path := filepath.Join(dir, scoresFileName)
	var c saveCounter
	c.attach(m)
	c.onNth = map[int]func(){1: func() { os.Remove(path); _ = os.Mkdir(path, 0o700) }}
	rep, err := m.Warm(context.Background(), vecDocs(postings), 1000)
	if !errors.Is(err, ErrCache) {
		t.Fatalf("%v %+v", err, rep)
	}
	if got := rep.Scored + rep.Skipped + rep.Failed; got != postings {
		t.Errorf("scored %d + skipped %d + failed %d = %d, want %d", rep.Scored, rep.Skipped, rep.Failed, got, postings)
	}
	if rep.Scored >= postings {
		t.Errorf("returned early expected, scored %d", rep.Scored)
	}
}

// Attack: a warm whose rescore phase saved and whose embed phase scored 0
// (every embed fails): no extra save beyond the rescore one, nothing lost.
func TestAttackWarmRescoreSavedThenEmbedScoresNothing(t *testing.T) {
	docs := vecDocs(60)
	m, dir := newMatcher(t, newSpy())
	if _, err := m.Warm(context.Background(), docs[:30], 100); err != nil {
		t.Fatal(err)
	}
	// Lose the scores, keep the vectors: 30 postings to rescore.
	if err := os.Remove(filepath.Join(dir, scoresFileName)); err != nil {
		t.Fatal(err)
	}
	down := newSpy()
	down.hook = func(_ context.Context, _ []string, k Kind, _ int) ([][]float32, error, bool) {
		if k == KindQuery {
			return nil, ErrEmbed, true
		}
		return nil, nil, false
	}
	m2, err := NewMatcher(down, synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	m2.warmEvery = 5
	var c saveCounter
	c.attach(m2)
	rep, _ := m2.Warm(context.Background(), docs, 100)
	if rep.Rescored != 30 {
		t.Fatalf("%+v", rep)
	}
	if c.saves != 1 {
		t.Errorf("%d saves, want exactly the rescore phase's one", c.saves)
	}
	m3, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil || m3.Len() != 30 {
		t.Errorf("saved %d, want 30 (%v)", m3.Len(), err)
	}
}

// Attack: cancellation exactly at a save boundary (inside the save tick). The
// boundary save has just happened; the exit path must not double save nor lose
// scores, and Skipped must account for the rest.
func TestAttackWarmCancelAtSaveBoundary(t *testing.T) {
	const every, postings = 20, 100
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = every
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var c saveCounter
	c.attach(m)
	c.onNth = map[int]func(){2: cancel}
	rep, err := m.Warm(ctx, vecDocs(postings), 1000)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%v %+v", err, rep)
	}
	if c.saves != 2 {
		t.Errorf("%d saves, want 2 (the boundary one, no extra)", c.saves)
	}
	if got := rep.Scored + rep.Skipped + rep.Failed; got != postings {
		t.Errorf("accounting %+v", rep)
	}
	m2, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil || m2.Len() != rep.Scored {
		t.Errorf("on disk %d, scored %d (%v)", m2.Len(), rep.Scored, err)
	}
}

func warmKeys(t *testing.T, every, maxEntries, postings int, live map[string]float64) map[string]bool {
	t.Helper()
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = every
	m.SetMaxEntries(maxEntries)
	if live != nil {
		m.SetVectorPriority(live)
	}
	if _, err := m.Warm(context.Background(), vecDocs(postings), postings); err != nil {
		t.Fatal(err)
	}
	m2, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for k := range m2.sf.Entries {
		out[k] = true
	}
	return out
}

// Attack: a warm of 3x maxEntries. Saving rarely must keep the same entries as
// saving after every group: the newest maxEntries, none of this warm's last
// postings lost.
func TestAttackWarmThreeTimesMaxEntriesSameSurvivorsAsFrequentSaves(t *testing.T) {
	const maxE, postings = 40, 120
	fine := warmKeys(t, 1, maxE, postings, nil)
	coarse := warmKeys(t, 500, maxE, postings, nil)
	if len(coarse) != maxE || len(fine) != maxE {
		t.Fatalf("sizes fine %d coarse %d, want %d", len(fine), len(coarse), maxE)
	}
	for k := range fine {
		if !coarse[k] {
			t.Errorf("%s survives frequent saves but not rare ones", k)
		}
	}
	// And with a live priority naming nothing of this warm's texts.
	fineL := warmKeys(t, 1, maxE, postings, map[string]float64{"x": 1})
	coarseL := warmKeys(t, 500, maxE, postings, map[string]float64{"x": 1})
	if len(fineL) != len(coarseL) {
		t.Fatalf("live: fine %d coarse %d", len(fineL), len(coarseL))
	}
	for k := range fineL {
		if !coarseL[k] {
			t.Errorf("live: %s survives frequent saves but not rare ones", k)
		}
	}
}

// Attack: an existing live entry vs this warm's postings under a tiny cap: a
// rare-save warm must not evict live ones that frequent saves would keep.
func TestAttackWarmLiveEntriesSurviveRareSaves(t *testing.T) {
	const maxE = 30
	docs := vecDocs(90)
	run := func(every int) map[string]bool {
		m, dir := newMatcher(t, newSpy())
		m.SetMaxEntries(maxE)
		prio := map[string]float64{}
		// Score 10 postings first and declare them live with high rank.
		if _, err := m.Warm(context.Background(), docs[:10], 10); err != nil {
			t.Fatal(err)
		}
		for _, d := range docs[:10] {
			prio[TextHash(d.Title, d.Description)] = 100
		}
		m.SetVectorPriority(prio)
		m.warmEvery = every
		if _, err := m.Warm(context.Background(), docs[10:], 80); err != nil {
			t.Fatal(err)
		}
		m2, err := NewMatcher(newSpy(), synthProfile(t), dir)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for k := range m2.sf.Entries {
			out[k] = true
		}
		return out
	}
	fine, coarse := run(1), run(500)
	if len(fine) != len(coarse) {
		t.Fatalf("fine %d coarse %d", len(fine), len(coarse))
	}
	for k := range fine {
		if !coarse[k] {
			t.Errorf("%s kept with frequent saves, lost with rare ones", k)
		}
	}
}

// Attack: two Warm calls and readers at once, under -race.
func TestAttackWarmConcurrentWarmAndReaders(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = 7
	docs := vecDocs(120)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, d := range docs[:20] {
					m.Lookup(d.Key, TextHash(d.Title, d.Description))
				}
				m.Len()
				if lm, err := OpenLookup(synthProfile(t), dir); err == nil {
					lm.Len()
				}
			}
		}()
	}
	var ww sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		ww.Add(1)
		go func() {
			defer ww.Done()
			_, errs[i] = m.Warm(context.Background(), docs, 1000)
		}()
	}
	ww.Wait()
	close(stop)
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			t.Errorf("warm: %v", e)
		}
	}
	m2, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil || m2.Len() != len(docs) {
		t.Errorf("on disk %d, want %d (%v)", m2.Len(), len(docs), err)
	}
}

// Attack: a crash between saves, then the vector log is compacted before the
// next Warm recovers: recovery must still rescore without embedding.
func TestAttackWarmCrashThenCompactThenRecover(t *testing.T) {
	const every, postings = 20, 100
	docs := vecDocs(postings)
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = every
	path := filepath.Join(dir, scoresFileName)
	var snap []byte
	var c saveCounter
	c.attach(m)
	c.onNth = map[int]func(){2: func() { snap, _ = os.ReadFile(path) }}
	if _, err := m.Warm(context.Background(), docs, 1000); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, snap, 0o600); err != nil {
		t.Fatal(err)
	}
	m2, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	prio := map[string]float64{}
	for _, d := range docs {
		prio[TextHash(d.Title, d.Description)] = 1
	}
	m2.SetVectorPriority(prio)
	if _, err := m2.CompactVectors(context.Background()); err != nil {
		t.Fatal(err)
	}
	lost := postings - m2.Len()
	sp := newSpy()
	m2.e = sp
	rep, err := m2.Warm(context.Background(), docs, 1000)
	if err != nil || rep.Rescored != lost || rep.Scored != 0 || sp.count() != 0 {
		t.Fatalf("recovery after compaction: %+v embeds %d %v", rep, sp.count(), err)
	}
}
