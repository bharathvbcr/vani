import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// A failed refresh keeps the rows but says they are from the last good load; they are never presented
/// as current. Against the fake `vani jobs` (JobsFixture).
@MainActor
final class JobsStaleRowsTests: XCTestCase {
    private func model(_ f: JobsFixture) -> JobsModel {
        let vani = f.dir.appendingPathComponent("vani")
        let m = JobsModel { JobsClient(vaniBinary: vani, timeout: 10) }
        m.reconcileDelay = 30
        return m
    }

    func testAFailedRefreshMarksTheKeptRowsAsFromTheLastGoodLoad() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        let before = Date()
        await m.refresh()
        XCTAssertNil(m.error)
        XCTAssertNil(m.lastGoodLoad, "fresh rows are current")
        let loaded = try XCTUnwrap(m.pageLoadedAt)
        XCTAssertGreaterThanOrEqual(loaded, before)

        try f.breakListReply()
        await m.refresh()
        XCTAssertNotNil(m.error)
        XCTAssertEqual(m.jobs.map(\.key), JobsFixture.standardOrder, "the rows stay")
        XCTAssertEqual(m.lastGoodLoad, loaded, "and say when they were read")

        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4])
        await m.refresh()
        XCTAssertNil(m.error)
        XCTAssertNil(m.lastGoodLoad, "a good load makes them current again")
    }

    func testNoRowsAndAFailureIsNotAStaleLoad() async throws {
        let f = try JobsFixture(self)
        try f.breakListReply()
        let m = model(f)
        await m.refresh()
        XCTAssertNotNil(m.error)
        XCTAssertNil(m.page)
        XCTAssertNil(m.lastGoodLoad, "nothing was loaded, so the view shows its own error, not stale rows")
    }
}
