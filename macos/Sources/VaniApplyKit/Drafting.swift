import Foundation

// Runs `vani draft` (Vani's Go CLI) for one answer draft. Vani resolves
// provider keys from the environment only (Manvi's credential policy). When
// Vani's config names the anthropic provider, `vani` is started through
// `anthropic-account exec`, the one place on this Mac that picks the Console
// organisation and puts its key (and workspace, if listed) into the child's
// environment. This process never reads, holds or passes an Anthropic key.
// The child gets a minimal environment, not this process's, so nothing else
// inherited leaks into it either.

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
    /// A later wording of the same question. Nil and 0 are a first draft and are omitted on the wire.
    /// The extension's counter, never page text. Out of 0...8 is refused.
    public var attempt: Int?

    public init(question: String, job: Job = Job(), maxWords: Int? = nil, attempt: Int? = nil) {
        self.question = question
        self.job = job
        self.max_words = maxWords
        self.attempt = attempt
    }

    // `job` and `max_words` may be absent; vani applies its own defaults.
    // `attempt` is omitted when it is nil or 0, so a first draft stays the old request.
    // Both methods are written out, so the keys are too: a custom encode and
    // decode together are not given a synthesized CodingKeys.
    private enum CodingKeys: String, CodingKey {
        case question, job, max_words, attempt
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        question = try c.decode(String.self, forKey: .question)
        job = try c.decodeIfPresent(Job.self, forKey: .job) ?? Job()
        max_words = try c.decodeIfPresent(Int.self, forKey: .max_words)
        attempt = try c.decodeIfPresent(Int.self, forKey: .attempt)
        if let attempt, attempt < 0 || attempt > 8 {
            throw DecodingError.dataCorruptedError(forKey: .attempt, in: c, debugDescription: "attempt is out of range")
        }
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(question, forKey: .question)
        try c.encode(job, forKey: .job)
        try c.encodeIfPresent(max_words, forKey: .max_words)
        if let attempt, attempt > 0 {
            try c.encode(attempt, forKey: .attempt)
        }
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

/// Manvi's `anthropic-account` launcher: where it is installed, which accounts
/// it lists, and the argv that starts a command through it. It reads no secret
/// here; only its `exec` does, inside the child it becomes.
public enum AnthropicAccount {
    /// Where the launcher is installed (`go install` / Homebrew), checked in order.
    public static var candidates: [URL] {
        [URL(fileURLWithPath: NSHomeDirectory() + "/.local/bin/anthropic-account"),
         URL(fileURLWithPath: "/opt/homebrew/bin/anthropic-account")]
    }

    /// The first executable candidate, or nil when the launcher is not installed.
    public static func locate(candidates: [URL] = AnthropicAccount.candidates) -> URL? {
        candidates.first { FileManager.default.isExecutableFile(atPath: $0.path) }
    }

    /// The launcher's own account-name rule (anthropic-account's accountName),
    /// plus no leading "-" so a saved name never reads as a flag, checked here
    /// so a bad saved name never reaches its argv.
    public static func isValidName(_ name: String) -> Bool {
        !name.isEmpty && name.utf8.count <= 64 && !name.hasPrefix("-")
            && name.unicodeScalars.allSatisfy { CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-").contains($0) }
    }

    /// `anthropic-account exec [--account NAME] -- CMD ARGS…`. An empty account
    /// leaves the pick to the launcher (its registry's first line by default).
    public static func execArguments(account: String?, command: URL, arguments: [String]) -> [String] {
        var argv = ["exec"]
        if let account, !account.isEmpty { argv += ["--account", account] }
        return argv + ["--", command.path] + arguments
    }

    /// The launcher's refusal, from its stderr: only lines it wrote itself
    /// ("anthropic-account: …"), which name accounts and files but never a key.
    /// Nil when it wrote none, so the failure is the child's.
    static func refusal(in stderr: Data) -> String? {
        let text = String(decoding: stderr.prefix(16 * 1024), as: UTF8.self)
        guard let first = text.split(separator: "\n").first,
              first.hasPrefix("anthropic-account: "), !first.hasPrefix("anthropic-account: warning:") else { return nil }
        return String(text.prefix(1500)).trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// The account names `anthropic-account list --json` reports, in registry
    /// order (the first is the default). Throws the launcher's own refusal, such
    /// as no ~/.anthropic-accounts yet.
    public static func list(launcher: URL, timeout: TimeInterval = 10) throws -> [String] {
        let out = VaniProcess.run(launcher, arguments: ["list", "--json"],
                                  environment: VaniProcess.environment(configPath: nil, stateDir: nil),
                                  timeout: timeout, maxOutputBytes: 64 * 1024)
        if let launchError = out.launchError { throw BankError(launchError) }
        if out.timedOut { throw BankError("anthropic-account list did not finish within \(Int(timeout)) s") }
        guard out.status == 0 else {
            throw BankError(refusal(in: out.stderr) ?? "anthropic-account list exited \(out.status ?? -1)")
        }
        struct Row: Decodable { let name: String }
        guard let rows = try? JSONDecoder().decode([Row].self, from: out.stdout) else {
            throw BankError("anthropic-account list --json printed something that is not an account list")
        }
        return rows.map(\.name).filter(isValidName)
    }
}

/// Which LLM provider Vani's config names (`llm.provider`), read leniently:
/// only that key matters here, and judging the rest is vani's job. Nil when
/// the file is missing, unreadable, oversized or names none, so vani runs
/// without the launcher and reports its own config error.
public enum VaniConfig {
    static let maxBytes = 1 << 20

    public static func llmProvider(configPath: String?) -> String? {
        let path = configPath.flatMap { $0.isEmpty ? nil : $0 } ?? VaniProcess.defaultConfigPath
        guard let h = FileHandle(forReadingAtPath: path) else { return nil }
        defer { try? h.close() }
        guard let data = try? h.read(upToCount: maxBytes + 1), data.count <= maxBytes else { return nil }
        struct File: Decodable {
            struct LLM: Decodable { let provider: String? }
            let llm: LLM?
        }
        return (try? JSONDecoder().decode(File.self, from: data))?.llm?.provider
    }
}

/// Where an injected key lookup comes from.
public typealias KeyLookup = @Sendable () throws -> String?

public enum Keychain {
    public static let anthropicService = "anthropic-api-key"
    /// The decision model's key (OpenRouter), passed to `vani suggest` as OPENROUTER_API_KEY.
    public static let openRouterService = "openrouter-api-key"

    /// Whether an item for (service, current user) exists, without reading its value: an
    /// attributes-only query is never an access prompt. Nil when the Keychain cannot say.
    public static func itemExists(service: String, account: String = NSUserName()) -> Bool? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnAttributes as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var out: CFTypeRef?
        switch SecItemCopyMatching(query as CFDictionary, &out) {
        case errSecSuccess: return true
        case errSecItemNotFound: return false
        default: return nil
        }
    }

    /// The generic password for (service, current user), or nil when there is none.
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
    public let keys: KeySource
    /// The account to pass as --account; nil or empty lets the launcher pick.
    public let account: String?
    public let workspace: String?
    let key: KeyLookup
    let decisionKey: KeyLookup
    let timeout: TimeInterval
    public static let keychainTimeout: TimeInterval = 60
    let keychainTimeout: TimeInterval
    /// When set, every launch (`prepare`, so draft, suggest, resume and run-once) is refused with
    /// this reply instead of starting vani: the settings that say which config and account to
    /// use could not be read, and defaults would run against the wrong ones.
    public let refusal: DraftReply?

    /// How vani gets an Anthropic key.
    public enum KeySource: Sendable, Equatable {
        /// No key at all: vani starts directly (the host's --no-keychain).
        case noKey
        /// Through this `anthropic-account` when the config names the anthropic
        /// provider; nil when it is not installed, which fails such a run with
        /// code "account" instead of starting vani keyless.
        case anthropicAccount(URL?)
    }

    public init(vaniBinary: URL, configPath: String? = nil, stateDir: String? = nil,
                keys: KeySource = .noKey, account: String? = nil,
                workspace: String? = nil,
                timeout: TimeInterval = DraftRunner.timeout,
                keychainTimeout: TimeInterval = DraftRunner.keychainTimeout,
                key: @escaping KeyLookup = { nil },
                decisionKey: @escaping KeyLookup = { try Keychain.genericPassword(service: Keychain.openRouterService) },
                refusal: DraftReply? = nil) {
        self.refusal = refusal
        self.vaniBinary = vaniBinary
        self.configPath = configPath
        self.stateDir = stateDir
        self.keys = keys
        self.account = account
        self.workspace = workspace
        self.timeout = timeout
        self.keychainTimeout = keychainTimeout
        self.key = key
        self.decisionKey = decisionKey
    }

    /// Validate a request given as Foundation JSON objects (from a browser) into a DraftRequest.
    public static func request(from raw: Any?) throws -> DraftRequest {
        guard let dict = raw as? [String: Any] else { throw BankError("draft needs a request object") }
        let allowed: Set<String> = ["question", "job", "max_words", "attempt"]
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
        if let refusal { return refusal }
        guard FileManager.default.isExecutableFile(atPath: vaniBinary.path) else {
            return .failure("config", "the vani binary is missing at \(vaniBinary.path)")
        }
        let input: Data
        do { input = try JSONEncoder().encode(request) } catch {
            return .failure("bad_request", "could not encode the request")
        }
        switch invoke("draft", input: input) {
        case let .failure(f):
            return f
        case let .output(out):
            guard let reply = try? JSONDecoder().decode(DraftReply.self, from: out.stdout),
                  reply.draft != nil || reply.error != nil else {
                if keys != .noKey, let refusal = AnthropicAccount.refusal(in: out.stderr) {
                    return .failure("account", refusal)
                }
                return .failure("provider", "vani draft exited \(out.status ?? -1) without a reply")
            }
            return reply
        }
    }

    /// Run `vani <kind>` on a JSON request for the agent and return the reply
    /// as JSON bytes: vani's own object when it wrote one (it always has
    /// "error" on failure), else a {"error","code"} object. Never throws.
    /// Run `vani <kind>` on a JSON request for the agent and return the reply
    /// as JSON bytes: vani's own object when it wrote one (it always has
    /// "error" on failure), else a {"error","code"} object. Never throws.
    public func runJob(_ kind: JobKind, input: Data) -> Data {
        let failure: DraftReply
        switch invoke(kind.rawValue, input: input, timeout: kind == .draft ? timeout : kind.timeout, withDecisionKey: kind == .suggest,
                      usesModel: kind.usesModel, maxOutputBytes: kind.maxReplyBytes) {
        case let .failure(f):
            failure = f
        case let .output(out):
            if (try? JSONSerialization.jsonObject(with: out.stdout)) is [String: Any] { return out.stdout }
            if keys != .noKey, let refusal = AnthropicAccount.refusal(in: out.stderr) {
                failure = .failure("account", refusal)
            } else {
                failure = .failure("provider", "vani \(kind.rawValue) exited \(out.status ?? -1) without a reply")
            }
        }
        return (try? JSONSerialization.data(withJSONObject: failure.asJSONObject)) ?? Data(#"{"error":"encoding failed","code":"provider"}"#.utf8)
    }

    enum Invocation {
        case output(VaniProcess.Outcome)
        case failure(DraftReply)
    }

    enum Command {
        case run(URL, [String])
        case refused(DraftReply)
    }

    /// The program and argv that start `vani <arguments>`: through
    /// anthropic-account when Vani's config names the anthropic provider, else
    /// vani itself. Both paths are absolute, because the child's PATH is only
    /// the system folders.
    func command(_ arguments: [String], usesModel: Bool = true) -> Command {
        guard usesModel, case let .anthropicAccount(launcher) = keys,
              VaniConfig.llmProvider(configPath: configPath) == "anthropic" else {
            return .run(vaniBinary, arguments)
        }
        guard let launcher, FileManager.default.isExecutableFile(atPath: launcher.path) else {
            let looked = launcher.map { [$0] } ?? AnthropicAccount.candidates
            return .refused(.failure("account", "anthropic-account is not installed (looked at \(looked.map(\.path).joined(separator: ", "))); Vani's config names the anthropic provider, which needs it"))
        }
        if let account, !account.isEmpty, !AnthropicAccount.isValidName(account) {
            return .refused(.failure("account", "the saved Anthropic account name is not valid; choose one in Settings"))
        }
        return .run(launcher, AnthropicAccount.execArguments(account: account, command: vaniBinary, arguments: arguments))
    }

    /// How to start one `vani` job: the program, its argv and its environment,
    /// keys included.
    public struct Launch: Sendable {
        public var program: URL
        public var arguments: [String]
        public var environment: [String: String]
        /// What became of the decision model's key, so a launch that goes ahead
        /// without it can say so rather than start a model-less job unannounced.
        public var decisionKey: KeyOutcome = .notRequested
    }

    /// The decision model's key (OPENROUTER_API_KEY) as a launch found it.
    public enum KeyOutcome: Sendable, Equatable {
        /// The job does not take the key (`withDecisionKey` was false).
        case notRequested
        /// The Keychain gave a key, now in the child's environment only.
        case present
        /// The Keychain holds no such item (or an empty one).
        case notFound
        /// The read was refused or outlived its bound; the reason names the
        /// service and status, never a value.
        case failed(String)

        public var isPresent: Bool { self == .present }
    }

    /// The launch for `vani <arguments>` with the minimal environment and, for a
    /// job that calls a model, its keys (`addKeys`), or the reply that refuses
    /// it. It may wait on a Keychain prompt, up to `keychainTimeout`, so call it
    /// off the main thread.
    public func prepare(_ arguments: [String], withDecisionKey: Bool = false, usesModel: Bool = true) -> Prepared {
        if let refusal { return .refused(refusal) }
        let program: URL, argv: [String]
        switch command(arguments, usesModel: usesModel) {
        case let .refused(f): return .refused(f)
        case let .run(p, a): (program, argv) = (p, a)
        }
        var env = VaniProcess.environment(configPath: configPath, stateDir: stateDir)
        // A job that calls no model gets no model key, workspace or decision key.
        var outcome = KeyOutcome.notRequested
        if usesModel, let failure = addKeys(to: &env, withDecisionKey: withDecisionKey, outcome: &outcome) { return .refused(failure) }
        return .launch(Launch(program: program, arguments: argv, environment: env, decisionKey: outcome))
    }

    public enum Prepared: Sendable {
        case launch(Launch)
        case refused(DraftReply)
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

    /// Start `vani <subcommand>` with the minimal environment. No key is added
    /// here: on the anthropic path anthropic-account adds it after this
    /// environment, in the process it replaces itself with.
    func invoke(_ subcommand: String, input: Data, timeout: TimeInterval? = nil, withDecisionKey: Bool = false,
                usesModel: Bool = true, maxOutputBytes: Int = DraftRunner.maxOutputBytes) -> Invocation {
        invoke(arguments: [subcommand], input: input, timeout: timeout, withDecisionKey: withDecisionKey,
               usesModel: usesModel, maxOutputBytes: maxOutputBytes)
    }

    /// `vani <arguments>` through `prepare`, so the keys, launcher and refusal are the ones every
    /// other job gets. `extraEnvironment` is added to the launch's, after the keys; `cancellation`
    /// ends the child early (the reply is then the failure code "cancelled"). Nothing is read from
    /// the Keychain for a call with `usesModel` false.
    func invoke(arguments: [String], input: Data? = nil, timeout: TimeInterval? = nil, withDecisionKey: Bool = false,
                usesModel: Bool = true, maxOutputBytes: Int = DraftRunner.maxOutputBytes,
                extraEnvironment: [String: String] = [:], cancellation: CancelFlag? = nil) -> Invocation {
        let launch: Launch
        switch prepare(arguments, withDecisionKey: withDecisionKey, usesModel: usesModel) {
        case let .refused(f): return .failure(f)
        case let .launch(l): launch = l
        }
        let name = arguments.prefix { !$0.hasPrefix("-") }.prefix(2).joined(separator: " ")
        let t = timeout ?? self.timeout
        let out = VaniProcess.run(launch.program, arguments: launch.arguments,
                                  environment: launch.environment.merging(extraEnvironment) { _, extra in extra }, input: input,
                                  timeout: t, maxOutputBytes: maxOutputBytes, cancellation: cancellation)
        if out.cancelled { return .failure(.failure("cancelled", "vani \(name) was cancelled")) }
        if let launchError = out.launchError { return .failure(.failure("config", launchError)) }
        if out.timedOut { return .failure(.failure("timeout", "vani \(name) did not finish within \(Int(t)) s")) }
        return .output(out)
    }

    /// The model keys a job that spends budget is given; a reply when the Keychain read failed.
    private func addKeys(to env: inout [String: String], withDecisionKey: Bool, outcome: inout KeyOutcome) -> DraftReply? {
        if keys == .noKey {
            do {
                if let k = try Self.bounded(key, seconds: keychainTimeout), !k.isEmpty {
                    env["ANTHROPIC_API_KEY"] = k
                }
            } catch {
                return .failure("keychain", String(describing: error))
            }
        }
        if let workspace, !workspace.isEmpty { env["ANTHROPIC_WORKSPACE_ID"] = workspace }
        // The decision key is optional for a launch (the model is just off without it), but
        // never silently so: the outcome says which of absent, refused or unanswered it was.
        if withDecisionKey {
            // The app is the one reader of this key for a vani it starts: whatever it read
            // (or was refused, or was not answered in time) is the outcome, and vani must not
            // ask the Keychain again behind it (a second prompt, from another binary, after a
            // refusal; cmd/vani/keychain.go honours the variable).
            env["VANI_NO_KEYCHAIN"] = "1"
            do {
                if let k = try Self.bounded(decisionKey, seconds: keychainTimeout),
                   !k.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    env["OPENROUTER_API_KEY"] = k
                    outcome = .present
                } else {
                    outcome = .notFound
                }
            } catch {
                outcome = .failed(String(describing: error))
            }
        }
        return nil
    }
}

/// One Keychain result crossing threads.
private final class KeyBox: @unchecked Sendable {
    private let lock = NSLock()
    private var value: Result<String?, Error>?
    func set(_ v: Result<String?, Error>) { lock.lock(); value = v; lock.unlock() }
    func get() -> Result<String?, Error>? { lock.lock(); defer { lock.unlock() }; return value }
}
