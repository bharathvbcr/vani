package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resumestore"
)

// The `vani jobs resume` tests run the real command in process, against the
// resume tests' fake rkb and fake provider (resume_test.go), a synthetic bank
// and a ledger seeded here. No test spends money or reads a key.

const (
	jrKey  = "gh:acme:1"
	jrDesc = "Build research tools in Python and TypeScript for evaluation, tracing, latency, agents and kernels."
)

// jrSetup is resumeSetup plus a ledger holding the job under jrKey, and returns
// the state dir and the store. p nil leaves no model.
func jrSetup(t *testing.T, f *fakeResumeRKB, p llm.Provider) (state string, st *resumestore.Store) {
	t.Helper()
	resumeSetup(t, "", f, p)
	state = os.Getenv("VANI_STATE_DIR")
	jrSeed(t, state, jrKey, "Acme Robotics", "Software Engineer, Tools", jrDesc)
	st, err := resumestore.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	return state, st
}

// jrSeed adds one posting to the ledger under state and closes the ledger again
// (a command that holds no lock must not need it released by the test).
func jrSeed(t *testing.T, state, key, company, title, desc string) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	p := posting.Posting{Key: posting.Key(key), ATS: "gh", Company: company, Title: title, Location: "Austin, TX", Description: desc, URL: "https://boards.invalid/" + key}
	e := ledger.Event{V: 1, At: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC), Key: p.Key, Status: posting.Scored, Posting: &p, Annotations: map[string]string{}}
	if err := l.Append(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

// jrRun runs `vani jobs resume ARGS` with stdin as the command's standard input
// and returns what it wrote. The command must exit with want.
func jrRun(t *testing.T, want int, stdin string, args ...string) (stdout, stderr string) {
	t.Helper()
	resumeInput = strings.NewReader(stdin)
	t.Cleanup(func() { resumeInput = io.Reader(os.Stdin) })
	stdout, stderr = mustCode(t, want, append([]string{"jobs", "resume"}, args...)...)
	noPersonalData(t, "the output of jobs resume "+strings.Join(args, " "), stdout, stderr)
	return stdout, stderr
}

func jrSaved(t *testing.T, stdout string) resumeSaved {
	t.Helper()
	var s resumeSaved
	if err := json.Unmarshal([]byte(stdout), &s); err != nil {
		t.Fatalf("stdout is not one saved reply: %q (%v)", stdout, err)
	}
	return s
}

func jrFail(t *testing.T, stdout string) resumeErrorReply {
	t.Helper()
	var r resumeErrorReply
	if err := json.Unmarshal([]byte(stdout), &r); err != nil || r.Error == "" || r.Code == "" {
		t.Fatalf("stdout is not an error reply: %q (%v)", stdout, err)
	}
	return r
}

func jrObject(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("stdout is not one JSON object: %q (%v)", stdout, err)
	}
	return m
}

// jrTailor is a provider whose Opus turn picks the tailored selection.
func jrTailor() (*draftLLM, llm.Provider) {
	p := &draftLLM{text: resumeReply1}
	return p, resumePriced{p}
}

// jrBuild runs a build that must succeed and returns its reply.
func jrBuild(t *testing.T) resumeSaved {
	t.Helper()
	out, _ := jrRun(t, 0, "", "build", "--json", jrKey)
	return jrSaved(t, out)
}

// jrNoVersion fails when the job has a saved version.
func jrNoVersion(t *testing.T, st *resumestore.Store) {
	t.Helper()
	if vs, err := st.List(jrKey); !errors.Is(err, resumestore.ErrNotFound) || len(vs) != 0 {
		t.Errorf("a version was saved: %d versions, %v", len(vs), err)
	}
}

// jrNoJob fails when the job has a version or a recorded attempt: nothing ran.
func jrNoJob(t *testing.T, st *resumestore.Store) {
	t.Helper()
	jrNoVersion(t, st)
	if at, err := st.Attempts(jrKey); (err != nil && !errors.Is(err, resumestore.ErrNotFound)) || len(at) != 0 {
		t.Errorf("attempts = %+v, %v", at, err)
	}
}

func jrConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load(os.Getenv("VANI_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func jrSaveConfig(t *testing.T, cfg config.Config) {
	t.Helper()
	if err := config.Save(os.Getenv("VANI_CONFIG"), cfg); err != nil {
		t.Fatal(err)
	}
}
