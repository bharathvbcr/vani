package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const staleTitle = "Staff Software Engineer (Core Experiences)"

// seedStale writes three dropped postings: one a model dropped on
// seniority = staff_plus (p 0.9124) that the configured rules no longer drop,
// one the rules still drop (manager, p 0.95), and one with no probability
// recorded, which cannot be judged. It configures today's rules as
// seniority = manager only.
func seedStale(t *testing.T) (cfgPath, stateDir string) {
	t.Helper()
	cfgPath, stateDir = setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Decide.OpenRouterModel = "liquid/d1-ultra"
	cfg.DropRules = []config.DropRule{{Slot: "seniority", Values: []string{"manager"}, MinOpenRouterProb: 0.9}}
	cfg.TitleSignals.Read = nil
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	read := func(value, p string) map[string]string {
		a := map[string]string{"triage.backend": "d1-ultra", "triage.seniority": value, "triage.seniority.reading": "answered",
			"triage.seniority.backend": "d1-ultra", "triage.sponsorship": "no_sponsorship", "triage.sponsorship.reading": "answered",
			"triage.sponsorship.backend": "d1-ultra"}
		if p != "" {
			a["triage.seniority.p"] = p
		}
		return a
	}
	for _, c := range []struct {
		id, title string
		ann       map[string]string
	}{
		{"6716344002", staleTitle, read("staff_plus", "0.9124")},
		{"1", "Engineering Manager", read("manager", "0.9500")},
		{"2", "Senior Software Engineer", read("manager", "")},
	} {
		p := posting.Posting{Key: posting.Key("gh:databricks:" + c.id), ATS: "gh", Company: "Databricks", Title: c.title,
			Location: "Bengaluru", Description: "build things in Go and Rust"}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Dropped, Posting: &p, Annotations: c.ann}); err != nil {
			t.Fatal(err)
		}
	}
	return cfgPath, stateDir
}

func ledgerStatusOf(t *testing.T, stateDir string, key posting.Key) posting.Status {
	t.Helper()
	snap, err := ledger.ReadSnapshot(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range snap.Records {
		if r.Key == key {
			return r.Status
		}
	}
	t.Fatalf("no record %s", key)
	return ""
}

// The report lists the drop today's rules no longer make, by class, with what
// it was made on and today's verdict; it holds no posting text and writes
// nothing, not even with --rescue --dry-run.
func TestRetriageStaleDropsReport(t *testing.T) {
	_, stateDir := seedStale(t)
	events := filepath.Join(stateDir, "ledger", "events.jsonl")
	before, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}

	out, _ := mustCode(t, 0, "retriage", "--stale-drops")
	for _, want := range []string{
		"3 dropped posting(s) re-judged under today's rules: 1 still dropped, 1 stale, 1 not judged",
		"recorded without a usable probability: 1",
		"model:other",
		"gh:databricks:6716344002",
		"seniority=staff_plus p=0.912 (d1-ultra)",
		"today: triaged",
		"read-only: add --rescue",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, leak := range []string{staleTitle, "Bengaluru", "Databricks", "build things", "no_sponsorship"} {
		if strings.Contains(out, leak) {
			t.Errorf("output holds %q:\n%s", leak, out)
		}
	}

	jout, _ := mustCode(t, 0, "--json", "retriage", "--stale-drops")
	var rep staleDropsReply
	decodeOne(t, jout, &rep)
	if !rep.StaleDrops || rep.Dropped != 3 || rep.StillDropped != 1 || rep.Stale != 1 || rep.NotJudged != 1 || rep.Truncated || rep.ItemsShown != 1 {
		t.Fatalf("json: %+v", rep)
	}
	if len(rep.Items) != 1 || rep.Items[0].Key != "gh:databricks:6716344002" || rep.Items[0].Class != "model:other" || rep.Items[0].Today != "triaged" {
		t.Fatalf("items: %+v", rep.Items)
	}
	for _, leak := range []string{staleTitle, "Bengaluru", "build things", "no_sponsorship", "Databricks"} {
		if strings.Contains(jout, leak) {
			t.Errorf("json holds %q:\n%s", leak, jout)
		}
	}

	if out, _ := mustCode(t, 0, "retriage", "--stale-drops", "--rescue", "--dry-run"); !strings.Contains(out, "1 model drop(s) would return to unsure") {
		t.Errorf("dry run rescue:\n%s", out)
	}
	after, err := os.ReadFile(events)
	if err != nil || string(after) != string(before) {
		t.Fatalf("the report wrote the ledger (%v)", err)
	}
}

// --rescue returns the stale model drop to unsure and nothing else; a second
// run changes nothing; the drop the rules still make stays dropped.
func TestRetriageStaleDropsRescue(t *testing.T) {
	_, stateDir := seedStale(t)
	out, _ := mustCode(t, 0, "retriage", "--stale-drops", "--rescue")
	if !strings.Contains(out, "1 model drop(s) returned to unsure") {
		t.Fatalf("rescue:\n%s", out)
	}
	for key, want := range map[posting.Key]posting.Status{
		"gh:databricks:6716344002": posting.Unsure, "gh:databricks:1": posting.Dropped, "gh:databricks:2": posting.Dropped,
	} {
		if got := ledgerStatusOf(t, stateDir, key); got != want {
			t.Errorf("%s is %s, want %s", key, got, want)
		}
	}
	snap, err := ledger.ReadSnapshot(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range snap.Records {
		if r.Key == "gh:databricks:6716344002" && (r.Annotations["triage.seniority"] != "staff_plus" || r.Annotations[ledger.AnnTriageUnsure] == "") {
			t.Errorf("readings or reason lost: %v", r.Annotations)
		}
	}

	events := filepath.Join(stateDir, "ledger", "events.jsonl")
	before, _ := os.ReadFile(events)
	jout, _ := mustCode(t, 0, "--json", "retriage", "--stale-drops", "--rescue")
	var rep staleDropsReply
	decodeOne(t, jout, &rep)
	if rep.Rescued != 0 || rep.Stale != 0 || rep.StillDropped != 1 || len(rep.RescuedKeys) != 0 {
		t.Fatalf("second run: %+v", rep)
	}
	if after, _ := os.ReadFile(events); string(after) != string(before) {
		t.Fatal("a second rescue wrote")
	}
}

// Flags that do not go together are usage errors, and a config that cannot
// judge a model drop is an error that writes nothing, not a report of drops
// that all look stale.
func TestRetriageStaleDropsRefusals(t *testing.T) {
	cfgPath, stateDir := seedStale(t)
	for _, args := range [][]string{
		{"retriage", "--rescue"},
		{"retriage", "--stale-drops", "--title-drops"},
		{"retriage", "--stale-drops", "extra"},
	} {
		if code, _, _ := do(t, args...); code != 2 {
			t.Errorf("vani %v exited %d, want 2", args, code)
		}
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Decide.OpenRouterModel = ""
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"retriage", "--stale-drops"}, {"retriage", "--stale-drops", "--rescue"}} {
		code, out, errOut := do(t, args...)
		if code == 0 {
			t.Errorf("vani %v exited 0 with no OpenRouter backend:\n%s%s", args, out, errOut)
		}
	}
	if got := ledgerStatusOf(t, stateDir, "gh:databricks:6716344002"); got != posting.Dropped {
		t.Errorf("a refused rescue wrote: %s", got)
	}
}

// An empty state reports nothing and succeeds.
func TestRetriageStaleDropsNoLedger(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	out, _ := mustCode(t, 0, "retriage", "--stale-drops")
	if !strings.Contains(out, "0 dropped posting(s)") {
		t.Fatalf("%q", out)
	}
}

func TestCapStaleItems(t *testing.T) {
	items := make([]pipeline.StaleDrop, 5)
	if got, cut := capStaleItems(items, 5); len(got) != 5 || cut {
		t.Errorf("at the limit: %d %v", len(got), cut)
	}
	if got, cut := capStaleItems(items, 2); len(got) != 2 || !cut {
		t.Errorf("over the limit: %d %v", len(got), cut)
	}
	if got, cut := capStaleItems(nil, 2); len(got) != 0 || cut {
		t.Errorf("empty: %d %v", len(got), cut)
	}
}
