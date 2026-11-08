package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/Vani/internal/letterstore"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const coverBody = "I want this role at Northwind."

func coverReq(company, question string) string {
	raw, _ := json.Marshal(draft.Request{
		Question: question,
		Job:      draft.Job{Company: company, Title: "Engineer", Description: "About the role\nOwn inference latency."},
		MaxWords: 80,
	})
	return string(raw)
}

type seqDraft struct {
	texts []string
	err   error
	block time.Duration
	reqs  []llm.Request
}

func (p *seqDraft) Name() string { return "fake" }
func (p *seqDraft) Capability(model string) (llm.Capability, bool) {
	return llm.Capability{Provider: "fake", Model: model, MaxOutputTokens: 4096}, true
}
func (p *seqDraft) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	i := len(p.reqs)
	p.reqs = append(p.reqs, req)
	if p.block > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(p.block):
		}
	}
	if p.err != nil {
		return nil, p.err
	}
	if i >= len(p.texts) {
		i = len(p.texts) - 1
	}
	return &draftStream{text: p.texts[i]}, nil
}

func stampHeader(t *testing.T, token string) {
	t.Helper()
	cfgPath := os.Getenv("VANI_CONFIG")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.KB.Selections) == 0 {
		t.Fatal("no selection to stamp")
	}
	path := cfg.KB.Selections[0]
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["header"] = map[string]any{"name": token}
	stamped, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, stamped, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.KB.BaseSelection = path
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestCoverWritesABodyAndAPDFWithoutTheLetterheadInThePrompt(t *testing.T) {
	const token = "HXTOKENQX"
	p := &seqDraft{texts: []string{coverBody}}
	draftSetup(t, coverReq("Northwind", "Why Northwind?"), &draftLLM{}, true)
	old := draftProvider
	draftProvider = func(context.Context, *wired) (llm.Provider, error) { return p, nil }
	t.Cleanup(func() { draftProvider = old })
	stampHeader(t, token)
	stdout, stderr := mustCode(t, 0, "cover")
	var reply coverReply
	if err := json.Unmarshal([]byte(stdout), &reply); err != nil {
		t.Fatalf("stdout %s", stdout)
	}
	if reply.Body != coverBody || reply.CompanyCheck != draft.CompanyNamed || reply.PDF == "" {
		t.Fatalf("reply %+v", reply)
	}
	pdf, err := base64.StdEncoding.DecodeString(reply.PDF)
	if err != nil || !strings.HasPrefix(string(pdf), "%PDF-") || !strings.Contains(string(pdf), token) {
		t.Fatalf("pdf err %v contains token %v", err, pdf != nil && strings.Contains(string(pdf), token))
	}
	prompt := p.reqs[0].Messages[0].Text() + p.reqs[0].System
	for _, leak := range []string{token, stdout, stderr} {
		_ = leak
	}
	if strings.Contains(prompt, token) || strings.Contains(stderr, token) || strings.Contains(reply.Body, token) || strings.Contains(reply.Error, token) {
		t.Fatal("the letterhead left the PDF")
	}
	if strings.Contains(stdout, token) {
		t.Fatal("the JSON reply contains the letterhead")
	}
	if !strings.Contains(prompt, "250") {
		t.Fatal("the cover prompt did not use the 250-word bound")
	}
	if strings.Contains(prompt, "visa") {
		t.Fatal("the request question reached the prompt")
	}
}

func TestCoverRefusesEmptyOversizedDuplicateAndUngroundedWithoutAPDF(t *testing.T) {
	p := &seqDraft{texts: []string{"I cut latency by 90% at Northwind."}}
	cases := []struct {
		name, input, code string
		calls             int
	}{
		{"empty", `{}`, "no_text", 0},
		{"not json", `{`, "bad_request", 0},
		{"duplicate", coverReq("Northwind", "q") + coverReq("Other", "q"), "bad_request", 0},
		{"oversized", `{"question":"` + strings.Repeat("q", maxDraftInput) + `"}`, "bad_request", 0},
		{"ungrounded", coverReq("Northwind", "visa sponsorship details"), "ungrounded", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pp := &seqDraft{texts: p.texts}
			draftSetup(t, tc.input, &draftLLM{}, true)
			old := draftProvider
			draftProvider = func(context.Context, *wired) (llm.Provider, error) { return pp, nil }
			t.Cleanup(func() { draftProvider = old })
			stdout, _ := mustCode(t, 1, "cover")
			var reply coverReply
			if err := json.Unmarshal([]byte(stdout), &reply); err != nil {
				t.Fatalf("stdout %s", stdout)
			}
			if reply.Code != tc.code || reply.PDF != "" || (tc.code != "ungrounded" && reply.Body != "") {
				t.Fatalf("reply %+v", reply)
			}
			if len(pp.reqs) != tc.calls {
				t.Fatalf("%d provider calls, want %d", len(pp.reqs), tc.calls)
			}
		})
	}
}

func TestCoverBudgetTimeoutAndNoBulletsSaveNothingUsable(t *testing.T) {
	p := &draftLLM{err: fmt.Errorf("reserve: %w", budget.ErrExhausted)}
	draftSetup(t, coverReq("Northwind", "q"), p, true)
	stdout, _ := mustCode(t, 1, "cover")
	var reply coverReply
	if err := json.Unmarshal([]byte(stdout), &reply); err != nil || reply.Code != draft.CodeBudget || reply.Body != "" || reply.PDF != "" {
		t.Fatalf("budget reply %+v %v", reply, err)
	}

	blocker := &seqDraft{texts: []string{coverBody}, block: time.Second}
	draftSetup(t, coverReq("Northwind", "q"), &draftLLM{}, true)
	oldT := coverModelTimeout
	coverModelTimeout = 40 * time.Millisecond
	t.Cleanup(func() { coverModelTimeout = oldT })
	old := draftProvider
	draftProvider = func(context.Context, *wired) (llm.Provider, error) { return blocker, nil }
	t.Cleanup(func() { draftProvider = old })
	stdout, _ = mustCode(t, 1, "cover")
	if err := json.Unmarshal([]byte(stdout), &reply); err != nil || reply.Code != draft.CodeTimeout || reply.PDF != "" || reply.Body != "" {
		t.Fatalf("timeout reply %+v", reply)
	}

	draftSetup(t, coverReq("Northwind", "q"), &draftLLM{text: coverBody}, false)
	stdout, _ = mustCode(t, 1, "cover")
	if err := json.Unmarshal([]byte(stdout), &reply); err != nil || reply.Code != "no_evidence" || reply.Body != "" {
		t.Fatalf("no bullets reply %+v", reply)
	}
}

func TestCoverRenderFailureKeepsTheTextAndReportsNoPDF(t *testing.T) {
	body := "I want this role at Northwind. λ"
	p := &seqDraft{texts: []string{body}}
	draftSetup(t, coverReq("Northwind", "q"), &draftLLM{}, true)
	old := draftProvider
	draftProvider = func(context.Context, *wired) (llm.Provider, error) { return p, nil }
	t.Cleanup(func() { draftProvider = old })
	stdout, _ := mustCode(t, 1, "cover")
	var reply coverReply
	if err := json.Unmarshal([]byte(stdout), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Code != "render" || reply.Body != body || reply.PDF != "" {
		t.Fatalf("reply %+v", reply)
	}
}

func TestJobsCoverBuildSavesAVersionAndUseIsWhatIsFetched(t *testing.T) {
	p := &seqDraft{texts: []string{"I want this role at Acme."}}
	draftSetup(t, `{}`, &draftLLM{}, true)
	old := draftProvider
	draftProvider = func(context.Context, *wired) (llm.Provider, error) { return p, nil }
	t.Cleanup(func() { draftProvider = old })
	state := os.Getenv("VANI_STATE_DIR")
	seedJobs(t, state, seedJob{key: "gh:acme:1", title: "Engineer", status: posting.Live, company: "Acme"})
	stdout, stderr := mustCode(t, 0, "jobs", "cover", "build", "--json", "gh:acme:1")
	var saved jobsCoverSaved
	if err := json.Unmarshal([]byte(stdout), &saved); err != nil {
		t.Fatalf("stdout %s stderr %s", stdout, stderr)
	}
	if saved.Version != 1 || !saved.PDF || strings.Contains(stdout, "I want this role") {
		t.Fatalf("saved %+v stdout %s", saved, stdout)
	}
	st, err := letterstore.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Used("gh:acme:1"); letterstore.CodeOf(err) != letterstore.CodeNotFound {
		t.Fatalf("build marked the letter used: %v", err)
	}
	stdout, _ = mustCode(t, 0, "jobs", "cover", "use", "--json", "gh:acme:1")
	var used jobsCoverUsed
	if err := json.Unmarshal([]byte(stdout), &used); err != nil || used.Version != 1 || !used.PDF || used.TextPath == "" {
		t.Fatalf("used %+v err %v stdout %s", used, err, stdout)
	}
	if strings.Contains(stdout, "I want this role") {
		t.Fatal("use echoed the letter")
	}
	fi, err := os.Stat(used.TextPath)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("text file %v %v", fi, err)
	}
}

func TestJobsCoverSavesNothingWhenTheDraftCannotBeUsed(t *testing.T) {
	stateSetup := func(t *testing.T, p llm.Provider) {
		t.Helper()
		draftSetup(t, `{}`, &draftLLM{}, true)
		old := draftProvider
		draftProvider = func(context.Context, *wired) (llm.Provider, error) { return p, nil }
		t.Cleanup(func() { draftProvider = old })
		seedJobs(t, os.Getenv("VANI_STATE_DIR"), seedJob{key: "gh:acme:1", title: "Engineer", status: posting.Live, company: "Acme"})
	}
	p := &seqDraft{texts: []string{"I cut latency by 90% at Acme."}}
	stateSetup(t, p)
	stdout, _ := mustCode(t, 1, "jobs", "cover", "build", "--json", "gh:acme:1")
	var saved jobsCoverSaved
	if err := json.Unmarshal([]byte(stdout), &saved); err != nil || saved.Code != "ungrounded" || saved.Version != 0 {
		t.Fatalf("ungrounded %+v stdout %s", saved, stdout)
	}
	st, err := letterstore.Open(os.Getenv("VANI_STATE_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Current("gh:acme:1"); letterstore.CodeOf(err) != letterstore.CodeNotFound {
		t.Fatalf("an ungrounded letter was saved: %v", err)
	}

	stateSetup(t, &draftLLM{err: fmt.Errorf("down: %w", io.ErrUnexpectedEOF)})
	stdout, _ = mustCode(t, 1, "jobs", "cover", "build", "--json", "gh:acme:1")
	if err := json.Unmarshal([]byte(stdout), &saved); err != nil || saved.Version != 0 {
		t.Fatalf("dependency down saved %+v", saved)
	}
}

func TestJobsCoverRenderFailureKeepsTheTextAndDoesNotReportAPDF(t *testing.T) {
	p := &seqDraft{texts: []string{"I want this role at Acme. λ"}}
	draftSetup(t, `{}`, &draftLLM{}, true)
	old := draftProvider
	draftProvider = func(context.Context, *wired) (llm.Provider, error) { return p, nil }
	t.Cleanup(func() { draftProvider = old })
	seedJobs(t, os.Getenv("VANI_STATE_DIR"), seedJob{key: "gh:acme:1", title: "Engineer", status: posting.Live, company: "Acme"})
	stdout, _ := mustCode(t, 1, "jobs", "cover", "build", "--json", "gh:acme:1")
	var saved jobsCoverSaved
	if err := json.Unmarshal([]byte(stdout), &saved); err != nil {
		t.Fatal(stdout)
	}
	if saved.Code != "render" || !saved.TextSaved || saved.PDF || saved.Version != 1 || strings.Contains(stdout, "λ") {
		t.Fatalf("saved %+v stdout %s", saved, stdout)
	}
	st, err := letterstore.Open(os.Getenv("VANI_STATE_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	text, err := st.Text("gh:acme:1", 1)
	if err != nil || !strings.Contains(text, "Acme") {
		t.Fatalf("text %q err %v", text, err)
	}
	if _, err := st.PDF("gh:acme:1", 1); letterstore.CodeOf(err) != letterstore.CodeNotFound {
		t.Fatalf("a render failure left a PDF: %v", err)
	}
}
