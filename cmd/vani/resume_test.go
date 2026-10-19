package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/resume"
)

// fakeSynth stands in for a started DevPrism: it records what it was asked and
// answers with a canned resume_synthesize payload. No process is started.
type fakeSynth struct {
	reply   json.RawMessage
	err     error
	args    []resume.SynthesizeArgs
	headers []resume.Header
	closed  bool
}

func (f *fakeSynth) Synthesize(ctx context.Context, src resume.HeaderSource, a resume.SynthesizeArgs) (json.RawMessage, error) {
	h, err := src.Header(ctx)
	if err != nil {
		return nil, err
	}
	f.args = append(f.args, a)
	f.headers = append(f.headers, h)
	return f.reply, f.err
}

func (f *fakeSynth) Close() error { f.closed = true; return nil }

// testPDF is a generated stand-in for DevPrism's PDF; no personal data.
var testPDF = []byte("%PDF-1.7\n%neutral test file\n%%EOF\n")

func synthReply(status string, pdf []byte, pages int, mutate func(m map[string]any)) json.RawMessage {
	mat := map[string]any{"status": status, "pageCount": pages, "errors": []string{}, "pdfBytesLength": len(pdf)}
	if pdf != nil {
		mat["pdfBase64"] = base64.StdEncoding.EncodeToString(pdf)
	}
	m := map[string]any{
		"materialization": mat,
		"matchReport":     map[string]any{"atsCoveragePercentage": 71.5, "mustHaveCoveragePercentage": 80.0},
	}
	if mutate != nil {
		mutate(m)
	}
	b, _ := json.Marshal(m)
	return b
}

const goodResumeReq = `{"job":{"company":"Acme Robotics","title":"Software Engineer, Tools","description":"Build research tools in Python and TypeScript."},` +
	`"header":{"name":"Test Applicant","contact_lines":["applicant@example.invalid | 555-0100","https://example.invalid/in/test"]}}`

func resumeSetup(t *testing.T, input string, f *fakeSynth, devprism string) {
	t.Helper()
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DevPrism.Binary = devprism
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	oldIn, oldStart := resumeInput, startSynth
	resumeInput = strings.NewReader(input)
	startSynth = func(context.Context, resume.Config) (synthesizer, error) { return f, nil }
	t.Cleanup(func() { resumeInput, startSynth = oldIn, oldStart })
}

func resumeOut(t *testing.T, stdout string) resumeReply {
	t.Helper()
	var r resumeReply
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not one JSON reply: %q (%v)", stdout, err)
	}
	return r
}

func TestResumeBuildsAPDFForTheJob(t *testing.T) {
	f := &fakeSynth{reply: synthReply("rendered", testPDF, 2, nil)}
	resumeSetup(t, goodResumeReq, f, "/fake/devprism")
	stdout, _ := mustCode(t, 0, "resume")
	r := resumeOut(t, stdout)
	got, err := base64.StdEncoding.DecodeString(r.PDF)
	if err != nil || string(got) != string(testPDF) || r.Bytes != len(testPDF) || r.Error != "" {
		t.Fatalf("reply = %+v", r)
	}
	if r.Name != "Resume_Acme_Robotics.pdf" {
		t.Errorf("name = %q: neutral, from the company, never the owner's name", r.Name)
	}
	if r.Pages != 2 || r.ATSCoverage != 71.5 {
		t.Errorf("pages %d coverage %v", r.Pages, r.ATSCoverage)
	}
	if len(f.args) != 1 || !strings.Contains(f.args[0].JD, "Build research tools") || !strings.Contains(f.args[0].JD, "Software Engineer, Tools") {
		t.Fatalf("JD sent = %+v", f.args)
	}
	if h := f.headers[0]; h.Name != "Test Applicant" || len(h.ContactLines) != 2 {
		t.Errorf("header = %+v", h)
	}
	// The header goes to DevPrism through the header source, never in the JD text.
	if strings.Contains(f.args[0].JD, "Test Applicant") || strings.Contains(f.args[0].JD, "applicant@example.invalid") {
		t.Errorf("personal data in the JD: %q", f.args[0].JD)
	}
	if !f.closed {
		t.Error("DevPrism was not closed")
	}
}

func TestResumeRefusalsAreJSONWithCodes(t *testing.T) {
	big := make([]byte, maxResumePDF+1)
	copy(big, testPDF)
	cases := []struct {
		name, input, code string
		f                 *fakeSynth
		devprism          string
	}{
		{"not json", "{", "bad_request", &fakeSynth{}, "/fake"},
		{"unknown field", `{"job":{"description":"x"},"header":{"name":"A"},"ssn":"1"}`, "bad_request", &fakeSynth{}, "/fake"},
		{"no description", `{"job":{"company":"A"},"header":{"name":"A"}}`, "bad_request", &fakeSynth{}, "/fake"},
		{"no name", `{"job":{"description":"x"},"header":{"name":" "}}`, "no_name", &fakeSynth{}, "/fake"},
		{"name with a newline", `{"job":{"description":"x"},"header":{"name":"A\nB"}}`, "bad_request", &fakeSynth{}, "/fake"},
		{"five contact lines", `{"job":{"description":"x"},"header":{"name":"A","contact_lines":["1","2","3","4","5"]}}`, "bad_request", &fakeSynth{}, "/fake"},
		{"oversized", `{"job":{"description":"` + strings.Repeat("d", maxResumeInput) + `"},"header":{"name":"A"}}`, "bad_request", &fakeSynth{}, "/fake"},
		{"devprism not configured", goodResumeReq, "config", &fakeSynth{}, ""},
		{"devprism failed", goodResumeReq, "devprism", &fakeSynth{err: errors.New("resume: DevPrism process is gone")}, "/fake"},
		{"compile failed", goodResumeReq, "render", &fakeSynth{reply: synthReply("render_compile_failed", nil, 0, func(m map[string]any) {
			m["materialization"].(map[string]any)["errors"] = []string{"unknown font"}
		})}, "/fake"},
		{"unsafe source refused", goodResumeReq, "render", &fakeSynth{reply: synthReply("refused_unsafe_source", nil, 0, nil)}, "/fake"},
		{"not a pdf", goodResumeReq, "render", &fakeSynth{reply: synthReply("rendered", []byte("<html>"), 2, nil)}, "/fake"},
		{"length mismatch", goodResumeReq, "render", &fakeSynth{reply: synthReply("rendered", testPDF, 2, func(m map[string]any) {
			m["materialization"].(map[string]any)["pdfBytesLength"] = 3
		})}, "/fake"},
		{"bad base64", goodResumeReq, "render", &fakeSynth{reply: synthReply("rendered", testPDF, 2, func(m map[string]any) {
			m["materialization"].(map[string]any)["pdfBase64"] = "%%%"
		})}, "/fake"},
		{"over the cap", goodResumeReq, "too_large", &fakeSynth{reply: synthReply("rendered", big, 2, nil)}, "/fake"},
		{"three pages", goodResumeReq, "pages", &fakeSynth{reply: synthReply("rendered", testPDF, 3, nil)}, "/fake"},
		{"no page count", goodResumeReq, "render", &fakeSynth{reply: synthReply("rendered", testPDF, 0, nil)}, "/fake"},
		{"malformed reply", goodResumeReq, "render", &fakeSynth{reply: json.RawMessage(`[]`)}, "/fake"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resumeSetup(t, c.input, c.f, c.devprism)
			stdout, stderr := mustCode(t, 1, "resume")
			r := resumeOut(t, stdout)
			if r.Code != c.code || r.Error == "" || r.PDF != "" {
				t.Fatalf("reply = %+v, want code %q (stderr %q)", r, c.code, stderr)
			}
			for _, personal := range []string{"Test Applicant", "applicant@example.invalid"} {
				if strings.Contains(stdout, personal) || strings.Contains(stderr, personal) {
					t.Errorf("%q in the output: %q %q", personal, stdout, stderr)
				}
			}
		})
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
