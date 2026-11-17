package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const w4aStatusBody = 48 << 10

// w4aStatusLedger seeds n postings across statuses, each carrying a description
// of w4aStatusBody bytes and a location, under a state's ledger.
func w4aStatusLedger(t *testing.T, state string, n int) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	statuses := []posting.Status{posting.Live, posting.Dropped, posting.Dead, posting.Digested, posting.Explained}
	places := []string{"Austin, TX", "Berlin, Germany", "Remote - US", ""}
	for i := range n {
		k := posting.Key(fmt.Sprintf("gh:co%d:%03d", i%7, i))
		p := posting.Posting{Key: k, ATS: "gh", Company: fmt.Sprintf("Co %d", i%7), Title: "Engineer", Location: places[i%4],
			Description: strings.Repeat(fmt.Sprintf("body %d ", i), w4aStatusBody/8)}
		st := statuses[i%len(statuses)]
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: k, Status: posting.Live, Posting: &p}); err != nil {
			t.Fatal(err)
		}
		if st != posting.Live {
			if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: k, Status: st, Posting: &p}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func w4aStatusState(t *testing.T, n int) (e *env, dir string) {
	t.Helper()
	cfgPath, state := setup(t)
	fixedNow(t)
	if err := config.Save(cfgPath, config.Default()); err != nil {
		t.Fatal(err)
	}
	t.Run("seed", func(t *testing.T) { w4aStatusLedger(t, state, n) })
	return &env{stateDir: state, configPath: cfgPath, stdout: &strings.Builder{}, stderr: &strings.Builder{}}, filepath.Join(state, "ledger")
}

// w4aFacetJSON is the ledger facet as `status --json` writes it, from the full
// read that status used before: the bytes the scoped read must reproduce.
func w4aFacetJSON(t *testing.T, dir string, full bool) string {
	t.Helper()
	var snap ledger.Snapshot
	var err error
	if full {
		snap, err = ledger.ReadSnapshot(dir)
	} else {
		snap, err = ledger.ReadSnapshotScoped(dir, nil)
	}
	b, merr := json.Marshal(ledgerFacetOf(dir, snap, err))
	if merr != nil {
		t.Fatal(merr)
	}
	return string(b)
}

// `vani status --json` reads what it reports (the heads), not every body: the
// snapshot its ledger facet keeps carries no description, and reading it
// allocates a small fraction of the bodies' size. The ledger facet, and what
// the other facets derive from the same records, are byte for byte what the
// full read gave.
func TestW4aStatusReadsNoBodies(t *testing.T) {
	const n = 100
	e, dir := w4aStatusState(t, n)
	// A saved and an applied posting, so the unwatched-companies facet (which
	// counts only marked postings, by company name) has something to report.
	mustCode(t, 0, "jobs", "mark", "gh:co1:001", "saved")
	mustCode(t, 0, "jobs", "mark", "gh:co2:002", "applied")

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f := e.ledgerFacet()
	runtime.ReadMemStats(&after)
	if !f.OK || f.Total != n {
		t.Fatalf("ledger facet: %+v", f.facet)
	}
	for _, r := range f.recs {
		if r.Posting.Description != "" || !r.BodyEvicted {
			t.Fatalf("%s: status loaded a body (%d bytes, evicted %v)", r.Key, len(r.Posting.Description), r.BodyEvicted)
		}
	}
	if got, bodies := after.TotalAlloc-before.TotalAlloc, uint64(n*w4aStatusBody); got > bodies/4 {
		t.Errorf("reading the status ledger allocated %d bytes for %d bytes of bodies", got, bodies)
	}

	if lean, full := w4aFacetJSON(t, dir, false), w4aFacetJSON(t, dir, true); lean != full {
		t.Fatalf("ledger facet\nscoped %s\n  full %s", lean, full)
	}
	full, err := ledger.ReadSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := *f.liveUS, liveInUS(full.Records); got != want {
		t.Fatalf("live in US %d, full read %d", got, want)
	}
	cfg := config.Default()
	lean, _ := json.Marshal(unwatchedCompaniesOf(f.recs, cfg, e.stateDir))
	whole, _ := json.Marshal(unwatchedCompaniesOf(full.Records, cfg, e.stateDir))
	if string(lean) != string(whole) || !strings.Contains(string(lean), `"count":2`) {
		t.Fatalf("unwatched companies (want the two marked ones)\n lean %s\nwhole %s", lean, whole)
	}

	// The command itself writes that facet.
	out, _ := mustCode(t, 0, "status", "--json")
	var got struct {
		Ledger    json.RawMessage `json:"ledger"`
		Unwatched json.RawMessage `json:"unwatched_companies"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	want := ledgerFacetOf(dir, full, nil)
	want.PendingReopens, want.PendingReopensError = e.pendingReopens()
	wb, _ := json.Marshal(want)
	if string(got.Ledger) != string(wb) {
		t.Fatalf("status --json ledger\n got %s\nwant %s", got.Ledger, wb)
	}
	if !strings.Contains(string(got.Unwatched), `"count":2`) {
		t.Fatalf("status --json unwatched_companies: %s", got.Unwatched)
	}
}

// A torn tail (a writer mid-append) is counted the same way and flagged torn; a
// ledger corrupt mid-file, or one that is not a ledger, is unknown, never zero.
func TestW4aStatusLedgerFacetTornAndCorrupt(t *testing.T) {
	t.Run("torn tail", func(t *testing.T) {
		_, dir := w4aStatusState(t, 12)
		f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(`{"v":1,"key":"gh:co0:999","status":"li`); err != nil {
			t.Fatal(err)
		}
		f.Close()
		lean, full := w4aFacetJSON(t, dir, false), w4aFacetJSON(t, dir, true)
		if lean != full || !strings.Contains(lean, `"torn":true`) || !strings.Contains(lean, `"total":12`) {
			t.Fatalf("torn tail\nscoped %s\n  full %s", lean, full)
		}
	})
	t.Run("corrupt mid-file", func(t *testing.T) {
		e, dir := w4aStatusState(t, 12)
		path := filepath.Join(dir, "events.jsonl")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.SplitAfter(string(raw), "\n")
		lines[3] = "this is not an event\n"
		if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, full := range []bool{false, true} {
			var f ledgerFacet
			if full {
				snap, err := ledger.ReadSnapshot(dir)
				f = ledgerFacetOf(dir, snap, err)
			} else {
				f = e.ledgerFacet()
			}
			if f.OK || f.Exists || f.Total != 0 || f.Reason == "" {
				t.Fatalf("full=%v: a corrupt ledger reads as %+v, want unknown with a reason", full, f)
			}
		}
	})
	t.Run("no ledger yet", func(t *testing.T) {
		cfgPath, state := setup(t)
		if err := config.Save(cfgPath, config.Default()); err != nil {
			t.Fatal(err)
		}
		e := &env{stateDir: state, configPath: cfgPath, stdout: &strings.Builder{}, stderr: &strings.Builder{}}
		if f := e.ledgerFacet(); !f.OK || f.Exists || !strings.Contains(f.Reason, "no ledger yet") {
			t.Fatalf("no ledger: %+v", f.facet)
		}
	})
}
