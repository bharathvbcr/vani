package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// Board health is reporting. It never changes what reconcile does: when a
// posting dies is decided by internal/pipeline alone (liveness.go).
//
// It is each watched board's memory across runs: when it last gave a good full
// read and how many attempts since were not good. A run record says what one
// run did; a board that fails every run (a host in a 429 cooldown, an API that
// moved) shows as one failed line per run and nothing more, while its stored
// postings sit unrefreshed. Health measures time since the last good read, not
// a run streak: ten manual runs in an afternoon are not ten days. A board is
// stale when that age exceeds boardStaleAfter, so a board no run reaches (a
// run cut short, a daemon that is not running) is named as well as one that
// fails.
//
// A good read is a board that listed. A failed, rate-limited or skipped board
// is not. (A listing that left most of a board's postings absent is a read, and
// reconcile's mass rule has already kept those postings; the run record names
// it.)
//
// The file is <state>/boards/health.json. It never fails a run: a file that
// cannot be read is replaced, with a warning; one that cannot be written is a
// warning and the run record's boards.health_error, and status says how long
// ago the file was last updated. It holds only watched boards (a removed
// board's entry goes; one added back starts fresh), at most
// maxBoardHealthEntries; a watched board without an entry is reported as not
// tracked, never as healthy. Only a run that lists the whole watchlist prunes:
// a run cut to some boards (--board) knows nothing about the others.
//
// The bounds agree: an entry is at most maxBoardHealthEntryBytes as written
// (its key and stored error are bounded, its times and counts are fixed in
// number), so maxBoardHealthEntries of them fit maxBoardHealthFile;
// TestBoardHealthWorstCaseFitsItsFile writes the worst case to prove it.
const (
	boardHealthVersion       = 1
	maxBoardHealthEntries    = 2048
	maxBoardHealthEntryBytes = 4 << 10
	maxBoardHealthFile       = maxBoardHealthEntries*maxBoardHealthEntryBytes + 64<<10
	// maxHealthErrorBytes bounds the reason an entry keeps, in bytes (a run
	// record's one-line error may be 300 runes; the file keeps its start).
	maxHealthErrorBytes = 120
	// boardStaleAfter is how long a board may go without a good read before it
	// is named. With a daily schedule a board that missed one daily read is at
	// most ~24h old; one that missed two is past 36h whatever time of day its
	// last good read was.
	boardStaleAfter = 36 * time.Hour
	// maxStaleListed bounds the boards a run record, the digest and status name
	// one by one; the count stays exact.
	maxStaleListed = 20
	// maxBoardFailCount bounds the attempt counter.
	maxBoardFailCount = 1_000_000
)

func boardHealthPath(stateDir string) string {
	return filepath.Join(stateDir, "boards", "health.json")
}

// boardHealthRec is one board's memory.
type boardHealthRec struct {
	// LastOK is the last good read; zero when none is known.
	LastOK time.Time `json:"last_ok,omitzero"`
	// FailSince is the first attempt since LastOK that was not good; zero
	// while the board reads well.
	FailSince time.Time `json:"fail_since,omitzero"`
	// Fails counts attempts since LastOK that were not good.
	Fails     int       `json:"fails,omitempty"`
	LastTried time.Time `json:"last_tried,omitzero"`
	// Outcome is the last attempt's: read, failed, rate_limited or skipped.
	// LastError is its one-line reason when it was not good.
	Outcome   string `json:"outcome,omitempty"`
	LastError string `json:"last_error,omitempty"`
	// Seeded says the record was rebuilt from the kept run history when the
	// file was first made. Run records keep failed boards and may leave read
	// ones out, so a seeded streak can overstate.
	Seeded bool `json:"seeded,omitempty"`
	// DeferredSince and ShortSince start the streak of good reads in which the
	// board deferred new rows past its detail cap, or listed fewer rows than it
	// counted (boardbacklog.go); zero while the last good read did neither. A
	// failed attempt leaves them as they are.
	DeferredSince time.Time `json:"deferred_since,omitzero"`
	ShortSince    time.Time `json:"short_since,omitzero"`
}

type boardHealthDoc struct {
	Version int `json:"version"`
	// Updated is when a run last folded its boards in; status names a file no
	// run has updated for boardStaleAfter.
	Updated time.Time                 `json:"updated,omitzero"`
	Boards  map[string]boardHealthRec `json:"boards"`
}

// sane returns r with every time stored more than pipeline.MaxStampAhead past
// at (written while the clock ran far ahead) made unreadable: a span it
// started restarts at at, and a last good read it claims is unknown. Without
// it, one run under a wrong clock would hold a board fresh once the clock is
// corrected.
func (r boardHealthRec) sane(at time.Time) boardHealthRec {
	ahead := func(t time.Time) bool { return !t.IsZero() && t.After(at.Add(pipeline.MaxStampAhead)) }
	if ahead(r.LastOK) {
		r.LastOK = time.Time{}
	}
	if ahead(r.FailSince) {
		r.FailSince = at
	}
	if ahead(r.LastTried) {
		r.LastTried = time.Time{}
	}
	if ahead(r.DeferredSince) {
		r.DeferredSince = at
	}
	if ahead(r.ShortSince) {
		r.ShortSince = at
	}
	return r
}

// healthErrorText bounds an error to maxHealthErrorBytes on a rune boundary.
func healthErrorText(s string) string {
	if len(s) <= maxHealthErrorBytes {
		return s
	}
	cut := maxHealthErrorBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// staleBoard is one board named as stale.
type staleBoard struct {
	ATS     string `json:"ats"`
	Token   string `json:"token"`
	Company string `json:"company,omitempty"`
	// LastOK is the last good read, absent when none is known.
	LastOK *time.Time `json:"last_ok,omitempty"`
	// Since is what the age is measured from: LastOK, else the first failure.
	Since   time.Time `json:"since"`
	Fails   int       `json:"fails"`
	Outcome string    `json:"outcome,omitempty"`
	Error   string    `json:"error,omitempty"`
	Seeded  bool      `json:"seeded,omitempty"`
}

// boardHealthKey names a board in the file: its ATS and token as the run
// record bounds them.
func boardHealthKey(ats, token string) string {
	return ats + ":" + oneLine(token, maxBoardNameRunes)
}

// goodRead is whether a tried board's entry is a good full read.
func goodRead(e boardReport) bool { return e.Outcome == boardRead }

// applyAttempt folds one tried board into its record.
func applyAttempt(r boardHealthRec, e boardReport, at time.Time, seeded bool) boardHealthRec {
	r = r.sane(at)
	if goodRead(e) {
		return boardHealthRec{LastOK: at, LastTried: at, Outcome: boardRead, DeferredSince: r.DeferredSince, ShortSince: r.ShortSince}
	}
	r.LastTried = at
	r.Outcome = e.Outcome
	if r.Fails == 0 || r.FailSince.IsZero() {
		r.FailSince = at
	}
	r.Fails = min(r.Fails+1, maxBoardFailCount)
	r.LastError = healthErrorText(oneLine(e.Error, maxBoardErrorRunes))
	r.Seeded = r.Seeded || seeded
	return r
}

// isStale reports whether r has gone more than boardStaleAfter without a good
// read: since is LastOK, else the first failure. A record with neither is
// unknown, not stale. A time a little ahead of at (the clock was set back) reads
// as fresh, never as old; one far ahead is unreadable (sane).
func (r boardHealthRec) isStale(at time.Time) (since time.Time, stale bool) {
	r = r.sane(at)
	since = r.LastOK
	if since.IsZero() {
		since = r.FailSince
	}
	if since.IsZero() || since.After(at) {
		return since, false
	}
	return since, at.Sub(since) > boardStaleAfter
}

// boardHealth is the file for one run: loaded (or seeded) before the listing,
// written once after.
type boardHealth struct {
	path string
	doc  boardHealthDoc
	// fetches are this run's per-board fetch counts, set before record so a
	// good read can start or end a deferred or short streak (boardbacklog.go).
	fetches map[boardKey]boardFetch
}

// openBoardHealth loads the file, seeding it from the kept run history when
// there is none. Nil only for an empty state dir.
func openBoardHealth(stateDir string, warnf func(string, ...any)) *boardHealth {
	if stateDir == "" {
		return nil
	}
	h := &boardHealth{path: boardHealthPath(stateDir)}
	doc, ok, warn := loadBoardHealth(h.path)
	if warn != "" {
		warnf("%s", warn)
	}
	if !ok && warn == "" {
		seedBoardHealth(stateDir, &doc)
	}
	h.doc = doc
	return h
}

// loadBoardHealth reads the file. ok is false when there is none (the caller
// may seed it); a file that cannot be used is none too, with a warning.
func loadBoardHealth(path string) (doc boardHealthDoc, ok bool, warn string) {
	doc = boardHealthDoc{Version: boardHealthVersion, Boards: map[string]boardHealthRec{}}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return doc, false, ""
	}
	if err != nil {
		return doc, false, fmt.Sprintf("board health not read: %v", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxBoardHealthFile+1))
	if err != nil {
		return doc, false, fmt.Sprintf("board health not read: %v", err)
	}
	if len(raw) > maxBoardHealthFile {
		return doc, false, fmt.Sprintf("board health ignored: %s is over %d bytes", path, maxBoardHealthFile)
	}
	var got boardHealthDoc
	if err := json.Unmarshal(raw, &got); err != nil || got.Version != boardHealthVersion {
		return doc, false, fmt.Sprintf("board health ignored: %s is not a version %d board health file", path, boardHealthVersion)
	}
	doc.Updated = got.Updated
	for k, r := range got.Boards {
		if len(doc.Boards) >= maxBoardHealthEntries {
			break
		}
		if k == "" || len(k) > 4*maxBoardNameRunes || r.Fails < 0 {
			continue
		}
		r.Fails = min(r.Fails, maxBoardFailCount)
		r.LastError = healthErrorText(oneLine(r.LastError, maxBoardErrorRunes))
		doc.Boards[k] = r
	}
	return doc, true, ""
}

// seedBoardHealth rebuilds records from the kept run history, oldest first,
// from the boards each record names. A board a record left out is unknown for
// that run and changes nothing.
func seedBoardHealth(stateDir string, doc *boardHealthDoc) {
	files, _, err := listRunHistory(stateDir, maxRunHistoryScan)
	if err != nil {
		return
	}
	for i := len(files) - 1; i >= 0; i-- {
		f := files[i]
		if !f.Regular {
			continue
		}
		r, err := readRunRecordFile(runHistoryPath(stateDir, f.ID))
		if err != nil || r == nil || r.Boards == nil || r.StartedAt.IsZero() {
			continue
		}
		for _, it := range r.Boards.Items {
			k := boardHealthKey(it.ATS, it.Token)
			if _, known := doc.Boards[k]; !known && len(doc.Boards) >= maxBoardHealthEntries {
				continue
			}
			doc.Boards[k] = applyAttempt(doc.Boards[k], it, r.StartedAt, true)
		}
	}
}

// record folds this run's tried boards in and writes the file. It returns the
// stale boards among watched (at most maxStaleListed, most overdue first), how
// many there are, and why the file could not be saved ("" when it was).
//
// watched is the config's whole watchlist, not this run's cut of it (a run
// limited to some boards says nothing about the others). Only a complete
// listing (every board of the run reached) prunes the boards watched no longer
// names; a listing the run cut short folds what it tried and prunes nothing.
// An entry marked endedRun (the board whose failure was the run's, a cancel
// or a ledger error) is left out: it says nothing about the board.
func (h *boardHealth) record(watched []sources.Board, tried []boardReport, complete bool, at time.Time,
	warnf func(string, ...any)) (stale []staleBoard, n int, saveErr string) {
	if h == nil {
		return nil, 0, ""
	}
	for _, e := range tried {
		if e.endedRun {
			continue
		}
		k := boardHealthKey(e.ATS, e.Token)
		if _, known := h.doc.Boards[k]; !known && len(h.doc.Boards) >= maxBoardHealthEntries {
			continue
		}
		h.doc.Boards[k] = trackBacklog(applyAttempt(h.doc.Boards[k], e, at, false), e, h.fetches[boardKey{e.ATS, e.Token}], at)
	}
	if complete && len(watched) > 0 {
		keys := watchedHealthKeys(watched)
		for k := range h.doc.Boards {
			if _, ok := keys[k]; !ok {
				delete(h.doc.Boards, k)
			}
		}
	}
	h.doc.Updated = at
	if err := saveBoardHealth(h.path, h.doc); err != nil {
		saveErr = healthErrorText(oneLine(err.Error(), maxBoardErrorRunes))
		warnf("board health not saved: %v", err)
	}
	stale, n = staleOf(h.doc, watched, at)
	return stale, n, saveErr
}

func watchedHealthKeys(watchlist []sources.Board) map[string]sources.Board {
	out := make(map[string]sources.Board, len(watchlist))
	for _, b := range watchlist {
		out[boardHealthKey(string(b.ATS), b.Token)] = b
	}
	return out
}

// staleOf lists the watched boards that are stale at at, most overdue first
// (then by key, so the order is stable), cut at maxStaleListed.
func staleOf(doc boardHealthDoc, watchlist []sources.Board, at time.Time) ([]staleBoard, int) {
	var out []staleBoard
	for k, b := range watchedHealthKeys(watchlist) {
		r, ok := doc.Boards[k]
		if !ok {
			continue
		}
		since, stale := r.isStale(at)
		if !stale {
			continue
		}
		r = r.sane(at)
		s := staleBoard{ATS: string(b.ATS), Token: oneLine(b.Token, maxBoardNameRunes), Company: oneLine(b.Company, maxBoardNameRunes),
			Since: since, Fails: r.Fails, Outcome: r.Outcome, Error: r.LastError, Seeded: r.Seeded}
		if !r.LastOK.IsZero() {
			t := r.LastOK
			s.LastOK = &t
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Since.Equal(out[j].Since) {
			return out[i].Since.Before(out[j].Since)
		}
		return out[i].ATS+":"+out[i].Token < out[j].ATS+":"+out[j].Token
	})
	n := len(out)
	if len(out) > maxStaleListed {
		out = out[:maxStaleListed]
	}
	return out, n
}

// encodeBoardHealth is the file's bytes. HTML characters are written as they
// are (not <), so an entry's size is bounded by its fields' bounds.
func encodeBoardHealth(doc boardHealthDoc) ([]byte, error) {
	doc.Version = boardHealthVersion
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func saveBoardHealth(path string, doc boardHealthDoc) error {
	raw, err := encodeBoardHealth(doc)
	if err != nil {
		return err
	}
	if len(raw) > maxBoardHealthFile {
		return fmt.Errorf("%d bytes is over the %d byte bound", len(raw), maxBoardHealthFile)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, raw)
}

// untrackedOf lists the watched boards the file holds no entry for (at most
// maxStaleListed, in watchlist order) and how many there are: never read since
// board health began, added since, or past maxBoardHealthEntries. Status
// names them so that "no stale board" never covers a board nothing judged.
func untrackedOf(doc boardHealthDoc, watchlist []sources.Board) ([]string, int) {
	var out []string
	n := 0
	seen := map[string]bool{}
	for _, b := range watchlist {
		k := boardHealthKey(string(b.ATS), b.Token)
		if seen[k] {
			continue
		}
		seen[k] = true
		if _, ok := doc.Boards[k]; ok {
			continue
		}
		n++
		if len(out) < maxStaleListed {
			out = append(out, k)
		}
	}
	return out, n
}

// boardHealthView is status's view of the file judged at at.
type boardHealthView struct {
	Stale      []staleBoard
	StaleCount int
	Untracked  []string
	UntrackedN int
	Updated    time.Time
	Tracked    bool
	Warn       string
}

// readBoardHealthView reads the file as it stands, judged at at. It writes
// nothing and seeds nothing. Tracked is false when there is no file yet.
func readBoardHealthView(stateDir string, watchlist []sources.Board, at time.Time) boardHealthView {
	doc, ok, warn := loadBoardHealth(boardHealthPath(stateDir))
	if !ok {
		return boardHealthView{Warn: warn}
	}
	v := boardHealthView{Tracked: true, Updated: doc.Updated}
	v.Stale, v.StaleCount = staleOf(doc, watchlist, at)
	v.Untracked, v.UntrackedN = untrackedOf(doc, watchlist)
	return v
}

// staleBoardLine is one stale board for a person: name, how long, why.
func staleBoardLine(s staleBoard, at time.Time) string {
	name := s.ATS + ":" + s.Token
	if s.Company != "" {
		name = s.Company + " (" + name + ")"
	}
	age := at.Sub(s.Since).Round(time.Hour)
	var when string
	switch {
	case s.LastOK != nil && s.Fails == 0:
		when = fmt.Sprintf("no good read for %s (last %s); no run has tried it since", age, s.LastOK.UTC().Format("2006-01-02 15:04 UTC"))
	case s.LastOK != nil:
		when = fmt.Sprintf("no good read for %s (last %s)", age, s.LastOK.UTC().Format("2006-01-02 15:04 UTC"))
	default:
		when = fmt.Sprintf("no good read on record; failing for %s", age)
	}
	line := name + ": " + when
	if s.Fails > 0 {
		line += fmt.Sprintf(", %d attempt(s) since", s.Fails)
	}
	if s.Outcome != "" && s.Fails > 0 {
		line += ", last " + s.Outcome
	}
	if s.Error != "" {
		line += ": " + s.Error
	}
	if s.Seeded {
		line += " [rebuilt from run history]"
	}
	return line
}
