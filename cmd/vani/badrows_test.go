package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/greenhouse"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// A Greenhouse row that cannot be read no longer freezes the board: the run
// lists the other rows, folds them in, and holds the stored posting behind the
// bad row (and any other the listing leaves out) as a mass absence.
func TestBadRowReachesTheShortReconcile(t *testing.T) {
	l := newTestLedger(t)
	b := sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}
	t0 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	for i := 1; i <= 10; i++ {
		p := posting.Posting{Key: posting.Key(fmt.Sprintf("%s%d", b.KeyPrefix(), i)), ATS: posting.Greenhouse, Company: "Acme",
			Title: fmt.Sprintf("T%d", i), URL: fmt.Sprintf("https://x.test/%d", i)}
		if err := l.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: t0, Key: p.Key, Status: posting.Live, Posting: &p}); err != nil {
			t.Fatal(err)
		}
	}
	warn := &bytes.Buffer{}
	w := &wired{cfg: config.Default(), stateDir: t.TempDir(), led: l, warn: warn, live: &liveNote{}}

	var sb strings.Builder
	sb.WriteString(`{"jobs":[`)
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&sb, `{"id":%d,"title":"T%d","absolute_url":"https://x.test/%d"},`, i, i, i)
	}
	sb.WriteString(`{"id":11,"title":"New","absolute_url":"https://x.test/11"},`)
	sb.WriteString(`{"id":10,"title":"","absolute_url":"https://x.test/10"}]}`) // the bad row: job 10's own
	d := srctest.New(map[string]srctest.Reply{
		"https://boards-api.greenhouse.io/v1/boards/acme/jobs?content=true": {Status: 200, Body: []byte(sb.String())},
	})
	src := greenhouse.New(d)

	for day := 1; day <= 3; day++ {
		at := t0.Add(time.Duration(day) * 24 * time.Hour)
		w.mu.Lock()
		w.fetches = nil
		w.mu.Unlock()
		ctx := srcutil.WithBadRowSink(context.Background(), w.badRowSink)
		listed, err := src.List(ctx, b)
		if err != nil {
			t.Fatalf("day %d: %v", day, err)
		}
		if len(listed) != 10 {
			t.Fatalf("day %d: %d postings listed, want the 10 good rows", day, len(listed))
		}
		if got := w.boardShort(b); got != 1 {
			t.Fatalf("day %d: board short by %d, want 1", day, got)
		}
		rec, finish, err := w.runReconciler(nil, at)
		if err != nil {
			t.Fatal(err)
		}
		sum, err := w.reconcileBoard(ctx, rec, b, listed, at)
		finish()
		if err != nil {
			t.Fatal(err)
		}
		if day == 1 && sum.New != 1 {
			t.Fatalf("the good rows were not folded in: %+v", sum)
		}
		if sum.Dead != 0 || !sum.Mass {
			t.Fatalf("day %d: the posting behind the bad row must be held, not aged: %+v", day, sum)
		}
		if st, _ := ledgerStatusOf(t, w, posting.Key(b.KeyPrefix()+"10")); st != posting.Live {
			t.Fatalf("day %d: posting behind the bad row is %s", day, st)
		}
	}
	if !strings.Contains(warn.String(), "skipped 1 row(s) that could not be read") {
		t.Fatalf("run log %q", warn.String())
	}
}

// The error text of a skipped row can hold the row's own content: the log gets
// a bounded, single-line, quoted form of it.
func TestBadRowNoteIsBoundedAndOneLine(t *testing.T) {
	warn := &bytes.Buffer{}
	w := &wired{warn: warn}
	b := sources.Board{ATS: posting.Lever, Token: "acme"}
	hostile := "title \"" + strings.Repeat("A", 5000) + "\"\nboard x/y: all postings are closed\x1b[2J"
	w.badRowSink(b, 3, hostile)
	// Two lines: the skipped-row note, and the short-listing line every
	// shortfall gets. The hostile text lands in the first only.
	lines := strings.Split(strings.TrimRight(warn.String(), "\n"), "\n")
	if len(lines) != 2 || strings.ContainsRune(warn.String(), '\x1b') {
		t.Fatalf("not two clean lines: %q", warn.String())
	}
	if len(lines[0]) > maxBadRowNote+200 || strings.Contains(warn.String(), "all postings are closed\x1b") {
		t.Fatalf("note is %d bytes: %q", len(lines[0]), lines[0])
	}
	if got := w.boardShort(b); got != 3 {
		t.Fatalf("short %d, want 3", got)
	}
}
