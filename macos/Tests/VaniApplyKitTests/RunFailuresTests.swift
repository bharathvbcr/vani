import Foundation
import XCTest
@testable import VaniApplyKit

/// Copy failures: the button's rule, the per-board line, and the call to
/// `vani runs failures --json` whose text is what gets copied. The report text
/// itself is vani's (Go tests hold its bounds); here it is only carried.

private func decodeRun(_ json: String) throws -> PipelineStatus.LastRun {
    try JobsClient.decoder.decode(PipelineStatus.LastRun.self, from: Data(json.utf8))
}

/// A stand-in `vani` answering `runs failures --json` with `reply`, or `body`'s own behaviour.
private func fakeRunsVani(_ t: XCTestCase, reply: String = "", status: Int32 = 0, body: String = "") throws -> URL {
    let url = try tempDir(t).appendingPathComponent("vani")
    let script = """
    #!/bin/sh
    echo "$*|$VANI_STATE_DIR" >> "$(dirname "$0")/calls.txt"
    [ "$*" = "runs failures --json" ] || { printf '{"error":"unexpected arguments: %s"}\\n' "$*"; exit 1; }
    \(body)
    cat <<'EOF'
    \(reply)
    EOF
    exit \(status)
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return url
}

private let okReply = #"{"version":1,"id":"20261022T090000.123Z-4242","source":"last","has_failures":true,"text":"Vani run report\nRun: 20261022T090000.123Z-4242\nFailed boards (1 of 3):\neightfold:microsoft (Microsoft): list: http 429\n"}"#

final class RunFailuresTests: XCTestCase {
    // MARK: the button's rule

    func testNothingFailedHidesTheButton() throws {
        let quiet = [
            #"{"ok":true,"state":"ok"}"#,
            #"{"ok":true,"state":"ok","boards":{"configured":1,"read":1,"failed":0,"skipped":0,"listed":3,"items":[{"ats":"gh","token":"a","outcome":"read","listed":3}]}}"#,
            // Skipped is not failed; a cancelled run with no error is the owner's own stop.
            #"{"ok":true,"state":"ok","boards":{"configured":1,"read":0,"failed":0,"skipped":1,"listed":0,"items":[{"ats":"pg","token":"a","outcome":"skipped","error":"disabled"}]}}"#,
            #"{"ok":true,"state":"cancelled"}"#,
            #"{"ok":true,"state":"running"}"#,
            #"{"ok":true,"state":"none"}"#,
            #"{"ok":true,"state":"ok","backend_errors":{"causes":[]}}"#,
            // A last run vani could not read has nothing to report; the facet says why.
            #"{"ok":false,"reason":"run record is corrupt","state":"unknown"}"#,
        ]
        for json in quiet {
            XCTAssertFalse(try decodeRun(json).hasFailures, json)
        }
    }

    func testEachKindOfFailureShowsTheButton() throws {
        let failing = [
            #"{"ok":true,"state":"failed","stage":"pipeline","error":"boom"}"#,
            #"{"ok":true,"state":"failed"}"#,
            #"{"ok":true,"state":"interrupted"}"#,
            #"{"ok":true,"state":"cancelled","error":"context canceled"}"#,
            // A board that failed under a run that still counts as ok.
            #"{"ok":true,"state":"ok","boards":{"configured":3,"read":2,"failed":1,"skipped":0,"listed":9,"items":[{"ats":"wd","token":"x","outcome":"failed","error":"429"}]}}"#,
            // The counts say a board failed though the items were bounded away.
            #"{"ok":true,"state":"ok","boards":{"configured":3,"read":2,"failed":1,"skipped":0,"listed":9,"items":[]}}"#,
            #"{"ok":true,"state":"ok","backend_errors":{"causes":[{"backend":"openrouter","count":2,"example":"402"}]}}"#,
            #"{"ok":true,"state":"ok","backend_errors":{"unrecorded":3}}"#,
        ]
        for json in failing {
            XCTAssertTrue(try decodeRun(json).hasFailures, json)
        }
    }

    /// An older vani sends none of the newer fields: still decodes, and a failed run still has failures.
    func testOlderVaniWithoutTheNewFieldsDecodesLeniently() throws {
        let old = try decodeRun(#"{"ok":true,"state":"failed","started_at":"2026-10-19T12:00:00Z","error":"x"}"#)
        XCTAssertTrue(old.hasFailures)
        XCTAssertNil(old.boards)
        XCTAssertNil(old.backendErrors)
        XCTAssertNil(old.trigger)
        let plain = try decodeRun(#"{"ok":true,"state":"ok","started_at":"2026-10-19T12:00:00Z"}"#)
        XCTAssertFalse(plain.hasFailures)
    }

    // MARK: the board line

    func testBoardCopyLineIsAtsTokenCompanyError() {
        let item = PipelineStatus.LastRun.Boards.Item(ats: "eightfold", token: "microsoft", company: "Microsoft", outcome: .failed,
                                                      error: "list: http 429 (too many requests)")
        XCTAssertEqual(item.copyLine, "eightfold:microsoft (Microsoft): list: http 429 (too many requests)")
        XCTAssertEqual(PipelineStatus.LastRun.Boards.Item(ats: "wd", token: "t/External", company: "", outcome: .failed, error: "x").copyLine,
                       "wd:t/External: x")
        XCTAssertEqual(PipelineStatus.LastRun.Boards.Item(ats: "gh", token: "a", outcome: .failed).copyLine, "gh:a: none")
    }

    /// An error is untrusted text: one line, control characters gone, and past vani's
    /// own line bound it is cut, but the copy keeps more than the row shows.
    func testBoardCopyLineIsOneBoundedLineButLongerThanTheRow() {
        let messy = PipelineStatus.LastRun.Boards.Item(ats: "gh", token: "a", outcome: .failed, error: "first\nsecond\u{1B}[31m\t third \u{202E}")
        XCTAssertFalse(messy.copyLine.contains("\n"))
        XCTAssertFalse(messy.copyLine.contains("\u{1B}"))
        XCTAssertFalse(messy.copyLine.contains("\u{202E}"))
        XCTAssertTrue(messy.copyLine.hasPrefix("gh:a: first second"))

        let long = PipelineStatus.LastRun.Boards.Item(ats: "gh", token: "a", outcome: .failed, error: String(repeating: "e", count: 5000))
        XCTAssertLessThanOrEqual(long.copyLine.count, FailureReport.maxLine)
        let boards = PipelineStatus.LastRun.Boards(configured: 1, read: 0, failed: 1, listed: 0, items: [
            .init(ats: "gh", token: "a", outcome: .failed, error: String(repeating: "e", count: 400)),
        ])
        let line = boards.problems()[0]
        XCTAssertEqual(line.error.count, PipelineStatus.LastRun.Boards.maxError, "the row shows a bounded error")
        XCTAssertTrue(line.copyText.hasSuffix(String(repeating: "e", count: 400)), "Copy error copies the whole error")
    }

    // MARK: the reply

    func testReplyDecodesAndOlderOrNewerFieldsAreIgnored() throws {
        let r = try JobsClient.decoder.decode(FailureReport.self, from: Data(okReply.utf8))
        XCTAssertEqual(r.id, "20261022T090000.123Z-4242")
        XCTAssertTrue(r.hasFailures)
        XCTAssertEqual(r.cutLines, 0)
        XCTAssertEqual(r.shortenedLines, 0)
        XCTAssertTrue(r.text.contains("eightfold:microsoft (Microsoft)"))
        let cut = try JobsClient.decoder.decode(FailureReport.self, from: Data(#"{"version":1,"id":"i","has_failures":true,"text":"t","cut_lines":4,"shortened_lines":2,"future":{"a":1}}"#.utf8))
        XCTAssertEqual(cut.cutLines, 4)
        XCTAssertEqual(cut.shortenedLines, 2)
        XCTAssertThrowsError(try JobsClient.decoder.decode(FailureReport.self, from: Data(#"{"version":1,"id":"i","has_failures":true}"#.utf8)),
                             "a reply with no text is not a report")
    }

    // MARK: the call

    func testClientAsksForTheLatestRunsFailuresWithTheVaniPaths() throws {
        let vani = try fakeRunsVani(self, reply: okReply)
        let report = try PipelineClient(vaniBinary: vani, configPath: "/tmp/c.json", stateDir: "/tmp/state").failureReport()
        XCTAssertEqual(report.id, "20261022T090000.123Z-4242")
        XCTAssertTrue(report.text.hasSuffix("http 429\n"))
        let seen = try String(contentsOf: vani.deletingLastPathComponent().appendingPathComponent("calls.txt"), encoding: .utf8)
        XCTAssertEqual(seen, "runs failures --json|/tmp/state\n")
    }

    /// Nothing to copy is an error with a reason, never an empty copy.
    func testFailuresAreErrorsNotEmptyCopies() throws {
        func client(_ vani: URL, timeout: TimeInterval = 5) -> PipelineClient {
            PipelineClient(vaniBinary: vani, configPath: nil, stateDir: "/tmp/s", timeout: timeout)
        }
        // An older vani has no `runs`: exit 2, said as such.
        let old = try fakeRunsVani(self, reply: "", status: 2, body: #"[ "$1" = runs ] && { echo 'vani: unknown command "runs"' >&2; exit 2; }"#)
        XCTAssertThrowsError(try client(old).failureReport()) { err in
            XCTAssertTrue(String(describing: err).contains("cannot report failures"), "\(err)")
        }
        // vani's own refusal is passed on as it worded it.
        let refused = try fakeRunsVani(self, reply: #"{"error":"no run has been recorded yet"}"#, status: 1)
        XCTAssertThrowsError(try client(refused).failureReport()) { err in
            XCTAssertEqual(String(describing: err), "no run has been recorded yet")
        }
        // An empty report text.
        let empty = try fakeRunsVani(self, reply: #"{"version":1,"id":"i","has_failures":false,"text":""}"#)
        XCTAssertThrowsError(try client(empty).failureReport()) { err in
            XCTAssertTrue(String(describing: err).contains("empty failure report"), "\(err)")
        }
        // Not JSON at all.
        let junk = try fakeRunsVani(self, reply: "not json")
        XCTAssertThrowsError(try client(junk).failureReport())
        // A reply of another contract version is named as that.
        let newer = try fakeRunsVani(self, reply: #"{"version":2,"id":"i","has_failures":true,"text":"t"}"#)
        XCTAssertThrowsError(try client(newer).failureReport()) { err in
            XCTAssertTrue(String(describing: err).contains("version 2"), "\(err)")
        }
        // A vani that hangs is cut off at the timeout.
        let slow = try fakeRunsVani(self, reply: okReply, body: "sleep 30")
        let started = Date()
        XCTAssertThrowsError(try client(slow, timeout: 1).failureReport()) { err in
            XCTAssertTrue(String(describing: err).contains("did not finish within 1 s"), "\(err)")
        }
        XCTAssertLessThan(Date().timeIntervalSince(started), 10)
        // A binary that is not there.
        XCTAssertThrowsError(try client(URL(fileURLWithPath: "/nonexistent/vani")).failureReport())
    }

    // MARK: against the real vani

    private func realVani() throws -> URL {
        let vani = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("build/vani")
        guard FileManager.default.isExecutableFile(atPath: vani.path) else {
            throw XCTSkip("no Go vani at \(vani.path); build it to run the contract test")
        }
        return vani
    }

    /// The failure report, the has_failures verdict and each board's line against the
    /// real `vani`: the app's button rule and per-board copy equal vani's, for runs of
    /// each shape, and the history `vani runs list` shows is what was written.
    func testButtonRuleAndBoardLinesAgreeWithTheRealVani() throws {
        let vani = try realVani()
        let state = try tempDir(self)
        let runs = state.appendingPathComponent("runs")
        try FileManager.default.createDirectory(at: runs, withIntermediateDirectories: true)
        let board = #"{"ats":"eightfold","token":"microsoft","company":"Microsoft","outcome":"failed","error":"list: http 429 (too many requests)"}"#
        let board2 = #"{"ats":"wd","token":"salesforce/External_Career_Site","company":"Salesforce","outcome":"failed","error":"list: page 41: workday pagination returned the same page twice"}"#
        let read = #"{"ats":"gh","token":"nvidia","company":"NVIDIA","outcome":"read","listed":3}"#
        func record(outcome: String, extra: String = "") -> String {
            #"{"v":1,"pid":1,"started_at":"2026-10-22T09:00:00.123Z","finished_at":"2026-10-22T09:04:00Z","outcome":"\#(outcome)"\#(extra)}"#
        }
        let cases: [(name: String, json: String)] = [
            ("clean", record(outcome: "ok", extra: #","boards":{"configured":1,"read":1,"failed":0,"skipped":0,"listed":3,"items":[\#(read)]}"#)),
            ("two failed boards", record(outcome: "ok", extra: #","boards":{"configured":3,"read":1,"failed":2,"skipped":0,"listed":3,"items":[\#(read),\#(board),\#(board2)]}"#)),
            ("failed run", record(outcome: "failed", extra: #","stage":"pipeline","error":"pipeline: no decision backend read 20 postings in a row""#)),
            ("backend errors only", record(outcome: "ok", extra: #","backend_errors":{"causes":[{"backend":"openrouter","count":2,"example":"http 402"}],"unrecorded":1}"#)),
            ("cancelled", record(outcome: "cancelled")),
            ("cancelled with error", record(outcome: "cancelled", extra: #","stage":"pipeline","error":"context canceled""#)),
        ]
        let client = PipelineClient(vaniBinary: vani, configPath: state.appendingPathComponent("none.json").path, stateDir: state.path)
        for (name, json) in cases {
            try Data(json.utf8).write(to: runs.appendingPathComponent("last.json"))
            let status = try client.status()
            let report = try client.failureReport()
            XCTAssertEqual(report.hasFailures, status.lastRun.hasFailures, "\(name): the button rule differs from vani's has_failures")
            XCTAssertFalse(report.text.isEmpty, name)
            XCTAssertTrue(report.text.contains("Run record: \(runs.path)/last.json"), "\(name): the report names the run record\n\(report.text)")
            let lines = Set(report.text.split(separator: "\n").map(String.init))
            for item in status.lastRun.boards?.items.filter({ $0.outcome == .failed }) ?? [] {
                XCTAssertTrue(lines.contains(item.copyLine), "\(name): vani's report has no line equal to the row's copy\n\(item.copyLine)\n\(report.text)")
            }
            if !report.hasFailures { XCTAssertTrue(report.text.contains("No failures in this run."), name) }
        }
    }

    /// Run history through the real vani: finishing writes it, `runs list` shows it.
    func testRunsListAndFailuresByIDAgainstTheRealVani() throws {
        let vani = try realVani()
        let state = try tempDir(self)
        let history = state.appendingPathComponent("runs/history")
        try FileManager.default.createDirectory(at: history, withIntermediateDirectories: true)
        let id = "20261021T090000.000Z-77"
        let rec = #"{"v":1,"pid":77,"started_at":"2026-10-21T09:00:00Z","finished_at":"2026-10-21T09:01:00Z","outcome":"failed","stage":"pipeline","error":"older run failed"}"#
        try Data(rec.utf8).write(to: history.appendingPathComponent("\(id).json"))
        let env = VaniProcess.environment(configPath: state.appendingPathComponent("none.json").path, stateDir: state.path)
        let out = VaniProcess.run(vani, arguments: ["runs", "failures", "--json", id], environment: env, input: nil, timeout: 10, maxOutputBytes: 1 << 20)
        XCTAssertEqual(out.status, 0, String(decoding: out.stdout, as: UTF8.self))
        let report = try JobsClient.decoder.decode(FailureReport.self, from: out.stdout)
        XCTAssertEqual(report.id, id)
        XCTAssertTrue(report.hasFailures)
        XCTAssertTrue(report.text.contains("Error: older run failed"))
    }
}
