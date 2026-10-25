import Foundation
import XCTest
@testable import VaniApplyKit

// `vani jobs retriage --batch`: decoding, the line splitter, the client against a fake vani, the
// stdout hook it rides on, and the words. No test reads the Keychain, a key or the owner's state.

private let en = Locale(identifier: "en_US")

private func item(_ key: String, before: String = "adjacent", after: String = "strong_fit", usd: Double = 0.004) -> String {
    """
    {"event":"item","key":"\(key)","result":{"key":"\(key)","model":"claude-sonnet-5-5","status":"scored",\
    "before":{"slots":{"role_fit":{"value":"\(before)"}},"relevance":50,"priority":40},\
    "after":{"slots":{"role_fit":{"value":"\(after)","backend":"manual"}},"relevance":60,"priority":45},\
    "cost":{"input_tokens":1000,"output_tokens":100,"usd":\(usd)}}}
    """
}

private let planJSON = """
{"batch":true,"dry_run":true,"model":"claude-sonnet-5-5","max_keys":500,"eligible":99,"already_read":99,
 "items":[{"key":"gh:a:1"},{"key":"gh:a:2","read":"current"},{"key":"gh:a:3","read":"stale"},
  {"key":"gh:a:4","skip":"untriaged","reason":"no run has triaged it yet"},{"key":"gh:a:5","skip":"text_gone","reason":"no text"}],
 "estimate":{"input_tokens":3300,"typical_usd":0.012,"max_usd":0.045,"daily_cap_usd":30,"spent_usd":10,"remaining_usd":20,
  "cap_reached":false,"postings":3,"fit_at_max":3},
 "estimate_unread":{"input_tokens":2200,"typical_usd":0.008,"max_usd":0.03,"daily_cap_usd":30,"spent_usd":10,"remaining_usd":20,
  "cap_reached":false,"postings":2,"fit_at_max":2}}
"""

final class RetriageBatchDecodingTests: XCTestCase {
    func testAPlanDecodesAndRecountsFromItsItems() throws {
        let plan = try JobsClient.decoder.decode(RetriageBatchPlan.self, from: Data(planJSON.utf8))
        XCTAssertEqual(plan.items.count, 5)
        XCTAssertEqual(plan.eligible, 3, "counted from the items, not the reply's own (99)")
        XCTAssertEqual(plan.alreadyRead, 1, "only a current reading counts as read")
        XCTAssertEqual(plan.keysToAsk(skipRead: false), ["gh:a:1", "gh:a:2", "gh:a:3"])
        XCTAssertEqual(plan.keysToAsk(skipRead: true), ["gh:a:1", "gh:a:3"])
        XCTAssertEqual(plan.estimate(skipRead: false)?.postings, 3)
        XCTAssertEqual(plan.estimate(skipRead: true)?.postings, 2)
        XCTAssertEqual(plan.estimate.fitAtMax, 3)
    }

    func testASinglePostingReplyOrOneWithoutAnEstimateIsNotAPlan() {
        let single = #"{"key":"gh:a:1","dry_run":true,"estimate":{"max_usd":0.01}}"#
        let noCost = #"{"batch":true,"items":[]}"#
        let notBatch = #"{"batch":false,"items":[],"estimate":{}}"#
        for raw in [single, noCost, notBatch, "[]", ""] {
            XCTAssertNil(try? JobsClient.decoder.decode(RetriageBatchPlan.self, from: Data(raw.utf8)), raw)
        }
    }

    func testSkippingReadWithNoSeparateEstimateIsUnknownNotTheFullOne() throws {
        let raw = #"{"batch":true,"items":[{"key":"a","read":"current"},{"key":"b"}],"estimate":{"max_usd":0.02,"typical_usd":0.01}}"#
        let plan = try JobsClient.decoder.decode(RetriageBatchPlan.self, from: Data(raw.utf8))
        XCTAssertNil(plan.estimate(skipRead: true))
        XCTAssertNotNil(plan.estimate(skipRead: false))
    }

    func testEveryEventKindDecodes() throws {
        let start = #"{"event":"start","model":"claude-sonnet-5-5","total":2,"parallel":3,"skipped":[{"key":"gh:a:9","skip":"untriaged"}]}"#
        XCTAssertEqual(RetriageBatchEvent.decode(line: Data(start.utf8)),
                       .start(model: "claude-sonnet-5-5", total: 2, skipped: [RetriageBatchItem(key: "gh:a:9", skip: "untriaged")]))

        guard case let .item(key, .success(r))? = RetriageBatchEvent.decode(line: Data(item("gh:a:1").utf8)) else {
            return XCTFail("an item with a result")
        }
        XCTAssertEqual(key, "gh:a:1")
        XCTAssertEqual(r.after.slots[.roleFit]?.value, "strong_fit")
        XCTAssertEqual(r.cost?.usd, 0.004)

        let fail = #"{"event":"item","key":"gh:a:2","error":"Sonnet declined","code":"refused","cost":{"usd":0.003}}"#
        XCTAssertEqual(RetriageBatchEvent.decode(line: Data(fail.utf8)),
                       .item(key: "gh:a:2", outcome: .failure(RetriageFailure(code: "refused", message: "Sonnet declined", cost: RetriageCost(usd: 0.003)))))

        let done = #"{"event":"done","summary":{"kept":1,"failed":1,"not_asked":2,"skipped":1,"changed":1,"usd":0.007,"stopped":"cap_reached","message":"cap","not_asked_keys":["x","y"]}}"#
        XCTAssertEqual(RetriageBatchEvent.decode(line: Data(done.utf8)),
                       .done(RetriageBatchSummary(kept: 1, failed: 1, notAsked: 2, skipped: 1, changed: 1, usd: 0.007,
                                                  stopped: "cap_reached", message: "cap", notAskedKeys: ["x", "y"])))
    }

    func testLinesThatAreNotEventsAreSkipped() {
        let bad = [
            "", "not json", "[]", #"{"event":"later_kind"}"#,
            #"{"event":"item","error":"x"}"#,                       // no key
            #"{"event":"item","key":"gh:a:1"}"#,                    // neither result nor error
            #"{"event":"done"}"#,                                   // no summary
            item("gh:a:1").replacingOccurrences(of: #"{"key":"gh:a:1","model""#, with: #"{"key":"gh:a:2","model""#), // result for another key
        ]
        for line in bad { XCTAssertNil(RetriageBatchEvent.decode(line: Data(line.utf8)), line) }
    }

    func testNegativeOrMissingCountsReadAsZero() throws {
        let raw = #"{"kept":-1,"failed":"two","usd":-5,"stopped":""}"#
        let s = try JSONDecoder().decode(RetriageBatchSummary.self, from: Data(raw.utf8))
        XCTAssertEqual(s, RetriageBatchSummary())
    }
}

final class LineSplitterTests: XCTestCase {
    func testLinesSplitAcrossChunksAndEmptyLinesAreLeftOut() {
        var s = LineSplitter(maxLine: 100)
        XCTAssertEqual(s.feed(Data("ab".utf8)), [])
        XCTAssertEqual(s.feed(Data("c\n\nde\nf".utf8)).map { String(decoding: $0, as: UTF8.self) }, ["abc", "de"])
        XCTAssertEqual(s.feed(Data("g\n".utf8)).map { String(decoding: $0, as: UTF8.self) }, ["fg"])
        XCTAssertEqual(s.tail, Data())
    }

    func testAnOversizedLineIsDroppedWholeAndTheNextOneStillReads() {
        var s = LineSplitter(maxLine: 4)
        XCTAssertEqual(s.feed(Data("abc".utf8)), [])
        XCTAssertEqual(s.feed(Data("defgh".utf8)), [], "past the ceiling: the line is being dropped")
        XCTAssertEqual(s.feed(Data("ij\nok\n".utf8)).map { String(decoding: $0, as: UTF8.self) }, ["ok"])
        XCTAssertEqual(s.dropped, 1)
        XCTAssertEqual(s.feed(Data("12345\nyes\n".utf8)).map { String(decoding: $0, as: UTF8.self) }, ["yes"])
        XCTAssertEqual(s.dropped, 2)
    }

    func testATornLastLineStaysInTheTail() {
        var s = LineSplitter(maxLine: 100)
        XCTAssertEqual(s.feed(Data("{\"event\":\"st".utf8)), [])
        XCTAssertEqual(String(decoding: s.tail, as: UTF8.self), "{\"event\":\"st")
    }
}

/// A fake `vani jobs retriage --batch`: records argv, prints `out` (lines), optionally waits for a
/// `go` file before printing `after`, then exits with `status`.
private struct BatchFake {
    let vani: URL
    let dir: URL

    var argv: [String] {
        ((try? String(contentsOf: dir.appendingPathComponent("argv"), encoding: .utf8)) ?? "")
            .split(separator: "\n").map(String.init)
    }

    var env: String {
        ((try? String(contentsOf: dir.appendingPathComponent("env"), encoding: .utf8)) ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    }

    func go() throws { try Data().write(to: dir.appendingPathComponent("go")) }
}

private func batchFake(_ t: XCTestCase, out: [String], after: [String] = [], wait: Bool = false, status: Int32 = 0) throws -> BatchFake {
    let dir = try tempDir(t)
    let url = dir.appendingPathComponent("vani")
    try Data((out.joined(separator: "\n") + (out.isEmpty ? "" : "\n")).utf8).write(to: dir.appendingPathComponent("out"))
    try Data((after.joined(separator: "\n") + (after.isEmpty ? "" : "\n")).utf8).write(to: dir.appendingPathComponent("after"))
    let script = """
    #!/bin/sh
    d='\(dir.path)'
    : > "$d/argv"
    for a in "$@"; do printf '%s\\n' "$a" >> "$d/argv"; done
    printf 'key=%s nokeychain=%s\\n' "${ANTHROPIC_API_KEY:+set}" "${VANI_NO_KEYCHAIN:-unset}" > "$d/env"
    cat "$d/out"
    \(wait ? "i=0; while [ ! -f \"$d/go\" ] && [ $i -lt 100 ]; do sleep 0.05; i=$((i+1)); done" : "")
    cat "$d/after"
    exit \(status)
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return BatchFake(vani: url, dir: dir)
}

/// Events delivered to `onEvent`, across threads.
private final class Events: @unchecked Sendable {
    private let lock = NSLock()
    private var list: [RetriageBatchEvent] = []
    var all: [RetriageBatchEvent] { lock.lock(); defer { lock.unlock() }; return list }
    func add(_ e: RetriageBatchEvent) { lock.lock(); list.append(e); lock.unlock() }
}

final class RetriageBatchClientTests: XCTestCase {
    private func client(_ f: BatchFake, perPosting: TimeInterval = 5) -> RetriageBatchClient {
        RetriageBatchClient(runner: DraftRunner(vaniBinary: f.vani, keys: .noKey, key: { "sk-test" }, decisionKey: { nil }),
                            perPosting: perPosting, quickTimeout: 5)
    }

    func testArgumentsAreExact() {
        XCTAssertEqual(RetriageBatchClient.arguments(["a", "b"], dryRun: true, skipRead: false),
                       ["jobs", "retriage", "--json", "--batch", "--dry-run", "a", "b"])
        XCTAssertEqual(RetriageBatchClient.arguments(["a"], dryRun: false, skipRead: true),
                       ["jobs", "retriage", "--json", "--batch", "--skip-read", "a"])
    }

    func testBadKeysAreNeverPassed() throws {
        let f = try batchFake(self, out: [planJSON])
        let c = client(f)
        for keys in [[], ["ok", "-flag"], ["ok", "bad\nkey"], Array(repeating: "k", count: RetriageBatchClient.maxKeys + 1)] {
            guard case let .failure(fail) = c.plan(keys) else { return XCTFail("\(keys.prefix(3)) was passed") }
            XCTAssertEqual(fail.code, "bad_request")
        }
        XCTAssertTrue(f.argv.isEmpty, "vani was started")
    }

    func testAPlanAsksWithNoKeyAndDecodes() throws {
        let f = try batchFake(self, out: [planJSON])
        guard case let .success(plan) = client(f).plan(["gh:a:1", "gh:a:2", "gh:a:3", "gh:a:4", "gh:a:5"]) else { return XCTFail() }
        XCTAssertEqual(plan.eligible, 3)
        XCTAssertEqual(f.argv, ["jobs", "retriage", "--json", "--batch", "--dry-run", "gh:a:1", "gh:a:2", "gh:a:3", "gh:a:4", "gh:a:5"])
        XCTAssertEqual(f.env, "key= nokeychain=1", "an estimate is given no key")
    }

    func testAPlanNamingARoleNotAskedAboutIsRefused() throws {
        let f = try batchFake(self, out: [planJSON])
        guard case let .failure(fail) = client(f).plan(["gh:a:1"]) else { return XCTFail() }
        XCTAssertEqual(fail.code, "unreadable_reply")
    }

    func testAPlanErrorKeepsVanisCode() throws {
        let f = try batchFake(self, out: [#"{"error":"no llm provider","code":"no_llm"}"#], status: 1)
        guard case let .failure(fail) = client(f).plan(["gh:a:1"]) else { return XCTFail() }
        XCTAssertEqual(fail, RetriageFailure(code: "no_llm", message: "no llm provider"))
    }

    func testAnOldVaniIsSaidSo() throws {
        let f = try batchFake(self, out: [], status: 2)
        guard case let .failure(fail) = client(f).plan(["gh:a:1"]) else { return XCTFail() }
        XCTAssertEqual(fail.code, "no_reply")
        XCTAssertTrue(fail.message.contains("rebuild the app"), fail.message)
    }

    /// The events reach the caller while vani is still running: the fake prints the start and one item,
    /// then waits for a file this test writes only after it has seen them.
    func testARunStreamsItsEventsBeforeItEnds() throws {
        let done = #"{"event":"done","summary":{"kept":2,"failed":0,"not_asked":0,"skipped":0,"changed":2,"usd":0.008}}"#
        let f = try batchFake(self, out: [#"{"event":"start","model":"m","total":2}"#, item("gh:a:1")],
                              after: [item("gh:a:2"), done], wait: true)
        let events = Events()
        let seenTwo = expectation(description: "two events before the end")
        let flag = OnceFlag()
        let c = client(f)
        let finished = expectation(description: "run returned")
        let result = ResultBox()
        DispatchQueue.global().async {
            result.value = c.run(["gh:a:1", "gh:a:2"], skipRead: true) { ev in
                events.add(ev)
                if events.all.count == 2, flag.first() { seenTwo.fulfill() }
            }
            finished.fulfill()
        }
        wait(for: [seenTwo], timeout: 4)
        try f.go()
        wait(for: [finished], timeout: 6)
        XCTAssertEqual(events.all.count, 4)
        guard case let .success(summary)? = result.value else { return XCTFail("\(String(describing: result.value))") }
        XCTAssertEqual(summary.kept, 2)
        XCTAssertEqual(f.argv, ["jobs", "retriage", "--json", "--batch", "--skip-read", "gh:a:1", "gh:a:2"])
        XCTAssertEqual(f.env, "key=set nokeychain=1", "a run gets the draft key, and vani reads no Keychain of its own")
    }

    func testAnItemForAKeyNotAskedIsDropped() throws {
        let done = #"{"event":"done","summary":{"kept":1}}"#
        let f = try batchFake(self, out: [item("gh:other:1"), item("gh:a:1"), "garbage", done])
        let events = Events()
        _ = client(f).run(["gh:a:1"], skipRead: false) { events.add($0) }
        let keys = events.all.compactMap { if case let .item(k, _) = $0 { k } else { nil } }
        XCTAssertEqual(keys, ["gh:a:1"])
    }

    func testAStoppedBatchStillReportsItsSummary() throws {
        let done = #"{"event":"done","summary":{"kept":1,"failed":1,"not_asked":3,"stopped":"cap_reached","message":"cap"}}"#
        let f = try batchFake(self, out: [#"{"event":"start","total":5}"#, item("gh:a:1"), done], status: 1)
        guard case let .success(s) = client(f).run(["gh:a:1"], skipRead: false, onEvent: { _ in }) else { return XCTFail() }
        XCTAssertEqual(s.stopped, "cap_reached")
        XCTAssertEqual(s.notAsked, 3)
    }

    func testAWholeBatchErrorIsAFailureWithItsCode() throws {
        let f = try batchFake(self, out: [#"{"error":"another manual reading is being asked","code":"busy"}"#], status: 1)
        guard case let .failure(fail) = client(f).run(["gh:a:1"], skipRead: false, onEvent: { _ in }) else { return XCTFail() }
        XCTAssertEqual(fail.code, "busy")
    }

    func testARunThatEndsWithoutDoneIsNoReply() throws {
        let f = try batchFake(self, out: [#"{"event":"start","total":1}"#, #"{"event":"ite"#], status: 0)
        guard case let .failure(fail) = client(f).run(["gh:a:1"], skipRead: false, onEvent: { _ in }) else { return XCTFail() }
        XCTAssertEqual(fail.code, "no_reply")
    }

    func testStopEndsTheChildAndIsCancelled() throws {
        let f = try batchFake(self, out: [#"{"event":"start","total":2}"#], after: [], wait: true)
        let cancel = CancelFlag()
        let started = expectation(description: "start seen")
        let once = OnceFlag()
        let c = client(f)
        let result = ResultBox()
        let finished = expectation(description: "returned")
        DispatchQueue.global().async {
            result.value = c.run(["gh:a:1"], skipRead: false, cancellation: cancel) { _ in if once.first() { started.fulfill() } }
            finished.fulfill()
        }
        wait(for: [started], timeout: 4)
        cancel.set()
        wait(for: [finished], timeout: 4)
        guard case .cancelled? = result.value else { return XCTFail("\(String(describing: result.value))") }
    }

    func testTheRunCeilingScalesWithTheBatchAndIsBounded() throws {
        let c = try client(batchFake(self, out: []), perPosting: 90)
        XCTAssertEqual(c.runTimeout(postings: 1), 150)
        XCTAssertEqual(c.runTimeout(postings: 3), 150)
        XCTAssertEqual(c.runTimeout(postings: 4), 240)
        XCTAssertEqual(c.runTimeout(postings: 10_000), 5 * 3600)
    }
}

private final class OnceFlag: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false
    func first() -> Bool { lock.lock(); defer { lock.unlock() }; if done { return false }; done = true; return true }
}

private final class ResultBox: @unchecked Sendable {
    private let lock = NSLock()
    private var v: RetriageOutcome<RetriageBatchSummary>?
    var value: RetriageOutcome<RetriageBatchSummary>? {
        get { lock.lock(); defer { lock.unlock() }; return v }
        set { lock.lock(); v = newValue; lock.unlock() }
    }
}

final class VaniProcessStreamTests: XCTestCase {
    /// The hook sees every byte, in order, past the ceiling on what the outcome keeps.
    func testOnStdoutSeesEveryChunkPastTheCeiling() throws {
        let dir = try tempDir(self)
        let sh = dir.appendingPathComponent("emit")
        try Data("#!/bin/sh\ni=0\nwhile [ $i -lt 2000 ]; do echo line-$i; i=$((i+1)); done\n".utf8).write(to: sh)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: sh.path)
        let seen = Bytes()
        let out = VaniProcess.run(sh, arguments: [], environment: VaniProcess.environment(configPath: nil, stateDir: nil),
                                  timeout: 10, maxOutputBytes: 100, onStdout: { seen.add($0) })
        XCTAssertTrue(out.truncated)
        XCTAssertEqual(out.stdout.count, 100)
        let text = String(decoding: seen.all, as: UTF8.self)
        let lines = text.split(separator: "\n")
        XCTAssertEqual(lines.count, 2000)
        XCTAssertEqual(lines.first, "line-0")
        XCTAssertEqual(lines.last, "line-1999")
    }
}

private final class Bytes: @unchecked Sendable {
    private let lock = NSLock()
    private var d = Data()
    var all: Data { lock.lock(); defer { lock.unlock() }; return d }
    func add(_ x: Data) { lock.lock(); d.append(x); lock.unlock() }
}

final class RetriageBatchPresentationTests: XCTestCase {
    private func plan(_ raw: String = planJSON) throws -> RetriageBatchPlan {
        try JobsClient.decoder.decode(RetriageBatchPlan.self, from: Data(raw.utf8))
    }

    func testTheConfirmationSaysTheCostSkipsAndWhatIsAlreadyRead() throws {
        let c = RetriageBatchPresentation.confirmation(for: try plan(), skipRead: true, locale: en)
        XCTAssertEqual(c.title, "Ask Sonnet about 2 roles")
        XCTAssertEqual(c.keys, ["gh:a:1", "gh:a:3"])
        XCTAssertEqual(c.costText, "About $0.008 typically, at most $0.030 · 2,200 input tokens · daily cap $30, $20 left")
        XCTAssertEqual(c.alreadyReadText, "1 role already has a current Sonnet reading.")
        XCTAssertEqual(c.skippedLines, ["1 role skipped: not triaged yet", "1 role skipped: no posting text left (the role closed)"])
        XCTAssertTrue(c.canConfirm)
        XCTAssertNil(c.capWarning)

        let all = RetriageBatchPresentation.confirmation(for: try plan(), skipRead: false, locale: en)
        XCTAssertEqual(all.title, "Ask Sonnet about 3 roles")
        XCTAssertTrue(all.costText.hasPrefix("About $0.012 typically, at most $0.045"))
    }

    func testTooLittleCapLeftWarnsAndASpentCapBlocks() throws {
        let tight = planJSON.replacingOccurrences(of: #""postings":3,"fit_at_max":3"#, with: #""postings":3,"fit_at_max":1"#)
        let w = RetriageBatchPresentation.confirmation(for: try plan(tight), skipRead: false, locale: en)
        XCTAssertTrue(w.canConfirm)
        XCTAssertEqual(w.capWarning?.hasPrefix("At their most, only 1 of 3 fit"), true)

        let spent = planJSON.replacingOccurrences(of: #""cap_reached":false,"postings":3"#, with: #""cap_reached":true,"postings":3"#)
        let b = RetriageBatchPresentation.confirmation(for: try plan(spent), skipRead: false, locale: en)
        XCTAssertFalse(b.canConfirm)
        XCTAssertEqual(b.blockedReason?.contains("daily cap is spent"), true)
    }

    func testNothingLeftToAskIsBlockedAndSaysHow() throws {
        let raw = #"{"batch":true,"items":[{"key":"a","read":"current"},{"key":"b","skip":"untriaged"}],"estimate":{"max_usd":0.01,"typical_usd":0.004},"estimate_unread":{}}"#
        let c = RetriageBatchPresentation.confirmation(for: try plan(raw), skipRead: true, locale: en)
        XCTAssertFalse(c.canConfirm)
        XCTAssertEqual(c.title, "Nothing to ask Sonnet")
        XCTAssertEqual(c.blockedReason?.contains("Read them again"), true)
        let none = #"{"batch":true,"items":[{"key":"b","skip":"untriaged"}],"estimate":{}}"#
        XCTAssertEqual(RetriageBatchPresentation.confirmation(for: try plan(none), skipRead: true).blockedReason,
                       "None of the selected roles can be read.")
    }

    func testTheSelectionIsCountedAndBlockedHonestly() {
        var read = Job(key: "a", company: "A", title: "T")
        read.manualTriage = ManualTriage(slots: ["role_fit": "adjacent"], current: true)
        var stale = Job(key: "b", company: "B", title: "T")
        stale.manualTriage = ManualTriage(slots: ["role_fit": "adjacent"], current: false)
        var waiting = Job(key: "c", company: "C", title: "T")
        waiting.untriaged = true
        let s = RetriageBatchPresentation.selection([read, stale, waiting])
        XCTAssertEqual(s, .init(total: 3, untriaged: 1, alreadyRead: 1))
        XCTAssertEqual(RetriageBatchPresentation.selectionLine(s), "3 roles selected · 1 not triaged · 1 already read by Sonnet")
        XCTAssertEqual(RetriageBatchPresentation.actionTitle(s), "Re-triage 2 roles with Sonnet…")
        XCTAssertNil(RetriageBatchPresentation.blockedReason(s, busy: false))
        XCTAssertNotNil(RetriageBatchPresentation.blockedReason(s, busy: true))
        XCTAssertNotNil(RetriageBatchPresentation.blockedReason(RetriageBatchPresentation.selection([waiting, waiting]), busy: false))
        XCTAssertNotNil(RetriageBatchPresentation.blockedReason(RetriageBatchPresentation.selection([]), busy: false))
    }

    func testTheSummarySaysWhatChangedFailedAndWhyItStopped() {
        let s = RetriageBatchSummary(kept: 9, failed: 1, notAsked: 2, skipped: 3, changed: 4, usd: 0.04, stopped: "cap_reached")
        XCTAssertEqual(RetriageBatchPresentation.summaryLine(s), "Sonnet read 9 of 12 roles · role fit changed on 4")
        let d = RetriageBatchPresentation.summaryDetails(s)
        XCTAssertEqual(d[0], "1 role could not be read; nothing was saved for it.")
        XCTAssertEqual(d[1], "2 roles not asked: today's Claude spending cap was reached (decide.daily_usd.llm).")
        XCTAssertEqual(d[2], "3 roles skipped before asking.")
        XCTAssertEqual(d[3], "Charged about $0.040 in all.")
        XCTAssertEqual(d.last, RetriageWords.ruleSeven)
        XCTAssertEqual(RetriageBatchPresentation.summaryLine(RetriageBatchSummary(kept: 2, changed: 0)), "Sonnet read 2 of 2 roles · no role fit changed")
        XCTAssertEqual(RetriageBatchPresentation.summaryDetails(RetriageBatchSummary(kept: 1))[0], "Vani did not report the cost.")
    }

    func testProgressLine() {
        XCTAssertEqual(RetriageBatchPresentation.progressLine(done: 6, failed: 1, total: 12, usd: 0.031), "7 of 12 read · 1 failed · $0.031 so far")
        XCTAssertEqual(RetriageBatchPresentation.progressLine(done: 0, failed: 0, total: 3, usd: 0), "0 of 3 read")
    }
}
