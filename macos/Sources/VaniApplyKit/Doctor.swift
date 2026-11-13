import Foundation

// The pipeline doctor (cmd/vani/doctorcmd.go, internal/doctor): Claude, through the configured llm
// model (Opus), reads the run history and run logs for boards that read nothing without failing, and a
// seeded sample of dropped postings for drops made for the wrong reason. It is a report: no posting,
// rule or board changes. Everything goes through vani, never the state folder: `--dry-run --json`
// prices it and sends nothing, a real run spends at most `--budget-usd`, and `--last --json` is the
// newest report written (here or from a shell). The reply's shape is pinned by a golden file Go writes
// (Tests/VaniApplyKitTests/Fixtures/doctor/reply.json, from cmd/vani/doctorwire_test.go).
//
// Every string in a report but the counts came from a board, a posting or the model: views show it
// verbatim, never as Markdown.

private extension KeyedDecodingContainer {
    /// A list Go may write as null or leave out, read as empty.
    func list<T: Decodable>(_ key: Key) throws -> [T] { try decodeIfPresent([T].self, forKey: key) ?? [] }
    func int(_ key: Key) throws -> Int { try decodeIfPresent(Int.self, forKey: key) ?? 0 }
    func text(_ key: Key) throws -> String? {
        guard let s = try decodeIfPresent(String.self, forKey: key), !s.isEmpty else { return nil }
        return s
    }
}

public struct DoctorStratum: Decodable, Equatable, Sendable {
    public var name: String
    public var pool: Int
    public var taken: Int
}

/// A pattern the model saw across several boards or drops, with the fix it proposes.
public struct DoctorPattern: Decodable, Equatable, Sendable {
    public var title: String
    public var detail: String
    public var refs: [String]
    public var fix: String

    enum CodingKeys: String, CodingKey { case title, detail, refs, fix }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        title = try c.decode(String.self, forKey: .title)
        detail = try c.decodeIfPresent(String.self, forKey: .detail) ?? ""
        refs = try c.list(.refs)
        fix = try c.decodeIfPresent(String.self, forKey: .fix) ?? ""
    }
}

/// One of the two requests: how many it was sent, how many it judged, and why it failed if it did.
public struct DoctorPart: Decodable, Equatable, Sendable {
    public var asked: Int
    public var answered: Int
    public var error: String?
    public var patterns: [DoctorPattern]
    /// The reply as received, when it could not be read (kept so a paid answer is not lost).
    public var rawReply: String?

    enum CodingKeys: String, CodingKey {
        case asked, answered, error, patterns
        case rawReply = "raw_reply"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        asked = try c.int(.asked)
        answered = try c.int(.answered)
        error = try c.text(.error)
        patterns = try c.list(.patterns)
        rawReply = try c.text(.rawReply)
    }

    /// Everything sent was judged and nothing failed.
    public var complete: Bool { error == nil && answered >= asked }
}

public struct DoctorBoardFinding: Decodable, Equatable, Sendable, Identifiable {
    public var board: String
    public var verdict: String
    public var confidence: String
    public var evidence: String
    public var nextStep: String
    public var id: String { board }

    enum CodingKeys: String, CodingKey {
        case board, verdict, confidence, evidence
        case nextStep = "next_step"
    }
}

public struct DoctorDropFinding: Decodable, Equatable, Sendable, Identifiable {
    public var key: String
    public var verdict: String
    public var cause: String
    public var why: String
    public var id: String { key }
}

/// A posting the drop review was sent: its title and stratum, not its text.
public struct DoctorSampledDrop: Decodable, Equatable, Sendable {
    public var key: String
    public var stratum: String
    public var company: String
    public var title: String
    public var bodyMissing: Bool

    enum CodingKeys: String, CodingKey {
        case key, stratum, company, title
        case bodyMissing = "body_missing"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        key = try c.decode(String.self, forKey: .key)
        stratum = try c.decodeIfPresent(String.self, forKey: .stratum) ?? ""
        company = try c.decodeIfPresent(String.self, forKey: .company) ?? ""
        title = try c.decodeIfPresent(String.self, forKey: .title) ?? ""
        bodyMissing = try c.decodeIfPresent(Bool.self, forKey: .bodyMissing) ?? false
    }
}

/// What the run history says about one board, computed by vani before any model reads it.
public struct DoctorSignal: Decodable, Equatable, Sendable, Identifiable {
    public var board: String
    public var company: String?
    public var signals: [String]
    public var runs: Int
    public var runsFromLog: Int
    public var reads: Int
    public var zeroReads: Int
    public var failures: Int
    public var medianListed: Int
    public var latestListed: Int?
    /// Oldest first; -1 is a run that did not read the board.
    public var listedSeries: [Int]
    public var maxMissed: Int
    public var lastError: String?
    public var rawDetails: [String]
    public var id: String { board }

    enum CodingKeys: String, CodingKey {
        case board, company, signals, runs, reads, failures
        case runsFromLog = "runs_from_log"
        case zeroReads = "zero_reads"
        case medianListed = "median_listed"
        case latestListed = "latest_listed"
        case listedSeries = "listed_series"
        case maxMissed = "max_missed_in_one_run"
        case lastError = "last_error"
        case rawDetails = "raw_details"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        board = try c.decode(String.self, forKey: .board)
        company = try c.text(.company)
        signals = try c.list(.signals)
        runs = try c.int(.runs)
        runsFromLog = try c.int(.runsFromLog)
        reads = try c.int(.reads)
        zeroReads = try c.int(.zeroReads)
        failures = try c.int(.failures)
        medianListed = try c.int(.medianListed)
        latestListed = try c.decodeIfPresent(Int.self, forKey: .latestListed)
        listedSeries = try c.list(.listedSeries)
        maxMissed = try c.int(.maxMissed)
        lastError = try c.text(.lastError)
        rawDetails = try c.list(.rawDetails)
    }
}

public struct DoctorLogLine: Decodable, Equatable, Sendable {
    public var text: String
    public var count: Int
    public var logs: Int
}

/// The run logs' distinct trouble lines, and how much of the logs could not be read or was held back.
public struct DoctorLogDigest: Decodable, Equatable, Sendable {
    public var lines: [DoctorLogLine]
    public var logsRead: Int
    public var linesSeen: Int
    public var distinct: Int
    public var leftOut: Int
    /// Trouble lines withheld for an identifying word (never sent).
    public var withheld: Int
    public var tooLong: Int
    public var unreadable: [String]
    public var tailOnlyFor: [String]

    enum CodingKeys: String, CodingKey {
        case lines, distinct, withheld, unreadable
        case logsRead = "logs_read"
        case linesSeen = "lines_seen"
        case leftOut = "left_out"
        case tooLong = "too_long"
        case tailOnlyFor = "tail_only_for"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        lines = try c.list(.lines)
        logsRead = try c.int(.logsRead)
        linesSeen = try c.int(.linesSeen)
        distinct = try c.int(.distinct)
        leftOut = try c.int(.leftOut)
        withheld = try c.int(.withheld)
        tooLong = try c.int(.tooLong)
        unreadable = try c.list(.unreadable)
        tailOnlyFor = try c.list(.tailOnlyFor)
    }

    static let empty = DoctorLogDigest(lines: [], logsRead: 0, linesSeen: 0, distinct: 0, leftOut: 0, withheld: 0,
                                       tooLong: 0, unreadable: [], tailOnlyFor: [])

    init(lines: [DoctorLogLine], logsRead: Int, linesSeen: Int, distinct: Int, leftOut: Int, withheld: Int,
         tooLong: Int, unreadable: [String], tailOnlyFor: [String]) {
        self.lines = lines
        self.logsRead = logsRead
        self.linesSeen = linesSeen
        self.distinct = distinct
        self.leftOut = leftOut
        self.withheld = withheld
        self.tooLong = tooLong
        self.unreadable = unreadable
        self.tailOnlyFor = tailOnlyFor
    }
}

/// A written report (internal/doctor.Report).
public struct DoctorReport: Decodable, Equatable, Sendable {
    public var at: Date
    public var model: String
    public var through: Date?
    public var seed: String?
    public var boards: DoctorPart
    public var boardFindings: [DoctorBoardFinding]
    /// Boards that tripped a signal but were not judged.
    public var boardsNotExamined: [String]
    public var drops: DoctorPart
    public var dropFindings: [DoctorDropFinding]
    public var dropsNotExamined: [String]
    public var dropsSent: [DoctorSampledDrop]
    public var strata: [DoctorStratum]
    public var signals: [DoctorSignal]
    public var logs: DoctorLogDigest
    public var unrecordedBoardsNewestRun: Int
    public var recoveredFromLogs: Int
    public var runProblems: [String]
    public var runsWithUnnamedBoardsAndNoLog: Int

    enum CodingKeys: String, CodingKey {
        case at, model, through, seed, boards, drops, strata, signals, logs
        case boardFindings = "board_findings"
        case boardsNotExamined = "boards_not_examined"
        case dropFindings = "drop_findings"
        case dropsNotExamined = "drops_not_examined"
        case dropsSent = "drops_sent"
        case unrecordedBoardsNewestRun = "unrecorded_boards_newest_run"
        case recoveredFromLogs = "board_reads_recovered_from_logs"
        case runProblems = "run_problems"
        case runsWithUnnamedBoardsAndNoLog = "runs_with_unnamed_boards_and_no_log"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        at = try c.decode(Date.self, forKey: .at)
        model = try c.decode(String.self, forKey: .model)
        through = try c.decodeIfPresent(Date.self, forKey: .through)
        seed = try c.text(.seed)
        boards = try c.decode(DoctorPart.self, forKey: .boards)
        boardFindings = try c.list(.boardFindings)
        boardsNotExamined = try c.list(.boardsNotExamined)
        drops = try c.decode(DoctorPart.self, forKey: .drops)
        dropFindings = try c.list(.dropFindings)
        dropsNotExamined = try c.list(.dropsNotExamined)
        dropsSent = try c.list(.dropsSent)
        strata = try c.list(.strata)
        signals = try c.list(.signals)
        logs = try c.decodeIfPresent(DoctorLogDigest.self, forKey: .logs) ?? .empty
        unrecordedBoardsNewestRun = try c.int(.unrecordedBoardsNewestRun)
        recoveredFromLogs = try c.int(.recoveredFromLogs)
        runProblems = try c.list(.runProblems)
        runsWithUnnamedBoardsAndNoLog = try c.int(.runsWithUnnamedBoardsAndNoLog)
    }

    /// Both parts judged everything they were sent.
    public var complete: Bool { boards.complete && drops.complete }

    public func signal(for board: String) -> DoctorSignal? { signals.first { $0.board == board } }
    public func sampled(_ key: String) -> DoctorSampledDrop? { dropsSent.first { $0.key == key } }
}

/// `vani doctor --json`: the plan (a dry run's whole reply) and, for a real run, the report.
public struct DoctorReply: Decodable, Equatable, Sendable {
    public var model: String
    public var runsRead: Int
    public var runProblems: [String]
    public var boardsSignaled: Int
    public var recoveredFromLogs: Int
    /// The owner's identifying-words.txt is missing: only the built-in work-authorization terms are
    /// screened out of what is sent.
    public var identifyingWordsBuiltinOnly: Bool
    public var runsWithUnnamedBoardsAndNoLog: Int
    public var dropsSampled: Int
    /// The drop sample's seed; passed back so the run asks what was priced.
    public var seed: String?
    public var strata: [DoctorStratum]
    public var dropsLeftOut: [String: Int]
    public var worstCaseUSD: Double
    /// Each request's size line, by part ("boards", "drops").
    public var requests: [String: String]
    /// The written report's .md (real runs).
    public var report: String?
    /// Every refusal passed and the first request was about to go: false means nothing was spent.
    public var sent: Bool
    public var result: DoctorReport?
    public var error: String?
    public var code: String?

    enum CodingKeys: String, CodingKey {
        case model, seed, strata, requests, report, sent, result, error, code
        case runsRead = "runs_read"
        case runProblems = "run_problems"
        case boardsSignaled = "boards_signaled"
        case recoveredFromLogs = "board_reads_recovered_from_logs"
        case identifyingWordsBuiltinOnly = "identifying_words_builtin_only"
        case runsWithUnnamedBoardsAndNoLog = "runs_with_unnamed_boards_and_no_log"
        case dropsSampled = "drops_sampled"
        case dropsLeftOut = "drops_left_out"
        case worstCaseUSD = "worst_case_usd"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        model = try c.decode(String.self, forKey: .model)
        runsRead = try c.int(.runsRead)
        runProblems = try c.list(.runProblems)
        boardsSignaled = try c.int(.boardsSignaled)
        recoveredFromLogs = try c.int(.recoveredFromLogs)
        identifyingWordsBuiltinOnly = try c.decodeIfPresent(Bool.self, forKey: .identifyingWordsBuiltinOnly) ?? false
        runsWithUnnamedBoardsAndNoLog = try c.int(.runsWithUnnamedBoardsAndNoLog)
        dropsSampled = try c.int(.dropsSampled)
        seed = try c.text(.seed)
        strata = try c.list(.strata)
        dropsLeftOut = try c.decodeIfPresent([String: Int].self, forKey: .dropsLeftOut) ?? [:]
        worstCaseUSD = try c.decode(Double.self, forKey: .worstCaseUSD)
        requests = try c.decodeIfPresent([String: String].self, forKey: .requests) ?? [:]
        report = try c.text(.report)
        // Required: a vani too old to say whether it sent anything is not trusted to say it did not.
        sent = try c.decode(Bool.self, forKey: .sent)
        result = try c.decodeIfPresent(DoctorReport.self, forKey: .result)
        error = try c.text(.error)
        code = try c.text(.code)
    }

    /// A dry run that found nothing to ask: no board tripped a signal and no drop was sampled.
    public var nothingToAsk: Bool { requests.isEmpty }
}

/// `vani doctor --last --json`: the newest report written, or neither field.
public struct DoctorLast: Decodable, Equatable, Sendable {
    public var report: String?
    public var result: DoctorReport?

    enum CodingKeys: String, CodingKey { case report, result }

    public init(report: String?, result: DoctorReport?) {
        self.report = report
        self.result = result
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        report = try c.text(.report)
        result = try c.decodeIfPresent(DoctorReport.self, forKey: .result)
    }
}

/// How a real run ended.
public enum DoctorRunOutcome: Equatable, Sendable {
    /// vani exited 0: a complete review, or nothing to ask (`result` nil).
    case finished(DoctorReply)
    /// vani refused or failed. `reply` is vani's own when it wrote one: `sent` says whether anything
    /// may have been spent, and a partial review still carries `result` and `report`.
    case failed(RetriageFailure, reply: DoctorReply?)
    /// The owner stopped it. A request already sent was paid for.
    case cancelled
}

/// Runs `vani doctor`. The key reaches the child as it does for a re-triage (the draft runner, with
/// `VANI_NO_KEYCHAIN=1`); the estimate and `--last` start vani with no key.
public struct DoctorClient: Sendable {
    /// vani's doctorMaxBudgetUSD.
    public static let maxBudgetUSD = 20.0
    /// A dry run on the live state took 2.7 s (30 run logs, the ledger); this is the ceiling.
    public static let quickTimeout: TimeInterval = 120
    /// Two requests, each up to internal/doctor's ten-minute ceiling, plus reading the state.
    public static let runTimeout: TimeInterval = 22 * 60
    /// A reply carries the report: two raw replies of at most 64 KiB and bounded findings.
    static let maxReplyBytes = 8 << 20

    public let runner: DraftRunner
    let quickTimeout: TimeInterval
    let runTimeout: TimeInterval

    public init(runner: DraftRunner, quickTimeout: TimeInterval = DoctorClient.quickTimeout,
                runTimeout: TimeInterval = DoctorClient.runTimeout) {
        self.runner = runner
        self.quickTimeout = quickTimeout
        self.runTimeout = runTimeout
    }

    static func arguments(dryRun: Bool, budgetUSD: Double? = nil, seed: String? = nil) -> [String] {
        var a = ["doctor", "--json"]
        if dryRun { a.append("--dry-run") }
        if let budgetUSD { a += ["--budget-usd", String(format: "%.2f", budgetUSD)] }
        if let seed, !seed.isEmpty { a += ["--seed", seed] }
        return a
    }

    /// What a review would send and its worst case. Spends nothing. Blocks.
    public func plan(cancellation: CancelFlag? = nil) -> RetriageOutcome<DoctorReply> {
        let invocation = runner.invoke(arguments: Self.arguments(dryRun: true), timeout: quickTimeout, usesModel: false,
                                       maxOutputBytes: Self.maxReplyBytes, extraEnvironment: ["VANI_NO_KEYCHAIN": "1"],
                                       cancellation: cancellation)
        switch Self.read(invocation) {
        case .reply(let r, 0): return .success(r)
        case .reply(let r, _): return .failure(Self.failure(of: r))
        case let .failed(f): return f.code == "cancelled" ? .cancelled : .failure(f)
        }
    }

    /// Ask the model. Spends at most `budgetUSD` (vani refuses before sending when the worst case is
    /// over it, or over the daily llm cap's headroom). Blocks for up to `runTimeout`.
    public func run(budgetUSD: Double, seed: String?, cancellation: CancelFlag? = nil) -> DoctorRunOutcome {
        guard budgetUSD.isFinite, budgetUSD > 0, budgetUSD <= Self.maxBudgetUSD else {
            return .failed(RetriageFailure(code: "bad_request", message: "the budget must be more than $0 and at most $\(Int(Self.maxBudgetUSD))"), reply: nil)
        }
        let invocation = runner.invoke(arguments: Self.arguments(dryRun: false, budgetUSD: budgetUSD, seed: seed), timeout: runTimeout,
                                       usesModel: true, maxOutputBytes: Self.maxReplyBytes,
                                       extraEnvironment: ["VANI_NO_KEYCHAIN": "1"], cancellation: cancellation)
        switch Self.read(invocation) {
        case .reply(let r, 0): return .finished(r)
        case .reply(let r, _): return .failed(Self.failure(of: r), reply: r)
        case let .failed(f): return f.code == "cancelled" ? .cancelled : .failed(f, reply: nil)
        }
    }

    /// The newest report written. Spends nothing. Blocks.
    public func last(cancellation: CancelFlag? = nil) -> RetriageOutcome<DoctorLast> {
        let invocation = runner.invoke(arguments: ["doctor", "--last", "--json"], timeout: quickTimeout, usesModel: false,
                                       maxOutputBytes: Self.maxReplyBytes, extraEnvironment: ["VANI_NO_KEYCHAIN": "1"],
                                       cancellation: cancellation)
        switch invocation {
        case let .failure(reply):
            if reply.code == "cancelled" { return .cancelled }
            return .failure(RetriageFailure(code: reply.code ?? "provider", message: reply.error ?? "vani could not be started"))
        case let .output(out):
            if out.truncated { return .failure(Self.tooLarge) }
            if out.status == 0, let last = try? JobsClient.decoder.decode(DoctorLast.self, from: out.stdout) { return .success(last) }
            if let f = RetriageBatchClient.failure(in: out.stdout) { return .failure(f) }
            return .failure(Self.noReply(out.status, what: "the newest report"))
        }
    }

    private enum Read {
        case reply(DoctorReply, status: Int32)
        case failed(RetriageFailure)
    }

    /// The reply is read whole before any failure envelope, so a partial review's report is kept.
    private static func read(_ invocation: DraftRunner.Invocation) -> Read {
        switch invocation {
        case let .failure(reply):
            return .failed(RetriageFailure(code: reply.code ?? "provider", message: reply.error ?? "vani could not be started"))
        case let .output(out):
            if out.truncated { return .failed(tooLarge) }
            if let r = try? JobsClient.decoder.decode(DoctorReply.self, from: out.stdout) {
                if out.status != 0, r.error == nil, let refusal = AnthropicAccount.refusal(in: out.stderr) {
                    var r = r
                    r.error = refusal
                    r.code = "account"
                    return .reply(r, status: out.status ?? 1)
                }
                return .reply(r, status: out.status ?? 1)
            }
            if let f = RetriageBatchClient.failure(in: out.stdout) { return .failed(f) }
            if let refusal = AnthropicAccount.refusal(in: out.stderr) { return .failed(RetriageFailure(code: "account", message: refusal)) }
            return .failed(noReply(out.status, what: "a reply"))
        }
    }

    private static func failure(of r: DoctorReply) -> RetriageFailure {
        RetriageFailure(code: r.code ?? "unknown", message: r.error ?? "vani doctor failed without saying why")
    }

    private static let tooLarge = RetriageFailure(code: "too_large", message: "vani doctor's reply was larger than 8 MB")

    private static func noReply(_ status: Int32?, what: String) -> RetriageFailure {
        let tooOld = status == 2 ? " (this vani has no doctor that answers the app; rebuild the app)" : ""
        return RetriageFailure(code: "no_reply", message: "vani doctor exited \(status ?? -1) without \(what)\(tooOld)")
    }
}

/// The doctor's words and judgements for the Pipeline tab, kept here so they are tested.
public enum DoctorPresentation {
    public enum Tone: Equatable, Sendable { case problem, watch, fine }

    /// The budget offered for a worst case: a quarter over it, rounded up to the next quarter dollar,
    /// and never under the worst case. Nil when even the worst case is over vani's ceiling.
    public static func suggestedBudget(worstUSD: Double) -> Double? {
        guard worstUSD.isFinite, worstUSD >= 0, worstUSD <= DoctorClient.maxBudgetUSD else { return nil }
        let padded = (worstUSD * 1.25 * 4).rounded(.up) / 4
        let atLeast = (worstUSD * 100).rounded(.up) / 100
        return min(max(padded, atLeast, 0.25), DoctorClient.maxBudgetUSD)
    }

    /// Whether `budget` covers `worstUSD` as vani will check it (in cents, rounded as it is passed).
    public static func covers(budget: Double, worstUSD: Double) -> Bool {
        let passed = (budget * 100).rounded() / 100
        return passed.isFinite && passed > 0 && passed <= DoctorClient.maxBudgetUSD && passed >= worstUSD
    }

    public static func boardVerdict(_ v: String) -> (label: String, tone: Tone) {
        switch v {
        case "silent_failure": ("Failing silently", .problem)
        case "loud_failure": ("Failing (reported)", .problem)
        case "transient_empty": ("Empty for now", .watch)
        case "unclear": ("Unclear", .watch)
        case "truly_empty": ("Really empty", .fine)
        case "healthy": ("Healthy", .fine)
        default: (v, .watch)
        }
    }

    public static func dropVerdict(_ v: String) -> (label: String, tone: Tone) {
        switch v {
        case "wrong_drop": ("Wrong drop", .problem)
        case "correct_drop": ("Right drop", .fine)
        case "unclear": ("Unclear", .watch)
        default: (v, .watch)
        }
    }

    public static func cause(_ c: String) -> String? {
        switch c {
        case "title_rule_too_broad": "a title rule too broad"
        case "seniority_rule": "the seniority rule"
        case "model_misread": "the model misread it"
        case "exclusion": "an exclusion"
        case "thin_description": "a thin description"
        case "other": "another cause"
        case "none", "": nil
        default: c
        }
    }

    public static func signal(_ s: String) -> String {
        switch s {
        case "zero_every_run": "0 every run"
        case "zero_after_nonzero": "0 after listing roles"
        case "sharp_drop": "sharp drop"
        case "mass_missed": "many roles missed at once"
        case "chronic_failure": "fails most runs"
        case "failing_now": "failing now"
        case "never_recorded": "no run reached it"
        default: s
        }
    }

    /// Board findings, the ones to act on first.
    public static func orderedBoards(_ r: DoctorReport) -> [DoctorBoardFinding] {
        r.boardFindings.enumerated().sorted { a, b in
            let ta = rank(boardVerdict(a.element.verdict).tone), tb = rank(boardVerdict(b.element.verdict).tone)
            return ta != tb ? ta < tb : a.offset < b.offset
        }.map(\.element)
    }

    /// Drop findings, wrong drops first.
    public static func orderedDrops(_ r: DoctorReport) -> [DoctorDropFinding] {
        r.dropFindings.enumerated().sorted { a, b in
            let ta = rank(dropVerdict(a.element.verdict).tone), tb = rank(dropVerdict(b.element.verdict).tone)
            return ta != tb ? ta < tb : a.offset < b.offset
        }.map(\.element)
    }

    private static func rank(_ t: Tone) -> Int {
        switch t {
        case .problem: 0
        case .watch: 1
        case .fine: 2
        }
    }

    /// One line for a report: what was judged and what was found. Counts only; never "all clear"
    /// for a part that did not judge everything.
    public static func headline(_ r: DoctorReport) -> String {
        var parts: [String] = []
        if r.boards.asked > 0 {
            let bad = r.boardFindings.filter { boardVerdict($0.verdict).tone == .problem }.count
            parts.append("\(r.boards.answered) of \(r.boards.asked) boards judged, \(bad) failing")
        }
        if r.drops.asked > 0 {
            let wrong = r.dropFindings.filter { $0.verdict == "wrong_drop" }.count
            parts.append("\(r.drops.answered) of \(r.drops.asked) drops judged, \(wrong) wrong")
        }
        return parts.isEmpty ? "Nothing was asked" : parts.joined(separator: "; ")
    }

    /// Why a report is not the whole answer, one line each. Empty means every part judged everything.
    public static func gaps(_ r: DoctorReport) -> [String] {
        var out: [String] = []
        for (name, p) in [("board", r.boards), ("drop", r.drops)] {
            if let e = p.error {
                out.append("The \(name) review did not complete: \(e)")
            } else if p.answered < p.asked {
                out.append("The \(name) review judged \(p.answered) of the \(p.asked) it was sent; the rest are not examined.")
            }
        }
        return out
    }

    /// What the owner should know before paying for a review, one line each.
    public static func warnings(_ r: DoctorReply) -> [String] {
        var out: [String] = []
        if r.identifyingWordsBuiltinOnly {
            out.append("Your identifying-words list is missing, so only the built-in work-authorization terms are screened out of what is sent. Your name, employers and schools are not screened.")
        }
        if !r.runProblems.isEmpty {
            out.append("\(r.runProblems.count) run record\(r.runProblems.count == 1 ? " is" : "s are") unreadable and left out.")
        }
        if r.runsWithUnnamedBoardsAndNoLog > 0 {
            out.append("\(r.runsWithUnnamedBoardsAndNoLog) run\(r.runsWithUnnamedBoardsAndNoLog == 1 ? "" : "s") left boards unnamed and had no whole log, so a board no run reached may be a reporting gap.")
        }
        let left = r.dropsLeftOut.values.reduce(0, +)
        if left > 0 {
            out.append("\(left) dropped posting\(left == 1 ? " is" : "s are") never sampled (sponsorship drops and your own exclusions).")
        }
        return out
    }

    public static func usd(_ v: Double) -> String {
        v.formatted(.currency(code: "USD").precision(.fractionLength(2...4)).locale(Locale(identifier: "en_US")))
    }
}
