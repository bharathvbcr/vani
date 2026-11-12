package jobfacts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/manualtriage"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

func vocab(t *testing.T) *jobinsight.Vocab {
	t.Helper()
	v, err := jobinsight.Default()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func samplePostings() []posting.Posting {
	ps := append([]posting.Posting(nil), probe...)
	return append(ps,
		posting.Posting{Key: "gh:a:1", Title: "Inference Engineer", Location: "Austin, TX", Description: strings.Repeat("Go and Rust on Kubernetes. ", 400) + "Pay: $150,000 - $210,000"},
		posting.Posting{Key: "gh:a:2", Title: "nul\x00title", Description: "bad utf8 \xff\xfe and nul \x00 inside"},
	)
}

// Derive answers exactly what each deriver answers on its own.
func TestDeriveIsEachDeriver(t *testing.T) {
	v := vocab(t)
	d := kbmatch.Digest{Text: "skills: Go", SHA256: strings.Repeat("ab", 32)}
	for _, p := range samplePostings() {
		f := Derive(v, p)
		kb, err := kbmatch.TextSHA256Of(f.KBText, "a summary", d)
		if !reflect.DeepEqual(f.Hits, v.Extract(p.Title+"\n"+p.Description)) ||
			f.TextHash != semantic.TextHash(p.Title, p.Description) ||
			f.ManualText != manualtriage.TextSHA256(p) ||
			!reflect.DeepEqual(f.Pay, jobinsight.PostingPay(p)) ||
			err != nil || kb != kbmatch.TextSHA256(p, "a summary", d) {
			t.Errorf("%q: Derive differs from the derivers", p.Title)
		}
	}
}

// What a saved store answers is what was derived, as every consumer sees it
// (JSON), for the Input it was derived from and no other.
func TestStoreRoundTripsAndMatchesItsInput(t *testing.T) {
	v := vocab(t)
	path := filepath.Join(t.TempDir(), "facts")
	ver := Version(v)
	s := Open(path, ver)
	if s.Info() != "empty: no store yet" {
		t.Fatalf("new store: %s", s.Info())
	}
	want := map[posting.Key]Facts{}
	ins := map[posting.Key]Input{}
	for i, p := range samplePostings() {
		k := posting.Key("gh:x:" + string(rune('a'+i)))
		in := InputOf(p, ledger.BodyFingerprint([]byte(p.Description)))
		f := Derive(v, p)
		s.Put(k, in, f)
		want[k], ins[k] = f, in
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s2 := Open(path, ver)
	if s2.Info() != "loaded" || s2.Len() != len(want) {
		t.Fatalf("reopened: %s, %d entries", s2.Info(), s2.Len())
	}
	for k, f := range want {
		got, ok := s2.Get(k, ins[k])
		if !ok {
			t.Fatalf("%s: missing after a round trip", k)
		}
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(f)
		if string(g) != string(w) {
			t.Errorf("%s: round trip changed the facts\n got %s\nwant %s", k, g, w)
		}
		for _, change := range []func(*Input){
			func(in *Input) { in.BodyFP[0] ^= 1 },
			func(in *Input) { in.Title += "x" },
			func(in *Input) { in.Location += "x" },
			func(in *Input) { in.Remote = !in.Remote },
		} {
			in := ins[k]
			change(&in)
			if _, ok := s2.Get(k, in); ok {
				t.Errorf("%s: answered for a changed input %+v", k, in)
			}
		}
	}
}

// A store from other derivers, or one that is not a store, is not read.
func TestStoreOfAnotherVersionOrGarbageStartsEmpty(t *testing.T) {
	v := vocab(t)
	path := filepath.Join(t.TempDir(), "facts")
	s := Open(path, "old-version")
	p := samplePostings()[0]
	s.Put("gh:a:1", InputOf(p, [16]byte{1}), Derive(v, p))
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if got := Open(path, Version(v)); got.Len() != 0 || !strings.Contains(got.Info(), "another version") {
		t.Fatalf("another version: %d entries, %s", got.Len(), got.Info())
	}
	if err := os.WriteFile(path, []byte("not a store"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Open(path, Version(v)); got.Len() != 0 || !strings.Contains(got.Info(), "unreadable") {
		t.Fatalf("garbage: %d entries, %s", got.Len(), got.Info())
	}
}

// The version moves with the vocabulary.
func TestVersionFollowsTheVocabulary(t *testing.T) {
	a, err := jobinsight.ParseVocab("language go|golang\nlanguage rust\n")
	if err != nil {
		t.Fatal(err)
	}
	b, err := jobinsight.ParseVocab("language go|golang\nlanguage rust\nlanguage python\n")
	if err != nil {
		t.Fatal(err)
	}
	if Version(a) == Version(b) {
		t.Error("two vocabularies share a version")
	}
	if again := Version(a); again != Version(a) {
		t.Error("the version is not stable")
	}
}

// Save writes only when something changed, skips while another writer holds
// the lock, and Prune drops postings no view reads.
func TestStoreSaveAndPrune(t *testing.T) {
	v := vocab(t)
	path := filepath.Join(t.TempDir(), "facts")
	s := Open(path, Version(v))
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("an unchanged store was written")
	}
	p := samplePostings()[0]
	s.Put("gh:a:1", InputOf(p, [16]byte{1}), Derive(v, p))
	s.Put("gh:a:2", InputOf(p, [16]byte{2}), Derive(v, p))
	lock, err := filelock.TryAcquire(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != ErrBusy {
		t.Fatalf("save under another writer's lock: %v", err)
	}
	lock.Release()
	s.Prune(func(k posting.Key) bool { return k == "gh:a:1" })
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if got := Open(path, Version(v)); got.Len() != 1 {
		t.Fatalf("after prune: %d entries", got.Len())
	}
}

// Changes counts every edit, and only edits: a Get, a Prune that forgets
// nothing and a Save leave it; Dirty is whether Save has edits to write.
func TestStoreChangesCountsEdits(t *testing.T) {
	v := vocab(t)
	s := Open(filepath.Join(t.TempDir(), "facts"), Version(v))
	p := samplePostings()[0]
	in := InputOf(p, [16]byte{1})
	if s.Changes() != 0 || s.Dirty() {
		t.Fatalf("opened: %d changes, dirty %v", s.Changes(), s.Dirty())
	}
	s.Put("gh:a:1", in, Derive(v, p))
	s.Put("gh:a:1", in, Derive(v, p)) // the same facts again is still an edit
	s.Put("gh:a:2", in, Derive(v, p))
	if s.Changes() != 3 || !s.Dirty() {
		t.Fatalf("three puts: %d changes, dirty %v", s.Changes(), s.Dirty())
	}
	s.Get("gh:a:1", in)
	s.Prune(func(posting.Key) bool { return true })
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if s.Changes() != 3 || s.Dirty() {
		t.Fatalf("get, empty prune, save: %d changes, dirty %v", s.Changes(), s.Dirty())
	}
	s.Prune(func(k posting.Key) bool { return k == "gh:a:1" })
	if s.Changes() != 4 || !s.Dirty() {
		t.Fatalf("a prune that forgot one: %d changes, dirty %v", s.Changes(), s.Dirty())
	}
}

func TestStoreConcurrentUse(t *testing.T) {
	v := vocab(t)
	s := Open(filepath.Join(t.TempDir(), "facts"), Version(v))
	p := samplePostings()[0]
	f := Derive(v, p)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 100 {
				k := posting.Key("gh:a:" + string(rune('a'+(i+g)%20)))
				in := InputOf(p, [16]byte{byte(i % 3)})
				s.Put(k, in, f)
				s.Get(k, in)
			}
		})
	}
	wg.Wait()
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
}
