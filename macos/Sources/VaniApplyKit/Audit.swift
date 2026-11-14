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

/// What kept a "maybe" or a "no" from a yes: the values `vani audit answer
/// --reason` takes (eval.WouldApplyReasons). Only those two verdicts carry one.
public enum AuditReason: String, CaseIterable, Sendable {
    case level, domain, location
    case employerType = "employer_type"
    case other

    public var title: String {
        switch self {
        case .level: "Level"
        case .domain: "Domain"
        case .location: "Location"
        case .employerType: "Employer type"
        case .other: "Other"
        }
    }

    /// One digit each, 1 to 5, pressed with no modifier.
    public var shortcut: Character {
        Character(String((Self.allCases.firstIndex(of: self) ?? 0) + 1))
    }

    /// Whether verdict may carry a reason.
    public static func allowed(for verdict: AuditVerdict) -> Bool { verdict == .maybe || verdict == .no }
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
    /// Each overlay vani could not draw this week, and why (no teacher
    /// readings yet, a taste column that does not pass, a cell the kill rule
    /// turned off). Empty when every overlay ran; a vani too old to send it
    /// decodes as empty.
    public var skipped: [String]

    public init(seed: String, blind: Bool, picks: [AuditPick], skipped: [String] = []) {
        self.seed = seed
        self.blind = blind
        self.picks = picks
        self.skipped = skipped
    }

    private enum CodingKeys: String, CodingKey { case seed, blind, picks, skipped }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        seed = try c.decode(String.self, forKey: .seed)
        blind = try c.decode(Bool.self, forKey: .blind)
        picks = try c.decode([AuditPick].self, forKey: .picks)
        skipped = try c.decodeIfPresent([String].self, forKey: .skipped) ?? []
    }
}

/// `vani audit answer --json`: the label that was written.
public struct AuditAnswered: Decodable, Equatable, Sendable {
    public var key: String
    public var verdict: String
    public var reason: String?
    public var stratum: String
    public var pSelect: Double

    enum CodingKeys: String, CodingKey {
        case key, verdict, reason, stratum
        case pSelect = "p_select"
    }
}

/// The overlay picks added to a draw. Every overlay is on in vani by default;
/// nil leaves one at vani's own default, and a number (0 included, which
/// turns it off) is sent. The random draw is the same whatever they are; their
/// answers are counted apart (cmd/vani/auditcmd.go). The app sends
/// `--soft-overlays`, so an overlay vani cannot run yet is skipped and named in
/// the draw's `skipped`, never an error that hides the cards.
public struct AuditOverlays: Equatable, Sendable {
    /// "Teacher wants" disagreement picks (`--disagree`).
    public var disagree: Int?
    /// Postings nearest the owner's yes answers (`--yes-neighbours`); vani
    /// draws them only once its taste column passes.
    public var neighbours: Int?
    /// Random engineering-titled postings (`--engineering`).
    public var engineering: Int?

    public init(disagree: Int? = nil, neighbours: Int? = nil, engineering: Int? = nil) {
        self.disagree = disagree
        self.neighbours = neighbours
        self.engineering = engineering
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

    /// The most "teacher wants" picks the app asks for: vani's own cap is twice the
    /// random scored and dropped places, 2 x (5 + 10) with the default quotas.
    public static let maxDisagree = 30
    /// The most yes-neighbour picks; vani counts them with the disagreement
    /// picks under the same cap, so the two together stay within maxDisagree.
    public static let maxNeighbours = 30
    /// The most engineering picks: vani's own bound on a quota. They count on
    /// neither side of the cap.
    public static let maxEngineering = 100

    /// vani's own default overlay quotas (cmd/vani/auditcmd.go: defaultDisagree,
    /// defaultYesNeighbours, defaultEngineering), shown on a stepper the owner
    /// has not set. The app sends no flag for those, so vani's default applies.
    public static let defaultDisagree = 5
    public static let defaultNeighbours = 3
    public static let defaultEngineering = 3

    /// This week's draw, with the model's readings left out of it (`--blind`)
    /// and the given overlay picks; with none given, vani's defaults.
    public func draw(_ overlays: AuditOverlays = AuditOverlays()) throws -> AuditDraw {
        let d: AuditDraw = try call(try Self.drawArguments(overlays))
        guard d.picks.count <= Self.maxPicks else { throw BankError("vani audit drew \(d.picks.count) postings; this app shows at most \(Self.maxPicks)") }
        for p in d.picks { _ = try Self.answerArguments(p, .skip) } // a malformed pick fails here, not mid-session
        return d
    }

    /// The card for a pick, through `vani jobs show`.
    public func card(_ pick: AuditPick) throws -> AuditCard {
        AuditCard(job: try jobs.show(pick.key))
    }

    /// Writes the verdict as a would_apply label carrying the pick's stratum and
    /// p_select, with what kept a maybe or a no from a yes when given.
    @discardableResult
    public func answer(_ pick: AuditPick, _ verdict: AuditVerdict, reason: AuditReason? = nil) throws -> AuditAnswered {
        try call(try Self.answerArguments(pick, verdict, reason: reason))
    }

    /// One-tap "Would you apply?" on a posting the owner is looking at: vani takes the stratum
    /// and p_select from the posting's newest sampled impression, else records the label as manual.
    /// A maybe or a no may carry what kept it from a yes, as in the Audit tab.
    @discardableResult
    public func answerShown(key: String, _ verdict: AuditVerdict, reason: AuditReason? = nil) throws -> AuditAnswered {
        try call(try Self.shownAnswerArguments(key: key, verdict: verdict, reason: reason))
    }

    /// vani refuses a second answer for a key with this phrase in its error.
    public static func isAlreadyAnswered(_ error: Error) -> Bool {
        String(describing: error).contains("already answered")
    }

    /// `--shown` takes the stratum and p_select itself, so --source, --p-select and --seed are never sent.
    static func shownAnswerArguments(key: String, verdict: AuditVerdict, reason: AuditReason? = nil) throws -> [String] {
        guard !key.isEmpty, key.utf8.count <= maxKeyBytes, !key.contains("\0") else {
            throw BankError("this posting has no usable key")
        }
        var args = ["audit", "answer", "--json", "--shown", "--key=\(key)", "--verdict=\(verdict.rawValue)"]
        if let reason {
            guard AuditReason.allowed(for: verdict) else {
                throw BankError("a reason goes with a maybe or a no, not with \(verdict.title)")
            }
            args.append("--reason=\(reason.rawValue)")
        }
        return args
    }

    /// `--soft-overlays` always, then each overlay the owner set, 0 included;
    /// one left nil is not sent, so vani's default applies.
    static func drawArguments(_ o: AuditOverlays) throws -> [String] {
        let disagree = o.disagree ?? defaultDisagree, neighbours = o.neighbours ?? defaultNeighbours
        let engineering = o.engineering ?? defaultEngineering
        guard (0...maxDisagree).contains(disagree) else {
            throw BankError("the disagreement picks must be within 0 to \(maxDisagree), got \(disagree)")
        }
        guard (0...maxNeighbours).contains(neighbours) else {
            throw BankError("the yes-neighbour picks must be within 0 to \(maxNeighbours), got \(neighbours)")
        }
        guard disagree + neighbours <= maxDisagree else {
            throw BankError("the disagreement and yes-neighbour picks together must be at most \(maxDisagree), got \(disagree + neighbours)")
        }
        guard (0...maxEngineering).contains(engineering) else {
            throw BankError("the engineering picks must be within 0 to \(maxEngineering), got \(engineering)")
        }
        var args = ["audit", "--json", "--blind", "--soft-overlays"]
        if let n = o.disagree { args.append("--disagree=\(n)") }
        if let n = o.neighbours { args.append("--yes-neighbours=\(n)") }
        if let n = o.engineering { args.append("--engineering=\(n)") }
        return args
    }

    /// `=` forms, so a key that begins with a dash can never be read as a flag.
    /// A reason goes only with maybe or no, as vani requires.
    static func answerArguments(_ pick: AuditPick, _ verdict: AuditVerdict, reason: AuditReason?) throws -> [String] {
        var args = try answerArguments(pick, verdict)
        if let reason {
            guard AuditReason.allowed(for: verdict) else {
                throw BankError("a reason goes with a maybe or a no, not with \(verdict.title)")
            }
            args.append("--reason=\(reason.rawValue)")
        }
        return args
    }

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
