package semantic

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A kept Matcher is returned while nothing it was opened from changed, and
// the file is read again once it has: a warm that adds a score is seen by the
// next Open.
func TestLookupsKeepsTheMatcherUntilTheFileChanges(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	mustWarm(t, m, k8sDoc())
	var l Lookups
	a, err := l.Open(synthProfile(t), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.Open(synthProfile(t), dir, "")
	if err != nil || a != b || l.Opens() != 1 {
		t.Fatalf("unchanged file: same matcher %v, opens %d, err %v", a == b, l.Opens(), err)
	}
	if _, ok := b.Lookup(pastryDoc().Key, TextHash(pastryDoc().Title, pastryDoc().Description)); ok {
		t.Fatal("setup: pastry is scored before its warm")
	}

	mustWarm(t, m, pastryDoc())
	c, err := l.Open(synthProfile(t), dir, "")
	if err != nil || c == b || l.Opens() != 2 {
		t.Fatalf("rewritten file: new matcher %v, opens %d, err %v", c != b, l.Opens(), err)
	}
	if _, ok := c.Lookup(pastryDoc().Key, TextHash(pastryDoc().Title, pastryDoc().Description)); !ok {
		t.Fatal("the score the warm added is not seen")
	}

	// Another directory or model prefix is another open, not the kept one.
	if _, err := l.Open(synthProfile(t), dir, "tessl/another-model"); !errors.Is(err, ErrModelMismatch) {
		t.Fatalf("a mismatching prefix: %v", err)
	}
	if d, err := l.Open(synthProfile(t), dir, ""); err != nil || d == c || l.Opens() != 3 {
		t.Fatalf("after a failed open nothing is kept: reused %v, opens %d, err %v", d == c, l.Opens(), err)
	}
}

// No score file is an empty matcher, and the file appearing is a change.
func TestLookupsSeesAFileThatAppears(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	var l Lookups
	a, err := l.Open(synthProfile(t), dir, "")
	if err != nil || a.Len() != 0 {
		t.Fatalf("no file: %v, %d scores", err, a.Len())
	}
	mustWarm(t, m, k8sDoc())
	b, err := l.Open(synthProfile(t), dir, "")
	if err != nil || b.Len() != 1 {
		t.Fatalf("after the first warm: %v, %d scores", err, b.Len())
	}
}

// A damaged file is reported every time, never answered from a kept matcher.
func TestLookupsReportsADamagedFile(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	mustWarm(t, m, k8sDoc())
	var l Lookups
	if _, err := l.Open(synthProfile(t), dir, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, scoresFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := l.Open(synthProfile(t), dir, ""); !errors.Is(err, ErrCache) {
			t.Fatalf("a damaged file: %v, want ErrCache", err)
		}
	}
}

func TestLookupsIsSafeForConcurrentUse(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	mustWarm(t, m, k8sDoc())
	var l Lookups
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				lk, err := l.Open(synthProfile(t), dir, "")
				if err != nil {
					t.Error(err)
					return
				}
				lk.Lookup(k8sDoc().Key, TextHash(k8sDoc().Title, k8sDoc().Description))
			}
		})
	}
	wg.Wait()
	if l.Opens() != 1 {
		t.Fatalf("opens %d, want 1", l.Opens())
	}
}
