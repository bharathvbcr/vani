import Foundation

/// The owner's motivation notes (`motivation_notes` in Vani's config), read and
/// saved through `vani config motivation`. Vani owns the cap and the checks and
/// writes the config; the app only shows what it replies.
public struct MotivationNotes: Decodable, Equatable, Sendable {
    public var text: String
    /// Vani's cap, in bytes after whitespace collapse.
    public var maxBytes: Int

    enum CodingKeys: String, CodingKey {
        case text = "motivation_notes"
        case maxBytes = "max_bytes"
    }
}

public struct MotivationNotesClient: Sendable {
    public static let timeout: TimeInterval = 15
    static let maxOutputBytes = 64 << 10

    public let vaniBinary: URL
    public let configPath: String?
    public let stateDir: String?
    let timeout: TimeInterval

    public init(vaniBinary: URL, configPath: String? = nil, stateDir: String? = nil, timeout: TimeInterval = MotivationNotesClient.timeout) {
        self.vaniBinary = vaniBinary
        self.configPath = configPath
        self.stateDir = stateDir
        self.timeout = timeout
    }

    public func load() throws -> MotivationNotes {
        try call(["config", "motivation"], input: nil)
    }

    /// Replace the notes; empty clears them. Vani validates the whole config
    /// before it writes, so a refused note (too long included) leaves the file
    /// as it was.
    public func save(_ text: String) throws -> MotivationNotes {
        try call(["config", "motivation", "--set"], input: Data(text.utf8))
    }

    private func call(_ args: [String], input: Data?) throws -> MotivationNotes {
        try VaniProcess.call(vaniBinary, args, configPath: configPath, stateDir: stateDir, input: input,
                             timeout: timeout, maxOutputBytes: Self.maxOutputBytes,
                             tooOld: "this vani is too old for motivation notes; rebuild the app")
    }
}

extension DraftSettings {
    /// Motivation notes in the same config drafting uses.
    public func motivationNotesClient(vaniBinary: URL) -> MotivationNotesClient {
        MotivationNotesClient(vaniBinary: vaniBinary,
                              configPath: vaniConfig.isEmpty ? nil : vaniConfig,
                              stateDir: vaniStateDir.isEmpty ? nil : vaniStateDir)
    }
}
