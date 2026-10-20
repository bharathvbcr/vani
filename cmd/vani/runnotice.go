package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/bharathvbcr/Vani/internal/daemon"
)

// The daemon runs under launchd, where nobody reads stderr, so a run that
// fails has to say so on screen: silence after a failed run reads as "nothing
// new today". The notice text is a fixed vocabulary keyed on the run's stage
// and stop reason, never the error text, which can carry fetched URLs or page
// content.

var (
	// errDigestHeldBack marks a digest withheld because the Jobs marks could
	// not be read.
	errDigestHeldBack = errors.New("digest held back")
	// errConfigStale marks a run that went on with the previous watchlist
	// because the config file could not be re-read.
	errConfigStale = errors.New("config reload failed")
)

const seeStatus = " Run vani status for details."

// failureNotice is the notification for a finished run, or ok false when the
// run needs none: it succeeded (publishDigest announces the digest) or was
// cancelled (someone stopped it).
func failureNotice(rec runRecord, err error) (title, body string, ok bool) {
	if rec.Outcome != outcomeFailed {
		return "", "", false
	}
	var stopped string
	if rec.Advance != nil {
		stopped = rec.Advance.Stopped
	}
	switch {
	case errors.Is(err, daemon.ErrFatal):
		return "Vani stopped", "The ledger could not be written safely, so Vani stopped. See vani's log.", true
	case errors.Is(err, errDigestHeldBack):
		return "Vani digest held back", "Your Jobs marks could not be read; see vani's log", true
	case stopped == stopUnreadStreak || stopped == stopNoneRead:
		return "Vani run failed", "No decision backend could read the postings; they stay live for the next run." + seeStatus, true
	case stopped == stopFailStreak || stopped == stopNoneAdvanced:
		return "Vani run failed", "Postings kept failing a stage; they wait for the next run." + seeStatus, true
	case rec.Stage == stagePipeline:
		return "Vani run failed", "The pipeline stopped before the digest." + seeStatus, true
	case errors.Is(err, errConfigStale):
		return "Vani config not reloaded", "The config file could not be read, so this run used the previous watchlist." + seeStatus, true
	default:
		return "Vani run failed", "The digest could not be finished." + seeStatus, true
	}
}

// announceFailure posts failureNotice for a finished run. A notifier failure
// is a warning: the run record already holds the outcome.
func (e *env) announceFailure(ctx context.Context, app *App, rec runRecord, err error) {
	title, body, ok := failureNotice(rec, err)
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
func digestBody(roles int, adv *advanceReport) string {
	body := fmt.Sprintf("%d roles to review", roles)
	if roles == 1 {
		body = "1 role to review"
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
