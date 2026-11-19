import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// ft-189e round 3: drafts are compared with the note vani holds as far as the app knows (read or
/// confirmed, plus the save in flight), not with what the list shows. Model level, fake vani.
@MainActor
final class JobsNoteHeldTests: XCTestCase {
    private func model(_ f: JobsFixture) -> JobsModel {
        let vani = f.dir.appendingPathComponent("vani")
        let m = JobsModel { JobsClient(vaniBinary: vani, timeout: 10) }
        m.reconcileDelay = 30
        return m
    }

    private func noteCalls(_ f: JobsFixture) -> [String] { f.calls().filter { $0.hasPrefix("jobs note") } }
    private let a = JobsFixture.roles[0].key

    /// A draft typed back to the note a save in flight is writing is no change.
    func testADraftEqualToTheInFlightNoteIsNotAChange() async throws {
        let f = try JobsFixture(self)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: "orig"])
        try f.setWriteReply(a, note: "A1")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "A1")
        await m.commitNote(a)
        m.editNote(a, "A1")
        XCTAssertNil(m.noteDrafts[a])
        XCTAssertEqual(noteCalls(f).count, 1)
    }

    /// An edit of a known note to blank, once the job has left the list, is a clear and is sent once.
    func testAClearAfterAConfirmedSaveForAJobOffTheListIsSent() async throws {
        let f = try JobsFixture(self)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: "orig"])
        let m = model(f)
        await m.refresh()
        try f.setWriteReply(a, note: "A1")
        m.editNote(a, "A1")
        await m.commitNote(a)
        try f.setListReply(JobsFixture.standardOrder.filter { $0 != a }, counts: ["new": 3, "all": 4])
        await m.refresh()
        try f.setWriteReply(a, note: "")
        m.editNote(a, "   ")
        XCTAssertNotNil(m.noteDrafts[a])
        await m.commitNote(a)
        XCTAssertEqual(noteCalls(f).count, 2, "\(f.calls())")
    }

    /// A blank draft for a job whose note was never read stays dropped.
    func testABlankDraftForAnUnreadJobIsStillDropped() async throws {
        let f = try JobsFixture(self)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4])
        let m = model(f)
        m.editNote("ats:never:read", "  ")
        XCTAssertNil(m.noteDrafts["ats:never:read"])
        let sent = await m.commitNote("ats:never:read")
        XCTAssertFalse(sent)
        XCTAssertEqual(noteCalls(f).count, 0)
    }
}
