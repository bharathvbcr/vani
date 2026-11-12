import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// How the Jobs tab keeps its page vani's own without waiting on vani: a write patches the page from
/// vani's reply at once and re-lists quietly once the writes stop, a superseded list is stopped, and
/// coming to the front re-lists. Driven against the fake `vani jobs` (JobsFixture), whose calls.log
/// says which commands ran, and whose list reply stands in for vani's after a write.
@MainActor
final class JobsListCostTests: XCTestCase {
    /// Short, so tests run fast; long enough that a test can act between a write and its re-list.
    static let delay: TimeInterval = 0.3

    private func model(_ f: JobsFixture) -> JobsModel {
        let vani = f.dir.appendingPathComponent("vani")
        let m = JobsModel { JobsClient(vaniBinary: vani, timeout: 10) }
        m.reconcileDelay = Self.delay
        return m
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

    /// Long enough for a reconcile that should not happen to have started and landed.
    private func quietPeriod() async throws {
        try await Task.sleep(nanoseconds: UInt64((Self.delay + 0.7) * 1_000_000_000))
    }

    // MARK: A write patches at once, then the page is vani's again

    /// A note shows on its row at once, with no list; once the writes stop, one quiet list makes the
    /// page vani's (here vani also moved the row, which no patch could know).
    func testANoteIsPatchedAtOnceThenReconciledOnce() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        let before = try XCTUnwrap(m.page)
        let n = lists(f)
        let key = JobsFixture.roles[1].key
        try f.setWriteReply(key, note: "call Tuesday")
        let vaniOrder = [key] + JobsFixture.standardOrder.filter { $0 != key }
        try f.setListReply(vaniOrder, counts: ["new": 4, "all": 4], notes: [key: "call Tuesday"])

        let saved = await m.saveNote(key, "call Tuesday")
        XCTAssertTrue(saved)
        XCTAssertNil(m.actionError)
        XCTAssertEqual(lists(f), n, "the patch needs no list: \(f.calls())")
        XCTAssertTrue(m.reconciling)
        XCTAssertFalse(m.loading, "the reconcile shows no progress")
        var want = before
        want.jobs[1].note = "call Tuesday"
        XCTAssertEqual(m.page, want, "only the written field changed")

        try await eventually("the reconcile") { !m.reconciling }
        XCTAssertEqual(lists(f), n + 1)
        XCTAssertFalse(m.loading)
        XCTAssertEqual(m.jobs.map(\.key), vaniOrder, "the page is vani's list, not the patch")
        XCTAssertEqual(m.jobs.first?.note, "call Tuesday")
        try await quietPeriod()
        XCTAssertEqual(lists(f), n + 1, "one list per write: \(f.calls())")
    }

    /// Hiding a row in New takes it out at once (counts moved as a re-list counts them, selection on
    /// the row that took its place); the reconcile then shows vani's order, which the company factor
    /// may have changed, and keeps the selection.
    func testAMarkThatLeavesTheViewRemovesTheRowThenTakesVanisPage() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        let n = lists(f)
        let key = JobsFixture.roles[1].key
        m.selection = key
        try f.setWriteReply(key, mark: "hidden")
        let rest = JobsFixture.standardOrder.filter { $0 != key }
        let vaniOrder = Array(rest.reversed())
        try f.setListReply(vaniOrder, counts: ["new": 3, "hidden": 1, "all": 4])

        await m.setMark(key, .hidden)
        XCTAssertNil(m.actionError)
        XCTAssertEqual(lists(f), n, "the patch needs no list: \(f.calls())")
        XCTAssertEqual(m.jobs.map(\.key), rest)
        let page = try XCTUnwrap(m.page)
        XCTAssertEqual(page.total, 3)
        XCTAssertEqual(page.count(.new), 3)
        XCTAssertEqual(page.count(.hidden), 1)
        XCTAssertEqual(page.count(.all), 4, "a hidden posting is still in All")
        XCTAssertEqual(m.selection, JobsFixture.roles[2].key, "the next row takes the selection")

        try await eventually("the reconcile") { !m.reconciling }
        XCTAssertEqual(lists(f), n + 1)
        XCTAssertEqual(m.jobs.map(\.key), vaniOrder)
        XCTAssertEqual(m.selection, JobsFixture.roles[2].key, "the reconcile keeps the selection")
    }

    /// Writes in a burst each patch at once and are reconciled by one list after the last.
    func testABurstOfWritesIsReconciledByOneList() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        let n = lists(f)
        for (i, r) in JobsFixture.roles.prefix(3).enumerated() {
            try f.setWriteReply(r.key, note: "n\(i)")
            let saved = await m.saveNote(r.key, "n\(i)")
            XCTAssertTrue(saved)
            XCTAssertEqual(m.jobs[i].note, "n\(i)")
        }
        XCTAssertEqual(lists(f), n)
        try await eventually("the reconcile") { !m.reconciling }
        try await quietPeriod()
        XCTAssertEqual(lists(f), n + 1, "\(f.calls())")
    }

    /// A write while the reconcile list runs stops that list (it may have read the marks before the
    /// write) and reconciles again after it; the stopped list is no error and never lands.
    func testAWriteDuringAReconcileStopsItAndReconcilesAfter() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        let n = lists(f)
        let first = JobsFixture.roles[0].key, second = JobsFixture.roles[2].key
        try f.setWriteReply(first, note: "a")
        try f.setWriteReply(second, note: "b")
        try f.slowNextList()
        _ = await m.saveNote(first, "a")
        try await eventually("the reconcile list to start") { f.exists("slow.started") }
        XCTAssertTrue(m.reconciling)

        try f.setListReply(JobsFixture.standardOrder, counts: ["new": 4, "all": 4], notes: [first: "a", second: "b"])
        _ = await m.saveNote(second, "b")
        try await eventually("the running reconcile to get SIGTERM") { f.exists("term.log") }
        XCTAssertEqual(m.jobs[2].note, "b", "the second write is patched at once")
        try await eventually("the second reconcile") { !m.reconciling }
        XCTAssertEqual(lists(f), n + 2, "\(f.calls())")
        XCTAssertNil(m.error, "a list this app stopped is not a failure")
        XCTAssertEqual(m.jobs.map(\.note), ["a", nil, "b", nil])
    }

    /// A reconcile list that read the marks before a second write must never land over that write's
    /// patch: here it would finish (with the list from before) well before the next reconcile.
    func testAReconcileThatPredatesAWriteNeverLandsOverIt() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        let first = JobsFixture.roles[0].key, second = JobsFixture.roles[2].key
        try f.setWriteReply(first, note: "a")
        try f.setWriteReply(second, note: "b")
        try f.slowNextList(seconds: 0.5)
        _ = await m.saveNote(first, "a")
        try await eventually("the reconcile list to start") { f.exists("slow.started") }
        m.reconcileDelay = 3 // the second write's reconcile starts long after the first list ends
        _ = await m.saveNote(second, "b")
        XCTAssertEqual(m.jobs[2].note, "b")
        try await Task.sleep(nanoseconds: 1_200_000_000)
        XCTAssertEqual(m.jobs[2].note, "b", "the list from before the write landed over its patch")
        XCTAssertNil(m.error)
        XCTAssertTrue(m.reconciling, "the second write's reconcile is still to come")
    }

    /// A list the owner starts after a write (a sort, a search) reads that write: the reconcile still
    /// waiting is dropped, not run as a second list.
    func testAListStartedAfterAWriteReplacesTheReconcile() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        let n = lists(f)
        let key = JobsFixture.roles[0].key
        try f.setWriteReply(key, note: "x")
        _ = await m.saveNote(key, "x")
        XCTAssertTrue(m.reconciling)
        await m.refresh()
        XCTAssertFalse(m.reconciling)
        try await quietPeriod()
        XCTAssertEqual(lists(f), n + 1, "\(f.calls())")
    }

    /// A reconcile vani cannot answer shows the error, as any failed list does, and leaves the
    /// patched page rather than an empty one.
    func testAFailedReconcileShowsTheErrorAndKeepsThePatch() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        let key = JobsFixture.roles[1].key
        try f.setWriteReply(key, note: "kept")
        try f.breakListReply()
        _ = await m.saveNote(key, "kept")
        try await eventually("the reconcile") { !m.reconciling }
        XCTAssertNotNil(m.error, "a reconcile that failed must not look like one that ran")
        XCTAssertEqual(m.jobs.map(\.key), JobsFixture.standardOrder)
        XCTAssertEqual(m.jobs[1].note, "kept")
    }

    /// vani refuses the write (the fake has no reply for this key): the error is shown, and the page,
    /// counts and selection are exactly as before, with no list and no reconcile.
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
        XCTAssertFalse(m.reconciling)
        try await quietPeriod()
        XCTAssertEqual(lists(f), n)
    }

    /// A write whose row the page does not hold cannot be placed, so the model re-lists at once.
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
        XCTAssertFalse(m.reconciling, "the re-list read the write")
    }

    /// While Insights is open its charts are over the selection, which only vani can recount: a write
    /// re-lists (with stats and gaps) at once rather than patch.
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
        XCTAssertFalse(m.reconciling)
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

    /// A write made while a visible list runs re-lists at once: that list may have read the marks
    /// before the write and would land over the patch. The running list is stopped, not waited for.
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

    // MARK: Activation re-lists

    /// Coming to the front always re-lists: the ranking reads stores no stamp can cover (rkb's
    /// profile reads files Vani cannot name), so "nothing changed" cannot be known.
    func testActivationReListsEachTime() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        let n = lists(f)
        for i in 1...2 {
            m.reloadAfterActivation()
            try await eventually("activation list \(i)") { lists(f) == n + i && !m.loading }
        }
        XCTAssertNil(m.error)
    }

    /// An activation after a failed list re-lists and recovers.
    func testActivationAfterAFailedListReLists() async throws {
        let f = try JobsFixture(self)
        var binary = f.dir.appendingPathComponent("missing-vani")
        let m = JobsModel { JobsClient(vaniBinary: binary, timeout: 10) }
        await m.refresh()
        XCTAssertNotNil(m.error)

        binary = f.dir.appendingPathComponent("vani")
        m.reloadAfterActivation()
        try await eventually("a list") { m.error == nil && m.jobs.map(\.key) == JobsFixture.standardOrder }
    }
}
