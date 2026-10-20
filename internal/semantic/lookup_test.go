package semantic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenLookupReturnsWhatWarmStored(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	mustWarm(t, m, k8sDoc())
	want, ok := m.Lookup(k8sDoc().Key, TextHash(k8sDoc().Title, k8sDoc().Description))
	if !ok {
		t.Fatal("warmed posting not found")
	}

	lk, err := OpenLookup(synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := lk.Lookup(k8sDoc().Key, TextHash(k8sDoc().Title, k8sDoc().Description))
	if !ok {
		t.Fatal("lookup matcher cannot see the warmed posting")
	}
	if got.Score != want.Score || got.Raw != want.Raw || got.Model != want.Model || len(got.Evidence) != len(want.Evidence) {
		t.Errorf("lookup %+v, warm %+v", got, want)
	}
	if lk.Model() == "" || lk.Model() != m.Model() {
		t.Errorf("model %q, want %q", lk.Model(), m.Model())
	}
	// Changed posting text is a different posting: not found.
	if _, ok := lk.Lookup(k8sDoc().Key, TextHash(k8sDoc().Title, k8sDoc().Description+" more")); ok {
		t.Error("a score survived a text change")
	}
}

func TestOpenLookupProfileChangeHidesScores(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	mustWarm(t, m, k8sDoc())
	p := synthProfile(t)
	p.Chunks = append(p.Chunks, Chunk{Label: "skills", Text: "Skills: Haskell", Weight: 1})
	lk, err := OpenLookup(p, dir)
	if err != nil {
		t.Fatal(err)
	}
	if lk.Len() != 0 || lk.Model() != "" {
		t.Errorf("old profile's scores visible: len %d model %q", lk.Len(), lk.Model())
	}
	if _, ok := lk.Lookup(k8sDoc().Key, TextHash(k8sDoc().Title, k8sDoc().Description)); ok {
		t.Error("score survived a profile change")
	}
}

func TestOpenLookupMissingCacheIsNotFoundNotError(t *testing.T) {
	lk, err := OpenLookup(synthProfile(t), filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lk.Lookup("k8s", "x"); ok || lk.Len() != 0 {
		t.Error("an empty cache found something")
	}
}

func TestOpenLookupCorruptCacheIsErrCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, scoresFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLookup(synthProfile(t), dir); !errors.Is(err, ErrCache) {
		t.Errorf("got %v, want ErrCache", err)
	}
	if _, err := OpenLookup(Profile{}, dir); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty profile: %v", err)
	}
}

// Warm on a lookup matcher never invents a score: cached postings count as
// cached, anything else is unavailable.
func TestOpenLookupWarmCannotEmbed(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	mustWarm(t, m, k8sDoc())
	lk, err := OpenLookup(synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := lk.Warm(context.Background(), []Doc{k8sDoc()}, 10)
	if err != nil || rep.Cached != 1 || rep.Scored != 0 {
		t.Fatalf("cached: %+v %v", rep, err)
	}
	fresh := Doc{Key: "other", Title: "Gardener", Description: "Plant things in rows and water them daily"}
	rep, err = lk.Warm(context.Background(), []Doc{fresh}, 10)
	if !errors.Is(err, ErrUnavailable) || !rep.Unavailable || rep.Scored != 0 {
		t.Fatalf("uncached: %+v %v", rep, err)
	}
	if _, ok := lk.Lookup("other", TextHash(fresh.Title, fresh.Description)); ok {
		t.Error("a lookup-only matcher scored a posting")
	}
}
