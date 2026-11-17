package main

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// w4aStaffSeeds are postings the triage read as staff level, with the years
// their text asks for: fewer than staff (the ranking demotes the staff
// reading), staff-level, none stated, and a plain posting beside them.
var w4aStaffSeeds = []struct{ key, title, desc string }{
	{"gh:co0:01", "Staff Engineer", "Go and Rust on Kubernetes. Requires 3+ years of experience."},
	{"gh:co1:02", "Staff Engineer", "Go and Rust on Kubernetes. Requires 12+ years of experience."},
	{"gh:co2:03", "Staff Engineer", "Go and Rust on Kubernetes. No figure stated."},
	{"gh:co3:04", "Inference Engineer", "Go and Rust on Kubernetes. Requires 6 years of experience."},
}

func w4aStaffState(t *testing.T) *env {
	t.Helper()
	cfgPath, state := setup(t)
	fixedNow(t)
	cfg := config.Default()
	cfg.TargetSummary = "Inference engineering in Go and Rust, with Kubernetes."
	cfg.DropRules = []config.DropRule{{Slot: "seniority", Values: []string{"staff_plus"}}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	t.Run("seed", func(t *testing.T) {
		l, err := ledger.Open(filepath.Join(state, "ledger"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		for i, s := range w4aStaffSeeds {
			p := posting.Posting{Key: posting.Key(s.key), ATS: "gh", Company: "Co " + s.key[3:6], Title: s.title, Description: s.desc, PostedAt: jobsT0.AddDate(0, 0, -i)}
			ann := map[string]string{"triage.role_fit": "strong_fit", "triage.role_fit.reading": "answered", "triage.role_fit.p": "0.9"}
			if s.title == "Staff Engineer" {
				ann["triage.seniority"], ann["triage.seniority.reading"], ann["triage.seniority.p"] = "staff_plus", "answered", "0.9"
			}
			if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Digested, Posting: &p, Annotations: ann}); err != nil {
				t.Fatal(err)
			}
		}
	})
	return &env{stateDir: state, configPath: cfgPath, stdout: &strings.Builder{}, stderr: &strings.Builder{}}
}

func w4aPriorities(t *testing.T, raw string) map[string]string {
	t.Helper()
	var list struct {
		Jobs []map[string]json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	out := map[string]string{}
	for _, j := range list.Jobs {
		var k string
		json.Unmarshal(j["key"], &k)
		out[k] = string(j["priority"])
	}
	return out
}

// A row's priority is one number on every surface for an unchanged ledger:
// `jobs list` (read from stored facts, with no description loaded, once the
// store is warm), `jobs show` (one company's text read whole) and the export
// (every description read). The ranking's staff-level penalty reads the years
// the posting states, which a lean record does not carry, so it must come from
// what every surface has (the stored fact).
func TestW4aPriorityIsTheSameInListShowAndExport(t *testing.T) {
	e := w4aStaffState(t)
	cold := w4aPriorities(t, cliReply(t, e, (*env).jobsList, "--view", "all", "--limit", "5000")) // derives and saves the facts
	warm := w4aPriorities(t, cliReply(t, e, (*env).jobsList, "--view", "all", "--limit", "5000")) // serves them: no text
	if len(warm) != len(w4aStaffSeeds) {
		t.Fatalf("the list holds %d of %d jobs: %v", len(warm), len(w4aStaffSeeds), warm)
	}

	exported := map[string]string{}
	old := jobsExportList
	t.Cleanup(func() { jobsExportList = old })
	jobsExportList = func(snap ledger.Snapshot, m map[posting.Key]marks.Entry, o *jobview.Opts, rk jobview.Ranker, info jobview.InsightInfo, kw, limit int) jobview.List {
		l := jobview.BuildList(snap, m, o, rk, info, kw, limit)
		for _, j := range l.Jobs {
			b, _ := json.Marshal(j.Priority)
			exported[string(j.Key)] = string(b)
		}
		return l
	}
	one := &env{stateDir: e.stateDir, configPath: e.configPath, stdout: io.Discard, stderr: io.Discard}
	if err := one.jobsExport([]string{"--json", "--view", "all", "--dry-run"}); err != nil {
		t.Fatal(err)
	}

	for _, s := range w4aStaffSeeds {
		var show map[string]json.RawMessage
		if err := json.Unmarshal([]byte(cliReply(t, e, (*env).jobsShow, s.key)), &show); err != nil {
			t.Fatal(err)
		}
		got := map[string]string{"list (derived)": cold[s.key], "list (stored facts)": warm[s.key], "show": string(show["priority"]), "export": exported[s.key]}
		for surface, p := range got {
			if p == "" || p != got["show"] {
				t.Errorf("%s (%s): priority %s on %s, show says %s (all: %v)", s.key, s.title, p, surface, got["show"], got)
			}
		}
	}
	// The case is only a test of the penalty if it moves the priority.
	if warm["gh:co0:01"] == warm["gh:co2:03"] {
		t.Fatalf("the staff reading below staff years was not demoted against the one without a figure: %v", warm)
	}
}
