import Foundation

/// Where `vani draft` finds Vani's config and state, and which Anthropic
/// account (Console organisation) anthropic-account launches it with. Kept
/// beside the bank in the App Group folder so the app and the Chrome host read
/// the same values. Empty paths mean Vani's own default
/// (~/Library/Application Support/Vani); an empty account lets anthropic-account
/// pick (the first line of ~/.anthropic-accounts).
public struct DraftSettings: Codable, Equatable, Sendable {
    public var vaniConfig: String
    public var vaniStateDir: String
    /// A name from `anthropic-account list`, passed as `--account`. A name, not
    /// a secret; the key and any workspace id stay with anthropic-account.
    public var anthropicAccount: String

    public init(vaniConfig: String = "", vaniStateDir: String = "", anthropicAccount: String = "") {
        self.vaniConfig = vaniConfig
        self.vaniStateDir = vaniStateDir
        self.anthropicAccount = anthropicAccount
    }

    // Every field may be absent, so a file saved before a field existed still
    // loads. A file from before accounts carries "anthropicWorkspace"; it is
    // ignored, because a workspace now belongs to its account's registry line.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        vaniConfig = try c.decodeIfPresent(String.self, forKey: .vaniConfig) ?? ""
        vaniStateDir = try c.decodeIfPresent(String.self, forKey: .vaniStateDir) ?? ""
        anthropicAccount = try c.decodeIfPresent(String.self, forKey: .anthropicAccount) ?? ""
    }

    static func url(in directory: URL) -> URL { directory.appendingPathComponent("draft-settings.json") }

    /// Missing or unreadable settings read as the defaults.
    public static func load(from directory: URL) -> DraftSettings {
        guard let data = try? Data(contentsOf: url(in: directory)),
              let s = try? JSONDecoder().decode(DraftSettings.self, from: data) else { return DraftSettings() }
        return s
    }

    /// Paths must be absolute (or empty for the default). The account is
    /// trimmed and must be a name anthropic-account accepts (or empty).
    public func validated() throws -> DraftSettings {
        for (name, p) in [("Vani config", vaniConfig), ("Vani state folder", vaniStateDir)] where !p.isEmpty {
            guard p.hasPrefix("/"), !p.contains("\0"), p.utf8.count <= 1024 else { throw BankError("\(name) must be an absolute path") }
        }
        var out = self
        out.anthropicAccount = anthropicAccount.trimmingCharacters(in: .whitespacesAndNewlines)
        guard out.anthropicAccount.isEmpty || AnthropicAccount.isValidName(out.anthropicAccount) else {
            throw BankError("Anthropic account must be a name from anthropic-account list (letters, digits, . _ -)")
        }
        return out
    }

    public func save(to directory: URL) throws {
        let checked = try validated()
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        _ = chmod(directory.path, 0o700)
        let enc = JSONEncoder()
        enc.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        let data = try enc.encode(checked)
        let target = Self.url(in: directory)
        let tmp = directory.appendingPathComponent(".draft-settings.\(UUID().uuidString).tmp")
        let fd = open(tmp.path, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0o600)
        if fd < 0 {
            throw BankError("cannot write draft settings: \(String(cString: strerror(errno)))")
        }
        _ = fchmod(fd, 0o600)
        var writeErr: Int32 = 0
        let written = data.withUnsafeBytes { buf -> Int in
            guard let ptr = buf.baseAddress else { return 0 }
            var total = 0
            while total < data.count {
                let n = write(fd, ptr + total, data.count - total)
                if n < 0 {
                    if errno == EINTR { continue }
                    writeErr = errno
                    return -1
                }
                total += n
            }
            return total
        }
        if written != data.count {
            close(fd)
            unlink(tmp.path)
            let errStr = writeErr != 0 ? String(cString: strerror(writeErr)) : "short write"
            throw BankError("cannot write draft settings: \(errStr)")
        }
        if fcntl(fd, F_FULLFSYNC) != 0 && fsync(fd) != 0 {
            let err = errno
            close(fd)
            unlink(tmp.path)
            throw BankError("cannot sync draft settings temp file: \(String(cString: strerror(err)))")
        }
        if close(fd) != 0 {
            let err = errno
            unlink(tmp.path)
            throw BankError("cannot close draft settings temp file: \(String(cString: strerror(err)))")
        }
        if rename(tmp.path, target.path) != 0 {
            let err = errno
            unlink(tmp.path)
            throw BankError("cannot replace draft settings: \(String(cString: strerror(err)))")
        }
        let dirFd = open(directory.path, O_RDONLY | O_CLOEXEC)
        if dirFd >= 0 {
            if fcntl(dirFd, F_FULLFSYNC) != 0 { _ = fsync(dirFd) }
            close(dirFd)
        }
    }

    /// `vani jobs` with the same config and state folder as drafting, so the app's Jobs
    /// tab, the Chrome host and the agent all read and write one set of marks. It reads no key.
    public func jobsClient(vaniBinary: URL, timeout: TimeInterval = JobsClient.timeout) -> JobsClient {
        JobsClient(vaniBinary: vaniBinary,
                   configPath: vaniConfig.isEmpty ? nil : vaniConfig,
                   stateDir: vaniStateDir.isEmpty ? nil : vaniStateDir,
                   timeout: timeout)
    }

    /// A runner for `vaniBinary` with these paths and this account. By default
    /// it launches through the installed anthropic-account when the config names
    /// the anthropic provider.
    public func runner(vaniBinary: URL,
                       keys: DraftRunner.KeySource = .anthropicAccount(AnthropicAccount.locate())) -> DraftRunner {
        DraftRunner(vaniBinary: vaniBinary,
                    configPath: vaniConfig.isEmpty ? nil : vaniConfig,
                    stateDir: vaniStateDir.isEmpty ? nil : vaniStateDir,
                    keys: keys,
                    account: anthropicAccount.isEmpty ? nil : anthropicAccount)
    }
}
