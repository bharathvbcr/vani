import Foundation
import XCTest
@testable import VaniApplyKit

/// A stand-in for `vani status --json`: replies with `reply`, or, when the
/// arguments are not exactly `status --json`, with an error naming them.
private func fakeStatusVani(_ t: XCTestCase, reply: String, status: Int32 = 0, extra: String = "") throws -> URL {
    let dir = try tempDir(t)
    let url = dir.appendingPathComponent("vani")
    let script = """
    #!/bin/sh
    \(extra)
    [ "$*" = "status --json" ] || { printf '{"error":"unexpected arguments: %s"}\\n' "$*"; exit 1; }
    cat <<'EOF'
    \(reply)
    EOF
    exit \(status)
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return url
}

private let now = Date(timeIntervalSince1970: 1_792_000_000) // 2026-10-14

private func iso(_ d: Date) -> String {
    d.formatted(Date.ISO8601FormatStyle())
}

/// What `vani status --json` printed for a healthy install, with Go's times.
private func statusReply(lastRun: String? = nil, backends: String? = nil, ledgerOK: Bool = true,
                         version: Int = 1, generated: Date = now, scheduleExtra: String = "") -> String {
    let finished = iso(now.addingTimeInterval(-3 * 3600))
    let started = iso(now.addingTimeInterval(-3 * 3600 - 312))
    return """
    {"version":\(version),"generated_at":"\(iso(generated))","state_dir":"/Users/me/Library/Application Support/Vani/state",\
    "config":{"ok":true,"boards":42},\
    "ledger":{"ok":\(ledgerOK),\(ledgerOK ? "" : #""reason":"ledger: corrupt events file","#)"exists":true,"path":"/s/ledger","total":812,"by_status":{"digested":300,"dead":200,"dropped":250,"unsure":12,"live":50}},\
    "last_run":\(lastRun ?? #"{"ok":true,"state":"ok","started_at":"\#(started)","finished_at":"\#(finished)","duration_sec":312.4,"postings_before":780,"postings_after":812,"counts_before":{"live":10,"digested":300,"dropped":240,"dead":200,"unsure":30},"counts_after":{"live":50,"digested":300,"dropped":250,"dead":200,"unsure":12}}"#),\
    "schedule":{"ok":true,"daily_at":"08:30","timezone":"America/Los_Angeles","next_run":"2026-10-15T15:30:00Z"\(scheduleExtra)},\
    "backends":\(backends ?? #"{"ok":true,"day":"2026-10-14","items":[{"ok":true,"name":"d1-ultra","kind":"openrouter","cap_usd":0.25,"spent_usd":0.1,"remaining_usd":0.15},{"ok":true,"name":"gemini","kind":"llm","cap_usd":1,"spent_usd":0.7,"remaining_usd":0.3}]}"#),\
    "decide":{"ok":true,"shadow":"local","local_configured":true,"local_mode":"server","local_model":"d1-3b","lappi_configured":false}}
    """
}

private func decode(_ s: String) throws -> PipelineStatus {
    try JobsClient.decoder.decode(PipelineStatus.self, from: Data(s.utf8))
}

final class PipelineTests: XCTestCase {
    func testANoticeVersionThisAppDoesNotReadIsAWarning() throws {
        let finished = iso(now.addingTimeInterval(-3 * 3600)), started = iso(now.addingTimeInterval(-3 * 3600 - 312))
        let run = #"{"ok":true,"state":"ok","started_at":"\#(started)","finished_at":"\#(finished)","notice":{"v":2,"announce":false}}"#
        let issue = try XCTUnwrap(decode(statusReply(lastRun: run)).issues(at: now).first)
        XCTAssertEqual(issue.title, "Run notices are off")
        XCTAssertEqual(issue.severity, .warning)
        XCTAssertEqual(issue.detail, "vani's run notice is version 2; this app reads version 1. Rebuild the app.")
    }

    func testDecodesAHealthyStatusWithGoTimes() throws {
        let s = try decode(statusReply())
        XCTAssertEqual(s.version, 1)
        XCTAssertEqual(s.config.boards, 42)
        XCTAssertEqual(s.ledger.total, 812)
        XCTAssertEqual(s.ledger.byStatus["digested"], 300)
        XCTAssertEqual(s.lastRun.state, .ok)
        XCTAssertEqual(try XCTUnwrap(s.lastRun.durationSec), 312.4, accuracy: 0.01)
        XCTAssertEqual(s.lastRun.postingsAdded, 32)
        XCTAssertEqual(s.schedule.dailyAt, "08:30")
        XCTAssertNotNil(s.schedule.nextRun)
        XCTAssertEqual(s.backends.day, "2026-10-14")
        let or = try XCTUnwrap(s.backends.items.first)
        XCTAssertEqual(or.kind, "openrouter")
        XCTAssertEqual(or.fractionSpent, 0.4)
        XCTAssertEqual(s.decide.localMode, "server")
        XCTAssertTrue(s.decide.localConfigured)
        XCTAssertEqual(s.issues(at: now), [])
        XCTAssertEqual(s.health(at: now), .healthy)
    }

    // MARK: - The schedule is only kept by a running daemon

    private func unkept(overdue: Bool = true, awaiting: Int? = 8525, lastRunHoursAgo: Double? = 30) -> String {
        var f = [#""why":"no daemon has announced itself""#, #""overdue":\#(overdue)"#, #""hint":"a daemon keeps the schedule""#]
        if let h = lastRunHoursAgo { f.append(#""last_run_at":"\#(iso(now.addingTimeInterval(-h * 3600)))""#) }
        if let n = awaiting { f.append(#""awaiting_triage":\#(n),"advance_per_run":750,"runs_needed":\#((n + 749) / 750)"#) }
        return #","kept":false,"unkept":{\#(f.joined(separator: ","))}"#
    }

    func testAScheduleNothingKeepsIsAWarningWithWhatItCost() throws {
        let s = try decode(statusReply(scheduleExtra: unkept()))
        let issue = try XCTUnwrap(s.issues(at: now).first)
        XCTAssertEqual(issue.title, "The daily schedule is not being kept")
        XCTAssertEqual(issue.severity, .warning)
        let detail = try XCTUnwrap(issue.detail)
        for want in ["No daemon is running, so nothing fires the 08:30 run.", "Last run 30 h ago and the scheduled run is overdue.",
                     "8525 postings await triage (about 12 runs at advance_per_run 750).", "Use Run Now with a higher advance cap."] {
            XCTAssertTrue(detail.contains(want), "\(want) missing from: \(detail)")
        }
        XCTAssertEqual(s.health(at: now), .warning)
    }

    func testAKeptScheduleAndOldAndUnknownReportsAreQuiet() throws {
        // A running daemon, a vani that predates the field, and one that could not
        // tell (kept absent) all raise nothing about the schedule.
        for extra in [#","kept":true"#, ""] {
            XCTAssertEqual(try decode(statusReply(scheduleExtra: extra)).issues(at: now), [], extra)
        }
    }

    func testAnUnkeptScheduleWithNothingOverdueOrWaitingIsQuiet() throws {
        let s = try decode(statusReply(scheduleExtra: unkept(overdue: false, awaiting: 0)))
        XCTAssertEqual(s.issues(at: now), [])
        // Not knowing the backlog or the overdue state is not "nothing waits".
        let unknown = try decode(statusReply(scheduleExtra: #","kept":false,"unkept":{"hint":"h"}"#))
        XCTAssertEqual(unknown.issues(at: now), [])
    }

    func testEitherOverdueOrABacklogIsEnoughToWarn() throws {
        let overdueOnly = try decode(statusReply(scheduleExtra: unkept(overdue: true, awaiting: 0)))
        XCTAssertEqual(overdueOnly.issues(at: now).map(\.title), ["The daily schedule is not being kept"])
        XCTAssertFalse(try XCTUnwrap(overdueOnly.issues(at: now).first?.detail).contains("await triage"))
        let backlogOnly = try decode(statusReply(scheduleExtra: unkept(overdue: false, awaiting: 1, lastRunHoursAgo: 1)))
        let detail = try XCTUnwrap(backlogOnly.issues(at: now).first?.detail)
        XCTAssertTrue(detail.contains("1 posting awaits triage"), detail)
        XCTAssertTrue(detail.contains("about 1 run at advance_per_run 750"), detail)
        XCTAssertFalse(detail.contains("overdue"), detail)
    }

    func testNoRunRecordedReadsAsOverdueWithNoLastRunTime() throws {
        let s = try decode(statusReply(scheduleExtra: unkept(lastRunHoursAgo: nil)))
        let detail = try XCTUnwrap(s.issues(at: now).first?.detail)
        XCTAssertTrue(detail.contains("No run is recorded, so the scheduled run is overdue."), detail)
        XCTAssertFalse(detail.contains("Last run"), detail)
    }

    /// Captured from `vani status --json` on a fresh state folder with a failed
    /// run recorded: Go's microsecond times with an offset, a missing ledger, and
    /// omitted fields (no shadow, no local model).
    func testDecodesWhatGoActuallyPrints() throws {
        let real = """
        {"version":1,"generated_at":"2026-10-19T05:46:27.219365Z","state_dir":"/s/st","config":{"ok":true,"boards":0},\
        "ledger":{"ok":true,"reason":"no ledger yet: no run has created it","exists":false,"path":"/s/st/ledger","total":0,"by_status":{}},\
        "last_run":{"ok":true,"state":"failed","started_at":"2026-10-19T05:00:00.123456-07:00","finished_at":"2026-10-19T05:00:09Z","duration_sec":9,"stage":"pipeline","error":"pipeline: no decision backend","postings_before":3,"postings_after":3,"counts_before":{"live":3},"counts_after":{"live":3}},\
        "schedule":{"ok":true,"daily_at":"08:30","timezone":"America/Los_Angeles","next_run":"2026-10-19T15:30:00Z"},\
        "backends":{"ok":true,"day":"2026-10-19","items":[{"ok":true,"name":"d1-ultra","kind":"openrouter","cap_usd":0.25,"spent_usd":0,"remaining_usd":0.25},{"ok":true,"name":"gemini","kind":"llm","cap_usd":1,"spent_usd":0,"remaining_usd":1}]},\
        "decide":{"ok":true,"local_configured":false,"lappi_configured":false}}
        """
        let s = try decode(real)
        XCTAssertFalse(s.ledger.exists)
        XCTAssertEqual(s.lastRun.state, .failed)
        XCTAssertEqual(s.lastRun.postingsAdded, 0)
        XCTAssertEqual(s.lastRun.statusChanges.count, 0)
        XCTAssertNil(s.decide.shadow)
        XCTAssertEqual(s.backends.items.map(\.kind), ["openrouter", "llm"])
        XCTAssertEqual(s.health(at: Date(timeIntervalSince1970: 1_792_000_000)), .problem)
    }

    func testStatusChangesListOnlyWhatMoved() throws {
        let s = try decode(statusReply())
        let changes = s.lastRun.statusChanges.map { "\($0.status)\($0.delta)" }
        XCTAssertEqual(changes, ["dropped10", "live40", "unsure-18"])
        XCTAssertEqual(PipelineStatus.LastRun(state: .ok).statusChanges.count, 0, "no counts recorded: nothing to compare")
        XCTAssertNil(PipelineStatus.LastRun(state: .ok, postingsBefore: 3).postingsAdded)
    }

    func testAFailedRunIsAProblemWithItsStageAndError() throws {
        let run = #"{"ok":true,"state":"failed","started_at":"2026-10-14T05:00:00Z","finished_at":"2026-10-14T05:00:09Z","duration_sec":9,"stage":"pipeline","error":"pipeline: no decision backend is available (configure decide.openrouter_model or llm)"}"#
        let s = try decode(statusReply(lastRun: run))
        XCTAssertEqual(s.lastRun.state, .failed)
        let issue = try XCTUnwrap(s.issues(at: now).first)
        XCTAssertEqual(issue.severity, .problem)
        XCTAssertEqual(issue.title, "The last run failed (pipeline)")
        XCTAssertTrue(try XCTUnwrap(issue.detail).contains("no decision backend"))
        XCTAssertEqual(s.health(at: now), .problem)
    }

    func testInterruptedAndUnreadableRunsAreProblems() throws {
        let interrupted = try decode(statusReply(lastRun: #"{"ok":true,"state":"interrupted","started_at":"2026-10-14T05:00:00Z"}"#))
        XCTAssertEqual(interrupted.issues(at: now).first?.title, "The last run did not finish")
        XCTAssertEqual(interrupted.health(at: now), .problem)

        let unreadable = try decode(statusReply(lastRun: #"{"ok":false,"state":"unknown","reason":"run record is corrupt: invalid character"}"#))
        XCTAssertEqual(unreadable.issues(at: now).first?.severity, .problem)
        XCTAssertEqual(unreadable.issues(at: now).first?.detail, "run record is corrupt: invalid character")
        // A state a newer vani adds must not look fine.
        let future = try decode(statusReply(lastRun: #"{"ok":true,"state":"paused"}"#))
        XCTAssertEqual(future.lastRun.state, .unknown)
        XCTAssertEqual(future.health(at: now), .problem)
    }

    func testRunningNeverAndCancelled() throws {
        let running = try decode(statusReply(lastRun: #"{"ok":true,"state":"running","started_at":"2026-10-14T04:59:00Z"}"#))
        XCTAssertEqual(running.health(at: now), .healthy)
        XCTAssertNil(running.lastRun.finishedAt)
        let never = try decode(statusReply(lastRun: #"{"ok":true,"reason":"no run has been recorded yet","state":"none"}"#))
        XCTAssertEqual(never.lastRun.state, .never)
        XCTAssertEqual(never.health(at: now), .warning)
        let cancelled = try decode(statusReply(lastRun: #"{"ok":true,"state":"cancelled","stage":"pipeline","error":"pipeline: context canceled"}"#))
        XCTAssertEqual(cancelled.issues(at: now).first?.severity, .warning)
    }

    func testAGoodRunThatIsTooOldIsAWarning() throws {
        let s = try decode(statusReply())
        let later = now.addingTimeInterval(40 * 3600)
        let issue = try XCTUnwrap(s.issues(at: later).first)
        XCTAssertEqual(issue.title, "No run in over a day")
        XCTAssertEqual(issue.severity, .warning)
        XCTAssertTrue(try XCTUnwrap(issue.detail).contains("43 h"))
    }

    func testUnreadableSpendIsUnknownNeverZero() throws {
        let backends = #"{"ok":true,"day":"2026-10-14","items":[{"ok":false,"reason":"openrouter budget: file is corrupt","name":"d1-ultra","kind":"openrouter","cap_usd":0.25},{"ok":true,"name":"gemini","kind":"llm","cap_usd":1,"spent_usd":0,"remaining_usd":1}]}"#
        let s = try decode(statusReply(backends: backends))
        let or = try XCTUnwrap(s.backends.items.first)
        XCTAssertNil(or.spentUSD)
        XCTAssertNil(or.remainingUSD)
        XCTAssertNil(or.fractionSpent, "an unread spend is not 0% spent")
        XCTAssertEqual(s.backends.items.last?.fractionSpent, 0, "a read spend of zero is zero")
        let issue = try XCTUnwrap(s.issues(at: now).first)
        XCTAssertEqual(issue.title, "Today's spend on d1-ultra could not be read")
        XCTAssertEqual(issue.detail, "openrouter budget: file is corrupt")
    }

    func testAnExhaustedBackendIsAWarning() throws {
        let backends = #"{"ok":true,"day":"2026-10-14","items":[{"ok":true,"name":"gemini","kind":"llm","cap_usd":1,"spent_usd":1.2,"remaining_usd":0,"exhausted":true}]}"#
        let s = try decode(statusReply(backends: backends))
        XCTAssertEqual(s.backends.items.first?.fractionSpent, 1, "clamped to the cap")
        XCTAssertEqual(s.issues(at: now).map(\.title), ["gemini's daily budget is spent"])
        XCTAssertEqual(s.health(at: now), .warning)
    }

    func testUnknownCapsAndUnreadableLedgerAreReported() throws {
        let backends = #"{"ok":false,"reason":"the config could not be read, so the caps are not known","day":"2026-10-14","items":[]}"#
        let s = try decode(statusReply(backends: backends, ledgerOK: false))
        let titles = s.issues(at: now).map(\.title)
        XCTAssertEqual(titles, ["The ledger could not be read", "Spend and caps are unknown"], "problems sort before warnings")
    }

    func testIssuesSortWorstFirst() throws {
        let run = #"{"ok":true,"state":"failed","stage":"digest","error":"digest held back"}"#
        let backends = #"{"ok":true,"day":"2026-10-14","items":[{"ok":true,"name":"gemini","kind":"llm","cap_usd":1,"spent_usd":1,"remaining_usd":0,"exhausted":true}]}"#
        let s = try decode(statusReply(lastRun: run, backends: backends))
        XCTAssertEqual(s.issues(at: now).map(\.severity), [.problem, .warning])
    }

    func testAgeLabels() {
        for (seconds, want) in [(-5.0, "under 2 min"), (30, "under 2 min"), (600, "10 min"), (7200, "2 h"), (43 * 3600, "43 h"), (4 * 86400, "4 days")] {
            XCTAssertEqual(PipelineStatus.age(seconds), want, "\(seconds) s")
        }
    }

    // MARK: run detail (advance, backend errors)

    /// The last_run `vani status --json` printed for a run record holding an
    /// advance report and backend errors (captured from build/vani).
    private static let runWithDetail = #"{"ok":true,"state":"failed","started_at":"2026-10-19T05:00:00.123456-07:00","finished_at":"2026-10-19T12:04:00Z","duration_sec":240,"stage":"pipeline","error":"pipeline: no decision backend read 20 postings in a row","postings_before":2639,"postings_after":2639,"counts_before":{"live":2639},"counts_after":{"live":2639},"advance":{"eligible":3027,"limit":300,"attempted":20,"unread":20,"failed":0,"deferred":3007,"stopped":"unread_streak"},"backend_errors":{"causes":[{"backend":"anthropic","count":2,"example":"anthropic: http 429 (request req_1): rate_limit_error"}],"unrecorded":3}}"#

    func testDecodesTheAdvanceReportAndBackendErrorsGoPrints() throws {
        let run = try decode(statusReply(lastRun: Self.runWithDetail)).lastRun
        let adv = try XCTUnwrap(run.advance)
        XCTAssertEqual(adv, .init(eligible: 3027, limit: 300, attempted: 20, unread: 20, failed: 0, deferred: 3007, stopped: .unreadStreak))
        XCTAssertEqual(adv.advanced, 0)
        XCTAssertEqual(adv.summary, "20 of 3027 eligible postings attempted this run; 3007 wait for the next run")
        XCTAssertEqual(adv.outcome, "0 advanced, 20 unread by every decision backend; those stay for the next run")
        XCTAssertEqual(adv.stopNote, "Stopped early: no decision backend could read several postings in a row.")

        let errs = try XCTUnwrap(run.backendErrors)
        XCTAssertEqual(errs.causes, [.init(backend: "anthropic", count: 2, example: "anthropic: http 429 (request req_1): rate_limit_error")])
        XCTAssertEqual(errs.unrecorded, 3)
        XCTAssertEqual(errs.totalCalls, 5)
        XCTAssertEqual(errs.summary, "5 failed calls to decision backends")
        XCTAssertEqual(errs.lines().map(\.example), ["anthropic: http 429 (request req_1): rate_limit_error"])
        XCTAssertEqual(errs.moreNote(), "3 calls with causes not recorded")
        // The detail annotates the run; it does not change how bad the run looks.
        XCTAssertEqual(try decode(statusReply(lastRun: Self.runWithDetail)).health(at: now), .problem)
    }

    func testAReplyWithoutTheRunDetailStillDecodes() throws {
        // A vani from before advance and backend_errors: the default fixture has neither.
        let s = try decode(statusReply())
        XCTAssertNil(s.lastRun.boards)
        XCTAssertNil(s.lastRun.advance)
        XCTAssertNil(s.lastRun.backendErrors)
        XCTAssertEqual(s.health(at: now), .healthy)
        XCTAssertEqual(PipelineStatus.supportedVersion, 1, "the fields are additive: no contract bump")
    }

    // MARK: boards

    /// A succeeded run where one board failed to list (the Intel case) and a
    /// careers page was skipped, in the shape cmd/vani/runrecord.go writes.
    private static let runWithBoards = #"{"ok":true,"state":"ok","boards":{"configured":4,"read":2,"failed":1,"skipped":1,"listed":35,"items":[{"ats":"gh","token":"nvidia","company":"NVIDIA","outcome":"read","listed":35,"reconcile":{"new":35}},{"ats":"wd","token":"intel/External","company":"Intel","outcome":"failed","error":"workday \"intel/External\": posting \"/job/X_JR1\": http 500"},{"ats":"pg","token":"hooli","company":"Hooli","outcome":"skipped","error":"careers-page source disabled this run: lightpanda not found"},{"ats":"lv","token":"quiet","outcome":"read","listed":0}]}}"#

    func testDecodesTheBoardsReportAndNamesTheBoardsNotRead() throws {
        let s = try decode(statusReply(lastRun: Self.runWithBoards))
        let b = try XCTUnwrap(s.lastRun.boards)
        XCTAssertEqual(b.configured, 4)
        XCTAssertEqual(b.unreadCount, 2)
        XCTAssertEqual(b.items.map(\.outcome), [.read, .failed, .skipped, .read])
        XCTAssertEqual(b.items[3].listed, 0)
        XCTAssertNil(b.items[3].company)
        XCTAssertEqual(b.summary, "2 of 4 boards read; 35 postings listed")
        XCTAssertEqual(b.outcome, "1 failed, 1 skipped; their postings were left as they were")
        XCTAssertEqual(b.problems().map(\.board), ["Intel (wd intel/External)", "Hooli (pg hooli)"])
        XCTAssertEqual(b.problems().map(\.outcome), ["failed", "skipped"])
        XCTAssertEqual(b.problems()[0].error, #"workday "intel/External": posting "/job/X_JR1": http 500"#)
        XCTAssertNil(b.moreNote())
        // A run that succeeded with a board unread is a warning, never healthy.
        XCTAssertEqual(s.health(at: now), .warning)
        let issue = try XCTUnwrap(s.issues(at: now).first { $0.title.contains("board") })
        XCTAssertEqual(issue.title, "2 boards could not be read last run")
        XCTAssertEqual(issue.detail, "Intel (wd intel/External), Hooli (pg hooli)")
    }

    func testEveryBoardReadIsHealthy() throws {
        let run = #"{"ok":true,"state":"ok","boards":{"configured":1,"read":1,"failed":0,"skipped":0,"listed":1,"items":[{"ats":"gh","token":"a","outcome":"read","listed":1}]}}"#
        let s = try decode(statusReply(lastRun: run))
        XCTAssertEqual(s.lastRun.boards?.summary, "1 of 1 board read; 1 posting listed")
        XCTAssertNil(s.lastRun.boards?.outcome)
        XCTAssertEqual(s.lastRun.boards?.problems(), [])
        XCTAssertEqual(s.health(at: now), .healthy)
    }

    func testBoardsCutByVaniAndNotReached() throws {
        // A cancelled run: one read, one failed mid-listing, one never tried;
        // and a report whose items vani cut (only the totals are whole).
        let run = #"{"ok":true,"state":"cancelled","boards":{"configured":30,"read":1,"failed":20,"skipped":0,"not_reached":9,"listed":3,"items":[{"ats":"gh","token":"a","outcome":"failed","error":"line one\nline two"},{"ats":"gh","token":"b","outcome":"newer","error":""}],"unrecorded":19}}"#
        let b = try XCTUnwrap(try decode(statusReply(lastRun: run)).lastRun.boards)
        XCTAssertEqual(b.notReached, 9)
        XCTAssertEqual(b.unrecorded, 19)
        XCTAssertEqual(b.outcome, "20 failed, 9 not reached; their postings were left as they were")
        XCTAssertEqual(b.problems()[0].error, "line one line two", "one line")
        XCTAssertEqual(b.items[1].outcome, .unknown("newer"))
        XCTAssertEqual(b.problems()[1].outcome, "newer", "a newer outcome is shown as written, never as read")
        XCTAssertEqual(b.moreNote(), "18 more boards not read")
        XCTAssertEqual(b.problems(limit: 1).count, 1)
        XCTAssertEqual(b.moreNote(limit: 1), "19 more boards not read")
    }

    func testBoardErrorsAndNamesAreBounded() throws {
        let item = PipelineStatus.LastRun.Boards.Item(ats: "wd", token: String(repeating: "t", count: 500),
                                                     company: String(repeating: "c", count: 500), outcome: .failed,
                                                     error: String(repeating: "x", count: 5000))
        let b = PipelineStatus.LastRun.Boards(configured: 1, read: 0, failed: 1, listed: 0, items: [item])
        let line = try XCTUnwrap(b.problems().first)
        XCTAssertEqual(line.error.count, PipelineStatus.LastRun.Boards.maxError)
        XCTAssertEqual(line.board.count, 80)
    }

    func testAMalformedBoardsReportFailsTheReadInsteadOfLookingAbsent() {
        let run = #"{"ok":true,"state":"ok","boards":{"configured":"many"}}"#
        XCTAssertThrowsError(try decode(statusReply(lastRun: run)))
    }

    func testTheCapExampleFromTheBrief() {
        let capped = PipelineStatus.LastRun.Advance(eligible: 2639, limit: 300, attempted: 300, deferred: 2339, stopped: .cap)
        XCTAssertEqual(capped.summary, "300 of 2639 eligible postings attempted this run; 2339 wait for the next run")
        XCTAssertNil(capped.outcome, "every attempted posting advanced")
        XCTAssertEqual(capped.stopNote, "Stopped at the per-run cap of 300 (advance_per_run).")
    }

    func testEachStopReasonsWording() throws {
        func stop(_ raw: String?) throws -> PipelineStatus.LastRun.Advance {
            let field = raw.map { #","stopped":"\#($0)""# } ?? ""
            let run = #"{"ok":true,"state":"ok","advance":{"eligible":10,"limit":5,"attempted":5,"unread":1,"failed":1,"deferred":5\#(field)}}"#
            return try XCTUnwrap(try decode(statusReply(lastRun: run)).lastRun.advance)
        }
        XCTAssertEqual(try stop("cap").stopped, .cap)
        XCTAssertEqual(try stop("cap").stopNote, "Stopped at the per-run cap of 5 (advance_per_run).")
        XCTAssertEqual(try stop("unread_streak").stopNote, "Stopped early: no decision backend could read several postings in a row.")
        XCTAssertEqual(try stop("none_read").stopped, .noneRead)
        XCTAssertEqual(try stop("none_read").stopNote, "Stopped: no decision backend read any posting.")
        XCTAssertEqual(try stop("cancelled").stopNote, "Stopped: the run was cancelled.")
        XCTAssertEqual(try stop("fail_streak").stopped, .failStreak)
        XCTAssertEqual(try stop("fail_streak").stopNote, "Stopped early: several postings in a row failed a stage (scoring or explaining).")
        XCTAssertEqual(try stop("none_advanced").stopped, .noneAdvanced)
        XCTAssertEqual(try stop("none_advanced").stopNote, "Stopped: no posting advanced; each failed a stage or went unread.")
        // A reason a newer vani adds is shown as written, never as finished.
        XCTAssertEqual(try stop("budget").stopped, .unknown("budget"))
        XCTAssertEqual(try stop("budget").stopNote, "Stopped: budget.")
        XCTAssertEqual(try stop(String(repeating: "z", count: 500)).stopNote?.count, "Stopped: .".count + 60)
        // Absent (Go omits "") or empty: the phase worked through everything.
        XCTAssertNil(try stop(nil).stopped)
        XCTAssertNil(try stop(nil).stopNote)
        XCTAssertNil(try stop("").stopped)
        XCTAssertEqual(try stop(nil).outcome, "3 advanced, 1 unread by every decision backend, 1 failed a stage; those stay for the next run")
    }

    func testAdvanceEdgeCounts() {
        typealias A = PipelineStatus.LastRun.Advance
        XCTAssertEqual(A(eligible: 0, limit: 300, attempted: 0, deferred: 0).summary, "No posting was waiting for a stage")
        XCTAssertEqual(A(eligible: 1, limit: 300, attempted: 1, deferred: 0).summary, "1 of 1 eligible posting attempted this run")
        XCTAssertEqual(A(eligible: 1, limit: 300, attempted: 1, unread: 3, deferred: 0).advanced, 0, "never negative")
    }

    func testAMalformedAdvanceFailsTheReadInsteadOfLookingAbsent() {
        let run = #"{"ok":true,"state":"ok","advance":{"eligible":"many"}}"#
        XCTAssertThrowsError(try decode(statusReply(lastRun: run)))
    }

    func testBackendErrorExamplesAreBoundedToOneLine() throws {
        let long = String(repeating: "x", count: 1000)
        let errs = PipelineStatus.LastRun.BackendErrors(causes: [
            .init(backend: "anthropic", count: 9, example: "line one\nline two\r\n\t\u{202E}reversed\u{0007}bell  "),
            .init(backend: "openrouter", count: 4, example: long),
            .init(backend: "gemini", count: 1, example: ""),
            .init(backend: "local", count: 2, example: "connection refused"),
            .init(backend: "lappi", count: 1, example: "timeout"),
        ])
        let lines = errs.lines()
        XCTAssertEqual(lines.map(\.backend), ["anthropic", "openrouter", "gemini"], "the first three, in vani's order")
        XCTAssertEqual(lines[0].example, "line one line two reversed bell")
        XCTAssertEqual(lines[1].example.count, PipelineStatus.LastRun.BackendErrors.maxExample)
        XCTAssertTrue(lines[1].example.hasSuffix("…"))
        XCTAssertEqual(lines[2].example, "")
        XCTAssertEqual(errs.moreNote(), "2 more causes (3 calls)")
        XCTAssertNil(errs.moreNote(limit: 5))
        XCTAssertEqual(errs.lines(limit: -1), [])
        XCTAssertEqual(PipelineStatus.bounded("  \n ", max: 10), "")
        XCTAssertEqual(PipelineStatus.bounded("abcdef", max: 3), "ab…")
        XCTAssertEqual(PipelineStatus.bounded("abc", max: 3), "abc")
    }

    func testBackendErrorsWithOnlyUnrecordedCauses() throws {
        let run = #"{"ok":true,"state":"ok","backend_errors":{"causes":[],"unrecorded":1}}"#
        let errs = try XCTUnwrap(try decode(statusReply(lastRun: run)).lastRun.backendErrors)
        XCTAssertEqual(errs.lines(), [])
        XCTAssertEqual(errs.summary, "1 failed call to decision backends")
        XCTAssertEqual(errs.moreNote(), "1 call with causes not recorded")
        // A cause list a vani leaves out entirely reads as none, not as a failure.
        let bare = try XCTUnwrap(try decode(statusReply(lastRun: #"{"ok":true,"state":"ok","backend_errors":{"unrecorded":2}}"#)).lastRun.backendErrors)
        XCTAssertEqual(bare.totalCalls, 2)
    }

    /// The run-detail contract end to end: a run record holding an advance
    /// report and backend errors, read back through the real `vani status`.
    /// Needs Vani/build/vani built from this checkout.
    func testRunDetailAgainstTheRealVani() throws {
        let vani = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("build/vani")
        guard FileManager.default.isExecutableFile(atPath: vani.path) else {
            throw XCTSkip("no Go vani at \(vani.path); build it to run the contract test")
        }
        let state = try tempDir(self)
        let runs = state.appendingPathComponent("runs")
        try FileManager.default.createDirectory(at: runs, withIntermediateDirectories: true)
        // The record shape of cmd/vani/runrecord.go (runRecord).
        let record = #"{"v":1,"pid":1,"started_at":"2026-10-19T12:00:00Z","finished_at":"2026-10-19T12:04:00Z","outcome":"failed","stage":"pipeline","error":"pipeline: no decision backend read 20 postings in a row","boards":{"configured":2,"read":1,"failed":1,"skipped":0,"listed":35,"items":[{"ats":"gh","token":"nvidia","outcome":"read","listed":35,"reconcile":{"new":35}},{"ats":"wd","token":"intel/External","company":"Intel","outcome":"failed","error":"http 500"}]},"advance":{"eligible":3027,"limit":300,"attempted":20,"unread":20,"failed":0,"deferred":3007,"stopped":"unread_streak"},"backend_errors":{"causes":[{"backend":"anthropic","count":2,"example":"anthropic: http 429 (request req_1): rate_limit_error"}],"unrecorded":3}}"#
        try Data(record.utf8).write(to: runs.appendingPathComponent("last.json"))

        let s = try PipelineClient(vaniBinary: vani, configPath: state.appendingPathComponent("none.json").path, stateDir: state.path).status()
        XCTAssertEqual(s.lastRun.state, .failed)
        XCTAssertEqual(try XCTUnwrap(s.lastRun.boards).problems().map(\.board), ["Intel (wd intel/External)"])
        XCTAssertEqual(s.lastRun.boards?.listed, 35)
        XCTAssertEqual(try XCTUnwrap(s.lastRun.advance),
                       PipelineStatus.LastRun.Advance(eligible: 3027, limit: 300, attempted: 20, unread: 20, failed: 0, deferred: 3007, stopped: .unreadStreak))
        XCTAssertEqual(try XCTUnwrap(s.lastRun.backendErrors),
                       PipelineStatus.LastRun.BackendErrors(causes: [.init(backend: "anthropic", count: 2, example: "anthropic: http 429 (request req_1): rate_limit_error")], unrecorded: 3))

        // A record from before the detail: both absent, still read.
        let old = #"{"v":1,"pid":1,"started_at":"2026-10-19T12:00:00Z","finished_at":"2026-10-19T12:04:00Z","outcome":"ok"}"#
        try Data(old.utf8).write(to: runs.appendingPathComponent("last.json"))
        let plain = try PipelineClient(vaniBinary: vani, configPath: state.appendingPathComponent("none.json").path, stateDir: state.path).status()
        XCTAssertEqual(plain.lastRun.state, .ok)
        XCTAssertNil(plain.lastRun.boards)
        XCTAssertNil(plain.lastRun.advance)
        XCTAssertNil(plain.lastRun.backendErrors)
    }

    // MARK: client

    func testClientReadsStatusAndPassesTheVaniPaths() throws {
        // The fake prints the state folder it was given inside the reply's state_dir.
        let vani = try fakeStatusVani(self, reply: statusReply(), extra: #"echo "$VANI_STATE_DIR|$VANI_CONFIG" >> "$(dirname "$0")/env.txt""#)
        let c = PipelineClient(vaniBinary: vani, configPath: "/tmp/c.json", stateDir: "/tmp/state")
        let s = try c.status()
        XCTAssertEqual(s.ledger.total, 812)
        let seen = try String(contentsOf: vani.deletingLastPathComponent().appendingPathComponent("env.txt"), encoding: .utf8)
        XCTAssertEqual(seen.trimmingCharacters(in: .whitespacesAndNewlines), "/tmp/state|/tmp/c.json")
    }

    func testClientSurfacesAnErrorReply() throws {
        let vani = try fakeStatusVani(self, reply: #"{"error":"state dir is not readable"}"#, status: 1)
        XCTAssertThrowsError(try PipelineClient(vaniBinary: vani).status()) {
            XCTAssertEqual(String(describing: $0), "state dir is not readable")
        }
    }

    func testClientRejectsATooOldVaniAndGarbage() throws {
        let old = try fakeStatusVani(self, reply: "vani: flag provided but not defined: -json", status: 2)
        XCTAssertThrowsError(try PipelineClient(vaniBinary: old).status()) {
            XCTAssertTrue(String(describing: $0).contains("too old"), "\($0)")
        }
        let garbage = try fakeStatusVani(self, reply: "not json at all")
        XCTAssertThrowsError(try PipelineClient(vaniBinary: garbage).status()) {
            XCTAssertTrue(String(describing: $0).contains("without a reply"), "\($0)")
        }
        // Valid JSON of the wrong shape is not a status either.
        let wrong = try fakeStatusVani(self, reply: #"{"version":1}"#)
        XCTAssertThrowsError(try PipelineClient(vaniBinary: wrong).status())
    }

    func testClientRefusesAnotherContractVersion() throws {
        let vani = try fakeStatusVani(self, reply: statusReply(version: 2))
        XCTAssertThrowsError(try PipelineClient(vaniBinary: vani).status()) {
            XCTAssertTrue(String(describing: $0).contains("version 2"), "\($0)")
        }
    }

    func testClientReportsAMissingBinaryATimeoutAndAnOversizedReply() throws {
        let missing = URL(fileURLWithPath: "/nonexistent/vani")
        XCTAssertThrowsError(try PipelineClient(vaniBinary: missing).status()) {
            XCTAssertTrue(String(describing: $0).contains("missing"), "\($0)")
        }
        let slow = try fakeStatusVani(self, reply: statusReply(), extra: "sleep 5")
        let started = Date()
        XCTAssertThrowsError(try PipelineClient(vaniBinary: slow, timeout: 0.4).status()) {
            XCTAssertTrue(String(describing: $0).contains("did not finish"), "\($0)")
        }
        XCTAssertLessThan(Date().timeIntervalSince(started), 4)
        let big = try fakeStatusVani(self, reply: String(repeating: "x", count: PipelineClient.maxOutputBytes + 10))
        XCTAssertThrowsError(try PipelineClient(vaniBinary: big).status()) {
            XCTAssertTrue(String(describing: $0).contains("larger than"), "\($0)")
        }
    }
}
