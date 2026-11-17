package main

import (
	"context"
	"encoding/json"
	"fmt"
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

// `vani status --json` reads what it reports (the heads), not every body: the
// snapshot its ledger facet keeps carries no description, and reading it
// allocates a small fraction of the bodies' size. What it reports is what the
// full read reports.
func TestW4aStatusReadsNoBodies(t *testing.T) {
	cfgPath, state := setup(t)
	if err := config.Save(cfgPath, config.Default()); err != nil {
		t.Fatal(err)
	}
	const n = 100
	t.Run("seed", func(t *testing.T) { w4aStatusLedger(t, state, n) })
	e := &env{stateDir: state, configPath: cfgPath, stdout: &strings.Builder{}, stderr: &strings.Builder{}}

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

	// Same answer as the full read.
	full, err := ledger.ReadSnapshot(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(struct {
		Total    int
		ByStatus map[string]int
		Torn     bool
		LiveUS   int
	}{len(full.Records), statusCounts(full.Records), full.Torn, liveInUS(full.Records)})
	got, _ := json.Marshal(struct {
		Total    int
		ByStatus map[string]int
		Torn     bool
		LiveUS   int
	}{f.Total, f.ByStatus, f.Torn, *f.liveUS})
	if string(got) != string(want) {
		t.Fatalf("status ledger facet\n got %s\nwant %s", got, want)
	}
	cfg := config.Default()
	lean, _ := json.Marshal(unwatchedCompaniesOf(f.recs, cfg, state))
	whole, _ := json.Marshal(unwatchedCompaniesOf(full.Records, cfg, state))
	if string(lean) != string(whole) {
		t.Fatalf("unwatched companies\n lean %s\nwhole %s", lean, whole)
	}
}

// No ledger yet is still "no ledger yet", and a ledger that is not a ledger is
// still unknown, never zero.
func TestW4aStatusLedgerFacetFailsAsBefore(t *testing.T) {
	cfgPath, state := setup(t)
	if err := config.Save(cfgPath, config.Default()); err != nil {
		t.Fatal(err)
	}
	e := &env{stateDir: state, configPath: cfgPath, stdout: &strings.Builder{}, stderr: &strings.Builder{}}
	if f := e.ledgerFacet(); !f.OK || f.Exists || !strings.Contains(f.Reason, "no ledger yet") {
		t.Fatalf("no ledger: %+v", f.facet)
	}
}
