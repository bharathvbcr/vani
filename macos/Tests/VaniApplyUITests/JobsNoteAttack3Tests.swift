import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// Final attacks (ft-189e round 3): confirmedNotes / inflightNotes bookkeeping. Model level, fake vani.
@MainActor
final class JobsNoteAttack3Tests: XCTestCase {
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




    /// A revert queued during a save that then FAILS: vani still holds the old note, so nothing is sent.
    func testQueuedRevertWhenTheInFlightSaveFailsSendsNothingAndKeepsNoDraft() async throws {
        let f = try JobsFixture(self)
        try slowNotes(f)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: "OLD"])
        try f.setWriteReply(a, note: "N1")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "N1")
        let first = Task { await m.commitNote(self.a) }
        try await eventually("save in flight") { f.exists("note.waiting") }
        m.editNote(a, "OLD")
        try FileManager.default.removeItem(at: f.dir.appendingPathComponent("write-\(a).json"))
        _ = await first.value
        XCTAssertEqual(noteCalls(f).count, 1, "\(noteCalls(f))")
        XCTAssertNil(m.noteDrafts[a])
    }

    /// A revert queued during a save that succeeds is sent, once, and the model ends in agreement.
    func testQueuedRevertAfterASuccessfulSaveIsSentOnce() async throws {
        let f = try JobsFixture(self)
        try slowNotes(f, "0.5")
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: "OLD"])
        try f.echoNoteWrites(a)
        let m = model(f)
        await m.refresh()
        m.editNote(a, "N1")
        let first = Task { await m.commitNote(self.a) }
        try await eventually("save in flight") { f.exists("note.waiting") }
        m.editNote(a, "OLD")
        _ = await first.value
        try await eventually("revert sent") { self.noteCalls(f).count >= 2 && m.noteDrafts[self.a] == nil }
        try await Task.sleep(nanoseconds: 800_000_000)
        let calls = noteCalls(f)
        XCTAssertEqual(calls.count, 2, "\(calls)")
        XCTAssertTrue(calls.last?.hasSuffix("OLD") ?? false, "\(calls)")
        m.editNote(a, "OLD")
        XCTAssertNil(m.noteDrafts[a])
    }

    /// A list that began before a save and lands after it must not move confirmedNotes back.
    func testStaleListLandingAfterASaveKeepsTheConfirmedNote() async throws {
        let f = try JobsFixture(self)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: "OLD"])
        try f.setWriteReply(a, note: "NEW")
        let m = model(f)
        await m.refresh()
        try f.slowNextList(seconds: 1)
        let slow = Task { await m.refresh(quiet: true) }
        try await eventually("slow list started") { f.exists("slow.started") }
        m.editNote(a, "NEW")
        _ = await m.commitNote(a)
        await slow.value
        try await Task.sleep(nanoseconds: 1_300_000_000)
        m.editNote(a, "OLD")
        XCTAssertNotNil(m.noteDrafts[a], "undo swallowed: a stale list rewrote the confirmed note")
    }

    /// Two jobs saving at once keep separate records.
    func testTwoJobsSavingAtOnceKeepSeparateRecords() async throws {
        let f = try JobsFixture(self)
        try slowNotes(f, "0.6")
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: "OA", b: "OB"])
        try f.setWriteReply(a, note: "NA")
        try f.setWriteReply(b, note: "NB")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "NA"); m.editNote(b, "NB")
        let ta = Task { await m.commitNote(self.a) }
        let tb = Task { await m.commitNote(self.b) }
        try await eventually("both in flight") { f.exists("note.waiting.\(self.a)") && f.exists("note.waiting.\(self.b)") }
        m.editNote(a, "OA"); m.editNote(b, "NB")   // a: undo (queued); b: same as in flight (no change)
        _ = await ta.value; _ = await tb.value
        try await eventually("a reverted") { self.noteCalls(f).count >= 3 && m.noteDrafts[self.a] == nil }
        try await Task.sleep(nanoseconds: 800_000_000)
        let calls = noteCalls(f)
        XCTAssertEqual(calls.count, 3, "\(calls)")
        XCTAssertEqual(calls.filter { $0.contains(self.b) }.count, 1, "\(calls)")
        XCTAssertNil(m.noteDrafts[b])
    }

    /// Committed, then the identical text typed again during the save (success): one call, no draft.
    func testIdenticalTextRetypedDuringASuccessfulSaveSendsOnce() async throws {
        let f = try JobsFixture(self)
        try slowNotes(f, "0.5")
        try f.setWriteReply(a, note: "A1")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "A1")
        let first = Task { await m.commitNote(self.a) }
        try await eventually("save in flight") { f.exists("note.waiting") }
        m.editNote(a, "A1")
        let again = await m.commitNote(a)
        XCTAssertFalse(again)
        _ = await first.value
        try await Task.sleep(nanoseconds: 700_000_000)
        XCTAssertEqual(noteCalls(f).count, 1, "\(noteCalls(f))")
        XCTAssertNil(m.noteDrafts[a])
    }

    /// 2000 bytes of two-byte characters is sent; one byte more is kept and never sent; trailing
    /// whitespace beyond the limit does not count.
    func testNoteAtTheByteLimitWithMultiByteCharacters() async throws {
        let f = try JobsFixture(self)
        try f.setWriteReply(a, note: "ok")
        let m = model(f)
        await m.refresh()
        let exact = String(repeating: "\u{00E9}", count: 1000)
        XCTAssertEqual(exact.utf8.count, JobsClient.maxNoteBytes)
        m.editNote(a, exact + "   \n")
        let ok = await m.commitNote(a)
        XCTAssertTrue(ok, "\(f.calls())")
        let over = exact + "a"
        m.editNote(a, over)
        let no = await m.commitNote(a)
        XCTAssertFalse(no)
        XCTAssertEqual(m.noteDrafts[a], over)
        XCTAssertEqual(noteCalls(f).count, 1, "\(noteCalls(f))")
        let open = await m.flushNoteDrafts(timeout: 1)
        XCTAssertEqual(open, [a])
        XCTAssertEqual(noteCalls(f).count, 1)
    }

    /// Selection moving during a flush neither drops nor double-sends a draft.
    func testSelectionChangesDuringAFlushLoseNothing() async throws {
        let f = try JobsFixture(self)
        try slowNotes(f, "0.4")
        try f.setWriteReply(a, note: "NA")
        try f.setWriteReply(b, note: "NB")
        let m = model(f)
        await m.refresh()
        m.selection = a
        m.editNote(a, "NA"); m.editNote(b, "NB")
        async let r = m.flushNoteDrafts(timeout: 8)
        try await eventually("a save in flight") { f.exists("note.waiting") }
        m.selection = b
        m.selection = nil
        let open = await r
        XCTAssertEqual(open, [])
        XCTAssertEqual(noteCalls(f).count, 2, "\(noteCalls(f))")
    }
}
