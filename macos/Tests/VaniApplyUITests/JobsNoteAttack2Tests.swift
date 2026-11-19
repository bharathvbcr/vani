import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// Round-2 attacks (ft-189e): queued note saves, flushNoteDrafts, the blank-draft rule. Model level, fake vani.
@MainActor
final class JobsNoteAttack2Tests: XCTestCase {
    private func model(_ f: JobsFixture) -> JobsModel {
        let vani = f.dir.appendingPathComponent("vani")
        let m = JobsModel { JobsClient(vaniBinary: vani, timeout: 10) }
        m.reconcileDelay = 30
        return m
    }

    private func eventually(_ what: String, _ done: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(10)
        while !done() {
            if Date() > deadline { return XCTFail("timed out waiting for \(what)") }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    private func noteCalls(_ f: JobsFixture) -> [String] { f.calls().filter { $0.hasPrefix("jobs note") } }
    private let a = JobsFixture.roles[0].key
    private let b = JobsFixture.roles[1].key

    /// Every note save sleeps `secs` seconds (after the call is logged) before the reply is read.
    private func slowNotes(_ f: JobsFixture, _ secs: String = "1") throws {
        let script = f.dir.appendingPathComponent("vani")
        var text = try String(contentsOf: script, encoding: .utf8)
        let hook = "\"jobs mark\"|\"jobs note\")"
        XCTAssertTrue(text.contains(hook))
        text = text.replacingOccurrences(of: hook, with: hook + " if [ \"$2\" = note ]; then : > \"$d/note.waiting\"; sleep \(secs); fi;")
        try Data(text.utf8).write(to: script)
    }

    func testThreeEditsWhileASaveIsInFlightStoreOnlyTheLatest() async throws {
        let f = try JobsFixture(self)
        try slowNotes(f)
        let m = model(f)
        try f.setWriteReply(a, note: "A1")
        await m.refresh()
        m.editNote(a, "A1")
        let first = Task { await m.commitNote(self.a) }
        try await eventually("first save in flight") { f.exists("note.waiting") }
        m.editNote(a, "A2")
        let q2 = await m.commitNote(a)
        XCTAssertFalse(q2)
        m.editNote(a, "A3")
        let q3 = await m.commitNote(a)
        XCTAssertFalse(q3)
        _ = await first.value
        try await eventually("A3 saved") { self.noteCalls(f).contains { $0.contains("A3") } && self.m_idle(m) }
        let calls = noteCalls(f)
        XCTAssertEqual(calls.count, 2, "\(calls)")
        XCTAssertFalse(calls.contains { $0.contains("A2") }, "\(calls)")
        XCTAssertNil(m.noteDrafts[a])
    }
    private func m_idle(_ m: JobsModel) -> Bool { m.noteDrafts[a] == nil }

    func testAKnownNoteClearedToBlankIsSent() async throws {
        let f = try JobsFixture(self)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: "keep me"])
        try f.setWriteReply(a, note: "")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "   ")
        XCTAssertNotNil(m.noteDrafts[a])
        let saved = await m.commitNote(a)
        XCTAssertTrue(saved, "\(f.calls())")
        XCTAssertEqual(noteCalls(f).count, 1)
    }

    func testAQueuedSaveAfterAFailedSaveKeepsTheDraftAndShowsTheError() async throws {
        let f = try JobsFixture(self)
        try slowNotes(f)
        let m = model(f)
        try f.setWriteReply(a, note: "A1")
        await m.refresh()
        m.editNote(a, "A1")
        let first = Task { await m.commitNote(self.a) }
        try await eventually("save in flight") { f.exists("note.waiting") }
        m.editNote(a, "A2")
        _ = await m.commitNote(a)
        try FileManager.default.removeItem(at: f.dir.appendingPathComponent("write-\(a).json")) // both saves now fail
        _ = await first.value
        XCTAssertNotNil(m.actionError)
        XCTAssertEqual(m.noteDrafts[a], "A2")
        XCTAssertEqual(noteCalls(f).count, 2, "bounded: one save and one queued retry, no loop: \(noteCalls(f))")
    }

    func testAQueuedSaveWhoseJobLeftTheListDoesNotLoseOrLoop() async throws {
        let f = try JobsFixture(self)
        try slowNotes(f)
        let m = model(f)
        try f.setWriteReply(a, note: "A2")
        await m.refresh()
        m.editNote(a, "A1")
        let first = Task { await m.commitNote(self.a) }
        try await eventually("save in flight") { f.exists("note.waiting") }
        m.editNote(a, "A2")
        _ = await m.commitNote(a)
        try f.setListReply(JobsFixture.standardOrder.filter { $0 != a }, counts: ["new": 3, "all": 4])
        await m.refresh()
        _ = await first.value
        try await Task.sleep(nanoseconds: 1_500_000_000)
        XCTAssertLessThanOrEqual(noteCalls(f).count, 3, "\(noteCalls(f))")
        XCTAssertTrue(noteCalls(f).contains { $0.contains("A2") }, "the newest draft was lost: \(noteCalls(f)) draft=\(String(describing: m.noteDrafts[a]))")
    }

    func testTwoConcurrentFlushesWhileASaveIsInFlightBothReturnEmpty() async throws {
        let f = try JobsFixture(self)
        try slowNotes(f)
        let m = model(f)
        try f.setWriteReply(a, note: "A1")
        await m.refresh()
        m.editNote(a, "A1")
        let first = Task { await m.commitNote(self.a) }
        try await eventually("save in flight") { f.exists("note.waiting") }
        m.editNote(a, "A2")
        async let r1 = m.flushNoteDrafts(timeout: 8)
        async let r2 = m.flushNoteDrafts(timeout: 8)
        let (x, y) = await (r1, r2)
        _ = await first.value
        XCTAssertEqual(x, [])
        XCTAssertEqual(y, [])
        XCTAssertEqual(noteCalls(f).count, 2, "\(noteCalls(f))")
        XCTAssertNil(m.noteDrafts[a])
    }

    func testFlushWithZeroTimeoutReturnsTheOpenKeysAndDoesNotCrash() async throws {
        let f = try JobsFixture(self)
        try slowNotes(f, "0.5")
        let m = model(f)
        try f.setWriteReply(a, note: "x")
        await m.refresh()
        m.editNote(a, "x")
        let r = await m.flushNoteDrafts(timeout: 0)
        XCTAssertEqual(r, [a])
        try await eventually("late save lands") { m.noteDrafts[self.a] == nil }
        XCTAssertEqual(noteCalls(f).count, 1)
    }

    func testADraftEditedDuringTheFlushIsReportedNotSilentlyDropped() async throws {
        let f = try JobsFixture(self)
        try slowNotes(f)
        let m = model(f)
        try f.setWriteReply(a, note: "A1")
        await m.refresh()
        m.editNote(a, "A1")
        async let r = m.flushNoteDrafts(timeout: 8)
        try await eventually("save in flight") { f.exists("note.waiting") }
        m.editNote(a, "A2")
        let unsaved = await r
        XCTAssertEqual(unsaved, m.noteDrafts.keys.sorted(), "returns exactly the unsaved keys")
        XCTAssertEqual(unsaved, [a], "A2 was typed during the flush and is unsaved")
    }
}
