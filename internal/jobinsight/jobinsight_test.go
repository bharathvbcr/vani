package jobinsight

import (
	"math"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func TestFresh(t *testing.T) {
	seen := now.Add(-5 * 24 * time.Hour)
	cases := []struct {
		name         string
		posted, seen time.Time
		want         time.Time
		src          FreshSource
	}{
		{"posted before seen", now.Add(-30 * 24 * time.Hour), seen, now.Add(-30 * 24 * time.Hour), FromPosted},
		{"no posted date", time.Time{}, seen, seen, FromSeen},
		{"placeholder date", time.Unix(0, 0), seen, seen, FromSeen},
		{"posted after seen by a day is skew", seen.Add(24 * time.Hour), seen, seen.Add(24 * time.Hour), FromPosted},
		{"posted well after first seen (a repost)", seen.Add(4 * 24 * time.Hour), seen, seen, FromSeen},
		{"future date", now.Add(10 * 24 * time.Hour), time.Time{}, time.Time{}, FromSeen},
		{"neither", time.Time{}, time.Time{}, time.Time{}, FromSeen},
	}
	for _, c := range cases {
		got, src := Fresh(c.posted, c.seen, now)
		if !got.Equal(c.want) || src != c.src {
			t.Errorf("%s: got %v %s, want %v %s", c.name, got, src, c.want, c.src)
		}
	}
}

func TestAgeDays(t *testing.T) {
	if got := AgeDays(time.Time{}, now); got != -1 {
		t.Errorf("zero time: %d, want -1", got)
	}
	if got := AgeDays(now.Add(time.Hour), now); got != 0 {
		t.Errorf("future: %d, want 0", got)
	}
	if got := AgeDays(now.Add(-49*time.Hour), now); got != 2 {
		t.Errorf("49h: %d, want 2", got)
	}
}

func TestRelevance(t *testing.T) {
	r := func(v float64) *float64 { return &v }
	nan := math.NaN()
	cases := []struct {
		name  string
		rank  *float64
		fit   string
		hits  int
		want  float64
		basis Basis
	}{
		{"rank wins over fit", r(80), "mismatch_domain", 0, 60, BasisRank},
		{"rank above 100 is clamped", r(130), "", 0, 75, BasisRank},
		{"negative rank", r(-5), "", 0, 0, BasisRank},
		{"NaN rank falls to fit", &nan, "strong_fit", 0, 63.8, BasisFit},
		{"fit only", nil, "partial_fit", 0, 48.8, BasisFit},
		{"unknown fit", nil, "bogus", 0, 26.3, BasisNone},
		{"keywords lift", nil, "", 3, 42.1, BasisNone},
		{"many keywords saturate below 100", r(100), "", 1000, 100, BasisRank},
		{"negative hits", nil, "", -4, 26.3, BasisNone},
	}
	for _, c := range cases {
		got, basis := Relevance(c.rank, c.fit, c.hits)
		if got != c.want || basis != c.basis {
			t.Errorf("%s: got %v %s, want %v %s", c.name, got, basis, c.want, c.basis)
		}
	}
	// More target terms never lowers relevance.
	prev := -1.0
	for h := 0; h < 30; h++ {
		v, _ := Relevance(nil, "adjacent", h)
		if v < prev {
			t.Fatalf("relevance fell from %v to %v at %d hits", prev, v, h)
		}
		prev = v
	}
}

func TestPriority(t *testing.T) {
	if got := Priority(80, 0, 14); got != 80 {
		t.Errorf("fresh: %v, want 80", got)
	}
	if got := Priority(80, 14, 14); got != 60 {
		t.Errorf("one half-life: %v, want 60", got)
	}
	if got := Priority(80, -1, 14); got != 40 {
		t.Errorf("unknown age gets the floor: %v, want 40", got)
	}
	if got := Priority(80, 100000, 14); got != 40 {
		t.Errorf("ancient: %v, want 40", got)
	}
	if got, def := Priority(80, 14, 0), Priority(80, 14, DefaultHalfLifeDays); got != def {
		t.Errorf("bad half-life uses the default: %v vs %v", got, def)
	}
	if got := Priority(math.NaN(), 0, 14); got != 0 {
		t.Errorf("NaN relevance: %v", got)
	}
	// Older never ranks higher at the same relevance.
	for age := 1; age < 200; age++ {
		if Priority(50, age, 14) > Priority(50, age-1, 14) {
			t.Fatalf("priority rose with age at %d", age)
		}
	}
}

func TestDefaultVocabParses(t *testing.T) {
	v, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if v.Len() < 150 {
		t.Errorf("vocab has %d terms", v.Len())
	}
}

func terms(hits []Hit) map[string]int {
	out := map[string]int{}
	for _, h := range hits {
		out[h.Term] = h.Count
	}
	return out
}

func TestExtract(t *testing.T) {
	v, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	text := "We build LLM inference in Go and Rust. Go to market is not a language. " +
		"You know C++, C# and .NET; Node.js and React Native apps; CI/CD with GitHub Actions.\n" +
		"React quickly to incidents. Experience with React and Kubernetes (k8s) is a plus. " +
		"Fine-tuning and fine tuning both count, as does Weights & Biases. Guard rails, not Rails apps on Ruby on Rails."
	got := terms(v.Extract(text))
	want := map[string]int{
		"LLM": 1, "inference": 1, "Go": 1, "Rust": 1, "C++": 1, "C#": 1, ".NET": 1, "Node.js": 1,
		"React Native": 1, "CI/CD": 1, "GitHub Actions": 1, "React": 1, "Kubernetes": 2,
		"fine-tuning": 2, "Weights & Biases": 1, "Rails": 2,
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s: %d, want %d (all: %v)", k, got[k], n, got)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected term %q (all: %v)", k, got)
		}
	}
}

func TestExtractOrderIsDeterministic(t *testing.T) {
	v, _ := Default()
	hits := v.Extract("Python Rust Python Kafka Rust Python Airflow")
	var names []string
	for _, h := range hits {
		names = append(names, h.Term)
	}
	if got := strings.Join(names, ","); got != "Python,Rust,Airflow,Kafka" {
		t.Errorf("order %s", got)
	}
}

func TestExtractAdversarial(t *testing.T) {
	v, _ := Default()
	for name, text := range map[string]string{
		"empty":           "",
		"punctuation":     strings.Repeat("...---&&&+++###", 1000),
		"one huge token":  strings.Repeat("a", 1<<20),
		"invalid utf8":    "Rust \xff\xfe Python",
		"control chars":   "Rust\x00Python\x1b[31m",
		"many tokens":     strings.Repeat("x ", MaxTokens*3),
		"split last rune": strings.Repeat("a", MaxScanBytes-1) + "é",
	} {
		hits := v.Extract(text) // must not panic or run away
		if len(hits) > v.Len() {
			t.Errorf("%s: %d hits", name, len(hits))
		}
	}
	// Terms past the scan bound are not read.
	far := strings.Repeat("x ", MaxScanBytes/2+10) + "Kubernetes"
	if got := terms(v.Extract(far)); got["Kubernetes"] != 0 {
		t.Error("read past MaxScanBytes")
	}
	// Invalid bytes split tokens rather than joining them.
	if got := terms(v.Extract("Rust \xff\xfe Python")); got["Rust"] != 1 || got["Python"] != 1 {
		t.Errorf("invalid utf8: %v", got)
	}
}

func TestStopPhrases(t *testing.T) {
	v, _ := Default()
	got := terms(v.Extract("Backed by Spark Capital. We use Apache Spark and our go-to-market team, and Go to market fast."))
	if got["Spark"] != 1 || got["Go"] != 0 || len(got) != 1 {
		t.Errorf("stop phrases: %v", got)
	}
	if n := v.Len(); n != len(v.terms)-3 {
		t.Errorf("Len counts stop phrases: %d of %d", n, len(v.terms))
	}
}

func TestBoilerplate(t *testing.T) {
	blurb := []Hit{{"agents", "llm", 1}}
	var companies []string
	var hits [][]Hit
	// Acme: 10 postings, every one with the blurb, 3 with Rust.
	for i := range 10 {
		h := append([]Hit{}, blurb...)
		if i < 3 {
			h = append(h, Hit{"Rust", "lang", 1})
		}
		companies, hits = append(companies, "Acme"), append(hits, h)
	}
	// Small: 3 postings, all with the same term; too few to call it a blurb.
	for range 3 {
		companies, hits = append(companies, "small"), append(hits, []Hit{{"Go", "lang", 1}})
	}
	b := Boilerplate(companies, hits)
	if !b["acme"]["agents"] || b["acme"]["Rust"] || len(b["small"]) != 0 {
		t.Fatalf("boilerplate %v", b)
	}
	kept := Without(hits[0], b["acme"])
	if len(kept) != 1 || kept[0].Term != "Rust" || len(hits[0]) != 2 {
		t.Errorf("Without %v (input %v)", kept, hits[0])
	}
	if got := Without(hits[0], nil); len(got) != 2 {
		t.Error("nil drop")
	}
	if len(Boilerplate(nil, nil)) != 0 || len(Boilerplate([]string{"a"}, nil)) != 0 {
		t.Error("empty or mismatched input")
	}
}

func TestParseVocabRefuses(t *testing.T) {
	for name, text := range map[string]string{
		"empty":         "",
		"comments only": "# nothing",
		"no term":       "lang",
		"duplicate":     "lang Go\nlang go",
		"alias dup":     "lang Rust|rs\ndata rs",
		"too long":      "lang a b c d e",
		"empty alias":   "lang Go||golang",
	} {
		if _, err := ParseVocab(text); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// Case-sensitive and case-insensitive spellings of one word may coexist.
	if _, err := ParseVocab("lang =Go\nverb go"); err != nil {
		t.Errorf("exact and loose: %v", err)
	}
}

func TestAnnotate(t *testing.T) {
	hits := []Hit{{"Python", "lang", 5}, {"Rust", "lang", 3}, {"Kafka", "data", 2}, {"Go", "lang", 1}}
	got := Annotate(hits, map[string]bool{"Go": true, "Rust": true}, map[string]bool{"Kafka": true}, 3)
	var names []string
	for _, k := range got {
		names = append(names, k.Term)
	}
	if s := strings.Join(names, ","); s != "Rust,Go,Kafka" {
		t.Errorf("order %s", s)
	}
	if !got[0].Target || got[2].Target || !got[2].Gap {
		t.Errorf("flags %+v", got)
	}
	if len(Annotate(hits, nil, nil, 0)) != 0 || len(Annotate(nil, nil, nil, 5)) != 0 {
		t.Error("limits")
	}
	if n := TargetHits(hits, map[string]bool{"Go": true, "Nope": true}); n != 1 {
		t.Errorf("TargetHits %d", n)
	}
}
