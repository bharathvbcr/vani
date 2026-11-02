import Foundation

// The Jobs tab is a client of `vani jobs … --json` (cmd/vani/jobscmd.go): the
// Go side owns the ledger and the marks store; this side only lists, shows and
// asks for marks. Posting text is untrusted board content: it is shown as
// plain text, never as Markdown, and only http(s) links are opened.

public enum JobView: String, CaseIterable, Identifiable, Sendable {
    /// `pending` is the postings no run has triaged yet (jobview.ViewPending):
    /// listed apart from the ranked views, and without fit, relevance or priority.
    case new, saved, applied, hidden, all, pending
    public var id: String { rawValue }
    public var title: String {
        switch self {
        case .new: "New"
        case .saved: "Saved"
        case .applied: "Applied"
        case .hidden: "Hidden"
        case .all: "All"
        case .pending: "Awaiting triage"
        }
    }

    /// The tab's words with its count, "Awaiting triage (8,525)"; the bare title
    /// when vani sent no count (unknown is not zero).
    public func label(count: Int?, locale: Locale = .current) -> String {
        guard let count else { return title }
        return "\(title) (\(count.formatted(.number.locale(locale))))"
    }

    /// The view's roles have a fit, relevance and priority to show, to chart and
    /// to count as seen. Postings awaiting triage have none, so the app asks no
    /// stats or gaps of them (vani refuses) and reports no impressions for them:
    /// a role the owner never had ranked was not "seen and passed".
    public var isRanked: Bool { self != .pending }
}

public enum JobMark: String, Codable, Sendable, CaseIterable {
    case saved, applied, hidden
}

/// What happened to an application after it was applied (vani's `stage`, internal/marks stages.go). The
/// mark stays `applied` at every stage, so JobMark above never changes; the stage is beside it. Declared
/// in the order a search moves through them, the two that end it last.
public enum JobStage: String, Codable, Sendable, CaseIterable {
    case screening, interview, offer, rejected, withdrawn

    /// Rejected and withdrawn end the application.
    public var isTerminal: Bool { self == .rejected || self == .withdrawn }
    public var title: String { rawValue.prefix(1).uppercased() + rawValue.dropFirst() }
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
    public var category: String?
    public var firstSeen: Date?
    public var postedAt: Date?
    public var mark: JobMark?
    public var markedAt: Date?
    /// The stage of an applied job, its latest change and the furthest stage it reached (rejected and
    /// withdrawn leave that as it was). Nil for a job with no stage, and from a vani that predates stages.
    /// Lenient: a stage this app does not know reads as nil and never fails the list. `markedAt` stays the
    /// date first applied.
    public var stage: JobStage? {
        get { stageWire?.value }
        set { stageWire = newValue.map { Lenient($0) } }
    }
    public var furthestStage: JobStage? {
        get { furthestStageWire?.value }
        set { furthestStageWire = newValue.map { Lenient($0) } }
    }
    public var stageAt: Date?
    var stageWire: Lenient<JobStage>?
    var furthestStageWire: Lenient<JobStage>?
    public var note: String?
    public var noteAt: Date?
    public var description: String?

    /// No run has triaged this posting (vani's `untriaged`, set from its status):
    /// it has no fit, relevance, priority, keywords or row, and none is shown.
    /// Absent from an older vani, which never sends such rows in a ranked view.
    /// Decoded leniently like the duplicate fields below: a value that is not a
    /// bool reads as nil (a triaged row) and never fails the list.
    public var untriaged: Bool? {
        get { untriagedWire?.value }
        set { untriagedWire = newValue.map { Lenient($0) } }
    }
    /// The kind of rule ("excluded" or "title") by which the next run's
    /// prefilter would drop this untriaged posting before triage; never the rule.
    /// Lenient: a value that is not a string reads as nil.
    public var prefilterDrop: String? {
        get { prefilterDropWire?.value }
        set { prefilterDropWire = newValue.map { Lenient($0) } }
    }
    var untriagedWire: Lenient<Bool>?
    var prefilterDropWire: Lenient<String>?

    /// One line about the posting's application form (`apply_form_note`, worded by vani): its flagged
    /// questions (sponsorship, work authorization, places you must be in), or that the form was not
    /// read. A note for a person, never a filter. Nil from a vani that predates it, and for an ATS whose
    /// form vani cannot read. Lenient: a value that is not a string reads as nil and never fails the list.
    public var applyFormNote: String? {
        get { applyFormNoteWire?.value }
        set { applyFormNoteWire = newValue.map { Lenient($0) } }
    }
    var applyFormNoteWire: Lenient<String>?

    /// Sonnet's manual re-triage of this posting (`vani jobs retriage`), stored beside
    /// the ledger. Absent until one is made, and from an older vani. Lenient like the
    /// fields above: an object this app cannot read is nil and never fails the list.
    public var manualTriage: ManualTriage? {
        get { manualTriageWire?.value }
        set { manualTriageWire = newValue.map { Lenient($0) } }
    }
    var manualTriageWire: Lenient<ManualTriage>?

    /// The job's tailored resume versions (`vani jobs resume`, summarised by `jobs show`). Nil when the
    /// job has none, and from a vani that predates it. A `resume` value this app cannot read is
    /// `ResumeSummary.unreadableReply`, never nil: absence would read as "no resume was ever built".
    public var resume: ResumeSummary? {
        get { resumeWire.map { $0.value ?? .unreadableReply } }
        set { resumeWire = newValue.map { Lenient($0) } }
    }
    var resumeWire: Lenient<ResumeSummary>?

    /// Other postings at this company were applied to lately, and this posting is likely an earlier
    /// role relisted (internal/samecompany, `jobs list|show`). Notes only, never a filter. Lenient: a
    /// note this app cannot read is nil and never fails the list; nil also from a vani that predates them.
    public var repeatApply: RepeatApply? {
        get { repeatApplyWire?.value }
        set { repeatApplyWire = newValue.map { Lenient($0) } }
    }
    public var repost: RepostNote? {
        get { repostWire?.value }
        set { repostWire = newValue.map { Lenient($0) } }
    }
    var repeatApplyWire: Lenient<RepeatApply>?
    var repostWire: Lenient<RepostNote>?

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
    /// How `priority` was built, one entry per component (jobinsight.Score).
    /// Display only, and decoded leniently: see `ScoreFactors`.
    public var scoreFactors: ScoreFactors?
    /// The cached match against your résumé knowledge base. Display only.
    public var semantic: SemanticMatch?
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

    // Near-duplicate display (cmd/vani/jobscmd.go jobJSON). Each is decoded
    // leniently (see `Lenient`): a value of the wrong type reads as absent and
    // never fails the list. Read them through the accessors below.
    var dupOfWire: Lenient<String>?
    var dupCandidateOfWire: Lenient<String>?
    var dupSiblingOfWire: Lenient<String>?
    var dupScoreWire: Lenient<Double>?

    /// The posting key this one repeats (status duplicate).
    public var dupOf: String? {
        get { dupOfWire?.value }
        set { dupOfWire = newValue.map { Lenient($0) } }
    }
    /// The posting key this one may repeat (a candidate, not a certainty).
    public var dupCandidateOf: String? {
        get { dupCandidateOfWire?.value }
        set { dupCandidateOfWire = newValue.map { Lenient($0) } }
    }
    /// The posting key of the same role in another location.
    public var dupSiblingOf: String? {
        get { dupSiblingOfWire?.value }
        set { dupSiblingOfWire = newValue.map { Lenient($0) } }
    }
    /// The description overlap behind the duplicate reading, 0...1; nil when
    /// absent, not a number or outside 0...1.
    public var dupScore: Double? {
        get { dupScoreWire?.value.flatMap { $0.isFinite && $0 >= 0 && $0 <= 1 ? $0 : nil } }
        set { dupScoreWire = newValue.map { Lenient($0) } }
    }

    /// The company icon Vani cached for this job (internal/logo), decoded
    /// leniently like the duplicate fields. Read it through `logoPath`.
    var logoWire: Lenient<String>?

    /// The cached icon's file, or nil to draw the monogram: only an absolute,
    /// already-standardized path to a .png, .ico or .jpg file is used.
    public var logoPath: String? {
        get { logoWire?.value.flatMap(Self.usableLogoPath) }
        set { logoWire = newValue.map { Lenient($0) } }
    }

    static func usableLogoPath(_ p: String) -> String? {
        guard p.count <= 1024, p.hasPrefix("/"), !p.contains("\0"),
              (p as NSString).standardizingPath == p,
              ["png", "ico", "jpg"].contains((p as NSString).pathExtension) else { return nil }
        return p
    }

    /// The versioned row contract (JobRow, owned by Go): Vani's own relevance,
    /// priority and display strings. Nil only for a vani that predates it; a
    /// row of an unknown version fails the whole decode, never half-renders.
    public var row: JobRow?

    public var id: String { key }

    enum CodingKeys: String, CodingKey {
        case key, company, title, location, remote, department, url, status, closed, rank, coverage,
             explanation, missing, unsure, lca, employer, triage, category, mark, note, description,
             relevance, priority, keywords, semantic, row
        case untriagedWire = "untriaged"
        case manualTriageWire = "manual_triage"
        case resumeWire = "resume"
        case repeatApplyWire = "repeat_apply"
        case repostWire = "repost"
        case prefilterDropWire = "prefilter_drop"
        case applyFormNoteWire = "apply_form_note"
        case dupOfWire = "dup_of"
        case dupCandidateOfWire = "dup_candidate_of"
        case dupSiblingOfWire = "dup_sibling_of"
        case dupScoreWire = "dup_score"
        case logoWire = "logo"
        case scoreFactors = "score_factors"
        case applyURL = "apply_url"
        case firstSeen = "first_seen"
        case postedAt = "posted_at"
        case markedAt = "marked_at"
        case stageWire = "stage"
        case furthestStageWire = "furthest_stage"
        case stageAt = "stage_at"
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
    public static var seniorityOptions: [String] { Labels.current.values(Labels.current.triage["seniority"] ?? []) }
    /// The currencies Vani reads pay in (jobinsight.PayCurrencies; the Go test
    /// TestSwiftPayCurrenciesMatch fails when this list drifts from it).
    public static let payCurrencies = ["USD", "CAD", "EUR", "GBP", "AUD", "CHF", "SGD", "NZD", "INR", "JPY", "SEK", "NOK", "DKK", "PLN",
                                       "CNY", "TWD", "KRW", "MYR", "RON", "RSD", "SAR"]

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
    /// The best link for sharing: the posting URL, or the apply URL if no posting URL was given.
    public var shareLink: URL? { postingLink ?? applyLink }

    /// A one-line summary for sharing (e.g. "Software Engineer at Acme").
    public var shareSubject: String {
        company.isEmpty ? title : "\(title) at \(company)"
    }

    /// An absolute http(s) URL with a host, or nil. Vani already filters links;
    /// this is the second check, at the point a link becomes clickable.
    public static func httpURL(_ raw: String?) -> URL? {
        guard let raw, let u = URL(string: raw), let scheme = u.scheme?.lowercased(),
              scheme == "http" || scheme == "https", let host = u.host, !host.isEmpty else { return nil }
        return u
    }

    /// Triage's role-fit label in words, when triage gave one. The string is
    /// Go's (`row.fitLabel`); only a row-less job (a vani older than the row
    /// contract) reads the loaded vocabulary (`Labels`) itself.
    public var fitLabel: String? {
        if let row { return row.fitLabel }
        guard let fit = triage?["role_fit"] else { return nil }
        return Labels.current.fit.first { $0.value == fit }?.label
    }

    /// Triage's other readings in words (seniority, work mode, sponsorship),
    /// leaving out "not stated".
    public var triageLabels: [String] {
        ["seniority", "work_mode", "sponsorship"].compactMap { slot in
            guard let v = triage?[slot], v != "not_stated" else { return nil }
            return Labels.current.label(for: v)
        }
    }

    /// Career track / category folder options, in vani's order.
    public static var categoryOptions: [String] { Labels.current.values(Labels.current.category) }

    /// Category folder / track name in words ("ai-engineer" → "AI Engineer").
    public var categoryLabel: String? {
        guard let category, !category.isEmpty else { return nil }
        return Labels.current.label(for: category) ?? category.replacingOccurrences(of: "-", with: " ").capitalized
    }

    /// A jdtriage option value in words ("strong_fit" → "Strong fit"); "none"
    /// is no reading. The words are vani's (`Labels`).
    public static func label(for option: String) -> String { Labels.label(for: option) }

    /// The fixture's role_fit options, best first, and the other slots' options.
    public static var roleFitOrder: [String] { Labels.current.values(Labels.current.fit) }
    public static var workModeOptions: [String] { Labels.current.values(Labels.current.triage["work_mode"] ?? []) }
    public static var sponsorshipOptions: [String] { Labels.current.values(Labels.current.triage["sponsorship"] ?? []) }

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
        if let row { return row.ageLabel }
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

    /// The posting waits for a run to triage it: show "Not triaged yet" where a
    /// fit and a score would be, and no score at all.
    public var isUntriaged: Bool { untriaged == true }

    /// What the owner is told about an untriaged posting.
    public static let untriagedNote = "Not triaged yet: no fit, relevance or priority until a run reads it."

    /// A note for an untriaged posting the next run's prefilter would drop
    /// before triage (so it is likely noise), or nil. The rule is never named.
    public var prefilterNote: String? {
        guard isUntriaged, let kind = prefilterDrop, !kind.isEmpty else { return nil }
        switch kind {
        case "excluded": return "The next run would drop this: it matches your exclusions."
        case "title": return "The next run would drop this: its title matches a rule that drops roles."
        default: return "The next run would drop this before triage."
        }
    }

    /// The ranking breakdown for the detail view, or nil when Vani sent none
    /// (an older vani, or a posting it did not score).
    public var rankingBreakdown: RankingBreakdown? {
        let factors = scoreFactors?.items ?? []
        let evidence = semantic?.topEvidence ?? []
        if factors.isEmpty && evidence.isEmpty && semantic?.score == nil { return nil }
        return RankingBreakdown(factors: factors, semanticScore: semantic?.score, evidence: evidence, ageKnown: ageDays != nil)
    }
}

/// A display-only field that decodes to nil when it is not a `T`, so one odd
/// value never fails the whole list.
struct Lenient<T: Codable & Equatable & Sendable>: Codable, Equatable, Sendable {
    var value: T?

    init(_ value: T?) { self.value = value }

    init(from decoder: Decoder) throws {
        value = try? decoder.singleValueContainer().decode(T.self)
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        if let value { try c.encode(value) } else { try c.encodeNil() }
    }
}

/// A row's duplicate note: "Possible duplicate of Acme — Engineer (87%)" or
/// "Same role, other location". Keys are untrusted text, shown verbatim.
public struct DuplicateBadge: Equatable, Sendable {
    public enum Kind: Sendable { case candidate, sibling }
    public var kind: Kind
    public var text: String
    public var help: String
}

/// A repeat-apply or repost note for the detail view (Job.sameCompanyNotes).
public struct SameCompanyNote: Equatable, Sendable {
    public enum Kind: Sendable { case repeatApply, repost }
    public var kind: Kind
    public var text: String

    /// The most a note shows; vani's own sentences stay well under it.
    static let maxRunes = 400

    static func clean(_ raw: String?) -> String? {
        guard let raw else { return nil }
        let t = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        if t.isEmpty { return nil }
        return t.count > maxRunes ? String(t.prefix(maxRunes)) + "…" : t
    }
}

extension Job {
    /// The target's "company — title" when it is among `loaded`, else the key text.
    static func duplicateTarget(_ key: String, in loaded: [String: Job]) -> String {
        if let t = loaded[key] {
            let parts = [t.company, t.title].map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty }
            if !parts.isEmpty { return parts.joined(separator: " — ") }
        }
        return key.count > 80 ? String(key.prefix(80)) + "…" : key
    }

    /// Candidate first, then sibling. `loaded` maps posting key to job, to name the target.
    public func duplicateBadges(loaded: [String: Job]) -> [DuplicateBadge] {
        var out: [DuplicateBadge] = []
        if let k = dupCandidateOf, !k.isEmpty {
            let score = dupScore.map { " (\(Int(($0 * 100).rounded()))%)" } ?? ""
            let target = Self.duplicateTarget(k, in: loaded)
            out.append(DuplicateBadge(kind: .candidate, text: "Possible duplicate of \(target)\(score)",
                                      help: "Its description overlaps \(target). Vani keeps both; check before applying twice."))
        }
        if let k = dupSiblingOf, !k.isEmpty {
            out.append(DuplicateBadge(kind: .sibling, text: "Same role, other location",
                                      help: "The same role is also listed at \(Self.duplicateTarget(k, in: loaded))."))
        }
        return out
    }

    /// The repeat-apply and repost sentences vani worded, repeat-apply first, for the detail view. Text is
    /// untrusted posting content (a title is quoted in it): bounded, and a blank sentence is no note.
    public var sameCompanyNotes: [SameCompanyNote] {
        var out: [SameCompanyNote] = []
        if let t = SameCompanyNote.clean(repeatApply?.text) { out.append(SameCompanyNote(kind: .repeatApply, text: t)) }
        if let t = SameCompanyNote.clean(repost?.text) { out.append(SameCompanyNote(kind: .repost, text: t)) }
        return out
    }

    /// `jobs` without the rows Vani flags as possible duplicates of another.
    public static func hidingPossibleDuplicates(_ jobs: [Job]) -> [Job] {
        jobs.filter { ($0.dupCandidateOf ?? "").isEmpty }
    }
}

/// One component of `priority` as `vani jobs` reports it (jobinsight.Factor).
/// `value` is in 0...1; `known` false means the signal was absent and `value`
/// is only a neutral prior, so it is shown as "not known", never as a score.
public struct ScoreFactor: Equatable, Sendable {
    public var name: String
    public var value: Double
    /// Its share of the match for additive components, 0 for multipliers.
    public var weight: Double
    public var known: Bool
    /// Why the value is what it is, for the few factors that say (`age_neutral`
    /// on a freshness held flat for an employer that closes by application cap).
    public var note: String?
    /// Which reading a fit factor rests on when it is not the primary model's: "escalate"
    /// (the escalation tier) or "manual" (Sonnet, asked by hand). Nil for every other
    /// factor and for a primary reading.
    public var backend: String?
    /// That reading's value (a role-fit option), when `backend` names one.
    public var reading: String?

    public init(name: String, value: Double, weight: Double = 0, known: Bool = true, note: String? = nil,
                backend: String? = nil, reading: String? = nil) {
        self.name = name
        self.value = value
        self.weight = weight
        self.known = known
        self.note = note
        self.backend = backend
        self.reading = reading
    }

    /// Who read the value, in words, for a factor that does not rest on the primary
    /// model; nil otherwise (and for a backend this app has no words for).
    public var sourceNote: String? {
        switch backend {
        case "manual": "read by \(RetriageWords.manualName)"
        case "escalate": "read by the escalation model"
        default: nil
        }
    }

    /// The order Score builds them in; a name this app does not know sorts after.
    public static let knownNames = ["fit", "coverage", "semantic", "keyword", "title", "employer", "kb_match", "gate", "fresh", "company"]
    /// The multipliers: they scale the blend rather than add to it.
    public static let multiplierNames: Set<String> = ["gate", "fresh", "company"]

    public var isMultiplier: Bool { Self.multiplierNames.contains(name) || weight <= 0 }

    /// The value as a 0...1 fraction, whatever Vani sent.
    public var fraction: Double { value.isFinite ? min(max(value, 0), 1) : 0 }

    /// Words for the factor, or the name itself when it is a new one.
    public var title: String {
        switch name {
        case "fit": "Role fit"
        case "coverage": "Résumé coverage"
        case "semantic": "Knowledge-base match"
        case "keyword": "Target keywords"
        case "title": "Title"
        case "employer": "Employer"
        case "kb_match": "Language-model read"
        case "gate": "Gate"
        case "fresh": "Freshness"
        case "company": "Company history"
        default: name.replacingOccurrences(of: "_", with: " ").capitalized
        }
    }

    /// What the factor measures, in one line.
    public var explanation: String {
        switch name {
        case "fit": "the decision model's role-fit reading, weighted by how sure it is"
        case "coverage": "how much of the posting's must-haves your résumé covers (DevPrism)"
        case "semantic": "how close the posting reads to your résumé roles (EmbeddingGemma)"
        case "keyword": "overlap with your target summary's terms"
        case "title": "signals in the job title"
        case "employer": "your employer preference"
        case "kb_match": "a language model's read of whether the posting's work is the work in your knowledge base (asked only for the top postings: vani kbmatch run)"
        case "gate": "1 unless a confident mismatch, an excluded title or a drop rule lowers it"
        case "fresh": "newer postings score higher: the bonus halves every half-life of age (Settings), with a floor"
        case "company": "a small nudge learned from the roles you saved and hid at this company"
        default: "a ranking component this app has no description for"
        }
    }

    /// "not known", or the value out of 100 for an additive component, or
    /// "×0.85" for a multiplier.
    public var valueLabel: String {
        guard known else { return "not known" }
        if isMultiplier { return "×" + String(format: "%.2f", fraction) }
        return "\(Int((fraction * 100).rounded()))"
    }

    /// What an absent signal means for this factor, shown beside "not known".
    /// The knowledge-base match is absent until the posting has been embedded
    /// against the current knowledge base, which is a gap in what Vani has
    /// computed, not a reading of the job.
    public var unknownNote: String {
        if name == "semantic" {
            return "not scored yet: this posting has not been compared with your knowledge base (`vani semantic warm`), so it is ranked on the other factors, held toward a cautious default until it is scored; it does not mean the job is a poor match"
        }
        return "no signal: a neutral prior stands in"
    }

    /// "25% of the blend", for an additive component that carries weight.
    public var weightLabel: String? {
        guard weight.isFinite, weight > 0 else { return nil }
        return "\(Int((min(weight, 1) * 100).rounded()))% of the blend"
    }
}

/// `score_factors`. Decoding never throws: it is explanation, not data the
/// list depends on, so an element that is not a factor is skipped and a field
/// that is not a list reads as none. Unknown factor names are kept.
public struct ScoreFactors: Codable, Equatable, Sendable {
    public var items: [ScoreFactor]

    public init(_ items: [ScoreFactor]) { self.items = items }

    private struct Raw: Decodable {
        var name: String
        var value: Double
        var weight: Double?
        var known: Bool?
        var note: String?
        var backend: String?
        var reading: String?

        private enum CodingKeys: String, CodingKey { case name, value, weight, known, note, backend, reading }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            name = try c.decode(String.self, forKey: .name)
            value = try c.decode(Double.self, forKey: .value)
            weight = try c.decodeIfPresent(Double.self, forKey: .weight)
            known = try c.decodeIfPresent(Bool.self, forKey: .known)
            // Unlike the numbers, a note that is not a string is dropped, not fatal: it only explains.
            note = try? c.decode(String.self, forKey: .note)
            backend = try? c.decode(String.self, forKey: .backend)
            reading = try? c.decode(String.self, forKey: .reading)
        }
    }

    public init(from decoder: Decoder) throws {
        var out: [ScoreFactor] = []
        if var list = try? decoder.unkeyedContainer() {
            while !list.isAtEnd {
                // Advance past an element that does not decode, or the loop would never end.
                guard let raw = try? list.decode(Raw.self) else { _ = try? list.decode(Skip.self); continue }
                guard !raw.name.isEmpty, raw.value.isFinite else { continue }
                // No `known` means the signal's presence was not stated: not known.
                out.append(ScoreFactor(name: raw.name, value: raw.value, weight: raw.weight ?? 0, known: raw.known ?? false, note: raw.note,
                                       backend: raw.backend, reading: raw.reading))
            }
        }
        items = out.reduce(into: [ScoreFactor]()) { acc, f in
            if !acc.contains(where: { $0.name == f.name }) { acc.append(f) }
        }
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.unkeyedContainer()
        for f in items {
            try c.encode(Wire(name: f.name, value: f.value, weight: f.weight, known: f.known, note: f.note,
                              backend: f.backend, reading: f.reading))
        }
    }

    private struct Wire: Encodable {
        var name: String
        var value: Double
        var weight: Double
        var known: Bool
        var note: String?
        var backend: String?
        var reading: String?
    }
}

/// Decodes anything and keeps nothing, to step an unkeyed container past a bad element.
struct Skip: Decodable {
    init(from decoder: Decoder) throws {}
}

/// `semantic`: the posting's match against the knowledge base, with the
/// posting lines that matched best. Decoding never throws (see `ScoreFactors`).
public struct SemanticMatch: Codable, Equatable, Sendable {
    /// 0...1, calibrated; nil when Vani sent none or it is not a number.
    public var score: Double?
    public var evidence: [Evidence]

    public struct Evidence: Codable, Equatable, Sendable {
        /// A posting line (at most 120 characters). Untrusted board text.
        public var snippet: String
        /// The résumé chunk it matched: a role title or "skills".
        public var label: String
        public var cosine: Double

        public init(snippet: String, label: String, cosine: Double) {
            self.snippet = snippet
            self.label = label
            self.cosine = cosine
        }
    }

    public init(score: Double? = nil, evidence: [Evidence] = []) {
        self.score = score
        self.evidence = evidence
    }

    private enum CodingKeys: String, CodingKey { case score, evidence }

    private struct RawEvidence: Decodable {
        var snippet: String
        var label: String
        var cosine: Double
    }

    public init(from decoder: Decoder) throws {
        guard let c = try? decoder.container(keyedBy: CodingKeys.self) else {
            score = nil
            evidence = []
            return
        }
        let s = try? c.decodeIfPresent(Double.self, forKey: .score)
        score = s.flatMap { $0 }.flatMap { $0.isFinite ? $0 : nil }
        var out: [Evidence] = []
        if var list = try? c.nestedUnkeyedContainer(forKey: .evidence) {
            while !list.isAtEnd {
                guard let e = try? list.decode(RawEvidence.self) else { _ = try? list.decode(Skip.self); continue }
                let snippet = e.snippet.trimmingCharacters(in: .whitespacesAndNewlines)
                guard !snippet.isEmpty, e.cosine.isFinite else { continue }
                out.append(Evidence(snippet: snippet, label: e.label.trimmingCharacters(in: .whitespacesAndNewlines), cosine: e.cosine))
            }
        }
        evidence = out
    }

    /// How many matched lines the detail view shows.
    public static let shownEvidence = 3

    /// The strongest matches first (ties keep Vani's order), at most `shownEvidence`.
    public var topEvidence: [Evidence] {
        let ranked = evidence.enumerated().sorted { a, b in
            a.element.cosine != b.element.cosine ? a.element.cosine > b.element.cosine : a.offset < b.offset
        }
        return ranked.prefix(Self.shownEvidence).map(\.element)
    }
}

/// What the detail view lists to explain a job's place in the order.
public struct RankingBreakdown: Equatable, Sendable {
    public var factors: [ScoreFactor]
    public var semanticScore: Double?
    public var evidence: [SemanticMatch.Evidence]
    /// False when the posting has no age, so its freshness sits at the floor.
    public var ageKnown: Bool

    /// The blend's inputs in Score's order, then the multipliers, then any
    /// factor this app does not know, in the order Vani sent them.
    public var ordered: [ScoreFactor] {
        let rank = Dictionary(uniqueKeysWithValues: ScoreFactor.knownNames.enumerated().map { ($1, $0) })
        return factors.enumerated().sorted { a, b in
            let ra = rank[a.element.name] ?? ScoreFactor.knownNames.count
            let rb = rank[b.element.name] ?? ScoreFactor.knownNames.count
            return ra != rb ? ra < rb : a.offset < b.offset
        }.map(\.element)
    }
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
    /// Places in this selection before the location filters. The location picker
    /// reads it, so choosing a country does not hide the others. Optional so a
    /// vani from before it still decodes; Insights stats stay the fallback.
    public var placeChoices: PlaceChoices?

    enum CodingKeys: String, CodingKey {
        case view, query, jobs, total, shown, counts, torn, ledger, records, sort, insight, pending
        case placeChoices = "where"
    }

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

/// Places a `jobs list` selection names before `--state`, `--area`, `--country`
/// and `--remote`. "none" is a job that names no state, or no country.
public struct PlaceChoices: Codable, Equatable, Sendable {
    public var state: [String: Int]?
    public var country: [String: Int]?
    public var area: [JobsStats.AreaCount]?

    /// States by how many jobs name them, most first; "none" left out.
    public var statesByCount: [(code: String, n: Int)] {
        (state ?? [:]).filter { $0.key != "none" }
            .sorted { $0.value != $1.value ? $0.value > $1.value : $0.key < $1.key }
            .map { ($0.key, $0.value) }
    }

    /// Countries by how many jobs name them, most first; "none" left out.
    public var countriesByCount: [(name: String, n: Int)] {
        (country ?? [:]).filter { $0.key != "none" }
            .sorted { $0.value != $1.value ? $0.value > $1.value : $0.key < $1.key }
            .map { ($0.key, $0.value) }
    }
}

/// One checkbox in the Jobs location picker. `count` is how many roles in the
/// current search name the place, before the location filter. It is nil when
/// the place is selected but this search does not count it.
public struct PlacePickerRow: Equatable, Sendable, Identifiable {
    public var token: String
    public var title: String
    public var count: Int?
    public var id: String { token }
}

/// Which place checkboxes the location picker shows.
public enum PlacePicker {
    /// `ranked` is most-named first and does not include "none". A selected
    /// token the census omitted stays, so a chosen country never disappears.
    /// A search hides titles that do not contain it, except a selected row,
    /// which stays at the top. An empty or blank query shows the whole list.
    /// "none" is last when `noneTitle` is set.
    public static func rows(
        ranked: [(token: String, count: Int)],
        selected: Set<String>,
        query: String,
        noneTitle: String? = nil,
        noneCount: Int? = nil
    ) -> [PlacePickerRow] {
        var seen = Set<String>()
        var all: [PlacePickerRow] = []
        for item in ranked {
            let token = item.token
            guard !token.isEmpty, token != "none", seen.insert(token).inserted else { continue }
            all.append(PlacePickerRow(token: token, title: token, count: item.count))
        }
        for token in selected.sorted() where !token.isEmpty && token != "none" && seen.insert(token).inserted {
            all.append(PlacePickerRow(token: token, title: token, count: nil))
        }
        if let noneTitle {
            all.append(PlacePickerRow(token: "none", title: noneTitle, count: noneCount))
        }
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty else { return all }
        let pinned = all.filter { selected.contains($0.token) && !matches($0, q) }
        return pinned + all.filter { matches($0, q) }
    }

    private static func matches(_ row: PlacePickerRow, _ query: String) -> Bool {
        row.title.localizedStandardContains(query) || row.token.localizedStandardContains(query)
    }
}

/// Which roles an export writes: the first N of the list, or the selected ones.
public enum JobsExportScope: Equatable, Sendable {
    case top(Int)
    case keys([String])
}

/// What an export does with roles an earlier export wrote (`--already-exported`). Without one,
/// vani refuses an export that holds any, writing nothing.
public enum JobsAlreadyExported: String, Equatable, Sendable {
    /// Leave them out; a "Top N" export takes the next roles of the list not yet exported.
    case skip
    /// Write them again.
    case include
}

/// What `vani jobs export --json` wrote, or with `--dry-run` would write.
public struct JobsExport: Codable, Equatable, Sendable {
    public var path: String
    public var rows: Int
    /// Roles that matched before the size limit, or roles asked for by key.
    public var total: Int
    public var view: String
    public var sort: String
    /// Asked-for keys the list no longer holds (a run closed or dropped them); not in the file.
    public var missing: [String]
    /// Of the roles the export holds, or would hold if they were included, how many an earlier
    /// export wrote; the latest of those exports was at `lastExportedAt`, to `lastFile` (nil when
    /// vani no longer names that file). 0 from a vani that keeps no record.
    public var alreadyExported: Int
    public var lastExportedAt: Date?
    public var lastFile: String?
    /// Roles exported before that a `skip` export left out.
    public var skipped: Int

    private enum CodingKeys: String, CodingKey {
        case path, rows, total, view, sort, missing, skipped
        case alreadyExported = "already_exported", lastExportedAt = "last_exported_at", lastFile = "last_file"
    }

    public init(path: String, rows: Int, total: Int, view: String, sort: String, missing: [String] = [],
                alreadyExported: Int = 0, lastExportedAt: Date? = nil, lastFile: String? = nil, skipped: Int = 0) {
        self.path = path
        self.rows = rows
        self.total = total
        self.view = view
        self.sort = sort
        self.missing = missing
        self.alreadyExported = alreadyExported
        self.lastExportedAt = lastExportedAt
        self.lastFile = lastFile
        self.skipped = skipped
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = try c.decode(String.self, forKey: .path)
        rows = try c.decode(Int.self, forKey: .rows)
        total = try c.decode(Int.self, forKey: .total)
        view = try c.decode(String.self, forKey: .view)
        sort = try c.decode(String.self, forKey: .sort)
        missing = try c.decode([String].self, forKey: .missing)
        alreadyExported = try c.decodeIfPresent(Int.self, forKey: .alreadyExported) ?? 0
        lastExportedAt = try c.decodeIfPresent(Date.self, forKey: .lastExportedAt)
        lastFile = try c.decodeIfPresent(String.self, forKey: .lastFile).flatMap { $0.isEmpty ? nil : $0 }
        skipped = try c.decodeIfPresent(Int.self, forKey: .skipped) ?? 0
    }

    /// The one line the app shows once the file is written.
    public func summary(_ scope: JobsExportScope) -> String {
        let file = URL(fileURLWithPath: path).lastPathComponent
        let noun = rows == 1 ? "role" : "roles"
        var line: String
        switch scope {
        case .top:
            line = rows < total && skipped == 0
                ? "Saved the first \(rows.formatted()) of \(total.formatted()) roles to \(file)."
                : "Saved \(rows.formatted()) \(noun) to \(file)."
        case .keys:
            line = "Saved \(rows.formatted()) selected \(noun) to \(file)."
            if !missing.isEmpty {
                line += " \(missing.count.formatted()) \(missing.count == 1 ? "is" : "are") no longer in this list and \(missing.count == 1 ? "was" : "were") left out."
            }
        }
        if skipped > 0 {
            line += " Left out \(skipped.formatted()) \(skipped == 1 ? "role" : "roles") exported before."
        }
        return line
    }

    /// What to ask before exporting roles an earlier export wrote, read from a dry run's reply;
    /// nil when there are none.
    public func repeatPrompt(_ scope: JobsExportScope, in zone: TimeZone = .current) -> JobsExportRepeatPrompt? {
        guard alreadyExported > 0 else { return nil }
        let title = alreadyExported == rows
            ? (rows == 1 ? "This role was exported before" : "All \(rows.formatted()) of these roles were exported before")
            : "\(alreadyExported.formatted()) of these \(rows.formatted()) roles \(alreadyExported == 1 ? "was" : "were") exported before"
        var message = ""
        if let at = lastExportedAt {
            var style = Date.FormatStyle(date: .abbreviated, time: .shortened)
            style.timeZone = zone
            message = "Last exported \(at.formatted(style))"
            if let lastFile { message += " to \(URL(fileURLWithPath: lastFile).lastPathComponent)" }
            message += ". "
        }
        switch scope {
        case .top:
            message += "Skip them to export only roles not yet exported; the next roles in the list take their place."
        case .keys:
            message += "Skip them to export only the selected roles not yet exported."
        }
        return JobsExportRepeatPrompt(title: title, message: message)
    }

    /// The name the save panel suggests: view, size and day, e.g. "Vani jobs (new, top 100) 2026-10-27.xlsx".
    public static func suggestedName(view: JobView, _ scope: JobsExportScope, on day: Date, in zone: TimeZone = .current) -> String {
        let size: String
        switch scope {
        case let .top(n): size = "top \(n)"
        case let .keys(keys): size = "\(keys.count) selected"
        }
        let date = day.formatted(Date.ISO8601FormatStyle(timeZone: zone).year().month().day())
        return "Vani jobs (\(view.rawValue), \(size)) \(date).xlsx"
    }
}

/// The question an export asks before writing roles again. The answers: skip them (the default),
/// export them again, or cancel.
public struct JobsExportRepeatPrompt: Equatable, Sendable {
    public var title: String
    public var message: String
    public let skip = "Skip Exported Roles"
    public let include = "Export Them Again"
    public let cancel = "Cancel"
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
        total > 0 ? "· \(total) awaiting triage (see the \(JobView.pending.title) view)" : nil
    }

    /// The footer note's help: who waits, where to see them, and that
    /// `advance_per_run` is the whole advance pass, not this list's length.
    public var footerHelp: String {
        let top = companies.prefix(5).map { "\($0.company) \($0.n)" }.joined(separator: ", ")
        let lead: String
        let whereSeen: String
        if total == 1 {
            lead = "1 posting was read from its board but not triaged yet, so the ranked views do not list it"
            whereSeen = "The \(JobView.pending.title) view lists it, unranked."
        } else {
            lead = "\(total) postings were read from their boards but not triaged yet, so the ranked views do not list them"
            whereSeen = "The \(JobView.pending.title) view lists them, unranked."
        }
        return lead
            + (top.isEmpty ? "" : " (\(top))")
            + ". \(whereSeen) Each run advances at most the config's advance_per_run postings. That cap also scores and explains postings already past triage."
    }

    /// What an empty ranked view adds when postings wait: where to see them. Nil
    /// when nothing waits, and in the awaiting view itself.
    public func emptyListNote(in view: JobView) -> String? {
        guard view != .pending, total > 0 else { return nil }
        return "\(total) \(total == 1 ? "posting is" : "postings are") awaiting triage; see \(total == 1 ? "it" : "them"), unranked, in the \(JobView.pending.title) view."
    }

    /// What an empty search result adds when the search names postings that
    /// are still waiting, or nil when it names none. Seen from the awaiting
    /// view itself (`in`), a waiting match would already be listed, so it says nothing.
    public func emptySearchNote(query: String, in view: JobView = .new) -> String? {
        guard view != .pending, matching > 0, !query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return nil }
        return "\(matching) \(matching == 1 ? "posting" : "postings") matching “\(query)” \(matching == 1 ? "is" : "are") awaiting triage; see \(matching == 1 ? "it" : "them"), unranked, in the \(JobView.pending.title) view."
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
    /// The order a view opens in: New leads with the highest priority, the number each
    /// row's badge shows; the marked views keep the standard order, which lists them by
    /// when they were marked.
    public static func defaultSort(for view: JobView) -> JobSort {
        switch view {
        case .new: .priority
        // No scores to order by: newest first (vani falls back to it as well).
        case .pending: .fresh
        default: .standard
        }
    }

    /// The orders a view can be listed in. Awaiting-triage postings have no
    /// priority or relevance, so only the orders that need neither are offered.
    public static func options(for view: JobView) -> [JobSort] {
        view == .pending ? [.fresh, .company] : allCases
    }
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
    public var categories: Set<String> = []
    public var fits: Set<String> = []
    public var workModes: Set<String> = []
    public var sponsorships: Set<String> = []
    /// At most this many days old; nil is any age.
    public var maxAgeDays: Int?
    public var openOnly = false
    /// US state codes ("none": no state listed), areas as the location names
    /// them, countries as Vani names them ("United States", "Germany"; "none":
    /// no country listed), and remote as the board lists it (not triage's work mode).
    public var states: Set<String> = []
    public var areas: Set<String> = []
    public var countries: Set<String> = []
    public var remoteOnly = false
    /// Widens `states`/`areas`/`countries` to roles listed as remote too
    /// ("remote, or in Texas"). It filters nothing without a state, area or
    /// country, and `remoteOnly` overrides it, so it is sent to Vani only when
    /// it changes the selection.
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
        [!categories.isEmpty, !fits.isEmpty, !workModes.isEmpty, !sponsorships.isEmpty, maxAgeDays != nil, openOnly,
         !states.isEmpty, !areas.isEmpty, !countries.isEmpty, remoteOnly, widensToRemote,
         minSalary != nil, salaryListed, !seniorities.isEmpty, minRelevance != nil].filter { $0 }.count
    }

    /// `orRemote` with a state, area or country to widen and no `remoteOnly`:
    /// the only case Vani accepts --or-remote, and the only one where it does anything.
    public var widensToRemote: Bool {
        orRemote && !remoteOnly && (!states.isEmpty || !sendableCountries.isEmpty || areas.contains { !$0.contains(",") && !$0.trimmingCharacters(in: .whitespaces).isEmpty })
    }

    /// Country names Vani can take on one `--country` flag. A comma would split
    /// into two countries, so it is left out instead.
    var sendableCountries: [String] {
        countries.filter { !$0.contains(",") && !$0.trimmingCharacters(in: .whitespaces).isEmpty }.sorted()
    }

    /// Countries export will write, in the order `--country` sends them.
    /// "none" is spoken as "no country listed". A name with a comma is left
    /// out, because the flag would split it into two countries.
    public var exportCountryNames: [String] {
        sendableCountries.map { $0 == "none" ? "no country listed" : $0 }
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
        if !categories.isEmpty { a += ["--category", categories.sorted().joined(separator: ",")] }
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
        if !sendableCountries.isEmpty { a += ["--country", sendableCountries.joined(separator: ",")] }
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
    public var category: [String: Int]?
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
    /// Jobs per US state ("none": names no state), per country ("none": names
    /// no country), the most-named areas, and how many the board lists as
    /// remote, by where. Optional so a vani from before location parsing still decodes.
    public var state: [String: Int]?
    public var country: [String: Int]?
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
        case view, total, fit, category, sponsorship, seniority, basis, age, relevance, priority, companies, keywords, insight, torn
        case state, country, area, salary
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

    /// Countries by how many jobs name them, most first; "none" left out.
    public var countriesByCount: [(name: String, n: Int)] {
        (country ?? [:]).filter { $0.key != "none" }
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
    /// Days of age that halve a job's freshness bonus (`--half-life`); nil
    /// leaves it to Vani's default. Sent only when set, so a vani from before
    /// the flag keeps working until the setting is used.
    public let halfLifeDays: Double?
    let timeout: TimeInterval

    public init(vaniBinary: URL, configPath: String? = nil, stateDir: String? = nil, halfLifeDays: Double? = nil, timeout: TimeInterval = JobsClient.timeout) {
        self.vaniBinary = vaniBinary
        self.configPath = configPath
        self.stateDir = stateDir
        self.halfLifeDays = halfLifeDays
        self.timeout = timeout
    }

    /// jobinsight.DefaultScoreHalfLifeDays and MaxHalfLifeDays: Vani refuses a
    /// half-life outside (0, 365].
    public static let defaultHalfLifeDays = 10.0
    public static let maxHalfLifeDays = 365.0

    /// `--half-life` for a setting: nothing when it is unset, not usable, or
    /// already Vani's default.
    static func halfLifeArguments(_ days: Double?) -> [String] {
        guard let d = days, d.isFinite, d > 0, d <= maxHalfLifeDays, d != defaultHalfLifeDays else { return [] }
        return ["--half-life", String(format: "%g", d)]
    }

    public func list(_ view: JobView, query: String = "", sort: JobSort = .standard, filter: JobFilter = JobFilter(), limit: Int = 1000) throws -> JobsPage {
        try call(Self.listArguments(view, query: query, sort: sort, filter: filter, limit: limit, halfLifeDays: halfLifeDays))
    }

    /// Distributions over the selection a list with the same arguments shows.
    public func stats(_ view: JobView, query: String = "", filter: JobFilter = JobFilter()) throws -> JobsStats {
        try call(Self.statsArguments(view, query: query, filter: filter, halfLifeDays: halfLifeDays))
    }

    static func listArguments(_ view: JobView, query: String, sort: JobSort, filter: JobFilter, limit: Int, halfLifeDays: Double? = nil) -> [String] {
        var args = ["jobs", "list", "--json", "--view", view.rawValue, "--limit", String(limit)]
        if sort != .standard { args += ["--sort", sort.rawValue] }
        return args + halfLifeArguments(halfLifeDays) + selection(query, filter)
    }

    static func statsArguments(_ view: JobView, query: String, filter: JobFilter, halfLifeDays: Double? = nil) -> [String] {
        ["jobs", "stats", "--json", "--view", view.rawValue] + halfLifeArguments(halfLifeDays) + selection(query, filter)
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

    /// Set the stage of an applied job, or set it back to plain applied with nil. The mark stays applied.
    public func stage(_ key: String, _ stage: JobStage?) throws -> Job {
        try call(["jobs", "mark", "--json", key, stage?.rawValue ?? "unstage"])
    }

    /// Save the note; an empty note clears it.
    public func note(_ key: String, _ text: String) throws -> Job {
        guard text.utf8.count <= Self.maxNoteBytes else {
            throw BankError("A note can be at most \(Self.maxNoteBytes) bytes.")
        }
        return try call(["jobs", "note", "--json", key, text])
    }

    /// The Export menu's sizes; the largest is the most `vani jobs export --top` takes.
    public static let exportSizes = [100, 250, 500, 1000, 5000]
    public static var maxExportRows: Int { exportSizes.last! }

    /// Write roles to `url` as an Excel workbook (`vani jobs export`): the first N of the list these
    /// arguments show, in its order, or the roles `keys` names, in their order. `already` says what
    /// to do with roles an earlier export wrote; nil refuses to write when there are any (the
    /// error says how many), so pass what the owner chose after `exportCheck`.
    public func export(_ view: JobView, query: String = "", sort: JobSort = .standard, filter: JobFilter = JobFilter(),
                       _ scope: JobsExportScope, to url: URL, already: JobsAlreadyExported? = nil) throws -> JobsExport {
        try runExport(Self.exportArguments(view, query: query, sort: sort, filter: filter, scope, out: url.path,
                                           already: already, halfLifeDays: halfLifeDays), scope)
    }

    /// What `export` would write, without writing anything (`--dry-run`): above all, how many of
    /// those roles an earlier export wrote, to ask the owner about before saving.
    public func exportCheck(_ view: JobView, query: String = "", sort: JobSort = .standard, filter: JobFilter = JobFilter(),
                            _ scope: JobsExportScope) throws -> JobsExport {
        try runExport(Self.exportArguments(view, query: query, sort: sort, filter: filter, scope, out: nil,
                                           halfLifeDays: halfLifeDays, dryRun: true), scope)
    }

    private func runExport(_ args: [String], _ scope: JobsExportScope) throws -> JobsExport {
        var input: Data?
        if case let .keys(keys) = scope {
            guard !keys.isEmpty else { throw BankError("Select at least one role to export.") }
            guard keys.count <= Self.maxExportRows else {
                throw BankError("At most \(Self.maxExportRows.formatted()) roles can be exported at once.")
            }
            input = Data((keys.joined(separator: "\n") + "\n").utf8)
        }
        return try VaniProcess.call(vaniBinary, args, configPath: configPath, stateDir: stateDir, input: input,
                                    // "missing" can name every asked-for key: the list's bound, not a small one.
                                    timeout: timeout, maxOutputBytes: Self.maxOutputBytes, decoder: Self.decoder,
                                    tooOld: "this vani is too old to export jobs; rebuild the app")
    }

    /// `out` nil is vani's default file, for a dry run that writes none.
    static func exportArguments(_ view: JobView, query: String, sort: JobSort, filter: JobFilter, _ scope: JobsExportScope,
                                out: String?, already: JobsAlreadyExported? = nil, halfLifeDays: Double? = nil,
                                dryRun: Bool = false) -> [String] {
        var args = ["jobs", "export", "--json", "--view", view.rawValue]
        // "--out=": a path is never read as a flag.
        if let out { args.append("--out=" + out) }
        switch scope {
        case let .top(n): args += ["--top", String(n)]
        case .keys: args.append("--keys-stdin")
        }
        if let already { args += ["--already-exported", already.rawValue] }
        if dryRun { args.append("--dry-run") }
        if sort != .standard { args += ["--sort", sort.rawValue] }
        return args + halfLifeArguments(halfLifeDays) + selection(query, filter)
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
