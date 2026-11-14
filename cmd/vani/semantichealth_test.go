package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestSemanticHealthLevelsAndCauses(t *testing.T) {
	cases := []struct {
		name      string
		h         semanticHealth
		warning   string
		level     string
		causeHas  string
		causeNone bool
	}{
		{"nothing open is fine", semanticHealth{}, "", semHealthOK, "", true},
		{"80% is ok", semanticHealth{Open: 10, Known: 8, Missing: 2}, "", semHealthOK, "", true},
		{"just under 80% warns", semanticHealth{Open: 1000, Known: 799, Missing: 201, StaleProfile: 0, Vectors: 900}, "", semHealthLow, "201 open postings have no score", false},
		{"50% warns", semanticHealth{Open: 10, Known: 5, Missing: 5, Vectors: 3}, "", semHealthLow, "holds 3 postings", false},
		{"under 50% is bad", semanticHealth{Open: 10, Known: 4, Missing: 6}, "", semHealthBad, "", false},
		{"no matcher: the lookup's own reason", semanticHealth{Open: 10, Missing: 10}, "knowledge-base matching has no scores yet; run `vani semantic warm`", semHealthBad, "no scores yet", false},
		{"a profile change is named before the missing count", semanticHealth{Open: 10, Missing: 10, StaleProfile: 8000, Vectors: 7600}, "", semHealthBad, "8000 cached scores were written for an earlier", false},
		{"unreadable vector count is said, not shown as 0", semanticHealth{Open: 10, Missing: 10, Vectors: -1}, "", semHealthBad, "unreadable count", false},
		{"thin only", semanticHealth{Open: 10, Known: 2, Thin: 8}, "", semHealthBad, "8 open postings have too little text", false},
	}
	for _, c := range cases {
		h := c.h
		h.classify(c.warning, 20)
		if h.Level != c.level {
			t.Errorf("%s: level %s, want %s", c.name, h.Level, c.level)
		}
		if c.causeNone && h.Cause != "" {
			t.Errorf("%s: cause %q on a healthy share", c.name, h.Cause)
		}
		if c.causeHas != "" && !strings.Contains(h.Cause, c.causeHas) {
			t.Errorf("%s: cause %q, want it to say %q", c.name, h.Cause, c.causeHas)
		}
		if !c.causeNone && h.Cause == "" {
			t.Errorf("%s: a low share with no cause", c.name)
		}
	}
	line := semanticHealth{Level: semHealthBad, Open: 4, Known: 1, Thin: 1, Missing: 2, Cause: "why"}.Line()
	if !strings.Contains(line, "known on 1 of 4 open postings (25%; 1 thin, 2 without a score): why") {
		t.Errorf("line %q", line)
	}
}

// Before a warm the similarity is unknown everywhere and both status surfaces
// say so with the cause; after it they report it known. A dropped posting is
// never counted.
func TestStatusReportsSemanticHealth(t *testing.T) {
	cfgPath, state := semanticSetup(t)
	seedInsight(t, state,
		insightSeed{key: "gh:a:1", company: "Acme", title: "Infrastructure Engineer", status: posting.Explained,
			desc: "Run k8s clusters and golang services in production", posted: daysAgo(3), seenAgo: time.Hour},
		insightSeed{key: "gh:a:2", company: "Acme", title: "Platform Engineer", status: posting.Explained,
			desc: "Operate Kubernetes and build Go tooling for developers", posted: daysAgo(3), seenAgo: time.Hour},
		insightSeed{key: "gh:a:3", company: "Acme", title: "Account Executive", status: posting.Dropped,
			desc: "Sell enterprise subscriptions", posted: daysAgo(3), seenAgo: time.Hour},
	)
	if out, _ := mustCode(t, 0, "status"); !strings.Contains(out, "semantic similarity: bad, known on 0 of 2 open postings") || !strings.Contains(out, "2 open postings have no score and the vector log holds") {
		t.Fatalf("unwarmed status:\n%s", out)
	}
	mustCode(t, 0, "semantic", "warm")
	if out, _ := mustCode(t, 0, "status"); !strings.Contains(out, "semantic similarity: ok, known on 2 of 2 open postings") {
		t.Fatalf("warmed status:\n%s", out)
	}
	o, _ := mustCode(t, 0, "semantic", "status", "--json")
	var st semanticStatus
	if err := json.Unmarshal([]byte(o), &st); err != nil {
		t.Fatal(err)
	}
	if h := st.Health; h == nil || h.Level != semHealthOK || h.Open != 2 || h.Known != 2 || h.Vectors < 2 {
		t.Fatalf("semantic status health: %+v (%s)", st.Health, st.HealthError)
	}
	if text, _ := mustCode(t, 0, "semantic", "status"); !strings.Contains(text, "semantic similarity: ok") {
		t.Errorf("semantic status text:\n%s", text)
	}

	// The knowledge base changes: every cached score is for the old profile.
	// The cause is the profile change, with the stored vectors that a warm
	// rescores from, never "no scores yet".
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(semBank, "internal developer tooling", "internal release tooling", 1)
	if err := os.WriteFile(cfg.KB.Bank, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	wireEligibleKB(t, &cfg, cfg.KB.Bank, changed)
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	out, _ := mustCode(t, 0, "status")
	if !strings.Contains(out, "semantic similarity: bad, known on 0 of 2") || !strings.Contains(out, "2 cached scores were written for an earlier knowledge-base profile") || strings.Contains(out, "no scores yet") {
		t.Fatalf("status after a profile change:\n%s", out)
	}
}

// With semantic matching off there is no line at all, never a false "bad".
func TestStatusHasNoSemanticLineWhenOff(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedInsight(t, state, insightSeed{key: "gh:a:1", company: "Acme", title: "Engineer", status: posting.Explained,
		desc: "Build things", posted: daysAgo(1), seenAgo: time.Hour})
	if out, _ := mustCode(t, 0, "status"); strings.Contains(out, "semantic similarity") {
		t.Fatalf("status with semantic off:\n%s", out)
	}
}
