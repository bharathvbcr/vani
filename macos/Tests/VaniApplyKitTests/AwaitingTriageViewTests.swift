import XCTest
@testable import VaniApplyKit

/// The Awaiting triage view (`vani jobs list --view pending`): postings read from
/// the boards that no run has triaged. They are listed apart from the ranked
/// views and carry no fit, relevance or priority, so nothing here may draw a
/// score for them.
final class AwaitingTriageViewTests: XCTestCase {
    /// What `vani jobs list --json --view pending` writes: no relevance, priority,
    /// row or triage, an `untriaged` flag, and the prefilter's kind (never its rule).
    private static let reply = """
    {"view":"pending","jobs":[\
    {"key":"gh:acme:1","company":"Acme","title":"Inference Engineer","status":"live","first_seen":"2026-10-20T12:00:00Z",\
    "fresh_at":"2026-10-20T12:00:00Z","fresh_src":"seen","age_days":2,"untriaged":true,"logo":"/s/logos/acme.png"},\
    {"key":"gh:acme:2","company":"Acme","title":"Engineering Manager","status":"live","first_seen":"2026-10-20T12:00:00Z",\
    "untriaged":true,"prefilter_drop":"title"}],\
    "total":2,"shown":2,"counts":{"new":4,"saved":1,"applied":0,"hidden":0,"all":5,"pending":2},\
    "ledger":"/s/ledger","records":7,"sort":"fresh",\
    "pending":{"total":2,"matching":2,"companies":[{"company":"Acme","n":2}]}}
    """

    func testDecodesUntriagedRowsWithoutAnyScore() throws {
        let page = try JobsClient.decoder.decode(JobsPage.self, from: Data(Self.reply.utf8))
        XCTAssertEqual(page.view, "pending")
        XCTAssertEqual(page.count(.pending), 2)
        XCTAssertEqual(JobListPresentation.count(page, .pending), 2)
        XCTAssertEqual(page.sort, "fresh")
        let a = try XCTUnwrap(page.jobs.first)
        XCTAssertTrue(a.isUntriaged)
        XCTAssertNil(a.relevance, "an untriaged row has no relevance: absent is unknown, not 0")
        XCTAssertNil(a.priority)
        XCTAssertNil(a.row)
        XCTAssertNil(a.fitLabel)
        XCTAssertNil(a.categoryLabel)
        XCTAssertTrue(a.keywordList.isEmpty)
        XCTAssertNil(a.rankingBreakdown)
        XCTAssertEqual(a.ageLabel, "~2d", "the age falls back to the row's own fields without a row")
        XCTAssertEqual(a.logoPath, "/s/logos/acme.png")
        XCTAssertNil(JobScore.forRow(a, sort: .fresh))
        XCTAssertNil(a.prefilterNote)
        XCTAssertEqual(page.jobs[1].prefilterDrop, "title")
    }

    /// `untriaged` and `prefilter_drop` are display-only like the duplicate and logo
    /// fields: a value of the wrong type reads as absent, and one odd row never
    /// fails the whole list.
    func testWrongTypedUntriagedFieldsDecodeAsNilAndKeepTheList() throws {
        let page = try JobsClient.decoder.decode(JobsPage.self, from: Data(#"""
        {"view":"pending","jobs":[
          {"key":"gh:a:1","company":"A","title":"T","status":"live","untriaged":"yes","prefilter_drop":7},
          {"key":"gh:a:2","company":"A","title":"T","status":"live","untriaged":1,"prefilter_drop":{"kind":"title"}},
          {"key":"gh:a:3","company":"A","title":"T","status":"live","untriaged":[true],"prefilter_drop":["title"]},
          {"key":"gh:a:4","company":"A","title":"T","status":"live","untriaged":null,"prefilter_drop":null},
          {"key":"gh:a:5","company":"A","title":"T","status":"live","untriaged":true,"prefilter_drop":"title"},
          {"key":"gh:a:6","company":"A","title":"T","status":"live"}],
         "total":6,"shown":6,"counts":{}}
        """#.utf8))
        XCTAssertEqual(page.jobs.map(\.key), (1...6).map { "gh:a:\($0)" }, "a bad field never fails the list")
        for j in page.jobs.prefix(3) {
            XCTAssertNil(j.untriaged, "\(j.key): a non-bool is not a flag")
            XCTAssertNil(j.prefilterDrop, "\(j.key): a non-string is not a rule kind")
            XCTAssertFalse(j.isUntriaged)
            XCTAssertNil(j.prefilterNote)
        }
        XCTAssertNil(page.jobs[3].untriaged); XCTAssertNil(page.jobs[3].prefilterDrop)
        XCTAssertTrue(page.jobs[4].isUntriaged, "a well-typed row is still read")
        XCTAssertEqual(page.jobs[4].prefilterDrop, "title")
        XCTAssertNil(page.jobs[5].untriaged, "absent (an older vani) stays absent")
    }

    /// A row built in code keeps the public optional API, and round-trips through
    /// the encoder the list path uses.
    func testUntriagedFieldsRoundTrip() throws {
        var j = Job(key: "gh:a:1", company: "A", title: "T", status: "live")
        j.untriaged = true
        j.prefilterDrop = "excluded"
        let back = try JobsClient.decoder.decode(Job.self, from: JSONEncoder().encode(j))
        XCTAssertEqual(back.untriaged, true)
        XCTAssertEqual(back.prefilterDrop, "excluded")
        j.untriaged = nil
        j.prefilterDrop = nil
        XCTAssertNil(j.untriaged); XCTAssertNil(j.prefilterDrop)
    }

    /// Even a reply that carried a score for an untriaged row (an odd or newer vani)
    /// is not drawn as a ranking.
    func testAnUntriagedRowNeverDrawsAScore() {
        var j = Job(key: "gh:acme:1", company: "Acme", title: "Engineer", status: "live")
        j.relevance = 38.1
        j.priority = 31.1
        XCTAssertNotNil(JobScore.forRow(j, sort: .priority), "a triaged row still shows its score")
        j.untriaged = true
        XCTAssertNil(JobScore.forRow(j, sort: .priority))
        XCTAssertNil(JobScore.forRow(j, sort: .relevance))
    }

    func testAnOlderReplyDecodesAsTriaged() throws {
        let reply = #"{"view":"new","jobs":[{"key":"gh:a:1","company":"A","title":"T","status":"digested","relevance":50}],"total":1,"shown":1,"counts":{"new":1}}"#
        let page = try JobsClient.decoder.decode(JobsPage.self, from: Data(reply.utf8))
        XCTAssertFalse(page.jobs[0].isUntriaged)
        XCTAssertNil(page.jobs[0].prefilterNote)
        XCTAssertEqual(page.count(.pending), 0, "a count vani did not send reads 0 in count(_:); the tab uses the optional")
        XCTAssertNil(JobListPresentation.count(page, .pending), "unknown is not zero")
    }

    func testThePrefilterNoteNamesTheKindNeverTheRule() {
        var j = Job(key: "gh:a:1", company: "A", title: "T", status: "live")
        j.prefilterDrop = "title"
        XCTAssertNil(j.prefilterNote, "a triaged row is never flagged")
        j.untriaged = true
        for kind in ["title", "excluded", "something-newer"] {
            j.prefilterDrop = kind
            let note = try? XCTUnwrap(j.prefilterNote)
            XCTAssertNotNil(note, kind)
            XCTAssertTrue(note?.hasPrefix("The next run would drop this") == true, "\(note ?? "")")
        }
        j.prefilterDrop = ""
        XCTAssertNil(j.prefilterNote)
        j.prefilterDrop = nil
        XCTAssertNil(j.prefilterNote)
    }

    func testTheViewAndItsWords() {
        XCTAssertEqual(JobView.pending.rawValue, "pending", "the Go flag value")
        XCTAssertEqual(JobView.allCases.last, .pending, "the tab sits after the ranked views")
        XCTAssertEqual(JobView.pending.title, "Awaiting triage")
        let us = Locale(identifier: "en_US")
        XCTAssertEqual(JobView.pending.label(count: 8525, locale: us), "Awaiting triage (8,525)")
        XCTAssertEqual(JobView.pending.label(count: 0, locale: us), "Awaiting triage (0)")
        XCTAssertEqual(JobView.pending.label(count: nil), "Awaiting triage", "no count is not 0")
        XCTAssertFalse(JobView.pending.isRanked)
        for v in JobView.allCases where v != .pending { XCTAssertTrue(v.isRanked, v.rawValue) }
    }

    func testSortsAreRankFree() {
        XCTAssertEqual(JobSort.defaultSort(for: .pending), .fresh)
        XCTAssertEqual(JobSort.defaultSort(for: .new), .priority, "the ranked views keep their orders")
        XCTAssertEqual(JobSort.defaultSort(for: .saved), .standard)
        XCTAssertEqual(JobSort.options(for: .pending), [.fresh, .company])
        XCTAssertEqual(JobSort.options(for: .new), JobSort.allCases)
        let caption = JobListPresentation.sortCaption(.fresh, view: .pending)
        XCTAssertFalse(caption.contains("Badge"), caption)
        XCTAssertTrue(caption.contains("Not triaged yet"), caption)
        XCTAssertEqual(JobListPresentation.sortCaption(.priority), "Sorted by Priority (relevance × freshness). Badge: priority.")
    }

    func testListArgumentsAskForThePendingView() {
        let args = JobsClient.listArguments(.pending, query: "acme", sort: JobSort.defaultSort(for: .pending), filter: JobFilter(), limit: 1000)
        XCTAssertEqual(args, ["jobs", "list", "--json", "--view", "pending", "--limit", "1000", "--sort", "fresh", "--q", "acme"])
    }

    func testPointersToTheView() {
        let p = PendingTriage(total: 5, matching: 2, companies: [.init(company: "Acme", n: 5)])
        XCTAssertEqual(p.footerNote, "· 5 awaiting triage (see the Awaiting triage view)")
        XCTAssertTrue(p.footerHelp.contains("The Awaiting triage view lists them, unranked"), p.footerHelp)
        XCTAssertTrue(p.footerHelp.contains("Each run advances at most the config's advance_per_run postings."), p.footerHelp)
        XCTAssertTrue(p.footerHelp.contains("That cap also scores and explains postings already past triage."), p.footerHelp)
        XCTAssertFalse(p.footerHelp.contains("advance_per_run of them"), p.footerHelp)
        let one = PendingTriage(total: 1, matching: 1, companies: [.init(company: "Acme", n: 1)])
        XCTAssertTrue(one.footerHelp.contains("1 posting was read from its board"), one.footerHelp)
        XCTAssertTrue(one.footerHelp.contains("The Awaiting triage view lists it, unranked."), one.footerHelp)
        XCTAssertEqual(p.emptyListNote(in: .new), "5 postings are awaiting triage; see them, unranked, in the Awaiting triage view.")
        XCTAssertNil(p.emptyListNote(in: .pending), "the view does not point at itself")
        XCTAssertNil(p.emptySearchNote(query: "acme", in: .pending))
        XCTAssertNotNil(p.emptySearchNote(query: "acme", in: .saved))
        XCTAssertEqual(PendingTriage(total: 1, matching: 1, companies: []).emptyListNote(in: .new),
                       "1 posting is awaiting triage; see it, unranked, in the Awaiting triage view.")
        XCTAssertNil(PendingTriage(total: 0, matching: 0, companies: []).emptyListNote(in: .new))
    }

    // MARK: against the real vani (build/vani; skipped when it is not built)

    private func realVani() throws -> URL {
        let vani = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("build/vani")
        guard FileManager.default.isExecutableFile(atPath: vani.path) else {
            throw XCTSkip("no Go vani at \(vani.path); build it to run the contract test")
        }
        return vani
    }

    private func ledger(_ state: URL, _ lines: [String]) throws {
        let dir = state.appendingPathComponent("ledger")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        try Data((lines.joined(separator: "\n") + "\n").utf8).write(to: dir.appendingPathComponent("events.jsonl"))
    }

    private func event(_ key: String, _ company: String, _ title: String, _ status: String, at: String, ann: String = "") -> String {
        let ats = key.split(separator: ":")[0]
        return #"{"v":1,"at":"\#(at)","key":"\#(key)","status":"\#(status)","reason":"new","posting":{"key":"\#(key)","ats":"\#(ats)","company":"\#(company)","title":"\#(title)","seen_at":"\#(at)"}\#(ann)}"#
    }

    func testAgainstTheRealVani() throws {
        let vani = try realVani()
        let state = try tempDir(self)
        try ledger(state, [
            event("gh:acme:0", "Acme", "Old Engineer", "dead", at: "2026-09-01T12:00:00Z"),
            event("gh:acme:1", "Acme", "Inference Engineer", "live", at: "2026-10-20T12:00:00Z"),
            event("gh:acme:2", "Acme", "Platform Engineer", "live", at: "2026-10-10T12:00:00Z"),
            event("gh:acme:3", "Acme", "ML Engineer", "digested", at: "2026-10-18T12:00:00Z", ann: #","annotations":{"triage.role_fit":"strong_fit"}"#),
        ])
        let c = JobsClient(vaniBinary: vani, stateDir: state.path)
        let page = try c.list(.pending, sort: JobSort.defaultSort(for: .pending))
        XCTAssertEqual(page.jobs.map(\.key), ["gh:acme:1", "gh:acme:2"], "untriaged only, freshest first")
        XCTAssertEqual(page.counts["pending"], 2)
        XCTAssertEqual(page.pending?.total, 2)
        for j in page.jobs {
            XCTAssertTrue(j.isUntriaged, j.key)
            XCTAssertNil(j.relevance, "\(j.key): absent, not 0")
            XCTAssertNil(j.priority)
            XCTAssertNil(j.row)
            XCTAssertNil(JobScore.forRow(j, sort: .fresh))
        }
        XCTAssertEqual(try c.list(.new).jobs.map(\.key), ["gh:acme:3"], "the ranked views are unchanged")
        XCTAssertEqual(try c.list(.all).jobs.map(\.key), ["gh:acme:3"])
        // A usage error, which the client words as a too-old vani; the app never asks (JobView.isRanked).
        XCTAssertThrowsError(try c.stats(.pending), "vani refuses stats of unranked postings")

        // Saving one moves it to Saved, still unranked, and the counts follow.
        let saved = try c.mark("gh:acme:1", .saved)
        XCTAssertTrue(saved.isUntriaged)
        XCTAssertNil(saved.priority)
        let after = try c.list(.pending)
        XCTAssertEqual(after.jobs.map(\.key), ["gh:acme:2"])
        XCTAssertEqual(after.counts["pending"], 1)
        XCTAssertEqual(after.pending?.total, 1)
        XCTAssertEqual(try c.list(.saved).jobs.map(\.key), ["gh:acme:1"])
    }
}
