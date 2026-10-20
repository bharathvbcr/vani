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

    /// The label table is read once from `vani labels --json` and then drives
    /// every menu and chart; the built-in table is not shown as vani's.
    func testLabelsAreReadFromVaniOnce() async throws {
        Labels.useFallback()
        defer { Labels.useFallback() }
        let f = try JobsFixture(self)
        let m = model(f)
        XCTAssertNotNil(m.labelsNote, "before the read, the fallback is declared")
        await m.refresh()
        await m.refresh()
        XCTAssertNil(m.labelsNote)
        XCTAssertEqual(Labels.source, .vani)
        XCTAssertEqual(Job.label(for: "strong_fit"), "Best fit", "the menus read vani's words, not the built-in ones")
        XCTAssertEqual(f.calls().filter { $0 == "labels --json" }.count, 1, "\(f.calls())")
        XCTAssertEqual(m.jobs.map(\.key), JobsFixture.standardOrder)
    }

    func testUnreadableLabelsShowFallbackVisiblyAndRetry() async throws {
        Labels.useFallback()
        defer { Labels.useFallback() }
        let f = try JobsFixture(self, labels: false)
        let m = model(f)
        await m.refresh()
        let note = try XCTUnwrap(m.labelsNote, "a fallback must not look like vani's table")
        XCTAssertTrue(note.contains("built-in"), note)
        XCTAssertEqual(Labels.source, .fallback)
        XCTAssertEqual(Job.label(for: "strong_fit"), "Strong fit")
        XCTAssertNil(m.error, "the list still loads without labels")
        XCTAssertEqual(m.jobs.map(\.key), JobsFixture.standardOrder)
        await m.refresh()
        XCTAssertEqual(f.calls().filter { $0 == "labels --json" }.count, 2, "an unread table is retried at the next refresh")
    }

    func testRefreshListsInStandardOrder() async throws {
        let m = model(try JobsFixture(self))
        await m.refresh()
        XCTAssertNil(m.error)
        XCTAssertFalse(m.loading)
        XCTAssertEqual(m.jobs.map(\.key), JobsFixture.standardOrder)
        XCTAssertNil(m.stats, "stats load only while Insights is open")
    }

    /// Each refresh tells vani which rows it put on screen: keys and 1-based
    /// positions in list order, under one slate for the view and day, and no
    /// title or company.
    func testRefreshReportsTheVisibleRowsAsImpressions() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        XCTAssertNil(m.impressionsError)
        let calls = f.calls().filter { $0.hasPrefix("impressions log") }
        XCTAssertEqual(calls.count, 1, "\(f.calls())")
        XCTAssertTrue(calls[0].contains("--surface jobs") && calls[0].contains("--slate new-"), calls[0])
        let sent = try String(contentsOf: f.dir.appendingPathComponent("impressions.in"), encoding: .utf8)
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(sent.utf8)) as? [String: Any])
        let rows = try XCTUnwrap(body["rows"] as? [[String: Any]])
        XCTAssertEqual(rows.compactMap { $0["key"] as? String }, JobsFixture.standardOrder)
        XCTAssertEqual(rows.compactMap { $0["position"] as? Int }, Array(1...JobsFixture.standardOrder.count))
        for role in JobsFixture.roles {
            XCTAssertFalse(sent.contains(role.title) || sent.contains(role.company), "posting text reached the impressions request")
        }
        await m.refresh()
        XCTAssertEqual(f.calls().filter { $0.hasPrefix("impressions log") }.count, 2, "a refresh reports again; vani keeps each key once per slate")
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

    /// New opens by priority; another view gets its own default; a sort the owner
    /// picked is kept across view changes.
    func testEachViewOpensInItsDefaultSortUnlessOneWasPicked() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        XCTAssertEqual(m.sort, .priority)
        await m.refresh()
        XCTAssertTrue(f.calls().contains { $0.contains("--sort priority") }, "\(f.calls())")

        m.view = .saved
        XCTAssertEqual(m.sort, .standard, "marked views list by when they were marked")
        m.view = .new
        XCTAssertEqual(m.sort, .priority)

        m.sort = .fresh
        m.view = .applied
        XCTAssertEqual(m.sort, .fresh, "a picked sort survives a view change")
        m.view = .new
        XCTAssertEqual(m.sort, .fresh)
        try await eventually("the fresh order") { m.jobs.map(\.key) == JobsFixture.freshOrder }
    }
}
