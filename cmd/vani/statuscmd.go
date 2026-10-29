package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// `vani status --json` is the pipeline's health in one object: did the last
// daily run happen and how did it end, what the ledger holds, what each paid
// backend has spent today against its cap, and how the decision models are
// configured. The Mac app's Pipeline tab is a client of exactly this contract
// (macos/Sources/VaniApplyKit/Pipeline.swift).
//
// Every section is a facet with its own ok/reason, and a facet that could not
// be read says so; it is never reported as zero, empty or "fine". One facet
// failing never fails the command. The ledger is read with ledger.ReadSnapshot
// (no lock), so the answer comes while a run or the daemon holds the ledger,
// which the text `status` (it opens the ledger) cannot do.

// statusTimeout bounds the whole read, the budget ledger's lock wait included.
// A variable so a test can hold the lock without waiting out the real bound.
var statusTimeout = 8 * time.Second

const (
	statusVersion = 1
	maxSpendFile  = 1 << 20
	spendFileVer  = 1
	nanoPerUSD    = 1e9
)

// facet is the ok/reason pair every section carries. Reason says why a facet
// is not ok, or a harmless note when it is.
type facet struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

func unknown(err error) facet { return facet{Reason: oneLine(err.Error(), 300)} }

type statusJSON struct {
	Version     int           `json:"version"`
	GeneratedAt time.Time     `json:"generated_at"`
	StateDir    string        `json:"state_dir"`
	Config      configFacet   `json:"config"`
	Ledger      ledgerFacet   `json:"ledger"`
	LastRun     lastRunFacet  `json:"last_run"`
	Schedule    scheduleFacet `json:"schedule"`
	Backends    backendsFacet `json:"backends"`
	Decide      decideFacet   `json:"decide"`
	// Daemon and RunRequest are additive (an older reader ignores them): is a
	// daemon holding the ledger, and is a manual run waiting for it.
	Daemon     daemonFacet     `json:"daemon"`
	RunRequest runRequestFacet `json:"run_request"`
	// Paths is additive: where the config and state dir came from and how
	// they compare with the Mac app's settings. Absent when unknown.
	Paths *pathsJSON `json:"paths,omitempty"`
	// LastSkip is the newest daily slot the daemon did not run, when that slot
	// is later than the last real run. Absent when there is nothing to say.
	LastSkip *skipFacet `json:"last_skip,omitempty"`
	// Inbox is the last extension-spool poll. Absent when none has been recorded.
	Inbox *inboxFacet `json:"inbox,omitempty"`
}

// pathsJSON is config.Resolved for status --json.
type pathsJSON struct {
	Config      string `json:"config"`
	ConfigFrom  string `json:"config_from"`
	StateFrom   string `json:"state_dir_from"`
	AppSettings string `json:"app_settings"`
}

// daemonFacet is daemon.json checked against this vani process. Running is
// false when no daemon announced itself, or the recorded pid is not this
// binary: gone, uninspectable, a different executable, or started after the
// announcement.
type daemonFacet struct {
	facet
	Running bool      `json:"running"`
	PID     int       `json:"pid,omitempty"`
	Since   time.Time `json:"since,omitzero"`
	// Model is whether the decision model can answer in the running daemon
	// (daemonmodel.go): absent when the daemon did not say.
	Model *daemonModel `json:"model,omitempty"`
}

// runRequestFacet is the manual run asked of the daemon (runrequest.go).
// Pending is a request it has not picked up; Claimed is one whose run it is
// making now (the run record shows its progress). Stale is a pending request
// past runRequestMaxAge, which the daemon will drop without running.
type runRequestFacet struct {
	facet
	Pending     bool        `json:"pending"`
	Stale       bool        `json:"stale,omitempty"`
	RequestedAt time.Time   `json:"requested_at,omitzero"`
	Options     *runOptions `json:"options,omitempty"`
	Claimed     bool        `json:"claimed"`
	// ClaimedRequestedAt is when the claimed request was made.
	ClaimedRequestedAt time.Time `json:"claimed_requested_at,omitzero"`
}

type configFacet struct {
	facet
	Boards *int `json:"boards,omitempty"`
}

type ledgerFacet struct {
	facet
	// Exists is false before the first run created the ledger: an empty ledger
	// and a missing one (a wrong state folder) are different answers.
	Exists   bool           `json:"exists"`
	Path     string         `json:"path"`
	Total    int            `json:"total"`
	ByStatus map[string]int `json:"by_status"`
	// Torn means a writer was mid-append: the counts are complete up to it.
	Torn bool `json:"torn,omitempty"`
	// liveUS is how many live postings are in the US, counted from the same
	// snapshot as ByStatus; nil when the ledger was not read. It is not part of
	// the ledger facet's wire shape: the schedule facet reports it.
	liveUS *int
}

type lastRunFacet struct {
	facet
	// State is none (never recorded), ok, failed, cancelled, running or
	// interrupted (recorded as started, its process gone), or unknown.
	State          string         `json:"state"`
	StartedAt      time.Time      `json:"started_at,omitzero"`
	FinishedAt     time.Time      `json:"finished_at,omitzero"`
	DurationSec    *float64       `json:"duration_sec,omitempty"`
	Trigger        string         `json:"trigger,omitempty"` // runRecord.Trigger
	Options        *runOptions    `json:"options,omitempty"` // runRecord.Options
	Stage          string         `json:"stage,omitempty"`
	Error          string         `json:"error,omitempty"`
	Cause          string         `json:"cause,omitempty"`        // runRecord.Cause
	DigestRoles    *int           `json:"digest_roles,omitempty"` // runRecord.DigestRoles
	PostingsBefore *int           `json:"postings_before,omitempty"`
	PostingsAfter  *int           `json:"postings_after,omitempty"`
	CountsBefore   map[string]int `json:"counts_before,omitempty"`
	CountsAfter    map[string]int `json:"counts_after,omitempty"`
	// Semantic is the run's knowledge-base warm (runRecord.Semantic).
	Semantic *semanticReport `json:"semantic,omitempty"`
	// DigestWarning is the run record's ranking warning (missing weights, an
	// unreadable knowledge base). Empty when the digest ranked as configured.
	DigestWarning string `json:"digest_warning,omitempty"`
	// Notice is what the Mac app announces for this run (decideNotice): the
	// app posts it and decides nothing about it.
	Notice *noticeFacet `json:"notice,omitempty"`
	// Advance and BackendErrors: what the cap left waiting, and why decision
	// backends failed (runDetail).
	runDetail
	// ImpressionsError is the run record's field: the digest was written but
	// what it showed was not recorded. Empty when there is nothing to say.
	ImpressionsError string `json:"impressions_error,omitempty"`
}

// skipFacet is one skipped daily slot (a history row, not last.json).
type skipFacet struct {
	facet
	Slot       time.Time `json:"slot,omitzero"`
	ObservedAt time.Time `json:"observed_at,omitzero"`
	LateSec    int       `json:"late_sec"`
	Error      string    `json:"error,omitempty"`
}

// inboxFacet is runs/inbox.json. OK is false when the file could not be read.
type inboxFacet struct {
	facet
	At         time.Time `json:"at,omitzero"`
	Error      string    `json:"error,omitempty"`
	Bad        int       `json:"bad,omitempty"`
	Unreadable int       `json:"unreadable,omitempty"`
	Remaining  int       `json:"remaining,omitempty"`
	Folded     int       `json:"folded,omitempty"`
	New        int       `json:"new,omitempty"`
	Duplicate  int       `json:"duplicate,omitempty"`
	Live       int       `json:"live,omitempty"`
}

type scheduleFacet struct {
	facet
	DailyAt  string    `json:"daily_at,omitempty"`
	Timezone string    `json:"timezone,omitempty"`
	NextRun  time.Time `json:"next_run,omitzero"`
	// Kept is whether a daemon is running to fire the schedule; nil when that
	// could not be told. Unkept is what it has cost while Kept is false
	// (schedulekept.go). Both are additive: an older reader ignores them.
	Kept   *bool           `json:"kept,omitempty"`
	Unkept *unkeptSchedule `json:"unkept,omitempty"`
}

type backendsFacet struct {
	facet
	// Day is the UTC day the spend is for: caps roll over at UTC midnight.
	Day   string        `json:"day"`
	Items []backendJSON `json:"items"`
}

type backendJSON struct {
	facet
	Name string `json:"name"`
	Kind string `json:"kind"` // llm, openrouter or openrouter_notes
	// CapUSD is the configured daily cap. SpentUSD and RemainingUSD are absent
	// when the spend could not be read (ok false), never zero.
	CapUSD       float64  `json:"cap_usd"`
	SpentUSD     *float64 `json:"spent_usd,omitempty"`
	RemainingUSD *float64 `json:"remaining_usd,omitempty"`
	Exhausted    bool     `json:"exhausted,omitempty"`
}

type decideFacet struct {
	facet
	Shadow string `json:"shadow,omitempty"` // the configured shadow's role, "" when off
	// LocalConfigured is whether a local decision model is configured; the
	// shadow server is configured when LocalMode is "server" (Vani starts it
	// for each run) and an external one when "endpoint".
	LocalConfigured bool   `json:"local_configured"`
	LocalMode       string `json:"local_mode,omitempty"`
	LocalModel      string `json:"local_model,omitempty"`
	LappiConfigured bool   `json:"lappi_configured"`
}

// statusReport prints the status object. Gathering it cannot fail: what could
// not be read is inside it.
func (e *env) statusReport() error {
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	return e.reply(true, e.buildStatus(ctx), nil, nil)
}

// buildStatus gathers every facet. It changes no state: the one lock it takes
// is the LLM budget ledger's flock (budget.Inspect), on the lock file the
// ledger already has, and only when that day's ledger exists.
func (e *env) buildStatus(ctx context.Context) statusJSON {
	at := now()
	out := statusJSON{Version: statusVersion, GeneratedAt: at.UTC(), StateDir: e.stateDir}
	if p := e.paths; p.ConfigFrom != "" {
		out.Paths = &pathsJSON{Config: e.configPath, ConfigFrom: p.ConfigFrom, StateFrom: p.StateFrom, AppSettings: p.AppSettings}
	}
	cfg, cfgErr := e.loadConfig()
	if cfgErr != nil {
		out.Config = configFacet{facet: unknown(cfgErr)}
	} else {
		n := len(cfg.Watchlist)
		out.Config = configFacet{facet: facet{OK: true}, Boards: &n}
	}
	out.Ledger = e.ledgerFacet()
	out.LastRun = e.lastRunFacet(at)
	out.LastSkip = e.lastSkipFacet()
	out.Inbox = e.inboxFacet()
	if out.LastRun.State == "none" && out.Ledger.Total > 0 {
		// "Nothing has run" would be false: the postings came from runs
		// that kept no record (a vani from before run records) or from
		// another tool writing this ledger.
		out.LastRun.Reason = fmt.Sprintf("no run record, but the ledger holds %d postings from earlier runs that kept none", out.Ledger.Total)
	}
	notice := decideNotice(out.LastRun, at, time.Local)
	out.LastRun.Notice = &notice
	out.Daemon = e.daemonFacet()
	out.Schedule = scheduleOf(cfg, cfgErr, at)
	if cfgErr == nil {
		var live *int
		if out.Ledger.OK && out.Ledger.Exists {
			live = new(out.Ledger.ByStatus[string(posting.Live)])
		}
		out.Schedule = withAwaitingUS(scheduleKeeping(cfg, out.Schedule, out.Daemon, out.LastRun, live, at), out.Ledger.liveUS)
	}
	out.Backends = e.backendsOf(ctx, cfg, cfgErr, at)
	out.Decide = decideOf(cfg, cfgErr)
	out.RunRequest = e.runRequestFacet(at)
	return out
}

func (e *env) daemonFacet() daemonFacet {
	d, err := readDaemonRecord(e.stateDir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return daemonFacet{facet: facet{OK: true, Reason: "no daemon has announced itself"}}
	case err != nil:
		return daemonFacet{facet: unknown(err)}
	}
	since := d.StartedAt
	if !daemonIsLive(d) {
		return daemonFacet{facet: facet{OK: true, Reason: fmt.Sprintf("the daemon recorded as pid %d is gone (it did not exit cleanly)", d.PID)}, PID: d.PID, Since: since}
	}
	return daemonFacet{facet: facet{OK: true}, Running: true, PID: d.PID, Since: since, Model: d.Model}
}

// runRequestFacet reads the pending and the claimed request; it removes
// neither (the daemon owns them).
func (e *env) runRequestFacet(at time.Time) runRequestFacet {
	f := runRequestFacet{facet: facet{OK: true}}
	var problems []string
	switch r, err := readRunRequest(runsPath(e.stateDir, runRequestFile), at); {
	case err == nil, errors.Is(err, errRequestStale):
		f.Pending, f.Stale, f.RequestedAt = true, err != nil, r.RequestedAt
		f.Options = r.Options.recorded()
	case errors.Is(err, os.ErrNotExist):
	default:
		problems = append(problems, "pending request: "+err.Error())
	}
	// A claimed request's run may outlast runRequestMaxAge: its age is no fault.
	switch r, err := readRunRequest(runsPath(e.stateDir, runClaimedFile), at); {
	case err == nil, errors.Is(err, errRequestStale):
		f.Claimed, f.ClaimedRequestedAt = true, r.RequestedAt
	case errors.Is(err, os.ErrNotExist):
	default:
		problems = append(problems, "claimed request: "+err.Error())
	}
	if len(problems) > 0 {
		f.facet = facet{Reason: oneLine(strings.Join(problems, "; "), 300)}
	}
	return f
}

func (e *env) ledgerFacet() ledgerFacet {
	dir := filepath.Join(e.stateDir, "ledger")
	f := ledgerFacet{Path: dir, ByStatus: map[string]int{}}
	snap, err := ledger.ReadSnapshot(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		f.facet = facet{OK: true, Reason: "no ledger yet: no run has created it"}
	case err != nil:
		f.facet = unknown(err)
	default:
		f.facet, f.Exists, f.Total, f.Torn = facet{OK: true}, true, len(snap.Records), snap.Torn
		f.ByStatus = statusCounts(snap.Records)
		f.liveUS = new(liveInUS(snap.Records))
	}
	return f
}

func (e *env) lastRunFacet(at time.Time) lastRunFacet {
	r, err := readRunRecord(e.stateDir)
	switch {
	case err != nil:
		return lastRunFacet{facet: unknown(err), State: "unknown"}
	case r == nil:
		return lastRunFacet{facet: facet{OK: true, Reason: "no run has been recorded yet"}, State: "none"}
	}
	f := lastRunFacet{
		facet: facet{OK: true}, State: runState(*r, at), StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		Trigger: r.Trigger, Options: r.Options, Stage: r.Stage, Error: r.Error, Cause: r.Cause, DigestRoles: r.DigestRoles, CountsBefore: r.CountsBefore, CountsAfter: r.CountsAfter,
		Semantic: r.Semantic, DigestWarning: r.DigestWarning, runDetail: r.runDetail, ImpressionsError: r.ImpressionsError,
	}
	if !r.FinishedAt.IsZero() {
		d := math.Max(r.FinishedAt.Sub(r.StartedAt).Seconds(), 0)
		f.DurationSec = &d
	}
	if r.CountsBefore != nil {
		n := sumCounts(r.CountsBefore)
		f.PostingsBefore = &n
	}
	if r.CountsAfter != nil {
		n := sumCounts(r.CountsAfter)
		f.PostingsAfter = &n
	}
	return f
}

// lastSkipFacet is the newest skipped slot that is later than the last real
// run. A last.json still marked running hides it: that run has not finished,
// and a skip must not be read as what is happening now. History is newest
// first, so the first skip that is not later than the cutoff ends the scan.
func (e *env) lastSkipFacet() *skipFacet {
	last, err := readRunRecord(e.stateDir)
	if err != nil || (last != nil && last.Outcome == outcomeRunning) {
		return nil
	}
	var cutoff time.Time
	if last != nil {
		cutoff = last.FinishedAt
		if cutoff.IsZero() {
			cutoff = last.StartedAt
		}
	}
	files, _, err := listRunHistory(e.stateDir, maxRunHistoryScan)
	if err != nil {
		return &skipFacet{facet: unknown(err)}
	}
	for _, f := range files {
		if !f.Regular {
			continue
		}
		r, rerr := readRunRecordFile(runHistoryPath(e.stateDir, f.ID))
		if rerr != nil || r == nil || r.Outcome != outcomeSkipped {
			continue
		}
		if !r.StartedAt.After(cutoff) {
			return nil
		}
		late := r.FinishedAt.Sub(r.StartedAt)
		if late < 0 {
			late = 0
		}
		return &skipFacet{
			facet: facet{OK: true}, Slot: r.StartedAt, ObservedAt: r.FinishedAt,
			LateSec: int(late.Round(time.Second) / time.Second), Error: r.Error,
		}
	}
	return nil
}

// inboxFacet reports the last inbox poll. A missing file is omitted. A file
// that cannot be read is present and not ok, so an unread poll is not a pass.
func (e *env) inboxFacet() *inboxFacet {
	st, err := readInboxStatus(e.stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return &inboxFacet{facet: unknown(err)}
	}
	return &inboxFacet{
		facet: facet{OK: true}, At: st.At, Error: st.Error,
		Bad: st.Bad, Unreadable: st.Unreadable, Remaining: st.Remaining,
		Folded: st.Folded, New: st.New, Duplicate: st.Duplicate, Live: st.Live,
	}
}

func sumCounts(m map[string]int) int {
	n := 0
	for _, c := range m {
		n += c
	}
	return n
}

func scheduleOf(cfg config.Config, cfgErr error, at time.Time) scheduleFacet {
	if cfgErr != nil {
		return scheduleFacet{facet: facet{Reason: "the config could not be read"}}
	}
	h, m, loc, err := cfg.Schedule.Parsed()
	if err != nil {
		return scheduleFacet{facet: unknown(err), DailyAt: cfg.Schedule.DailyAt, Timezone: cfg.Schedule.Timezone}
	}
	return scheduleFacet{
		facet: facet{OK: true}, DailyAt: cfg.Schedule.DailyAt, Timezone: cfg.Schedule.Timezone,
		NextRun: daemon.NextRun(at, h, m, loc).UTC(),
	}
}

func decideOf(cfg config.Config, cfgErr error) decideFacet {
	if cfgErr != nil {
		return decideFacet{facet: facet{Reason: "the config could not be read"}}
	}
	d := decideFacet{facet: facet{OK: true}, Shadow: cfg.Decide.Shadow, LappiConfigured: cfg.Decide.Lappi != nil}
	if l := cfg.Decide.Local; l != nil {
		d.LocalConfigured, d.LocalModel = true, l.Model
		d.LocalMode = "endpoint"
		if l.Server != nil {
			d.LocalMode = "server"
		}
	}
	return d
}

// backendsOf reports each configured paid backend's spend today. The two
// backends keep their spend in different stores (Manvi's budget ledger for the
// LLM, decide's spend file for OpenRouter), each read the way its owner writes
// it, without taking a write path.
func (e *env) backendsOf(ctx context.Context, cfg config.Config, cfgErr error, at time.Time) backendsFacet {
	day := at.UTC().Format("2006-01-02")
	if cfgErr != nil {
		return backendsFacet{facet: facet{Reason: "the config could not be read, so the caps are not known"}, Day: day, Items: []backendJSON{}}
	}
	out := backendsFacet{facet: facet{OK: true}, Day: day, Items: []backendJSON{}}
	if cfg.OpenRouterConfigured() {
		b := backendJSON{Name: cfg.Decide.OpenRouterName(), Kind: "openrouter", CapUSD: cfg.Decide.DailyUSD.OpenRouter}
		spent, err := readOpenRouterSpend(filepath.Join(e.stateDir, "budget", openRouterBudgetFile), day, openRouterSpendKey)
		out.Items = append(out.Items, b.withSpend(spent, err))
		// The notes' own cap, in its own file beside the triage one.
		nb := backendJSON{Name: b.Name + " notes", Kind: "openrouter_notes", CapUSD: cfg.Funnel.NotesCapUSD()}
		spent, err = readOpenRouterSpend(filepath.Join(e.stateDir, "budget", notesBudgetFile), day, openRouterSpendKey)
		out.Items = append(out.Items, nb.withSpend(spent, err))
	}
	if cfg.LLM.Provider != "" {
		b := backendJSON{Name: cfg.LLM.Provider, Kind: "llm", CapUSD: cfg.Decide.DailyUSD.LLM}
		spent, err := readLLMSpend(ctx, filepath.Join(e.stateDir, "budget", "llm-"+day+".json"))
		out.Items = append(out.Items, b.withSpend(spent, err))
	}
	return out
}

func (b backendJSON) withSpend(spent float64, err error) backendJSON {
	if err != nil {
		b.facet = unknown(err)
		return b
	}
	rem := math.Max(b.CapUSD-spent, 0)
	b.facet, b.SpentUSD, b.RemainingUSD, b.Exhausted = facet{OK: true}, &spent, &rem, spent >= b.CapUSD
	return b
}

// readLLMSpend is today's committed spend (settled, retained and in flight, all
// of which count against the cap) from Manvi's budget ledger. A missing file is
// a day with nothing spent: the ledger is created by the first request.
func readLLMSpend(ctx context.Context, path string) (float64, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	snap, err := budget.Inspect(ctx, path)
	if err != nil {
		return 0, fmt.Errorf("llm budget: %w", err)
	}
	return float64(snap.CommittedNanoUSD()) / nanoPerUSD, nil
}

// readOpenRouterSpend is key's spend for day from decide's DailyCap file
// ({"version":1,"days":{"YYYY-MM-DD":{key:usd}}}). It re-reads that file's
// format because decide has no reader for it (docs/FOLLOWUPS.md); the test
// writes the file with decide.DailyCap itself so a format change fails there.
func readOpenRouterSpend(path, day, key string) (float64, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("openrouter budget: %w", err)
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxSpendFile+1))
	if err != nil {
		return 0, fmt.Errorf("openrouter budget: %w", err)
	}
	if len(raw) > maxSpendFile {
		return 0, fmt.Errorf("openrouter budget: file exceeds %d bytes", maxSpendFile)
	}
	var s struct {
		Version int                           `json:"version"`
		Days    map[string]map[string]float64 `json:"days"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return 0, fmt.Errorf("openrouter budget: file is corrupt: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return 0, errors.New("openrouter budget: file has trailing data")
	}
	if s.Version != spendFileVer {
		return 0, fmt.Errorf("openrouter budget: file version %d, want %d", s.Version, spendFileVer)
	}
	for _, keys := range s.Days {
		for _, v := range keys {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return 0, errors.New("openrouter budget: file holds an invalid amount")
			}
		}
	}
	return s.Days[day][key], nil
}
