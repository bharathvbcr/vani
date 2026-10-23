package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/bharathvbcr/Vani/internal/pipeline"
)

// The daemon runs under launchd, where nobody reads stderr, so a run that
// fails has to say so on screen: silence after a failed run reads as "nothing
// new today". The notice text is a fixed vocabulary keyed on the run's cause
// code, stage and stop reason, never the error text, which can carry fetched
// URLs or page content.

var (
	// errDigestHeldBack marks a digest withheld because the Jobs marks could
	// not be read.
	errDigestHeldBack = errors.New("digest held back")
	// errConfigStale marks a run that went on with the previous watchlist
	// because the config file could not be re-read.
	errConfigStale = errors.New("config reload failed")
	// errRunPanic marks a run that panicked and was recovered (dailyJob).
	errRunPanic = errors.New("panic")
)

const seeStatus = " Run vani status for details."

// failureNotice is the notification for a finished run, or ok false when the
// run needs none: it succeeded (publishDigest announces the digest) or was
// cancelled (someone stopped it). It reads only the record, and decideNotice
// hands the same words to the Mac app: the cause code (runCause), the stop
// reason, then the stage.
func failureNotice(rec runRecord) (title, body string, ok bool) {
	if rec.Outcome != outcomeFailed {
		return "", "", false
	}
	var stopped string
	if rec.Advance != nil {
		stopped = rec.Advance.Stopped
	}
	switch {
	case rec.Cause == causeLedgerFatal:
		return "Vani stopped", "The ledger could not be written safely, so Vani stopped. See vani's log.", true
	case rec.Cause == causeDigestHeldBack:
		return "Vani digest held back", "Your Jobs marks could not be read; see vani's log", true
	case stopped == pipeline.StopUnreadStreak || stopped == pipeline.StopNoneRead:
		return "Vani run failed", "No decision backend could read the postings; they stay live for the next run." + seeStatus, true
	case stopped == pipeline.StopFailStreak || stopped == pipeline.StopNoneAdvanced:
		return "Vani run failed", "Postings kept failing a stage; they wait for the next run." + seeStatus, true
	case rec.Stage == stagePipeline:
		return "Vani run failed", "The pipeline stopped before the digest." + seeStatus, true
	case rec.Cause == causeConfigStale:
		return "Vani config not reloaded", "The config file could not be read, so this run used the previous watchlist." + seeStatus, true
	default:
		return "Vani run failed", "The digest could not be finished." + seeStatus, true
	}
}

// announceFailure posts failureNotice for a finished run. A notifier failure
// is a warning: the run record already holds the outcome.
func (e *env) announceFailure(ctx context.Context, app *App, rec runRecord) {
	title, body, ok := failureNotice(rec)
	if !ok || app.Notifier == nil {
		return
	}
	// The run's own context may be the reason it failed; the notice still goes.
	if nerr := app.Notifier.Notify(context.WithoutCancel(ctx), title, body); nerr != nil {
		fmt.Fprintf(e.stderr, "vani: notification failed: %v\n", nerr)
	}
}

// digestBody is the digest notification's text: how many roles to review and,
// when the advance phase left postings it could not read or score, how many
// wait for the next run.
func digestBody(roles int, adv *pipeline.AdvanceReport, stats map[string]backendStat) string {
	body := fmt.Sprintf("%d roles to review", roles)
	if roles == 1 {
		body = "1 role to review"
	}
	// Lappi is free; a question it could not read went to the paid tiers.
	if n := stats["lappi"].FellThrough; n > 0 {
		body += fmt.Sprintf("; %d question(s) went to the paid model because Lappi was down", n)
	}
	if adv == nil {
		return body
	}
	switch left := adv.Unread + adv.Failed; {
	case left == 1:
		body += "; 1 posting could not be read or scored and waits for the next run"
	case left > 1:
		body += fmt.Sprintf("; %d postings could not be read or scored and wait for the next run", left)
	}
	return body
}

// runNoticeVersion is the version of the notice object in status --json's
// last_run. The Mac app reads exactly this version and refuses any other
// loudly (RunNotice.swift), so a change to the object's shape bumps it.
const runNoticeVersion = 1

// noticeMaxAge is how long after a run ended it is still news: an app opened
// days after a run does not announce it.
const noticeMaxAge = 36 * time.Hour

// maxNoticeReason bounds the untrusted reason text carried into a skip reason.
const maxNoticeReason = 120

// noticeFacet is what the Mac app announces for the last run. Go owns whether
// to announce and every word of it (the daemon's own notices come from the same
// failureNotice and digestBody); the app only posts Title and Body, once per
// Key, and holds the notifier lock. Announce false says why in Reason, so a
// check that could not run never looks like a quiet day.
type noticeFacet struct {
	V        int    `json:"v"`
	Announce bool   `json:"announce"`
	Reason   string `json:"reason,omitempty"`
	Title    string `json:"title,omitempty"`
	Body     string `json:"body,omitempty"`
	// ThreadID groups a day's notices together.
	ThreadID string `json:"thread_id,omitempty"`
	// Key names the run; a run whose key was announced is never announced again.
	Key string `json:"key,omitempty"`
}

// decideNotice is the notice for the last-run facet as of at; the thread is
// the day the run finished in loc. It reads only the facet, so the text never
// carries the run's error text.
func decideNotice(f lastRunFacet, at time.Time, loc *time.Location) noticeFacet {
	skip := func(why string) noticeFacet { return noticeFacet{V: runNoticeVersion, Reason: why} }
	if !f.OK {
		why := "the run record could not be read"
		if f.Reason != "" {
			why += ": " + oneLine(f.Reason, maxNoticeReason)
		}
		return skip(why)
	}
	switch f.State {
	case "none":
		return skip("no run has been recorded yet")
	case outcomeRunning:
		return skip("the run is still going")
	case outcomeCancelled:
		return skip("the run was cancelled")
	case outcomeInterrupted:
		return skip("the run did not finish (its process is gone)")
	case outcomeOK, outcomeFailed:
	default:
		return skip("the run's state is one this vani does not know")
	}
	if f.StartedAt.IsZero() || f.FinishedAt.IsZero() {
		return skip("the run record has no start or end time")
	}
	if at.Sub(f.FinishedAt) > noticeMaxAge {
		return skip("the run finished over " + noticeAge(noticeMaxAge) + " ago")
	}
	n := noticeFacet{
		V: runNoticeVersion, Announce: true,
		Key:      fmt.Sprintf("run-%d", (f.StartedAt.UnixNano()+int64(time.Millisecond)/2)/int64(time.Millisecond)),
		ThreadID: "vani-run-" + f.FinishedAt.In(loc).Format("2006-01-02"),
	}
	if f.State == outcomeFailed {
		n.Title, n.Body, _ = failureNotice(runRecord{Outcome: outcomeFailed, Stage: f.Stage, Cause: f.Cause, runDetail: f.runDetail})
		return n
	}
	roles, known := digestedRoles(f)
	switch {
	case !known:
		return skip("the run record has no ledger counts, so the digest's size is unknown")
	case roles <= 0:
		return skip("the digest listed no roles")
	}
	n.Title, n.Body = "Vani digest", digestBody(roles, f.Advance, f.BackendStats)
	return n
}

// digestedRoles is the roles the run's digest listed, as recorded
// (digest_roles). A record that holds none falls back to the run's own change
// in the ledger's digested count, which undercounts when the same run's
// reconcile moved digested postings to dead or live.
func digestedRoles(f lastRunFacet) (roles int, known bool) {
	if f.DigestRoles != nil {
		return *f.DigestRoles, true
	}
	if f.CountsBefore == nil || f.CountsAfter == nil {
		return 0, false
	}
	return f.CountsAfter["digested"] - f.CountsBefore["digested"], true
}

// noticeAge is "5 min", "3 h", "2 days": the coarsest unit that is not zero.
func noticeAge(d time.Duration) string {
	s := d.Seconds()
	switch {
	case s < 90:
		return "under 2 min"
	case s < 5400:
		return fmt.Sprintf("%d min", int(math.Round(s/60)))
	case s < 172_800:
		return fmt.Sprintf("%d h", int(math.Round(s/3600)))
	default:
		return fmt.Sprintf("%d days", int(math.Round(s/86400)))
	}
}
