import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// The Pipeline tab's doctor flow, in this process, against a fake `vani doctor`. No real vani, no
/// Keychain, no owner state, nothing sent.
@MainActor
final class DoctorModelTests: XCTestCase {
    private static let plan = """
    {"model":"claude-opus-5-5","runs_read":30,"boards_signaled":7,"drops_sampled":40,"seed":"2026-W46",\
    "worst_case_usd":1.21922,"requests":{"boards":"b","drops":"d"},"sent":false}
    """
    private static let report = """
    {"at":"2026-11-12T14:30:05Z","model":"claude-opus-5-5","boards":{"asked":1,"answered":1},"drops":{"asked":1,"answered":1},\
    "board_findings":[{"board":"wd:snap","verdict":"silent_failure","confidence":"high","evidence":"e","next_step":"n"}],\
    "drop_findings":[{"key":"gh:a:1","verdict":"wrong_drop","cause":"seniority_rule","why":"w"}],\
    "drops_sent":[{"key":"gh:a:1","stratum":"dropped:title:seniority","company":"Acme","title":"Staff Engineer"}]}
    """

    private struct Fake {
        let dir: URL
        var vani: URL { dir.appendingPathComponent("vani") }
        func calls() -> [String] {
            ((try? String(contentsOf: dir.appendingPathComponent("calls.log"), encoding: .utf8)) ?? "")
                .split(separator: "\n").map(String.init)
        }
        func put(_ name: String, _ text: String) throws { try Data(text.utf8).write(to: dir.appendingPathComponent(name)) }
    }

    /// `run` is the real run's reply and exit status; with `wait`, the run waits for `go` to exist.
    private func fake(plan: String = DoctorModelTests.plan, run: String = "", status: Int = 0, last: String = "{}",
                      lastStatus: Int = 0, wait: Bool = false) throws -> Fake {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("vani-apply-doctor-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        let f = Fake(dir: dir)
        try f.put("plan.json", plan)
        try f.put("run.json", run)
        try f.put("last.json", last)
        let script = """
        #!/bin/sh
        d='\(dir.path)'
        printf '%s\\n' "$*" >> "$d/calls.log"
        case " $* " in
        *" --last "*) cat "$d/last.json"; exit \(lastStatus) ;;
        *" --dry-run "*) cat "$d/plan.json"; exit 0 ;;
        esac
        \(wait ? "i=0; while [ ! -f \"$d/go\" ] && [ $i -lt 200 ]; do sleep 0.05; i=$((i+1)); done" : "")
        cat "$d/run.json"
        exit \(status)
        """
        try Data(script.utf8).write(to: f.vani)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: f.vani.path)
        return f
    }

    private func model(_ f: Fake) -> DoctorModel {
        let runner = DraftRunner(vaniBinary: f.vani, key: { nil }, decisionKey: { nil })
        return DoctorModel { DoctorClient(runner: runner, quickTimeout: 10, runTimeout: 10) }
    }

    private func eventually(_ what: String, _ done: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(8)
        while !done() {
            if Date() > deadline { return XCTFail("timed out waiting for \(what)") }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    private func reply(sent: Bool, report: Bool, error: String? = nil) -> String {
        var fields = [#""model":"claude-opus-5-5","worst_case_usd":1.21922,"sent":\#(sent)"#]
        if report { fields.append(#""report":"/state/doctor/r.md","result":\#(Self.report)"#) }
        if let error { fields.append(#""error":"\#(error)","code":"runtime""#) }
        return "{" + fields.joined(separator: ",") + "}"
    }

    func testAReviewIsPricedThenRunWithTheSeedItWasPricedWith() async throws {
        let f = try fake(run: reply(sent: true, report: true), wait: true)
        let m = model(f)
        await m.loadLast()
        XCTAssertNil(m.report)
        XCTAssertTrue(m.lastLoaded)

        m.estimate()
        try await eventually("the confirmation") { m.confirming != nil }
        XCTAssertEqual(m.budget, 1.75, "a quarter over the worst case, to the next quarter dollar")
        XCTAssertNil(m.runBlocker(try XCTUnwrap(m.confirming)))
        XCTAssertTrue(m.isBusy)

        m.confirm()
        try await eventually("running") { m.phase == .running(cancelling: false) }
        try await eventually("the paid call") { f.calls().count == 3 }
        XCTAssertEqual(f.calls().last, "doctor --json --budget-usd 1.75 --seed 2026-W46")
        try f.put("go", "")
        try await eventually("finished") { if case .finished = m.phase { true } else { false } }
        let shown = try XCTUnwrap(m.report)
        XCTAssertEqual(shown.path, "/state/doctor/r.md")
        XCTAssertEqual(shown.report.boardFindings.first?.verdict, "silent_failure")
        XCTAssertEqual(m.last?.report, "/state/doctor/r.md", "the newest report is the one just written")
        m.dismiss()
        XCTAssertEqual(m.phase, .idle)
        XCTAssertNotNil(m.report, "the newest report stays shown after Dismiss")
    }

    func testABudgetThatNoLongerCoversTheWorstCaseIsARefusalThatAsksForANewEstimate() async throws {
        let f = try fake(run: reply(sent: false, report: false, error: "doctor: the worst case $1.9000 is over --budget-usd $1.7500; nothing was sent"), status: 1)
        let m = model(f)
        m.estimate()
        try await eventually("the confirmation") { m.confirming != nil }
        m.confirm()
        try await eventually("failed") { if case .failed = m.phase { true } else { false } }
        guard case let .failed(failure) = m.phase else { return XCTFail() }
        XCTAssertEqual(failure.title, "The review was refused; nothing was sent")
        XCTAssertFalse(failure.mayHaveSpent)
        XCTAssertTrue(failure.wantsNewEstimate)
        XCTAssertNil(m.report)
    }

    func testAPartialReviewIsAFailureWhoseReportIsShown() async throws {
        let f = try fake(run: reply(sent: true, report: true, error: "doctor: drops: judged 0 of the 40 sent; the rest are not examined"), status: 1)
        let m = model(f)
        m.estimate()
        try await eventually("the confirmation") { m.confirming != nil }
        m.confirm()
        try await eventually("failed") { if case .failed = m.phase { true } else { false } }
        guard case let .failed(failure) = m.phase else { return XCTFail() }
        XCTAssertEqual(failure.title, "The review is incomplete")
        XCTAssertTrue(failure.mayHaveSpent)
        XCTAssertFalse(failure.wantsNewEstimate)
        XCTAssertEqual(m.report?.path, "/state/doctor/r.md", "the partial report is shown, not lost")
        XCTAssertEqual(m.last?.report, "/state/doctor/r.md")
    }

    func testAFailureWithoutAReplyMayHaveSpent() async throws {
        let f = try fake(run: "not json", status: 1)
        let m = model(f)
        m.estimate()
        try await eventually("the confirmation") { m.confirming != nil }
        m.confirm()
        try await eventually("failed") { if case .failed = m.phase { true } else { false } }
        guard case let .failed(failure) = m.phase else { return XCTFail() }
        XCTAssertEqual(failure.title, "The review failed")
        XCTAssertTrue(failure.mayHaveSpent, "unknown is not shown as nothing spent")
    }

    func testNothingToAskOffersNoRun() async throws {
        let empty = #"{"model":"claude-opus-5-5","runs_read":30,"worst_case_usd":0,"sent":false}"#
        let f = try fake(plan: empty)
        let m = model(f)
        m.estimate()
        try await eventually("the confirmation") { m.confirming != nil }
        let plan = try XCTUnwrap(m.confirming)
        XCTAssertTrue(plan.nothingToAsk)
        XCTAssertNotNil(m.runBlocker(plan))
        m.confirm()
        try await Task.sleep(nanoseconds: 100_000_000)
        XCTAssertEqual(f.calls().count, 1, "nothing was run")
        m.cancel()
        XCTAssertEqual(m.phase, .idle)
    }

    func testABudgetUnderTheWorstCaseIsBlockedHere() async throws {
        let f = try fake()
        let m = model(f)
        m.estimate()
        try await eventually("the confirmation") { m.confirming != nil }
        m.budget = 1.0
        XCTAssertNotNil(m.runBlocker(try XCTUnwrap(m.confirming)))
        m.confirm()
        try await Task.sleep(nanoseconds: 100_000_000)
        XCTAssertEqual(f.calls().count, 1, "nothing was run")
        m.budget = 25
        XCTAssertNotNil(m.runBlocker(try XCTUnwrap(m.confirming)), "over vani's ceiling")
    }

    /// A stopped review may still have written what it had: the newest report is read again.
    func testStoppingAReviewShowsAnyReportItWrote() async throws {
        let f = try fake(run: reply(sent: true, report: true), wait: true)
        let m = model(f)
        m.estimate()
        try await eventually("the confirmation") { m.confirming != nil }
        m.confirm()
        try await eventually("the paid call") { f.calls().count == 2 }
        try f.put("last.json", #"{"report":"/state/doctor/stopped.md","result":\#(Self.report)}"#)
        m.cancel()
        XCTAssertEqual(m.phase, .running(cancelling: true))
        try await eventually("stopped") { m.phase == .stopped }
        try await eventually("the newest report read again") { m.report?.path == "/state/doctor/stopped.md" }
        XCTAssertEqual(f.calls().last, "doctor --last --json")
    }

    /// The confirmation sheet closing after Run Review must not stop the review it started.
    func testClosingTheConfirmationDoesNotStopTheReview() async throws {
        let f = try fake(run: reply(sent: true, report: true), wait: true)
        let m = model(f)
        m.estimate()
        try await eventually("the confirmation") { m.confirming != nil }
        m.confirm()
        m.dismissConfirmation()
        XCTAssertEqual(m.phase, .running(cancelling: false))
        try f.put("go", "")
        try await eventually("finished") { if case .finished = m.phase { true } else { false } }

        let g = try fake()
        let n = model(g)
        n.estimate()
        try await eventually("the confirmation") { n.confirming != nil }
        n.dismissConfirmation()
        XCTAssertEqual(n.phase, .idle, "closing a confirmation still cancels it")
    }

    func testOnlyAnUnknownOutcomeWithoutAReplyMayHaveSpent() {
        func failure(_ code: String) -> DoctorModel.Failure {
            DoctorModel.Failure(stage: .run, failure: RetriageFailure(code: code, message: "m"))
        }
        for code in ["timeout", "no_reply", "too_large"] { XCTAssertTrue(failure(code).mayHaveSpent, code) }
        for code in ["config", "bad_request", "account", "keychain"] { XCTAssertFalse(failure(code).mayHaveSpent, code) }
        XCTAssertFalse(DoctorModel.Failure(stage: .estimate, failure: RetriageFailure(code: "timeout", message: "m")).mayHaveSpent)
    }

    func testTheNewestReportIsReadOrItsFailureShown() async throws {
        let ok = try fake(last: #"{"report":"/state/doctor/old.md","result":\#(Self.report)}"#)
        let m = model(ok)
        await m.loadLast()
        XCTAssertEqual(m.report?.path, "/state/doctor/old.md")
        XCTAssertNil(m.lastError)

        let bad = try fake(last: #"{"error":"doctor: the newest report x.json is unreadable","code":"runtime"}"#, lastStatus: 1)
        let n = model(bad)
        await n.loadLast()
        XCTAssertNil(n.report)
        XCTAssertEqual(n.lastError, "doctor: the newest report x.json is unreadable")
    }

    /// A later read that fails leaves the report from the earlier read on screen: the error is kept and
    /// the report is marked stale, until a read succeeds again. A report from a review just run is
    /// current whatever the last read did.
    func testAFailedReReadKeepsTheErrorAndMarksTheOldReportStale() async throws {
        let f = try fake(last: #"{"report":"/state/doctor/old.md","result":\#(Self.report)}"#)
        let m = model(f)
        await m.loadLast()
        XCTAssertNotNil(m.report)
        XCTAssertFalse(m.reportIsStale)

        let text = try String(contentsOf: f.vani, encoding: .utf8)
        let hook = #"cat "$d/last.json"; exit 0 ;;"#
        XCTAssertTrue(text.contains(hook))
        try Data(text.replacingOccurrences(of: hook, with: #"cat "$d/last.json"; exit 1 ;;"#).utf8).write(to: f.vani)
        try f.put("last.json", #"{"error":"doctor: the newest report x.json is unreadable","code":"runtime"}"#)
        await m.loadLast()
        XCTAssertEqual(m.lastError, "doctor: the newest report x.json is unreadable", "the failure is not dropped")
        XCTAssertNotNil(m.report, "the old report is kept, not blanked")
        XCTAssertTrue(m.reportIsStale, "and not presented as the newest")

        try f.put("last.json", #"{"report":"/state/doctor/new.md","result":\#(Self.report)}"#)
        try Data(text.utf8).write(to: f.vani)
        await m.loadLast()
        XCTAssertNil(m.lastError)
        XCTAssertFalse(m.reportIsStale)
        XCTAssertEqual(m.report?.path, "/state/doctor/new.md")
    }

    func testAFailedReadWithNoEarlierReportIsNotStale() async throws {
        let f = try fake(last: #"{"error":"doctor: unreadable","code":"runtime"}"#, lastStatus: 1)
        let m = model(f)
        await m.loadLast()
        XCTAssertNotNil(m.lastError)
        XCTAssertNil(m.report)
        XCTAssertFalse(m.reportIsStale, "there is no report to be stale")
    }
}
