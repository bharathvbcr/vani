import Foundation

/// Where `vani draft` finds Vani's config and state, and which Anthropic
/// workspace it acts in. Kept beside the bank in the App Group folder so the app
/// and the Chrome host read the same values. Empty paths mean Vani's own default
/// (~/Library/Application Support/Vani); an empty workspace means none, which is
/// right for an API key created inside a workspace.
public struct DraftSettings: Codable, Equatable, Sendable {
    public var vaniConfig: String
    public var vaniStateDir: String
    /// Passed to vani as ANTHROPIC_WORKSPACE_ID (Manvi's variable). An identifier
    /// (wrkspc_…), not a secret; vani checks its format and refuses a bad one.
    public var anthropicWorkspace: String

    public init(vaniConfig: String = "", vaniStateDir: String = "", anthropicWorkspace: String = "") {
        self.vaniConfig = vaniConfig
        self.vaniStateDir = vaniStateDir
        self.anthropicWorkspace = anthropicWorkspace
    }

    // Every field may be absent, so a file saved before a field existed still loads.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        vaniConfig = try c.decodeIfPresent(String.self, forKey: .vaniConfig) ?? ""
        vaniStateDir = try c.decodeIfPresent(String.self, forKey: .vaniStateDir) ?? ""
        anthropicWorkspace = try c.decodeIfPresent(String.self, forKey: .anthropicWorkspace) ?? ""
    }

    static func url(in directory: URL) -> URL { directory.appendingPathComponent("draft-settings.json") }

    /// Missing or unreadable settings read as the defaults.
    public static func load(from directory: URL) -> DraftSettings {
        guard let data = try? Data(contentsOf: url(in: directory)),
              let s = try? JSONDecoder().decode(DraftSettings.self, from: data) else { return DraftSettings() }
        return s
    }

    /// Paths must be absolute (or empty for the default). The workspace is
    /// trimmed and bounded here; its format is vani's to judge.
    public func validated() throws -> DraftSettings {
        for (name, p) in [("Vani config", vaniConfig), ("Vani state folder", vaniStateDir)] where !p.isEmpty {
            guard p.hasPrefix("/"), !p.contains("\0"), p.utf8.count <= 1024 else { throw BankError("\(name) must be an absolute path") }
        }
        var out = self
        out.anthropicWorkspace = anthropicWorkspace.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !out.anthropicWorkspace.contains("\0"), out.anthropicWorkspace.utf8.count <= 128 else {
            throw BankError("Anthropic workspace must be a workspace id (wrkspc_…)")
        }
        return out
    }

    public func save(to directory: URL) throws {
        let checked = try validated()
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let data = try JSONEncoder().encode(checked)
        let target = Self.url(in: directory)
        try data.write(to: target, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: target.path)
    }

    /// A runner for the vani binary beside `helpers` (the app's Contents/Helpers).
    public func runner(vaniBinary: URL,
                       key: @escaping KeyLookup = { try Keychain.genericPassword(service: Keychain.anthropicService) }) -> DraftRunner {
        DraftRunner(vaniBinary: vaniBinary,
                    configPath: vaniConfig.isEmpty ? nil : vaniConfig,
                    stateDir: vaniStateDir.isEmpty ? nil : vaniStateDir,
                    workspace: anthropicWorkspace.isEmpty ? nil : anthropicWorkspace,
                    key: key)
    }
}
