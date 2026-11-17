import Foundation

/// Which decision model and which language model Vani uses, and every choice it offers, as
/// `vani config models` replies. Vani owns the provider tables, the prices and the write; the
/// app shows what it replies and saves a choice through it. Keys are never part of a reply.
public struct ModelChoices: Decodable, Equatable, Sendable {
    public var decision: Decision
    public var llm: LLM

    public struct Decision: Decodable, Equatable, Sendable {
        public var provider: String
        public var model: String
        public var choices: [DecisionChoice]
    }

    public struct DecisionChoice: Decodable, Equatable, Sendable, Identifiable {
        public var id: String
        public var label: String
        public var model: String
        public var host: String
        public var keychainService: String
        public var keyEnvs: [String]

        enum CodingKeys: String, CodingKey {
            case id, label, model, host
            case keychainService = "keychain_service"
            case keyEnvs = "key_envs"
        }
    }

    public struct LLM: Decodable, Equatable, Sendable {
        public var provider: String
        public var model: String
        public var triageModel: String
        public var choices: [LLMProvider]

        enum CodingKeys: String, CodingKey {
            case provider, model, choices
            case triageModel = "triage_model"
        }
    }

    public struct LLMProvider: Decodable, Equatable, Sendable, Identifiable {
        public var id: String
        public var label: String
        /// Empty for Claude, whose key anthropic-account supplies.
        public var keychainService: String
        public var models: [LLMModel]

        enum CodingKeys: String, CodingKey {
            case id, label, models
            case keychainService = "keychain_service"
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            label = try c.decode(String.self, forKey: .label)
            keychainService = try c.decodeIfPresent(String.self, forKey: .keychainService) ?? ""
            models = try c.decode([LLMModel].self, forKey: .models)
        }
    }

    public struct LLMModel: Decodable, Equatable, Sendable, Identifiable {
        public var id: String
        public var label: String
    }

    /// The model keys Settings lets the user paste: each Keychain item once, with what uses it.
    public var keyItems: [ModelKeyItem] {
        var out: [ModelKeyItem] = []
        func add(_ service: String, _ label: String) {
            guard !service.isEmpty else { return }
            if let i = out.firstIndex(where: { $0.service == service }) {
                if !out[i].usedBy.contains(label) { out[i].usedBy.append(label) }
            } else {
                out.append(ModelKeyItem(service: service, usedBy: [label]))
            }
        }
        for d in decision.choices { add(d.keychainService, d.label) }
        for l in llm.choices { add(l.keychainService, l.label) }
        return out
    }
}

/// One login Keychain item a model key is pasted into.
public struct ModelKeyItem: Equatable, Sendable, Identifiable {
    public var service: String
    /// The choices that read this key ("SubQ Snap", "Google AI Studio", ...).
    public var usedBy: [String]
    public var id: String { service }

    /// The name Settings shows for the key.
    public var title: String {
        switch service {
        case Keychain.openRouterService: "OpenRouter API key"
        case Keychain.subqService: "SubQ API key"
        case Keychain.geminiService: "Google AI Studio API key"
        default: service
        }
    }
}

/// What the switch saves: an empty field leaves that part of the config as it is.
public struct ModelSelection: Equatable, Sendable {
    public var decisionProvider: String
    public var llmProvider: String
    public var model: String
    public var triageModel: String

    public init(decisionProvider: String = "", llmProvider: String = "", model: String = "", triageModel: String = "") {
        self.decisionProvider = decisionProvider
        self.llmProvider = llmProvider
        self.model = model
        self.triageModel = triageModel
    }

    /// `vani config models --set` arguments for this selection.
    public var arguments: [String] {
        var args = ["config", "models", "--set"]
        if !decisionProvider.isEmpty { args += ["--decision", decisionProvider] }
        if !llmProvider.isEmpty {
            args += ["--llm", llmProvider, "--model", model]
            if !triageModel.isEmpty, triageModel != model { args += ["--triage-model", triageModel] }
        }
        return args
    }
}

public struct ModelSwitchClient: Sendable {
    public static let timeout: TimeInterval = 15
    static let maxOutputBytes = 64 << 10

    public let vaniBinary: URL
    public let configPath: String?
    public let stateDir: String?
    let timeout: TimeInterval

    public init(vaniBinary: URL, configPath: String? = nil, stateDir: String? = nil, timeout: TimeInterval = ModelSwitchClient.timeout) {
        self.vaniBinary = vaniBinary
        self.configPath = configPath
        self.stateDir = stateDir
        self.timeout = timeout
    }

    public func load() throws -> ModelChoices {
        try call(["config", "models"])
    }

    /// Saves the selection. Vani validates the whole config before it writes, so a refused
    /// selection leaves the file as it was.
    public func save(_ selection: ModelSelection) throws -> ModelChoices {
        guard !selection.decisionProvider.isEmpty || !selection.llmProvider.isEmpty else {
            throw BankError("Choose a decision model or a language model to save")
        }
        return try call(selection.arguments)
    }

    private func call(_ args: [String]) throws -> ModelChoices {
        try VaniProcess.call(vaniBinary, args, configPath: configPath, stateDir: stateDir, input: nil,
                             timeout: timeout, maxOutputBytes: Self.maxOutputBytes,
                             tooOld: "this vani is too old for the model switch; rebuild the app")
    }
}

extension DraftSettings {
    /// The model switch for the same config drafting uses.
    public func modelSwitchClient(vaniBinary: URL) -> ModelSwitchClient {
        ModelSwitchClient(vaniBinary: vaniBinary,
                          configPath: vaniConfig.isEmpty ? nil : vaniConfig,
                          stateDir: vaniStateDir.isEmpty ? nil : vaniStateDir)
    }
}

/// Reads, saves and removes model keys. The live store is the login Keychain; tests use a fake.
public protocol ModelKeyStore: Sendable {
    func exists(service: String) -> Bool?
    func save(_ key: String, service: String) throws
    func remove(service: String) throws
}

public struct KeychainModelKeyStore: ModelKeyStore {
    public init() {}
    public func exists(service: String) -> Bool? { Keychain.itemExists(service: service) }
    public func save(_ key: String, service: String) throws { try Keychain.setModelKey(key, service: service) }
    public func remove(service: String) throws { try Keychain.removeModelKey(service: service) }
}
