import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// Attacks on the note drafts, the list freshness and the unknown-mark paths (ft-189e). Model level, fake vani.
@MainActor
final class JobsNoteAttackTests: XCTestCase {
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

    func testNoteAtExactlyTheByteLimitWithMultibyteTextSavesAndOneOverDoesNot() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        try f.setWriteReply(a, note: "ok")
        await m.refresh()
        m.selection = a
        // 4-byte scalars: 500 of them are exactly 2000 bytes.
        let exact = String(repeating: "\u{1F600}", count: JobsClient.maxNoteBytes / 4)
        XCTAssertEqual(exact.utf8.count, JobsClient.maxNoteBytes)
        // Trailing whitespace is trimmed before the limit is checked, as saveNote trims.
        m.editNote(a, exact + "   \n  ")
        let saved = await m.commitNote(a)
        XCTAssertTrue(saved, "\(f.calls())")
        let over = exact + "x"
        m.editNote(a, over)
        let savedOver = await m.commitNote(a)
        XCTAssertFalse(savedOver)
        XCTAssertEqual(noteCalls(f).count, 1, "\(f.calls())")
        XCTAssertEqual(m.noteDrafts[a], over, "the oversized draft stays for the pane")
    }

    func testADraftSurvivesItsJobBeingRefreshedOutOfTheList() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        try f.setWriteReply(a, note: "still here")
        await m.refresh()
        m.selection = a
        m.editNote(a, "still here")
        try f.setListReply(JobsFixture.standardOrder.filter { $0 != a }, counts: ["new": 3, "all": 4])
        await m.refresh()
        XCTAssertEqual(m.noteDrafts[a], "still here", "a refresh never drops a draft")
        _ = await m.commitNote(a)
        XCTAssertEqual(noteCalls(f).count, 1, "\(f.calls())")
        XCTAssertTrue((noteCalls(f).first ?? "").contains("still here"))
    }

    func testABurstOfRefreshesEndsOnTheLastListAndNotLoading() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        try f.setListReply(JobsFixture.standardOrder.filter { $0 != a }, counts: ["new": 3, "all": 4])
        let tasks = (0..<6).map { _ in Task { await m.refresh() } }
        for t in tasks { await t.value }
        XCTAssertFalse(m.loading)
        XCTAssertFalse(m.jobs.contains { $0.key == self.a })
        XCTAssertNil(m.error)
        XCTAssertNil(m.lastGoodLoad)
    }

    func testARefreshTaskCancelledMidAwaitLeavesNothingLoading() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        try f.slowNextList()
        let t = Task { await m.refresh() }
        try await eventually("the slow list") { f.exists("slow.started") }
        t.cancel()
        await m.refresh() // supersedes (SIGTERM) the slow one
        await t.value
        XCTAssertFalse(m.loading)
        XCTAssertNil(m.error)
        XCTAssertEqual(m.jobs.count, JobsFixture.roles.count)
    }

    func testAFailedRefreshMarksRowsStaleAndARecoveryClearsIt() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        XCTAssertNil(m.lastGoodLoad)
        try f.breakListReply()
        await m.refresh()
        XCTAssertNotNil(m.error)
        XCTAssertNil(m.lastGoodLoad, "nothing was ever loaded, so no rows are stale")
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4])
        await m.refresh()
        XCTAssertNil(m.error)
        XCTAssertNil(m.lastGoodLoad)
        let before = Date()
        try f.breakListReply()
        await m.refresh()
        XCTAssertNotNil(m.error)
        let at = try XCTUnwrap(m.lastGoodLoad, "kept rows carry the time they were read")
        XCTAssertLessThanOrEqual(at, Date())
        XCTAssertGreaterThanOrEqual(at.timeIntervalSince(before), -5)
        XCTAssertFalse(m.jobs.isEmpty, "the rows stay")
        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4])
        await m.refresh()
        XCTAssertNil(m.lastGoodLoad)
    }

    /// A mark this app does not know (a newer vani's) is read as nil; no write the app makes sends nil back.
    func testNoWritePathErasesAnUnknownMark() async throws {
        let f = try JobsFixture(self)
        let listURL = f.dir.appendingPathComponent("list.json")
        var page = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: listURL)) as? [String: Any])
        var rows = try XCTUnwrap(page["jobs"] as? [[String: Any]])
        rows[0]["mark"] = "starred"
        rows[0]["fresh_src"] = "guessed"
        page["jobs"] = rows
        try JSONSerialization.data(withJSONObject: page).write(to: listURL)
        let m = model(f)
        try f.setWriteReply(a, note: "n")
        await m.refresh()
        let job = try XCTUnwrap(m.jobs.first { $0.key == a })
        XCTAssertNil(job.mark)
        XCTAssertEqual(m.jobs.count, JobsFixture.roles.count)
        // A note save, and a detail load, send only what they were asked.
        m.selection = a
        m.editNote(a, "n")
        _ = await m.commitNote(a)
        _ = await m.saveNote(a, "n")
        let writes = f.calls().filter { $0.hasPrefix("jobs mark") || $0.hasPrefix("jobs note") }
        XCTAssertFalse(writes.contains { $0.contains("clear") }, "\(writes)")
        XCTAssertTrue(writes.allSatisfy { $0.hasPrefix("jobs note") }, "\(writes)")
    }
}
