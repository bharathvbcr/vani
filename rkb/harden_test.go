package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var testRoot string

func TestMain(m *testing.M) {
	// Tests run on the embedded default only: a developer's own
	// ~/.config/rkb/config.json or RKB_SKILL_DIR must never change them.
	home, err := os.MkdirTemp("", "rkb-test-home-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("HOME", home); err != nil {
		panic(err)
	}
	if err := os.Unsetenv("RKB_SKILL_DIR"); err != nil {
		panic(err)
	}
	loadConfig()
	if configSource != "embedded default" {
		panic("tests must load the embedded default, got " + configSource)
	}
	if h := os.Getenv("RKB_TEST_HELPER"); h != "" {
		codeRootFlag = os.Getenv("RKB_TEST_ROOT")
		cfg.VerifyLog = os.Getenv("RKB_TEST_LOG")
		helper(h)
		os.Exit(0)
	}
	d, err := os.MkdirTemp("", "rkb-test-root-")
	if err != nil {
		panic(err)
	}
	testRoot = d
	_ = os.MkdirAll(filepath.Join(d, cfg.CVDir), 0o755)
	codeRootFlag = d
	code := m.Run()
	_ = os.RemoveAll(d)    // best-effort temp cleanup
	_ = os.RemoveAll(home) // best-effort temp cleanup
	if rkbBinDir != "" {
		_ = os.RemoveAll(rkbBinDir) // the sample tests' built rkb (example_test.go)
	}
	os.Exit(code)
}

// helper runs inside a child process for the cross-process stress tests.
func helper(kind string) {
	n := os.Getenv("RKB_TEST_N")
	switch kind {
	case "appendlog":
		for i := 0; i < 5; i++ {
			appendLog(VerifyLog{fmt.Sprintf("proc%s-%d", n, i): {LogEntry{Pass: 2, Verdict: "CONFIRMED", By: "p" + n, At: nowISO(), Found: fmt.Sprint(i)}}})
		}
	case "review":
		bp := os.Getenv("RKB_TEST_BANK")
		for i := 0; i < 5; i++ {
			id := fmt.Sprintf("b%d", i)
			err := updateBank(bp, func(b *Bank) (bool, error) {
				return applyReview(b, "hr", []reviewItem{{id, Review{Round: 1, Verdict: "pass", TextHash: "h" + n, By: "rev" + n}}})
			})
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(3)
			}
		}
	}
}

func tmpFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if body != "" {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// ---------------------------------------------------------------- dupKeys

func TestDupKeys(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`{"a":1,"b":2}`, nil},
		{`{"a":1,"a":2}`, []string{"$.a"}},
		{`{"bullets":{"x":{"text":"1"},"x":{"text":"2"}}}`, []string{"$.bullets.x"}},
		{`{"a":{"k":1},"b":{"k":1}}`, nil},                      // same key, different objects
		{`[{"k":1},{"k":2}]`, nil},                              // array elements are separate objects
		{`[{"k":1,"k":2}]`, []string{"$[0].k"}},                 // dup inside an array element
		{`{"a":[1,{"x":1,"x":2}],"b":3}`, []string{"$.a[1].x"}}, // nested
		{`{"a":"k","k":"a"}`, nil},                              // values that look like keys
		{`{"a":{"b":{"c":1,"c":1}},"d":{"c":1}}`, []string{"$.a.b.c"}},
	}
	for _, c := range cases {
		got, err := dupKeys([]byte(c.in))
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: got %v want %v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{`{"a":`, `{"a":1}{"b":2}`, ``, `   `, `{"a":1} x`, `[1,2`} {
		if _, err := dupKeys([]byte(bad)); err == nil {
			t.Errorf("%q must be an error", bad)
		}
	}
	if err := decodeJSONStrict("x", []byte(`{"a":1,"a":2}`), &map[string]int{}); err == nil {
		t.Error("decodeJSONStrict accepted a duplicate key")
	}
}

func FuzzDupKeys(f *testing.F) {
	for _, s := range []string{`{"a":1}`, `{"a":{"a":1},"a":2}`, `[1,2,{"x":[{}]}]`, `"s"`, `{"":1,"":2}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := dupKeys([]byte(s))
		v := jsontext.Value(s)
		if err != nil {
			if v.IsValid(jsontext.AllowDuplicateNames(true)) {
				t.Fatalf("dupKeys refused valid JSON %q: %v", s, err)
			}
			return
		}
		// The strict validator is an independent oracle: it rejects a value
		// exactly when some object in it repeats a name.
		if strict := v.IsValid(); (len(d) == 0) != strict {
			t.Fatalf("dupKeys listed %v for %q, but the strict validator says valid=%v", d, s, strict)
		}
	})
}

// ---------------------------------------------------------------- droppedFields

func TestDroppedFields(t *testing.T) {
	old := `{"bullets":{"a":{"text":"x","note":"keep me","empty":""}},"list":[{"k":1,"extra":2}]}`
	if d := droppedFields([]byte(old), []byte(`{"bullets":{"a":{"text":"y"}},"list":[{"k":1,"extra":2}]}`)); strings.Join(d, ",") != "$.bullets.a.note" {
		t.Errorf("got %v", d)
	}
	if d := droppedFields([]byte(old), []byte(`{"bullets":{"a":{"text":"y","note":"k"}},"list":[{"k":1}]}`)); strings.Join(d, ",") != "$.list[0].extra" {
		t.Errorf("array element field drop not caught: %v", d)
	}
	if d := droppedFields([]byte(old), []byte(`{"bullets":{},"list":[]}`)); len(d) != 1 || d[0] != "$.bullets.a" {
		t.Errorf("dropped bullet not caught: %v", d)
	}
	if d := droppedFields([]byte(old), []byte(old)); len(d) != 0 {
		t.Errorf("identical reported %v", d)
	}
}

// ---------------------------------------------------------------- quotes

func TestQuoteInText(t *testing.T) {
	text := "## Results\n| model | **p95** |\n|---|---|\n| Acme search | 41 ms → 12 ms (3.4× faster) |\nThe engine — written in Rust — streams “tokens”.\nIt's &amp; also µs-level."
	yes := []string{
		"Acme search | 41 ms -> 12 ms (3.4x faster)",
		"acme search 41 ms → 12 ms",
		"The engine - written in Rust - streams \"tokens\"",
		"Acme search ... (3.4× faster)",
		"Acme search … streams “tokens”",
		"It's & also us-level",
	}
	no := []string{
		"",
		"Rust",                                 // too short to be evidence
		"ms ... 41",                            // only short fragments
		"streams \"tokens\" ... Acme search",   // fragments out of order
		"Acme search | 41 ms -> 10 ms",         // a different number
		"The engine is written in Go and fast", // invented
	}
	for _, q := range yes {
		if !quoteInText(text, q) {
			t.Errorf("should match: %q", q)
		}
	}
	for _, q := range no {
		if quoteInText(text, q) {
			t.Errorf("should NOT match: %q", q)
		}
	}
	p := tmpFile(t, "a.json", `{"note":"speedup 3.4\u00d7 on the \"hot\" path"}`)
	if !quoteInFile(p, `speedup 3.4× on the "hot" path`) {
		t.Error("JSON-escaped quote not found")
	}
	if quoteInFile(filepath.Dir(p), "speedup 3.4x on the hot path") {
		t.Error("a directory cannot hold a quote")
	}
}

func FuzzQuoteSelf(f *testing.F) {
	f.Add("The quick brown fox jumps over the lazy dog")
	f.Add("p95 41 ms → 12 ms | **bold** `code`")
	f.Fuzz(func(t *testing.T, s string) {
		if strings.Contains(s, "...") || strings.Contains(s, "…") {
			return
		}
		n := normQuote(s)
		if len([]rune(n)) >= minQuoteFrag && !quoteInText(s, s) {
			t.Fatalf("a text must contain itself: %q (norm %q)", s, n)
		}
	})
}

// ---------------------------------------------------------------- atomic + CAS

func TestWriteAtomicLeavesNoTemp(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "f.json")
	for i := 0; i < 20; i++ {
		if err := writeAtomic(p, []byte(fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}
	ents, _ := os.ReadDir(d)
	if len(ents) != 1 {
		t.Fatalf("temp files left behind: %v", ents)
	}
	if b, _ := os.ReadFile(p); string(b) != "19" {
		t.Fatalf("got %q", b)
	}
	_ = os.Chmod(p, 0o600)
	_ = writeAtomic(p, []byte("x"))
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode not preserved: %v", st.Mode())
	}
}

func TestGuardCommitDetectsOutsideEdit(t *testing.T) {
	p := tmpFile(t, "bank.json", `{"v":1}`)
	_, g, _ := readGuarded(p)
	_ = os.WriteFile(p, []byte(`{"v":2,"editor":true}`), 0o644) // someone else edits
	if err := g.commit([]byte(`{"v":3}`)); !errors.Is(err, errConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "editor") {
		t.Fatal("the outside edit was overwritten")
	}
	// missing file: created once, a second blind create conflicts
	q := filepath.Join(t.TempDir(), "new.json")
	_, g1, _ := readGuarded(q)
	_, g2, _ := readGuarded(q)
	if err := g1.commit([]byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := g2.commit([]byte("2")); !errors.Is(err, errConflict) {
		t.Fatalf("second creator must conflict, got %v", err)
	}
}

func TestGuardedUpdateRetriesAfterOutsideEdit(t *testing.T) {
	p := tmpFile(t, "log.json", `{"a":1}`)
	attempt := 0
	err := guardedUpdate(p, 3, func(old []byte, exists bool) ([]byte, error) {
		attempt++
		var m map[string]int
		_ = json.Unmarshal(old, &m)
		if attempt == 1 { // an editor saves between our read and our write
			_ = os.WriteFile(p, []byte(`{"a":1,"editor":7}`), 0o644)
		}
		m["rkb"] = attempt
		return json.Marshal(m)
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]int
	b, _ := os.ReadFile(p)
	_ = json.Unmarshal(b, &m)
	if m["editor"] != 7 || m["rkb"] != 2 || attempt != 2 {
		t.Fatalf("lost an update: %s (attempts %d)", b, attempt)
	}
}

func TestGuardedUpdateGivesUp(t *testing.T) {
	p := tmpFile(t, "log.json", `{}`)
	i := 0
	err := guardedUpdate(p, 3, func(old []byte, exists bool) ([]byte, error) {
		i++
		_ = os.WriteFile(p, []byte(fmt.Sprintf(`{"edit":%d}`, i)), 0o644)
		return []byte(`{"rkb":1}`), nil
	})
	if err == nil || !strings.Contains(err.Error(), "gave up") {
		t.Fatalf("expected give-up, got %v", err)
	}
	if b, _ := os.ReadFile(p); strings.Contains(string(b), "rkb") {
		t.Fatal("wrote over a file that kept changing")
	}
}

// ---------------------------------------------------------------- verify log

func TestMergeLogKeepsUnknownFieldsAndDedupes(t *testing.T) {
	p := tmpFile(t, "Claims.verify.json", `{"clmA":[{"pass":1,"verdict":"CONFIRMED","method":"manual-read","by":"me","at":"t","legacy_note":"from the python tool"}]}`)
	cfg.VerifyLog = p
	e := LogEntry{Pass: 2, Kind: "figure", Verdict: "CONFIRMED", Method: "blind-rederive", By: "v1", At: "t1", Found: "41 ms", SentenceHash: "s1"}
	appendLog(VerifyLog{"clmA": {e}})
	e.At = "t2" // the same verdict ingested again later
	appendLog(VerifyLog{"clmA": {e}, "clmB": {e}})
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "legacy_note") {
		t.Fatal("unknown field dropped")
	}
	var l VerifyLog
	_ = json.Unmarshal(b, &l)
	if len(l["clmA"]) != 2 || len(l["clmB"]) != 1 {
		t.Fatalf("dedupe wrong: %d %d", len(l["clmA"]), len(l["clmB"]))
	}
	if strings.Contains(string(b), `\u003c`) || strings.Contains(string(b), `\u0026`) {
		t.Error("HTML-escaped output")
	}
	if _, _, err := mergeLog([]byte(`{"x":[`), true, VerifyLog{"a": {e}}); err == nil {
		t.Error("corrupt log must be refused")
	}
	if _, _, err := mergeLog([]byte(`{"x":[],"x":[]}`), true, VerifyLog{"a": {e}}); err == nil {
		t.Error("duplicate-key log must be refused")
	}
}

// Many goroutines and many processes append at once: nothing may be lost.
func TestStressAppendLogConcurrent(t *testing.T) {
	p := tmpFile(t, "Claims.verify.json", `{}`)
	cfg.VerifyLog = p
	const G = 24
	var wg sync.WaitGroup
	for g := 0; g < G; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				appendLog(VerifyLog{fmt.Sprintf("g%d-%d", g, i): {LogEntry{Pass: 2, Verdict: "CONFIRMED", By: fmt.Sprint(g), Found: fmt.Sprint(i)}}})
			}
		}(g)
	}
	const P = 8
	exe, _ := os.Executable()
	var procs []*exec.Cmd
	for i := 0; i < P; i++ {
		c := exec.Command(exe, "-test.run=^$")
		c.Env = append(os.Environ(), "RKB_TEST_HELPER=appendlog", "RKB_TEST_N="+fmt.Sprint(i), "RKB_TEST_ROOT="+testRoot, "RKB_TEST_LOG="+p)
		c.Stderr = os.Stderr
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		procs = append(procs, c)
	}
	wg.Wait()
	for _, c := range procs {
		if err := c.Wait(); err != nil {
			t.Fatalf("helper failed: %v", err)
		}
	}
	var l VerifyLog
	b, _ := os.ReadFile(p)
	if err := json.Unmarshal(b, &l); err != nil {
		t.Fatalf("log corrupted: %v", err)
	}
	if len(l) != G*5+P*5 {
		t.Fatalf("lost updates: %d keys, want %d", len(l), G*5+P*5)
	}
}

// ---------------------------------------------------------------- bank

const miniBank = `{
  "headers": {"default": {"name": "Test Person", "contact": ["a@b.c"]}},
  "entries": {"e1": {"title": "Project", "bullets": ["b0","b1"]}},
  "bullets": {
    "b0": {"text": "Built a thing that cut latency by {{clmA}}", "author": "drafter", "hand_note": "keep"},
    "b1": {"text": "Shipped another thing", "author": "drafter"},
    "b2": {"text": "x", "author": "drafter"}, "b3": {"text": "y", "author": "drafter"}, "b4": {"text": "z", "author": "drafter"}
  },
  "lines": {}
}`

func TestStressReviewsConcurrentProcesses(t *testing.T) {
	bp := tmpFile(t, "bank.json", miniBank)
	const P = 10
	exe, _ := os.Executable()
	var procs []*exec.Cmd
	for i := 0; i < P; i++ {
		c := exec.Command(exe, "-test.run=^$")
		c.Env = append(os.Environ(), "RKB_TEST_HELPER=review", "RKB_TEST_N="+fmt.Sprint(i), "RKB_TEST_ROOT="+testRoot, "RKB_TEST_BANK="+bp)
		c.Stderr = os.Stderr
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		procs = append(procs, c)
	}
	for _, c := range procs {
		if err := c.Wait(); err != nil {
			t.Fatalf("helper: %v", err)
		}
	}
	b := loadBank(bp)
	for i := 0; i < 5; i++ {
		if n := len(b.Bullets[fmt.Sprintf("b%d", i)].Review["hr"]); n != P {
			t.Fatalf("b%d has %d reviews, want %d (lost update)", i, n, P)
		}
	}
	raw, _ := os.ReadFile(bp)
	if !strings.Contains(string(raw), "hand_note") {
		t.Fatal("unknown bullet field dropped")
	}
}

func TestBankSaveRefusesToDropFields(t *testing.T) {
	bp := tmpFile(t, "bank.json", miniBank)
	err := updateBank(bp, func(b *Bank) (bool, error) {
		delete(b.Bullets, "b4") // would drop a bullet silently
		return true, nil
	})
	if err == nil || !strings.Contains(err.Error(), "drop") {
		t.Fatalf("expected refusal, got %v", err)
	}
	err = updateBank(bp, func(b *Bank) (bool, error) {
		delete(b.Bullets, "b4")
		b.allowDrop = append(b.allowDrop, "$.bullets.b4")
		return true, nil
	})
	if err != nil {
		t.Fatalf("an explicit drop must be allowed: %v", err)
	}
}

func TestParseBankRejectsBadShapes(t *testing.T) {
	for _, s := range []string{
		`{"bullets":{"a":null}}`,
		`{"bullets":{"a":{"text":"1"},"a":{"text":"2"}}}`,
		`{"entries":{"e":null}}`,
		`{"bullets":`,
	} {
		if _, err := parseBank("b.json", []byte(s)); err == nil {
			t.Errorf("accepted %s", s)
		}
	}
}

// ---------------------------------------------------------------- reviews

func sheetOf(reviewer string, round int, items ...SheetItem) *Sheet {
	return &Sheet{Reviewer: reviewer, Round: round, Items: items}
}

func item(id, hash, hr, hm string) SheetItem {
	return SheetItem{ID: id, TextHash: hash, HR: map[string]string{"verdict": hr, "note": "n"}, HM: map[string]string{"verdict": hm, "note": "n"}}
}

func TestPlanReview(t *testing.T) {
	cur := map[string]string{"b0": "h0", "b1": "h1"}
	auth := map[string]string{"b0": "drafter", "b1": "drafter"}
	rp, err := planReview(sheetOf("hr-1", 1, item("b0", "h0", "PASS ", ""), item("b1", "old", "pass", "")), cur, auth)
	if err != nil {
		t.Fatal(err)
	}
	if len(rp.items) != 1 || rp.items[0].review.Verdict != "pass" || len(rp.stale) != 1 || rp.persona != "hr" {
		t.Fatalf("plan wrong: %+v", rp)
	}
	bad := []*Sheet{
		sheetOf("", 1, item("b0", "h0", "pass", "")),                                                     // no reviewer
		sheetOf("x", 3, item("b0", "h0", "pass", "")),                                                    // bad round
		sheetOf("x", 1, item("b0", "h0", "looks good", "")),                                              // bad verdict
		sheetOf("x", 1, item("b0", "h0", "pass", "pass")),                                                // both personas
		sheetOf("x", 1, item("b0", "h0", "pass", ""), item("b1", "h1", "", "pass")),                      // persona switch
		sheetOf("drafter", 1, item("b0", "h0", "pass", "")),                                              // author reviewing
		sheetOf("x", 1, item("b0", "h0", "pass", ""), item("b0", "h0", "pass", "")),                      // duplicate item
		sheetOf("x", 1, SheetItem{ID: "b0", TextHash: "h0", HR: map[string]string{"verdict": "revise"}}), // revise, no note
	}
	for i, sh := range bad {
		if _, err := planReview(sh, cur, auth); err == nil {
			t.Errorf("bad sheet %d accepted", i)
		}
	}
}

func TestApplyReviewKeepsHistory(t *testing.T) {
	b, _ := parseBank("b", []byte(miniBank))
	r := func(round int, v, by, h string) reviewItem {
		return reviewItem{"b0", Review{Round: round, Verdict: v, By: by, TextHash: h}}
	}
	_, _ = applyReview(b, "hr", []reviewItem{r(1, "pass", "a", "h")})
	_, _ = applyReview(b, "hr", []reviewItem{r(2, "revise", "b", "h")})
	_, _ = applyReview(b, "hr", []reviewItem{r(2, "pass", "b", "h")}) // same sheet re-ingested: replaces
	h := b.Bullets["b0"].Review["hr"]
	if len(h) != 2 || h[0].Verdict != "pass" || h[1].Verdict != "pass" {
		t.Fatalf("history wrong: %+v", h)
	}
}

// ---------------------------------------------------------------- claims

func TestParseClaimsFiles(t *testing.T) {
	d := t.TempDir()
	a := filepath.Join(d, "a.tex")
	b := filepath.Join(d, "b.tex")
	_ = os.WriteFile(a, []byte("% src one  [x.one]\n\\newcommand{\\clmOne}{41 ms}\n% CAUTION 2026: hand run; re-record before reuse.\n% src two [x.two]\n\\newcommand{\\clmTwo}{2.0$\\times$}\n\\newcommand{\\clmNoCheck}{7}\n\\newcommand{\\clm3x}{1}\n"), 0o644)
	_ = os.WriteFile(b, []byte("% again [x.one]\n\\newcommand{\\clmOne}{42 ms}\n"), 0o644)
	cl, errs, warns := parseClaimsFiles([]string{a, b, filepath.Join(d, "missing.tex")}, func(s string) string { return s })
	all := strings.Join(errs, "\n")
	for _, want := range []string{"already defined", "missing.tex", "cannot parse the definition of \\clm3x"} {
		if !strings.Contains(all, want) {
			t.Errorf("errors lack %q:\n%s", want, all)
		}
	}
	if cl["clmOne"].Value != "41 ms" {
		t.Errorf("first definition must stand, got %q", cl["clmOne"].Value)
	}
	if cl["clmTwo"].Caution == "" || cl["clmOne"].Caution != "" {
		t.Errorf("caution detection wrong: %q / %q", cl["clmTwo"].Caution, cl["clmOne"].Caution)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "clmNoCheck") {
		t.Errorf("warns: %v", warns)
	}
}

func TestLintCautionGate(t *testing.T) {
	b, _ := parseBank("b", []byte(miniBank))
	c := &Composed{Header: Header{Name: "T"}, Sections: []*Section{{Title: "Projects", Kind: "entries",
		Entries: []*Entry{{ID: "e1", Title: "P", Bullets: []*Bullet{b.Bullets["b0"]}}}}}}
	cf := &ClaimsFile{Claims: map[string]*ClaimView{"clmA": {Value: "40%", Kind: "percent", Caution: "CAUTION: hand run"}}, Statements: map[string]*StatementView{}}
	has := func(xs []string) bool {
		for _, x := range xs {
			if strings.Contains(x, "flagged in Claims.tex") {
				return true
			}
		}
		return false
	}
	if R := lint(c, cf, false, ""); !has(R.Warns) || has(R.Errors) {
		t.Errorf("draft: caution should warn")
	}
	if R := lint(c, cf, true, ""); !has(R.Errors) {
		t.Errorf("final: caution must be an error")
	}
	b.Bullets["b0"].CautionOK = map[string]string{"clmA": "wording now says 'hand-run'"}
	if R := lint(c, cf, true, ""); has(R.Errors) {
		t.Errorf("caution_ok must clear it")
	}
}

func TestLintComparisonGate(t *testing.T) {
	for _, tc := range []struct {
		plain string
		want  bool
	}{
		{"Raised accuracy from 31.0% to 52.4% on 240 questions", true},
		{"Ran 2.10–2.85× faster than the stock library on one laptop", true},
		{"Answered at 91.3% precision vs 68.0% closed-book", true},
		{"Cut cold-start time 27% across four services", true},
		{"Raised quiz scores 6.0–9.5 percentage points by turning on spaced review", true},
		{"reaching 64.0% on handwritten letters, 7.5 points above a lookup table", true},
		{"Trained a 60M-parameter model at 9.1k tok/s and 21.0% MFU", false},
		{"Indexed 9 file formats with one parser", false},
		{"Reached 91.3% precision on public questions", false},
	} {
		if got := hasComparison(tc.plain); got != tc.want {
			t.Errorf("hasComparison(%q) = %v, want %v", tc.plain, got, tc.want)
		}
	}
	cf := &ClaimsFile{Claims: map[string]*ClaimView{
		"clmP": {Value: "91.3%", Kind: "percent"},
		"clmL": {Value: "9 file formats", Kind: "count"},
	}, Statements: map[string]*StatementView{}}
	bare := &Bullet{ID: "bare", Need: "n", Text: "**Delivered a support-ticket assistant at {{clmP}} precision:** searched help articles, resolved full text and abstained when unsure."}
	other := &Bullet{ID: "other", Need: "n", Text: "**Built a log indexer across {{clmL}}:** parsed each file once and resolved references across modules and services."}
	c := &Composed{Header: Header{Name: "T"}, Sections: []*Section{{Title: "Projects", Kind: "entries",
		Entries: []*Entry{{ID: "e1", Title: "P", Bullets: []*Bullet{bare, other}}}}}}
	has := func(xs []string, s string) bool {
		for _, x := range xs {
			if strings.Contains(x, s) {
				return true
			}
		}
		return false
	}
	if R := lint(c, cf, false, ""); !has(R.Warns, "say what the result beats") || has(R.Errors, "say what the result beats") {
		t.Errorf("draft: a missing comparison share must warn, not fail")
	}
	if R := lint(c, cf, true, ""); !has(R.Errors, "say what the result beats") {
		t.Errorf("final: a missing comparison share must be an error")
	}
	bare.ComparisonOK = "first system of its kind; nothing to compare against"
	other.Text = "**Built a log indexer across {{clmL}}, searching faster than two older indexers:** parsed each file once and resolved references across modules."
	if R := lint(c, cf, true, ""); has(R.Errors, "say what the result beats") {
		t.Errorf("comparison_ok and a named comparator must clear the share gate")
	}
}

func TestLintSummaryNeedsComparedResult(t *testing.T) {
	cf := &ClaimsFile{Claims: map[string]*ClaimView{
		"clmP": {Value: "91.3%", Kind: "percent"},
		"clmX": {Value: "2.10–2.85×", Kind: "mult"},
	}, Statements: map[string]*StatementView{}}
	ident := &Bullet{ID: "ident", Need: "n", Text: "**Frontend engineer who designs the booking flows small shops rely on,** and the payment pages behind them."}
	c := &Composed{Header: Header{Name: "T"}, Sections: []*Section{{Title: "Summary", Kind: "bullets", Bullets: []*Bullet{ident}}}}
	has := func(xs []string, s string) bool {
		for _, x := range xs {
			if strings.Contains(x, s) {
				return true
			}
		}
		return false
	}
	const gate = "none carries a {{clm}} result with a comparison"
	if R := lint(c, cf, false, ""); !has(R.Warns, gate) || has(R.Errors, gate) {
		t.Errorf("draft: an identity-only Summary must warn, not fail")
	}
	if R := lint(c, cf, true, ""); !has(R.Errors, gate) {
		t.Errorf("final: an identity-only Summary must be an error")
	}
	// A figure with no comparator is still not enough.
	ident.Text = "**Built a support-ticket assistant answering at {{clmP}} precision,** searching help articles and reading full text."
	if R := lint(c, cf, true, ""); !has(R.Errors, gate) {
		t.Errorf("final: a Summary figure without a comparator must still fail")
	}
	ident.Text = "**Built an image engine running {{clmX}} faster than the stock library,** built on plain integer math."
	if R := lint(c, cf, true, ""); has(R.Errors, gate) {
		t.Errorf("a Summary figure with a named comparator must clear the gate")
	}
}

// ---------------------------------------------------------------- flags

func TestParseArgs(t *testing.T) {
	ok := []struct {
		in   []string
		pos  string
		opts string
		b    string
	}{
		{[]string{"r.json", "--claims", "c.json", "--final"}, "r.json", "claims=c.json", "final"},
		{[]string{"--claims=c.json", "r.json", "--final=false"}, "r.json", "claims=c.json", ""},
		{[]string{"--found", "-0.227", "x"}, "x", "found=-0.227", ""},
		{[]string{"--", "--weird-file"}, "--weird-file", "", ""},
	}
	for _, c := range ok {
		a, err := parseArgsErr(c.in, "final")
		if err != nil {
			t.Errorf("%v: %v", c.in, err)
			continue
		}
		var o, b []string
		for k, v := range a.opts {
			o = append(o, k+"="+v)
		}
		for k := range a.bools {
			b = append(b, k)
		}
		if strings.Join(a.pos, " ") != c.pos || strings.Join(o, ",") != c.opts || strings.Join(b, ",") != c.b {
			t.Errorf("%v: pos=%v opts=%v bools=%v", c.in, a.pos, o, b)
		}
	}
	for _, in := range [][]string{
		{"--claims"}, {"--claims", "--final"}, {"--out", "a", "--out", "b"}, {"-final"}, {"--final=maybe"}, {"--=x"},
	} {
		if _, err := parseArgsErr(in, "final"); err == nil {
			t.Errorf("accepted %v", in)
		}
	}
	a := &args{seen: []string{"finl"}}
	if err := a.check("rkb lint", flagTable["lint"]); err == nil || !strings.Contains(err.Error(), "did you mean --final") {
		t.Errorf("typo not caught: %v", err)
	}
}

func TestFlagTablesConsistent(t *testing.T) {
	for k := range flagTable {
		if _, ok := posTable[k]; !ok {
			t.Errorf("%s has flags but no positional rule", k)
		}
		if u := usageLine(k); !strings.HasPrefix(u, "rkb "+k) || strings.Contains(u, "see rkb help") {
			t.Errorf("%s has no usage line", k)
		}
	}
	for k := range posTable {
		if _, ok := flagTable[k]; !ok {
			t.Errorf("%s has a positional rule but no flag table", k)
		}
	}
	// every flag a command's code reads must be accepted by its table
	src := map[string][]string{}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		src[f] = flagNames(string(b))
	}
	accepted := map[string]bool{"code-root": true}
	for _, fl := range flagTable {
		for _, f := range fl {
			accepted[f] = true
		}
	}
	for f, names := range src {
		for _, n := range names {
			if !accepted[n] {
				t.Errorf("%s reads --%s but no command accepts it", f, n)
			}
		}
	}
}

func flagNames(s string) []string {
	var out []string
	for _, pre := range []string{`a.get("`, `a.bools["`, `a.opts["`, `a.intFlag("`, `a.floatFlag("`} {
		for _, part := range strings.Split(s, pre)[1:] {
			if i := strings.IndexAny(part, `"`); i > 0 {
				out = append(out, part[:i])
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- worksheet split

func TestSplitWorksheet(t *testing.T) {
	ws := Worksheet{Instructions: "i"}
	for i := 0; i < 30; i++ {
		repo := []string{"projects/alpha/a.md", "projects/beta/b.md", "research/gamma/c.md", "https://arxiv.org/x"}[i%4]
		ws.Items = append(ws.Items, &WorkItem{Kind: "statement", Key: fmt.Sprint(i), Sources: []string{repo}})
	}
	parts := splitWorksheet(ws, 3)
	if len(parts) != 3 {
		t.Fatalf("%d parts", len(parts))
	}
	seen, where := 0, map[string]int{}
	for pi, p := range parts {
		if p.Instructions != "i" {
			t.Error("instructions lost")
		}
		for _, it := range p.Items {
			seen++
			g := workGroup(it)
			if w, ok := where[g]; ok && w != pi {
				t.Errorf("group %s split across parts", g)
			}
			where[g] = pi
		}
	}
	if seen != 30 {
		t.Fatalf("items lost: %d", seen)
	}
	if len(splitWorksheet(Worksheet{}, 4)) != 1 {
		t.Error("empty worksheet should give one part")
	}
}

// ---------------------------------------------------------------- validator run

func TestVMAbnormal(t *testing.T) {
	good := "====\n[PRIMARY] a.b  ok\n[FAIL   ] c.d  bad\n----\nRESULT: PROBLEMS FOUND\n"
	// An ExitError with no ProcessState reports exit code -1: abnormal, not a clean run.
	if w := vmAbnormal(good, &exec.ExitError{}); w == "" {
		t.Error("an exit with no process state (code -1) must be abnormal")
	}
	if w := vmAbnormal(good, nil); w != "" {
		t.Errorf("healthy run flagged: %s", w)
	}
	for _, s := range []string{"", "panic: runtime error\n[PRIMARY] a ok\nRESULT: clean", "====\n[PRIMARY] a.b ok\n", "CV METRIC VALIDATION\nRESULT: clean"} {
		if vmAbnormal(s, nil) == "" {
			t.Errorf("broken run accepted: %q", s)
		}
	}
	p := tmpFile(t, "vm.txt", "# rkb-vm-report generated=2026-01-02T03:04:05Z\n[PRIMARY] a ok\n# --- merged partial run (x) ---\n# rkb-vm-report generated=2026-05-01T00:00:00Z\n[PRIMARY] b ok\n")
	_, when := parseVM(p)
	if when.Format(time.RFC3339) != "2026-01-02T03:04:05Z" {
		t.Errorf("report age must be the oldest stamp, got %v", when)
	}
}

func TestSelectionUnknownKeys(t *testing.T) {
	b := `{"bank":"b.json","meta":{"target":"x","pagez":2},"sections":[{"title":"S","bulets":["a"],"entries":[{"entry":"e","titel":"t"}]}],"_about":"ok"}`
	got := strings.Join(selectionUnknownKeys([]byte(b)), ",")
	for _, w := range []string{"$.meta.pagez", "$.sections[0].bulets", "$.sections[0].entries[0].titel"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %s in %s", w, got)
		}
	}
	if strings.Contains(got, "_about") {
		t.Error("underscore keys are comments")
	}
}

func TestEditDistance(t *testing.T) {
	if editDistance("finl", "final") != 1 || editDistance("", "abc") != 3 || editDistance("same", "same") != 0 {
		t.Error("editDistance")
	}
}

func FuzzParseArgs(f *testing.F) {
	f.Add("r.json --claims c.json --final")
	f.Add("--x=1 -- --y")
	f.Add("--final=yes --out")
	f.Fuzz(func(t *testing.T, s string) {
		a, err := parseArgsErr(strings.Fields(s), "final", "strict")
		if err != nil {
			return
		}
		_ = a.check("rkb lint", flagTable["lint"])
		for k := range a.bools {
			if _, dup := a.opts[k]; dup {
				t.Fatalf("%q is both a bool and a value", k)
			}
		}
	})
}

func FuzzMergeLog(f *testing.F) {
	f.Add(`{"clmA":[{"pass":1,"verdict":"CONFIRMED","x":1}]}`)
	f.Add(`{}`)
	f.Add(`{"a":[]}`)
	f.Fuzz(func(t *testing.T, old string) {
		cfg.VerifyLog = "/nonexistent/log.json"
		e := LogEntry{Pass: 2, Verdict: "CONFIRMED", By: "v"}
		nb, _, err := mergeLog([]byte(old), true, VerifyLog{"clmNew": {e}})
		if err != nil || nb == nil {
			return
		}
		var before, after map[string][]jsontext.Value
		if json.Unmarshal([]byte(old), &before) != nil && strings.TrimSpace(old) != "" {
			t.Fatalf("merge accepted an unparsable log %q", old)
		}
		if err := json.Unmarshal(nb, &after); err != nil {
			t.Fatalf("merge produced invalid JSON: %v", err)
		}
		for k, v := range before {
			if len(after[k]) < len(v) {
				t.Fatalf("merge lost entries of %s", k)
			}
		}
		if len(after["clmNew"]) < 1 {
			t.Fatal("new entry missing")
		}
	})
}

func TestMergeLogOddButValid(t *testing.T) {
	e := LogEntry{Pass: 2, Verdict: "CONFIRMED", By: "v"}
	if _, _, err := mergeLog([]byte("null"), true, VerifyLog{"k": {e}}); err == nil {
		t.Error("a null log must be refused")
	}
	for _, old := range []string{"null", `{"a":null}`, `{"a":[null]}`, `[]`, `"x"`, `{"a":{}}`} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: panic %v", old, r)
				}
			}()
			_, _, _ = mergeLog([]byte(old), true, VerifyLog{"k": {e}})
		}()
	}
}

// A bank that parses must round-trip: encode -> parse -> encode is stable and
// loses no field (unknown ones included).
func FuzzBankRoundTrip(f *testing.F) {
	f.Add(miniBank)
	f.Add(`{"bullets":{"a":{"text":"x","zz":[1,{"q":null}],"review":{"hr":[{"round":1,"verdict":"pass","text_hash":"h","by":"r","extra":true}]}}},"_top":1}`)
	f.Add(`{"entries":{"e":{"title":"T & <b>","weird":"x"}}}`)
	f.Fuzz(func(t *testing.T, s string) {
		b, err := parseBank("b.json", []byte(s))
		if err != nil {
			return
		}
		out, err := encodeBank(b, []byte(s))
		if err != nil {
			t.Fatalf("parsed bank does not re-encode: %v\n%s", err, s)
		}
		b2, err := parseBank("b.json", out)
		if err != nil {
			t.Fatalf("re-encoded bank does not parse: %v\n%s", err, out)
		}
		out2, err := encodeBank(b2, out)
		if err != nil || string(out2) != string(out) {
			t.Fatalf("round trip unstable:\n%s\n---\n%s", out, out2)
		}
	})
}

// An editor that rewrites the file in place (truncate, then write slowly)
// while rkb updates it: rkb must never write a broken file, and once the
// editor stops every rkb update must have landed.
func TestStressNaiveEditorInPlace(t *testing.T) {
	p := tmpFile(t, "Claims.verify.json", `{}`)
	cfg.VerifyLog = p
	stop := make(chan struct{})
	var ed sync.WaitGroup
	ed.Add(1)
	go func() {
		defer ed.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			b, _ := os.ReadFile(p)
			f, err := os.OpenFile(p, os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				continue
			}
			half := len(b) / 2
			// The torn write is the point; a failed write or close means the
			// race this test needs did not happen. Errorf, not Fatal: this is
			// not the test goroutine.
			if _, err := f.Write(b[:half]); err != nil {
				t.Errorf("editor write: %v", err)
				_ = f.Close()
				return
			}
			time.Sleep(2 * time.Millisecond)
			if _, err := f.Write(b[half:]); err != nil {
				t.Errorf("editor write: %v", err)
				_ = f.Close()
				return
			}
			if err := f.Close(); err != nil {
				t.Errorf("editor close: %v", err)
				return
			}
			time.Sleep(3 * time.Millisecond)
		}
	}()
	var wg sync.WaitGroup
	errs := make(chan string, 100)
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 4; i++ {
				err := guardedUpdate(p, 12, func(old []byte, exists bool) ([]byte, error) {
					nb, _, err := mergeLog(old, exists, VerifyLog{fmt.Sprintf("g%d-%d", g, i): {LogEntry{Pass: 2, Verdict: "CONFIRMED", By: "x"}}})
					return nb, err
				})
				if err != nil {
					errs <- err.Error()
				}
			}
		}(g)
	}
	wg.Wait()
	close(stop)
	ed.Wait()
	close(errs)
	failed := 0
	for e := range errs {
		failed++
		if !strings.Contains(e, "gave up") && !strings.Contains(e, "unreadable") {
			t.Errorf("unexpected error: %s", e)
		}
	}
	b, _ := os.ReadFile(p)
	var l VerifyLog
	if err := json.Unmarshal(b, &l); err != nil {
		t.Fatalf("file left broken: %v", err)
	}
	// the naive editor can clobber rkb's writes (it does no compare-and-swap);
	// what rkb guarantees is a parseable file and an error, never a silent loss
	// it caused itself
	t.Logf("%d keys present, %d updates reported failure, editor raced throughout", len(l), failed)
}

func TestLockExclusiveAndTimesOut(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bank.json.lock")
	rel, err := lockFile(p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockFile(p, 150*time.Millisecond); err == nil {
		t.Fatal("second holder got the lock")
	}
	rel()
	rel2, err := lockFile(p, time.Second)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	rel2()
	if _, err := os.Stat(p); err != nil {
		t.Fatal("the lock file must stay (deletes are not allowed on some mounts)")
	}
}

func TestHardenedLintGates(t *testing.T) {
	// 1. LaTeX < and > escaping prevents inverted punctuation in OT1 encoding
	gotTeX := mdToTeX("Engineered kernels (<2×10⁻³ error, >95% util)", true)
	if !strings.Contains(gotTeX, "$<$2") || !strings.Contains(gotTeX, "$>$95") {
		t.Fatalf("mdToTeX did not escape < and > into math mode: %s", gotTeX)
	}
	roundMD := texToMD(gotTeX, map[string]bool{})
	if !strings.Contains(roundMD, "<2×10⁻³") || !strings.Contains(roundMD, ">95%") {
		t.Fatalf("texToMD round-trip lost < or >: %s", roundMD)
	}

	// 2. Lint gates for dual-persona structure, third-person, stub colon, unit mismatch, elementary gloss, metric pileups, summary codenames, present-tense verbs
	cf := &ClaimsFile{
		Claims: map[string]*ClaimView{
			"clmA":      {Value: "10 → 5 ms (-50%)", Kind: "time"},
			"clmB":      {Value: "20 → 10 ms (-50%)", Kind: "time"},
			"clmC":      {Value: "30 → 15 ms (-50%)", Kind: "time"},
			"clmD":      {Value: "40 → 20 ms (-50%)", Kind: "time"},
			"clmE":      {Value: "50 → 25 ms (-50%)", Kind: "time"},
			"clmBytes":  {Value: "96 bytes to 1", Kind: "count"},
			"clmSchema": {Value: "schema v12", Kind: "count"},
			"clmCov":    {Value: "92% line coverage", Kind: "pct"},
			"clmTests":  {Value: "2,412 unit tests passing", Kind: "count"},
		},
		Statements: map[string]*StatementView{},
	}
	c := &Composed{
		Header: Header{Name: "Test"},
		Sections: []*Section{
			{
				Title: "Summary",
				Kind:  "bullets",
				Bullets: []*Bullet{
					{ID: "sum-bad", Text: "**Brings workflow tooling ({{clmA}}):** architected Orion, Vega, and Lyra to cut latency across all workloads.", Need: "n"},
				},
			},
			{
				Title: "Experience",
				Kind:  "entries",
				Entries: []*Entry{
					{
						ID:    "e1",
						Title: "Eng",
						Bullets: []*Bullet{
							{ID: "b-third", Text: "**Built his GPU runtime ({{clmA}})** that cut latency across all workloads in benchmark.", Need: "n"},
							{ID: "b-stub", Text: "**Raised quiz completion by {{clmA}} with a new reminder flow:** on 8 quizzes; separately cut page size.", AllowLiterals: []string{"8 quizzes"}, Need: "n"},
							{ID: "b-unit", Text: "**Audited the memory boundary and shrank per-call allocations from {{clmBytes}},** eliminating heap escapes across all worker threads.", Need: "n"},
							{ID: "b-elem", Text: "**Ran an order queue peaking at {{clmA}} (orders handled per second)** on a single node with batched writes.", Need: "n"},
							{ID: "b-pileup", Text: "**Merged reminders {{clmA}}, retries {{clmB}}, alerts {{clmC}}:** cut emails {{clmD}} and pages {{clmE}} across all teams.", Need: "n"},
							{ID: "b-pres", Text: "**Ship production model features ({{clmA}})** that cut latency across customer deployments in production.", Need: "n"},
							{ID: "b-nolead", Text: "Engineered a native runtime that cut latency {{clmA}} across all multi-core worker pools.", Need: "n"},
							{ID: "b-buried", Text: "**Optimized for large boards:** cut query latency {{clmA}} across all indexed search pages in production.", Need: "n"},
							{ID: "b-ljargon", Text: "**Accelerated the epoll loop for audio resampling {{clmA}}:** reused resampler buffers across all worker threads on Linux.", Need: "n"},
							{ID: "b-shorttail", Text: "**Published a spreadsheet formula engine running {{clmA}} faster than the stock parser:** 8 sheets.", AllowLiterals: []string{"8 sheets"}, Need: "n"},
							{ID: "b-schema", Text: "**Raised call resolution to {{clmSchema}} across two languages:** parsed Ruby modules and split the lookup into two stages.", Need: "n"},
							{ID: "b-coverage", Text: "**Held the gateway to {{clmCov}} under continuous integration:** enforced auth boundaries across the API edge and job scheduler.", Need: "n"},
							{ID: "b-tests", Text: "**Shipped the indexer after {{clmTests}} in CI:** rebuilt the search index and kept incremental refresh on the hot path.", Need: "n"},
						},
					},
				},
			},
		},
	}
	// The codename list and extra banned phrases are per user (bullet_rules);
	// this test sets its own and restores the loaded config afterwards.
	saved, savedBanned := cfg.Rules.Codenames, cfg.Rules.ExtraBanned
	defer func() { cfg.Rules.Codenames, cfg.Rules.ExtraBanned = saved, savedBanned }()
	cfg.Rules.Codenames = []string{"Orion", "Vega", "Lyra"}
	cfg.Rules.ExtraBanned = []string{`\borders handled per second\b`}
	R := lint(c, cf, false, "")
	allErrs := strings.Join(R.Errors, "\n")
	for _, want := range []string{
		"b-schema: [prose.no-schema-version]",
		"b-coverage: [prose.no-coverage-vanity]",
		"b-tests: [prose.no-test-count-filler]",
		"sum-bad: Summary bullet names internal project codenames",
		"b-third: third-person pronoun or reference",
		"b-stub: broken stub clause between colon and semicolon",
		"b-unit: ambiguous unit or count-vs-byte mismatch",
		"b-elem: filler or self-assessment phrase",
		"b-pileup: chains 5 before→after transitions",
		"b-pres: opens with present-tense verb",
		"b-nolead: missing bold outcome lead",
		"b-buried: bold lead (\"Optimized for large boards:\") buries the metric in the tail",
		"b-ljargon: bold lead contains low-level technical jargon",
		"b-shorttail: technical tail after bold lead has only 2 words (min 6)",
	} {
		if !strings.Contains(allErrs, want) {
			t.Errorf("expected lint error containing %q, got:\n%s", want, allErrs)
		}
	}
}

func TestDetexKeepsEscapedDollar(t *testing.T) {
	for in, want := range map[string]string{
		`\$0.14 per question`:                  "$0.14 per question",
		`\$1--\$3 vs 7.8 $\rightarrow$ 0.4 ms`: "$1–$3 vs 7.8 → 0.4 ms",
		`2.1$\times$ faster`:                   "2.1× faster",
	} {
		if got := detex(in); got != want {
			t.Errorf("detex(%q) = %q, want %q", in, got, want)
		}
	}
}

// The Summary codename check reads bullet_rules.codenames: a configured list
// flags a Summary bullet naming two of them, a single name passes, and an
// empty list (the embedded default) turns off only that check.
func TestSummaryCodenamesFromConfig(t *testing.T) {
	cf := &ClaimsFile{Claims: map[string]*ClaimView{"clmA": {Value: "10 → 5 ms (-50%)", Kind: "time"}}, Statements: map[string]*StatementView{}}
	page := func(text string) *Composed {
		return &Composed{Header: Header{Name: "T"}, Sections: []*Section{{Title: "Summary", Kind: "bullets",
			Bullets: []*Bullet{{ID: "sum", Need: "n", Text: text}}}}}
	}
	const rule = "Summary bullet names internal project codenames"
	two := "**Cut query latency {{clmA}} versus the old planner:** built Orion and Vega, a planner and a cache for analytics."
	one := "**Cut query latency {{clmA}} versus the old planner:** built Orion, a planner and cache for analytics teams."
	saved := cfg.Rules.Codenames
	defer func() { cfg.Rules.Codenames = saved }()

	cfg.Rules.Codenames = []string{"Orion", "Vega", "C++ Engine", " "}
	if R := lint(page(two), cf, false, ""); !guardHas(R.Errors, rule) {
		t.Errorf("two configured codenames in a Summary bullet must fail: %v", R.Errors)
	}
	if R := lint(page(one), cf, false, ""); guardHas(R.Errors, rule) {
		t.Errorf("one codename must pass: %v", R.Errors)
	}
	if R := lint(page("**Cut query latency {{clmA}} versus the old planner:** built orion and vega, a planner and cache."), cf, false, ""); guardHas(R.Errors, rule) {
		t.Errorf("codenames match as written (case-sensitive), as the compiled-in list did: %v", R.Errors)
	}
	for _, empty := range [][]string{nil, {}, {"", "  "}} {
		cfg.Rules.Codenames = empty
		if R := lint(page(two), cf, false, ""); guardHas(R.Errors, rule) {
			t.Errorf("codenames %q must turn the check off: %v", empty, R.Errors)
		}
	}
	if re := codenameRe(nil); re.MatchString("") || re.MatchString("Orion") {
		t.Errorf("an empty codename list must match nothing")
	}
	if re := codenameRe([]string{"C++ Engine"}); !re.MatchString("the C++ Engine team") || re.MatchString("CCC Engine") {
		t.Errorf("codenames must be quoted, not read as patterns")
	}
}

// sibling_roots decides which missing top-level folders are an unmounted
// sandbox mount (skipped) rather than a missing source (failed).
func TestSiblingRootsFromConfig(t *testing.T) {
	root := t.TempDir()
	savedBank, savedRoots := cfg.Bank, cfg.SiblingRoots
	defer func() { cfg.Bank, cfg.SiblingRoots = savedBank, savedRoots }()
	cfg.Bank = "career/bank.json"
	if err := os.MkdirAll(filepath.Join(root, "career"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, cfg.Bank), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.SiblingRoots = nil
	if isUnmountedSibling(root, "projects/alpha/README.md") {
		t.Errorf("with no sibling_roots, a missing source must not be skipped as unmounted")
	}
	cfg.SiblingRoots = []string{"projects", ""}
	if !isUnmountedSibling(root, "projects/alpha/README.md") || !isUnmountedSibling(root, "./projects/x") {
		t.Errorf("a missing listed sibling root must read as unmounted")
	}
	if isUnmountedSibling(root, "other/alpha/README.md") {
		t.Errorf("an unlisted top folder must not read as unmounted")
	}
	if err := os.MkdirAll(filepath.Join(root, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if isUnmountedSibling(root, "projects/alpha/README.md") {
		t.Errorf("a mounted sibling root must not read as unmounted")
	}
	if isUnmountedSibling("", "projects/alpha") || isUnmountedSibling(t.TempDir(), "projects/alpha") {
		t.Errorf("without a bank under root, nothing is a sandbox sibling")
	}
}
