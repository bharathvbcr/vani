package semantic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/filelock"
)

// Two warms of one directory, each from its own matcher (as the daemon and a
// manual `vani semantic warm` would be), both finish and the file holds both
// writers' postings. Without the lock and the reload under it, the later save
// overwrites the earlier one.
func TestConcurrentWarmsKeepBothWritersEntries(t *testing.T) {
	_, dir := newMatcher(t, newSpy())
	a, err := NewMatcher(NewFakeEmbedder(256, synonyms), synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewMatcher(NewFakeEmbedder(256, synonyms), synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(prefix string) []Doc {
		var docs []Doc
		for i := 0; i < 20; i++ {
			docs = append(docs, Doc{Key: prefix + string(rune('a'+i)), Title: "Engineer " + prefix + string(rune('a'+i)),
				Description: "Operate k8s clusters and build golang services number " + string(rune('a'+i))})
		}
		return docs
	}
	da, db := mk("x"), mk("y")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	reps := make([]Report, 2)
	for i, w := range []struct {
		m    *Matcher
		docs []Doc
	}{{a, da}, {b, db}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reps[i], errs[i] = w.m.Warm(context.Background(), w.docs, 1000)
		}()
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil || reps[i].Scored != 20 {
			t.Fatalf("warm %d: %+v %v", i, reps[i], errs[i])
		}
	}
	lk, err := OpenLookup(synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range append(da, db...) {
		if _, ok := lk.Lookup(d.Key, TextHash(d.Title, d.Description)); !ok {
			t.Errorf("posting %s lost by the other writer", d.Key)
		}
	}
	if lk.Len() != 40 {
		t.Errorf("cache holds %d postings, want 40", lk.Len())
	}
}

// A lock held past the bound is a skipped warm: an error and a report that
// says nothing was scored, never a pass, and the embedder is not called.
func TestWarmSkippedWhileAnotherWarmHoldsTheLock(t *testing.T) {
	s := newSpy()
	m, dir := newMatcher(t, s)
	m.lockWait = 120 * time.Millisecond

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := filelock.TryLock(held, true); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	rep, err := m.Warm(context.Background(), []Doc{k8sDoc(), pastryDoc(), {Title: "no key"}}, 10)
	if !errors.Is(err, ErrWarmBusy) || !strings.Contains(rep.Err, "another warm is running") {
		t.Fatalf("got %+v, %v; want ErrWarmBusy", rep, err)
	}
	if rep.Scored != 0 || rep.Skipped != 2 || rep.Failed != 1 {
		t.Errorf("report %+v: want skipped 2, failed 1 (the keyless one), scored 0", rep)
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Errorf("gave up after %v, before the wait", time.Since(start))
	}
	if s.count() != 0 {
		t.Errorf("embedder called %d times while skipped", s.count())
	}

	// Released: the same matcher warms.
	if err := filelock.Unlock(held); err != nil {
		t.Fatal(err)
	}
	held.Close()
	if rep := mustWarm(t, m, k8sDoc()); rep.Scored != 1 {
		t.Errorf("after release: %+v", rep)
	}
}

func TestWarmLockWaitHonorsContext(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.lockWait = time.Minute
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := filelock.TryLock(held, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	rep, err := m.Warm(ctx, []Doc{k8sDoc()}, 10)
	if !errors.Is(err, context.DeadlineExceeded) || !rep.Canceled || rep.Skipped != 1 {
		t.Fatalf("got %+v, %v", rep, err)
	}
}

func TestStaleScores(t *testing.T) {
	if n, err := StaleScores(synthProfile(t), filepath.Join(t.TempDir(), "none")); err != nil || n != 0 {
		t.Errorf("missing cache: %d, %v", n, err)
	}
	m, dir := newMatcher(t, newSpy())
	mustWarm(t, m, k8sDoc(), pastryDoc())
	if n, err := StaleScores(synthProfile(t), dir); err != nil || n != 0 {
		t.Errorf("same profile: %d, %v", n, err)
	}
	other := synthProfile(t)
	other.Chunks = other.Chunks[1:]
	if other.Hash() == synthProfile(t).Hash() {
		t.Fatal("dropping a chunk must change the profile hash")
	}
	if n, err := StaleScores(other, dir); err != nil || n != 2 {
		t.Errorf("other profile: %d, %v, want 2", n, err)
	}
	if err := os.WriteFile(filepath.Join(dir, scoresFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StaleScores(other, dir); !errors.Is(err, ErrCache) {
		t.Errorf("malformed cache: %v, want ErrCache", err)
	}
}

func TestOpenLookupForModelPrefix(t *testing.T) {
	s := newSpy()
	m, dir := newMatcher(t, s)
	mustWarm(t, m, k8sDoc())
	model := m.Model()
	doc := k8sDoc()
	th := TextHash(doc.Title, doc.Description)

	lk, err := OpenLookupFor(synthProfile(t), dir, model[:8])
	if err != nil {
		t.Fatalf("matching prefix: %v", err)
	}
	if _, ok := lk.Lookup(doc.Key, th); !ok {
		t.Error("matching prefix: posting not found")
	}
	if _, err := OpenLookupFor(synthProfile(t), dir, "tessl/embeddinggemma-2@new-snapshot/f32/d"); !errors.Is(err, ErrModelMismatch) {
		t.Errorf("mismatching prefix: %v, want ErrModelMismatch", err)
	}
	if lk, err := OpenLookupFor(synthProfile(t), dir, ""); err != nil || lk.Len() != 1 {
		t.Errorf("empty prefix accepts any model: %v", err)
	}
	// Nothing cached: nothing to mislead, so any prefix opens.
	if lk, err := OpenLookupFor(synthProfile(t), filepath.Join(t.TempDir(), "none"), "tessl/x"); err != nil || lk.Len() != 0 {
		t.Errorf("empty cache: %v", err)
	}
}

func TestTesslModelPrefixIsTheIDStem(t *testing.T) {
	c := TesslConfig{Snapshot: "/models/hub/embeddinggemma-2/snapshots/abc123/"}
	want := "tessl/embeddinggemma-2@abc123/f32/d"
	if got := c.ModelPrefix(); got != want {
		t.Errorf("prefix %q, want %q", got, want)
	}
	if got := (TesslConfig{}).ModelPrefix(); got != "" {
		t.Errorf("no snapshot: %q, want empty", got)
	}
}
