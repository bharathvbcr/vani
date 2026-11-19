import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// Round-2 attacks (ft-189e): queued note saves, flushNoteDrafts, the blank-draft rule. Model level, fake vani.
@MainActor
final class JobsNoteAttack2BreaksTests: XCTestCase {
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

    /// The owner types A1, a save starts, then they type the text the note had before: latest wins.
    func testRevertingToTheStoredNoteWhileASaveIsInFlightIsNotLost() async throws {
        let f = try JobsFixture(self)
        try slowNotes(f)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: "orig"])
        let m = model(f)
        try f.setWriteReply(a, note: "A1")
        await m.refresh()
        m.editNote(a, "A1")
        let first = Task { await m.commitNote(self.a) }
        try await eventually("save in flight") { f.exists("note.waiting") }
        m.editNote(a, "orig") // the latest thing typed equals the note vani still has
        _ = await first.value
        try await Task.sleep(nanoseconds: 500_000_000)
        let stored = m.jobs.first { $0.key == self.a }?.note
        XCTAssertEqual(stored, "orig", "the owner's last edit was 'orig' but vani now holds \(String(describing: stored)); calls \(noteCalls(f))")
    }

    /// The owner cleared a known note; a refresh then drops the job from the list. The clear is still owed.
    func testAClearedNoteForAJobRefreshedOutOfTheListIsStillSent() async throws {
        let f = try JobsFixture(self)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: "keep me"])
        try f.setWriteReply(a, note: "")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "")
        XCTAssertNotNil(m.noteDrafts[a])
        try f.setListReply(JobsFixture.standardOrder.filter { $0 != a }, counts: ["new": 3, "all": 4])
        await m.refresh()
        _ = await m.commitNote(a)
        XCTAssertEqual(noteCalls(f).count, 1, "the explicit clear was silently dropped: \(f.calls()) draft=\(String(describing: m.noteDrafts[a]))")
    }
}
