import Foundation

// The Pipeline tab is a client of `vani status --json` (cmd/vani/statuscmd.go):
// whether the daily run happened and how it ended, what the ledger holds, what
// each paid backend has spent today against its cap, and how the decision
// models are configured. Every section is a facet with its own `ok`; a facet
// vani could not read is not ok and says why, and its numbers are absent, never
// zero. Nothing here reads the state folder itself.

public struct PipelineStatus: Decodable, Equatable, Sendable {
    /// The contract version this app reads.
    public static let supportedVersion = 1

    public var version: Int
    public var generatedAt: Date
    public var stateDir: String
    public var config: ConfigFacet
    public var ledger: LedgerFacet
    public var lastRun: LastRun
    public var schedule: Schedule
    public var backends: Backends
    public var decide: Decide

    enum CodingKeys: String, CodingKey {
        case version, config, ledger, schedule, backends, decide
        case generatedAt = "generated_at"
        case stateDir = "state_dir"
        case lastRun = "last_run"
    }

    public struct ConfigFacet: Decodable, Equatable, Sendable {
        public var ok: Bool
        public var reason: String?
        public var boards: Int?
    }

    public struct LedgerFacet: Decodable, Equatable, Sendable {
        public var ok: Bool
        public var reason: String?
        /// False before the first run created the ledger.
        public var exists: Bool
        public var path: String
        public var total: Int
        public var byStatus: [String: Int]
        /// A writer was mid-append: the counts are complete up to it.
        public var torn: Bool?

        enum CodingKeys: String, CodingKey {
            case ok, reason, exists, path, total, torn
            case byStatus = "by_status"
        }
    }

    public struct LastRun: Decodable, Equatable, Sendable {
        public enum State: String, Decodable, Sendable {
            /// Never recorded: a fresh install, or the record was removed.
            case never = "none"
            case ok, failed, cancelled, running, interrupted, unknown
        }

        public var ok: Bool
        public var reason: String?
        /// A state a newer vani adds decodes as `.unknown`, never fails the read.
        public var state: State
        public var startedAt: Date?
        public var finishedAt: Date?
        public var durationSec: Double?
        /// Where a failed run stopped: pipeline or digest.
        public var stage: String?
        public var error: String?
        public var postingsBefore: Int?
        public var postingsAfter: Int?
        public var countsBefore: [String: Int]?
        public var countsAfter: [String: Int]?

        enum CodingKeys: String, CodingKey {
            case ok, reason, state, stage, error
            case startedAt = "started_at"
            case finishedAt = "finished_at"
            case durationSec = "duration_sec"
            case postingsBefore = "postings_before"
            case postingsAfter = "postings_after"
            case countsBefore = "counts_before"
            case countsAfter = "counts_after"
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            ok = try c.decode(Bool.self, forKey: .ok)
            reason = try c.decodeIfPresent(String.self, forKey: .reason)
            state = (try? c.decode(State.self, forKey: .state)) ?? .unknown
            startedAt = try c.decodeIfPresent(Date.self, forKey: .startedAt)
            finishedAt = try c.decodeIfPresent(Date.self, forKey: .finishedAt)
            durationSec = try c.decodeIfPresent(Double.self, forKey: .durationSec)
            stage = try c.decodeIfPresent(String.self, forKey: .stage)
            error = try c.decodeIfPresent(String.self, forKey: .error)
            postingsBefore = try c.decodeIfPresent(Int.self, forKey: .postingsBefore)
            postingsAfter = try c.decodeIfPresent(Int.self, forKey: .postingsAfter)
            countsBefore = try c.decodeIfPresent([String: Int].self, forKey: .countsBefore)
            countsAfter = try c.decodeIfPresent([String: Int].self, forKey: .countsAfter)
        }

        public init(ok: Bool = true, reason: String? = nil, state: State, startedAt: Date? = nil, finishedAt: Date? = nil,
                    durationSec: Double? = nil, stage: String? = nil, error: String? = nil,
                    postingsBefore: Int? = nil, postingsAfter: Int? = nil) {
            self.ok = ok
            self.reason = reason
            self.state = state
            self.startedAt = startedAt
            self.finishedAt = finishedAt
            self.durationSec = durationSec
            self.stage = stage
            self.error = error
            self.postingsBefore = postingsBefore
            self.postingsAfter = postingsAfter
        }

        /// Postings the run added to the ledger (a ledger-derived difference, not
        /// an engine count). Nil when either end was not recorded.
        public var postingsAdded: Int? {
            guard let a = postingsAfter, let b = postingsBefore else { return nil }
            return a - b
        }

        /// What the run changed, by status: only statuses whose count moved.
        public var statusChanges: [(status: String, delta: Int)] {
            guard let before = countsBefore, let after = countsAfter else { return [] }
            return Set(before.keys).union(after.keys).sorted().compactMap { s in
                let d = (after[s] ?? 0) - (before[s] ?? 0)
                return d == 0 ? nil : (s, d)
            }
        }
    }

    public struct Schedule: Decodable, Equatable, Sendable {
        public var ok: Bool
        public var reason: String?
        public var dailyAt: String?
        public var timezone: String?
        public var nextRun: Date?

        enum CodingKeys: String, CodingKey {
            case ok, reason, timezone
            case dailyAt = "daily_at"
            case nextRun = "next_run"
        }
    }

    public struct Backends: Decodable, Equatable, Sendable {
        public var ok: Bool
        public var reason: String?
        /// The UTC day the spend is for: caps roll over at UTC midnight.
        public var day: String
        public var items: [Backend]
    }

    public struct Backend: Decodable, Equatable, Sendable, Identifiable {
        public var ok: Bool
        public var reason: String?
        public var name: String
        public var kind: String
        public var capUSD: Double
        /// Absent (with `ok` false) when the spend could not be read.
        public var spentUSD: Double?
        public var remainingUSD: Double?
        public var exhausted: Bool?

        public var id: String { kind + ":" + name }

        enum CodingKeys: String, CodingKey {
            case ok, reason, name, kind, exhausted
            case capUSD = "cap_usd"
            case spentUSD = "spent_usd"
            case remainingUSD = "remaining_usd"
        }

        /// Fraction of the cap spent, 0...1, or nil when the spend is unknown.
        public var fractionSpent: Double? {
            guard ok, let spent = spentUSD, capUSD > 0 else { return nil }
            return min(max(spent / capUSD, 0), 1)
        }
    }

    public struct Decide: Decodable, Equatable, Sendable {
        public var ok: Bool
        public var reason: String?
        public var shadow: String?
        public var localConfigured: Bool
        /// "server" (vani starts the local model's server for each run) or "endpoint".
        public var localMode: String?
        public var localModel: String?
        public var lappiConfigured: Bool

        enum CodingKeys: String, CodingKey {
            case ok, reason, shadow
            case localConfigured = "local_configured"
            case localMode = "local_mode"
            case localModel = "local_model"
            case lappiConfigured = "lappi_configured"
        }
    }
}

// MARK: - What needs attention

public struct PipelineIssue: Equatable, Sendable {
    public enum Severity: Int, Comparable, Sendable {
        case warning, problem
        public static func < (a: Self, b: Self) -> Bool { a.rawValue < b.rawValue }
    }

    public var severity: Severity
    public var title: String
    public var detail: String?
}

public enum PipelineHealth: Equatable, Sendable {
    case healthy, warning, problem
}

extension PipelineStatus {
    /// A finished run older than this, with no newer one, means the daily run
    /// did not happen (one day plus margin for a late or skipped start).
    public static let staleAfter: TimeInterval = 36 * 3600

    /// Everything the user should look at, worst first. An empty list is a
    /// pipeline whose every section was read and is fine.
    public func issues(at now: Date = Date()) -> [PipelineIssue] {
        var out: [PipelineIssue] = []
        func add(_ s: PipelineIssue.Severity, _ title: String, _ detail: String? = nil) {
            out.append(PipelineIssue(severity: s, title: title, detail: detail))
        }
        if !config.ok { add(.problem, "The Vani config could not be read", config.reason) }
        if !ledger.ok { add(.problem, "The ledger could not be read", ledger.reason) }

        switch lastRun.state {
        case .failed:
            let stage = lastRun.stage.map { " (\($0))" } ?? ""
            add(.problem, "The last run failed\(stage)", lastRun.error ?? lastRun.reason)
        case .interrupted:
            add(.problem, "The last run did not finish", "It started but its process is gone (a crash, a shutdown or a kill).")
        case .unknown:
            add(.problem, "The last run could not be read", lastRun.reason)
        case .cancelled:
            add(.warning, "The last run was cancelled", lastRun.error)
        case .never:
            add(.warning, "No daily run has been recorded yet", lastRun.reason)
        case .ok:
            if let finished = lastRun.finishedAt, now.timeIntervalSince(finished) > Self.staleAfter {
                add(.warning, "No run in over a day", "The last successful run finished \(Self.age(now.timeIntervalSince(finished))) ago.")
            }
        case .running:
            break
        }

        if !schedule.ok { add(.warning, "The schedule could not be read", schedule.reason) }
        if !backends.ok { add(.warning, "Spend and caps are unknown", backends.reason) }
        for b in backends.items {
            if !b.ok {
                add(.warning, "Today's spend on \(b.name) could not be read", b.reason)
            } else if b.exhausted == true {
                add(.warning, "\(b.name)'s daily budget is spent", "Postings wait for tomorrow's cap or another backend.")
            }
        }
        return out.sorted { $0.severity > $1.severity }
    }

    public func health(at now: Date = Date()) -> PipelineHealth {
        switch issues(at: now).map(\.severity).max() {
        case .problem?: .problem
        case .warning?: .warning
        case nil: .healthy
        }
    }

    /// "5 min", "3 h", "2 days": the coarsest unit that is not zero.
    public static func age(_ seconds: TimeInterval) -> String {
        let s = max(seconds, 0)
        switch s {
        case ..<90: return "under 2 min"
        case ..<5400: return "\(Int((s / 60).rounded())) min"
        case ..<172_800: return "\(Int((s / 3600).rounded())) h"
        default: return "\(Int((s / 86400).rounded())) days"
        }
    }
}

// MARK: - Client

public struct PipelineClient: Sendable {
    /// vani bounds its own read at 8 s.
    public static let timeout: TimeInterval = 15
    static let maxOutputBytes = 1 << 20

    public let vaniBinary: URL
    public let configPath: String?
    public let stateDir: String?
    let timeout: TimeInterval

    public init(vaniBinary: URL, configPath: String? = nil, stateDir: String? = nil, timeout: TimeInterval = PipelineClient.timeout) {
        self.vaniBinary = vaniBinary
        self.configPath = configPath
        self.stateDir = stateDir
        self.timeout = timeout
    }

    private struct Failure: Decodable { var error: String }

    public func status() throws -> PipelineStatus {
        let args = ["status", "--json"]
        let out = VaniProcess.run(vaniBinary, arguments: args,
                                  environment: VaniProcess.environment(configPath: configPath, stateDir: stateDir),
                                  timeout: timeout, maxOutputBytes: Self.maxOutputBytes)
        if let launchError = out.launchError { throw BankError(launchError) }
        if out.timedOut { throw BankError("vani status did not finish within \(Int(timeout)) s") }
        if out.truncated { throw BankError("vani's status reply was larger than \(Self.maxOutputBytes >> 10) KB") }
        let decoder = JobsClient.decoder
        if out.status == 0, let value = try? decoder.decode(PipelineStatus.self, from: out.stdout) {
            guard value.version == PipelineStatus.supportedVersion else {
                throw BankError("vani reports status version \(value.version); this app reads version \(PipelineStatus.supportedVersion). Rebuild the app.")
            }
            return value
        }
        if let failure = try? decoder.decode(Failure.self, from: out.stdout) {
            throw BankError(failure.error)
        }
        throw BankError("vani status exited \(out.status ?? -1) without a reply"
            + (out.status == 2 ? " (this vani is too old for the Pipeline tab; rebuild the app)" : ""))
    }
}
