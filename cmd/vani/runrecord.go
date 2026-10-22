package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/daemon"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
)

// The run record is the one place the last daily run is written down, so
// `vani status --json` (and the Mac app's Pipeline tab) can say whether it ran,
// failed or is still going. dailyJob writes it, so the daemon and run-once
// record identically. It lives at <state dir>/runs/last.json and holds only the
// latest run; every finished run is also kept in runs/history/ (runhistory.go),
// and the ledger and the digests are the record of what the runs did.
//
// A record with no finished_at is either a run in progress or one whose process
// died; the pid and the age tell them apart (runState).

const (
	runRecordVersion = 1
	runRecordDir     = "runs"
	runRecordFile    = "last.json"
	// maxRunRecord bounds the record a reader accepts. The record's own bounds
	// (boards, backend errors) keep a worst-case record well under it
	// (TestWorstCaseRunRecordIsReadable); a record past it reads as unknown.
	maxRunRecord     = 256 << 10
	maxRunErrorRunes = 500
	// maxRunAge is how long a record with no end is still believed to be a
	// running process: a pid can be reused, and no run takes this long.
	maxRunAge = 24 * time.Hour
)

// Run outcomes as written to the record. "running" is the state before the end.
const (
	outcomeRunning   = "running"
	outcomeOK        = "ok"
	outcomeFailed    = "failed"
	outcomeCancelled = "cancelled"
)

// Run stages: where a failed run stopped.
const (
	stagePipeline = "pipeline"
	stageDigest   = "digest"
)

// Run causes: why a failed run failed, when it is one a reader words on its
// own (failureNotice, and the Mac app's RunNotice). Fixed codes, set from the
// error's identity and never from its text.
const (
	causeLedgerFatal    = "ledger_fatal"     // daemon.ErrFatal: the ledger could not be written safely
	causeDigestHeldBack = "digest_held_back" // errDigestHeldBack: the Jobs marks could not be read
	causeConfigStale    = "config_stale"     // errConfigStale: the run used the previous watchlist
)

// runCause is the run error's cause code. A joined error can carry several;
// the code is the first in failureNotice's order, which is also the order of
// what the user most needs to know.
func runCause(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, daemon.ErrFatal):
		return causeLedgerFatal
	case errors.Is(err, errDigestHeldBack):
		return causeDigestHeldBack
	case errors.Is(err, errConfigStale):
		return causeConfigStale
	default:
		return ""
	}
}

type runRecord struct {
	V          int       `json:"v"`
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Outcome    string    `json:"outcome"`
	// Trigger is who started the run: triggerSchedule (the daemon) or
	// triggerManual (run-once). Absent in an older vani's record.
	Trigger string `json:"trigger,omitempty"`
	// Options are a manual run's overrides; nil when it had none.
	Options *runOptions `json:"options,omitempty"`
	Stage   string      `json:"stage,omitempty"`
	Error   string      `json:"error,omitempty"`
	// Cause names a failed run's known cause (runCause), so a reader can word
	// the failure without reading Error; "" when the failure has none of them.
	Cause string `json:"cause,omitempty"`
	// DigestRoles is how many roles the run's digest listed (publishDigest),
	// 0 when nothing was new. Nil when no digest was written: the run stopped
	// before it, it was held back, or writing it failed.
	DigestRoles *int `json:"digest_roles,omitempty"`
	// DigestWarning is why the digest ranked on less than the config asked for
	// (a missing rank_weights_file, an unreadable knowledge base); "" when it
	// ranked as configured.
	DigestWarning string `json:"digest_warning,omitempty"`
	// ImpressionsError says the digest was written but what it showed was not
	// recorded (internal/feedback), so the ranking's measurement has a gap for
	// this run; "" when they were recorded or no digest was written.
	ImpressionsError string `json:"impressions_error,omitempty"`
	// CountsBefore and CountsAfter are the ledger's postings by status at the
	// start and the end of the run, so the change is the run's own work.
	CountsBefore map[string]int `json:"counts_before,omitempty"`
	CountsAfter  map[string]int `json:"counts_after,omitempty"`
	// Semantic is the knowledge-base warm between the advance phase and the
	// digest (semanticwire.go); nil when semantic matching is off.
	Semantic *semanticReport `json:"semantic,omitempty"`
	runDetail
}

// runDetail is what the pipeline itself reports about a run (App.RunReport).
type runDetail struct {
	// Boards is the listing phase, board by board; nil when the run ended
	// before it (the stages could not be built). A board that failed to list
	// otherwise reaches only stderr.
	Boards *boardsReport `json:"boards,omitempty"`
	// Advance is the triage/score/explain phase; nil when the run ended
	// before it (no board could be read, the stages could not be built).
	Advance *pipeline.AdvanceReport `json:"advance,omitempty"`
	// BackendErrors are the decision backends' failed calls, by cause. A
	// backend error becomes an "unavailable" reading that keeps no text, so
	// this is the only place a refused key or an outage is written down.
	BackendErrors *backendErrorReport `json:"backend_errors,omitempty"`
	// SemanticPre is the knowledge-base warm of live postings before the
	// advance phase (semanticwire.go); nil when it did not run.
	SemanticPre *semanticReport `json:"semantic_pre,omitempty"`
	// BackendStats is what each decision backend did, by name: calls, slot
	// readings by kind, cost, wall time, and what the circuit breaker turned
	// away. Additive: readers that do not know the key ignore it.
	BackendStats map[string]backendStat `json:"backend_stats,omitempty"`
}

// Board outcomes in the run's board report (boardReport.Outcome).
const (
	boardRead    = "read"
	boardFailed  = "failed"  // listing or reconciling failed; its postings are untouched
	boardSkipped = "skipped" // no adapter, or the careers-page source is disabled this run
)

// boardsReport is what one run's listing phase did. The totals count every
// configured board, whatever Items keeps.
type boardsReport struct {
	// Configured is the watchlist's size.
	Configured int `json:"configured"`
	Read       int `json:"read"`
	Failed     int `json:"failed"`
	Skipped    int `json:"skipped"`
	// NotReached boards were never tried: the run ended (cancelled, a ledger
	// failure) before them.
	NotReached int `json:"not_reached,omitempty"`
	// Listed is the postings the read boards listed, together.
	Listed int `json:"listed"`
	// Items holds one entry per tried board, in watchlist order. When the
	// bounds cut it, failed and skipped boards are kept before read ones.
	Items []boardReport `json:"items"`
	// Unrecorded counts tried boards left out of Items by its bounds
	// (maxRecordedBoards, maxBoardsBytes).
	Unrecorded int `json:"unrecorded,omitempty"`
}

// boardReport is one board in one run.
type boardReport struct {
	ATS     string `json:"ats"`
	Token   string `json:"token"`
	Company string `json:"company,omitempty"`
	Outcome string `json:"outcome"`
	// Listed is how many postings the board listed; absent when the listing
	// failed or the board was skipped. Zero is a board that answered empty.
	Listed *int `json:"listed,omitempty"`
	// Reconcile is what the listing did to the ledger; read boards only.
	Reconcile *reconcileCounts `json:"reconcile,omitempty"`
	// Error is why the board failed or was skipped: the adapter's error on one
	// bounded line (it names the board, a search term or a posting's path,
	// never a description).
	Error string `json:"error,omitempty"`
}

// reconcileCounts is pipeline.Summary as the run record writes it.
type reconcileCounts struct {
	New        int `json:"new,omitempty"`
	Changed    int `json:"changed,omitempty"`
	Rerendered int `json:"rerendered,omitempty"`
	Unchanged  int `json:"unchanged,omitempty"`
	Reappeared int `json:"reappeared,omitempty"`
	Missed     int `json:"missed,omitempty"`
	Dead       int `json:"dead,omitempty"`
	Duplicate  int `json:"duplicate,omitempty"`
	Invalid    int `json:"invalid,omitempty"`
}

func runRecordPath(stateDir string) string {
	return filepath.Join(stateDir, runRecordDir, runRecordFile)
}

// writeRunRecord replaces the record atomically: a reader sees the old record
// or the new one, never half of either.
func writeRunRecord(stateDir string, r runRecord) error {
	raw, err := marshalRunRecord(r)
	if err != nil {
		return err
	}
	return atomicfile.Write(runRecordPath(stateDir), raw)
}

// marshalRunRecord is the record's one encoding: last.json and the history's
// copy of it (runhistory.go) are the same bytes.
func marshalRunRecord(r runRecord) ([]byte, error) {
	r.V = runRecordVersion
	return json.MarshalIndent(r, "", "  ")
}

// readRunRecord returns the last record. A missing file is (nil, nil): no run
// has been recorded. Anything unreadable is an error, never "no run".
func readRunRecord(stateDir string) (*runRecord, error) {
	return readRunRecordFile(runRecordPath(stateDir))
}

// readRunRecordFile is readRunRecord for any record file: last.json or a
// history entry.
func readRunRecordFile(path string) (*runRecord, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxRunRecord+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxRunRecord {
		return nil, fmt.Errorf("run record exceeds %d bytes", maxRunRecord)
	}
	var r runRecord
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("run record is corrupt: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("run record has trailing data")
	}
	if r.V != runRecordVersion {
		return nil, fmt.Errorf("run record version %d, want %d", r.V, runRecordVersion)
	}
	switch r.Outcome {
	case outcomeRunning, outcomeOK, outcomeFailed, outcomeCancelled:
	default:
		return nil, fmt.Errorf("run record has outcome %q", r.Outcome)
	}
	if r.StartedAt.IsZero() {
		return nil, errors.New("run record has no start time")
	}
	return &r, nil
}

// pidAlive reports whether a process with this pid exists (processExists, per
// platform). Tests replace it.
var pidAlive = processExists

// runState is the record's state as the user should read it: the written
// outcome, except that a record with no end is "running" only while its process
// lives and is recent, and "interrupted" otherwise.
func runState(r runRecord, at time.Time) string {
	if r.Outcome != outcomeRunning {
		return r.Outcome
	}
	if pidAlive(r.PID) && at.Sub(r.StartedAt) < maxRunAge {
		return outcomeRunning
	}
	return "interrupted"
}

// statusCounts folds records into postings by status, the one counting both
// the text and the JSON status use.
func statusCounts(recs []ledger.Record) map[string]int {
	by := map[string]int{}
	for _, r := range recs {
		by[string(r.Status)]++
	}
	return by
}

// runErrorText is an error flattened to one bounded line for the record.
func runErrorText(err error) string { return oneLine(err.Error(), maxRunErrorRunes) }
