package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
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

	o, _ := mustCode(t, 0, "migrate", "rekey-fallback", "--dry-run")
	if !strings.Contains(o, "would move: 1") || !strings.Contains(o, string(oldKey)+" -> "+string(newKey)) {
		t.Fatalf("dry run:\n%s", o)
	}
	o, _ = mustCode(t, 0, "migrate", "rekey-fallback")
	if !strings.Contains(o, "moved: 1") || !strings.Contains(o, "backup: ") {
		t.Fatalf("run:\n%s", o)
	}
	if _, e := mustCode(t, 1, "migrate", "rekey-fallback"); !strings.Contains(e, "already ran") {
		t.Fatalf("second run: %s", e)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "ledger", "rekey-fallback-title.json")); err != nil {
		t.Fatal(err)
	}
}
