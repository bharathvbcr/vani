import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// The re-triage flow in this process against a fake `vani jobs retriage` (and the Jobs fixture for
/// the list). Like JobsModelTests these drive the models directly: no accessibility trust, no real
/// vani, no Keychain (every key lookup is a closure that returns nothing) and no owner state.
@MainActor
final class RetriageModelTests: XCTestCase {
    private static let dry = """
    {"key":"fx:alpha:1","dry_run":true,"model":"claude-sonnet-5-5","estimate":{"input_tokens":1800,"typical_usd":0.0053,"max_usd":0.0164,
     "daily_cap_usd":20,"spent_usd":1.25,"remaining_usd":18.75,"cap_reached":false}}
    """
    private static let run = """
    {"key":"fx:alpha:1","model":"claude-sonnet-5-5","status":"scored",
     "before":{"slots":{"role_fit":{"value":"adjacent"}},"relevance":71.2,"priority":55.1},
     "after":{"slots":{"role_fit":{"value":"strong_fit","backend":"manual"},"seniority":{"value":"mid","backend":"manual"},
      "sponsorship":{"value":"not_stated","backend":"manual"},"work_mode":{"value":"remote","backend":"manual"}},
      "relevance":74.0,"priority":60.2,"at":"2026-10-24T10:00:00Z"},
     "cost":{"input_tokens":1790,"output_tokens":212,"usd":0.0072}}
    """

    /// A fake `vani` for `jobs retriage`; `slow` makes the paid call sleep (so a test can cancel it).
    private struct Fake {
        let dir: URL
        var vani: URL { dir.appendingPathComponent("vani") }

        func calls() -> [String] {
            ((try? String(contentsOf: dir.appendingPathComponent("calls.log"), encoding: .utf8)) ?? "")
                .split(separator: "\n").map(String.init)
        }

        func put(_ name: String, _ text: String) throws { try Data(text.utf8).write(to: dir.appendingPathComponent(name)) }
        func remove(_ name: String) { try? FileManager.default.removeItem(at: dir.appendingPathComponent(name)) }

        var childPID: pid_t? {
            (try? String(contentsOf: dir.appendingPathComponent("pid"), encoding: .utf8))
                .flatMap { pid_t($0.trimmingCharacters(in: .whitespacesAndNewlines)) }
        }
    }

    private func fake(dry: String = RetriageModelTests.dry, run: String = RetriageModelTests.run) throws -> Fake {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("vani-apply-retriage-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        let script = """
        #!/bin/sh
        d='\(dir.path)'
        printf '%s\\n' "$*" >> "$d/calls.log"
        case " $* " in
        *" --dry-run "*) cat "$d/dry.json"; exit 0 ;;
        *" --clear "*) cat "$d/clear.json"; exit 0 ;;
        esac
        if [ -f "$d/slow" ]; then echo $$ > "$d/pid"; exec sleep 30; fi
        cat "$d/run.json"
        [ -f "$d/run.fail" ] && exit 1
        exit 0
        """
        let f = Fake(dir: dir)
        try Data(script.utf8).write(to: f.vani)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: f.vani.path)
        try f.put("dry.json", dry)
        try f.put("run.json", run)
        try f.put("clear.json", #"{"key":"fx:alpha:1","cleared":true,"had_manual":true,"relevance":71.2,"priority":55.1}"#)
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

    func testAskingShowsTheCostAndSpendsNothingUntilConfirmed() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        let m = model(jobs, f)
        await m.refresh()
        let job = try XCTUnwrap(m.jobs.first)
        m.startRetriage(job)
        XCTAssertEqual(m.selection, job.key, "the detail pane shows the posting the progress and the answer are about")
        try await eventually("the confirmation") { m.retriage.confirming != nil }
        XCTAssertEqual(f.calls(), ["jobs retriage --json --dry-run fx:alpha:1"], "only the estimate has run")
        XCTAssertTrue(m.retriage.isBusy)
        XCTAssertNotNil(m.retriage.blockedReason(for: job), "nothing else starts while one waits")
        m.retriage.cancel()
        XCTAssertEqual(m.retriage.phase, .idle)
        XCTAssertEqual(f.calls().count, 1, "closing the confirmation asked nothing")
    }

    func testConfirmingAsksSonnetAndRelistsTheJobs() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        let m = model(jobs, f)
        await m.refresh()
        let before = listCalls(jobs)
        m.startRetriage(try XCTUnwrap(m.jobs.first))
        try await eventually("the confirmation") { m.retriage.confirming != nil }
        m.retriage.confirm()
        try await eventually("the answer") { if case .succeeded = m.retriage.phase { true } else { false } }
        guard case let .succeeded(key, result, _) = m.retriage.phase else { return XCTFail("\(m.retriage.phase)") }
        XCTAssertEqual(key, "fx:alpha:1")
        XCTAssertEqual(result.after.slots[.roleFit]?.backend, "manual")
        XCTAssertEqual(f.calls(), ["jobs retriage --json --dry-run fx:alpha:1", "jobs retriage --json fx:alpha:1"])
        try await eventually("the list to be read again") { self.listCalls(jobs) > before }
        XCTAssertFalse(m.retriage.isBusy, "a shown result does not block the next re-triage")
        m.retriage.dismiss()
        XCTAssertEqual(m.retriage.phase, .idle)
    }

    func testAnUntriagedPostingIsNeverStartedAndWithoutARunnerTheActionIsAbsent() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        let m = model(jobs, f)
        await m.refresh()
        var job = try XCTUnwrap(m.jobs.first)
        job.untriaged = true
        m.startRetriage(job)
        try await Task.sleep(nanoseconds: 100_000_000)
        XCTAssertEqual(m.retriage.phase, .idle)
        XCTAssertTrue(f.calls().isEmpty)

        let bare = model(jobs, nil)
        XCTAssertFalse(bare.retriage.isAvailable)
        bare.startRetriage(try XCTUnwrap(m.jobs.first))
        XCTAssertEqual(bare.retriage.phase, .idle)
    }

    func testASpentCapCannotBeConfirmed() async throws {
        let spent = #"{"key":"fx:alpha:1","estimate":{"typical_usd":0.005,"max_usd":0.016,"daily_cap_usd":20,"spent_usd":20,"remaining_usd":0,"cap_reached":true}}"#
        let jobs = try JobsFixture(self), f = try fake(dry: spent)
        let m = model(jobs, f)
        await m.refresh()
        m.startRetriage(try XCTUnwrap(m.jobs.first))
        try await eventually("the confirmation") { m.retriage.confirming != nil }
        m.retriage.confirm()
        try await Task.sleep(nanoseconds: 150_000_000)
        XCTAssertNotNil(m.retriage.confirming, "confirm is refused while the cap is spent")
        XCTAssertEqual(f.calls().count, 1, "the paid call never ran")
    }

    func testAFailureShowsInlineAndRetryGoesBackThroughTheConfirmation() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        try f.put("run.json", #"{"error":"the model refused","code":"unreadable","cost":{"usd":0.0072}}"#)
        try f.put("run.fail", "")
        let m = model(jobs, f)
        await m.refresh()
        m.startRetriage(try XCTUnwrap(m.jobs.first))
        try await eventually("the confirmation") { m.retriage.confirming != nil }
        m.retriage.confirm()
        try await eventually("the failure") { if case .failed = m.retriage.phase { true } else { false } }
        guard case let .failed(_, failure) = m.retriage.phase else { return XCTFail("\(m.retriage.phase)") }
        XCTAssertEqual(failure.code, "unreadable")
        XCTAssertNil(m.actionError, "an error is inline in the detail, never the blocking alert")
        XCTAssertFalse(m.retriage.isBusy)
        f.remove("run.fail")
        try f.put("run.json", Self.run)
        m.retriage.retry()
        try await eventually("the new confirmation") { m.retriage.confirming != nil }
        XCTAssertEqual(f.calls().filter { $0.contains("--dry-run") }.count, 2, "a retry asks for the cost again")
        XCTAssertEqual(f.calls().filter { !$0.contains("--dry-run") }.count, 1, "and spends nothing until confirmed")
    }

    func testAnEstimateFailureIsShownAndSpendsNothing() async throws {
        let jobs = try JobsFixture(self)
        let f = try fake(dry: #"{"error":"no run has triaged it","code":"untriaged"}"#)
        let m = model(jobs, f)
        await m.refresh()
        m.startRetriage(try XCTUnwrap(m.jobs.first))
        try await eventually("the failure") { if case .failed = m.retriage.phase { true } else { false } }
        guard case let .failed(_, failure) = m.retriage.phase else { return XCTFail("\(m.retriage.phase)") }
        XCTAssertEqual(failure.code, "untriaged")
        XCTAssertEqual(f.calls().count, 1)
    }

    func testCancellingARunStopsTheChildSaysNothingAndLooksForTheReading() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        try f.put("slow", "")
        let m = model(jobs, f)
        await m.refresh()
        let before = listCalls(jobs)
        m.startRetriage(try XCTUnwrap(m.jobs.first))
        try await eventually("the confirmation") { m.retriage.confirming != nil }
        m.retriage.confirm()
        try await eventually("the child to start") { f.childPID != nil }
        let child = try XCTUnwrap(f.childPID)
        // Closing the confirmation sheet (the binding's setter) must never stop a run.
        m.retriage.dismissConfirmation()
        XCTAssertEqual(m.retriage.phase, .running(key: "fx:alpha:1", cancelling: false))
        XCTAssertTrue(m.retriage.isBusy)

        m.retriage.cancel()
        XCTAssertEqual(m.retriage.phase, .running(key: "fx:alpha:1", cancelling: true), "still busy until the child is gone")
        try await eventually("the run to end") { m.retriage.phase == .idle }
        XCTAssertTrue(kill(child, 0) == -1 && errno == ESRCH, "the child was terminated and reaped")
        try await eventually("the list to be read again") { self.listCalls(jobs) > before }
        XCTAssertNil(m.actionError)
    }

    func testClearingForgetsTheReadingAndRelists() async throws {
        let jobs = try JobsFixture(self), f = try fake()
        let m = model(jobs, f)
        await m.refresh()
        let before = listCalls(jobs)
        let job = try XCTUnwrap(m.jobs.first)
        m.retriage.clear(job)
        try await eventually("the clear") { if case .cleared = m.retriage.phase { true } else { false } }
        XCTAssertEqual(f.calls(), ["jobs retriage --json --clear fx:alpha:1"])
        try await eventually("the list to be read again") { self.listCalls(jobs) > before }
        guard case let .cleared(_, result, relevanceBefore) = m.retriage.phase else { return XCTFail("\(m.retriage.phase)") }
        XCTAssertEqual(result.relevance, 71.2)
        XCTAssertEqual(relevanceBefore, job.relevance)
    }
}
