package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// rowFixture is a ledger whose rows depend on company-wide learning: "Acme" has
// enough postings (spelled three ways) that a term they all repeat is blurb,
// "Tiny" is under Boilerplate's minimum, and "Solo" has one posting. Some
// postings are marked, one is closed and one has an empty description.
func rowFixture() (ledger.Snapshot, map[posting.Key]marks.Entry) {
	tech := []string{"Go", "Rust", "Python", "CUDA", "PyTorch", "TensorRT", "Docker", "SQL", "Terraform", "Spark"}
	var snap ledger.Snapshot
	add := func(i int, company, title, desc string, st posting.Status) {
		k := posting.Key(fmt.Sprintf("gh:%s:%03d", strings.ToLower(strings.TrimSpace(company)), i))
		snap.Records = append(snap.Records, ledger.Record{
			Key: k, Status: st, FirstSeen: jobsT0.AddDate(0, 0, -i),
			Posting: posting.Posting{Key: k, Company: company, Title: title, Description: desc,
				URL: fmt.Sprintf("https://boards.greenhouse.io/x/jobs/%03d", i)},
			Annotations: map[string]string{"triage.role_fit": "partial_fit", ledger.AnnMissing: "Kubernetes (weak)"},
		})
	}
	spell := []string{"Acme", "acme ", " ACME"}
	for i := range 14 {
		add(i, spell[i%3], "Inference Engineer",
			"About Acme: we run Kubernetes and Docker everywhere. You will use "+tech[i%len(tech)]+" and "+tech[(i*3+1)%len(tech)]+".", posting.Digested)
	}
	for i := range 3 {
		add(20+i, "Tiny", "ML Engineer", "Kubernetes and "+tech[i]+" daily.", posting.Digested)
	}
	add(30, "Solo", "Go Engineer", "Go, Rust and Kubernetes.", posting.Digested)
	add(31, "Solo2", "Empty", "", posting.Digested)
	add(32, "Acme", "Closed role", "Kubernetes with Rust.", posting.Dead)
	m := map[posting.Key]marks.Entry{
		snap.Records[0].Key: {Mark: marks.Saved},
		snap.Records[4].Key: {Mark: marks.Hidden},
		snap.Records[7].Key: {Mark: marks.Applied},
	}
	return snap, m
}

// A row built from the one record's company is byte-identical to the row the
// full pass (every company learned) builds, for every record, with and without
// a target summary. The full path stays here as the oracle.
func TestRowInsightParity(t *testing.T) {
	old := now
	now = func() time.Time { return jobsT0 }
	t.Cleanup(func() { now = old })
	snap, m := rowFixture()
	for _, tc := range []struct {
		name string
		cfg  config.Config
		err  error
	}{
		{"target", config.Config{TargetSummary: "Inference engineer: Go, Rust, CUDA and Kubernetes."}, nil},
		{"no target", config.Config{}, nil},
		{"config unreadable", config.Config{}, fmt.Errorf("no config")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			full, err := newInsightFor(tc.cfg, tc.err, t.TempDir(), 14, snap, m)
			if err != nil {
				t.Fatal(err)
			}
			if len(full.boiler) == 0 {
				t.Fatal("fixture learns no boilerplate, so the test would prove nothing")
			}
			for _, r := range snap.Records {
				one, err := newInsightScoped(tc.cfg, tc.err, t.TempDir(), 14, snap, m, companyKey(r.Posting.Company))
				if err != nil {
					t.Fatal(err)
				}
				var want, got [2][]byte
				for i, in := range []*insight{full, one} {
					j := jobview.ToJob(r, m[r.Key])
					in.Enrich(&j, r, showKeywords)
					b, err := json.Marshal(j)
					if err != nil {
						t.Fatal(err)
					}
					row, _ := json.Marshal(j.Row)
					if i == 0 {
						want = [2][]byte{b, row}
					} else {
						got = [2][]byte{b, row}
					}
				}
				if string(want[0]) != string(got[0]) || string(want[1]) != string(got[1]) {
					t.Errorf("%s: row differs from the full pass\nfull: %s\none:  %s", r.Key, want[0], got[0])
				}
			}
			// The scoped pass really is scoped: it holds only the one company's hits.
			one, _ := newInsightScoped(tc.cfg, tc.err, t.TempDir(), 14, snap, m, "tiny")
			if len(one.hits) != 3 {
				t.Errorf("scoped pass extracted %d postings, want Tiny's 3", len(one.hits))
			}
		})
	}
}

// The env entry point (jobs mark/find/show/note) gives the same row as the
// full insight for the record it names.
func TestNewRowInsightMatchesFull(t *testing.T) {
	old := now
	now = func() time.Time { return jobsT0 }
	t.Cleanup(func() { now = old })
	snap, m := rowFixture()
	e := &env{stateDir: t.TempDir()}
	full, err := e.newInsight(jobinsight.DefaultScoreHalfLifeDays, snap, m)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range snap.Records {
		one, err := e.newRowInsight(snap, m, r)
		if err != nil {
			t.Fatal(err)
		}
		a, b := jobview.ToJob(r, m[r.Key]), jobview.ToJob(r, m[r.Key])
		full.Enrich(&a, r, showKeywords)
		one.Enrich(&b, r, showKeywords)
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if string(ja) != string(jb) {
			t.Errorf("%s differs\nfull: %s\none:  %s", r.Key, ja, jb)
		}
	}
}
