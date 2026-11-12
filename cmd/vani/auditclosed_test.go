package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// seedMore appends records of one status to the ledger seedAudit wrote.
func seedMore(t *testing.T, stateDir, prefix string, n int, st posting.Status, ann map[string]string) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		k := posting.Key(fmt.Sprintf("%s:%03d", prefix, i))
		p := posting.Posting{Key: k, ATS: "gh", Company: "Acme", Title: "Engineer " + string(k), Description: "Build things. " + string(k)}
		a := map[string]string{}
		for kk, v := range ann {
			a[kk] = v
		}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: k, Status: st, Posting: &p, Annotations: a}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

// drawn is `audit --list`'s picks: stratum to keys.
func drawn(t *testing.T, args ...string) map[string][]string {
	t.Helper()
	o, _ := mustCode(t, 0, append([]string{"audit", "--list"}, args...)...)
	out := map[string][]string{}
	for _, ln := range strings.Split(o, "\n") {
		if f := strings.Split(ln, "\t"); len(f) == 3 {
			out[f[0]] = append(out[f[0]], f[2])
		}
	}
	return out
}

// Dead and duplicate postings were never drawn, so a liveness or duplicate
// fold that lost a wanted role could not show up anywhere. The closed group
// draws them with its own places; the scored and dropped draw is unchanged.
func TestAuditDrawsClosedPostingsWithTheirOwnPlaces(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 30, 10, 10)
	before := drawn(t, "--seed", "w1")
	seedMore(t, state, "gh:dead", 8, posting.Dead, nil)
	seedMore(t, state, "gh:dup", 4, posting.Duplicate, map[string]string{pipeline.AnnDupMethod: pipeline.DupMethodShingle})

	after := drawn(t, "--seed", "w1")
	if len(after["closed:dead"]) != 1 || len(after["closed:duplicate:shingle"]) != 1 {
		t.Fatalf("closed picks %v", after)
	}
	for src, keys := range before {
		if strings.Join(after[src], ",") != strings.Join(keys, ",") {
			t.Errorf("%s moved: %v then %v", src, keys, after[src])
		}
	}
	if off := drawn(t, "--seed", "w1", "--closed", "0"); len(off["closed:dead"])+len(off["closed:duplicate:shingle"]) != 0 {
		t.Errorf("--closed 0 drew closed postings: %v", off)
	}

	// The JSON draw carries the stratum and p_select the answer records.
	o, _ := mustCode(t, 0, "audit", "--json", "--seed", "w1")
	var d struct {
		Picks []struct {
			Key     string  `json:"key"`
			Stratum string  `json:"stratum"`
			PSelect float64 `json:"p_select"`
		} `json:"picks"`
	}
	if err := json.Unmarshal([]byte(o), &d); err != nil {
		t.Fatal(err)
	}
	var key string
	for _, p := range d.Picks {
		if p.Stratum == "closed:dead" {
			key = p.Key
			if p.PSelect != 1.0/8 {
				t.Errorf("closed:dead p_select %v, want 1/8", p.PSelect)
			}
		}
	}
	if key == "" {
		t.Fatalf("no closed:dead pick in %s", o)
	}
	mustCode(t, 0, "audit", "answer", "--key", key, "--verdict", "yes", "--seed", "w1")
	labels := readLabels(t, state)
	if e := labels[posting.Key(key)]; e.Source != "closed:dead" || e.PSelect != 1.0/8 {
		t.Errorf("label %+v", e)
	}
	rep, _ := mustCode(t, 0, "relevance")
	if !strings.Contains(rep, "## Wanted among closed postings") || !strings.Contains(rep, "| closed:dead | 1/1") {
		t.Errorf("relevance lacks the closed stratum:\n%s", rep)
	}
	// A closed posting is not in the recall pool: its label is left out of it.
	if !strings.Contains(rep, "1 on postings no longer kept or dropped") {
		t.Errorf("relevance does not say the closed label is out of the recall pool:\n%s", rep)
	}
}

// An exclusion drop is drawn as its own class, not as the model's.
func TestAuditClassesExclusionDropsApart(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 10, 0, 3)
	seedMore(t, state, "gh:x", 3, posting.Dropped, map[string]string{
		"triage.backend": "d1-ultra", "triage.role_fit": "mismatch_domain", "exclude.matched": `excluded: title matches "sales"`,
	})
	got := drawn(t, "--seed", "w1")
	if len(got["dropped:exclude:title"]) == 0 {
		t.Errorf("no exclusion class drawn: %v", got)
	}
	for _, k := range got["dropped:model:other"] {
		if strings.HasPrefix(k, "gh:x:") {
			t.Errorf("exclusion drop %s filed under the model", k)
		}
	}
}

// An inherited drop (a reading copied from an identical posting) is drawn,
// and classed by the copied reading: the audit samples the dropped population,
// and the copy was dropped by today's policy on that reading. The label queue
// (eval.Queue) leaves copies out because there a copy would be a second
// observation of one backend reading; the would_apply audit asks about the
// posting, so leaving copies out would shrink the population it stands for.
func TestAuditDrawsInheritedDropsUnderTheirReading(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 10, 0, 0)
	seedMore(t, state, "gh:inh", 2, posting.Dropped, map[string]string{
		"triage.backend": "d1-ultra", "triage.role_fit": "mismatch_domain", ledger.AnnTriageInheritedFrom: "gh:src:1",
	})
	got := drawn(t, "--seed", "w1")
	if len(got["dropped:model:other"]) != 2 {
		t.Errorf("inherited drops: %v", got)
	}
}

func TestRelevanceJSONCarriesTheRecallEstimate(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 10, 2, 2)
	// No labels: insufficient, said so, and no ledger read.
	o, _ := mustCode(t, 0, "relevance", "--json")
	var r struct {
		DropRecall *struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"drop_recall"`
	}
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatal(err)
	}
	if r.DropRecall == nil || r.DropRecall.Status != "insufficient" || !strings.Contains(r.DropRecall.Reason, "no judged") {
		t.Errorf("no labels: %s", o)
	}
	txt, _ := mustCode(t, 0, "relevance")
	if !strings.Contains(txt, "Recall: insufficient") {
		t.Errorf("text:\n%s", txt)
	}
}
