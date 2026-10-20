package notify

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	// AppBundleID is the Vani Apply Mac app (macos/Support/App-Info.plist).
	AppBundleID = "dev.vbcr.vani.apply"
	// AnnounceArgument asks a launched app to announce the run that brought it
	// up (AnnounceLaunch in macos/Sources/VaniApplyKit/RunNotice.swift).
	AnnounceArgument = "-VaniApplyAnnounce"

	openPath = "/usr/bin/open"
	// handoffWait bounds how long the daemon waits for the launched app to
	// take the notifier lock, and handoffPoll is how often it looks.
	handoffWait = 20 * time.Second
	handoffPoll = 250 * time.Millisecond
)

// ViaApp returns a Notifier that hands the notice to the Vani Apply app, so it
// is shown as the app's rather than Script Editor's: it launches the app in
// the background, and the app reads the run from `vani status` and announces
// it (one notice per run). title and body are not passed on; the app words the
// run itself, as it does while it runs.
//
// The handoff counts only once the app holds the lock at lockPath, the
// daemon's own state dir. Otherwise (the app is not installed, the launch
// failed, or the app watches another state dir) the notice goes through
// fallback: a duplicate is better than silence.
//
// Why a handoff fell back is written to warn (the daemon's log), so a notice
// from Script Editor can be traced. A nil runner uses the real /usr/bin/open;
// a nil warn discards.
func ViaApp(lockPath string, r Runner, fallback Notifier, warn io.Writer) Notifier {
	if r == nil {
		r = execRunner{}
	}
	if warn == nil {
		warn = io.Discard
	}
	return viaApp{lockPath: lockPath, runner: r, fallback: fallback, warn: warn, wait: handoffWait, poll: handoffPoll}
}

type viaApp struct {
	lockPath   string
	runner     Runner
	fallback   Notifier
	warn       io.Writer
	wait, poll time.Duration
}

// Notify implements Notifier.
func (v viaApp) Notify(ctx context.Context, title, body string) error {
	err := v.handOff(ctx)
	if err == nil {
		return nil
	}
	fmt.Fprintf(v.warn, "vani: notice posted through osascript: %v\n", err)
	return v.fallback.Notify(ctx, title, body)
}

// handOff launches the app and waits for it to take the lock.
func (v viaApp) handOff(ctx context.Context) error {
	octx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	// -g keeps the app in the background and -j launches it hidden.
	out, err := v.runner.Run(octx, openPath, "-g", "-j", "-b", AppBundleID, "--args", AnnounceArgument, "YES")
	if err != nil {
		return fmt.Errorf("notify: open %s: %w (%s)", AppBundleID, err, strings.TrimSpace(string(out)))
	}
	deadline := time.NewTimer(v.wait)
	defer deadline.Stop()
	tick := time.NewTicker(v.poll)
	defer tick.Stop()
	for !appHolds(v.lockPath) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("notify: %s did not take %s within %s", AppBundleID, v.lockPath, v.wait)
		case <-tick.C:
		}
	}
	return nil
}
