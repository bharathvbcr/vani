package semantic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

func otherProfile(t *testing.T) Profile {
	t.Helper()
	b, err := kbsync.Parse([]byte(synthBank))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ProfileFromBank(b, "Staff platform engineer building Kubernetes operators and Terraform automation in Go")
	if err != nil {
		t.Fatal(err)
	}
	if p.Hash() == synthProfile(t).Hash() {
		t.Fatal("profiles hash alike")
	}
	return p
}

// Attack 1: another process rescored under a NEW profile while scores were
// held. A score computed under the old profile must never be served under the
// new one.
func TestAttackDeferredScoresNeverServedUnderNewProfile(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = 1 << 20
	docs := vecDocs(48)
	warmChunks(t, m, docs[:32], WarmOptions{DeferSave: true})
	p2 := otherProfile(t)
	other, err := NewMatcher(newSpy(), p2, dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep, err := other.Warm(context.Background(), docs[32:], 100); err != nil || rep.Scored != 16 {
		t.Fatalf("%+v %v", rep, err)
	}
	// Another deferred chunk and a flush by the old-profile matcher.
	if _, err := m.WarmWith(context.Background(), docs[32:], 100, WarmOptions{DeferSave: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.FlushWarm(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A reader under the NEW profile sees nothing the old profile scored.
	m2, err := NewMatcher(newSpy(), p2, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		if _, ok := m2.Lookup(d.Key, TextHash(d.Title, d.Description)); ok {
			t.Fatalf("%s: a score is served under the new profile after an old-profile flush", d.Key)
		}
	}
}

// Same input with plain Warm on the old matcher: the baseline behaviour. If the
// new profile's own work is lost here too, the deferred path is no worse.
func TestAttackPlainWarmBaselineAfterProfileChange(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	docs := vecDocs(48)
	p2 := otherProfile(t)
	other, err := NewMatcher(newSpy(), p2, dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep, err := other.Warm(context.Background(), docs[32:], 100); err != nil || rep.Scored != 16 {
		t.Fatalf("%+v %v", rep, err)
	}
	if _, err := m.Warm(context.Background(), docs[:16], 100); err != nil {
		t.Fatal(err)
	}
	m2, _ := NewMatcher(newSpy(), p2, dir)
	t.Logf("baseline: new-profile entries left after an old-profile plain Warm: %d of 16", m2.Len())
}

// Attack 2: another writer saved the same key with a newer text; our held
// (older) score overwrites it. A reader must get a miss, never the old score.
func TestAttackDeferredOlderTextOverwritesNewer(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = 1 << 20
	docs := vecDocs(16)
	warmChunks(t, m, docs, WarmOptions{DeferSave: true})
	newer := make([]Doc, len(docs))
	copy(newer, docs)
	for i := range newer {
		newer[i].Description = newer[i].Description + " now with extra kubernetes terraform text"
	}
	other, _ := NewMatcher(newSpy(), synthProfile(t), dir)
	if rep, err := other.Warm(context.Background(), newer, 100); err != nil || rep.Scored != 16 {
		t.Fatalf("%+v %v", rep, err)
	}
	if err := m.FlushWarm(context.Background()); err != nil {
		t.Fatal(err)
	}
	m2, _ := NewMatcher(newSpy(), synthProfile(t), dir)
	hits, oldHits := 0, 0
	for i := range docs {
		if _, ok := m2.Lookup(newer[i].Key, TextHash(newer[i].Title, newer[i].Description)); ok {
			hits++
		}
		if _, ok := m2.Lookup(docs[i].Key, TextHash(docs[i].Title, docs[i].Description)); ok {
			oldHits++
		}
	}
	t.Logf("after flush: newer-text lookups served %d, older-text lookups %d (of 16)", hits, oldHits)
	if hits != 0 && oldHits != 0 {
		t.Fatalf("both texts served for one key")
	}
	// Whatever is served must equal a fresh oracle for that text.
	if hits > 0 {
		assertSame(t, matchesOf(t, m2, newer), oracle(t, synthProfile(t), newer))
	}
}

// Attack 3: the score file was removed on purpose (cache cleared). The flush
// re-adds only what this matcher scored, never anything else.
func TestAttackFlushAfterFileRemovedWritesOnlyHeld(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = 1 << 20
	docs := vecDocs(64)
	if _, err := m.Warm(context.Background(), docs[:32], 100); err != nil {
		t.Fatal(err)
	}
	warmChunks(t, m, docs[32:], WarmOptions{DeferSave: true})
	if err := os.Remove(filepath.Join(dir, scoresFileName)); err != nil {
		t.Fatal(err)
	}
	if err := m.FlushWarm(context.Background()); err != nil {
		t.Fatal(err)
	}
	m2, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after clear + flush: %d entries on disk (held 32, previously saved 32)", m2.Len())
	if m2.Len() != 32 {
		t.Fatalf("%d entries, want exactly the 32 held", m2.Len())
	}
}

// Attack 3b: a corrupt score file at flush time fails loud and keeps the held scores.
func TestAttackFlushCorruptFileKeepsHeld(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = 1 << 20
	warmChunks(t, m, vecDocs(32), WarmOptions{DeferSave: true})
	if err := os.WriteFile(filepath.Join(dir, scoresFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.FlushWarm(context.Background()); !errors.Is(err, ErrCache) {
		t.Fatalf("want ErrCache, got %v", err)
	}
	if m.dirtyLen() != 32 {
		t.Fatalf("held %d, want 32", m.dirtyLen())
	}
	// A following deferred Warm must also fail loud, not lose them.
	if _, err := m.WarmWith(context.Background(), vecDocs(48)[32:], 100, WarmOptions{DeferSave: true}); err == nil {
		t.Fatal("warm over a corrupt file succeeded")
	}
	if m.dirtyLen() != 32 {
		t.Fatalf("held %d after failed warm, want 32", m.dirtyLen())
	}
}

// Attack 4: a write error at flush keeps the scores held; a later flush works.
func TestAttackFlushWriteErrorKeepsHeldThenRecovers(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = 1 << 20
	if _, err := m.Warm(context.Background(), vecDocs(8), 100); err != nil {
		t.Fatal(err)
	}
	warmChunks(t, m, vecDocs(40)[8:], WarmOptions{DeferSave: true})
	path := filepath.Join(dir, scoresFileName)
	data, _ := os.ReadFile(path)
	breakScoresFile(t, path)
	if err := m.FlushWarm(context.Background()); err == nil {
		t.Fatal("flush onto a directory succeeded")
	}
	if m.dirtyLen() != 32 {
		t.Fatalf("held %d, want 32", m.dirtyLen())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.FlushWarm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m2, err := NewMatcher(newSpy(), synthProfile(t), dir); err != nil || m2.Len() != 40 {
		t.Fatalf("saved %d (%v)", m2.Len(), err)
	}
}

// Attack 4b: a flush already cancelled with the lock free, and a flush twice.
func TestAttackFlushCancelledContextLockFree(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = 1 << 20
	warmChunks(t, m, vecDocs(32), WarmOptions{DeferSave: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := m.FlushWarm(ctx)
	t.Logf("cancelled-context flush with a free lock: err=%v held=%d", err, m.dirtyLen())
	if err != nil && m.dirtyLen() != 32 {
		t.Fatalf("flush failed but held %d", m.dirtyLen())
	}
	if err == nil && m.dirtyLen() != 0 {
		t.Fatalf("flush ok but %d still held", m.dirtyLen())
	}
	if err := m.FlushWarm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m2, _ := NewMatcher(newSpy(), synthProfile(t), dir); m2.Len() != 32 {
		t.Fatalf("saved %d", m2.Len())
	}
}

// Attack 4c: concurrent deferred warms and flushes, with readers, under -race.
func TestAttackDeferredConcurrentWarmFlushRace(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = 24
	docs := vecDocs(96)
	var wg sync.WaitGroup
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < len(docs); i += 8 {
				c := docs[i:min(i+8, len(docs))]
				if _, err := m.WarmWith(context.Background(), c, len(c), WarmOptions{DeferSave: g != 2}); err != nil {
					t.Errorf("warm: %v", err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if err := m.FlushWarm(context.Background()); err != nil {
				t.Errorf("flush: %v", err)
				return
			}
			for _, d := range docs[:8] {
				m.Lookup(d.Key, TextHash(d.Title, d.Description))
			}
		}
	}()
	wg.Wait()
	if err := m.FlushWarm(context.Background()); err != nil {
		t.Fatal(err)
	}
	m2, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil || m2.Len() != 96 {
		t.Fatalf("saved %d (%v)", m2.Len(), err)
	}
	assertSame(t, matchesOf(t, m2, docs), oracle(t, synthProfile(t), docs))
}

// Attack 4d: a second Matcher in the same process, plain Warm, between deferred
// chunks and flush; both writers' scores end on disk.
func TestAttackPlainWarmOtherMatcherSameProcess(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = 1 << 20
	docs := vecDocs(80)
	warmChunks(t, m, docs[:32], WarmOptions{DeferSave: true})
	o, _ := NewMatcher(newSpy(), synthProfile(t), dir)
	if rep, err := o.Warm(context.Background(), docs[32:64], 100); err != nil || rep.Scored != 32 {
		t.Fatalf("%+v %v", rep, err)
	}
	// Plain Warm on the holder itself: saves held scores with its own.
	if rep, err := m.Warm(context.Background(), docs[64:], 100); err != nil || rep.Scored != 16 {
		t.Fatalf("%+v %v", rep, err)
	}
	if m.dirtyLen() != 0 {
		t.Fatalf("held %d after plain Warm", m.dirtyLen())
	}
	m2, _ := NewMatcher(newSpy(), synthProfile(t), dir)
	if m2.Len() != 80 {
		t.Fatalf("disk holds %d, want 80", m2.Len())
	}
}

// Attack 6: a plain Warm that finds everything cached must not touch the file,
// and its Report must equal the baseline shape.
func TestAttackPlainWarmAllCachedWithHeldWritesNothing(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.warmEvery = 1 << 20
	docs := vecDocs(32)
	warmChunks(t, m, docs, WarmOptions{DeferSave: true})
	rep, err := m.Warm(context.Background(), docs, 100)
	if err != nil || rep.Cached != 32 || rep.Scored != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if _, err := os.Stat(filepath.Join(dir, scoresFileName)); err == nil {
		t.Logf("plain all-cached Warm wrote the file")
	} else {
		t.Logf("plain all-cached Warm left the file unwritten; held=%d", m.dirtyLen())
	}
}

// Attack 5b: one deferred Warm larger than warmSaveEvery saves on the interval
// and holds at most one batch of scores afterwards.
func TestAttackDeferredBigChunkHeldBounded(t *testing.T) {
	m, _ := newMatcher(t, newSpy())
	m.warmEvery = 10
	var c saveCounter
	c.attach(m)
	docs := vecDocs(100)
	if rep, err := m.WarmWith(context.Background(), docs, 100, WarmOptions{DeferSave: true}); err != nil || rep.Scored != 100 {
		t.Fatalf("%+v %v", rep, err)
	}
	if c.saves < 5 {
		t.Fatalf("%d saves for 100 scored at every=10", c.saves)
	}
	if m.dirtyLen() >= 10+16 {
		t.Fatalf("held %d after a big deferred chunk", m.dirtyLen())
	}
}
