package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// watchGreenhouse puts the Greenhouse board token on cfgPath's watchlist (and
// its API host on allowed_hosts), as a real drop's board is: a reopen leaves
// a drop on an unwatched board dropped.
func watchGreenhouse(t *testing.T, cfgPath, token string) {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Watchlist = append(cfg.Watchlist, sources.Board{ATS: posting.Greenhouse, Token: token, Company: "Acme"})
	if !slices.Contains(cfg.AllowedHosts, "boards-api.greenhouse.io") {
		cfg.AllowedHosts = append(cfg.AllowedHosts, "boards-api.greenhouse.io")
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
}

// A title drop the rules no longer make, on a board no longer on the
// watchlist, is left dropped and named: reopened, it would never age, since
// no listing of its board runs. Both modes report it, in text and in JSON.
func TestRetriageLeavesAnUnwatchedBoardsDropAndSaysSo(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	p := posting.Posting{Key: "gh:gone:1", ATS: "gh", Company: "Gone", Title: "Staff Engineer", Description: "jd"}
	ann := map[string]string{"triage.backend": "title", "triage.seniority": "staff_plus", "triage.seniority.reading": "answered"}
	if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Dropped, Posting: &p, Annotations: ann}); err != nil {
		t.Fatal(err)
	}
	l.Close()
	for _, flag := range []string{"--title-drops", "--policy-drops"} {
		o, _ := mustCode(t, 0, "retriage", "--dry-run", flag)
		if !strings.Contains(o, "0 posting(s) would return") || !strings.Contains(o, "left dropped, on a board no longer watched: 1") {
			t.Errorf("%s:\n%s", flag, o)
		}
		o, _ = mustCode(t, 0, "--json", "retriage", "--dry-run", flag)
		var rt retriageRunReply
		decodeOne(t, o, &rt)
		if rt.Returned != 0 || rt.Stale == nil || *rt.Stale != 1 {
			t.Errorf("%s json: %s", flag, o)
		}
	}
	mustCode(t, 0, "retriage", "--policy-drops")
	snap, err := ledger.ReadSnapshot(filepath.Join(stateDir, "ledger"))
	if err != nil || len(snap.Records) != 1 || snap.Records[0].Status != posting.Dropped {
		t.Fatalf("after: %+v (%v)", snap.Records, err)
	}
}

// A count that is zero is still reported, so "nothing left dropped" is told
// apart from a binary that does not count it (review finding, 2026-11-12). It
// failed on 9384225, whose unjudged count was omitted at zero.
func TestRetriageJSONReportsLeftDroppedCountsAtZero(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	for _, c := range []struct {
		flag string
		want []string
	}{
		{"--policy-drops", []string{"unjudged", "stale", "unreadable"}},
		{"--title-drops", []string{"stale", "unreadable"}},
	} {
		o, _ := mustCode(t, 0, "--json", "retriage", "--dry-run", c.flag)
		var m map[string]json.RawMessage
		decodeOne(t, o, &m)
		for _, k := range c.want {
			if v, ok := m[k]; !ok || string(v) != "0" {
				t.Errorf("%s: %q = %s (present %v), want 0 present:\n%s", c.flag, k, v, ok, o)
			}
		}
	}
}
