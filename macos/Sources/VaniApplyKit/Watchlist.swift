import Foundation

/// One board of Vani's watchlist, as `vani watchlist list --json` prints it
/// (docs/EXTENSION_INGEST.md "Saved searches in the app"). Decoded leniently:
/// unknown fields are ignored, and every field but `ats`, `token` and `company` may be absent.
public struct WatchlistEntry: Decodable, Equatable, Identifiable, Sendable {
    public var ats: String
    public var token: String
    public var company: String
    public var region: String?
    public var host: String?
    public var search: String?
    public var searches: [String]?
    public var url: String?
    public var linkPattern: String?

    public var id: String { "\(ats)/\(token)/\(host ?? "")/\(url ?? "")" }

    enum CodingKeys: String, CodingKey {
        case ats, token, company, region, host, search, searches, url
        case linkPattern = "link_pattern"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        ats = try c.decode(String.self, forKey: .ats)
        token = try c.decode(String.self, forKey: .token)
        company = try c.decodeIfPresent(String.self, forKey: .company) ?? ""
        region = try c.decodeIfPresent(String.self, forKey: .region)
        host = try c.decodeIfPresent(String.self, forKey: .host)
        search = try c.decodeIfPresent(String.self, forKey: .search)
        searches = try c.decodeIfPresent([String].self, forKey: .searches)
        url = try c.decodeIfPresent(String.self, forKey: .url)
        linkPattern = try c.decodeIfPresent(String.self, forKey: .linkPattern)
    }

    public init(ats: String, token: String, company: String, region: String? = nil, host: String? = nil,
                search: String? = nil, searches: [String]? = nil, url: String? = nil, linkPattern: String? = nil) {
        self.ats = ats; self.token = token; self.company = company; self.region = region; self.host = host
        self.search = search; self.searches = searches; self.url = url; self.linkPattern = linkPattern
    }

    /// Vani's ats codes by name. `pg` is a careers page the user saved from the extension.
    static let kinds = ["gh": "Greenhouse", "lv": "Lever", "ab": "Ashby", "wd": "Workday", "gm": "Gem", "pg": "Careers page"]
    static let kinds = ["gh": "Greenhouse", "lv": "Lever", "ab": "Ashby", "wd": "Workday", "sr": "SmartRecruiters", "pg": "Careers page"]
    static let kinds = ["gh": "Greenhouse", "lv": "Lever", "ab": "Ashby", "wd": "Workday", "ib": "IBM", "pg": "Careers page"]
    static let kinds = ["gh": "Greenhouse", "lv": "Lever", "ab": "Ashby", "wd": "Workday", "av": "Avature", "pg": "Careers page"]
    static let kinds = ["gh": "Greenhouse", "lv": "Lever", "ab": "Ashby", "wd": "Workday", "ds": "D. E. Shaw", "at": "Atlassian", "pg": "Careers page"]

    /// "Greenhouse", "Careers page", ...; an ats code this app does not know is shown as it came.
    public var kind: String { Self.kinds[ats] ?? ats }

    public var isPage: Bool { ats == "pg" }

    /// The company, or the token when the entry names none.
    public var title: String { company.isEmpty ? token : company }

    /// The search terms: `searches` when vani sent them, else the single `search`; blanks dropped.
    public var terms: [String] {
        let all = (searches ?? []) + (search.map { [$0] } ?? [])
        var seen = Set<String>()
        return all.map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty && seen.insert($0).inserted }
    }

    /// The page to open for a careers-page board: its `url`, only when it is https.
    public var openURL: URL? {
        guard isPage, let url, url.utf8.count <= JobLinks.maxURLBytes, let u = URL(string: url),
              u.scheme?.lowercased() == "https", u.host?.isEmpty == false else { return nil }
        return u
    }
}

extension JobsClient {
    /// A watchlist of thousands of boards would still be well under this.
    static let maxWatchlistBytes = 4 << 20
    /// The most boards shown; a longer list is refused rather than cut.
    static let maxWatchlistEntries = 5000

    /// `vani watchlist list --json`. Throws vani's own message when it fails, never an empty list.
    public func watchlist() throws -> [WatchlistEntry] {
        let list: WatchlistReply = try VaniProcess.call(vaniBinary, ["watchlist", "list", "--json"], configPath: configPath, stateDir: stateDir,
                                                        timeout: timeout, maxOutputBytes: Self.maxWatchlistBytes,
                                                        tooOld: "this vani is too old to list saved searches; rebuild the app")
        guard list.entries.count <= Self.maxWatchlistEntries else { throw BankError("vani listed more than \(Self.maxWatchlistEntries) boards") }
        return list.entries
    }
}

/// The array vani prints; Go writes an empty list as `null`, which is an empty watchlist, not a failure.
struct WatchlistReply: Decodable {
    var entries: [WatchlistEntry]

    init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        entries = c.decodeNil() ? [] : try c.decode([WatchlistEntry].self)
    }
}
