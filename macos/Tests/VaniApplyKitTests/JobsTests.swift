import Foundation
import XCTest
@testable import VaniApplyKit

/// A stand-in for `vani jobs`: replies with `reply`, or, when the arguments
/// contain "echo", with the arguments and Vani paths it was given.
private func fakeJobsVani(_ t: XCTestCase, reply: String, status: Int32 = 0) throws -> URL {
    let dir = try tempDir(t)
    let url = dir.appendingPathComponent("vani")
    let script = """
    #!/bin/sh
    case "$*" in *echo*) printf '{"key":"gh:a:1","company":"%s","title":"%s","status":"live"}\\n' "$*" "${VANI_STATE_DIR:-none}"; exit 0 ;; esac
    cat <<'EOF'
    \(reply)
    EOF
    exit \(status)
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return url
}

private let listReply = """
{"view":"new","jobs":[{"key":"ab:baseten:0d1f","company":"Baseten","title":"Inference Engineer","location":"San Francisco","remote":true,\
"url":"https://jobs.ashbyhq.com/baseten/0d1f","apply_url":"javascript:alert(1)","status":"digested","rank":71.5,\
"explanation":"Fits: Inference serving and runtimes (88%). Gap: Asks for senior or staff-level experience (40%). Check: Ask about on-call and production support load (70%).",\
"triage":{"role_fit":"strong_fit","seniority":"senior","work_mode":"not_stated"},"first_seen":"2026-10-18T21:15:20.122204-05:00",\
"posted_at":"2025-04-07T18:41:33.018Z","mark":"saved","marked_at":"2026-10-19T09:00:00Z"}],"total":283,"shown":1,"counts":{"new":283,"saved":1,"all":284}}
"""

final class JobsTests: XCTestCase {
    func testDecodesAListWithGoTimes() throws {
        let c = JobsClient(vaniBinary: try fakeJobsVani(self, reply: listReply))
        let page = try c.list(.new)
        XCTAssertEqual(page.total, 283)
        XCTAssertEqual(page.shown, 1)
        XCTAssertEqual(page.count(.saved), 1)
        XCTAssertEqual(page.count(.hidden), 0)
        let j = try XCTUnwrap(page.jobs.first)
        XCTAssertEqual(j.mark, .saved)
        XCTAssertEqual(j.rank, 71.5)
        XCTAssertEqual(j.fitLabel, "Strong fit")
        XCTAssertEqual(j.triageLabels, ["Senior"])
        let want = try XCTUnwrap(ISO8601DateFormatter().date(from: "2026-10-19T02:15:20Z")).addingTimeInterval(0.122204)
        // Foundation keeps milliseconds of Go's microseconds.
        XCTAssertEqual(try XCTUnwrap(j.firstSeen).timeIntervalSince1970, want.timeIntervalSince1970, accuracy: 0.001)
        XCTAssertNotNil(j.postedAt)
        XCTAssertEqual(j.postingLink?.absoluteString, "https://jobs.ashbyhq.com/baseten/0d1f")
        XCTAssertNil(j.applyLink, "a javascript: link never becomes clickable")
    }

    /// Vani splits the note (Go owns the parse); the app only decodes it. A note
    /// without a split (an older vani) is shown whole.
    func testReasonsComeFromVani() throws {
        let reply = """
        {"key":"gh:a:1","company":"A","title":"T","status":"digested",\
        "explanation":"Fits: Inference serving (88%). Gap: Senior (40%).",\
        "reasons":[{"kind":"fits","text":"Inference serving","pct":88},{"kind":"gap","text":"Senior","pct":40},{"kind":"bogus","text":"later kind"}]}
        """
        let j = try JobsClient.decoder.decode(Job.self, from: Data(reply.utf8))
        XCTAssertEqual(j.reasons, [
            .init(kind: .fits, text: "Inference serving", pct: 88),
            .init(kind: .gap, text: "Senior", pct: 40),
            .init(kind: .other, text: "later kind"),
        ])
        var old = Job(key: "gh:a:1", company: "A", title: "T")
        old.explanation = "Fits: A (1%)."
        XCTAssertEqual(old.reasons, [.init(kind: .other, text: "Fits: A (1%).")])
        old.explanation = "   "
        XCTAssertEqual(old.reasons, [])
    }

    func testDecodesInsightFields() throws {
        let reply = """
        {"key":"gh:a:1","company":"A","title":"T","status":"digested","fresh_at":"2026-10-17T12:00:00Z",\
        "fresh_src":"seen","age_days":40,"relevance":63.8,"relevance_basis":"fit","priority":33.1,\
        "keywords":[{"term":"Go","cat":"lang","n":2,"target":true},{"term":"Kubernetes","cat":"infra","n":1,"gap":true}]}
        """
        let j = try JobsClient.decoder.decode(Job.self, from: Data(reply.utf8))
        XCTAssertEqual(j.freshSource, .seen)
        XCTAssertEqual(j.ageDays, 40)
        XCTAssertEqual(j.ageLabel, "~5w", "an age from first sight is marked as approximate")
        XCTAssertEqual(j.relevance, 63.8)
        XCTAssertEqual(j.priority, 33.1)
        XCTAssertEqual(j.keywordList.map(\.term), ["Go", "Kubernetes"])
        XCTAssertEqual(j.keywordList.first?.target, true)
        XCTAssertEqual(j.keywordList.last?.gap, true)
        // A vani without the insight fields still decodes.
        let bare = try JobsClient.decoder.decode(Job.self, from: Data(#"{"key":"k:a:1","company":"A","title":"T","status":"live"}"#.utf8))
        XCTAssertNil(bare.ageLabel)
        XCTAssertEqual(bare.keywordList, [])
    }

    func testAgeLabels() {
        var j = Job(key: "gh:a:1", company: "A", title: "T")
        j.freshSource = .posted
        for (days, want) in [(0, "today"), (3, "3d"), (13, "13d"), (14, "2w"), (59, "8w"), (60, "2mo"), (400, "13mo")] {
            j.ageDays = days
            XCTAssertEqual(j.ageLabel, want, "\(days) days")
        }
        j.ageDays = -1
        XCTAssertNil(j.ageLabel)
    }

    func testFilterArguments() {
        var f = JobFilter()
        XCTAssertTrue(f.isEmpty)
        XCTAssertEqual(JobsClient.selection("  ", f), [])
        f.fits = ["strong_fit", "adjacent"]
        f.workModes = ["remote"]
        f.maxAgeDays = 14
        f.openOnly = true
        XCTAssertEqual(f.activeCount, 4)
        XCTAssertEqual(JobsClient.selection(" infra ", f),
                       ["--q", "infra", "--fit", "adjacent,strong_fit", "--work-mode", "remote", "--max-age", "14", "--open"])

        var loc = JobFilter()
        loc.states = ["TX", "none", "NM"]
        loc.areas = ["El Paso", "Las Cruces", "Bad, Area", " "]
        loc.remoteOnly = true
        XCTAssertFalse(loc.isEmpty)
        XCTAssertEqual(loc.activeCount, 3)
        XCTAssertEqual(JobsClient.selection("", loc),
                       ["--state", "NM,TX,none", "--area", "El Paso,Las Cruces", "--remote"],
                       "an area holding a comma (or nothing) is left out rather than split")
        loc.areas = ["Bad, Area"]
        XCTAssertEqual(JobsClient.selection("", loc), ["--state", "NM,TX,none", "--remote"])

        // "Remote, or in these places": sent only where Vani accepts it.
        var or = JobFilter()
        or.orRemote = true
        XCTAssertFalse(or.widensToRemote, "nothing to widen")
        XCTAssertEqual(or.activeCount, 0)
        XCTAssertTrue(or.isEmpty, "an inert or-remote filters nothing")
        XCTAssertEqual(JobsClient.selection("", or), [])
        or.areas = ["Bad, Area", " "]
        XCTAssertEqual(JobsClient.selection("", or), [], "only unsendable areas: nothing to widen")
        or.states = ["TX"]
        XCTAssertEqual(or.activeCount, 3)
        XCTAssertEqual(JobsClient.selection("", or), ["--state", "TX", "--or-remote"])
        or.remoteOnly = true
        XCTAssertFalse(or.widensToRemote, "--remote narrows; never sent with --or-remote")
        XCTAssertEqual(JobsClient.selection("", or), ["--state", "TX", "--remote"])

        // The setters keep the two exclusive, whichever control sets them.
        var ex = JobFilter()
        ex.states = ["TX"]
        ex.setOrRemote(true)
        XCTAssertTrue(ex.widensToRemote)
        ex.setRemoteOnly(true)
        XCTAssertEqual([ex.remoteOnly, ex.orRemote], [true, false])
        ex.setOrRemote(true)
        XCTAssertEqual([ex.remoteOnly, ex.orRemote], [false, true])
        ex.setRemoteOnly(false)
        XCTAssertEqual([ex.remoteOnly, ex.orRemote], [false, true], "turning one off leaves the other")
    }

    func testDecodesPlaces() throws {
        let reply = """
        {"view":"all","jobs":[\
        {"key":"gh:a:1","company":"A","title":"T","status":"digested","location":"Las Cruces, New Mexico; El Paso, TX",\
        "places":[{"area":"Las Cruces","state":"NM"},{"area":"El Paso","state":"TX"}],"first_seen":"2026-10-18T07:00:00Z"},\
        {"key":"gh:a:2","company":"A","title":"T","status":"digested","location":"Remote - US","remote_listed":true,\
        "remote_scope":"US","first_seen":"2026-10-18T07:00:00Z"},\
        {"key":"gh:a:3","company":"A","title":"T","status":"digested","location":"Austin, TX","remote":true,\
        "places":[{"area":"Austin","state":"TX"}],"remote_listed":true,"first_seen":"2026-10-18T07:00:00Z"},\
        {"key":"gh:a:4","company":"A","title":"T","status":"digested","location":"2 Locations","first_seen":"2026-10-18T07:00:00Z"},\
        {"key":"gh:a:5","company":"A","title":"T","status":"digested","places":[{"area":"London","region":"United Kingdom"}],\
        "first_seen":"2026-10-18T07:00:00Z"}],\
        "total":5,"shown":5,"counts":{"all":5}}
        """
        let page = try JobsClient.decoder.decode(JobsPage.self, from: Data(reply.utf8))
        XCTAssertEqual(page.jobs[0].places, [Job.Place(area: "Las Cruces", state: "NM"), Job.Place(area: "El Paso", state: "TX")])
        XCTAssertEqual(page.jobs[0].whereLabel, "Las Cruces, NM; El Paso, TX")
        XCTAssertNil(page.jobs[0].remoteLabel)
        XCTAssertEqual(page.jobs[1].whereLabel, "Remote (US)")
        XCTAssertEqual(page.jobs[2].whereLabel, "Austin, TX · Remote")
        XCTAssertEqual(page.jobs[3].whereLabel, "2 Locations", "nothing recognised: the board's own text")
        XCTAssertEqual(page.jobs[4].whereLabel, "London, United Kingdom")
        // An older vani sends no places: the board's text and flag, as before.
        var old = Job(key: "k", company: "C", title: "T")
        old.location = "Berlin"
        old.remote = true
        XCTAssertEqual(old.whereLabel, "Berlin · Remote")
        XCTAssertNil(Job(key: "k", company: "C", title: "T").whereLabel)
    }

    func testListAndStatsArguments() {
        var f = JobFilter()
        f.sponsorships = ["none"]
        XCTAssertEqual(JobsClient.listArguments(.saved, query: "", sort: .standard, filter: JobFilter(), limit: 10),
                       ["jobs", "list", "--json", "--view", "saved", "--limit", "10"], "the default order sends no --sort")
        XCTAssertEqual(JobsClient.listArguments(.new, query: "q", sort: .priority, filter: f, limit: 1000),
                       ["jobs", "list", "--json", "--view", "new", "--limit", "1000", "--sort", "priority", "--q", "q", "--sponsorship", "none"])
        XCTAssertEqual(JobsClient.statsArguments(.all, query: "", filter: f),
                       ["jobs", "stats", "--json", "--view", "all", "--sponsorship", "none"])
    }

    func testDecodesStats() throws {
        let reply = """
        {"view":"all","total":3,"fit":{"strong_fit":1,"none":2},"work_mode":{"remote":1},"sponsorship":{},"seniority":{},\
        "basis":{"fit":1,"none":2},"fresh_source":{"posted":2,"seen":1},\
        "age":[{"label":"≤1d","max_days":1,"posted":1,"seen":0},{"label":">2mo","max_days":-1,"posted":1,"seen":1}],\
        "relevance":[0,0,1,0,0,0,2,0,0,0],"priority":[0,1,0,0,2,0,0,0,0,0],\
        "companies":[{"company":"Acme","n":2,"strong":1,"mean_relevance":55.5}],\
        "keywords":[{"term":"Go","cat":"lang","n":2,"target":true}],\
        "insight":{"target":true,"target_terms":4,"half_life_days":14,"vocab":198,"boilerplate":0},"ledger":"/l","records":4}
        """
        let s = try JobsClient.decoder.decode(JobsStats.self, from: Data(reply.utf8))
        XCTAssertEqual(s.total, 3)
        XCTAssertEqual(s.fit["none"], 2)
        XCTAssertEqual(s.age.last?.maxDays, -1)
        XCTAssertEqual(s.companies.first?.meanRelevance, 55.5)
        XCTAssertEqual(s.keywords.first?.target, true)
        XCTAssertEqual(s.insight?.targetTerms, 4)
        XCTAssertNil(s.state, "a vani from before location parsing still decodes")
        XCTAssertTrue(s.statesByCount.isEmpty)
    }

    func testDecodesLocationStats() throws {
        let reply = """
        {"view":"all","total":4,"fit":{},"work_mode":{},"sponsorship":{},"seniority":{},"basis":{},"fresh_source":{},\
        "age":[],"relevance":[],"priority":[],"companies":[],"keywords":[],\
        "state":{"TX":3,"NM":1,"CA":1,"none":2},"area":[{"area":"El Paso","state":"TX","n":2},{"area":"London","n":1}],\
        "remote_listed":2,"remote_scope":{"US":1,"unstated":1}}
        """
        let s = try JobsClient.decoder.decode(JobsStats.self, from: Data(reply.utf8))
        XCTAssertEqual(s.statesByCount.map(\.code), ["TX", "CA", "NM"], "most first, ties by code, none left out")
        XCTAssertEqual(s.area?.map(\.label), ["El Paso, TX", "London"])
        XCTAssertEqual(s.remoteListed, 2)
        XCTAssertEqual(s.remoteScope?["unstated"], 1)
    }

    /// The app's option lists are the jdtriage fixture's, read in place.
    func testOptionListsMatchTheFixture() throws {
        let fixture = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("internal/decide/jdtriage/fixture.json")
        struct Fixture: Decodable {
            struct Family: Decodable {
                struct Slot: Decodable {
                    struct Option: Decodable { var value: String }
                    var name: String
                    var options: [Option]
                }
                var slot: Slot
            }
            var families: [Family]
        }
        let f = try JSONDecoder().decode(Fixture.self, from: Data(contentsOf: fixture))
        let options = Dictionary(uniqueKeysWithValues: f.families.map { ($0.slot.name, Set($0.slot.options.map(\.value))) })
        XCTAssertEqual(options.count, 4)
        XCTAssertEqual(Set(Job.roleFitOrder), options["role_fit"])
        XCTAssertEqual(Set(Job.workModeOptions), options["work_mode"])
        XCTAssertEqual(Set(Job.sponsorshipOptions), options["sponsorship"])
        for (_, values) in options {
            for v in values where v != "not_stated" {
                XCTAssertNotEqual(Job.label(for: v), v.replacingOccurrences(of: "_", with: " "), "\(v) has no words")
            }
        }
    }

    func testOnlyHTTPLinksOpen() {
        for bad in ["javascript:alert(1)", "data:text/html,x", "file:///etc/passwd", "https://", "notaurl", ""] {
            XCTAssertNil(Job.httpURL(bad), bad)
        }
        XCTAssertNotNil(Job.httpURL("http://example.com/x"))
    }

    func testPassesArgumentsAndStateDirOnly() throws {
        let c = JobsClient(vaniBinary: try fakeJobsVani(self, reply: ""), stateDir: "/state")
        let j = try c.note("gh:a:1", "echo; rm -rf / $(whoami)")
        XCTAssertEqual(j.company, "jobs note --json gh:a:1 echo; rm -rf / $(whoami)", "arguments reach vani verbatim, never through a shell")
        XCTAssertEqual(j.title, "/state")
    }

    func testErrorsCarryVanisMessage() throws {
        let c = JobsClient(vaniBinary: try fakeJobsVani(self, reply: #"{"error":"jobs: no posting \"gh:a:typo\" in the ledger"}"#, status: 1))
        XCTAssertThrowsError(try c.mark("gh:a:typo", .saved)) { err in
            XCTAssertTrue(String(describing: err).contains("no posting"), String(describing: err))
        }
        let old = JobsClient(vaniBinary: try fakeJobsVani(self, reply: "vani: unknown command", status: 2))
        XCTAssertThrowsError(try old.list(.new)) { err in
            XCTAssertTrue(String(describing: err).contains("too old"), String(describing: err))
        }
        let missing = JobsClient(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani"))
        XCTAssertThrowsError(try missing.list(.new))
    }

    func testRefusesAnOversizedNoteBeforeRunningVani() throws {
        let c = JobsClient(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani"))
        XCTAssertThrowsError(try c.note("gh:a:1", String(repeating: "x", count: JobsClient.maxNoteBytes + 1))) { err in
            XCTAssertTrue(String(describing: err).contains("at most"), String(describing: err))
        }
    }

    func testTimesOut() throws {
        let dir = try tempDir(self)
        let url = dir.appendingPathComponent("vani")
        try Data("#!/bin/sh\nsleep 5\n".utf8).write(to: url)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
        let started = Date()
        XCTAssertThrowsError(try JobsClient(vaniBinary: url, timeout: 1).list(.new))
        XCTAssertLessThan(Date().timeIntervalSince(started), 4)
    }

    /// The contract end to end: the real Go `vani jobs` against a small ledger.
    /// Needs Vani/build/vani built from this checkout (`go -C … build -o build/vani ./cmd/vani`).
    func testChartHitPicksTheNearestPointWithinTheRadius() {
        let pts: [CGPoint?] = [CGPoint(x: 0, y: 0), CGPoint(x: 10, y: 0), nil, CGPoint(x: 4, y: 3)]
        XCTAssertEqual(ChartHit.nearest(to: CGPoint(x: 9, y: 0), among: pts), 1)
        XCTAssertEqual(ChartHit.nearest(to: CGPoint(x: 4, y: 2), among: pts), 3)
        // Equidistant: the earlier point wins.
        XCTAssertEqual(ChartHit.nearest(to: CGPoint(x: 5, y: 0), among: [CGPoint(x: 0, y: 0), CGPoint(x: 10, y: 0)]), 0)
        // Exactly at the radius is outside it; just inside is a hit.
        XCTAssertNil(ChartHit.nearest(to: CGPoint(x: 0, y: 14), among: [CGPoint(x: 0, y: 0)]))
        XCTAssertEqual(ChartHit.nearest(to: CGPoint(x: 0, y: 13.9), among: [CGPoint(x: 0, y: 0)]), 0)
        XCTAssertEqual(ChartHit.nearest(to: CGPoint(x: 0, y: 20), among: [CGPoint(x: 0, y: 0)], within: 25), 0)
    }

    func testChartHitRefusesWhatItCannotPlace() {
        XCTAssertNil(ChartHit.nearest(to: .zero, among: []))
        XCTAssertNil(ChartHit.nearest(to: .zero, among: [nil, nil]))
        XCTAssertNil(ChartHit.nearest(to: .zero, among: [CGPoint(x: CGFloat.nan, y: 0), CGPoint(x: CGFloat.infinity, y: 0)]))
        XCTAssertNil(ChartHit.nearest(to: CGPoint(x: CGFloat.nan, y: 0), among: [.zero]))
        XCTAssertNil(ChartHit.nearest(to: .zero, among: [.zero], within: 0))
        XCTAssertNil(ChartHit.nearest(to: .zero, among: [.zero], within: -1))
        // A point the chart could not place never shadows one it could.
        XCTAssertEqual(ChartHit.nearest(to: .zero, among: [nil, CGPoint(x: 1, y: 1)]), 1)
    }

    func testAgainstTheRealVani() throws {
        let vani = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("build/vani")
        guard FileManager.default.isExecutableFile(atPath: vani.path) else {
            throw XCTSkip("no Go vani at \(vani.path); build it to run the contract test")
        }
        let state = try tempDir(self)
        let ledger = state.appendingPathComponent("ledger")
        try FileManager.default.createDirectory(at: ledger, withIntermediateDirectories: true)
        let line = #"{"v":1,"at":"2026-10-18T12:00:00.5Z","key":"gh:acme:1","status":"digested","posting":{"key":"gh:acme:1","ats":"gh","company":"Acme","title":"Inference Engineer","url":"https://boards.example.com/1","description":"Serve models.","seen_at":"2026-10-18T12:00:00Z"},"annotations":{"explanation":"Fits: Inference serving and runtimes (88%).","triage.role_fit":"strong_fit"}}"#
        try Data((line + "\n").utf8).write(to: ledger.appendingPathComponent("events.jsonl"))

        let c = JobsClient(vaniBinary: vani, stateDir: state.path)
        var page = try c.list(.new)
        guard page.jobs.count == 1 else {
            return XCTFail("list: \(page)")
        }
        XCTAssertEqual(page.records, 1)
        XCTAssertEqual(page.ledger, ledger.path)
        XCTAssertEqual(page.jobs[0].reasons, [.init(kind: .fits, text: "Inference serving and runtimes", pct: 88)])
        XCTAssertEqual(page.jobs[0].freshSource, .seen, "the ledger line has no posted date")
        XCTAssertNotNil(page.jobs[0].priority)
        XCTAssertNotNil(page.insight)
        let sorted = try c.list(.new, sort: .fresh, filter: { var f = JobFilter(); f.fits = ["strong_fit"]; return f }())
        XCTAssertEqual(sorted.jobs.map(\.key), ["gh:acme:1"])
        XCTAssertEqual(sorted.sort, "fresh")
        let stats = try c.stats(.new)
        XCTAssertEqual(stats.total, 1)
        XCTAssertEqual(stats.fit["strong_fit"], 1)
        var j = try c.mark("gh:acme:1", .saved)
        XCTAssertEqual(j.mark, .saved)
        XCTAssertNotNil(j.markedAt)
        j = try c.note("gh:acme:1", "ask about on-call")
        XCTAssertEqual(j.note, "ask about on-call")
        page = try c.list(.new)
        XCTAssertEqual(page.jobs.count, 0)
        XCTAssertEqual(page.count(.saved), 1)
        XCTAssertEqual(try c.show("gh:acme:1").description, "Serve models.")
        XCTAssertThrowsError(try c.mark("gh:acme:typo", .saved))
        _ = try c.mark("gh:acme:1", nil)
        XCTAssertEqual(try c.list(.new).jobs.count, 1)
    }

    /// The pending contract end to end: a posting awaiting triage is not
    /// listed, and the real vani's count of it decodes here.
    func testPendingAgainstTheRealVani() throws {
        let vani = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("build/vani")
        guard FileManager.default.isExecutableFile(atPath: vani.path) else {
            throw XCTSkip("no Go vani at \(vani.path); build it to run the contract test")
        }
        let state = try tempDir(self)
        let ledger = state.appendingPathComponent("ledger")
        try FileManager.default.createDirectory(at: ledger, withIntermediateDirectories: true)
        let lines = [
            #"{"v":1,"at":"2026-10-18T12:00:00Z","key":"gh:anthropic:1","status":"live","reason":"new","posting":{"key":"gh:anthropic:1","ats":"gh","company":"Anthropic","title":"Performance Engineer","seen_at":"2026-10-18T12:00:00Z"}}"#,
            #"{"v":1,"at":"2026-10-18T12:00:00Z","key":"gh:acme:1","status":"digested","posting":{"key":"gh:acme:1","ats":"gh","company":"Acme","title":"Inference Engineer","seen_at":"2026-10-18T12:00:00Z"},"annotations":{"triage.role_fit":"strong_fit"}}"#,
        ]
        try Data((lines.joined(separator: "\n") + "\n").utf8).write(to: ledger.appendingPathComponent("events.jsonl"))

        let c = JobsClient(vaniBinary: vani, stateDir: state.path)
        let page = try c.list(.new, query: "anthropic")
        XCTAssertEqual(page.jobs.count, 0, "a posting awaiting triage is not listed")
        XCTAssertEqual(page.pending, PendingTriage(total: 1, matching: 1, companies: [.init(company: "Anthropic", n: 1)]))
        XCTAssertNotNil(page.pending?.emptySearchNote(query: "anthropic"))
        XCTAssertEqual(try c.list(.new).jobs.map(\.key), ["gh:acme:1"])
    }

    /// The location contract end to end: the places Go reads decode here, and
    /// the Location filters the app sends are flags the real vani accepts.
    func testLocationAgainstTheRealVani() throws {
        let vani = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("build/vani")
        guard FileManager.default.isExecutableFile(atPath: vani.path) else {
            throw XCTSkip("no Go vani at \(vani.path); build it to run the contract test")
        }
        let state = try tempDir(self)
        let ledger = state.appendingPathComponent("ledger")
        try FileManager.default.createDirectory(at: ledger, withIntermediateDirectories: true)
        func line(_ key: String, _ location: String, remote: Bool = false) -> String {
            #"{"v":1,"at":"2026-10-18T12:00:00.5Z","key":"\#(key)","status":"digested","posting":{"key":"\#(key)","ats":"gh","company":"Acme","title":"Engineer","url":"https://boards.example.com/1","location":"\#(location)","remote":\#(remote),"seen_at":"2026-10-18T12:00:00.5Z"}}"#
        }
        let lines = [line("gh:acme:1", "Las Cruces, New Mexico; El Paso, TX"), line("gh:acme:2", "Remote - US"),
                     line("gh:acme:3", "London, United Kingdom")]
        try Data((lines.joined(separator: "\n") + "\n").utf8).write(to: ledger.appendingPathComponent("events.jsonl"))

        let c = JobsClient(vaniBinary: vani, stateDir: state.path)
        let page = try c.list(.new)
        let byKey = Dictionary(uniqueKeysWithValues: page.jobs.map { ($0.key, $0) })
        XCTAssertEqual(byKey["gh:acme:1"]?.whereLabel, "Las Cruces, NM; El Paso, TX")
        XCTAssertEqual(byKey["gh:acme:2"]?.whereLabel, "Remote (US)")
        XCTAssertEqual(byKey["gh:acme:3"]?.whereLabel, "London, United Kingdom")

        var f = JobFilter()
        f.states = ["NM"]
        XCTAssertEqual(try c.list(.new, filter: f).jobs.map(\.key), ["gh:acme:1"])
        f = JobFilter()
        f.remoteOnly = true
        XCTAssertEqual(try c.list(.new, filter: f).jobs.map(\.key), ["gh:acme:2"])
        f = JobFilter()
        f.areas = ["El Paso"]
        f.states = ["TX"]
        XCTAssertEqual(try c.list(.new, filter: f).jobs.map(\.key), ["gh:acme:1"])
        f = JobFilter()
        f.states = ["none"]
        XCTAssertEqual(Set(try c.list(.new, filter: f).jobs.map(\.key)), ["gh:acme:2", "gh:acme:3"])
        f = JobFilter()
        f.states = ["NM"]
        f.orRemote = true
        XCTAssertEqual(Set(try c.list(.new, filter: f).jobs.map(\.key)), ["gh:acme:1", "gh:acme:2"])
        f.remoteOnly = true // the app drops --or-remote rather than send both
        XCTAssertEqual(try c.list(.new, filter: f).jobs.map(\.key), [])

        let stats = try c.stats(.new)
        XCTAssertEqual(stats.state?["TX"], 1)
        XCTAssertEqual(stats.state?["none"], 2)
        XCTAssertEqual(stats.remoteListed, 1)
        XCTAssertEqual(stats.remoteScope?["US"], 1)
        XCTAssertEqual(Set(stats.area?.map(\.label) ?? []), ["Las Cruces, NM", "El Paso, TX", "London"])
    }
}
