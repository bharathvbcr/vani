package rkbresume

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/filelock"
)

var renderReq = Request{Company: "Example AI", Title: "Inference Engineer", Description: testJD}

func (f *fixture) render(edit []byte, fit bool) (Result, error) {
	f.t.Helper()
	return Render(context.Background(), f.cfg, renderReq, edit, fit)
}

// baseEdit is the edit of a base build: what the editor would load and save.
func baseEdit(t *testing.T, f *fixture) []byte {
	t.Helper()
	g := *f
	g.cfg.Provider, g.cfg.NoModel = nil, "off"
	g.cfg.WorkDir, g.cfg.LockDir = "", ""
	res, err := Build(context.Background(), g.cfg, renderReq)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	f.rkb.steps, f.rkb.calls, f.rkb.sels = nil, nil, nil
	return res.SelectionJSON
}

func workEntries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

// Render with fit builds the edited choice with no model and returns the PDF.
func TestRenderBuildsTheEditedChoiceWithoutAModel(t *testing.T) {
	f := newFixture(t)
	edit := baseEdit(t, f)
	sp := &scriptedProvider{prices: testRates, calls: []call{{text: validReply()}}}
	f.cfg.Provider, f.cfg.Model = sp, "m"
	f.cfg.WorkDir = filepath.Join(f.dir, "work")
	f.rkb.fit = func(string) fitSpec {
		return fitSpec{pages: 2, pdf: testPDF, lines: map[string]int{"a1": 2}}
	}
	res, err := f.render(edit, true)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if sp.n != 0 {
		t.Fatalf("Render asked the model %d times", sp.n)
	}
	if res.Source != SourceEdited || string(res.PDF) != string(testPDF) || res.Pages != 2 || res.HTMLPath != "" || res.BuildDir != "" {
		t.Fatalf("result = %+v", res)
	}
	if want := (Checks{Eligibility: "current", Lint: "passed", Fit: "passed", Figures: "verified"}); res.Checks != want {
		t.Errorf("checks = %+v", res.Checks)
	}
	if res.Cost != (Cost{Complete: true}) || res.Model != "" {
		t.Errorf("a render with no model has cost %+v model %q", res.Cost, res.Model)
	}
	if res.FitLines["a1"] != 2 || len(res.Bullets) == 0 || len(res.SelectionJSON) == 0 {
		t.Errorf("record = %+v", res)
	}
	if got := workEntries(t, f.cfg.WorkDir); len(got) != 0 {
		t.Errorf("the build folder was left behind: %v", got)
	}
	if f.rkb.count("claims-plain") != 1 {
		t.Errorf("eligibility was not read fresh: %v", f.rkb.steps)
	}
}

// Without fit nothing is fitted or measured: no PDF, no pages, the Fit check says
// it did not run, and the caller gets rkb's page in a folder it must remove,
// holding the page and not the bank copy.
func TestRenderWithoutFitReturnsThePageAndNoPDF(t *testing.T) {
	f := newFixture(t)
	edit := baseEdit(t, f)
	f.cfg.WorkDir = filepath.Join(f.dir, "work")
	f.cfg.LockDir = f.dir
	// Another page's fit holds Chrome: a render that fits no page does not wait for it.
	chrome, err := filelock.TryAcquire(filepath.Join(f.dir, chromeLockName))
	if err != nil {
		t.Fatal(err)
	}
	defer chrome.Release()
	f.rkb.html = func(string) string { return "<html>the page</html>" }
	res, err := f.render(edit, false)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(res.PDF) != 0 || res.Pages != 0 || res.Checks.Fit != "not_run" || res.Source != SourceEdited {
		t.Fatalf("result = %+v", res)
	}
	if f.rkb.count("fit") != 0 {
		t.Errorf("rkb fit ran: %v", f.rkb.steps)
	}
	if res.FitLines != nil {
		t.Errorf("fit lines without a fit: %v", res.FitLines)
	}
	page, rerr := os.ReadFile(res.HTMLPath)
	if rerr != nil || string(page) != "<html>the page</html>" {
		t.Fatalf("page %q: %v", page, rerr)
	}
	if !strings.HasPrefix(res.HTMLPath, res.BuildDir+string(filepath.Separator)) || filepath.Dir(res.BuildDir) != f.cfg.WorkDir {
		t.Errorf("page %s is not in the build folder %s inside the work folder", res.HTMLPath, res.BuildDir)
	}
	if _, serr := os.Stat(filepath.Join(res.BuildDir, "bank.json")); serr == nil {
		t.Error("the bank copy was left in the folder the caller gets")
	}
	if _, serr := os.Stat(filepath.Join(res.BuildDir, "edited", "sel.json")); serr == nil {
		t.Error("the selection (with the header) was left in the folder the caller gets")
	}
	if got := workEntries(t, f.cfg.WorkDir); len(got) != 1 {
		t.Errorf("work folder = %v, want only the caller's build folder", got)
	}
	// The build lock is released: the next render is not busy; remove the first folder as a caller would.
	if err := os.RemoveAll(res.BuildDir); err != nil {
		t.Fatal(err)
	}
	if _, err := f.render(edit, false); err != nil {
		t.Errorf("a second render: %v", err)
	}
}

// The build lock is the one Build takes: a render during a build is busy.
func TestRenderTakesTheBuildLock(t *testing.T) {
	f := newFixture(t)
	edit := baseEdit(t, f)
	f.cfg.LockDir = f.dir
	held, err := filelock.TryAcquire(filepath.Join(f.dir, buildLockName))
	if err != nil {
		t.Fatal(err)
	}
	_, rerr := f.render(edit, false)
	held.Release()
	wantCode(t, rerr, CodeBusy)
	if len(f.rkb.steps) != 0 {
		t.Errorf("rkb ran while busy: %v", f.rkb.steps)
	}
}

// An override reaches rkb; Source stays edited.
func TestRenderAppliesTheEditsOverrides(t *testing.T) {
	f := newFixture(t)
	edit := string(baseEdit(t, f))
	edit = strings.Replace(edit, `{"entry":"exp-a","bullets"`, `{"entry":"exp-a","title":"Staff Engineer","bullets"`, 1)
	if _, err := f.render([]byte(edit), true); err != nil {
		t.Fatalf("Render: %v", err)
	}
	last := f.rkb.sels[len(f.rkb.sels)-1]
	if !strings.Contains(last, `"Staff Engineer"`) {
		t.Errorf("the override did not reach the selection:\n%s", last)
	}
}

// Eligibility is judged now, not when the edit was made: a bullet whose wording
// changed since is refused with its id, not dropped, and nothing is rendered.
func TestRenderRefusesABulletThatBecameIneligibleByName(t *testing.T) {
	f := newFixture(t)
	edit := baseEdit(t, f)
	if !strings.Contains(string(edit), `"a1"`) {
		t.Fatalf("scenario: the base edit does not hold a1: %s", edit)
	}
	editJSON(t, f.bank, func(m map[string]any) { // reworded after review: the readers' hash no longer matches
		m["bullets"].(map[string]any)["a1"].(map[string]any)["text"] = "Cut tail latency by half in the serving path"
	})
	res, err := f.render(edit, true)
	e := wantCode(t, err, CodeIneligible)
	if !strings.Contains(e.Msg, `"a1"`) {
		t.Errorf("the refusal does not name the bullet: %q", e.Msg)
	}
	if len(res.PDF) != 0 || f.rkb.count("render") != 0 || f.rkb.count("fit") != 0 {
		t.Errorf("something was built from an edit with an ineligible bullet: %v", f.rkb.steps)
	}
	if strings.Contains(e.Msg, ownerName) || strings.Contains(e.Msg, "serving path") {
		t.Errorf("the message quotes text: %q", e.Msg)
	}
}

// The first ineligible bullet in page order is the one named, every time.
func TestRenderNamesTheFirstIneligibleBulletInPageOrder(t *testing.T) {
	f := newFixture(t)
	edit := baseEdit(t, f)
	editJSON(t, f.bank, func(m map[string]any) {
		bl := m["bullets"].(map[string]any)
		for _, id := range []string{"a2", "s2"} {
			bl[id].(map[string]any)["text"] = "reworded " + id
		}
	})
	for i := 0; i < 5; i++ {
		_, err := f.render(edit, false)
		if e := wantCode(t, err, CodeIneligible); !strings.Contains(e.Msg, `"s2"`) { // Summary is first on the page
			t.Fatalf("run %d: %q", i, e.Msg)
		}
	}
}

// A bullet no selection lists any more is not on offer: refused as a bad edit.
func TestRenderRefusesABulletThatIsNoLongerOnOffer(t *testing.T) {
	f := newFixture(t)
	edit := baseEdit(t, f)
	editJSON(t, f.base, func(m map[string]any) {
		secs := m["sections"].([]any)
		exp := secs[1].(map[string]any)
		exp["entries"].([]any)[0].(map[string]any)["bullets"] = []string{"a2"} // a1 leaves the base
	})
	_, err := f.render(edit, false)
	wantCode(t, err, CodeEdit)
}

// Malformed, oversized, empty and over-keyed edits are refused before a lock is
// taken or rkb runs.
func TestRenderRefusesBadEditsBeforeAnythingRuns(t *testing.T) {
	f := newFixture(t)
	good := string(baseEdit(t, f))
	cases := map[string]string{
		"empty":                      "",
		"whitespace":                 "  \n",
		"not json":                   "{",
		"an array":                   "[]",
		"null":                       "null",
		"oversized":                  `{"sections":[],"x":"` + strings.Repeat("a", maxEditBytes) + `"}`,
		"text after":                 good + " {}",
		"a header key":               strings.Replace(good, `{"sections"`, `{"header":{"name":"x"},"sections"`, 1),
		"a bank key":                 strings.Replace(good, `{"sections"`, `{"bank":"/tmp/b.json","sections"`, 1),
		"a meta key":                 strings.Replace(good, `{"sections"`, `{"meta":{},"sections"`, 1),
		"a path on an entry":         strings.Replace(good, `{"entry":"exp-a"`, `{"path":"/etc/passwd","entry":"exp-a"`, 1),
		"a duplicate key":            strings.Replace(good, `{"sections"`, `{"jd_keywords":[],"jd_keywords":[],"sections"`, 1),
		"no sections":                `{"sections":[]}`,
		"a malformed id":             strings.Replace(good, `"a1"`, `"a1/../x"`, 1),
		"a spelled-wrong key":        strings.Replace(good, `"entries"`, `"Entries"`, 1),
		"an override with {{":        strings.Replace(good, `{"entry":"exp-a"`, `{"entry":"exp-a","title":"{{clmLat}}"`, 1),
		"an override with md":        strings.Replace(good, `{"entry":"exp-a"`, `{"entry":"exp-a","lead":"[x](http://y)"`, 1),
		"an override too long":       strings.Replace(good, `{"entry":"exp-a"`, `{"entry":"exp-a","org":"`+strings.Repeat("o", 121)+`"`, 1),
		"an override with a newline": strings.Replace(good, `{"entry":"exp-a"`, `{"entry":"exp-a","title":"a\nb"`, 1),
	}
	f.cfg.LockDir = f.dir
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			f.rkb.steps = nil
			res, err := f.render([]byte(edit), true)
			if err == nil {
				t.Fatalf("accepted %q", clip(edit, 80))
			}
			var e *Error
			if !errors.As(err, &e) || e.Code != CodeEdit {
				t.Errorf("error = %v, want code %s", err, CodeEdit)
			}
			if len(res.PDF) != 0 {
				t.Error("a refused edit returned a page")
			}
			if strings.Contains(e.Msg, ownerName) || strings.Contains(e.Msg, "/etc/passwd") && name != "a path on an entry" {
				t.Errorf("message echoes input: %q", e.Msg)
			}
			if len(f.rkb.steps) != 0 {
				t.Errorf("rkb ran for a malformed edit: %v", f.rkb.steps)
			}
		})
	}
}

// Edits that are well formed but cannot go on the page: a bullet under the wrong
// entry, an entry in the wrong section, a repeated bullet, keywords the posting
// lacks, nothing on the page.
func TestRenderRefusesEditsThatBreakThePageRules(t *testing.T) {
	f := newFixture(t)
	for name, edit := range map[string]string{
		"a bullet under the wrong entry":        editWith(`{"entry":"exp-a","bullets":["b2"]}`, `{"entry":"exp-b","bullets":["b2"]}`),
		"an unknown bullet":                     editWith(`{"entry":"exp-a","bullets":["nope"]}`, `{"entry":"exp-b","bullets":["b2"]}`),
		"a repeated bullet":                     editWith(`{"entry":"exp-a","bullets":["a3","a3"]}`, `{"entry":"exp-b","bullets":["b2"]}`),
		"an entry in a section it cannot go in": `{"sections":[{"title":"Summary","bullets":["s3"]},{"title":"Education","entries":[{"entry":"exp-a","bullets":["a3"]}]}]}`,
		"keywords the posting lacks":            strings.Replace(editWith(`{"entry":"exp-a","bullets":["a3"]}`), `"Kubernetes"`, `"Fortran"`, 1),
		"nothing on the page":                   `{"sections":[{"title":"Experience","entries":[]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			f.rkb.steps = nil
			_, err := f.render([]byte(edit), true)
			wantCode(t, err, CodeEdit)
			if f.rkb.count("render") != 0 {
				t.Error("rendered a refused edit")
			}
		})
	}
}

// Cancelling stops the render, at the start or in the middle of rkb, and leaves
// no build folder.
func TestRenderCancelled(t *testing.T) {
	t.Run("before it starts", func(t *testing.T) {
		f := newFixture(t)
		edit := baseEdit(t, f)
		f.cfg.WorkDir = filepath.Join(f.dir, "work")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Render(ctx, f.cfg, renderReq, edit, true)
		wantCode(t, err, CodeCancel)
		if got := workEntries(t, f.cfg.WorkDir); len(got) != 0 {
			t.Errorf("build folder left: %v", got)
		}
	})
	t.Run("during lint", func(t *testing.T) {
		f := newFixture(t)
		edit := baseEdit(t, f)
		f.cfg.WorkDir = filepath.Join(f.dir, "work")
		f.cfg.LockDir = f.dir
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.rkb.onStep = func(step string, c Command) {
			if step == "lint" {
				cancel()
			}
		}
		f.rkb.block = "lint"
		res, err := Render(ctx, f.cfg, renderReq, edit, true)
		wantCode(t, err, CodeCancel)
		if len(res.PDF) != 0 || res.Checks.Lint != "not_run" {
			t.Errorf("result = %+v", res)
		}
		if got := workEntries(t, f.cfg.WorkDir); len(got) != 0 {
			t.Errorf("build folder left: %v", got)
		}
		// The lock is released.
		f.rkb.onStep, f.rkb.block = nil, ""
		if _, err := f.render(edit, false); err != nil {
			t.Errorf("render after a cancelled one: %v", err)
		}
	})
}

// rkb that cannot run is a coded error with no page; it never looks like a pass.
func TestRenderWithRkbDown(t *testing.T) {
	for step, code := range map[string]string{"claims-plain": CodeKB, "claims-used": CodeRender, "lint": CodeRender, "render": CodeRender, "fit": CodeRender} {
		t.Run(step, func(t *testing.T) {
			f := newFixture(t)
			edit := baseEdit(t, f)
			f.cfg.WorkDir = filepath.Join(f.dir, "work")
			f.rkb.runErr = map[string]error{step: errors.New("rkb: exec format error")}
			res, err := f.render(edit, true)
			wantCode(t, err, code)
			if len(res.PDF) != 0 || res.HTMLPath != "" || res.BuildDir != "" {
				t.Errorf("result = %+v", res)
			}
			if res.Checks.Fit == "passed" || res.Checks.Lint == "passed" && step == "lint" {
				t.Errorf("a check that did not run reports a pass: %+v", res.Checks)
			}
			if got := workEntries(t, f.cfg.WorkDir); len(got) != 0 {
				t.Errorf("build folder left: %v", got)
			}
		})
	}
}

// rkb's lint rejecting the edit is an error carrying the message, with lint
// "failed" in the checks.
func TestRenderLintFailureIsAnErrorWithFailedCheck(t *testing.T) {
	f := newFixture(t)
	edit := baseEdit(t, f)
	f.rkb.lint = func(string) (int, string) { return 1, "ERROR a1 is too long\n" }
	res, err := f.render(edit, true)
	e := wantCode(t, err, CodeLint)
	if !strings.Contains(e.Msg, "too long") || res.Checks.Lint != "failed" || len(res.PDF) != 0 {
		t.Errorf("err %q result %+v", e.Msg, res)
	}
}

// The owner accepts soft layout flaws, as the base does: kept with a note, fit "soft".
func TestRenderKeepsSoftLayoutFlawsWithANote(t *testing.T) {
	f := newFixture(t)
	edit := baseEdit(t, f)
	f.rkb.fit = func(string) fitSpec {
		return fitSpec{status: 5, advice: "OK: 2 page(s). STRICT: 1 orphan last line(s)", pages: 2, pdf: testPDF, orphans: []string{"a1"}}
	}
	res, err := f.render(edit, true)
	if err != nil || res.Checks.Fit != "soft" || len(res.Notes) != 1 || len(res.PDF) == 0 {
		t.Fatalf("result %+v, err %v", res, err)
	}
	// A hard failure is an error.
	f.rkb.fit = func(string) fitSpec {
		return fitSpec{status: 2, advice: "OVERFLOW: cut 3 lines", pages: 3, pdf: testPDF}
	}
	res, err = f.render(edit, true)
	wantCode(t, err, CodeFit)
	if res.Checks.Fit != "failed" || len(res.PDF) != 0 {
		t.Errorf("result %+v", res)
	}
}

// A render that fits no page needs no pdftotext; one that fits does.
func TestRenderNeedsPdftotextOnlyToFit(t *testing.T) {
	f := newFixture(t)
	edit := baseEdit(t, f)
	if err := os.Remove(filepath.Join(f.dir, "bin", "pdftotext")); err != nil {
		t.Fatal(err)
	}
	f.cfg.ToolDirs = []string{} // none: the host's Homebrew copy is not consulted
	f.cfg.WorkDir = filepath.Join(f.dir, "work")
	res, err := f.render(edit, false)
	if err != nil {
		t.Fatalf("a render with no fit needs no pdftotext: %v", err)
	}
	_ = os.RemoveAll(res.BuildDir)
	_, err = f.render(edit, true)
	wantCode(t, err, CodeRender)
}

// An unverified figure is dropped and listed, as in a build.
func TestRenderDropsAnUnverifiedFigureAndSaysSo(t *testing.T) {
	f := newFixture(t)
	edit := strings.Replace(editWith(`{"entry":"exp-a","bullets":["a3"]}`, `{"entry":"exp-c","bullets":["c1","u1"]}`), `{"entry":"exp-a"`, `{"entry":"exp-a"`, 1)
	f.rkb.unverified = map[string][]string{"clmUnv": {"u1"}}
	res, err := f.render([]byte(edit), true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Checks.Figures != "dropped" || strings.Join(res.UnverifiedDropped, ",") != "u1" || strings.Contains(string(res.SelectionJSON), "u1") {
		t.Errorf("result = %+v", res)
	}
}

// What Render returns round-trips: its own record is an edit Render accepts.
func TestRenderRecordIsAcceptedAsAnEditAgain(t *testing.T) {
	f := newFixture(t)
	res, err := f.render(baseEdit(t, f), true)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.render(res.SelectionJSON, true)
	if err != nil || string(again.SelectionJSON) != string(res.SelectionJSON) {
		t.Fatalf("err %v\n%s\n%s", err, res.SelectionJSON, again.SelectionJSON)
	}
}
