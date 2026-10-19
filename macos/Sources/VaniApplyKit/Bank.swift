import Foundation

// The answer bank, mirroring extension/src/types.ts. The browser extension
// owns capture and matching; this side stores, validates and edits.

public enum FieldKind: String, Codable, CaseIterable, Sendable {
    case text, email, tel, url, number, date, textarea, select, radio, checkbox, file, password, hidden, other

    /// Choice kinds answer with option labels; the rest answer with text.
    public var isChoice: Bool { self == .select || self == .radio || self == .checkbox }
}

public enum Sensitivity: String, Codable, Sendable {
    /// Never stored, never filled.
    case secret
    /// Voluntary self-identification; stored and filled only when opted in.
    case selfID = "self_id"
    case standard
}

public struct Answer: Codable, Equatable, Identifiable, Sendable {
    public var key: String
    public var labels: [String]
    public var kind: FieldKind
    public var value: [String]
    public var sensitivity: Sensitivity
    public var signatures: [String]
    public var uses: Int
    public var fills: Int
    public var updatedAt: String
    public var review: Bool

    public var id: String { key }

    public init(key: String, labels: [String], kind: FieldKind, value: [String], sensitivity: Sensitivity,
                signatures: [String], uses: Int, fills: Int, updatedAt: String, review: Bool) {
        self.key = key
        self.labels = labels
        self.kind = kind
        self.value = value
        self.sensitivity = sensitivity
        self.signatures = signatures
        self.uses = uses
        self.fills = fills
        self.updatedAt = updatedAt
        self.review = review
    }
}

public struct Settings: Codable, Equatable, Sendable {
    public var captureSelfId: Bool
    public var disabledHosts: [String]

    public init(captureSelfId: Bool = false, disabledHosts: [String] = []) {
        self.captureSelfId = captureSelfId
        self.disabledHosts = disabledHosts
    }
}

public struct Bank: Codable, Equatable, Sendable {
    public var version: Int
    public var answers: [String: Answer]
    public var settings: Settings

    public init(answers: [String: Answer] = [:], settings: Settings = Settings()) {
        self.version = 1
        self.answers = answers
        self.settings = settings
    }

    public static let empty = Bank()
}

/// Bounds shared with extension/src/bank.ts.
public enum Limits {
    public static let maxAnswers = 2000
    public static let maxValueChars = 10_000
    public static let maxLabels = 20
    public static let maxSignatures = 50
    public static let maxOptions = 200
    public static let maxDisabledHosts = 500
    public static let maxKeyChars = 200
    public static let reviewChars = 280
    /// Under Chrome's 1 MB host-to-extension message limit, with room for the reply envelope.
    public static let maxBankBytes = 900_000
}

extension Bank {
    /// Compact JSON, as the store writes it and as the size cap measures it.
    public func encoded() throws -> Data {
        let enc = JSONEncoder()
        enc.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        return try enc.encode(self)
    }

    /// The bank as Foundation JSON objects, for replies that are not raw bytes (Safari).
    public func jsonObject() throws -> Any {
        try JSONSerialization.jsonObject(with: encoded())
    }
}

/// ISO-8601 time in the same shape as JavaScript's `Date.toISOString()`.
public func isoNow(_ date: Date = Date()) -> String {
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    f.timeZone = TimeZone(identifier: "UTC")
    return f.string(from: date)
}
