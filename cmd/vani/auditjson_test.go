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

type auditDrawJSON struct {
	Seed   string `json:"seed"`
	Blind  bool   `json:"blind"`
	Picks  []map[string]any
	Strata []map[string]any
}

func drawJSON(t *testing.T, args ...string) (auditDrawJSON, string) {
	t.Helper()
	o, e := mustCode(t, 0, append([]string{"audit", "--json"}, args...)...)
	if e != "" {
		t.Fatalf("stderr: %q", e)
	}
	var d auditDrawJSON
	if err := json.Unmarshal([]byte(o), &d); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, o)
	}
	return d, o
}

func readLabels(t *testing.T, state string) map[posting.Key]eval.Entry {
	t.Helper()
	s, err := eval.NewStore(filepath.Join(state, "eval", "labels"), filepath.Join(state, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	labels, bad, err := s.Labels()
	if err != nil || bad != 0 {
		t.Fatalf("labels: bad %d err %v", bad, err)
	}
	return labels
}

func TestAuditAliasStillWorks(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 10, 3, 3)
	a, _ := mustCode(t, 0, "audit-sample", "--list", "--seed", "w")
	b, _ := mustCode(t, 0, "audit", "--list", "--seed", "w")
	if a != b || !strings.Contains(a, "p_select=") {
		t.Fatalf("alias differs:\n%s\n---\n%s", a, b)
	}
}

// The JSON draw is for a screen that fetches title, company and place through
// the jobs row contract: it carries keys, strata and probabilities and no
// posting text at all.
func TestAuditJSONHoldsNoPostingText(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 40, 8, 8)
	d, raw := drawJSON(t, "--seed", "w1", "--blind")
	if d.Seed != "w1" || !d.Blind || len(d.Picks) != 15 {
		t.Fatalf("draw %+v", d)
	}
	for _, bad := range []string{"Engineer", "Build things", "Acme", "strong_fit", "mismatch_domain", "answered", "d1-ultra\""} {
		if strings.Contains(raw, bad) {
			t.Fatalf("draw carries %q:\n%s", bad, raw)
		}
	}
	for _, p := range d.Picks {
		if len(p) != 3 {
			t.Fatalf("pick fields %v, want key, stratum, p_select only", p)
		}
		k, _ := p["key"].(string)
		src, _ := p["stratum"].(string)
		ps, _ := p["p_select"].(float64)
		if k == "" || src == "" || !(ps > 0 && ps <= 1) {
			t.Fatalf("pick %v", p)
		}
	}
	// The same draw as --list, and as stable as it is.
	d2, _ := drawJSON(t, "--seed", "w1", "--blind")
	b1, _ := json.Marshal(d)
	b2, _ := json.Marshal(d2)
	if string(b1) != string(b2) {
		t.Fatal("json draw is not deterministic")
	}
	// --json asks nothing and writes nothing.
	if len(readLabels(t, state)) != 0 {
		t.Fatal("--json wrote labels")
	}
}

func TestAuditJSONEmptyAndUsage(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 0, 0, 0)
	d, raw := drawJSON(t)
	if len(d.Picks) != 0 || !strings.Contains(raw, `"picks":[]`) {
		t.Fatalf("empty draw must be an empty list, not null or text:\n%s", raw)
	}
	mustCode(t, 2, "audit", "--json", "--list")
}

func TestAuditBlindSessionHidesReadings(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 12, 4, 4)
	old := stdin
	t.Cleanup(func() { stdin = old })

	stdin = strings.NewReader(strings.Repeat("1\n", 15))
	sighted, _ := mustCode(t, 0, "audit", "--seed", "w1", "--scored", "2", "--dropped", "2")
	if !strings.Contains(sighted, "Current triage") || !strings.Contains(sighted, "dropped:") {
		t.Fatalf("sighted session shows no readings:\n%s", sighted)
	}
	stdin = strings.NewReader(strings.Repeat("3\n", 15))
	blind, _ := mustCode(t, 0, "audit", "--blind", "--seed", "w1", "--scored", "2", "--dropped", "2")
	for _, leak := range []string{"Current triage", "strong_fit", "mismatch_domain", "dropped:", "decile:", "d1-ultra", "title"} {
		if strings.Contains(blind, leak) {
			t.Fatalf("blind session leaks %q:\n%s", leak, blind)
		}
	}
	if !strings.Contains(blind, "audited ") || !strings.Contains(blind, "Title:") {
		t.Fatalf("blind session:\n%s", blind)
	}
	n := 0
	for _, e := range readLabels(t, state) {
		if e.Source == "" || e.PSelect <= 0 {
			t.Fatalf("blind label lost its stratum: %+v", e)
		}
		n++
	}
	if n == 0 {
		t.Fatal("blind session wrote nothing")
	}
}

func TestAuditAnswerWritesTheDrawnStratum(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 30, 6, 6)
	d, _ := drawJSON(t, "--seed", "w1")
	p := d.Picks[0]
	key, src, ps := p["key"].(string), p["stratum"].(string), p["p_select"].(float64)

	// With the draw's own numbers passed back, as the app does.
	o, _ := mustCode(t, 0, "audit", "answer", "--key", key, "--verdict", "maybe", "--source", src, "--p-select", jsonNum(ps))
	if !strings.Contains(o, "maybe") {
		t.Fatalf("stdout %q", o)
	}
	e := readLabels(t, state)[posting.Key(key)]
	if e.Labels[eval.WouldApply] != "maybe" || e.Source != src || e.PSelect != ps || e.TextSHA256 == "" {
		t.Fatalf("label %+v, want maybe at %s %v with the text hash", e, src, ps)
	}
	// Answering twice never double-counts a posting.
	if _, se := mustCode(t, 1, "audit", "answer", "--key", key, "--verdict", "no", "--source", src, "--p-select", jsonNum(ps)); !strings.Contains(se, "already") {
		t.Fatalf("second answer: %q", se)
	}
	if got := readLabels(t, state)[posting.Key(key)].Labels[eval.WouldApply]; got != "maybe" {
		t.Fatalf("second answer overwrote the first: %q", got)
	}

	// Without the numbers, the current draw supplies them (same seed).
	d2, _ := drawJSON(t, "--seed", "w1")
	q := d2.Picks[0]
	mustCode(t, 0, "audit", "answer", "--seed", "w1", "--key", q["key"].(string), "--verdict", "skip")
	e2 := readLabels(t, state)[posting.Key(q["key"].(string))]
	if e2.Labels[eval.WouldApply] != eval.Skip || e2.Source != q["stratum"] || e2.PSelect != q["p_select"] {
		t.Fatalf("looked-up label %+v vs draw %v", e2, q)
	}
}

// The app reads --json replies only: a success is the label written, a failure
// is {"error"} on stdout with exit 1, never an empty reply.
func TestAuditAnswerJSONReplies(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 10, 2, 2)
	o, _ := mustCode(t, 0, "audit", "answer", "--json", "--key", "gh:k:001", "--verdict", "yes", "--source", "decile:3", "--p-select", "0.5")
	var ok auditAnswerJSON
	if err := json.Unmarshal([]byte(o), &ok); err != nil || ok.Key != "gh:k:001" || ok.Verdict != "yes" || ok.Stratum != "decile:3" || ok.PSelect != 0.5 {
		t.Fatalf("reply %q (%v)", o, err)
	}
	o, _ = mustCode(t, 1, "audit", "answer", "--json", "--key", "gh:k:001", "--verdict", "no", "--source", "decile:3", "--p-select", "0.5")
	var fail struct{ Error string }
	if err := json.Unmarshal([]byte(o), &fail); err != nil || !strings.Contains(fail.Error, "already answered") {
		t.Fatalf("failure reply %q (%v)", o, err)
	}
	// An unreadable label store fails the draw the same way.
	lf := filepath.Join(state, "eval", "labels", eval.LabelsFile)
	if err := os.Remove(lf); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lf, 0o755); err != nil {
		t.Fatal(err)
	}
	o, _ = mustCode(t, 1, "audit", "--json")
	if err := json.Unmarshal([]byte(o), &fail); err != nil || !strings.Contains(fail.Error, "unreadable") {
		t.Fatalf("draw failure reply %q (%v)", o, err)
	}
}

func jsonNum(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func TestAuditAnswerAdversarial(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 10, 2, 2)
	for name, args := range map[string][]string{
		"no key":           {"audit", "answer", "--verdict", "yes", "--source", "decile:1", "--p-select", "0.5"},
		"no verdict":       {"audit", "answer", "--key", "gh:k:000", "--source", "decile:1", "--p-select", "0.5"},
		"bad verdict":      {"audit", "answer", "--key", "gh:k:000", "--verdict", "definitely", "--source", "decile:1", "--p-select", "0.5"},
		"source without p": {"audit", "answer", "--key", "gh:k:000", "--verdict", "yes", "--source", "decile:1"},
		"p without source": {"audit", "answer", "--key", "gh:k:000", "--verdict", "yes", "--p-select", "0.5"},
		"stray argument":   {"audit", "answer", "--key", "gh:k:000", "--verdict", "yes", "--source", "decile:1", "--p-select", "0.5", "extra"},
		"oversized key":    {"audit", "answer", "--key", strings.Repeat("k", 5000), "--verdict", "yes", "--source", "decile:1", "--p-select", "0.5"},
		"oversized seed":   {"audit", "answer", "--key", "gh:k:000", "--verdict", "yes", "--seed", strings.Repeat("s", 300)},
	} {
		if o, _ := mustCode(t, 2, args...); o != "" {
			t.Errorf("%s: wrote %q", name, o)
		}
	}
	for name, args := range map[string][]string{
		"unknown key":    {"audit", "answer", "--key", "gh:nope:1", "--verdict", "yes", "--source", "decile:1", "--p-select", "0.5"},
		"p out of range": {"audit", "answer", "--key", "gh:k:000", "--verdict", "yes", "--source", "decile:1", "--p-select", "1.5"},
		"p zero":         {"audit", "answer", "--key", "gh:k:000", "--verdict", "yes", "--source", "decile:1", "--p-select", "0"},
		"p NaN":          {"audit", "answer", "--key", "gh:k:000", "--verdict", "yes", "--source", "decile:1", "--p-select", "NaN"},
		"bad source":     {"audit", "answer", "--key", "gh:k:000", "--verdict", "yes", "--source", "Decile 1!", "--p-select", "0.5"},
	} {
		if o, _ := mustCode(t, 1, args...); o != "" {
			t.Errorf("%s: wrote %q", name, o)
		}
	}
	// Not in the current draw and no numbers given: refuse rather than guess.
	if _, se := mustCode(t, 1, "audit", "answer", "--seed", "w1", "--scored", "1", "--dropped", "1", "--key", "gh:k:009", "--verdict", "yes"); !strings.Contains(se, "not in the draw") && !strings.Contains(se, "--source") {
		t.Errorf("stderr %q", se)
	}
	if n := len(readLabels(t, state)); n != 0 {
		t.Fatalf("%d labels written by refused answers", n)
	}
}
