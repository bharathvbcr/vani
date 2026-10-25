import Foundation
import XCTest
@testable import VaniApplyKit

/// A stand-in for `vani jobs resume`: records its argv, stdin and the environment that matters, then
/// prints the reply and exit status a test chose (or sleeps first). Never reads the Keychain or the
/// owner's state.
private struct ResumeFake {
    let vani: URL
    let dir: URL

    var argv: [String]? {
        guard let text = try? String(contentsOf: dir.appendingPathComponent("argv"), encoding: .utf8) else { return nil }
        return text.split(separator: "\n", omittingEmptySubsequences: true).map(String.init)
    }

    var stdin: String? { try? String(contentsOf: dir.appendingPathComponent("stdin"), encoding: .utf8) }

    var env: String? {
        (try? String(contentsOf: dir.appendingPathComponent("env"), encoding: .utf8))?.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    func reply(_ json: String, status: Int32 = 0, sleep: Int = 0) throws {
        try Data(json.utf8).write(to: dir.appendingPathComponent("reply.json"))
        try Data("\(status)".utf8).write(to: dir.appendingPathComponent("status"))
        try Data("\(sleep)".utf8).write(to: dir.appendingPathComponent("sleep"))
    }
}

private func resumeFake(_ t: XCTestCase, reply: String = "{}", status: Int32 = 0) throws -> ResumeFake {
    let dir = try tempDir(t)
    let url = dir.appendingPathComponent("vani")
    let script = """
    #!/bin/sh
    d='\(dir.path)'
    : > "$d/argv"
    for a in "$@"; do printf '%s\\n' "$a" >> "$d/argv"; done
    cat > "$d/stdin"
    printf 'key=%s nokeychain=%s decision=%s cfg=%s state=%s\\n' "${ANTHROPIC_API_KEY:+set}" "${VANI_NO_KEYCHAIN:-unset}" "${OPENROUTER_API_KEY:+set}" "${VANI_CONFIG:-none}" "${VANI_STATE_DIR:-none}" > "$d/env"
    s="$(cat "$d/sleep")"
    [ "$s" -gt 0 ] && exec sleep "$s"
    cat "$d/reply.json"
    exit "$(cat "$d/status")"
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    let fake = ResumeFake(vani: url, dir: dir)
    try fake.reply(reply, status: status)
    return fake
}

private final class Lookups: @unchecked Sendable {
    private let lock = NSLock()
    private var n = 0
    var count: Int { lock.lock(); defer { lock.unlock() }; return n }
    func hit(_ value: String?) -> String? { lock.lock(); n += 1; lock.unlock(); return value }
}

private func runner(_ fake: ResumeFake, keys: DraftRunner.KeySource = .noKey, config: String? = nil, state: String? = nil,
                    key: Lookups = Lookups(), decision: Lookups = Lookups()) -> DraftRunner {
    DraftRunner(vaniBinary: fake.vani, configPath: config, stateDir: state, keys: keys,
                key: { key.hit("sk-test-anthropic") }, decisionKey: { decision.hit("sk-test-openrouter") })
}

private let checksJSON = #"{"eligibility":"current","lint":"passed","fit":"passed","figures":"verified"}"#

private let savedJSON = """
{"schema":1,"key":"gh:acme:1","version":3,"parent":2,"source":"tailored","model":"claude-opus-5-5",
 "cost":{"usd":0.31,"complete":true,"turns":2},"checks":\(checksJSON),"pages":2,"notes":["one orphan line"],
 "jd_keywords":["go","kubernetes"],"bullets":[{"id":"b1","text_hash":"abc123def456"},{"id":"b2","text_hash":"0123456789ab"}],
 "fit_lines":{"b1":2},"fingerprints":{"bank":"x"},"pdf_sha256":"aabbcc","created":"2026-10-25T12:00:00.123456789Z",
 "current_moved":true,"pdf_path":"/s/resumes/h/v3.pdf","future":{"x":1}}
"""

private let dryJSON = """
{"key":"gh:acme:1","dry_run":true,"model":"claude-opus-5-5","opus_usable":true,"current":2,
 "budget":{"daily_cap_usd":20,"spent_usd":1.25,"remaining_usd":18.75},
 "estimate":{"turns":2,"input_tokens":18400,"max_output_tokens":24000,"max_usd":0.42,"price_source":"Anthropic's published rates","cap_reached":false}}
"""

private let usedJSON = #"{"key":"gh:acme:1","version":3,"pdf_path":"/s/resumes/h/v3.pdf","name":"Acme_Resume.pdf","pdf_sha256":"aabbcc"}"#

private let shownJSON = """
{"key":"gh:acme:1","eligibility_checked":true,"current":3,"used":2,"version":3,
 "versions":[
  {"schema":1,"key":"gh:acme:1","version":3,"parent":2,"source":"tailored","cost":{"usd":0.31,"complete":false,"turns":2},"checks":\(checksJSON),
   "pages":2,"bullets":[{"id":"b1","text_hash":"abc123def456"}],"pdf_sha256":"aabbcc","pdf_path":"/s/resumes/h/v3.pdf","fit_lines":{"b1":2,"b2":0,"b3":51},"created":"2026-10-25T12:00:00Z",
   "selection":{"sections":[{"title":"Experience","entries":[{"id":"e1","bullets":["b1"]}]}]},
   "current":true,"eligibility":{"state":"stale","reworded":["b1"],"gone":[],"ineligible":["b9"],"inputs_changed":["bank"],"why":"changed"}},
  {"schema":1,"key":"gh:acme:1","version":2,"source":"base","reason":"no model","cost":{"usd":0,"complete":true,"turns":0},
   "checks":{"eligibility":"unknown","lint":"not_run","fit":"could_not_run","figures":"not_run"},"created":"2026-10-24T09:00:00Z","used":true}],
 "pool":{"summary":[],"entries":[]},"keywords":[{"term":"Kubernetes","cat":"infra","n":3,"target":true,"gap":true}]}
"""

final class ResumeStudioClientTests: XCTestCase {
    private let key = "gh:acme:1"

    // MARK: argv and timeouts

    func testEveryCallsArgumentsAndTimeoutAreExact() {
        let c = ResumeStudioClient(runner: DraftRunner(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani")))
        let edit = Data("{}".utf8)
        let calls: [(ResumeStudioClient.Call, [String], TimeInterval, Bool)] = [
            (c.estimateCall(key), ["jobs", "resume", "build", "--json", "--dry-run", key], 60, true),
            (c.buildCall(key, allowBase: false), ["jobs", "resume", "build", "--json", key], 375, true),
            (c.buildCall(key, allowBase: true), ["jobs", "resume", "build", "--json", "--allow-base", key], 375, true),
            (c.renderPreviewCall(key, edit: edit), ["jobs", "resume", "render", "--json", key], 115, false),
            (c.renderFitCall(key, edit: edit, from: nil), ["jobs", "resume", "render", "--json", "--fit", key], 275, false),
            (c.renderFitCall(key, edit: edit, from: 2), ["jobs", "resume", "render", "--json", "--fit", "--from", "v2", key], 275, false),
            (c.showCall(key, version: nil, check: true), ["jobs", "resume", "show", "--json", key], 60, false),
            (c.showCall(key, version: 3, check: true), ["jobs", "resume", "show", "--json", key, "v3"], 60, false),
            (c.showCall(key, version: nil, check: false), ["jobs", "resume", "show", "--json", "--no-check", key], 20, false),
            (c.useCall(key, version: 3), ["jobs", "resume", "use", "--json", key, "v3"], 25, false),
            (c.revalidateCall(key, version: 3), ["jobs", "resume", "revalidate", "--json", key, "v3"], 275, false),
        ]
        for (call, argv, timeout, usesModel) in calls {
            XCTAssertEqual(call.arguments, argv)
            XCTAssertEqual(call.timeout, timeout, "\(argv)")
            XCTAssertEqual(call.usesModel, usesModel, "\(argv)")
        }
        // Go's deadlines (docs/RUNTIME.md) plus 15 s: none of them is DraftRunner's 150 s default.
        let go: [TimeInterval: TimeInterval] = [60: 45, 375: 360, 115: 100, 275: 260, 20: 5, 25: 10]
        for (call, _, timeout, _) in calls { XCTAssertEqual(timeout, go[timeout].map { $0 + 15 }, "\(call.arguments)") }
        XCTAssertEqual(calls.compactMap { $0.0.input }.count, 3, "only the edits carry stdin")
    }

    func testTheCommandsRunWithThoseArgumentsAndTheEditOnStdin() throws {
        let fake = try resumeFake(self, reply: shownJSON)
        let client = ResumeStudioClient(runner: runner(fake))
        guard case .success = client.show(key, version: 3, check: false) else { return XCTFail("show") }
        XCTAssertEqual(fake.argv, ["jobs", "resume", "show", "--json", "--no-check", key, "v3"])
        try fake.reply(usedJSON)
        guard case .success = client.use(key, version: 3) else { return XCTFail("use") }
        XCTAssertEqual(fake.argv, ["jobs", "resume", "use", "--json", key, "v3"])
        try fake.reply(savedJSON)
        let edit = Data(#"{"sections":[]}"#.utf8)
        guard case .success = client.renderFit(key, edit: edit, from: 2) else { return XCTFail("render") }
        XCTAssertEqual(fake.argv, ["jobs", "resume", "render", "--json", "--fit", "--from", "v2", key])
        XCTAssertEqual(fake.stdin, #"{"sections":[]}"#, "the edit reaches vani on stdin")
    }

    func testAnExplicitTimeoutIsPassedAndSaid() throws {
        let fake = try resumeFake(self)
        try fake.reply("{}", sleep: 6)
        var t = ResumeStudioClient.Timeouts()
        t.build = 1
        let f = try XCTUnwrap(ResumeStudioClient(runner: runner(fake), timeouts: t).build(key).fail)
        XCTAssertEqual(f.code, "timeout")
        XCTAssertTrue(f.message.contains("within 1 s"), f.message)
    }

    // MARK: keys

    func testOnlyTheEstimateAndTheBuildTakeTheKeyAndNeverTheKeychain() throws {
        let fake = try resumeFake(self, reply: dryJSON)
        let key = Lookups(), decision = Lookups()
        let client = ResumeStudioClient(runner: runner(fake, config: "/cfg.json", state: "/state", key: key, decision: decision))
        guard case .success = client.estimate(self.key) else { return XCTFail("estimate") }
        XCTAssertEqual(key.count, 1, "a dry run needs the credential to tell whether Opus is usable; it sends nothing")
        XCTAssertEqual(fake.env, "key=set nokeychain=1 decision= cfg=/cfg.json state=/state")
        try fake.reply(savedJSON)
        guard case .success = client.build(self.key) else { return XCTFail("build") }
        XCTAssertEqual(key.count, 2)
        for (reply, call) in [(shownJSON, { client.show(self.key).failureCode }), (usedJSON, { client.use(self.key, version: 3).failureCode }),
                              (savedJSON, { client.revalidate(self.key, version: 3).failureCode }),
                              (savedJSON, { client.renderFit(self.key, edit: Data("{}".utf8)).failureCode })] {
            try fake.reply(reply)
            _ = call()
            XCTAssertEqual(fake.env, "key= nokeychain=1 decision= cfg=/cfg.json state=/state", "show, use, render and revalidate get no key")
        }
        XCTAssertEqual(key.count, 2, "none of them read it")
        XCTAssertEqual(decision.count, 0, "the OpenRouter key is never read for a resume")
    }

    func testTheKeyComesFromAnthropicAccountForTheEstimateAndBuildOnly() throws {
        let fake = try resumeFake(self, reply: dryJSON)
        let cfg = try vaniConfig(self, provider: "anthropic")
        let launcher = try fakeLauncher(self)
        let viaLauncher = DraftRunner(vaniBinary: fake.vani, configPath: cfg, keys: .anthropicAccount(launcher.url), account: "org500",
                                      key: { "must-not-be-read" }, decisionKey: { "must-not-be-read" })
        let client = ResumeStudioClient(runner: viaLauncher)
        guard case .success = client.estimate(key) else { return XCTFail("estimate") }
        XCTAssertEqual(launcher.recordedArguments, ["exec", "--account", "org500", "--", fake.vani.path, "jobs", "resume", "build", "--json", "--dry-run", key])
        XCTAssertEqual(launcher.handedKey, "key=unset ws=unset", "Swift hands the launcher no key")
        XCTAssertEqual(fake.env, "key=set nokeychain=1 decision= cfg=\(cfg) state=none", "the launcher's key reached vani, with VANI_NO_KEYCHAIN")

        let idle = try fakeLauncher(self)
        try fake.reply(usedJSON)
        let keyless = ResumeStudioClient(runner: DraftRunner(vaniBinary: fake.vani, configPath: cfg, keys: .anthropicAccount(idle.url)))
        guard case .success = keyless.use(key, version: 3) else { return XCTFail("use") }
        XCTAssertNil(idle.recordedArguments, "a use never starts the launcher")
        XCTAssertEqual(fake.env, "key= nokeychain=1 decision= cfg=\(cfg) state=none")
    }

    func testARefusingRunnerRefusesBeforeStartingVani() throws {
        let fake = try resumeFake(self, reply: dryJSON)
        let client = ResumeStudioClient(runner: DraftRunner(vaniBinary: fake.vani, refusal: .failure("settings", "draft-settings.json is unreadable")))
        XCTAssertEqual(client.estimate(key).failureCode, "settings")
        XCTAssertEqual(client.build(key).failureCode, "settings")
        XCTAssertEqual(client.use(key, version: 1).failureCode, "settings")
        XCTAssertNil(fake.argv, "vani never started")
    }

    func testAKeyOrVersionVaniCouldNotHaveIssuedIsNeverPassed() throws {
        let fake = try resumeFake(self, reply: dryJSON)
        let client = ResumeStudioClient(runner: runner(fake))
        for bad in ["", "--allow-base", "-x", "a\nb", "a\u{0}b", String(repeating: "k", count: 600)] {
            XCTAssertEqual(client.estimate(bad).failureCode, "bad_request", bad)
            XCTAssertEqual(client.build(bad).failureCode, "bad_request", bad)
            XCTAssertEqual(client.show(bad).failureCode, "bad_request", bad)
        }
        for v in [0, -1, 10_000] {
            XCTAssertEqual(client.use(key, version: v).failureCode, "bad_request", "v\(v)")
            XCTAssertEqual(client.revalidate(key, version: v).failureCode, "bad_request", "v\(v)")
        }
        XCTAssertEqual(client.renderFit(key, edit: Data()).failureCode, "bad_edit")
        XCTAssertEqual(client.renderPreview(key, edit: Data(repeating: 0x20, count: 64 * 1024 + 1)).failureCode, "bad_edit")
        XCTAssertNil(fake.argv)
    }

    func testAMissingBinaryIsAConfigFailure() {
        let gone = DraftRunner(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani"))
        XCTAssertEqual(ResumeStudioClient(runner: gone).estimate(key).failureCode, "config")
    }

    // MARK: decoding

    func testABuildReplyDecodesEverythingAndToleratesUnknownFields() throws {
        let fake = try resumeFake(self, reply: savedJSON)
        guard case let .success(saved) = ResumeStudioClient(runner: runner(fake)).build(key) else { return XCTFail("did not decode") }
        let v = saved.version
        XCTAssertEqual(v.version, 3)
        XCTAssertEqual(v.parent, 2)
        XCTAssertEqual(v.source, "tailored")
        XCTAssertEqual(v.cost, ResumeCost(usd: 0.31, complete: true, turns: 2))
        XCTAssertEqual(v.checks, ResumeChecks(eligibility: "current", lint: "passed", fit: "passed", figures: "verified"))
        XCTAssertEqual(v.pages, 2)
        XCTAssertEqual(v.bullets.map(\.id), ["b1", "b2"])
        XCTAssertEqual(v.bullets[0].textHash, "abc123def456")
        XCTAssertEqual(v.keywords, ["go", "kubernetes"])
        XCTAssertTrue(v.hasPDF)
        let expected = try XCTUnwrap(ISO8601DateFormatter().date(from: "2026-10-25T12:00:00Z")).timeIntervalSince1970
        XCTAssertEqual(v.created.timeIntervalSince1970, expected + 0.123456, accuracy: 0.001)
        XCTAssertTrue(saved.currentMoved)
        XCTAssertEqual(saved.pdfPath, "/s/resumes/h/v3.pdf")
        XCTAssertEqual(v.pdfPath, "/s/resumes/h/v3.pdf")
        XCTAssertEqual(v.fitLines, ["b1": 2])
        XCTAssertEqual(saved.droppedStale, [])
        XCTAssertNil(v.selection, "build's reply carries no choice")
    }

    func testARevalidationNamesWhatItDropped() throws {
        let json = savedJSON.replacingOccurrences(of: #""current_moved":true"#, with: #""current_moved":false,"current_note":"v2 stays current: made from v1","dropped_stale":["b7","b8"]"#)
        let fake = try resumeFake(self, reply: json)
        guard case let .success(saved) = ResumeStudioClient(runner: runner(fake)).revalidate(key, version: 1) else { return XCTFail() }
        XCTAssertFalse(saved.currentMoved)
        XCTAssertEqual(saved.droppedStale, ["b7", "b8"])
        let line = ResumeWords.savedLine(saved)
        XCTAssertTrue(line.contains("v2 stays current"), line)
        XCTAssertTrue(line.contains("Dropped: b7, b8"), line)
        XCTAssertFalse(line.contains("now the current"), "a version that did not take the pointer is not called current: \(line)")
    }

    func testAVersionMissingARequiredFieldIsAnErrorNotAPass() throws {
        let fake = try resumeFake(self)
        let client = ResumeStudioClient(runner: runner(fake))
        let broken: [(String, String)] = [
            ("no version", savedJSON.replacingOccurrences(of: #""version":3,"#, with: "")),
            ("no checks", savedJSON.replacingOccurrences(of: #""checks":\#(checksJSON),"#, with: "")),
            ("an empty fit state", savedJSON.replacingOccurrences(of: #""fit":"passed""#, with: #""fit":"""#)),
            ("no fit state", savedJSON.replacingOccurrences(of: #""fit":"passed","#, with: "")),
            ("no created", savedJSON.replacingOccurrences(of: #""created":"2026-10-25T12:00:00.123456789Z","#, with: "")),
            ("no current_moved", savedJSON.replacingOccurrences(of: #""current_moved":true,"#, with: "")),
            ("version 0", savedJSON.replacingOccurrences(of: #""version":3,"#, with: #""version":0,"#)),
        ]
        for (what, json) in broken {
            try fake.reply(json)
            let f = try XCTUnwrap(client.build(key).fail, what)
            XCTAssertEqual(f.code, "unreadable_reply", what)
        }
    }

    func testAnUnreadableSuccessKeepsTheCostOfThePaidCall() throws {
        let fake = try resumeFake(self, reply: #"{"key":"gh:acme:1","cost":{"usd":0.31,"complete":true,"turns":2}}"#)
        let f = try XCTUnwrap(ResumeStudioClient(runner: runner(fake)).build(key).fail)
        XCTAssertEqual(f.code, "unreadable_reply")
        XCTAssertEqual(f.cost?.usd, 0.31, "money spent is never hidden by a reply this app cannot read")
        try fake.reply("not json")
        XCTAssertEqual(ResumeStudioClient(runner: runner(fake)).build(key).failureCode, "unreadable_reply")
    }

    func testADryRunDecodesAndAUsableModelNeedsItsEstimate() throws {
        let fake = try resumeFake(self, reply: dryJSON)
        let client = ResumeStudioClient(runner: runner(fake))
        guard case let .success(dry) = client.estimate(key) else { return XCTFail() }
        XCTAssertTrue(dry.opusUsable)
        XCTAssertEqual(dry.current, 2)
        XCTAssertEqual(dry.estimate, ResumeEstimate(turns: 2, inputTokens: 18400, maxOutputTokens: 24000, maxUSD: 0.42,
                                                    priceSource: "Anthropic's published rates", capReached: false))
        XCTAssertEqual(dry.budget, ResumeBudget(dailyCapUSD: 20, spentUSD: 1.25, remainingUSD: 18.75))

        try fake.reply(#"{"key":"gh:acme:1","dry_run":true,"model":"claude-opus-5-5","opus_usable":false,"no_model":"the model provider is not available","budget":{"daily_cap_usd":20}}"#)
        guard case let .success(none) = client.estimate(key) else { return XCTFail("a model that cannot run is a reply, not a failure") }
        XCTAssertFalse(none.opusUsable)
        XCTAssertNil(none.estimate)
        XCTAssertEqual(none.noModel, "the model provider is not available")
        XCTAssertNil(none.budget.spentUSD, "a spend that could not be read is never 0")
        XCTAssertNil(none.budget.remainingUSD)

        try fake.reply(#"{"key":"gh:acme:1","opus_usable":true,"budget":{"daily_cap_usd":20}}"#)
        XCTAssertEqual(client.estimate(key).failureCode, "unreadable_reply", "a usable model with no estimate has no cost to confirm")
        try fake.reply(#"{"key":"gh:acme:1","budget":{}}"#)
        XCTAssertEqual(client.estimate(key).failureCode, "unreadable_reply", "opus_usable is required")
        try fake.reply(#"{"key":"gh:acme:1","opus_usable":true,"estimate":{"turns":2,"input_tokens":1,"max_output_tokens":1,"max_usd":-1}}"#)
        XCTAssertEqual(client.estimate(key).failureCode, "unreadable_reply", "a negative cost is not a cost")
    }

    func testAUnfittedRenderIsNeverAFittedPage() throws {
        let ok = #"{"key":"gh:acme:1","saved":false,"html_path":"/s/resumes/.tmp/preview-1.html","checks":{"eligibility":"current","lint":"passed","fit":"not_run","figures":"verified"},"notes":["x"],"keywords":["go"],"bullets":[{"id":"b1","text_hash":"abc123def456"}]}"#
        let fake = try resumeFake(self, reply: ok)
        let client = ResumeStudioClient(runner: runner(fake))
        guard case let .success(r) = client.renderPreview(key, edit: Data("{}".utf8)) else { return XCTFail() }
        XCTAssertEqual(r.htmlPath, "/s/resumes/.tmp/preview-1.html")
        XCTAssertEqual(r.checks.fit, "not_run")
        XCTAssertEqual(fake.argv, ["jobs", "resume", "render", "--json", key])
        for lie in [ok.replacingOccurrences(of: #""saved":false"#, with: #""saved":true"#),
                    ok.replacingOccurrences(of: #""fit":"not_run""#, with: #""fit":"passed""#)] {
            try fake.reply(lie)
            XCTAssertEqual(client.renderPreview(key, edit: Data("{}".utf8)).failureCode, "unreadable_reply")
        }
    }

    func testShowDecodesEveryVersionTheirVerdictsAndThePoolWithoutCallingAnythingAPass() throws {
        let fake = try resumeFake(self, reply: shownJSON)
        guard case let .success(shown) = ResumeStudioClient(runner: runner(fake)).show(key) else { return XCTFail() }
        XCTAssertTrue(shown.eligibilityChecked)
        XCTAssertEqual([shown.current, shown.used, shown.version], [3, 2, 3])
        XCTAssertEqual(shown.versions.map(\.version), [3, 2])
        let v3 = shown.versions[0]
        XCTAssertTrue(v3.current)
        XCTAssertFalse(v3.used)
        XCTAssertEqual(v3.eligibility?.state, "stale")
        XCTAssertEqual(v3.eligibility?.summary, "1 reworded, 1 no longer eligible")
        XCTAssertEqual(v3.eligibility?.staleIDs, ["b1", "b9"])
        XCTAssertEqual(v3.cost, ResumeCost(usd: 0.31, complete: false, turns: 2))
        XCTAssertNotNil(v3.selection, "the chosen version carries its selection")
        XCTAssertNotNil(shown.pool)
        XCTAssertEqual(v3.pdfPath, "/s/resumes/h/v3.pdf", "show names the PDF of any version that has one")
        XCTAssertEqual(v3.fitLines, ["b1": 2], "a line count rkb cannot have reported (0, 51) is dropped, never clamped")
        XCTAssertNil(shown.versions[1].pdfPath)
        XCTAssertEqual(shown.versions[1].fitLines, [:])
        let v2 = shown.versions[1]
        XCTAssertTrue(v2.used)
        XCTAssertNil(v2.selection)
        XCTAssertEqual(v2.checks.fit, "could_not_run")
        XCTAssertFalse(v2.hasPDF)
        XCTAssertEqual(shown.keywords, [try JobsClient.decoder.decode(ResumeKeyword.self, from: Data(#"{"term":"Kubernetes","cat":"infra","n":3,"target":true,"gap":true}"#.utf8))])
        for state in [v2.checks.fit, v2.checks.lint, v2.checks.figures, v2.checks.eligibility] {
            XCTAssertNotEqual(ResumeWords.fit(state).level, .ok, state)
        }
    }

    func testAUseReplyNeedsAllFiveFields() throws {
        let fake = try resumeFake(self, reply: usedJSON)
        let client = ResumeStudioClient(runner: runner(fake))
        guard case let .success(u) = client.use(key, version: 3) else { return XCTFail() }
        XCTAssertEqual(u, ResumeUsed(key: key, version: 3, pdfPath: "/s/resumes/h/v3.pdf", name: "Acme_Resume.pdf", pdfSHA256: "aabbcc"))
        for field in ["key", "version", "pdf_path", "name", "pdf_sha256"] {
            var obj = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(usedJSON.utf8)) as? [String: Any])
            obj[field] = nil
            try fake.reply(String(decoding: try JSONSerialization.data(withJSONObject: obj), as: UTF8.self))
            XCTAssertEqual(client.use(key, version: 3).failureCode, "unreadable_reply", "without \(field)")
        }
        try fake.reply(usedJSON.replacingOccurrences(of: #""key":"gh:acme:1""#, with: #""key":"gh:other:9""#))
        XCTAssertEqual(client.use(key, version: 3).failureCode, "unreadable_reply", "a reply for another posting is refused")
    }

    // MARK: errors

    func testEveryCodeComesBackWithItsCodeWhateverTheExitStatus() throws {
        for code in ResumeWords.knownCodes where code != "cancelled" {
            // A failure exits 1; one that arrives with exit 0 is still a failure.
            let failing = try resumeFake(self, reply: #"{"error":"plain words","code":"\#(code)"}"#, status: 1)
            let client = ResumeStudioClient(runner: runner(failing))
            for out in [client.build(key).fail, client.use(key, version: 1).fail] {
                XCTAssertEqual(out?.code, code, "exit 1")
                XCTAssertEqual(out?.message, "plain words")
            }
            let odd = try resumeFake(self, reply: #"{"error":"plain words","code":"\#(code)"}"#, status: 0)
            XCTAssertEqual(ResumeStudioClient(runner: runner(odd)).build(key).fail?.code, code, "exit 0")
        }
    }

    func testAStaleRefusalCarriesTheVerdict() throws {
        let fake = try resumeFake(self, reply: #"{"error":"v3 is stale","code":"stale","eligibility":{"state":"stale","reworded":["b1"],"gone":["b2"],"why":"x"}}"#, status: 1)
        let f = try XCTUnwrap(ResumeStudioClient(runner: runner(fake)).use(key, version: 3).fail)
        XCTAssertEqual(f.code, "stale")
        XCTAssertEqual(f.eligibility?.gone, ["b2"])
        XCTAssertEqual(f.eligibility?.summary, "1 reworded, 1 gone")
    }

    func testAPaidFailureCarriesItsCostAndWhetherTheAttemptWasRecorded() throws {
        let fake = try resumeFake(self, reply: #"{"error":"the page does not fit","code":"fit","cost":{"usd":0.31,"complete":false,"turns":2},"attempt_recorded":true}"#, status: 1)
        let f = try XCTUnwrap(ResumeStudioClient(runner: runner(fake)).build(key).fail)
        XCTAssertEqual(f.cost, ResumeCost(usd: 0.31, complete: false, turns: 2))
        XCTAssertTrue(f.attemptRecorded)
        let text = try XCTUnwrap(ResumeWords.failure(f))
        XCTAssertEqual(text.costNote, "Opus 5.5 was asked and charged at least $0.310, but nothing was saved; Vani recorded the attempt.")
        let lost = ResumeFailure(code: "fit", message: "m", cost: ResumeCost(usd: 0.2, complete: true, turns: 1), attemptRecorded: false)
        XCTAssertEqual(ResumeWords.failure(lost)?.costNote, "Opus 5.5 was asked and charged $0.200, but nothing was saved; Vani could not record the attempt.")
    }

    func testACancelIsTheOwnersStopUnlessMoneyWasSpent() throws {
        let stopped = try resumeFake(self, reply: #"{"error":"stopped","code":"cancelled"}"#, status: 1)
        XCTAssertEqual(ResumeStudioClient(runner: runner(stopped)).build(key), .cancelled)
        XCTAssertNil(ResumeWords.failure(ResumeFailure(code: "cancelled", message: "stopped")), "an owner's stop has nothing to say")
        let paid = try resumeFake(self, reply: #"{"error":"stopped","code":"cancelled","cost":{"usd":0.12,"complete":true,"turns":1},"attempt_recorded":true}"#, status: 1)
        let f = try XCTUnwrap(ResumeStudioClient(runner: runner(paid)).build(key).fail, "a stop that came after the model was paid is shown")
        XCTAssertEqual(f.code, "cancelled")
        XCTAssertNotNil(ResumeWords.failure(f)?.costNote)
    }

    func testAnErrorWithoutACodeIsUnknownAndNoReplyNamesTheExitStatus() throws {
        let odd = try resumeFake(self, reply: #"{"error":"something new"}"#, status: 1)
        XCTAssertEqual(ResumeStudioClient(runner: runner(odd)).build(key).fail, ResumeFailure(code: "unknown", message: "something new"))
        let broken = try resumeFake(self, reply: "panic: boom", status: 1)
        let f = try XCTUnwrap(ResumeStudioClient(runner: runner(broken)).use(key, version: 1).fail)
        XCTAssertEqual(f.code, "no_reply")
        XCTAssertTrue(f.message.contains("exited 1 without a reply"), f.message)
        let old = try resumeFake(self, reply: "usage: vani jobs", status: 2)
        XCTAssertTrue(try XCTUnwrap(ResumeStudioClient(runner: runner(old)).show(key).fail).message.contains("rebuild the app"))
    }

    func testAnOversizeReplyIsTooLargeNotACutOffDecode() throws {
        let fake = try resumeFake(self, reply: shownJSON)
        let big = #"{"key":"gh:acme:1","pad":""# + String(repeating: "x", count: 600 * 1024) + #""}"#
        try fake.reply(big)
        XCTAssertEqual(ResumeStudioClient(runner: runner(fake)).build(key).failureCode, "too_large", "build and use replies are small")
        XCTAssertEqual(ResumeStudioClient(runner: runner(fake)).show(key).failureCode, "unreadable_reply", "show has room for the pool; this is just not a show reply")
    }
}

extension ResumeOutcome {
    var fail: ResumeFailure? {
        if case let .failure(f) = self { return f }
        return nil
    }
    var failureCode: String? { fail?.code }
}

final class ResumeWordsTests: XCTestCase {
    private let en = Locale(identifier: "en_US")

    func testEveryCodeHasWordsAndAnOwnersStopHasNone() {
        for code in ResumeWords.knownCodes where code != "cancelled" {
            let t = ResumeWords.failure(ResumeFailure(code: code, message: "the detail"))
            XCTAssertNotNil(t, code)
            XCTAssertFalse(t?.message.isEmpty ?? true, code)
            XCTAssertFalse(t?.message.hasPrefix("The resume step failed") ?? true, "\(code) falls through to the default words")
        }
        XCTAssertNil(ResumeWords.failure(ResumeFailure(code: "cancelled", message: "stopped")))
        XCTAssertTrue(ResumeWords.failure(ResumeFailure(code: "future_code", message: ""))?.message.contains("future_code") ?? false)
    }

    func testTheCodesWordsSayWhatTheOwnerCanDo() throws {
        func text(_ code: String, _ msg: String = "d", eligibility: ResumeEligibility? = nil) throws -> ResumeFailureText {
            try XCTUnwrap(ResumeWords.failure(ResumeFailure(code: code, message: msg, eligibility: eligibility)))
        }
        XCTAssertTrue(try text("busy").message.contains("Nothing was spent"))
        XCTAssertTrue(try text("busy").canRetry)
        XCTAssertTrue(try text("kb_changed").message.contains("knowledge base changed"))
        XCTAssertEqual(try text("kb_changed").offer, .buildAgain)
        let nm = try text("no_model", "the provider has no price for claude-opus-5-5")
        XCTAssertEqual(nm.offer, .buildBase, "Build base anyway is offered, never done")
        XCTAssertTrue(nm.message.contains("nothing was spent"))
        let stale = try text("stale", eligibility: ResumeEligibility(state: "stale", reworded: ["b1"], gone: ["b2", "b3"]))
        XCTAssertEqual(stale.offer, .revalidate)
        XCTAssertTrue(stale.message.contains("1 reworded, 2 gone"), stale.message)
        XCTAssertTrue(try text("ineligible", "bullet exp-7f3 is not offered").message.contains("exp-7f3"), "ineligible names the id")
        XCTAssertTrue(try text("unchecked").message.contains("will not use it"), "an unchecked version is never used")
        XCTAssertEqual(try text("not_stale").offer, .buildAgain)
        XCTAssertTrue(try text("damaged").message.contains("not the same as having no resume"))
        XCTAssertTrue(try text("timeout").message.contains("Nothing is saved unless"))
        XCTAssertTrue(try text("budget_exhausted").message.contains("spending cap"))
        XCTAssertTrue(try text("account", "anthropic-account is not installed").message.contains("anthropic-account"))
    }

    func testAStateThatDidNotRunIsNeverWordedLikeAPass() {
        for state in ["unknown", "not_run", "could_not_run", "", nil, "something_new"] {
            XCTAssertNotEqual(ResumeWords.eligibility(state).level, .ok, "eligibility \(state ?? "nil")")
            XCTAssertNotEqual(ResumeWords.fit(state).level, .ok, "fit \(state ?? "nil")")
            XCTAssertNotEqual(ResumeWords.lint(state).level, .ok, "lint \(state ?? "nil")")
            XCTAssertNotEqual(ResumeWords.figures(state).level, .ok, "figures \(state ?? "nil")")
        }
        XCTAssertEqual(ResumeWords.eligibility("current").level, .ok)
        XCTAssertEqual(ResumeWords.eligibility("stale").level, .bad)
        XCTAssertEqual(ResumeWords.fit("passed").level, .ok)
        XCTAssertEqual(ResumeWords.fit("soft").level, .caution)
        XCTAssertEqual(ResumeWords.fit("failed").level, .bad)
        XCTAssertEqual(ResumeWords.fit("not_run").level, .notRun)
        XCTAssertEqual(ResumeWords.fit("could_not_run").level, .notRun)
        for word in [ResumeWords.eligibility("unknown"), ResumeWords.fit("not_run"), ResumeWords.fit("could_not_run"), ResumeWords.fit(nil)] {
            let said = (word.short + " " + word.long).lowercased()
            XCTAssertFalse(said.contains("vouches for every"), said)
            XCTAssertFalse(said.contains("and fits"), said)
        }
        XCTAssertTrue(ResumeWords.eligibility("current").short.contains("when built"), "a stored verdict is worded as of when it was built")
        XCTAssertEqual(ResumeWords.eligibility("current", fresh: true).short, "Vouched for")
    }

    func testTheConfirmationSaysTheCostTheCapAndWhenOpusCannotRun() {
        let usable = ResumeDryRun(key: "k", model: "claude-opus-5-5", opusUsable: true, current: 2,
                                  budget: ResumeBudget(dailyCapUSD: 20, spentUSD: 1.25, remainingUSD: 18.75),
                                  estimate: ResumeEstimate(turns: 2, inputTokens: 18400, maxOutputTokens: 24000, maxUSD: 0.42, priceSource: "rates"))
        let c = ResumeWords.confirmation(for: usable, locale: en)
        XCTAssertEqual(c.costText, "At most $0.420 · up to 2 turns · 18,400 input tokens at most · daily cap $20, $18.75 left")
        XCTAssertTrue(c.canConfirm)
        XCTAssertFalse(c.offersBase)
        XCTAssertEqual(c.priceSource, "rates")
        XCTAssertTrue(c.sentText.hasPrefix("Sent to Opus 5.5"))
        XCTAssertTrue(c.sentText.contains("Never your name"))
        XCTAssertTrue(c.madeFromText?.contains("v2") ?? false)

        var unread = usable
        unread.budget = ResumeBudget(dailyCapUSD: 20)
        XCTAssertTrue(ResumeWords.confirmation(for: unread, locale: en).costText?.contains("today's spend could not be read") ?? false, "unknown is not zero")

        var capped = usable
        capped.estimate?.capReached = true
        let k = ResumeWords.confirmation(for: capped, locale: en)
        XCTAssertFalse(k.canConfirm)
        XCTAssertNotNil(k.blockedReason)

        let none = ResumeDryRun(key: "k", model: "claude-opus-5-5", opusUsable: false, noModel: "the model provider is not available")
        let n = ResumeWords.confirmation(for: none, locale: en)
        XCTAssertFalse(n.canConfirm, "Build never spends when Opus cannot run")
        XCTAssertTrue(n.offersBase)
        XCTAssertNil(n.costText)
        XCTAssertTrue(n.blockedReason?.contains("the model provider is not available") ?? false)
        XCTAssertTrue(n.baseNote?.contains("no model and no cost") ?? false)
        XCTAssertTrue(n.baseNote?.contains("does not replace a current version") ?? false)
    }

    func testASavedLineSaysWhetherItBecameCurrent() throws {
        let moved = try JobsClient.decoder.decode(ResumeSaved.self, from: Data(savedJSON.utf8))
        XCTAssertEqual(ResumeWords.savedLine(moved), "v3 saved: tailored, 2 pages, fits. It is now the current version.")
        XCTAssertEqual(ResumeWords.costLine(moved.version.cost), "Charged $0.310 over 2 turns")
        XCTAssertNil(ResumeWords.costLine(ResumeCost(usd: 0, complete: true, turns: 0)), "no model, no cost line")
    }
}

final class ResumeCardTests: XCTestCase {
    private func summary(_ extra: String) throws -> ResumeSummary {
        try JobsClient.decoder.decode(ResumeSummary.self, from: Data("{\(extra)}".utf8))
    }

    func testNoSummaryIsNoResumeAndAnErrorIsNever() throws {
        XCTAssertEqual(ResumeCardPresentation.card(for: nil).kind, .none)
        XCTAssertEqual(ResumeCardPresentation.card(for: try summary(#""versions":0"#)).kind, .none)
        for error in ["unreadable", "unsupported", "unreadable_reply", "future"] {
            let card = ResumeCardPresentation.card(for: try summary(#""versions":0,"error":"\#(error)""#))
            guard case let .unreadable(words) = card.kind else { return XCTFail("\(error): \(card.kind)") }
            XCTAssertTrue(words.contains("not the same as having none") || words.contains("newer Vani"), words)
            XCTAssertEqual(card.use, .none, "nothing is offered for a store that cannot be read")
        }
    }

    func testTheCurrentVersionIsSummarisedWithHonestStates() throws {
        let card = ResumeCardPresentation.card(for: try summary(#""current":3,"used":2,"versions":3,"pages":2,"fit":"soft","eligibility":"current","source":"tailored""#))
        XCTAssertEqual(card.kind, .ready)
        XCTAssertEqual(card.headline, "v3 · 2 pages")
        XCTAssertEqual(card.fit?.level, .caution)
        XCTAssertEqual(card.eligibility?.short, "Vouched for when built")
        XCTAssertEqual(card.sourceText, "tailored")
        XCTAssertEqual(card.usedLine, "v2 is in use; v3 is the current version.")
        XCTAssertEqual(card.use, .available(version: 3), "soft fit is usable, with its note")
        let same = ResumeCardPresentation.card(for: try summary(#""current":3,"used":3,"versions":3,"pages":2,"fit":"passed","eligibility":"current","source":"edited""#))
        XCTAssertEqual(same.use, .inUse(version: 3))
        XCTAssertEqual(same.usedLine, "v3 is in use for this job.")
    }

    func testAVersionThatCannotBeUsedOffersTheRightRemedyNotUse() throws {
        func use(_ fields: String) throws -> ResumeUseAction {
            ResumeCardPresentation.card(for: try summary(#""current":3,"versions":3,\#(fields)"#)).use
        }
        guard case let .blocked(_, staleWhy, staleOffer) = try use(#""pages":2,"fit":"passed","eligibility":"stale""#) else { return XCTFail() }
        XCTAssertEqual(staleOffer, .revalidate, "stale: revalidate, not use")
        XCTAssertTrue(staleWhy.contains("no longer vouched for"), staleWhy)
        guard case let .blocked(_, _, unknownOffer) = try use(#""pages":2,"fit":"passed","eligibility":"unknown""#) else { return XCTFail() }
        XCTAssertEqual(unknownOffer, .revalidate, "an unchecked version is never used; revalidating re-checks it")
        for fit in ["failed", "not_run", "could_not_run"] {
            guard case let .blocked(_, _, offer) = try use(#""pages":2,"fit":"\#(fit)","eligibility":"current""#) else { return XCTFail(fit) }
            XCTAssertEqual(offer, .buildAgain, fit)
        }
        guard case let .blocked(_, why, offer) = try use(#""pages":0,"fit":"passed","eligibility":"current""#) else { return XCTFail() }
        XCTAssertEqual(offer, .buildAgain)
        XCTAssertTrue(why.contains("no PDF"), why)
        guard case .blocked = try use(#""pages":2,"eligibility":"current""#) else { return XCTFail("a missing fit state is not a pass") }
    }

    func testASavedVersionIsUsableOnlyFromTheStatesItWasSavedWith() throws {
        func version(_ replace: (String) -> String) throws -> ResumeVersion {
            try JobsClient.decoder.decode(ResumeSaved.self, from: Data(replace(savedJSON).utf8)).version
        }
        XCTAssertEqual(ResumeCardPresentation.useAction(forSaved: try version { $0 }), .available(version: 3))
        XCTAssertEqual(ResumeCardPresentation.useAction(forSaved: try version { $0 }, inUse: true), .inUse(version: 3))
        guard case let .blocked(_, _, fitOffer) = ResumeCardPresentation.useAction(forSaved: try version { $0.replacingOccurrences(of: #""fit":"passed""#, with: #""fit":"could_not_run""#) }) else { return XCTFail() }
        XCTAssertEqual(fitOffer, .buildAgain)
        guard case let .blocked(_, _, eligOffer) = ResumeCardPresentation.useAction(forSaved: try version { $0.replacingOccurrences(of: #""eligibility":"current""#, with: #""eligibility":"unknown""#) }) else { return XCTFail() }
        XCTAssertEqual(eligOffer, .revalidate)
        guard case .blocked = ResumeCardPresentation.useAction(forSaved: try version { $0.replacingOccurrences(of: #""pages":2,"#, with: "") }) else { return XCTFail("an unfitted version has no PDF to use") }
    }

    func testAJobWhoseDetailHasNotLoadedOrFailedIsUnknownNeverNoResume() throws {
        func job(_ extra: String) throws -> Job {
            try JobsClient.decoder.decode(Job.self, from: Data(#"{"key":"gh:acme:1","company":"Acme","title":"Engineer","status":"scored"\#(extra)}"#.utf8))
        }
        let row = try job(""), withResume = try job(#","resume":{"current":2,"versions":2}"#)
        XCTAssertEqual(ResumeKnowledge.of(detail: nil, job: row, detailFailed: false), .loading, "a list row has no summary yet: not 'none'")
        XCTAssertEqual(ResumeKnowledge.of(detail: nil, job: row, detailFailed: true), .failed, "a failed show is not 'none' either")
        XCTAssertNil(ResumeKnowledge.loading.summary)
        XCTAssertNil(ResumeKnowledge.failed.summary)
        XCTAssertEqual(ResumeKnowledge.of(detail: row, job: row, detailFailed: false), .known(nil), "shown, and it has none")
        XCTAssertEqual(ResumeKnowledge.of(detail: withResume, job: row, detailFailed: true), .known(withResume.resume), "a loaded detail wins over an old failure")
        XCTAssertEqual(ResumeKnowledge.of(detail: nil, job: withResume, detailFailed: false), .known(withResume.resume))
    }

    func testTheApplyKitLineSaysWhatIsAttached() throws {
        let used = try summary(#""current":3,"used":2,"versions":3"#)
        XCTAssertEqual(ResumeCardPresentation.applyKitLine(for: used, browserCopy: true), "Tailored resume v2 is the one attached for this job.")
        XCTAssertTrue(ResumeCardPresentation.applyKitLine(for: used, browserCopy: false)?.contains("no saved copy") ?? false, "a used version with no browser copy is said, not shown as attached")
        XCTAssertEqual(ResumeCardPresentation.applyKitLine(for: try summary(#""current":3,"versions":1"#), browserCopy: false),
                       "A tailored resume (v3) is built but not in use yet.")
        XCTAssertNil(ResumeCardPresentation.applyKitLine(for: nil, browserCopy: false))
        XCTAssertNil(ResumeCardPresentation.applyKitLine(for: try summary(#""error":"unreadable""#), browserCopy: false))
    }

    func testAJobsResumeFieldDecodesAndAnUnreadableOneIsNeverNoResume() throws {
        func job(_ extra: String) throws -> Job {
            try JobsClient.decoder.decode(Job.self, from: Data(#"{"key":"gh:acme:1","company":"Acme","title":"Engineer","status":"scored"\#(extra)}"#.utf8))
        }
        XCTAssertNil(try job("").resume, "a job with no resume, or from an older vani")
        XCTAssertNil(try job(#","resume":null"#).resume)
        let r = try XCTUnwrap(try job(#","resume":{"current":3,"used":3,"versions":3,"pages":2,"fit":"passed","eligibility":"current","source":"tailored","future":1}"#).resume)
        XCTAssertEqual(r, ResumeSummary(current: 3, used: 3, versions: 3, pages: 2, fit: "passed", eligibility: "current", source: "tailored"))
        XCTAssertEqual(try job(#","resume":{"versions":0,"error":"unreadable"}"#).resume?.error, "unreadable")
        XCTAssertEqual(try job(#","resume":"garbage""#).resume, .unreadableReply, "a value this app cannot read is shown as unreadable, never as no resume")
        XCTAssertEqual(try job(#","resume":7"#).resume, .unreadableReply)
    }
}
