import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// The Insights stats follow the newest list: a failed reload clears them and shows its error, and an
/// older list's reply never lands over a newer one's. Against the fake `vani jobs` (JobsFixture).
@MainActor
final class JobsStatsFreshnessTests: XCTestCase {
    private func model(_ f: JobsFixture) -> JobsModel {
        let vani = f.dir.appendingPathComponent("vani")
        let m = JobsModel { JobsClient(vaniBinary: vani, timeout: 10) }
        m.reconcileDelay = 30
        return m
    }

    private func statsJSON(_ f: JobsFixture) throws -> [String: Any] {
        let data = try Data(contentsOf: f.dir.appendingPathComponent("stats.json"))
        return try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    private func putStats(_ f: JobsFixture, total: Int) throws {
        var j = try statsJSON(f)
        j["total"] = total
        try JSONSerialization.data(withJSONObject: j).write(to: f.dir.appendingPathComponent("stats.json"))
    }

    func testAFailedReloadClearsTheStatsAndShowsTheError() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        m.showInsights = true
        await m.refresh()
        XCTAssertEqual(m.stats?.total, JobsFixture.roles.count)
        XCTAssertNil(m.statsError)

        let good = try statsJSON(f)
        try Data("not json\n".utf8).write(to: f.dir.appendingPathComponent("stats.json"))
        await m.refresh()
        XCTAssertNil(m.stats, "the old selection's charts must not sit under the failure")
        XCTAssertNotNil(m.statsError)

        var fixed = good
        fixed["total"] = 7
        try JSONSerialization.data(withJSONObject: fixed).write(to: f.dir.appendingPathComponent("stats.json"))
        await m.refresh()
        XCTAssertEqual(m.stats?.total, 7)
        XCTAssertNil(m.statsError)
    }

    /// The older list is held in its impressions report while a newer list lands; it then must not
    /// write its stats over the newer one's.
    func testAnOlderListsStatsDoNotOverwriteANewerOnes() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        m.showInsights = true
        await m.refresh() // labels read, nothing in flight
        let script = f.dir.appendingPathComponent("vani")
        var text = try String(contentsOf: script, encoding: .utf8)
        let hook = "\"impressions log\")"
        XCTAssertTrue(text.contains(hook))
        text = text.replacingOccurrences(of: hook, with: hook + " if [ -f \"$d/slow-imp\" ]; then rm -f \"$d/slow-imp\"; : > \"$d/imp.waiting\"; sleep 1.5; fi;")
        try Data(text.utf8).write(to: script)
        try Data().write(to: f.dir.appendingPathComponent("slow-imp"))
        try putStats(f, total: 4)

        let older = Task { await m.refresh() }
        let deadline = Date().addingTimeInterval(5)
        while !f.exists("imp.waiting") {
            if Date() > deadline { return XCTFail("the older list never reached its impressions report") }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        try putStats(f, total: 99)
        await m.refresh()
        XCTAssertEqual(m.stats?.total, 99)
        await older.value
        XCTAssertEqual(m.stats?.total, 99, "the older reply must be dropped")
    }
}
