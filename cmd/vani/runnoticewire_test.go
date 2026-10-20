package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The run notice is what `vani status --json` tells the Mac app to announce:
// Go owns whether to announce, the words, the key and the version; the app
// only posts. These cases are the Swift RunNotice.decide tests, moved here when
// that logic was deleted, plus a golden file per case that the app's tests
// replay (macos/Tests/VaniApplyKitTests/RunNoticeTests.swift).

var updateNoticeGolden = flag.Bool("update", false, "rewrite the run-notice golden fixtures under macos/Tests")

var (
	noticeNow      = time.Unix(1_792_000_000, 0).UTC() // 2026-10-14
	noticeFinished = noticeNow.Add(-time.Hour)
	noticeStarted  = noticeFinished.Add(-5 * time.Minute)
)

const noticeSee = " Run vani status for details."

func noticeRun(state string, mod func(*lastRunFacet)) lastRunFacet {
	f := lastRunFacet{
		facet: facet{OK: true}, State: state, StartedAt: noticeStarted, FinishedAt: noticeFinished,
		CountsBefore: map[string]int{"digested": 300, "live": 40},
		CountsAfter:  map[string]int{"digested": 312, "live": 28},
	}
	if mod != nil {
		mod(&f)
	}
	return f
}

func TestRunNoticeDecision(t *testing.T) {
	unread := &advanceReport{Eligible: 9, Limit: 9, Attempted: 5, Unread: 5, Deferred: 4, Stopped: stopUnreadStreak}
	unreadBody := "No decision backend could read the postings; they stay live for the next run." + noticeSee
	stopped := [2]string{"Vani stopped", "The ledger could not be written safely, so Vani stopped. See vani's log."}
	held := [2]string{"Vani digest held back", "Your Jobs marks could not be read; see vani's log"}
	pipeline := [2]string{"Vani run failed", "The pipeline stopped before the digest." + noticeSee}
	digestFailed := [2]string{"Vani run failed", "The digest could not be finished." + noticeSee}
	cases := []struct {
		name   string
		f      lastRunFacet
		at     time.Time
		title  string
		body   string
		reason string // wanted skip reason; "" means announce
	}{
		{"digest counts from ledger", noticeRun("ok", nil), noticeNow, "Vani digest", "12 roles to review", ""},
		{"digest roles recorded", noticeRun("ok", func(f *lastRunFacet) { f.DigestRoles = intPtr(3) }), noticeNow, "Vani digest", "3 roles to review", ""},
		{"digest roles beat unchanged counts", noticeRun("ok", func(f *lastRunFacet) {
			f.DigestRoles = intPtr(12)
			f.CountsAfter = f.CountsBefore
		}), noticeNow, "Vani digest", "12 roles to review", ""},
		{"one role, no counts", noticeRun("ok", func(f *lastRunFacet) {
			f.DigestRoles, f.CountsBefore, f.CountsAfter = intPtr(1), nil, nil
		}), noticeNow, "Vani digest", "1 role to review", ""},
		{"digest with unread postings", noticeRun("ok", func(f *lastRunFacet) {
			f.DigestRoles = intPtr(2)
			f.Advance = &advanceReport{Unread: 1}
		}), noticeNow, "Vani digest", "2 roles to review; 1 posting could not be read or scored and waits for the next run", ""},
		{"digest with many left", noticeRun("ok", func(f *lastRunFacet) {
			f.DigestRoles = intPtr(5)
			f.Advance = &advanceReport{Unread: 2, Failed: 3}
		}), noticeNow, "Vani digest", "5 roles to review; 5 postings could not be read or scored and wait for the next run", ""},
		{"empty digest", noticeRun("ok", func(f *lastRunFacet) { f.CountsAfter = f.CountsBefore }), noticeNow, "", "", "the digest listed no roles"},
		{"fallen count", noticeRun("ok", func(f *lastRunFacet) { f.CountsAfter = map[string]int{"digested": 299} }), noticeNow, "", "", "the digest listed no roles"},
		{"zero digest roles", noticeRun("ok", func(f *lastRunFacet) { f.DigestRoles = intPtr(0) }), noticeNow, "", "", "the digest listed no roles"},
		{"no counts", noticeRun("ok", func(f *lastRunFacet) { f.CountsBefore, f.CountsAfter = nil, nil }), noticeNow, "", "",
			"the run record has no ledger counts, so the digest's size is unknown"},

		{"unread streak", noticeRun("failed", func(f *lastRunFacet) { f.Stage, f.Advance = stagePipeline, unread }), noticeNow, "Vani run failed", unreadBody, ""},
		{"none read", noticeRun("failed", func(f *lastRunFacet) {
			f.Stage, f.Advance = stageDigest, &advanceReport{Stopped: stopNoneRead}
		}), noticeNow, "Vani run failed", unreadBody, ""},
		{"fail streak", noticeRun("failed", func(f *lastRunFacet) {
			f.Stage, f.Advance = stagePipeline, &advanceReport{Stopped: stopFailStreak}
		}), noticeNow, "Vani run failed", "Postings kept failing a stage; they wait for the next run." + noticeSee, ""},
		{"none advanced", noticeRun("failed", func(f *lastRunFacet) {
			f.Stage, f.Advance = stageDigest, &advanceReport{Stopped: stopNoneAdvanced}
		}), noticeNow, "Vani run failed", "Postings kept failing a stage; they wait for the next run." + noticeSee, ""},
		{"pipeline stage", noticeRun("failed", func(f *lastRunFacet) { f.Stage = stagePipeline }), noticeNow, pipeline[0], pipeline[1], ""},
		{"cap stop at pipeline", noticeRun("failed", func(f *lastRunFacet) {
			f.Stage, f.Advance = stagePipeline, &advanceReport{Stopped: stopCap}
		}), noticeNow, pipeline[0], pipeline[1], ""},
		{"digest stage", noticeRun("failed", func(f *lastRunFacet) { f.Stage = stageDigest }), noticeNow, digestFailed[0], digestFailed[1], ""},
		{"no stage", noticeRun("failed", nil), noticeNow, digestFailed[0], digestFailed[1], ""},
		{"unknown stop reason", noticeRun("failed", func(f *lastRunFacet) {
			f.Stage, f.Advance = stageDigest, &advanceReport{Stopped: "new_reason"}
		}), noticeNow, digestFailed[0], digestFailed[1], ""},
		{"ledger fatal", noticeRun("failed", func(f *lastRunFacet) { f.Stage, f.Cause = stageDigest, causeLedgerFatal }), noticeNow, stopped[0], stopped[1], ""},
		{"ledger fatal beats stop reason", noticeRun("failed", func(f *lastRunFacet) {
			f.Stage, f.Cause, f.Advance = stagePipeline, causeLedgerFatal, unread
		}), noticeNow, stopped[0], stopped[1], ""},
		{"digest held back", noticeRun("failed", func(f *lastRunFacet) { f.Stage, f.Cause = stageDigest, causeDigestHeldBack }), noticeNow, held[0], held[1], ""},
		{"held back beats stop reason", noticeRun("failed", func(f *lastRunFacet) {
			f.Stage, f.Cause, f.Advance = stageDigest, causeDigestHeldBack, unread
		}), noticeNow, held[0], held[1], ""},
		{"config stale", noticeRun("failed", func(f *lastRunFacet) { f.Stage, f.Cause = stageDigest, causeConfigStale }), noticeNow,
			"Vani config not reloaded", "The config file could not be read, so this run used the previous watchlist." + noticeSee, ""},
		{"config stale ranks below pipeline stage", noticeRun("failed", func(f *lastRunFacet) { f.Stage, f.Cause = stagePipeline, causeConfigStale }), noticeNow,
			pipeline[0], pipeline[1], ""},
		{"config stale ranks below stop reason", noticeRun("failed", func(f *lastRunFacet) {
			f.Stage, f.Cause, f.Advance = stageDigest, causeConfigStale, unread
		}), noticeNow, "Vani run failed", unreadBody, ""},
		{"unknown cause at digest", noticeRun("failed", func(f *lastRunFacet) { f.Stage, f.Cause = stageDigest, "new_cause" }), noticeNow, digestFailed[0], digestFailed[1], ""},
		{"unknown cause at pipeline", noticeRun("failed", func(f *lastRunFacet) { f.Stage, f.Cause = stagePipeline, "new_cause" }), noticeNow, pipeline[0], pipeline[1], ""},
		{"config stale with digest written", noticeRun("failed", func(f *lastRunFacet) {
			f.Stage, f.Cause, f.DigestRoles = stageDigest, causeConfigStale, intPtr(5)
		}), noticeNow, "Vani config not reloaded", "The config file could not be read, so this run used the previous watchlist." + noticeSee, ""},
		{"error text only, no cause", noticeRun("failed", func(f *lastRunFacet) {
			f.Stage, f.Error, f.DigestRoles = stageDigest, "config reload failed; this run uses the previous watchlist", intPtr(5)
		}), noticeNow, digestFailed[0], digestFailed[1], ""},

		{"running", noticeRun("running", func(f *lastRunFacet) { f.FinishedAt = time.Time{} }), noticeNow, "", "", "the run is still going"},
		{"cancelled", noticeRun("cancelled", func(f *lastRunFacet) { f.Stage = stagePipeline }), noticeNow, "", "", "the run was cancelled"},
		{"interrupted", noticeRun("interrupted", func(f *lastRunFacet) { f.FinishedAt = time.Time{} }), noticeNow, "", "", "the run did not finish (its process is gone)"},
		{"unknown state", lastRunFacet{facet: facet{OK: true}, State: "newstate"}, noticeNow, "", "", "the run's state is one this vani does not know"},
		{"never run", lastRunFacet{facet: facet{OK: true, Reason: "no run has been recorded yet"}, State: "none"}, noticeNow, "", "", "no run has been recorded yet"},
		{"corrupt record", lastRunFacet{facet: facet{Reason: "run record is corrupt: unexpected EOF"}, State: "unknown"}, noticeNow, "", "",
			"the run record could not be read: run record is corrupt: unexpected EOF"},
		{"unexplained unreadable", lastRunFacet{State: "unknown"}, noticeNow, "", "", "the run record could not be read"},
		{"failed without start", noticeRun("failed", func(f *lastRunFacet) { f.Stage, f.StartedAt = stageDigest, time.Time{} }), noticeNow, "", "",
			"the run record has no start or end time"},
		{"ok without end", noticeRun("ok", func(f *lastRunFacet) { f.FinishedAt = time.Time{} }), noticeNow, "", "", "the run record has no start or end time"},
		{"just inside the age limit", noticeRun("ok", nil), noticeFinished.Add(noticeMaxAge - time.Second), "Vani digest", "12 roles to review", ""},
		{"past the age limit", noticeRun("ok", nil), noticeFinished.Add(noticeMaxAge + time.Second), "", "",
			"the run finished over 36 h ago"},
	}
	for _, c := range cases {
		n := decideNotice(c.f, c.at, time.UTC)
		if n.V != runNoticeVersion {
			t.Errorf("%s: v = %d", c.name, n.V)
		}
		if c.reason != "" {
			if n.Announce || n.Reason != c.reason || n.Title != "" || n.Body != "" || n.Key != "" {
				t.Errorf("%s: want skip %q, got %+v", c.name, c.reason, n)
			}
			continue
		}
		if !n.Announce || n.Title != c.title || n.Body != c.body || n.Reason != "" {
			t.Errorf("%s: got %+v, want announce %q / %q", c.name, n, c.title, c.body)
		}
		if n.ThreadID != "vani-run-2026-10-14" || n.Key == "" {
			t.Errorf("%s: thread %q key %q", c.name, n.ThreadID, n.Key)
		}
	}
}

// The key is what the app stores to announce a run once: it must stay what the
// app computed before Go owned it, or an upgrade would re-announce the last run.
func TestRunNoticeKeyAndThread(t *testing.T) {
	started := time.Date(2026, 10, 14, 7, 55, 0, 123_456_789, time.UTC)
	f := noticeRun("ok", func(f *lastRunFacet) { f.StartedAt = started })
	n := decideNotice(f, noticeNow, time.UTC)
	if want := "run-1791964500123"; n.Key != want { // .123456789 s rounds to .123
		t.Errorf("key = %q, want %q", n.Key, want)
	}
	started = started.Add(400 * time.Microsecond) // .123856789 rounds up to .124
	f.StartedAt = started
	if got := decideNotice(f, noticeNow, time.UTC).Key; got != "run-1791964500124" {
		t.Errorf("rounded key = %q", got)
	}
	// The thread is the finish day in the given zone.
	f.FinishedAt = time.Date(2026, 10, 14, 23, 30, 0, 0, time.UTC)
	loc := time.FixedZone("x", 2*3600)
	if got := decideNotice(f, f.FinishedAt, loc).ThreadID; got != "vani-run-2026-10-15" {
		t.Errorf("thread = %q", got)
	}
}

// The run's error text can carry fetched URLs and page content; it is never in
// the notice.
func TestRunNoticeNeverCarriesErrorText(t *testing.T) {
	f := noticeRun("failed", func(f *lastRunFacet) {
		f.Stage, f.Error = stageDigest, "GET https://boards.example.com/jobs?token=abc123: Jane Doe"
	})
	raw, err := json.Marshal(decideNotice(f, noticeNow, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if s := string(raw); strings.Contains(s, "example") || strings.Contains(s, "abc123") || strings.Contains(s, "Jane") {
		t.Errorf("error text reached the notice: %s", s)
	}
}

// A long unreadable reason is bounded to one line.
func TestRunNoticeSkipReasonIsBounded(t *testing.T) {
	f := lastRunFacet{facet: facet{Reason: strings.Repeat("x\n", 500)}, State: "unknown"}
	n := decideNotice(f, noticeNow, time.UTC)
	if strings.Contains(n.Reason, "\n") || len([]rune(n.Reason)) > 200 {
		t.Errorf("reason not bounded: %q", n.Reason)
	}
}

// status --json prints the notice inside last_run, and it matches what the
// record yields.
func TestStatusPrintsTheNotice(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	st := statusOf(t)
	if st.LastRun.Notice == nil || st.LastRun.Notice.V != runNoticeVersion || st.LastRun.Notice.Announce {
		t.Errorf("a vani with no run printed notice %+v", st.LastRun.Notice)
	}
}

// goldenNotices are the app-facing fixtures: one last_run facet per case, as
// status --json prints it, with its notice. The Swift test decodes each and
// checks the announcer posts exactly what is in it.
func TestRunNoticeGolden(t *testing.T) {
	dir := filepath.Join("..", "..", "macos", "Tests", "VaniApplyKitTests", "Fixtures", "runnotice")
	unread := &advanceReport{Eligible: 9, Limit: 9, Attempted: 5, Unread: 5, Deferred: 4, Stopped: stopUnreadStreak}
	golden := map[string]lastRunFacet{
		"digest":      noticeRun("ok", func(f *lastRunFacet) { f.DigestRoles = intPtr(12) }),
		"digest_left": noticeRun("ok", func(f *lastRunFacet) { f.DigestRoles, f.Advance = intPtr(5), &advanceReport{Unread: 2, Failed: 3} }),
		"digest_empty": noticeRun("ok", func(f *lastRunFacet) {
			f.DigestRoles = intPtr(0)
		}),
		"failed_unread": noticeRun("failed", func(f *lastRunFacet) { f.Stage, f.Advance = stagePipeline, unread }),
		"failed_ledger": noticeRun("failed", func(f *lastRunFacet) { f.Stage, f.Cause = stageDigest, causeLedgerFatal }),
		"failed_config": noticeRun("failed", func(f *lastRunFacet) { f.Stage, f.Cause = stageDigest, causeConfigStale }),
		"cancelled":     noticeRun("cancelled", nil),
		"running":       noticeRun("running", func(f *lastRunFacet) { f.FinishedAt = time.Time{} }),
		"unreadable":    {facet: facet{Reason: "run record is corrupt: unexpected EOF"}, State: "unknown"},
		"never":         {facet: facet{OK: true, Reason: "no run has been recorded yet"}, State: "none"},
		"too_old": noticeRun("ok", func(f *lastRunFacet) {
			f.DigestRoles = intPtr(1)
			f.FinishedAt = noticeNow.Add(-72 * time.Hour)
			f.StartedAt = f.FinishedAt.Add(-time.Minute)
		}),
	}
	for name, f := range golden {
		f.Notice = ptr(decideNotice(f, noticeNow, time.UTC))
		raw, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, '\n')
		path := filepath.Join(dir, name+".json")
		if *updateNoticeGolden {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		have, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (go test ./cmd/vani -run TestRunNoticeGolden -update writes it)", err)
		}
		if !bytes.Equal(have, raw) {
			t.Errorf("%s is stale: run go test ./cmd/vani -run TestRunNoticeGolden -update and commit it\nwant:\n%s", path, raw)
		}
	}
}

func ptr[T any](v T) *T { return &v }
