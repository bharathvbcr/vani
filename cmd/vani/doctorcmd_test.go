package main

import (
	"context"
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
	"github.com/bharathvbcr/Vani/internal/sources"
)

type doctorStream struct {
	text string
	done bool
}

func (s *doctorStream) Next() (llm.Chunk, error) {
	if !s.done {
		s.done = true
		return llm.Chunk{Kind: llm.ChunkText, Text: s.text}, nil
	}
	return llm.Chunk{}, io.EOF
}
func (s *doctorStream) Response() (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock{Text: s.text}}}, StopReason: llm.StopEndTurn}, nil
}
func (s *doctorStream) Close() error { return nil }

// doctorFake answers each part with a fixed reply and keeps what was sent.
type doctorFake struct {
	boards, drops string
	sent          []string
}

func (f *doctorFake) Name() string { return "fake" }
func (f *doctorFake) Capability(model string) (llm.Capability, bool) {
	return llm.Capability{Provider: "fake", Model: model, MaxOutputTokens: 64000}, true
}
func (f *doctorFake) Stream(_ context.Context, req llm.Request) (llm.Stream, error) {
	f.sent = append(f.sent, req.System+"\n"+req.Messages[0].Text())
	if strings.Contains(req.System, "audit the drop decisions") {
		return &doctorStream{text: f.drops}, nil
	}
	return &doctorStream{text: f.boards}, nil
}

const doctorSponsorTitle = "Engineer Needing Sponsorship Review"

// doctorSetup writes a config (with a sponsorship drop rule), three runs in
// which gh:acme read nothing, their logs (naming the home directory), and a
// ledger of dropped postings, one of them dropped on sponsorship.
func doctorSetup(t *testing.T) (state string, home string) {
	t.Helper()
	cfgPath, state := setup(t)
	fixedNow(t)
	home = t.TempDir()
	t.Setenv("HOME", home)
	cfg := config.Default()
	cfg.TargetSummary = "early-career ML systems roles"
	cfg.LLM = config.LLM{Provider: "anthropic", Model: "claude-test", InputNanoUSDPerToken: 3000, OutputNanoUSDPerToken: 15000}
	cfg.Decide.DailyUSD.LLM = 5
	cfg.Watchlist = []sources.Board{{ATS: "gh", Token: "acme", Company: "Acme"}, {ATS: "gh", Token: "quiet", Company: "Quiet"}}
	cfg.DropRules = []config.DropRule{
		{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9},
		{Slot: "sponsorship", Values: []string{"no_sponsorship"}, MinOpenRouterProb: 0.9},
	}
	cfg.AllowedHosts = append(cfg.AllowedHosts, "boards-api.greenhouse.io")
	writeConfig(t, cfgPath, cfg)

	zero := 0
	for i := range 3 {
		r := runRecord{V: 1, PID: 100 + i, StartedAt: time.Date(2026, 11, 10+i, 9, 0, 0, 0, time.UTC), Outcome: "ok"}
		// gh:quiet was read but the record's size bound left it unnamed.
		r.Boards = &boardsReport{Configured: 2, Read: 2, Unrecorded: 1, Items: []boardReport{{ATS: "gh", Token: "acme", Company: "Acme", Outcome: boardRead, Listed: &zero}}}
		if err := writeRunHistory(state, r); err != nil {
			t.Fatal(err)
		}
		log := "vani: board gh/acme: details {Listed:0 Requests:1 Prefiltered:0}\n" +
			"vani: board gh/acme: failed reading " + filepath.Join(home, "Library", "x.json") + ": permission denied\n" +
			"vani: board gh/acme: {New:0 Unchanged:0 Missed:0 Dead:0}\n" +
			"vani: board gh/quiet: {New:1 Unchanged:11 Missed:0 Dead:0}\n"
		if err := os.WriteFile(filepath.Join(state, runRecordDir, runHistoryDir, runID(r)+".log"), []byte(log), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	add := func(key, title string, ann map[string]string) {
		p := posting.Posting{Key: posting.Key(key), ATS: "gh", Company: "Acme", Title: title, Description: "Build things for " + title}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Dropped, Posting: &p, Annotations: ann}); err != nil {
			t.Fatal(err)
		}
	}
	add("gh:acme:1", "Account Executive", map[string]string{"triage.backend": "title", "triage.role_fit": "mismatch_domain"})
	add("gh:acme:2", "Welder", map[string]string{"triage.backend": "d1-ultra", "triage.role_fit": "mismatch_domain", "triage.role_fit.p": "0.97"})
	add("gh:acme:3", doctorSponsorTitle, map[string]string{"triage.backend": "d1-ultra", "triage.sponsorship": "no_sponsorship", "triage.sponsorship.p": "0.95"})
	return state, home
}

// doctorSeams replaces the provider and the spend reader; built reports
// whether a provider was asked for.
func doctorSeams(t *testing.T, f *doctorFake, spent int64) (built *bool) {
	t.Helper()
	b := false
	oldP, oldS := doctorProvider, doctorSpent
	doctorProvider = func(context.Context, *wired) (llm.Provider, error) { b = true; return f, nil }
	doctorSpent = func(context.Context, string) (int64, error) { return spent, nil }
	t.Cleanup(func() { doctorProvider, doctorSpent = oldP, oldS })
	return &b
}

func TestDoctorDryRunSendsAndWritesNothing(t *testing.T) {
	state, home := doctorSetup(t)
	built := doctorSeams(t, &doctorFake{}, 0)
	out, _ := mustCode(t, 0, "doctor", "--dry-run", "--show")
	if *built {
		t.Fatal("a dry run built a provider")
	}
	if _, err := os.Stat(filepath.Join(state, doctorReportDir)); !os.IsNotExist(err) {
		t.Fatalf("a dry run wrote a report: %v", err)
	}
	// gh:quiet, unnamed by every record, is read back from the logs (3 runs),
	// so it is not reported as a board no run reached; gh:acme, which the
	// records name, is not read back twice.
	if strings.Contains(out, `"board":"gh:quiet"`) {
		t.Errorf("a board recovered from the logs was signalled:\n%s", out)
	}
	for _, want := range []string{"3 board reads the records left unnamed recovered from run logs", "boards tripped a signal", "dropped postings sampled", "worst case", "zero_every_run", "Listed:0 Requests:1 Prefiltered:0"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	doctorNoPersonal(t, out, home)
}

// doctorNoPersonal fails when sent text carries anything about work
// authorization or names the owner's home directory or user name.
func doctorNoPersonal(t *testing.T, sent, home string) {
	t.Helper()
	low := strings.ToLower(sent)
	for _, bad := range []string{"sponsor", strings.ToLower(home), strings.ToLower(filepath.Base(home))} {
		if strings.Contains(low, bad) {
			t.Errorf("sent text carries %q", bad)
		}
	}
	if !strings.Contains(sent, "~") {
		t.Error("the home path was not folded to ~")
	}
}

func TestDoctorRefusesBeforeBuildingAProvider(t *testing.T) {
	doctorSetup(t)
	built := doctorSeams(t, &doctorFake{}, 0)
	mustCode(t, 2, "doctor") // no --budget-usd
	mustCode(t, 2, "doctor", "--budget-usd", "1000")
	mustCode(t, 2, "doctor", "--show", "--budget-usd", "5")
	mustCode(t, 2, "doctor", "--dry-run", "--sample", "999")
	if _, e := mustCode(t, 1, "doctor", "--budget-usd", "0.01"); !strings.Contains(e, "over --budget-usd") {
		t.Errorf("stderr %q", e)
	}
	*built = false
	doctorSeams(t, &doctorFake{}, 4_900_000_000) // $4.90 of the $5 cap spent
	if _, e := mustCode(t, 1, "doctor", "--budget-usd", "5"); !strings.Contains(e, "headroom") {
		t.Errorf("stderr %q", e)
	}
	if *built {
		t.Fatal("a refused run built a provider")
	}
}

func TestDoctorRealRunWritesTheReport(t *testing.T) {
	state, home := doctorSetup(t)
	f := &doctorFake{boards: `{"boards":[],"patterns":[]}`, drops: `{"drops":[],"patterns":[]}`}
	doctorSeams(t, f, 0)
	out, _ := mustCode(t, 0, "doctor", "--budget-usd", "5")
	if len(f.sent) != 2 {
		t.Fatalf("%d requests", len(f.sent))
	}
	doctorNoPersonal(t, strings.Join(f.sent, "\n"), home)
	if strings.Contains(strings.Join(f.sent, "\n"), doctorSponsorTitle) {
		t.Fatal("a posting dropped on sponsorship was sampled")
	}
	mds, _ := filepath.Glob(filepath.Join(state, doctorReportDir, "*.md"))
	jss, _ := filepath.Glob(filepath.Join(state, doctorReportDir, "*.json"))
	if len(mds) != 1 || len(jss) != 1 || !strings.Contains(out, "report written to") {
		t.Fatalf("reports %v %v\n%s", mds, jss, out)
	}
	md, _ := os.ReadFile(mds[0])
	// Nothing was judged, so everything signalled is named as not examined.
	if !strings.Contains(string(md), "Not examined (signalled, but not judged") || !strings.Contains(string(md), "gh:acme") {
		t.Fatalf("report:\n%s", md)
	}
}

// A part that fails exits non-zero, and the report is still written with the failure in it.
func TestDoctorFailedPartFailsTheCommand(t *testing.T) {
	state, _ := doctorSetup(t)
	doctorSeams(t, &doctorFake{boards: `{"boards":[],"patterns":[]}`, drops: "I will not answer in JSON."}, 0)
	_, e := mustCode(t, 1, "doctor", "--budget-usd", "5")
	if !strings.Contains(e, "drops: doctor: the reply holds no JSON object") {
		t.Fatalf("stderr %q", e)
	}
	mds, _ := filepath.Glob(filepath.Join(state, doctorReportDir, "*.md"))
	if len(mds) != 1 {
		t.Fatalf("reports %v", mds)
	}
	if md, _ := os.ReadFile(mds[0]); !strings.Contains(string(md), "The drop review did not complete") {
		t.Fatalf("report hides the failure:\n%s", md)
	}
}

func TestReadTailBounds(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := readTail(filepath.Join(dir, "missing.log"), 10); err == nil {
		t.Error("a missing log read")
	}
	if _, _, err := readTail(dir, 10); err == nil {
		t.Error("a directory read as a log")
	}
	p := filepath.Join(dir, "big.log")
	if err := os.WriteFile(p, []byte("0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, cut, err := readTail(p, 6)
	if err != nil || !cut || string(b) != "abcdef" {
		t.Fatalf("%q %v %v", b, cut, err)
	}
	b, cut, err = readTail(p, 100)
	if err != nil || cut || len(b) != 16 {
		t.Fatalf("%q %v %v", b, cut, err)
	}
	if err := os.Symlink(p, filepath.Join(dir, "link.log")); err == nil {
		if _, _, err := readTail(filepath.Join(dir, "link.log"), 10); err == nil || errors.Is(err, os.ErrNotExist) {
			// atomicfile.OpenRead refuses a symlinked leaf; either way it is not read as the log.
			if err == nil {
				t.Error("a symlink read as a log")
			}
		}
	}
}

func TestDoctorRedactorFoldsHomeAndUser(t *testing.T) {
	t.Setenv("HOME", "/Users/someone")
	r := doctorRedactor()
	got := r("open /Users/someone/Library/x: denied; SomeOne's access_token=abc123")
	if strings.Contains(strings.ToLower(got), "someone") || !strings.Contains(got, "~/Library/x") || strings.Contains(got, "abc123") {
		t.Fatalf("%q", got)
	}
}
