package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/resumestore"
)

// jrEdit is a valid owner edit of the test bank: the model's own reply shape.
const jrEdit = resumeReply1

func jrEditOf(entry string, ids ...string) string {
	b, _ := json.Marshal(map[string]any{"sections": []any{map[string]any{"title": "Experience", "entries": []any{map[string]any{"entry": entry, "bullets": ids}}}}})
	return string(b)
}

// jrBankDoc rewrites the test bank (and the base selection's list for the one
// entry) so tests can reword, remove and add bullets after a version was built.
func jrBankDoc(t *testing.T, order []string, put map[string]map[string]any, del ...string) {
	t.Helper()
	cfg := jrConfig(t)
	raw, err := os.ReadFile(cfg.KB.Bank)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for id, b := range put {
		doc["bullets"][id] = b
	}
	for _, id := range del {
		delete(doc["bullets"], id)
	}
	doc["entries"]["alpha-job"].(map[string]any)["bullets"] = order
	out, _ := json.Marshal(doc)
	if err := os.WriteFile(cfg.KB.Bank, out, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(cfg.KB.BaseSelection)
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	secs := base["sections"].([]any)
	secs[0].(map[string]any)["entries"].([]any)[0].(map[string]any)["bullets"] = order
	out, _ = json.Marshal(base)
	if err := os.WriteFile(cfg.KB.BaseSelection, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func jrPreviews(t *testing.T, state string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(state, "resumes", ".tmp"))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func TestJobsResumeRenderWithoutFitSavesNothingAndNeverReadsAsFitted(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	p, prov := jrTailor()
	state, st := jrSetup(t, f, prov)

	out, _ := jrRun(t, 0, jrEdit, "render", "--json", jrKey)
	r := jrObject(t, out)
	if r["saved"] != false {
		t.Errorf("saved = %v", r["saved"])
	}
	for _, k := range []string{"version", "pdf_path", "pages", "pdf_sha256", "current_moved", "selection"} {
		if _, ok := r[k]; ok {
			t.Errorf("an unfitted render reports %q: %s", k, out)
		}
	}
	checks, _ := r["checks"].(map[string]any)
	if checks["fit"] != "not_run" || checks["lint"] != "passed" || checks["eligibility"] != "current" {
		t.Errorf("checks = %v", checks)
	}
	if strings.Contains(out, `"passed"`) && checks["fit"] != "not_run" {
		t.Errorf("reads as fitted: %s", out)
	}
	if bs, _ := r["bullets"].([]any); len(bs) != 2 {
		t.Errorf("bullets = %v", r["bullets"])
	}
	if kw, _ := r["keywords"].([]any); len(kw) < 8 {
		t.Errorf("keywords = %v", r["keywords"])
	}
	path, _ := r["html_path"].(string)
	if !filepath.IsAbs(path) || filepath.Dir(path) != filepath.Join(state, "resumes", ".tmp") || !strings.HasPrefix(filepath.Base(path), "preview-") {
		t.Fatalf("html_path = %q", path)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "<html></html>" {
		t.Errorf("preview = %q, %v", b, err)
	}
	if fi, _ := os.Stat(path); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi)
	}
	// Only the preview remains: rkb's build folder, with its header, is gone.
	if names := jrPreviews(t, state); len(names) != 1 || names[0] != filepath.Base(path) {
		t.Errorf("work folder holds %v", names)
	}
	jrNoJob(t, st)
	for _, a := range f.args {
		if a[0] == "fit" {
			t.Errorf("rkb fit ran for an unfitted render: %v", a)
		}
	}
	if len(p.reqs) != 0 {
		t.Errorf("render asked the model %d times", len(p.reqs))
	}
}

func TestJobsResumeRenderSweepsOldPreviewsOnly(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	state, _ := jrSetup(t, f, nil)
	dir := filepath.Join(state, "resumes", ".tmp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old, fresh, other := filepath.Join(dir, "preview-111.html"), filepath.Join(dir, "preview-222.html"), filepath.Join(dir, "notes.html")
	for _, p := range []string{old, fresh, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	aged := time.Now().Add(-2 * time.Hour)
	for _, p := range []string{old, other} {
		if err := os.Chtimes(p, aged, aged); err != nil {
			t.Fatal(err)
		}
	}
	jrRun(t, 0, jrEdit, "render", "--json", jrKey)
	if _, err := os.Stat(old); err == nil {
		t.Error("a preview older than an hour was kept")
	}
	for _, p := range []string{fresh, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(p), err)
		}
	}
}

func TestJobsResumeRenderFitSavesAnEditedVersionMadeFromTheCurrentOne(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	p, prov := jrTailor()
	_, st := jrSetup(t, f, prov)

	// The first version of a job: no parent, and it is current.
	out, _ := jrRun(t, 0, jrEdit, "render", "--json", "--fit", jrKey)
	v1 := jrSaved(t, out)
	if v1.Version != 1 || v1.Source != "edited" || v1.Parent != 0 || !v1.CurrentMoved || v1.Checks.Fit != "passed" || v1.Pages != 2 || v1.Model != "" || v1.Cost.Turns != 0 {
		t.Fatalf("v1 = %+v", v1.Meta)
	}
	if b, err := os.ReadFile(v1.PDFPath); err != nil || !bytes.Equal(b, testPDF) {
		t.Errorf("pdf: %v", err)
	}
	if _, ok := jrObject(t, out)["selection"]; ok {
		t.Error("reply carries the selection")
	}

	// Made from the current version, it takes its place.
	out, _ = jrRun(t, 0, jrEditOf("alpha-job", "b1"), "render", "--json", "--fit", jrKey)
	v2 := jrSaved(t, out)
	if v2.Version != 2 || v2.Parent != 1 || !v2.CurrentMoved {
		t.Fatalf("v2 = %+v current_moved=%v", v2.Meta, v2.CurrentMoved)
	}
	if got, _, _ := st.Get(jrKey, 2); !strings.Contains(string(got.Selection), `"b1"`) || strings.Contains(string(got.Selection), `"b2"`) {
		t.Errorf("stored choice = %s", got.Selection)
	}

	// --from names the parent; the current version stays where a newer one is.
	out, _ = jrRun(t, 0, jrEdit, "render", "--json", "--fit", "--from", "v1", jrKey)
	v3 := jrSaved(t, out)
	if v3.Version != 3 || v3.Parent != 1 || v3.CurrentMoved || !strings.Contains(v3.CurrentNote, "v2 stays current") {
		t.Fatalf("v3 = %+v current_moved=%v note=%q", v3.Meta, v3.CurrentMoved, v3.CurrentNote)
	}
	if cur, _ := st.Current(jrKey); cur.Version != 2 {
		t.Errorf("current = v%d", cur.Version)
	}
	if len(p.reqs) != 0 {
		t.Errorf("an owner's edit asked the model %d times", len(p.reqs))
	}
}

func TestJobsResumeRenderRefusals(t *testing.T) {
	huge := `{"sections":[],"jd_keywords":["` + strings.Repeat("k", resumestore.MaxSelectionBytes) + `"]}`
	cases := []struct {
		name  string
		stdin string
		args  []string
		code  string
		exit  int
		tweak func(t *testing.T, f *fakeResumeRKB)
	}{
		{"empty stdin", "", nil, "bad_edit", 1, nil},
		{"not json", "{", nil, "bad_edit", 1, nil},
		{"unknown key", `{"sections":[],"bank":"x"}`, nil, "bad_edit", 1, nil},
		{"header smuggled in", `{"sections":[],"header":{"name":"A"}}`, nil, "bad_edit", 1, nil},
		{"over 64 KiB", huge, nil, "bad_edit", 1, nil},
		{"a bullet that is not on offer", jrEditOf("alpha-job", "nope"), nil, "bad_edit", 1, nil},
		{"no bullet on the page", jrEditOf("alpha-job"), nil, "bad_edit", 1, nil},
		{"override with markup", `{"sections":[{"title":"Experience","entries":[{"entry":"alpha-job","bullets":["b1"],"title":"A *B*"}]}]}`, nil, "bad_edit", 1, nil},
		{"unknown key in the ledger", jrEdit, []string{"gh:nobody:9"}, "unknown_key", 1, nil},
		{"fit: unknown parent", jrEdit, []string{"--fit", "--from", "v9"}, "not_found", 1, nil},
		{"fit: parent is not a version", jrEdit, []string{"--fit", "--from", "latest"}, "bad_request", 1, nil},
		{"rkb cannot run", jrEdit, nil, "kb", 1, func(t *testing.T, f *fakeResumeRKB) { f.err = os.ErrNotExist }},
		{"fit: lint fails", jrEdit, []string{"--fit"}, "lint", 1, func(t *testing.T, f *fakeResumeRKB) { f.lint = 1 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeResumeRKB{pages: 2, pdf: testPDF}
			p, prov := jrTailor()
			state, st := jrSetup(t, f, prov)
			if c.tweak != nil {
				c.tweak(t, f)
			}
			args := []string{"render", "--json"}
			key := jrKey
			rest := c.args
			if len(rest) > 0 && strings.HasPrefix(rest[len(rest)-1], "gh:") {
				key, rest = rest[len(rest)-1], rest[:len(rest)-1]
			}
			args = append(append(args, rest...), key)
			out, _ := jrRun(t, c.exit, c.stdin, args...)
			if r := jrFail(t, out); r.Code != c.code {
				t.Errorf("reply = %+v, want code %q", r, c.code)
			}
			jrNoJob(t, st)
			if names := jrPreviews(t, state); len(names) != 0 {
				t.Errorf("a refused render left %v in the work folder", names)
			}
			if len(p.reqs) != 0 {
				t.Errorf("%d model turns", len(p.reqs))
			}
		})
	}
}

func TestJobsResumeRenderRefusesAnIneligibleBulletByName(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	state, st := jrSetup(t, f, nil)
	// b3 is in the bank and the owner's selection, but no reader has passed it.
	jrBankDoc(t, []string{"b1", "b2", "b3"}, map[string]map[string]any{"b3": {"text": "Shipped a thing nobody reviewed."}})
	out, _ := jrRun(t, 1, jrEditOf("alpha-job", "b1", "b3"), "render", "--json", jrKey)
	r := jrFail(t, out)
	if r.Code != "ineligible" || !strings.Contains(r.Error, `"b3"`) {
		t.Fatalf("reply = %+v", r)
	}
	jrNoJob(t, st)
	if names := jrPreviews(t, state); len(names) != 0 {
		t.Errorf("work folder holds %v", names)
	}
}

func TestJobsResumeRenderUsageAndBusy(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	state, st := jrSetup(t, f, nil)
	for _, args := range [][]string{
		{"render", "--json"},
		{"render", "--json", "--from", "v1", jrKey}, // --from needs --fit
		{"render", "--json", jrKey, "extra"},
	} {
		if stdout, _ := jrRun(t, 2, jrEdit, args...); stdout != "" {
			t.Errorf("%v: stdout %q", args, stdout)
		}
	}
	l, err := filelock.TryAcquire(filepath.Join(state, "resume-build.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	out, _ := jrRun(t, 1, jrEdit, "render", "--json", jrKey)
	if r := jrFail(t, out); r.Code != "busy" {
		t.Errorf("reply = %+v", r)
	}
	jrNoJob(t, st)
}

// ---- revalidate ------------------------------------------------------------

// jrThreeBullets saves v1 from an edit of four bullets and returns it. The bank
// is the test bank plus b3 and a continuation b4 ("Also ..."), all reviewed.
func jrThreeBullets(t *testing.T, f *fakeResumeRKB) (state string, st *resumestore.Store, v1 resumeSaved) {
	t.Helper()
	state, st = jrSetup(t, f, nil)
	jrBankDoc(t, []string{"b1", "b2", "b3", "b4"}, map[string]map[string]any{
		"b3": reviewedBullet("Shipped a serving path.", "Shipped a serving path."),
		"b4": reviewedBullet("Also tuned the serving path.", "Also tuned the serving path."),
	})
	out, _ := jrRun(t, 0, jrEditOf("alpha-job", "b1", "b2", "b3", "b4"), "render", "--json", "--fit", jrKey)
	v1 = jrSaved(t, out)
	if v1.Version != 1 || len(v1.Bullets) != 4 {
		t.Fatalf("v1 = %+v", v1.Meta)
	}
	return state, st, v1
}

func TestJobsResumeRevalidateDropsStaleBulletsAndTheirContinuations(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, st, _ := jrThreeBullets(t, f)
	// b2 is untouched; b3 is reworded (reviewed afresh on its new wording), so the
	// version was not judged on it and b4, which continues b3, cannot stand alone.
	jrBankDoc(t, []string{"b1", "b2", "b3", "b4"}, map[string]map[string]any{
		"b3": reviewedBullet("Shipped a faster serving path.", "Shipped a faster serving path."),
	})

	out, _ := jrRun(t, 0, "", "revalidate", "--json", jrKey, "v1")
	v2 := jrSaved(t, out)
	if v2.Version != 2 || v2.Source != "revalidated" || v2.Parent != 1 || !v2.CurrentMoved || v2.Checks.Fit != "passed" || v2.Cost.Turns != 0 {
		t.Fatalf("v2 = %+v", v2.Meta)
	}
	if strings.Join(v2.DroppedStale, ",") != "b3,b4" || !strings.Contains(v2.Reason, "b3, b4") || !strings.Contains(v2.Reason, "v1") {
		t.Errorf("dropped %v reason %q", v2.DroppedStale, v2.Reason)
	}
	if ids := bulletIDsOf(v2.Bullets); strings.Join(ids, ",") != "b1,b2" {
		t.Errorf("page bullets = %v", ids)
	}
	got, _, err := st.Get(jrKey, 2)
	if err != nil || got.Source != "revalidated" || strings.Contains(string(got.Selection), "b3") {
		t.Errorf("stored v2: %v %s", err, got.Selection)
	}
	if last := f.sels[len(f.sels)-1]; strings.Contains(last, "b3") || strings.Contains(last, "b4") || !strings.Contains(last, "b1") || !strings.Contains(last, "b2") {
		t.Errorf("rkb was given %s", last)
	}
	// The revalidated version is current now, so asking again finds nothing to do.
	out, _ = jrRun(t, 1, "", "revalidate", "--json", jrKey, "v2")
	if r := jrFail(t, out); r.Code != "not_stale" || r.Eligibility == nil || r.Eligibility.State != "current" {
		t.Errorf("reply = %+v", r)
	}
}

func TestJobsResumeRevalidateNamesWhatIsGoneAndIneligible(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, st, _ := jrThreeBullets(t, f)
	// b1 is removed from the bank, b3 loses its review (its wording changes with no
	// new pass), b4 is untouched but continues b3, so it goes with its lead.
	jrBankDoc(t, []string{"b2", "b3", "b4"}, map[string]map[string]any{
		"b3": {"text": "Shipped a rewritten serving path."},
	}, "b1")
	out, _ := jrRun(t, 0, "", "revalidate", "--json", jrKey, "v1")
	v2 := jrSaved(t, out)
	if strings.Join(v2.DroppedStale, ",") != "b1,b3,b4" || strings.Join(bulletIDsOf(v2.Bullets), ",") != "b2" {
		t.Fatalf("dropped %v, page %v", v2.DroppedStale, bulletIDsOf(v2.Bullets))
	}
	if v1, _, err := st.Get(jrKey, 1); err != nil || len(v1.Bullets) != 4 {
		t.Errorf("revalidating changed v1: %v %+v", err, v1.Bullets)
	}

	// Nothing is left of a version once everything it holds goes: Render refuses
	// it, nothing is saved, and v2 stays the only addition.
	jrBankDoc(t, []string{"b3", "b4"}, nil, "b2")
	out, _ = jrRun(t, 1, "", "revalidate", "--json", jrKey, "v2")
	if r := jrFail(t, out); r.Code != "bad_edit" {
		t.Errorf("reply = %+v", r)
	}
	if vs, _ := st.List(jrKey); len(vs) != 2 {
		t.Errorf("%d versions", len(vs))
	}
}

func bulletIDsOf(bs []resumestore.BulletRef) []string {
	ids := make([]string, len(bs))
	for i, b := range bs {
		ids[i] = b.ID
	}
	return ids
}

func TestJobsResumeRevalidateWhenOnlyTheValidatorReportChanged(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, st, _ := jrThreeBullets(t, f)
	report := filepath.Join(t.TempDir(), "vm_report.txt")
	if err := os.WriteFile(report, []byte("validated"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := jrConfig(t)
	cfg.KB.VMReport = report
	jrSaveConfig(t, cfg)
	out, _ := jrRun(t, 0, "", "revalidate", "--json", jrKey, "v1")
	v2 := jrSaved(t, out)
	if v2.Source != "revalidated" || len(v2.DroppedStale) != 0 || !strings.Contains(v2.Reason, "no bullet dropped") || !strings.Contains(v2.Reason, "validator report") {
		t.Fatalf("v2 = %+v dropped %v", v2.Meta, v2.DroppedStale)
	}
	if got, _, _ := st.Get(jrKey, 2); got.Prints.VMReport == "" || got.Prints.VMReport == v2.Prints.Bank {
		t.Errorf("the new version did not record the report it was built with: %+v", got.Prints)
	}
}

func TestJobsResumeRevalidateRefusals(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, st, _ := jrThreeBullets(t, f)
	cases := []struct {
		name  string
		args  []string
		code  string
		exit  int
		tweak func()
	}{
		{"unknown version", []string{jrKey, "v9"}, "not_found", 1, nil},
		{"not a version", []string{jrKey, "latest"}, "bad_request", 1, nil},
		{"zero is not a version", []string{jrKey, "v0"}, "bad_request", 1, nil},
		{"unknown key", []string{"gh:nobody:9", "v1"}, "unknown_key", 1, nil},
		{"still vouched for", []string{jrKey, "v1"}, "not_stale", 1, nil},
		{"cannot be checked", []string{jrKey, "v1"}, "unchecked", 1, func() { f.err = os.ErrNotExist }},
		{"one argument", []string{jrKey}, "", 2, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f.err = nil
			if c.tweak != nil {
				c.tweak()
			}
			out, _ := jrRun(t, c.exit, "", append([]string{"revalidate", "--json"}, c.args...)...)
			if c.exit == 1 {
				if r := jrFail(t, out); r.Code != c.code {
					t.Errorf("reply = %+v, want %q", r, c.code)
				}
			}
			if vs, _ := st.List(jrKey); len(vs) != 1 {
				t.Errorf("%d versions after a refusal", len(vs))
			}
		})
	}
}

func TestDropBulletsKeepsEverythingElse(t *testing.T) {
	sel := json.RawMessage(`{"sections":[{"title":"Summary","bullets":["s1","s2"]},{"title":"Experience","entries":[{"entry":"e","bullets":["a","b"],"title":"Lead","lead":"-"},{"entry":"f","bullets":["c"]}]}],"jd_keywords":["go"]}`)
	got, err := dropBullets(sel, map[string]bool{"s1": true, "s2": true, "a": true, "c": true})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Sections []map[string]any `json:"sections"`
		Keywords []string         `json:"jd_keywords"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Keywords) != 1 || len(doc.Sections) != 2 {
		t.Fatalf("doc = %s", got)
	}
	if _, ok := doc.Sections[0]["bullets"]; ok {
		t.Errorf("an emptied section keeps an empty list: %s", got)
	}
	ents := doc.Sections[1]["entries"].([]any)
	e0, e1 := ents[0].(map[string]any), ents[1].(map[string]any)
	if e0["title"] != "Lead" || e0["lead"] != "-" || len(e0["bullets"].([]any)) != 1 || e0["bullets"].([]any)[0] != "b" {
		t.Errorf("entry e = %v", e0)
	}
	if b, ok := e1["bullets"].([]any); !ok || len(b) != 0 { // a heading, never null
		t.Errorf("entry f bullets = %#v", e1["bullets"])
	}
	for _, bad := range []string{``, `null`, `[]`, `{"sections":{}}`, `{"sections":[{"bullets":"x"}]}`, `{"sections":[{"entries":[{"bullets":5}]}]}`} {
		if _, err := dropBullets(json.RawMessage(bad), nil); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
