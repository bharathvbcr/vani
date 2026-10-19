import Foundation

// Registers the native messaging host with Chrome (and Chromium) for this
// user: a small JSON manifest naming the host binary and the one extension
// allowed to start it. Only an explicit "Connect" in the app writes it;
// "Disconnect" removes exactly that file.

public struct ChromeConnector {
    public static let hostName = "dev.vbcr.vani_apply"
    /// The id Chrome derives from the public key in extension/static/chrome-key.json.
    public static let defaultExtensionID = "clhaobdmkmapebgnmpcfmakbfakmgfip"

    public enum Browser: String, CaseIterable, Sendable {
        case chrome = "Google Chrome"
        case chromium = "Chromium"

        /// Relative to ~/Library/Application Support.
        var hostsDirectory: String {
            switch self {
            case .chrome: "Google/Chrome/NativeMessagingHosts"
            case .chromium: "Chromium/NativeMessagingHosts"
            }
        }
        /// The browser's profile root; its presence means the browser has been run.
        var profileDirectory: String {
            switch self {
            case .chrome: "Google/Chrome"
            case .chromium: "Chromium"
            }
        }
    }

    public enum Status: Equatable, Sendable {
        case browserNotFound
        case notConnected
        case connected
        /// A manifest exists but names another binary or extension (the app moved, or the id changed).
        case stale(reason: String)
    }

    public let applicationSupport: URL
    public let hostBinary: URL

    public init(applicationSupport: URL, hostBinary: URL) {
        self.applicationSupport = applicationSupport
        self.hostBinary = hostBinary
    }

    public static func forCurrentUser(hostBinary: URL) -> ChromeConnector {
        let home = FileManager.default.homeDirectoryForCurrentUser
        return ChromeConnector(applicationSupport: home.appendingPathComponent("Library/Application Support"), hostBinary: hostBinary)
    }

    public func manifestURL(_ browser: Browser) -> URL {
        applicationSupport.appendingPathComponent(browser.hostsDirectory).appendingPathComponent("\(Self.hostName).json")
    }

    public static func isValidExtensionID(_ id: String) -> Bool {
        id.count == 32 && id.unicodeScalars.allSatisfy { $0.value >= 97 && $0.value <= 112 } // a...p
    }

    public func manifest(extensionID: String) throws -> Data {
        guard Self.isValidExtensionID(extensionID) else { throw BankError("\(extensionID) is not a Chrome extension id") }
        let obj: [String: Any] = [
            "name": Self.hostName,
            "description": "Vani Apply answer bank",
            "path": hostBinary.path,
            "type": "stdio",
            "allowed_origins": ["chrome-extension://\(extensionID)/"],
        ]
        return try JSONSerialization.data(withJSONObject: obj, options: [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes])
    }

    public func status(_ browser: Browser, extensionID: String) -> Status {
        let fm = FileManager.default
        let url = manifestURL(browser)
        guard let data = fm.contents(atPath: url.path) else {
            let profile = applicationSupport.appendingPathComponent(browser.profileDirectory)
            return fm.fileExists(atPath: profile.path) ? .notConnected : .browserNotFound
        }
        guard let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return .stale(reason: "the host manifest is unreadable")
        }
        if obj["path"] as? String != hostBinary.path { return .stale(reason: "it points at another copy of the app") }
        if obj["allowed_origins"] as? [String] != ["chrome-extension://\(extensionID)/"] {
            return .stale(reason: "it allows a different extension id")
        }
        if !fm.isExecutableFile(atPath: hostBinary.path) { return .stale(reason: "the host binary is missing") }
        return .connected
    }

    public func connect(_ browser: Browser, extensionID: String) throws {
        let data = try manifest(extensionID: extensionID)
        let url = manifestURL(browser)
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        try data.write(to: url, options: .atomic)
    }

    /// Remove our manifest only; never anything else in that directory.
    public func disconnect(_ browser: Browser) throws {
        let url = manifestURL(browser)
        if FileManager.default.fileExists(atPath: url.path) { try FileManager.default.removeItem(at: url) }
    }
}
