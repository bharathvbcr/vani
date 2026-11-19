import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// Attacks on the Doctor's stale-report mark (ft-189e).
@MainActor
final class DoctorStaleAttackTests: XCTestCase {
    static let plan = """
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
    private func fake(plan: String = DoctorStaleAttackTests.plan, run: String = "", status: Int = 0, last: String = "{}",
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


    func testAFailedReadThenASuccessfulOneClearsTheStaleMark() async throws {
        let good = reply(sent: true, report: true)
        let f = try fake(last: good)
        let m = model(f)
        await m.loadLast()
        XCTAssertNotNil(m.report)
        XCTAssertFalse(m.reportIsStale)
        try f.put("last.json", "garbage")
        await m.loadLast()
        XCTAssertNotNil(m.lastError)
        XCTAssertNotNil(m.report, "the older report stays")
        XCTAssertTrue(m.reportIsStale)
        try f.put("last.json", good)
        await m.loadLast()
        XCTAssertNil(m.lastError)
        XCTAssertFalse(m.reportIsStale)
    }

    func testAFailedReadWithNoReportEverReadIsNotStale() async throws {
        let f = try fake(last: "garbage")
        let m = model(f)
        await m.loadLast()
        XCTAssertNotNil(m.lastError)
        XCTAssertNil(m.report)
        XCTAssertFalse(m.reportIsStale, "there is no older report to be stale")
    }

    func testAReportFromTheReviewJustRunIsNeverStaleWhateverTheLastReadDid() async throws {
        let f = try fake(run: reply(sent: true, report: true), last: "garbage")
        let m = model(f)
        await m.loadLast()
        XCTAssertNotNil(m.lastError)
        m.estimate()
        try await eventually("the confirmation") { m.confirming != nil }
        m.confirm()
        try await eventually("finished") { if case .finished = m.phase { true } else { false } }
        XCTAssertNil(m.lastError, "the run's own report supersedes the failed read")
        XCTAssertFalse(m.reportIsStale)
        XCTAssertEqual(m.report?.path, "/state/doctor/r.md")
    }

    func testASuccessfulReadOfNoReportDropsTheOlderReportAndTheMark() async throws {
        let f = try fake(last: reply(sent: true, report: true))
        let m = model(f)
        await m.loadLast()
        XCTAssertNotNil(m.report)
        try f.put("last.json", "{}")
        await m.loadLast()
        XCTAssertNil(m.report)
        XCTAssertNil(m.lastError)
        XCTAssertFalse(m.reportIsStale)
    }
}
