package kbmatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	h64a = strings.Repeat("a", 64)
	h64b = strings.Repeat("b", 64)
)

func entry(v string, at time.Time) Entry {
	return Entry{Value: v, Model: "m", KB: h64a, Text: h64b, At: at}
}

func TestStoreRoundTripAndCurrency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", FileName)
	s, err := OpenStore(path)
	if err != nil || s.Len() != 0 {
		t.Fatalf("a missing store should be empty: %v, %d", err, s.Len())
	}
	at := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	if err := s.Put("gh:a:1", entry("core", at)); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("store mode: %v %v", fi, err)
	}
	r, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := r.Lookup("gh:a:1", h64a, h64b); !ok || e.Value != "core" || !e.At.Equal(at) {
		t.Errorf("round trip lost the answer: %+v %v", e, ok)
	}
	// An answer given other text or another digest is not found, not a score of 0.
	if _, ok := r.Lookup("gh:a:1", h64a, h64a); ok {
		t.Errorf("an answer to other text was found")
	}
	if _, ok := r.Lookup("gh:a:1", h64b, h64b); ok {
		t.Errorf("an answer given another knowledge base was found")
	}
	if _, ok := r.Lookup("gh:a:2", h64a, h64b); ok {
		t.Errorf("an unknown key was found")
	}
}

func TestStoreRefusesWhatItCannotTrust(t *testing.T) {
	dir := t.TempDir()
	good := `"value":"core","model":"m","kb":"` + h64a + `","text":"` + h64b + `","at":"2026-10-20T01:00:00Z"`
	for name, raw := range map[string]string{
		"not json":       `{`,
		"trailing":       `{"version":1,"entries":{}} {}`,
		"unknown field":  `{"version":1,"entries":{},"extra":1}`,
		"wrong version":  `{"version":2,"entries":{}}`,
		"bad option":     `{"version":1,"entries":{"k":{"value":"perfect","model":"m","kb":"` + h64a + `","text":"` + h64b + `","at":"2026-10-20T01:00:00Z"}}}`,
		"short hash":     `{"version":1,"entries":{"k":{"value":"core","model":"m","kb":"x","text":"` + h64b + `","at":"2026-10-20T01:00:00Z"}}}`,
		"empty key":      `{"version":1,"entries":{"":{` + good + `}}}`,
		"p out of range": `{"version":1,"entries":{"k":{` + good + `,"p":7}}}`,
		"oversized":      `{"version":1,"entries":{},"pad":"` + strings.Repeat("x", maxFileBytes) + `"}`,
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenStore(path); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: want ErrCorrupt, got %v", name, err)
		}
	}
}

func TestStorePutValidatesAndBounds(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), FileName))
	at := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if err := s.Put("k", entry("nonsense", at)); err == nil {
		t.Errorf("Put took an off-list value")
	}
	if err := s.Put("", entry("core", at)); err == nil {
		t.Errorf("Put took an empty key")
	}
	if err := s.Put("k", Entry{Value: "core", KB: h64a, Text: h64b, P: -1}); err == nil {
		t.Errorf("Put took a negative probability")
	}
	for i := 0; i < MaxEntries+5; i++ {
		if err := s.Put(fmt.Sprintf("gh:a:%d", i), entry("partial", at.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	if s.Len() != MaxEntries {
		t.Errorf("store holds %d entries, want the cap %d", s.Len(), MaxEntries)
	}
	if _, ok := s.Lookup("gh:a:0", h64a, h64b); ok {
		t.Errorf("the oldest answer should have been dropped")
	}
	if _, ok := s.Lookup(fmt.Sprintf("gh:a:%d", MaxEntries+4), h64a, h64b); !ok {
		t.Errorf("the newest answer should be kept")
	}
}

func TestLockExcludesASecondRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", FileName)
	unlock, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(path); !errors.Is(err, ErrBusy) {
		t.Errorf("a second lock should be ErrBusy, got %v", err)
	}
	unlock()
	again, err := Lock(path)
	if err != nil {
		t.Fatalf("the lock should be free after unlock: %v", err)
	}
	again()
}
