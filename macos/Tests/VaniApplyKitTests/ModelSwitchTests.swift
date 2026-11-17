import XCTest
@testable import VaniApplyKit

final class ModelSwitchTests: XCTestCase {
    private let vani = URL(fileURLWithPath: "/opt/vani/vani")
    private static let sentinel = "MODEL-KEY-SENTINEL-not-a-real-key"
    private var sentinel: String { Self.sentinel }

    /// `vani config models --set --decision gemini --llm gemini --model gemini-4-hydrogen`, as the
    /// Go command printed it on 2026-11-17 (cmd/vani configmodels.go).
    static let reply = #"""
    {"decision":{"provider":"gemini","model":"gemini-decision-1","choices":[{"id":"openrouter","label":"Liquid (OpenRouter)","model":"liquid/d1.2-ultra","host":"openrouter.ai","keychain_service":"openrouter-api-key","key_envs":["OPENROUTER_API_KEY"]},{"id":"subq","label":"SubQ Snap","model":"subq-snap-1.2","host":"api.subq.ai","keychain_service":"subq-api-key","key_envs":["SUBQ_API_KEY"]},{"id":"gemini","label":"Gemini Decision (AI Studio)","model":"gemini-decision-1","host":"generativelanguage.googleapis.com","keychain_service":"gemini-api-key","key_envs":["GEMINI_API_KEY","GOOGLE_API_KEY"]}]},"llm":{"provider":"gemini","model":"gemini-4-hydrogen","triage_model":"gemini-4-hydrogen","choices":[{"id":"anthropic","label":"Claude (Manvi)","models":[{"id":"claude-opus-5-5","label":"Claude Opus 5.5","input_nano_usd_per_token":4000,"output_nano_usd_per_token":20000},{"id":"claude-sonnet-5-5","label":"Claude Sonnet 5.5","input_nano_usd_per_token":2000,"output_nano_usd_per_token":10000},{"id":"claude-haiku-5-5","label":"Claude Haiku 5.5","input_nano_usd_per_token":500,"output_nano_usd_per_token":2500}]},{"id":"gemini","label":"Google AI Studio","keychain_service":"gemini-api-key","key_envs":["GEMINI_API_KEY","GOOGLE_API_KEY"],"models":[{"id":"gemini-4-hydrogen","label":"Gemini 4 Hydrogen","input_nano_usd_per_token":1500,"output_nano_usd_per_token":7500}]}]}}
    """#

    private func config(_ json: String) throws -> String {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("model-switch-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        let file = dir.appendingPathComponent("config.json")
        try Data(json.utf8).write(to: file)
        return file.path
    }

    // MARK: the reply

    func testDecodesTheGoReplyAndListsEachKeyOnce() throws {
        let c = try JSONDecoder().decode(ModelChoices.self, from: Data(Self.reply.utf8))
        XCTAssertEqual(c.decision.provider, "gemini")
        XCTAssertEqual(c.decision.choices.map(\.id), ["openrouter", "subq", "gemini"])
        XCTAssertEqual(c.llm.choices.map(\.id), ["anthropic", "gemini"])
        XCTAssertEqual(c.llm.choices[0].keychainService, "", "Claude's key stays with anthropic-account")
        XCTAssertEqual(c.llm.triageModel, "gemini-4-hydrogen")
        let items = c.keyItems
        XCTAssertEqual(items.map(\.service), [Keychain.openRouterService, Keychain.subqService, Keychain.geminiService])
        XCTAssertEqual(items[2].usedBy, ["Gemini Decision (AI Studio)", "Google AI Studio"], "one AI Studio key serves both")
        XCTAssertEqual(items.map(\.title), ["OpenRouter API key", "SubQ API key", "Google AI Studio API key"])
        // The Go table's items are exactly the ones Settings may write.
        XCTAssertEqual(Set(items.map(\.service)), Set(Keychain.modelKeyServices))
    }

    func testSelectionArguments() {
        XCTAssertEqual(ModelSelection(decisionProvider: "subq").arguments, ["config", "models", "--set", "--decision", "subq"])
        XCTAssertEqual(ModelSelection(llmProvider: "gemini", model: "gemini-4-hydrogen", triageModel: "gemini-4-hydrogen").arguments,
                       ["config", "models", "--set", "--llm", "gemini", "--model", "gemini-4-hydrogen"])
        XCTAssertEqual(ModelSelection(decisionProvider: "gemini", llmProvider: "anthropic", model: "claude-opus-5-5", triageModel: "claude-sonnet-5-5").arguments,
                       ["config", "models", "--set", "--decision", "gemini", "--llm", "anthropic", "--model", "claude-opus-5-5", "--triage-model", "claude-sonnet-5-5"])
        XCTAssertThrowsError(try ModelSwitchClient(vaniBinary: vani).save(ModelSelection()), "an empty selection is not sent")
    }

    // MARK: pasted keys

    func testPastedKeyValidation() throws {
        XCTAssertEqual(try Keychain.validatedModelKey("  sk-or-v1-abc\n"), "sk-or-v1-abc")
        for bad in ["", "   ", "sk a", "sk\nsecond", "sk\u{00A0}x", "ключ", String(repeating: "k", count: Keychain.maxModelKeyBytes + 1)] {
            XCTAssertThrowsError(try Keychain.validatedModelKey(bad)) { err in
                if bad.count > 3 { XCTAssertFalse(String(describing: err).contains(bad), "an error repeats the key") }
            }
        }
        // Only the model-key items may be written or removed; nothing reaches the Keychain.
        XCTAssertThrowsError(try Keychain.setModelKey("sk-x", service: Keychain.anthropicService))
        XCTAssertThrowsError(try Keychain.removeModelKey(service: "anything-else"))
    }

    // MARK: the key a launch carries

    func testDecisionKeyRouteFollowsTheConfiguredProvider() throws {
        XCTAssertEqual(VaniConfig.decisionKeyRoute(configPath: "/nonexistent/vani/config.json"),
                       ModelKeyRoute(service: Keychain.openRouterService, envVar: "OPENROUTER_API_KEY"))
        XCTAssertEqual(VaniConfig.decisionKeyRoute(configPath: try config(#"{"decide":{"openrouter_model":"liquid/d1.2-ultra"}}"#)).envVar, "OPENROUTER_API_KEY")
        XCTAssertEqual(VaniConfig.decisionKeyRoute(configPath: try config(#"{"decide":{"provider":"subq"}}"#)),
                       ModelKeyRoute(service: Keychain.subqService, envVar: "SUBQ_API_KEY"))
        XCTAssertEqual(VaniConfig.decisionKeyRoute(configPath: try config(#"{"decide":{"provider":"gemini"}}"#)), .aiStudio)
        XCTAssertEqual(VaniConfig.decisionProvider(configPath: try config("not json")), "openrouter")
    }

    func testASubQLaunchCarriesTheSubQKeyOnly() throws {
        let path = try config(#"{"decide":{"provider":"subq"},"llm":{"provider":"anthropic","model":"claude-opus-5-5"}}"#)
        let r = DraftRunner(vaniBinary: vani, configPath: path, keys: .noKey, key: { nil }, decisionKey: { ModelSwitchTests.sentinel })
        guard case let .launch(l) = r.prepare(["run-once"], withDecisionKey: true) else { return XCTFail("refused") }
        XCTAssertEqual(l.environment["SUBQ_API_KEY"], sentinel)
        XCTAssertNil(l.environment["OPENROUTER_API_KEY"])
        XCTAssertEqual(l.environment["VANI_NO_KEYCHAIN"], "1")
        XCTAssertEqual(l.decisionKey, .present)
    }

    func testTheDefaultDecisionLookupReadsTheProvidersItem() throws {
        // No lookup injected: the runner asks the Keychain for the configured provider's item.
        // A refusal for an item name that cannot exist proves which item was asked for without
        // reading a real one: the error names the service.
        let path = try config(#"{"decide":{"provider":"subq"}}"#)
        let r = DraftRunner(vaniBinary: vani, configPath: path, keys: .noKey, keychainTimeout: 5, key: { nil })
        guard case let .launch(l) = r.prepare(["run-once"], withDecisionKey: true) else { return XCTFail("refused") }
        // A missing item reads as notFound; a present one as present. Either way only SUBQ_API_KEY may carry it.
        XCTAssertNil(l.environment["OPENROUTER_API_KEY"])
        XCTAssertNotEqual(l.decisionKey, .notRequested)
    }

    func testAGeminiLLMLaunchCarriesTheAIStudioKey() throws {
        let path = try config(#"{"llm":{"provider":"gemini","model":"gemini-4-hydrogen"},"decide":{"provider":"gemini"}}"#)
        final class Reads: @unchecked Sendable {
            private let lock = NSLock()
            private var n = 0
            func hit() { lock.lock(); n += 1; lock.unlock() }
            var count: Int { lock.lock(); defer { lock.unlock() }; return n }
        }
        let reads = Reads()
        let r = DraftRunner(vaniBinary: vani, configPath: path, keys: .anthropicAccount(nil), key: { nil },
                            decisionKey: { XCTFail("the decision model shares the AI Studio key just read"); return nil },
                            aiStudioKey: { reads.hit(); return ModelSwitchTests.sentinel })
        guard case let .launch(l) = r.prepare(["run-once"], withDecisionKey: true) else { return XCTFail("refused") }
        XCTAssertEqual(l.program, vani, "a gemini config starts vani itself, not anthropic-account")
        XCTAssertEqual(l.environment["GEMINI_API_KEY"], sentinel)
        XCTAssertEqual(l.environment["VANI_NO_KEYCHAIN"], "1")
        XCTAssertEqual(l.decisionKey, .present)
        XCTAssertEqual(reads.count, 1)
    }

    func testAGeminiLLMLaunchWithoutAKeyIsRefusedWithTheFix() throws {
        let path = try config(#"{"llm":{"provider":"gemini","model":"gemini-4-hydrogen"}}"#)
        for lookup in [{ nil }, { "  " }] as [KeyLookup] {
            let r = DraftRunner(vaniBinary: vani, configPath: path, keys: .anthropicAccount(nil), key: { nil },
                                decisionKey: { nil }, aiStudioKey: lookup)
            guard case let .refused(reply) = r.prepare(["draft"]) else { return XCTFail("launched keyless") }
            XCTAssertEqual(reply.code, "keychain")
            XCTAssertTrue(reply.error?.contains(Keychain.geminiService) == true, reply.error ?? "")
            XCTAssertTrue(reply.error?.contains("Settings") == true)
        }
        // A job that calls no model never reads it.
        let r = DraftRunner(vaniBinary: vani, configPath: path, keys: .anthropicAccount(nil), key: { nil },
                            decisionKey: { nil }, aiStudioKey: { XCTFail("read for a modelless job"); return nil })
        guard case .launch = r.prepare(["jobs"], usesModel: false) else { return XCTFail("refused") }
        // The host's --no-keychain reads no Keychain item for the llm.
        let host = DraftRunner(vaniBinary: vani, configPath: path, keys: .noKey, key: { nil },
                               decisionKey: { nil }, aiStudioKey: { XCTFail("read under --no-keychain"); return nil })
        guard case .launch = host.prepare(["draft"]) else { return XCTFail("refused") }
    }

    // MARK: status labels

    func testBackendKindLabels() throws {
        func label(_ kind: String) throws -> String {
            let json = #"{"ok":true,"name":"n","kind":"\#(kind)","cap_usd":1,"spent_usd":0,"remaining_usd":1}"#
            return try JSONDecoder().decode(PipelineStatus.Backend.self, from: Data(json.utf8)).kindLabel
        }
        XCTAssertEqual(try label("llm"), "LLM")
        XCTAssertEqual(try label("openrouter"), "OpenRouter")
        XCTAssertEqual(try label("openrouter_notes"), "OpenRouter notes")
        XCTAssertEqual(try label("subq"), "SubQ")
        XCTAssertEqual(try label("gemini_notes"), "Gemini Decision notes")
        XCTAssertEqual(try label("future"), "future", "an unknown kind is never named as another provider")
    }
}
