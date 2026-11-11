import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// What keeps the Jobs tab from re-reading the ledger more than it must (a list takes 15–23 s on the
/// owner's 62,734 postings): a write patches the page from vani's reply, a superseded list is stopped,
/// and coming to the front re-lists only when a file a list reads changed. Driven against the fake
/// `vani jobs` (JobsFixture), whose calls.log says which commands ran.
@MainActor
final class JobsListCostTests: XCTestCase {
    private func model(_ f: JobsFixture, state: Bool = false) -> JobsModel {
        let vani = f.dir.appendingPathComponent("vani")
        if state {
            let stateDir = f.stateDir.path, config = f.configPath.path
            return JobsModel { JobsClient(vaniBinary: vani, configPath: config, stateDir: stateDir, timeout: 10) }
        }
        return JobsModel { JobsClient(vaniBinary: vani, timeout: 10) }
    }

    private func lists(_ f: JobsFixture) -> Int { f.calls().filter { $0.hasPrefix("jobs list") }.count }

    /// Waits, at most five seconds, for work a setter started in a Task.
    private func eventually(_ what: String, _ done: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(5)
        while !done() {
            if Date() > deadline { return XCTFail("timed out waiting for \(what)") }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    // MARK: Writes patch the page

    /// A note in New with no search keeps the row where it is: the row shows the note, every other
    /// field and row is as listed, and vani is not asked for the list again.
    func testANoteIsPatchedInPlaceWithoutAList() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        let before = try XCTUnwrap(m.page)
        let n = lists(f)
        let key = JobsFixture.roles[1].key
        try f.setWriteReply(key, note: "call Tuesday")

        let saved = await m.saveNote(key, "call Tuesday")
        XCTAssertTrue(saved)
        XCTAssertNil(m.actionError)
        XCTAssertEqual(lists(f), n, "a note does not re-list: \(f.calls())")
        XCTAssertTrue(f.calls().contains("jobs note --json \(key) call Tuesday"), "\(f.calls())")
        XCTAssertEqual(m.jobs.map(\.key), JobsFixture.standardOrder)
        XCTAssertEqual(m.jobs[1].note, "call Tuesday")
        var want = before
        want.jobs[1].note = "call Tuesday"
        XCTAssertEqual(m.page, want, "only the written field changed")
    }

    /// Hiding a row in New takes it out of the view: the row goes, the counts move as a re-list would
    /// count them, selection moves to the row that took its place, and vani is not asked to re-list.
    func testAMarkThatLeavesTheViewRemovesTheRowAndMovesTheCounts() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        let n = lists(f)
        let key = JobsFixture.roles[1].key
        m.selection = key
        try f.setWriteReply(key, mark: "hidden")

        await m.setMark(key, .hidden)
        XCTAssertNil(m.actionError)
        XCTAssertEqual(lists(f), n, "a mark does not re-list: \(f.calls())")
        XCTAssertEqual(m.jobs.map(\.key), JobsFixture.standardOrder.filter { $0 != key })
        let page = try XCTUnwrap(m.page)
        XCTAssertEqual(page.total, 3)
        XCTAssertEqual(page.shown, 3)
        XCTAssertEqual(page.count(.new), 3)
        XCTAssertEqual(page.count(.hidden), 1)
        XCTAssertEqual(page.count(.all), 4, "a hidden posting is still in All")
        XCTAssertEqual(m.selection, JobsFixture.roles[2].key, "the next row takes the selection")
    }

    /// vani refuses the write (the fake has no reply for this key): the error is shown, and the page,
    /// counts and selection are exactly as before, with no list.
    func testAFailedWriteLeavesTheListUnchanged() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        let before = try XCTUnwrap(m.page)
        let n = lists(f)
        let key = JobsFixture.roles[0].key
        m.selection = key

        await m.setMark(key, .saved)
        XCTAssertEqual(m.actionError, "fake vani: no reply for jobs mark")
        XCTAssertEqual(m.page, before)
        XCTAssertEqual(m.selection, key)
        m.actionError = nil
        let saved = await m.saveNote(key, "x")
        XCTAssertFalse(saved)
        XCTAssertEqual(m.actionError, "fake vani: no reply for jobs note")
        XCTAssertEqual(m.page, before)
        XCTAssertEqual(lists(f), n)
    }

    /// A write whose row the page does not hold cannot be placed, so the model re-lists.
    func testAWriteToARowTheListDoesNotHoldReLists() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        m.filter.fits = ["strong_fit"]
        try await eventually("strong fits only") { m.jobs.map(\.key) == JobsFixture.strongOrder }
        let n = lists(f)
        let key = JobsFixture.roles[2].key // adjacent: not in this list
        XCTAssertFalse(m.jobs.contains { $0.key == key })
        try f.setWriteReply(key, mark: "saved")

        await m.setMark(key, .saved)
        XCTAssertNil(m.actionError)
        XCTAssertEqual(lists(f), n + 1, "\(f.calls())")
    }

    /// While Insights is open its charts are over the selection, which only vani can recount: a write
    /// re-lists (with stats and gaps) rather than patch.
    func testAWriteWithInsightsOpenReLists() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        m.showInsights = true
        await m.refresh()
        try await eventually("settled") { !m.loading && m.stats != nil }
        let n = lists(f)
        let key = JobsFixture.roles[0].key
        try f.setWriteReply(key, mark: "saved")
        await m.setMark(key, .saved)
        XCTAssertEqual(lists(f), n + 1, "\(f.calls())")
    }

    // MARK: A superseded list is stopped

    /// A newer list supersedes a slow one: the slow one's `vani` gets SIGTERM at once instead of
    /// running to its timeout, its cancellation is not shown as an error, and the newer page lands.
    func testASupersededListsProcessIsTerminated() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh() // labels read; the next refresh goes straight to the list
        try f.slowNextList()
        m.reload()
        try await eventually("the slow list to start") { f.exists("slow.started") }
        XCTAssertTrue(m.loading)
        XCTAssertFalse(f.exists("term.log"))

        let started = Date()
        await m.refresh()
        XCTAssertNil(m.error, "a list this app stopped is not a failure")
        XCTAssertFalse(m.loading)
        XCTAssertEqual(m.jobs.map(\.key), JobsFixture.standardOrder)
        try await eventually("the superseded vani to get SIGTERM") { f.exists("term.log") }
        XCTAssertLessThan(Date().timeIntervalSince(started), 5, "stopped, not left to its 10 s timeout")
        // The cancelled list's result never lands over the newer page.
        try await Task.sleep(nanoseconds: 300_000_000)
        XCTAssertNil(m.error)
        XCTAssertEqual(m.jobs.map(\.key), JobsFixture.standardOrder)
    }

    /// A write made while a list runs re-lists: that list may have read the marks before the write and
    /// would land over the patch. The running list is stopped, not waited for.
    func testAWriteDuringARunningListReListsAndStopsIt() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        let key = JobsFixture.roles[0].key
        try f.setWriteReply(key, mark: "saved")
        try f.slowNextList()
        m.reload()
        try await eventually("the slow list to start") { f.exists("slow.started") }
        let n = lists(f)

        await m.setMark(key, .saved)
        XCTAssertEqual(lists(f), n + 1, "\(f.calls())")
        XCTAssertFalse(m.loading)
        try await eventually("the running vani to get SIGTERM") { f.exists("term.log") }
    }

    // MARK: Activation re-lists only on a change

    /// Coming to the front with nothing changed asks vani for no list; a change to the ledger, the
    /// marks or the stages does.
    func testActivationWithNoFileChangeMakesNoListCall() async throws {
        let f = try JobsFixture(self)
        try f.makeState()
        let m = model(f, state: true)
        await m.refresh()
        let n = lists(f)

        XCTAssertFalse(m.listInputsChanged())
        m.reloadAfterActivation()
        try await Task.sleep(nanoseconds: 300_000_000)
        XCTAssertEqual(lists(f), n, "nothing changed: \(f.calls())")

        try f.append("marks/marks.jsonl")
        XCTAssertTrue(m.listInputsChanged())
        m.reloadAfterActivation()
        try await eventually("a list after the marks changed") { lists(f) == n + 1 && !m.loading }
        XCTAssertFalse(m.listInputsChanged(), "the new list read the files as they are now")

        try f.append("ledger/events.jsonl")
        m.reloadAfterActivation()
        try await eventually("a list after the ledger changed") { lists(f) == n + 2 && !m.loading }

        // A stages file appearing (the first stage written) is a change too.
        try Data("{}\n".utf8).write(to: f.stateDir.appendingPathComponent("marks/stages.jsonl"))
        m.reloadAfterActivation()
        try await eventually("a list after the stages appeared") { lists(f) == n + 3 && !m.loading }
    }

    /// No list to compare with (none ran, or the last one failed) re-lists: doubt is a change.
    func testActivationAfterAFailedListReLists() async throws {
        let f = try JobsFixture(self)
        try f.makeState()
        var binary = f.dir.appendingPathComponent("missing-vani")
        let stateDir = f.stateDir.path
        let m = JobsModel { JobsClient(vaniBinary: binary, stateDir: stateDir, timeout: 10) }
        XCTAssertTrue(m.listInputsChanged(), "no list has run")
        await m.refresh()
        XCTAssertNotNil(m.error)
        XCTAssertTrue(m.listInputsChanged(), "a failed list proves nothing about the files")

        binary = f.dir.appendingPathComponent("vani")
        m.reloadAfterActivation()
        try await eventually("a list") { m.error == nil && m.jobs.map(\.key) == JobsFixture.standardOrder }
        XCTAssertFalse(m.listInputsChanged())
    }
}
