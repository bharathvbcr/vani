import Foundation

// `vani jobs gaps --json` (cmd/vani/jobsgaps.go, internal/jobinsight/gaps.go):
// "what should I learn next", over the roles a jobs selection shows. It is
// display-only, like the charts: nothing here filters or reorders the list.
// Vani has already ranked it (skills DevPrism found missing first, by the
// priority of the roles they block, then keyword-only gaps), so the order is
// kept as received. Terms and role labels are board content: shown verbatim.

public struct JobsGaps: Codable, Equatable, Sendable {
    /// One role that asks for a gap; `key` selects it in the Jobs list.
    public struct Example: Codable, Equatable, Sendable, Identifiable {
        public var key: String
        public var company: String
        public var title: String
        public var id: String { key }

        public init(key: String, company: String, title: String) {
            self.key = key
            self.company = company
            self.title = title
        }
    }

    public struct Gap: Codable, Equatable, Sendable, Identifiable {
        public var term: String
        public var category: String
        /// Roles where it is a gap, and of those the ones DevPrism found it missing in.
        public var roles: Int
        public var missing: Int
        /// The summed priority of those roles, and of the ones DevPrism flagged.
        public var weight: Double
        public var missingWeight: Double
        public var companies: Int
        public var examples: [Example]
        public var id: String { term }

        enum CodingKeys: String, CodingKey {
            case term, roles, missing, weight, companies, examples
            case category = "cat"
            case missingWeight = "missing_weight"
        }

        public init(term: String, category: String = "", roles: Int, missing: Int = 0, weight: Double = 0,
                    missingWeight: Double = 0, companies: Int = 1, examples: [Example] = []) {
            self.term = term
            self.category = category
            self.roles = roles
            self.missing = missing
            self.weight = weight
            self.missingWeight = missingWeight
            self.companies = companies
            self.examples = examples
        }

        /// Why it is a gap: DevPrism's finding, else the target summary not naming it.
        public var basisLabel: String {
            if missing > 0 {
                return missing >= roles
                    ? "Found missing by DevPrism in \(JobsGaps.count(roles, "role"))"
                    : "Found missing by DevPrism in \(missing) of \(JobsGaps.count(roles, "role"))"
            }
            return "Not named by your target summary"
        }

        /// "3 roles · 2 companies".
        public var reachLabel: String {
            "\(JobsGaps.count(roles, "role")) · \(JobsGaps.count(companies, "company"))"
        }

        /// The tooltip: what the weights add up.
        public var weightHelp: String {
            var s = "Weight \(Self.one(weight)): the summed priority of the roles that ask for it."
            if missing > 0 { s += " \(Self.one(missingWeight)) of it from roles where DevPrism found it missing." }
            return s
        }

        static func one(_ v: Double) -> String { String(format: "%.1f", v) }
    }

    public var view: String
    public var query: String?
    /// Distinct roles considered, and of those the ones carrying a DevPrism gap analysis.
    public var roles: Int
    public var withMissing: Int
    /// false: the target summary was not read, so keyword gaps were not judged
    /// and only DevPrism-found gaps were counted.
    public var targetKnown: Bool
    /// Gaps found before Vani cut the list to its limit.
    public var totalGaps: Int
    public var gaps: [Gap]
    public var insight: JobsInsight?
    public var torn: Bool?
    public var ledger: String?
    public var records: Int?

    enum CodingKeys: String, CodingKey {
        case view, query, roles, gaps, insight, torn, ledger, records
        case withMissing = "with_missing"
        case targetKnown = "target_known"
        case totalGaps = "total_gaps"
    }

    public init(view: String = "new", roles: Int, withMissing: Int = 0, targetKnown: Bool = true,
                totalGaps: Int? = nil, gaps: [Gap] = [], insight: JobsInsight? = nil) {
        self.view = view
        self.roles = roles
        self.withMissing = withMissing
        self.targetKnown = targetKnown
        self.totalGaps = totalGaps ?? gaps.count
        self.gaps = gaps
        self.insight = insight
    }

    // MARK: - Bounds

    /// Vani returns at most 200 gaps with 10 examples each, label text cut to
    /// 120 characters: well under 1 MB. A reply past this is not Vani's.
    public static let maxReplyBytes = 4 << 20
    public static let maxGaps = 200
    public static let maxExamples = 10

    /// Decodes one reply, refusing one that is too large, malformed, or that
    /// contradicts itself (a count that cannot add up is never displayed).
    public static func decode(_ data: Data) throws -> JobsGaps {
        guard data.count <= maxReplyBytes else {
            throw BankError("vani's gaps reply was larger than \(maxReplyBytes >> 20) MB")
        }
        do {
            return try JobsClient.decoder.decode(JobsGaps.self, from: data)
        } catch let error as DecodingError {
            throw BankError("vani's gaps reply is malformed: \(Self.describe(error))")
        }
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        view = try c.decode(String.self, forKey: .view)
        query = try c.decodeIfPresent(String.self, forKey: .query)
        roles = try c.decode(Int.self, forKey: .roles)
        withMissing = try c.decode(Int.self, forKey: .withMissing)
        targetKnown = try c.decode(Bool.self, forKey: .targetKnown)
        totalGaps = try c.decode(Int.self, forKey: .totalGaps)
        // Go writes an empty list as [], never null; a null or absent list is a different reply.
        gaps = try c.decode([Gap].self, forKey: .gaps)
        insight = try c.decodeIfPresent(JobsInsight.self, forKey: .insight)
        torn = try c.decodeIfPresent(Bool.self, forKey: .torn)
        ledger = try c.decodeIfPresent(String.self, forKey: .ledger)
        records = try c.decodeIfPresent(Int.self, forKey: .records)

        func bad(_ why: String) -> DecodingError {
            .dataCorrupted(.init(codingPath: decoder.codingPath, debugDescription: why))
        }
        guard roles >= 0, withMissing >= 0, withMissing <= roles else { throw bad("role counts do not add up") }
        guard gaps.count <= Self.maxGaps else { throw bad("more than \(Self.maxGaps) gaps") }
        guard totalGaps >= gaps.count else { throw bad("fewer gaps found than listed") }
        guard gaps.isEmpty || roles > 0 else { throw bad("gaps without roles") }
        var seen = Set<String>()
        for g in gaps {
            guard !g.term.isEmpty, seen.insert(g.term).inserted else { throw bad("empty or repeated term") }
            guard g.roles >= 1, g.roles <= roles, g.missing >= 0, g.missing <= g.roles,
                  g.companies >= 0, g.companies <= g.roles else { throw bad("counts for “\(g.term.prefix(40))” do not add up") }
            guard g.weight.isFinite, g.weight >= 0, g.missingWeight.isFinite, g.missingWeight >= 0 else {
                throw bad("weight of “\(g.term.prefix(40))” is not a number")
            }
            guard g.examples.count <= Self.maxExamples else { throw bad("too many examples for “\(g.term.prefix(40))”") }
        }
    }

    private static func describe(_ error: DecodingError) -> String {
        switch error {
        case let .dataCorrupted(ctx), let .typeMismatch(_, ctx), let .valueNotFound(_, ctx):
            return ctx.debugDescription
        case let .keyNotFound(key, _):
            return "missing “\(key.stringValue)”"
        @unknown default:
            return "unreadable"
        }
    }

    // MARK: - What to say

    /// The one thing the panel leads with. An unjudged set never reads as "no gaps".
    public enum Status: Equatable, Sendable {
        /// The selection holds no roles.
        case noRoles
        /// Roles, but none has a DevPrism analysis and the target summary was not
        /// read: nothing could be judged.
        case unjudged
        /// Judged, nothing found. `keywordsJudged` false: DevPrism gaps only.
        case none(keywordsJudged: Bool)
        case listed
    }

    public var status: Status {
        if !gaps.isEmpty { return .listed }
        if roles == 0 { return .noRoles }
        if !targetKnown && withMissing == 0 { return .unjudged }
        return .none(keywordsJudged: targetKnown)
    }

    public var statusText: String? {
        switch status {
        case .listed:
            return nil
        case .noRoles:
            return "No roles in this list, so there is nothing to judge."
        case .unjudged:
            return "No gaps could be judged: no role has a DevPrism gap analysis and your target summary was not read."
        case .none(keywordsJudged: true):
            return "No gaps found."
        case .none(keywordsJudged: false):
            return "No DevPrism gaps found. Keyword gaps were not judged."
        }
    }

    /// Shown whenever the target summary was not read and roles were counted.
    public var targetNote: String? {
        guard !targetKnown, roles > 0 else { return nil }
        return "Only DevPrism gaps were counted: your target summary was not read."
    }

    /// "Showing 25 of 61", when Vani cut the list.
    public var shownNote: String? {
        totalGaps > gaps.count ? "Showing \(gaps.count) of \(totalGaps)" : nil
    }

    /// "12 roles, 4 with a DevPrism gap analysis".
    public var summaryLine: String {
        "\(Self.count(roles, "role")), \(withMissing) with a DevPrism gap analysis"
    }

    static func count(_ n: Int, _ noun: String) -> String {
        if n == 1 { return "1 \(noun)" }
        if noun.hasSuffix("y"), let before = noun.dropLast().last, !"aeiou".contains(before) {
            return "\(n) \(noun.dropLast())ies"
        }
        return "\(n) \(noun)s"
    }
}

extension JobsClient {
    static func gapsArguments(_ view: JobView, query: String, filter: JobFilter) -> [String] {
        ["jobs", "gaps", "--json", "--view", view.rawValue] + selection(query, filter)
    }

    /// The skills the roles a list with the same arguments shows ask for that
    /// you lack. Display-only.
    public func gaps(_ view: JobView, query: String = "", filter: JobFilter = JobFilter()) throws -> JobsGaps {
        try VaniProcess.call(vaniBinary, Self.gapsArguments(view, query: query, filter: filter),
                             configPath: configPath, stateDir: stateDir, timeout: timeout,
                             maxOutputBytes: JobsGaps.maxReplyBytes, decoder: Self.decoder,
                             tooOld: "this vani is too old for skills to build; rebuild the app")
    }
}
