package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile/synctest"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func openAt(t *testing.T, dir string) *ledger.File {
	t.Helper()
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func listingOf(t *testing.T, n int, desc string) []posting.Posting {
	t.Helper()
	ps := make([]posting.Posting, n)
	for i := range ps {
		ps[i] = ghPost(t, fmt.Sprint(i+1), fmt.Sprintf("%s %d", desc, i+1))
	}
	return ps
}

// A board's events are made durable by one fsync, however many there are.
// Before the group, each new or changed posting was an fsync of its own.
func TestReconcileBoardSyncsOnce(t *testing.T) {
	dir := t.TempDir()
	l := openAt(t, dir)
	defer l.Close()
	log := synctest.Record(t, dir)
	s, err := Reconcile(ctx, l, board, listingOf(t, 40, "build things"), t0)
	if err != nil || s.New != 40 {
		t.Fatalf("new: %+v %v", s, err)
	}
	if n := len(log.Files()); n != 1 {
		t.Fatalf("40 new postings: %d ledger syncs, want 1", n)
	}
	log.Reset()
	changed := listingOf(t, 40, "build other things")
	s, err = Reconcile(ctx, l, board, changed, t0.Add(24*time.Hour))
	if err != nil || s.Changed != 40 {
		t.Fatalf("changed: %+v %v", s, err)
	}
	if n := len(log.Files()); n != 1 {
		t.Fatalf("40 changed postings: %d ledger syncs, want 1", n)
	}
}

// A crash part way through a board leaves a whole-line prefix of its events
// (or that and a torn line). Listing the board again converges on the state the
// uninterrupted board reached.
func TestReconcileCrashPrefixConverges(t *testing.T) {
	src := t.TempDir()
	l := openAt(t, src)
	first := listingOf(t, 12, "build things")
	if _, err := Reconcile(ctx, l, board, first, t0); err != nil {
		t.Fatal(err)
	}
	l.Close()
	before, err := os.ReadFile(filepath.Join(src, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// The second listing changes half the postings and adds eight.
	second := listingOf(t, 20, "build things")
	for i := range 6 {
		second[i] = ghPost(t, fmt.Sprint(i+1), fmt.Sprintf("rewritten %d", i+1))
	}
	at := t0.Add(24 * time.Hour)
	l = openAt(t, src)
	if _, err := Reconcile(ctx, l, board, second, at); err != nil {
		t.Fatal(err)
	}
	want := l.All()
	l.Close()
	after, err := os.ReadFile(filepath.Join(src, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	tail := after[len(before):]
	var cuts []int
	for i, b := range tail {
		if b == '\n' {
			cuts = append(cuts, i+1)
		}
	}
	if len(cuts) < 10 {
		t.Fatalf("the second board wrote %d events, want enough to cut", len(cuts))
	}
	check := func(name string, data []byte) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		c := openAt(t, dir)
		defer c.Close()
		if _, err := Reconcile(ctx, c, board, second, at); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := c.All(); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: listing again did not converge (%d records, want %d)", name, len(got), len(want))
		}
	}
	prev := 0
	for _, cut := range cuts {
		check(fmt.Sprintf("crash after %d bytes", cut), append(bytes.Clone(before), tail[:cut]...))
		if mid := prev + (cut-prev)/2; mid > prev {
			check(fmt.Sprintf("torn at %d bytes", mid), append(bytes.Clone(before), tail[:mid]...))
		}
		prev = cut
	}
}

// If the board's fsync fails the ledger is broken and none of the board's
// events stand: the summary is zero and memory holds only what was durable.
func TestReconcileFailedGroupSyncReportsNothing(t *testing.T) {
	dir := t.TempDir()
	l := openAt(t, dir)
	defer l.Close()
	if _, err := Reconcile(ctx, l, board, listingOf(t, 3, "build things"), t0); err != nil {
		t.Fatal(err)
	}
	log := synctest.Record(t, dir)
	boom := errors.New("fsync: boom")
	log.Fail(func(_ string, d bool) error {
		if d {
			return nil
		}
		return boom
	})
	s, err := Reconcile(ctx, l, board, listingOf(t, 9, "build things"), t0.Add(24*time.Hour))
	if err == nil || l.Broken() == nil {
		t.Fatalf("err %v, broken %v; want the failed sync and a broken ledger", err, l.Broken())
	}
	if s != (Summary{}) {
		t.Fatalf("summary %+v after a failed sync, want zero", s)
	}
	if n := len(l.All()); n != 3 {
		t.Fatalf("%d records answered, want the 3 durable", n)
	}
}
