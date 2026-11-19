import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// Round 5: a save's own reply does not end the lag protection, with the page and the detail both loaded.
@MainActor
final class JobsNoteAttack5Tests: XCTestCase {
    private func model(_ f: JobsFixture) -> JobsModel {
        let vani = f.dir.appendingPathComponent("vani")
        let m = JobsModel { JobsClient(vaniBinary: vani, timeout: 10) }
        m.reconcileDelay = 30
        return m
    }

    private let a = JobsFixture.roles[0].key

    func testLaggingListAfterASaveWithTheDetailOpenKeepsTheConfirmedNote() async throws {
        let f = try JobsFixture(self)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: "OLD"])
        try f.setWriteReply(a, note: "N1")
        let m = model(f)
        await m.refresh()
        m.selection = a
        let deadline = Date().addingTimeInterval(10)
        while m.detail?.key != a, Date() < deadline { try await Task.sleep(nanoseconds: 10_000_000) }
        XCTAssertEqual(m.detail?.key, a)
        m.editNote(a, "N1")
        let ok = await m.commitNote(a)
        XCTAssertTrue(ok)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: ""])
        await m.refresh()
        m.editNote(a, "")
        XCTAssertNotNil(m.noteDrafts[a], "the lagging list erased the confirmed note")
    }

    /// A write of another job re-patches the page; it must not re-learn the saved job's row (which holds
    /// the note from the save's own reply) and so end the protection.
    func testAnotherJobsMarkDoesNotEndTheProtectionOfASavedNote() async throws {
        let f = try JobsFixture(self)
        let b = JobsFixture.roles[1].key
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: "OLD"])
        try f.setWriteReply(a, note: "N1")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "N1")
        _ = await m.commitNote(a)
        await m.setMark(b, .applied)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: ""])
        await m.refresh()
        m.editNote(a, "")
        XCTAssertNotNil(m.noteDrafts[a], "another job's write ended the protection and the lagging list erased the note")
    }

    /// A blank read after a save is neither believed nor ignored: a draft of the old note is kept too.
    func testBlankReadAfterASaveKeepsEitherDraftOpen() async throws {
        let f = try JobsFixture(self)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: "OLD"])
        try f.setWriteReply(a, note: "N1")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "N1")
        _ = await m.commitNote(a)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4])
        await m.refresh()
        m.editNote(a, "N1")
        XCTAssertNotNil(m.noteDrafts[a])
        m.editNote(a, "")
        XCTAssertNotNil(m.noteDrafts[a])
    }

    /// Bookkeeping of a job that left both the page and the detail is forgotten: afterwards a blank draft
    /// for it is a clear only if the note was read again (never read: no change).
    func testBookkeepingIsForgottenOnceTheJobLeavesThePageAndDetail() async throws {
        let f = try JobsFixture(self)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: "OLD"])
        try f.setWriteReply(a, note: "N1")
        let m = model(f)
        await m.refresh()
        m.editNote(a, "N1")
        _ = await m.commitNote(a)
        XCTAssertNil(m.noteDrafts[a])
        // the job leaves the page
        try f.setListReply(JobsFixture.standardOrder.filter { $0 != a }, counts: ["new": 3, "all": 3])
        await m.refresh()
        XCTAssertFalse(m.jobs.contains { $0.key == a })
        // back, lagging blank: the protection was pruned, so the blank read is believed (a typed N1 is a change)
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [a: ""])
        await m.refresh()
        m.editNote(a, "N1")
        XCTAssertNotNil(m.noteDrafts[a])
        m.editNote(a, "")
        XCTAssertNil(m.noteDrafts[a], "the forgotten protection should leave a blank read believed (no change from blank)")
    }
}
