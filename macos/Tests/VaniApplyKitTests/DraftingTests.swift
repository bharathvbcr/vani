import Foundation
import XCTest
@testable import VaniApplyKit

/// A stand-in for `vani draft`: reports what it was given without echoing the key.
func fakeVani(_ t: XCTestCase, body: String? = nil) throws -> URL {
    let dir = try tempDir(t)
    let url = dir.appendingPathComponent("vani")
    let script = body ?? """
    #!/bin/sh
    [ "$1" = "draft" ] || { echo '{"error":"wrong subcommand","code":"bad_request"}'; exit 1; }
    input=$(cat)
    case "$input" in *slow*) sleep 5 ;; esac
    case "$input" in *fail*) echo '{"error":"today is spent","code":"budget_exhausted"}'; exit 1 ;; esac
    k=unset
    [ -n "$ANTHROPIC_API_KEY" ] && k=set
    [ "$ANTHROPIC_API_KEY" = "launcher-key" ] && k=match
    printf '{"draft":"key=%s cfg=%s state=%s ws=%s inherited=%s","model":"claude-opus-5-5","evidence_items":2}\\n' \
      "$k" "${VANI_CONFIG:-none}" "${VANI_STATE_DIR:-none}" "${ANTHROPIC_WORKSPACE_ID:-none}" "${VANI_APPLY_TEST_INHERITED:-none}"
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return url
}

/// A stand-in for Manvi's `anthropic-account`. It records its argv and what key
/// environment it was handed (one line each, in `args`), answers `list --json`,
/// and for `exec` adds its own key and replaces itself with the command after
/// `--`, as the real one does. `refuse` makes it fail the way the real one does
/// on a machine with no ~/.anthropic-accounts.
struct FakeLauncher {
    let url: URL
    let argsFile: URL

    /// Its argv, or nil when it never ran.
    var recordedArguments: [String]? {
        guard let text = try? String(contentsOf: argsFile, encoding: .utf8) else { return nil }
        let lines = text.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        return Array(lines.dropFirst().dropLast())
    }

    /// What key environment the launcher itself was started with.
    var handedKey: String? {
        (try? String(contentsOf: argsFile, encoding: .utf8))?.split(separator: "\n").first.map(String.init)
    }
}

func fakeLauncher(_ t: XCTestCase, refuse: Bool = false, list: String = #"[{"name":"org200"},{"name":"org500","workspace":"wrkspc_01Test"}]"#) throws -> FakeLauncher {
    let dir = try tempDir(t)
    let url = dir.appendingPathComponent("anthropic-account")
    let args = dir.appendingPathComponent("args")
    let script = """
    #!/bin/sh
    printf 'key=%s ws=%s\\n' "${ANTHROPIC_API_KEY:-unset}" "${ANTHROPIC_WORKSPACE_ID:-unset}" > '\(args.path)'
    for a in "$@"; do printf '%s\\n' "$a" >> '\(args.path)'; done
    \(refuse ? "echo 'anthropic-account: no account registry at /home/.anthropic-accounts.' >&2; echo 'For each Console organisation, store its key under a name' >&2; exit 1" : "")
    if [ "$1" = "list" ]; then echo '\(list)'; exit 0; fi
    [ "$1" = "exec" ] || exit 2
    while [ $# -gt 0 ] && [ "$1" != "--" ]; do shift; done
    shift
    ANTHROPIC_API_KEY=launcher-key exec "$@"
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return FakeLauncher(url: url, argsFile: args)
}

/// A Vani config file naming `provider` (nil writes an llm block without one).
func vaniConfig(_ t: XCTestCase, provider: String?) throws -> String {
    let url = try tempDir(t).appendingPathComponent("config.json")
    let llm = provider.map { #"{"provider":"\#($0)","model":"m"}"# } ?? #"{"model":"m"}"#
    try Data(#"{"version":1,"watchlist":[],"llm":\#(llm),"future_key":{"x":1}}"#.utf8).write(to: url)
    return url.path
}

final class DraftingTests: XCTestCase {
    func testRunsVaniWithAMinimalEnvironmentAndNoKeyOfItsOwn() throws {
        // Nothing in this process's environment reaches vani: neither an
        // inherited variable nor an Anthropic key or workspace set here.
        setenv("VANI_APPLY_TEST_INHERITED", "leaked", 1)
        setenv("ANTHROPIC_API_KEY", "leaked-key", 1)
        setenv("ANTHROPIC_WORKSPACE_ID", "wrkspc_leaked", 1)
        defer { unsetenv("VANI_APPLY_TEST_INHERITED"); unsetenv("ANTHROPIC_API_KEY"); unsetenv("ANTHROPIC_WORKSPACE_ID") }
        let r = DraftRunner(vaniBinary: try fakeVani(self), configPath: "/cfg.json", stateDir: "/state")
        let reply = r.run(DraftRequest(question: "Why us?"))
        XCTAssertNil(reply.error)
        XCTAssertEqual(reply.draft, "key=unset cfg=/cfg.json state=/state ws=none inherited=none")
        XCTAssertEqual(reply.model, "claude-opus-5-5")
        XCTAssertEqual(reply.evidence_items, 2)
    }

    func testNoKeyAndDefaultPaths() throws {
        let r = DraftRunner(vaniBinary: try fakeVani(self))
        XCTAssertEqual(r.run(DraftRequest(question: "Why us?")).draft, "key=unset cfg=none state=none ws=none inherited=none")
    }

    func testAnthropicConfigStartsVaniThroughAnthropicAccount() throws {
        setenv("ANTHROPIC_API_KEY", "leaked-key", 1)
        defer { unsetenv("ANTHROPIC_API_KEY") }
        let vani = try fakeVani(self), cfg = try vaniConfig(self, provider: "anthropic")

        let chosen = try fakeLauncher(self)
        let reply = DraftRunner(vaniBinary: vani, configPath: cfg, keys: .anthropicAccount(chosen.url), account: "org500")
            .run(DraftRequest(question: "Why us?"))
        XCTAssertNil(reply.error)
        XCTAssertEqual(reply.draft, "key=match cfg=\(cfg) state=none ws=none inherited=none", "the key is the launcher's")
        XCTAssertEqual(chosen.recordedArguments, ["exec", "--account", "org500", "--", vani.path, "draft"])
        XCTAssertEqual(chosen.handedKey, "key=unset ws=unset", "Swift hands the launcher no key and no workspace")

        let byDefault = try fakeLauncher(self)
        XCTAssertEqual(DraftRunner(vaniBinary: vani, configPath: cfg, keys: .anthropicAccount(byDefault.url))
            .run(DraftRequest(question: "q")).draft, "key=match cfg=\(cfg) state=none ws=none inherited=none")
        XCTAssertEqual(byDefault.recordedArguments, ["exec", "--", vani.path, "draft"], "no account: the launcher picks")
    }

    func testOtherProvidersAndUnreadableConfigsSkipTheLauncher() throws {
        let vani = try fakeVani(self)
        let dir = try tempDir(self)
        let oversized = dir.appendingPathComponent("big.json")
        try Data(#"{"llm":{"provider":"anthropic"},"pad":""#.utf8 + Data(repeating: 0x61, count: VaniConfig.maxBytes) + Data(#""}"#.utf8)).write(to: oversized)
        let malformed = dir.appendingPathComponent("bad.json")
        try Data(#"{"llm":{"provider":"anthropic""#.utf8).write(to: malformed)
        let wrongType = dir.appendingPathComponent("type.json")
        try Data(#"{"llm":{"provider":7}}"#.utf8).write(to: wrongType)

        for cfg in [try vaniConfig(self, provider: "gemini"), try vaniConfig(self, provider: nil), try vaniConfig(self, provider: "Anthropic"),
                    "/nonexistent/config.json", oversized.path, malformed.path, wrongType.path, dir.path] {
            let launcher = try fakeLauncher(self)
            let reply = DraftRunner(vaniBinary: vani, configPath: cfg, keys: .anthropicAccount(launcher.url), account: "org500")
                .run(DraftRequest(question: "q"))
            XCTAssertEqual(reply.draft, "key=unset cfg=\(cfg) state=none ws=none inherited=none", cfg)
            XCTAssertNil(launcher.recordedArguments, "\(cfg): the launcher must not run")
        }
        XCTAssertEqual(VaniConfig.llmProvider(configPath: try vaniConfig(self, provider: "anthropic")), "anthropic")
    }

    func testNoKeyNeverUsesTheLauncherEvenForAnthropic() throws {
        let cfg = try vaniConfig(self, provider: "anthropic")
        XCTAssertEqual(DraftRunner(vaniBinary: try fakeVani(self), configPath: cfg, keys: .noKey, account: "org500")
            .run(DraftRequest(question: "q")).draft, "key=unset cfg=\(cfg) state=none ws=none inherited=none")
    }

    func testAnAnthropicRunWithoutTheLauncherFailsInsteadOfGoingKeyless() throws {
        let vani = try fakeVani(self), cfg = try vaniConfig(self, provider: "anthropic")
        let notInstalled = DraftRunner(vaniBinary: vani, configPath: cfg, keys: .anthropicAccount(nil)).run(DraftRequest(question: "q"))
        XCTAssertEqual(notInstalled.code, "account")
        XCTAssertTrue(notInstalled.error?.contains("/opt/homebrew/bin/anthropic-account") == true, notInstalled.error ?? "")
        let gone = DraftRunner(vaniBinary: vani, configPath: cfg, keys: .anthropicAccount(URL(fileURLWithPath: "/nonexistent/anthropic-account")))
        XCTAssertEqual(gone.run(DraftRequest(question: "q")).code, "account")
        // A gemini config needs no launcher, installed or not.
        XCTAssertNil(DraftRunner(vaniBinary: vani, configPath: try vaniConfig(self, provider: "gemini"), keys: .anthropicAccount(nil))
            .run(DraftRequest(question: "q")).error)
    }

    /// `vani resume` calls no model: it runs vani itself, never through anthropic-account, and
    /// reads no key, even with an anthropic config and the launcher missing.
    func testAResumeJobRunsVaniDirectlyWithNoKey() throws {
        let vani = try fakeVani(self, body: """
        #!/bin/sh
        [ "$1" = "resume" ] || { echo '{"error":"wrong subcommand","code":"bad_request"}'; exit 1; }
        cat > /dev/null
        printf '{"pdf":"JVBERi0=","name":"Resume_Acme.pdf","bytes":5,"key":"%s"}\\n' "${ANTHROPIC_API_KEY:-unset}"
        """)
        let cfg = try vaniConfig(self, provider: "anthropic")
        let launcher = try fakeLauncher(self)
        final class Count: @unchecked Sendable { let lock = NSLock(); var n = 0; func bump() { lock.lock(); n += 1; lock.unlock() } }
        for keys: DraftRunner.KeySource in [.anthropicAccount(nil), .anthropicAccount(launcher.url), .noKey] {
            let asked = Count()
            let runner = DraftRunner(vaniBinary: vani, configPath: cfg, keys: keys, key: { asked.bump(); return "should-not-be-read" })
            let out = runner.runJob(.resume, input: Data(#"{"job":{"description":"d"},"header":{"name":"Test Applicant"}}"#.utf8))
            let reply = try XCTUnwrap(JSONSerialization.jsonObject(with: out) as? [String: Any])
            XCTAssertEqual(reply["name"] as? String, "Resume_Acme.pdf", "\(keys): \(reply)")
            XCTAssertEqual(reply["key"] as? String, "unset", "\(keys): no model key reaches vani resume")
            XCTAssertEqual(asked.n, 0, "\(keys): the Keychain is not read for a resume")
        }
        XCTAssertNil(launcher.recordedArguments, "the launcher never ran")
        XCTAssertEqual(JobKind.resume.timeout, 180)
        XCTAssertEqual(JobKind.resume.maxRequestBytes, 128 * 1024)
        XCTAssertFalse(JobKind.resume.usesModel)
        // The largest PDF, base64-encoded, fits the reply cap, and the cap fits Chrome's 1 MiB native reply.
        XCTAssertGreaterThan(JobKind.resume.maxReplyBytes, (ResumeStore.maxBytes + 2) / 3 * 4 + 4096)
        XCTAssertLessThan(JobKind.resume.maxReplyBytes, NativeMessaging.maxReplyBytes - 4096)
        XCTAssertTrue(JobKind.draft.usesModel && JobKind.suggest.usesModel)
    }

    func testABadSavedAccountNeverReachesTheLauncher() throws {
        let vani = try fakeVani(self), cfg = try vaniConfig(self, provider: "anthropic")
        for bad in ["--repo", "a b", "../org", "org\n500", String(repeating: "a", count: 65)] {
            let launcher = try fakeLauncher(self)
            let reply = DraftRunner(vaniBinary: vani, configPath: cfg, keys: .anthropicAccount(launcher.url), account: bad).run(DraftRequest(question: "q"))
            XCTAssertEqual(reply.code, "account", bad)
            XCTAssertNil(launcher.recordedArguments, bad)
        }
    }

    func testTheLaunchersRefusalBecomesAnAccountReply() throws {
        let vani = try fakeVani(self), cfg = try vaniConfig(self, provider: "anthropic")
        let reply = DraftRunner(vaniBinary: vani, configPath: cfg, keys: .anthropicAccount(try fakeLauncher(self, refuse: true).url))
            .run(DraftRequest(question: "q"))
        XCTAssertEqual(reply.code, "account")
        XCTAssertTrue(reply.error?.hasPrefix("anthropic-account: no account registry") == true, reply.error ?? "")
        XCTAssertNil(reply.draft)

        // vani's own failure after the launcher handed over stays vani's.
        let crashing = try fakeVani(self, body: "#!/bin/sh\ncat >/dev/null\necho 'panic: boom' >&2\nexit 2\n")
        XCTAssertEqual(DraftRunner(vaniBinary: crashing, configPath: cfg, keys: .anthropicAccount(try fakeLauncher(self).url))
            .run(DraftRequest(question: "q")).code, "provider")
        // So is the coded reply vani writes after the launcher handed over.
        XCTAssertEqual(DraftRunner(vaniBinary: vani, configPath: cfg, keys: .anthropicAccount(try fakeLauncher(self).url))
            .run(DraftRequest(question: "fail")).code, "budget_exhausted")
        XCTAssertNil(AnthropicAccount.refusal(in: Data("anthropic-account: warning: no fallback key\npanic\n".utf8)))
        XCTAssertNil(AnthropicAccount.refusal(in: Data()))
    }

    func testAccountComesFromDraftSettings() throws {
        let vani = try fakeVani(self), cfg = try vaniConfig(self, provider: "anthropic")
        let launcher = try fakeLauncher(self)
        let set = DraftSettings(vaniConfig: cfg, anthropicAccount: "org200").runner(vaniBinary: vani, keys: .anthropicAccount(launcher.url))
        XCTAssertEqual(set.run(DraftRequest(question: "q")).draft, "key=match cfg=\(cfg) state=none ws=none inherited=none")
        XCTAssertEqual(launcher.recordedArguments, ["exec", "--account", "org200", "--", vani.path, "draft"])

        let unset = try fakeLauncher(self)
        _ = DraftSettings(vaniConfig: cfg).runner(vaniBinary: vani, keys: .anthropicAccount(unset.url)).run(DraftRequest(question: "q"))
        XCTAssertEqual(unset.recordedArguments, ["exec", "--", vani.path, "draft"])
        XCTAssertEqual(DraftSettings(vaniConfig: cfg).runner(vaniBinary: vani).keys, .anthropicAccount(AnthropicAccount.locate()),
                       "the default runner uses the installed launcher")
    }

    func testFailuresBecomeCodedReplies() throws {
        let vani = try fakeVani(self)
        XCTAssertEqual(DraftRunner(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani")).run(DraftRequest(question: "q")).code, "config")
        XCTAssertEqual(DraftRunner(vaniBinary: vani).run(DraftRequest(question: "fail")).code, "budget_exhausted")

        let slow = DraftRunner(vaniBinary: vani, timeout: 1)
        let started = Date()
        XCTAssertEqual(slow.run(DraftRequest(question: "slow")).code, "timeout")
        XCTAssertLessThan(Date().timeIntervalSince(started), 4, "the timeout must stop the child")

        // Through the launcher the timeout still stops vani, which it became.
        let throughLauncher = DraftRunner(vaniBinary: vani, configPath: try vaniConfig(self, provider: "anthropic"),
                                          keys: .anthropicAccount(try fakeLauncher(self).url), timeout: 1)
        let again = Date()
        XCTAssertEqual(throughLauncher.run(DraftRequest(question: "slow")).code, "timeout")
        XCTAssertLessThan(Date().timeIntervalSince(again), 4)

        let garbage = try fakeVani(self, body: "#!/bin/sh\ncat >/dev/null\necho not-json\n")
        XCTAssertEqual(DraftRunner(vaniBinary: garbage).run(DraftRequest(question: "q")).code, "provider")
    }

    func testListsTheLaunchersAccounts() throws {
        XCTAssertEqual(try AnthropicAccount.list(launcher: try fakeLauncher(self).url), ["org200", "org500"])
        XCTAssertEqual(try AnthropicAccount.list(launcher: try fakeLauncher(self, list: "[]").url), [])
        XCTAssertEqual(try AnthropicAccount.list(launcher: try fakeLauncher(self, list: #"[{"name":"ok"},{"name":"not ok"}]"#).url), ["ok"])
        XCTAssertThrowsError(try AnthropicAccount.list(launcher: try fakeLauncher(self, refuse: true).url)) {
            XCTAssertTrue(String(describing: $0).contains("no account registry"), String(describing: $0))
        }
        XCTAssertThrowsError(try AnthropicAccount.list(launcher: try fakeLauncher(self, list: "not json").url))
        XCTAssertThrowsError(try AnthropicAccount.list(launcher: URL(fileURLWithPath: "/nonexistent/anthropic-account")))
        let listed = try fakeLauncher(self)
        _ = try AnthropicAccount.list(launcher: listed.url)
        XCTAssertEqual(listed.recordedArguments, ["list", "--json"])
    }

    func testLocatesTheFirstInstalledLauncher() throws {
        let a = try fakeLauncher(self).url, b = try fakeLauncher(self).url
        let missing = URL(fileURLWithPath: "/nonexistent/anthropic-account")
        XCTAssertEqual(AnthropicAccount.locate(candidates: [missing, b, a]), b)
        XCTAssertNil(AnthropicAccount.locate(candidates: [missing]))
        XCTAssertEqual(AnthropicAccount.candidates.map(\.path),
                       [NSHomeDirectory() + "/.local/bin/anthropic-account", "/opt/homebrew/bin/anthropic-account"])
    }

    func testAccountNames() {
        for ok in ["org200", "Org.500", "a_b-c", String(repeating: "a", count: 64)] { XCTAssertTrue(AnthropicAccount.isValidName(ok), ok) }
        for bad in ["", "a b", "a/b", "-", "--account", "é", "a\0b", String(repeating: "a", count: 65)] {
            XCTAssertFalse(AnthropicAccount.isValidName(bad), bad)
        }
    }

    func testRequestFromBrowserIsStrict() throws {
        XCTAssertEqual(try DraftRunner.request(from: ["question": "Why?", "job": ["company": "Modal"], "max_words": 100]),
                       DraftRequest(question: "Why?", job: .init(company: "Modal"), maxWords: 100))
        for bad: Any? in [nil, "Why?", ["question": 3], ["question": "Why?", "answers": ["x"]], ["job": [:]]] {
            XCTAssertThrowsError(try DraftRunner.request(from: bad), "\(String(describing: bad))")
        }
    }

    func testProtocolDraftOp() throws {
        let store = BankStore(directory: try tempDir(self))
        let req: [String: Any] = ["op": "draft", "request": ["question": "Why us?"]]

        let safari = NativeProtocol.handle(req, store: store, source: .safari)
        XCTAssertEqual(safari["code"] as? String, "unsupported")

        let drafter = DraftRunner(vaniBinary: try fakeVani(self))
        let chrome = NativeProtocol.handle(req, store: store, source: .chrome, drafter: drafter)
        XCTAssertEqual(chrome["draft"] as? String, "key=unset cfg=none state=none ws=none inherited=none")

        let bad = NativeProtocol.handle(["op": "draft", "request": ["question": "Why?", "extra": 1]], store: store, source: .chrome, drafter: drafter)
        XCTAssertEqual(bad["code"] as? String, "bad_request")

        let log = try store.recentActivity()
        XCTAssertEqual(log.count, 1, "only a run that reached vani is logged")
        XCTAssertEqual(log.first?.event, .draft)
        XCTAssertEqual(log.first?.counts, ["drafted": 1])
        XCTAssertNil(log.first?.host)
        let raw = try String(contentsOf: store.directory.appendingPathComponent("activity.jsonl"), encoding: .utf8)
        XCTAssertFalse(raw.contains("Why us"), "the question must not reach the log")
    }

    func testDraftSettingsRoundTripAndValidation() throws {
        let dir = try tempDir(self)
        XCTAssertEqual(try DraftSettings.load(from: dir), DraftSettings())
        try DraftSettings(vaniConfig: "/a/config.json", vaniStateDir: "/a/state").save(to: dir)
        XCTAssertEqual(try DraftSettings.load(from: dir), DraftSettings(vaniConfig: "/a/config.json", vaniStateDir: "/a/state"))
        let mode = try (FileManager.default.attributesOfItem(atPath: dir.appendingPathComponent("draft-settings.json").path)[.posixPermissions] as! NSNumber).intValue
        XCTAssertEqual(mode, 0o600)
        XCTAssertThrowsError(try DraftSettings(vaniConfig: "relative/config.json").save(to: dir))
    }

    /// B2-9: a missing file is the defaults; a present but corrupt one is a named error, and every
    /// launch path built from it refuses instead of running against the default config and account.
    func testCorruptDraftSettingsAreANamedErrorAndEveryRunnerRefuses() throws {
        let vani = URL(fileURLWithPath: "/x/vani")
        let missing = try tempDir(self)
        XCTAssertEqual(try DraftSettings.load(from: missing), DraftSettings())
        XCTAssertNil(DraftSettings.runner(loadingFrom: missing, vaniBinary: vani, keys: .noKey).refusal)

        for (name, body) in [("truncated", "{nope"), ("empty", ""), ("wrong type", #"{"vaniConfig":5}"#), ("array", "[]")] {
            let dir = try tempDir(self)
            try Data(body.utf8).write(to: dir.appendingPathComponent("draft-settings.json"))
            XCTAssertThrowsError(try DraftSettings.load(from: dir), name) { error in
                XCTAssertTrue(error is DraftSettings.LoadError, "\(name): \(error)")
            }
            let runner = DraftSettings.runner(loadingFrom: dir, vaniBinary: vani, keys: .noKey)
            XCTAssertEqual(runner.refusal?.code, "settings", name)
            guard case let .refused(reply) = runner.prepare(["draft"], usesModel: false) else { return XCTFail("\(name): launched") }
            XCTAssertEqual(reply.code, "settings")
            XCTAssertTrue(reply.error?.contains("draft-settings.json") == true)
            let job = try JSONSerialization.jsonObject(with: runner.runJob(.draft, input: Data("{}".utf8))) as? [String: Any]
            XCTAssertEqual(job?["code"] as? String, "settings", name)
            let jobs = DraftSettings.jobsBackend(loadingFrom: dir, vaniBinary: vani)
            XCTAssertThrowsError(try jobs.markApplied(key: "gh:acme:1"), name)
            XCTAssertThrowsError(try jobs.find(url: "https://example.com/a"), name)
            XCTAssertThrowsError(try jobs.ingest(payload: Data("{}".utf8)), name)
        }

        // An unreadable (not merely absent) path is also an error: a directory where the file belongs.
        let dirAsFile = try tempDir(self)
        try FileManager.default.createDirectory(at: dirAsFile.appendingPathComponent("draft-settings.json"), withIntermediateDirectories: true)
        XCTAssertThrowsError(try DraftSettings.load(from: dirAsFile))

        // The host's draft op answers code "settings" through the protocol, never runs vani.
        let store = BankStore(directory: try tempDir(self))
        try Data("{nope".utf8).write(to: store.directory.appendingPathComponent("draft-settings.json"))
        let drafter = DraftSettings.runner(loadingFrom: store.directory, vaniBinary: vani, keys: .noKey)
        let reply = NativeProtocol.handle(["op": "draft", "request": ["question": "q"]], store: store, source: .chrome, drafter: drafter)
        XCTAssertEqual(reply["code"] as? String, "settings")
    }

    func testDraftSettingsAccount() throws {
        let dir = try tempDir(self)
        // A file saved before accounts existed keeps its paths; its workspace
        // id is dropped, because the account's registry line carries it now.
        try Data(#"{"vaniConfig":"/a/config.json","vaniStateDir":"/a/state","anthropicWorkspace":"wrkspc_01Old"}"#.utf8)
            .write(to: dir.appendingPathComponent("draft-settings.json"))
        XCTAssertEqual(try DraftSettings.load(from: dir), DraftSettings(vaniConfig: "/a/config.json", vaniStateDir: "/a/state"))

        try DraftSettings(anthropicAccount: "  org500\n").save(to: dir)
        XCTAssertEqual(try DraftSettings.load(from: dir).anthropicAccount, "org500", "saved trimmed")
        let saved = try String(contentsOf: dir.appendingPathComponent("draft-settings.json"), encoding: .utf8)
        XCTAssertFalse(saved.contains("anthropicWorkspace"))
        for bad in ["org 500", "../org", "org\0x", String(repeating: "w", count: 65)] {
            XCTAssertThrowsError(try DraftSettings(anthropicAccount: bad).save(to: dir), bad)
        }
        XCTAssertEqual(try DraftSettings.load(from: dir).anthropicAccount, "org500", "a refused save leaves the file alone")
    }
}
