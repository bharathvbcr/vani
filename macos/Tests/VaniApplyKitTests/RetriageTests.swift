import Foundation
import XCTest
@testable import VaniApplyKit

/// A stand-in for `vani jobs retriage`: records its argv and the environment that matters, then
/// prints the reply and exit status a test chose. Never reads the Keychain or the owner's state.
private struct RetriageFake {
    let vani: URL
    let dir: URL

    var argv: [String]? {
        guard let text = try? String(contentsOf: dir.appendingPathComponent("argv"), encoding: .utf8) else { return nil }
        return text.split(separator: "\n", omittingEmptySubsequences: true).map(String.init)
    }

    var env: String? {
        (try? String(contentsOf: dir.appendingPathComponent("env"), encoding: .utf8))?.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    func reply(_ json: String, status: Int32 = 0) throws {
        try Data(json.utf8).write(to: dir.appendingPathComponent("reply.json"))
        try Data("\(status)".utf8).write(to: dir.appendingPathComponent("status"))
    }
}

private func retriageFake(_ t: XCTestCase, reply: String = "{}", status: Int32 = 0) throws -> RetriageFake {
    let dir = try tempDir(t)
    let url = dir.appendingPathComponent("vani")
    let script = """
    #!/bin/sh
    d='\(dir.path)'
    : > "$d/argv"
    for a in "$@"; do printf '%s\\n' "$a" >> "$d/argv"; done
    printf 'key=%s nokeychain=%s decision=%s cfg=%s state=%s\\n' "${ANTHROPIC_API_KEY:+set}" "${VANI_NO_KEYCHAIN:-unset}" "${OPENROUTER_API_KEY:+set}" "${VANI_CONFIG:-none}" "${VANI_STATE_DIR:-none}" > "$d/env"
    cat "$d/reply.json"
    exit "$(cat "$d/status")"
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    let fake = RetriageFake(vani: url, dir: dir)
    try fake.reply(reply, status: status)
    return fake
}

/// Counts Keychain-shaped lookups, so a test can say none happened.
private final class Lookups: @unchecked Sendable {
    private let lock = NSLock()
    private var n = 0
    var count: Int { lock.lock(); defer { lock.unlock() }; return n }
    func hit(_ value: String?) -> String? { lock.lock(); n += 1; lock.unlock(); return value }
}

private let dryJSON = """
{"key":"gh:acme:1","dry_run":true,"model":"claude-sonnet-5-5",
 "estimate":{"input_tokens":1800,"typical_output_tokens":150,"max_output_tokens":1280,
  "typical_usd":0.0053,"max_usd":0.0164,"price_source":"Anthropic's published rates for claude-sonnet-5-5",
  "daily_cap_usd":20,"spent_usd":1.25,"remaining_usd":18.75,"cap_reached":false,"future":1},
 "manual_triage":{"slots":{"role_fit":"adjacent","seniority":"mid","sponsorship":"not_stated","work_mode":"remote"},
  "model":"claude-sonnet-5-5","at":"2026-10-24T09:00:00Z","current":true}}
"""

private let resultJSON = """
{"key":"gh:acme:1","model":"claude-sonnet-5-5","status":"scored",
 "before":{"slots":{"role_fit":{"value":"adjacent","backend":"d1-ultra","p":0.61},"seniority":{"value":"mid"},
   "sponsorship":{},"work_mode":{"value":"remote","backend":"escalate"}},"relevance":71.2,"priority":55.1},
 "after":{"slots":{"role_fit":{"value":"strong_fit","backend":"manual"},"seniority":{"value":"mid","backend":"manual"},
   "sponsorship":{"value":"not_stated","backend":"manual"},"work_mode":{"value":"remote","backend":"manual"}},
   "relevance":74.0,"priority":60.2,"model":"claude-sonnet-5-5","at":"2026-10-24T10:00:00Z"},
 "cost":{"input_tokens":1790,"output_tokens":212,"usd":0.0072},"unknown":{"x":1}}
"""

private let en = Locale(identifier: "en_US")

private func runner(_ fake: RetriageFake, keys: DraftRunner.KeySource = .noKey, config: String? = nil, state: String? = nil,
                    key: Lookups = Lookups(), decision: Lookups = Lookups(), anthropicKey: String? = "sk-test-anthropic") -> DraftRunner {
    DraftRunner(vaniBinary: fake.vani, configPath: config, stateDir: state, keys: keys,
                key: { key.hit(anthropicKey) }, decisionKey: { decision.hit("sk-test-openrouter") })
}

final class RetriageDecodingTests: XCTestCase {
    private func job(_ extra: String) throws -> Job {
        try JobsClient.decoder.decode(Job.self, from: Data(#"{"key":"gh:acme:1","company":"Acme","title":"Engineer","status":"scored"\#(extra)}"#.utf8))
    }

    func testAFullManualTriageDecodes() throws {
        let j = try job(#","manual_triage":{"slots":{"role_fit":"strong_fit","seniority":"mid","sponsorship":"not_stated","work_mode":"remote"},"model":"claude-sonnet-5-5","at":"2026-10-24T10:00:00Z","current":true,"future":[1]}"#)
        let m = try XCTUnwrap(j.manualTriage)
        XCTAssertEqual(m.value(for: .roleFit), "strong_fit")
        XCTAssertEqual(m.value(for: .workMode), "remote")
        XCTAssertEqual(m.model, "claude-sonnet-5-5")
        XCTAssertEqual(m.at, ISO8601DateFormatter().date(from: "2026-10-24T10:00:00Z"))
        XCTAssertTrue(m.current)
        XCTAssertNil(m.staleReason)
    }

    func testAMinimalManualTriageDecodesAndAbsentIsNil() throws {
        let m = try XCTUnwrap(try job(#","manual_triage":{"slots":{"role_fit":"adjacent"}}"#).manualTriage)
        XCTAssertEqual(m.slots, ["role_fit": "adjacent"])
        XCTAssertNil(m.model)
        XCTAssertNil(m.at)
        XCTAssertFalse(m.current, "a reading vani did not call current is not shown as current")
        XCTAssertNil(try job("").manualTriage)
    }

    func testAStaleManualTriageKeepsItsReason() throws {
        let m = try XCTUnwrap(try job(#","manual_triage":{"slots":{"role_fit":"adjacent"},"current":false,"stale_reason":"text_changed"}"#).manualTriage)
        XCTAssertFalse(m.current)
        XCTAssertEqual(m.staleReason, "text_changed")
    }

    /// A malformed object must read as no reading and never fail the job, let alone the list.
    func testAMalformedManualTriageNeverFailsTheJob() throws {
        for bad in [#""nope""#, "42", "[1,2]", "null", "{}", #"{"slots":"x"}"#, #"{"slots":{}}"#, #"{"slots":{"role_fit":7,"seniority":null}}"#,
                    #"{"slots":{"bogus":"x"}}"#, #"{"slots":[1]}"#, "true"] {
            let j = try job(#","manual_triage":\#(bad)"#)
            XCTAssertNil(j.manualTriage, bad)
            XCTAssertEqual(j.key, "gh:acme:1", bad)
        }
        // A field of the wrong type costs only that field.
        let m = try XCTUnwrap(try job(#","manual_triage":{"slots":{"role_fit":"adjacent","work_mode":3},"model":9,"at":"yesterday","current":"yes","stale_reason":5}"#).manualTriage)
        XCTAssertEqual(m.slots, ["role_fit": "adjacent"])
        XCTAssertNil(m.model)
        XCTAssertNil(m.at)
        XCTAssertFalse(m.current)
        XCTAssertNil(m.staleReason)
    }

    func testAListWithAMalformedManualTriageStillDecodes() throws {
        let page = try JobsClient.decoder.decode(JobsPage.self, from: Data(#"""
        {"view":"new","jobs":[{"key":"a","company":"A","title":"T","status":"scored","manual_triage":"x"},
         {"key":"b","company":"B","title":"T","status":"scored","manual_triage":{"slots":{"role_fit":"adjacent"},"current":true}}],
         "total":2,"shown":2,"counts":{}}
        """#.utf8))
        XCTAssertEqual(page.jobs.map(\.key), ["a", "b"])
        XCTAssertNil(page.jobs[0].manualTriage)
        XCTAssertEqual(page.jobs[1].manualTriage?.value(for: .roleFit), "adjacent")
    }

    func testADryRunDecodesLeniently() throws {
        let d = try JobsClient.decoder.decode(RetriageDryRun.self, from: Data(dryJSON.utf8))
        XCTAssertEqual(d.key, "gh:acme:1")
        XCTAssertEqual(d.model, "claude-sonnet-5-5")
        XCTAssertEqual(d.estimate.inputTokens, 1800)
        XCTAssertEqual(d.estimate.typicalUSD, 0.0053)
        XCTAssertEqual(d.estimate.maxUSD, 0.0164)
        XCTAssertEqual(d.estimate.dailyCapUSD, 20)
        XCTAssertEqual(d.estimate.spentUSD, 1.25)
        XCTAssertEqual(d.estimate.remainingUSD, 18.75)
        XCTAssertFalse(d.estimate.capReached)
        XCTAssertNotNil(d.manualTriage)

        let min = try JobsClient.decoder.decode(RetriageDryRun.self, from: Data(#"{"estimate":{"typical_usd":"x","max_usd":-1,"cap_reached":true}}"#.utf8))
        XCTAssertNil(min.key)
        XCTAssertNil(min.estimate.typicalUSD, "a number of the wrong type is absent, not zero")
        XCTAssertNil(min.estimate.maxUSD)
        XCTAssertNil(min.estimate.spentUSD, "an unreadable spend is never 0")
        XCTAssertTrue(min.estimate.capReached)
        XCTAssertNil(min.manualTriage)
        XCTAssertThrowsError(try JobsClient.decoder.decode(RetriageDryRun.self, from: Data(#"{"key":"k"}"#.utf8)), "no estimate, no cost to confirm")
    }

    func testAResultDecodesWithBeforeAfterAndCost() throws {
        let r = try JobsClient.decoder.decode(RetriageResult.self, from: Data(resultJSON.utf8))
        XCTAssertEqual(r.status, "scored")
        XCTAssertEqual(r.before?.slots[.roleFit]?.value, "adjacent")
        XCTAssertEqual(r.before?.slots[.roleFit]?.p, 0.61)
        XCTAssertNil(r.before?.slots[.sponsorship]?.value, "a slot with no reading has no value")
        XCTAssertEqual(r.before?.slots[.workMode]?.backend, "escalate")
        XCTAssertEqual(r.after.slots[.roleFit]?.backend, "manual")
        XCTAssertEqual(r.after.relevance, 74)
        XCTAssertEqual(r.after.model, "claude-sonnet-5-5")
        XCTAssertEqual(r.cost?.usd, 0.0072)
        XCTAssertEqual(r.cost?.outputTokens, 212)

        let minimal = try JobsClient.decoder.decode(RetriageResult.self, from: Data(#"{"after":{"slots":{}}}"#.utf8))
        XCTAssertNil(minimal.before)
        XCTAssertNil(minimal.cost)
        XCTAssertNil(minimal.after.relevance)
    }

    func testAFitFactorFromTheManualTierNamesWhoReadIt() throws {
        let j = try job(#","score_factors":[{"name":"fit","value":1,"weight":0.3,"known":true,"backend":"manual","reading":"strong_fit"},{"name":"gate","value":1,"weight":0,"known":true,"backend":3}]"#)
        let items = try XCTUnwrap(j.scoreFactors).items
        XCTAssertEqual(items[0].backend, "manual")
        XCTAssertEqual(items[0].reading, "strong_fit")
        XCTAssertEqual(items[0].sourceNote, "read by Sonnet (manual)")
        XCTAssertNil(items[1].backend, "a mistyped backend is dropped, the factor stays")
        XCTAssertNil(items[1].sourceNote)
        XCTAssertEqual(ScoreFactor(name: "fit", value: 1, backend: "escalate").sourceNote, "read by the escalation model")
        XCTAssertNil(ScoreFactor(name: "fit", value: 1, backend: "d1-ultra").sourceNote, "a primary backend is not called out")
        // The encoding keeps what was read, so a copied job round-trips.
        let again = try JSONDecoder().decode(ScoreFactors.self, from: JSONEncoder().encode(try XCTUnwrap(j.scoreFactors)))
        XCTAssertEqual(again.items[0].backend, "manual")
    }
}

final class RetriageClientTests: XCTestCase {
    func testArgumentsAreExact() {
        XCTAssertEqual(RetriageClient.arguments("gh:acme:1", .estimate), ["jobs", "retriage", "--json", "--dry-run", "gh:acme:1"])
        XCTAssertEqual(RetriageClient.arguments("gh:acme:1", .run), ["jobs", "retriage", "--json", "gh:acme:1"])
        XCTAssertEqual(RetriageClient.arguments("gh:acme:1", .clear), ["jobs", "retriage", "--json", "--clear", "gh:acme:1"])
    }

    func testTheCommandsAreRunWithThoseArguments() throws {
        let fake = try retriageFake(self, reply: dryJSON)
        let client = RetriageClient(runner: runner(fake))
        _ = client.estimate("gh:acme:1")
        XCTAssertEqual(fake.argv, ["jobs", "retriage", "--json", "--dry-run", "gh:acme:1"])
        try fake.reply(resultJSON)
        _ = client.run("gh:acme:1")
        XCTAssertEqual(fake.argv, ["jobs", "retriage", "--json", "gh:acme:1"])
        try fake.reply(#"{"key":"gh:acme:1","cleared":true,"had_manual":true,"relevance":71.2,"priority":55.1}"#)
        _ = client.clear("gh:acme:1")
        XCTAssertEqual(fake.argv, ["jobs", "retriage", "--json", "--clear", "gh:acme:1"])
    }

    func testAnEstimateDecodesAndSpendsNothing() throws {
        let fake = try retriageFake(self, reply: dryJSON)
        let key = Lookups(), decision = Lookups()
        guard case let .success(dry) = RetriageClient(runner: runner(fake, key: key, decision: decision)).estimate("gh:acme:1") else {
            return XCTFail("the estimate did not decode")
        }
        XCTAssertEqual(dry.estimate.typicalUSD, 0.0053)
        XCTAssertEqual(dry.manualTriage?.value(for: .roleFit), "adjacent")
        XCTAssertEqual(key.count, 0, "an estimate takes no key")
        XCTAssertEqual(decision.count, 0, "and reads no decision key from the Keychain")
        XCTAssertEqual(fake.env, "key= nokeychain=1 decision= cfg=none state=none")
    }

    func testARunGetsTheDraftKeyAndNoKeychainReadOfItsOwn() throws {
        let fake = try retriageFake(self, reply: resultJSON)
        let key = Lookups(), decision = Lookups()
        let r = runner(fake, config: "/cfg.json", state: "/state", key: key, decision: decision)
        guard case let .success(result) = RetriageClient(runner: r).run("gh:acme:1") else { return XCTFail("the run did not decode") }
        XCTAssertEqual(result.after.slots[.roleFit]?.value, "strong_fit")
        XCTAssertEqual(key.count, 1, "the key is read once, the way a draft reads it")
        XCTAssertEqual(decision.count, 0, "the OpenRouter key is never read for a re-triage")
        XCTAssertEqual(fake.env, "key=set nokeychain=1 decision= cfg=/cfg.json state=/state")
    }

    func testTheKeyComesFromAnthropicAccountOnlyForARun() throws {
        let fake = try retriageFake(self, reply: resultJSON)
        let cfg = try vaniConfig(self, provider: "anthropic")
        let key = Lookups(), decision = Lookups()

        let launcher = try fakeLauncher(self)
        let viaLauncher = DraftRunner(vaniBinary: fake.vani, configPath: cfg, keys: .anthropicAccount(launcher.url), account: "org500",
                                      key: { key.hit("must-not-be-read") }, decisionKey: { decision.hit("must-not-be-read") })
        let client = RetriageClient(runner: viaLauncher)
        guard case .success = client.run("gh:acme:1") else { return XCTFail("the run failed") }
        XCTAssertEqual(launcher.recordedArguments, ["exec", "--account", "org500", "--", fake.vani.path, "jobs", "retriage", "--json", "gh:acme:1"])
        XCTAssertEqual(launcher.handedKey, "key=unset ws=unset", "Swift hands the launcher no key")
        XCTAssertEqual(fake.env, "key=set nokeychain=1 decision= cfg=\(cfg) state=none", "the launcher's key reached vani, with VANI_NO_KEYCHAIN")

        let idle = try fakeLauncher(self)
        let keyless = RetriageClient(runner: DraftRunner(vaniBinary: fake.vani, configPath: cfg, keys: .anthropicAccount(idle.url)))
        try fake.reply(dryJSON)
        guard case .success = keyless.estimate("gh:acme:1") else { return XCTFail("the estimate failed") }
        try fake.reply(#"{"cleared":true}"#)
        guard case .success = keyless.clear("gh:acme:1") else { return XCTFail("the clear failed") }
        XCTAssertNil(idle.recordedArguments, "an estimate and a clear never start the launcher")
        XCTAssertEqual(fake.env, "key= nokeychain=1 decision= cfg=\(cfg) state=none")
        XCTAssertEqual(key.count + decision.count, 0)
    }

    func testARefusingRunnerRefusesAllThreeBeforeStartingVani() throws {
        let fake = try retriageFake(self, reply: dryJSON)
        let refusing = DraftRunner(vaniBinary: fake.vani, refusal: .failure("settings", "draft-settings.json is unreadable"))
        let client = RetriageClient(runner: refusing)
        for outcome in [client.estimate("gh:acme:1").failureCode, client.run("gh:acme:1").failureCode, client.clear("gh:acme:1").failureCode] {
            XCTAssertEqual(outcome, "settings")
        }
        XCTAssertNil(fake.argv, "vani never started")
    }

    func testAKeyVaniCouldNotHaveIssuedIsNeverPassed() throws {
        let fake = try retriageFake(self, reply: dryJSON)
        let client = RetriageClient(runner: runner(fake))
        for bad in ["", "--clear", "-x", "a\nb", "a\u{0}b", String(repeating: "k", count: 600)] {
            XCTAssertEqual(client.estimate(bad).failureCode, "bad_request", bad)
            XCTAssertEqual(client.run(bad).failureCode, "bad_request", bad)
        }
        XCTAssertNil(fake.argv)
    }

    func testAMissingBinaryIsAConfigFailure() {
        let gone = DraftRunner(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani"))
        let out = RetriageClient(runner: gone).estimate("k")
        XCTAssertEqual(out.failureCode, "config")
    }

    // MARK: Errors

    func testEveryCodeVaniDefinesComesBackWithItsCode() throws {
        let codes = ["bad_request", "unknown_key", "text_gone", "untriaged", "no_llm", "no_key", "cap_reached", "provider", "refused",
                     "unsure", "unreadable", "busy", "store", "config"]
        for code in codes {
            let fake = try retriageFake(self, reply: #"{"error":"plain words","code":"\#(code)"}"#, status: 1)
            for out in [RetriageClient(runner: runner(fake)).estimate("k").fail, RetriageClient(runner: runner(fake)).run("k").fail] {
                XCTAssertEqual(out?.code, code)
                XCTAssertEqual(out?.message, "plain words")
            }
        }
        // vani ending itself on our SIGTERM is the owner's stop, not a failure to show.
        let stopped = try retriageFake(self, reply: #"{"error":"cancelled","code":"cancelled"}"#, status: 1)
        XCTAssertEqual(RetriageClient(runner: runner(stopped)).run("k"), .cancelled)
    }

    func testAPaidFailureCarriesItsCost() throws {
        let fake = try retriageFake(self, reply: #"{"error":"the model refused","code":"refused","cost":{"input_tokens":1790,"output_tokens":9,"usd":0.0072}}"#, status: 1)
        let f = try XCTUnwrap(RetriageClient(runner: runner(fake)).run("k").fail)
        XCTAssertEqual(f.cost, RetriageCost(inputTokens: 1790, outputTokens: 9, usd: 0.0072))
    }

    func testAnErrorWithoutACodeIsUnknown() throws {
        let fake = try retriageFake(self, reply: #"{"error":"something new"}"#, status: 1)
        XCTAssertEqual(RetriageClient(runner: runner(fake)).run("k").fail, RetriageFailure(code: "unknown", message: "something new"))
    }

    func testNoReplyIsReportedWithItsExitStatusAndATooOldVaniIsSaid() throws {
        let broken = try retriageFake(self, reply: "panic: boom", status: 1)
        let f = try XCTUnwrap(RetriageClient(runner: runner(broken)).run("k").fail)
        XCTAssertEqual(f.code, "no_reply")
        XCTAssertTrue(f.message.contains("exited 1 without a reply"), f.message)
        let old = try retriageFake(self, reply: "usage: vani jobs", status: 2)
        let g = try XCTUnwrap(RetriageClient(runner: runner(old)).estimate("k").fail)
        XCTAssertTrue(g.message.contains("rebuild the app"), g.message)
    }

    func testAnUnreadableSuccessKeepsTheCostOfTheCallThatWasPaidFor() throws {
        let fake = try retriageFake(self, reply: #"{"key":"k","cost":{"usd":0.01}}"#)
        let f = try XCTUnwrap(RetriageClient(runner: runner(fake)).run("k").fail)
        XCTAssertEqual(f.code, "unreadable_reply")
        XCTAssertEqual(f.cost?.usd, 0.01)
        let garbage = try retriageFake(self, reply: "not json")
        XCTAssertEqual(RetriageClient(runner: runner(garbage)).run("k").failureCode, "unreadable_reply")
    }

    func testAReplyForAnotherPostingIsRefused() throws {
        let fake = try retriageFake(self, reply: resultJSON)
        XCTAssertEqual(RetriageClient(runner: runner(fake)).run("gh:other:9").failureCode, "unreadable_reply")
    }

    func testAnOversizedReplyIsRefused() throws {
        let fake = try retriageFake(self, reply: #"{"error":""# + String(repeating: "x", count: 70_000) + #"","code":"provider"}"#, status: 1)
        XCTAssertEqual(RetriageClient(runner: runner(fake)).run("k").failureCode, "too_large")
    }

    func testAClearNeedsClearedTrue() throws {
        let fake = try retriageFake(self, reply: #"{"key":"k","cleared":false}"#)
        XCTAssertEqual(RetriageClient(runner: runner(fake)).clear("k").failureCode, "unreadable_reply")
        try fake.reply(#"{"key":"k","cleared":true,"had_manual":false,"relevance":71.2,"priority":55.1}"#)
        guard case let .success(c) = RetriageClient(runner: runner(fake)).clear("k") else { return XCTFail("not cleared") }
        XCTAssertEqual(c.hadManual, false)
        XCTAssertEqual(c.relevance, 71.2)
    }

    // MARK: Timeouts and cancel

    private func sleeper(_ t: XCTestCase, trap: Bool = false) throws -> (vani: URL, pidFile: URL) {
        let dir = try tempDir(t)
        let pid = dir.appendingPathComponent("pid")
        let url = dir.appendingPathComponent("vani")
        // `exec sleep` makes the sleeper the child itself, so no grandchild keeps a pipe open.
        let body = trap
            ? "#!/bin/sh\ntrap '' TERM\necho $$ > '\(pid.path)'\nwhile true; do sleep 1; done\n"
            : "#!/bin/sh\necho $$ > '\(pid.path)'\nexec sleep 30\n"
        try Data(body.utf8).write(to: url)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
        return (url, pid)
    }

    private func pid(from file: URL, timeout: TimeInterval = 5) -> pid_t? {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if let text = try? String(contentsOf: file, encoding: .utf8), let p = pid_t(text.trimmingCharacters(in: .whitespacesAndNewlines)) { return p }
            Thread.sleep(forTimeInterval: 0.02)
        }
        return nil
    }

    /// Gone and reaped: kill(pid, 0) succeeds on a zombie, so ESRCH is the proof.
    private func isGone(_ p: pid_t) -> Bool { kill(p, 0) == -1 && errno == ESRCH }

    private func runAsync(_ work: @escaping @Sendable () -> RetriageOutcome<RetriageResult>) -> (wait: (TimeInterval) -> RetriageOutcome<RetriageResult>?, started: Date) {
        final class Box: @unchecked Sendable { var value: RetriageOutcome<RetriageResult>? }
        let box = Box(), done = DispatchSemaphore(value: 0)
        Thread.detachNewThread { box.value = work(); done.signal() }
        return ({ limit in done.wait(timeout: .now() + limit) == .success ? box.value : nil }, Date())
    }

    func testCancelStopsTheChildReapsItAndIsNotAnError() throws {
        let (vani, pidFile) = try sleeper(self)
        let client = RetriageClient(runner: DraftRunner(vaniBinary: vani, key: { nil }, decisionKey: { nil }))
        let cancel = CancelFlag()
        let job = runAsync { client.run("k", cancellation: cancel) }
        let child = try XCTUnwrap(pid(from: pidFile), "the fake never started")
        XCTAssertFalse(isGone(child))
        cancel.set()
        let out = try XCTUnwrap(job.wait(10), "a cancelled run did not return")
        XCTAssertEqual(out, .cancelled)
        XCTAssertLessThan(Date().timeIntervalSince(job.started), 6, "cancel must not wait for the child's own 30 s")
        XCTAssertTrue(isGone(child), "the child is terminated and reaped, not left a zombie")
    }

    func testCancelEscalatesToKillForAChildThatIgnoresTerm() throws {
        let (vani, pidFile) = try sleeper(self, trap: true)
        let client = RetriageClient(runner: DraftRunner(vaniBinary: vani, key: { nil }, decisionKey: { nil }))
        let cancel = CancelFlag()
        let job = runAsync { client.run("k", cancellation: cancel) }
        let child = try XCTUnwrap(pid(from: pidFile))
        cancel.set()
        XCTAssertEqual(try XCTUnwrap(job.wait(15)), .cancelled)
        XCTAssertTrue(isGone(child))
    }

    func testACancelSetBeforeTheStartStartsNothing() throws {
        let fake = try retriageFake(self, reply: resultJSON)
        let cancel = CancelFlag()
        cancel.set()
        XCTAssertEqual(RetriageClient(runner: runner(fake)).run("k", cancellation: cancel), .cancelled)
        XCTAssertNil(fake.argv, "nothing started")
    }

    func testARunThatOutlivesItsBoundIsATimeoutNotACancel() throws {
        let (vani, pidFile) = try sleeper(self)
        let client = RetriageClient(runner: DraftRunner(vaniBinary: vani, key: { nil }, decisionKey: { nil }), runTimeout: 1, quickTimeout: 1)
        let started = Date()
        let f = try XCTUnwrap(client.run("k").fail)
        XCTAssertEqual(f.code, "timeout")
        XCTAssertLessThan(Date().timeIntervalSince(started), 6)
        let child = try XCTUnwrap(pid(from: pidFile))
        XCTAssertTrue(isGone(child), "the timed-out child is reaped")
        XCTAssertEqual(client.estimate("k").failureCode, "timeout", "the estimate is bounded as well")
        XCTAssertEqual(RetriageClient.runTimeout, 90)
        XCTAssertEqual(RetriageClient.quickTimeout, JobsClient.timeout)
    }
}

extension RetriageOutcome {
    var fail: RetriageFailure? {
        if case let .failure(f) = self { return f }
        return nil
    }

    var failureCode: String? { fail?.code }
}

final class RetriagePresentationTests: XCTestCase {
    override func setUp() { Labels.useFallback() }
    override func tearDown() { Labels.useFallback() }

    private func dry(_ e: RetriageEstimate, manual: ManualTriage? = nil) -> RetriageDryRun {
        RetriageDryRun(key: "k", model: "claude-sonnet-5-5", estimate: e, manualTriage: manual)
    }

    func testMoneyRounding() {
        XCTAssertEqual(RetriagePresentation.money(0.0053), "$0.005")
        XCTAssertEqual(RetriagePresentation.money(0.0164), "$0.016")
        XCTAssertEqual(RetriagePresentation.money(0.0072), "$0.007")
        XCTAssertEqual(RetriagePresentation.money(18.75), "$18.75")
        XCTAssertEqual(RetriagePresentation.money(20), "$20")
        XCTAssertEqual(RetriagePresentation.money(0.00004), "<$0.001")
        XCTAssertEqual(RetriagePresentation.money(0.99996), "$1")
        XCTAssertEqual(RetriagePresentation.money(0), "$0")
    }

    func testTheCostTextTheOwnerSees() throws {
        let d = try JobsClient.decoder.decode(RetriageDryRun.self, from: Data(dryJSON.utf8))
        let c = RetriagePresentation.confirmation(for: d, locale: en)
        XCTAssertEqual(c.costText, "About $0.005 typically, at most $0.016 · 1,800 input tokens · daily cap $20, $18.75 left")
        XCTAssertTrue(c.canConfirm)
        XCTAssertNil(c.blockedReason)
        XCTAssertEqual(c.priceSource, "Anthropic's published rates for claude-sonnet-5-5")
        XCTAssertNotNil(c.replacesText, "an existing reading is replaced, and the sheet says so")
        XCTAssertTrue(c.sentText.contains("claude-sonnet-5-5") && c.sentText.contains("target summary"), c.sentText)
        for personal in ["name", "contact", "work authorization"] { XCTAssertTrue(c.sentText.contains(personal), personal) }
    }

    func testASpentCapDisablesConfirm() {
        let c = RetriagePresentation.confirmation(for: dry(RetriageEstimate(inputTokens: 1800, typicalUSD: 0.0053, maxUSD: 0.0164,
                                                                            dailyCapUSD: 20, spentUSD: 20, remainingUSD: 0, capReached: true)), locale: en)
        XCTAssertFalse(c.canConfirm)
        XCTAssertTrue(c.costText.contains("the daily cap of $20 is spent"), c.costText)
        XCTAssertTrue(c.blockedReason?.contains("already spent") == true)
    }

    func testAnUnreadSpendIsSaidNotShownAsZero() {
        let c = RetriagePresentation.confirmation(for: dry(RetriageEstimate(inputTokens: 1, typicalUSD: 0.0053, maxUSD: 0.0164, dailyCapUSD: 20)), locale: en)
        XCTAssertEqual(c.costText, "About $0.005 typically, at most $0.016 · 1 input token · daily cap $20, today's spend could not be read")
        XCTAssertTrue(c.canConfirm, "an unread spend is not a spent cap: vani still holds the cap")
        XCTAssertFalse(c.costText.contains("$0 left"))
        let none = RetriagePresentation.confirmation(for: dry(RetriageEstimate(typicalUSD: 0.01, maxUSD: 0.02)), locale: en)
        XCTAssertTrue(none.costText.contains("today's spend could not be read"), none.costText)
    }

    func testNoEstimatedCostCannotBeConfirmed() {
        let c = RetriagePresentation.confirmation(for: dry(RetriageEstimate()), locale: en)
        XCTAssertFalse(c.canConfirm)
        XCTAssertNotNil(c.blockedReason)
        XCTAssertTrue(c.costText.hasPrefix("Vani did not estimate the cost"), c.costText)
        XCTAssertTrue(RetriagePresentation.confirmation(for: dry(RetriageEstimate(maxUSD: 0.02)), locale: en).costText.hasPrefix("At most $0.020"))
    }

    func testDiffRowsUseLabelsAndSayNoReading() {
        let before = RetriageRanking(slots: [.roleFit: RetriageReading(value: "adjacent"), .seniority: RetriageReading(value: "mid"),
                                             .sponsorship: RetriageReading(), .workMode: RetriageReading(value: "none")])
        let after = RetriageRanking(slots: [.roleFit: RetriageReading(value: "strong_fit", backend: "manual"),
                                            .seniority: RetriageReading(value: "mid", backend: "manual"),
                                            .sponsorship: RetriageReading(value: "not_stated"), .workMode: RetriageReading(value: "remote")])
        let rows = RetriagePresentation.diffRows(before: before, after: after)
        XCTAssertEqual(rows.map(\.title), ["Role fit", "Seniority", "Sponsorship", "Work mode"])
        XCTAssertEqual(rows[0], RetriageDiffRow(slot: .roleFit, before: "Adjacent", after: "Strong fit", changed: true))
        XCTAssertEqual(rows[1], RetriageDiffRow(slot: .seniority, before: "Mid-level", after: "Mid-level", changed: false))
        XCTAssertEqual(rows[2], RetriageDiffRow(slot: .sponsorship, before: "no reading", after: "Not stated", changed: true))
        XCTAssertEqual(rows[3], RetriageDiffRow(slot: .workMode, before: "no reading", after: "Remote", changed: true), "\"none\" is no reading")
        let fresh = RetriagePresentation.diffRows(before: nil, after: after)
        XCTAssertEqual(fresh.map(\.before), Array(repeating: "no reading", count: 4))
        XCTAssertTrue(fresh.allSatisfy(\.changed))
        XCTAssertEqual(RetriagePresentation.diffRows(before: after, after: after).filter(\.changed).count, 0)
    }

    func testValueRowsOfAStoredReading() {
        let m = ManualTriage(slots: ["role_fit": "strong_fit", "work_mode": "hybrid"])
        XCTAssertEqual(RetriagePresentation.valueRows(m).map(\.value), ["Strong fit", "no reading", "no reading", "Hybrid"])
    }

    func testHeaderAndRelativeTime() {
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        XCTAssertEqual(RetriagePresentation.header(at: now.addingTimeInterval(-120), now: now, locale: en), "Sonnet (manual) · 2 minutes ago")
        XCTAssertEqual(RetriagePresentation.header(at: now.addingTimeInterval(-2), now: now, locale: en), "Sonnet (manual) · just now")
        XCTAssertEqual(RetriagePresentation.header(at: nil, now: now, locale: en), "Sonnet (manual)")
        XCTAssertEqual(RetriagePresentation.header(at: now.addingTimeInterval(-3 * 86_400), now: now, locale: en), "Sonnet (manual) · 3 days ago")
    }

    func testScoreLinesAndTheOrderNote() {
        XCTAssertEqual(RetriagePresentation.scoreLine("Relevance", before: 71.2, after: 74.0), "Relevance 71 → 74")
        XCTAssertEqual(RetriagePresentation.scoreLine("Relevance", before: 74.2, after: 73.9), "Relevance 74 (unchanged)")
        XCTAssertEqual(RetriagePresentation.scoreLine("Relevance", before: nil, after: 74), "Relevance 74")
        XCTAssertNil(RetriagePresentation.scoreLine("Relevance", before: 71, after: nil))
        XCTAssertNil(RetriagePresentation.scoreLine("Relevance", before: 71, after: .nan))
        let a = RetriageRanking(relevance: 74, priority: 60.2), b = RetriageRanking(relevance: 71.2, priority: 55.1)
        XCTAssertEqual(RetriagePresentation.orderNote(before: b, after: a), "The list has been re-sorted.")
        XCTAssertEqual(RetriagePresentation.orderNote(before: a, after: a), "The list has been refreshed; its order did not change.")
        XCTAssertEqual(RetriagePresentation.orderNote(before: nil, after: a), "The list has been refreshed.")
    }

    func testStaleWordingNeverPresentsAnOldReadingAsCurrent() {
        let stale = ManualTriage(slots: ["role_fit": "adjacent"], current: false, staleReason: "text_changed")
        XCTAssertEqual(RetriagePresentation.staleNote(stale),
                       "This reading is out of date: the posting text changed since Sonnet read it. Ranking ignores it.")
        XCTAssertTrue(RetriagePresentation.staleNote(ManualTriage(slots: ["role_fit": "adjacent"], current: false, staleReason: "text_gone"))?.contains("text is gone") == true)
        XCTAssertEqual(RetriagePresentation.staleNote(ManualTriage(slots: ["role_fit": "adjacent"], current: false)),
                       "This reading is out of date. Ranking ignores it.")
        XCTAssertNil(RetriagePresentation.staleNote(ManualTriage(slots: ["role_fit": "adjacent"], current: true)))
    }

    func testRuleSevenAndTheDroppedNote() {
        XCTAssertEqual(RetriagePresentation.ruleNotes(status: "scored"), ["A manual reading only changes the order. It never drops a posting or brings a dropped one back."])
        let dropped = RetriagePresentation.ruleNotes(status: "dropped")
        XCTAssertEqual(dropped.count, 2)
        XCTAssertEqual(dropped[1], "This posting is dropped, and this reading does not bring it back.")
        XCTAssertEqual(RetriagePresentation.ruleNotes(status: nil).count, 1)
    }

    func testCostSummary() {
        XCTAssertEqual(RetriagePresentation.costSummary(RetriageCost(inputTokens: 1790, outputTokens: 212, usd: 0.0072), locale: en),
                       "Charged $0.007 · 1,790 input and 212 output tokens")
        XCTAssertEqual(RetriagePresentation.costSummary(RetriageCost(usd: 0.0072), locale: en), "Charged $0.007")
        XCTAssertNil(RetriagePresentation.costSummary(RetriageCost()))
        XCTAssertNil(RetriagePresentation.costSummary(nil))
    }

    func testBlockedReasons() {
        var j = Job(key: "k", company: "C", title: "T", status: "scored")
        XCTAssertNil(RetriagePresentation.blockedReason(for: j, busy: false))
        XCTAssertTrue(RetriagePresentation.blockedReason(for: j, busy: true)?.contains("Another re-triage is running") == true)
        j.untriaged = true
        XCTAssertEqual(RetriagePresentation.blockedReason(for: j, busy: false), RetriageWords.untriagedReason)
        XCTAssertEqual(RetriagePresentation.blockedReason(for: j, busy: true), RetriageWords.untriagedReason, "an untriaged posting is never offered, busy or not")
    }

    // MARK: Errors

    func testEveryCodeHasPlainWordsExceptCancelled() throws {
        let expected: [String: String] = [
            "no_key": "No Anthropic key is available. Add it in Settings, then try again.",
            "cap_reached": "Today's Claude spending cap is spent (decide.daily_usd.llm in Vani's config). Try again tomorrow.",
            "provider": "Sonnet could not be reached, or did not answer in time. Nothing was saved.",
            "refused": "Sonnet declined to read this posting. Nothing was saved.",
            "unsure": "Sonnet could not tell on at least one of the four readings, so nothing was saved.",
            "unreadable": "Sonnet's reply could not be used. Nothing was saved.",
            "busy": "Another re-triage is already running. Wait for it to finish, then try again.",
            "text_gone": "The posting's text was cleared after the role closed, so there is nothing to send to Sonnet.",
            "untriaged": "No run has triaged this posting yet. Let a run read it first, then re-triage it.",
            "unknown_key": "Vani no longer has this posting in its ledger. Refresh the list.",
            "no_llm": "No language-model provider is configured. Set llm.provider in Vani's config, then try again.",
            "store": "Sonnet answered, but the reading could not be saved.",
            "timeout": "Sonnet did not answer within 90 s. Nothing was saved.",
        ]
        for (code, words) in expected {
            XCTAssertEqual(RetriagePresentation.errorText(for: RetriageFailure(code: code, message: "raw vani text"))?.message, words, code)
        }
        for code in ["bad_request", "config", "account", "keychain", "settings", "too_large", "no_reply", "unreadable_reply"] {
            let text = try XCTUnwrap(RetriagePresentation.errorText(for: RetriageFailure(code: code, message: "raw vani text")), code)
            XCTAssertTrue(text.message.contains("raw vani text"), "\(code): \(text.message)")
        }
        XCTAssertNil(RetriagePresentation.errorText(for: RetriageFailure(code: "cancelled", message: "cancelled")), "a cancel is not an error banner")
    }

    func testRetryOnlyWhereItMakesSense() {
        func retry(_ code: String) -> Bool { RetriagePresentation.errorText(for: RetriageFailure(code: code, message: "m"))?.canRetry ?? false }
        for code in ["provider", "timeout", "unreadable", "busy", "store", "no_reply", "too_large", "unreadable_reply", "keychain"] { XCTAssertTrue(retry(code), code) }
        for code in ["no_key", "no_llm", "cap_reached", "text_gone", "untriaged", "unknown_key", "refused", "unsure", "bad_request", "config", "cancelled", "mystery"] {
            XCTAssertFalse(retry(code), code)
        }
    }

    func testAnUnknownCodeShowsItsTextClipped() throws {
        let long = String(repeating: "w", count: 1000)
        let text = try XCTUnwrap(RetriagePresentation.errorText(for: RetriageFailure(code: "ungrounded", message: "  " + long + "  ")))
        XCTAssertEqual(text.message.count, 241)
        XCTAssertTrue(text.message.hasSuffix("…"))
        XCTAssertEqual(RetriagePresentation.errorText(for: RetriageFailure(code: "odd", message: ""))?.message, "Re-triage failed (odd).")
        XCTAssertEqual(RetriagePresentation.errorText(for: RetriageFailure(code: "odd", message: "new thing"))?.message, "new thing")
    }

    func testAPaidButUnusableFailureShowsWhatWasCharged() throws {
        let paid = RetriageFailure(code: "unreadable", message: "m", cost: RetriageCost(inputTokens: 1790, outputTokens: 9, usd: 0.0072))
        XCTAssertEqual(RetriagePresentation.errorText(for: paid)?.costNote, "Sonnet was asked and charged $0.007 but the reply could not be used.")
        for code in ["refused", "unsure", "unreadable", "store"] {
            var f = paid
            f.code = code
            XCTAssertNotNil(RetriagePresentation.errorText(for: f)?.costNote, code)
        }
        XCTAssertNil(RetriagePresentation.errorText(for: RetriageFailure(code: "provider", message: "m"))?.costNote, "no cost reported, none shown")
        XCTAssertNil(RetriagePresentation.errorText(for: RetriageFailure(code: "refused", message: "m", cost: RetriageCost(usd: 0)))?.costNote)
    }
}

final class RetriageNoKeychainTests: XCTestCase {
    /// Every key lookup in these tests is injected, so none reaches the real Keychain. This pins that the
    /// client itself never asks for the OpenRouter key (a run, an estimate and a clear all use
    /// `withDecisionKey: false`), and that only the paid run reads the Anthropic key.
    func testNoCallAsksForTheDecisionKey() throws {
        let fake = try retriageFake(self, reply: resultJSON)
        let decision = Lookups(), key = Lookups()
        let client = RetriageClient(runner: runner(fake, key: key, decision: decision))
        _ = client.run("gh:acme:1")
        try fake.reply(dryJSON)
        _ = client.estimate("gh:acme:1")
        try fake.reply(#"{"cleared":true}"#)
        _ = client.clear("gh:acme:1")
        XCTAssertEqual(decision.count, 0)
        XCTAssertEqual(key.count, 1, "only the run reads the Anthropic key")
    }
}
