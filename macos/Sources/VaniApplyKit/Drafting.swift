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
    /// The decision model's key (OpenRouter), passed to `vani suggest` as OPENROUTER_API_KEY.
    public static let openRouterService = "openrouter-api-key"

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
    /// The OpenRouter key for the decision model, read only for `vani suggest`.
    let decisionKey: KeyLookup
    let timeout: TimeInterval
    /// Ceiling on each Keychain read, prompt included.
    public static let keychainTimeout: TimeInterval = 60
    let keychainTimeout: TimeInterval

    public init(vaniBinary: URL, configPath: String? = nil, stateDir: String? = nil,
                workspace: String? = nil,
                timeout: TimeInterval = DraftRunner.timeout,
                keychainTimeout: TimeInterval = DraftRunner.keychainTimeout,
                key: @escaping KeyLookup = { try Keychain.genericPassword(service: Keychain.anthropicService) },
                decisionKey: @escaping KeyLookup = { try Keychain.genericPassword(service: Keychain.openRouterService) }) {
        self.vaniBinary = vaniBinary
        self.configPath = configPath
        self.stateDir = stateDir
        self.workspace = workspace
        self.timeout = timeout
        self.keychainTimeout = keychainTimeout
        self.key = key
        self.decisionKey = decisionKey
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
        let input: Data
        do { input = try JSONEncoder().encode(request) } catch {
            return .failure("bad_request", "could not encode the request")
        }
        switch invoke("draft", input: input, timeout: timeout) {
        case let .failure(f):
            return f
        case let .output(data):
            guard let reply = try? JSONDecoder().decode(DraftReply.self, from: data),
                  reply.draft != nil || reply.error != nil else {
                return .failure("provider", "vani draft exited without a reply")
            }
            return reply
        }
    }

    /// Run `vani <kind>` on a JSON request for the agent and return the reply
    /// as JSON bytes: vani's own object when it wrote one (it always has
    /// "error" on failure), else a {"error","code"} object. Never throws.
    public func runJob(_ kind: JobKind, input: Data) -> Data {
        let failure: DraftReply
        switch invoke(kind.rawValue, input: input, timeout: kind == .draft ? timeout : kind.timeout, withDecisionKey: kind == .suggest) {
        case let .failure(f):
            failure = f
        case let .output(data):
            if (try? JSONSerialization.jsonObject(with: data)) is [String: Any] { return data }
            failure = .failure("provider", "vani \(kind.rawValue) exited without a reply")
        }
        return (try? JSONSerialization.data(withJSONObject: failure.asJSONObject)) ?? Data(#"{"error":"encoding failed","code":"provider"}"#.utf8)
    }

    enum Invocation {
        case output(Data)
        case failure(DraftReply)
    }

    /// A Keychain read can block on a macOS "allow access" prompt that nobody
    /// answers; the agent has one job slot, so the wait is capped. A read that
    /// outlives the cap is abandoned (its thread ends when the prompt does).
    static func bounded(_ lookup: @escaping KeyLookup, seconds: TimeInterval) throws -> String? {
        let done = DispatchSemaphore(value: 0)
        let box = KeyBox()
        DispatchQueue.global(qos: .userInitiated).async {
            box.set(Result { try lookup() })
            done.signal()
        }
        guard done.wait(timeout: .now() + seconds) == .success, let r = box.get() else {
            throw BankError("the Keychain prompt was not answered within \(Int(seconds)) s; allow Vani Apply to read the key, then try again")
        }
        return try r.get()
    }

    /// Start `vani <subcommand>` with the keys in its environment only. The
    /// decision key is optional: without it (no Keychain item, or access
    /// denied) vani skips the OpenRouter model and asks Claude instead.
    func invoke(_ subcommand: String, input: Data, timeout: TimeInterval, withDecisionKey: Bool = false) -> Invocation {
        guard FileManager.default.isExecutableFile(atPath: vaniBinary.path) else {
            return .failure(.failure("config", "the vani binary is missing at \(vaniBinary.path)"))
        }
        let apiKey: String?
        do { apiKey = try Self.bounded(key, seconds: keychainTimeout) } catch {
            return .failure(.failure("keychain", String(describing: error)))
        }
        var env = VaniProcess.environment(configPath: configPath, stateDir: stateDir)
        if let apiKey, !apiKey.isEmpty { env["ANTHROPIC_API_KEY"] = apiKey }
        if let workspace, !workspace.isEmpty { env["ANTHROPIC_WORKSPACE_ID"] = workspace }
        if withDecisionKey, let k = try? Self.bounded(decisionKey, seconds: keychainTimeout), !k.isEmpty {
            env["OPENROUTER_API_KEY"] = k
        }

        let out = VaniProcess.run(vaniBinary, arguments: [subcommand], environment: env, input: input,
                                  timeout: timeout, maxOutputBytes: Self.maxOutputBytes)
        if let launchError = out.launchError { return .failure(.failure("config", launchError)) }
        if out.timedOut { return .failure(.failure("timeout", "vani \(subcommand) did not finish within \(Int(timeout)) s")) }
        return .output(out.stdout)
    }
}

/// One Keychain result crossing threads.
private final class KeyBox: @unchecked Sendable {
    private let lock = NSLock()
    private var value: Result<String?, Error>?
    func set(_ v: Result<String?, Error>) { lock.lock(); value = v; lock.unlock() }
    func get() -> Result<String?, Error>? { lock.lock(); defer { lock.unlock() }; return value }
}
