package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/resumestore"
	"github.com/bharathvbcr/Vani/internal/rkbresume"
)

func TestJobsResumeBuildSavesATailoredVersionAndMovesCurrent(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	p, prov := jrTailor()
	state, st := jrSetup(t, f, prov)

	out, stderr := jrRun(t, 0, "", "build", "--json", jrKey)
	v1 := jrSaved(t, out)
	if v1.Version != 1 || v1.Source != "tailored" || v1.Parent != 0 || !v1.CurrentMoved || v1.CurrentNote != "" || v1.Key != jrKey {
		t.Fatalf("v1 = %+v", v1.Meta)
	}
	if v1.Model != rkbresume.Model || v1.Cost.Turns != 1 || v1.Pages != 2 || v1.Checks.Fit != "passed" || v1.Checks.Eligibility != "current" {
		t.Errorf("record = %+v", v1.Meta)
	}
	if len(p.reqs) != 1 || p.reqs[0].Model != rkbresume.Model {
		t.Fatalf("model turns: %d", len(p.reqs))
	}
	// Only the path of the PDF, absolute, in the state dir, holding the build.
	if !filepath.IsAbs(v1.PDFPath) || !strings.HasPrefix(v1.PDFPath, resumestore.Dir(state)) {
		t.Errorf("pdf_path = %q", v1.PDFPath)
	}
	if b, err := os.ReadFile(v1.PDFPath); err != nil || !bytes.Equal(b, testPDF) {
		t.Errorf("the path does not hold the PDF: %v", err)
	}
	if fi, _ := os.Stat(v1.PDFPath); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("pdf mode = %v", fi)
	}
	// The choice stays in the store; the reply carries neither it nor the PDF.
	m := jrObject(t, out)
	for _, k := range []string{"selection", "pdf"} {
		if _, ok := m[k]; ok {
			t.Errorf("reply carries %q", k)
		}
	}
	for _, k := range []string{"version", "source", "cost", "checks", "bullets", "fingerprints", "pdf_sha256", "current_moved", "pdf_path", "created", "key"} {
		if _, ok := m[k]; !ok {
			t.Errorf("reply lacks %q: %s", k, out)
		}
	}
	if strings.Contains(stderr, "Owner_Name") {
		t.Errorf("stderr = %q", stderr)
	}
	if cur, err := st.Current(jrKey); err != nil || cur.Version != 1 {
		t.Fatalf("current = %+v, %v", cur, err)
	}
	if got, _, err := st.Get(jrKey, 1); err != nil || !strings.Contains(string(got.Selection), `"b2"`) {
		t.Errorf("the stored choice is not the model's: %v %s", err, got.Selection)
	}

	// A second build is made from the current one and takes its place.
	v2 := jrBuild(t)
	if v2.Version != 2 || v2.Parent != 1 || !v2.CurrentMoved {
		t.Fatalf("v2 = %+v current_moved=%v", v2.Meta, v2.CurrentMoved)
	}
	if cur, _ := st.Current(jrKey); cur.Version != 2 {
		t.Errorf("current = v%d", cur.Version)
	}
}

func TestJobsResumeBuildWithoutOpusIsRefusedUnlessAllowBase(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, st := jrSetup(t, f, nil)

	out, _ := jrRun(t, 1, "", "build", "--json", jrKey)
	r := jrFail(t, out)
	if r.Code != "no_model" || r.Cost != nil || r.AttemptRecorded {
		t.Fatalf("reply = %+v", r)
	}
	if len(f.args) != 0 {
		t.Errorf("rkb ran %d times for a build that was refused", len(f.args))
	}
	jrNoJob(t, st)

	out, _ = jrRun(t, 0, "", "build", "--json", "--allow-base", jrKey)
	v := jrSaved(t, out)
	if v.Source != "base" || v.Reason != "no llm provider is configured" || !v.CurrentMoved || v.Version != 1 {
		t.Fatalf("v = %+v current_moved=%v", v.Meta, v.CurrentMoved)
	}
	if v.Cost.Turns != 0 || v.Model != "" {
		t.Errorf("a build with no model reports a model turn: %+v", v.Cost)
	}
}

// Gap 25: the base selection is a fallback, not a choice. Once a job has a
// current version, a base fallback is saved beside it and never displaces it.
func TestJobsResumeBaseFallbackDoesNotMoveCurrent(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	good := &draftLLM{text: resumeReply1}
	state, st := jrSetup(t, f, resumePriced{good})
	v1 := jrBuild(t)

	// Opus now answers in prose: the build falls back to the base (no --allow-base
	// needed, a model that answered badly is not "could not run").
	good.text = "I would pick b1."
	out, _ := jrRun(t, 0, "", "build", "--json", jrKey)
	v2 := jrSaved(t, out)
	if v2.Source != "base" || v2.Reason == "" || v2.Version != 2 || v2.Parent != 0 {
		t.Fatalf("v2 = %+v", v2.Meta)
	}
	if v2.CurrentMoved || !strings.Contains(v2.CurrentNote, "v1 stays current") || !strings.Contains(v2.CurrentNote, "base") {
		t.Errorf("current_moved=%v note=%q", v2.CurrentMoved, v2.CurrentNote)
	}
	if cur, err := st.Current(jrKey); err != nil || cur.Version != v1.Version {
		t.Fatalf("current = %+v %v, want v1", cur, err)
	}
	if _, err := os.Stat(v2.PDFPath); err != nil || !strings.HasPrefix(v2.PDFPath, resumestore.Dir(state)) {
		t.Errorf("the fallback was not kept: %v", err)
	}
}

func TestJobsResumeBuildDryRunSpendsNothing(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	p, prov := jrTailor()
	state, st := jrSetup(t, f, prov)

	out, _ := jrRun(t, 0, "", "build", "--json", "--dry-run", jrKey)
	r := jrObject(t, out)
	if r["dry_run"] != true || r["model"] != rkbresume.Model || r["opus_usable"] != true || r["key"] != jrKey {
		t.Fatalf("reply = %s", out)
	}
	est, _ := r["estimate"].(map[string]any)
	budget, _ := r["budget"].(map[string]any)
	if est == nil || budget == nil {
		t.Fatalf("no estimate or budget: %s", out)
	}
	if est["turns"].(float64) != rkbresume.MaxTurns || est["max_usd"].(float64) <= 0 || est["input_tokens"].(float64) <= 0 || est["max_output_tokens"].(float64) < rkbresume.TurnMaxTokens {
		t.Errorf("estimate = %v", est)
	}
	// Today's spend: nothing is in the day's file, so the cap is all that is left.
	if budget["daily_cap_usd"].(float64) <= 0 || budget["spent_usd"].(float64) != 0 || budget["remaining_usd"].(float64) != budget["daily_cap_usd"].(float64) {
		t.Errorf("budget = %v", budget)
	}
	if len(p.reqs) != 0 || len(f.args) != 0 {
		t.Errorf("a dry run made %d model turns and ran rkb %d times", len(p.reqs), len(f.args))
	}
	jrNoJob(t, st)
	if _, err := os.Stat(filepath.Join(resumestore.Dir(state))); err == nil {
		t.Error("a dry run created the resumes folder")
	}
	// The estimate is the model's own: two turns' ceiling, so a tighter cap is advice.
	cfg := jrConfig(t)
	cfg.Decide.DailyUSD.LLM = 0.0001
	jrSaveConfig(t, cfg)
	out, _ = jrRun(t, 0, "", "build", "--json", "--dry-run", jrKey)
	r = jrObject(t, out)
	if r["estimate"].(map[string]any)["cap_reached"] != true {
		t.Errorf("a cap below one turn's reserve is not reported: %s", out)
	}
}

func TestJobsResumeBuildDryRunSaysWhenOpusCannotRun(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	p := &draftLLM{text: resumeReply1}
	jrSetup(t, f, p) // draftLLM publishes no prices: Opus cannot be metered
	out, _ := jrRun(t, 0, "", "build", "--json", "--dry-run", jrKey)
	r := jrObject(t, out)
	if r["opus_usable"] != false || r["no_model"] != "the provider has no price for "+rkbresume.Model {
		t.Fatalf("reply = %s", out)
	}
	if _, ok := r["estimate"]; ok {
		t.Errorf("an estimate for a model that cannot run: %s", out)
	}
	if len(p.reqs) != 0 {
		t.Errorf("%d model turns", len(p.reqs))
	}
}

func TestJobsResumeBuildRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code string
		exit int
		seed func(t *testing.T, state string)
	}{
		{"unknown key", []string{"gh:nobody:9"}, "unknown_key", 1, nil},
		{"key with a control character", []string{"gh:a\x07b"}, "bad_request", 1, nil},
		{"empty key", []string{""}, "bad_request", 1, nil},
		{"key over the store's bound", []string{strings.Repeat("k", resumestore.MaxKeyBytes+1)}, "bad_request", 1, nil},
		{"no description", []string{"gh:blank:1"}, "bad_request", 1, func(t *testing.T, state string) {
			jrSeed(t, state, "gh:blank:1", "Blank Co", "Engineer", "  \n ")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeResumeRKB{pages: 2, pdf: testPDF}
			p, prov := jrTailor()
			state, _ := jrSetup(t, f, prov)
			if c.seed != nil {
				c.seed(t, state)
			}
			out, _ := jrRun(t, c.exit, "", append([]string{"build", "--json"}, c.args...)...)
			if r := jrFail(t, out); r.Code != c.code {
				t.Errorf("reply = %+v, want code %q", r, c.code)
			}
			if len(p.reqs) != 0 || len(f.args) != 0 {
				t.Errorf("a refused request made %d model turns and ran rkb %d times", len(p.reqs), len(f.args))
			}
		})
	}
}

func TestJobsResumeBuildUsageErrorsExitTwo(t *testing.T) {
	jrSetup(t, &fakeResumeRKB{}, nil)
	for _, args := range [][]string{
		{},
		{"nonsense", "--json", jrKey},
		{"build", "--json"},
		{"build", "--json", jrKey, "extra"},
		{"build", "--json", "--dry-run", "--allow-base", jrKey},
		{"build", "--bogus", jrKey},
	} {
		stdout, stderr := jrRun(t, 2, "", args...)
		if stdout != "" || !strings.Contains(stderr, "jobs resume") && !strings.Contains(stderr, "flag provided") {
			t.Errorf("%v: stdout %q stderr %q", args, stdout, stderr)
		}
	}
}

// A job whose folder is damaged is found out before the model is paid, not after.
func TestJobsResumeBuildFindsADamagedStoreBeforeSpending(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	p, prov := jrTailor()
	state, st := jrSetup(t, f, prov)
	v1 := jrBuild(t)
	if v1.Version != 1 {
		t.Fatal(v1.Version)
	}
	before := len(p.reqs)
	folder := filepath.Dir(v1.PDFPath)
	if err := os.Remove(filepath.Join(folder, "key")); err != nil {
		t.Fatal(err)
	}
	out, _ := jrRun(t, 1, "", "build", "--json", jrKey)
	if r := jrFail(t, out); r.Code != "damaged" || r.Cost != nil {
		t.Fatalf("reply = %+v", r)
	}
	if len(p.reqs) != before {
		t.Errorf("a model turn was paid for a store that could not take its result")
	}
	_ = state
	_ = st
}

func TestJobsResumeBuildRefusedWhileAnotherBuildHoldsTheLock(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	p, prov := jrTailor()
	state, st := jrSetup(t, f, prov)
	l, err := filelock.TryAcquire(filepath.Join(state, "resume-build.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	out, _ := jrRun(t, 1, "", "build", "--json", jrKey)
	r := jrFail(t, out)
	if r.Code != "busy" || r.Cost != nil || r.AttemptRecorded {
		t.Fatalf("reply = %+v", r)
	}
	if len(p.reqs) != 0 {
		t.Errorf("%d model turns while another build held the lock", len(p.reqs))
	}
	jrNoJob(t, st)
}

// A build that failed after the model was paid carries its cost and writes it
// to the job's attempts, so the money is never invisible.
func TestJobsResumePaidFailureIsRecordedWithItsCost(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF, lint: 1} // every page fails lint, base included
	p, prov := jrTailor()
	_, st := jrSetup(t, f, prov)
	out, _ := jrRun(t, 1, "", "build", "--json", jrKey)
	r := jrFail(t, out)
	if r.Code != "lint" || r.Cost == nil || r.Cost.Turns < 1 || !r.AttemptRecorded {
		t.Fatalf("reply = %+v", r)
	}
	if r.Cost.Turns != len(p.reqs) {
		t.Errorf("cost says %d turns, the provider saw %d", r.Cost.Turns, len(p.reqs))
	}
	jrNoVersion(t, st)
	at, err := st.Attempts(jrKey)
	if err != nil || len(at) != 1 || at[0].Code != "lint" || at[0].Cost.Turns != r.Cost.Turns {
		t.Fatalf("attempts = %+v, %v", at, err)
	}
}

// blockingLLM sends its turn and then stops the command while it waits for the
// answer: the build is cancelled after the model was paid for.
type blockingLLM struct {
	draftLLM
	stop context.CancelFunc
}

func (p *blockingLLM) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	p.reqs = append(p.reqs, req)
	return &blockingStream{ctx: ctx, stop: p.stop}, nil
}

type blockingStream struct {
	ctx  context.Context
	stop context.CancelFunc
}

func (s *blockingStream) Next() (llm.Chunk, error) {
	s.stop()
	<-s.ctx.Done()
	return llm.Chunk{}, s.ctx.Err()
}
func (s *blockingStream) Response() (llm.Response, error) { return llm.Response{}, s.ctx.Err() }
func (s *blockingStream) Close() error                    { return nil }

func TestJobsResumeCancelledBuildRecordsTheAttempt(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	p := &blockingLLM{}
	_, st := jrSetup(t, f, resumePriced{p})
	old := signalContext
	signalContext = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		p.stop = cancel
		return ctx, cancel
	}
	t.Cleanup(func() { signalContext = old })

	out, _ := jrRun(t, 1, "", "build", "--json", jrKey)
	r := jrFail(t, out)
	if r.Code != "cancelled" || r.Cost == nil || r.Cost.Turns != 1 || r.Cost.Complete || !r.AttemptRecorded {
		t.Fatalf("reply = %+v", r)
	}
	jrNoVersion(t, st)
	at, err := st.Attempts(jrKey)
	if err != nil || len(at) != 1 || at[0].Code != "cancelled" || at[0].Cost.Turns != 1 || at[0].Cost.Complete {
		t.Fatalf("attempts = %+v, %v (a cancelled turn's cost is unknown, never zero)", at, err)
	}
}

// A refusal that spent nothing writes no attempt.
func TestJobsResumeUnpaidFailureWritesNoAttempt(t *testing.T) {
	f := &fakeResumeRKB{err: errors.New("exec: no such file")}
	_, st := jrSetup(t, f, nil)
	out, _ := jrRun(t, 1, "", "build", "--json", "--allow-base", jrKey)
	if r := jrFail(t, out); r.Code != "kb" || r.Cost != nil || r.AttemptRecorded {
		t.Fatalf("reply = %+v", r)
	}
	if at, _ := st.Attempts(jrKey); len(at) != 0 {
		t.Errorf("attempts = %+v", at)
	}
}

func TestJobsResumeBuildTextOutput(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, prov := jrTailor()
	jrSetup(t, f, prov)
	out, _ := jrRun(t, 0, "", "build", jrKey)
	if !strings.Contains(out, "saved v1") || !strings.Contains(out, "now current") || !strings.Contains(out, "pdf: ") {
		t.Errorf("text = %q", out)
	}
}
