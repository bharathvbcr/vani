import Foundation
import XCTest
@testable import VaniApplyKit

/// Replays Go's label table (internal/jobview/testdata/labels.golden.json,
/// written by `go test ./internal/jobview -update`) and holds the Swift
/// fallback to it.
final class LabelsTests: XCTestCase {
    override func setUp() { Labels.useFallback() }
    override func tearDown() { Labels.useFallback() }

    static var goldenURL: URL {
        URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent()
            .appendingPathComponent("internal/jobview/testdata/labels.golden.json")
    }

    func golden() throws -> LabelVocabulary {
        try JSONDecoder().decode(LabelVocabulary.self, from: Data(contentsOf: Self.goldenURL))
    }

    func testGoldenDecodesAtTheSupportedVersion() throws {
        let g = try golden()
        XCTAssertEqual(g.v, LabelVocabulary.supportedVersion, "bump LabelVocabulary.supportedVersion with jobview.LabelsVersion")
        XCTAssertEqual(g.fit.first, .init("strong_fit", "Strong fit"))
        XCTAssertEqual(g.label(for: "dead"), "Closed")
        XCTAssertEqual(g.label(for: "onsite"), "On-site")
        XCTAssertNil(g.label(for: "future_value"))
    }

    /// The built-in table is a fallback, not a second owner: it must equal Go's.
    func testFallbackEqualsGo() throws {
        let g = try golden(), f = LabelVocabulary.fallback
        XCTAssertEqual(f.fit, g.fit)
        XCTAssertEqual(f.status, g.status)
        XCTAssertEqual(f.category, g.category)
        XCTAssertEqual(f.triage, g.triage)
    }

    func testInstalledTableWinsAndTheSourceSaysSo() throws {
        XCTAssertEqual(Labels.source, .fallback)
        var g = try golden()
        g.fit[0].label = "Top match"
        g = LabelVocabulary(v: g.v, fit: g.fit, status: g.status, category: g.category, triage: g.triage)
        Labels.install(g)
        XCTAssertEqual(Labels.source, .vani)
        XCTAssertEqual(Job.label(for: "strong_fit"), "Top match")
        XCTAssertEqual(Job.roleFitOrder, g.fit.map(\.value))
        XCTAssertEqual(Job.seniorityOptions, ["intern_or_new_grad", "mid", "senior", "staff_plus", "manager"])
        var job = Job(key: "k", company: "c", title: "t")
        job.triage = ["role_fit": "strong_fit"]
        XCTAssertEqual(job.fitLabel, "Top match")
        Labels.useFallback()
        XCTAssertEqual(Labels.source, .fallback)
        XCTAssertEqual(Job.label(for: "strong_fit"), "Strong fit")
    }

    func testUnknownValuesAndNone() {
        XCTAssertEqual(Labels.label(for: "none"), "No reading")
        XCTAssertEqual(Labels.label(for: "some_new-value"), "some new value", "an option no table knows is never shown as a known word")
    }

    func testNewerTableIsRefusedByVersion() throws {
        let json = #"{"v":2,"unexpected":true}"#
        XCTAssertThrowsError(try JSONDecoder().decode(LabelVocabulary.self, from: Data(json.utf8))) { err in
            XCTAssertEqual(err as? LabelVocabulary.DecodeError, .init(version: 2))
        }
        XCTAssertThrowsError(try JSONDecoder().decode(LabelVocabulary.self, from: Data(#"{"v":1}"#.utf8)), "a missing list is an error, not an empty menu")
    }

    private func vani(_ t: XCTestCase, body: String) throws -> URL {
        let dir = try tempDir(t)
        let vani = dir.appendingPathComponent("vani")
        try Data("#!/bin/sh\n\(body)\n".utf8).write(to: vani)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: vani.path)
        return vani
    }

    func testClientReadsLabelsFromVani() throws {
        let bin = try vani(self, body: "[ \"$1 $2\" = \"labels --json\" ] || exit 2\ncat '\(Self.goldenURL.path)'")
        let table = try JobsClient(vaniBinary: bin).labels()
        XCTAssertEqual(table, try golden())
    }

    func testClientFailsLoudlyWhenVaniCannotAnswer() throws {
        let old = try vani(self, body: "echo 'vani: unknown command \"labels\"' >&2\nexit 2")
        XCTAssertThrowsError(try JobsClient(vaniBinary: old).labels())
        let junk = try vani(self, body: "echo '{\"v\":1}'")
        XCTAssertThrowsError(try JobsClient(vaniBinary: junk).labels())
        let empty = try vani(self, body: "exit 0")
        XCTAssertThrowsError(try JobsClient(vaniBinary: empty).labels())
    }
}
