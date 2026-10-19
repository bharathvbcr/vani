package kbsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Synthetic inputs only.

func TestPlainTextMarkdown(t *testing.T) {
	for in, want := range map[string]string{
		"[Chronicle](https://example.invalid/c): Local-First":            "Chronicle: Local-First",
		"Harness ([MANVI](https://example.invalid/m))":                   "Harness (MANVI)",
		"[Whole Title Is A Link](https://example.invalid/w)":             "Whole Title Is A Link",
		"[ScholarLM](https://example.invalid/)":                          "ScholarLM",
		"*Recipe-Dependent Rankings* (manuscript, 2026)":                 "Recipe-Dependent Rankings (manuscript, 2026)",
		"**bold** and *it* and **more**":                                 "bold and it and more",
		"[x](https://example.invalid/a_(b)_c) tail":                      "x tail",
		"![logo](https://example.invalid/i.png) Name":                    "logo Name",
		"[![img](https://example.invalid/i)](https://example.invalid/l)": "img",
		"[](https://example.invalid/e)":                                  "",
		"[unclosed (https://example.invalid/u)":                          "[unclosed (https://example.invalid/u)",
		"[a] (https://example.invalid/space)":                            "[a] (https://example.invalid/space)",
		"[a]b](https://example.invalid/x)":                               "[a]b](https://example.invalid/x)",
		"5 * 3 * 2":                                                      "5 * 3 * 2",
		"lone * star":                                                    "lone * star",
		"** unmatched bold":                                              "** unmatched bold",
		"  spaced \t out \n title ":                                      "spaced out title",
		"Plain & Simple: C++ / Go":                                       "Plain & Simple: C++ / Go",
		"":                                                               "",
	} {
		if got := PlainText(in); got != want {
			t.Errorf("PlainText(%q) = %q, want %q", in, got, want)
		}
	}
	// The outer pair is crossed by the inner link's url, so only the inner
	// "[b](c ] d)" is a link.
	if got := PlainText("[a [b](c ] d)](e)"); got != "[a b](e)" {
		t.Errorf("crossed pairs: %q", got)
	}
}

// Adversarial nesting must stay linear: a quadratic scan of a megabyte would
// not finish inside the test timeout.
func TestPlainTextIsLinear(t *testing.T) {
	for _, in := range []string{
		strings.Repeat("[", 1<<20),
		strings.Repeat("*", 1<<20),
		strings.Repeat("[a](", 1<<18),
		strings.Repeat("* a ", 1<<18),
		strings.Repeat("[", 1<<19) + strings.Repeat("](x)", 1<<17),
	} {
		start := time.Now()
		_ = PlainText(in)
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("PlainText on %d bytes took %v", len(in), d)
		}
	}
}

func TestParseStack(t *testing.T) {
	for in, want := range map[string][]string{
		"":                                       nil,
		"  ,, ;; ":                               nil,
		"Go, Rust, Python":                       {"Go", "Rust", "Python"},
		"Go, go,  GO , Rust":                     {"Go", "Rust"},
		"Swift 6.2,  Swift   6.2":                {"Swift 6.2"},
		"Rust, Python, AWS; 2025–26":             {"Rust", "Python", "AWS"},
		"PyTorch, CUDA, NVIDIA GH200; 2025-2026": {"PyTorch", "CUDA", "NVIDIA GH200"},
		"Evo2PopgenSBI; [MANVI](https://example.invalid/a,b;c); 2026": {"Evo2PopgenSBI", "MANVI"},
		"React, TypeScript, Canvas, Tauri, Swift/Rust":                {"React", "TypeScript", "Canvas", "Tauri", "Swift/Rust"},
		"*Metal 4*, **CUDA**": {"Metal 4", "CUDA"},
	} {
		got, err := ParseStack(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ParseStack(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	many := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf("skill%d", i)
		}
		return strings.Join(parts, ", ")
	}
	if got, err := ParseStack(many(MaxSkills)); err != nil || len(got) != MaxSkills {
		t.Errorf("exactly %d skills: %d %v", MaxSkills, len(got), err)
	}
	// Duplicates do not count toward the cap.
	if got, err := ParseStack(many(MaxSkills) + ", SKILL0, skill1"); err != nil || len(got) != MaxSkills {
		t.Errorf("cap with duplicates: %d %v", len(got), err)
	}
	for name, in := range map[string]string{
		"too many skills": many(MaxSkills + 1),
		"too many bytes":  strings.Repeat("a,", MaxStackBytes),
		"name too long":   "Go, " + strings.Repeat("x", MaxSkillNameBytes+1),
		"control char":    "Go, Ru\x00st",
		"invalid utf-8":   "Go, \xff\xfe",
	} {
		if got, err := ParseStack(in); err == nil {
			t.Errorf("%s: accepted as %q", name, got)
		}
	}
	if _, err := ParseStack("Go, " + strings.Repeat("x", MaxSkillNameBytes)); err != nil {
		t.Errorf("name at the limit: %v", err)
	}
}

func TestISODates(t *testing.T) {
	for in, want := range map[string]string{
		"AUG 2023": "2023-08", "august 2023": "2023-08", "Sept 2023": "2023-09", "Sep. 2023": "2023-09",
		"2023-08": "2023-08", "2023": "2023", "DEC 1999": "1999-12",
	} {
		if got, err := isoDate(in); err != nil || got != want {
			t.Errorf("isoDate(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"Fall 2023", "08/2023", "Smarch 2023", "JAN 1800", "2023-13", "2023-8", "AUG 23", "2101", "AUG2023"} {
		if got, err := isoDate(in); err == nil {
			t.Errorf("isoDate(%q) accepted as %q", in, got)
		}
	}
	if _, _, err := ParseDates("Fall 2023 – Present"); err == nil {
		t.Error("an unreadable start must block, not be stored")
	}
	if _, _, err := ParseDates("AUG 2023 – Spring 2024"); err == nil {
		t.Error("an unreadable end must block, not be stored")
	}
}

const variantBank = `{
  "entries": {
    "proj": {"title": "[Proj](https://example.invalid/p): Thing", "stack": "Go, Rust", "origin": ["proj"], "bullets": ["shared", "base-only"]},
    "proj-v2": {"title": "Proj: Thing", "stack": "Go", "origin": ["proj"], "bullets": ["shared", "v-only-1", "v-only-2"]},
    "proj-v3": {"title": "Proj", "origin": ["proj"], "bullets": ["v-only-2"]},
    "lonely-v2": {"title": "Lonely", "origin": ["gone"], "bullets": ["lonely"]},
    "self": {"title": "Self", "origin": ["self", "proj"], "bullets": []},
    "linkonly": {"title": "[](https://example.invalid/x)", "bullets": []}
  },
  "bullets": {
    "shared": {"text": "Shared."}, "base-only": {"text": "Base."},
    "v-only-1": {"text": "Variant one."}, "v-only-2": {"text": "Variant two."}, "lonely": {"text": "Lonely."}
  }
}`

func TestVariantsAreReportedNotSynced(t *testing.T) {
	r := ResolveBank(mustBank(t, variantBank), Claims{Values: map[string]string{}})
	by := map[string]Block{}
	for _, b := range r.Blocks {
		by[b.ID] = b
	}
	for id, want := range map[string]string{"proj": "", "proj-v2": "proj", "proj-v3": "proj", "lonely-v2": "", "self": "", "linkonly": ""} {
		if got := by[id].VariantOf; got != want {
			t.Errorf("%s VariantOf = %q, want %q", id, got, want)
		}
	}
	// v-only-1 and v-only-2 are listed only by variants; "shared" is not.
	if r.VariantOnly != 2 {
		t.Errorf("VariantOnly = %d, want 2", r.VariantOnly)
	}
	if by["proj"].Title != "Proj: Thing" || !reflect.DeepEqual(by["proj"].Skills, []string{"Go", "Rust"}) || !reflect.DeepEqual(by["proj"].Personas, []string{"ai"}) {
		t.Errorf("proj = %+v", by["proj"])
	}
	if !strings.Contains(by["linkonly"].Blocked, "no title") {
		t.Errorf("a title that is only an empty link must block: %q", by["linkonly"].Blocked)
	}

	f := newFake()
	st := NewSyncState(filepath.Join(t.TempDir(), "s.json"))
	p, res, err := applyAll(t, f, r, st)
	if err != nil {
		t.Fatal(err)
	}
	if p.Count(ActionVariant) != 2 || res.Variants != 2 || res.VariantOnly != 2 {
		t.Fatalf("plan %d variants, result %+v", p.Count(ActionVariant), res)
	}
	if o := outcome(res, "proj-v2"); o.Status != StatusVariant || !strings.Contains(o.Reason, "proj") {
		t.Errorf("proj-v2 outcome %+v", o)
	}
	for _, c := range f.upserts() {
		if bytes.Contains(c.Args, []byte(`"id":"proj-v2"`)) || bytes.Contains(c.Args, []byte(`"id":"proj-v3"`)) {
			t.Errorf("a variant was sent: %s", c.Args)
		}
	}
	if st.Blocks["proj-v2"] != "" {
		t.Error("a variant must not be recorded as synced")
	}
	if !strings.Contains(res.Summary(), "2 rkb variants not synced (2 bullets appear only in them)") {
		t.Errorf("summary %q", res.Summary())
	}
	var dry bytes.Buffer
	if err := DryRun(&dry, p); err != nil || !strings.Contains(dry.String(), "2 rkb variant entries are not synced; 2 bullets appear only in them") ||
		!strings.Contains(dry.String(), "variant   proj-v2") {
		t.Errorf("dry run:\n%s", dry.String())
	}
}

func TestLoadRejectsBadOrigins(t *testing.T) {
	long := make([]string, maxOrigins+1)
	for i := range long {
		long[i] = fmt.Sprintf("o%d", i)
	}
	longJSON, _ := json.Marshal(long)
	for name, src := range map[string]string{
		"bad id":        `{"entries":{"a":{"title":"A","origin":["has space"]}},"bullets":{}}`,
		"empty id":      `{"entries":{"a":{"title":"A","origin":[""]}},"bullets":{}}`,
		"too many":      `{"entries":{"a":{"title":"A","origin":` + string(longJSON) + `}},"bullets":{}}`,
		"not a list":    `{"entries":{"a":{"title":"A","origin":"a"}},"bullets":{}}`,
		"stack not str": `{"entries":{"a":{"title":"A","stack":["Go"]}},"bullets":{}}`,
	} {
		if _, err := Parse([]byte(src)); !errors.Is(err, ErrBank) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestOversizedStackBlocksItsBlock(t *testing.T) {
	// Many copies of one skill collapse to one: not over the cap.
	src := `{"entries":{"a":{"title":"A","stack":"` + strings.Repeat("x,", MaxSkills+1) + `"}},"bullets":{}}`
	r := ResolveBank(mustBank(t, src), Claims{Values: map[string]string{}})
	if len(r.Blocks) != 1 || r.Blocks[0].Blocked != "" || len(r.Blocks[0].Skills) != 1 {
		t.Fatalf("blocks %+v", r.Blocks)
	}
	parts := make([]string, MaxSkills+1)
	for i := range parts {
		parts[i] = fmt.Sprintf("s%d", i)
	}
	src = `{"entries":{"a":{"title":"A","stack":"` + strings.Join(parts, ",") + `"}},"bullets":{}}`
	r = ResolveBank(mustBank(t, src), Claims{Values: map[string]string{}})
	if !strings.Contains(r.Blocks[0].Blocked, "more than") {
		t.Fatalf("over the cap must block: %+v", r.Blocks[0])
	}
	p := Plan(r, DevPrismState{Blocks: map[string]json.RawMessage{}}, *NewSyncState(""), fixedNow)
	if p.Count(ActionBlocked) != 1 || p.Items[0].Payload != nil {
		t.Fatalf("plan %+v", p.Items)
	}
}

func updatePayload(t *testing.T, existing string) (map[string]json.RawMessage, Item) {
	t.Helper()
	r := ResolveBank(mustBank(t, `{"entries":{"a":{"title":"A","stack":"Go, Rust, Kotlin"}},"bullets":{}}`), Claims{Values: map[string]string{}})
	p := Plan(r, DevPrismState{Blocks: map[string]json.RawMessage{"a": json.RawMessage(existing)}}, *NewSyncState(""), fixedNow)
	it := p.Items[0]
	var got map[string]json.RawMessage
	if it.Payload != nil {
		if err := json.Unmarshal(it.Payload, &got); err != nil {
			t.Fatal(err)
		}
	}
	return got, it
}

func TestUpdateUnionsSkillsAndPersonas(t *testing.T) {
	existing := `{"id":"a","kind":"project","title":"A","org":"","dateRange":{"start":"","end":null},
	  "personas":["management"],"domains":[],"skills":[{"name":"rust","level":5,"years":2.5},{"name":"Typst","level":2}],
	  "seniorityLevel":"senior","updatedAt":"1","bullets":[],"facts":[]}`
	got, it := updatePayload(t, existing)
	if it.Action != ActionUpdate {
		t.Fatalf("%s %s", it.Action, it.Reason)
	}
	var skills []map[string]any
	if err := json.Unmarshal(got["skills"], &skills); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"name": "rust", "level": 5.0, "years": 2.5}, // DevPrism's own, kept whole
		{"name": "Typst", "level": 2.0},
		{"name": "Go", "level": 3.0},
		{"name": "Kotlin", "level": 3.0}, // "Rust" matched "rust" and was not added
	}
	if !reflect.DeepEqual(skills, want) {
		t.Errorf("skills = %v", skills)
	}
	if string(got["personas"]) != `["management","ai"]` {
		t.Errorf("personas = %s", got["personas"])
	}

	// Key order in DevPrism's answer does not change the bytes sent.
	reordered := strings.Replace(existing, `{"name":"rust","level":5,"years":2.5}`, `{"years":2.5,"level":5,"name":"rust"}`, 1)
	_, it2 := updatePayload(t, reordered)
	if !bytes.Equal(it.Payload, it2.Payload) {
		t.Errorf("payload depends on DevPrism's key order:\n%s\n%s", it.Payload, it2.Payload)
	}

	// Missing or null fields take rkb's.
	got, _ = updatePayload(t, `{"id":"a","kind":"project","title":"A","org":"","dateRange":{"start":"","end":null},"skills":null,"seniorityLevel":"ic","updatedAt":"1","bullets":[]}`)
	if string(got["personas"]) != `["ai"]` || !strings.Contains(string(got["skills"]), `"Kotlin"`) {
		t.Errorf("null fields: %s %s", got["personas"], got["skills"])
	}
}

func TestUpdateBlocksOnUnreadableTags(t *testing.T) {
	base := `{"id":"a","kind":"project","title":"A","org":"","dateRange":{"start":"","end":null},"seniorityLevel":"ic","updatedAt":"1","bullets":[],%s}`
	for name, field := range map[string]string{
		"skills not a list":   `"skills":"Go"`,
		"skill not an object": `"skills":["Go"]`,
		"skill without name":  `"skills":[{"level":3}]`,
		"skill name a number": `"skills":[{"name":7,"level":3}]`,
		"null skill":          `"skills":[null]`,
		"personas not a list": `"skills":[],"personas":"ai"`,
		"persona a number":    `"skills":[],"personas":[1]`,
	} {
		_, it := updatePayload(t, fmt.Sprintf(base, field))
		if it.Action != ActionBlocked || it.Payload != nil || it.Reason == "" {
			t.Errorf("%s: %s %q", name, it.Action, it.Reason)
		}
	}
}

func TestStackChangeChangesHash(t *testing.T) {
	h := func(stack string) string {
		r := ResolveBank(mustBank(t, `{"entries":{"a":{"title":"A","stack":"`+stack+`"}},"bullets":{}}`), Claims{Values: map[string]string{}})
		return r.Blocks[0].Hash
	}
	if h("Go") == h("Go, Rust") || h("Go") != h("Go, go") || h("Go") != h("Go; 2026") {
		t.Error("the hash must follow the parsed skills, not the raw stack text")
	}
}
