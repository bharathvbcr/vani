import Foundation
import XCTest
@testable import VaniApplyKit

// `vani doctor`: the golden reply Go writes (cmd/vani/doctorwire_test.go), the client against a fake vani,
// and the words. No test reads the Keychain, a key or the owner's state, and nothing is sent anywhere.

private func goldenReply() throws -> Data {
    // Tests/VaniApplyKitTests/<file> -> Fixtures/doctor/reply.json, written by
    // `go test ./cmd/vani -run TestDoctorReplyGolden -update`.
    let url = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        .appendingPathComponent("Fixtures/doctor/reply.json")
    return try Data(contentsOf: url)
}

private let dryJSON = """
{"model":"claude-opus-5-5","runs_read":30,"boards_signaled":7,"board_reads_recovered_from_logs":1578,"drops_sampled":40,\
"seed":"2026-W46","strata":[{"name":"dropped:title:other","pool":3996,"taken":7}],"worst_case_usd":1.21922,\
"requests":{"boards":"24025 prompt bytes","drops":"79308 prompt bytes"},"sent":false}
"""

private struct DoctorFake {
    let vani: URL
    let dir: URL

    var argv: [String]? {
        guard let text = try? String(contentsOf: dir.appendingPathComponent("argv"), encoding: .utf8) else { return nil }
        return text.split(separator: "\n").map(String.init)
    }

    var env: String? {
        (try? String(contentsOf: dir.appendingPathComponent("env"), encoding: .utf8))?.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    func reply(_ json: String, status: Int32 = 0, stderr: String = "") throws {
        try Data(json.utf8).write(to: dir.appendingPathComponent("reply.json"))
        try Data("\(status)".utf8).write(to: dir.appendingPathComponent("status"))
        try Data(stderr.utf8).write(to: dir.appendingPathComponent("stderr"))
    }

    func client(timeout: TimeInterval = 10) -> DoctorClient {
        DoctorClient(runner: DraftRunner(vaniBinary: vani, key: { "sk-test-anthropic" }, decisionKey: { nil }),
                     quickTimeout: timeout, runTimeout: timeout)
    }
}

private func doctorFake(_ t: XCTestCase, reply: String = "{}", status: Int32 = 0, sleep: Int = 0) throws -> DoctorFake {
    let dir = try tempDir(t)
    let url = dir.appendingPathComponent("vani")
    let script = """
    #!/bin/sh
    d='\(dir.path)'
    : > "$d/argv"
    for a in "$@"; do printf '%s\\n' "$a" >> "$d/argv"; done
    printf 'key=%s nokeychain=%s\\n' "${ANTHROPIC_API_KEY:+set}" "${VANI_NO_KEYCHAIN:-unset}" > "$d/env"
    \(sleep > 0 ? "sleep \(sleep)" : "")
    cat "$d/stderr" >&2
    cat "$d/reply.json"
    exit "$(cat "$d/status")"
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    let fake = DoctorFake(vani: url, dir: dir)
    try fake.reply(reply, status: status)
    return fake
}

final class DoctorDecodingTests: XCTestCase {
    /// Go's golden reply fills every field; reading it proves each one maps.
    func testTheGoldenReplyDecodesWhole() throws {
        let r = try JobsClient.decoder.decode(DoctorReply.self, from: goldenReply())
        XCTAssertEqual(r.model, "claude-opus-5-5")
        XCTAssertEqual(r.runsRead, 30)
        XCTAssertEqual(r.runProblems, ["r9.json: unreadable"])
        XCTAssertEqual(r.boardsSignaled, 3)
        XCTAssertEqual(r.recoveredFromLogs, 1578)
        XCTAssertEqual(r.runsWithUnnamedBoardsAndNoLog, 1)
        XCTAssertEqual(r.dropsSampled, 3)
        XCTAssertEqual(r.seed, "2026-W46")
        XCTAssertEqual(r.strata.map(\.name), ["title:staff_plus", "model:role_fit"])
        XCTAssertEqual(r.dropsLeftOut, ["sponsorship": 5])
        XCTAssertEqual(r.worstCaseUSD, 1.234567, accuracy: 1e-9)
        XCTAssertEqual(r.requests.keys.sorted(), ["boards", "drops"])
        XCTAssertEqual(r.report, "/state/doctor/20261112T143005Z.md")
        XCTAssertTrue(r.sent)
        XCTAssertEqual(r.code, "runtime")
        XCTAssertNotNil(r.error)

        let rep = try XCTUnwrap(r.result)
        XCTAssertEqual(rep.at, ISO8601DateFormatter().date(from: "2026-11-12T14:30:05Z"))
        XCTAssertEqual(rep.through, ISO8601DateFormatter().date(from: "2026-11-12T12:30:05Z"))
        XCTAssertEqual(rep.boards.asked, 3)
        XCTAssertEqual(rep.boards.answered, 2)
        XCTAssertEqual(rep.boards.rawReply, #"{"boards":["#)
        XCTAssertEqual(rep.boards.patterns.first?.refs, ["wd:snapchat/snap"])
        XCTAssertEqual(rep.boards.patterns.first?.fix, "treat Listed:0 with Total>0 as a failure")
        XCTAssertEqual(rep.boardFindings.first?.nextStep, "re-read the board by hand")
        XCTAssertEqual(rep.boardsNotExamined, ["gh:canva"])
        XCTAssertEqual(rep.drops.error, "doctor: a key was not sent")
        XCTAssertEqual(rep.dropFindings.map(\.key), ["gh:acme:1", "lv:beta:2"])
        XCTAssertEqual(rep.dropsNotExamined, ["ab:gamma:3"])
        XCTAssertEqual(rep.dropsSent.count, 3)
        XCTAssertEqual(rep.sampled("ab:gamma:3")?.bodyMissing, true)
        XCTAssertEqual(rep.sampled("gh:acme:1")?.title, "Staff Engineer")
        let s = try XCTUnwrap(rep.signal(for: "wd:snapchat/snap"))
        XCTAssertEqual(s.company, "Snap")
        XCTAssertEqual(s.signals, ["zero_after_nonzero", "mass_missed"])
        XCTAssertEqual([s.runs, s.runsFromLog, s.reads, s.zeroReads, s.failures, s.medianListed, s.maxMissed], [3, 1, 3, 1, 1, 46, 42])
        XCTAssertEqual(s.latestListed, 0, "a latest listing of 0 is a count, not an absence")
        XCTAssertEqual(s.listedSeries, [46, 46, 0, -1])
        XCTAssertEqual(s.lastError, "context deadline exceeded")
        XCTAssertEqual(s.rawDetails.count, 1)
        XCTAssertEqual(rep.logs.lines.first?.count, 3)
        XCTAssertEqual([rep.logs.logsRead, rep.logs.linesSeen, rep.logs.distinct, rep.logs.leftOut, rep.logs.withheld, rep.logs.tooLong], [30, 41, 12, 1, 2, 1])
        XCTAssertEqual(rep.logs.unreadable, ["gone.log"])
        XCTAssertEqual(rep.logs.tailOnlyFor, ["big.log"])
        XCTAssertEqual(rep.unrecordedBoardsNewestRun, 4)
        XCTAssertEqual(rep.recoveredFromLogs, 1578)
        XCTAssertEqual(rep.runProblems.count, 1)
        XCTAssertEqual(rep.runsWithUnnamedBoardsAndNoLog, 1)
        XCTAssertFalse(rep.complete)
    }

    /// Go writes a nil slice as null; a list read as null or absent is empty, never a decode failure.
    func testNullAndAbsentListsReadAsEmpty() throws {
        let raw = """
        {"model":"m","worst_case_usd":0,"sent":true,"strata":null,"run_problems":null,
         "result":{"at":"2026-11-12T14:30:05.123456789Z","model":"m","boards":{"asked":1,"answered":1,"patterns":[{"title":"t","refs":null}]},
          "drops":{"asked":0,"answered":0},"board_findings":null,"signals":[{"board":"gh:x","signals":null,"listed_series":null}],
          "logs":{"lines":null,"unreadable":null,"tail_only_for":null}}}
        """
        let r = try JobsClient.decoder.decode(DoctorReply.self, from: Data(raw.utf8))
        let rep = try XCTUnwrap(r.result)
        XCTAssertTrue(r.strata.isEmpty && r.runProblems.isEmpty && rep.boardFindings.isEmpty && rep.logs.lines.isEmpty)
        XCTAssertEqual(rep.boards.patterns.first?.refs, [])
        XCTAssertNil(rep.signals.first?.latestListed)
        XCTAssertTrue(rep.complete)
        XCTAssertNil(rep.through)
    }

    /// A reply from a vani too old to say whether it sent anything is not read as "nothing sent".
    func testAReplyWithoutSentIsNotAReply() {
        let old = #"{"model":"m","runs_read":1,"worst_case_usd":1.0,"report":"/x.md"}"#
        XCTAssertNil(try? JobsClient.decoder.decode(DoctorReply.self, from: Data(old.utf8)))
        XCTAssertNil(try? JobsClient.decoder.decode(DoctorReply.self, from: Data(#"{"error":"x","code":"runtime"}"#.utf8)))
    }
}

final class DoctorClientTests: XCTestCase {
    func testTheEstimateSendsNoKeyAndAsksADryRun() throws {
        let fake = try doctorFake(self, reply: dryJSON)
        guard case let .success(plan) = fake.client().plan() else { return XCTFail("no plan") }
        XCTAssertEqual(fake.argv, ["doctor", "--json", "--dry-run"])
        XCTAssertEqual(fake.env, "key= nokeychain=1", "an estimate gets no key, and vani reads no Keychain")
        XCTAssertFalse(plan.sent)
        XCTAssertEqual(plan.dropsSampled, 40)
        XCTAssertFalse(plan.nothingToAsk)
    }

    func testTheRunPassesTheBudgetAndTheSeedItWasPricedWith() throws {
        let fake = try doctorFake(self, reply: try String(decoding: goldenReply(), as: UTF8.self))
        let out = fake.client().run(budgetUSD: 1.75, seed: "2026-W46")
        XCTAssertEqual(fake.argv, ["doctor", "--json", "--budget-usd", "1.75", "--seed", "2026-W46"])
        XCTAssertEqual(fake.env, "key=set nokeychain=1")
        guard case let .finished(r) = out else { return XCTFail("\(out)") }
        XCTAssertNotNil(r.result)
    }

    /// A partial review exits 1 with its report: the failure keeps the reply, so the report is shown.
    func testAPartialReviewIsAFailureThatKeepsItsReport() throws {
        let fake = try doctorFake(self, reply: try String(decoding: goldenReply(), as: UTF8.self), status: 1)
        guard case let .failed(f, reply) = fake.client().run(budgetUSD: 2, seed: nil) else { return XCTFail("not a failure") }
        XCTAssertEqual(f.code, "runtime")
        XCTAssertTrue(f.message.contains("judged 2 of the 3"), f.message)
        XCTAssertNotNil(reply?.result, "the partial report survives the failure")
        XCTAssertEqual(reply?.report, "/state/doctor/20261112T143005Z.md")
    }

    func testARefusalSaysNothingWasSent() throws {
        let refused = #"{"model":"m","runs_read":3,"boards_signaled":1,"drops_sampled":2,"worst_case_usd":3.5,"sent":false,"error":"doctor: the worst case $3.5000 is over --budget-usd $1.0000; nothing was sent","code":"runtime"}"#
        let fake = try doctorFake(self, reply: refused, status: 1)
        guard case let .failed(f, reply) = fake.client().run(budgetUSD: 1, seed: nil) else { return XCTFail("not a failure") }
        XCTAssertEqual(reply?.sent, false)
        XCTAssertNil(reply?.result)
        XCTAssertTrue(f.message.contains("over --budget-usd"))
    }

    func testABareFailureEnvelopeAndNoReplyAreFailuresWithoutAReply() throws {
        let fake = try doctorFake(self, reply: #"{"error":"doctor: no model","code":"runtime"}"#, status: 1)
        guard case let .failed(f, reply) = fake.client().run(budgetUSD: 1, seed: nil) else { return XCTFail("not a failure") }
        XCTAssertEqual(f.message, "doctor: no model")
        XCTAssertNil(reply)

        try fake.reply("not json", status: 2)
        guard case let .failure(g) = fake.client().plan() else { return XCTFail("garbage read as a plan") }
        XCTAssertEqual(g.code, "no_reply")
        XCTAssertTrue(g.message.contains("rebuild the app"), g.message)
    }

    func testAnAccountRefusalOnStderrIsNamed() throws {
        let fake = try doctorFake(self, reply: "", status: 1)
        try fake.reply("", status: 1, stderr: "anthropic-account: account org500 is not allowed to use this workspace\n")
        guard case let .failed(f, nil) = fake.client().run(budgetUSD: 1, seed: nil) else { return XCTFail("not a failure without a reply") }
        XCTAssertEqual(f.code, "account")
        XCTAssertTrue(f.message.hasPrefix("anthropic-account: account org500"), f.message)
        // A warning from the launcher is not a refusal.
        try fake.reply("", status: 1, stderr: "anthropic-account: warning: cache is stale\n")
        guard case let .failed(g, nil) = fake.client().run(budgetUSD: 1, seed: nil) else { return XCTFail("not a failure") }
        XCTAssertEqual(g.code, "no_reply")
    }

    func testABudgetOutOfRangeStartsNothing() throws {
        let fake = try doctorFake(self, reply: dryJSON)
        for b in [0, -1, 20.01, .nan, .infinity] {
            guard case let .failed(f, nil) = fake.client().run(budgetUSD: b, seed: nil) else { return XCTFail("\(b) ran") }
            XCTAssertEqual(f.code, "bad_request")
        }
        XCTAssertNil(fake.argv, "nothing started")
    }

    func testStoppingARunIsNotAFailure() throws {
        let fake = try doctorFake(self, reply: dryJSON, sleep: 30)
        let cancel = CancelFlag()
        let started = Date()
        DispatchQueue.global().asyncAfter(deadline: .now() + 0.5) { cancel.set() }
        XCTAssertEqual(fake.client().run(budgetUSD: 1, seed: nil, cancellation: cancel), .cancelled)
        XCTAssertLessThan(Date().timeIntervalSince(started), 10)
    }

    func testARunThatOutlivesItsBoundIsATimeout() throws {
        let fake = try doctorFake(self, reply: dryJSON, sleep: 30)
        guard case let .failed(f, nil) = fake.client(timeout: 1).run(budgetUSD: 1, seed: nil) else { return XCTFail("no timeout") }
        XCTAssertEqual(f.code, "timeout")
    }

    func testLastReadsTheNewestReportOrNone() throws {
        let fake = try doctorFake(self, reply: "{}")
        guard case let .success(none) = fake.client().last() else { return XCTFail("no reply") }
        XCTAssertNil(none.result)
        XCTAssertEqual(fake.argv, ["doctor", "--last", "--json"])
        XCTAssertEqual(fake.env, "key= nokeychain=1")

        let golden = try JSONSerialization.jsonObject(with: goldenReply()) as? [String: Any]
        let last = try JSONSerialization.data(withJSONObject: ["report": "/state/doctor/a.md", "result": golden?["result"] as Any])
        try fake.reply(String(decoding: last, as: UTF8.self))
        guard case let .success(l) = fake.client().last() else { return XCTFail("no reply") }
        XCTAssertEqual(l.report, "/state/doctor/a.md")
        XCTAssertEqual(l.result?.dropsSent.count, 3)

        try fake.reply(#"{"error":"doctor: the newest report 29991231T235959Z.json is unreadable","code":"runtime"}"#, status: 1)
        guard case let .failure(f) = fake.client().last() else { return XCTFail("an unreadable report read as none") }
        XCTAssertTrue(f.message.contains("unreadable"))
    }
}

final class DoctorPresentationTests: XCTestCase {
    func testTheSuggestedBudgetCoversTheWorstCaseAndStaysUnderTheCeiling() {
        XCTAssertEqual(DoctorPresentation.suggestedBudget(worstUSD: 1.21922), 1.75)
        XCTAssertEqual(DoctorPresentation.suggestedBudget(worstUSD: 0.001), 0.25)
        XCTAssertEqual(DoctorPresentation.suggestedBudget(worstUSD: 0), 0.25)
        XCTAssertEqual(DoctorPresentation.suggestedBudget(worstUSD: 19.9), 20)
        XCTAssertEqual(DoctorPresentation.suggestedBudget(worstUSD: 20), 20)
        for bad in [20.0001, -1, .nan, .infinity] { XCTAssertNil(DoctorPresentation.suggestedBudget(worstUSD: bad), "\(bad)") }
        for w in stride(from: 0.0, through: 20, by: 0.137) {
            let b = try? XCTUnwrap(DoctorPresentation.suggestedBudget(worstUSD: w))
            XCTAssertTrue(DoctorPresentation.covers(budget: b ?? 0, worstUSD: w), "\(w) -> \(String(describing: b))")
        }
    }

    func testCoversChecksTheBudgetAsItIsPassed() {
        XCTAssertTrue(DoctorPresentation.covers(budget: 1.22, worstUSD: 1.21922))
        XCTAssertFalse(DoctorPresentation.covers(budget: 1.214, worstUSD: 1.21922), "passed as 1.21, under the worst case")
        XCTAssertFalse(DoctorPresentation.covers(budget: 25, worstUSD: 1))
        XCTAssertFalse(DoctorPresentation.covers(budget: 0, worstUSD: 0))
        XCTAssertFalse(DoctorPresentation.covers(budget: .nan, worstUSD: 0))
    }

    func testAPartialReportSaysWhatWasNotJudged() throws {
        let rep = try XCTUnwrap(try JobsClient.decoder.decode(DoctorReply.self, from: goldenReply()).result)
        XCTAssertEqual(DoctorPresentation.headline(rep), "2 of 3 boards judged, 1 failing; 2 of 3 drops judged, 1 wrong")
        let gaps = DoctorPresentation.gaps(rep)
        XCTAssertEqual(gaps.count, 2)
        XCTAssertTrue(gaps[0].hasPrefix("The board review did not complete"))
        XCTAssertEqual(DoctorPresentation.orderedBoards(rep).first?.verdict, "silent_failure")
        XCTAssertEqual(DoctorPresentation.orderedDrops(rep).first?.verdict, "wrong_drop")
    }

    /// The Go side no longer sends `identifying_words_builtin_only` (a run without a words
    /// file is refused before anything is sent), so the reply carries no word-list warning.
    func testWarningsCarryNoWordListLineAndRunProblemsComeFirst() throws {
        let r = try JobsClient.decoder.decode(DoctorReply.self, from: goldenReply())
        let w = DoctorPresentation.warnings(r)
        XCTAssertEqual(w.count, 3)
        XCTAssertFalse(w.contains { $0.contains("identifying-words") })
        XCTAssertTrue(w[0].contains("unreadable and left out"))
        XCTAssertTrue(w[2].contains("5 dropped postings are never sampled"))
        let clean = try JobsClient.decoder.decode(DoctorReply.self, from: Data(dryJSON.utf8))
        XCTAssertTrue(DoctorPresentation.warnings(clean).isEmpty)
    }

    func testEveryVerdictCauseAndSignalHasWords() {
        for v in ["silent_failure", "transient_empty", "truly_empty", "loud_failure", "healthy", "unclear"] {
            XCTAssertNotEqual(DoctorPresentation.boardVerdict(v).label, v, v)
        }
        XCTAssertEqual(DoctorPresentation.boardVerdict("silent_failure").tone, .problem)
        XCTAssertEqual(DoctorPresentation.boardVerdict("truly_empty").tone, .fine)
        for v in ["correct_drop", "wrong_drop"] { XCTAssertNotEqual(DoctorPresentation.dropVerdict(v).label, v) }
        for c in ["title_rule_too_broad", "seniority_rule", "model_misread", "exclusion", "thin_description", "other"] {
            XCTAssertNotNil(DoctorPresentation.cause(c))
        }
        XCTAssertNil(DoctorPresentation.cause("none"))
        for s in ["zero_every_run", "zero_after_nonzero", "sharp_drop", "mass_missed", "chronic_failure", "failing_now", "never_recorded"] {
            XCTAssertNotEqual(DoctorPresentation.signal(s), s, s)
        }
    }

    /// Go's enumerations are the source: every verdict, cause and signal it can write has words here.
    func testTheWordsCoverGosEnumerations() throws {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent().appendingPathComponent("internal/doctor")
        let doctor = try String(contentsOf: root.appendingPathComponent("doctor.go"), encoding: .utf8)
        let signals = try String(contentsOf: root.appendingPathComponent("signals.go"), encoding: .utf8)
        func values(_ src: String, _ pattern: String) throws -> [String] {
            let re = try NSRegularExpression(pattern: pattern)
            return re.matches(in: src, range: NSRange(src.startIndex..., in: src)).compactMap { Range($0.range(at: 1), in: src).map { String(src[$0]) } }
        }
        let verdicts = try values(doctor, #"Verdict[A-Za-z]+\s*=\s*"([a-z_]+)""#)
        let sigs = try values(signals, #"Sig[A-Za-z]+\s*=\s*"([a-z_]+)""#)
        let causesLine = try XCTUnwrap(try values(doctor, #"dropCauses\s*=\s*\[\]string\{([^}]*)\}"#).first)
        let causes = try values(causesLine, #""([a-z_]+)""#)
        XCTAssertEqual(verdicts.count, 6)
        XCTAssertEqual(sigs.count, 7)
        XCTAssertEqual(causes.count, 7)
        for v in verdicts where v != "unclear" { XCTAssertNotEqual(DoctorPresentation.boardVerdict(v).label, v, v) }
        for s in sigs { XCTAssertNotEqual(DoctorPresentation.signal(s), s, s) }
        for c in causes where c != "none" { XCTAssertNotEqual(DoctorPresentation.cause(c), c, c) }
    }
}
