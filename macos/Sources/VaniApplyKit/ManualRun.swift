import Foundation

// A run started from the Pipeline tab is `vani run-once` (cmd/vani/main.go)
// with the overrides chosen in the app, started as a long job whose output goes
// to <state dir>/runs/manual.log (the previous run's log is kept in runs/history). Its progress and outcome are the run record,
// read through `vani status --json` like every other run: the app parses no
// output of the run itself.
//
// While the daemon runs it holds the ledger and run-once is refused, so the app
// asks the daemon instead: `vani run-request --json` (cmd/vani/runrequest.go)
// with the same flags. The daemon makes the run with its own keys and records
// it like any other; status's run_request facet says whether it is waiting.

/// A manual run's overrides: `vani run-once`'s flags (cmd/vani/runoptions.go),
/// and how `vani status --json` reports them (`last_run.options`).
public struct ManualRunOptions: Codable, Equatable, Sendable {
    /// config.MaxAdvancePerRun.
    public static let maxAdvance = 5000
    /// maxRunBoards: at most this many --board flags.
    public static let maxBoards = 200

    /// Cap on postings triaged, scored and explained this run; 0 keeps the
    /// config's advance_per_run.
    public var advance: Int
    /// The watchlist boards to list, as "ats:token"; empty lists every board.
    public var boards: [String]
    /// List and reconcile boards only: no triage, scoring or explaining. No
    /// decision model is called, and none needs to be configured (a run that
    /// advances postings still needs one).
    public var listOnly: Bool
    /// Write no digest: the run's roles wait for the next one.
    public var noDigest: Bool
    /// Triage postings located in the United States before the rest
    /// (`--us-first`). It only orders; nothing is dropped. A list-only run has
    /// no advance phase to order, so vani refuses the pair.
    public var usFirst: Bool
    /// The run's scope (cmd/vani/runscope.go): triage, score and explain only
    /// the postings at these companies and places, read as `vani jobs list`
    /// reads its flags of the same names. Vani reports them folded (company
    /// keys, USPS codes, areas); empty and false take every posting.
    public var companies: [String]
    public var states: [String]
    public var areas: [String]
    public var remote: Bool
    public var orRemote: Bool

    public init(advance: Int = 0, boards: [String] = [], listOnly: Bool = false, noDigest: Bool = false,
                usFirst: Bool = false,
                companies: [String] = [], states: [String] = [], areas: [String] = [], remote: Bool = false, orRemote: Bool = false) {
        self.advance = advance
        self.boards = boards
        self.listOnly = listOnly
        self.noDigest = noDigest
        self.usFirst = usFirst
        self.companies = companies
        self.states = states
        self.areas = areas
        self.remote = remote
        self.orRemote = orRemote
    }

    enum CodingKeys: String, CodingKey {
        case advance, boards
        case listOnly = "list_only"
        case noDigest = "no_digest"
        case usFirst = "us_first"
        case companies, states, areas, remote
        case orRemote = "or_remote"
    }

    /// Lenient: Go omits each field at its zero value.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        advance = try c.decodeIfPresent(Int.self, forKey: .advance) ?? 0
        boards = try c.decodeIfPresent([String].self, forKey: .boards) ?? []
        listOnly = try c.decodeIfPresent(Bool.self, forKey: .listOnly) ?? false
        noDigest = try c.decodeIfPresent(Bool.self, forKey: .noDigest) ?? false
        usFirst = try c.decodeIfPresent(Bool.self, forKey: .usFirst) ?? false
        companies = try c.decodeIfPresent([String].self, forKey: .companies) ?? []
        states = try c.decodeIfPresent([String].self, forKey: .states) ?? []
        areas = try c.decodeIfPresent([String].self, forKey: .areas) ?? []
        remote = try c.decodeIfPresent(Bool.self, forKey: .remote) ?? false
        orRemote = try c.decodeIfPresent(Bool.self, forKey: .orRemote) ?? false
    }

    /// "gh:acme": how run-once names a watchlist board.
    public static func ref(ats: String, token: String) -> String { "\(ats):\(token)" }

    /// The run as the config describes it, which is what the daemon runs.
    public var isDefault: Bool { advance == 0 && boards.isEmpty && !listOnly && !noDigest && !usFirst && !scoped }

    /// Whether the run triages only some companies or places.
    public var scoped: Bool { !companies.isEmpty || !states.isEmpty || !areas.isEmpty || remote || orRemote }

    /// `vani run-once`'s argv after the command, or the reason vani would
    /// refuse it (the bounds checkRunOptions applies), so a bad choice is
    /// caught before anything starts. `run-request` takes the same flags.
    public func arguments(command: String = "run-once") throws -> [String] {
        guard (0...Self.maxAdvance).contains(advance) else {
            throw BankError("the advance cap must be within 1…\(Self.maxAdvance), or 0 for advance_per_run")
        }
        if listOnly && advance != 0 { throw BankError("a list-only run has no advance phase to cap") }
        if listOnly && usFirst { throw BankError("a list-only run has no advance phase for US first to order") }
        guard boards.count <= Self.maxBoards else { throw BankError("choose at most \(Self.maxBoards) boards, or all of them") }
        var args = [command]
        if advance > 0 { args += ["--advance", String(advance)] }
        for b in boards {
            let parts = b.split(separator: ":", maxSplits: 1)
            guard parts.count == 2, !parts[0].isEmpty, !parts[1].isEmpty, !b.hasPrefix("-") else {
                throw BankError("board \(PipelineStatus.bounded(b, max: 80)) is not ats:token")
            }
            args += ["--board", b]
        }
        if scoped {
            if listOnly { throw BankError("a list-only run has no advance phase to limit to companies or places") }
            if remote && orRemote { throw BankError("only remote and remote-or-these-places exclude each other") }
            if orRemote && states.isEmpty && areas.isEmpty { throw BankError("remote-or-these-places needs a state or an area") }
            for (flag, items) in [("--company", companies), ("--state", states), ("--area", areas)] where !items.isEmpty {
                // vani splits each list on commas, so an item holding one would become two.
                guard items.allSatisfy({ !$0.trimmingCharacters(in: .whitespaces).isEmpty && !$0.contains(",") }) else {
                    throw BankError("\(flag) needs names without commas")
                }
                args += [flag, items.joined(separator: ",")]
            }
            if remote { args.append("--remote") }
            if orRemote { args.append("--or-remote") }
        }
        if listOnly { args.append("--list-only") }
        if usFirst { args.append("--us-first") }
        if noDigest { args.append("--no-digest") }
        return args
    }

    /// One on-demand pass over the backlog: advance (triage, score, explain) at
    /// the config's `advance_per_run`, every board, no digest. The cap stays 0
    /// on purpose. Eligible postings include ones already triaged and waiting
    /// to be scored or explained, so a cap of the awaiting-triage count would
    /// stop short of them. A shorter pass is the sheet's own cap.
    public static let backlog = ManualRunOptions(noDigest: true)

    /// Shown in the Run Now sheet when Triage now opened it.
    public static let triageNowBanner = "Triage now prefilled this sheet: every board, advance at advance_per_run (this also scores and explains postings already triaged), and no digest. Changes here apply to this run only and are not kept for the next Run Now."

    /// The sheet's footer. While the daemon holds the ledger it makes the run;
    /// otherwise another vani holding the ledger is why a run is refused.
    public static func runFootnote(daemonRunning: Bool) -> String {
        if daemonRunning {
            return "The daemon is running and holds the ledger, so it will make this run with its own keys."
        }
        return "A run is refused while another vani holds the ledger."
    }

    /// What Triage now tells the person before they confirm.
    /// `waiting` is the awaiting-triage count. `advancePerRun` is
    /// `schedule.unkept.advance_per_run` when that facet carries it. The two
    /// numbers are separate sentences: the cap also scores and explains
    /// postings already past triage, and a kept schedule omits the number.
    public static func backlogHelp(waiting: Int, advancePerRun: Int? = nil) -> String {
        let waitingSentence: String
        switch waiting {
        case ..<1: waitingSentence = "Nothing is awaiting triage."
        case 1: waitingSentence = "1 posting is awaiting triage."
        default: waitingSentence = "\(waiting.formatted()) postings are awaiting triage."
        }
        let capSentence: String
        if let cap = advancePerRun, cap > 0, waiting > cap {
            let rest = waiting - cap
            // Slots also go to scoring and explaining, so more of this list can
            // stay than waiting minus the cap. The count is a floor.
            let restSentence = rest == 1
                ? "At least 1 awaiting triage stays for a later run."
                : "At least \(rest.formatted()) awaiting triage stay for a later run."
            capSentence = "This pass advances at most \(cap.formatted()) postings (advance_per_run). That cap also covers scoring and explaining postings already past triage. \(restSentence)"
        } else if let cap = advancePerRun, cap > 0 {
            capSentence = "The cap is advance_per_run of \(cap.formatted()). It is not the awaiting-triage count, because this pass also scores and explains postings already past triage."
        } else {
            capSentence = "The cap is advance_per_run. It is not the awaiting-triage count, because this pass also scores and explains postings already past triage. This status does not say what advance_per_run is."
        }
        return "\(waitingSentence) \(capSentence) Every board is listed first. No digest is written. A cap set in the sheet is a shorter pass. Start Run is what spends. These choices are not kept for the next Run Now."
    }

    /// Help for the Triage now button. `kept` is `schedule.kept` and
    /// `advancePerRun` is `schedule.unkept.advance_per_run`. Go reports that
    /// number only while the schedule is unkept, so a kept or unknown schedule
    /// is not given a cap from a stray value.
    public static func backlogHelp(waiting: Int, kept: Bool?, advancePerRun: Int?) -> String {
        let quoted: Int? = kept == false ? advancePerRun : nil
        return backlogHelp(waiting: waiting, advancePerRun: quoted)
    }

    /// "3 boards; advance cap 50; US first; advancing only postings at acme in
    /// TX; no digest", as `vani runs show` words it (optionsSummary). Nil for a
    /// run with no overrides.
    public var summary: String? {
        guard !isDefault else { return nil }
        var parts: [String] = []
        if !boards.isEmpty { parts.append(boards.count == 1 ? "1 board" : "\(boards.count) boards") }
        if listOnly { parts.append("list only") } else if advance > 0 { parts.append("advance cap \(advance)") }
        if usFirst { parts.append("US first") }
        if let s = scopeWords { parts.append("advancing only \(s)") }
        if noDigest { parts.append("no digest") }
        return parts.joined(separator: "; ")
    }

    /// The scope as vani's scopeWords says it: "postings at acme, initech in
    /// TX or remote", "remote postings". Nil for no scope.
    public var scopeWords: String? {
        guard scoped else { return nil }
        var s = remote ? "remote postings" : "postings"
        if !companies.isEmpty {
            let shown = companies.prefix(5)
            s += " at " + shown.joined(separator: ", ")
            if companies.count > shown.count { s += " and \(companies.count - shown.count) more companies" }
        }
        let places = (states + areas).joined(separator: ", ")
        if !places.isEmpty {
            s += " in " + places
            if orRemote { s += " or remote" }
        }
        return s
    }
}

/// The open Run Now sheet. `id` is its presentation identity and is left out
/// of equality, which compares the source and the options only.
public struct RunSheet: Identifiable, Equatable, Sendable {
    public enum Source: String, Sendable {
        case triageNow
        case runNow
    }

    public let id: UUID
    public var options: ManualRunOptions
    public var source: Source

    /// Run Now keeps the confirmed choices. Triage now does not.
    public var remembers: Bool { source == .runNow }

    /// Present only when Triage now opened the sheet.
    public var banner: String? {
        source == .triageNow ? ManualRunOptions.triageNowBanner : nil
    }

    public init(options: ManualRunOptions, source: Source, id: UUID = UUID()) {
        self.id = id
        self.options = options
        self.source = source
    }

    public static func == (lhs: RunSheet, rhs: RunSheet) -> Bool {
        lhs.source == rhs.source && lhs.options == rhs.options
    }

    /// Banner, footer and whether Start Run keeps the choices. The sheet view
    /// shows this and passes `remembers` through; it does not decide them.
    public func presentation(daemonRunning: Bool) -> RunSheetPresentation {
        RunSheetPresentation(options: options, banner: banner,
                             footnote: ManualRunOptions.runFootnote(daemonRunning: daemonRunning),
                             remembers: remembers)
    }
}

/// What the Run Now sheet shows for one `RunSheet`.
public struct RunSheetPresentation: Equatable, Sendable {
    public var options: ManualRunOptions
    public var banner: String?
    public var footnote: String
    public var remembers: Bool

    public init(options: ManualRunOptions, banner: String?, footnote: String, remembers: Bool) {
        self.options = options
        self.banner = banner
        self.footnote = footnote
        self.remembers = remembers
    }
}

/// Starts `vani run-once` as a long job, with the keys drafting uses (the
/// pipeline calls decision models): through anthropic-account when Vani's
/// config names the anthropic provider, with the OpenRouter key from the Keychain.
public struct ManualRunLauncher: Sendable {
    public let runner: DraftRunner

    public init(runner: DraftRunner) {
        self.runner = runner
    }

    /// The variable that tells `vani run-once` where its output goes (cmd/vani/runlog.go
    /// runLogEnv); vani records the path, once it passes its own check that the
    /// path is a file under <state>/runs, in the run record's `log`.
    public static let logEnvironmentKey = "VANI_RUN_LOG"

    /// Where a manual run's output goes: <state dir>/runs/manual.log, beside
    /// the run record. Each run replaces it, after moving the previous one to
    /// runs/history/manual-<run id>.log (`ManualRunLog.rotate`, newest 20 within 8 MiB).
    public static func logURL(stateDir: String) -> URL {
        URL(fileURLWithPath: stateDir).appendingPathComponent("runs/manual.log")
    }

    /// Start the run. It may wait on a Keychain prompt, so call it off the main
    /// thread. Throws when the options are refused or vani cannot start; a run
    /// vani refuses after starting (another vani holds the ledger) exits
    /// non-zero, and `VaniProcess.lastVaniLine` reads why from the log.
    public func start(_ options: ManualRunOptions, stateDir: String,
                      onExit: @escaping @Sendable (Int32) -> Void) throws -> LaunchedJob {
        guard !stateDir.isEmpty else { throw BankError("vani has not reported its state folder yet") }
        let args = try options.arguments()
        switch runner.prepare(args, withDecisionKey: true) {
        case let .refused(reply):
            throw BankError(reply.error ?? "vani could not be started")
        case let .launch(l):
            // The launch truncates manual.log: keep the previous run's first (RunLogs.swift).
            try ManualRunLog.rotate(stateDir: stateDir)
            let log = Self.logURL(stateDir: stateDir)
            // vani records this path in the run's record, so `vani runs failures`
            // names this run's log. An environment variable, not a flag: a vani
            // that predates it ignores it and still runs.
            var environment = l.environment
            environment[Self.logEnvironmentKey] = log.path
            return try VaniProcess.launch(l.program, arguments: l.arguments, environment: environment,
                                          log: log, onExit: onExit)
        }
    }
}
