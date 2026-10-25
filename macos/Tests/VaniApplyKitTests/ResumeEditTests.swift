import Foundation
import XCTest
@testable import VaniApplyKit

/// The Studio editor's draft, its rules and its edit JSON, with no window and no vani. Every id, title and
/// sentence here is invented: no personal data in any test.
final class ResumeEditTests: XCTestCase {
    static let poolJSON = """
    {"summary":[{"id":"s1","text":"Builds data pipelines.","text_hash":"aaaaaaaaaaaa"},
                {"id":"s2","text":"Keeps them fast.","text_hash":"bbbbbbbbbbbb","follows":"s1"},
                {"id":"s3","text":"Writes Kubernetes operators.","text_hash":"cccccccccccc"}],
     "summary_sections":["Summary"],
     "entries":[
       {"id":"job1","title":"ML Engineer","org":"Acme","dates":"2022 – 2024","sections":["Experience"],
        "bullets":[{"id":"j1","text":"Trained models.","text_hash":"111111111111"},
                   {"id":"j2","text":"Shipped them.","text_hash":"222222222222","follows":"j1"},
                   {"id":"j3","text":"Watched them.","text_hash":"333333333333","follows":"j2"},
                   {"id":"j4","text":"Used CUDA kernels.","text_hash":"444444444444"}]},
       {"id":"projA","title":"Project A","sections":["Projects","Open Source"],
        "bullets":[{"id":"p1","text":"Built a compiler.","text_hash":"555555555555"},{"id":"p2","text":"Wrote docs.","text_hash":"666666666666"}]},
       {"id":"projB","title":"Project B","sections":["Projects","Open Source"],
        "bullets":[{"id":"q1","text":"Built a router.","text_hash":"777777777777"}]},
       {"id":"edu","title":"BS Computer Science","sections":["Education"],"bullets":[{"id":"e1","text":"Graduated.","text_hash":"888888888888"}]}],
     "base":{"sections":[
       {"title":"Summary","bullets":["s1"]},
       {"title":"Experience","entries":[{"entry":"job1","bullets":["j1","j2"]}]},
       {"title":"Projects","entries":[{"entry":"projA","bullets":["p1"]}]},
       {"title":"Open Source"},
       {"title":"Education","entries":[{"entry":"edu","bullets":[]}]}],
       "jd_keywords":["CUDA"]}}
    """

    static let selectionJSON = """
    {"sections":[
      {"title":"Summary","bullets":["s1","s2"]},
      {"title":"Experience","entries":[{"entry":"job1","bullets":["j1","j2","j3"]}]},
      {"title":"Projects","entries":[{"entry":"projA","bullets":["p1"],"title":"Compiler"}]},
      {"title":"Education","entries":[{"entry":"edu","bullets":[]}]}],
     "jd_keywords":["Kubernetes","CUDA"]}
    """

    private func json(_ s: String) throws -> ResumeJSON { try JSONDecoder().decode(ResumeJSON.self, from: Data(s.utf8)) }
    private func pool() throws -> ResumePool { try XCTUnwrap(ResumePool(try json(Self.poolJSON))) }
    private func draft(_ selection: String = ResumeEditTests.selectionJSON) throws -> ResumeDraft {
        try XCTUnwrap(ResumeDraft.parse(selection: try json(selection), pool: try pool()))
    }

    private func object(_ data: Data) throws -> [String: Any] {
        try XCTUnwrap(try JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    // MARK: Pool

    func testThePoolReadsEveryEntryBulletAndContinuation() throws {
        let p = try pool()
        XCTAssertEqual(p.summary.map(\.id), ["s1", "s2", "s3"])
        XCTAssertEqual(p.follows, ["s2": "s1", "j2": "j1", "j3": "j2"])
        XCTAssertEqual(p.entry("job1")?.bullets.count, 4)
        XCTAssertEqual(p.entry("projA")?.sections, ["Projects", "Open Source"])
        XCTAssertEqual(p.bareInBase, ["edu"])
        XCTAssertEqual(p.sectionTitles, ["Summary", "Experience", "Projects", "Open Source", "Education"])
        XCTAssertEqual(p.reorderableSections, ["Projects", "Open Source"], "only the sections a project may go in can be reordered")
    }

    func testAPoolThatIsNotWholeIsNoPoolNeverAnEmptyOne() throws {
        XCTAssertNil(ResumePool(nil))
        XCTAssertNil(ResumePool(try json("[]")))
        XCTAssertNil(ResumePool(try json(#"{"summary":[]}"#)), "no entries list: not an empty pool")
        XCTAssertNil(ResumePool(try json(#"{"entries":[]}"#)))
        XCTAssertNotNil(ResumePool(try json(#"{"summary":[],"entries":[]}"#)))
        let junk = try XCTUnwrap(ResumePool(try json(#"{"summary":[{"text":"no id"},{"id":""}],"entries":[{"title":"no id"}]}"#)))
        XCTAssertTrue(junk.summary.isEmpty && junk.entries.isEmpty, "a bullet or entry with no id cannot be placed")
    }

    // MARK: Draft and edit JSON

    func testASelectionBecomesADraftAndWritesBackTheSameEdit() throws {
        let d = try draft()
        XCTAssertEqual(d.sections.map(\.title), ["Summary", "Experience", "Projects", "Education", "Open Source"],
                       "the selection's sections in its order, then the ones it leaves out")
        XCTAssertEqual(d.sections.last?.listed, false)
        XCTAssertEqual(d.sections.first?.kind, .bullets)
        XCTAssertEqual(d.entry("projA")?.overrides.title, "Compiler")
        XCTAssertEqual(d.keywords, ["Kubernetes", "CUDA"])

        let again = try XCTUnwrap(ResumeDraft.parse(selection: try json(String(decoding: try d.editJSON(), as: UTF8.self)), pool: try pool()))
        XCTAssertEqual(again, d, "reading the edit back gives the draft (an unlisted empty section is not written, then comes back unlisted)")

        let o = try object(try d.editJSON())
        XCTAssertEqual(Set(o.keys), ["sections", "jd_keywords"])
        let sections = try XCTUnwrap(o["sections"] as? [[String: Any]])
        XCTAssertEqual(sections.map { $0["title"] as? String }, ["Summary", "Experience", "Projects", "Education"])
        XCTAssertEqual(sections[0]["bullets"] as? [String], ["s1", "s2"])
        XCTAssertNil(sections[0]["entries"])
        let edu = try XCTUnwrap((sections[3]["entries"] as? [[String: Any]])?.first)
        XCTAssertEqual(edu["bullets"] as? [String], [], "a heading-only entry lists no bullets, and the key is there")
        let proj = try XCTUnwrap((sections[2]["entries"] as? [[String: Any]])?.first)
        XCTAssertEqual(Set(proj.keys), ["entry", "bullets", "title"], "an empty override is not written")
    }

    func testTheEditNeverCarriesAnythingButTheKeysGoReads() throws {
        var d = try draft()
        d.setOverride(.stack, "Swift, Go", entry: "projA")
        d.setOverride(.lead, "Built a thing.", entry: "projA")
        d.setOverride(.org, "Acme Labs", entry: "job1")
        d.setOverride(.dates, "2022 – 2024", entry: "job1")
        let o = try object(try d.editJSON())
        var keys: Set<String> = []
        for s in try XCTUnwrap(o["sections"] as? [[String: Any]]) {
            keys.formUnion(s.keys.map { "section." + $0 })
            for e in (s["entries"] as? [[String: Any]]) ?? [] { keys.formUnion(e.keys.map { "entry." + $0 }) }
        }
        let allowed: Set<String> = ["section.title", "section.bullets", "section.entries", "entry.entry", "entry.bullets", "entry.title",
                                    "entry.stack", "entry.lead", "entry.org", "entry.dates"]
        XCTAssertTrue(keys.isSubset(of: allowed), "\(keys.subtracting(allowed))")
    }

    func testNoKeywordsMeansNoKeyAndABaseDraftCarriesTheBasesOwn() throws {
        var d = try draft()
        d.keywords = []
        XCTAssertNil(try object(try d.editJSON())["jd_keywords"])
        let base = try XCTUnwrap(ResumeDraft.parse(selection: try pool().base, pool: try pool()))
        XCTAssertEqual(base.keywords, ["CUDA"])
        XCTAssertEqual(base.section("Open Source")?.listed, true, "the base lists it, so it is written even when empty")
        XCTAssertEqual(base.section("Open Source")?.kind, .entries, "an empty section's kind comes from the pool")
        XCTAssertEqual(base.section("Summary")?.bullets.map(\.id), ["s1"])
    }

    func testAnUnlistedSectionIsWrittenOnceItHasAnEntryAndNotAfterItIsEmptiedAgain() throws {
        var d = try draft()
        XCTAssertNil(d.addEntry("projB", to: "Open Source", pool: try pool()))
        var titles = (try object(try d.editJSON())["sections"] as? [[String: Any]])?.compactMap { $0["title"] as? String }
        XCTAssertEqual(titles?.last, "Open Source")
        d.removeEntry("projB")
        titles = (try object(try d.editJSON())["sections"] as? [[String: Any]])?.compactMap { $0["title"] as? String }
        XCTAssertFalse(titles?.contains("Open Source") ?? true)
    }

    func testASectionTheOwnerEmptiedStaysListedSoItsPlaceSurvives() throws {
        var d = try draft()
        d.removeEntry("projA")
        let sections = try XCTUnwrap(try object(try d.editJSON())["sections"] as? [[String: Any]])
        let projects = try XCTUnwrap(sections.first { $0["title"] as? String == "Projects" })
        XCTAssertNil(projects["entries"], "no entries key for an empty section; the title keeps its place")
    }

    func testSomethingThatIsNotASelectionIsNotADraft() throws {
        XCTAssertNil(ResumeDraft.parse(selection: nil, pool: nil))
        XCTAssertNil(ResumeDraft.parse(selection: try json(#"{"sections":"x"}"#), pool: nil))
        XCTAssertNil(ResumeDraft.parse(selection: try json(#"{"sections":[{"bullets":["a"]}]}"#), pool: nil), "a section with no title")
        XCTAssertNil(ResumeDraft.parse(selection: try json(#"{"sections":[{"title":"A","entries":[{"bullets":[]}]}]}"#), pool: nil), "an entry with no id")
        let bare = try XCTUnwrap(ResumeDraft.parse(selection: try json(#"{"sections":[{"title":"A","bullets":["a"]}]}"#), pool: nil))
        XCTAssertEqual(bare.sections.count, 1, "without a pool only what the selection lists")
    }

    // MARK: Editing

    func testSwitchingALeadOffSwitchesItsContinuationsOffAndAContinuationNeedsItsLead() throws {
        var d = try draft(), p = try pool()
        let place = ResumeDraft.Place.entry("job1")
        XCTAssertNil(d.setIncluded("j1", false, in: place, pool: p))
        XCTAssertEqual(d.rows(in: place)?.map(\.included), [false, false, false], "j2 and j3 continue j1, so they go with it (a chain of two)")
        XCTAssertNotNil(d.setIncluded("j3", true, in: place, pool: p), "j3 continues j2, which is off")
        XCTAssertNotNil(d.setIncluded("j2", true, in: place, pool: p), "j2 continues j1, which is off")
        XCTAssertNil(d.setIncluded("j1", true, in: place, pool: p))
        XCTAssertEqual(d.rows(in: place)?.map(\.included), [true, false, false], "switching a lead on does not bring its continuations back")
        XCTAssertNil(d.setIncluded("j2", true, in: place, pool: p))
        XCTAssertNil(d.setIncluded("j3", true, in: place, pool: p))
        XCTAssertNil(d.setIncluded("j3", false, in: place, pool: p))
        XCTAssertEqual(d.rows(in: place)?.map(\.included), [true, true, false], "a continuation can go alone")
        p = try pool()
        XCTAssertNotNil(d.setIncluded("nope", true, in: place, pool: p))
    }

    func testMovingABulletMovesItsContinuationsWithIt() throws {
        var d = try draft()
        let p = try pool()
        let place = ResumeDraft.Place.entry("job1")
        XCTAssertNil(d.addBullet("j4", to: place, pool: p))
        XCTAssertEqual(d.rows(in: place)?.map(\.id), ["j1", "j2", "j3", "j4"])
        XCTAssertEqual(ResumeDraft.groups(d.rows(in: place) ?? [], follows: p.follows), [0..<3, 3..<4])
        d.moveGroup(in: place, from: 1, toOffset: 0, pool: p)
        XCTAssertEqual(d.rows(in: place)?.map(\.id), ["j4", "j1", "j2", "j3"], "j4 first; the chain j1 j2 j3 stayed together and in order")
        d.moveGroup(in: place, from: 0, toOffset: 2, pool: p)
        XCTAssertEqual(d.rows(in: place)?.map(\.id), ["j1", "j2", "j3", "j4"], "a drop below the last group (destination 2) lands last")
        d.moveGroup(in: place, from: 0, toOffset: 9, pool: p)
        d.moveGroup(in: place, from: 5, toOffset: 0, pool: p)
        XCTAssertEqual(d.rows(in: place)?.map(\.id), ["j1", "j2", "j3", "j4"], "an out-of-range move changes nothing")
        XCTAssertTrue(d.issues(pool: p).isEmpty, "\(d.issues(pool: p))")
    }

    func testAddingABulletKeepsContinuationsAfterTheirLeadAndRefusesOrphans() throws {
        var d = try draft(), p = try pool()
        let place = ResumeDraft.Place.entry("job1")
        d.removeEntry("job1")
        XCTAssertNil(d.addEntry("job1", to: "Experience", pool: p))
        XCTAssertEqual(d.entry("job1")?.bullets.map(\.id), ["j1"], "a new entry comes with its first bullet that is not a continuation")
        XCTAssertNotNil(d.addBullet("j3", to: place, pool: p), "j3 continues j2, which is not on the page")
        XCTAssertNil(d.addBullet("j2", to: place, pool: p))
        XCTAssertNil(d.addBullet("j3", to: place, pool: p))
        XCTAssertNil(d.addBullet("j4", to: place, pool: p))
        XCTAssertEqual(d.rows(in: place)?.map(\.id), ["j1", "j2", "j3", "j4"])
        XCTAssertNotNil(d.addBullet("j4", to: place, pool: p), "already on the page")
        XCTAssertNotNil(d.addBullet("p1", to: place, pool: p), "a bullet of another entry is not offered here")
        XCTAssertNil(d.setIncluded("j4", false, in: place, pool: p))
        XCTAssertNil(d.addBullet("j4", to: place, pool: p), "a bullet switched off is switched on again where it was")
        XCTAssertEqual(d.rows(in: place)?.map(\.id), ["j1", "j2", "j3", "j4"])
    }

    func testAContinuationAddedAfterItsLeadGoesDirectlyAfterIt() throws {
        var d = try draft(#"{"sections":[{"title":"Summary","bullets":["s1","s3"]}]}"#)
        let p = try pool()
        XCTAssertNil(d.addBullet("s2", to: .summary("Summary"), pool: p))
        XCTAssertEqual(d.rows(in: .summary("Summary"))?.map(\.id), ["s1", "s2", "s3"], "s2 continues s1: right after it, not at the end")
    }

    func testSwappingReplacesALeadAndItsContinuationsInPlace() throws {
        var d = try draft(), p = try pool()
        let place = ResumeDraft.Place.entry("job1")
        XCTAssertNotNil(d.swapBullet("j1", for: "j2", in: place, pool: p), "a continuation is added with its lead, not swapped in")
        XCTAssertNotNil(d.swapBullet("j1", for: "j3", in: place, pool: p))
        XCTAssertNil(d.swapBullet("j1", for: "j4", in: place, pool: p))
        XCTAssertEqual(d.rows(in: place)?.map(\.id), ["j4"], "j1 went with j2 and j3, which continued it")
        XCTAssertNotNil(d.swapBullet("j4", for: "j4", in: place, pool: p))
        XCTAssertNotNil(d.swapBullet("zz", for: "j1", in: place, pool: p))
        XCTAssertNotNil(d.swapBullet("j4", for: "p1", in: place, pool: p), "p1 is not offered under job1")
    }

    func testEntriesMoveAndSectionsMoveOnlyAmongTheReorderableOnes() throws {
        var d = try draft(), p = try pool()
        XCTAssertNil(d.addEntry("projB", to: "Projects", pool: p))
        XCTAssertEqual(d.section("Projects")?.entries.map(\.id), ["projA", "projB"])
        d.moveEntry("projB", by: -1)
        XCTAssertEqual(d.section("Projects")?.entries.map(\.id), ["projB", "projA"])
        d.moveEntry("projB", by: -1)
        XCTAssertEqual(d.section("Projects")?.entries.map(\.id), ["projB", "projA"], "already first")

        XCTAssertEqual(d.sections.map(\.title), ["Summary", "Experience", "Projects", "Education", "Open Source"])
        d.moveSection("Open Source", by: -1, among: p.reorderableSections)
        XCTAssertEqual(d.sections.map(\.title), ["Summary", "Experience", "Open Source", "Education", "Projects"],
                       "the two project sections trade places; the sections between them stay where they are")
        d.moveSection("Experience", by: 1, among: p.reorderableSections)
        d.moveSection("Open Source", by: -1, among: p.reorderableSections)
        XCTAssertEqual(d.sections.map(\.title), ["Summary", "Experience", "Open Source", "Education", "Projects"], "a section that is not reorderable never moves; the first one cannot go up")
        let written = (try object(try d.editJSON())["sections"] as? [[String: Any]])?.compactMap { $0["title"] as? String }
        XCTAssertEqual(written, ["Summary", "Experience", "Education", "Projects"], "Open Source was never listed and is empty, so it is not written")
    }

    func testAddingAnEntryOnlyWhereThePoolSaysItMayGo() throws {
        var d = try draft(), p = try pool()
        XCTAssertNotNil(d.addEntry("job1", to: "Projects", pool: p), "a job goes only in the work history (and it is already there)")
        d.removeEntry("job1")
        XCTAssertNotNil(d.addEntry("job1", to: "Projects", pool: p))
        XCTAssertNotNil(d.addEntry("nope", to: "Projects", pool: p))
        XCTAssertNotNil(d.addEntry("projB", to: "Experience", pool: p))
        XCTAssertNotNil(d.addEntry("projB", to: "Summary", pool: p), "Summary takes bullets, not entries")
        XCTAssertNil(d.addEntry("projB", to: "Projects", pool: p))
        XCTAssertNotNil(d.addEntry("projB", to: "Open Source", pool: p), "an entry is on the page once")
    }

    // MARK: Rules

    private func targets(_ d: ResumeDraft, _ p: ResumePool?) -> [ResumeDraft.Issue.Target] { d.issues(pool: p).map(\.target) }

    func testAnUntouchedVersionHasNoIssues() throws {
        XCTAssertEqual(try draft().issues(pool: try pool()), [])
        XCTAssertEqual(try XCTUnwrap(ResumeDraft.parse(selection: try pool().base, pool: try pool())).issues(pool: try pool()), [])
    }

    func testABulletOnThePageTwiceIsMarkedEvenAcrossEntries() throws {
        var d = try draft()
        d.sections[2].entries.append(.init(id: "projB", bullets: [.init(id: "p1"), .init(id: "q1")]))
        let issues = d.issues(pool: try pool())
        XCTAssertTrue(issues.contains { $0.target == .bullet("p1") && $0.message.contains("twice") }, "\(issues)")
        XCTAssertTrue(issues.contains { $0.target == .bullet("p1") && $0.message.contains("does not offer") }, "p1 is not offered under projB either")
        d.sections[2].entries[1].bullets = [.init(id: "q1")]
        d.sections[2].entries.append(.init(id: "projB", bullets: [.init(id: "q1")]))
        XCTAssertTrue(d.issues(pool: nil).contains { $0.target == .entry("projB") && $0.message.contains("twice") }, "an entry once, with no pool too")
    }

    func testABulletNotOfferedUnderItsEntryAndAnEntryInTheWrongSectionAreMarked() throws {
        var d = try draft(), p = try pool()
        d.sections[1].entries[0].bullets.append(.init(id: "p2"))
        XCTAssertTrue(targets(d, p).contains(.bullet("p2")))
        d.sections[1].entries[0].bullets.removeLast()
        let moved = d.sections[1].entries.remove(at: 0)
        d.sections[2].entries.append(moved)
        let issues = d.issues(pool: p)
        XCTAssertTrue(issues.contains { $0.target == .entry("job1") && $0.message.contains("does not go in Projects") }, "\(issues)")
        d = try draft()
        d.sections[1].entries.append(.init(id: "ghost", bullets: [.init(id: "g1")]))
        XCTAssertTrue(targets(d, p).contains(.entry("ghost")))
        XCTAssertTrue(targets(d, p).contains(.bullet("g1")))
        d = try draft()
        d.sections[0].bullets.append(.init(id: "j4"))
        XCTAssertTrue(targets(d, p).contains(.bullet("j4")), "a summary bullet must come from the summary pool")
        d = try draft()
        d.sections[1].kind = .bullets
        d.sections[1].bullets = [.init(id: "s3")]
        XCTAssertTrue(targets(d, p).contains(.section("Experience")), "a section that takes entries is not a summary")
    }

    func testAContinuationThatIsNotDirectlyAfterItsLeadIsMarked() throws {
        var d = try draft(), p = try pool()
        d.sections[1].entries[0].bullets = [.init(id: "j2"), .init(id: "j1"), .init(id: "j3")]
        XCTAssertTrue(targets(d, p).contains(.bullet("j2")), "j2 comes first, before its lead")
        XCTAssertTrue(targets(d, p).contains(.bullet("j3")), "j3 follows j1, not j2")
        d.sections[1].entries[0].bullets = [.init(id: "j1"), .init(id: "j4"), .init(id: "j2")]
        XCTAssertTrue(targets(d, p).contains(.bullet("j2")), "something sits between")
        d.sections[1].entries[0].bullets = [.init(id: "j1"), .init(id: "j4", included: false), .init(id: "j2")]
        XCTAssertFalse(targets(d, p).contains(.bullet("j2")), "a row switched off is not on the page, so j2 is directly after j1")
        d.sections[1].entries[0].bullets = [.init(id: "j1", included: false), .init(id: "j2")]
        XCTAssertTrue(targets(d, p).contains(.bullet("j2")), "its lead is off")
        d.sections[0].bullets = [.init(id: "s2")]
        XCTAssertTrue(targets(d, p).contains(.bullet("s2")), "the summary's continuations too")
    }

    func testAnEntryWithNoBulletsMayStayOnlyAsAJobOrWhereTheBaseListsItBare() throws {
        var d = try draft(), p = try pool()
        d.sections[1].entries[0].bullets = []
        XCTAssertEqual(targets(d, p), [], "a job (org and dates) stays as a heading")
        XCTAssertEqual(targets(try draft(), p), [], "the base's bare entry (edu) stays")
        d.sections[2].entries[0].bullets = []
        XCTAssertEqual(targets(d, p), [.entry("projA")], "a project with nothing under it is refused")
        d.sections[2].entries[0].bullets = [.init(id: "p1", included: false)]
        XCTAssertEqual(targets(d, p), [.entry("projA")], "switching its only bullet off leaves it bare")
    }

    func testAnEmptyPageIsMarkedWhateverThePool() throws {
        var d = try draft()
        for i in d.sections.indices { d.sections[i].bullets = []; d.sections[i].entries = [] }
        XCTAssertEqual(targets(d, try pool()), [.page])
        XCTAssertEqual(targets(d, nil), [.page])
        XCTAssertEqual(targets(try draft(), nil), [], "with no pool nothing else is checked")
    }

    func testEveryOverrideRuleOfGoIsMarkedOnItsField() throws {
        func problem(_ f: ResumeOverrideField, _ v: String) -> String? { ResumeOverrideRules.problem(f, v) }
        for f in ResumeOverrideField.allCases { XCTAssertNil(problem(f, ""), "empty overrides nothing") }
        XCTAssertNil(problem(.lead, "-"), "a lead of exactly - suppresses the lead")
        XCTAssertNotNil(problem(.title, "-x\n"))
        XCTAssertNotNil(problem(.org, "   "), "whitespace only is refused, not read as empty")
        XCTAssertNotNil(problem(.org, " Acme"))
        XCTAssertNotNil(problem(.org, "Acme "))
        XCTAssertNotNil(problem(.org, "Acme\u{00A0}"), "a no-break space is padding")
        XCTAssertNotNil(problem(.title, "two\nlines"))
        XCTAssertNotNil(problem(.title, "tab\there"))
        XCTAssertNotNil(problem(.title, "bidi \u{202E}evil"), "a formatting character is invisible")
        XCTAssertNotNil(problem(.title, "a\u{00A0}b"), "a space that is not an ASCII space is not printable")
        XCTAssertNil(problem(.title, "CUDA 12 · 3M"), "names and versions hold digits")
        XCTAssertNil(problem(.title, "Ünïcode ☕"))
        // lengths count characters (runes), not bytes
        XCTAssertNil(problem(.title, String(repeating: "é", count: 120)))
        XCTAssertNotNil(problem(.title, String(repeating: "é", count: 121)))
        XCTAssertNil(problem(.stack, String(repeating: "a", count: 120)))
        XCTAssertNotNil(problem(.stack, String(repeating: "a", count: 121)))
        XCTAssertNil(problem(.lead, String(repeating: "a", count: 300)))
        XCTAssertNotNil(problem(.lead, String(repeating: "a", count: 301)))
        XCTAssertNil(problem(.dates, String(repeating: "1", count: 40)))
        XCTAssertNotNil(problem(.dates, String(repeating: "1", count: 41)))
        // the lead holds no figure; the others may
        XCTAssertNotNil(problem(.lead, "Cut cost by 3x"))
        XCTAssertNotNil(problem(.lead, "Second to none, ²"), "any number, not only an ASCII digit")
        XCTAssertNil(problem(.lead, "Cut cost sharply"))
        XCTAssertNil(problem(.org, "3M"))
        // dates: letters, digits, space and – - / , .
        XCTAssertNil(problem(.dates, "AUG 2023 – APR 2025"))
        XCTAssertNil(problem(.dates, "09/2023 - Present, 2.5 yrs"))
        XCTAssertNotNil(problem(.dates, "2023 (est)"))
        XCTAssertNotNil(problem(.dates, "2023; 2024"))
        // markup rkb would render
        for bad in ["{{claim}}", "a }} b", "[a](b)", "*bold*", "`code`", "<b>", "a > b"] {
            XCTAssertNotNil(problem(.title, bad), bad)
            XCTAssertNotNil(problem(.lead, bad), bad)
        }
        XCTAssertNil(problem(.title, "[a] (b)"), "a bracket and a paren apart are not a link")
    }

    func testAnOverrideProblemIsMarkedOnTheEntryAndFieldItIsAbout() throws {
        var d = try draft(), p = try pool()
        d.setOverride(.lead, "Doubled it 2x", entry: "projA")
        d.setOverride(.dates, "2023!", entry: "job1")
        let issues = d.issues(pool: p)
        XCTAssertEqual(Set(issues.map(\.target)), [.override(entry: "projA", field: .lead), .override(entry: "job1", field: .dates)])
        XCTAssertTrue(issues.allSatisfy { !$0.message.isEmpty })
        d.setOverride(.lead, "", entry: "projA")
        d.setOverride(.dates, "2023", entry: "job1")
        XCTAssertEqual(d.issues(pool: p), [])
        p = try pool()
    }

    func testOnlyBulletsOnThePageAreCheckedAndASwitchedOffRowIsNot() throws {
        var d = try draft(), p = try pool()
        d.sections[1].entries[0].bullets.append(.init(id: "zzz", included: false))
        XCTAssertEqual(targets(d, p), [], "a row that is switched off is not on the page")
        d.sections[1].entries[0].bullets[3].included = true
        XCTAssertEqual(targets(d, p), [.bullet("zzz")])
    }

    // MARK: Words

    func testKeywordsAreHighlightedCaseInsensitivelyEachOnce() {
        XCTAssertEqual(ResumeKeywordHits.hits(in: "Wrote CUDA kernels for kubernetes, then more cuda.", terms: ["cuda", "Kubernetes", "Rust", "", "CUDA"]),
                       ["cuda", "Kubernetes"])
        XCTAssertEqual(ResumeKeywordHits.hits(in: "", terms: ["a"]), [])
        XCTAssertEqual(ResumeKeywordHits.hits(in: "Résumé", terms: ["resume"]), ["resume"])
    }

    func testTheSaveAsNameIsVanisOwnRule() {
        for (company, want) in [
            ("Acme Robotics", "Resume_Acme_Robotics.pdf"),
            ("  ", "Resume.pdf"),
            ("", "Resume.pdf"),
            ("Ünïcode & Co., Inc.", "Resume_Ünïcode_Co_Inc.pdf"),
            ("Acme\u{0}\n<script>", "Resume_Acme_script.pdf"),
            ("../../etc/passwd", "Resume_etc_passwd.pdf"),
            (String(repeating: "a", count: 500), "Resume_" + String(repeating: "a", count: 60) + ".pdf"),
            (String(repeating: "a", count: 59) + " b", "Resume_" + String(repeating: "a", count: 59) + ".pdf"),
        ] {
            XCTAssertEqual(ResumeWords.resumeFileName(company: company), want, company)
        }
    }

    func testTheCostChipIsALowerBoundWhenIncompleteAndNothingWhenUnknown() {
        XCTAssertEqual(ResumeWords.costChip(ResumeCost(usd: 0.031, complete: true, turns: 2)), "$0.031")
        XCTAssertEqual(ResumeWords.costChip(ResumeCost(usd: 0.031, complete: false, turns: 2)), "at least $0.031")
        XCTAssertNil(ResumeWords.costChip(ResumeCost(usd: nil, complete: false, turns: 2)), "unknown is never $0")
        XCTAssertNil(ResumeWords.costChip(nil))
    }
}
