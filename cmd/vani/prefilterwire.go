package main

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A listing-time drop (pipeline.DropPolicy.ListedDrops, the adapters'
// DropListed) removes a new row before the ledger sees it. The run keeps a
// bounded, deterministic sample of those rows (pipeline.PrefilterSample) in a
// state file of its own, and the weekly audit draws from it as the classes
// "dropped:prefilter:<rule>". The listing rows carry no id, so the sample is
// keyed by what they do carry (pipeline.PrefilterKey).

// prefilterRun is what one run's listing drops added up to: the sample it
// notes into and a count of every drop by rule, for the run record. Nil when
// the run has no state dir or its sample file could not be read.
type prefilterRun struct {
	mu     sync.Mutex
	sample *pipeline.PrefilterSample
	rules  map[string]int
}

func prefilterSamplePath(stateDir string) string {
	return filepath.Join(stateDir, pipeline.PrefilterSampleFile)
}

// openPrefilterSample readies the run's listing-drop recorder. A sample that
// cannot be read is never taken for an empty one nor written over: the run
// goes on counting its drops by rule and keeps none.
func (w *wired) openPrefilterSample() {
	run := &prefilterRun{rules: map[string]int{}}
	if w.stateDir != "" {
		s, err := pipeline.LoadPrefilterSample(prefilterSamplePath(w.stateDir), pipeline.DefaultPrefilterSamplePerWeek)
		if err != nil {
			w.warnf("prefilter sample not kept this run: %v", err)
		} else {
			run.sample = s
		}
	}
	w.mu.Lock()
	w.prefilter = run
	w.mu.Unlock()
}

// listedDrops is the policy's listing-time drop rule (ListedDrops) that also
// notes each row it drops, with the rule that dropped it, in the run's sample
// and counts. It is the DropListed the adapters take.
func (w *wired) listedDrops(policy pipeline.DropPolicy) func(posting.Posting) bool {
	return policy.ListedDropsRecorded(w.notePrefilter)
}

func (w *wired) notePrefilter(p posting.Posting, rule string) {
	w.mu.Lock()
	run := w.prefilter
	w.mu.Unlock()
	if run == nil {
		return
	}
	at := now()
	run.mu.Lock()
	run.rules[rule]++
	s := run.sample
	run.mu.Unlock()
	s.Note(p, rule, eval.WeekSeed(at), at) // a nil sample counts nothing
}

// savePrefilterSample writes the run's sample once its listing is done. A
// failure is a warning: the audit loses a week's rows, the run is not failed.
func (w *wired) savePrefilterSample() {
	w.mu.Lock()
	run := w.prefilter
	w.mu.Unlock()
	if run == nil || run.sample == nil || !run.sample.Changed() {
		return
	}
	if err := run.sample.Save(prefilterSamplePath(w.stateDir), now()); err != nil {
		w.warnf("prefilter sample not saved: %v", err)
	}
}

// prefilterRulesLocked is how many listed rows each rule dropped this run (nil
// when none), copied for the run record. The caller holds w.mu.
func (w *wired) prefilterRulesLocked() map[string]int {
	run := w.prefilter
	if run == nil {
		return nil
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if len(run.rules) == 0 {
		return nil
	}
	out := make(map[string]int, len(run.rules))
	for k, v := range run.rules {
		out[k] = v
	}
	return out
}

// loadPrefilterSample reads the sample for the audit. A sample that cannot be
// read is said and left as it was; the draw goes on without its rows.
func (e *env) loadPrefilterSample() *pipeline.PrefilterSample {
	if e.stateDir == "" {
		return nil
	}
	s, err := pipeline.LoadPrefilterSample(prefilterSamplePath(e.stateDir), pipeline.DefaultPrefilterSamplePerWeek)
	if err != nil {
		fmt.Fprintf(e.stderr, "vani: the prefilter sample is unreadable, so the draw has no pre-ledger drops: %v\n", err)
		return nil
	}
	return s
}

// prefilterRecord is the sampled row for k as the audit card's record: the
// listed fields as a dropped posting, with no description and no reading.
func prefilterRecord(rows map[posting.Key]pipeline.PrefilterRow, k posting.Key) (ledger.Record, bool) {
	r, ok := rows[k]
	if !ok {
		return ledger.Record{}, false
	}
	return ledger.Record{Key: k, Status: posting.Dropped, Posting: r.Posting()}, true
}

// prefilterRows indexes the sample's rows by key.
func prefilterRows(s *pipeline.PrefilterSample) map[posting.Key]pipeline.PrefilterRow {
	out := map[posting.Key]pipeline.PrefilterRow{}
	for _, r := range s.Rows() {
		out[r.Key] = r
	}
	return out
}
