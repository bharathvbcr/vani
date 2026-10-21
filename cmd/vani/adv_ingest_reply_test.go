package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isIngestReplyGo mirrors extension/src/ingest.ts isIngestReply and the first
// branch of describeIngest (a non-empty "error" is a failure, never "queued").
func isIngestReplyGo(t *testing.T, raw string) (search map[string]any) {
	t.Helper()
	var r map[string]any
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, raw)
	}
	count := func(v any) bool { f, ok := v.(float64); return ok && !math.IsInf(f, 0) && f >= 0 }
	if e, ok := r["error"].(string); ok && e != "" {
		t.Errorf("reply carries error %q: the extension reports a failure", e)
	}
	if !count(r["queued"]) {
		t.Errorf("queued %v is not a count", r["queued"])
	}
	if v, ok := r["duplicates"]; ok && !count(v) {
		t.Errorf("duplicates %v", v)
	}
	if v, ok := r["rejected"]; ok {
		if _, isArr := v.([]any); !isArr {
			t.Errorf("rejected %v is not an array (null fails Array.isArray)", v)
		}
	}
	if v, ok := r["analysis"]; ok {
		if _, isStr := v.(string); !isStr {
			t.Errorf("analysis %v", v)
		}
	}
	s, ok := r["search"].(map[string]any)
	if !ok {
		t.Fatalf("search missing or not an object: %v", r["search"])
	}
	if _, isStr := s["status"].(string); !isStr {
		t.Errorf("search.status %v is not a string", s["status"])
	}
	return s
}

func TestAdvIngestSearchErrorReplyShape(t *testing.T) {
	// No config: loadConfig fails.
	_, _ = setup(t)
	_, stdout, _ := ingestCLI(t, 0, ingestPayload(t, "https://careers.acme.com/search", true, ingestJobs("careers.acme.com", 2)))
	s := isIngestReplyGo(t, stdout)
	if s["status"] != "error" || s["reason"] == "" || s["reason"] == nil {
		t.Errorf("search %v, want status error with a reason", s)
	}
	if strings.Contains(stdout, `"error":`) {
		t.Errorf("top-level error key present: %s", stdout)
	}
}

// The config exists but cannot be rewritten (its lock is held, as by a long
// `config` edit): the scrape still spools, exit 0, search.status error.
func TestAdvIngestConfigWriteFailureKeepsTheScrape(t *testing.T) {
	cfgPath, _ := setup(t)
	stateDir := filepath.Join(t.TempDir(), "state") // outside the read-only config dir
	t.Setenv("VANI_STATE_DIR", stateDir)
	mustCode(t, 0, "init")
	if err := os.Chmod(filepath.Dir(cfgPath), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(cfgPath), 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	out, stdout, _ := ingestCLI(t, 0, ingestPayload(t, "https://boards.greenhouse.io/acme", true, ingestJobs("boards.greenhouse.io", 2)))
	s := isIngestReplyGo(t, stdout)
	if s["status"] != "error" {
		t.Errorf("search %v, want error", s)
	}
	if n := len(ledgerRecords(t, stateDir)) + spooled(t, stateDir); out.Queued != 2 || n == 0 {
		t.Errorf("jobs dropped: queued %d, stored %d", out.Queued, n)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Error(err)
	}
}
