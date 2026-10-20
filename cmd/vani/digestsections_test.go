package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// The config's bounds are the digest's own: a value the config accepts is never
// silently clamped by the digest.
func TestDigestKnobBoundsMatchTheDigest(t *testing.T) {
	if config.MaxDigestTopOpen != digest.MaxTopOpen || config.MaxDigestExplore != digest.MaxExplore {
		t.Fatalf("config bounds %d/%d, digest %d/%d", config.MaxDigestTopOpen, config.MaxDigestExplore, digest.MaxTopOpen, digest.MaxExplore)
	}
}

func TestDigestOptionsCarryTheSectionKnobs(t *testing.T) {
	o, _ := digestOptions(config.Config{}, t.TempDir())
	if o.TopOpen != 20 || o.ExploreBelowFold != 2 || o.ExploreDropped != 1 {
		t.Fatalf("defaults: top %d, explore %d+%d, want 20, 2+1", o.TopOpen, o.ExploreBelowFold, o.ExploreDropped)
	}
	zero, five, three := 0, 5, 3
	o, _ = digestOptions(config.Config{Digest: config.Digest{TopOpen: &zero, ExploreBelowFold: &five, ExploreDropped: &three}}, t.TempDir())
	if o.TopOpen != 0 || o.ExploreBelowFold != 5 || o.ExploreDropped != 3 {
		t.Fatalf("configured: top %d, explore %d+%d, want 0, 5+3", o.TopOpen, o.ExploreBelowFold, o.ExploreDropped)
	}
}

func sectionsLedger() *memLedger {
	at := time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)
	l := fixture()
	l.recs["gh:a:1"].Annotations = map[string]string{ledger.AnnRank: "70", ledger.AnnCoverage: "60"}
	add := func(k string, st posting.Status) {
		l.recs[posting.Key(k)] = &ledger.Record{
			Key: posting.Key(k), Status: st, FirstSeen: at, LastEvent: at,
			Posting:     posting.Posting{Company: fmt.Sprintf("Co%s", k), Title: "Role " + k, URL: "https://x.example/" + k},
			Annotations: map[string]string{ledger.AnnRank: "60", ledger.AnnCoverage: "60", "triage.backend": "title"},
		}
	}
	for i := 0; i < 25; i++ {
		add(fmt.Sprintf("gh:d:%02d", i), posting.Digested)
	}
	for i := 0; i < 4; i++ {
		add(fmt.Sprintf("gh:x:%02d", i), posting.Dropped)
	}
	return l
}

func runWith(t *testing.T, l *memLedger, adv *advanceReport) string {
	t.Helper()
	stateDir := func() string { _, s := setup(t); return s }()
	mustCode(t, 0, "init")
	old, oldNow := buildApp, now
	t.Cleanup(func() { buildApp, now = old, oldNow })
	now = func() time.Time { return time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC) }
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		return &App{
			Ledger: l, Notifier: &fakeNotifier{},
			RunOnce:   func(context.Context) error { return nil },
			RunReport: func() runDetail { return runDetail{Advance: adv} },
			Reload:    func([]sources.Board, []string) {},
		}, nil
	}
	mustCode(t, 0, "run-once")
	data, err := os.ReadFile(filepath.Join(stateDir, "digests", "2026-10-18.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The production path (run-once with a default config) renders the new roles
// by priority, Top open by relevance and Explore with its sampling numbers.
func TestRunOnceDigestRendersTopOpenAndExplore(t *testing.T) {
	md := runWith(t, sectionsLedger(), nil)
	for _, want := range []string{"## Ranked roles", "## Top open (20)", "## Explore (3)", "stratum `explore:below_fold` · p_select 0.4", "stratum `explore:dropped` · p_select 0.2"} {
		if !strings.Contains(md, want) {
			t.Fatalf("digest lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "could not check") {
		t.Fatalf("no cause, no line:\n%s", md)
	}
}

func TestRunOnceDigestFooterNamesFailOpenCauses(t *testing.T) {
	adv := &advanceReport{FailOpen: pipeline.FailOpenCounts{EmptyDescription: 4, PrescoreFailed: 1}}
	md := runWith(t, sectionsLedger(), adv)
	if !strings.Contains(md, "could not check: 4 empty description, 1 prescore failed\n") {
		t.Fatalf("footer:\n%s", md)
	}
	if strings.Contains(md, "title unread") || strings.Contains(md, "location unreadable") {
		t.Fatalf("a zero cause printed:\n%s", md)
	}
}
