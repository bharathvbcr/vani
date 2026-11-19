import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// Final attacks (ft-189e round 4): save generation, learnSuppressed, notesConfirmedBySave. Model level, fake vani.
@MainActor
final class JobsNoteAttack4Tests: XCTestCase {
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

    private func slowNotes(_ f: JobsFixture, _ secs: String = "1") throws {
        let script = f.dir.appendingPathComponent("vani")
        var text = try String(contentsOf: script, encoding: .utf8)
        let hook = "\"jobs mark\"|\"jobs note\")"
        XCTAssertTrue(text.contains(hook))
        text = text.replacingOccurrences(of: hook, with: hook + " if [ \"$2\" = note ]; then : > \"$d/note.waiting.$4\"; : > \"$d/note.waiting\"; sleep \(secs); fi;")
        try Data(text.utf8).write(to: script)
    }

    /// The next `jobs show` reads its reply at once (a snapshot), waits a second, then prints the snapshot.
    private func slowShow(_ f: JobsFixture) throws {
        let script = f.dir.appendingPathComponent("vani")
        var text = try String(contentsOf: script, encoding: .utf8)
        let hook = "for last; do :; done"
        XCTAssertTrue(text.contains(hook))
        text = text.replacingOccurrences(of: hook, with: hook + "; if [ -f \"$d/slowshow\" ]; then rm -f \"$d/slowshow\"; cp \"$d/show-$last.json\" \"$d/show.snap\"; : > \"$d/show.waiting\"; sleep 1; exec cat \"$d/show.snap\"; fi")
        try Data(text.utf8).write(to: script)
        try Data().write(to: f.dir.appendingPathComponent("slowshow"))
    }

    private func setShowNote(_ f: JobsFixture, _ key: String, _ note: String) throws {
        let url = f.dir.appendingPathComponent("show-\(key).json")
        var j = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: url)) as? [String: Any])
        j["note"] = note
        try JSONSerialization.data(withJSONObject: j).write(to: url)
    }





    /// A detail read that STARTS after the save ended is learned.
    func testDetailReadStartedAfterTheSaveEndedIsLearned() async throws {
        let f = try JobsFixture(self)
        try f.setWriteReply(a, note: "N1")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "N1")
        let ok = await m.commitNote(a)
        XCTAssertTrue(ok)
        try setShowNote(f, a, "EXT")
        m.selection = a
        try await eventually("detail landed") { m.detail?.note == "EXT" }
        m.editNote(a, "EXT")
        XCTAssertNil(m.noteDrafts[a], "a read that started after the save was not learned")
        m.editNote(a, "N1")
        XCTAssertNotNil(m.noteDrafts[a], "the old saved note is still believed")
    }

    /// A suppressed read does not leave learning off: the next read of the job is learned.
    func testSuppressedReadDoesNotSilenceALaterRead() async throws {
        let f = try JobsFixture(self)
        try slowShow(f)
        try setShowNote(f, a, "OLD")
        try f.setWriteReply(a, note: "N1")
        let m = model(f)
        m.selection = a
        try await eventually("detail read started") { f.exists("show.waiting") }
        m.editNote(a, "N1")
        _ = await m.commitNote(a)
        try await eventually("stale detail landed") { m.detail?.note == "OLD" }
        try setShowNote(f, a, "EXT")
        m.selection = b
        m.selection = a
        try await eventually("fresh detail landed") { m.detail?.note == "EXT" }
        m.editNote(a, "EXT")
        XCTAssertNil(m.noteDrafts[a], "learning stayed suppressed after the stale read")
    }

    /// Two saves back to back with a stale read (it says N1) in flight across the second.
    func testTwoSavesBackToBackWithAStaleReadBetween() async throws {
        let f = try JobsFixture(self)
        try f.setWriteReply(a, note: "N1")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "N1")
        _ = await m.commitNote(a)
        try slowShow(f)
        try setShowNote(f, a, "N1")
        m.selection = a
        try await eventually("detail read started") { f.exists("show.waiting") }
        try f.setWriteReply(a, note: "N2")
        m.editNote(a, "N2")
        _ = await m.commitNote(a)
        try await eventually("stale detail landed") { m.detail?.note == "N1" }
        m.editNote(a, "N2")
        XCTAssertNil(m.noteDrafts[a])
        m.editNote(a, "N1")
        XCTAssertNotNil(m.noteDrafts[a])
    }

    /// A failed save, then a successful one: one draft, cleared once, two calls.
    func testFailedSaveThenSuccessfulSave() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        m.editNote(a, "N1")
        let first = await m.commitNote(a)
        XCTAssertFalse(first)
        XCTAssertEqual(m.noteDrafts[a], "N1")
        try f.setWriteReply(a, note: "N1")
        let second = await m.commitNote(a)
        XCTAssertTrue(second)
        XCTAssertNil(m.noteDrafts[a])
        XCTAssertEqual(noteCalls(f).count, 2, "\(noteCalls(f))")
        m.editNote(a, "N1")
        XCTAssertNil(m.noteDrafts[a])
    }

    /// flush after a failed save reports the restored draft, every time.
    func testFlushAfterAFailedSaveReportsTheRestoredDraft() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        m.editNote(a, "N1")
        _ = await m.commitNote(a)
        let open = await m.flushNoteDrafts(timeout: 2)
        XCTAssertEqual(open, [a])
        let again = await m.flushNoteDrafts(timeout: 2)
        XCTAssertEqual(again, [a])
    }

    /// vani's note is cleared by another client after this app saved one; lists then carry no note.
    /// The protection must end: typing the old note back is a real change.
    func testNoteClearedElsewhereAfterASaveIsEventuallyLearned() async throws {
        let f = try JobsFixture(self)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: "OLD"])
        try f.setWriteReply(a, note: "N1")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "N1")
        _ = await m.commitNote(a)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4])   // cleared elsewhere
        await m.refresh()
        m.editNote(a, "N1")
        XCTAssertNotNil(m.noteDrafts[a], "a note cleared elsewhere was never learned: typing it back was dropped as no change")
    }

    /// The protection ends once a read carries a note: a later clear is learned.
    func testClearAfterAReadWithANoteIsLearned() async throws {
        let f = try JobsFixture(self)
        try f.setWriteReply(a, note: "N1")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "N1")
        _ = await m.commitNote(a)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: "EXT"])
        await m.refresh()
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4])
        await m.refresh()
        m.editNote(a, "EXT")
        XCTAssertNotNil(m.noteDrafts[a], "a clear after a read with a note was not learned")
    }

    /// Same, but the clear is seen through the detail read (jobs show) only.
    func testNoteClearedElsewhereSeenByDetailReadIsLearned() async throws {
        let f = try JobsFixture(self)
        try f.setWriteReply(a, note: "N1")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "N1")
        _ = await m.commitNote(a)
        m.selection = a
        try await eventually("detail landed") { m.detail?.key == self.a }
        try await Task.sleep(nanoseconds: 300_000_000)
        m.selection = b
        m.selection = a   // fresh read: show has no note (cleared elsewhere)
        try await eventually("detail landed again") { m.detail?.key == self.a }
        try await Task.sleep(nanoseconds: 300_000_000)
        m.editNote(a, "N1")
        XCTAssertNotNil(m.noteDrafts[a], "a note cleared elsewhere and read by the detail was never learned")
    }
}
