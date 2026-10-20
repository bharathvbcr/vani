package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/egress"
)

// The OpenRouter cap file and spend key keep their pre-rename names. decide's
// DailyCap re-reads its file on every call, treats a missing file as nothing
// spent, and takes no lock across processes, so a rename could not be made
// safe: a process still holding the old path (a running daemon, an older
// binary) would start the day again at zero, which is a second full cap.
// Changing either name is a fail-open bug, not a cleanup.
func TestOpenRouterCapFileKeepsItsName(t *testing.T) {
	if openRouterBudgetFile != "jev.json" || openRouterSpendKey != "jev" {
		t.Fatalf("cap file %q, spend key %q: renaming either resets the day's OpenRouter spend", openRouterBudgetFile, openRouterSpendKey)
	}
}

// decisionModelServer answers every kbmatch question, charging cost each time.
func decisionModelServer(t *testing.T, cost float64) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		_, _ = io.WriteString(w, `{"answers":{"kb_match":{"choice":"core","confidence":0.9}},"usage":{"cost":`+strconv.FormatFloat(cost, 'f', -1, 64)+`}}`)
	}))
	t.Cleanup(srv.Close)
	oldE, oldD := kbmatchEndpoint, kbmatchDoer
	t.Cleanup(func() { kbmatchEndpoint, kbmatchDoer = oldE, oldD })
	kbmatchEndpoint = srv.URL
	kbmatchDoer = func(config.Config) (egress.Doer, error) { return egress.NewLoopback(5*time.Second, 0) }
	return &calls
}

func openRouterKBMatch(t *testing.T) *kbmEnv {
	t.Helper()
	k := twoPostings(t)
	clearKeys(t)
	t.Setenv("OPENROUTER_API_KEY", "test-not-a-real-key")
	k.cfg.LLM = config.LLM{}
	k.cfg.Decide.DailyUSD.LLM = 0
	k.cfg.Decide.OpenRouterModel = "liquid/d1-ultra"
	k.cfg.Decide.DailyUSD.OpenRouter = 1
	k.cfg.KBMatch.Backend = config.KBMatchOpenRouter
	k.save(t)
	return k
}

// A day's spend recorded in budget/jev.json before the rename still counts: at
// the cap, the decision model is not asked at all.
func TestOpenRouterSpendBeforeTheRenameStillCounts(t *testing.T) {
	k := openRouterKBMatch(t)
	calls := decisionModelServer(t, 0.01)
	spent := `{"version":1,"days":{"` + today() + `":{"jev":1}}}`
	writeStateFile(t, k.state, "budget/jev.json", spent)

	_, _, _ = do(t, "kbmatch", "run")
	if n := calls.Load(); n != 0 {
		t.Fatalf("the decision model was asked %d times with the day's cap already spent", n)
	}
	raw, err := os.ReadFile(filepath.Join(k.state, "budget", "jev.json"))
	if err != nil || strings.TrimSpace(string(raw)) != spent {
		t.Errorf("the spent cap file changed: %v\n%s", err, raw)
	}
	assertOnlyJevCapFile(t, k.state)
}

// With no cap file yet, the first charge creates budget/jev.json under key
// "jev", the file every OpenRouter client (triage, notes, suggest, kbmatch)
// and `vani status` read.
func TestOpenRouterSpendStartsInTheJevFile(t *testing.T) {
	k := openRouterKBMatch(t)
	calls := decisionModelServer(t, 0.01)

	mustCode(t, 0, "kbmatch", "run")
	if n := calls.Load(); n != 2 {
		t.Fatalf("asked %d times, want 2", n)
	}
	got, err := readOpenRouterSpend(filepath.Join(k.state, "budget", openRouterBudgetFile), today(), openRouterSpendKey)
	if err != nil || got < 0.019 || got > 0.021 {
		t.Errorf("spend in the jev file %v (%v), want 0.02", got, err)
	}
	assertOnlyJevCapFile(t, k.state)
}

func assertOnlyJevCapFile(t *testing.T, stateDir string) {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(stateDir, "budget"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "openrouter") || strings.Contains(e.Name(), "liquid") || strings.Contains(e.Name(), "d1") {
			t.Errorf("a second OpenRouter cap file %s appeared", e.Name())
		}
	}
}
