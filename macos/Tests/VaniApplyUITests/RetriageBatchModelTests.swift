import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// The batch re-triage flow and multi-selection, in this process, against a fake
/// `vani jobs retriage --batch` (and the Jobs fixture for the list). No accessibility trust, no real
/// vani, no Keychain, no owner state.
@MainActor
final class RetriageBatchModelTests: XCTestCase {
    private static let plan = """
    {"batch":true,"dry_run":true,"model":"claude-sonnet-5-5",
     "items":[{"key":"fx:alpha:1"},{"key":"fx:beta:2","read":"current"}],
     "estimate":{"input_tokens":2200,"typical_usd":0.008,"max_usd":0.03,"daily_cap_usd":30,"spent_usd":1,"remaining_usd":29,"cap_reached":false,"postings":2,"fit_at_max":2},
     "estimate_unread":{"input_tokens":1100,"typical_usd":0.004,"max_usd":0.015,"daily_cap_usd":30,"spent_usd":1,"remaining_usd":29,"cap_reached":false,"postings":1,"fit_at_max":1}}
    """

    private static func item(_ key: String, after: String = "strong_fit") -> String {
        """
        {"event":"item","key":"\(key)","result":{"key":"\(key)","model":"claude-sonnet-5-5","status":"scored",\
        "before":{"slots":{"role_fit":{"value":"adjacent"}},"relevance":50},"after":{"slots":{"role_fit":{"value":"\(after)","backend":"manual"}},"relevance":60},\
        "cost":{"usd":0.004}}}
        """
    }

    private struct Fake {
        let dir: URL
        var vani: URL { dir.appendingPathComponent("vani") }
        func calls() -> [String] {
            ((try? String(contentsOf: dir.appendingPathComponent("calls.log"), encoding: .utf8)) ?? "")
                .split(separator: "\n").map(String.init)
        }
        func put(_ name: String, _ text: String) throws { try Data(text.utf8).write(to: dir.appendingPathComponent(name)) }
    }

    /// `run` is printed line by line; with `wait`, the fake stops after the first line until `go` exists.
    private func fake(run: [String], wait: Bool = false) throws -> Fake {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("vani-apply-batch-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        let f = Fake(dir: dir)
        try f.put("plan.json", Self.plan)
        try f.put("first", (run.first ?? "") + "\n")
        try f.put("rest", run.dropFirst().joined(separator: "\n") + "\n")
        let script = """
        #!/bin/sh
        d='\(dir.path)'
        printf '%s\\n' "$*" >> "$d/calls.log"
        case " $* " in
        *" --dry-run "*) cat "$d/plan.json"; exit 0 ;;
        esac
        cat "$d/first"
        \(wait ? "i=0; while [ ! -f \"$d/go\" ] && [ $i -lt 200 ]; do sleep 0.05; i=$((i+1)); done" : "")
        cat "$d/rest"
        exit 0
        """
        try Data(script.utf8).write(to: f.vani)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: f.vani.path)
        return f
    }

    private func model(_ jobs: JobsFixture, _ f: Fake?) -> JobsModel {
        let vani = jobs.dir.appendingPathComponent("vani")
        guard let f else { return JobsModel { JobsClient(vaniBinary: vani, timeout: 10) } }
        let runner = DraftRunner(vaniBinary: f.vani, key: { nil }, decisionKey: { nil })
        return JobsModel(client: { JobsClient(vaniBinary: vani, timeout: 10) }, retriage: { RetriageClient(runner: runner) })
    }

    private func eventually(_ what: String, _ done: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(8)
        while !done() {
            if Date() > deadline { return XCTFail("timed out waiting for \(what)") }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    private func listCalls(_ jobs: JobsFixture) -> Int { jobs.calls().filter { $0.hasPrefix("jobs list") }.count }

    func testSelectingSeveralRowsClosesTheDetailAndOneRowOpensIt() async throws {
        let jobs = try JobsFixture(self)
        let m = model(jobs, nil)
        await m.refresh()
        m.selection = "fx:alpha:1"
        XCTAssertEqual(m.selectedKeys, ["fx:alpha:1"])
        m.selectedKeys = ["fx:alpha:1", "fx:beta:2"]
        XCTAssertNil(m.selection, "several rows open no one role")
        XCTAssertTrue(m.isMultiSelection)
        XCTAssertEqual(m.selectedJobs.map(\.key).sorted(), ["fx:alpha:1", "fx:beta:2"])
        m.selectedKeys = ["fx:beta:2"]
        XCTAssertEqual(m.selection, "fx:beta:2", "one row left open is the open role")
        m.selection = nil
        XCTAssertTrue(m.selectedKeys.isEmpty)
        m.selectedKeys = ["fx:alpha:1", "fx:beta:2"]
        m.view = .saved
        XCTAssertTrue(m.selectedKeys.isEmpty, "switching views clears the selection")
    }

    func testABatchShowsTheCostThenStreamsAndRelistsOnce() async throws {
        let jobs = try JobsFixture(self)
        let done = #"{"event":"done","summary":{"kept":1,"failed":0,"not_asked":0,"skipped":1,"changed":1,"usd":0.004}}"#
        let f = try fake(run: [#"{"event":"start","total":1}"#, Self.item("fx:alpha:1"), done], wait: true)
        let m = model(jobs, f)
        await m.refresh()
        m.selectedKeys = ["fx:alpha:1", "fx:beta:2"]
        m.startBatchRetriage()
        try await eventually("the confirmation") { m.batch.confirming != nil }
        XCTAssertEqual(f.calls().count, 1)
        XCTAssertTrue(f.calls()[0].hasPrefix("jobs retriage --json --batch --dry-run"), f.calls()[0])
        XCTAssertNotNil(m.retriage.blockedReason(for: try XCTUnwrap(m.jobs.first)), "one-role re-triage waits while a batch is pending")
        XCTAssertTrue(m.batch.skipRead, "already-read roles are left out unless asked")

        let before = listCalls(jobs)
        m.batch.confirm()
        try await eventually("running") { if case .running = m.batch.phase { true } else { false } }
        try await eventually("the paid call to start") { f.calls().count == 2 }
        XCTAssertEqual(f.calls().last, "jobs retriage --json --batch --skip-read fx:alpha:1", "only the unread role is asked")
        XCTAssertEqual(m.batch.rows["fx:alpha:1"], .queued)
        XCTAssertNil(m.batch.rows["fx:beta:2"], "a skipped role gets no row state")
        try await Task.sleep(nanoseconds: 200_000_000)
        XCTAssertEqual(listCalls(jobs), before, "the list is not re-read while the batch runs")
        try f.put("go", "")
        try await eventually("finished") { if case .finished = m.batch.phase { true } else { false } }
        XCTAssertEqual(m.batch.rows["fx:alpha:1"], .done(changed: true))
        XCTAssertEqual(m.batch.changedKeys, ["fx:alpha:1"])
        XCTAssertEqual(m.batch.kept, 1)
        try await eventually("one re-list") { self.listCalls(jobs) == before + 1 }
        XCTAssertFalse(m.batch.isBusy)

        m.selectChanged()
        XCTAssertEqual(m.selectedKeys, ["fx:alpha:1"])
        m.batch.dismiss()
        XCTAssertTrue(m.batch.rows.isEmpty)
        XCTAssertEqual(m.batch.phase, .idle)
    }

    func testReadingThemAgainAsksEveryRole() async throws {
        let jobs = try JobsFixture(self)
        let done = #"{"event":"done","summary":{"kept":2,"changed":0}}"#
        let f = try fake(run: [#"{"event":"start","total":2}"#, Self.item("fx:alpha:1", after: "adjacent"), Self.item("fx:beta:2", after: "adjacent"), done])
        let m = model(jobs, f)
        await m.refresh()
        m.selectedKeys = ["fx:alpha:1", "fx:beta:2"]
        m.startBatchRetriage()
        try await eventually("the confirmation") { m.batch.confirming != nil }
        m.batch.skipRead = false
        m.batch.confirm()
        try await eventually("finished") { if case .finished = m.batch.phase { true } else { false } }
        XCTAssertEqual(f.calls().last, "jobs retriage --json --batch fx:alpha:1 fx:beta:2")
        XCTAssertEqual(m.batch.rows["fx:beta:2"], .done(changed: false))
        XCTAssertTrue(m.batch.changedKeys.isEmpty)
    }

    func testStopKeepsWhatLandedAndMarksTheRestNotAsked() async throws {
        let jobs = try JobsFixture(self)
        let f = try fake(run: [Self.item("fx:alpha:1"), #"{"event":"done","summary":{"kept":2}}"#], wait: true)
        let m = model(jobs, f)
        await m.refresh()
        m.selectedKeys = ["fx:alpha:1", "fx:beta:2"]
        m.startBatchRetriage()
        try await eventually("the confirmation") { m.batch.confirming != nil }
        m.batch.skipRead = false
        m.batch.confirm()
        try await eventually("the first reading") { m.batch.kept == 1 }
        m.batch.cancel()
        try await eventually("finished") { if case .finished = m.batch.phase { true } else { false } }
        guard case let .finished(s) = m.batch.phase else { return XCTFail() }
        XCTAssertEqual(s.stopped, "cancelled")
        XCTAssertEqual(s.kept, 1)
        XCTAssertEqual(s.notAsked, 1)
        XCTAssertEqual(m.batch.rows["fx:alpha:1"], .done(changed: true))
        XCTAssertEqual(m.batch.rows["fx:beta:2"], .notAsked)
        XCTAssertEqual(m.batch.retryable, ["fx:beta:2"])
    }

    func testASingleReTriageBlocksTheBatchAndWithoutARunnerThereIsNone() async throws {
        let jobs = try JobsFixture(self)
        let f = try fake(run: [])
        let m = model(jobs, f)
        await m.refresh()
        try f.put("plan.json", #"{"key":"fx:alpha:1","estimate":{"typical_usd":0.005,"max_usd":0.016}}"#)
        m.startRetriage(try XCTUnwrap(m.jobs.first))
        try await eventually("the single confirmation") { m.retriage.confirming != nil }
        XCTAssertNotNil(m.batch.blockedReason(for: m.jobs))
        m.batch.start(m.jobs)
        XCTAssertEqual(m.batch.phase, .idle, "a batch never starts while one role's re-triage is pending")
        XCTAssertEqual(f.calls().count, 1)
        let bare = model(jobs, nil)
        XCTAssertFalse(bare.batch.isAvailable)
        bare.batch.start(m.jobs)
        XCTAssertEqual(bare.batch.phase, .idle)
    }
}
