package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func advScopedEnv(t *testing.T) (*env, ledger.Snapshot) {
	t.Helper()
	t.Setenv("VANI_NO_KEYCHAIN", "1")
	old := now
	now = func() time.Time { return jobsT0 }
	t.Cleanup(func() { now = old })
	mk := func(k, co, body string) ledger.Record {
		return ledger.Record{Key: posting.Key(k), Status: posting.Triaged, FirstSeen: jobsT0, LastEvent: jobsT0,
			Annotations: map[string]string{"description": "ann-" + k},
			Posting: posting.Posting{Key: posting.Key(k), ATS: "gh", Company: co, Title: "Engineer " + k,
				URL: "https://boards.greenhouse.io/x/jobs/" + strings.ReplaceAll(k, ":", ""), Description: body, SeenAt: jobsT0}}
	}
	recs := []ledger.Record{
		mk("gh:a:1", "Acme", "ACME-BODY-1 Go Rust"), mk("gh:a:2", "acme ", "ACME-BODY-2 \"description\":\"x\""),
		mk("gh:o:1", "Other", "OTHER-SECRET-1"), mk("gh:o:2", "Other", "OTHER-SECRET-2"),
		mk("gh:n:1", "", "NOCO-BODY-1"), mk("gh:n:2", "  ", "NOCO-BODY-2"),
	}
	sortRecs(recs)
	snap := ledger.Snapshot{Records: recs}
	return scaleState(t, snap), snap
}

func sortRecs(r []ledger.Record) {
	for i := 1; i < len(r); i++ {
		for j := i; j > 0 && r[j].Key < r[j-1].Key; j-- {
			r[j], r[j-1] = r[j-1], r[j]
		}
	}
}

// A row read with rowScope that was skipped is an error, never an empty record.
func TestRowRecordRefusesASkippedRow(t *testing.T) {
	e, _ := advScopedEnv(t)
	snap, err := ledger.ReadSnapshotScoped(filepath.Join(e.stateDir, "ledger"), rowScope(func(ledger.Snapshot) (posting.Key, bool) { return "gh:a:1", true }))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []posting.Key{"gh:o:1", "gh:o:2"} {
		if _, ok, err := rowRecord(snap, k); err == nil || ok {
			t.Errorf("rowRecord(%s) = ok %v err %v; want an error", k, ok, err)
		}
	}
	for _, k := range []posting.Key{"gh:a:1", "gh:a:2"} {
		r, ok, err := rowRecord(snap, k)
		if err != nil || !ok || r.Posting.Description == "" || r.BodyEvicted {
			t.Errorf("rowRecord(%s) = %+v ok %v err %v; want the whole record", k, r, ok, err)
		}
	}
}

// Every command's reply, for every key, never carries another company's text,
// and `show` carries exactly the full-path description, including for rows with
// no company (which load every record).
func TestRowCommandsNeverEmitASkippedBody(t *testing.T) {
	e, snap := advScopedEnv(t)
	for _, r := range snap.Records {
		k := string(r.Key)
		show := runJSON(t, e, e.jobsShow, k)
		var j jobview.Job
		if err := json.Unmarshal(show, &j); err != nil {
			t.Fatal(err)
		}
		if want := jobview.CleanText(r.Posting.Description, jobview.DescRunes, true); j.Description != want || want == "" {
			t.Errorf("show %s: description %q, want %q", k, j.Description, want)
		}
		for _, o := range snap.Records {
			if o.Posting.Company != r.Posting.Company && !strings.EqualFold(strings.TrimSpace(o.Posting.Company), strings.TrimSpace(r.Posting.Company)) &&
				strings.TrimSpace(r.Posting.Company) != "" && strings.Contains(string(show), strings.Fields(o.Posting.Description)[0]) {
				t.Errorf("show %s leaks %s's text", k, o.Key)
			}
		}
		for name, out := range map[string][]byte{
			"mark": runJSON(t, e, e.jobsMark, k, "saved"),
			"note": runJSON(t, e, e.jobsNote, k, "n"),
			"find": runJSON(t, e, e.jobsFind, "--url", r.Posting.URL),
		} {
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("%s %s: %v", name, k, err)
			}
			if name == "find" && m["job"] == nil {
				t.Errorf("find %s: no job matched (vacuous): %s", k, out)
			}
			if d, _ := m["description"].(string); name != "find" && d != "" {
				t.Errorf("%s %s: unexpected description %q", name, k, d)
			}
			if strings.Contains(string(out), "BODY-") && name != "show" && name != "find" {
				// mark/note replies are the job view: a body there must be this row's own.
				if !strings.Contains(string(out), strings.Fields(r.Posting.Description)[0]) {
					t.Errorf("%s %s emits another row's body: %s", name, k, out)
				}
			}
			if strings.Contains(string(out), "SECRET") && r.Posting.Company != "Other" {
				t.Errorf("%s %s emits another company's body: %s", name, k, out)
			}
		}
	}
}
