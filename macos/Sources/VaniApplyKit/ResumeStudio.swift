import Foundation

// Resume Studio's client: a job's tailored resume, kept as numbered versions by `vani jobs resume`
// (cmd/vani/jobsresume*.go, the Go half; docs/RUNTIME.md "Resume Studio" is the contract). Nothing
// here picks a bullet, asks a model, prices a call or checks a bullet against the knowledge base:
// the app runs the command, decodes its one JSON reply, and words it.
//
//   build [--dry-run] [--allow-base] KEY   Opus picks the bullets (spends); --dry-run only estimates
//   render [--fit] [--from vN] KEY         the owner's edit on stdin, no model
//   show [--no-check] KEY [vN]             every version, whether the bank still vouches for it
//   use KEY vN                             check a version afresh, then bind it for applying
//   revalidate KEY vN                      drop what went stale, rebuild, save (no model)
//
// Replies decode strictly where a missing field would turn "did not run" into "passed" (a version's
// four check states, `use`'s five fields, a dry run's estimate) and leniently everywhere else
// (unknown fields are ignored). A reply this app cannot read is an error shown as such.

// MARK: - Replies

/// Any JSON value, for the parts of a reply the editor (the Studio window) reads and this layer does not:
/// a version's `selection` and the editor's `pool`.
public enum ResumeJSON: Codable, Equatable, Sendable {
    case null
    case bool(Bool)
    case number(Double)
    case string(String)
    case array([ResumeJSON])
    case object([String: ResumeJSON])

    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null }
        else if let v = try? c.decode(Bool.self) { self = .bool(v) }
        else if let v = try? c.decode(Double.self) { self = .number(v) }
        else if let v = try? c.decode(String.self) { self = .string(v) }
        else if let v = try? c.decode([ResumeJSON].self) { self = .array(v) }
        else { self = .object(try c.decode([String: ResumeJSON].self)) }
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .null: try c.encodeNil()
        case let .bool(v): try c.encode(v)
        case let .number(v): try c.encode(v)
        case let .string(v): try c.encode(v)
        case let .array(v): try c.encode(v)
        case let .object(v): try c.encode(v)
        }
    }
}

/// What a build's model turns cost. `complete` false means a turn's cost was unknown, so `usd` is a
/// lower bound; unknown is never zero.
public struct ResumeCost: Decodable, Equatable, Sendable {
    public var usd: Double?
    public var complete: Bool
    public var turns: Int

    public init(usd: Double? = nil, complete: Bool = false, turns: Int = 0) {
        self.usd = usd
        self.complete = complete
        self.turns = turns
    }

    private enum CodingKeys: String, CodingKey { case usd, complete, turns }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        usd = (try? c.decode(Double.self, forKey: .usd)).flatMap { $0.isFinite && $0 >= 0 ? $0 : nil }
        complete = (try? c.decode(Bool.self, forKey: .complete)) ?? false
        turns = (try? c.decode(Int.self, forKey: .turns)).flatMap { $0 >= 0 ? $0 : nil } ?? 0
    }
}

/// A version's four check states, as vani wrote them. Every one is a non-empty string (vani never
/// writes "", so a check that did not run never reads as one that passed); a value this app does not
/// know is kept as it is and worded as unrecognised, never as a pass.
public struct ResumeChecks: Decodable, Equatable, Sendable {
    public var eligibility: String
    public var lint: String
    public var fit: String
    public var figures: String

    public init(eligibility: String, lint: String, fit: String, figures: String) {
        self.eligibility = eligibility
        self.lint = lint
        self.fit = fit
        self.figures = figures
    }

    private enum CodingKeys: String, CodingKey { case eligibility, lint, fit, figures }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        func state(_ k: CodingKeys) throws -> String {
            let s = try c.decode(String.self, forKey: k)
            guard !s.isEmpty else {
                throw DecodingError.dataCorruptedError(forKey: k, in: c, debugDescription: "a check state is empty")
            }
            return s
        }
        eligibility = try state(.eligibility)
        lint = try state(.lint)
        fit = try state(.fit)
        figures = try state(.figures)
    }
}

/// One bullet on a page and the text it was chosen for.
public struct ResumeBulletRef: Decodable, Equatable, Sendable {
    public var id: String
    public var textHash: String

    public init(id: String, textHash: String) {
        self.id = id
        self.textHash = textHash
    }

    private enum CodingKeys: String, CodingKey {
        case id
        case textHash = "text_hash"
    }
}

/// A version's fresh verdict: is what it prints still vouched for by the knowledge base?
/// `unknown` means the check could not run, and is never `current`.
public struct ResumeEligibility: Decodable, Equatable, Sendable {
    public var state: String
    public var reworded: [String]
    public var gone: [String]
    public var ineligible: [String]
    public var inputsChanged: [String]
    public var why: String?

    public init(state: String, reworded: [String] = [], gone: [String] = [], ineligible: [String] = [],
                inputsChanged: [String] = [], why: String? = nil) {
        self.state = state
        self.reworded = reworded
        self.gone = gone
        self.ineligible = ineligible
        self.inputsChanged = inputsChanged
        self.why = why
    }

    private enum CodingKeys: String, CodingKey {
        case state, reworded, gone, ineligible, why
        case inputsChanged = "inputs_changed"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        state = try c.decode(String.self, forKey: .state)
        reworded = (try? c.decode([String].self, forKey: .reworded)) ?? []
        gone = (try? c.decode([String].self, forKey: .gone)) ?? []
        ineligible = (try? c.decode([String].self, forKey: .ineligible)) ?? []
        inputsChanged = (try? c.decode([String].self, forKey: .inputsChanged)) ?? []
        why = try? c.decode(String.self, forKey: .why)
    }

    /// "2 reworded, 1 gone": the ids' counts, no text. Nil when no bullet is named.
    public var summary: String? {
        let parts = [(reworded.count, "reworded"), (gone.count, "gone"), (ineligible.count, "no longer eligible")]
            .filter { $0.0 > 0 }.map { "\($0.0) \($0.1)" }
        return parts.isEmpty ? nil : parts.joined(separator: ", ")
    }

    public var staleIDs: [String] { reworded + gone + ineligible }
}

/// One version of a job's resume (`resumestore.Meta` plus what `show` adds). `selection` and the
/// per-version `eligibility` are present only in `show`.
public struct ResumeVersion: Decodable, Equatable, Sendable {
    public var version: Int
    public var parent: Int
    /// "tailored", "base", "edited" or "revalidated".
    public var source: String
    public var reason: String?
    public var model: String?
    public var cost: ResumeCost?
    public var checks: ResumeChecks
    /// 0: not fitted, so unknown.
    public var pages: Int
    public var notes: [String]
    public var unverifiedDropped: [String]
    public var keywords: [String]
    public var bullets: [ResumeBulletRef]
    public var pdfSHA256: String?
    public var created: Date
    public var selection: ResumeJSON?
    public var current: Bool
    public var used: Bool
    public var eligibility: ResumeEligibility?
    /// Absolute path of the version's PDF (0600), when it has one: `show` names it for every version
    /// that has a PDF, and so does a reply that saved one.
    public var pdfPath: String?
    /// Bullet id -> rendered lines, from rkb's fit report: present only for a version that was fitted.
    public var fitLines: [String: Int]

    /// The version has a PDF (a render without `--fit` has none).
    public var hasPDF: Bool { !(pdfSHA256 ?? "").isEmpty }

    private enum CodingKeys: String, CodingKey {
        case version, parent, source, reason, model, cost, checks, pages, notes, bullets, created, selection, current, used, eligibility
        case pdfPath = "pdf_path", fitLines = "fit_lines"
        case unverifiedDropped = "unverified_dropped"
        case keywords = "jd_keywords"
        case pdfSHA256 = "pdf_sha256"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        version = try c.decode(Int.self, forKey: .version)
        guard version >= 1 else {
            throw DecodingError.dataCorruptedError(forKey: .version, in: c, debugDescription: "a version is numbered from 1")
        }
        parent = (try? c.decode(Int.self, forKey: .parent)) ?? 0
        source = try c.decode(String.self, forKey: .source)
        guard !source.isEmpty else {
            throw DecodingError.dataCorruptedError(forKey: .source, in: c, debugDescription: "a version has a source")
        }
        reason = try? c.decode(String.self, forKey: .reason)
        model = try? c.decode(String.self, forKey: .model)
        cost = try? c.decode(ResumeCost.self, forKey: .cost)
        checks = try c.decode(ResumeChecks.self, forKey: .checks)
        pages = (try? c.decode(Int.self, forKey: .pages)).flatMap { $0 >= 0 ? $0 : nil } ?? 0
        notes = (try? c.decode([String].self, forKey: .notes)) ?? []
        unverifiedDropped = (try? c.decode([String].self, forKey: .unverifiedDropped)) ?? []
        keywords = (try? c.decode([String].self, forKey: .keywords)) ?? []
        bullets = (try? c.decode([ResumeBulletRef].self, forKey: .bullets)) ?? []
        pdfSHA256 = try? c.decode(String.self, forKey: .pdfSHA256)
        pdfPath = (try? c.decode(String.self, forKey: .pdfPath)).flatMap { $0.isEmpty ? nil : $0 }
        // A count outside what rkb can report is not a measurement: left out, never clamped.
        fitLines = ((try? c.decode([String: Int].self, forKey: .fitLines)) ?? [:]).filter { $0.value >= 1 && $0.value <= 50 }
        created = try c.decode(Date.self, forKey: .created)
        selection = try? c.decode(ResumeJSON.self, forKey: .selection)
        current = (try? c.decode(Bool.self, forKey: .current)) ?? false
        used = (try? c.decode(Bool.self, forKey: .used)) ?? false
        if c.contains(.eligibility), (try? c.decodeNil(forKey: .eligibility)) != true {
            // A verdict this app cannot read is "unknown", never a pass and never absent.
            eligibility = (try? c.decode(ResumeEligibility.self, forKey: .eligibility))
                ?? ResumeEligibility(state: "unknown", why: "this app could not read the verdict")
        } else {
            eligibility = nil
        }
    }
}

/// The reply of a command that saved a version: `build`, `render --fit` and `revalidate`.
public struct ResumeSaved: Decodable, Equatable, Sendable {
    public var version: ResumeVersion
    /// The saved version is now the one the Studio shows. When it is not, `currentNote` says why.
    public var currentMoved: Bool
    public var currentNote: String?
    /// Absolute path of the version's PDF (0600), when it has one.
    public var pdfPath: String?
    /// The bullets a revalidation took off the page.
    public var droppedStale: [String]

    private enum CodingKeys: String, CodingKey {
        case currentMoved = "current_moved", currentNote = "current_note", pdfPath = "pdf_path", droppedStale = "dropped_stale"
    }

    public init(from decoder: Decoder) throws {
        version = try ResumeVersion(from: decoder)
        let c = try decoder.container(keyedBy: CodingKeys.self)
        currentMoved = try c.decode(Bool.self, forKey: .currentMoved)
        currentNote = try? c.decode(String.self, forKey: .currentNote)
        pdfPath = try? c.decode(String.self, forKey: .pdfPath)
        droppedStale = (try? c.decode([String].self, forKey: .droppedStale)) ?? []
    }
}

/// Today's llm cap. `spentUSD` and `remainingUSD` are nil when the spend could not be read: unknown
/// is never shown as zero.
public struct ResumeBudget: Decodable, Equatable, Sendable {
    public var dailyCapUSD: Double?
    public var spentUSD: Double?
    public var remainingUSD: Double?

    public init(dailyCapUSD: Double? = nil, spentUSD: Double? = nil, remainingUSD: Double? = nil) {
        self.dailyCapUSD = dailyCapUSD
        self.spentUSD = spentUSD
        self.remainingUSD = remainingUSD
    }

    private enum CodingKeys: String, CodingKey {
        case dailyCapUSD = "daily_cap_usd", spentUSD = "spent_usd", remainingUSD = "remaining_usd"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        func money(_ k: CodingKeys) -> Double? { (try? c.decode(Double.self, forKey: k)).flatMap { $0.isFinite && $0 >= 0 ? $0 : nil } }
        dailyCapUSD = money(.dailyCapUSD)
        spentUSD = money(.spentUSD)
        remainingUSD = money(.remainingUSD)
    }
}

/// What a build would cost at most: up to `turns` model turns.
public struct ResumeEstimate: Decodable, Equatable, Sendable {
    public var turns: Int
    public var inputTokens: Int
    public var maxOutputTokens: Int
    public var maxUSD: Double
    public var priceSource: String?
    /// What is left of today's cap is less than the first turn reserves at most. Advice only.
    public var capReached: Bool

    public init(turns: Int, inputTokens: Int, maxOutputTokens: Int, maxUSD: Double, priceSource: String? = nil, capReached: Bool = false) {
        self.turns = turns
        self.inputTokens = inputTokens
        self.maxOutputTokens = maxOutputTokens
        self.maxUSD = maxUSD
        self.priceSource = priceSource
        self.capReached = capReached
    }

    private enum CodingKeys: String, CodingKey {
        case turns, inputTokens = "input_tokens", maxOutputTokens = "max_output_tokens", maxUSD = "max_usd"
        case priceSource = "price_source", capReached = "cap_reached"
    }

    /// The cost and the turns are required: without them there is nothing to confirm.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        turns = try c.decode(Int.self, forKey: .turns)
        inputTokens = try c.decode(Int.self, forKey: .inputTokens)
        maxOutputTokens = try c.decode(Int.self, forKey: .maxOutputTokens)
        maxUSD = try c.decode(Double.self, forKey: .maxUSD)
        guard turns >= 1, inputTokens >= 0, maxOutputTokens >= 0, maxUSD.isFinite, maxUSD >= 0 else {
            throw DecodingError.dataCorruptedError(forKey: .maxUSD, in: c, debugDescription: "the estimate is not a cost")
        }
        priceSource = try? c.decode(String.self, forKey: .priceSource)
        capReached = (try? c.decode(Bool.self, forKey: .capReached)) ?? false
    }
}

/// `build --dry-run`: whether Opus can run and what a build would cost at most. Spends nothing.
public struct ResumeDryRun: Decodable, Equatable, Sendable {
    public var key: String?
    public var model: String?
    /// A build would run its model turn. When not, `noModel` says why and a build is refused
    /// (`no_model`) unless `--allow-base`.
    public var opusUsable: Bool
    public var noModel: String?
    public var current: Int
    public var budget: ResumeBudget
    public var estimate: ResumeEstimate?

    public init(key: String? = nil, model: String? = nil, opusUsable: Bool, noModel: String? = nil, current: Int = 0,
                budget: ResumeBudget = ResumeBudget(), estimate: ResumeEstimate? = nil) {
        self.key = key
        self.model = model
        self.opusUsable = opusUsable
        self.noModel = noModel
        self.current = current
        self.budget = budget
        self.estimate = estimate
    }

    private enum CodingKeys: String, CodingKey {
        case key, model, current, budget, estimate
        case opusUsable = "opus_usable", noModel = "no_model"
    }

    /// `opus_usable` is required, and a usable Opus must come with its estimate: a cost this app
    /// cannot read is not a cost to confirm.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        key = try? c.decode(String.self, forKey: .key)
        model = try? c.decode(String.self, forKey: .model)
        opusUsable = try c.decode(Bool.self, forKey: .opusUsable)
        noModel = try? c.decode(String.self, forKey: .noModel)
        current = (try? c.decode(Int.self, forKey: .current)).flatMap { $0 >= 0 ? $0 : nil } ?? 0
        budget = (try? c.decode(ResumeBudget.self, forKey: .budget)) ?? ResumeBudget()
        if c.contains(.estimate) {
            estimate = try c.decode(ResumeEstimate.self, forKey: .estimate)
        } else if opusUsable {
            throw DecodingError.keyNotFound(CodingKeys.estimate, .init(codingPath: c.codingPath, debugDescription: "a usable model has no estimate"))
        }
    }
}

/// `render` without `--fit`: a page to look at, never a version. The layout was not measured.
public struct ResumeRendered: Decodable, Equatable, Sendable {
    public var key: String?
    public var htmlPath: String
    public var checks: ResumeChecks
    public var notes: [String]
    public var keywords: [String]
    public var bullets: [ResumeBulletRef]

    private enum CodingKeys: String, CodingKey {
        case key, saved, checks, notes, keywords, bullets
        case htmlPath = "html_path"
    }

    /// `saved` must be false and `fit` must be "not_run": anything else is not what an unfitted render
    /// says, and is refused rather than shown as a fitted page.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        guard try c.decode(Bool.self, forKey: .saved) == false else {
            throw DecodingError.dataCorruptedError(forKey: .saved, in: c, debugDescription: "an unfitted render saved a version")
        }
        key = try? c.decode(String.self, forKey: .key)
        htmlPath = try c.decode(String.self, forKey: .htmlPath)
        checks = try c.decode(ResumeChecks.self, forKey: .checks)
        guard checks.fit == "not_run" else {
            throw DecodingError.dataCorruptedError(forKey: .checks, in: c, debugDescription: "an unfitted render claims a fit result")
        }
        notes = (try? c.decode([String].self, forKey: .notes)) ?? []
        keywords = (try? c.decode([String].self, forKey: .keywords)) ?? []
        bullets = (try? c.decode([ResumeBulletRef].self, forKey: .bullets)) ?? []
    }
}

/// A posting keyword, as `jobs show` reports it.
public struct ResumeKeyword: Decodable, Equatable, Sendable {
    public var term: String
    public var category: String?
    public var count: Int
    public var target: Bool
    public var gap: Bool

    private enum CodingKeys: String, CodingKey { case term, category = "cat", count = "n", target, gap }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        term = try c.decode(String.self, forKey: .term)
        category = try? c.decode(String.self, forKey: .category)
        count = (try? c.decode(Int.self, forKey: .count)) ?? 0
        target = (try? c.decode(Bool.self, forKey: .target)) ?? false
        gap = (try? c.decode(Bool.self, forKey: .gap)) ?? false
    }
}

/// `show`: every version of a job's resume. `versionsError` is set when the store could not be read
/// whole (the versions are then what read, never a claim that it is everything); `checkError` is why
/// the fresh check could not run (each version is then "unknown", and there is no pool).
public struct ResumeShown: Decodable, Equatable, Sendable {
    public var key: String
    /// False under `--no-check`: eligibility on each version is then only what was true when it was built.
    public var eligibilityChecked: Bool
    public var current: Int
    public var used: Int
    /// The version whose full `selection` is in `versions`.
    public var version: Int
    public var versions: [ResumeVersion]
    public var versionsError: String?
    public var checkError: String?
    public var pool: ResumeJSON?
    public var keywords: [ResumeKeyword]
    public var keywordsError: String?

    private enum CodingKeys: String, CodingKey {
        case key, current, used, version, versions, pool, keywords
        case eligibilityChecked = "eligibility_checked", versionsError = "versions_error"
        case checkError = "check_error", keywordsError = "keywords_error"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        key = try c.decode(String.self, forKey: .key)
        eligibilityChecked = try c.decode(Bool.self, forKey: .eligibilityChecked)
        current = (try? c.decode(Int.self, forKey: .current)) ?? 0
        used = (try? c.decode(Int.self, forKey: .used)) ?? 0
        version = (try? c.decode(Int.self, forKey: .version)) ?? 0
        versions = try c.decode([ResumeVersion].self, forKey: .versions)
        versionsError = try? c.decode(String.self, forKey: .versionsError)
        checkError = try? c.decode(String.self, forKey: .checkError)
        pool = try? c.decode(ResumeJSON.self, forKey: .pool)
        keywords = (try? c.decode([ResumeKeyword].self, forKey: .keywords)) ?? []
        keywordsError = try? c.decode(String.self, forKey: .keywordsError)
    }
}

/// `use`: the version now bound for applying, and where its PDF is (0600), under the name the page shows.
public struct ResumeUsed: Decodable, Equatable, Sendable {
    public var key: String
    public var version: Int
    public var pdfPath: String
    public var name: String
    public var pdfSHA256: String

    public init(key: String, version: Int, pdfPath: String, name: String, pdfSHA256: String) {
        self.key = key
        self.version = version
        self.pdfPath = pdfPath
        self.name = name
        self.pdfSHA256 = pdfSHA256
    }

    private enum CodingKeys: String, CodingKey {
        case key, version, name
        case pdfPath = "pdf_path", pdfSHA256 = "pdf_sha256"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        key = try c.decode(String.self, forKey: .key)
        version = try c.decode(Int.self, forKey: .version)
        pdfPath = try c.decode(String.self, forKey: .pdfPath)
        name = try c.decode(String.self, forKey: .name)
        pdfSHA256 = try c.decode(String.self, forKey: .pdfSHA256)
        guard version >= 1, !pdfPath.isEmpty, !pdfSHA256.isEmpty else {
            throw DecodingError.dataCorruptedError(forKey: .pdfPath, in: c, debugDescription: "use named no version or no PDF")
        }
    }
}

/// Why a step did not complete. `code` is vani's, or one of this app's own for what vani could not
/// say: `timeout`, `too_large`, `no_reply`, `unreadable_reply`, `bad_request` for a key or version the
/// app refuses to pass, and the runner's refusals (`account`, `keychain`, `settings`, `config`).
/// `cost` is set when model turns were paid for and nothing was saved; `attemptRecorded` says vani
/// wrote that down. `eligibility` is the verdict on a version a `stale` or `unchecked` refusal is about.
public struct ResumeFailure: Error, Equatable, Sendable {
    public var code: String
    public var message: String
    public var cost: ResumeCost?
    public var attemptRecorded: Bool
    public var eligibility: ResumeEligibility?

    public init(code: String, message: String, cost: ResumeCost? = nil, attemptRecorded: Bool = false,
                eligibility: ResumeEligibility? = nil) {
        self.code = code
        self.message = message
        self.cost = cost
        self.attemptRecorded = attemptRecorded
        self.eligibility = eligibility
    }
}

/// A step's result. `cancelled` is the owner stopping it: not a failure, and it has no message.
public enum ResumeOutcome<Value: Sendable>: Sendable {
    case success(Value)
    case failure(ResumeFailure)
    case cancelled
}

extension ResumeOutcome: Equatable where Value: Equatable {}

// MARK: - Client

/// Runs `vani jobs resume` for one posting through the draft runner, so a key reaches the child
/// exactly as it does for `vani draft` (through `anthropic-account` when the config names the
/// anthropic provider; this app reads, holds and passes none).
///
/// Model keys. `build` without `--dry-run` spends, so it takes keys. `build --dry-run` asks no model
/// either, but it decides whether Opus is usable by building the provider, which needs the credential
/// (vani takes keys from the environment only): without it every estimate would say `no_model`. So it
/// takes the same key path and still sends nothing. `render`, `show`, `use` and `revalidate` call no
/// model and read no key. `VANI_NO_KEYCHAIN=1` is set on every call.
///
/// Every call carries its own timeout, the Go deadline plus 15 s (DraftRunner's default is 150 s,
/// which a build would hit).
public struct ResumeStudioClient: Sendable {
    /// The app's ceiling per call: Go's deadline (cmd/vani/jobsresume.go, docs/RUNTIME.md) + 15 s.
    public struct Timeouts: Sendable, Equatable {
        public var build: TimeInterval = 375
        public var estimate: TimeInterval = 60
        public var render: TimeInterval = 115
        public var renderFit: TimeInterval = 275
        public var show: TimeInterval = 60
        public var showStored: TimeInterval = 20
        public var use: TimeInterval = 25
        public var revalidate: TimeInterval = 275

        public static let standard = Timeouts()
        public init() {}
    }

    /// `show` carries every version's bullets, the chosen version's selection and the editor's pool.
    static let maxShowBytes = 4 << 20
    static let maxReplyBytes = 512 * 1024
    static let maxKeyBytes = 512
    /// The edit on stdin is at most 64 KiB (resumestore.MaxSelectionBytes); the app does not send more.
    public static let maxEditBytes = 64 * 1024

    public let runner: DraftRunner
    public let timeouts: Timeouts

    public init(runner: DraftRunner, timeouts: Timeouts = .standard) {
        self.runner = runner
        self.timeouts = timeouts
    }

    /// One call: the argv after `vani`, its ceiling, whether it spends, and its reply cap.
    struct Call {
        var arguments: [String]
        var timeout: TimeInterval
        var usesModel: Bool
        var maxBytes: Int = ResumeStudioClient.maxReplyBytes
        var input: Data?
    }

    static func versionArgument(_ n: Int) -> String { "v\(n)" }

    func estimateCall(_ key: String) -> Call {
        Call(arguments: ["jobs", "resume", "build", "--json", "--dry-run", key], timeout: timeouts.estimate, usesModel: true)
    }

    func buildCall(_ key: String, allowBase: Bool) -> Call {
        Call(arguments: ["jobs", "resume", "build", "--json"] + (allowBase ? ["--allow-base"] : []) + [key],
             timeout: timeouts.build, usesModel: true)
    }

    func renderPreviewCall(_ key: String, edit: Data) -> Call {
        Call(arguments: ["jobs", "resume", "render", "--json", key], timeout: timeouts.render, usesModel: false, input: edit)
    }

    func renderFitCall(_ key: String, edit: Data, from: Int?) -> Call {
        Call(arguments: ["jobs", "resume", "render", "--json", "--fit"] + (from.map { ["--from", Self.versionArgument($0)] } ?? []) + [key],
             timeout: timeouts.renderFit, usesModel: false, input: edit)
    }

    func showCall(_ key: String, version: Int?, check: Bool) -> Call {
        Call(arguments: ["jobs", "resume", "show", "--json"] + (check ? [] : ["--no-check"]) + [key] + (version.map { [Self.versionArgument($0)] } ?? []),
             timeout: check ? timeouts.show : timeouts.showStored, usesModel: false, maxBytes: Self.maxShowBytes)
    }

    func useCall(_ key: String, version: Int) -> Call {
        Call(arguments: ["jobs", "resume", "use", "--json", key, Self.versionArgument(version)], timeout: timeouts.use, usesModel: false)
    }

    func revalidateCall(_ key: String, version: Int) -> Call {
        Call(arguments: ["jobs", "resume", "revalidate", "--json", key, Self.versionArgument(version)],
             timeout: timeouts.revalidate, usesModel: false)
    }

    /// A key vani could not have issued, or one that would read as a flag, is never passed; nor is a
    /// version that is not `v1` to `v9999`.
    static func refuse(key: String, version: Int? = nil) -> ResumeFailure? {
        guard !key.isEmpty, key.utf8.count <= maxKeyBytes, !key.hasPrefix("-"),
              !key.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }) else {
            return ResumeFailure(code: "bad_request", message: "this posting's key is not one Vani issued")
        }
        if let version, !(1...9999).contains(version) {
            return ResumeFailure(code: "bad_request", message: "v\(version) is not a version (v1 to v9999)")
        }
        return nil
    }

    // MARK: Verbs

    /// What a build would cost at most, and whether Opus can run. Asks no model and spends nothing.
    /// Blocks: call it off the main thread.
    public func estimate(_ key: String, cancellation: CancelFlag? = nil) -> ResumeOutcome<ResumeDryRun> {
        perform(key, estimateCall(key), cancellation: cancellation)
    }

    /// Build a tailored version with Opus and save it. Spends. Blocks for up to 375 s. With
    /// `allowBase`, a build that cannot run Opus saves the owner's base selection instead of being
    /// refused (it never displaces a job's current version); it is the owner's explicit choice.
    public func build(_ key: String, allowBase: Bool = false, cancellation: CancelFlag? = nil) -> ResumeOutcome<ResumeSaved> {
        perform(key, buildCall(key, allowBase: allowBase), cancellation: cancellation)
    }

    /// Render the owner's edit (JSON, at most 64 KiB) without fitting it: a page to look at, nothing saved.
    public func renderPreview(_ key: String, edit: Data, cancellation: CancelFlag? = nil) -> ResumeOutcome<ResumeRendered> {
        if let refusal = Self.refuse(edit: edit) { return .failure(refusal) }
        return perform(key, renderPreviewCall(key, edit: edit), cancellation: cancellation)
    }

    /// Render the owner's edit, fit it in Chrome and save it as a version made from `from` (default the
    /// current one). No model, so nothing is spent.
    public func renderFit(_ key: String, edit: Data, from: Int? = nil, cancellation: CancelFlag? = nil) -> ResumeOutcome<ResumeSaved> {
        if let refusal = Self.refuse(edit: edit) ?? Self.refuse(key: key, version: from) { return .failure(refusal) }
        return perform(key, renderFitCall(key, edit: edit, from: from), cancellation: cancellation)
    }

    /// Every version of the job's resume. `check: false` reads the stored files only (no fresh
    /// eligibility, no pool).
    public func show(_ key: String, version: Int? = nil, check: Bool = true, cancellation: CancelFlag? = nil) -> ResumeOutcome<ResumeShown> {
        if let refusal = Self.refuse(key: key, version: version) { return .failure(refusal) }
        return perform(key, showCall(key, version: version, check: check), cancellation: cancellation)
    }

    /// Check version `version` afresh and bind it for applying; a stale or unchecked one is refused.
    public func use(_ key: String, version: Int, cancellation: CancelFlag? = nil) -> ResumeOutcome<ResumeUsed> {
        if let refusal = Self.refuse(key: key, version: version) { return .failure(refusal) }
        return perform(key, useCall(key, version: version), cancellation: cancellation)
    }

    /// Drop what went stale from `version`, rebuild it with fit and save a "revalidated" version. No model.
    public func revalidate(_ key: String, version: Int, cancellation: CancelFlag? = nil) -> ResumeOutcome<ResumeSaved> {
        if let refusal = Self.refuse(key: key, version: version) { return .failure(refusal) }
        return perform(key, revalidateCall(key, version: version), cancellation: cancellation)
    }

    static func refuse(edit: Data) -> ResumeFailure? {
        edit.isEmpty || edit.count > maxEditBytes
            ? ResumeFailure(code: "bad_edit", message: edit.isEmpty ? "the edit is empty" : "the edit is over \(maxEditBytes >> 10) KiB")
            : nil
    }

    // MARK: Running and decoding

    private func perform<T: Decodable & Sendable>(_ key: String, _ call: Call, cancellation: CancelFlag?) -> ResumeOutcome<T> {
        if let refusal = Self.refuse(key: key) { return .failure(refusal) }
        let invocation = runner.invoke(arguments: call.arguments, input: call.input, timeout: call.timeout, usesModel: call.usesModel,
                                       maxOutputBytes: call.maxBytes, extraEnvironment: ["VANI_NO_KEYCHAIN": "1"],
                                       cancellation: cancellation)
        switch invocation {
        case let .failure(reply):
            if reply.code == "cancelled" { return .cancelled }
            return .failure(ResumeFailure(code: reply.code ?? "provider", message: reply.error ?? "vani could not be started"))
        case let .output(out):
            return decode(out, key: key, call: call)
        }
    }

    private struct ErrorReply: Decodable {
        var error: String?
        var code: String?
        var cost: ResumeCost?
        var attemptRecorded: Bool
        var eligibility: ResumeEligibility?

        private enum CodingKeys: String, CodingKey {
            case error, code, cost, eligibility
            case attemptRecorded = "attempt_recorded"
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            error = try? c.decode(String.self, forKey: .error)
            code = try? c.decode(String.self, forKey: .code)
            cost = try? c.decode(ResumeCost.self, forKey: .cost)
            attemptRecorded = (try? c.decode(Bool.self, forKey: .attemptRecorded)) ?? false
            eligibility = try? c.decode(ResumeEligibility.self, forKey: .eligibility)
        }
    }

    /// The reply's key, when it names one: a reply about another posting is refused.
    private struct Named: Decodable { var key: String? }

    /// vani's reply, whatever its exit status: its `{"error","code"}` object when it wrote one (the
    /// code, the cost of a paid failure and the verdict on a version are what the owner is shown),
    /// else the success shape.
    private func decode<T: Decodable & Sendable>(_ out: VaniProcess.Outcome, key: String, call: Call) -> ResumeOutcome<T> {
        if out.truncated {
            return .failure(ResumeFailure(code: "too_large", message: "vani's reply was larger than \(call.maxBytes >> 10) KB"))
        }
        let decoder = JobsClient.decoder
        if let failure = try? decoder.decode(ErrorReply.self, from: out.stdout), let message = failure.error {
            let code = failure.code.flatMap { $0.isEmpty ? nil : $0 } ?? "unknown"
            // vani ended itself on the SIGTERM this app sent: the owner's own stop, not an error...
            // unless model turns were paid for, which the owner is told (the attempt is on record).
            if code == "cancelled", failure.cost == nil { return .cancelled }
            return .failure(ResumeFailure(code: code, message: message, cost: failure.cost,
                                          attemptRecorded: failure.attemptRecorded, eligibility: failure.eligibility))
        }
        if out.status == 0 {
            do {
                let value = try decoder.decode(T.self, from: out.stdout)
                if let named = (try? decoder.decode(Named.self, from: out.stdout))?.key, named != key {
                    return .failure(ResumeFailure(code: "unreadable_reply", message: "vani answered for a different posting"))
                }
                return .success(value)
            } catch {
                return .failure(ResumeFailure(code: "unreadable_reply", message: "vani sent a reply this app cannot read",
                                              cost: (try? decoder.decode(ErrorReply.self, from: out.stdout))?.cost))
            }
        }
        if call.usesModel, let refusal = AnthropicAccount.refusal(in: out.stderr) {
            return .failure(ResumeFailure(code: "account", message: refusal))
        }
        let tooOld = out.status == 2 ? " (this vani has no resume command; rebuild the app)" : ""
        return .failure(ResumeFailure(code: "no_reply", message: "vani jobs resume exited \(out.status ?? -1) without a reply\(tooOld)"))
    }
}
