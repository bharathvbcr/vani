import Foundation
import XCTest
@testable import VaniApplyKit

/// A stand-in for `vani jobs`: replies with `reply`, or, when the arguments
/// contain "echo", with the arguments and Vani paths it was given.
private func fakeJobsVani(_ t: XCTestCase, reply: String, status: Int32 = 0) throws -> URL {
    let dir = try tempDir(t)
    let url = dir.appendingPathComponent("vani")
    let script = """
    #!/bin/sh
    case "$*" in *echo*) printf '{"key":"gh:a:1","company":"%s","title":"%s","status":"live"}\\n' "$*" "${VANI_STATE_DIR:-none}"; exit 0 ;; esac
    cat <<'EOF'
    \(reply)
    EOF
    exit \(status)
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return url
}

private let listReply = """
{"view":"new","jobs":[{"key":"ab:baseten:0d1f","company":"Baseten","title":"Inference Engineer","location":"San Francisco","remote":true,\
"url":"https://jobs.ashbyhq.com/baseten/0d1f","apply_url":"javascript:alert(1)","status":"digested","rank":71.5,\
"explanation":"Fits: Inference serving and runtimes (88%). Gap: Asks for senior or staff-level experience (40%). Check: Ask about on-call and production support load (70%).",\
"triage":{"role_fit":"strong_fit","seniority":"senior","work_mode":"not_stated"},"first_seen":"2026-10-18T21:15:20.122204-05:00",\
"posted_at":"2025-04-07T18:41:33.018Z","mark":"saved","marked_at":"2026-10-19T09:00:00Z"}],"total":283,"shown":1,"counts":{"new":283,"saved":1,"all":284}}
"""

final class JobsTests: XCTestCase {
    func testDecodesAListWithGoTimes() throws {
        let c = JobsClient(vaniBinary: try fakeJobsVani(self, reply: listReply))
        let page = try c.list(.new)
        XCTAssertEqual(page.total, 283)
        XCTAssertEqual(page.shown, 1)
        XCTAssertEqual(page.count(.saved), 1)
        XCTAssertEqual(page.count(.hidden), 0)
        let j = try XCTUnwrap(page.jobs.first)
        XCTAssertEqual(j.mark, .saved)
        XCTAssertEqual(j.rank, 71.5)
        XCTAssertEqual(j.fitLabel, "Strong fit")
        XCTAssertEqual(j.triageLabels, ["Senior"])
        let want = try XCTUnwrap(ISO8601DateFormatter().date(from: "2026-10-19T02:15:20Z")).addingTimeInterval(0.122204)
        // Foundation keeps milliseconds of Go's microseconds.
        XCTAssertEqual(try XCTUnwrap(j.firstSeen).timeIntervalSince1970, want.timeIntervalSince1970, accuracy: 0.001)
        XCTAssertNotNil(j.postedAt)
        XCTAssertEqual(j.postingLink?.absoluteString, "https://jobs.ashbyhq.com/baseten/0d1f")
        XCTAssertNil(j.applyLink, "a javascript: link never becomes clickable")
    }

    func testSplitsThePickedNoteIntoReasons() {
        var j = Job(key: "gh:a:1", company: "A", title: "T")
        j.explanation = "Fits: Inference serving and runtimes (88%). Gap: Senior (40%). Check: On-call load (70%)."
        XCTAssertEqual(j.reasons, [
            .init(kind: .fits, text: "Inference serving and runtimes (88%)"),
            .init(kind: .gap, text: "Senior (40%)"),
            .init(kind: .check, text: "On-call load (70%)"),
        ])
        j.explanation = "Gap: Sales, solutions or customer-facing engineering (96%)."
        XCTAssertEqual(j.reasons, [.init(kind: .gap, text: "Sales, solutions or customer-facing engineering (96%)")])
        j.explanation = "No listed reason clearly applies."
        XCTAssertEqual(j.reasons, [.init(kind: .other, text: "No listed reason clearly applies.")])
        j.explanation = "Fits: A (1%). trailing prose"
        XCTAssertEqual(j.reasons.map(\.kind), [.fits], "a label it cannot place ends the reason")
        j.explanation = "   "
        XCTAssertEqual(j.reasons, [])
    }

    func testOnlyHTTPLinksOpen() {
        for bad in ["javascript:alert(1)", "data:text/html,x", "file:///etc/passwd", "https://", "notaurl", ""] {
            XCTAssertNil(Job.httpURL(bad), bad)
        }
        XCTAssertNotNil(Job.httpURL("http://example.com/x"))
    }

    func testPassesArgumentsAndStateDirOnly() throws {
        let c = JobsClient(vaniBinary: try fakeJobsVani(self, reply: ""), stateDir: "/state")
        let j = try c.note("gh:a:1", "echo; rm -rf / $(whoami)")
        XCTAssertEqual(j.company, "jobs note --json gh:a:1 echo; rm -rf / $(whoami)", "arguments reach vani verbatim, never through a shell")
        XCTAssertEqual(j.title, "/state")
    }

    func testErrorsCarryVanisMessage() throws {
        let c = JobsClient(vaniBinary: try fakeJobsVani(self, reply: #"{"error":"jobs: no posting \"gh:a:typo\" in the ledger"}"#, status: 1))
        XCTAssertThrowsError(try c.mark("gh:a:typo", .saved)) { err in
            XCTAssertTrue(String(describing: err).contains("no posting"), String(describing: err))
        }
        let old = JobsClient(vaniBinary: try fakeJobsVani(self, reply: "vani: unknown command", status: 2))
        XCTAssertThrowsError(try old.list(.new)) { err in
            XCTAssertTrue(String(describing: err).contains("too old"), String(describing: err))
        }
        let missing = JobsClient(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani"))
        XCTAssertThrowsError(try missing.list(.new))
    }

    func testRefusesAnOversizedNoteBeforeRunningVani() throws {
        let c = JobsClient(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani"))
        XCTAssertThrowsError(try c.note("gh:a:1", String(repeating: "x", count: JobsClient.maxNoteBytes + 1))) { err in
            XCTAssertTrue(String(describing: err).contains("at most"), String(describing: err))
        }
    }

    func testTimesOut() throws {
        let dir = try tempDir(self)
        let url = dir.appendingPathComponent("vani")
        try Data("#!/bin/sh\nsleep 5\n".utf8).write(to: url)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
        let started = Date()
        XCTAssertThrowsError(try JobsClient(vaniBinary: url, timeout: 1).list(.new))
        XCTAssertLessThan(Date().timeIntervalSince(started), 4)
    }

    /// The contract end to end: the real Go `vani jobs` against a small ledger.
    /// Needs Vani/build/vani built from this checkout (`go -C … build -o build/vani ./cmd/vani`).
    func testAgainstTheRealVani() throws {
        let vani = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("build/vani")
        guard FileManager.default.isExecutableFile(atPath: vani.path) else {
            throw XCTSkip("no Go vani at \(vani.path); build it to run the contract test")
        }
        let state = try tempDir(self)
        let ledger = state.appendingPathComponent("ledger")
        try FileManager.default.createDirectory(at: ledger, withIntermediateDirectories: true)
        let line = #"{"v":1,"at":"2026-10-18T12:00:00.5Z","key":"gh:acme:1","status":"digested","posting":{"key":"gh:acme:1","ats":"gh","company":"Acme","title":"Inference Engineer","url":"https://boards.example.com/1","description":"Serve models.","seen_at":"2026-10-18T12:00:00Z"},"annotations":{"explanation":"Fits: Inference serving and runtimes (88%).","triage.role_fit":"strong_fit"}}"#
        try Data((line + "\n").utf8).write(to: ledger.appendingPathComponent("events.jsonl"))

        let c = JobsClient(vaniBinary: vani, stateDir: state.path)
        var page = try c.list(.new)
        guard page.jobs.count == 1 else {
            return XCTFail("list: \(page)")
        }
        XCTAssertEqual(page.records, 1)
        XCTAssertEqual(page.ledger, ledger.path)
        XCTAssertEqual(page.jobs[0].reasons.first?.kind, .fits)
        var j = try c.mark("gh:acme:1", .saved)
        XCTAssertEqual(j.mark, .saved)
        XCTAssertNotNil(j.markedAt)
        j = try c.note("gh:acme:1", "ask about on-call")
        XCTAssertEqual(j.note, "ask about on-call")
        page = try c.list(.new)
        XCTAssertEqual(page.jobs.count, 0)
        XCTAssertEqual(page.count(.saved), 1)
        XCTAssertEqual(try c.show("gh:acme:1").description, "Serve models.")
        XCTAssertThrowsError(try c.mark("gh:acme:typo", .saved))
        _ = try c.mark("gh:acme:1", nil)
        XCTAssertEqual(try c.list(.new).jobs.count, 1)
    }
}
