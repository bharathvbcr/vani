package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bharathvbcr/Vani/internal/resumestore"
)

func choiceOf(t *testing.T, doc string) pageOf {
	t.Helper()
	p, err := readChoice(json.RawMessage(doc))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func ids(xs []bulletAt) []string {
	out := []string{}
	for _, x := range xs {
		out = append(out, x.ID)
	}
	return out
}

func TestDiffChoicesNamesAddedRemovedAndMovedBullets(t *testing.T) {
	a := choiceOf(t, `{"sections":[
		{"title":"Summary","bullets":["s1","s2"]},
		{"title":"Experience","entries":[{"entry":"job","bullets":["a","b","c","d"]},{"entry":"other","bullets":["x"]}]},
		{"title":"Agents","entries":[{"entry":"p","bullets":["p1"]}]}],"jd_keywords":["go","rust"]}`)
	b := choiceOf(t, `{"sections":[
		{"title":"Summary","bullets":["s2","s3"]},
		{"title":"Experience","entries":[{"entry":"job","bullets":["a","c","d"]},{"entry":"other","bullets":["x","b"]}]},
		{"title":"Agents","entries":[{"entry":"p","bullets":["p1","a2"]}]}],"jd_keywords":["rust","python"]}`)
	bd, ov, so, kd := diffChoices(a, b)
	if got := ids(bd.Added); !reflect.DeepEqual(got, []string{"s3", "a2"}) {
		t.Errorf("added = %v", got)
	}
	if got := ids(bd.Removed); !reflect.DeepEqual(got, []string{"s1"}) {
		t.Errorf("removed = %v", got)
	}
	// b went from job to other (a different entry): moved. a, c, d keep their order in
	// job, so removing b does not make c and d "move"; s2 shifted only because s1 left.
	if len(bd.Moved) != 1 || bd.Moved[0].ID != "b" || bd.Moved[0].From.Entry != "job" || bd.Moved[0].To.Entry != "other" || bd.Moved[0].To.Index != 1 || bd.Moved[0].From.Section != "Experience" {
		t.Errorf("moved = %+v", bd.Moved)
	}
	if len(ov) != 0 || so != nil {
		t.Errorf("overrides %v order %v", ov, so)
	}
	if !reflect.DeepEqual(kd.Added, []string{"python"}) || !reflect.DeepEqual(kd.Removed, []string{"go"}) {
		t.Errorf("keywords = %+v", kd)
	}
}

func TestDiffChoicesSeesAReorderWithinAnEntryAsTheSmallestMove(t *testing.T) {
	a := choiceOf(t, `{"sections":[{"title":"E","entries":[{"entry":"j","bullets":["a","b","c","d","e"]}]}]}`)
	b := choiceOf(t, `{"sections":[{"title":"E","entries":[{"entry":"j","bullets":["a","c","d","e","b"]}]}]}`)
	bd, _, _, _ := diffChoices(a, b)
	if len(bd.Moved) != 1 || bd.Moved[0].ID != "b" || bd.Moved[0].From.Index != 1 || bd.Moved[0].To.Index != 4 {
		t.Fatalf("moved = %+v: one bullet jumped, the rest kept their order", bd.Moved)
	}
	// Two bullets swapped: one of them moved (a deterministic pick), never both.
	c := choiceOf(t, `{"sections":[{"title":"E","entries":[{"entry":"j","bullets":["b","a"]}]}]}`)
	d := choiceOf(t, `{"sections":[{"title":"E","entries":[{"entry":"j","bullets":["a","b"]}]}]}`)
	if bd, _, _, _ := diffChoices(c, d); len(bd.Moved) != 1 {
		t.Fatalf("swapped pair = %+v", bd.Moved)
	}
	// Identical pages differ in nothing.
	if bd, ov, so, kd := diffChoices(a, a); len(bd.Added)+len(bd.Removed)+len(bd.Moved)+len(ov)+len(kd.Added)+len(kd.Removed) != 0 || so != nil {
		t.Fatalf("a page against itself: %+v %v %v %v", bd, ov, so, kd)
	}
}

func TestDiffChoicesReportsOverridesPerFieldAndTheSectionOrder(t *testing.T) {
	a := choiceOf(t, `{"sections":[
		{"title":"Experience","entries":[{"entry":"job","bullets":["a"],"title":"Engineer","lead":"A lead."},{"entry":"j2","bullets":["z"],"dates":"2020"}]},
		{"title":"GPU","entries":[{"entry":"g","bullets":["g1"]}]},
		{"title":"Agents","entries":[{"entry":"p","bullets":["p1"]}]}]}`)
	b := choiceOf(t, `{"sections":[
		{"title":"Experience","entries":[{"entry":"job","bullets":["a"],"title":"Principal Engineer","org":"Acme"},{"entry":"j2","bullets":["z"],"dates":"2020"}]},
		{"title":"Agents","entries":[{"entry":"p","bullets":["p1"]}]},
		{"title":"GPU","entries":[{"entry":"g","bullets":["g1"]}]}]}`)
	_, ov, so, _ := diffChoices(a, b)
	want := []overrideChange{
		{Entry: "job", Field: "title", From: "Engineer", To: "Principal Engineer"},
		{Entry: "job", Field: "org", From: "", To: "Acme"},
		{Entry: "job", Field: "lead", From: "A lead.", To: ""},
	}
	if !reflect.DeepEqual(ov, want) {
		t.Errorf("overrides = %+v", ov)
	}
	if so == nil || !reflect.DeepEqual(so.From, []string{"Experience", "GPU", "Agents"}) || !reflect.DeepEqual(so.To, []string{"Experience", "Agents", "GPU"}) {
		t.Errorf("section order = %+v", so)
	}
	// A section only one side has is not an order change.
	c := choiceOf(t, `{"sections":[{"title":"Experience","entries":[]},{"title":"GPU","entries":[]}]}`)
	d := choiceOf(t, `{"sections":[{"title":"Experience","entries":[]},{"title":"Agents","entries":[]},{"title":"GPU","entries":[]}]}`)
	if _, _, so, _ := diffChoices(c, d); so != nil {
		t.Errorf("a section added moved no other: %+v", so)
	}
}

func TestReadChoiceToleratesADuplicateIdAndRefusesAShapeThatIsNotAChoice(t *testing.T) {
	p := choiceOf(t, `{"sections":[{"title":"E","entries":[{"entry":"j","bullets":["a","a"]}]}]}`)
	if len(p.bullets) != 1 {
		t.Errorf("bullets = %+v", p.bullets)
	}
	for _, bad := range []string{`[]`, `"x"`, `{"sections":{}}`, `{"sections":[{"title":5}]}`} {
		if _, err := readChoice(json.RawMessage(bad)); err == nil {
			t.Errorf("%s was read as a choice", bad)
		}
	}
	if p, err := readChoice(json.RawMessage(`{}`)); err != nil || len(p.bullets) != 0 {
		t.Errorf("an empty choice: %+v, %v", p, err)
	}
}

// ---- the commands

func jrDiff(t *testing.T, want int, args ...string) (out string, d resumeDiffed) {
	t.Helper()
	out, _ = jrRun(t, want, "", append([]string{"diff", "--json"}, args...)...)
	if want == 0 {
		if err := json.Unmarshal([]byte(out), &d); err != nil {
			t.Fatalf("not a diff: %q (%v)", out, err)
		}
	}
	return out, d
}

func TestJobsResumeDiffComparesTwoStoredVersions(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	p, prov := jrTailor()
	jrSetup(t, f, prov)
	jrBuild(t) // v1: b2, b1
	edit := `{"sections":[{"title":"Experience","entries":[{"entry":"alpha-job","bullets":["b1"],"title":"Principal Engineer"}]}],"jd_keywords":["research","tools","Python","TypeScript","evaluation","tracing","latency","agents"]}`
	jrRun(t, 0, edit, "render", "--json", "--fit", jrKey) // v2: b1 and an override
	p.text = replyB1
	jrRun(t, 0, "", "build", "--json", "--from", "v1", "--instruction", "prefer tracing", jrKey) // v3, instruction

	out, d := jrDiff(t, 0, jrKey, "v1", "v2")
	if d.Key != jrKey || d.From.Version != 1 || d.To.Version != 2 || d.From.Source != "tailored" || d.To.Source != "edited" || d.To.Parent != 1 || d.Same {
		t.Fatalf("diff = %s", out)
	}
	if got := ids(d.Bullets.Removed); !reflect.DeepEqual(got, []string{"b2"}) || len(d.Bullets.Added) != 0 || len(d.Bullets.Moved) != 0 {
		t.Errorf("bullets = %+v", d.Bullets)
	}
	if len(d.Overrides) != 1 || d.Overrides[0] != (overrideChange{Entry: "alpha-job", Field: "title", From: "", To: "Principal Engineer"}) {
		t.Errorf("overrides = %+v", d.Overrides)
	}
	// Every list is on the wire even when empty (a reader never sees null).
	m := jrObject(t, out)
	for _, k := range []string{"bullets", "overrides", "keywords"} {
		if m[k] == nil {
			t.Errorf("%s is null", k)
		}
	}
	bm := m["bullets"].(map[string]any)
	for _, k := range []string{"added", "removed", "moved"} {
		if bm[k] == nil {
			t.Errorf("bullets.%s is null", k)
		}
	}

	// The side that was asked with an instruction names it; the other does not.
	_, d = jrDiff(t, 0, jrKey, "v2", "v3")
	if d.To.Instruction != "prefer tracing" || d.From.Instruction != "" || d.To.Parent != 1 {
		t.Errorf("sides = %+v / %+v", d.From, d.To)
	}
	// A version against itself is the same page.
	if _, d := jrDiff(t, 0, jrKey, "v1", "v1"); !d.Same || d.SectionOrder != nil {
		t.Errorf("v1 against v1 = %+v", d)
	}
	// Order matters: swapping the sides swaps added and removed.
	_, ab := jrDiff(t, 0, jrKey, "v1", "v2")
	_, ba := jrDiff(t, 0, jrKey, "v2", "v1")
	if !reflect.DeepEqual(ids(ab.Bullets.Removed), ids(ba.Bullets.Added)) {
		t.Errorf("a->b removed %v, b->a added %v", ids(ab.Bullets.Removed), ids(ba.Bullets.Added))
	}
}

func TestJobsResumeDiffRefusalsAreCoded(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, prov := jrTailor()
	_, st := jrSetup(t, f, prov)
	jrBuild(t)
	for name, tc := range map[string]struct {
		args []string
		code string
	}{
		"an unknown version":   {[]string{jrKey, "v1", "v9"}, "not_found"},
		"the first unknown":    {[]string{jrKey, "v9", "v1"}, "not_found"},
		"a bad version":        {[]string{jrKey, "v1", "two"}, "bad_request"},
		"a zero version":       {[]string{jrKey, "v0", "v1"}, "bad_request"},
		"an unknown posting":   {[]string{"gh:nobody:9", "v1", "v1"}, "unknown_key"},
		"a key that is no key": {[]string{"bad\x00key", "v1", "v1"}, "bad_request"},
	} {
		out, _ := jrRun(t, 1, "", append([]string{"diff", "--json"}, tc.args...)...)
		if r := jrFail(t, out); r.Code != tc.code {
			t.Errorf("%s: %+v, want %s", name, r, tc.code)
		}
	}
	// Usage errors exit 2.
	jrRun(t, 2, "", "diff", "--json", jrKey, "v1")
	jrRun(t, 2, "", "diff", "--json", jrKey, "v1", "v2", "v3")
	// A damaged version is damage, and the other side is not blamed.
	jrRun(t, 0, jrEditOf("alpha-job", "b1"), "render", "--json", "--fit", jrKey)
	p := filepath.Join(resumestore.Dir(os.Getenv("VANI_STATE_DIR")), jobFolderOf(jrKey), "v2.json")
	if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := jrRun(t, 1, "", "diff", "--json", jrKey, "v1", "v2")
	if r := jrFail(t, out); r.Code != "damaged" {
		t.Errorf("damaged = %+v", r)
	}
	if _, d := jrDiff(t, 0, jrKey, "v1", "v1"); !d.Same {
		t.Errorf("an undamaged version still diffs")
	}
	_ = st
}

// jobFolderOf is the store's folder name of a key (sha256 hex): the layout the
// store documents.
func jobFolderOf(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func TestJobsResumeCurrentRestoresAnOlderVersionWithoutDeletingAnything(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, prov := jrTailor()
	_, st := jrSetup(t, f, prov)
	jrBuild(t)
	jrRun(t, 0, jrEditOf("alpha-job", "b1"), "render", "--json", "--fit", jrKey) // v2 current
	if _, err := st.Use(t.Context(), jrKey, 2); err != nil {
		t.Fatal(err)
	}

	out, _ := jrRun(t, 0, "", "current", "--json", jrKey, "v1")
	var r resumeCurrentSet
	if err := json.Unmarshal([]byte(out), &r); err != nil || r != (resumeCurrentSet{Key: jrKey, Version: 1, Previous: 2, Changed: true}) {
		t.Fatalf("current = %s (%v)", out, err)
	}
	if cur, err := st.Current(jrKey); err != nil || cur.Version != 1 {
		t.Fatalf("current is %+v, %v", cur, err)
	}
	if vs, _ := st.List(jrKey); len(vs) != 2 {
		t.Errorf("a restore deleted a version: %d left", len(vs))
	}
	// What was applied with is `use`'s, and untouched.
	if used, err := st.Used(jrKey); err != nil || used.Version != 2 {
		t.Errorf("used = %+v, %v", used, err)
	}
	// Show agrees; asking again is a no-op that says so.
	if m := jrShow(t, "--no-check", jrKey); m["current"] != float64(1) {
		t.Errorf("show current = %v", m["current"])
	}
	out, _ = jrRun(t, 0, "", "current", "--json", jrKey, "v1")
	json.Unmarshal([]byte(out), &r)
	if r.Changed || r.Version != 1 || r.Previous != 1 {
		t.Errorf("again = %s", out)
	}
	// The next build is made from the restored version.
	if cur, _ := currentOf(st, "gh:acme:1"); cur != 1 {
		t.Errorf("currentOf = %d", cur)
	}
}

func TestJobsResumeCurrentRefusalsAndRepair(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, prov := jrTailor()
	state, st := jrSetup(t, f, prov)
	jrBuild(t)
	for name, tc := range map[string]struct {
		args []string
		code string
	}{
		"an unknown version": {[]string{jrKey, "v7"}, "not_found"},
		"a bad version":      {[]string{jrKey, "latest"}, "bad_request"},
		"an unknown posting": {[]string{"gh:nobody:9", "v1"}, "unknown_key"},
	} {
		out, _ := jrRun(t, 1, "", append([]string{"current", "--json"}, tc.args...)...)
		if r := jrFail(t, out); r.Code != tc.code {
			t.Errorf("%s: %+v", name, r)
		}
	}
	jrRun(t, 2, "", "current", "--json", jrKey)
	// A job with a posting but no versions at all.
	jrSeed(t, state, "gh:acme:2", "Acme", "Other Role", jrDesc)
	out, _ := jrRun(t, 1, "", "current", "--json", "gh:acme:2", "v1")
	if r := jrFail(t, out); r.Code != "not_found" {
		t.Errorf("no versions: %+v", r)
	}
	// A damaged pointer is replaced: the restore is also the repair.
	jrRun(t, 0, jrEditOf("alpha-job", "b1"), "render", "--json", "--fit", jrKey)
	if err := os.WriteFile(filepath.Join(resumestore.Dir(state), jobFolderOf(jrKey), "current"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ = jrRun(t, 0, "", "current", "--json", jrKey, "v1")
	var r resumeCurrentSet
	if err := json.Unmarshal([]byte(out), &r); err != nil || !r.Changed || r.Version != 1 || r.Previous != 0 {
		t.Fatalf("repair = %s (%v)", out, err)
	}
	if cur, err := st.Current(jrKey); err != nil || cur.Version != 1 {
		t.Errorf("current = %+v, %v", cur, err)
	}
}
