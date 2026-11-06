import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// Hostile paths around the export flight: cancel, a save that writes nothing,
/// an empty key list, a second call queued before the first panel, and duplicate keys.
@MainActor
final class JobsExportFlightAdversarialTests: XCTestCase {
    func testCancelReleasesTheFlagAndWritesNothing() async throws {
        let f = try JobsFixture(self)
        let m = JobsModel { JobsClient(vaniBinary: f.dir.appendingPathComponent("vani"), timeout: 10) }
        var asked = false
        let panels = ExportPanels(
            ask: { _ in
                asked = true
                XCTAssertTrue(m.exporting)
                return nil
            },
            save: { _, _ in
                XCTFail("the save panel ran after cancel")
                return nil
            }
        )
        exportJobs(m, .keys(["fx:alpha:1", "名前/odd"]), panels: panels)
        try await waitUntil { asked && !m.exporting }
        XCTAssertNil(m.actionError)
        XCTAssertTrue(m.beginExport(), "cancel left the flight set")
        m.endExport()
        let writes = f.calls().filter { $0.hasPrefix("jobs export") && $0.contains("--out=") }
        XCTAssertEqual(writes.count, 0, "\(f.calls())")
    }

    func testSaveReturningNilReleasesTheFlagAndWritesNothing() async throws {
        let f = try JobsFixture(self)
        let m = JobsModel { JobsClient(vaniBinary: f.dir.appendingPathComponent("vani"), timeout: 10) }
        var saved = false
        let panels = ExportPanels(
            ask: { _ in .include },
            save: { _, scope in
                saved = true
                XCTAssertTrue(m.exporting)
                if case let .keys(keys) = scope {
                    XCTAssertEqual(keys, ["名前/odd"])
                } else {
                    XCTFail("scope \(scope)")
                }
                return nil
            }
        )
        exportJobs(m, .keys(["名前/odd"]), panels: panels)
        try await waitUntil { saved && !m.exporting }
        XCTAssertNil(m.actionError)
        XCTAssertTrue(m.beginExport())
        m.endExport()
        let writes = f.calls().filter { $0.hasPrefix("jobs export") && $0.contains("--out=") }
        XCTAssertEqual(writes.count, 0, "\(f.calls())")
    }

    func testEmptyKeyListFailsAndReleasesTheFlag() async throws {
        let f = try JobsFixture(self)
        let m = JobsModel { JobsClient(vaniBinary: f.dir.appendingPathComponent("vani"), timeout: 10) }
        var asked = false
        let panels = ExportPanels(
            ask: { _ in asked = true; return .skip },
            save: { _, _ in XCTFail("saved an empty selection"); return nil }
        )
        exportJobs(m, .keys([]), panels: panels)
        // `exporting` is false until the task starts, so wait for the failure it records.
        try await waitUntil { m.actionError != nil && !m.exporting }
        XCTAssertFalse(asked)
        XCTAssertTrue(m.actionError?.contains("Select at least one") == true, "error \(m.actionError ?? "nil")")
        XCTAssertTrue(m.beginExport())
        m.endExport()
        XCTAssertEqual(f.calls().filter { $0.hasPrefix("jobs export") }.count, 0, "\(f.calls())")
    }

    func testASecondExportQueuedBeforeThePanelDoesNotStart() async throws {
        let f = try JobsFixture(self)
        let m = JobsModel { JobsClient(vaniBinary: f.dir.appendingPathComponent("vani"), timeout: 10) }
        var asks = 0
        let panels = ExportPanels(
            ask: { _ in
                asks += 1
                XCTAssertFalse(m.beginExport(), "the queued export took the flight")
                return .skip
            },
            save: { _, _ in f.dir.appendingPathComponent("one.xlsx") }
        )
        exportJobs(m, .keys(["fx:alpha:1"]), panels: panels)
        exportJobs(m, .keys(["fx:beta:2"]), panels: panels)
        try await waitUntil { asks == 1 && !m.exporting }
        XCTAssertEqual(asks, 1)
        let writes = f.calls().filter { $0.hasPrefix("jobs export") && $0.contains("--out=") }
        XCTAssertEqual(writes.count, 1, "\(f.calls())")
        XCTAssertTrue(writes[0].contains("one.xlsx"), writes[0])
        XCTAssertFalse(writes[0].contains("fx:beta:2"), writes[0])
    }

    func testDuplicateAndOddKeysAreSentAsGiven() {
        let keys = ["fx:alpha:1", "fx:alpha:1", "a/../../b", "名前/odd", ""]
        XCTAssertEqual(
            OnScreenExport.scope(hideDuplicates: true, onScreenKeys: keys, pageTotal: 9, choice: .selected(keys)),
            .keys(keys))
        XCTAssertEqual(
            OnScreenExport.scope(hideDuplicates: false, onScreenKeys: ["only"], pageTotal: 0, choice: .top(100)),
            .top(100))
    }

    private func waitUntil(file: StaticString = #filePath, line: UInt = #line, _ ready: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(5)
        while !ready() {
            if Date() > deadline {
                XCTFail("timed out", file: file, line: line)
                return
            }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }
}
