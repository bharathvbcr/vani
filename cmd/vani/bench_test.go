package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// firstOption is the option the fake server picks for slot.
func firstOption(slot string) string {
	var opts []string
	for o := range jdtriage.Descriptions()[slot] {
		opts = append(opts, o)
	}
	sort.Strings(opts)
	return opts[0]
}

func secondOption(slot string) string {
	var opts []string
	for o := range jdtriage.Descriptions()[slot] {
		opts = append(opts, o)
	}
	sort.Strings(opts)
	return opts[1]
}

// seedBench writes postings with primary readings. override changes one
// slot's primary value; skip leaves a slot unanswered.
func seedBench(t *testing.T, stateDir string, key, desc, backend string, override map[string]string, skip string, shadow map[string]string) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	p := posting.Posting{Key: posting.Key(key), ATS: "gh", Company: "Acme", Title: "Engineer", Description: desc}
	ann := map[string]string{}
	for _, slot := range digest.TriageSlots {
		pre := ledger.AnnTriagePrefix + slot
		if slot == skip {
			ann[pre+ledger.AnnTriageSuffixReading] = "unavailable"
			continue
		}
		v := firstOption(slot)
		if o, ok := override[slot]; ok {
			v = o
		}
		ann[pre] = v
		ann[pre+ledger.AnnTriageSuffixReading] = "answered"
		ann[pre+ledger.AnnTriageSuffixBackend] = backend
		ann[pre+ledger.AnnTriageSuffixP] = "0.9500"
		if s, ok := shadow[slot]; ok {
			ann[pre+ledger.AnnTriageSuffixShadow] = s
		}
	}
	if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Digested, Posting: &p, Annotations: ann}); err != nil {
		t.Fatal(err)
	}
}

func benchSetup(t *testing.T) (state, bin, model string) {
	cfgPath, state := setup(t)
	writeTarget(t, cfgPath, "Inference engineering roles.")
	bin = filepath.Join(t.TempDir(), "llama-server")
	writeLlama(t, bin, true)
	model = filepath.Join(t.TempDir(), "fake-d1.gguf")
	if err := os.WriteFile(model, []byte("GGUF"), 0o600); err != nil {
		t.Fatal(err)
	}
	return state, bin, model
}

func TestBenchLocalMeasuresAgainstThePrimary(t *testing.T) {
	state, bin, model := benchSetup(t)
	seedBench(t, state, "gh:a:1", "Serve models.", "jev", nil, "", map[string]string{"role_fit": firstOption("role_fit")})
	seedBench(t, state, "gh:a:2", "Train models.", "openrouter", map[string]string{"role_fit": secondOption("role_fit")}, "", nil)
	seedBench(t, state, "gh:a:3", "FAILME", "jev", nil, "", nil)
	seedBench(t, state, "gh:a:4", "Unread slot.", "jev", nil, "work_mode", nil)
	seedBench(t, state, "gh:a:5", "Shadow only.", "local", nil, "", nil)
	before, err := os.ReadFile(filepath.Join(state, "ledger", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	o, _ := mustCode(t, 0, "bench", "local", "--json", "--binary", bin, "--model", model)
	var rep benchReport
	if err := json.Unmarshal([]byte(o), &rep); err != nil {
		t.Fatalf("report %q: %v", o, err)
	}
	if rep.Postings != 3 || rep.Eligible != 3 || rep.Primary["jev"] != 2 || rep.Primary["openrouter"] != 1 || len(rep.Models) != 1 {
		t.Fatalf("population %+v", rep)
	}
	m := rep.Models[0]
	if m.Name != "fake-d1" || m.Asked != 3 || m.Errors != 1 || m.Error != "" || m.Stopped != "" {
		t.Fatalf("model %+v", m)
	}
	slots := map[string]benchSlot{}
	for _, s := range m.Slots {
		slots[s.Slot] = s
	}
	rf := slots["role_fit"]
	if rf.N != 2 || rf.Agree != 1 || rf.Unanswered != 1 || rf.ReproN != 1 || rf.ReproAgree != 1 {
		t.Errorf("role_fit %+v", rf)
	}
	if rf.Confusion[secondOption("role_fit")][firstOption("role_fit")] != 1 {
		t.Errorf("confusion %+v", rf.Confusion)
	}
	if b := rf.ByLocalP[len(rf.ByLocalP)-1]; b.N != 2 || b.Agree != 1 {
		t.Errorf("p >= .9 bucket %+v", b)
	}
	if s := slots["sponsorship"]; s.N != 2 || s.Agree != 2 {
		t.Errorf("sponsorship %+v", s)
	}
	after, _ := os.ReadFile(filepath.Join(state, "ledger", "events.jsonl"))
	if !bytes.Equal(before, after) {
		t.Fatal("the bench wrote the ledger")
	}
	// The text report says agreement is not accuracy and shows the slot table.
	o, _ = mustCode(t, 0, "bench", "local", "--binary", bin, "--model", model, "-n", "1")
	if !strings.Contains(o, "not accuracy") || !strings.Contains(o, "role_fit") || !strings.Contains(o, "postings: 1 of 3 eligible") {
		t.Errorf("text report %q", o)
	}
}

// A model that cannot start is reported and fails the command, so a bench
// that measured nothing never reads as a pass; the other models still run.
func TestBenchLocalModelThatCannotStartFails(t *testing.T) {
	state, bin, model := benchSetup(t)
	seedBench(t, state, "gh:a:1", "Serve models.", "jev", nil, "", nil)
	bad := filepath.Join(t.TempDir(), "llama-bad")
	writeLlama(t, bad, false)
	o, _ := mustCode(t, 1, "bench", "local", "--json", "--binary", bad, "--model", model)
	var rep benchReport
	if err := json.Unmarshal([]byte(o), &rep); err != nil || len(rep.Models) != 1 || rep.Models[0].Error == "" {
		t.Fatalf("report %q (%v)", o, err)
	}
	// Two models, one missing: the good one is measured, the command still fails.
	missing := filepath.Join(t.TempDir(), "missing.gguf")
	o, _ = mustCode(t, 1, "bench", "local", "--json", "--binary", bin, "--model", model, "--model", missing)
	if err := json.Unmarshal([]byte(o), &rep); err != nil || len(rep.Models) != 2 || rep.Models[0].Asked != 1 || rep.Models[1].Error == "" {
		t.Fatalf("report %q (%v)", o, err)
	}
}

// A model that stops answering is given up on after a bounded streak.
func TestBenchLocalStopsAModelThatNeverAnswers(t *testing.T) {
	state, bin, model := benchSetup(t)
	for i := range benchMaxConsecutiveErrors + 5 {
		seedBench(t, state, "gh:f:"+string(rune('a'+i)), "FAILME", "jev", nil, "", nil)
	}
	o, _ := mustCode(t, 1, "bench", "local", "--json", "--binary", bin, "--model", model)
	var rep benchReport
	if err := json.Unmarshal([]byte(o), &rep); err != nil {
		t.Fatal(err)
	}
	if m := rep.Models[0]; m.Asked != benchMaxConsecutiveErrors || m.Stopped == "" {
		t.Fatalf("model %+v", m)
	}
}

// A model that answered no posting fails the command even when the run is
// too short for the error streak to stop it.
func TestBenchLocalModelThatAnsweredNothingFails(t *testing.T) {
	state, bin, model := benchSetup(t)
	seedBench(t, state, "gh:f:a", "FAILME", "jev", nil, "", nil)
	seedBench(t, state, "gh:f:b", "FAILME", "jev", nil, "", nil)
	o, _ := mustCode(t, 1, "bench", "local", "--json", "--binary", bin, "--model", model)
	var rep benchReport
	if err := json.Unmarshal([]byte(o), &rep); err != nil {
		t.Fatal(err)
	}
	if m := rep.Models[0]; m.Asked != 2 || m.Errors != 2 || m.Stopped != "" {
		t.Fatalf("model %+v", m)
	}
}

// A bare `bench local` measures the configured shadow: its model, binary,
// batch and slots, so the numbers describe the server a run starts.
func TestBenchLocalUsesTheConfiguredServer(t *testing.T) {
	cfgPath, state := setup(t)
	bin := filepath.Join(t.TempDir(), "llama-server")
	argsFile := filepath.Join(t.TempDir(), "args")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nVANI_FAKE_LLAMA=1 VANI_FAKE_LLAMA_ARGS='" + argsFile + "' exec '" + self + "' \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(t.TempDir(), "d3.gguf")
	if err := os.WriteFile(model, []byte("GGUF"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.TargetSummary = "Inference engineering roles."
	cfg.Decide.Shadow = "local"
	cfg.Decide.Local = &config.Local{Model: "d3", Server: &config.LocalServer{
		Binary: bin, ModelPath: model, BatchSize: 4096, Parallel: 1}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	seedBench(t, state, "gh:a:1", "Serve models.", "jev", nil, "", nil)
	o, _ := mustCode(t, 0, "bench", "local", "--json")
	var rep benchReport
	if err := json.Unmarshal([]byte(o), &rep); err != nil || len(rep.Models) != 1 || rep.Models[0].Path != model || rep.Models[0].Asked != 1 {
		t.Fatalf("report %q (%v)", o, err)
	}
	got, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(got), "-b 4096 -ub 4096") || !strings.Contains(string(got), "-np 1") {
		t.Errorf("configured batch and slots not passed: %q", got)
	}
	// A flag overrides the config.
	mustCode(t, 0, "bench", "local", "--json", "--batch", "2048")
	if got, _ := os.ReadFile(argsFile); !strings.Contains(string(got), "-b 2048 -ub 2048") {
		t.Errorf("--batch did not override: %q", got)
	}
}

// --endpoint measures a decision server that is already running on loopback
// (one llama.cpp cannot load), with the same request and the same report, and
// starts no llama-server.
func TestBenchLocalMeasuresARunningEndpoint(t *testing.T) {
	state, _, _ := benchSetup(t)
	t.Setenv("PATH", t.TempDir()) // no llama-server: the bench must not need one
	seedBench(t, state, "gh:a:1", "Serve models.", "jev", nil, "", nil)
	seedBench(t, state, "gh:a:2", "Train models.", "jev", map[string]string{"role_fit": secondOption("role_fit")}, "", nil)
	var models []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		models = append(models, req.Model)
		r.Body = io.NopCloser(bytes.NewReader(body))
		fakeSystemOne(w, r)
	}))
	defer srv.Close()
	ep := srv.URL + "/v1/systemone"

	o, _ := mustCode(t, 0, "bench", "local", "--json", "--endpoint", ep, "--name", "rsi-jev-v6")
	var rep benchReport
	if err := json.Unmarshal([]byte(o), &rep); err != nil || len(rep.Models) != 1 {
		t.Fatalf("report %q (%v)", o, err)
	}
	m := rep.Models[0]
	if m.Name != "rsi-jev-v6" || m.Path != ep || m.Asked != 2 || m.Errors != 0 || m.Error != "" || m.StartMS != 0 {
		t.Fatalf("model %+v", m)
	}
	for _, s := range m.Slots {
		want := 2
		if s.Slot == "role_fit" {
			want = 1
		}
		if s.N != 2 || s.Agree != want {
			t.Errorf("%s %+v", s.Slot, s)
		}
	}
	if len(models) != 2 || models[0] != "rsi-jev-v6" {
		t.Errorf("request model names %v", models)
	}

	// A server that is not there measures nothing, and that fails the command.
	srv.Close()
	o, _ = mustCode(t, 1, "bench", "local", "--json", "--endpoint", ep)
	rep = benchReport{}
	if err := json.Unmarshal([]byte(o), &rep); err != nil || len(rep.Models) != 1 || rep.Models[0].Name != "endpoint" || rep.Code != codeRuntime {
		t.Errorf("dead endpoint report %q (%v)", o, err)
	}
}

// A partial run samples every company, not the first one in key order.
func TestBenchCasesSpreadAcrossCompanies(t *testing.T) {
	ann := map[string]string{}
	for _, slot := range digest.TriageSlots {
		pre := ledger.AnnTriagePrefix + slot
		ann[pre], ann[pre+ledger.AnnTriageSuffixReading], ann[pre+ledger.AnnTriageSuffixBackend] = firstOption(slot), "answered", "d1-ultra"
	}
	var snap ledger.Snapshot
	for _, k := range []string{"ab:a:1", "ab:a:2", "ab:a:3", "ab:b:1", "ab:b:2", "gh:c:1"} {
		co := strings.Split(k, ":")[1]
		snap.Records = append(snap.Records, ledger.Record{Key: posting.Key(k), Annotations: ann,
			Posting: posting.Posting{Key: posting.Key(k), Company: strings.ToUpper(co), Description: "x"}})
	}
	got, eligible, primary := benchCases(snap, 4)
	var keys []string
	for _, c := range got {
		keys = append(keys, string(c.rec.Key))
	}
	if strings.Join(keys, " ") != "ab:a:1 ab:b:1 gh:c:1 ab:a:2" || eligible != 6 || primary["d1-ultra"] != 4 {
		t.Errorf("picked %v of %d (%v)", keys, eligible, primary)
	}
	if got, _, _ := benchCases(snap, 100); len(got) != 6 {
		t.Errorf("n above the population takes all: %d", len(got))
	}
}

// A reading copied from an identical posting (triage.inherited_from) is not a
// primary's measurement of that posting: measuring a model against it would
// count one reading twice. The cases leave inherited records out, as the eval
// report does.
func TestBenchCasesSkipInheritedReadings(t *testing.T) {
	ann := map[string]string{}
	for _, slot := range digest.TriageSlots {
		pre := ledger.AnnTriagePrefix + slot
		ann[pre], ann[pre+ledger.AnnTriageSuffixReading], ann[pre+ledger.AnnTriageSuffixBackend] = firstOption(slot), "answered", "d1-ultra"
	}
	copied := map[string]string{ledger.AnnTriageInheritedFrom: "gh:a:1"}
	for k, v := range ann {
		copied[k] = v
	}
	var snap ledger.Snapshot
	for k, a := range map[string]map[string]string{"gh:a:1": ann, "gh:a:2": copied} {
		snap.Records = append(snap.Records, ledger.Record{Key: posting.Key(k), Annotations: a,
			Posting: posting.Posting{Key: posting.Key(k), Company: "A", Description: "x"}})
	}
	got, eligible, primary := benchCases(snap, 10)
	if len(got) != 1 || got[0].rec.Key != "gh:a:1" || eligible != 1 || primary["d1-ultra"] != 1 {
		t.Errorf("inherited reading counted: %d cases, eligible %d, %v", len(got), eligible, primary)
	}
}

func TestBenchLocalRefusals(t *testing.T) {
	state, bin, model := benchSetup(t)
	for _, args := range [][]string{
		{"bench"},
		{"bench", "remote"},
		{"bench", "local", "-n", "0"},
		{"bench", "local", "-n", "999999"},
		{"bench", "local", "--questions", "both"},
		{"bench", "local", "--timeout-ms", "10"},
		{"bench", "local", "--binary", bin, "--model", "relative.gguf"},
		{"bench", "local", "--binary", bin, "--model", "/abs/not-a-model.bin"},
		{"bench", "local", "--binary", bin, "--model", model, "--model", model, "--model", model, "--model", model, "--model", model},
		{"bench", "local", "extra"},
		{"bench", "local", "--name", "x"},
		{"bench", "local", "--endpoint", "http://example.com:8000/v1/systemone"},
		{"bench", "local", "--endpoint", "https://127.0.0.1:8000/v1/systemone"},
		{"bench", "local", "--endpoint", "http://127.0.0.1/v1/systemone"},
		{"bench", "local", "--endpoint", "http://10.0.0.5:8000/v1/systemone"},
		{"bench", "local", "--endpoint", "http://127.0.0.1:8000/v1/systemone", "--model", model},
		{"bench", "local", "--endpoint", "http://127.0.0.1:8000/v1/systemone", "--binary", bin},
		{"bench", "local", "--endpoint", "http://127.0.0.1:8000/v1/systemone", "--batch", "512"},
	} {
		mustCode(t, 2, args...)
	}
	// No ledger, then a ledger with nothing the primary answered.
	mustCode(t, 1, "bench", "local", "--binary", bin, "--model", model)
	seedBench(t, state, "gh:a:1", "Only the shadow read it.", "local", nil, "", nil)
	if _, e := mustCode(t, 1, "bench", "local", "--binary", bin, "--model", model); !strings.Contains(e, "no posting") {
		t.Errorf("empty population: %q", e)
	}
}
