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
        /// The listing phase, board by board; nil when the run ended before it,
        /// or from a vani that does not report it.
        public var boards: Boards?
        /// The triage/score/explain phase; nil when the run ended before it,
        /// or from a vani that does not report it.
        public var advance: Advance?
        /// The decision backends' failed calls by cause; nil when none failed,
        /// or from a vani that does not report them.
        public var backendErrors: BackendErrors?

        enum CodingKeys: String, CodingKey {
            case ok, reason, state, stage, error, boards, advance
            case startedAt = "started_at"
            case finishedAt = "finished_at"
            case durationSec = "duration_sec"
            case postingsBefore = "postings_before"
            case postingsAfter = "postings_after"
            case countsBefore = "counts_before"
            case countsAfter = "counts_after"
            case backendErrors = "backend_errors"
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
            boards = try c.decodeIfPresent(Boards.self, forKey: .boards)
            advance = try c.decodeIfPresent(Advance.self, forKey: .advance)
            backendErrors = try c.decodeIfPresent(BackendErrors.self, forKey: .backendErrors)
        }

        public init(ok: Bool = true, reason: String? = nil, state: State, startedAt: Date? = nil, finishedAt: Date? = nil,
                    durationSec: Double? = nil, stage: String? = nil, error: String? = nil,
                    postingsBefore: Int? = nil, postingsAfter: Int? = nil,
                    boards: Boards? = nil, advance: Advance? = nil, backendErrors: BackendErrors? = nil) {
            self.boards = boards
            self.advance = advance
            self.backendErrors = backendErrors
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

        /// What one run's listing phase did, board by board (boardsReport in
        /// cmd/vani/runrecord.go). The totals count every configured board;
        /// `items` may be cut by vani's bounds, failed and skipped boards kept
        /// before read ones, and the cut is `unrecorded`.
        public struct Boards: Decodable, Equatable, Sendable {
            public struct Item: Decodable, Equatable, Sendable {
                public enum Outcome: Equatable, Sendable {
                    case read
                    /// Listing or reconciling failed; the board's postings were left as they were.
                    case failed
                    /// No adapter, or the careers-page source was disabled this run.
                    case skipped
                    /// An outcome a newer vani adds: shown as written, never as read.
                    case unknown(String)

                    init(raw: String) {
                        switch raw {
                        case "read": self = .read
                        case "failed": self = .failed
                        case "skipped": self = .skipped
                        default: self = .unknown(raw)
                        }
                    }
                }

                public var ats: String
                public var token: String
                public var company: String?
                public var outcome: Outcome
                /// Postings the board listed; nil when it failed or was skipped.
                public var listed: Int?
                /// Why the board failed or was skipped: adapter text, untrusted.
                /// Show it through `problems`, which bounds it.
                public var error: String?

                enum CodingKeys: String, CodingKey { case ats, token, company, outcome, listed, error }

                public init(from decoder: Decoder) throws {
                    let c = try decoder.container(keyedBy: CodingKeys.self)
                    ats = try c.decode(String.self, forKey: .ats)
                    token = try c.decode(String.self, forKey: .token)
                    company = try c.decodeIfPresent(String.self, forKey: .company)
                    outcome = Outcome(raw: try c.decode(String.self, forKey: .outcome))
                    listed = try c.decodeIfPresent(Int.self, forKey: .listed)
                    error = try c.decodeIfPresent(String.self, forKey: .error)
                }

                public init(ats: String, token: String, company: String? = nil, outcome: Outcome, listed: Int? = nil, error: String? = nil) {
                    self.ats = ats
                    self.token = token
                    self.company = company
                    self.outcome = outcome
                    self.listed = listed
                    self.error = error
                }
            }

            /// A board that was not read, ready to show: one bounded line each.
            public struct Line: Equatable, Sendable {
                /// "Intel (wd intel/External)"
                public var board: String
                /// "failed", "skipped", or a newer vani's outcome as written.
                public var outcome: String
                public var error: String
            }

            /// Longest error shown, in characters.
            public static let maxError = 240

            public var configured: Int
            public var read: Int
            public var failed: Int
            public var skipped: Int
            /// Boards never tried: the run ended before them.
            public var notReached: Int
            /// Postings the read boards listed, together.
            public var listed: Int
            public var items: [Item]
            /// Tried boards vani left out of `items`.
            public var unrecorded: Int

            enum CodingKeys: String, CodingKey {
                case configured, read, failed, skipped, listed, items, unrecorded
                case notReached = "not_reached"
            }

            public init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                configured = try c.decode(Int.self, forKey: .configured)
                read = try c.decode(Int.self, forKey: .read)
                failed = try c.decode(Int.self, forKey: .failed)
                skipped = try c.decode(Int.self, forKey: .skipped)
                listed = try c.decode(Int.self, forKey: .listed)
                notReached = try c.decodeIfPresent(Int.self, forKey: .notReached) ?? 0
                items = try c.decodeIfPresent([Item].self, forKey: .items) ?? []
                unrecorded = try c.decodeIfPresent(Int.self, forKey: .unrecorded) ?? 0
            }

            public init(configured: Int, read: Int, failed: Int = 0, skipped: Int = 0, notReached: Int = 0,
                        listed: Int, items: [Item] = [], unrecorded: Int = 0) {
                self.configured = configured
                self.read = read
                self.failed = failed
                self.skipped = skipped
                self.notReached = notReached
                self.listed = listed
                self.items = items
                self.unrecorded = unrecorded
            }

            /// Boards tried but not read: failed and skipped.
            public var unreadCount: Int { failed + skipped }

            /// "17 of 18 boards read; 412 postings listed"
            public var summary: String {
                "\(read) of \(configured) board\(configured == 1 ? "" : "s") read; \(listed) posting\(listed == 1 ? "" : "s") listed"
            }

            /// How the boards that were not read went, or nil when every board was read.
            public var outcome: String? {
                var parts: [String] = []
                if failed > 0 { parts.append("\(failed) failed") }
                if skipped > 0 { parts.append("\(skipped) skipped") }
                if notReached > 0 { parts.append("\(notReached) not reached") }
                guard !parts.isEmpty else { return nil }
                return parts.joined(separator: ", ") + "; their postings were left as they were"
            }

            /// The first `limit` boards that were not read, in vani's order.
            public func problems(limit: Int = 5) -> [Line] {
                items.filter { $0.outcome != .read }.prefix(max(limit, 0)).map { item in
                    let name = item.company.flatMap { $0.isEmpty ? nil : $0 }
                    let where_ = "\(item.ats) \(item.token)"
                    let outcome: String = switch item.outcome {
                    case .read: "read"
                    case .failed: "failed"
                    case .skipped: "skipped"
                    case .unknown(let raw): PipelineStatus.bounded(raw, max: 30)
                    }
                    return Line(board: PipelineStatus.bounded(name.map { "\($0) (\(where_))" } ?? where_, max: 80),
                                outcome: outcome,
                                error: PipelineStatus.bounded(item.error ?? "", max: Self.maxError))
                }
            }

            /// What `problems(limit:)` left out, or nil when it shows every board not read.
            public func moreNote(limit: Int = 5) -> String? {
                let shown = min(items.filter { $0.outcome != .read }.count, max(limit, 0))
                let hidden = unreadCount - shown
                return hidden > 0 ? "\(hidden) more board\(hidden == 1 ? "" : "s") not read" : nil
            }
        }

        /// What one run's advance phase did (advanceReport in cmd/vani/runrecord.go).
        /// Eligible is every posting waiting for a stage (live, triaged, unsure,
        /// scored), so it is more than the Jobs tab's "awaiting triage" count.
        public struct Advance: Decodable, Equatable, Sendable {
            public enum Stop: Equatable, Sendable {
                /// The per-run cap (config advance_per_run) was reached.
                case cap
                /// No decision backend read several postings in a row.
                case unreadStreak
                /// No decision backend read any posting.
                case noneRead
                /// Several postings in a row failed a stage other than triage.
                case failStreak
                /// No attempted posting advanced: each failed or went unread.
                case noneAdvanced
                case cancelled
                /// A reason a newer vani adds: shown as written, never as finished.
                case unknown(String)

                init(raw: String) {
                    switch raw {
                    case "cap": self = .cap
                    case "unread_streak": self = .unreadStreak
                    case "none_read": self = .noneRead
                    case "fail_streak": self = .failStreak
                    case "none_advanced": self = .noneAdvanced
                    case "cancelled": self = .cancelled
                    default: self = .unknown(raw)
                    }
                }
            }

            public var eligible: Int
            /// The per-run cap.
            public var limit: Int
            public var attempted: Int
            /// Attempted postings no decision backend could read; they stay live.
            public var unread: Int
            /// Attempted postings that hit another stage error; they wait.
            public var failed: Int
            /// Not attempted this run; they wait for the next one.
            public var deferred: Int
            /// Nil when the phase worked through every eligible posting.
            public var stopped: Stop?

            enum CodingKeys: String, CodingKey {
                case eligible, limit, attempted, unread, failed, deferred, stopped
            }

            public init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                eligible = try c.decode(Int.self, forKey: .eligible)
                limit = try c.decode(Int.self, forKey: .limit)
                attempted = try c.decode(Int.self, forKey: .attempted)
                unread = try c.decode(Int.self, forKey: .unread)
                failed = try c.decode(Int.self, forKey: .failed)
                deferred = try c.decode(Int.self, forKey: .deferred)
                stopped = try c.decodeIfPresent(String.self, forKey: .stopped).flatMap { $0.isEmpty ? nil : Stop(raw: $0) }
            }

            public init(eligible: Int, limit: Int, attempted: Int, unread: Int = 0, failed: Int = 0, deferred: Int, stopped: Stop? = nil) {
                self.eligible = eligible
                self.limit = limit
                self.attempted = attempted
                self.unread = unread
                self.failed = failed
                self.deferred = deferred
                self.stopped = stopped
            }

            /// Attempted postings that moved on: neither unread nor failed.
            public var advanced: Int { max(attempted - unread - failed, 0) }

            /// "300 of 2639 eligible postings attempted this run; 2339 wait for the next run"
            public var summary: String {
                if eligible == 0 { return "No posting was waiting for a stage" }
                let head = "\(attempted) of \(PipelineStatus.postings(eligible, "eligible")) attempted this run"
                return deferred > 0 ? head + "; \(deferred) wait for the next run" : head
            }

            /// How the attempted postings went, or nil when every one advanced.
            public var outcome: String? {
                guard unread > 0 || failed > 0 else { return nil }
                var parts = ["\(advanced) advanced"]
                if unread > 0 { parts.append("\(unread) unread by every decision backend") }
                if failed > 0 { parts.append("\(failed) failed a stage") }
                return parts.joined(separator: ", ") + "; those stay for the next run"
            }

            /// Why the phase ended early, or nil when it worked through everything.
            public var stopNote: String? {
                switch stopped {
                case nil: nil
                case .cap?: "Stopped at the per-run cap of \(limit) (advance_per_run)."
                case .unreadStreak?: "Stopped early: no decision backend could read several postings in a row."
                case .noneRead?: "Stopped: no decision backend read any posting."
                case .failStreak?: "Stopped early: several postings in a row failed a stage (scoring or explaining)."
                case .noneAdvanced?: "Stopped: no posting advanced; each failed a stage or went unread."
                case .cancelled?: "Stopped: the run was cancelled."
                case .unknown(let raw)?: "Stopped: \(PipelineStatus.bounded(raw, max: 60))."
                }
            }
        }

        /// The decision backends' failed calls in one run, most frequent cause
        /// first (backendErrorReport in cmd/vani/backendlog.go).
        public struct BackendErrors: Decodable, Equatable, Sendable {
            public struct Cause: Decodable, Equatable, Sendable {
                public var backend: String
                /// Calls that failed this way in the run.
                public var count: Int
                /// The first such error as the provider worded it: untrusted
                /// text. Show it through `lines`, which bounds it.
                public var example: String

                public init(backend: String, count: Int, example: String) {
                    self.backend = backend
                    self.count = count
                    self.example = example
                }
            }

            /// A cause ready to show: names and example bounded, one line each.
            public struct Line: Equatable, Sendable {
                public var backend: String
                public var count: Int
                public var example: String
            }

            /// Longest example shown, in characters.
            public static let maxExample = 240

            public var causes: [Cause]
            /// Failed calls whose cause vani did not keep (it keeps at most 20).
            public var unrecorded: Int

            enum CodingKeys: String, CodingKey { case causes, unrecorded }

            public init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                causes = try c.decodeIfPresent([Cause].self, forKey: .causes) ?? []
                unrecorded = try c.decodeIfPresent(Int.self, forKey: .unrecorded) ?? 0
            }

            public init(causes: [Cause], unrecorded: Int = 0) {
                self.causes = causes
                self.unrecorded = unrecorded
            }

            /// Every failed call, recorded cause or not.
            public var totalCalls: Int { causes.reduce(0) { $0 + $1.count } + unrecorded }

            /// "22 failed calls to decision backends"
            public var summary: String {
                "\(totalCalls) failed call\(totalCalls == 1 ? "" : "s") to decision backends"
            }

            /// The first `limit` causes, in vani's order (most frequent first).
            public func lines(limit: Int = 3) -> [Line] {
                causes.prefix(max(limit, 0)).map {
                    Line(backend: PipelineStatus.bounded($0.backend, max: 60), count: $0.count,
                         example: PipelineStatus.bounded($0.example, max: Self.maxExample))
                }
            }

            /// What `lines(limit:)` left out, or nil when it shows everything.
            public func moreNote(limit: Int = 3) -> String? {
                var parts: [String] = []
                let hidden = causes.dropFirst(max(limit, 0))
                if !hidden.isEmpty {
                    let calls = hidden.reduce(0) { $0 + $1.count }
                    parts.append("\(hidden.count) more cause\(hidden.count == 1 ? "" : "s") (\(calls) call\(calls == 1 ? "" : "s"))")
                }
                if unrecorded > 0 {
                    parts.append("\(unrecorded) call\(unrecorded == 1 ? "" : "s") with causes not recorded")
                }
                return parts.isEmpty ? nil : parts.joined(separator: "; ")
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

        // A board that failed or was skipped is a warning even on a run that
        // succeeded: its postings silently stop updating otherwise.
        if lastRun.state != .running, let b = lastRun.boards, b.unreadCount > 0 {
            let names = b.problems(limit: 3).map(\.board).joined(separator: ", ")
            add(.warning, "\(b.unreadCount) board\(b.unreadCount == 1 ? "" : "s") could not be read last run",
                names.isEmpty ? nil : names)
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

    /// "1 eligible posting", "2639 eligible postings": plain numbers, as the
    /// Ledger section and the Jobs tab print them.
    static func postings(_ n: Int, _ adjective: String) -> String {
        "\(n) \(adjective) posting\(n == 1 ? "" : "s")"
    }

    /// Untrusted text made fit for one line: whitespace, control and format
    /// characters (newlines, bidi overrides) become single spaces, and anything
    /// past `max` characters is cut with an ellipsis.
    public static func bounded(_ s: String, max: Int) -> String {
        var out = ""
        var space = false
        for ch in s {
            let blank = ch.isWhitespace || ch.unicodeScalars.contains {
                switch $0.properties.generalCategory {
                case .control, .format, .lineSeparator, .paragraphSeparator: true
                default: false
                }
            }
            if blank {
                space = !out.isEmpty
                continue
            }
            if space { out.append(" "); space = false }
            out.append(ch)
        }
        guard out.count > max else { return out }
        return String(out.prefix(Swift.max(max - 1, 0))) + "…"
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

    public func status() throws -> PipelineStatus {
        let value: PipelineStatus = try VaniProcess.call(vaniBinary, ["status", "--json"], configPath: configPath, stateDir: stateDir,
                                                         timeout: timeout, maxOutputBytes: Self.maxOutputBytes, decoder: JobsClient.decoder,
                                                         tooOld: "this vani is too old for the Pipeline tab; rebuild the app")
        guard value.version == PipelineStatus.supportedVersion else {
            throw BankError("vani reports status version \(value.version); this app reads version \(PipelineStatus.supportedVersion). Rebuild the app.")
        }
        return value
    }
}
