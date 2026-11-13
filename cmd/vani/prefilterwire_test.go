package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func prefilterPolicy() pipeline.DropPolicy {
	x := config.Exclude{Titles: []string{"intern"}, Locations: []string{"France"}}
	return pipeline.DropPolicy{Exclude: x.Match}
}

// The DropListed the adapters take notes each row it drops, with the rule, in
// the run's sample and counts, and writes the sample once when the run saves it.
func TestListedDropsFeedTheSampleAndTheRunCounts(t *testing.T) {
	state := t.TempDir()
	var warn bytes.Buffer
	w := &wired{stateDir: state, warn: &warn}
	w.openPrefilterSample()
	drop := w.listedDrops(prefilterPolicy())
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			drop(posting.Posting{ATS: posting.Workday, Company: "Acme", Title: "Software Intern", Location: "Austin, TX"})
			drop(posting.Posting{ATS: posting.Workday, Company: "Acme", Title: "Engineer", Location: "Paris, France"})
			if drop(posting.Posting{ATS: posting.Workday, Company: "Acme", Title: "Engineer", Location: "Austin, TX"}) {
				t.Error("a posting no rule matches was dropped")
			}
		}()
	}
	wg.Wait()
	w.mu.Lock()
	counts := w.prefilterRulesLocked()
	w.mu.Unlock()
	if counts["exclude_title"] != 4 || counts["exclude_location"] != 4 || len(counts) != 2 {
		t.Errorf("counts = %v", counts)
	}
	path := filepath.Join(state, "audit", "prefilter-sample.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the sample was written before the run saved it: %v", err)
	}
	w.savePrefilterSample()
	s, err := pipeline.LoadPrefilterSample(path, 50)
	if err != nil {
		t.Fatal(err)
	}
	rows, seen := s.Week(eval.WeekSeed(now()))
	if len(rows) != 2 || seen != 8 {
		t.Fatalf("rows %d seen %d, want the two distinct rows of 8 reports: %+v", len(rows), seen, rows)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Rule+"|"+r.Title+"|"+r.Location] = true
	}
	if !got["exclude_title|Software Intern|Austin, TX"] || !got["exclude_location|Engineer|Paris, France"] {
		t.Errorf("rows = %v", got)
	}
	// A second run adds to the file and does not repeat a row.
	w2 := &wired{stateDir: state, warn: &warn}
	w2.openPrefilterSample()
	w2.listedDrops(prefilterPolicy())(posting.Posting{ATS: posting.Workday, Company: "Acme", Title: "Software Intern", Location: "Austin, TX"})
	w2.savePrefilterSample()
	s, _ = pipeline.LoadPrefilterSample(path, 50)
	if rows, seen := s.Week(eval.WeekSeed(now())); len(rows) != 2 || seen != 9 {
		t.Errorf("after a second run rows %d seen %d", len(rows), seen)
	}
}

// A sample that cannot be read is said and left alone, and the run still
// drops and counts.
func TestListedDropsWithAnUnreadableSampleStillDropAndLeaveTheFile(t *testing.T) {
	state := t.TempDir()
	path := filepath.Join(state, "audit", "prefilter-sample.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"v":7}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	w := &wired{stateDir: state, warn: &warn}
	w.openPrefilterSample()
	if !w.listedDrops(prefilterPolicy())(posting.Posting{ATS: posting.Workday, Title: "Intern"}) {
		t.Error("an unreadable sample stopped the drop")
	}
	w.savePrefilterSample()
	if b, _ := os.ReadFile(path); string(b) != `{"v":7}` {
		t.Errorf("the unreadable file was overwritten: %q", b)
	}
	if !strings.Contains(warn.String(), "prefilter sample not kept") {
		t.Errorf("no warning:\n%s", warn.String())
	}
	w.mu.Lock()
	counts := w.prefilterRulesLocked()
	w.mu.Unlock()
	if counts["exclude_title"] != 1 {
		t.Errorf("counts = %v", counts)
	}
}

// No exclusions, no listing drop: the adapters see nil, as before, and a run
// with no state dir records nothing but still counts.
func TestListedDropsNilWithoutAPolicyAndNoStateDirKeepsNothing(t *testing.T) {
	w := &wired{warn: &bytes.Buffer{}}
	w.openPrefilterSample()
	if w.listedDrops(pipeline.DropPolicy{}) != nil {
		t.Error("a policy with no rules must give the adapters nil")
	}
	if !w.listedDrops(prefilterPolicy())(posting.Posting{Title: "Intern"}) {
		t.Error("no drop")
	}
	w.savePrefilterSample() // nothing to write, nowhere to write it: no panic, no file
	var nilRun wired
	nilRun.notePrefilter(posting.Posting{Title: "x"}, "exclude_title") // before openPrefilterSample: ignored
}

// The production listing opens the sample, and a run that reads no board still
// saves nothing and leaves an existing file alone.
func TestReadBoardsOpensAndSavesThePrefilterSample(t *testing.T) {
	l := newTestLedger(t)
	cfg := config.Default()
	cfg.Watchlist = nil
	d, err := egress.New(egress.Config{AllowedHosts: []string{"boards.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	w := &wired{cfg: cfg, stateDir: state, led: l, warn: &bytes.Buffer{}, live: &liveNote{}}
	if _, err := w.readBoards(t.Context(), d, prefilterPolicy(), nil); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	opened := w.prefilter != nil && w.prefilter.sample != nil
	w.mu.Unlock()
	if !opened {
		t.Error("readBoards did not open the prefilter sample")
	}
	if _, err := os.Stat(filepath.Join(state, "audit", "prefilter-sample.json")); !os.IsNotExist(err) {
		t.Errorf("a run that dropped nothing wrote a sample: %v", err)
	}
}
