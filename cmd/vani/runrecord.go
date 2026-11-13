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
	"github.com/bharathvbcr/Vani/internal/logo"
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
//
// Which board a run is reading, and the stage it is in, is not this file. That
// is the sibling runs/progress.json (runprogress.go): replaced as the run moves
// and removed when the run ends, and not part of the history. Rewriting
// last.json per board would flush history and change this record's contract.

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
	// outcomeInterrupted is a run whose process died: runState reports it for a
	// `running` record whose process is gone, and archiveInterruptedRun writes it
	// as the outcome of such a record it keeps in the run history. last.json never
	// holds it: the next run replaces that record the moment it starts.
	outcomeInterrupted = "interrupted"
	// outcomeSkipped is a daily slot the daemon woke too late to run. It is
	// written only to the run history, never to last.json, so the last real run
	// stays what status and the Pipeline tab show as the last run.
	outcomeSkipped = "skipped"
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
	V int `json:"v"`
	// ID is the run's id (runID), written with every record so a reader that
	// names a file after the run (the Mac app's rotated manual logs) takes the
	// name from here instead of rebuilding it. runID(r) stays the authority:
	// readers of this package ignore the field, and a record without it (an
	// older vani's) is as valid.
	ID         string    `json:"id,omitempty"`
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Outcome    string    `json:"outcome"`
	// Trigger is who started the run: triggerSchedule (the daemon) or
	// triggerManual (run-once). Absent in an older vani's record.
	Trigger string `json:"trigger,omitempty"`
	// Options are a manual run's overrides; nil when it had none.
	Options *runOptions `json:"options,omitempty"`
	// Log is the file the run's output was sent to. A manual run the app started
	// names the path it set (VANI_RUN_LOG). A run that named none keeps its own
	// file at runs/history/<id>.log. "" when neither was possible. The path is
	// always inside <state dir>/runs (checkRunLog).
	Log   string `json:"log,omitempty"`
	Stage string `json:"stage,omitempty"`
	Error string `json:"error,omitempty"`
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
	// NearDup, Exclusions and Logos are the stages' counts. A nil pointer means
	// the stage did not run; a pointer, even to zeros, means it did. None of
	// them carries a posting key, a description or a host list.
	NearDup    *nearDupSummary   `json:"near_dup,omitempty"`
	Exclusions *exclusionSummary `json:"exclusions,omitempty"`
	Logos      *logoSummary      `json:"logos,omitempty"`
	// Shadow is set when a configured shadow did not start, did not attach, or
	// missed calls. Nil when there is nothing to say, including a list-only run.
	Shadow *shadowNote `json:"shadow,omitempty"`
	// EscalateBackfill is the escalation backfill after the advance phase: how
	// many already-triaged postings it asked the escalation tier about and what
	// came back (escalatebackfill.go). Nil when decide.escalate is off or the
	// advance phase did not end well. Additive: readers that do not know the key
	// ignore it.
	EscalateBackfill *escalateBackfillReport `json:"escalate_backfill,omitempty"`
	// Explain is set when the digest's notes were off or paused. Nil when they
	// ran, or the run was not going to ask a backend.
	Explain *explainNote `json:"explain,omitempty"`
	// LogDropped counts lines the per-run log did not keep after maxRunLogBytes.
	LogDropped int `json:"log_dropped,omitempty"`
}

// maxStageNote bounds one example or error kept on a stage summary.
const maxStageNote = 200

// nearDupSummary is pipeline.NearDupReport as counts. Note is set when the
// stage returned an error, one line, and never a posting.
type nearDupSummary struct {
	Records           int    `json:"records,omitempty"`
	Examined          int    `json:"examined,omitempty"`
	Compared          int    `json:"compared,omitempty"`
	Duplicates        int    `json:"duplicates,omitempty"`
	Siblings          int    `json:"siblings,omitempty"`
	Candidates        int    `json:"candidates,omitempty"`
	Partial           bool   `json:"partial,omitempty"`
	SimilarityErrors  int    `json:"similarity_errors,omitempty"`
	SimilaritySkipped int    `json:"similarity_skipped,omitempty"`
	Note              string `json:"note,omitempty"`
	// SimilarityOff is why the cosine did not run. Empty when it ran, and when
	// semantic matching is off: a load failure is not a shingles-only pass.
	SimilarityOff string `json:"similarity_off,omitempty"`
	// SimilarityNote is a cache that could not be used or saved. The cosine
	// still ran. Empty when there is nothing to say.
	SimilarityNote string `json:"similarity_note,omitempty"`
}

func summarizeNearDup(nd pipeline.NearDupReport, err error) *nearDupSummary {
	s := &nearDupSummary{
		Records: nd.Records, Examined: nd.Examined, Compared: nd.Compared,
		Duplicates: nd.Duplicates, Siblings: nd.Siblings, Candidates: nd.Candidates,
		Partial: nd.Partial, SimilarityErrors: nd.SimilarityErrors, SimilaritySkipped: nd.SimilaritySkipped,
	}
	if err != nil {
		s.Note = oneLine(err.Error(), maxStageNote)
	}
	return s
}

// exclusionSummary is pipeline.ExclusionChange as counts. Unread and
// BodyUnavailable are checks that could not run, which is not a pass.
type exclusionSummary struct {
	Dropped int `json:"dropped,omitempty"`
	// Reopened came back to Live at once; Deferred were marked as pending
	// reopens, still dropped, for their board's next full listing; Stale are on
	// a board no longer watched and stay dropped.
	Reopened        int `json:"reopened,omitempty"`
	Deferred        int `json:"deferred,omitempty"`
	AlreadyPending  int `json:"already_pending,omitempty"`
	Stale           int `json:"stale,omitempty"`
	OverCap         int `json:"over_cap,omitempty"`
	Unread          int `json:"unread,omitempty"`
	BodyUnavailable int `json:"body_unavailable,omitempty"`
}

func summarizeExclusions(ch pipeline.ExclusionChange) *exclusionSummary {
	return &exclusionSummary{
		Dropped: len(ch.Drop), Reopened: ch.Reopened, Deferred: ch.Deferred, AlreadyPending: ch.AlreadyPending,
		Stale: ch.Stale, OverCap: ch.OverCap, Unread: len(ch.Unread),
		BodyUnavailable: ch.BodyUnavailable,
	}
}

// logoSummary is logo.Result as counts. MissingHosts is how many hosts were
// not asked; Example is the first failure, one line.
type logoSummary struct {
	Fetched      int    `json:"fetched,omitempty"`
	Fresh        int    `json:"fresh,omitempty"`
	Waiting      int    `json:"waiting,omitempty"`
	Failed       int    `json:"failed,omitempty"`
	Deferred     int    `json:"deferred,omitempty"`
	MissingHosts int    `json:"missing_hosts,omitempty"`
	StateReset   string `json:"state_reset,omitempty"`
	Example      string `json:"example,omitempty"`
}

func summarizeLogos(res logo.Result, missing int, err error) *logoSummary {
	s := &logoSummary{
		Fetched: res.Fetched, Fresh: res.Fresh, Waiting: res.Waiting, Failed: res.Failed,
		Deferred: res.Deferred, MissingHosts: missing,
	}
	if res.StateReset != "" {
		s.StateReset = oneLine(res.StateReset, maxStageNote)
	}
	switch {
	case len(res.Errors) > 0:
		s.Example = oneLine(res.Errors[0], maxStageNote)
	case err != nil:
		s.Example = oneLine(err.Error(), maxStageNote)
	}
	return s
}

// shadowNote is a configured shadow that did not do what the run asked.
// Local is a local-server start error. Note is why a configured shadow never
// attached (one line). Errors counts calls or slots that failed, timed out, or
// came back unusable. BusySkips counts sampled postings skipped while the
// shadow was busy: those carry no shadow reading. An empty note is not a miss.
type shadowNote struct {
	Local     string `json:"local,omitempty"`
	Note      string `json:"note,omitempty"`
	Errors    int    `json:"errors,omitempty"`
	BusySkips int    `json:"busy_skips,omitempty"`
}

func (s *shadowNote) failed() bool {
	return s != nil && (s.Local != "" || s.Note != "" || s.Errors > 0 || s.BusySkips > 0)
}

// explainNote says the digest's notes did not run. Mode is explainOff or
// explainPaused. Note is one line, the same words as the warning.
type explainNote struct {
	Mode string `json:"mode,omitempty"`
	Note string `json:"note,omitempty"`
}

func (n *explainNote) failed() bool {
	return n != nil && (n.Mode == explainOff || n.Mode == explainPaused)
}

// Board outcomes in the run's board report (boardReport.Outcome).
const (
	boardRead    = "read"
	boardFailed  = "failed"  // listing or reconciling failed; its postings are untouched
	boardSkipped = "skipped" // no adapter, or the careers-page source is disabled this run
	// boardRateLimited is a board whose host kept answering HTTP 429 through the
	// retries: not broken, left untouched, tried again next run. Older readers
	// show an outcome they do not know as written.
	boardRateLimited = "rate_limited"
)

// boardsReport is what one run's listing phase did. The totals count every
// configured board, whatever Items keeps.
type boardsReport struct {
	// Configured is the watchlist's size.
	Configured int `json:"configured"`
	Read       int `json:"read"`
	// Failed counts every board whose listing failed, the rate-limited ones
	// included, so a reader that does not know RateLimited still counts them.
	Failed int `json:"failed"`
	// RateLimited is the part of Failed that ended in HTTP 429 (outcome
	// rate_limited). Additive: readers that do not know the key ignore it.
	RateLimited int `json:"rate_limited,omitempty"`
	Skipped     int `json:"skipped"`
	// NotReached boards were never tried: the run ended (cancelled, a ledger
	// failure) before them.
	NotReached int `json:"not_reached,omitempty"`
	// Listed is the postings the read boards listed, together.
	Listed int `json:"listed"`
	// Added is how many new ledger rows the tried boards wrote: the sum of
	// reconcile.new on every tried board, including ones Items had to leave
	// out. Zero is written. A missing key means this record did not report it.
	Added *int `json:"added,omitempty"`
	// Deferred, PayFailed, PaySkipped and Gone are detail and pay counts for
	// the whole listing. They stay exact when Items is cut. A pay failure
	// leaves no pay line, the same shape as a job that publishes none.
	// Deferred rows were not stored, and their absence does not age them.
	Deferred   int    `json:"deferred,omitempty"`
	PayFailed  int    `json:"pay_failed,omitempty"`
	PaySkipped int    `json:"pay_skipped,omitempty"`
	Gone       int    `json:"gone,omitempty"`
	PayError   string `json:"pay_error,omitempty"`
	// Prefiltered is new listed rows the drop policy (exclusions and title
	// reads) rejected before any detail request, on the adapters that read a
	// detail per posting. They were never stored, so no audit or retriage can
	// see them; a later listing under a policy that no longer drops them
	// stores them as new.
	Prefiltered int `json:"prefiltered,omitempty"`
	// Mass counts read boards whose full listing newly marked postings as a mass
	// absence (a share of the board's open postings, or all of them, left it: an
	// outage, a moved board, a renumbering; pipeline liveness.go). Their absent
	// postings were kept, and die only after the mass hold. A later listing that
	// only carries those marks is not counted. Counted in Read too.
	// MassMarked is the postings those listings newly marked; Held is absent
	// postings past the miss count that were kept alive inside a hold (an
	// ordinary or a mass one). Additive.
	Mass       int `json:"mass,omitempty"`
	MassMarked int `json:"mass_marked,omitempty"`
	Held       int `json:"held,omitempty"`
	// ClockBehind counts read boards whose full listing aged nothing because
	// the ledger holds events stamped ahead of the run's clock (pipeline
	// liveness.go "The clock"): the clock is behind, or those events came from a
	// clock that ran ahead. A failure: those boards age nothing until the clock
	// passes the events. Additive.
	ClockBehind int `json:"clock_behind,omitempty"`
	// MassDead is postings a mass absence had marked that died this run, their
	// mass hold over and their board still not listing them. A failure, so the
	// owner sees them: a mass hold usually ends in its postings coming back.
	// Counted in the items' dead too. Additive.
	MassDead int `json:"mass_dead,omitempty"`
	// DeathsDeferred is deaths that waited one more read because more than
	// pipeline.ClockWindow had passed since the posting's last event (a gap a
	// jumped clock would also make). Shown, not a failure. Additive.
	DeathsDeferred int `json:"deaths_deferred,omitempty"`
	// MassHeld names the boards whose postings wait out a mass hold after this
	// run (from the ledger, not only this run's listings), with when the holds
	// end, at most maxStaleListed; MassHeldCount is all such boards and
	// MassHeldPostings all such postings. An attention line every run while any
	// is held, not a failure. Additive.
	MassHeld         []massHeldBoard `json:"mass_held,omitempty"`
	MassHeldCount    int             `json:"mass_held_count,omitempty"`
	MassHeldPostings int             `json:"mass_held_postings,omitempty"`
	// Short is rows the boards counted but did not list, together (see
	// boardReport.Short). Additive: readers that do not know the key ignore it.
	Short int `json:"short,omitempty"`
	// Items holds one entry per tried board, in watchlist order. When the
	// bounds cut it, failed, skipped, mass and clock-behind boards are kept
	// before read ones.
	Items []boardReport `json:"items"`
	// Unrecorded counts tried boards left out of Items by its bounds
	// (maxRecordedBoards, maxBoardsBytes).
	Unrecorded int `json:"unrecorded,omitempty"`
	// Stale names watched boards that have gone more than boardStaleAfter
	// without a good read (board health, boardhealth.go), most overdue first, at
	// most maxStaleListed; StaleCount is all of them. Additive.
	Stale      []staleBoard `json:"stale,omitempty"`
	StaleCount int          `json:"stale_count,omitempty"`
	// HealthError is why board health could not be saved this run (one line);
	// the stale list above is then this run's view only. Additive.
	HealthError string `json:"health_error,omitempty"`
	// Empty names watched boards that read fine this run and listed nothing
	// though the ledger holds postings of theirs that died or are held after an
	// absence (boardsnone.go): a board that used to list and now lists none. At
	// most maxStaleListed; EmptyCount is all of them. Derived from the ledger,
	// not from board health. Additive.
	Empty      []emptyBoard `json:"empty,omitempty"`
	EmptyCount int          `json:"empty_count,omitempty"`

	// all is every tried board, uncut, for this process (board health and the
	// empty list). Never written.
	all []boardReport
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
	// Deferred is new Workday rows past the detail cap: not stored, and their
	// absence does not age them.
	Deferred int `json:"deferred,omitempty"`
	// PayFailed and PaySkipped are Greenhouse pay requests that did not land.
	PayFailed  int    `json:"pay_failed,omitempty"`
	PaySkipped int    `json:"pay_skipped,omitempty"`
	PayError   string `json:"pay_error,omitempty"`
	// Gone is details that answered 404 under a listing that succeeded, under
	// the threshold that fails the board.
	Gone int `json:"gone,omitempty"`
	// Prefiltered is new listed rows the drop policy rejected before any
	// detail request: never stored.
	Prefiltered int `json:"prefiltered,omitempty"`
	// Short is rows a read board counted but did not list: a Workday query
	// under its 2000-row window that ended before its own total
	// (workday.Stats.Short). The listing is kept, not failed, so the postings
	// behind those rows go unseen, so the board is reconciled as a partial listing and ages nothing (shortreconcile.go).
	Short int `json:"short,omitempty"`

	// DeferredSince and ShortSince, set when Deferred or Short is, are when the
	// board's current streak of good reads with that count began (board
	// health, boardbacklog.go). The oldest deferred row is no older than
	// DeferredSince; a short board ages nothing while ShortSince is open.
	// Additive.
	DeferredSince *time.Time `json:"deferred_since,omitempty"`
	ShortSince    *time.Time `json:"short_since,omitempty"`

	// endedRun marks the board whose failure ended the listing (the run was
	// cancelled, or the ledger failed under it): its outcome says nothing
	// about the board, so board health leaves it out. Never written.
	endedRun bool
}

// reconcileCounts is pipeline.Summary as the run record writes it.
type reconcileCounts struct {
	New        int `json:"new,omitempty"`
	Changed    int `json:"changed,omitempty"`
	Rerendered int `json:"rerendered,omitempty"`
	Kept       int `json:"kept,omitempty"`
	Redated    int `json:"redated,omitempty"`
	Refreshed  int `json:"refreshed,omitempty"`
	Revived    int `json:"revived,omitempty"`
	Unchanged  int `json:"unchanged,omitempty"`
	Reappeared int `json:"reappeared,omitempty"`
	Missed     int `json:"missed,omitempty"`
	Dead       int `json:"dead,omitempty"`
	Duplicate  int `json:"duplicate,omitempty"`
	Invalid    int `json:"invalid,omitempty"`
	// Open is the board's open postings when the listing began and Absent how
	// many of them it left out (pipeline.Summary); Held is the absent ones past
	// the miss count that were kept alive inside their hold. Mass says the
	// listing was a mass absence, MassMarked how many postings it newly marked,
	// ClockBehind that it aged nothing because the ledger holds events stamped
	// ahead of the clock, ClockAhead the newest such event. Deferred is deaths
	// that waited a read for a gap of more than pipeline.ClockWindow; MassDead
	// is deaths of mass-marked postings.
	Open        int       `json:"open,omitempty"`
	Absent      int       `json:"absent,omitempty"`
	Held        int       `json:"held,omitempty"`
	Mass        bool      `json:"mass,omitempty"`
	MassMarked  int       `json:"mass_marked,omitempty"`
	ClockBehind bool      `json:"clock_behind,omitempty"`
	ClockAhead  time.Time `json:"clock_ahead,omitzero"`
	Deferred    int       `json:"deaths_deferred,omitempty"`
	MassDead    int       `json:"mass_dead,omitempty"`
}

func runRecordPath(stateDir string) string {
	return filepath.Join(stateDir, runRecordDir, runRecordFile)
}

// errNoStateDir is what a runs/ writer returns for an empty state dir. Joined
// onto "", runs/... is relative, so the write would land in the working
// directory (a test run from the source tree left runs/inbox.json there).
var errNoStateDir = errors.New("no state dir: refusing to write under the working directory")

// writeRunRecord replaces the record atomically: a reader sees the old record
// or the new one, never half of either.
func writeRunRecord(stateDir string, r runRecord) error {
	if stateDir == "" {
		return errNoStateDir
	}
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
	r.ID = runID(r)
	return json.MarshalIndent(r, "", "  ")
}

// readRunRecord returns the last record. A missing file is (nil, nil): no run
// has been recorded. Anything unreadable is an error, never "no run".
func readRunRecord(stateDir string) (*runRecord, error) {
	return readRunRecordFile(runRecordPath(stateDir))
}

// readRunRecordFile is readRunRecord for any record file: last.json or a
// history entry. Bytes come from readBoundedFile, which refuses a symlink or
// any other non-regular file without opening it. A missing file is no record.
func readRunRecordFile(path string) (*runRecord, error) {
	raw, err := readBoundedFile(path, maxRunRecord)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
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
	case outcomeRunning, outcomeOK, outcomeFailed, outcomeCancelled, outcomeInterrupted, outcomeSkipped:
	default:
		return nil, fmt.Errorf("run record has outcome %q", r.Outcome)
	}
	if r.StartedAt.IsZero() {
		return nil, errors.New("run record has no start time")
	}
	return &r, nil
}

// pidAlive reports whether this process can see pid (processExists, per
// platform). A pid the kernel will not let us inspect is not alive. Tests
// replace it. Whether the pid is this vani daemon is daemonIsLive, not this.
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
	return outcomeInterrupted
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
