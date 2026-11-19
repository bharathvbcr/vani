import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// Final attacks (ft-189e round 3): confirmedNotes / inflightNotes bookkeeping. Model level, fake vani.
@MainActor
final class JobsNoteAttack3BreaksTests: XCTestCase {
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

    /// A detail read that started before a confirmed save and lands after it must not make the model
    /// believe the older note is what vani holds.
    func testStaleDetailReadLandingAfterASaveDoesNotRewriteTheConfirmedNote() async throws {
        let f = try JobsFixture(self)
        try slowShow(f)
        try setShowNote(f, a, "OLD")
        try f.setWriteReply(a, note: "NEW")
        let m = model(f)
        m.selection = a                       // starts the slow detail read (snapshot says OLD)
        try await eventually("detail read started") { f.exists("show.waiting") }
        m.editNote(a, "NEW")
        let saved = await m.commitNote(a)
        XCTAssertTrue(saved)
        try await eventually("stale detail landed") { m.detail?.note == "OLD" }
        // vani holds NEW. Typing OLD back is a real change and must stay a draft to save.
        m.editNote(a, "OLD")
        XCTAssertNotNil(m.noteDrafts[a], "undo to the pre-save note was swallowed as no change: stale detail overwrote confirmedNotes")
    }

    /// Same, seen from the other side: after the stale read lands, the note vani holds (NEW) typed
    /// again is a no-op, not a phantom draft.
    func testStaleDetailThenTypingTheSavedNoteLeavesNoPhantomDraft() async throws {
        let f = try JobsFixture(self)
        try slowShow(f)
        try setShowNote(f, a, "OLD")
        try f.setWriteReply(a, note: "NEW")
        let m = model(f)
        m.selection = a
        try await eventually("detail read started") { f.exists("show.waiting") }
        m.editNote(a, "NEW")
        _ = await m.commitNote(a)
        try await eventually("stale detail landed") { m.detail?.note == "OLD" }
        m.editNote(a, "NEW")
        XCTAssertNil(m.noteDrafts[a], "a draft equal to the note vani holds stayed open")
    }

    /// A failing save, with the same text typed again during the flight: the draft must not vanish.
    func testRetypingTheInFlightTextThenAFailedSaveKeepsTheDraft() async throws {
        let f = try JobsFixture(self)
        try slowNotes(f)
        try f.setWriteReply(a, note: "A1")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "A1")
        let first = Task { await m.commitNote(self.a) }
        try await eventually("save in flight") { f.exists("note.waiting") }
        m.editNote(a, "A1 x")
        m.editNote(a, "A1")                  // deleted the extra char: same as in flight
        try FileManager.default.removeItem(at: f.dir.appendingPathComponent("write-\(a).json"))
        let saved = await first.value
        XCTAssertFalse(saved)
        XCTAssertNotNil(m.actionError)
        XCTAssertNotNil(m.noteDrafts[a], "the save failed and the note text is gone: no draft, flush would report nothing")
        let open = await m.flushNoteDrafts(timeout: 2)
        XCTAssertEqual(open, [a])
    }
}
