import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

@MainActor
final class JobsNoteAttackBreaksTests: XCTestCase {
    private func model(_ f: JobsFixture) -> JobsModel {
        let vani = f.dir.appendingPathComponent("vani")
        let m = JobsModel { JobsClient(vaniBinary: vani, timeout: 10) }
        m.reconcileDelay = 30
        return m
    }

    private func eventually(_ what: String, _ done: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(8)
        while !done() {
            if Date() > deadline { return XCTFail("timed out waiting for \(what)") }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    private func noteCalls(_ f: JobsFixture) -> [String] { f.calls().filter { $0.hasPrefix("jobs note") } }
    private let a = JobsFixture.roles[0].key
    private let b = JobsFixture.roles[1].key

    /// An older save is in flight when the owner reopens the job, types more, and leaves again: the
    /// newer draft must still be saved, not dropped because a save of the same job was running.
    func testANewerDraftTypedWhileASaveIsInFlightIsStillSaved() async throws {
        let f = try JobsFixture(self)
        let script = f.dir.appendingPathComponent("vani")
        var text = try String(contentsOf: script, encoding: .utf8)
        let hook = "\"jobs mark\"|\"jobs note\")"
        XCTAssertTrue(text.contains(hook))
        text = text.replacingOccurrences(of: hook, with: hook + " if [ \"$2\" = note ] && [ -f \"$d/slow-note\" ]; then rm -f \"$d/slow-note\"; : > \"$d/note.waiting\"; sleep 1.5; fi;")
        try Data(text.utf8).write(to: script)
        let m = model(f)
        try f.setWriteReply(a, note: "v1")
        await m.refresh()
        try Data().write(to: f.dir.appendingPathComponent("slow-note"))
        m.selection = a
        m.editNote(a, "v1")
        m.selection = b // save of v1 starts and sits in flight
        try await eventually("the first save") { f.exists("note.waiting") }
        m.selection = a
        m.editNote(a, "v2")
        m.selection = b // second leave while the first save is still running
        try await Task.sleep(nanoseconds: 2_500_000_000)
        try await eventually("v2 saved") { self.noteCalls(f).contains { $0.contains("v2") } }
        XCTAssertNil(m.noteDrafts[a], "the newer draft was never saved: \(noteCalls(f)) drafts=\(m.noteDrafts)")
    }
}
