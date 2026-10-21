package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// storedFor hands an adapter every posting under a board, closed ones with
// their text, even after a restart evicted it from memory.
func TestStoredForReturnsClosedBodiesAfterRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := posting.Key("wd:nvidia:site:jr2")
	p := posting.Posting{Key: key, ATS: key.ATS(), Company: "N", Title: "t", Description: "the full job text"}
	for _, e := range []ledger.Event{
		{V: 1, At: jobsT0, Key: key, Status: posting.Live, Posting: &p},
		{V: 1, At: jobsT0, Key: key, Status: posting.Dead},
	} {
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	l, err = ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if r := l.All()[0]; !r.BodyEvicted {
		t.Fatal("setup: the closed record's text is resident")
	}
	got := (&wired{led: l}).storedFor(sources.Board{ATS: posting.Workday, Token: "NVIDIA/Site"})
	if len(got) != 1 || got[0].Description != "the full job text" {
		t.Fatalf("storedFor: %+v", got)
	}
}

// warmDocs never embeds a marked closed job whose text is not resident: that
// would score the title alone under the key the Jobs views look the full text up by.
func TestWarmDocsSkipsEvictedBodies(t *testing.T) {
	open := ledger.Record{Key: "gh:a:1", Status: posting.Triaged, Posting: posting.Posting{Key: "gh:a:1", Title: "open", Description: "text"}}
	closed := ledger.Record{Key: "gh:a:2", Status: posting.Dead, BodyEvicted: true, Posting: posting.Posting{Key: "gh:a:2", Title: "closed"}}
	docs := warmDocs([]ledger.Record{open, closed}, map[posting.Key]marks.Mark{"gh:a:2": marks.Saved})
	if len(docs) != 1 || docs[0].Key != "gh:a:1" {
		t.Fatalf("docs: %+v", docs)
	}
}
