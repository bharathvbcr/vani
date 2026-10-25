package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resumestore"
)

func TestMigrateRekeyFallback(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 2, "migrate")
	mustCode(t, 2, "migrate", "other")
	if _, e := mustCode(t, 1, "migrate", "rekey-fallback"); !strings.Contains(e, "no ledger") {
		t.Fatalf("no ledger: %s", e)
	}

	// A fallback key made by the old fold, which read "C++" as "c".
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	const oldKey = posting.Key("x:4ce76c1f2f688f7c1489a8fd")
	p := posting.Posting{Key: oldKey, Company: "Acme", Title: "Senior C++ Engineer", Location: "Remote", Description: "d"}
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	if err := l.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: at, Key: oldKey, Status: posting.Live, Posting: &p, Reason: "new"}); err != nil {
		t.Fatal(err)
	}
	l.Close()
	newKey, _ := posting.FallbackKey(p.Company, p.Title, p.Location)

	// The posting has a resume version; it must follow the posting.
	rs, err := resumestore.Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	hex := func(c string) string { return strings.Repeat(c, 64) }
	meta := resumestore.Meta{
		Source: "tailored", Pages: 1, Selection: json.RawMessage(`{"sections":[]}`),
		Checks:  resumestore.Checks{Eligibility: "current", Lint: "passed", Fit: "passed", Figures: "verified"},
		Bullets: []resumestore.BulletRef{{ID: "b1", TextHash: "0123456789ab"}},
		Prints:  resumestore.Fingerprints{Bank: hex("a"), BaseSelection: hex("b"), Selections: hex("c"), Claims: hex("d"), RKB: hex("e")},
	}
	if _, _, err := rs.Save(context.Background(), string(oldKey), meta, []byte("%PDF-1.7\nr\n%%EOF\n")); err != nil {
		t.Fatal(err)
	}

	o, _ := mustCode(t, 0, "migrate", "rekey-fallback", "--dry-run")
	if !strings.Contains(o, "would move: 1") || !strings.Contains(o, string(oldKey)+" -> "+string(newKey)) {
		t.Fatalf("dry run:\n%s", o)
	}
	o, _ = mustCode(t, 0, "migrate", "rekey-fallback")
	if !strings.Contains(o, "moved: 1") || !strings.Contains(o, "resume folders moved: 1") || !strings.Contains(o, "backup: ") {
		t.Fatalf("run:\n%s", o)
	}
	if ms, err := rs.List(string(newKey)); err != nil || len(ms) != 1 || ms[0].Key != string(newKey) {
		t.Fatalf("the resume did not follow the posting: %v %v", ms, err)
	}
	if _, err := rs.List(string(oldKey)); err == nil {
		t.Fatal("the old key still has a resume")
	}
	if _, e := mustCode(t, 1, "migrate", "rekey-fallback"); !strings.Contains(e, "already ran") {
		t.Fatalf("second run: %s", e)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "ledger", "rekey-fallback-title.json")); err != nil {
		t.Fatal(err)
	}
}
