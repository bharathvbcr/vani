package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// statusOf runs `vani status --json` and decodes the reply, failing on a
// non-zero exit: a facet that cannot be read never fails the command.
func statusOf(t *testing.T) statusJSON {
	t.Helper()
	o, _ := mustCode(t, 0, "status", "--json")
	var s statusJSON
	if err := json.Unmarshal([]byte(o), &s); err != nil {
		t.Fatalf("status --json is not one JSON object: %v\n%s", err, o)
	}
	return s
}

func openRouterItem(t *testing.T, s statusJSON) backendJSON {
	t.Helper()
	for _, b := range s.Backends.Items {
		if b.Kind == "openrouter" {
			return b
		}
	}
	t.Fatalf("no openrouter backend in %+v", s.Backends)
	return backendJSON{}
}

func llmItem(t *testing.T, s statusJSON) backendJSON {
	t.Helper()
	for _, b := range s.Backends.Items {
		if b.Kind == "llm" {
			return b
		}
	}
	t.Fatalf("no llm backend in %+v", s.Backends)
	return backendJSON{}
}

func writeStateFile(t *testing.T, stateDir, rel, content string) {
	t.Helper()
	p := filepath.Join(stateDir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func today() string { return time.Now().UTC().Format("2006-01-02") }

func TestStatusJSONFreshInstallSaysWhatIsMissing(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	s := statusOf(t)

	if !s.Config.OK || s.Config.Boards == nil || *s.Config.Boards != 0 {
		t.Errorf("config: %+v", s.Config)
	}
	// No ledger yet is stated, not passed off as a ledger holding nothing.
	if !s.Ledger.OK || s.Ledger.Exists || s.Ledger.Total != 0 || !strings.Contains(s.Ledger.Reason, "no ledger") {
		t.Errorf("ledger: %+v", s.Ledger)
	}
	if s.LastRun.State != "none" || !s.LastRun.OK || s.LastRun.Reason == "" {
		t.Errorf("last run: %+v", s.LastRun)
	}
	if !s.Schedule.OK || s.Schedule.NextRun.IsZero() || !s.Schedule.NextRun.After(s.GeneratedAt) {
		t.Errorf("schedule: %+v", s.Schedule)
	}
	if !s.Backends.OK || len(s.Backends.Items) != 2 || s.Backends.Day != today() {
		t.Fatalf("backends: %+v", s.Backends)
	}
	for _, b := range s.Backends.Items {
		if !b.OK || b.SpentUSD == nil || *b.SpentUSD != 0 || b.RemainingUSD == nil || *b.RemainingUSD != b.CapUSD || b.Exhausted {
			t.Errorf("backend %s: %+v", b.Kind, b)
		}
	}
	if !s.Decide.OK || s.Decide.LocalConfigured || s.Decide.Shadow != "" {
		t.Errorf("decide: %+v", s.Decide)
	}
	if s.StateDir != stateDir {
		t.Errorf("state dir %q, want %q", s.StateDir, stateDir)
	}
	// Reading the status must not create anything.
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Errorf("status --json created the state dir: %v", err)
	}
}

func TestStatusJSONAnswersWhileTheLedgerIsLocked(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	led, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer led.Close() // held open: a daemon mid-run
	at := time.Date(2026, 10, 18, 15, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		key string
		st  posting.Status
	}{{"gh:a:1", posting.Explained}, {"gh:a:2", posting.Explained}, {"gh:a:3", posting.Dead}} {
		p := posting.Posting{Key: posting.Key(c.key), Company: "Acme", Title: "Eng", URL: "https://x.example/" + c.key}
		if err := led.Append(context.Background(), ledger.Event{V: 1, At: at, Key: posting.Key(c.key), Status: c.st, Posting: &p}); err != nil {
			t.Fatal(err)
		}
	}
	s := statusOf(t)
	if !s.Ledger.OK || !s.Ledger.Exists || s.Ledger.Total != 3 || s.Ledger.ByStatus["explained"] != 2 || s.Ledger.ByStatus["dead"] != 1 {
		t.Errorf("ledger while locked: %+v", s.Ledger)
	}
}

func TestStatusJSONUnreadableLedgerIsUnknownNotEmpty(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	// Corruption before the last line: ReadSnapshot refuses it.
	writeStateFile(t, stateDir, "ledger/events.jsonl", "not json\n{\"v\":1}\n")
	s := statusOf(t)
	if s.Ledger.OK || s.Ledger.Reason == "" || s.Ledger.Total != 0 || len(s.Ledger.ByStatus) != 0 {
		t.Errorf("ledger: %+v", s.Ledger)
	}
	if !s.Backends.OK || s.Schedule.NextRun.IsZero() {
		t.Error("an unreadable ledger took the other facets with it")
	}
}

func TestStatusJSONSpendComesFromTheBackendsOwnFiles(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	cfg := config.Default()

	// Written by decide's own DailyCap, so a format change fails here.
	capf, err := decide.NewDailyCap(filepath.Join(stateDir, "budget", openRouterBudgetFile), cfg.Decide.DailyUSD.OpenRouter)
	if err != nil {
		t.Fatal(err)
	}
	if err := capf.Charge(context.Background(), openRouterSpendKey, 0.10); err != nil {
		t.Fatal(err)
	}
	// 0.4 settled + 0.1 retained + one 0.2 in flight = 0.7 committed.
	writeStateFile(t, stateDir, "budget/llm-"+today()+".json", `{
  "schema_version": 2, "spent_nano_usd": 400000000, "retained_nano_usd": 100000000,
  "settled": 3, "retained_attempts": 1, "overruns": 0, "next_id": 2,
  "in_flight": [{"id":1,"model":"m","state":"reserved","reserved_nano_usd":200000000,"pid":1,"started_unix":1}]
}`)
	s := statusOf(t)
	or, llm := openRouterItem(t, s), llmItem(t, s)
	if !or.OK || or.SpentUSD == nil || *or.SpentUSD < 0.0999 || *or.SpentUSD > 0.1001 || *or.RemainingUSD < 0.1499 || *or.RemainingUSD > 0.1501 || or.Exhausted {
		t.Errorf("openrouter: %+v", or)
	}
	if !llm.OK || llm.SpentUSD == nil || *llm.SpentUSD < 0.6999 || *llm.SpentUSD > 0.7001 || *llm.RemainingUSD < 0.2999 || *llm.RemainingUSD > 0.3001 || llm.Exhausted {
		t.Errorf("llm: %+v", llm)
	}

	// Past the cap: exhausted, remaining clamped at zero.
	if err := capf.Charge(context.Background(), openRouterSpendKey, 1); err != nil {
		t.Fatal(err)
	}
	or = openRouterItem(t, statusOf(t))
	if !or.Exhausted || or.RemainingUSD == nil || *or.RemainingUSD != 0 {
		t.Errorf("openrouter past its cap: %+v", or)
	}
}

func TestStatusJSONSpendOfAnotherDayDoesNotCount(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	writeStateFile(t, stateDir, "budget/"+openRouterBudgetFile,
		`{"version":1,"days":{"2001-01-01":{"jev":19},"`+today()+`":{"other":5}}}`)
	or := openRouterItem(t, statusOf(t))
	if !or.OK || or.SpentUSD == nil || *or.SpentUSD != 0 {
		t.Errorf("openrouter: %+v", or)
	}
}

// A spend file that cannot be read is unknown with the reason, never $0
// remaining-in-full; the other backend and every other facet still answer.
func TestStatusJSONUnreadableSpendIsUnknownNotZero(t *testing.T) {
	huge := `{"version":1,"days":{},"pad":"` + strings.Repeat("x", maxSpendFile) + `"}`
	for name, tc := range map[string]struct{ file, content string }{
		"corrupt":       {"budget/" + openRouterBudgetFile, "{nope"},
		"empty":         {"budget/" + openRouterBudgetFile, ""},
		"wrong version": {"budget/" + openRouterBudgetFile, `{"version":9,"days":{}}`},
		"trailing data": {"budget/" + openRouterBudgetFile, `{"version":1,"days":{}} {}`},
		"negative":      {"budget/" + openRouterBudgetFile, `{"version":1,"days":{"` + today() + `":{"jev":-1}}}`},
		"unknown field": {"budget/" + openRouterBudgetFile, `{"version":1,"days":{},"x":1}`},
		"oversized":     {"budget/" + openRouterBudgetFile, huge},
	} {
		t.Run(name, func(t *testing.T) {
			_, stateDir := setup(t)
			mustCode(t, 0, "init")
			writeStateFile(t, stateDir, tc.file, tc.content)
			s := statusOf(t)
			or := openRouterItem(t, s)
			if or.OK || or.Reason == "" || or.SpentUSD != nil || or.RemainingUSD != nil || or.Exhausted {
				t.Errorf("openrouter: %+v", or)
			}
			if or.CapUSD <= 0 {
				t.Errorf("the configured cap was lost: %+v", or)
			}
			if l := llmItem(t, s); !l.OK {
				t.Errorf("one bad spend file took the llm backend with it: %+v", l)
			}
			if !s.Ledger.OK || !s.Schedule.OK {
				t.Error("one bad spend file took other facets with it")
			}
		})
	}
}

const llmLedgerFixture = `{"schema_version":2,"spent_nano_usd":400000000,"settled":1,"next_id":1,"in_flight":[]}`

// A budget ledger whose lock is held (a wedged writer) must not hang status: the
// read gives up at statusTimeout and says so, and the other facets still answer.
func TestStatusJSONHeldBudgetLockIsBoundedAndUnknown(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	rel := "budget/llm-" + today() + ".json"
	writeStateFile(t, stateDir, rel, llmLedgerFixture)
	lock, err := os.OpenFile(filepath.Join(stateDir, rel+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	old := statusTimeout
	statusTimeout = 300 * time.Millisecond
	t.Cleanup(func() { statusTimeout = old })

	started := time.Now()
	s := statusOf(t)
	if took := time.Since(started); took > 3*time.Second {
		t.Errorf("status waited %v on a held lock", took)
	}
	l := llmItem(t, s)
	if l.OK || l.SpentUSD != nil || l.RemainingUSD != nil || !strings.Contains(l.Reason, "deadline") {
		t.Errorf("llm: %+v", l)
	}
	if o := openRouterItem(t, s); !o.OK {
		t.Errorf("openrouter: %+v", o)
	}
	if !s.Ledger.OK || !s.Schedule.OK {
		t.Error("a held budget lock took other facets with it")
	}
}

// Reading the status leaves every file where it was: the LLM ledger, its lock
// file and the spend file are not rewritten, and nothing is added.
func TestStatusJSONChangesNothingOnDisk(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	writeStateFile(t, stateDir, "budget/llm-"+today()+".json", llmLedgerFixture)
	writeStateFile(t, stateDir, "budget/llm-"+today()+".json.lock", "")
	writeStateFile(t, stateDir, "budget/"+openRouterBudgetFile, `{"version":1,"days":{}}`)
	writeStateFile(t, stateDir, "runs/last.json", `{"v":1,"started_at":"2026-10-18T15:00:00Z","outcome":"ok"}`)
	snapshot := func() map[string]string {
		out := map[string]string{}
		_ = filepath.WalkDir(stateDir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, _ := os.ReadFile(p)
			info, _ := d.Info()
			out[p] = info.ModTime().String() + string(b)
			return nil
		})
		return out
	}
	before := snapshot()
	statusOf(t)
	after := snapshot()
	if len(before) != len(after) {
		t.Fatalf("files before %d, after %d", len(before), len(after))
	}
	for p, v := range before {
		if after[p] != v {
			t.Errorf("%s changed", p)
		}
	}
}

func TestStatusJSONUnreadableLLMBudgetIsUnknown(t *testing.T) {
	for name, content := range map[string]string{
		"corrupt":       "{nope",
		"wrong schema":  `{"schema_version":1,"next_id":1,"in_flight":[]}`,
		"negative":      `{"schema_version":2,"spent_nano_usd":-5,"next_id":1,"in_flight":[]}`,
		"unknown field": `{"schema_version":2,"next_id":1,"in_flight":[],"x":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, stateDir := setup(t)
			mustCode(t, 0, "init")
			writeStateFile(t, stateDir, "budget/llm-"+today()+".json", content)
			s := statusOf(t)
			l := llmItem(t, s)
			if l.OK || l.Reason == "" || l.SpentUSD != nil || l.RemainingUSD != nil {
				t.Errorf("llm: %+v", l)
			}
			if o := openRouterItem(t, s); !o.OK {
				t.Errorf("openrouter: %+v", o)
			}
		})
	}
}

func TestStatusJSONWithABrokenConfigStillAnswersTheRest(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	writeStateFile(t, stateDir, "runs/last.json",
		`{"v":1,"pid":1,"started_at":"2026-10-18T15:00:00Z","finished_at":"2026-10-18T15:05:00Z","outcome":"failed","stage":"pipeline","error":"no decision backend is available"}`)
	if err := os.WriteFile(cfgPath, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := statusOf(t)
	if s.Config.OK || s.Config.Boards != nil || !strings.Contains(s.Config.Reason, "version") {
		t.Errorf("config: %+v", s.Config)
	}
	if s.Backends.OK || len(s.Backends.Items) != 0 || s.Backends.Reason == "" {
		t.Errorf("caps are unknown without a config: %+v", s.Backends)
	}
	if s.Schedule.OK || s.Decide.OK {
		t.Errorf("schedule %+v decide %+v", s.Schedule, s.Decide)
	}
	if s.LastRun.State != "failed" || s.LastRun.Error != "no decision backend is available" {
		t.Errorf("the run record is independent of the config: %+v", s.LastRun)
	}
}

func TestStatusJSONDecideSection(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Decide.Shadow = "local"
	cfg.Decide.Local = &config.Local{
		Server: &config.LocalServer{Binary: "/opt/homebrew/bin/llama-server", ModelPath: "/tmp/m.gguf"},
		Model:  "d1-3b",
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	d := statusOf(t).Decide
	if !d.OK || !d.LocalConfigured || d.LocalMode != "server" || d.LocalModel != "d1-3b" || d.Shadow != "local" {
		t.Errorf("decide: %+v", d)
	}
	// The binary and model paths are not part of the contract.
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), "llama-server") || strings.Contains(string(raw), "gguf") {
		t.Errorf("decide leaks local paths: %s", raw)
	}

	cfg.Decide.Local = &config.Local{Endpoint: "http://127.0.0.1:8080", Model: "d1-3b"}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	if d := statusOf(t).Decide; d.LocalMode != "endpoint" || !d.LocalConfigured {
		t.Errorf("decide: %+v", d)
	}
}

func TestStatusJSONRejectsStrayArguments(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 2, "status", "--json", "extra")
	mustCode(t, 2, "status", "--nope")
}

// The text status is unchanged by --json existing.
func TestTextStatusStillCountsByStatus(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	wire(t, fixture(), &fakeNotifier{}, nil)
	o, _ := mustCode(t, 0, "status")
	for _, want := range []string{"postings: 4", "explained  1", "dropped    1"} {
		if !strings.Contains(o, want) {
			t.Errorf("status missing %q:\n%s", want, o)
		}
	}
}

// --- the run record ---

func TestRunOnceRecordsSuccessWithLedgerDelta(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := fixture()
	wire(t, l, &fakeNotifier{}, nil)
	mustCode(t, 0, "run-once")

	r, err := readRunRecord(stateDir)
	if err != nil || r == nil {
		t.Fatalf("record: %v %v", r, err)
	}
	if r.Outcome != outcomeOK || r.Stage != "" || r.Error != "" || r.FinishedAt.IsZero() || r.PID != os.Getpid() {
		t.Errorf("record: %+v", r)
	}
	if r.CountsBefore["explained"] != 1 || r.CountsBefore["unsure"] != 1 {
		t.Errorf("before: %v", r.CountsBefore)
	}
	// publishDigest marked the two kept roles digested.
	if r.CountsAfter["digested"] != 2 || r.CountsAfter["explained"] != 0 {
		t.Errorf("after: %v", r.CountsAfter)
	}
	s := statusOf(t)
	if s.LastRun.State != "ok" || s.LastRun.PostingsBefore == nil || *s.LastRun.PostingsBefore != 4 || *s.LastRun.PostingsAfter != 4 || s.LastRun.DurationSec == nil {
		t.Errorf("status last run: %+v", s.LastRun)
	}
}

func TestRunOnceRecordsAFailureAndItsStage(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	wire(t, fixture(), &fakeNotifier{}, errors.New("no decision backend read any posting:\n20 of 20 unread"))
	mustCode(t, 1, "run-once")

	r, err := readRunRecord(stateDir)
	if err != nil || r == nil {
		t.Fatalf("record: %v %v", r, err)
	}
	if r.Outcome != outcomeFailed || r.Stage != stagePipeline || r.FinishedAt.IsZero() {
		t.Errorf("record: %+v", r)
	}
	if strings.Contains(r.Error, "\n") || !strings.Contains(r.Error, "20 of 20 unread") {
		t.Errorf("error should be one line: %q", r.Error)
	}
	if got := statusOf(t).LastRun; got.State != "failed" || got.Stage != "pipeline" || got.Error != r.Error {
		t.Errorf("status last run: %+v", got)
	}
}

func TestRunOnceRecordsADigestFailureAtTheDigestStage(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := fixture()
	l.fail = true // the pipeline passes; marking roles digested fails
	wire(t, l, &fakeNotifier{}, nil)
	mustCode(t, 1, "run-once")
	r, _ := readRunRecord(stateDir)
	if r == nil || r.Outcome != outcomeFailed || r.Stage != stageDigest || !strings.Contains(r.Error, "disk full") {
		t.Errorf("record: %+v", r)
	}
}

func TestRunOnceRecordsACancelledRun(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	wire(t, fixture(), &fakeNotifier{}, context.Canceled)
	mustCode(t, 1, "run-once")
	r, _ := readRunRecord(stateDir)
	if r == nil || r.Outcome != outcomeCancelled || r.Stage != stagePipeline {
		t.Errorf("record: %+v", r)
	}
}

func TestRunRecordIsWrittenBeforeTheRunEnds(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := fixture()
	wire(t, l, &fakeNotifier{}, nil)
	var during *runRecord
	old := buildApp // wire's cleanup restores the original after ours runs
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		return &App{Ledger: l, Notifier: &fakeNotifier{}, RunOnce: func(context.Context) error {
			during, _ = readRunRecord(stateDir)
			return nil
		}}, nil
	}
	t.Cleanup(func() { buildApp = old })
	mustCode(t, 0, "run-once")
	if during == nil || during.Outcome != outcomeRunning || !during.FinishedAt.IsZero() || during.PID != os.Getpid() {
		t.Fatalf("record during the run: %+v", during)
	}
	if during.CountsBefore["explained"] != 1 {
		t.Errorf("counts before: %v", during.CountsBefore)
	}
}

// A run whose process died leaves a record with no end; status must not call
// it healthy or running.
func TestStatusJSONRunningInterruptedAndStale(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	old := pidAlive
	t.Cleanup(func() { pidAlive = old })

	started := time.Now().UTC().Add(-time.Minute)
	rec := runRecord{PID: 4242, StartedAt: started, Outcome: outcomeRunning}
	if err := writeRunRecord(stateDir, rec); err != nil {
		t.Fatal(err)
	}
	pidAlive = func(int) bool { return true }
	if st := statusOf(t).LastRun; st.State != "running" || !st.FinishedAt.IsZero() || st.DurationSec != nil {
		t.Errorf("live process: %+v", st)
	}
	pidAlive = func(int) bool { return false }
	if st := statusOf(t).LastRun; st.State != "interrupted" {
		t.Errorf("dead process: %+v", st)
	}
	// A reused pid does not keep a day-old record "running".
	pidAlive = func(int) bool { return true }
	rec.StartedAt = started.Add(-2 * maxRunAge)
	if err := writeRunRecord(stateDir, rec); err != nil {
		t.Fatal(err)
	}
	if st := statusOf(t).LastRun; st.State != "interrupted" {
		t.Errorf("stale record: %+v", st)
	}
}

func TestStatusJSONUnreadableRunRecordIsUnknown(t *testing.T) {
	for name, content := range map[string]string{
		"corrupt":        "{nope",
		"empty":          "",
		"wrong version":  `{"v":7,"started_at":"2026-10-18T15:00:00Z","outcome":"ok"}`,
		"bad outcome":    `{"v":1,"started_at":"2026-10-18T15:00:00Z","outcome":"great"}`,
		"no start":       `{"v":1,"outcome":"ok"}`,
		"trailing data":  `{"v":1,"started_at":"2026-10-18T15:00:00Z","outcome":"ok"} {}`,
		"oversized":      `{"v":1,"started_at":"2026-10-18T15:00:00Z","outcome":"ok","error":"` + strings.Repeat("x", maxRunRecord) + `"}`,
		"missing fields": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, stateDir := setup(t)
			mustCode(t, 0, "init")
			writeStateFile(t, stateDir, "runs/last.json", content)
			s := statusOf(t)
			if s.LastRun.OK || s.LastRun.State != "unknown" || s.LastRun.Reason == "" {
				t.Errorf("last run: %+v", s.LastRun)
			}
			if !s.Backends.OK {
				t.Error("an unreadable run record took other facets with it")
			}
		})
	}
}

// Not being able to write the record must not fail the run: its work is in the
// ledger. The failure is on stderr.
func TestRunRecordWriteFailureDoesNotFailTheRun(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	wire(t, fixture(), &fakeNotifier{}, nil)
	writeStateFile(t, stateDir, "runs", "a file where the runs directory should be")
	_, e := mustCode(t, 0, "run-once")
	if !strings.Contains(e, "could not record the run") {
		t.Errorf("stderr %q", e)
	}
}

func TestRunErrorTextIsBounded(t *testing.T) {
	long := errors.New("boom\n" + strings.Repeat("é", 5000) + "\x00\xff")
	got := runErrorText(long)
	if n := len([]rune(got)); n > maxRunErrorRunes+1 || strings.ContainsAny(got, "\n\x00") {
		t.Errorf("error text: %d runes, %q", n, got[:20])
	}
}

func TestRunRecordIsPrivateAndReplacedWhole(t *testing.T) {
	_, stateDir := setup(t)
	for i := 0; i < 3; i++ {
		rec := runRecord{PID: i + 1, StartedAt: time.Now(), Outcome: outcomeOK}
		if err := writeRunRecord(stateDir, rec); err != nil {
			t.Fatal(err)
		}
	}
	st, err := os.Stat(runRecordPath(stateDir))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("record mode: %v %v", st, err)
	}
	ents, _ := os.ReadDir(filepath.Dir(runRecordPath(stateDir)))
	if len(ents) != 1 {
		t.Errorf("temp files left behind: %v", ents)
	}
	r, err := readRunRecord(stateDir)
	if err != nil || r.PID != 3 {
		t.Errorf("record: %+v %v", r, err)
	}
}
