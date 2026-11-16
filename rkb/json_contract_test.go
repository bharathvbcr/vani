package main

// The bytes rkb writes are a contract: bank.json, the verify log and resume
// files live in git, are edited by hand and are read by other tools, so a
// change of JSON library must not reorder, re-escape or re-null anything.
// The golden files in testdata/json-contract were written by the
// encoding/json v1 build of rkb; the v2 build must reproduce them exactly.

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func contractGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "json-contract", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sameBytes(t *testing.T, name string, got []byte) {
	t.Helper()
	want := contractGolden(t, name)
	if bytes.Equal(got, want) {
		return
	}
	gl, wl := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
	for i := 0; i < len(gl) || i < len(wl); i++ {
		var g, w string
		if i < len(gl) {
			g = gl[i]
		}
		if i < len(wl) {
			w = wl[i]
		}
		if g != w {
			t.Fatalf("%s: line %d differs\n got: %q\nwant: %q", name, i+1, g, w)
		}
	}
	t.Fatalf("%s: bytes differ", name)
}

// A hand-edited bank: keys out of order, fields rkb does not model at every
// level (with escapes and number spellings of their own), prose with & < >,
// U+2028 and non-ASCII text, a header without contact lines. <LS> becomes a
// literal U+2028 inside a carried-over field.
const contractBankIn = `{
 "lines": {"skills": [{"text": "Go & Rust", "label": "", "note": "kept"}]},
 "bullets": {
  "b2": {"text": "Cut p99 <1 ms & kept \"hot\" paths", "sources": ["a.md"], "x_raw": "caf\u00e9 \/ \u0026", "x_num": 1.50,
         "review": {"hr": [{"round": 1, "verdict": "pass", "text_hash": "h", "by": "r", "x_seen": 1e3}]}},
  "b1": {"text": "Line\u2028break é", "caution_ok": {"clmB": "b", "clmA": "a"}, "need": "", "x_ls": "a<LS>b"}
 },
 "headers": {"default": {"name": "Sample Person", "x_alt": {"z": 1, "a": [true, null]}}},
 "entries": {"e1": {"title": "Shelfwise", "bullets": ["b1", "b2"], "members": [], "x_flag": false}},
 "_about": "invented",
 "x_top": [3, 2, 1]
}`

func TestJSONContractBankRoundTrip(t *testing.T) {
	in := []byte(strings.ReplaceAll(contractBankIn, "<LS>", "\u2028"))
	b, err := parseBank("bank.json", in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := encodeBank(b, in)
	if err != nil {
		t.Fatal(err)
	}
	sameBytes(t, "bank.golden.json", got)
	// rkb's own output is a fixed point: load-then-save changes nothing.
	b2, err := parseBank("bank.json", got)
	if err != nil {
		t.Fatal(err)
	}
	again, err := encodeBank(b2, got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, got) {
		t.Fatal("encoding rkb's own bank output changed it")
	}
}

// A key that matches a field only when case is ignored used to be read into
// the field and also carried along as an unknown key, so the next save wrote
// both. Now it stops the load and names the right spelling.
func TestMiscasedFieldIsAnError(t *testing.T) {
	for _, in := range []string{
		`{"bullets": {"b1": {"Text": "x"}}}`,
		`{"bullets": {"b1": {"text": "x", "ID": "b1"}}}`,
		`{"entries": {"e1": {"Title": "x"}}}`,
		`{"headers": {"default": {"Name": "x"}}}`,
		`{"lines": {"l": [{"TEXT": "x"}]}}`,
		`{"bullets": {"b1": {"text": "x", "review": {"hr": [{"Verdict": "pass"}]}}}}`,
		`{"Bullets": {}}`,
	} {
		_, err := parseBank("bank.json", []byte(in))
		if err == nil || !strings.Contains(err.Error(), "case-sensitive") {
			t.Errorf("%s: got %v, want a case-sensitive field error", in, err)
		}
	}
	// Exact names and unrelated unknown keys still load.
	if _, err := parseBank("bank.json", []byte(`{"bullets": {"b1": {"text": "x", "id": "b1", "texts": 1}}}`)); err != nil {
		t.Fatal(err)
	}
}

// Invalid UTF-8 used to be replaced with U+FFFD on read, so the next save
// rewrote the bytes. Now the file is refused before anything is written.
func TestInvalidUTF8IsRefused(t *testing.T) {
	in := []byte("{\"bullets\": {\"b1\": {\"text\": \"caf\xe9\"}}}")
	if _, err := parseBank("bank.json", in); err == nil {
		t.Fatal("a bank with invalid UTF-8 loaded")
	}
	if _, _, err := mergeLog(in, true, VerifyLog{"c": {{Pass: 1, Verdict: "CONFIRMED", By: "b", At: "t"}}}); err == nil {
		t.Fatal("a verify log with invalid UTF-8 was rewritten")
	}
}

func TestJSONContractComposed(t *testing.T) {
	yes := true
	c := &Composed{
		Composed:  true,
		Selection: "resumes/r.json",
		BankPath:  "bank.json",
		Meta:      Meta{Target: "Backend & data", BoldFigures: &yes, Style: map[string]string{"z": "1", "a": "2"}},
		Header:    Header{Name: "Sample Person"},
		Sections: []*Section{
			{Title: "Experience", Kind: "entries", Entries: []*Entry{{ID: "e1", Title: "Shelfwise", Bullets: []*Bullet{{ID: "b1", Text: "Sorted <3> queues & tags"}}}}},
			{Title: "Skills", Kind: "lines", Bulleted: true, Lines: []Line{{Text: "Go"}, {Label: "Tools", Text: "git"}}},
			{Title: "Empty", Kind: "bullets"},
		},
	}
	got, err := encodeJSON(c)
	if err != nil {
		t.Fatal(err)
	}
	sameBytes(t, "composed.golden.json", got)
}

func TestJSONContractSelection(t *testing.T) {
	in := `{"bank": "../bank.json", "meta": {"pages": 1}, "header": "default",
	        "sections": [{"title": "Work", "entries": ["e1", {"entry": "e2", "bullets": ["b1"], "title": "Lead & owner"}]},
	                     {"title": "Skills", "lines": "skills"},
	                     {"title": "Inline", "lines": [{"text": "Go"}]}]}`
	var sel Selection
	if err := decodeJSONStrict("r.json", []byte(in), &sel); err != nil {
		t.Fatal(err)
	}
	got, err := encodeJSON(&sel)
	if err != nil {
		t.Fatal(err)
	}
	sameBytes(t, "selection.golden.json", got)
}

func TestJSONContractVerifyLog(t *testing.T) {
	// An existing log written by an older tool: escaped & and <, an unknown
	// field, a number spelled 1.50, a line given as a number and as text.
	old := `{"clmB": [{"pass": 1, "verdict": "CONFIRMED", "method": "manual-read", "by": "me", "at": "t0",
	                    "found": "4 \u0026 5 \u003c 6", "line": 12, "legacy_note": "python", "score": 1.50}],
	         "clmA": [{"pass": 2, "verdict": "CONFIRMED", "method": "blind", "by": "v", "at": "t0", "line": "L3"}]}`
	add := VerifyLog{
		"clmC": {{Pass: 1, Kind: "figure", Verdict: "CONFIRMED", Method: "manual-read", By: "me", At: "t1",
			Found: "41 ms", Line: 7, Sources: []struct {
				Path string `json:"path"`
				SHA  string `json:"sha256"`
			}{{Path: "a.md", SHA: "s"}}}},
		"clmA": {{Pass: 2, Verdict: "CONFIRMED", Method: "blind", By: "w", At: "t1"}},
	}
	got, n, err := mergeLog([]byte(old), true, add)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("added %d entries, want 2", n)
	}
	sameBytes(t, "verifylog.golden.json", got)
}

// rkb writes & < > literally everywhere; new verify-log entries used to be the
// exception (escaped as & < >). Entries already in the log
// keep whatever escapes they were written with.
func TestNewLogEntriesAreNotHTMLEscaped(t *testing.T) {
	old := `{"clmA": [{"pass": 1, "verdict": "CONFIRMED", "method": "m", "by": "b", "at": "t0", "found": "4 \u0026 5"}]}`
	add := VerifyLog{"clmB": {{Pass: 1, Verdict: "CONFIRMED", Method: "m", By: "b", At: "t1", Evidence: "a & <b>"}}}
	got, _, err := mergeLog([]byte(old), true, add)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"evidence": "a & <b>"`) {
		t.Errorf("new entry was escaped:\n%s", got)
	}
	if !strings.Contains(string(got), `"found": "4 \u0026 5"`) {
		t.Errorf("existing entry lost its own escapes:\n%s", got)
	}
}

// Every encode in rkb goes through writeOpts (marshalNoEscape, encodeJSON).
// A bare json.Marshal under v2 writes maps in random order and [] for nil, so
// a summary line or a written file would change from run to run.
func TestNoMarshalWithoutWriteOpts(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "json" || !strings.HasPrefix(sel.Sel.Name, "Marshal") {
				return true
			}
			usesOpts := false
			for _, a := range call.Args[1:] {
				if id, ok := a.(*ast.Ident); ok && id.Name == "writeOpts" {
					usesOpts = true
				}
			}
			if !usesOpts {
				t.Errorf("%s: json.%s without writeOpts; use marshalNoEscape or encodeJSON", fset.Position(call.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
}

func TestJSONContractGenericValues(t *testing.T) {
	v := struct {
		Text     string             `json:"text"`
		NilSlice []string           `json:"nil_slice"`
		NilMap   map[string]int     `json:"nil_map"`
		Empty    []string           `json:"empty"`
		Sorted   map[string]int     `json:"sorted"`
		Floats   []float64          `json:"floats"`
		Nested   map[string][]Line  `json:"nested"`
		Any      []any              `json:"any"`
		Ptrs     map[string]*Bullet `json:"ptrs"`
	}{
		Text:   "a & b <c> \u2028 \u2029 é \"q\" \\ \t",
		Empty:  []string{},
		Sorted: map[string]int{"b": 2, "a": 1, "C": 3, "aa": 4},
		Floats: []float64{0.1, 1e21, 1e20, 1e-7, 0.000001, 100, 2.5, 1.0 / 3},
		Nested: map[string][]Line{"k": nil, "j": {{Text: "x"}}},
		Any:    []any{nil, 1, "s", true, map[string]any{"y": 1, "x": []any{}}},
		Ptrs:   map[string]*Bullet{"n": nil, "b": {ID: "b", Text: "t"}},
	}
	got, err := encodeJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	sameBytes(t, "generic.golden.json", got)
}
