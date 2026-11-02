package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// Live progress is the sibling of last.json, not part of it. A run rewrites
// runs/progress.json as it moves (which board it is reading, then the later
// stages) and removes the file when it ends. last.json stays the start and
// end record: rewriting it per board would flush history and change the
// finished-record contract. The file is counts and bounded names only. It is
// not history (listRunHistory reads runs/history) and a missing file is not a
// failed check.

const (
	runProgressFile    = "progress.json"
	runProgressVersion = 1
	// maxRunProgress bounds the file a reader accepts. A honest snapshot is a
	// few hundred bytes; past this it is refused, not trimmed.
	maxRunProgress = 4 << 10
	// maxProgressTotal bounds board counts. It is a corrupt-file cap, not the
	// watchlist's own limit.
	maxProgressTotal = 100_000
	// maxProgressCount bounds the other counters the same way.
	maxProgressCount = 10_000_000
)

// Stages a snapshot may name. The Mac app words these; an unknown stage is
// refused on read, so a corrupt file cannot invent a portal.
const (
	progressStarting   = "starting"
	progressPreparing  = "preparing"
	progressInbox      = "inbox"
	progressListing    = "listing"
	progressNearDup    = "near_dup"
	progressExclusions = "exclusions"
	progressLogos      = "logos"
	progressWarm       = "warm"
	progressAdvance    = "advance"
	progressBackfill   = "backfill"
	progressDigest     = "digest"
)

// Phases of one board. Empty for every stage that is not listing.
const (
	progressPhaseListing     = "listing"
	progressPhaseReconciling = "reconciling"
	progressPhaseDone        = "done"
)

// runProgress is one atomic snapshot of the run that is going.
type runProgress struct {
	V           int       `json:"v"`
	PID         int       `json:"pid"`
	ID          string    `json:"id"`
	At          time.Time `json:"updated_at"`
	Stage       string    `json:"stage"`
	Phase       string    `json:"phase,omitempty"`
	Index       int       `json:"index,omitempty"`
	Total       int       `json:"total,omitempty"`
	Done        int       `json:"done,omitempty"`
	Read        int       `json:"read,omitempty"`
	Failed      int       `json:"failed,omitempty"`
	RateLimited int       `json:"rate_limited,omitempty"`
	Skipped     int       `json:"skipped,omitempty"`
	Listed      int       `json:"listed,omitempty"`
	ATS         string    `json:"ats,omitempty"`
	Token       string    `json:"token,omitempty"`
	Company     string    `json:"company,omitempty"`
	// Outcome is how the board named above finished. Set only when Phase is
	// done: read, failed, rate_limited, or skipped. Empty while the board is
	// still being read, so a failure is not shown as a successful read.
	Outcome   string `json:"outcome,omitempty"`
	Attempted int    `json:"attempted,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Eligible  int    `json:"eligible,omitempty"`
}

// progressFacet is last_run.progress. ok false means the file was present and
// could not be trusted; the field is omitted when there is no file. A failed
// facet carries no board name, so another run's portal is not shown as current.
type progressFacet struct {
	facet
	Stage       string    `json:"stage,omitempty"`
	Phase       string    `json:"phase,omitempty"`
	Index       int       `json:"index,omitempty"`
	Total       int       `json:"total,omitempty"`
	Done        int       `json:"done,omitempty"`
	Read        int       `json:"read,omitempty"`
	Failed      int       `json:"failed,omitempty"`
	RateLimited int       `json:"rate_limited,omitempty"`
	Skipped     int       `json:"skipped,omitempty"`
	Listed      int       `json:"listed,omitempty"`
	ATS         string    `json:"ats,omitempty"`
	Token       string    `json:"token,omitempty"`
	Company     string    `json:"company,omitempty"`
	Outcome     string    `json:"outcome,omitempty"`
	Attempted   int       `json:"attempted,omitempty"`
	Limit       int       `json:"limit,omitempty"`
	Eligible    int       `json:"eligible,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
}

func knownProgressStage(s string) bool {
	switch s {
	case progressStarting, progressPreparing, progressInbox, progressListing,
		progressNearDup, progressExclusions, progressLogos, progressWarm,
		progressAdvance, progressBackfill, progressDigest:
		return true
	default:
		return false
	}
}

func (p runProgress) sane() error {
	if !knownProgressStage(p.Stage) {
		return errors.New("progress stage is unknown")
	}
	switch p.Phase {
	case "", progressPhaseListing, progressPhaseReconciling, progressPhaseDone:
	default:
		return errors.New("progress phase is unknown")
	}
	switch p.Outcome {
	case "":
	case boardRead, boardFailed, boardRateLimited, boardSkipped:
		if p.Phase != progressPhaseDone {
			return errors.New("progress outcome is set before the board finished")
		}
	default:
		return errors.New("progress outcome is unknown")
	}
	if p.At.IsZero() {
		return errors.New("progress time is missing")
	}
	// A slow board stays visible. Only a timestamp in the future is refused;
	// age is not a reason to hide the board the run is on.
	if p.At.After(now().Add(time.Minute)) {
		return errors.New("progress time is ahead of now")
	}
	if p.PID <= 0 || !validRunID(p.ID) {
		return errors.New("progress run id is invalid")
	}
	if p.Total > maxProgressTotal || p.Index > maxProgressTotal {
		return errors.New("progress board count is too large")
	}
	if p.Index > p.Total || p.Done > p.Total {
		return errors.New("progress board counts are impossible")
	}
	for _, c := range []struct {
		n    int
		name string
	}{
		{p.Done, "done"}, {p.Read, "read"}, {p.Failed, "failed"},
		{p.RateLimited, "rate_limited"}, {p.Skipped, "skipped"}, {p.Listed, "listed"},
		{p.Attempted, "attempted"}, {p.Limit, "limit"}, {p.Eligible, "eligible"},
	} {
		if c.n < 0 {
			return fmt.Errorf("progress %s is negative", c.name)
		}
		if c.n > maxProgressCount {
			return fmt.Errorf("progress %s is too large", c.name)
		}
	}
	if p.Read+p.Failed+p.Skipped != p.Done {
		return errors.New("progress board counts are impossible")
	}
	if p.RateLimited > p.Failed {
		return errors.New("progress rate limit count exceeds failures")
	}
	if p.Limit <= 0 {
		if p.Attempted != 0 {
			return errors.New("progress attempted without a limit")
		}
	} else if p.Attempted > min(p.Limit, p.Eligible) {
		return errors.New("progress attempted more than the limit")
	}
	for _, pair := range []struct{ s, name string }{
		{p.ATS, "ats"}, {p.Token, "token"}, {p.Company, "company"},
	} {
		if utf8.RuneCountInString(pair.s) > maxBoardNameRunes || strings.ContainsAny(pair.s, "\r\n") {
			return fmt.Errorf("progress %s is not one bounded line", pair.name)
		}
	}
	return nil
}

func writeRunProgress(stateDir string, p runProgress) error {
	if stateDir == "" {
		return errNoStateDir
	}
	if err := p.sane(); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(raw) > maxRunProgress {
		return fmt.Errorf("%s exceeds %d bytes", runProgressFile, maxRunProgress)
	}
	return atomicfile.Write(runsPath(stateDir, runProgressFile), raw)
}

// readProgressFile reads the snapshot. A test replaces it to simulate the
// writer renaming a new file into place between the inspection and the read.
var readProgressFile = func(path string) ([]byte, error) {
	return readBoundedFile(path, maxRunProgress)
}

// progressReadAttempts is how many times a replace under the open is tried
// before the snapshot is reported unreadable. A missing file is not retried.
const progressReadAttempts = 3

func readRunProgress(stateDir string) (runProgress, error) {
	if stateDir == "" {
		return runProgress{}, errNoStateDir
	}
	var raw []byte
	var err error
	for range progressReadAttempts {
		raw, err = readProgressFile(runsPath(stateDir, runProgressFile))
		if !errors.Is(err, errChangedWhileOpen) {
			break
		}
	}
	if err != nil {
		return runProgress{}, err
	}
	var p runProgress
	if err := decodeObject(raw, &p, false); err != nil {
		return runProgress{}, err
	}
	if p.V != runProgressVersion {
		return runProgress{}, fmt.Errorf("progress version %d", p.V)
	}
	if err := p.sane(); err != nil {
		return runProgress{}, err
	}
	return p, nil
}

// removeRunProgress deletes the snapshot. A missing file is success. A symlink
// leaf is unlinked and not followed. A directory is an error, not removed
// (os.Remove would delete an empty directory).
func removeRunProgress(stateDir string) error {
	if stateDir == "" {
		return nil
	}
	path := runsPath(stateDir, runProgressFile)
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory", runProgressFile)
	}
	return os.Remove(path)
}

// liveNote is the function the running app calls as a stage changes. The
// daemon's pid is stable across runs, so the function reads the current run
// id when it is called rather than capturing one id at bind time. Nil is a
// no-op, which is what a test App that was not wired has.
type liveNote struct {
	mu sync.Mutex
	fn func(runProgress)
}

func (n *liveNote) set(fn func(runProgress)) {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.fn = fn
	n.mu.Unlock()
}

func (n *liveNote) call(p runProgress) {
	if n == nil {
		return
	}
	n.mu.Lock()
	fn := n.fn
	n.mu.Unlock()
	if fn != nil {
		fn(p)
	}
}

// BindLive installs the function called as a run moves. A nil function, or an
// app that was not wired with a live box, does nothing.
func (a *App) BindLive(fn func(runProgress)) {
	if a == nil || a.live == nil {
		return
	}
	a.live.set(fn)
}

func (e *env) bindLive(app *App) {
	if e == nil || app == nil {
		return
	}
	app.BindLive(e.noteProgress)
}

// beginLive opens the snapshot for id. The run has already written its
// running record. A write error is warned once and does not fail the run.
func (e *env) beginLive(id string) {
	if e == nil || id == "" {
		return
	}
	e.liveMu.Lock()
	e.liveRun = id
	e.liveWarned = false
	e.liveMu.Unlock()
	e.noteProgress(runProgress{Stage: progressStarting})
}

// endLive drops the snapshot and forgets the run id. Safe when beginLive
// wrote nothing. Registered with defer so a cancelled, panicked or
// config-restarted run does not leave a portal on screen.
func (e *env) endLive() {
	if e == nil {
		return
	}
	e.liveMu.Lock()
	e.liveRun = ""
	dir := e.stateDir
	e.liveMu.Unlock()
	if err := removeRunProgress(dir); err != nil {
		e.liveMu.Lock()
		e.warnLiveLocked(err)
		e.liveMu.Unlock()
	}
}

// noteProgress rewrites the snapshot for the current run. It does nothing
// when no run is live or the state dir is empty (never the working directory).
// Names are bounded here. The lock is held across the write so endLive cannot
// delete the file and then lose a rewrite that recreates it.
func (e *env) noteProgress(p runProgress) {
	if e == nil {
		return
	}
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	if e.liveRun == "" || e.stateDir == "" {
		return
	}
	p.V = runProgressVersion
	p.PID = os.Getpid()
	p.ID = e.liveRun
	p.At = now()
	p.ATS = boundProgressName(p.ATS)
	p.Token = boundProgressName(p.Token)
	p.Company = boundProgressName(p.Company)
	if err := writeRunProgress(e.stateDir, p); err != nil {
		e.warnLiveLocked(err)
	}
}

// boundProgressName is one line at the board-name cap. CleanText's ellipsis
// can land one rune past the cap, and a snapshot that does not fit is refused
// rather than written.
func boundProgressName(s string) string {
	s = oneLine(s, maxBoardNameRunes)
	if utf8.RuneCountInString(s) <= maxBoardNameRunes {
		return s
	}
	return string([]rune(s)[:maxBoardNameRunes-1]) + "…"
}

func (e *env) warnLiveLocked(err error) {
	if e.liveWarned || err == nil {
		return
	}
	e.liveWarned = true
	if e.stderr == nil {
		return
	}
	fmt.Fprintf(e.stderr, "vani: live progress was not written: %s\n", oneLine(err.Error(), 300))
}

// attachProgress is the live snapshot for a record status has already decided
// is running. A missing file is omitted. Anything else that cannot be trusted
// is ok false and names no portal.
func (e *env) attachProgress(r runRecord, at time.Time) *progressFacet {
	p, err := readRunProgress(e.stateDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return &progressFacet{facet: unknown(err)}
	}
	if p.PID != r.PID || p.ID != runID(r) {
		return &progressFacet{facet: facet{Reason: "live progress is from another run"}}
	}
	if p.At.Before(r.StartedAt.Add(-time.Minute)) {
		return &progressFacet{facet: facet{Reason: "live progress is from before this run"}}
	}
	if p.At.After(at.Add(time.Minute)) {
		return &progressFacet{facet: facet{Reason: "live progress time is ahead of this status"}}
	}
	return &progressFacet{
		facet: facet{OK: true}, Stage: p.Stage, Phase: p.Phase,
		Index: p.Index, Total: p.Total, Done: p.Done, Read: p.Read, Failed: p.Failed,
		RateLimited: p.RateLimited, Skipped: p.Skipped, Listed: p.Listed,
		ATS: p.ATS, Token: p.Token, Company: p.Company, Outcome: p.Outcome,
		Attempted: p.Attempted, Limit: p.Limit, Eligible: p.Eligible, UpdatedAt: p.At,
	}
}

func progressLine(f lastRunFacet) string {
	p := f.Progress
	if p == nil {
		return ""
	}
	if !p.OK {
		reason := p.Reason
		if reason == "" {
			reason = "unreadable"
		}
		return "run: live progress could not be read: " + reason
	}
	s := progressSummary(p)
	if s == "" {
		return ""
	}
	return "run: " + s
}

func progressSummary(p *progressFacet) string {
	switch p.Stage {
	case progressListing:
		return listingSummary(p)
	case progressAdvance:
		return advanceSummary(p)
	default:
		return progressStageWord(p.Stage)
	}
}

func boardLabel(p *progressFacet) string {
	if p.Company != "" {
		return p.Company
	}
	if p.ATS == "" {
		return ""
	}
	if p.Token == "" {
		return p.ATS
	}
	return p.ATS + ":" + p.Token
}

func listingSummary(p *progressFacet) string {
	if p.Index == 0 {
		if p.Total > 0 {
			return fmt.Sprintf("reading, %d boards", p.Total)
		}
		return "reading boards"
	}
	verb := "reading"
	switch p.Phase {
	case progressPhaseReconciling:
		verb = "saving"
	case progressPhaseDone:
		verb = doneVerb(p.Outcome)
	}
	name := boardLabel(p)
	if name == "" {
		name = "a board"
	}
	s := fmt.Sprintf("%s %s", verb, name)
	if p.Total > 0 {
		s += fmt.Sprintf(" (%d of %d)", p.Index, p.Total)
	}
	if c := progressCounts(p); c != "" {
		s += "; " + c
	}
	return s
}

// doneVerb is the word for a board that just finished. An empty outcome is
// the older snapshot that did not record one, and it stays "read".
func doneVerb(outcome string) string {
	switch outcome {
	case boardFailed:
		return "could not read"
	case boardRateLimited:
		return "rate-limited"
	case boardSkipped:
		return "skipped"
	default:
		return "read"
	}
}

func progressCounts(p *progressFacet) string {
	var parts []string
	add := func(n int, word string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, word))
		}
	}
	add(p.Read, "read")
	add(p.Failed, "failed")
	add(p.RateLimited, "rate-limited")
	add(p.Skipped, "skipped")
	add(p.Listed, "listed")
	return strings.Join(parts, ", ")
}

func advanceSummary(p *progressFacet) string {
	s := "triaging"
	if p.Limit > 0 {
		s += fmt.Sprintf(", %d of %d started", p.Attempted, p.Limit)
	}
	if p.Limit > 0 && p.Eligible > p.Limit {
		s += fmt.Sprintf(", %d eligible", p.Eligible)
	}
	return s
}

func progressStageWord(stage string) string {
	switch stage {
	case progressStarting:
		return "starting"
	case progressPreparing:
		return "preparing the run"
	case progressInbox:
		return "reading saved listings"
	case progressNearDup:
		return "checking near-duplicates"
	case progressExclusions:
		return "applying exclusions"
	case progressLogos:
		return "fetching company icons"
	case progressWarm:
		return "warming the knowledge base"
	case progressBackfill:
		return "filling in earlier readings"
	case progressDigest:
		return "writing the digest"
	default:
		return oneLine(stage, 80)
	}
}
