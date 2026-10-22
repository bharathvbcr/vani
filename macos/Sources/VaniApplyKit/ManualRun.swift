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

    public init(advance: Int = 0, boards: [String] = [], listOnly: Bool = false, noDigest: Bool = false) {
        self.advance = advance
        self.boards = boards
        self.listOnly = listOnly
        self.noDigest = noDigest
    }

    enum CodingKeys: String, CodingKey {
        case advance, boards
        case listOnly = "list_only"
        case noDigest = "no_digest"
    }

    /// Lenient: Go omits each field at its zero value.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        advance = try c.decodeIfPresent(Int.self, forKey: .advance) ?? 0
        boards = try c.decodeIfPresent([String].self, forKey: .boards) ?? []
        listOnly = try c.decodeIfPresent(Bool.self, forKey: .listOnly) ?? false
        noDigest = try c.decodeIfPresent(Bool.self, forKey: .noDigest) ?? false
    }

    /// "gh:acme": how run-once names a watchlist board.
    public static func ref(ats: String, token: String) -> String { "\(ats):\(token)" }

    /// The run as the config describes it, which is what the daemon runs.
    public var isDefault: Bool { advance == 0 && boards.isEmpty && !listOnly && !noDigest }

    /// `vani run-once`'s argv after the command, or the reason vani would
    /// refuse it (the bounds checkRunOptions applies), so a bad choice is
    /// caught before anything starts. `run-request` takes the same flags.
    public func arguments(command: String = "run-once") throws -> [String] {
        guard (0...Self.maxAdvance).contains(advance) else {
            throw BankError("the advance cap must be within 1…\(Self.maxAdvance), or 0 for advance_per_run")
        }
        if listOnly && advance != 0 { throw BankError("a list-only run has no advance phase to cap") }
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
        if listOnly { args.append("--list-only") }
        if noDigest { args.append("--no-digest") }
        return args
    }

    /// "3 boards; advance cap 50; no digest". Nil for a run with no overrides.
    public var summary: String? {
        guard !isDefault else { return nil }
        var parts: [String] = []
        if !boards.isEmpty { parts.append(boards.count == 1 ? "1 board" : "\(boards.count) boards") }
        if listOnly { parts.append("list only") } else if advance > 0 { parts.append("advance cap \(advance)") }
        if noDigest { parts.append("no digest") }
        return parts.joined(separator: "; ")
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

    /// Where a manual run's output goes: <state dir>/runs/manual.log, beside
    /// the run record. Each run replaces it, after moving the previous one to
    /// runs/history/manual-<time>.log (`ManualRunLog.rotate`, newest 20 within 8 MiB).
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
            return try VaniProcess.launch(l.program, arguments: l.arguments, environment: l.environment,
                                          log: Self.logURL(stateDir: stateDir), onExit: onExit)
        }
    }
}
