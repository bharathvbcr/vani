import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// Hide-duplicates is client-side. The export must send the rows on screen, and
/// the flight flag must stay set through the alert and the save panel.
@MainActor
final class JobsExportFlightTests: XCTestCase {
    func testHideDuplicatesExportsOnScreenKeysNotTheServerTotal() {
        let keys = ["fx:alpha:1", "fx:beta:2", "名前/odd"]
        XCTAssertEqual(
            OnScreenExport.scope(hideDuplicates: true, onScreenKeys: keys, pageTotal: 4000, choice: .top(2)),
            .keys(["fx:alpha:1", "fx:beta:2"]))
        XCTAssertEqual(
            OnScreenExport.scope(hideDuplicates: true, onScreenKeys: keys, pageTotal: 4000, choice: .all),
            .keys(keys))
        XCTAssertEqual(OnScreenExport.menuTotal(hideDuplicates: true, onScreenCount: keys.count, pageTotal: 4000), keys.count)
        XCTAssertEqual(
            OnScreenExport.scope(hideDuplicates: false, onScreenKeys: keys, pageTotal: 4000, choice: .top(100)),
            .top(100))
        XCTAssertEqual(
            OnScreenExport.scope(hideDuplicates: false, onScreenKeys: keys, pageTotal: 4000, choice: .all),
            .top(JobsClient.maxExportRows))
        XCTAssertEqual(OnScreenExport.menuTotal(hideDuplicates: false, onScreenCount: 3, pageTotal: 4000), 4000)
        XCTAssertEqual(
            OnScreenExport.scope(hideDuplicates: true, onScreenKeys: keys, pageTotal: 9, choice: .selected(["fx:beta:2"])),
            .keys(["fx:beta:2"]))
    }

    func testOnScreenExportBounds() {
        XCTAssertEqual(OnScreenExport.scope(hideDuplicates: true, onScreenKeys: [], pageTotal: 10, choice: .all), .keys([]))
        XCTAssertEqual(OnScreenExport.scope(hideDuplicates: true, onScreenKeys: ["a"], pageTotal: 10, choice: .top(0)), .keys([]))
        XCTAssertEqual(OnScreenExport.scope(hideDuplicates: true, onScreenKeys: ["a"], pageTotal: 10, choice: .top(-1)), .keys([]))
        let many = (0..<JobsClient.maxExportRows + 3).map { "k:\($0)" }
        if case let .keys(got) = OnScreenExport.scope(hideDuplicates: true, onScreenKeys: many, pageTotal: many.count, choice: .all) {
            XCTAssertEqual(got.count, JobsClient.maxExportRows)
            XCTAssertEqual(got.first, "k:0")
            XCTAssertEqual(got.last, "k:\(JobsClient.maxExportRows - 1)")
        } else {
            XCTFail("all of a long on-screen list is keys, capped")
        }
        XCTAssertEqual(OnScreenExport.menuTotal(hideDuplicates: true, onScreenCount: 0, pageTotal: 10), 0)
    }

    func testCheckDoesNotReleaseAFlagItDidNotTake() async throws {
        let f = try JobsFixture(self)
        let m = JobsModel { JobsClient(vaniBinary: f.dir.appendingPathComponent("vani"), timeout: 10) }
        XCTAssertTrue(m.beginExport())
        let reply = await m.checkExport(.keys(["fx:alpha:1"]))
        XCTAssertNotNil(reply, "the check ran")
        XCTAssertTrue(m.exporting, "checkExport cleared the flight before the panel")
        let calls = f.calls().filter { $0.hasPrefix("jobs export") }
        XCTAssertEqual(calls.count, 1, "\(f.calls())")
        XCTAssertTrue(calls[0].contains("--keys-stdin"), calls[0])
        XCTAssertTrue(calls[0].contains("--dry-run"), calls[0])
        m.endExport()
        XCTAssertFalse(m.exporting)
        XCTAssertTrue(m.beginExport())
        m.endExport()
    }

    func testExportingStaysSetThroughThePanelsAndASecondExportDoesNotStart() async throws {
        let f = try JobsFixture(self)
        let m = JobsModel { JobsClient(vaniBinary: f.dir.appendingPathComponent("vani"), timeout: 10) }
        let path = f.dir.appendingPathComponent("名前 export.xlsx")
        var asked = false
        var saved = false
        let panels = ExportPanels(
            ask: { prompt in
                asked = true
                XCTAssertTrue(m.exporting)
                XCTAssertFalse(m.beginExport(), "a second export started during the alert")
                XCTAssertTrue(prompt.title.contains("exported before"), prompt.title)
                return .skip
            },
            save: { _, scope in
                saved = true
                XCTAssertTrue(m.exporting)
                XCTAssertFalse(m.beginExport(), "a second export started during the save panel")
                if case let .keys(keys) = scope {
                    XCTAssertEqual(keys, ["fx:alpha:1", "名前/odd"])
                } else {
                    XCTFail("scope \(scope)")
                }
                return path
            }
        )
        exportJobs(m, .keys(["fx:alpha:1", "名前/odd"]), panels: panels)
        let deadline = Date().addingTimeInterval(5)
        while !(asked && saved && !m.exporting) {
            if Date() > deadline {
                XCTFail("asked \(asked) saved \(saved) exporting \(m.exporting) error \(m.actionError ?? "")")
                return
            }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTAssertNil(m.actionError)
        let writes = f.calls().filter { $0.hasPrefix("jobs export") && $0.contains("--out=") }
        XCTAssertEqual(writes.count, 1, "\(f.calls())")
        XCTAssertTrue(writes[0].contains("名前 export.xlsx"), writes[0])
        XCTAssertTrue(writes[0].contains("--already-exported skip"), writes[0])
    }

    func testADirectExportStillRefusesASecondFlight() async throws {
        let f = try JobsFixture(self)
        let m = JobsModel { JobsClient(vaniBinary: f.dir.appendingPathComponent("vani"), timeout: 10) }
        XCTAssertTrue(m.beginExport())
        await m.export(.keys(["fx:alpha:1"]), to: f.dir.appendingPathComponent("a.xlsx"), already: .include)
        XCTAssertTrue(m.exporting, "export cleared a flag it did not take")
        XCTAssertFalse(m.beginExport())
        m.endExport()
    }
}
