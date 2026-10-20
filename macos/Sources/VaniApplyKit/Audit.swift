import Foundation

// The weekly Audit is a client of `vani audit` (cmd/vani/auditcmd.go): Go draws
// the sample and writes the labels; this side shows one card at a time and
// sends the owner's verdict back. The draw (`audit --json --blind`) holds keys,
// strata and inclusion probabilities and no posting text. A card's title,
// company and place come from the jobs row contract (`JobRow`, via `vani jobs
// show`), and nothing of the model's reading is shown: the stratum names say
// whether a posting was dropped, so the card never displays them either.

/// The owner's answer to "would you apply to this": the same four values
/// `vani audit answer --verdict` takes.
public enum AuditVerdict: String, CaseIterable, Sendable {
    case yes, maybe, no, skip

    /// One key each, pressed with no modifier.
    public var shortcut: Character {
        switch self {
        case .yes: "y"
        case .maybe: "m"
        case .no: "n"
        case .skip: "s"
        }
    }

    public var title: String {
        switch self {
        case .yes: "Yes"
        case .maybe: "Maybe"
        case .no: "No"
        case .skip: "Can't tell"
        }
    }

    public init?(shortcut: Character) {
        guard let v = Self.allCases.first(where: { $0.shortcut == Character(shortcut.lowercased()) }) else { return nil }
        self = v
    }
}

/// One drawn posting: its key and the stratum and inclusion probability the
/// answer's label must carry.
public struct AuditPick: Decodable, Equatable, Sendable, Identifiable {
    public var key: String
    public var stratum: String
    public var pSelect: Double

    public var id: String { key }

    public init(key: String, stratum: String, pSelect: Double) {
        self.key = key
        self.stratum = stratum
        self.pSelect = pSelect
    }

    enum CodingKeys: String, CodingKey {
        case key, stratum
        case pSelect = "p_select"
    }
}

/// `vani audit --json`. The strata summary is not read.
public struct AuditDraw: Decodable, Equatable, Sendable {
    public var seed: String
    public var blind: Bool
    public var picks: [AuditPick]

    public init(seed: String, blind: Bool, picks: [AuditPick]) {
        self.seed = seed
        self.blind = blind
        self.picks = picks
    }
}

/// `vani audit answer --json`: the label that was written.
public struct AuditAnswered: Decodable, Equatable, Sendable {
    public var key: String
    public var verdict: String
    public var stratum: String
    public var pSelect: Double

    enum CodingKeys: String, CodingKey {
        case key, verdict, stratum
        case pSelect = "p_select"
    }
}

/// What a card shows: the posting as the jobs row contract names it, and its
/// description as plain text. Nothing here is the model's reading.
public struct AuditCard: Equatable, Sendable {
    public var key: String
    public var title: String
    public var company: String
    public var location: String?
    public var description: String?

    /// At most this many characters of the description are shown.
    public static let maxDescription = 2500

    /// From a job `vani jobs show` returned: title, company and location from
    /// its `row` (the versioned contract), else from the job's own fields (a
    /// vani that predates the row).
    public init(job: Job) {
        key = job.key
        title = job.row?.title ?? job.title
        company = job.row?.company ?? job.company
        let loc = job.row?.location ?? job.location
        location = loc?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false ? loc : nil
        let d = job.description?.trimmingCharacters(in: .whitespacesAndNewlines)
        description = d.flatMap { $0.isEmpty ? nil : String($0.prefix(Self.maxDescription)) }
    }
}

/// Where the owner is in the week's draw: pure state, no I/O.
public struct AuditSession: Equatable, Sendable {
    public let picks: [AuditPick]
    public private(set) var index = 0
    public private(set) var answers: [(key: String, verdict: AuditVerdict)] = []

    public static func == (a: AuditSession, b: AuditSession) -> Bool {
        a.picks == b.picks && a.index == b.index
            && a.answers.map(\.key) == b.answers.map(\.key) && a.answers.map(\.verdict) == b.answers.map(\.verdict)
    }

    /// A duplicate key in the draw is shown once: an answer is one label per posting.
    public init(picks: [AuditPick]) {
        var seen = Set<String>()
        self.picks = picks.filter { seen.insert($0.key).inserted }
    }

    public var current: AuditPick? { index < picks.count ? picks[index] : nil }
    public var isDone: Bool { index >= picks.count }
    public var answered: Int { answers.count }
    /// "3 of 15", counting the card on screen.
    public var position: String { isDone ? "\(picks.count) of \(picks.count)" : "\(index + 1) of \(picks.count)" }

    /// Records the verdict for the card on screen and moves to the next. Call it
    /// only once `vani audit answer` succeeded, so a failed write never advances.
    /// Does nothing when the draw is finished.
    public mutating func record(_ verdict: AuditVerdict) {
        guard let pick = current else { return }
        answers.append((pick.key, verdict))
        index += 1
    }

    public func count(_ v: AuditVerdict) -> Int { answers.filter { $0.verdict == v }.count }
}

public struct AuditClient: Sendable {
    public static let timeout: TimeInterval = 30
    static let maxOutputBytes = 1 << 20
    /// Bounds a draw: `vani audit` allows 100 + 100; more is not a draw.
    public static let maxPicks = 200
    /// vani's own limit on --key.
    static let maxKeyBytes = 512

    public let jobs: JobsClient

    public init(jobs: JobsClient) { self.jobs = jobs }

    /// This week's draw, with the model's readings left out of it (`--blind`).
    public func draw() throws -> AuditDraw {
        let d: AuditDraw = try call(Self.drawArguments)
        guard d.picks.count <= Self.maxPicks else { throw BankError("vani audit drew \(d.picks.count) postings; this app shows at most \(Self.maxPicks)") }
        for p in d.picks { _ = try Self.answerArguments(p, .skip) } // a malformed pick fails here, not mid-session
        return d
    }

    /// The card for a pick, through `vani jobs show`.
    public func card(_ pick: AuditPick) throws -> AuditCard {
        AuditCard(job: try jobs.show(pick.key))
    }

    /// Writes the verdict as a would_apply label carrying the pick's stratum and p_select.
    @discardableResult
    public func answer(_ pick: AuditPick, _ verdict: AuditVerdict) throws -> AuditAnswered {
        try call(try Self.answerArguments(pick, verdict))
    }

    static let drawArguments = ["audit", "--json", "--blind"]

    /// `=` forms, so a key that begins with a dash can never be read as a flag.
    static func answerArguments(_ pick: AuditPick, _ verdict: AuditVerdict) throws -> [String] {
        guard !pick.key.isEmpty, pick.key.utf8.count <= maxKeyBytes, !pick.key.contains("\0") else {
            throw BankError("an audit pick has no usable key")
        }
        guard !pick.stratum.isEmpty, pick.stratum.utf8.count <= 128, !pick.stratum.contains("\0") else {
            throw BankError("the audit pick \(pick.key.prefix(60)) has no usable stratum")
        }
        guard pick.pSelect.isFinite, pick.pSelect > 0, pick.pSelect <= 1 else {
            throw BankError("the audit pick \(pick.key.prefix(60)) has an inclusion probability outside (0, 1]")
        }
        return ["audit", "answer", "--json", "--key=\(pick.key)", "--verdict=\(verdict.rawValue)",
                "--source=\(pick.stratum)", "--p-select=\(pick.pSelect)"]
    }

    private func call<T: Decodable>(_ args: [String]) throws -> T {
        try VaniProcess.call(jobs.vaniBinary, args, configPath: jobs.configPath, stateDir: jobs.stateDir,
                             timeout: jobs.timeout, maxOutputBytes: Self.maxOutputBytes,
                             tooOld: "this vani is too old for the Audit; rebuild the app")
    }
}
