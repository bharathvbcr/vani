import Foundation
import XCTest
@testable import VaniApplyKit

/// `JobsPage.patched`: the page a re-list would show after a mark or note write, worked out from the
/// reply, or nil where only a re-list can say. Membership is vani's `jobview.InView`
/// (internal/jobview/job.go), the counts its `jobview.Select`.
final class JobsPatchTests: XCTestCase {
    private func job(_ key: String, company: String = "Acme", status: String = "digested", mark: JobMark? = nil,
                     note: String? = nil, relevance: Double? = 50) -> Job {
        var j = Job(key: key, company: company, title: "Engineer \(key)", status: status)
        j.mark = mark
        j.note = note
        j.relevance = relevance
        return j
    }

    private func page(_ view: JobView, _ jobs: [Job], counts: [String: Int], total: Int? = nil,
                      query: String? = nil, sort: String? = nil) -> JobsPage {
        var p = JobsPage(view: view.rawValue, jobs: jobs, total: total ?? jobs.count, shown: jobs.count, counts: counts)
        p.query = query
        p.sort = sort
        return p
    }

    // MARK: Membership

    /// vani's InView, every view against every mark, for a kept, a live and a dead posting.
    func testViewMembershipIsVanisInView() {
        for status in ["triaged", "unsure", "scored", "explained", "digested"] {
            XCTAssertTrue(JobView.new.contains(status: status, mark: nil), status)
            XCTAssertTrue(JobView.all.contains(status: status, mark: nil), status)
            XCTAssertFalse(JobView.pending.contains(status: status, mark: nil), status)
        }
        for status in ["live", "dead", "dropped", "duplicate", "discovered", ""] {
            XCTAssertFalse(JobView.new.contains(status: status, mark: nil), status)
            XCTAssertFalse(JobView.all.contains(status: status, mark: nil), status)
            XCTAssertEqual(JobView.pending.contains(status: status, mark: nil), status == "live", status)
        }
        for mark in JobMark.allCases {
            for status in ["digested", "live", "dead"] {
                XCTAssertFalse(JobView.new.contains(status: status, mark: mark))
                XCTAssertFalse(JobView.pending.contains(status: status, mark: mark))
                XCTAssertTrue(JobView.all.contains(status: status, mark: mark), "a marked posting is in All whatever its status")
                for v in [JobView.saved, .applied, .hidden] {
                    XCTAssertEqual(v.contains(status: status, mark: mark), v.rawValue == mark.rawValue)
                }
            }
        }
    }

    // MARK: Rows and counts

    func testAMarkLeavingNewRemovesTheRowAndMovesEveryCount() throws {
        let rows = [job("k:1"), job("k:2", company: "Beta"), job("k:3", company: "Gamma")]
        let p = page(.new, rows, counts: ["new": 3, "saved": 1, "all": 4], total: 3)
        let next = try XCTUnwrap(p.patched(with: job("k:2", company: "Beta", mark: .saved), minRelevance: nil))
        XCTAssertEqual(next.jobs.map(\.key), ["k:1", "k:3"])
        XCTAssertEqual(next.total, 2)
        XCTAssertEqual(next.shown, 2)
        XCTAssertEqual(next.counts, ["new": 2, "saved": 2, "all": 4])
    }

    /// The last posting of a view leaves it: vani writes no count for an empty view, nor does the patch.
    func testACountThatReachesZeroIsDropped() throws {
        let p = page(.saved, [job("k:1", mark: .saved)], counts: ["saved": 1, "all": 1])
        let next = try XCTUnwrap(p.patched(with: job("k:1", mark: .hidden), minRelevance: nil))
        XCTAssertEqual(next.jobs, [])
        XCTAssertEqual(next.counts, ["hidden": 1, "all": 1])
    }

    /// In All a mark keeps a kept row: the row shows the new mark and only the mark views' counts move.
    func testAMarkInAllKeepsTheRowAndShowsTheMark() throws {
        let p = page(.all, [job("k:1"), job("k:2")], counts: ["new": 2, "all": 2])
        let next = try XCTUnwrap(p.patched(with: job("k:1", mark: .hidden), minRelevance: nil))
        XCTAssertEqual(next.jobs.map(\.key), ["k:1", "k:2"])
        XCTAssertEqual(next.jobs[0].mark, .hidden)
        XCTAssertEqual(next.counts, ["new": 1, "hidden": 1, "all": 2])
        XCTAssertEqual(next.total, 2)
    }

    /// A closed posting is in All only while marked: clearing its mark takes it out.
    func testClearingTheMarkOfADeadPostingTakesItOutOfAll() throws {
        let p = page(.all, [job("k:1", status: "dead", mark: .saved), job("k:2")], counts: ["new": 1, "saved": 1, "all": 2])
        let next = try XCTUnwrap(p.patched(with: job("k:1", status: "dead"), minRelevance: nil))
        XCTAssertEqual(next.jobs.map(\.key), ["k:2"])
        XCTAssertEqual(next.counts, ["new": 1, "all": 1])
    }

    /// Only the write's own fields come from the reply (it is enriched for `jobs show`, not the list):
    /// ranking and everything else stay as listed, and a cleared note stays cleared.
    func testOnlyTheWritesFieldsComeFromTheReply() throws {
        var listed = job("k:1", mark: .applied, note: "old note", relevance: 61)
        listed.rank = 70
        listed.keywords = [Job.Keyword(term: "go", cat: "lang", n: 2)]
        listed.description = nil
        var reply = job("k:1", mark: .applied, note: nil, relevance: 55)
        reply.rank = 12
        reply.keywords = (0..<30).map { Job.Keyword(term: "t\($0)", cat: "x", n: 1) }
        reply.description = "a description the list never carries"
        reply.stage = .interview
        reply.noteAt = nil
        let p = page(.applied, [listed], counts: ["applied": 1, "all": 1])
        let next = try XCTUnwrap(p.patched(with: reply, minRelevance: nil))
        let row = next.jobs[0]
        XCTAssertNil(row.note, "a cleared note is cleared, never refilled from the list")
        XCTAssertEqual(row.stage, .interview)
        XCTAssertEqual(row.rank, 70)
        XCTAssertEqual(row.relevance, 61)
        XCTAssertEqual(row.keywords, listed.keywords)
        XCTAssertNil(row.description)
        XCTAssertEqual(next.counts, p.counts)
    }

    // MARK: Search and floor

    /// The search reads the note: clearing the note it matched takes the row out; one that still
    /// matches by title stays. Case and surrounding space are ignored, as vani does.
    func testANoteThatNoLongerMatchesTheSearchLeaves() throws {
        let rows = [job("k:1", note: "Kubernetes heavy"), job("k:2", note: "kubernetes")]
        var second = rows[1]
        second.title = "Kubernetes Engineer"
        let p = page(.saved, [rows[0], second].map { var j = $0; j.mark = .saved; return j },
                     counts: ["saved": 2, "all": 2], query: "  KUBERNETES ")
        let out = try XCTUnwrap(p.patched(with: job("k:1", mark: .saved, note: "call Tue"), minRelevance: nil))
        XCTAssertEqual(out.jobs.map(\.key), ["k:2"])
        XCTAssertEqual(out.total, 1)
        XCTAssertEqual(out.counts, p.counts, "per-view counts ignore the search")
        let kept = try XCTUnwrap(p.patched(with: job("k:2", mark: .saved, note: nil), minRelevance: nil))
        XCTAssertEqual(kept.jobs.map(\.key), ["k:1", "k:2"], "still matched by its title")
    }

    /// Go and Swift lower-case differently outside ASCII, so a search there is left to vani.
    func testANonASCIISearchIsLeftToARelist() {
        let p = page(.saved, [job("k:1", mark: .saved, note: "Zürich")], counts: ["saved": 1], query: "zür")
        XCTAssertNil(p.patched(with: job("k:1", mark: .saved, note: "x"), minRelevance: nil))
        let plain = page(.saved, [job("k:1", mark: .saved, note: "Zürich")], counts: ["saved": 1])
        XCTAssertNotNil(plain.patched(with: job("k:1", mark: .saved, note: "x"), minRelevance: nil), "no search, nothing to compare")
    }

    /// The reply's relevance is the list's (company factor included): a mark that moves it under the
    /// relevance floor takes the row out.
    func testTheRelevanceFloorReadsTheRepliesRelevance() throws {
        let p = page(.all, [job("k:1", relevance: 41)], counts: ["new": 1, "all": 1])
        let out = try XCTUnwrap(p.patched(with: job("k:1", mark: .hidden, relevance: 39), minRelevance: 40))
        XCTAssertEqual(out.jobs, [])
        XCTAssertEqual(out.counts, ["hidden": 1, "all": 1])
        let kept = try XCTUnwrap(p.patched(with: job("k:1", mark: .hidden, relevance: 39), minRelevance: nil))
        XCTAssertEqual(kept.jobs.count, 1)
    }

    // MARK: What only a re-list can say

    func testAKeyThePageDoesNotHoldIsLeftToARelist() {
        let p = page(.saved, [job("k:1", mark: .saved)], counts: ["saved": 1, "new": 5, "all": 6])
        XCTAssertNil(p.patched(with: job("k:9", mark: .saved), minRelevance: nil))
        var unknown = p
        unknown.view = "someday"
        XCTAssertNil(unknown.patched(with: job("k:1", mark: .hidden), minRelevance: nil))
    }

    /// A New row that carried folded duplicates leaves: a re-list shows one of them in its place.
    func testARowWithFoldedDuplicatesLeavingADiversifiedNewIsLeftToARelist() {
        var lead = job("k:1")
        lead.similar = 2
        let p = page(.new, [lead, job("k:2", company: "Beta")], counts: ["new": 2, "all": 2], sort: "priority")
        XCTAssertNil(p.patched(with: job("k:1", mark: .hidden), minRelevance: nil))
        var fresh = p
        fresh.sort = "fresh" // vani does not fold this order
        XCTAssertNotNil(fresh.patched(with: job("k:1", mark: .hidden), minRelevance: nil))
    }

    /// In the first 50 rows of a diversified New, a company that already leads three of them may have
    /// had rows moved below: a removal there is left to vani. Below the window, or with no company at
    /// the cap, removing the row is what vani would do.
    func testAWindowRowWithACompanyAtTheCapIsLeftToARelist() throws {
        let capped = [job("a:1", company: "Acme"), job("a:2", company: " acme "), job("a:3", company: "ACME")]
            + (0..<60).map { job("o:\($0)", company: "Other \($0)") }
        let p = page(.new, capped, counts: ["new": 63, "all": 63], sort: "default")
        XCTAssertNil(p.patched(with: job("o:5", company: "Other 5", mark: .saved), minRelevance: nil))
        let below = try XCTUnwrap(p.patched(with: job("o:55", company: "Other 55", mark: .saved), minRelevance: nil))
        XCTAssertFalse(below.jobs.contains { $0.key == "o:55" })

        let uncapped = Array(capped.dropFirst())
        let q = page(.new, uncapped, counts: ["new": 62, "all": 62], sort: "relevance")
        XCTAssertNotNil(q.patched(with: job("o:5", company: "Other 5", mark: .saved), minRelevance: nil))
    }

    // MARK: Awaiting triage

    /// Marking a posting awaiting triage takes it out of Pending and its summary: the total, the
    /// matching count and its company's count, re-sorted as vani sorts them (most first, then name).
    func testMarkingAPendingPostingMovesThePendingSummary() throws {
        let rows = [job("p:1", company: "Beta", status: "live", relevance: nil),
                    job("p:2", company: "Beta", status: "live", relevance: nil),
                    job("p:3", company: "Acme", status: "live", relevance: nil)]
        var p = page(.pending, rows, counts: ["pending": 3, "new": 7, "all": 7])
        p.pending = PendingTriage(total: 3, matching: 3, companies: [.init(company: "Beta", n: 2), .init(company: "Acme", n: 1)])
        let out = try XCTUnwrap(p.patched(with: job("p:1", company: "Beta", status: "live", mark: .hidden), minRelevance: nil))
        XCTAssertEqual(out.jobs.map(\.key), ["p:2", "p:3"])
        XCTAssertEqual(out.counts, ["pending": 2, "new": 7, "hidden": 1, "all": 8], "a marked live posting is in All")
        XCTAssertEqual(out.pending, PendingTriage(total: 2, matching: 2, companies: [.init(company: "Acme", n: 1), .init(company: "Beta", n: 1)]))

        let gone = try XCTUnwrap(p.patched(with: job("p:3", company: "Acme", status: "live", mark: .saved), minRelevance: nil))
        XCTAssertEqual(gone.pending?.companies, [.init(company: "Beta", n: 2)], "a company with none left is dropped")
    }

    /// Clearing the mark of a live posting in Saved puts it back among those awaiting triage.
    func testClearingAMarkOnALivePostingAddsItToPending() throws {
        var p = page(.saved, [job("p:1", company: "Zeta", status: "live", mark: .saved, relevance: nil)],
                     counts: ["saved": 1, "all": 1, "pending": 1])
        p.pending = PendingTriage(total: 1, matching: 1, companies: [.init(company: "Acme", n: 1)])
        let out = try XCTUnwrap(p.patched(with: job("p:1", company: "Zeta", status: "live"), minRelevance: nil))
        XCTAssertEqual(out.jobs, [])
        XCTAssertEqual(out.counts, ["pending": 2])
        XCTAssertEqual(out.pending, PendingTriage(total: 2, matching: 2, companies: [.init(company: "Acme", n: 1), .init(company: "Zeta", n: 1)]))
    }

    // MARK: List inputs

    func testListInputsChangeWithContentAndPresence() throws {
        let dir = try tempDir(self)
        let client = JobsClient(vaniBinary: dir.appendingPathComponent("vani"), configPath: dir.appendingPathComponent("c.json").path,
                                stateDir: dir.path)
        let paths = client.listInputPaths(ledgerDir: nil)
        XCTAssertEqual(paths, [dir.path + "/ledger/events.jsonl", dir.path + "/marks/marks.jsonl", dir.path + "/marks/stages.jsonl",
                               dir.path + "/retriage/readings.jsonl", dir.path + "/c.json"])
        let other = client.listInputPaths(ledgerDir: "/elsewhere/state/ledger")
        XCTAssertEqual(other.first, "/elsewhere/state/ledger/events.jsonl", "the folder vani reported wins")

        let none = try XCTUnwrap(JobsListInputs.read(paths))
        XCTAssertEqual(JobsListInputs.read(paths), none, "absent files read the same twice")
        try FileManager.default.createDirectory(at: dir.appendingPathComponent("marks"), withIntermediateDirectories: true)
        let marks = dir.appendingPathComponent("marks/marks.jsonl")
        try Data("a\n".utf8).write(to: marks)
        let one = try XCTUnwrap(JobsListInputs.read(paths))
        XCTAssertNotEqual(one, none, "a file appearing is a change")
        XCTAssertEqual(JobsListInputs.read(paths), one)
        // Same size, same mtime second, new content written by replacing the file: the inode or the
        // nanoseconds tell it apart.
        try Data("b\n".utf8).write(to: marks, options: .atomic)
        XCTAssertNotEqual(JobsListInputs.read(paths), one)
    }

    /// A file that cannot be looked at (a path through a regular file is ENOTDIR, read as absent;
    /// one too long is ENAMETOOLONG) is doubt, never "unchanged".
    func testAnUnreadableInputIsNeverUnchanged() {
        XCTAssertNil(JobsListInputs.read(["/" + String(repeating: "x", count: 5000)]))
    }

    // MARK: Cancellation

    /// A cancelled list stops its child and throws `VaniCallCancelled`, not a timeout.
    func testACancelledListThrowsCancelledPromptly() throws {
        let dir = try tempDir(self)
        let url = dir.appendingPathComponent("vani")
        try Data("#!/bin/sh\ntrap 'kill $! 2>/dev/null; exit 143' TERM\nsleep 30 &\nwait\n".utf8).write(to: url)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
        let client = JobsClient(vaniBinary: url, timeout: 20)
        let flag = CancelFlag()
        DispatchQueue.global().asyncAfter(deadline: .now() + 0.3) { flag.set() }
        let started = Date()
        XCTAssertThrowsError(try client.list(.new, cancellation: flag)) { error in
            XCTAssertEqual(error as? VaniCallCancelled, VaniCallCancelled(command: "vani jobs list"))
        }
        XCTAssertLessThan(Date().timeIntervalSince(started), 5)
    }
}
