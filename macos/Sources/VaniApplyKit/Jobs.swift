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

    public var id: String { key }

    enum CodingKeys: String, CodingKey {
        case key, company, title, location, remote, department, url, status, closed, rank, coverage,
             explanation, missing, unsure, lca, employer, triage, mark, note, description
        case applyURL = "apply_url"
        case firstSeen = "first_seen"
        case postedAt = "posted_at"
        case markedAt = "marked_at"
        case noteAt = "note_at"
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

    public struct Reason: Equatable, Sendable {
        public enum Kind: String, Sendable { case fits = "Fits", gap = "Gap", check = "Check", other }
        public var kind: Kind
        public var text: String
    }

    /// The picked note ("Fits: X (91%). Gap: Y (40%). Check: Z (70%).") split
    /// into its reasons; any other text comes back as one `.other` reason.
    public var reasons: [Reason] {
        guard let note = explanation?.trimmingCharacters(in: .whitespaces), !note.isEmpty else { return [] }
        var out: [Reason] = []
        var rest = Substring(note)
        while let head = Reason.Kind.allLabelled.first(where: { rest.hasPrefix($0.rawValue + ": ") }) {
            rest = rest.dropFirst(head.rawValue.count + 2)
            let next = Reason.Kind.allLabelled
                .compactMap { rest.range(of: ". \($0.rawValue): ")?.lowerBound }
                .min()
            let end = next ?? rest.endIndex
            var text = String(rest[..<end]).trimmingCharacters(in: .whitespaces)
            if text.hasSuffix(".") { text.removeLast() }
            out.append(Reason(kind: head, text: text))
            rest = next.map { rest[rest.index($0, offsetBy: 2)...] } ?? ""
        }
        return out.isEmpty || !rest.isEmpty ? [Reason(kind: .other, text: note)] : out
    }
}

extension Job.Reason.Kind {
    static let allLabelled: [Self] = [.fits, .gap, .check]
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

    public func list(_ view: JobView, query: String = "", limit: Int = 1000) throws -> JobsPage {
        var args = ["jobs", "list", "--json", "--view", view.rawValue, "--limit", String(limit)]
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        if !q.isEmpty { args += ["--q", String(q.prefix(200))] }
        return try call(args)
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

    private struct Failure: Decodable { var error: String }

    private func call<T: Decodable>(_ args: [String]) throws -> T {
        let out = VaniProcess.run(vaniBinary, arguments: args,
                                  environment: VaniProcess.environment(configPath: configPath, stateDir: stateDir),
                                  timeout: timeout, maxOutputBytes: Self.maxOutputBytes)
        if let launchError = out.launchError { throw BankError(launchError) }
        if out.timedOut { throw BankError("vani \(args.prefix(2).joined(separator: " ")) did not finish within \(Int(timeout)) s") }
        if out.truncated { throw BankError("vani's reply was larger than \(Self.maxOutputBytes >> 20) MB") }
        let decoder = Self.decoder
        if out.status == 0, let value = try? decoder.decode(T.self, from: out.stdout) {
            return value
        }
        if let failure = try? decoder.decode(Failure.self, from: out.stdout) {
            throw BankError(failure.error)
        }
        throw BankError("vani \(args.prefix(2).joined(separator: " ")) exited \(out.status ?? -1) without a reply"
            + (out.status == 2 ? " (this vani is too old for Jobs; rebuild the app)" : ""))
    }

    /// Go writes RFC 3339 times with optional fractional seconds and an offset.
    static var decoder: JSONDecoder {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { dec in
            let s = try dec.singleValueContainer().decode(String.self)
            let frac = ISO8601DateFormatter()
            frac.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = frac.date(from: s) { return date }
            let plain = ISO8601DateFormatter()
            plain.formatOptions = [.withInternetDateTime]
            if let date = plain.date(from: s) { return date }
            throw DecodingError.dataCorrupted(.init(codingPath: dec.codingPath, debugDescription: "not an RFC 3339 time: \(s.prefix(40))"))
        }
        return d
    }
}
