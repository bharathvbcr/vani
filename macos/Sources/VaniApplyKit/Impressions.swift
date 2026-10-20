import Foundation

// A screen reports the rows it shows to `vani impressions log`
// (cmd/vani/impressions.go), so an unmarked posting can count as seen-and-passed
// when the ranking is measured. The request holds keys and 1-based positions
// and nothing else: never a title, company or description. Go keeps a key once
// per slate, so reporting the same slate again on a refresh costs nothing.

/// Where a reported row was shown; the values `--surface` takes.
public enum ImpressionSurface: String, Sendable {
    case jobs, audit
}

/// One row on screen: its key and 1-based position.
public struct ImpressionRow: Encodable, Equatable, Sendable {
    public var key: String
    public var position: Int
}

extension JobsClient {
    /// How many of the top rows a refresh reports: a screenful, not the list.
    public static let impressionRows = 20
    /// vani's own bound on one request; a larger list is cut here, not sent.
    static let maxImpressionRows = 200

    /// `vani impressions log --json`'s reply.
    struct ImpressionsLogged: Decodable, Equatable {
        var logged: Int
    }

    /// The slate of a Jobs refresh: one per view per local day, so refreshing
    /// the same view again the same day reports nothing new and a new day is a
    /// new slate.
    public static func jobsSlate(view: JobView, day: Date, calendar: Calendar = .current) -> String {
        let c = calendar.dateComponents([.year, .month, .day], from: day)
        return slateToken(String(format: "%@-%04d%02d%02d", view.rawValue, c.year ?? 0, c.month ?? 0, c.day ?? 0))
    }

    /// `s` as the token Go's feedback log takes: lower case, anything outside
    /// letters, digits and `_:-.` becomes `-`, at most 64 characters.
    public static func slateToken(_ s: String) -> String {
        String(s.lowercased().unicodeScalars.prefix(64).map { u -> Character in
            switch u {
            case "a"..."z", "0"..."9", "_", ":", "-", ".": Character(u)
            default: "-"
            }
        })
    }

    static func impressionsArguments(surface: ImpressionSurface, slate: String, section: String) -> [String] {
        var args = ["impressions", "log", "--json", "--surface", surface.rawValue, "--slate", slateToken(slate)]
        let s = slateToken(section)
        if !s.isEmpty { args += ["--section", s] }
        return args
    }

    /// The request body: `keys` in display order from `firstPosition`, at most `maxImpressionRows`.
    static func impressionsRequest(keys: [String], firstPosition: Int = 1) throws -> Data {
        struct Request: Encodable { var rows: [ImpressionRow] }
        let rows = keys.prefix(maxImpressionRows).enumerated().map { ImpressionRow(key: $1, position: $0 + max(firstPosition, 1)) }
        return try JSONEncoder().encode(Request(rows: rows))
    }

    /// Reports `keys` (in display order, first is position 1) as shown on
    /// `surface` in `slate` (the first at `firstPosition`), and returns how many rows vani had not already
    /// logged for that slate. Nothing is sent for an empty list.
    @discardableResult
    public func logImpressions(surface: ImpressionSurface, slate: String, section: String = "", keys: [String], firstPosition: Int = 1) throws -> Int {
        guard !keys.isEmpty else { return 0 }
        let reply: ImpressionsLogged = try VaniProcess.call(
            vaniBinary, Self.impressionsArguments(surface: surface, slate: slate, section: section),
            configPath: configPath, stateDir: stateDir, input: try Self.impressionsRequest(keys: keys, firstPosition: firstPosition),
            timeout: timeout, maxOutputBytes: 1 << 16, decoder: JSONDecoder(),
            tooOld: "this vani is too old to record impressions; rebuild the app")
        return reply.logged
    }
}
