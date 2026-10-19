import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// JobsModel in this process against the fake `vani jobs` (JobsFixture). Unlike
/// JobsControlsTests these drive the model directly, so they need no
/// accessibility trust and always run.
@MainActor
final class JobsModelTests: XCTestCase {
    private func model(_ f: JobsFixture) -> JobsModel {
        let vani = f.dir.appendingPathComponent("vani")
        return JobsModel { JobsClient(vaniBinary: vani, timeout: 10) }
    }

    /// Waits, at most five seconds, for work a setter started in a Task.
    private func eventually(_ what: String, _ done: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(5)
        while !done() {
            if Date() > deadline { return XCTFail("timed out waiting for \(what)") }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    func testRefreshListsInStandardOrder() async throws {
        let m = model(try JobsFixture(self))
        await m.refresh()
        XCTAssertNil(m.error)
        XCTAssertFalse(m.loading)
        XCTAssertEqual(m.jobs.map(\.key), JobsFixture.standardOrder)
        XCTAssertNil(m.stats, "stats load only while Insights is open")
    }

    func testSortAndFilterChangesRelist() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        m.sort = .fresh
        try await eventually("the fresh order") { m.jobs.map(\.key) == JobsFixture.freshOrder }
        XCTAssertTrue(f.calls().contains { $0.contains("--sort fresh") }, "\(f.calls())")

        m.sort = .standard
        m.filter.fits = ["strong_fit"]
        try await eventually("strong fits only") { m.jobs.map(\.key) == JobsFixture.strongOrder }
        m.clearFilter()
        try await eventually("the full list again") { m.jobs.map(\.key) == JobsFixture.standardOrder }
    }

    /// Setters fire overlapping lists; whichever finishes last, the page shown
    /// is the one the newest request asked for.
    func testANewerListSupersedesAnOlderOne() async throws {
        let m = model(try JobsFixture(self))
        for _ in 0..<5 {
            m.sort = .fresh
            m.sort = .standard
        }
        await m.refresh()
        // Let every superseded list finish; none of them may land.
        try await Task.sleep(nanoseconds: 300_000_000)
        XCTAssertEqual(m.jobs.map(\.key), JobsFixture.standardOrder)
        XCTAssertFalse(m.loading)
    }

    func testInsightsLoadsStatsWithTheList() async throws {
        let m = model(try JobsFixture(self))
        m.showInsights = true
        try await eventually("stats") { m.stats != nil }
        XCTAssertNil(m.statsError)
        XCTAssertEqual(m.stats?.total, JobsFixture.roles.count)
    }

    func testSelectionLoadsTheDetail() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        let key = JobsFixture.roles[2].key
        m.selection = key
        XCTAssertEqual(m.selected?.key, key, "the list's copy until the detail arrives")
        try await eventually("the detail") { m.detail?.key == key }
        XCTAssertNil(m.detailError)
        XCTAssertTrue(f.calls().contains("jobs show --json \(key)"), "\(f.calls())")

        m.selection = nil
        XCTAssertNil(m.detail)
        XCTAssertNil(m.selected)
    }

    /// A missing binary is an error, not an empty list, and a later good list clears it.
    func testAFailedListIsReportedAndRecovers() async throws {
        let f = try JobsFixture(self)
        var binary = f.dir.appendingPathComponent("missing-vani")
        let m = JobsModel { JobsClient(vaniBinary: binary, timeout: 10) }
        await m.refresh()
        XCTAssertNotNil(m.error)
        XCTAssertNil(m.page)
        XCTAssertFalse(m.loading)

        binary = f.dir.appendingPathComponent("vani")
        await m.refresh()
        XCTAssertNil(m.error)
        XCTAssertEqual(m.jobs.map(\.key), JobsFixture.standardOrder)
    }

    /// The fake answers no `jobs mark`, as a vani that refuses would: the
    /// error is shown and the list is not re-read as if the mark had landed.
    func testAFailedMarkIsShownAndChangesNothing() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        let lists = f.calls().filter { $0.hasPrefix("jobs list") }.count
        let key = JobsFixture.roles[0].key
        m.selection = key
        await m.setMark(key, .hidden)
        XCTAssertEqual(m.actionError, "fake vani: no reply for jobs mark")
        XCTAssertEqual(m.selection, key)
        XCTAssertEqual(f.calls().filter { $0.hasPrefix("jobs list") }.count, lists)
    }

    func testAnOversizedNoteIsRefusedBeforeVaniRuns() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        let saved = await m.saveNote(JobsFixture.roles[0].key, String(repeating: "x", count: JobsClient.maxNoteBytes + 1))
        XCTAssertFalse(saved)
        XCTAssertNotNil(m.actionError)
        XCTAssertFalse(f.calls().contains { $0.hasPrefix("jobs note") })
    }
}
