package jobfacts

import (
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// previousFormat is the format before the pay parser stopped reading bonus,
// equity and non-US dollar amounts as salary (ft-11d0).
const previousFormat = 2

// A store written at the previous format holds the old parser's pay. It is
// derived again once and gets the new values; the next open serves it as is.
func TestPayFixBumpsFormatAndRederivesOnce(t *testing.T) {
	if format != previousFormat+1 {
		t.Fatalf("format = %d, want %d: the pay parser's answers changed by one format", format, previousFormat+1)
	}
	v := vocab(t)
	ver := Version(v)
	p := posting.Posting{Key: "gh:a:9", Title: "Engineer", Location: "Taipei, Taiwan",
		Description: "Salary: NT$1,200,000 - NT$1,800,000 per year. Bonus range: $20,000 - $40,000."}
	in := InputOf(p, [16]byte{9})
	stale := Derive(v, p)
	stale.Pay = []jobinsight.Pay{{Currency: "USD", Period: jobinsight.Year, Min: 20000, Max: 40000, AnnualMin: 20000, AnnualMax: 40000, Text: "$20,000 - $40,000"}}

	path := filepath.Join(t.TempDir(), "facts")
	writeRawStore(t, path, storeFile{Format: previousFormat, Version: ver, Entries: []storeEntry{
		{Key: p.Key, In: in, F: stale, HitsSet: true, PaySet: true},
	}})
	s := Open(path, ver)
	if s.Len() != 0 {
		t.Fatalf("a store at format %d was served at format %d: %d entries (%s)", previousFormat, format, s.Len(), s.Info())
	}
	if _, ok := s.Get(p.Key, in); ok {
		t.Fatal("the stale pay was served")
	}
	fresh := Derive(v, p)
	if len(fresh.Pay) != 0 {
		t.Fatalf("derived pay %+v, want none (NT$ is not USD, a bonus range is not salary)", fresh.Pay)
	}
	s.Put(p.Key, in, fresh)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s2 := Open(path, ver)
	if s2.Info() != "loaded" {
		t.Fatalf("second open: %s", s2.Info())
	}
	got, ok := s2.Get(p.Key, in)
	if !ok || len(got.Pay) != 0 || s2.Changes() != 0 || s2.Dirty() {
		t.Fatalf("second open: ok %v pay %+v changes %d dirty %v; the store at the new format was derived again", ok, got.Pay, s2.Changes(), s2.Dirty())
	}
}
