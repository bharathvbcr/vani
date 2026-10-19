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
    let timeout: TimeInterval

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
                timeout: TimeInterval = DraftRunner.timeout) {
        self.vaniBinary = vaniBinary
        self.configPath = configPath
        self.stateDir = stateDir
        self.keys = keys
        self.account = account
        self.timeout = timeout
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

    enum Invocation {
        case output(VaniProcess.Outcome)
        case failure(DraftReply)
    }

    enum Command {
        case run(URL, [String])
        case refused(DraftReply)
    }

    /// The program and argv that start `vani <subcommand>`: through
    /// anthropic-account when Vani's config names the anthropic provider, else
    /// vani itself. Both paths are absolute, because the child's PATH is only
    /// the system folders.
    func command(_ subcommand: String) -> Command {
        guard case let .anthropicAccount(launcher) = keys,
              VaniConfig.llmProvider(configPath: configPath) == "anthropic" else {
            return .run(vaniBinary, [subcommand])
        }
        guard let launcher, FileManager.default.isExecutableFile(atPath: launcher.path) else {
            let looked = launcher.map { [$0] } ?? AnthropicAccount.candidates
            return .refused(.failure("account", "anthropic-account is not installed (looked at \(looked.map(\.path).joined(separator: ", "))); Vani's config names the anthropic provider, which needs it"))
        }
        if let account, !account.isEmpty, !AnthropicAccount.isValidName(account) {
            return .refused(.failure("account", "the saved Anthropic account name is not valid; choose one in Settings"))
        }
        return .run(launcher, AnthropicAccount.execArguments(account: account, command: vaniBinary, arguments: [subcommand]))
    }

    /// Start `vani <subcommand>` with the minimal environment. No key is added
    /// here: on the anthropic path anthropic-account adds it after this
    /// environment, in the process it replaces itself with.
    func invoke(_ subcommand: String, input: Data) -> Invocation {
        let program: URL, arguments: [String]
        switch command(subcommand) {
        case let .refused(f): return .failure(f)
        case let .run(p, a): (program, arguments) = (p, a)
        }
        let env = VaniProcess.environment(configPath: configPath, stateDir: stateDir)
        let out = VaniProcess.run(program, arguments: arguments, environment: env, input: input,
                                  timeout: timeout, maxOutputBytes: Self.maxOutputBytes)
        if let launchError = out.launchError { return .failure(.failure("config", launchError)) }
        if out.timedOut { return .failure(.failure("timeout", "vani \(subcommand) did not finish within \(Int(timeout)) s")) }
        return .output(out)
    }
}
