import Foundation

/// The jobs-row contract, owned by Go (internal/jobview/row.go) and carried as
/// `row` on every job `vani jobs … --json` lists and on a `jobs find` match.
/// Relevance, priority, the fit vocabulary and every fit/status/age display
/// string are computed there; this type only decodes and shows them.
///
/// A row of a schema version this build does not know fails to decode, loudly:
/// a newer vani's row is refused, never half-rendered. The golden replay is
/// internal/jobview/testdata/rows.golden.json (JobRowTests).
public struct JobRow: Codable, Equatable, Sendable {
    /// The schema version this build reads (jobview.Version).
    public static let supportedVersion = 1

    public struct DecodeError: Error, Equatable, CustomStringConvertible, LocalizedError {
        public var version: Int
        public var description: String {
            "jobs row schema version \(version) is not supported (this app reads \(JobRow.supportedVersion)); update Vani Apply or vani"
        }
        public var errorDescription: String? { description }
    }

    public var v: Int
    public var key: String
    public var title: String
    public var company: String
    public var location: String?
    public var status: String
    public var statusLabel: String
    /// 0-100, age-free.
    public var relevance: Double
    public var relevanceBasis: String?
    /// Relevance discounted by age.
    public var priority: Double
    /// triage's role_fit option and its display string; nil with no reading.
    public var fit: String?
    public var fitLabel: String?
    public var reasons: [Job.Reason]?
    /// Other postings this one is linked to as a duplicate, candidate or sibling.
    public var similar: Int
    public var ageDays: Int?
    public var freshSrc: String?
    public var ageLabel: String?

    enum CodingKeys: String, CodingKey {
        case v, key, title, company, location, status, relevance, priority, fit, reasons, similar
        case statusLabel = "status_label"
        case relevanceBasis = "relevance_basis"
        case fitLabel = "fit_label"
        case ageDays = "age_days"
        case freshSrc = "fresh_src"
        case ageLabel = "age_label"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        // The version is read first and checked before any other field, so a
        // changed shape is reported as a version problem, not a missing key.
        v = try c.decode(Int.self, forKey: .v)
        guard v == Self.supportedVersion else { throw DecodeError(version: v) }
        key = try c.decode(String.self, forKey: .key)
        title = try c.decode(String.self, forKey: .title)
        company = try c.decode(String.self, forKey: .company)
        location = try c.decodeIfPresent(String.self, forKey: .location)
        status = try c.decode(String.self, forKey: .status)
        statusLabel = try c.decode(String.self, forKey: .statusLabel)
        relevance = try c.decode(Double.self, forKey: .relevance)
        relevanceBasis = try c.decodeIfPresent(String.self, forKey: .relevanceBasis)
        priority = try c.decode(Double.self, forKey: .priority)
        fit = try c.decodeIfPresent(String.self, forKey: .fit)
        fitLabel = try c.decodeIfPresent(String.self, forKey: .fitLabel)
        reasons = try c.decodeIfPresent([Job.Reason].self, forKey: .reasons)
        similar = try c.decode(Int.self, forKey: .similar)
        ageDays = try c.decodeIfPresent(Int.self, forKey: .ageDays)
        freshSrc = try c.decodeIfPresent(String.self, forKey: .freshSrc)
        ageLabel = try c.decodeIfPresent(String.self, forKey: .ageLabel)
    }

    /// The row as a JSON object, for the native host to hand the extension
    /// unchanged (the extension reads the same contract).
    public var jsonObject: [String: Any]? {
        guard let data = try? JSONEncoder().encode(self) else { return nil }
        return (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
    }
}
