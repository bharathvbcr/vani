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
                         version: Int = 1, generated: Date = now) -> String {
    let finished = iso(now.addingTimeInterval(-3 * 3600))
    let started = iso(now.addingTimeInterval(-3 * 3600 - 312))
    return """
    {"version":\(version),"generated_at":"\(iso(generated))","state_dir":"/Users/me/Library/Application Support/Vani/state",\
    "config":{"ok":true,"boards":42},\
    "ledger":{"ok":\(ledgerOK),\(ledgerOK ? "" : #""reason":"ledger: corrupt events file","#)"exists":true,"path":"/s/ledger","total":812,"by_status":{"digested":300,"dead":200,"dropped":250,"unsure":12,"live":50}},\
    "last_run":\(lastRun ?? #"{"ok":true,"state":"ok","started_at":"\#(started)","finished_at":"\#(finished)","duration_sec":312.4,"postings_before":780,"postings_after":812,"counts_before":{"live":10,"digested":300,"dropped":240,"dead":200,"unsure":30},"counts_after":{"live":50,"digested":300,"dropped":250,"dead":200,"unsure":12}}"#),\
    "schedule":{"ok":true,"daily_at":"08:30","timezone":"America/Los_Angeles","next_run":"2026-10-15T15:30:00Z"},\
    "backends":\(backends ?? #"{"ok":true,"day":"2026-10-14","items":[{"ok":true,"name":"jev-1.13","kind":"openrouter","cap_usd":0.25,"spent_usd":0.1,"remaining_usd":0.15},{"ok":true,"name":"gemini","kind":"llm","cap_usd":1,"spent_usd":0.7,"remaining_usd":0.3}]}"#),\
    "decide":{"ok":true,"shadow":"local","local_configured":true,"local_mode":"server","local_model":"d1-3b","lappi_configured":false}}
    """
}

private func decode(_ s: String) throws -> PipelineStatus {
    try JobsClient.decoder.decode(PipelineStatus.self, from: Data(s.utf8))
}

final class PipelineTests: XCTestCase {
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

    /// Captured from `vani status --json` on a fresh state folder with a failed
    /// run recorded: Go's microsecond times with an offset, a missing ledger, and
    /// omitted fields (no shadow, no local model).
    func testDecodesWhatGoActuallyPrints() throws {
        let real = """
        {"version":1,"generated_at":"2026-10-19T05:46:27.219365Z","state_dir":"/s/st","config":{"ok":true,"boards":0},\
        "ledger":{"ok":true,"reason":"no ledger yet: no run has created it","exists":false,"path":"/s/st/ledger","total":0,"by_status":{}},\
        "last_run":{"ok":true,"state":"failed","started_at":"2026-10-19T05:00:00.123456-07:00","finished_at":"2026-10-19T05:00:09Z","duration_sec":9,"stage":"pipeline","error":"pipeline: no decision backend","postings_before":3,"postings_after":3,"counts_before":{"live":3},"counts_after":{"live":3}},\
        "schedule":{"ok":true,"daily_at":"08:30","timezone":"America/Los_Angeles","next_run":"2026-10-19T15:30:00Z"},\
        "backends":{"ok":true,"day":"2026-10-19","items":[{"ok":true,"name":"jev-1-13","kind":"openrouter","cap_usd":0.25,"spent_usd":0,"remaining_usd":0.25},{"ok":true,"name":"gemini","kind":"llm","cap_usd":1,"spent_usd":0,"remaining_usd":1}]},\
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
        let backends = #"{"ok":true,"day":"2026-10-14","items":[{"ok":false,"reason":"openrouter budget: file is corrupt","name":"jev-1.13","kind":"openrouter","cap_usd":0.25},{"ok":true,"name":"gemini","kind":"llm","cap_usd":1,"spent_usd":0,"remaining_usd":1}]}"#
        let s = try decode(statusReply(backends: backends))
        let or = try XCTUnwrap(s.backends.items.first)
        XCTAssertNil(or.spentUSD)
        XCTAssertNil(or.remainingUSD)
        XCTAssertNil(or.fractionSpent, "an unread spend is not 0% spent")
        XCTAssertEqual(s.backends.items.last?.fractionSpent, 0, "a read spend of zero is zero")
        let issue = try XCTUnwrap(s.issues(at: now).first)
        XCTAssertEqual(issue.title, "Today's spend on jev-1.13 could not be read")
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
