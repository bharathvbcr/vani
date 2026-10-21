package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func ledgerEvents(stateDir string) string { return filepath.Join(stateDir, "ledger", "events.jsonl") }

// seedTitleDrop adds a posting dropped on its title that the default rules do
// not drop, so --title-drops has something to return.
func seedTitleDrop(t *testing.T, stateDir, id string) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	p := posting.Posting{Key: posting.Key("gh:d:" + id), ATS: "gh", Company: "Acme", Title: "Software Engineer", Description: "Build things."}
	if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Dropped, Posting: &p,
		Annotations: map[string]string{"triage.backend": "title"}}); err != nil {
		t.Fatal(err)
	}
}

func tearTail(t *testing.T, stateDir string) {
	t.Helper()
	f, err := os.OpenFile(ledgerEvents(stateDir), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(`{"v":1,"key":"gh:u:torn","status":"unsu`); err != nil {
		t.Fatal(err)
	}
}

// The dry run prints what the real, locked run then prints, bar the verb, over
// a ledger with unread postings, title drops and a torn tail; and it leaves the
// events file byte for byte as it found it (the locked path would cut the tail).
func TestRetriageDryRunMatchesTheLockedRun(t *testing.T) {
	for _, flags := range [][]string{nil, {"--title-drops"}} {
		_, stateDir := setup(t)
		mustCode(t, 0, "init")
		seedUnread(t, stateDir, 3)
		seedTitleDrop(t, stateDir, "1")
		seedTitleDrop(t, stateDir, "2")
		tearTail(t, stateDir)
		before, err := os.ReadFile(ledgerEvents(stateDir))
		if err != nil {
			t.Fatal(err)
		}
		dry, derr := mustCode(t, 0, append([]string{"retriage", "--dry-run"}, flags...)...)
		after, _ := os.ReadFile(ledgerEvents(stateDir))
		if !bytes.Equal(before, after) {
			t.Errorf("%v: the dry run changed the ledger file", flags)
		}
		if !strings.Contains(derr, "torn") {
			t.Errorf("%v: no torn-tail notice: %q", flags, derr)
		}
		real, _ := mustCode(t, 0, append([]string{"retriage"}, flags...)...)
		if got := strings.Replace(dry, "would return to live", "returned to live", 1); got != real {
			t.Errorf("%v: dry %q vs real %q", flags, dry, real)
		}
		if strings.HasPrefix(real, "0 ") {
			t.Errorf("%v: the oracle returned nothing, so the comparison is empty: %q", flags, real)
		}
	}
}

// Flag order and a stray flag do not change the dry run's refusal to write:
// --title-drops before --dry-run, --dry-run=false (a real run), and an unknown
// flag (usage error, nothing opened).
func TestRetriageDryRunFlagCombinations(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	seedUnread(t, stateDir, 2)
	before, _ := os.ReadFile(ledgerEvents(stateDir))
	a, _ := mustCode(t, 0, "retriage", "--title-drops", "--dry-run")
	b, _ := mustCode(t, 0, "retriage", "--dry-run", "--title-drops")
	if a != b {
		t.Errorf("flag order changed the output: %q vs %q", a, b)
	}
	if code, _, _ := do(t, "retriage", "--dry-run", "--nope"); code != 2 {
		t.Errorf("unknown flag exited %d", code)
	}
	if after, _ := os.ReadFile(ledgerEvents(stateDir)); !bytes.Equal(before, after) {
		t.Error("a dry run or a usage error changed the ledger")
	}
	if o, _ := mustCode(t, 0, "retriage", "--dry-run=false"); !strings.Contains(o, "2 posting(s) returned to live") {
		t.Errorf("--dry-run=false must be a real run: %q", o)
	}
}

// A ledger the snapshot cannot read is an error, not "0 posting(s)": a check
// that could not run must not look like a pass.
func TestRetriageDryRunUnreadableLedgerFails(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	seedUnread(t, stateDir, 1)
	f, err := os.OpenFile(ledgerEvents(stateDir), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	// A complete but invalid line in the middle is corruption, not a torn tail.
	if _, err := f.WriteString("this is not json\n{\"v\":1}\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	code, o, e := do(t, "retriage", "--dry-run")
	if code == 0 || strings.Contains(o, "0 posting(s)") {
		t.Errorf("corrupt ledger: exit %d stdout %q stderr %q", code, o, e)
	}
	if !strings.Contains(e, "retriage: read the ledger") {
		t.Errorf("stderr %q", e)
	}
}

// snapshotLedger refuses every write, so a planner that skipped its dry-run check
// fails loudly rather than appearing to succeed.
func TestSnapshotLedgerRefusesWrites(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	seedUnread(t, stateDir, 2)
	snap, err := ledger.ReadSnapshot(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	l := snapshotLedger{recs: snap.Records}
	ev := ledger.Event{V: 1, At: jobsT0, Key: "gh:u:a", Status: posting.Live}
	if !errors.Is(l.Append(context.Background(), ev), errSnapshotReadOnly) || !errors.Is(l.AppendBatch(context.Background(), []ledger.Event{ev}), errSnapshotReadOnly) {
		t.Error("a write was not refused")
	}
	if keys, err := pipeline.RetriageUnread(context.Background(), l, jobsT0, false); !errors.Is(err, errSnapshotReadOnly) || len(keys) != 0 {
		t.Errorf("a non-dry planner over a snapshot = %v, %v", keys, err)
	}
}
