package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The schedule in the config (schedule.daily_at) is only a promise: something
// has to fire it, and the only thing that does is `vani daemon`. With no daemon
// running, `schedule: 08:30 America/Los_Angeles` describes a run that never
// happens, and the runs that do happen (Run in the app, `vani run-once`) each
// triage at most advance_per_run postings, so a backlog just sits there. Status
// therefore says whether the schedule is kept, and when it is not, what that has
// cost: when the last run was, whether a run is overdue, and how many postings
// are waiting for triage.
//
// The facts are additive fields on the schedule facet (status version stays 1:
// an older reader ignores them), and `kept` is a pointer so that "could not tell
// whether a daemon is running" is absent, never false and never true.

// unkeptSchedule is what the schedule not being kept has cost so far. Present
// only when scheduleFacet.Kept is false.
type unkeptSchedule struct {
	// Why is why no daemon is keeping the schedule (daemonFacet.Reason).
	Why string `json:"why,omitempty"`
	// LastRunAt is when the last run finished (started, when it has not
	// finished); absent when no run is recorded or the record is unreadable.
	LastRunAt time.Time `json:"last_run_at,omitzero"`
	// DueAt is the most recent scheduled slot, the one a daemon would have run.
	DueAt time.Time `json:"due_at,omitzero"`
	// Overdue is true when no run has started since DueAt (also when no run is
	// recorded at all). Nil when the last run could not be read.
	Overdue *bool `json:"overdue,omitempty"`
	// AwaitingTriage is the ledger's live postings, those no run has triaged
	// yet; AdvancePerRun is how many one run triages (advance_per_run) and
	// RunsNeeded how many such runs clear them. Each is absent when it could
	// not be read, never 0.
	AwaitingTriage *int `json:"awaiting_triage,omitempty"`
	AdvancePerRun  *int `json:"advance_per_run,omitempty"`
	RunsNeeded     *int `json:"runs_needed,omitempty"`
	// AwaitingTriageUS is how many of AwaitingTriage are located in the US
	// (jobview.InUS), read from the same ledger snapshot: what `run-once
	// --us-first` would triage first. Absent when AwaitingTriage is, or when
	// the count was not made, never 0.
	AwaitingTriageUS *int `json:"awaiting_triage_us,omitempty"`
	// Hint is how to get the schedule kept, and what that needs.
	Hint string `json:"hint"`
}

// scheduleKeeping fills s's Kept and Unkept from the daemon and the last run.
// live is the ledger's live-posting count, nil when the ledger could not be
// read or does not exist. s is returned unchanged when the schedule itself
// could not be read or whether a daemon runs is unknown.
func scheduleKeeping(cfg config.Config, s scheduleFacet, d daemonFacet, last lastRunFacet, live *int, at time.Time) scheduleFacet {
	if !s.OK || !d.OK {
		return s
	}
	kept := d.Running
	s.Kept = &kept
	if kept {
		return s
	}
	u := &unkeptSchedule{Why: d.Reason, Hint: keepHint(cfg)}
	if h, m, loc, err := cfg.Schedule.Parsed(); err == nil {
		u.DueAt = daemon.PrevRun(at, h, m, loc).UTC()
	}
	switch last.State {
	case "unknown":
	case "none":
		u.Overdue = new(true)
	default:
		u.LastRunAt = last.FinishedAt
		if u.LastRunAt.IsZero() {
			u.LastRunAt = last.StartedAt
		}
		if !u.DueAt.IsZero() {
			u.Overdue = new(last.StartedAt.Before(u.DueAt))
		}
	}
	if live != nil {
		n, limit := *live, cfg.AdvanceLimit()
		runs := (n + limit - 1) / limit
		u.AwaitingTriage, u.AdvancePerRun, u.RunsNeeded = &n, &limit, &runs
	}
	s.Unkept = u
	return s
}

// liveInUS counts the live postings (the ones awaiting triage, as status counts
// them: marked or not) whose location is in the US. It reads the records the
// caller already holds, so it is the same snapshot as the count beside it.
func liveInUS(recs []ledger.Record) int {
	n := 0
	for _, r := range recs {
		if r.Status == posting.Live && jobview.InUS(r.Posting) {
			n++
		}
	}
	return n
}

// withAwaitingUS adds the US part of the awaiting-triage count to an unkept
// schedule. It does nothing when the schedule is kept, the backlog was not
// counted, or us is nil.
func withAwaitingUS(s scheduleFacet, us *int) scheduleFacet {
	if s.Unkept != nil && s.Unkept.AwaitingTriage != nil && us != nil {
		s.Unkept.AwaitingTriageUS = us
	}
	return s
}

// keepHint is what to do about an unkept schedule. The Mac app keeps it: its
// Pipeline tab's "Run on schedule" registers a login item that runs `vani
// daemon` with the decision-model key read from the login Keychain into the
// child's environment only. Without the app, `vani launchd plist` prints an
// agent that starts the daemon. Its environment holds only VANI_CONFIG and
// VANI_STATE_DIR, so the key is not in the environment (decide.openrouter_key_env);
// the daemon then reads the login Keychain item itself, once, at its start
// (keychain.go), unless VANI_NO_KEYCHAIN is set or the environment names the
// key. Without a key the daemon's runs log "openrouter (...) disabled" and read
// no posting with the model, so a posting stays live (the daemon says so in
// daemon.json, and status reports it: daemonLines).
func keepHint(cfg config.Config) string {
	// The app's toggle is the way: it holds the key for the daemon. The plain
	// launchd agent is named only as the route without the app, and never with
	// advice to write the key into its plist.
	var b strings.Builder
	b.WriteString("turn on Run on schedule in Vani Apply's Pipeline tab: it runs the daemon as a login item")
	if cfg.OpenRouterConfigured() {
		b.WriteString(", with the decision model's key read from the Keychain and never written to disk")
	}
	b.WriteString(". Until then, Run in the app with a higher advance cap, or `vani run-once --advance N`. " +
		"Without the app, `vani launchd plist` prints an agent for the daemon (vani installs nothing)")
	if cfg.OpenRouterConfigured() {
		fmt.Fprintf(&b, "; that agent carries no model key (%s is not in its environment), so the daemon reads the login Keychain item "+
			"openrouter-api-key itself when it starts (macOS may ask once to let vani read it; VANI_NO_KEYCHAIN=1 turns that off), "+
			"and without a key it triages nothing (`vani status` says so)", cfg.Decide.OpenRouterKeyEnv)
	}
	return b.String()
}

// unkeptLines is the text status's account of an unkept schedule: one line for
// the fact, then what it has cost, then the way out.
func unkeptLines(s scheduleFacet, at time.Time) []string {
	u := s.Unkept
	if u == nil {
		return nil
	}
	loc := time.UTC
	if l, err := time.LoadLocation(s.Timezone); err == nil {
		loc = l
	}
	why := u.Why
	if why == "" {
		why = "no daemon is running"
	}
	lines := []string{"schedule not kept: nothing fires it (" + why + "); runs happen only when you start one"}
	switch {
	case !u.LastRunAt.IsZero():
		line := fmt.Sprintf("  last run: %s (%s ago)", u.LastRunAt.In(loc).Format("2006-01-02 15:04 MST"), noticeAge(at.Sub(u.LastRunAt)))
		if u.Overdue != nil && *u.Overdue && !u.DueAt.IsZero() {
			line += fmt.Sprintf("; overdue: the %s run was due %s ago", s.DailyAt, noticeAge(at.Sub(u.DueAt)))
		}
		lines = append(lines, line)
	case u.Overdue != nil && *u.Overdue:
		lines = append(lines, "  last run: none recorded; overdue")
	default:
		lines = append(lines, "  last run: unknown (the run record could not be read)")
	}
	switch {
	case u.AwaitingTriage == nil:
		lines = append(lines, "  awaiting triage: unknown (the ledger could not be counted)")
	case *u.AwaitingTriage > 0:
		inUS := ""
		if u.AwaitingTriageUS != nil {
			inUS = fmt.Sprintf(" (%d in the US)", *u.AwaitingTriageUS)
		}
		lines = append(lines, fmt.Sprintf("  awaiting triage: %s%s, %s at advance_per_run %d",
			plural(*u.AwaitingTriage, "posting"), inUS, plural(*u.RunsNeeded, "run"), *u.AdvancePerRun))
	}
	return append(lines, "  fix: "+u.Hint)
}
