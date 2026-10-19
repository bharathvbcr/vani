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
    [ "$ANTHROPIC_API_KEY" = "test-key" ] && k=match
    printf '{"draft":"key=%s cfg=%s state=%s ws=%s inherited=%s","model":"claude-opus-5-5","evidence_items":2}\\n' \
      "$k" "${VANI_CONFIG:-none}" "${VANI_STATE_DIR:-none}" "${ANTHROPIC_WORKSPACE_ID:-none}" "${VANI_APPLY_TEST_INHERITED:-none}"
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return url
}

final class DraftingTests: XCTestCase {
    func testRunsVaniWithTheKeyAndAMinimalEnvironment() throws {
        setenv("VANI_APPLY_TEST_INHERITED", "leaked", 1)
        defer { unsetenv("VANI_APPLY_TEST_INHERITED") }
        let r = DraftRunner(vaniBinary: try fakeVani(self), configPath: "/cfg.json", stateDir: "/state",
                            workspace: "wrkspc_01Test", key: { "test-key" })
        let reply = r.run(DraftRequest(question: "Why us?"))
        XCTAssertNil(reply.error)
        XCTAssertEqual(reply.draft, "key=match cfg=/cfg.json state=/state ws=wrkspc_01Test inherited=none")
        XCTAssertEqual(reply.model, "claude-opus-5-5")
        XCTAssertEqual(reply.evidence_items, 2)
    }

    func testNoKeyAndDefaultPaths() throws {
        let r = DraftRunner(vaniBinary: try fakeVani(self), key: { nil })
        XCTAssertEqual(r.run(DraftRequest(question: "Why us?")).draft, "key=unset cfg=none state=none ws=none inherited=none")
    }

    func testWorkspaceComesFromDraftSettings() throws {
        let vani = try fakeVani(self)
        let set = DraftSettings(anthropicWorkspace: "wrkspc_01Test").runner(vaniBinary: vani, key: { nil })
        XCTAssertEqual(set.run(DraftRequest(question: "q")).draft, "key=unset cfg=none state=none ws=wrkspc_01Test inherited=none")
        let unset = DraftSettings().runner(vaniBinary: vani, key: { nil })
        XCTAssertEqual(unset.run(DraftRequest(question: "q")).draft, "key=unset cfg=none state=none ws=none inherited=none")
    }

    func testFailuresBecomeCodedReplies() throws {
        let vani = try fakeVani(self)
        XCTAssertEqual(DraftRunner(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani"), key: { nil }).run(DraftRequest(question: "q")).code, "config")
        XCTAssertEqual(DraftRunner(vaniBinary: vani, key: { throw BankError("denied") }).run(DraftRequest(question: "q")).code, "keychain")
        XCTAssertEqual(DraftRunner(vaniBinary: vani, key: { nil }).run(DraftRequest(question: "fail")).code, "budget_exhausted")

        let slow = DraftRunner(vaniBinary: vani, timeout: 1, key: { nil })
        let started = Date()
        XCTAssertEqual(slow.run(DraftRequest(question: "slow")).code, "timeout")
        XCTAssertLessThan(Date().timeIntervalSince(started), 4, "the timeout must stop the child")

        let garbage = try fakeVani(self, body: "#!/bin/sh\ncat >/dev/null\necho not-json\n")
        XCTAssertEqual(DraftRunner(vaniBinary: garbage, key: { nil }).run(DraftRequest(question: "q")).code, "provider")
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

        let drafter = DraftRunner(vaniBinary: try fakeVani(self), key: { nil })
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
        XCTAssertEqual(DraftSettings.load(from: dir), DraftSettings())
        try DraftSettings(vaniConfig: "/a/config.json", vaniStateDir: "/a/state").save(to: dir)
        XCTAssertEqual(DraftSettings.load(from: dir), DraftSettings(vaniConfig: "/a/config.json", vaniStateDir: "/a/state"))
        let mode = try (FileManager.default.attributesOfItem(atPath: dir.appendingPathComponent("draft-settings.json").path)[.posixPermissions] as! NSNumber).intValue
        XCTAssertEqual(mode, 0o600)
        XCTAssertThrowsError(try DraftSettings(vaniConfig: "relative/config.json").save(to: dir))
        try Data("{nope".utf8).write(to: dir.appendingPathComponent("draft-settings.json"))
        XCTAssertEqual(DraftSettings.load(from: dir), DraftSettings())
    }

    func testDraftSettingsWorkspace() throws {
        let dir = try tempDir(self)
        // A file saved before the workspace field existed keeps its paths.
        try Data(#"{"vaniConfig":"/a/config.json","vaniStateDir":"/a/state"}"#.utf8).write(to: dir.appendingPathComponent("draft-settings.json"))
        XCTAssertEqual(DraftSettings.load(from: dir), DraftSettings(vaniConfig: "/a/config.json", vaniStateDir: "/a/state"))

        try DraftSettings(anthropicWorkspace: "  wrkspc_01Test\n").save(to: dir)
        XCTAssertEqual(DraftSettings.load(from: dir).anthropicWorkspace, "wrkspc_01Test", "saved trimmed")
        XCTAssertThrowsError(try DraftSettings(anthropicWorkspace: String(repeating: "w", count: 129)).save(to: dir))
        XCTAssertThrowsError(try DraftSettings(anthropicWorkspace: "wrkspc\0x").save(to: dir))
    }
}
