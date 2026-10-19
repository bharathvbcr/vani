import Foundation
import Security

// Runs `vani draft` (Vani's Go CLI) for one answer draft. Vani resolves
// provider keys from the environment only (Manvi's credential policy), so the
// launcher supplies the key the way the owner's `manvi` shell function does:
// read from the login Keychain item "anthropic-api-key" and passed only into
// the child's environment. The key is never logged, never written to disk and
// never put in a reply. The child gets a minimal environment, not this
// process's, so nothing else inherited leaks into it either.

public struct DraftRequest: Codable, Equatable, Sendable {
    public struct Job: Codable, Equatable, Sendable {
        public var company: String?
        public var title: String?
        public var description: String?
        public init(company: String? = nil, title: String? = nil, description: String? = nil) {
            self.company = company
            self.title = title
            self.description = description
        }
    }
    public var question: String
    public var job: Job
    public var max_words: Int?

    public init(question: String, job: Job = Job(), maxWords: Int? = nil) {
        self.question = question
        self.job = job
        self.max_words = maxWords
    }

    // `job` and `max_words` may be absent; vani applies its own defaults.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        question = try c.decode(String.self, forKey: .question)
        job = try c.decodeIfPresent(Job.self, forKey: .job) ?? Job()
        max_words = try c.decodeIfPresent(Int.self, forKey: .max_words)
    }
}

public struct DraftReply: Codable, Equatable, Sendable {
    public var draft: String?
    public var model: String?
    public var evidence_items: Int?
    public var error: String?
    public var code: String?

    public init(draft: String? = nil, model: String? = nil, evidence_items: Int? = nil, error: String? = nil, code: String? = nil) {
        self.draft = draft
        self.model = model
        self.evidence_items = evidence_items
        self.error = error
        self.code = code
    }

    static func failure(_ code: String, _ message: String) -> DraftReply { DraftReply(error: message, code: code) }

    public var asJSONObject: [String: Any] {
        var out: [String: Any] = [:]
        if let draft { out["draft"] = draft }
        if let model { out["model"] = model }
        if let evidence_items { out["evidence_items"] = evidence_items }
        if let error { out["error"] = error }
        if let code { out["code"] = code }
        return out
    }
}

/// Where the Anthropic key comes from. The default reads the login Keychain.
public typealias KeyLookup = @Sendable () throws -> String?

public enum Keychain {
    public static let anthropicService = "anthropic-api-key"

    /// The generic password for (service, current user), or nil when there is none.
    /// macOS asks the user the first time a new binary reads the item.
    public static func genericPassword(service: String, account: String = NSUserName()) throws -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var out: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &out)
        switch status {
        case errSecSuccess:
            guard let data = out as? Data, let s = String(data: data, encoding: .utf8) else { return nil }
            return s.trimmingCharacters(in: .whitespacesAndNewlines)
        case errSecItemNotFound:
            return nil
        default:
            throw BankError("Keychain refused access to \(service) (status \(status))")
        }
    }
}

public struct DraftRunner: Sendable {
    /// Ceiling on one draft, including loading the knowledge base.
    public static let timeout: TimeInterval = 150
    static let maxOutputBytes = 256 * 1024

    public let vaniBinary: URL
    public let configPath: String?
    public let stateDir: String?
    /// The Anthropic workspace for a key not scoped to one (ANTHROPIC_WORKSPACE_ID).
    public let workspace: String?
    let key: KeyLookup
    let timeout: TimeInterval

    public init(vaniBinary: URL, configPath: String? = nil, stateDir: String? = nil,
                workspace: String? = nil,
                timeout: TimeInterval = DraftRunner.timeout,
                key: @escaping KeyLookup = { try Keychain.genericPassword(service: Keychain.anthropicService) }) {
        self.vaniBinary = vaniBinary
        self.configPath = configPath
        self.stateDir = stateDir
        self.workspace = workspace
        self.timeout = timeout
        self.key = key
    }

    /// Validate a request given as Foundation JSON objects (from a browser) into a DraftRequest.
    public static func request(from raw: Any?) throws -> DraftRequest {
        guard let dict = raw as? [String: Any] else { throw BankError("draft needs a request object") }
        let allowed: Set<String> = ["question", "job", "max_words"]
        if let extra = dict.keys.first(where: { !allowed.contains($0) }) {
            throw BankError("draft request has an unknown field \(String(extra.prefix(40)))")
        }
        let data = try JSONSerialization.data(withJSONObject: dict)
        do { return try JSONDecoder().decode(DraftRequest.self, from: data) } catch {
            throw BankError("draft request is malformed")
        }
    }

    /// Run one draft. Never throws: every failure is a reply with a code.
    public func run(_ request: DraftRequest) -> DraftReply {
        guard FileManager.default.isExecutableFile(atPath: vaniBinary.path) else {
            return .failure("config", "the vani binary is missing at \(vaniBinary.path)")
        }
        let apiKey: String?
        do { apiKey = try key() } catch {
            return .failure("keychain", String(describing: error))
        }
        let input: Data
        do { input = try JSONEncoder().encode(request) } catch {
            return .failure("bad_request", "could not encode the request")
        }

        var env = VaniProcess.environment(configPath: configPath, stateDir: stateDir)
        if let apiKey, !apiKey.isEmpty { env["ANTHROPIC_API_KEY"] = apiKey }
        if let workspace, !workspace.isEmpty { env["ANTHROPIC_WORKSPACE_ID"] = workspace }

        let out = VaniProcess.run(vaniBinary, arguments: ["draft"], environment: env, input: input,
                                  timeout: timeout, maxOutputBytes: Self.maxOutputBytes)
        if let launchError = out.launchError { return .failure("config", launchError) }
        if out.timedOut { return .failure("timeout", "vani draft did not finish within \(Int(timeout)) s") }
        guard let reply = try? JSONDecoder().decode(DraftReply.self, from: out.stdout),
              reply.draft != nil || reply.error != nil else {
            return .failure("provider", "vani draft exited \(out.status ?? -1) without a reply")
        }
        return reply
    }
}
