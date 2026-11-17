package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// A dropped record whose unreadable body a later listing heals is still an old
// drop: the digest footer ("Dropped today", countsFor) and a reopened ledger
// must not date it to the day of the heal.
func TestW1dHealedOldDropIsNotDroppedToday(t *testing.T) {
	const text, garbled = "alpha beta gamma delta epsilon", "ZZZZZ ZZZZ ZZZZZ ZZZZZ ZZZZZZZ"
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "ledger")
	b := sources.Board{ATS: posting.Greenhouse, Token: "Acme", Company: "Acme"}
	key := posting.Key("gh:acme:1")
	p := posting.Posting{Key: key, ATS: key.ATS(), Company: "Acme", Title: "Engineer", Description: text}
	loc := time.UTC
	dropDay := time.Date(2026, 10, 1, 9, 0, 0, 0, loc)
	healDay := time.Date(2026, 10, 4, 9, 0, 0, 0, loc)

	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Reconcile(ctx, l, b, []posting.Posting{p}, dropDay); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: dropDay.Add(time.Minute), Key: key, Status: posting.Dropped, Reason: "w1d"}); err != nil {
		t.Fatal(err)
	}
	l.Close()
	if l, err = ledger.Open(dir); err != nil { // the restart evicts the closed record's body
		t.Fatal(err)
	}
	path := filepath.Join(dir, "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), text, garbled)), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, _ := l.Get(key); !r.BodyEvicted || r.Posting.Description != "" || r.Status != posting.Dropped {
		t.Fatalf("setup: want an unreadable dropped record, got %+v", r)
	}
	s, err := pipeline.Reconcile(ctx, l, b, []posting.Posting{p}, healDay)
	if err != nil || s.Refreshed != 1 {
		t.Fatalf("heal: %+v, %v", s, err)
	}
	l.Close()
	if l, err = ledger.Open(dir); err != nil { // replay: the stamp survives a restart
		t.Fatal(err)
	}
	defer l.Close()
	if r, _ := l.Get(key); r.Status != posting.Dropped || r.BodyEvicted {
		t.Fatalf("after the heal: status %s evicted %v", r.Status, r.BodyEvicted)
	}
	if c := countsFor(l.All(), nil, healDay, loc); c.Dropped != 0 {
		t.Fatalf("the heal day's footer counts %d dropped, want 0 (counts %+v)", c.Dropped, c)
	}
	if c := countsFor(l.All(), nil, dropDay, loc); c.Dropped != 1 {
		t.Fatalf("the drop day's footer counts %d dropped, want 1 (counts %+v)", c.Dropped, c)
	}
}
