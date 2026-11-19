import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// Final attacks (ft-189e round 4): save generation, learnSuppressed, notesConfirmedBySave. Model level, fake vani.
@MainActor
final class JobsNoteAttack4BreaksTests: XCTestCase {
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





    /// The save's own patched reply carries the note, so it ends the lag protection at once: a list from the
    /// lagging reader that carries no note then erases the confirmed note, and clearing the note (blank) is
    /// dropped as "no change" although vani holds N1.
    func testLaggingListAfterASaveWithALoadedPageDoesNotEraseTheConfirmedNote() async throws {
        let f = try JobsFixture(self)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: "OLD"])
        try f.setWriteReply(a, note: "N1")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "N1")
        let ok = await m.commitNote(a)
        XCTAssertTrue(ok)
        // a lagging list: still no note on the job (the reader has not seen the write)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: ""])
        await m.refresh()
        m.editNote(a, "")
        XCTAssertNotNil(m.noteDrafts[a], "clearing N1 was swallowed as no change: the lagging list erased the confirmed note")
    }
}
