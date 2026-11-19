import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// The quit path: open note drafts are saved before the app exits, within a bound, and a draft that
/// could not be saved is reported, never dropped.
@MainActor
final class JobsNoteQuitFlushTests: XCTestCase {
    private let a = JobsFixture.roles[0].key
    private let b = JobsFixture.roles[1].key

    private func model(_ f: JobsFixture) -> JobsModel {
        let vani = f.dir.appendingPathComponent("vani")
        let m = JobsModel { JobsClient(vaniBinary: vani, timeout: 10) }
        m.reconcileDelay = 30
        return m
    }

    func testFlushSavesAnOpenDraft() async throws {
        let f = try JobsFixture(self)
        try f.setWriteReply(a, note: "typed")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "typed")
        XCTAssertTrue(m.hasNoteDrafts)
        let unsaved = await m.flushNoteDrafts(timeout: 5)
        XCTAssertEqual(unsaved, [])
        XCTAssertFalse(m.hasNoteDrafts)
        XCTAssertEqual(f.calls().filter { $0.hasPrefix("jobs note") }.count, 1)
    }

    func testFlushWithNoDraftsIsImmediateAndSendsNothing() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        let start = Date()
        let unsaved = await m.flushNoteDrafts(timeout: 5)
        XCTAssertEqual(unsaved, [])
        XCTAssertLessThan(Date().timeIntervalSince(start), 1)
        XCTAssertTrue(f.calls().filter { $0.hasPrefix("jobs note") }.isEmpty)
    }

    /// A save that outlives the bound is reported unsaved: the draft stays, the flush returns on time.
    func testASlowSaveIsReportedUnsavedAtTheBoundAndTheDraftStays() async throws {
        let f = try JobsFixture(self)
        let script = f.dir.appendingPathComponent("vani")
        var text = try String(contentsOf: script, encoding: .utf8)
        let hook = "\"jobs mark\"|\"jobs note\")"
        XCTAssertTrue(text.contains(hook))
        text = text.replacingOccurrences(of: hook, with: hook + " if [ \"$2\" = note ]; then sleep 3; fi;")
        try Data(text.utf8).write(to: script)
        try f.setWriteReply(a, note: "slow")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "slow")
        let start = Date()
        let unsaved = await m.flushNoteDrafts(timeout: 0.4)
        XCTAssertLessThan(Date().timeIntervalSince(start), 2.5, "the flush must honour its bound")
        XCTAssertEqual(unsaved, [a])
        XCTAssertEqual(m.noteDrafts[a], "slow")
    }

    /// An oversized draft can never be saved: it is reported, and stays.
    func testAnOversizedDraftIsReportedUnsaved() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        m.editNote(a, String(repeating: "x", count: JobsClient.maxNoteBytes + 1))
        let unsaved = await m.flushNoteDrafts(timeout: 1)
        XCTAssertEqual(unsaved, [a])
        XCTAssertNotNil(m.noteDrafts[a])
    }

    func testSeveralDraftsAreAllSaved() async throws {
        let f = try JobsFixture(self)
        try f.setWriteReply(a, note: "one")
        try f.setWriteReply(b, note: "two")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "one")
        m.editNote(b, "two")
        let unsaved = await m.flushNoteDrafts(timeout: 8)
        XCTAssertEqual(unsaved, [])
        XCTAssertEqual(f.calls().filter { $0.hasPrefix("jobs note") }.count, 2)
    }
}
