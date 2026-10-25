package rkbcoverage

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func sel(ids ...string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"sections": []any{
		map[string]any{"title": "Summary", "bullets": ids[:min(1, len(ids))]},
		map[string]any{"title": "Experience", "entries": []any{map[string]any{"entry": "job", "bullets": ids[min(1, len(ids)):]}}},
	}})
	return b
}

func pool(texts ...string) []Bullet {
	var out []Bullet
	for i, t := range texts {
		out = append(out, Bullet{ID: fmt.Sprintf("b%d", i+1), Text: t})
	}
	return out
}

func mustCompute(t *testing.T, in Input) Report {
	t.Helper()
	r, err := Compute(in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func hitTerms(r Report) []string {
	out := []string{}
	for _, h := range r.Hit {
		out = append(out, h.Term)
	}
	return out
}

func TestCoverageSplitsHitMissAndUncovered(t *testing.T) {
	r := mustCompute(t, Input{
		Selection: sel("b1", "b2"),
		Pool: pool(
			"Built training pipelines in Python.",
			"Optimized CUDA for attention.",
			"Shipped a Rust service.", // eligible but not chosen
		),
		Posting: []string{"Python", "CUDA", "Rust", "Kubernetes"},
	})
	if got := hitTerms(r); !reflect.DeepEqual(got, []string{"Python", "CUDA"}) {
		t.Errorf("hit = %v", got)
	}
	if want := []string{"Rust", "Kubernetes"}; !reflect.DeepEqual(r.Miss, want) {
		t.Errorf("miss = %v, want %v", r.Miss, want)
	}
	// Rust is in the pool (a swap fixes it); Kubernetes is in no eligible bullet.
	if want := []string{"Kubernetes"}; !reflect.DeepEqual(r.Uncovered, want) {
		t.Errorf("uncovered = %v, want %v", r.Uncovered, want)
	}
	if r.Hit[0].Bullets[0] != "b1" || r.Hit[1].Bullets[0] != "b2" || r.Hit[0].N != 1 {
		t.Errorf("hit = %+v", r.Hit)
	}
	if r.TermsTotal != 4 || r.TermsTruncated || len(r.Unresolved) != 0 || len(r.Unjudged) != 0 {
		t.Errorf("report = %+v", r)
	}
	// Uncovered is always part of miss, never beside hit.
	for _, u := range r.Uncovered {
		if !contains(r.Miss, u) || contains(hitTerms(r), u) {
			t.Errorf("%q is uncovered but not only a miss", u)
		}
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestCoverageMatchesAsJobinsightDoes(t *testing.T) {
	r := mustCompute(t, Input{
		Selection: sel("b1", "b2", "b3"),
		Pool: pool(
			"PYTHON, c++; and (machine learning) systems.",       // case and punctuation
			"Let's go to market with golang services.",           // "go to market" is a stop phrase; golang is Go
			"Trained ML models; wrote agentic systems at scale.", // aliases of machine learning and llm agents
		),
		Posting: []string{"Python", "C++", "machine learning", "Go", "agents"},
	})
	if got := hitTerms(r); !reflect.DeepEqual(got, []string{"Python", "C++", "machine learning", "Go", "agents"}) {
		t.Errorf("hit = %v (miss %v)", got, r.Miss)
	}
	for _, h := range r.Hit {
		if h.Term == "machine learning" && !reflect.DeepEqual(h.Bullets, []string{"b1", "b3"}) {
			t.Errorf("machine learning bullets = %v", h.Bullets)
		}
	}
}

func TestCoverageIsWholeWordNotSubstring(t *testing.T) {
	r := mustCompute(t, Input{
		Selection: sel("b1"),
		Pool:      pool("Pythonic style guides, CUDAs and Rusty tools."),
		Posting:   []string{"Python", "CUDA", "Rust"},
	})
	if len(r.Hit) != 0 || len(r.Miss) != 3 || len(r.Uncovered) != 3 {
		t.Errorf("report = %+v", r)
	}
	// A case-sensitive term is not "go" the verb at a sentence start.
	r = mustCompute(t, Input{Selection: sel("b1"), Pool: pool("Go build things. We go far."), Posting: []string{"Go"}})
	if len(r.Hit) != 0 {
		t.Errorf("Go hit in plain English: %+v", r.Hit)
	}
}

func TestCoverageMatchesAVersionsOwnPhrases(t *testing.T) {
	r := mustCompute(t, Input{
		Selection: sel("b1", "b2"),
		Pool: pool(
			"Cut P99 latency by half; built Scikit-Learn pipelines.",
			"Ran the distributed training stack, daily.",
			"Wrote documentation.",
		),
		JD: []string{"latency", "scikit-learn", "Distributed   Training", "tracing", "  "},
	})
	if got := hitTerms(r); !reflect.DeepEqual(got, []string{"latency", "scikit-learn", "Distributed Training"}) {
		t.Errorf("hit = %v", got)
	}
	if !reflect.DeepEqual(r.Miss, []string{"tracing"}) || !reflect.DeepEqual(r.Uncovered, []string{"tracing"}) {
		t.Errorf("miss %v uncovered %v", r.Miss, r.Uncovered)
	}
	if r.TermsTotal != 4 {
		t.Errorf("terms = %d", r.TermsTotal)
	}
}

func TestCoverageMergesTheSameTermFromBothSources(t *testing.T) {
	r := mustCompute(t, Input{
		Selection: sel("b1"),
		Pool:      pool("Python tooling."),
		Posting:   []string{"Python"},
		JD:        []string{"python", "PYTHON"},
	})
	if len(r.Hit) != 1 || r.Hit[0].Term != "Python" || r.TermsTotal != 1 {
		t.Errorf("report = %+v", r)
	}
}

func TestCoverageATermOnlyInAnIneligibleBulletIsUncovered(t *testing.T) {
	// b9 says CUDA, but it is not in the pool (ineligible now): choosing it
	// covers nothing, and no eligible bullet covers CUDA.
	r := mustCompute(t, Input{
		Selection: sel("b1", "b9"),
		Pool:      pool("Python tooling."),
		Posting:   []string{"Python", "CUDA"},
	})
	if !reflect.DeepEqual(r.Uncovered, []string{"CUDA"}) || !reflect.DeepEqual(r.Miss, []string{"CUDA"}) {
		t.Errorf("miss %v uncovered %v", r.Miss, r.Uncovered)
	}
	// The unseen bullet is named, so the miss is not read as a clean one.
	if !reflect.DeepEqual(r.Unresolved, []string{"b9"}) {
		t.Errorf("unresolved = %v", r.Unresolved)
	}
}

func TestCoverageEmptyInputs(t *testing.T) {
	// An empty selection covers nothing: every term misses.
	r := mustCompute(t, Input{Selection: json.RawMessage(`{"sections":[]}`), Pool: pool("Python."), Posting: []string{"Python", "Rust"}})
	if len(r.Hit) != 0 || !reflect.DeepEqual(r.Miss, []string{"Python", "Rust"}) || !reflect.DeepEqual(r.Uncovered, []string{"Rust"}) {
		t.Errorf("empty selection: %+v", r)
	}
	// No keywords: nothing to judge, and every list is an empty list, never null.
	r = mustCompute(t, Input{Selection: sel("b1"), Pool: pool("Python.")})
	b, _ := json.Marshal(r)
	for _, f := range []string{"hit", "miss", "uncovered", "unjudged", "unresolved"} {
		if !strings.Contains(string(b), `"`+f+`":[]`) {
			t.Errorf("%s is not []: %s", f, b)
		}
	}
	// An empty pool: every term is uncovered.
	r = mustCompute(t, Input{Selection: sel("b1"), Posting: []string{"Python"}})
	if !reflect.DeepEqual(r.Uncovered, []string{"Python"}) || !reflect.DeepEqual(r.Unresolved, []string{"b1"}) {
		t.Errorf("empty pool: %+v", r)
	}
}

func TestCoverageRefusesABadSelection(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"none":      nil,
		"not json":  json.RawMessage(`{"sections":`),
		"a list":    json.RawMessage(`[1]`),
		"oversized": json.RawMessage(`{"sections":[],"x":"` + strings.Repeat("a", MaxSelectionBytes) + `"}`),
	} {
		if _, err := Compute(Input{Selection: raw, Posting: []string{"Python"}}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ids := make([]string, MaxSelected+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("x%d", i)
	}
	b, _ := json.Marshal(map[string]any{"sections": []any{map[string]any{"bullets": ids}}})
	if _, err := Compute(Input{Selection: b}); err == nil {
		t.Error("an oversized choice was accepted")
	}
	if _, err := Compute(Input{Selection: sel("b1"), Pool: make([]Bullet, MaxPool+1)}); err == nil {
		t.Error("an oversized pool was accepted")
	}
}

func TestCoverageReadsEachIDOnceWhateverTheShape(t *testing.T) {
	b := json.RawMessage(`{"sections":[{"title":"S","bullets":["b1","b1",""]},{"title":"E","entries":[{"entry":"e","bullets":["b2","b1"]},{"entry":"f","bullets":null}]}],"jd_keywords":["x"],"header":"ignored"}`)
	got, err := SelectedIDs(b)
	if err != nil || !reflect.DeepEqual(got, []string{"b1", "b2"}) {
		t.Errorf("ids = %v, %v", got, err)
	}
}

func TestCoverageCapsAreCarriedNotHidden(t *testing.T) {
	// 300 posting-style terms: only the first MaxTerms are judged and the total says so.
	var jd []string
	for i := 0; i < 300; i++ {
		jd = append(jd, fmt.Sprintf("term%d", i))
	}
	r := mustCompute(t, Input{Selection: sel("b1"), Pool: pool("term0 term1"), JD: jd})
	if r.TermsTotal != 300 || !r.TermsTruncated || len(r.Hit)+len(r.Miss) != MaxTerms {
		t.Errorf("total %d truncated %v judged %d", r.TermsTotal, r.TermsTruncated, len(r.Hit)+len(r.Miss))
	}
	// A term in 60 chosen bullets lists 40 ids and says 60.
	var texts []string
	var ids []string
	for i := 0; i < 60; i++ {
		texts = append(texts, "Python work")
		ids = append(ids, fmt.Sprintf("b%d", i+1))
	}
	r = mustCompute(t, Input{Selection: sel(ids...), Pool: pool(texts...), Posting: []string{"Python"}})
	if len(r.Hit) != 1 || len(r.Hit[0].Bullets) != MaxBulletsPerTerm || r.Hit[0].N != 60 {
		t.Errorf("hit = %d ids, n %d", len(r.Hit[0].Bullets), r.Hit[0].N)
	}
}

func TestCoverageUnjudgedPhrasesAreNeitherHitNorMiss(t *testing.T) {
	long := "one two three four five six" // over 4 tokens
	r := mustCompute(t, Input{
		Selection: sel("b1"),
		Pool:      pool("Python. one two three four five six."),
		JD:        []string{long, "a|b", "=Go", "---", strings.Repeat("x", 70), "Python"},
	})
	if want := []string{"---", "=Go", "a|b", long, strings.Repeat("x", 70)}; len(r.Unjudged) != len(want) {
		t.Errorf("unjudged = %v", r.Unjudged)
	}
	if got := hitTerms(r); !reflect.DeepEqual(got, []string{"Python"}) || len(r.Miss) != 0 {
		t.Errorf("hit %v miss %v", got, r.Miss)
	}
	if r.TermsTotal != 6 {
		t.Errorf("terms = %d", r.TermsTotal)
	}
}

func TestCoverageTwoSpellingsOfOneTokenSequenceDoNotBreakTheRest(t *testing.T) {
	// "scikit-learn," and "scikit-learn" tokenize alike; the second is unjudged,
	// the others still match.
	r := mustCompute(t, Input{
		Selection: sel("b1"),
		Pool:      pool("scikit-learn and tracing."),
		JD:        []string{"scikit-learn", "scikit-learn,", "tracing"},
	})
	if got := hitTerms(r); !reflect.DeepEqual(got, []string{"scikit-learn", "tracing"}) || !reflect.DeepEqual(r.Unjudged, []string{"scikit-learn,"}) {
		t.Errorf("hit %v unjudged %v", got, r.Unjudged)
	}
}

func TestCoverageNeverCarriesBulletText(t *testing.T) {
	r := mustCompute(t, Input{
		Selection: sel("b1"),
		Pool:      pool("Built SECRET-WORDING in Python."),
		Posting:   []string{"Python", "Rust"},
	})
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "SECRET") {
		t.Errorf("bullet text in the reply: %s", b)
	}
}
