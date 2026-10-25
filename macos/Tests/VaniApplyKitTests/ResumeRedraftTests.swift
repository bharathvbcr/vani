import Foundation
import XCTest
@testable import VaniApplyKit

/// The redraft, comparison, restore and coverage wire: what `jobs resume build --from`, `diff`, `current`,
/// `coverage` and `gaps` send and answer (docs/RUNTIME.md), and how it is worded. Every id, company and sentence
/// is made up. No real vani: a script records its arguments and prints the reply a test chose.

private struct Fake {
    let vani: URL
    let dir: URL

    var argv: [String]? {
        guard let text = try? String(contentsOf: dir.appendingPathComponent("argv"), encoding: .utf8) else { return nil }
        return text.split(separator: "\n", omittingEmptySubsequences: true).map(String.init)
    }

    func reply(_ json: String, status: Int32 = 0) throws {
        try Data(json.utf8).write(to: dir.appendingPathComponent("reply.json"))
        try Data("\(status)".utf8).write(to: dir.appendingPathComponent("status"))
    }
}

private func makeFake(_ t: XCTestCase, reply: String = "{}", status: Int32 = 0) throws -> Fake {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent("vani-apply-redraft-\(UUID().uuidString)")
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    t.addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
    let url = dir.appendingPathComponent("vani")
    let script = """
    #!/bin/sh
    d='\(dir.path)'
    : > "$d/argv"
    for a in "$@"; do printf '%s\\n' "$a" >> "$d/argv"; done
    cat "$d/reply.json"
    exit "$(cat "$d/status")"
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    let fake = Fake(vani: url, dir: dir)
    try fake.reply(reply, status: status)
    return fake
}

private func client(_ f: Fake) -> ResumeStudioClient {
    ResumeStudioClient(runner: DraftRunner(vaniBinary: f.vani, key: { nil }, decisionKey: { nil }))
}

private let key = "gh:acme:1"
private let checks = #"{"eligibility":"current","lint":"passed","fit":"passed","figures":"verified"}"#

private func decode<T: Decodable>(_ type: T.Type, _ json: String) throws -> T {
    try JobsClient.decoder.decode(T.self, from: Data(json.utf8))
}

private let diffJSON = """
{"key":"gh:acme:1",
 "from":{"version":2,"source":"edited","parent":1,"created":"2026-10-25T10:00:00Z"},
 "to":{"version":5,"source":"tailored","parent":3,"instruction":"prefer the tracing work","scope":"GPU Systems","lock":["Summary","Experience"],"created":"2026-10-25T12:00:00.5Z"},
 "same":false,
 "bullets":{"added":[{"id":"p4","section":"GPU Systems","entry":"projB","index":0}],
            "removed":[{"id":"b2","section":"Experience","entry":"job1","index":1}],
            "moved":[{"id":"p1","from":{"section":"Agent Systems","entry":"projA","index":1},"to":{"section":"GPU Systems","entry":"projB","index":2}}]},
 "overrides":[{"entry":"job1","field":"title","from":"","to":"Principal Engineer"}],
 "section_order":{"from":["Summary","Experience","Agent Systems"],"to":["Summary","Agent Systems","Experience"]},
 "keywords":{"added":["tracing"],"removed":["etl"]}}
"""

private let coverageJSON = """
{"key":"gh:acme:1","version":3,"keywords_source":"posting+jd_keywords",
 "hit":[{"term":"Kubernetes","bullets":["b1","b3"],"n":2},{"term":"CUDA","bullets":["b2"],"n":1}],
 "miss":["Rust","Triton"],"uncovered":["Triton"],"unjudged":["a phrase that is far too long to match"],"unresolved":["b9"],
 "terms_total":6,"gap_missing":["Triton"]}
"""

final class ResumeRedraftWireTests: XCTestCase {
    // MARK: Eligibility, versions, dry run

    func testEligibilityNamesAFigureThatNoLongerVerifies() throws {
        let e = try decode(ResumeEligibility.self, #"{"state":"stale","unverified":["b7","b8"],"reworded":["b1"],"why":"rkb no longer double-verifies a figure in b7, b8"}"#)
        XCTAssertEqual(e.unverified, ["b7", "b8"])
        XCTAssertEqual(e.summary, "1 reworded, 2 with a figure no longer verified")
        XCTAssertEqual(Set(e.staleIDs), ["b1", "b7", "b8"], "an unverified bullet is stale like any other")
        XCTAssertEqual(try decode(ResumeEligibility.self, #"{"state":"current"}"#).unverified, [])
        let detail = try XCTUnwrap(ResumeWords.eligibilityDetail(e))
        XCTAssertTrue(detail.contains("rkb no longer double-verifies a figure in b7, b8"), detail)
        XCTAssertTrue(detail.contains("reworded in the bank: b1"), detail)
        XCTAssertNil(ResumeWords.eligibilityDetail(ResumeEligibility(state: "current")), "a vouched-for version has nothing to explain")
        XCTAssertNotNil(ResumeWords.eligibilityDetail(ResumeEligibility(state: "unknown", why: "the figures of this version could not be re-checked: x")),
                        "a check that could not run says why")
    }

    func testAVersionCarriesItsInstructionScopeAndLocksOrNone() throws {
        func v(_ extra: String) throws -> ResumeVersion {
            try decode(ResumeVersion.self, #"{"schema":2,"key":"k","version":4,"parent":3,"source":"tailored","checks":\#(checks),"created":"2026-10-25T12:00:00Z"\#(extra)}"#)
        }
        let a = try v(#","instruction":"prefer the tracing work","scope":"GPU Systems","lock":["Summary"]"#)
        XCTAssertEqual(a.instruction, "prefer the tracing work")
        XCTAssertEqual(a.scope, "GPU Systems")
        XCTAssertEqual(a.lock, ["Summary"])
        let b = try v("")
        XCTAssertNil(b.instruction)
        XCTAssertNil(b.scope)
        XCTAssertEqual(b.lock, [])
        let empty = try v(#","instruction":"","scope":"","lock":null"#)
        XCTAssertNil(empty.instruction)
        XCTAssertNil(empty.scope, "an empty name is none, never a scope called \"\"")
        XCTAssertEqual(empty.lock, [])
    }

    func testTheDryRunEchoesTheRedraftAndNeverTheInstructionText() throws {
        let d = try decode(ResumeDryRun.self, """
        {"key":"k","dry_run":true,"model":"claude-opus-5-5","opus_usable":true,"current":3,"from":2,"scope":"GPU Systems","lock":["Summary"],"instruction_runes":24,
         "budget":{"daily_cap_usd":20},"estimate":{"turns":2,"input_tokens":1,"max_output_tokens":2,"max_usd":0.1}}
        """)
        XCTAssertEqual(d.from, 2)
        XCTAssertEqual(d.scope, "GPU Systems")
        XCTAssertEqual(d.lock, ["Summary"])
        XCTAssertEqual(d.instructionRunes, 24)
        let plain = try decode(ResumeDryRun.self, #"{"opus_usable":false,"no_model":"x"}"#)
        XCTAssertNil(plain.from)
        XCTAssertNil(plain.scope)
        XCTAssertEqual(plain.lock, [])
        XCTAssertNil(plain.instructionRunes)
    }

    // MARK: diff

    func testADiffReadsEverySectionOfGosReply() throws {
        let d = try decode(ResumeDiff.self, diffJSON)
        XCTAssertEqual(d.from.version, 2)
        XCTAssertEqual(d.to.instruction, "prefer the tracing work")
        XCTAssertEqual(d.to.scope, "GPU Systems")
        XCTAssertEqual(d.to.lock, ["Summary", "Experience"])
        XCTAssertNotNil(d.to.created, "fractional seconds read")
        XCTAssertEqual(d.bullets.added, [.init(id: "p4", section: "GPU Systems", entry: "projB", index: 0)])
        XCTAssertEqual(d.bullets.moved.first?.to, .init(section: "GPU Systems", entry: "projB", index: 2))
        XCTAssertEqual(d.overrides, [.init(entry: "job1", field: "title", from: "", to: "Principal Engineer")])
        XCTAssertEqual(d.sectionOrder?.to, ["Summary", "Agent Systems", "Experience"])
        XCTAssertEqual(d.keywords, .init(added: ["tracing"], removed: ["etl"]))
        XCTAssertFalse(d.same)
        XCTAssertFalse(d.listsAreEmpty)
    }

    func testADiffRoundTripsThroughCodable() throws {
        let d = try decode(ResumeDiff.self, diffJSON)
        let enc = JSONEncoder()
        enc.dateEncodingStrategy = .iso8601
        let again = try decode(ResumeDiff.self, String(decoding: try enc.encode(d), as: UTF8.self))
        XCTAssertEqual(again.bullets, d.bullets)
        XCTAssertEqual(again.overrides, d.overrides)
        XCTAssertEqual(again.sectionOrder, d.sectionOrder)
        XCTAssertEqual(again.keywords, d.keywords)
        XCTAssertEqual(again.to.lock, d.to.lock)
        XCTAssertEqual(again.same, d.same)
    }

    func testADiffMissingAListOrItsVerdictIsNotReadAsNothingDiffers() throws {
        let base = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(diffJSON.utf8)) as? [String: Any])
        for field in ["same", "bullets", "overrides", "keywords", "from", "to", "key"] {
            var obj = base
            obj[field] = nil
            let data = try JSONSerialization.data(withJSONObject: obj)
            XCTAssertThrowsError(try JobsClient.decoder.decode(ResumeDiff.self, from: data), "without \(field)")
        }
        for list in ["added", "removed", "moved"] {
            var obj = base
            var bullets = try XCTUnwrap(obj["bullets"] as? [String: Any])
            bullets[list] = nil
            obj["bullets"] = bullets
            XCTAssertThrowsError(try JobsClient.decoder.decode(ResumeDiff.self, from: try JSONSerialization.data(withJSONObject: obj)), "bullets without \(list)")
        }
        // A section order the reply leaves out is "the order did not change": it is optional on the wire.
        var obj = base
        obj["section_order"] = nil
        XCTAssertNil(try JobsClient.decoder.decode(ResumeDiff.self, from: try JSONSerialization.data(withJSONObject: obj)).sectionOrder)
    }

    func testTheClientRefusesADiffThatContradictsItselfOrAnswersForOtherVersions() throws {
        // `same: true` with an added bullet.
        let lying = diffJSON.replacingOccurrences(of: #""same":false"#, with: #""same":true"#)
        let f = try makeFake(self, reply: lying)
        let out = client(f).diff(key, from: 2, to: 5)
        guard case let .failure(fail) = out else { return XCTFail("a self-contradicting diff was accepted") }
        XCTAssertEqual(fail.code, "unreadable_reply")
        try f.reply(diffJSON)
        guard case .failure(let other) = client(f).diff(key, from: 1, to: 5) else { return XCTFail("a diff of other versions was accepted") }
        XCTAssertEqual(other.code, "unreadable_reply")
        guard case let .success(ok) = client(f).diff(key, from: 2, to: 5) else { return XCTFail("a good diff was refused") }
        XCTAssertEqual(ok.to.version, 5)
        XCTAssertEqual(f.argv, ["jobs", "resume", "diff", "--json", key, "v2", "v5"])
    }

    // MARK: current

    func testCurrentReadsAMoveAndANoOpAndNeedsItsVerdict() throws {
        let moved = try decode(ResumeCurrentSet.self, #"{"key":"k","version":2,"previous":3,"changed":true}"#)
        XCTAssertEqual(moved.previous, 3)
        XCTAssertTrue(moved.changed)
        let same = try decode(ResumeCurrentSet.self, #"{"key":"k","version":3,"changed":false}"#)
        XCTAssertNil(same.previous)
        XCTAssertFalse(same.changed)
        XCTAssertThrowsError(try decode(ResumeCurrentSet.self, #"{"key":"k","version":3}"#), "without `changed`, a no-op and a move look alike")
        XCTAssertThrowsError(try decode(ResumeCurrentSet.self, #"{"key":"k","version":0,"changed":true}"#))
        let f = try makeFake(self, reply: #"{"key":"gh:acme:1","version":4,"changed":true}"#)
        guard case .failure(let wrong) = client(f).makeCurrent(key, version: 2) else { return XCTFail("an answer for another version was accepted") }
        XCTAssertEqual(wrong.code, "unreadable_reply")
        try f.reply(#"{"key":"gh:acme:1","version":2,"previous":3,"changed":true}"#)
        guard case let .success(ok) = client(f).makeCurrent(key, version: 2) else { return XCTFail("current") }
        XCTAssertEqual(ok.previous, 3)
        XCTAssertEqual(f.argv, ["jobs", "resume", "current", "--json", key, "v2"])
    }

    // MARK: coverage

    func testCoverageKeepsMissUncoveredUnjudgedAndUnresolvedApart() throws {
        let c = try decode(ResumeCoverage.self, coverageJSON)
        XCTAssertEqual(c.version, 3)
        XCTAssertFalse(c.base)
        XCTAssertEqual(c.hit.map(\.term), ["Kubernetes", "CUDA"])
        XCTAssertEqual(c.hit.first?.bullets, ["b1", "b3"])
        XCTAssertEqual(c.judged, 4, "2 hit and 2 missed; the unjudged phrase is in neither")
        XCTAssertEqual(c.swappable, ["Rust"], "a swap can fix a miss the bank can cover")
        XCTAssertEqual(c.uncovered, ["Triton"])
        XCTAssertEqual(c.unjudged.count, 1)
        XCTAssertEqual(c.unresolved, ["b9"])
        XCTAssertFalse(c.termsTruncated)
        let base = try decode(ResumeCoverage.self, coverageJSON.replacingOccurrences(of: #""version":3"#, with: #""base":true"#))
        XCTAssertNil(base.version)
        XCTAssertTrue(base.base)
    }

    func testCoverageNeedsEveryListAndNamesWhatItMeasured() throws {
        let base = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(coverageJSON.utf8)) as? [String: Any])
        for field in ["hit", "miss", "uncovered", "unjudged", "unresolved", "terms_total", "keywords_source", "key", "version"] {
            var obj = base
            obj[field] = nil
            XCTAssertThrowsError(try JobsClient.decoder.decode(ResumeCoverage.self, from: try JSONSerialization.data(withJSONObject: obj)),
                                 "without \(field): a missing list must never read as nothing missed")
        }
        var neg = base
        neg["terms_total"] = -1
        XCTAssertThrowsError(try JobsClient.decoder.decode(ResumeCoverage.self, from: try JSONSerialization.data(withJSONObject: neg)))
        let c = try decode(ResumeCoverage.self, coverageJSON)
        let enc = try JSONEncoder().encode(c)
        XCTAssertEqual(try decode(ResumeCoverage.self, String(decoding: enc, as: UTF8.self)), c, "Codable round trip")
    }

    func testCoverageAskedForAVersionMustAnswerForIt() throws {
        let f = try makeFake(self, reply: coverageJSON)
        guard case .failure(let wrong) = client(f).coverage(key, version: 4) else { return XCTFail("coverage of another version was accepted") }
        XCTAssertEqual(wrong.code, "unreadable_reply")
        guard case .success = client(f).coverage(key, version: 3) else { return XCTFail("coverage") }
        XCTAssertEqual(f.argv, ["jobs", "resume", "coverage", "--json", key, "v3"])
        guard case .success = client(f).coverage(key, version: nil) else { return XCTFail("coverage of the current version") }
        XCTAssertEqual(f.argv, ["jobs", "resume", "coverage", "--json", key], "no version asked, none sent")
    }

    // MARK: gaps

    func testGapsSaysWhetherAnythingWasWrittenAndADryRunNeverPassesForAWrite() throws {
        let dry = #"{"key":"gh:acme:1","terms":["Triton","Rust"],"written":false,"file":"/s/resumes/gaps.jsonl","dry_run":true,"note":"dry run"}"#
        let wrote = #"{"key":"gh:acme:1","terms":["Triton","Rust"],"written":true,"file":"/s/resumes/gaps.jsonl"}"#
        let f = try makeFake(self, reply: dry)
        guard case let .success(g) = client(f).gaps(key, dryRun: true) else { return XCTFail("dry run") }
        XCTAssertEqual(g.terms, ["Triton", "Rust"])
        XCTAssertFalse(g.written)
        XCTAssertTrue(g.dryRun)
        XCTAssertEqual(f.argv, ["jobs", "resume", "gaps", "--json", "--dry-run", key])
        // A real write that comes back as a dry run, and a dry run that wrote, are both refused.
        guard case .failure(let a) = client(f).gaps(key, dryRun: false) else { return XCTFail("a dry run passed for a write") }
        XCTAssertEqual(a.code, "unreadable_reply")
        XCTAssertEqual(f.argv, ["jobs", "resume", "gaps", "--json", key], "the real call has no --dry-run")
        try f.reply(wrote)
        guard case .failure(let b) = client(f).gaps(key, dryRun: true) else { return XCTFail("a write passed for a dry run") }
        XCTAssertEqual(b.code, "unreadable_reply")
        guard case let .success(done) = client(f).gaps(key, dryRun: false) else { return XCTFail("write") }
        XCTAssertTrue(done.written)
        XCTAssertThrowsError(try decode(ResumeGaps.self, #"{"key":"k","terms":[]}"#), "without `written`, nothing says whether it was")
        XCTAssertThrowsError(try decode(ResumeGaps.self, #"{"key":"k","written":false}"#), "without `terms`")
        try f.reply(#"{"key":"gh:acme:1","terms":["x"],"written":true,"dry_run":true}"#)
        guard case .failure = client(f).gaps(key, dryRun: true) else { return XCTFail("a dry run that says it wrote was accepted") }
    }

    func testEveryHandOffOutcomeIsWordedAsWhatItIs() throws {
        func g(_ json: String) throws -> ResumeGaps { try decode(ResumeGaps.self, json) }
        let sent = ResumeWords.handOff(try g(#"{"key":"k","terms":["a","b"],"written":true,"file":"/s/resumes/gaps.jsonl"}"#))
        XCTAssertTrue(sent.hasPrefix("Sent 2 terms"), sent)
        XCTAssertTrue(sent.contains("gaps.jsonl"), sent)
        let dry = ResumeWords.handOff(try g(#"{"key":"k","terms":["a"],"written":false,"dry_run":true}"#))
        XCTAssertTrue(dry.contains("Nothing has been written yet"), dry)
        XCTAssertFalse(dry.lowercased().contains("sent"), dry)
        let none = ResumeWords.handOff(try g(#"{"key":"k","terms":[],"written":false,"note":"no eligible bullet is missing a term, and DevPrism named none: nothing to hand over"}"#))
        XCTAssertTrue(none.hasPrefix("Nothing was written."), none)
        XCTAssertTrue(none.contains("nothing to hand over"), none)
        let same = ResumeWords.handOff(try g(#"{"key":"k","terms":["a"],"written":false,"unchanged":true}"#))
        XCTAssertTrue(same.contains("Nothing was added"), same)
        for text in [dry, none, same] { XCTAssertFalse(text.hasPrefix("Sent"), text) }
    }
}

final class ResumeRedraftClientTests: XCTestCase {
    private let dry = """
    {"key":"gh:acme:1","dry_run":true,"model":"claude-opus-5-5","opus_usable":true,"current":3,"from":2,"scope":"GPU Systems","lock":["Summary"],"instruction_runes":9,
     "budget":{"daily_cap_usd":20},"estimate":{"turns":2,"input_tokens":1,"max_output_tokens":2,"max_usd":0.1}}
    """

    func testTheRedraftFlagsAreSentInTheEqualsFormSoAValueIsNeverAFlag() throws {
        let f = try makeFake(self, reply: dry)
        let r = ResumeRedraft(from: 2, scope: "GPU Systems", lock: ["Summary", "Experience"], instruction: "  --help -x show v9  ")
        guard case .success = client(f).estimate(key, redraft: r) else { return XCTFail("estimate") }
        XCTAssertEqual(f.argv, ["jobs", "resume", "build", "--json", "--dry-run", "--from=v2", "--scope=GPU Systems", "--lock=Summary", "--lock=Experience",
                                "--instruction=--help -x show v9", key], "the instruction is trimmed and passed verbatim after =, so a leading dash is a value")
        try f.reply(#"{"error":"stopped","code":"bad_scope"}"#, status: 1)
        _ = client(f).build(key, redraft: r)
        XCTAssertEqual(f.argv, ["jobs", "resume", "build", "--json", "--from=v2", "--scope=GPU Systems", "--lock=Summary", "--lock=Experience",
                                "--instruction=--help -x show v9", key])
    }

    func testAWholeRedraftSendsOnlyFromAndAPlainBuildSendsNone() throws {
        let f = try makeFake(self, reply: dry)
        _ = client(f).estimate(key, redraft: ResumeRedraft(from: 4))
        XCTAssertEqual(f.argv, ["jobs", "resume", "build", "--json", "--dry-run", "--from=v4", key])
        _ = client(f).estimate(key, redraft: ResumeRedraft(from: 4, instruction: "   "))
        XCTAssertEqual(f.argv, ["jobs", "resume", "build", "--json", "--dry-run", "--from=v4", key], "whitespace is no instruction")
        _ = client(f).estimate(key)
        XCTAssertEqual(f.argv, ["jobs", "resume", "build", "--json", "--dry-run", key], "no redraft, no flags")
    }

    func testWhatGoCertainlyRefusesIsRefusedBeforeVaniIsRun() throws {
        let f = try makeFake(self, reply: dry)
        let cases: [(String, ResumeRedraft, String)] = [
            ("over 500 characters", ResumeRedraft(from: 2, instruction: String(repeating: "é", count: 501)), "bad_instruction"),
            ("a newline inside", ResumeRedraft(from: 2, instruction: "a\nb"), "bad_instruction"),
            ("a tab inside", ResumeRedraft(from: 2, instruction: "a\tb"), "bad_instruction"),
            ("a bidi override", ResumeRedraft(from: 2, instruction: "abc\u{202E}def"), "bad_instruction"),
            ("a NUL", ResumeRedraft(from: 2, instruction: "a\u{0}b"), "bad_instruction"),
            ("an empty lock", ResumeRedraft(from: 2, lock: [" "]), "bad_scope"),
            ("a 121 character scope", ResumeRedraft(from: 2, scope: String(repeating: "x", count: 121)), "bad_scope"),
            ("a scope with a newline", ResumeRedraft(from: 2, scope: "a\nb"), "bad_scope"),
            ("17 locks", ResumeRedraft(from: 2, lock: (1...17).map { "S\($0)" }), "bad_scope"),
            ("version 0", ResumeRedraft(from: 0), "bad_request"),
            ("version 10000", ResumeRedraft(from: 10000), "bad_request"),
        ]
        func code<T>(_ o: ResumeOutcome<T>) -> String? {
            if case let .failure(f) = o { return f.code }
            return nil
        }
        for (name, r, expected) in cases {
            XCTAssertEqual(code(client(f).estimate(key, redraft: r)), expected, "estimate: \(name)")
            XCTAssertEqual(code(client(f).build(key, redraft: r)), expected, "build: \(name)")
        }
        XCTAssertNil(f.argv, "vani was never run for a refusal")
        // At the bounds, it is sent.
        guard case .success = client(f).estimate(key, redraft: ResumeRedraft(from: 2, scope: String(repeating: "x", count: 120), lock: (1...16).map { "S\($0)" },
                                                                          instruction: String(repeating: "é", count: 500))) else { return XCTFail("a redraft at the bounds was refused") }
        XCTAssertNotNil(f.argv)
    }

    func testAScopeOrLockCannotBeCombinedWithTheBaseFallback() throws {
        let f = try makeFake(self, reply: "{}")
        for r in [ResumeRedraft(from: 2, scope: "Summary"), ResumeRedraft(from: 2, lock: ["Summary"])] {
            guard case let .failure(fail) = client(f).build(key, allowBase: true, redraft: r) else { return XCTFail("allowed") }
            XCTAssertEqual(fail.code, "config")
        }
        XCTAssertNil(f.argv, "refused before vani ran")
        _ = client(f).build(key, allowBase: true, redraft: ResumeRedraft(from: 2))
        XCTAssertNotNil(f.argv, "a whole redraft keeps nothing, so the flag is not combined with a kept part")
    }

    func testTheNewCallsHaveTheirOwnTimeoutsAndNeverUseAModel() {
        let c = ResumeStudioClient(runner: DraftRunner(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani")))
        let calls: [(ResumeStudioClient.Call, [String], TimeInterval)] = [
            (c.diffCall(key, from: 1, to: 2), ["jobs", "resume", "diff", "--json", key, "v1", "v2"], 20),
            (c.currentCall(key, version: 2), ["jobs", "resume", "current", "--json", key, "v2"], 25),
            (c.coverageCall(key, version: nil), ["jobs", "resume", "coverage", "--json", key], 60),
            (c.coverageCall(key, version: 3), ["jobs", "resume", "coverage", "--json", key, "v3"], 60),
            (c.gapsCall(key, dryRun: true), ["jobs", "resume", "gaps", "--json", "--dry-run", key], 60),
            (c.gapsCall(key, dryRun: false), ["jobs", "resume", "gaps", "--json", key], 60),
        ]
        // Go's deadlines (docs/RUNTIME.md) plus 15 s: diff 5, current 10, coverage and gaps 45.
        let go: [TimeInterval: TimeInterval] = [20: 5, 25: 10, 60: 45]
        for (call, argv, timeout) in calls {
            XCTAssertEqual(call.arguments, argv)
            XCTAssertEqual(call.timeout, timeout, "\(argv)")
            XCTAssertEqual(timeout, go[timeout].map { $0 + 15 }, "\(argv)")
            XCTAssertFalse(call.usesModel, "\(argv): nothing here asks a model")
            XCTAssertNil(call.input)
        }
    }
}
