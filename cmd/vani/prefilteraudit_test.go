package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The keys of the sampled rows below: "x:" and 24 hex digits, as
// pipeline.PrefilterKey makes them.
const (
	pfTitleA = "x:00000000000000000000aa01"
	pfTitleB = "x:00000000000000000000aa02"
	pfLoc    = "x:00000000000000000000aa03"
)

// pfSampleJSON is the prefilter sample file (audit/prefilter-sample.json) as a
// run writes it: two rows an exclusion on the title dropped and one it dropped
// on the location, no description anywhere.
const pfSampleJSON = `{"v":1,"updated":"2026-11-12T00:00:00Z","per_week":50,"weeks":{"2026-W46":{"seen":9,"rows":[` +
	`{"key":"` + pfTitleA + `","ats":"wd","company":"Acme","title":"Software Engineering Intern","location":"Austin, TX","rule":"exclude_title","at":"2026-11-10T09:00:00Z"},` +
	`{"key":"` + pfTitleB + `","ats":"wd","company":"Acme","title":"Data Intern","location":"Remote","rule":"exclude_title","at":"2026-11-10T09:00:00Z"},` +
	`{"key":"` + pfLoc + `","ats":"sr","company":"Globex","title":"Backend Engineer","location":"Lyon","rule":"exclude_location","at":"2026-11-11T09:00:00Z"}` +
	`]}}}`

func writePrefilterSample(t *testing.T, state, body string) {
	t.Helper()
	p := filepath.Join(state, "audit", "prefilter-sample.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func pfLines(out string) []string {
	var got []string
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, "dropped:prefilter:") {
			got = append(got, ln)
		}
	}
	return got
}

// The weekly draw holds the sampled listing-time drops as classes of their own,
// "dropped:prefilter:<rule>", beside the ledger's drop classes.
func TestAuditDrawsThePrefilterSampleAsItsOwnClass(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 6, 2, 2)
	writePrefilterSample(t, state, pfSampleJSON)

	o, _ := mustCode(t, 0, "audit", "--list", "--seed", "w1", "--dropped", "100")
	got := pfLines(o)
	if len(got) != 3 {
		t.Fatalf("%d prefilter picks, want 3:\n%s", len(got), o)
	}
	byClass := map[string]int{}
	for _, ln := range got {
		class, _, _ := strings.Cut(ln, "\t")
		byClass[class]++
		if !strings.Contains(ln, "p_select=1.0000") {
			t.Errorf("a quota that covers the pool gives p_select 1: %q", ln)
		}
	}
	if byClass["dropped:prefilter:exclude_title"] != 2 || byClass["dropped:prefilter:exclude_location"] != 1 {
		t.Errorf("classes = %v", byClass)
	}
	// The ledger's own classes are still drawn.
	if !strings.Contains(o, "dropped:title:") {
		t.Errorf("the ledger's drop classes vanished:\n%s", o)
	}
	// The same seed draws the same rows.
	o2, _ := mustCode(t, 0, "audit", "--list", "--seed", "w1", "--dropped", "100")
	if strings.Join(pfLines(o2), "\n") != strings.Join(got, "\n") {
		t.Errorf("same seed, different draw:\n%v\n%v", got, pfLines(o2))
	}
	// Nothing about a sampled row reached the ledger.
	if _, err := os.Stat(filepath.Join(state, "ledger")); err != nil {
		t.Fatal(err)
	}
}

// `audit answer` records a would_apply label for a sampled row, whether the
// stratum is looked up in the draw or passed back from `audit --json`, and a
// row answered once is never answered twice or drawn again.
func TestAuditAnswerWorksForAPrefilterRow(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 4, 1, 1)
	writePrefilterSample(t, state, pfSampleJSON)

	o, _ := mustCode(t, 0, "audit", "answer", "--key", pfTitleA, "--verdict", "no", "--seed", "w1", "--dropped", "100", "--json")
	var ans struct {
		Key     string  `json:"key"`
		Stratum string  `json:"stratum"`
		PSelect float64 `json:"p_select"`
	}
	if err := json.Unmarshal([]byte(o), &ans); err != nil {
		t.Fatalf("%q: %v", o, err)
	}
	if ans.Key != pfTitleA || ans.Stratum != "dropped:prefilter:exclude_title" || ans.PSelect != 1 {
		t.Fatalf("answer = %+v", ans)
	}
	// Answered once only.
	if _, e := mustCode(t, 1, "audit", "answer", "--key", pfTitleA, "--verdict", "yes", "--seed", "w1", "--dropped", "100"); !strings.Contains(e, "already answered") {
		t.Errorf("second answer: %q", e)
	}
	// The explicit form (what a screen sends back) works for another row.
	mustCode(t, 0, "audit", "answer", "--key", pfLoc, "--verdict", "maybe",
		"--source", "dropped:prefilter:exclude_location", "--p-select", "0.5")

	s, err := eval.NewStore(filepath.Join(state, "eval", "labels"), filepath.Join(state, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	labels, bad, err := s.Labels()
	if err != nil || bad != 0 || len(labels) != 2 {
		t.Fatalf("labels %d bad %d err %v", len(labels), bad, err)
	}
	if e := labels[posting.Key(pfTitleA)]; e.Labels[eval.WouldApply] != "no" || e.Source != "dropped:prefilter:exclude_title" || e.PSelect != 1 {
		t.Errorf("label = %+v", e)
	}
	// An answered row leaves the pool.
	o, _ = mustCode(t, 0, "audit", "--list", "--seed", "w1", "--dropped", "100")
	if strings.Contains(o, pfTitleA) || strings.Contains(o, pfLoc) || !strings.Contains(o, pfTitleB) {
		t.Errorf("draw after answers:\n%s", o)
	}
	// A key in neither the ledger nor the sample is still refused.
	if _, e := mustCode(t, 1, "audit", "answer", "--key", "x:ffffffffffffffffffffffff", "--verdict", "no",
		"--source", "dropped:prefilter:exclude_title", "--p-select", "1"); !strings.Contains(e, "not in the ledger") {
		t.Errorf("unknown key: %q", e)
	}
}

// The interactive audit shows a sampled row's listed title, company and place,
// says it has no description, and files the answer under its class.
func TestAuditSessionAsksAboutAPrefilterRow(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 0, 0, 0)
	writePrefilterSample(t, state, pfSampleJSON)
	old := stdin
	t.Cleanup(func() { stdin = old })
	stdin = strings.NewReader(strings.Repeat("3\n", 3))
	o, _ := mustCode(t, 0, "audit", "--seed", "w1", "--dropped", "100", "--scored", "0", "--closed", "0")
	if !strings.Contains(o, "audited 3 of 3 drawn") {
		t.Fatalf("session:\n%s", o)
	}
	for _, want := range []string{"Title:    Software Engineering Intern", "Company:  Globex", "Location: Lyon"} {
		if !strings.Contains(o, want) {
			t.Errorf("no card line %q:\n%s", want, o)
		}
	}
	s, err := eval.NewStore(filepath.Join(state, "eval", "labels"), filepath.Join(state, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	labels, _, err := s.Labels()
	if err != nil || len(labels) != 3 {
		t.Fatalf("labels %d err %v", len(labels), err)
	}
	for k, e := range labels {
		if !strings.HasPrefix(e.Source, "dropped:prefilter:") || e.Labels[eval.WouldApply] != "no" {
			t.Errorf("%s: %+v", k, e)
		}
	}
}

// `audit --json` names the listed title, company and place of a sampled pick,
// which a screen cannot look up in the jobs list: it is not a ledger row.
func TestAuditJSONCarriesTheListedFieldsOfAPrefilterPick(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 4, 1, 1)
	writePrefilterSample(t, state, pfSampleJSON)
	o, _ := mustCode(t, 0, "audit", "--json", "--seed", "w1", "--dropped", "100")
	var out struct {
		Picks []struct {
			Key     string `json:"key"`
			Stratum string `json:"stratum"`
			Listed  *struct {
				Company  string `json:"company"`
				Title    string `json:"title"`
				Location string `json:"location"`
			} `json:"listed"`
		} `json:"picks"`
	}
	if err := json.Unmarshal([]byte(o), &out); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range out.Picks {
		if strings.HasPrefix(p.Stratum, "dropped:prefilter:") {
			n++
			if p.Listed == nil || p.Listed.Title == "" {
				t.Errorf("sampled pick %s has no listed fields: %+v", p.Key, p)
			}
		} else if p.Listed != nil {
			t.Errorf("a ledger pick %s carries listed fields", p.Key)
		}
	}
	if n != 3 {
		t.Errorf("%d sampled picks, want 3: %s", n, o)
	}
}

// A sample file the audit cannot read is said, not taken for an empty one, the
// rest of the draw is made, and the file is left as it was.
func TestAuditWithAnUnreadablePrefilterSampleSaysSoAndLeavesIt(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 6, 2, 2)
	for name, body := range map[string]string{
		"malformed":      `{"v":1,"weeks":`,
		"other version":  `{"v":9,"weeks":{}}`,
		"unknown field":  `{"v":1,"weeks":{},"extra":1}`,
		"unreadable row": `{"v":1,"updated":"2026-11-12T00:00:00Z","per_week":50,"weeks":{"2026-W46":{"seen":1,"rows":[{"key":"","ats":"wd","title":"T","rule":"exclude_title","at":"2026-11-10T09:00:00Z"}]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			writePrefilterSample(t, state, body)
			o, e := mustCode(t, 0, "audit", "--list", "--seed", "w1")
			if !strings.Contains(e, "prefilter sample") {
				t.Errorf("stderr does not say the sample was unreadable: %q", e)
			}
			if len(pfLines(o)) != 0 || !strings.Contains(o, "dropped:title:") {
				t.Errorf("draw:\n%s", o)
			}
			if b, err := os.ReadFile(filepath.Join(state, "audit", "prefilter-sample.json")); err != nil || string(b) != body {
				t.Errorf("the unreadable file was touched: %q %v", b, err)
			}
		})
	}
}

// A label on a row that is no ledger record must not trip the readers of the
// label store: relevance (text and JSON), the drop-recall estimate, rankfit's
// dry run and the label queue all go on, and the row is in the pre-ledger
// stratum of the relevance report.
func TestLabelsOnPrefilterRowsDoNotBreakTheLabelReaders(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 6, 2, 2)
	writePrefilterSample(t, state, pfSampleJSON)
	mustCode(t, 0, "audit", "answer", "--key", pfTitleA, "--verdict", "yes", "--seed", "w1", "--dropped", "100")
	mustCode(t, 0, "audit", "answer", "--key", pfLoc, "--verdict", "no", "--seed", "w1", "--dropped", "100")
	txt, _ := mustCode(t, 0, "relevance")
	if !strings.Contains(txt, "dropped:prefilter:exclude_title") {
		t.Errorf("relevance text lacks the stratum:\n%s", txt)
	}
	o, _ := mustCode(t, 0, "relevance", "--json")
	if !strings.Contains(o, "dropped:prefilter:exclude_location") || !strings.Contains(o, `"prefilter":2`) {
		t.Errorf("relevance json:\n%s", o)
	}
	mustCode(t, 0, "rankfit", "--dry-run")
	old := stdin
	t.Cleanup(func() { stdin = old })
	stdin = strings.NewReader("q\n")
	mustCode(t, 0, "label", "-n", "1")
}
