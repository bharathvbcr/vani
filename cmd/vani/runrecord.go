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
	"github.com/bharathvbcr/Vani/internal/ledger"
)

// The run record is the one place the last daily run is written down, so
// `vani status --json` (and the Mac app's Pipeline tab) can say whether it ran,
// failed or is still going. dailyJob writes it, so the daemon and run-once
// record identically. It lives at <state dir>/runs/last.json and holds only the
// latest run: the ledger and the digests are the history.
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

type runRecord struct {
	V          int       `json:"v"`
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Outcome    string    `json:"outcome"`
	Stage      string    `json:"stage,omitempty"`
	Error      string    `json:"error,omitempty"`
	// CountsBefore and CountsAfter are the ledger's postings by status at the
	// start and the end of the run, so the change is the run's own work.
	CountsBefore map[string]int `json:"counts_before,omitempty"`
	CountsAfter  map[string]int `json:"counts_after,omitempty"`
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
	Advance *advanceReport `json:"advance,omitempty"`
	// BackendErrors are the decision backends' failed calls, by cause. A
	// backend error becomes an "unavailable" reading that keeps no text, so
	// this is the only place a refused key or an outage is written down.
	BackendErrors *backendErrorReport `json:"backend_errors,omitempty"`
}

// Why an advance phase ended before it had worked through every eligible
// posting (advanceReport.Stopped).
const (
	stopCap          = "cap"           // advance_per_run reached
	stopUnreadStreak = "unread_streak" // maxConsecutiveUnread unread in a row
	stopNoneRead     = "none_read"     // no backend read any posting
	stopFailStreak   = "fail_streak"   // maxConsecutiveFailed stage failures in a row
	stopNoneAdvanced = "none_advanced" // every attempted posting failed or went unread
	stopCancelled    = "cancelled"
)

// advanceReport is what one run's advance phase did. Deferred postings were
// not attempted this run and wait, still listed, for the next one.
type advanceReport struct {
	// Eligible postings were waiting for a stage when the phase started.
	Eligible int `json:"eligible"`
	// Limit is the per-run cap (config advance_per_run).
	Limit     int `json:"limit"`
	Attempted int `json:"attempted"`
	// Unread postings no decision backend could read; they stay live.
	Unread int `json:"unread"`
	// Failed postings hit another stage error; they wait for the next run.
	Failed   int    `json:"failed"`
	Deferred int    `json:"deferred"`
	Stopped  string `json:"stopped,omitempty"`
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
	r.V = runRecordVersion
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(runRecordPath(stateDir), raw)
}

// readRunRecord returns the last record. A missing file is (nil, nil): no run
// has been recorded. Anything unreadable is an error, never "no run".
func readRunRecord(stateDir string) (*runRecord, error) {
	f, err := os.Open(runRecordPath(stateDir))
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
