import Foundation
import XCTest
@testable import VaniApplyKit

/// A fake `vani` that records its arguments and prints a file's bytes, so a
/// reply of any size reaches the client untouched by shell quoting.
private func fakeGapsVani(_ t: XCTestCase, reply: Data, status: Int32 = 0) throws -> (vani: URL, argv: URL) {
    let dir = try tempDir(t)
    let body = dir.appendingPathComponent("reply.json")
    let argv = dir.appendingPathComponent("argv.txt")
    let vani = dir.appendingPathComponent("vani")
    try reply.write(to: body)
    let script = "#!/bin/sh\nprintf '%s\\n' \"$@\" > '\(argv.path)'\ncat '\(body.path)'\nexit \(status)\n"
    try Data(script.utf8).write(to: vani)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: vani.path)
    return (vani, argv)
}

/// The shape cmd/vani/jobsgaps.go writes: the report flat beside the view.
private let gapsReply = """
{"view":"new","query":"infra","roles":12,"with_missing":4,"target_known":true,"total_gaps":3,\
"gaps":[\
{"term":"Kubernetes","cat":"infra","roles":3,"missing":2,"weight":151.5,"missing_weight":98.2,"companies":2,\
"examples":[{"key":"gh:acme:1","company":"Acme","title":"Platform Engineer"},{"key":"gh:beta:7","company":"Beta","title":"SRE"}]},\
{"term":"CUDA","cat":"lang","roles":1,"missing":1,"weight":40.0,"missing_weight":40.0,"companies":1,\
"examples":[{"key":"gh:acme:9","company":"Acme","title":"Kernel Engineer"}]},\
{"term":"Terraform","cat":"infra","roles":5,"missing":0,"weight":120.3,"missing_weight":0,"companies":4,"examples":[]}],\
"insight":{"target":true,"target_terms":9,"half_life_days":14,"vocab":198,"boilerplate":2},\
"ledger":"/state/ledger","records":283}
"""

final class JobsGapsTests: XCTestCase {
    // MARK: Decoding

    func testDecodesARealReply() throws {
        let g = try JobsGaps.decode(Data(gapsReply.utf8))
        XCTAssertEqual(g.view, "new")
        XCTAssertEqual(g.query, "infra")
        XCTAssertEqual(g.roles, 12)
        XCTAssertEqual(g.withMissing, 4)
        XCTAssertTrue(g.targetKnown)
        XCTAssertEqual(g.totalGaps, 3)
        XCTAssertEqual(g.gaps.map(\.term), ["Kubernetes", "CUDA", "Terraform"], "Vani's order is kept")
        let k = g.gaps[0]
        XCTAssertEqual(k.category, "infra")
        XCTAssertEqual(k.missing, 2)
        XCTAssertEqual(k.weight, 151.5)
        XCTAssertEqual(k.missingWeight, 98.2)
        XCTAssertEqual(k.companies, 2)
        XCTAssertEqual(k.examples.map(\.key), ["gh:acme:1", "gh:beta:7"])
        XCTAssertEqual(k.examples[0].company, "Acme")
        XCTAssertEqual(g.insight?.targetTerms, 9)
        XCTAssertEqual(g.records, 283)
        XCTAssertEqual(g.status, .listed)
        XCTAssertNil(g.statusText)
        XCTAssertNil(g.targetNote)
        XCTAssertNil(g.shownNote)
    }

    func testLabelsSayWhyEachSkillIsAGap() throws {
        let g = try JobsGaps.decode(Data(gapsReply.utf8))
        XCTAssertEqual(g.gaps[0].basisLabel, "Found missing by DevPrism in 2 of 3 roles")
        XCTAssertEqual(g.gaps[1].basisLabel, "Found missing by DevPrism in 1 role")
        XCTAssertEqual(g.gaps[2].basisLabel, "Not named by your target summary")
        XCTAssertEqual(g.gaps[0].reachLabel, "3 roles · 2 companies")
        XCTAssertEqual(g.gaps[1].reachLabel, "1 role · 1 company")
        XCTAssertTrue(g.gaps[0].weightHelp.contains("98.2"), g.gaps[0].weightHelp)
        XCTAssertFalse(g.gaps[2].weightHelp.contains("DevPrism"), "no DevPrism claim for a keyword-only gap")
        XCTAssertEqual(g.summaryLine, "12 roles, 4 with a DevPrism gap analysis")
    }

    func testAnEmptyGapListWithAKnownTargetSaysNoGapsFound() throws {
        let g = try JobsGaps.decode(Data(#"{"view":"new","roles":6,"with_missing":2,"target_known":true,"total_gaps":0,"gaps":[],"insight":{"target":true},"ledger":"/l","records":6}"#.utf8))
        XCTAssertTrue(g.gaps.isEmpty)
        XCTAssertEqual(g.status, .none(keywordsJudged: true))
        XCTAssertEqual(g.statusText, "No gaps found.")
        XCTAssertNil(g.targetNote)
    }

    func testTargetNotReadCountsOnlyDevPrismGapsAndSaysSo() throws {
        let warn = "no target summary was read, so keywords are not marked"
        let g = try JobsGaps.decode(Data("""
        {"view":"new","roles":4,"with_missing":2,"target_known":false,"total_gaps":1,\
        "gaps":[{"term":"CUDA","cat":"lang","roles":2,"missing":2,"weight":10,"missing_weight":10,"companies":2,"examples":[]}],\
        "insight":{"target":false,"warning":"\(warn)"},"ledger":"/l","records":4}
        """.utf8))
        XCTAssertFalse(g.targetKnown)
        XCTAssertEqual(g.status, .listed)
        XCTAssertEqual(g.targetNote, "Only DevPrism gaps were counted: your target summary was not read.")
        XCTAssertEqual(g.insight?.warning, warn)

        // No DevPrism gap either: said as such, and keyword gaps are not claimed absent.
        let none = try JobsGaps.decode(Data(#"{"view":"new","roles":4,"with_missing":1,"target_known":false,"total_gaps":0,"gaps":[],"insight":{"target":false}}"#.utf8))
        XCTAssertEqual(none.status, .none(keywordsJudged: false))
        XCTAssertEqual(none.statusText, "No DevPrism gaps found. Keyword gaps were not judged.")
        XCTAssertNotNil(none.targetNote)
    }

    /// Roles, no target, no DevPrism analysis: nothing was judged, which is not "no gaps".
    func testNothingJudgedIsNeverReportedAsNoGaps() throws {
        let g = try JobsGaps.decode(Data(#"{"view":"new","roles":5,"with_missing":0,"target_known":false,"total_gaps":0,"gaps":[],"insight":{"target":false}}"#.utf8))
        XCTAssertEqual(g.status, .unjudged)
        let text = try XCTUnwrap(g.statusText)
        XCTAssertTrue(text.hasPrefix("No gaps could be judged"), text)
        XCTAssertFalse(text.localizedCaseInsensitiveContains("no gaps found"), text)
        XCTAssertNotNil(g.targetNote)
    }

    func testAnEmptySelectionHasNothingToJudge() throws {
        let g = try JobsGaps.decode(Data(#"{"view":"saved","roles":0,"with_missing":0,"target_known":true,"total_gaps":0,"gaps":[],"insight":{"target":true}}"#.utf8))
        XCTAssertEqual(g.status, .noRoles)
        XCTAssertNotEqual(g.statusText, "No gaps found.")
        let noTarget = try JobsGaps.decode(Data(#"{"view":"saved","roles":0,"with_missing":0,"target_known":false,"total_gaps":0,"gaps":[],"insight":{"target":false}}"#.utf8))
        XCTAssertEqual(noTarget.status, .noRoles)
        XCTAssertNil(noTarget.targetNote, "no roles were counted, so nothing was counted by DevPrism alone")
    }

    func testSaysHowManyOfHowManyWhenVaniCutTheList() throws {
        let g = try JobsGaps.decode(Data(#"{"view":"new","roles":9,"with_missing":0,"target_known":true,"total_gaps":61,"gaps":[{"term":"Go","cat":"lang","roles":2,"missing":0,"weight":5,"missing_weight":0,"companies":1,"examples":[]}],"insight":{"target":true}}"#.utf8))
        XCTAssertEqual(g.shownNote, "Showing 1 of 61")
        let all = try JobsGaps.decode(Data(gapsReply.utf8))
        XCTAssertNil(all.shownNote)
    }

    func testTextIsKeptVerbatim() throws {
        let g = try JobsGaps.decode(Data(#"{"view":"new","roles":1,"with_missing":0,"target_known":true,"total_gaps":1,"gaps":[{"term":"**C++** [x](http://evil)","cat":"","roles":1,"missing":0,"weight":1,"missing_weight":0,"companies":1,"examples":[{"key":"k","company":"A & B <i>","title":"$(whoami)"}]}],"insight":{"target":true}}"#.utf8))
        XCTAssertEqual(g.gaps[0].term, "**C++** [x](http://evil)")
        XCTAssertEqual(g.gaps[0].examples[0].company, "A & B <i>")
        XCTAssertEqual(g.gaps[0].examples[0].title, "$(whoami)")
        XCTAssertEqual(g.gaps[0].category, "")
    }

    // MARK: Refusals

    private func refused(_ json: String, _ why: String, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertThrowsError(try JobsGaps.decode(Data(json.utf8)), why, file: file, line: line) { err in
            XCTAssertTrue(String(describing: err).contains("gaps reply"), "\(why): \(err)", file: file, line: line)
        }
    }

    private func gapJSON(_ term: String = "Go", roles: Int = 2, missing: Int = 0, weight: String = "5", missingWeight: String = "0",
                         companies: Int = 1, examples: String = "[]") -> String {
        #"{"term":"\#(term)","cat":"lang","roles":\#(roles),"missing":\#(missing),"weight":\#(weight),"missing_weight":\#(missingWeight),"companies":\#(companies),"examples":\#(examples)}"#
    }

    private func report(roles: Int = 5, withMissing: Int = 0, total: Int = 1, gaps: String) -> String {
        #"{"view":"new","roles":\#(roles),"with_missing":\#(withMissing),"target_known":true,"total_gaps":\#(total),"gaps":[\#(gaps)],"insight":{"target":true}}"#
    }

    func testRefusesMalformedReplies() {
        refused("", "empty")
        refused("not json", "not JSON")
        refused("[]", "wrong top-level type")
        refused("{}", "no fields")
        refused(#"{"view":"new","roles":1,"with_missing":0,"target_known":true,"total_gaps":0,"gaps":null}"#, "null gaps is not an empty list")
        refused(#"{"view":"new","roles":1,"with_missing":0,"target_known":true,"total_gaps":0}"#, "absent gaps")
        refused(#"{"view":"new","roles":"5","with_missing":0,"target_known":true,"total_gaps":0,"gaps":[]}"#, "roles as a string")
        refused(#"{"view":"new","roles":1,"with_missing":0,"total_gaps":0,"gaps":[]}"#, "no target_known: unjudged must not read as judged")
        refused(report(gaps: #"{"term":"Go"}"#), "a gap missing its counts")
    }

    func testRefusesCountsThatCannotAddUp() {
        refused(report(roles: 3, withMissing: 4, total: 0, gaps: ""), "more roles analysed than considered")
        refused(report(roles: -1, total: 0, gaps: ""), "negative roles")
        refused(report(total: 0, gaps: gapJSON()), "fewer gaps found than listed")
        refused(report(roles: 0, gaps: gapJSON(roles: 1)), "gaps without roles")
        refused(report(roles: 2, gaps: gapJSON(roles: 3)), "a gap in more roles than exist")
        refused(report(gaps: gapJSON(roles: 0)), "a gap in no role")
        refused(report(gaps: gapJSON(roles: 2, missing: 3)), "more roles found missing than roles")
        refused(report(gaps: gapJSON(roles: 2, companies: 3)), "more companies than roles")
        refused(report(gaps: gapJSON(weight: "-1")), "negative weight")
        refused(report(gaps: gapJSON(missingWeight: "-0.5")), "negative missing weight")
        refused(report(gaps: gapJSON("")), "empty term")
        refused(report(total: 2, gaps: gapJSON() + "," + gapJSON()), "a term listed twice")
        let many = (0 ..< 11).map { #"{"key":"k\#($0)","company":"C","title":"T"}"# }.joined(separator: ",")
        refused(report(gaps: gapJSON(examples: "[\(many)]")), "more examples than Vani ever sends")
    }

    func testRefusesMoreGapsThanVaniEverSends() {
        let gaps = (0 ... JobsGaps.maxGaps).map { gapJSON("t\($0)") }.joined(separator: ",")
        refused(report(roles: 5, total: JobsGaps.maxGaps + 1, gaps: gaps), "201 gaps")
        let ok = (0 ..< JobsGaps.maxGaps).map { gapJSON("t\($0)") }.joined(separator: ",")
        XCTAssertNoThrow(try JobsGaps.decode(Data(report(roles: 5, total: JobsGaps.maxGaps, gaps: ok).utf8)), "200 is the limit, not past it")
    }

    func testRefusesAnOversizedReplyBeforeParsingIt() {
        let pad = String(repeating: " ", count: JobsGaps.maxReplyBytes)
        refused(gapsReply + pad, "valid JSON but past the byte ceiling")
        let atLimit = gapsReply + String(repeating: " ", count: JobsGaps.maxReplyBytes - gapsReply.utf8.count)
        XCTAssertNoThrow(try JobsGaps.decode(Data(atLimit.utf8)), "exactly at the ceiling is read")
    }

    // MARK: Client

    func testGapsArguments() {
        var f = JobFilter()
        f.sponsorships = ["none"]
        XCTAssertEqual(JobsClient.gapsArguments(.new, query: "", filter: JobFilter()), ["jobs", "gaps", "--json", "--view", "new"])
        XCTAssertEqual(JobsClient.gapsArguments(.all, query: " go ", filter: f),
                       ["jobs", "gaps", "--json", "--view", "all", "--q", "go", "--sponsorship", "none"],
                       "the same selection arguments as the list and the stats, and no --n/--sort")
    }

    func testClientRunsVaniAndDecodes() throws {
        let (vani, argv) = try fakeGapsVani(self, reply: Data(gapsReply.utf8))
        let g = try JobsClient(vaniBinary: vani, stateDir: "/state").gaps(.saved, query: "infra")
        XCTAssertEqual(g.gaps.count, 3)
        let args = try String(contentsOf: argv, encoding: .utf8).split(separator: "\n").map(String.init)
        XCTAssertEqual(args, ["jobs", "gaps", "--json", "--view", "saved", "--q", "infra"])
    }

    func testClientReportsVanisErrorAnOldVaniAndAnOversizedReply() throws {
        let err = try fakeGapsVani(self, reply: Data(#"{"error":"jobs gaps: the ledger is locked"}"#.utf8), status: 1)
        XCTAssertThrowsError(try JobsClient(vaniBinary: err.vani).gaps(.new)) { e in
            XCTAssertTrue(String(describing: e).contains("ledger is locked"), "\(e)")
        }
        let old = try fakeGapsVani(self, reply: Data("vani: unknown command".utf8), status: 2)
        XCTAssertThrowsError(try JobsClient(vaniBinary: old.vani).gaps(.new)) { e in
            XCTAssertTrue(String(describing: e).contains("too old"), "\(e)")
        }
        // Valid JSON the app would refuse for size: the process layer cuts it off first.
        let big = try fakeGapsVani(self, reply: Data((gapsReply + String(repeating: " ", count: JobsGaps.maxReplyBytes + 10)).utf8))
        XCTAssertThrowsError(try JobsClient(vaniBinary: big.vani).gaps(.new)) { e in
            XCTAssertTrue(String(describing: e).contains("larger than"), "\(e)")
        }
        // Exit 0 with a self-contradicting reply is an error, never a panel.
        let bad = try fakeGapsVani(self, reply: Data(report(roles: 0, gaps: gapJSON()).utf8))
        XCTAssertThrowsError(try JobsClient(vaniBinary: bad.vani).gaps(.new))
        XCTAssertThrowsError(try JobsClient(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani")).gaps(.new))
    }

    /// The contract end to end against a Go vani that has `jobs gaps` (skipped
    /// when build/vani is missing or predates the command).
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
        let line = #"{"v":1,"at":"2026-10-18T12:00:00.5Z","key":"gh:acme:1","status":"digested","posting":{"key":"gh:acme:1","ats":"gh","company":"Acme","title":"Inference Engineer","url":"https://boards.example.com/1","description":"Serve models with Kubernetes.","seen_at":"2026-10-18T12:00:00Z"},"annotations":{"explanation":"Fits: Inference serving and runtimes (88%).","triage.role_fit":"strong_fit","gap.missing":"Kubernetes"}}"#
        try Data((line + "\n").utf8).write(to: ledger.appendingPathComponent("events.jsonl"))
        let c = JobsClient(vaniBinary: vani, stateDir: state.path)
        let g: JobsGaps
        do { g = try c.gaps(.new) } catch {
            if String(describing: error).contains("too old") { throw XCTSkip("build/vani predates `jobs gaps`") }
            throw error
        }
        XCTAssertEqual(g.roles, 1)
        XCTAssertEqual(g.withMissing, 1)
        XCTAssertEqual(g.ledger, ledger.path)
        XCTAssertEqual(g.records, 1)
        XCTAssertEqual(g.totalGaps, g.gaps.count)
    }
}
