import Foundation

// The Jobs tab is a client of `vani jobs … --json` (cmd/vani/jobscmd.go): the
// Go side owns the ledger and the marks store; this side only lists, shows and
// asks for marks. Posting text is untrusted board content: it is shown as
// plain text, never as Markdown, and only http(s) links are opened.

public enum JobView: String, CaseIterable, Identifiable, Sendable {
    case new, saved, applied, hidden, all
    public var id: String { rawValue }
    public var title: String {
        switch self {
        case .new: "New"
        case .saved: "Saved"
        case .applied: "Applied"
        case .hidden: "Hidden"
        case .all: "All"
        }
    }
}

public enum JobMark: String, Codable, Sendable, CaseIterable {
    case saved, applied, hidden
}

public struct Job: Codable, Identifiable, Equatable, Sendable {
    public var key: String
    public var company: String
    public var title: String
    public var location: String?
    public var remote: Bool?
    public var department: String?
    public var url: String?
    public var applyURL: String?
    public var status: String
    public var closed: Bool?
    public var rank: Double?
    public var coverage: Double?
    public var explanation: String?
    public var missing: String?
    public var unsure: String?
    public var lca: String?
    public var employer: String?
    public var triage: [String: String]?
    public var firstSeen: Date?
    public var postedAt: Date?
    public var mark: JobMark?
    public var markedAt: Date?
    public var note: String?
    public var noteAt: Date?
    public var description: String?

    // Display-only insight from `vani jobs` (cmd/vani/jobsinsight.go).
    /// The date the age is measured from: the board's posted date, or when
    /// Vani first saw the posting (`freshSource == .seen`, an upper bound).
    public var freshAt: Date?
    public var freshSource: FreshSource?
    public var ageDays: Int?
    /// 0-100: rank (or triage fit) plus overlap with the target summary.
    public var relevance: Double?
    public var relevanceBasis: String?
    /// Relevance discounted by age, at most halved.
    public var priority: Double?
    public var keywords: [Keyword]?
    /// The picked note split by Vani into its reasons.
    public var reasonList: [Reason]?

    // What the board's location says, read by Vani (internal/location): the
    // places it names, and whether (and where) it lists the role as remote.
    // Not triage's work-mode reading.
    public var places: [Place]?
    public var remoteListed: Bool?
    public var remoteScope: String?

    /// The pay the posting's text states, read by Vani (jobinsight.ParsePay):
    /// at most one range per currency.
    public var pay: [Pay]?

    public var id: String { key }

    enum CodingKeys: String, CodingKey {
        case key, company, title, location, remote, department, url, status, closed, rank, coverage,
             explanation, missing, unsure, lca, employer, triage, mark, note, description,
             relevance, priority, keywords
        case applyURL = "apply_url"
        case firstSeen = "first_seen"
        case postedAt = "posted_at"
        case markedAt = "marked_at"
        case noteAt = "note_at"
        case freshAt = "fresh_at"
        case freshSource = "fresh_src"
        case ageDays = "age_days"
        case relevanceBasis = "relevance_basis"
        case reasonList = "reasons"
        case places
        case remoteListed = "remote_listed"
        case remoteScope = "remote_scope"
        case pay
    }

    public enum FreshSource: String, Codable, Sendable { case posted, seen }

    /// A pay range as stated: Min and Max per Period, and over a year.
    public struct Pay: Codable, Equatable, Hashable, Sendable {
        public var currency: String
        public var period: String
        public var min: Double
        public var max: Double
        public var annualMin: Double
        public var annualMax: Double
        /// The span of posting text it was read from.
        public var text: String?

        public init(currency: String, period: String, min: Double, max: Double, annualMin: Double, annualMax: Double, text: String? = nil) {
            self.currency = currency
            self.period = period
            self.min = min
            self.max = max
            self.annualMin = annualMin
            self.annualMax = annualMax
            self.text = text
        }

        enum CodingKeys: String, CodingKey {
            case currency, period, min, max, text
            case annualMin = "annual_min"
            case annualMax = "annual_max"
        }

        /// "USD 120k–160k/yr", "USD 70–90/hr": the same short form `vani jobs list` prints.
        public var label: String {
            let per = ["hour": "/hr", "day": "/day", "week": "/wk", "month": "/mo", "year": "/yr"][period] ?? ""
            let amount = min == max ? Self.short(min) : Self.short(min) + "–" + Self.short(max)
            return currency + " " + amount + per
        }

        /// 150000 → "150k", 2500000 → "2.5M", 52.5 → "52.5".
        public static func short(_ v: Double) -> String {
            func trim(_ x: Double) -> String {
                x == x.rounded() ? String(Int(x)) : String(format: "%g", x)
            }
            switch v {
            case 1_000_000...: return trim((v / 100_000).rounded() / 10) + "M"
            case 1_000...: return trim((v / 100).rounded() / 10) + "k"
            default: return trim((v * 100).rounded() / 100)
            }
        }
    }

    /// The stated pay in `currency`, else the first one stated.
    public func payLabel(preferring currency: String) -> String? {
        guard let pay, !pay.isEmpty else { return nil }
        return (pay.first { $0.currency == currency } ?? pay[0]).label
    }

    /// The fixture's seniority options, most junior first.
    public static let seniorityOptions = ["intern_or_new_grad", "mid", "senior", "staff_plus", "manager"]
    /// The currencies Vani reads pay in (jobinsight.PayCurrencies).
    public static let payCurrencies = ["USD", "CAD", "EUR", "GBP", "AUD", "CHF", "SGD", "NZD", "INR", "JPY", "SEK", "NOK", "DKK", "PLN"]

    /// One place a posting names: an area and a US state code, or an area and
    /// what followed it (a country or province) as the board wrote it.
    public struct Place: Codable, Equatable, Hashable, Sendable {
        public var area: String?
        public var state: String?
        public var region: String?

        public init(area: String? = nil, state: String? = nil, region: String? = nil) {
            self.area = area
            self.state = state
            self.region = region
        }

        /// "El Paso, TX", "London, United Kingdom", "TX".
        public var label: String {
            [area, state ?? region].compactMap { $0?.isEmpty == false ? $0 : nil }.joined(separator: ", ")
        }
    }

    /// "Remote (US)" when the board lists the role as remote, else nil.
    public var remoteLabel: String? {
        guard remoteListed == true || remote == true else { return nil }
        if let s = remoteScope, !s.isEmpty { return "Remote (\(s))" }
        return "Remote"
    }

    /// The location as Vani read it ("El Paso, TX; Las Cruces, NM · Remote
    /// (US)"), or the board's own text when Vani recognised nothing in it.
    public var whereLabel: String? {
        var s = (places ?? []).map(\.label).filter { !$0.isEmpty }.joined(separator: "; ")
        let r = remoteLabel
        if s.isEmpty, let loc = location?.trimmingCharacters(in: .whitespaces), !loc.isEmpty {
            // Nothing recognised (or an older vani that sends no places): the
            // board's own text, unless it only says remote, which r says better.
            if r == nil || !loc.localizedCaseInsensitiveContains("remote") { s = loc }
        }
        if let r { s = s.isEmpty ? r : s + " · " + r }
        return s.isEmpty ? nil : s
    }

    /// A skill or technology term the posting mentions. `target`: your target
    /// summary names it; `gap`: DevPrism listed it missing or weak.
    public struct Keyword: Codable, Equatable, Hashable, Sendable {
        public var term: String
        public var cat: String
        public var n: Int
        public var target: Bool?
        public var gap: Bool?

        public init(term: String, cat: String, n: Int, target: Bool? = nil, gap: Bool? = nil) {
            self.term = term
            self.cat = cat
            self.n = n
            self.target = target
            self.gap = gap
        }
    }

    public init(key: String, company: String, title: String, status: String = "digested") {
        self.key = key
        self.company = company
        self.title = title
        self.status = status
    }

    /// The posting's own page, when it is an http(s) URL.
    public var postingLink: URL? { Self.httpURL(url) }
    /// The application form, when the board gives one.
    public var applyLink: URL? { Self.httpURL(applyURL) }

    /// An absolute http(s) URL with a host, or nil. Vani already filters links;
    /// this is the second check, at the point a link becomes clickable.
    public static func httpURL(_ raw: String?) -> URL? {
        guard let raw, let u = URL(string: raw), let scheme = u.scheme?.lowercased(),
              scheme == "http" || scheme == "https", let host = u.host, !host.isEmpty else { return nil }
        return u
    }

    /// Triage's role-fit label in words, when triage gave one.
    public var fitLabel: String? { Self.words[triage?["role_fit"] ?? ""] }

    /// Triage's other readings in words (seniority, work mode, sponsorship),
    /// leaving out "not stated".
    public var triageLabels: [String] {
        ["seniority", "work_mode", "sponsorship"].compactMap { Self.words[triage?[$0] ?? ""] }
    }

    /// A jdtriage option value in words ("strong_fit" → "Strong fit"); "none"
    /// is no reading.
    public static func label(for option: String) -> String {
        if option == "none" { return "No reading" }
        return words[option] ?? option.replacingOccurrences(of: "_", with: " ")
    }

    /// The fixture's role_fit options, best first (cmd/vani roleFitOrder).
    public static let roleFitOrder = ["strong_fit", "partial_fit", "adjacent", "mismatch_seniority", "mismatch_domain"]
    public static let workModeOptions = ["remote", "hybrid", "onsite", "not_stated"]
    public static let sponsorshipOptions = ["sponsors_visa", "no_sponsorship", "citizenship_or_clearance_required", "not_stated"]

    // The jdtriage fixture's option values (internal/decide/jdtriage/fixture.json).
    static let words: [String: String] = [
        "strong_fit": "Strong fit", "partial_fit": "Partial fit", "adjacent": "Adjacent",
        "mismatch_seniority": "Seniority mismatch", "mismatch_domain": "Different field",
        "intern_or_new_grad": "Intern / new grad", "mid": "Mid-level", "senior": "Senior",
        "staff_plus": "Staff+", "manager": "Manager",
        "remote": "Remote", "hybrid": "Hybrid", "onsite": "On-site",
        "sponsors_visa": "Sponsors visas", "no_sponsorship": "No sponsorship",
        "citizenship_or_clearance_required": "Citizenship or clearance",
    ]

    /// One picked reason. Vani splits the note (cmd/vani/jobsinsight.go
    /// parseReasons); a note it cannot split arrives whole as `.other`.
    public struct Reason: Codable, Equatable, Sendable {
        public enum Kind: String, Codable, Sendable {
            case fits, gap, check, other

            public var label: String {
                switch self {
                case .fits: "Fits"
                case .gap: "Gap"
                case .check: "Check"
                case .other: ""
                }
            }
        }

        public var kind: Kind
        public var text: String
        /// The backend's probability for this pick, in percent.
        public var pct: Int?

        public init(kind: Kind, text: String, pct: Int? = nil) {
            self.kind = kind
            self.text = text
            self.pct = pct
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            // A kind this app does not know reads as plain text, never as a decode failure.
            kind = (try? c.decode(Kind.self, forKey: .kind)) ?? .other
            text = try c.decode(String.self, forKey: .text)
            pct = try c.decodeIfPresent(Int.self, forKey: .pct)
        }
    }

    /// The reasons Vani split the note into; a note with no split (an older
    /// vani) is shown whole.
    public var reasons: [Reason] {
        if let reasonList, !reasonList.isEmpty { return reasonList }
        guard let note = explanation?.trimmingCharacters(in: .whitespaces), !note.isEmpty else { return [] }
        return [Reason(kind: .other, text: note)]
    }

    /// "3d", "5w", "4mo": how old the posting is; "~" marks an age measured
    /// from when Vani first saw it, not a board date.
    public var ageLabel: String? {
        guard let d = ageDays, d >= 0 else { return nil }
        let prefix = freshSource == .seen ? "~" : ""
        switch d {
        case 0: return prefix + "today"
        case ..<14: return prefix + "\(d)d"
        case ..<60: return prefix + "\(d / 7)w"
        default: return prefix + "\(d / 30)mo"
        }
    }

    /// Keywords your target summary names, then gaps, then the rest (Vani's order).
    public var keywordList: [Keyword] { keywords ?? [] }
}

public struct JobsPage: Codable, Equatable, Sendable {
    public var view: String
    public var query: String?
    public var jobs: [Job]
    public var total: Int
    public var shown: Int
    public var counts: [String: Int]
    public var torn: Bool?
    /// The ledger directory vani read, and how many postings it holds.
    public var ledger: String?
    public var records: Int?
    public var sort: String?
    public var insight: JobsInsight?
    /// Postings read from a board but not triaged yet, which no view lists.
    /// Optional so a vani from before it still decodes.
    public var pending: PendingTriage?

    public init(view: String, jobs: [Job], total: Int, shown: Int, counts: [String: Int], torn: Bool? = nil) {
        self.view = view
        self.jobs = jobs
        self.total = total
        self.shown = shown
        self.counts = counts
        self.torn = torn
    }

    public func count(_ v: JobView) -> Int { counts[v.rawValue] ?? 0 }
}

/// Postings vani has read from a board but not triaged (status live). The
/// Jobs views list only triaged roles, so without this a company whose
/// postings all wait for triage looks like a company with no jobs.
public struct PendingTriage: Codable, Equatable, Sendable {
    public struct Company: Codable, Equatable, Sendable {
        public var company: String
        public var n: Int
    }

    /// Every posting awaiting triage.
    public var total: Int
    /// Those the search names (`total` when there is no search).
    public var matching: Int
    /// Companies with the most waiting, most first (vani keeps at most 20).
    public var companies: [Company]

    public init(total: Int, matching: Int, companies: [Company]) {
        self.total = total
        self.matching = matching
        self.companies = companies
    }

    /// The footer note, or nil when nothing waits.
    public var footerNote: String? {
        total > 0 ? "· \(total) awaiting triage" : nil
    }

    /// The footer note's help: who waits, and why they are not listed.
    public var footerHelp: String {
        let top = companies.prefix(5).map { "\($0.company) \($0.n)" }.joined(separator: ", ")
        return "\(total) postings were read from their boards but not triaged yet, so no view lists them"
            + (top.isEmpty ? "" : " (\(top))")
            + ". Each run triages up to the config's advance_per_run of them."
    }

    /// What an empty search result adds when the search names postings that
    /// are still waiting, or nil when it names none.
    public func emptySearchNote(query: String) -> String? {
        guard matching > 0, !query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return nil }
        return "\(matching) \(matching == 1 ? "posting" : "postings") matching “\(query)” \(matching == 1 ? "is" : "are") awaiting triage; \(matching == 1 ? "it appears" : "they appear") here once a run triages \(matching == 1 ? "it" : "them")."
    }
}

/// What the scores rest on: without a target summary keywords are listed but
/// not marked, and `warning` says so.
public struct JobsInsight: Codable, Equatable, Sendable {
    public var target: Bool
    public var targetTerms: Int?
    public var halfLifeDays: Double?
    public var vocab: Int?
    public var boilerplate: Int?
    public var warning: String?

    enum CodingKeys: String, CodingKey {
        case target, vocab, boilerplate, warning
        case targetTerms = "target_terms"
        case halfLifeDays = "half_life_days"
    }
}

/// List orders `vani jobs list --sort` takes.
public enum JobSort: String, CaseIterable, Identifiable, Sendable {
    case standard = "default", priority, relevance, fresh, company, salary
    public var id: String { rawValue }
    public var title: String {
        switch self {
        case .standard: "Rank, then fit"
        case .priority: "Priority (relevance × freshness)"
        case .relevance: "Relevance"
        case .fresh: "Newest first"
        case .company: "Company"
        case .salary: "Salary (highest stated first)"
        }
    }
}

/// The selection filters list and stats share. Values are the jdtriage
/// fixture's options; "none" means no reading.
public struct JobFilter: Equatable, Sendable {
    public var fits: Set<String> = []
    public var workModes: Set<String> = []
    public var sponsorships: Set<String> = []
    /// At most this many days old; nil is any age.
    public var maxAgeDays: Int?
    public var openOnly = false
    /// US state codes ("none": no state listed), areas as the location names
    /// them, and remote as the board lists it (not triage's work mode).
    public var states: Set<String> = []
    public var areas: Set<String> = []
    public var remoteOnly = false
    /// Widens `states`/`areas` to roles listed as remote too ("remote, or in
    /// Texas"). It filters nothing without a state or area, and `remoteOnly`
    /// overrides it, so it is sent to Vani only when it changes the selection.
    public var orRemote = false
    /// At least this much a year in `currency`, by the top of the stated
    /// range. A job stating no pay stays unless `salaryListed`; one stating
    /// pay only in another currency is left out (Vani has no exchange rates).
    public var minSalary: Int?
    /// The currency of `minSalary`, the salary sort and the pay stats.
    public var currency = "USD"
    public var salaryListed = false
    public var seniorities: Set<String> = []
    /// At least this relevance (0-100); nil is any.
    public var minRelevance: Int?

    public init() {}

    /// No filter on. The currency alone filters nothing, nor does `orRemote`
    /// with nothing to widen.
    public var isEmpty: Bool {
        var f = self
        f.currency = JobFilter().currency
        if !widensToRemote { f.orRemote = false }
        return f == JobFilter()
    }

    /// How many filter groups are on, for a badge.
    public var activeCount: Int {
        [!fits.isEmpty, !workModes.isEmpty, !sponsorships.isEmpty, maxAgeDays != nil, openOnly,
         !states.isEmpty, !areas.isEmpty, remoteOnly, widensToRemote,
         minSalary != nil, salaryListed, !seniorities.isEmpty, minRelevance != nil].filter { $0 }.count
    }

    /// `orRemote` with a state or area to widen and no `remoteOnly`: the only
    /// case Vani accepts --or-remote, and the only one where it does anything.
    public var widensToRemote: Bool {
        orRemote && !remoteOnly && (!states.isEmpty || areas.contains { !$0.contains(",") && !$0.trimmingCharacters(in: .whitespaces).isEmpty })
    }

    /// "Only remote" and "or remote" exclude each other: turning one on turns
    /// the other off, so every control that sets them agrees.
    public mutating func setRemoteOnly(_ on: Bool) {
        remoteOnly = on
        if on { orRemote = false }
    }

    public mutating func setOrRemote(_ on: Bool) {
        orRemote = on
        if on { remoteOnly = false }
    }

    /// Turn every filter off, keeping the chosen currency.
    public mutating func clear() {
        let c = currency
        self = JobFilter()
        currency = c
    }

    var arguments: [String] {
        var a: [String] = []
        if !fits.isEmpty { a += ["--fit", fits.sorted().joined(separator: ",")] }
        if !workModes.isEmpty { a += ["--work-mode", workModes.sorted().joined(separator: ",")] }
        if !sponsorships.isEmpty { a += ["--sponsorship", sponsorships.sorted().joined(separator: ",")] }
        if let d = maxAgeDays { a += ["--max-age", String(d)] }
        if openOnly { a.append("--open") }
        if !states.isEmpty { a += ["--state", states.sorted().joined(separator: ",")] }
        // An area never holds a comma (Vani splits locations on them), but one
        // that did would split into two filters, so it is left out instead.
        let areaList = areas.filter { !$0.contains(",") && !$0.trimmingCharacters(in: .whitespaces).isEmpty }
        if !areaList.isEmpty { a += ["--area", areaList.sorted().joined(separator: ",")] }
        if remoteOnly { a.append("--remote") }
        if widensToRemote { a.append("--or-remote") }
        if let s = minSalary, s > 0 { a += ["--min-salary", String(s)] }
        if salaryListed { a.append("--salary-listed") }
        if currency != JobFilter().currency { a += ["--currency", currency] }
        if !seniorities.isEmpty { a += ["--seniority", seniorities.sorted().joined(separator: ",")] }
        if let r = minRelevance, r > 0 { a += ["--min-relevance", String(r)] }
        return a
    }
}

/// `vani jobs stats --json`: distributions over the same selection a list shows.
/// Which plotted point a click picks: the nearest one strictly within
/// `radius` points, the earliest on a tie; nil when none is that close or a
/// position is unknown (a point the chart could not place is never picked).
public enum ChartHit {
    public static let defaultRadius: Double = 14

    public static func nearest(to target: CGPoint, among points: [CGPoint?], within radius: Double = defaultRadius) -> Int? {
        guard radius > 0, target.x.isFinite, target.y.isFinite else { return nil }
        var best: (index: Int, distance: Double)?
        for (i, p) in points.enumerated() {
            guard let p, p.x.isFinite, p.y.isFinite else { continue }
            let d = Double(hypot(p.x - target.x, p.y - target.y))
            if d < (best?.distance ?? radius) { best = (i, d) }
        }
        return best?.index
    }
}

public struct JobsStats: Codable, Equatable, Sendable {
    public struct AgeBucket: Codable, Equatable, Sendable, Identifiable {
        public var label: String
        public var maxDays: Int
        public var posted: Int
        public var seen: Int
        public var id: String { label }
        enum CodingKeys: String, CodingKey {
            case label, posted, seen
            case maxDays = "max_days"
        }
    }

    public struct Company: Codable, Equatable, Sendable, Identifiable {
        public var company: String
        public var n: Int
        public var strong: Int
        public var meanRelevance: Double
        public var id: String { company }
        enum CodingKeys: String, CodingKey {
            case company, n, strong
            case meanRelevance = "mean_relevance"
        }
    }

    public struct KeywordCount: Codable, Equatable, Sendable, Identifiable {
        public var term: String
        public var cat: String
        public var n: Int
        public var target: Bool?
        public var id: String { term }
    }

    public var view: String
    public var total: Int
    public var fit: [String: Int]
    public var workMode: [String: Int]
    public var sponsorship: [String: Int]
    public var seniority: [String: Int]
    public var basis: [String: Int]
    public var freshSource: [String: Int]
    public var age: [AgeBucket]
    public var relevance: [Int]
    public var priority: [Int]
    public var companies: [Company]
    public var keywords: [KeywordCount]
    public var insight: JobsInsight?
    public var torn: Bool?
    /// Jobs per US state ("none": names no state), the most-named areas, and
    /// how many the board lists as remote, by where. Optional so a vani from
    /// before location parsing still decodes.
    public var state: [String: Int]?
    public var area: [AreaCount]?
    public var remoteListed: Int?
    public var remoteScope: [String: Int]?
    /// The pay the jobs state in the filter's currency. Optional so a vani
    /// from before pay reading still decodes.
    public var salary: Salary?

    public struct Salary: Codable, Equatable, Sendable {
        public struct Bucket: Codable, Equatable, Sendable, Identifiable {
            public var label: String
            public var min: Double
            /// -1: no upper bound.
            public var max: Double
            public var n: Int
            public var id: String { label }
        }

        public var currency: String
        /// Jobs stating pay in `currency`, only in another one, and none.
        public var listed: Int
        public var other: Int
        public var unlisted: Int
        /// The median middle of the listed annual ranges; 0 with none listed.
        public var median: Double
        /// By the middle of each job's annual range.
        public var buckets: [Bucket]
    }

    public struct AreaCount: Codable, Equatable, Sendable, Identifiable {
        public var area: String
        public var state: String?
        public var n: Int
        public var id: String { label }
        public var label: String { [area, state ?? ""].filter { !$0.isEmpty }.joined(separator: ", ") }
    }

    enum CodingKeys: String, CodingKey {
        case view, total, fit, sponsorship, seniority, basis, age, relevance, priority, companies, keywords, insight, torn
        case state, area, salary
        case workMode = "work_mode"
        case freshSource = "fresh_source"
        case remoteListed = "remote_listed"
        case remoteScope = "remote_scope"
    }

    /// States by how many jobs name them, most first; "none" left out.
    public var statesByCount: [(code: String, n: Int)] {
        (state ?? [:]).filter { $0.key != "none" }
            .sorted { $0.value != $1.value ? $0.value > $1.value : $0.key < $1.key }
            .map { ($0.key, $0.value) }
    }
}

public struct JobsClient: Sendable {
    public static let timeout: TimeInterval = 30
    /// A list of 5,000 jobs without descriptions is about 5 MB.
    static let maxOutputBytes = 16 << 20
    public static let maxNoteBytes = 2000

    public let vaniBinary: URL
    public let configPath: String?
    public let stateDir: String?
    let timeout: TimeInterval

    public init(vaniBinary: URL, configPath: String? = nil, stateDir: String? = nil, timeout: TimeInterval = JobsClient.timeout) {
        self.vaniBinary = vaniBinary
        self.configPath = configPath
        self.stateDir = stateDir
        self.timeout = timeout
    }

    public func list(_ view: JobView, query: String = "", sort: JobSort = .standard, filter: JobFilter = JobFilter(), limit: Int = 1000) throws -> JobsPage {
        try call(Self.listArguments(view, query: query, sort: sort, filter: filter, limit: limit))
    }

    /// Distributions over the selection a list with the same arguments shows.
    public func stats(_ view: JobView, query: String = "", filter: JobFilter = JobFilter()) throws -> JobsStats {
        try call(Self.statsArguments(view, query: query, filter: filter))
    }

    static func listArguments(_ view: JobView, query: String, sort: JobSort, filter: JobFilter, limit: Int) -> [String] {
        var args = ["jobs", "list", "--json", "--view", view.rawValue, "--limit", String(limit)]
        if sort != .standard { args += ["--sort", sort.rawValue] }
        return args + selection(query, filter)
    }

    static func statsArguments(_ view: JobView, query: String, filter: JobFilter) -> [String] {
        ["jobs", "stats", "--json", "--view", view.rawValue] + selection(query, filter)
    }

    static func selection(_ query: String, _ filter: JobFilter) -> [String] {
        var args: [String] = []
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        if !q.isEmpty { args += ["--q", String(q.prefix(200))] }
        return args + filter.arguments
    }

    public func show(_ key: String) throws -> Job {
        try call(["jobs", "show", "--json", key])
    }

    /// Set the mark, or clear it with nil.
    public func mark(_ key: String, _ mark: JobMark?) throws -> Job {
        try call(["jobs", "mark", "--json", key, mark?.rawValue ?? "clear"])
    }

    /// Save the note; an empty note clears it.
    public func note(_ key: String, _ text: String) throws -> Job {
        guard text.utf8.count <= Self.maxNoteBytes else {
            throw BankError("A note can be at most \(Self.maxNoteBytes) bytes.")
        }
        return try call(["jobs", "note", "--json", key, text])
    }

    func call<T: Decodable>(_ args: [String]) throws -> T {
        try VaniProcess.call(vaniBinary, args, configPath: configPath, stateDir: stateDir,
                             timeout: timeout, maxOutputBytes: Self.maxOutputBytes, decoder: Self.decoder,
                             tooOld: "this vani is too old for Jobs; rebuild the app")
    }

    /// Go writes RFC 3339 times with optional fractional seconds and an offset.
    /// The parse styles are Sendable values built once per decode, not a
    /// formatter object per date: a 5,000-job list carries some 15,000 dates.
    static var decoder: JSONDecoder {
        let d = JSONDecoder()
        let frac = Date.ISO8601FormatStyle(includingFractionalSeconds: true)
        let plain = Date.ISO8601FormatStyle()
        d.dateDecodingStrategy = .custom { dec in
            let s = try dec.singleValueContainer().decode(String.self)
            if let date = (try? frac.parse(s)) ?? (try? plain.parse(s)) { return date }
            throw DecodingError.dataCorrupted(.init(codingPath: dec.codingPath, debugDescription: "not an RFC 3339 time: \(s.prefix(40))"))
        }
        return d
    }
}
