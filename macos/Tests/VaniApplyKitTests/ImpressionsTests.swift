import Foundation
import XCTest
@testable import VaniApplyKit

/// What a screen reports to `vani impressions log`: keys and positions, once per
/// slate, bounded, and never a posting's text.
final class ImpressionsTests: XCTestCase {
    func testRequestHoldsKeysAndPositionsOnly() throws {
        let data = try JobsClient.impressionsRequest(keys: ["gh:a:1", "gh:a:2"])
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertEqual(Set(json.keys), ["rows"])
        let rows = try XCTUnwrap(json["rows"] as? [[String: Any]])
        XCTAssertEqual(rows.count, 2)
        for r in rows { XCTAssertEqual(Set(r.keys), ["key", "position"], "a row carries no text: \(r)") }
        XCTAssertEqual(rows.map { $0["position"] as? Int }, [1, 2])
    }

    func testRequestPositionsStartWhereTheCallerSays() throws {
        let data = try JobsClient.impressionsRequest(keys: ["gh:a:1"], firstPosition: 7)
        let rows = try XCTUnwrap((JSONSerialization.jsonObject(with: data) as? [String: Any])?["rows"] as? [[String: Any]])
        XCTAssertEqual(rows.first?["position"] as? Int, 7)
        let low = try JobsClient.impressionsRequest(keys: ["gh:a:1"], firstPosition: 0)
        let lowRows = try XCTUnwrap((JSONSerialization.jsonObject(with: low) as? [String: Any])?["rows"] as? [[String: Any]])
        XCTAssertEqual(lowRows.first?["position"] as? Int, 1, "a position is 1-based")
    }

    func testRequestIsBoundedByVanisOwnLimit() throws {
        let keys = (0..<(JobsClient.maxImpressionRows + 50)).map { "gh:a:\($0)" }
        let data = try JobsClient.impressionsRequest(keys: keys)
        let rows = try XCTUnwrap((JSONSerialization.jsonObject(with: data) as? [String: Any])?["rows"] as? [[String: Any]])
        XCTAssertEqual(rows.count, JobsClient.maxImpressionRows)
    }

    func testSlateTokenIsTheTokenGoTakes() {
        XCTAssertEqual(JobsClient.slateToken("All 2026/10/20"), "all-2026-10-20")
        XCTAssertEqual(JobsClient.slateToken("2026-W43"), "2026-w43")
        XCTAssertEqual(JobsClient.slateToken("decile:3"), "decile:3")
        XCTAssertEqual(JobsClient.slateToken(String(repeating: "x", count: 100)).count, 64)
        XCTAssertEqual(JobsClient.slateToken(""), "")
    }

    func testJobsSlateIsOnePerViewPerLocalDay() {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "America/Los_Angeles")!
        let morning = cal.date(from: DateComponents(year: 2026, month: 10, day: 20, hour: 8))!
        let evening = cal.date(from: DateComponents(year: 2026, month: 10, day: 20, hour: 22))!
        let next = cal.date(from: DateComponents(year: 2026, month: 10, day: 21, hour: 1))!
        XCTAssertEqual(JobsClient.jobsSlate(view: .new, day: morning, calendar: cal), "new-20261020")
        XCTAssertEqual(JobsClient.jobsSlate(view: .new, day: evening, calendar: cal), JobsClient.jobsSlate(view: .new, day: morning, calendar: cal))
        XCTAssertNotEqual(JobsClient.jobsSlate(view: .new, day: next, calendar: cal), JobsClient.jobsSlate(view: .new, day: morning, calendar: cal))
        XCTAssertNotEqual(JobsClient.jobsSlate(view: .saved, day: morning, calendar: cal), JobsClient.jobsSlate(view: .new, day: morning, calendar: cal))
    }

    func testArguments() {
        XCTAssertEqual(JobsClient.impressionsArguments(surface: .jobs, slate: "New-1", section: "new"),
                       ["impressions", "log", "--json", "--surface", "jobs", "--slate", "new-1", "--section", "new"])
        XCTAssertEqual(JobsClient.impressionsArguments(surface: .audit, slate: "w1", section: ""),
                       ["impressions", "log", "--json", "--surface", "audit", "--slate", "w1"], "no section flag when there is none")
    }

    func testNothingShownSendsNothingAndAFailureIsThrown() throws {
        let missing = JobsClient(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani"), timeout: 2)
        XCTAssertEqual(try missing.logImpressions(surface: .jobs, slate: "s", keys: []), 0, "an empty list must not start vani")
        XCTAssertThrowsError(try missing.logImpressions(surface: .jobs, slate: "s", keys: ["gh:a:1"]),
                             "an unreported slate must be an error, never a silent pass")
    }
}
