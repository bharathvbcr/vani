import Foundation
import XCTest
@testable import VaniApplyKit

/// A stand-in for `vani`: it appends every argument list to `calls.log` and
/// replies per command, so the tests see what the app asked for.
private struct FakeAuditVani {
    var binary: URL
    var log: URL
    var calls: [String] { ((try? String(contentsOf: log, encoding: .utf8)) ?? "").split(separator: "\n").map(String.init) }
}

private func fakeAuditVani(_ t: XCTestCase, draw: String, answer: String = "", answerStatus: Int32 = 0, show: String = "") throws -> FakeAuditVani {
    let dir = try tempDir(t)
    let log = dir.appendingPathComponent("calls.log")
    let url = dir.appendingPathComponent("vani")
    let script = """
    #!/bin/sh
    printf '%s\\n' "$*" >> '\(log.path)'
    case "$*" in
    "audit answer"*) cat <<'EOF'
    \(answer)
    EOF
    exit \(answerStatus) ;;
    "audit --json"*) cat <<'EOF'
    \(draw)
    EOF
    exit 0 ;;
    "jobs show"*) cat <<'EOF'
    \(show)
    EOF
    exit 0 ;;
    esac
    exit 2
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return FakeAuditVani(binary: url, log: log)
}

private let drawReply = """
{"seed":"2026-W43","blind":true,"picks":[\
{"key":"gh:acme:1","stratum":"decile:10","p_select":0.25},\
{"key":"gh:acme:2","stratum":"dropped:title:role_fit","p_select":0.5}],\
"strata":[{"stratum":"decile:10","pool":4,"taken":1},{"stratum":"dropped:title:role_fit","pool":2,"taken":1}]}
"""

private let showReply = """
{"key":"gh:acme:1","company":"Acme Corp","title":"Inference Engineer","location":"Remote","status":"dropped",\
"description":"Build inference servers.","triage":{"role_fit":"mismatch_domain"},\
"row":{"v":1,"key":"gh:acme:1","title":"Inference Engineer (row)","company":"Acme (row)","location":"Berlin","status":"dropped",\
"status_label":"Dropped","relevance":10,"priority":5,"fit":"mismatch_domain","fit_label":"Different field","similar":0}}
"""

final class AuditTests: XCTestCase {
    private func client(_ f: FakeAuditVani) -> AuditClient {
        AuditClient(jobs: JobsClient(vaniBinary: f.binary, timeout: 10))
    }

    func testDrawIsFetchedBlindAndDecoded() throws {
        let f = try fakeAuditVani(self, draw: drawReply)
        let d = try client(f).draw()
        XCTAssertEqual(f.calls, ["audit --json --blind"])
        XCTAssertEqual(d.seed, "2026-W43")
        XCTAssertTrue(d.blind)
        XCTAssertEqual(d.picks, [AuditPick(key: "gh:acme:1", stratum: "decile:10", pSelect: 0.25),
                                 AuditPick(key: "gh:acme:2", stratum: "dropped:title:role_fit", pSelect: 0.5)])
    }

    func testAnswerWritesThroughVaniAuditAnswerWithTheDrawnNumbers() throws {
        let reply = #"{"key":"gh:acme:2","verdict":"maybe","stratum":"dropped:title:role_fit","p_select":0.5}"#
        let f = try fakeAuditVani(self, draw: drawReply, answer: reply)
        let pick = AuditPick(key: "gh:acme:2", stratum: "dropped:title:role_fit", pSelect: 0.5)
        let a = try client(f).answer(pick, .maybe)
        XCTAssertEqual(f.calls, ["audit answer --json --key=gh:acme:2 --verdict=maybe --source=dropped:title:role_fit --p-select=0.5"])
        XCTAssertEqual(a.verdict, "maybe")
        XCTAssertEqual(a.pSelect, 0.5)
    }

    func testAFailedAnswerSurfacesVanisReasonAndWritesNothingHere() throws {
        let f = try fakeAuditVani(self, draw: drawReply, answer: #"{"error":"audit answer: gh:acme:1 is already answered"}"#, answerStatus: 1)
        XCTAssertThrowsError(try client(f).answer(AuditPick(key: "gh:acme:1", stratum: "decile:10", pSelect: 0.25), .yes)) {
            XCTAssertTrue(String(describing: $0).contains("already answered"), "\($0)")
        }
    }

    func testMalformedPicksNeverReachTheCommandLine() throws {
        let good = AuditPick(key: "gh:acme:1", stratum: "decile:10", pSelect: 0.25)
        var bad: [AuditPick] = []
        for p in [0.0, -0.1, 1.5, .nan, .infinity] { var b = good; b.pSelect = p; bad.append(b) }
        var b = good; b.key = ""; bad.append(b)
        b = good; b.key = "k\0x"; bad.append(b)
        b = good; b.key = String(repeating: "k", count: 513); bad.append(b)
        b = good; b.stratum = ""; bad.append(b)
        for p in bad {
            XCTAssertThrowsError(try AuditClient.answerArguments(p, .yes), "\(p)")
        }
        // A key that looks like a flag stays one argument and cannot be read as a flag.
        var dash = good
        dash.key = "--verdict=yes"
        XCTAssertEqual(try AuditClient.answerArguments(dash, .no)[3], "--key=--verdict=yes")
    }

    func testADrawWithABadPickOrTooManyPicksIsRefusedWhole() throws {
        let badPick = #"{"seed":"s","blind":true,"picks":[{"key":"gh:a:1","stratum":"decile:1","p_select":2}]}"#
        XCTAssertThrowsError(try client(try fakeAuditVani(self, draw: badPick)).draw())
        let many = (0...AuditClient.maxPicks).map { #"{"key":"gh:a:\#($0)","stratum":"decile:1","p_select":0.5}"# }.joined(separator: ",")
        XCTAssertThrowsError(try client(try fakeAuditVani(self, draw: #"{"seed":"s","blind":true,"picks":[\#(many)]}"#)).draw())
        XCTAssertThrowsError(try client(try fakeAuditVani(self, draw: "not json")).draw())
    }

    func testAnEmptyDrawDecodesAsNoPicks() throws {
        let d = try client(try fakeAuditVani(self, draw: #"{"seed":"s","blind":true,"picks":[],"strata":[]}"#)).draw()
        XCTAssertTrue(d.picks.isEmpty)
        XCTAssertTrue(AuditSession(picks: d.picks).isDone)
    }

    func testTheCardComesFromTheJobsRowContractAndShowsNoReading() throws {
        let f = try fakeAuditVani(self, draw: drawReply, show: showReply)
        let card = try client(f).card(AuditPick(key: "gh:acme:1", stratum: "decile:10", pSelect: 0.25))
        XCTAssertEqual(f.calls, ["jobs show --json gh:acme:1"])
        XCTAssertEqual(card.title, "Inference Engineer (row)")
        XCTAssertEqual(card.company, "Acme (row)")
        XCTAssertEqual(card.location, "Berlin")
        XCTAssertEqual(card.description, "Build inference servers.")
        // The card type holds no status, fit or stratum: nothing to leak the model's reading.
        let shown = Mirror(reflecting: card).children.compactMap(\.label)
        XCTAssertEqual(Set(shown), ["key", "title", "company", "location", "description"])
    }

    func testACardWithoutARowFallsBackToTheJobsOwnFieldsAndBoundsTheText() {
        var j = Job(key: "k", company: "Co", title: "T")
        j.location = "   "
        j.description = String(repeating: "x", count: AuditCard.maxDescription + 500)
        let c = AuditCard(job: j)
        XCTAssertEqual(c.title, "T")
        XCTAssertEqual(c.company, "Co")
        XCTAssertNil(c.location)
        XCTAssertEqual(c.description?.count, AuditCard.maxDescription)
        j.description = " \n "
        XCTAssertNil(AuditCard(job: j).description)
    }

    func testAJobRowOfAnUnknownVersionIsRefusedNotHalfShown() throws {
        let v2 = showReply.replacingOccurrences(of: #""v":1"#, with: #""v":2"#)
        let f = try fakeAuditVani(self, draw: drawReply, show: v2)
        XCTAssertThrowsError(try client(f).card(AuditPick(key: "gh:acme:1", stratum: "decile:10", pSelect: 0.25)))
    }

    func testSessionAdvancesOnlyOnRecordAndKeepsTheTally() {
        let a = AuditPick(key: "a", stratum: "decile:1", pSelect: 0.5), b = AuditPick(key: "b", stratum: "decile:2", pSelect: 0.5)
        var s = AuditSession(picks: [a, b, a]) // a duplicate is one card
        XCTAssertEqual(s.picks.count, 2)
        XCTAssertEqual(s.current, a)
        XCTAssertEqual(s.position, "1 of 2")
        XCTAssertFalse(s.isDone)
        s.record(.yes)
        XCTAssertEqual(s.current, b)
        XCTAssertEqual(s.position, "2 of 2")
        s.record(.skip)
        XCTAssertTrue(s.isDone)
        XCTAssertNil(s.current)
        XCTAssertEqual(s.answered, 2)
        XCTAssertEqual(s.count(.yes), 1)
        XCTAssertEqual(s.count(.skip), 1)
        XCTAssertEqual(s.count(.no), 0)
        s.record(.no) // past the end: nothing happens
        XCTAssertEqual(s.answered, 2)
        XCTAssertEqual(s.position, "2 of 2")
    }

    func testOneKeyEachForTheFourVerdicts() {
        XCTAssertEqual(AuditVerdict.allCases.map(\.rawValue), ["yes", "maybe", "no", "skip"])
        XCTAssertEqual(Set(AuditVerdict.allCases.map(\.shortcut)).count, 4)
        XCTAssertEqual(AuditVerdict(shortcut: "y"), .yes)
        XCTAssertEqual(AuditVerdict(shortcut: "M"), .maybe)
        XCTAssertEqual(AuditVerdict(shortcut: "n"), .no)
        XCTAssertEqual(AuditVerdict(shortcut: "s"), .skip)
        XCTAssertNil(AuditVerdict(shortcut: "x"))
    }
}
