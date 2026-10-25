import Foundation
import XCTest
@testable import VaniApplyKit

/// The pure parts of the Studio's redraft form, versions list, comparison, coverage and quit prompt, and the
/// words of the new codes. Every id, title and sentence is made up.

private func decode<T: Decodable>(_ type: T.Type, _ json: String) throws -> T {
    try JobsClient.decoder.decode(T.self, from: Data(json.utf8))
}

private let checks = #"{"eligibility":"current","lint":"passed","fit":"passed","figures":"verified"}"#

private func makeVersion(_ n: Int, parent: Int = 0, source: String = "tailored", instruction: String? = nil, scope: String? = nil,
                     lock: [String] = [], current: Bool = false, used: Bool = false, notes: [String] = [],
                     bullets: [String] = []) throws -> ResumeVersion {
    var obj: [String: Any] = ["schema": 2, "key": "k", "version": n, "parent": parent, "source": source,
                              "checks": try JSONSerialization.jsonObject(with: Data(checks.utf8)), "created": "2026-10-25T1\(n % 10):00:00Z",
                              "pages": 2, "current": current, "used": used, "notes": notes,
                              "bullets": bullets.map { ["id": $0, "text_hash": "aaaaaaaaaaaa"] }]
    if let instruction { obj["instruction"] = instruction }
    if let scope { obj["scope"] = scope }
    if !lock.isEmpty { obj["lock"] = lock }
    return try JobsClient.decoder.decode(ResumeVersion.self, from: try JSONSerialization.data(withJSONObject: obj))
}

private let poolJSON = """
{"summary":[{"id":"s1","text":"Builds data pipelines.","text_hash":"aaaaaaaaaaaa"}],"summary_sections":["Summary"],
 "reorderable_sections":["Projects","Open Source"],"headings":[{"section":"Experience","entry":"job1"}],
 "entries":[
  {"id":"job1","title":"ML Engineer","org":"Acme","dates":"2022","sections":["Experience"],
   "bullets":[{"id":"j1","text":"Trained models.","text_hash":"111111111111"},{"id":"j2","text":"Used CUDA kernels.","text_hash":"222222222222"}]},
  {"id":"projA","title":"Project A","sections":["Projects","Open Source"],
   "bullets":[{"id":"p1","text":"Built a compiler.","text_hash":"333333333333"}]},
  {"id":"edu","title":"BS","sections":["Education"],"bullets":[{"id":"e1","text":"Graduated.","text_hash":"444444444444"}]}],
 "base":{"sections":[{"title":"Summary","bullets":["s1"]}]}}
"""

private func pool() throws -> ResumePool { try XCTUnwrap(ResumePool(try decode(ResumeJSON.self, poolJSON))) }

final class ResumeVersionsTests: XCTestCase {
    // MARK: Instruction of a version

    func testADerivedVersionShowsItsParentsInstructionNamedAndAModelShapedOneShowsItsOwnOrNone() throws {
        let v1 = try makeVersion(1, instruction: "lead with the serving work")
        let v2 = try makeVersion(2, parent: 1, source: "edited")
        let v3 = try makeVersion(3, parent: 2, source: "revalidated")
        let v4 = try makeVersion(4, parent: 3, source: "tailored")           // asked with none: shows none
        let v5 = try makeVersion(5, parent: 4, source: "edited")             // its parent was asked with none
        let v6 = try makeVersion(6, parent: 1, source: "tailored", instruction: "prefer Go")
        let all = [v1, v2, v3, v4, v5, v6]
        XCTAssertEqual(ResumeVersions.instruction(of: v1, in: all), ResumeInstructionLine(text: "lead with the serving work", from: nil))
        XCTAssertEqual(ResumeVersions.instruction(of: v2, in: all)?.display, "from v1: lead with the serving work")
        XCTAssertEqual(ResumeVersions.instruction(of: v3, in: all)?.display, "from v1: lead with the serving work", "through a chain of derived versions, naming the one that was asked")
        XCTAssertNil(ResumeVersions.instruction(of: v4, in: all), "a tailored version asked with none shows none, never its parent's")
        XCTAssertNil(ResumeVersions.instruction(of: v5, in: all), "an edit of a version that was asked with none has nothing to show")
        XCTAssertEqual(ResumeVersions.instruction(of: v6, in: all)?.display, "prefer Go")
        let base = try makeVersion(7, source: "base")
        XCTAssertNil(ResumeVersions.instruction(of: base, in: all + [base]))
    }

    func testTheInstructionWalkIsBoundedAndNeverLoopsOnAParentThatIsNotOlder() throws {
        let a = try makeVersion(2, parent: 3, source: "edited")   // a parent that is newer than the child
        let b = try makeVersion(3, parent: 2, source: "edited")
        XCTAssertNil(ResumeVersions.instruction(of: a, in: [a, b]))
        XCTAssertNil(ResumeVersions.instruction(of: b, in: [a, b]))
        let lone = try makeVersion(2, parent: 9, source: "edited") // a parent that is not in the list
        XCTAssertNil(ResumeVersions.instruction(of: lone, in: [lone]))
    }

    func testTheVersionsListShowsEveryVersionNewestFirstWithBadgesKeptPartsAndInstruction() throws {
        let v1 = try makeVersion(1, instruction: "prefer tracing")
        let v2 = try makeVersion(2, parent: 1, source: "edited", used: true)
        let v3 = try makeVersion(3, parent: 1, scope: "GPU Systems", lock: ["Summary", "Experience"], current: true)
        let rows = ResumeVersions.rows([v1, v2, v3])
        XCTAssertEqual(rows.map(\.version), [3, 2, 1])
        XCTAssertEqual(rows[0].badges, ["current"])
        XCTAssertEqual(rows[1].badges, ["in use"])
        XCTAssertEqual(rows[2].badges, [])
        XCTAssertEqual(rows[0].kept, "re-picked only GPU Systems · kept Summary, Experience")
        XCTAssertNil(rows[1].kept)
        XCTAssertEqual(rows[0].parent, 1)
        XCTAssertEqual(rows[1].instruction?.display, "from v1: prefer tracing")
        XCTAssertNil(rows[0].instruction, "v3 was asked with no instruction")
        XCTAssertEqual(rows[0].title, "v3 · tailored · 2 pages")
        XCTAssertEqual(ResumeVersions.comparable(to: 2, in: [v1, v2, v3]), [3, 1])
        XCTAssertEqual(ResumeVersions.keptLine(scope: nil, lock: ["A"]), "kept A")
        XCTAssertNil(ResumeVersions.keptLine(scope: nil, lock: []))
    }

    // MARK: Comparison

    func testAComparisonShowsBulletTextFromThePoolAndIdsWhereThePoolHasNone() throws {
        let d = try decode(ResumeDiff.self, """
        {"key":"k","from":{"version":2,"source":"edited","parent":1,"created":"2026-10-25T10:00:00Z"},
         "to":{"version":5,"source":"tailored","parent":2,"instruction":"prefer tracing","scope":"Projects","created":"2026-10-25T12:00:00Z"},
         "same":false,
         "bullets":{"added":[{"id":"p1","section":"Projects","entry":"projA","index":0}],
                    "removed":[{"id":"gone9","section":"Experience","entry":"job1","index":1}],
                    "moved":[{"id":"j2","from":{"section":"Experience","entry":"job1","index":1},"to":{"section":"Experience","entry":"job1","index":0}}]},
         "overrides":[{"entry":"job1","field":"title","from":"","to":"Principal"},{"entry":"projA","field":"lead","from":"old","to":""}],
         "section_order":{"from":["Summary","Projects"],"to":["Projects","Summary"]},
         "keywords":{"added":["tracing"],"removed":[]}}
        """)
        let v = ResumeDiffPresentation.make(d, pool: try pool(), versions: [try makeVersion(2, source: "edited"), try makeVersion(5, instruction: "prefer tracing", scope: "Projects")])
        XCTAssertEqual(v.title, "v2 → v5")
        XCTAssertEqual(v.added, [ResumeDiffBullet(id: "p1", text: "Built a compiler.", place: "Projects · Project A · #1")])
        XCTAssertEqual(v.removed, [ResumeDiffBullet(id: "gone9", text: nil, place: "Experience · ML Engineer · #2")], "an id the pool does not offer is shown as its id")
        XCTAssertEqual(v.moved.first?.text, "Used CUDA kernels.")
        XCTAssertEqual(v.moved.first?.change, "Experience · ML Engineer · #2 → Experience · ML Engineer · #1")
        XCTAssertEqual(v.overrides, ["ML Engineer · Title: (no override) → Principal", "Project A · Lead line: old → (no override)"])
        XCTAssertEqual(v.sectionOrder, "Summary, Projects → Projects, Summary")
        XCTAssertEqual(v.keywordsAdded, ["tracing"])
        XCTAssertTrue(v.headline.hasPrefix("v2 to v5: 1 added, 1 removed, 1 moved, 2 overrides changed, 1 section order changed, 1 keyword changed"), v.headline)
        XCTAssertTrue(v.toLine.contains("re-picked only Projects") && v.toLine.contains("asked: prefer tracing"), v.toLine)
        XCTAssertEqual(v.fromLine, "v2 · edited by hand")
        // Without a pool the ids stand in, and the place falls back to the entry id.
        let bare = ResumeDiffPresentation.make(d, pool: nil, versions: [])
        XCTAssertNil(bare.added.first?.text)
        XCTAssertEqual(bare.added.first?.place, "Projects · projA · #1")
    }

    func testASameComparisonSaysSoAndSaysTheVersionsCanStillBeMadeDifferently() throws {
        let d = try decode(ResumeDiff.self, """
        {"key":"k","from":{"version":1,"source":"tailored","created":"2026-10-25T10:00:00Z"},"to":{"version":2,"source":"revalidated","parent":1,"created":"2026-10-25T10:00:00Z"},
         "same":true,"bullets":{"added":[],"removed":[],"moved":[]},"overrides":[],"keywords":{"added":[],"removed":[]}}
        """)
        let v = ResumeDiffPresentation.make(d, pool: nil, versions: [])
        XCTAssertTrue(v.same)
        XCTAssertTrue(v.headline.hasPrefix("No difference in bullets, overrides, section order or keywords."), v.headline)
        XCTAssertTrue(v.headline.contains("made differently"))
        XCTAssertTrue(d.listsAreEmpty)
    }

    // MARK: Coverage

    func testCoverageWordsKeepWhatASwapCanFixApartFromWhatTheBankLacks() throws {
        let c = try decode(ResumeCoverage.self, """
        {"key":"k","version":3,"keywords_source":"posting","hit":[{"term":"CUDA","bullets":["j2","gone7"],"n":4}],
         "miss":["Rust","Triton"],"uncovered":["triton"],"unjudged":["x y z a b c"],"unresolved":["b9"],"terms_total":205,"terms_truncated":true,"gap_missing":["Triton"]}
        """)
        let v = ResumeCoveragePresentation.make(c, pool: try pool(), dirty: true)
        XCTAssertEqual(v.headline, "v3 covers 1 of 3 judged keywords.")
        XCTAssertEqual(v.scopeLine, "Measured on v3 as saved. Your unsaved edits are not counted.")
        XCTAssertEqual(v.swappable, ["Rust"], "uncovered is matched without regard to case")
        XCTAssertEqual(v.uncovered, ["triton"])
        XCTAssertEqual(v.unjudged, ["x y z a b c"])
        XCTAssertEqual(v.unresolved, ["b9"])
        XCTAssertEqual(v.hits.first?.count, 4)
        XCTAssertEqual(v.hits.first?.bullets, [.init(id: "j2", text: "Used CUDA kernels."), .init(id: "gone7", text: nil)], "the wording comes from the pool, an id it lacks stays an id")
        XCTAssertTrue(v.notes.contains { $0.contains("205 terms were asked about and only the first 200 were judged") }, "\(v.notes)")
        XCTAssertTrue(v.notes.contains { $0.contains("1 phrase could not be matched at all. They are not counted as misses") }, "\(v.notes)")
        XCTAssertEqual(v.gapMissing, ["Triton"])
        XCTAssertEqual(v.sourceLine, "Judged on the posting's keywords.")
        let clean = ResumeCoveragePresentation.make(c, pool: nil, dirty: false)
        XCTAssertEqual(clean.scopeLine, "Measured on v3 as saved.")
    }

    func testCoverageWithNothingJudgedIsNotAHundredPercentAndTheBaseIsSaidToBeTheBase() throws {
        let c = try decode(ResumeCoverage.self, """
        {"key":"k","base":true,"keywords_source":"none","keywords_error":"the posting's keywords could not be read: x","hit":[],"miss":[],"uncovered":[],"unjudged":[],"unresolved":[],"terms_total":0}
        """)
        let v = ResumeCoveragePresentation.make(c, pool: nil, dirty: false)
        XCTAssertEqual(v.headline, "No keyword could be judged, so there is no coverage to report.")
        XCTAssertEqual(v.scopeLine, "Measured on your base selection: this job has no saved version yet.")
        XCTAssertEqual(v.sourceLine, "No keywords were available to judge.")
        XCTAssertTrue(v.notes.first?.contains("could not be read") ?? false, "a read that failed is a note, not a silence")
        XCTAssertTrue(ResumeCoveragePresentation.sourceLine("posting+jd_keywords").contains("and the ones Opus took"))
        XCTAssertTrue(ResumeCoveragePresentation.sourceLine("future").contains("does not recognise"))
    }

    // MARK: Orphan last lines

    func testOrphanIdsAreReadBackFromGosFitNoteAndOnlyWhenOnThePage() throws {
        // The note vani writes: rkb's advice, then fitIDs' ids in parentheses (internal/rkbresume/build.go).
        let note = "OK: 2 page(s). STRICT: 2 orphan last line(s) (orphan b1, orphan b4, long b2)"
        let bullets = ["b1", "b2", "b3"].map { ResumeBulletRef(id: $0, textHash: "aaaaaaaaaaaa") }
        XCTAssertEqual(ResumeOrphans.ids(inNotes: [note], bullets: bullets), ["b1"], "b4 is not on the page; b2 is long, not an orphan; \"last\" is not a bullet")
        XCTAssertEqual(ResumeOrphans.ids(inNotes: ["tight (orphan b3)", note], bullets: bullets), ["b1", "b3"])
        XCTAssertEqual(ResumeOrphans.ids(inNotes: ["no orphans here", "orphan"], bullets: bullets), [])
        XCTAssertEqual(ResumeOrphans.ids(inNotes: [], bullets: bullets), [])
        XCTAssertEqual(ResumeOrphans.words, "ends with a one-word line")
        // A bullet literally named "last" is the one id the advice text could name by accident: it must be on the page to count.
        XCTAssertEqual(ResumeOrphans.ids(inNotes: [note], bullets: [.init(id: "last", textHash: "aaaaaaaaaaaa")]), ["last"],
                       "the one false positive this parse can make (rkb's own sentence \"orphan last line(s)\"); recorded in the README")
    }

    // MARK: Quit

    func testQuittingAsksOnceNamingTheJobsAndNotAtAllWhenNothingIsUnsaved() {
        XCTAssertNil(ResumeQuit.prompt(jobs: []))
        let one = ResumeQuit.prompt(jobs: ["Alpha Labs, Platform Engineer"])
        XCTAssertEqual(one?.title, "Quit and discard your unsaved resume edits?")
        XCTAssertTrue(one?.message.contains("Alpha Labs, Platform Engineer") ?? false)
        XCTAssertEqual(one?.quit, "Quit and Discard")
        XCTAssertEqual(one?.stay, "Keep Editing")
        let many = ResumeQuit.prompt(jobs: (1...10).map { "Job \($0)" })
        XCTAssertEqual(many?.title, "Quit and discard unsaved resume edits in 10 windows?")
        XCTAssertTrue(many?.message.contains("Job 8") ?? false)
        XCTAssertFalse(many?.message.contains("Job 9") ?? true, "the list is capped")
        XCTAssertTrue(many?.message.contains("and 2 more") ?? false)
    }
}

final class ResumeRedraftFormTests: XCTestCase {
    private func targets() throws -> ResumeRedraftTargets {
        let selection = try decode(ResumeJSON.self, """
        {"sections":[{"title":"Summary","bullets":["s1"]},
                     {"title":"Experience","entries":[{"entry":"job1","bullets":["j1"]}]},
                     {"title":"Projects","entries":[{"entry":"projA","bullets":["p1"],"title":"Compiler"},{"entry":"projB","bullets":["q1"]}]},
                     {"title":"Education","entries":[{"entry":"edu","bullets":[]}]}]}
        """)
        return try XCTUnwrap(ResumeRedraftTargets.of(selection: selection, pool: try pool()))
    }

    func testTheTargetsAreTheSectionsTheModelFillsAndTheirEntries() throws {
        let t = try targets()
        XCTAssertEqual(t.sections.map(\.title), ["Summary", "Experience", "Projects", "Education"], "in the version's own order, only what the pool can fill")
        XCTAssertEqual(t.sections.first { $0.title == "Projects" }?.entries.map(\.title), ["Compiler", "projB"], "an override's title, else the pool's, else the id")
        XCTAssertEqual(t.section(containingEntry: "projA"), "Projects")
        XCTAssertNil(t.section(containingEntry: "nowhere"))
        XCTAssertNil(ResumeRedraftTargets.of(selection: nil, pool: try pool()))
        XCTAssertNil(ResumeRedraftTargets.of(selection: try decode(ResumeJSON.self, #"{"sections":[]}"#), pool: try pool()), "nothing to redraft")
        XCTAssertNil(ResumeRedraftTargets.of(selection: try decode(ResumeJSON.self, #"{"sections":[{"title":"Summary","bullets":[]}]}"#), pool: nil),
                     "without a pool what the model fills is not known")
        XCTAssertNil(ResumeRedraftTargets.of(selection: try decode(ResumeJSON.self, #"{"sections":[{"title":"Skills","lines":[]}]}"#), pool: try pool()),
                     "a section the pool cannot fill is not offered")
    }

    func testTheFormBuildsTheRequestInThePagesOrderAndDropsALockOnTheScopedSection() throws {
        let t = try targets()
        var f = ResumeRedraftForm()
        f.locked = ["Projects", "Summary", "Experience"]
        f.instruction = "  prefer tracing  "
        var r = f.request(from: 3, targets: t)
        XCTAssertEqual(r, ResumeRedraft(from: 3, scope: nil, lock: ["Summary", "Experience", "Projects"], instruction: "  prefer tracing  "))
        XCTAssertEqual(r.arguments.last, "--instruction=prefer tracing", "the flag carries the trimmed text")
        f.scope = .entry("projA")
        XCTAssertEqual(f.scopedSection(t), "Projects")
        XCTAssertFalse(f.isLockable("Projects", t))
        XCTAssertTrue(f.isLockable("Summary", t))
        r = f.request(from: 3, targets: t)
        XCTAssertEqual(r.scope, "projA")
        XCTAssertEqual(r.lock, ["Summary", "Experience"], "the section being re-picked cannot also be kept")
        f.scope = .section("Experience")
        XCTAssertEqual(f.request(from: 3, targets: t).scope, "Experience")
        XCTAssertEqual(f.request(from: 3, targets: t).lock, ["Summary", "Projects"])
        XCTAssertEqual(f.request(from: 3, targets: t).lock.count, 2)
    }

    func testTheFormStopsOnlyWhatGoCertainlyRefuses() throws {
        let t = try targets()
        XCTAssertEqual(ResumeRedraftForm().problems(t), [], "the whole page, no locks, no instruction is a valid redraft")
        var f = ResumeRedraftForm(instruction: String(repeating: "x", count: 501))
        XCTAssertEqual(f.problems(t), ["The instruction is 501 characters, over 500."])
        f.instruction = String(repeating: "x", count: 500)
        XCTAssertEqual(f.problems(t), [])
        f.instruction = "call 555-123-4567"
        XCTAssertEqual(f.problems(t), [], "the phone screen is Go's: its refusal is shown when it comes")
        f.instruction = ""
        f.locked = Set(t.sections.map(\.title))
        XCTAssertEqual(f.problems(t), ["Every section is locked, so there is nothing to redraft."])
        f.scope = .section("Projects")
        XCTAssertEqual(f.problems(t), [], "a scope leaves its own section open")
        f.scope = .section("Nowhere")
        XCTAssertEqual(f.problems(t), ["The section to re-pick is not on this version."])
        f.scope = .entry("nowhere")
        XCTAssertEqual(f.problems(t), ["The entry to re-pick is not on this version."])
    }

    func testTheCounterCountsTrimmedUnicodeScalarsLikeGo() {
        XCTAssertEqual(ResumeRedraftRules.instructionCount("  héllo  "), 5)
        XCTAssertEqual(ResumeRedraftRules.instructionCount("👩‍💻"), 3, "scalars, as Go's runes: one emoji sequence is three")
        XCTAssertEqual(ResumeRedraftRules.counter(String(repeating: "é", count: 62)), "62 / 500")
        XCTAssertNil(ResumeRedraftRules.instructionProblem("   "), "whitespace only is no instruction")
        XCTAssertNotNil(ResumeRedraftRules.instructionProblem("a\nb"))
        XCTAssertNotNil(ResumeRedraftRules.instructionProblem("a\u{200B}b"), "an invisible character is refused")
        XCTAssertNil(ResumeRedraftRules.instructionProblem("a trailing newline is trimmed\n"))
    }
}

final class ResumeRedraftWordsTests: XCTestCase {
    private func dry(_ extra: String = "", usable: Bool = true, current: Int = 3) throws -> ResumeDryRun {
        try decode(ResumeDryRun.self, """
        {"key":"k","dry_run":true,"model":"claude-opus-5-5","opus_usable":\(usable),"current":\(current),\(usable ? "" : #""no_model":"no key","#)\(extra)
         "budget":{"daily_cap_usd":20,"remaining_usd":18},"estimate":{"turns":2,"input_tokens":18400,"max_output_tokens":24000,"max_usd":0.42,"cap_reached":false}}
        """)
    }

    func testARedraftConfirmationSaysWhatIsRepickedKeptAndSentAndNeverTheInstructionText() throws {
        let d = try dry(#""from":3,"scope":"GPU Systems","lock":["Summary","Experience"],"instruction_runes":24,"#)
        let c = ResumeWords.confirmation(for: d, redraft: ResumeRedraft(from: 3, scope: "GPU Systems", lock: ["Summary", "Experience"], instruction: "never shown here, only counted"),
                                         locale: Locale(identifier: "en_US"))
        XCTAssertEqual(c.redraftText, "Redraft of v3: re-picks only GPU Systems, keeps Summary, Experience, and sends your 24-character instruction with the posting (it is never printed on the page).")
        XCTAssertFalse(c.redraftText?.contains("never shown here") ?? true)
        XCTAssertEqual(c.madeFromText, "A new version is made from v3, the current one, and replaces it as the current version.")
        XCTAssertTrue(c.canConfirm)
        XCTAssertFalse(c.offersBase)
        let whole = ResumeWords.confirmation(for: try dry(#""from":3,"#), redraft: ResumeRedraft(from: 3))
        XCTAssertEqual(whole.redraftText, "Redraft of v3: re-picks the whole page.")
        // From an older version than the current one: it does not displace it, and says so.
        let older = ResumeWords.confirmation(for: try dry(#""from":2,"#), redraft: ResumeRedraft(from: 2))
        XCTAssertEqual(older.madeFromText, "A new version is made from v2. It does not replace the current version, v3; make it current afterwards if you want it.")
        XCTAssertNil(ResumeWords.confirmation(for: try dry()).redraftText, "a plain build says nothing of a redraft")
        XCTAssertEqual(ResumeWords.confirmation(for: try dry()).madeFromText, "A new version is made from v3, the current one, and replaces it as the current version.")
    }

    func testARedraftThatCannotRunOpusOffersNoBaseSelectionAndAPlainBuildStillDoes() throws {
        let redraft = ResumeWords.confirmation(for: try dry(#""from":3,"#, usable: false), redraft: ResumeRedraft(from: 3))
        XCTAssertFalse(redraft.canConfirm)
        XCTAssertFalse(redraft.offersBase, "the base selection is not a redraft and would not keep what was kept")
        XCTAssertNil(redraft.baseNote)
        XCTAssertTrue(redraft.blockedReason?.contains("no redraft can be made") ?? false, redraft.blockedReason ?? "")
        let plain = ResumeWords.confirmation(for: try dry(usable: false))
        XCTAssertTrue(plain.offersBase)
        XCTAssertTrue(plain.blockedReason?.contains("a tailored resume cannot be built") ?? false)
    }

    func testTheFourNewCodesHaveWordsThatSayWhatWasSpentAndWhatToDo() throws {
        func text(_ code: String, _ message: String = "the rule named", cost: ResumeCost? = nil, recorded: Bool = false) throws -> ResumeFailureText {
            try XCTUnwrap(ResumeWords.failure(ResumeFailure(code: code, message: message, cost: cost, attemptRecorded: recorded)))
        }
        let bi = try text("bad_instruction", "the instruction looks like it holds a phone number; leave contact details out")
        XCTAssertTrue(bi.message.contains("phone number"), "the server's refusal is shown as it said it")
        XCTAssertTrue(bi.message.contains("leave contact details out. Nothing was spent"), "a full stop between Go's words and ours: \(bi.message)")
        XCTAssertTrue(try text("bad_scope", "already ends well.").message.contains("already ends well. Nothing was spent"), "no doubled stop")
        XCTAssertTrue(try text("bad_scope", "the scope is in a locked section").message.contains("Nothing was spent"))
        let scope = try text("scope", "the reply changed Summary", cost: ResumeCost(usd: 0.2, complete: true, turns: 2), recorded: true)
        XCTAssertTrue(scope.message.contains("changed a part you asked to keep"))
        XCTAssertTrue(scope.message.contains("nothing was saved"))
        XCTAssertEqual(scope.costNote, "Opus 5.5 was asked and charged $0.200, but nothing was saved; Vani recorded the attempt.", "a paid refusal says it cost money")
        let redraft = try text("redraft", "no valid page")
        XCTAssertTrue(redraft.message.contains("base selection is not built in its place"))
        for code in ["bad_instruction", "bad_scope", "scope", "redraft"] {
            XCTAssertTrue(ResumeWords.knownCodes.contains(code), code)
            XCTAssertFalse(try text(code).message.hasPrefix("The resume step failed"), code)
        }
    }

    func testAStaleRefusalNamesTheFigureThatNoLongerVerifies() throws {
        let e = ResumeEligibility(state: "stale", unverified: ["b7"])
        let f = try XCTUnwrap(ResumeWords.failure(ResumeFailure(code: "stale", message: "m", eligibility: e)))
        XCTAssertTrue(f.message.contains("1 with a figure no longer verified"), f.message)
    }
}

/// The codes Go can write, read from its sources in place: a code added there without words here fails this.
final class ResumeCodesFromGoTests: XCTestCase {
    private func repoFile(_ path: String) -> URL {
        URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent(path)
    }

    private func codes(in files: [URL], pattern: String) throws -> Set<String> {
        let re = try NSRegularExpression(pattern: pattern)
        var out: Set<String> = []
        for f in files {
            let text = try String(contentsOf: f, encoding: .utf8)
            for m in re.matches(in: text, range: NSRange(text.startIndex..., in: text)) {
                if let r = Range(m.range(at: 1), in: text) { out.insert(String(text[r])) }
            }
        }
        return out
    }

    private func goFiles(_ dir: String, prefix: String? = nil) throws -> [URL] {
        let url = repoFile(dir)
        let names = try FileManager.default.contentsOfDirectory(atPath: url.path)
        return names.filter { $0.hasSuffix(".go") && !$0.hasSuffix("_test.go") && (prefix.map($0.hasPrefix) ?? true) }.sorted()
            .map { url.appendingPathComponent($0) }
    }

    /// Every code of `internal/rkbresume` (`CodeXxx = "code"`) and of `cmd/vani/jobsresume*.go` (`jrXxx = "code"`).
    func goCodes() throws -> Set<String> {
        let rk = try codes(in: try goFiles("internal/rkbresume"), pattern: #"\bCode[A-Za-z]*\s*=\s*"([a-z_]+)""#)
        let cmd = try codes(in: try goFiles("cmd/vani", prefix: "jobsresume"), pattern: #"\bjr[A-Z][A-Za-z]*\s*=\s*"([a-z_]+)""#)
        return rk.union(cmd)
    }

    /// What this app adds for what it could not ask, or what vani's draft path says: not in the resume sources.
    private let appOwn: Set<String> = ["account", "keychain", "settings", "provider", "budget", "budget_exhausted", "cap_reached", "no_reply", "unreadable_reply", "bind"]

    func testEveryCodeGoCanWriteIsKnownAndWorded() throws {
        let go = try goCodes()
        XCTAssertGreaterThan(go.count, 30, "the scan found too few codes: the sources moved? \(go.sorted())")
        for expected in ["bad_instruction", "bad_scope", "scope", "redraft", "busy", "kb_changed", "no_model", "ineligible", "bad_edit", "stale", "unchecked", "not_found"] {
            XCTAssertTrue(go.contains(expected), "the scan did not see \(expected)")
        }
        for code in go.sorted() {
            XCTAssertTrue(ResumeWords.knownCodes.contains(code), "Go writes the code \"\(code)\" and ResumeWords.knownCodes does not list it, so it has no words")
            if code == "cancelled" { continue }
            let text = ResumeWords.failure(ResumeFailure(code: code, message: "the detail"))
            XCTAssertFalse(text?.message.hasPrefix("The resume step failed") ?? true, "\"\(code)\" falls through to the default words")
        }
    }

    func testEveryKnownCodeIsOneGoWritesOrOneTheAppAddsSoNoWordsGoStale() throws {
        let go = try goCodes()
        for code in ResumeWords.knownCodes {
            XCTAssertTrue(go.contains(code) || appOwn.contains(code), "\"\(code)\" is worded but neither Go nor the app writes it")
        }
    }
}
