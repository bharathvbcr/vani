package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/rkbresume"
)

// fakeResumeRKB plays rkb for `vani resume`: it answers claims export, lint,
// render and fit, and records the arguments and the selections it was handed.
type fakeResumeRKB struct {
	mu    sync.Mutex
	args  [][]string
	sels  []string
	lint  int    // exit code of rkb lint
	fit   int    // status of rkb fit (and its exit code)
	pages int    // pages the fit report names
	pdf   []byte // what fit writes
	die   bool   // fit dies: exit 2, no report
	err   error  // runner error for every call
}

var testPDF = []byte("%PDF-1.7\n%neutral test file\n%%EOF\n")

func (f *fakeResumeRKB) Run(_ context.Context, c rkbresume.Command, _ time.Duration) (rkbresume.Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.args = append(f.args, c.Args)
	if f.err != nil {
		return rkbresume.Output{}, f.err
	}
	val := func(name string) string {
		for i, a := range c.Args {
			if a == name && i+1 < len(c.Args) {
				return c.Args[i+1]
			}
		}
		return ""
	}
	switch c.Args[0] {
	case "claims":
		if sel := val("--used-in"); sel != "" {
			b, _ := os.ReadFile(sel)
			f.sels = append(f.sels, string(b))
		}
		return rkbresume.Output{}, os.WriteFile(val("--out"), []byte(`{"claims":{"clmLat":{"value":"42%","double_verified":true}}}`), 0o600)
	case "lint":
		return rkbresume.Output{Exit: f.lint, Stdout: "ERROR b1: weak verb\n"}, nil
	case "render":
		dir := val("--out-dir")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return rkbresume.Output{}, err
		}
		return rkbresume.Output{}, os.WriteFile(filepath.Join(dir, "resume.html"), []byte("<html></html>"), 0o600)
	case "fit":
		if f.die {
			return rkbresume.Output{Exit: 2, Stderr: "rkb: no Chrome/Chromium found"}, nil
		}
		if err := os.WriteFile(val("--out"), f.pdf, 0o600); err != nil {
			return rkbresume.Output{}, err
		}
		rep, _ := json.Marshal(map[string]any{"status": f.fit, "advice": "OVERFLOW: cut a line", "pages": f.pages})
		if err := os.WriteFile(val("--report"), rep, 0o600); err != nil {
			return rkbresume.Output{}, err
		}
		return rkbresume.Output{Exit: f.fit}, nil
	}
	return rkbresume.Output{}, errors.New("unexpected rkb command " + c.Args[0])
}

const goodResumeReq = `{"job":{"company":"Acme Robotics","title":"Software Engineer, Tools","description":"Build research tools in Python and TypeScript for evaluation, tracing, latency, agents and kernels."},` +
	`"header":{"name":"Test Applicant","contact_lines":["applicant@example.invalid | 555-0100","https://example.invalid/in/test"]}}`

const resumeReply1 = `{"sections":[{"title":"Experience","entries":[{"entry":"alpha-job","bullets":["b2","b1"]}]}],` +
	`"jd_keywords":["research","tools","Python","TypeScript","evaluation","tracing","latency","agents"]}`

// resumeSetup writes a synthetic bank, two bullets both readers passed, a base
// selection and a config that names them, and replaces the stdin, rkb and
// provider seams. withLLM adds the test provider's configuration.
func resumeSetup(t *testing.T, input string, f *fakeResumeRKB, p llm.Provider) {
	t.Helper()
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "pdftotext"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	bank := filepath.Join(dir, "bank.json")
	raw, _ := json.Marshal(map[string]any{
		"entries": map[string]any{"alpha-job": draftEntry("Engineer", "Acme", "b1", "b2")},
		"bullets": map[string]any{
			"b1": reviewedBullet("Cut latency by {{clmLat}}.", "Cut latency by 42%."),
			"b2": reviewedBullet("Built a tracing pipeline for evaluation.", "Built a tracing pipeline for evaluation."),
		},
	})
	if err := os.WriteFile(bank, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, "base.json")
	sel, _ := json.Marshal(map[string]any{
		"bank": "bank.json", "header": "default",
		"meta": map[string]any{"pages": 2, "output_basename": "Owner_Name_Resume"},
		"sections": []any{
			map[string]any{"title": "Experience", "kind": "entries", "entries": []any{map[string]any{"entry": "alpha-job", "bullets": []string{"b1", "b2"}}}},
			map[string]any{"title": "Skills", "kind": "lines", "lines": []any{map[string]any{"label": "Languages", "text": "Go"}}},
		},
	})
	if err := os.WriteFile(base, sel, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.KB = config.KB{Bank: bank, RKB: "/fake/rkb", CodeRoot: dir, Selections: []string{base}, BaseSelection: base}
	if p != nil {
		cfg.LLM = testLLM
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	oldIn, oldRun, oldProv := resumeInput, resumeRunner, resumeProvider
	resumeInput = strings.NewReader(input)
	resumeRunner = f
	resumeProvider = func(context.Context, *wired) (llm.Provider, error) {
		if p == nil {
			return nil, errors.New("no provider in this test")
		}
		return p, nil
	}
	t.Cleanup(func() { resumeInput, resumeRunner, resumeProvider = oldIn, oldRun, oldProv })
}

func resumeOut(t *testing.T, stdout string) resumeReply {
	t.Helper()
	var r resumeReply
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not one JSON reply: %q (%v)", stdout, err)
	}
	return r
}

func noPersonalData(t *testing.T, where string, texts ...string) {
	t.Helper()
	for _, s := range texts {
		for _, personal := range []string{"Test Applicant", "applicant@example.invalid", "555-0100"} {
			if strings.Contains(s, personal) {
				t.Errorf("%q in %s: %q", personal, where, s)
			}
		}
	}
}

func TestResumeBuildsABasePDFWithoutAModelAndWithoutDevPrism(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	resumeSetup(t, goodResumeReq, f, nil)
	stdout, stderr := mustCode(t, 0, "resume")
	r := resumeOut(t, stdout)
	got, err := base64.StdEncoding.DecodeString(r.PDF)
	if err != nil || string(got) != string(testPDF) || r.Bytes != len(testPDF) || r.Error != "" {
		t.Fatalf("reply = %+v", r)
	}
	if r.Name != "Resume_Acme_Robotics.pdf" || r.Pages != 2 || r.Selection != "base" || r.Reason != "no llm provider is configured" {
		t.Errorf("reply = %+v", r)
	}
	// The shape the app and the extension read, unchanged, plus the new fields.
	var m map[string]any
	_ = json.Unmarshal([]byte(stdout), &m)
	for _, k := range []string{"pdf", "name", "bytes", "pages", "selection", "reason"} {
		if _, ok := m[k]; !ok {
			t.Errorf("reply lacks %q: %v", k, m)
		}
	}
	// The header was checked and then left out of everything.
	noPersonalData(t, "the reply or stderr", stdout, stderr)
	for _, sel := range f.sels {
		noPersonalData(t, "a selection rkb read", sel)
		if strings.Contains(sel, "Owner_Name_Resume") {
			t.Error("the owner's output name was passed on")
		}
	}
	if strings.Contains(strings.Join(f.args[len(f.args)-1], " "), "--final") {
		t.Errorf("fit args = %v", f.args[len(f.args)-1])
	}
}

func TestResumeTailorsWithTheProviderAndNeverSendsTheHeader(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	p := &draftLLM{text: resumeReply1}
	resumeSetup(t, goodResumeReq, f, p)
	stdout, stderr := mustCode(t, 0, "resume")
	r := resumeOut(t, stdout)
	if r.Selection != "tailored" || r.Reason != "" || r.Error != "" || r.Pages != 2 {
		t.Fatalf("reply = %+v", r)
	}
	if len(p.reqs) != 1 {
		t.Fatalf("%d model turns", len(p.reqs))
	}
	noPersonalData(t, "the prompt", p.reqs[0].System, p.reqs[0].Messages[0].Text())
	noPersonalData(t, "the reply or stderr", stdout, stderr)
	user := p.reqs[0].Messages[0].Text()
	if !strings.Contains(user, "Build research tools") || !strings.Contains(user, "b1: Cut latency by 42%.") {
		t.Errorf("prompt lacks the posting or the evidence:\n%s", user)
	}
	if !strings.Contains(f.sels[len(f.sels)-1], `"b2"`) || strings.Index(f.sels[len(f.sels)-1], `"b2"`) > strings.Index(f.sels[len(f.sels)-1], `"b1"`) {
		t.Errorf("the model's order was not built:\n%s", f.sels[len(f.sels)-1])
	}
}

func TestResumeFallsBackToTheBaseWhenTheModelCannotAnswer(t *testing.T) {
	for name, p := range map[string]*draftLLM{
		"budget exhausted": {err: errors.Join(budget.ErrExhausted, errors.New("spent"))},
		"prose":            {text: "I would pick b1."},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeResumeRKB{pages: 2, pdf: testPDF}
			resumeSetup(t, goodResumeReq, f, p)
			stdout, _ := mustCode(t, 0, "resume")
			r := resumeOut(t, stdout)
			if r.Selection != "base" || r.Reason == "" || r.PDF == "" {
				t.Fatalf("reply = %+v", r)
			}
		})
	}
}

func TestResumeWithoutAUsableProviderBuildsTheBase(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	resumeSetup(t, goodResumeReq, f, nil)
	cfgPath := os.Getenv("VANI_CONFIG")
	cfg, _ := config.Load(cfgPath)
	cfg.LLM = testLLM // configured, but the provider cannot be built (no key)
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := mustCode(t, 0, "resume")
	r := resumeOut(t, stdout)
	if r.Selection != "base" || r.Reason != "the model provider is not available" {
		t.Fatalf("reply = %+v", r)
	}
	if !strings.Contains(stderr, "no model") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestResumeRefusalsAreJSONWithCodes(t *testing.T) {
	big := append([]byte("%PDF-1.7\n"), make([]byte, maxResumePDF)...)
	cases := []struct {
		name, input, code string
		f                 *fakeResumeRKB
		mutate            func(t *testing.T, cfgPath string)
	}{
		{"not json", "{", "bad_request", &fakeResumeRKB{}, nil},
		{"unknown field", `{"job":{"description":"x"},"header":{"name":"A"},"ssn":"1"}`, "bad_request", &fakeResumeRKB{}, nil},
		{"no description", `{"job":{"company":"A"},"header":{"name":"A"}}`, "bad_request", &fakeResumeRKB{}, nil},
		{"no name", `{"job":{"description":"x"},"header":{"name":" "}}`, "no_name", &fakeResumeRKB{}, nil},
		{"name with a newline", `{"job":{"description":"x"},"header":{"name":"A\nB"}}`, "bad_request", &fakeResumeRKB{}, nil},
		{"five contact lines", `{"job":{"description":"x"},"header":{"name":"A","contact_lines":["1","2","3","4","5"]}}`, "bad_request", &fakeResumeRKB{}, nil},
		{"contact line with a control character", `{"job":{"description":"x"},"header":{"name":"A","contact_lines":["a\u0007b"]}}`, "bad_request", &fakeResumeRKB{}, nil},
		{"oversized", `{"job":{"description":"` + strings.Repeat("d", maxResumeInput) + `"},"header":{"name":"A"}}`, "bad_request", &fakeResumeRKB{}, nil},
		{"kb not configured", goodResumeReq, "config", &fakeResumeRKB{}, func(t *testing.T, p string) {
			cfg, _ := config.Load(p)
			cfg.KB = config.KB{}
			if err := config.Save(p, cfg); err != nil {
				t.Fatal(err)
			}
		}},
		{"no base selection", goodResumeReq, "config", &fakeResumeRKB{}, func(t *testing.T, p string) {
			cfg, _ := config.Load(p)
			cfg.KB.Selections, cfg.KB.BaseSelection = nil, ""
			if err := config.Save(p, cfg); err != nil {
				t.Fatal(err)
			}
		}},
		{"rkb cannot run", goodResumeReq, "kb", &fakeResumeRKB{err: errors.New("exec: no such file")}, nil},
		{"lint errors", goodResumeReq, "lint", &fakeResumeRKB{lint: 1, pages: 2, pdf: testPDF}, nil},
		{"fit overflow", goodResumeReq, "fit", &fakeResumeRKB{fit: 2, pages: 3, pdf: testPDF}, nil},
		{"fit short", goodResumeReq, "fit", &fakeResumeRKB{fit: 3, pages: 1, pdf: testPDF}, nil},
		{"fit dies (no Chrome)", goodResumeReq, "render", &fakeResumeRKB{die: true}, nil},
		{"not a pdf", goodResumeReq, "render", &fakeResumeRKB{pages: 2, pdf: []byte("<html>")}, nil},
		{"over the cap", goodResumeReq, "too_large", &fakeResumeRKB{pages: 2, pdf: big}, nil},
		{"three pages", goodResumeReq, "pages", &fakeResumeRKB{pages: 3, pdf: testPDF}, nil},
		{"no page count", goodResumeReq, "render", &fakeResumeRKB{pages: 0, pdf: testPDF}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resumeSetup(t, c.input, c.f, nil)
			if c.mutate != nil {
				c.mutate(t, os.Getenv("VANI_CONFIG"))
			}
			stdout, stderr := mustCode(t, 1, "resume")
			r := resumeOut(t, stdout)
			if r.Code != c.code || r.Error == "" || r.PDF != "" {
				t.Fatalf("reply = %+v, want code %q (stderr %q)", r, c.code, stderr)
			}
			noPersonalData(t, "the output", stdout, stderr)
		})
	}
}

// kb.vm_report wins and is never replaced by a guess; unset, the fallback in
// rkb's CV folder is used and the reply's note says which file and how old.
func TestResumeVMReportIsConfiguredOrNamedInTheNote(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	cfg := config.Config{KB: config.KB{CodeRoot: root}}
	if got, note, err := resumeVMReport(cfg, now); got != "" || err != nil || !strings.Contains(note, "no validator report") {
		t.Errorf("no report: %q %q %v", got, note, err)
	}
	cv := filepath.Join(root, "career", "CurriculumVitae")
	if err := os.MkdirAll(cv, 0o755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(cv, "vm_report.txt")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, note, err := resumeVMReport(cfg, now); got != plain || err != nil || !strings.Contains(note, "vm_report.txt") {
		t.Errorf("got %q %q %v, want %q named in the note", got, note, err, plain)
	}
	full := filepath.Join(cv, "vm_report_full.txt")
	if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, note, _ := resumeVMReport(cfg, now); got != full || !strings.Contains(note, "vm_report_full.txt") || !strings.Contains(note, "kb.vm_report is not set") {
		t.Errorf("got %q %q, want %q", got, note, full)
	}
	cfg.KB.VMReport = plain // the owner's choice beats the fallback, with no note
	if got, note, err := resumeVMReport(cfg, now); got != plain || note != "" || err != nil {
		t.Errorf("configured: %q %q %v", got, note, err)
	}
	cfg.KB.VMReport = filepath.Join(cv, "gone.txt") // set but missing: an error, not a guess
	if got, _, err := resumeVMReport(cfg, now); got != "" || err == nil {
		t.Errorf("missing configured report: %q %v", got, err)
	}
	cfg.KB.VMReport = cv // a folder is not a report
	if _, _, err := resumeVMReport(cfg, now); err == nil {
		t.Error("a folder must not pass as the report")
	}
}

func TestResumeFileName(t *testing.T) {
	for in, want := range map[string]string{
		"Acme Robotics":          "Resume_Acme_Robotics.pdf",
		"  ":                     "Resume.pdf",
		"Ünïcode & Co., Inc.":    "Resume_Ünïcode_Co_Inc.pdf",
		"Acme\u0000\n<script>":   "Resume_Acme_script.pdf",
		"../../etc/passwd":       "Resume_etc_passwd.pdf",
		strings.Repeat("a", 500): "Resume_" + strings.Repeat("a", 60) + ".pdf",
	} {
		if got := resumeFileName(in); got != want {
			t.Errorf("resumeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResumeIsInTheUsage(t *testing.T) {
	setup(t)
	_, stderr := mustCode(t, 2, "bogus")
	if !strings.Contains(stderr, "  resume ") {
		t.Errorf("usage does not list resume:\n%s", stderr)
	}
}
