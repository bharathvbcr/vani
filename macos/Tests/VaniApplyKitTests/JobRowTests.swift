import Foundation
import XCTest
@testable import VaniApplyKit

/// Replays Go's golden rows (internal/jobview/testdata/rows.golden.json, written
/// by `go test ./internal/jobview -update`) through the one JobRow type, and
/// holds the Swift fallback vocabulary to Go's.
final class JobRowTests: XCTestCase {
    struct Golden: Decodable {
        let version: Int
        let fit_order: [String]
        let fit_labels: [String: String]
        let status_labels: [String: String]
        let rows: [JobRow]
    }

    static func goldenData() throws -> Data {
        // Tests/VaniApplyKitTests/<file> -> macos -> Vani/internal/jobview/testdata
        let url = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent()
            .appendingPathComponent("internal/jobview/testdata/rows.golden.json")
        return try Data(contentsOf: url)
    }

    func golden() throws -> Golden { try JSONDecoder().decode(Golden.self, from: Self.goldenData()) }

    func testGoldenRowsDecodeAtTheSupportedVersion() throws {
        let g = try golden()
        XCTAssertEqual(g.version, JobRow.supportedVersion, "bump JobRow.supportedVersion with jobview.Version")
        XCTAssertGreaterThanOrEqual(g.rows.count, 5, "golden file looks truncated")
        let first = try XCTUnwrap(g.rows.first)
        XCTAssertEqual(first.fit, "strong_fit")
        XCTAssertEqual(first.fitLabel, "Strong fit")
        XCTAssertEqual(first.statusLabel, "Scored")
        XCTAssertEqual(first.ageLabel, "3d")
        XCTAssertEqual(first.reasons?.count, 2)
        XCTAssertEqual(g.rows.first { $0.status == "duplicate" }?.similar, 2)
        XCTAssertNil(g.rows.first { $0.status == "duplicate" }?.fit, "a reading outside the fixture carries no fit")
    }

    func testEveryGoldenRowRoundTripsWithoutLoss() throws {
        for row in try golden().rows {
            let back = try JSONDecoder().decode(JobRow.self, from: JSONEncoder().encode(row))
            XCTAssertEqual(back, row, row.key)
        }
    }

    func testAnUnknownMajorVersionIsRefusedLoudly() throws {
        let raw = try XCTUnwrap(JSONSerialization.jsonObject(with: Self.goldenData()) as? [String: Any])
        var row = try XCTUnwrap((raw["rows"] as? [[String: Any]])?.first)
        for v in [0, 2, 99, -1] {
            row["v"] = v
            let data = try JSONSerialization.data(withJSONObject: row)
            XCTAssertThrowsError(try JSONDecoder().decode(JobRow.self, from: data), "v=\(v)") {
                XCTAssertEqual($0 as? JobRow.DecodeError, JobRow.DecodeError(version: v))
                XCTAssertTrue(String(describing: $0).contains("not supported"))
            }
        }
        row.removeValue(forKey: "v")
        XCTAssertThrowsError(try JSONDecoder().decode(JobRow.self, from: JSONSerialization.data(withJSONObject: row)))
    }

    /// A newer vani's row inside a list fails the whole page: the Jobs tab shows
    /// an error rather than a list of mis-rendered rows.
    func testAJobWithAnUnknownRowVersionFailsTheDecode() throws {
        let raw = try XCTUnwrap(JSONSerialization.jsonObject(with: Self.goldenData()) as? [String: Any])
        var row = try XCTUnwrap((raw["rows"] as? [[String: Any]])?.first)
        row["v"] = 2
        let job: [String: Any] = ["key": "k", "company": "C", "title": "T", "status": "scored", "row": row]
        let data = try JSONSerialization.data(withJSONObject: job)
        XCTAssertThrowsError(try JSONDecoder().decode(Job.self, from: data))
    }

    func testJobShowsGoSuppliedLabelsAndFallsBackOnlyWithoutARow() throws {
        let g = try golden()
        var row = try XCTUnwrap(g.rows.first)
        var job = Job(key: row.key, company: row.company, title: row.title, status: row.status)
        job.triage = ["role_fit": "strong_fit"]
        XCTAssertEqual(job.fitLabel, "Strong fit", "no row: the labelled fallback table")
        row.fitLabel = "Go says so"
        row.ageLabel = "Go age"
        job.row = row
        XCTAssertEqual(job.fitLabel, "Go says so", "a row's own string wins over the fallback")
        XCTAssertEqual(job.ageLabel, "Go age")
        row.fit = nil
        row.fitLabel = nil
        job.row = row
        XCTAssertNil(job.fitLabel, "a row with no reading shows none, even when triage carries one")
    }

    func testFallbackFitVocabularyMatchesGo() throws {
        let g = try golden()
        XCTAssertEqual(Job.roleFitOrder, g.fit_order)
        for f in g.fit_order {
            XCTAssertEqual(Job.label(for: f), g.fit_labels[f], f)
        }
    }

    func testFindReplyPassesTheRowThrough() throws {
        let row = try XCTUnwrap(try golden().rows.first)
        let rowJSON = try XCTUnwrap(row.jsonObject)
        let reply: [String: Any] = [
            "url": "https://example.com/a", "matches": 1,
            "job": ["key": row.key, "company": row.company, "title": row.title, "status": row.status,
                    "matched_on": "url", "row": rowJSON] as [String: Any],
        ]
        let match = try JSONDecoder().decode(JobMatch.self, from: JSONSerialization.data(withJSONObject: reply))
        let out = match.asJSONObject
        let job = try XCTUnwrap(out["job"] as? [String: Any])
        let passed = try XCTUnwrap(job["row"] as? [String: Any])
        XCTAssertEqual(passed["fit_label"] as? String, "Strong fit")
        XCTAssertEqual(passed["v"] as? Int, 1)
    }
}
