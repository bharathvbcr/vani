package jobinsight

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

const gapVocabText = `
lang Python
lang Rust
infra Kubernetes|k8s
infra Docker
data SQL
ml PyTorch
`

func gapVocab(t *testing.T) *Vocab {
	t.Helper()
	v, err := ParseVocab(gapVocabText)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func hit(term, cat string) Hit { return Hit{Term: term, Category: cat, Count: 1} }

func gapByTerm(r GapReport, term string) (Gap, bool) {
	for _, g := range r.Gaps {
		if g.Term == term {
			return g, true
		}
	}
	return Gap{}, false
}

func TestGapsRankingByWeight(t *testing.T) {
	v := gapVocab(t)
	jobs := []GapJob{
		{Key: "a", Company: "Acme", Title: "A", Priority: 90, Hits: []Hit{hit("Rust", "lang"), hit("Docker", "infra")}},
		{Key: "b", Company: "Beta", Title: "B", Priority: 50, Hits: []Hit{hit("Rust", "lang")}},
		{Key: "c", Company: "Gamma", Title: "C", Priority: 80, Hits: []Hit{hit("SQL", "data")}},
	}
	r := Gaps(jobs, v, map[string]bool{}, GapOptions{})
	if r.Roles != 3 || !r.TargetKnown || r.TotalGaps != 3 {
		t.Fatalf("report = %+v", r)
	}
	var order []string
	for _, g := range r.Gaps {
		order = append(order, g.Term)
	}
	if want := []string{"Rust", "Docker", "SQL"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	rust := r.Gaps[0]
	if rust.Roles != 2 || rust.Weight != 140 || rust.Companies != 2 || rust.Category != "lang" || rust.Missing != 0 {
		t.Errorf("rust = %+v", rust)
	}
}

func TestGapsTieBreaks(t *testing.T) {
	v := gapVocab(t)
	// Equal weight: more roles first; then more DevPrism-missing; then term.
	jobs := []GapJob{
		{Key: "a", Priority: 40, Hits: []Hit{hit("Rust", "lang"), hit("SQL", "data")}},
		{Key: "b", Priority: 40, Hits: []Hit{hit("Docker", "infra")}},
		{Key: "c", Priority: 40, Hits: []Hit{hit("Python", "lang")}},
	}
	r := Gaps(jobs, v, map[string]bool{}, GapOptions{})
	var order []string
	for _, g := range r.Gaps {
		order = append(order, g.Term)
	}
	if want := []string{"Docker", "Python", "Rust", "SQL"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	// Roles desc beats term asc at equal weight: Rust in two roles of 20.
	jobs = []GapJob{
		{Key: "a", Priority: 20, Hits: []Hit{hit("Rust", "lang")}},
		{Key: "b", Priority: 20, Hits: []Hit{hit("Rust", "lang")}},
		{Key: "c", Priority: 40, Hits: []Hit{hit("Docker", "infra")}},
	}
	r = Gaps(jobs, v, map[string]bool{}, GapOptions{})
	if r.Gaps[0].Term != "Rust" {
		t.Errorf("roles tiebreak: %+v", r.Gaps)
	}
	// Missing desc: both in one role at the same weight, one DevPrism-named.
	jobs = []GapJob{{Key: "a", Priority: 10, Missing: "SQL", Hits: []Hit{hit("Docker", "infra"), hit("SQL", "data")}}}
	r = Gaps(jobs, v, map[string]bool{}, GapOptions{})
	if r.Gaps[0].Term != "SQL" || r.Gaps[0].Missing != 1 || r.Gaps[1].Missing != 0 {
		t.Errorf("missing tiebreak: %+v", r.Gaps)
	}
}

func TestGapsTargetTermIsNotKeywordGap(t *testing.T) {
	v := gapVocab(t)
	jobs := []GapJob{{Key: "a", Priority: 50, Hits: []Hit{hit("Rust", "lang"), hit("Docker", "infra")}}}
	r := Gaps(jobs, v, map[string]bool{"Rust": true}, GapOptions{})
	if _, ok := gapByTerm(r, "Rust"); ok {
		t.Error("Rust is in the target and must not be a keyword gap")
	}
	if _, ok := gapByTerm(r, "Docker"); !ok || r.TotalGaps != 1 {
		t.Errorf("report = %+v", r)
	}
}

func TestGapsDevPrismMissingCountsEvenIfTargetNamesIt(t *testing.T) {
	v := gapVocab(t)
	jobs := []GapJob{
		{Key: "a", Priority: 60, Missing: "k8s; some free text phrase (weak)", Hits: []Hit{hit("Kubernetes", "infra")}},
		{Key: "b", Priority: 30, Missing: "Kubernetes"},
	}
	r := Gaps(jobs, v, map[string]bool{"Kubernetes": true}, GapOptions{})
	g, ok := gapByTerm(r, "Kubernetes")
	if !ok {
		t.Fatalf("report = %+v", r)
	}
	if g.Roles != 2 || g.Missing != 2 || g.Weight != 90 || g.Category != "infra" {
		t.Errorf("gap = %+v", g)
	}
	if r.TotalGaps != 1 || r.WithMissing != 2 {
		t.Errorf("free text must not become a gap: %+v", r)
	}
}

func TestGapsRoleCountedOnceWhenBothRulesFire(t *testing.T) {
	v := gapVocab(t)
	jobs := []GapJob{{Key: "a", Priority: 70, Missing: "Docker; docker", Hits: []Hit{hit("Docker", "infra"), hit("Docker", "infra")}}}
	r := Gaps(jobs, v, map[string]bool{}, GapOptions{})
	g := r.Gaps[0]
	if g.Roles != 1 || g.Missing != 1 || g.Weight != 70 {
		t.Errorf("gap = %+v", g)
	}
}

func TestGapsDevPrismCategoryFromVocab(t *testing.T) {
	v := gapVocab(t)
	r := Gaps([]GapJob{{Key: "a", Priority: 1, Missing: "pytorch"}}, v, nil, GapOptions{})
	if g := r.Gaps[0]; g.Term != "PyTorch" || g.Category != "ml" {
		t.Errorf("gap = %+v", g)
	}
}

func TestGapsNilTargetOnlyDevPrism(t *testing.T) {
	v := gapVocab(t)
	jobs := []GapJob{{Key: "a", Priority: 50, Missing: "Rust", Hits: []Hit{hit("Docker", "infra"), hit("Rust", "lang")}}}
	r := Gaps(jobs, v, nil, GapOptions{})
	if r.TargetKnown {
		t.Error("TargetKnown must be false")
	}
	if r.TotalGaps != 1 || r.Gaps[0].Term != "Rust" {
		t.Errorf("report = %+v", r)
	}
}

func TestGapsDuplicateAndEmptyKeys(t *testing.T) {
	v := gapVocab(t)
	jobs := []GapJob{
		{Key: "a", Priority: 50, Hits: []Hit{hit("Rust", "lang")}},
		{Key: "a", Priority: 99, Hits: []Hit{hit("Rust", "lang")}},
		{Key: "", Priority: 99, Hits: []Hit{hit("Rust", "lang")}, Missing: "SQL"},
		{Key: "", Priority: 99, Hits: []Hit{hit("Rust", "lang")}},
	}
	r := Gaps(jobs, v, map[string]bool{}, GapOptions{})
	if r.Roles != 1 || r.WithMissing != 0 {
		t.Fatalf("report = %+v", r)
	}
	if g := r.Gaps[0]; g.Roles != 1 || g.Weight != 50 {
		t.Errorf("gap = %+v", g)
	}
}

func TestGapsEmptyInputMarshalsEmptyArrays(t *testing.T) {
	for name, r := range map[string]GapReport{
		"nil jobs": Gaps(nil, gapVocab(t), map[string]bool{}, GapOptions{}),
		"nil all":  Gaps(nil, nil, nil, GapOptions{}),
	} {
		if r.Roles != 0 || r.Gaps == nil || len(r.Gaps) != 0 {
			t.Errorf("%s: %+v", name, r)
		}
		b, err := json.Marshal(r)
		if err != nil || !strings.Contains(string(b), `"gaps":[]`) {
			t.Errorf("%s: json = %s, err %v", name, b, err)
		}
	}
}

func TestGapsExamplesNonNil(t *testing.T) {
	jobs := []GapJob{{Key: "a", Hits: []Hit{hit("Rust", "lang")}}}
	r := Gaps(jobs, nil, map[string]bool{}, GapOptions{})
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"examples":[{`) {
		t.Errorf("json = %s", b)
	}
}

func TestGapsOversizedMissingIsBounded(t *testing.T) {
	v := gapVocab(t)
	// A term past the 4096-byte cut must not be read.
	late := strings.Repeat("x", 5000) + "; Rust"
	r := Gaps([]GapJob{{Key: "a", Priority: 1, Missing: late}}, v, nil, GapOptions{})
	if r.TotalGaps != 0 || r.WithMissing != 1 {
		t.Errorf("late term read: %+v", r)
	}
	// 1 MB of a single run, and of multibyte runes cut mid-rune.
	for _, m := range []string{strings.Repeat("a", 1<<20), strings.Repeat("é", 1<<19), "Docker;" + strings.Repeat("é", 1<<19)} {
		Gaps([]GapJob{{Key: "a", Priority: 1, Missing: m}}, v, nil, GapOptions{})
	}
	// 10,000 items: only the first 50 count.
	var items []string
	for i := range 10_000 {
		if i == 49 {
			items = append(items, "Docker")
		} else if i == 50 {
			items = append(items, "Rust")
		} else {
			items = append(items, "filler")
		}
	}
	r = Gaps([]GapJob{{Key: "a", Priority: 1, Missing: strings.Join(items, "; ")}}, v, nil, GapOptions{})
	if _, ok := gapByTerm(r, "Docker"); !ok {
		t.Error("item 50 (index 49) must count")
	}
	if _, ok := gapByTerm(r, "Rust"); ok {
		t.Error("item 51 must not count")
	}
}

func TestBoundMissing(t *testing.T) {
	cut := boundMissing(strings.Repeat("é", 3000)) // 6000 bytes
	if len(cut) > maxMissingBytes || !utf8.ValidString(cut) {
		t.Errorf("len %d valid %v", len(cut), utf8.ValidString(cut))
	}
	if got := strings.Count(boundMissing(strings.Repeat("a;", 500)), ";"); got != maxMissingItems-1 {
		t.Errorf("separators = %d", got)
	}
}

func TestGapsMalformedMissing(t *testing.T) {
	v := gapVocab(t)
	for _, m := range []string{";;;;;;", "\x00\x01\x02\x1b[31m", "\xff\xfe\xfd", "Rust\xff;\xc3", "   \t\n"} {
		r := Gaps([]GapJob{{Key: "a", Priority: 1, Missing: m}}, v, nil, GapOptions{})
		if r.Roles != 1 {
			t.Errorf("%q: %+v", m, r)
		}
	}
	// Blank (whitespace-only) is not "with missing"; semicolons-only is.
	if r := Gaps([]GapJob{{Key: "a", Missing: "  \t\n"}}, v, nil, GapOptions{}); r.WithMissing != 0 {
		t.Errorf("blank counted: %+v", r)
	}
	if r := Gaps([]GapJob{{Key: "a", Missing: ";;;"}}, v, nil, GapOptions{}); r.WithMissing != 1 || r.TotalGaps != 0 {
		t.Errorf("semicolons: %+v", r)
	}
}

func TestGapsPriorityClamped(t *testing.T) {
	v := gapVocab(t)
	hits := []Hit{hit("Rust", "lang")}
	for _, c := range []struct {
		p, want float64
	}{{-5, 0}, {math.NaN(), 0}, {math.Inf(-1), 0}, {250, 100}, {math.Inf(1), 100}, {42.34, 42.3}} {
		r := Gaps([]GapJob{{Key: "a", Priority: c.p, Hits: hits}}, v, map[string]bool{}, GapOptions{})
		if w := r.Gaps[0].Weight; w != c.want {
			t.Errorf("priority %v: weight %v, want %v", c.p, w, c.want)
		}
	}
}

func TestGapsLimitAndExamplesDefaultsAndCaps(t *testing.T) {
	var jobs []GapJob
	for i := range 300 {
		jobs = append(jobs, GapJob{Key: fmt.Sprintf("k%03d", i), Priority: 50, Hits: []Hit{hit(fmt.Sprintf("t%03d", i), "x")}})
	}
	for i := range 12 {
		jobs = append(jobs, GapJob{Key: fmt.Sprintf("s%02d", i), Priority: 50, Hits: []Hit{hit("shared", "x")}})
	}
	for _, c := range []struct{ limit, ex, wantGaps, wantEx int }{
		{0, 0, DefaultGapLimit, DefaultGapExamples},
		{-1, -1, DefaultGapLimit, DefaultGapExamples},
		{1000, 1000, MaxGapLimit, MaxGapExamples},
		{2, 1, 2, 1},
	} {
		r := Gaps(jobs, nil, map[string]bool{}, GapOptions{Limit: c.limit, Examples: c.ex})
		if len(r.Gaps) != c.wantGaps || r.TotalGaps != 301 {
			t.Errorf("%+v: gaps %d total %d", c, len(r.Gaps), r.TotalGaps)
		}
		if g := r.Gaps[0]; g.Term != "shared" || len(g.Examples) != c.wantEx {
			t.Errorf("%+v: %s examples %d", c, g.Term, len(g.Examples))
		}
	}
}

func TestGapsExampleOrderAndCleaning(t *testing.T) {
	jobs := []GapJob{
		{Key: "k1", Company: "Zed", Title: "T", Priority: 50, Hits: []Hit{hit("Rust", "lang")}},
		{Key: "k2", Company: "Acme", Title: "B", Priority: 50, Hits: []Hit{hit("Rust", "lang")}},
		{Key: "k3", Company: "Acme", Title: "A", Priority: 50, Hits: []Hit{hit("Rust", "lang")}},
		{Key: "k4", Company: "Acme", Title: "A", Priority: 50, Hits: []Hit{hit("Rust", "lang")}},
		{Key: "k5", Company: "Low", Title: "L", Priority: 10, Hits: []Hit{hit("Rust", "lang")}},
		{Key: "k6", Company: "  Evil\x00\x1b[2J\n Corp‮  ", Title: "Dev\t\tops\r\n" + strings.Repeat("x", 500), Priority: 95, Hits: []Hit{hit("Rust", "lang")}},
	}
	r := Gaps(jobs, nil, map[string]bool{}, GapOptions{Examples: 10})
	var keys []string
	for _, e := range r.Gaps[0].Examples {
		keys = append(keys, e.Key)
	}
	if want := []string{"k6", "k3", "k4", "k2", "k1", "k5"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("example order = %v, want %v", keys, want)
	}
	e := r.Gaps[0].Examples[0]
	if e.Company != "Evil [2J Corp" {
		t.Errorf("company = %q", e.Company)
	}
	if !strings.HasPrefix(e.Title, "Dev ops xxx") || utf8.RuneCountInString(e.Title) != maxLabelRunes {
		t.Errorf("title = %q (%d runes)", e.Title, utf8.RuneCountInString(e.Title))
	}
	for _, r := range e.Company + e.Title {
		if r < 0x20 || r == 0x7f || r == 0x202e {
			t.Errorf("control rune %U survived", r)
		}
	}
	// A huge multibyte company is bounded and stays valid UTF-8.
	got := cleanLabel(strings.Repeat("é", 1<<20))
	if utf8.RuneCountInString(got) > maxLabelRunes || !utf8.ValidString(got) {
		t.Errorf("clean = %d runes valid %v", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
	if got := cleanLabel("\xff\xfeok"); got != "ok" {
		t.Errorf("invalid utf-8 = %q", got)
	}
}

func TestGapsCompaniesDedupedCaseInsensitively(t *testing.T) {
	jobs := []GapJob{
		{Key: "a", Company: "Acme", Hits: []Hit{hit("Rust", "lang")}},
		{Key: "b", Company: " ACME ", Hits: []Hit{hit("Rust", "lang")}},
		{Key: "c", Company: "acme", Hits: []Hit{hit("Rust", "lang")}},
		{Key: "d", Company: "Beta", Hits: []Hit{hit("Rust", "lang")}},
		{Key: "e", Company: "", Hits: []Hit{hit("Rust", "lang")}},
	}
	r := Gaps(jobs, nil, map[string]bool{}, GapOptions{})
	if g := r.Gaps[0]; g.Roles != 5 || g.Companies != 2 {
		t.Errorf("gap = %+v", g)
	}
}

func TestGapsDeterministic(t *testing.T) {
	v := gapVocab(t)
	var jobs []GapJob
	for i := range 60 {
		jobs = append(jobs, GapJob{
			Key:      fmt.Sprintf("k%02d", i),
			Company:  fmt.Sprintf("co%d", i%7),
			Title:    fmt.Sprintf("title %d", i%5),
			Priority: float64(i%11) * 8.7,
			Missing:  []string{"Rust; Docker", "k8s", "SQL (weak)", ""}[i%4],
			Hits:     []Hit{hit([]string{"Rust", "Python", "Docker", "SQL"}[i%4], "c"), hit("PyTorch", "ml")},
		})
	}
	want := Gaps(jobs, v, map[string]bool{"SQL": true}, GapOptions{Limit: 50, Examples: 4})
	rng := rand.New(rand.NewSource(1))
	for range 20 {
		shuffled := append([]GapJob(nil), jobs...)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if got := Gaps(shuffled, v, map[string]bool{"SQL": true}, GapOptions{Limit: 50, Examples: 4}); !reflect.DeepEqual(got, want) {
			t.Fatalf("order-dependent output:\n got %+v\nwant %+v", got, want)
		}
	}
}

func TestGapsNilVocab(t *testing.T) {
	jobs := []GapJob{{Key: "a", Priority: 50, Missing: "Rust; SQL", Hits: []Hit{hit("Docker", "infra"), hit("Rust", "")}}}
	r := Gaps(jobs, nil, map[string]bool{}, GapOptions{})
	if r.WithMissing != 1 || r.TotalGaps != 2 {
		t.Errorf("report = %+v", r)
	}
	r = Gaps(jobs, nil, nil, GapOptions{})
	if r.TotalGaps != 0 || r.Gaps == nil {
		t.Errorf("report = %+v", r)
	}
}
