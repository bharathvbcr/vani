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
    /// Whether a daemon holds the ledger; nil from a vani that does not report it.
    public var daemon: Daemon?
    /// The manual run asked of the daemon; nil from a vani that does not report it.
    public var runRequest: RunRequest?
    /// A daily slot the daemon woke too late to run, when it is later than the
    /// last real run. Nil when there is nothing to say, including from an older vani.
    public var lastSkip: Skip?
    /// The last extension-spool poll. Nil when none has been recorded.
    public var inbox: Inbox?

    enum CodingKeys: String, CodingKey {
        case version, config, ledger, schedule, backends, decide, daemon, inbox
        case generatedAt = "generated_at"
        case stateDir = "state_dir"
        case lastRun = "last_run"
        case runRequest = "run_request"
        case lastSkip = "last_skip"
    }

    /// The daemon as daemon.json and the process table show it. A facet that
    /// does not decode reads as not ok, never fails the status read.
    public struct Daemon: Decodable, Equatable, Sendable {
        public var ok: Bool
        public var reason: String?
        /// A daemon is running: a manual run goes through `vani run-request`.
        public var running: Bool
        public var pid: Int?
        public var since: Date?
        /// Whether the decision model can answer in the running daemon; nil when the daemon
        /// did not say (it predates the field, or the config names no OpenRouter model).
        public var model: Model?

        /// `daemon.model`: the key's variable was set (`enabled`) when the daemon started, or why
        /// not (`reason`, vani's words). Never a key.
        public struct Model: Decodable, Equatable, Sendable {
            public var enabled: Bool
            public var keyEnv: String?
            public var reason: String?

            enum CodingKeys: String, CodingKey {
                case enabled, reason
                case keyEnv = "key_env"
            }

            public init(enabled: Bool, keyEnv: String? = nil, reason: String? = nil) {
                self.enabled = enabled
                self.keyEnv = keyEnv
                self.reason = reason
            }
        }

        enum CodingKeys: String, CodingKey { case ok, reason, running, pid, since, model }

        public init(ok: Bool = true, reason: String? = nil, running: Bool, pid: Int? = nil, since: Date? = nil, model: Model? = nil) {
            self.ok = ok
            self.reason = reason
            self.running = running
            self.pid = pid
            self.since = since
            self.model = model
        }

        public init(from decoder: Decoder) throws {
            guard let c = try? decoder.container(keyedBy: CodingKeys.self) else {
                self.init(ok: false, reason: "vani's daemon section did not decode", running: false)
                return
            }
            self.init(ok: (try? c.decode(Bool.self, forKey: .ok)) ?? false,
                      reason: try? c.decodeIfPresent(String.self, forKey: .reason),
                      running: (try? c.decode(Bool.self, forKey: .running)) ?? false,
                      pid: try? c.decodeIfPresent(Int.self, forKey: .pid),
                      since: try? c.decodeIfPresent(Date.self, forKey: .since),
                      // A model section this app cannot read is no claim either way.
                      model: (try? c.decodeIfPresent(Model.self, forKey: .model)).flatMap { $0 })
        }
    }

    /// A manual run waiting for the daemon (`pending`) or being made by it
    /// (`claimed`, while the run record shows its progress).
    public struct RunRequest: Decodable, Equatable, Sendable {
        public var ok: Bool
        public var reason: String?
        public var pending: Bool
        /// A pending request past its hour: the daemon will drop it unrun.
        public var stale: Bool
        public var requestedAt: Date?
        public var options: ManualRunOptions?
        public var claimed: Bool
        public var claimedRequestedAt: Date?

        enum CodingKeys: String, CodingKey {
            case ok, reason, pending, stale, options, claimed
            case requestedAt = "requested_at"
            case claimedRequestedAt = "claimed_requested_at"
        }

        public init(ok: Bool = true, reason: String? = nil, pending: Bool = false, stale: Bool = false, requestedAt: Date? = nil,
                    options: ManualRunOptions? = nil, claimed: Bool = false, claimedRequestedAt: Date? = nil) {
            self.ok = ok
            self.reason = reason
            self.pending = pending
            self.stale = stale
            self.requestedAt = requestedAt
            self.options = options
            self.claimed = claimed
            self.claimedRequestedAt = claimedRequestedAt
        }

        public init(from decoder: Decoder) throws {
            guard let c = try? decoder.container(keyedBy: CodingKeys.self) else {
                self.init(ok: false, reason: "vani's run request section did not decode")
                return
            }
            self.init(ok: (try? c.decode(Bool.self, forKey: .ok)) ?? false,
                      reason: try? c.decodeIfPresent(String.self, forKey: .reason),
                      pending: (try? c.decode(Bool.self, forKey: .pending)) ?? false,
                      stale: (try? c.decodeIfPresent(Bool.self, forKey: .stale)) ?? false,
                      requestedAt: try? c.decodeIfPresent(Date.self, forKey: .requestedAt),
                      options: try? c.decodeIfPresent(ManualRunOptions.self, forKey: .options),
                      claimed: (try? c.decode(Bool.self, forKey: .claimed)) ?? false,
                      claimedRequestedAt: try? c.decodeIfPresent(Date.self, forKey: .claimedRequestedAt))
        }
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

        /// Who started a run (runRecord.Trigger).
        public enum Trigger: Equatable, Sendable {
            /// The daemon's daily schedule.
            case schedule
            /// `vani run-once`: from a shell, or this app's Run Now.
            case manual
            /// A trigger a newer vani adds: shown as written.
            case unknown(String)

            init(raw: String) {
                switch raw {
                case "schedule": self = .schedule
                case "manual": self = .manual
                default: self = .unknown(raw)
                }
            }
        }

        public var ok: Bool
        public var reason: String?
        /// A state a newer vani adds decodes as `.unknown`, never fails the read.
        public var state: State
        /// Nil from a vani that does not record it.
        public var trigger: Trigger?
        /// A manual run's overrides; nil when it had none, or from an older vani.
        /// Display only, so one that does not decode is dropped, never fails the read.
        public var options: ManualRunOptions?
        public var startedAt: Date?
        public var finishedAt: Date?
        public var durationSec: Double?
        /// Where a failed run stopped: pipeline or digest.
        public var stage: String?
        public var error: String?
        /// A failed run's cause code; nil when the run had none of the known
        /// ones, or from a vani that does not record it.
        public var cause: Cause?
        /// How many roles the run's digest listed; nil when no digest was
        /// written, or from a vani that does not record it.
        public var digestRoles: Int?
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
        /// What vani says to announce for this run (cmd/vani/runnotice.go
        /// decideNotice); nil from a vani that sends none.
        public var notice: NoticeWire?
        /// The digest was written but what it showed was not recorded. Empty when
        /// there is nothing to say.
        public var impressionsError: String?
        /// Lines the per-run log dropped after its cap. Zero when none were, or
        /// from a vani that does not record it.
        public var logDropped: Int
        /// Near-duplicate counts. Nil when the stage did not run; a value, even
        /// of zeros, means it did. No posting key or description.
        public var nearDup: NearDup?
        /// Exclusion counts. Nil when the stage did not run.
        public var exclusions: Exclusions?
        /// Logo counts. Nil when the stage did not run. Missing hosts is a count.
        public var logos: Logos?
        /// Set when a configured shadow did not start, did not attach, or missed
        /// calls. Nil when there is nothing to say, including a list-only run.
        public var shadow: Shadow?
        /// The knowledge-base warm of live postings before the advance phase.
        /// Nil when it did not run.
        public var semanticPre: SemanticReport?
        /// The knowledge-base warm after the advance phase. Nil when it did not run.
        public var semantic: SemanticReport?
        /// A digest ranking warning (missing weights, an unreadable knowledge base).
        /// Empty when the digest ranked as configured.
        public var digestWarning: String?
        /// Set when the digest's notes were off or paused. Nil when they ran,
        /// or the run was not going to ask a backend.
        public var explain: Explain?
        /// The run that is going, as `runs/progress.json` reported it. Nil when
        /// vani sent none: the file is absent, or this run is not going. A value
        /// with `ok` false was present and could not be trusted.
        public var progress: Progress?

        /// Live progress of one run (cmd/vani/runprogress.go). Counts and bounded
        /// names only. `headline` and `line` are what the Pipeline banner shows.
        public struct Progress: Decodable, Equatable, Sendable {
            public var ok: Bool
            public var reason: String?
            public var stage: String
            public var phase: String
            public var index: Int
            public var total: Int
            public var done: Int
            public var read: Int
            public var failed: Int
            public var rateLimited: Int
            public var skipped: Int
            public var listed: Int
            /// New ledger rows the boards already finished wrote. Nil when this
            /// snapshot did not report it; 0 is a real zero.
            public var added: Int?
            public var ats: String
            public var token: String
            public var company: String
            /// How the board just finished: read, failed, rate_limited, skipped.
            /// Empty while it is still being read.
            public var outcome: String
            public var attempted: Int
            public var limit: Int
            public var eligible: Int
            public var updatedAt: Date?

            enum CodingKeys: String, CodingKey {
                case ok, reason, stage, phase, index, total, done, read, failed, skipped, listed, added
                case ats, token, company, outcome, attempted, limit, eligible
                case rateLimited = "rate_limited"
                case updatedAt = "updated_at"
            }

            public init(ok: Bool, reason: String? = nil, stage: String = "", phase: String = "",
                        index: Int = 0, total: Int = 0, done: Int = 0, read: Int = 0, failed: Int = 0,
                        rateLimited: Int = 0, skipped: Int = 0, listed: Int = 0, added: Int? = nil,
                        ats: String = "", token: String = "", company: String = "", outcome: String = "",
                        attempted: Int = 0, limit: Int = 0, eligible: Int = 0, updatedAt: Date? = nil) {
                self.ok = ok
                self.reason = reason
                self.stage = stage
                self.phase = phase
                self.index = index
                self.total = total
                self.done = done
                self.read = read
                self.failed = failed
                self.rateLimited = rateLimited
                self.skipped = skipped
                self.listed = listed
                self.added = added
                self.ats = ats
                self.token = token
                self.company = company
                self.outcome = outcome
                self.attempted = attempted
                self.limit = limit
                self.eligible = eligible
                self.updatedAt = updatedAt
            }

            public init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                ok = try c.decodeIfPresent(Bool.self, forKey: .ok) ?? false
                reason = try c.decodeIfPresent(String.self, forKey: .reason)
                stage = try c.decodeIfPresent(String.self, forKey: .stage) ?? ""
                phase = try c.decodeIfPresent(String.self, forKey: .phase) ?? ""
                index = try c.decodeIfPresent(Int.self, forKey: .index) ?? 0
                total = try c.decodeIfPresent(Int.self, forKey: .total) ?? 0
                done = try c.decodeIfPresent(Int.self, forKey: .done) ?? 0
                read = try c.decodeIfPresent(Int.self, forKey: .read) ?? 0
                failed = try c.decodeIfPresent(Int.self, forKey: .failed) ?? 0
                rateLimited = try c.decodeIfPresent(Int.self, forKey: .rateLimited) ?? 0
                skipped = try c.decodeIfPresent(Int.self, forKey: .skipped) ?? 0
                listed = try c.decodeIfPresent(Int.self, forKey: .listed) ?? 0
                added = try c.decodeIfPresent(Int.self, forKey: .added)
                ats = try c.decodeIfPresent(String.self, forKey: .ats) ?? ""
                token = try c.decodeIfPresent(String.self, forKey: .token) ?? ""
                company = try c.decodeIfPresent(String.self, forKey: .company) ?? ""
                outcome = try c.decodeIfPresent(String.self, forKey: .outcome) ?? ""
                attempted = try c.decodeIfPresent(Int.self, forKey: .attempted) ?? 0
                limit = try c.decodeIfPresent(Int.self, forKey: .limit) ?? 0
                eligible = try c.decodeIfPresent(Int.self, forKey: .eligible) ?? 0
                updatedAt = try c.decodeIfPresent(Date.self, forKey: .updatedAt)
            }

            /// The banner title while this snapshot can be trusted. Empty when it
            /// cannot, so the banner keeps "A run is going".
            public var headline: String {
                guard ok else { return "" }
                switch stage {
                case "listing":
                    return listingHeadline
                case "advance":
                    return "Triaging postings"
                case "starting":
                    return "Starting"
                case "preparing":
                    return "Preparing the run"
                case "inbox":
                    return "Reading saved listings"
                case "near_dup":
                    return "Checking near-duplicates"
                case "exclusions":
                    return "Applying exclusions"
                case "logos":
                    return "Fetching company icons"
                case "warm":
                    return "Warming the knowledge base"
                case "backfill":
                    return "Filling in earlier readings"
                case "digest":
                    return "Writing the digest"
                default:
                    return PipelineStatus.bounded(stage, max: 80)
                }
            }

            /// The banner's second line: place and counts. Empty when there is
            /// nothing to add under the title.
            public var line: String {
                guard ok else { return "" }
                switch stage {
                case "listing":
                    return listingLine
                case "advance":
                    return advanceLine
                default:
                    return ""
                }
            }

            /// One row for the last-run panel.
            public var nowText: String {
                if !ok {
                    return "Live progress could not be read: " + PipelineStatus.bounded(reason ?? "unreadable", max: 300)
                }
                return [headline, portalID, line].filter { !$0.isEmpty }.joined(separator: " · ")
            }

            /// How far this stage has got, when the snapshot names a fraction.
            /// Nil for a stage with no count, and when the snapshot cannot be trusted.
            public struct Meter: Equatable, Sendable {
                public var done: Int
                public var total: Int
            }

            /// Whether the current step needs a calmer or a louder tint.
            public enum Attention: Equatable, Sendable {
                /// The run is moving, and the board in hand was read or is still being read.
                case calm
                /// The board in hand was rate-limited.
                case caution
                /// The board in hand failed, or the snapshot could not be read.
                case problem
            }

            public var meter: Meter? {
                guard ok else { return nil }
                switch stage {
                case "listing":
                    guard total > 0 else { return nil }
                    return Meter(done: min(max(index, 0), total), total: total)
                case "advance":
                    guard limit > 0 else { return nil }
                    return Meter(done: min(max(attempted, 0), limit), total: limit)
                default:
                    return nil
                }
            }

            /// "2 of 5", "5 boards", or the triage fraction. Empty when this stage has no place.
            public var placeText: String {
                guard ok else { return "" }
                switch stage {
                case "listing":
                    if index > 0, total > 0 { return "\(index) of \(total)" }
                    if total > 0 { return "\(total) boards" }
                    return ""
                case "advance":
                    return advanceLine
                default:
                    return ""
                }
            }

            /// Counts only, so a bar can carry the fraction beside them.
            public var countsText: String {
                guard ok, stage == "listing" else { return "" }
                return countParts.joined(separator: " · ")
            }

            /// The board id under a company title. Empty when the title already is that id.
            public var portalID: String {
                guard ok, !company.isEmpty, !ats.isEmpty else { return "" }
                return token.isEmpty ? ats : "\(ats):\(token)"
            }

            /// Set while a board's listings are being saved, which can sit there a while.
            public var phaseNote: String {
                guard ok, stage == "listing", phase == "reconciling" else { return "" }
                return "saving the listing"
            }

            public var attention: Attention {
                guard ok else { return .problem }
                switch outcome {
                case "failed": return .problem
                case "rate_limited": return .caution
                default: return .calm
                }
            }

            /// How long this snapshot has been the current one. Empty when it
            /// cannot be trusted or names no time. Under five seconds is "just
            /// now", so a clock that is a little ahead does not print a negative age.
            public func updatedText(at now: Date) -> String {
                guard ok, let updatedAt else { return "" }
                let s = now.timeIntervalSince(updatedAt)
                if !s.isFinite || s < 5 { return "Updated just now." }
                if s < 90 { return "Updated \(Int(s)) s ago." }
                return "Updated \(PipelineStatus.age(s)) ago."
            }

            private var countParts: [String] {
                var parts: [String] = []
                func add(_ n: Int, _ word: String) {
                    if n > 0 { parts.append("\(n) \(word)") }
                }
                add(read, "read")
                add(failed, "failed")
                add(rateLimited, "rate-limited")
                add(skipped, "skipped")
                add(listed, "listed")
                return parts
            }

            private var portalName: String {
                if !company.isEmpty { return company }
                if ats.isEmpty { return "" }
                return token.isEmpty ? ats : "\(ats):\(token)"
            }

            private var listingHeadline: String {
                let verb: String
                switch phase {
                case "reconciling": verb = "Saving"
                case "done":
                    switch outcome {
                    case "failed": verb = "Could not read"
                    case "rate_limited": verb = "Rate-limited"
                    case "skipped": verb = "Skipped"
                    default: verb = "Read"
                    }
                default: verb = "Reading"
                }
                let name = portalName
                if name.isEmpty {
                    return index == 0 ? "Reading boards" : "Reading a board"
                }
                return "\(verb) \(name)"
            }

            private var listingLine: String {
                var parts: [String] = []
                if phase == "reconciling" { parts.append("saving the listing") }
                if !placeText.isEmpty { parts.append(placeText) }
                parts.append(contentsOf: countParts)
                return parts.joined(separator: " · ")
            }

            private var advanceLine: String {
                guard limit > 0 else { return "" }
                var s = "\(attempted) of \(limit)"
                if eligible > limit { s += " · \(eligible) eligible" }
                return s
            }
        }

        enum CodingKeys: String, CodingKey {
            case ok, reason, state, stage, error, cause, boards, advance, trigger, options, notice
            case exclusions, logos, shadow, semantic, explain
            case digestRoles = "digest_roles"
            case digestWarning = "digest_warning"
            case semanticPre = "semantic_pre"
            case startedAt = "started_at"
            case finishedAt = "finished_at"
            case durationSec = "duration_sec"
            case postingsBefore = "postings_before"
            case postingsAfter = "postings_after"
            case countsBefore = "counts_before"
            case countsAfter = "counts_after"
            case backendErrors = "backend_errors"
            case impressionsError = "impressions_error"
            case logDropped = "log_dropped"
            case nearDup = "near_dup"
            case progress
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            ok = try c.decode(Bool.self, forKey: .ok)
            reason = try c.decodeIfPresent(String.self, forKey: .reason)
            state = (try? c.decode(State.self, forKey: .state)) ?? .unknown
            trigger = try c.decodeIfPresent(String.self, forKey: .trigger).flatMap { $0.isEmpty ? nil : Trigger(raw: $0) }
            options = (try? c.decodeIfPresent(ManualRunOptions.self, forKey: .options)) ?? nil
            startedAt = try c.decodeIfPresent(Date.self, forKey: .startedAt)
            finishedAt = try c.decodeIfPresent(Date.self, forKey: .finishedAt)
            durationSec = try c.decodeIfPresent(Double.self, forKey: .durationSec)
            stage = try c.decodeIfPresent(String.self, forKey: .stage)
            error = try c.decodeIfPresent(String.self, forKey: .error)
            cause = try c.decodeIfPresent(String.self, forKey: .cause).flatMap { $0.isEmpty ? nil : Cause(raw: $0) }
            digestRoles = try c.decodeIfPresent(Int.self, forKey: .digestRoles)
            postingsBefore = try c.decodeIfPresent(Int.self, forKey: .postingsBefore)
            postingsAfter = try c.decodeIfPresent(Int.self, forKey: .postingsAfter)
            countsBefore = try c.decodeIfPresent([String: Int].self, forKey: .countsBefore)
            countsAfter = try c.decodeIfPresent([String: Int].self, forKey: .countsAfter)
            boards = try c.decodeIfPresent(Boards.self, forKey: .boards)
            advance = try c.decodeIfPresent(Advance.self, forKey: .advance)
            backendErrors = try c.decodeIfPresent(BackendErrors.self, forKey: .backendErrors)
            // Present but not readable is a notice this app cannot trust, not an absent one.
            notice = c.contains(.notice) ? ((try? c.decode(NoticeWire.self, forKey: .notice)) ?? .unsupported("malformed")) : nil
            impressionsError = try c.decodeIfPresent(String.self, forKey: .impressionsError)
            logDropped = try c.decodeIfPresent(Int.self, forKey: .logDropped) ?? 0
            nearDup = try c.decodeIfPresent(NearDup.self, forKey: .nearDup)
            exclusions = try c.decodeIfPresent(Exclusions.self, forKey: .exclusions)
            logos = try c.decodeIfPresent(Logos.self, forKey: .logos)
            shadow = try c.decodeIfPresent(Shadow.self, forKey: .shadow)
            semanticPre = try c.decodeIfPresent(SemanticReport.self, forKey: .semanticPre)
            semantic = try c.decodeIfPresent(SemanticReport.self, forKey: .semantic)
            digestWarning = try c.decodeIfPresent(String.self, forKey: .digestWarning)
            explain = try c.decodeIfPresent(Explain.self, forKey: .explain)
            // Present but unreadable is a failed check, not an absent snapshot,
            // and it must not fail the rest of the status. Null is absent.
            if c.contains(.progress), try c.decodeNil(forKey: .progress) == false {
                progress = (try? c.decode(Progress.self, forKey: .progress))
                    ?? Progress(ok: false, reason: "Live progress could not be read")
            } else {
                progress = nil
            }
        }

        public init(ok: Bool = true, reason: String? = nil, state: State, startedAt: Date? = nil, finishedAt: Date? = nil,
                    durationSec: Double? = nil, stage: String? = nil, error: String? = nil,
                    cause: Cause? = nil, digestRoles: Int? = nil,
                    postingsBefore: Int? = nil, postingsAfter: Int? = nil,
                    countsBefore: [String: Int]? = nil, countsAfter: [String: Int]? = nil,
                    boards: Boards? = nil, advance: Advance? = nil, backendErrors: BackendErrors? = nil,
                    trigger: Trigger? = nil, options: ManualRunOptions? = nil, notice: NoticeWire? = nil,
                    impressionsError: String? = nil, logDropped: Int = 0,
                    nearDup: NearDup? = nil, exclusions: Exclusions? = nil, logos: Logos? = nil, shadow: Shadow? = nil,
                    semanticPre: SemanticReport? = nil, semantic: SemanticReport? = nil,
                    digestWarning: String? = nil, explain: Explain? = nil, progress: Progress? = nil) {
            self.notice = notice
            self.trigger = trigger
            self.options = options
            self.cause = cause
            self.digestRoles = digestRoles
            self.countsBefore = countsBefore
            self.countsAfter = countsAfter
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
            self.impressionsError = impressionsError
            self.logDropped = logDropped
            self.nearDup = nearDup
            self.exclusions = exclusions
            self.logos = logos
            self.shadow = shadow
            self.semanticPre = semanticPre
            self.semantic = semantic
            self.digestWarning = digestWarning
            self.explain = explain
            self.progress = progress
        }

        /// Why a failed run failed, when vani names it (cmd/vani/runrecord.go
        /// runCause): set from the error's identity, never its text.
        public enum Cause: Equatable, Sendable {
            /// The ledger could not be written safely, so the daemon stopped.
            case ledgerFatal
            /// The Jobs marks could not be read, so the digest was held back.
            case digestHeldBack
            /// The config could not be re-read; the run used the previous watchlist.
            case configStale
            /// A cause a newer vani adds: worded as the stage's default.
            case unknown(String)

            init(raw: String) {
                switch raw {
                case "ledger_fatal": self = .ledgerFatal
                case "digest_held_back": self = .digestHeldBack
                case "config_stale": self = .configStale
                default: self = .unknown(raw)
                }
            }
        }

        /// Near-duplicate counts (nearDupSummary). A value means the stage ran.
        public struct NearDup: Decodable, Equatable, Sendable {
            public var records: Int
            public var examined: Int
            public var compared: Int
            public var duplicates: Int
            public var siblings: Int
            public var candidates: Int
            public var partial: Bool
            public var similarityErrors: Int
            public var similaritySkipped: Int
            public var note: String?
            /// The cosine did not load, so the stage ran on shingles alone.
            /// Empty when the cosine ran, and when semantic matching is off.
            public var similarityOff: String?
            /// The vector cache could not be used or saved. The cosine still ran.
            public var similarityNote: String?

            enum CodingKeys: String, CodingKey {
                case records, examined, compared, duplicates, siblings, candidates, partial, note
                case similarityErrors = "similarity_errors"
                case similaritySkipped = "similarity_skipped"
                case similarityOff = "similarity_off"
                case similarityNote = "similarity_note"
            }

            public init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                records = try c.decodeIfPresent(Int.self, forKey: .records) ?? 0
                examined = try c.decodeIfPresent(Int.self, forKey: .examined) ?? 0
                compared = try c.decodeIfPresent(Int.self, forKey: .compared) ?? 0
                duplicates = try c.decodeIfPresent(Int.self, forKey: .duplicates) ?? 0
                siblings = try c.decodeIfPresent(Int.self, forKey: .siblings) ?? 0
                candidates = try c.decodeIfPresent(Int.self, forKey: .candidates) ?? 0
                partial = try c.decodeIfPresent(Bool.self, forKey: .partial) ?? false
                similarityErrors = try c.decodeIfPresent(Int.self, forKey: .similarityErrors) ?? 0
                similaritySkipped = try c.decodeIfPresent(Int.self, forKey: .similaritySkipped) ?? 0
                note = try c.decodeIfPresent(String.self, forKey: .note)
                similarityOff = try c.decodeIfPresent(String.self, forKey: .similarityOff)
                similarityNote = try c.decodeIfPresent(String.self, forKey: .similarityNote)
            }

            /// A deadline cut the stage, a similarity reading failed, or the
            /// cosine did not load. A cache note is not one of these.
            public var isFailure: Bool { partial || similarityErrors > 0 || !(similarityOff ?? "").isEmpty }

            public var summary: String {
                if isFailure {
                    var parts: [String] = []
                    if partial || similarityErrors > 0 {
                        parts.append("partial=\(partial), \(similarityErrors) similarity error(s)")
                    }
                    if let note, !note.isEmpty { parts.append(PipelineStatus.bounded(note, max: 200)) }
                    if let similarityOff, !similarityOff.isEmpty { parts.append(PipelineStatus.bounded(similarityOff, max: 200)) }
                    return parts.joined(separator: ": ")
                }
                var line = "finished (\(compared) compared)"
                if let similarityNote, !similarityNote.isEmpty {
                    line += "; " + PipelineStatus.bounded(similarityNote, max: 200)
                }
                return line
            }
        }

        /// Exclusion counts (exclusionSummary). Unread and a description that could
        /// not be read back are checks that did not run, which is not a pass.
        public struct Exclusions: Decodable, Equatable, Sendable {
            public var dropped: Int
            public var reopened: Int
            public var unread: Int
            public var bodyUnavailable: Int

            enum CodingKeys: String, CodingKey {
                case dropped, reopened, unread
                case bodyUnavailable = "body_unavailable"
            }

            public init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                dropped = try c.decodeIfPresent(Int.self, forKey: .dropped) ?? 0
                reopened = try c.decodeIfPresent(Int.self, forKey: .reopened) ?? 0
                unread = try c.decodeIfPresent(Int.self, forKey: .unread) ?? 0
                bodyUnavailable = try c.decodeIfPresent(Int.self, forKey: .bodyUnavailable) ?? 0
            }

            public var isFailure: Bool { bodyUnavailable > 0 || unread > 0 }

            public var summary: String {
                if isFailure {
                    return "\(bodyUnavailable) description(s) could not be read back, \(unread) location check(s) could not run"
                }
                if dropped == 0, reopened == 0 { return "finished" }
                return "\(dropped) dropped, \(reopened) reopened"
            }
        }

        /// Logo counts (logoSummary). Missing hosts is how many were not asked.
        public struct Logos: Decodable, Equatable, Sendable {
            public var fetched: Int
            public var fresh: Int
            public var waiting: Int
            public var failed: Int
            public var deferred: Int
            public var missingHosts: Int
            public var stateReset: String?
            public var example: String?

            enum CodingKeys: String, CodingKey {
                case fetched, fresh, waiting, failed, deferred, example
                case missingHosts = "missing_hosts"
                case stateReset = "state_reset"
            }

            public init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                fetched = try c.decodeIfPresent(Int.self, forKey: .fetched) ?? 0
                fresh = try c.decodeIfPresent(Int.self, forKey: .fresh) ?? 0
                waiting = try c.decodeIfPresent(Int.self, forKey: .waiting) ?? 0
                failed = try c.decodeIfPresent(Int.self, forKey: .failed) ?? 0
                deferred = try c.decodeIfPresent(Int.self, forKey: .deferred) ?? 0
                missingHosts = try c.decodeIfPresent(Int.self, forKey: .missingHosts) ?? 0
                stateReset = try c.decodeIfPresent(String.self, forKey: .stateReset)
                example = try c.decodeIfPresent(String.self, forKey: .example)
            }

            public var isFailure: Bool { failed > 0 || !(stateReset ?? "").isEmpty }

            public var summary: String {
                if isFailure {
                    var line = "\(failed) failed"
                    if let example, !example.isEmpty { line += ": " + PipelineStatus.bounded(example, max: 200) }
                    if let stateReset, !stateReset.isEmpty { line += "; state reset: " + PipelineStatus.bounded(stateReset, max: 200) }
                    return line
                }
                return "\(fetched) fetched, \(fresh) fresh"
            }
        }

        /// A configured shadow that did not do what the run asked (shadowNote).
        public struct Shadow: Decodable, Equatable, Sendable {
            public var local: String?
            /// Why a configured shadow never attached. One line.
            public var note: String?
            /// Calls or slots that failed, timed out, or came back unusable.
            public var errors: Int
            /// Sampled postings skipped while the shadow was busy.
            public var busySkips: Int

            public init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                local = try c.decodeIfPresent(String.self, forKey: .local)
                note = try c.decodeIfPresent(String.self, forKey: .note)
                errors = try c.decodeIfPresent(Int.self, forKey: .errors) ?? 0
                busySkips = try c.decodeIfPresent(Int.self, forKey: .busySkips) ?? 0
            }

            enum CodingKeys: String, CodingKey {
                case local, note, errors
                case busySkips = "busy_skips"
            }

            public var isFailure: Bool {
                !(local ?? "").isEmpty || !(note ?? "").isEmpty || errors > 0 || busySkips > 0
            }

            public var summary: String {
                var parts: [String] = []
                if let local, !local.isEmpty {
                    parts.append("Local decision model not started: " + PipelineStatus.bounded(local, max: 200))
                }
                if let note, !note.isEmpty { parts.append(PipelineStatus.bounded(note, max: 200)) }
                if errors > 0 || busySkips > 0 {
                    parts.append("\(errors) call(s) or slot(s) failed, \(busySkips) sampled posting(s) skipped while busy")
                }
                return parts.joined(separator: "; ")
            }
        }

        /// One knowledge-base warm (semanticReport). A value means it ran.
        public struct SemanticReport: Decodable, Equatable, Sendable {
            public var ran: Bool
            public var scored: Int
            public var cached: Int
            public var failed: Int
            public var skipped: Int
            public var unavailable: Bool
            public var canceled: Bool
            public var rebuilt: Bool
            public var note: String?

            enum CodingKeys: String, CodingKey {
                case ran, scored, cached, failed, skipped, unavailable, canceled, note
                case rebuilt = "cache_rebuilt"
            }

            public init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                ran = try c.decodeIfPresent(Bool.self, forKey: .ran) ?? false
                scored = try c.decodeIfPresent(Int.self, forKey: .scored) ?? 0
                cached = try c.decodeIfPresent(Int.self, forKey: .cached) ?? 0
                failed = try c.decodeIfPresent(Int.self, forKey: .failed) ?? 0
                skipped = try c.decodeIfPresent(Int.self, forKey: .skipped) ?? 0
                unavailable = try c.decodeIfPresent(Bool.self, forKey: .unavailable) ?? false
                canceled = try c.decodeIfPresent(Bool.self, forKey: .canceled) ?? false
                rebuilt = try c.decodeIfPresent(Bool.self, forKey: .rebuilt) ?? false
                note = try c.decodeIfPresent(String.self, forKey: .note)
            }

            /// A warm that did not do what it was asked. Spending the warm
            /// budget is not one of these.
            public var isFailure: Bool {
                if unavailable || failed > 0 || canceled { return true }
                let n = (note ?? "").lowercased()
                return n.contains("not run") || n.contains("stopped early")
                    || n.contains("stored description unreadable") || n.contains("marks could not be read")
            }

            public var summary: String {
                if isFailure {
                    if let note, !note.isEmpty { return PipelineStatus.bounded(note, max: 200) }
                    return "scored \(scored), failed \(failed)"
                }
                if let note, !note.isEmpty { return PipelineStatus.bounded(note, max: 200) }
                if scored == 0, cached == 0 { return "finished" }
                return "scored \(scored), cached \(cached)"
            }
        }

        /// The digest's notes did not run (explainNote).
        public struct Explain: Decodable, Equatable, Sendable {
            public var mode: String?
            public var note: String?

            public init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                mode = try c.decodeIfPresent(String.self, forKey: .mode)
                note = try c.decodeIfPresent(String.self, forKey: .note)
            }

            enum CodingKeys: String, CodingKey { case mode, note }

            public var isFailure: Bool { mode == "off" || mode == "paused" }

            public var summary: String {
                if let note, !note.isEmpty { return PipelineStatus.bounded(note, max: 500) }
                switch mode {
                case "off": return "Notes off"
                case "paused": return "Notes paused"
                default: return "Notes"
                }
            }
        }

        /// Whether the run has anything to report. Same rule as
        /// cmd/vani/runscmd.go runHasFailures: a failed or interrupted run, a run
        /// error, a board that failed to list, a pay fetch that failed, a
        /// backend error, or a stage that could not finish its check. It decides
        /// whether Copy failures is offered. A last run vani could not read has
        /// none to report. Coverage unknown, a notes floor or budget, deferred
        /// or gone details, a skipped pay fetch, and a spent warm budget are
        /// shown and are not failures.
        public var hasFailures: Bool {
            guard ok else { return false }
            if state == .failed || state == .interrupted { return true }
            if let e = error, !e.isEmpty { return true }
            if let b = boards, b.failed > 0 || b.payFailed > 0 || b.items.contains(where: { $0.outcome == .failed || $0.outcome == .rateLimited }) { return true }
            if let e = impressionsError, !e.isEmpty { return true }
            if logDropped > 0 { return true }
            if shadow?.isFailure == true { return true }
            if exclusions?.isFailure == true { return true }
            if logos?.isFailure == true { return true }
            if nearDup?.isFailure == true { return true }
            if semantic?.isFailure == true || semanticPre?.isFailure == true { return true }
            if let w = digestWarning, !w.isEmpty { return true }
            if explain?.isFailure == true { return true }
            if advance?.failOpen.isFailure == true || advance?.notes.isFailure == true { return true }
            if let be = backendErrors, !be.causes.isEmpty || be.unrecorded > 0 { return true }
            return false
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
                    /// The host kept answering HTTP 429 through vani's retries: not broken,
                    /// the board's postings were left as they were and it is tried next run.
                    case rateLimited
                    /// No adapter, or the careers-page source was disabled this run.
                    case skipped
                    /// An outcome a newer vani adds: shown as written, never as read.
                    case unknown(String)

                    init(raw: String) {
                        switch raw {
                        case "read": self = .read
                        case "failed": self = .failed
                        case "rate_limited": self = .rateLimited
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
                /// New Workday rows past the detail cap: not stored.
                public var deferred: Int
                /// Greenhouse pay requests that did not land.
                public var payFailed: Int
                public var paySkipped: Int
                public var payError: String?
                /// Details that answered 404 under a listing that succeeded.
                public var gone: Int
                /// `reconcile.new`: new ledger rows this board wrote. Nil when the
                /// board was not read. A read board whose key was omitted wrote 0.
                public var reconcileNew: Int?

                private struct ReconcileWire: Decodable {
                    var new: Int?
                    enum CodingKeys: String, CodingKey { case new }
                }

                enum CodingKeys: String, CodingKey {
                    case ats, token, company, outcome, listed, error, deferred, gone, reconcile
                    case payFailed = "pay_failed"
                    case paySkipped = "pay_skipped"
                    case payError = "pay_error"
                }

                public init(from decoder: Decoder) throws {
                    let c = try decoder.container(keyedBy: CodingKeys.self)
                    ats = try c.decode(String.self, forKey: .ats)
                    token = try c.decode(String.self, forKey: .token)
                    company = try c.decodeIfPresent(String.self, forKey: .company)
                    outcome = Outcome(raw: try c.decode(String.self, forKey: .outcome))
                    listed = try c.decodeIfPresent(Int.self, forKey: .listed)
                    error = try c.decodeIfPresent(String.self, forKey: .error)
                    deferred = try c.decodeIfPresent(Int.self, forKey: .deferred) ?? 0
                    payFailed = try c.decodeIfPresent(Int.self, forKey: .payFailed) ?? 0
                    paySkipped = try c.decodeIfPresent(Int.self, forKey: .paySkipped) ?? 0
                    payError = try c.decodeIfPresent(String.self, forKey: .payError)
                    gone = try c.decodeIfPresent(Int.self, forKey: .gone) ?? 0
                    if let wire = try c.decodeIfPresent(ReconcileWire.self, forKey: .reconcile) {
                        reconcileNew = wire.new ?? 0
                    } else {
                        reconcileNew = nil
                    }
                }

                public init(ats: String, token: String, company: String? = nil, outcome: Outcome, listed: Int? = nil, error: String? = nil,
                            deferred: Int = 0, payFailed: Int = 0, paySkipped: Int = 0, payError: String? = nil, gone: Int = 0,
                            reconcileNew: Int? = nil) {
                    self.ats = ats
                    self.token = token
                    self.company = company
                    self.outcome = outcome
                    self.listed = listed
                    self.error = error
                    self.deferred = deferred
                    self.payFailed = payFailed
                    self.paySkipped = paySkipped
                    self.payError = payError
                    self.gone = gone
                    self.reconcileNew = reconcileNew
                }

                /// The board and its error as one line, "ats:token (Company): error": what
                /// a row's Copy error puts on the pasteboard. It is the line vani's failure
                /// report (`vani runs failures`, cmd/vani/runscmd.go boardLine) prints for
                /// the board, and a contract test holds the two equal; the report itself is
                /// vani's, never worded here. The error is untrusted text, kept to one line
                /// of `FailureReport.maxLine` characters.
                public var copyLine: String {
                    let name = company.flatMap { $0.isEmpty ? nil : " (\($0))" } ?? ""
                    let why = PipelineStatus.bounded(error ?? "", max: FailureReport.maxLine)
                    return PipelineStatus.bounded("\(ats):\(token)\(name): \(why.isEmpty ? "none" : why)", max: FailureReport.maxLine)
                }
            }

            /// A board that was not read, ready to show: one bounded line each.
            public struct Line: Equatable, Sendable {
                /// "Intel (wd intel/External)"
                public var board: String
                /// "failed", "rate limited", "skipped", or a newer vani's outcome as written.
                public var outcome: String
                public var error: String
                /// What Copy error copies: the board's `Item.copyLine`, its whole error.
                public var copyText: String
            }

            /// Longest error shown, in characters.
            public static let maxError = 240

            public var configured: Int
            public var read: Int
            /// Every board whose listing failed, the rate-limited ones included.
            public var failed: Int
            /// The part of `failed` the host rate limited (HTTP 429); 0 from a vani that
            /// does not report it.
            public var rateLimited: Int
            public var skipped: Int
            /// Boards never tried: the run ended before them.
            public var notReached: Int
            /// Postings the read boards listed, together.
            public var listed: Int
            /// Detail and pay counts for the whole listing. They stay exact when
            /// `items` is cut. A pay failure leaves no pay line.
            public var deferred: Int
            public var payFailed: Int
            public var paySkipped: Int
            public var gone: Int
            public var payError: String?
            public var items: [Item]
            /// Tried boards vani left out of `items`.
            public var unrecorded: Int
            /// New ledger rows every tried board wrote, including ones `items`
            /// left out. Nil when this vani did not report it; 0 is a real zero.
            public var added: Int?

            enum CodingKeys: String, CodingKey {
                case configured, read, failed, skipped, listed, items, unrecorded, deferred, gone, added
                case notReached = "not_reached"
                case rateLimited = "rate_limited"
                case payFailed = "pay_failed"
                case paySkipped = "pay_skipped"
                case payError = "pay_error"
            }

            public init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                configured = try c.decode(Int.self, forKey: .configured)
                read = try c.decode(Int.self, forKey: .read)
                failed = try c.decode(Int.self, forKey: .failed)
                rateLimited = try c.decodeIfPresent(Int.self, forKey: .rateLimited) ?? 0
                skipped = try c.decode(Int.self, forKey: .skipped)
                listed = try c.decode(Int.self, forKey: .listed)
                notReached = try c.decodeIfPresent(Int.self, forKey: .notReached) ?? 0
                deferred = try c.decodeIfPresent(Int.self, forKey: .deferred) ?? 0
                payFailed = try c.decodeIfPresent(Int.self, forKey: .payFailed) ?? 0
                paySkipped = try c.decodeIfPresent(Int.self, forKey: .paySkipped) ?? 0
                gone = try c.decodeIfPresent(Int.self, forKey: .gone) ?? 0
                payError = try c.decodeIfPresent(String.self, forKey: .payError)
                items = try c.decodeIfPresent([Item].self, forKey: .items) ?? []
                unrecorded = try c.decodeIfPresent(Int.self, forKey: .unrecorded) ?? 0
                added = try c.decodeIfPresent(Int.self, forKey: .added)
            }

            public init(configured: Int, read: Int, failed: Int = 0, rateLimited: Int = 0, skipped: Int = 0, notReached: Int = 0,
                        listed: Int, items: [Item] = [], unrecorded: Int = 0, added: Int? = nil,
                        deferred: Int = 0, payFailed: Int = 0, paySkipped: Int = 0, gone: Int = 0, payError: String? = nil) {
                self.configured = configured
                self.read = read
                self.failed = failed
                self.rateLimited = rateLimited
                self.skipped = skipped
                self.notReached = notReached
                self.listed = listed
                self.items = items
                self.unrecorded = unrecorded
                self.added = added
                self.deferred = deferred
                self.payFailed = payFailed
                self.paySkipped = paySkipped
                self.gone = gone
                self.payError = payError
            }

            /// Boards tried but not read: failed and skipped.
            public var unreadCount: Int { failed + skipped }

            /// "17 of 18 boards read; 412 postings listed"
            public var summary: String {
                "\(read) of \(configured) board\(configured == 1 ? "" : "s") read; \(listed) posting\(listed == 1 ? "" : "s") listed"
            }

            /// Detail and pay counts, or nil when this listing fetched everything
            /// it listed. Pay failures are the only part that is a failure.
            public var fetchSummary: String? {
                var parts: [String] = []
                if deferred > 0 { parts.append("\(deferred) deferred") }
                if payFailed > 0 { parts.append("\(payFailed) pay failed") }
                if paySkipped > 0 { parts.append("\(paySkipped) pay skipped") }
                if gone > 0 { parts.append("\(gone) gone") }
                guard !parts.isEmpty else { return nil }
                return parts.joined(separator: ", ")
            }

            /// How the boards that were not read went, or nil when every board was read.
            public var outcome: String? {
                var parts: [String] = []
                if failed > 0 {
                    // A rate-limited board is counted in `failed`; name the part that is
                    // the host throttling vani rather than the board being broken.
                    let limited = min(max(rateLimited, 0), failed)
                    parts.append(limited > 0 ? "\(failed) failed (\(limited) rate limited, tried again next run)" : "\(failed) failed")
                }
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
                    case .rateLimited: "rate limited"
                    case .skipped: "skipped"
                    case .unknown(let raw): PipelineStatus.bounded(raw, max: 30)
                    }
                    return Line(board: PipelineStatus.bounded(name.map { "\($0) (\(where_))" } ?? where_, max: 80),
                                outcome: outcome,
                                error: PipelineStatus.bounded(item.error ?? "", max: Self.maxError),
                                copyText: item.copyLine)
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

            /// Postings kept because a check could not run (FailOpenCounts).
            /// Coverage unknown is a check that ran and found no must-haves.
            public struct FailOpen: Decodable, Equatable, Sendable {
                public var emptyDescription: Int
                public var titleUnread: Int
                public var locationUnreadable: Int
                public var prescoreFailed: Int
                public var coverageUnknown: Int
                public var other: Int

                enum CodingKeys: String, CodingKey {
                    case emptyDescription = "empty_description"
                    case titleUnread = "title_unread"
                    case locationUnreadable = "location_unreadable"
                    case prescoreFailed = "prescore_failed"
                    case coverageUnknown = "coverage_unknown"
                    case other
                }

                public init(emptyDescription: Int = 0, titleUnread: Int = 0, locationUnreadable: Int = 0,
                            prescoreFailed: Int = 0, coverageUnknown: Int = 0, other: Int = 0) {
                    self.emptyDescription = emptyDescription
                    self.titleUnread = titleUnread
                    self.locationUnreadable = locationUnreadable
                    self.prescoreFailed = prescoreFailed
                    self.coverageUnknown = coverageUnknown
                    self.other = other
                }

                public init(from decoder: Decoder) throws {
                    let c = try decoder.container(keyedBy: CodingKeys.self)
                    self.init(
                        emptyDescription: try c.decodeIfPresent(Int.self, forKey: .emptyDescription) ?? 0,
                        titleUnread: try c.decodeIfPresent(Int.self, forKey: .titleUnread) ?? 0,
                        locationUnreadable: try c.decodeIfPresent(Int.self, forKey: .locationUnreadable) ?? 0,
                        prescoreFailed: try c.decodeIfPresent(Int.self, forKey: .prescoreFailed) ?? 0,
                        coverageUnknown: try c.decodeIfPresent(Int.self, forKey: .coverageUnknown) ?? 0,
                        other: try c.decodeIfPresent(Int.self, forKey: .other) ?? 0)
                }

                /// Checks that did not run. Coverage unknown is not one of them.
                public var unchecked: Int { emptyDescription + titleUnread + locationUnreadable + prescoreFailed + other }
                public var isFailure: Bool { unchecked > 0 }

                public var summary: String? {
                    var parts: [String] = []
                    func add(_ n: Int, _ label: String) { if n > 0 { parts.append("\(n) \(label)") } }
                    add(emptyDescription, "empty description")
                    add(titleUnread, "title unread")
                    add(locationUnreadable, "location unreadable")
                    add(prescoreFailed, "prescore failed")
                    add(coverageUnknown, "coverage unknown")
                    add(other, "other")
                    guard !parts.isEmpty else { return nil }
                    return "Could not check: " + parts.joined(separator: ", ")
                }
            }

            /// What the notes gate did (NotesCounts). Below the floor and a spent
            /// budget are gates. Unknown relevance is a check that could not run.
            public struct Notes: Decodable, Equatable, Sendable {
                public var belowFloor: Int
                public var unknownRelevance: Int
                public var budgetSpent: Int

                enum CodingKeys: String, CodingKey {
                    case belowFloor = "below_floor"
                    case unknownRelevance = "unknown_relevance"
                    case budgetSpent = "budget_spent"
                }

                public init(belowFloor: Int = 0, unknownRelevance: Int = 0, budgetSpent: Int = 0) {
                    self.belowFloor = belowFloor
                    self.unknownRelevance = unknownRelevance
                    self.budgetSpent = budgetSpent
                }

                public init(from decoder: Decoder) throws {
                    let c = try decoder.container(keyedBy: CodingKeys.self)
                    self.init(
                        belowFloor: try c.decodeIfPresent(Int.self, forKey: .belowFloor) ?? 0,
                        unknownRelevance: try c.decodeIfPresent(Int.self, forKey: .unknownRelevance) ?? 0,
                        budgetSpent: try c.decodeIfPresent(Int.self, forKey: .budgetSpent) ?? 0)
                }

                public var isFailure: Bool { unknownRelevance > 0 }

                public var summary: String? {
                    var parts: [String] = []
                    if belowFloor > 0 { parts.append("\(belowFloor) below the floor") }
                    if unknownRelevance > 0 { parts.append("\(unknownRelevance) relevance unknown") }
                    if budgetSpent > 0 { parts.append("\(budgetSpent) notes budget spent") }
                    guard !parts.isEmpty else { return nil }
                    return "Notes gate: " + parts.joined(separator: ", ")
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
            public var failOpen: FailOpen
            public var notes: Notes

            enum CodingKeys: String, CodingKey {
                case eligible, limit, attempted, unread, failed, deferred, stopped, notes
                case failOpen = "fail_open"
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
                failOpen = try c.decodeIfPresent(FailOpen.self, forKey: .failOpen) ?? FailOpen()
                notes = try c.decodeIfPresent(Notes.self, forKey: .notes) ?? Notes()
            }

            public init(eligible: Int, limit: Int, attempted: Int, unread: Int = 0, failed: Int = 0, deferred: Int, stopped: Stop? = nil,
                        failOpen: FailOpen = FailOpen(), notes: Notes = Notes()) {
                self.eligible = eligible
                self.limit = limit
                self.attempted = attempted
                self.unread = unread
                self.failed = failed
                self.deferred = deferred
                self.stopped = stopped
                self.failOpen = failOpen
                self.notes = notes
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
        /// Whether a daemon is running to fire the schedule. Nil from a vani
        /// that does not report it, and when vani could not tell: never read
        /// nil as kept or as not kept.
        public var kept: Bool?
        /// What the schedule not being kept has cost; present only when `kept`
        /// is false.
        public var unkept: Unkept?

        enum CodingKeys: String, CodingKey {
            case ok, reason, timezone, kept, unkept
            case dailyAt = "daily_at"
            case nextRun = "next_run"
        }

        /// The schedule is configured but nothing fires it.
        public var isUnkept: Bool { ok && kept == false }

        /// `vani status --json`'s `schedule.unkept`. Decoded leniently: a field
        /// that does not decode is absent, never a failed status read.
        public struct Unkept: Decodable, Equatable, Sendable {
            /// Why no daemon is keeping the schedule.
            public var why: String?
            public var lastRunAt: Date?
            /// The most recent scheduled slot, the one a daemon would have run.
            public var dueAt: Date?
            /// No run has started since `dueAt`; nil when the last run could not be read.
            public var overdue: Bool?
            /// Live postings no run has triaged yet; nil when they could not be counted.
            public var awaitingTriage: Int?
            public var advancePerRun: Int?
            public var runsNeeded: Int?
            /// How to get the schedule kept, and what that needs (vani's words, written for
            /// the terminal: it names `vani launchd plist`). The app shows `appHint` instead.
            public var hint: String?

            /// What the Pipeline tab says to get the schedule kept: the Run on schedule switch
            /// on the same tab. vani's `hint` is not shown in the app, since its terminal
            /// route (a hand-installed launchd agent) is the wrong advice where the switch is.
            public static let appHint = "Turn on Run on schedule above. Until then, use Run Now with a higher advance cap."

            enum CodingKeys: String, CodingKey {
                case why, overdue, hint
                case lastRunAt = "last_run_at"
                case dueAt = "due_at"
                case awaitingTriage = "awaiting_triage"
                case advancePerRun = "advance_per_run"
                case runsNeeded = "runs_needed"
            }

            public init(why: String? = nil, lastRunAt: Date? = nil, dueAt: Date? = nil, overdue: Bool? = nil, awaitingTriage: Int? = nil,
                        advancePerRun: Int? = nil, runsNeeded: Int? = nil, hint: String? = nil) {
                self.why = why
                self.lastRunAt = lastRunAt
                self.dueAt = dueAt
                self.overdue = overdue
                self.awaitingTriage = awaitingTriage
                self.advancePerRun = advancePerRun
                self.runsNeeded = runsNeeded
                self.hint = hint
            }

            public init(from decoder: Decoder) throws {
                guard let c = try? decoder.container(keyedBy: CodingKeys.self) else {
                    self.init()
                    return
                }
                self.init(why: try? c.decodeIfPresent(String.self, forKey: .why),
                          lastRunAt: try? c.decodeIfPresent(Date.self, forKey: .lastRunAt),
                          dueAt: try? c.decodeIfPresent(Date.self, forKey: .dueAt),
                          overdue: try? c.decodeIfPresent(Bool.self, forKey: .overdue),
                          awaitingTriage: try? c.decodeIfPresent(Int.self, forKey: .awaitingTriage),
                          advancePerRun: try? c.decodeIfPresent(Int.self, forKey: .advancePerRun),
                          runsNeeded: try? c.decodeIfPresent(Int.self, forKey: .runsNeeded),
                          hint: try? c.decodeIfPresent(String.self, forKey: .hint))
            }

            /// "schedule not kept" in one sentence for the Pipeline warning: no
            /// daemon, how long since the last run, and the backlog in runs.
            public func summary(dailyAt: String?, now: Date) -> String {
                var parts = ["No daemon is running, so nothing fires\(dailyAt.map { " the \($0)" } ?? " the daily") run."]
                if let last = lastRunAt {
                    parts.append("Last run \(PipelineStatus.age(now.timeIntervalSince(last))) ago\(overdue == true ? " and the scheduled run is overdue" : "").")
                } else if overdue == true {
                    parts.append("No run is recorded, so the scheduled run is overdue.")
                }
                if let n = awaitingTriage, n > 0 {
                    var s = "\(n) posting\(n == 1 ? " awaits" : "s await") triage"
                    if let runs = runsNeeded, let cap = advancePerRun { s += " (about \(runs) run\(runs == 1 ? "" : "s") at advance_per_run \(cap))" }
                    parts.append(s + ".")
                }
                parts.append("Turn on Run on schedule in the Pipeline tab, or use Run Now with a higher advance cap.")
                return parts.joined(separator: " ")
            }
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

    /// One skipped daily slot (skipFacet). A facet that does not decode reads as
    /// not ok, never fails the status read, and is not a recorded skip.
    public struct Skip: Decodable, Equatable, Sendable {
        public var ok: Bool
        public var reason: String?
        public var slot: Date?
        public var observedAt: Date?
        public var lateSec: Int
        public var error: String?

        enum CodingKeys: String, CodingKey {
            case ok, reason, slot, error
            case observedAt = "observed_at"
            case lateSec = "late_sec"
        }

        public init(ok: Bool = false, reason: String? = nil, slot: Date? = nil, observedAt: Date? = nil, lateSec: Int = 0, error: String? = nil) {
            self.ok = ok
            self.reason = reason
            self.slot = slot
            self.observedAt = observedAt
            self.lateSec = lateSec
            self.error = error
        }

        public init(from decoder: Decoder) throws {
            guard let c = try? decoder.container(keyedBy: CodingKeys.self) else {
                self.init(ok: false, reason: "vani's skipped-slot section did not decode")
                return
            }
            self.init(ok: (try? c.decode(Bool.self, forKey: .ok)) ?? false,
                      reason: try? c.decodeIfPresent(String.self, forKey: .reason),
                      slot: try? c.decodeIfPresent(Date.self, forKey: .slot),
                      observedAt: try? c.decodeIfPresent(Date.self, forKey: .observedAt),
                      lateSec: (try? c.decodeIfPresent(Int.self, forKey: .lateSec)) ?? 0,
                      error: try? c.decodeIfPresent(String.self, forKey: .error))
        }

        /// Why the slot was skipped, as one line. The error vani stored, or how late it was.
        public var detail: String? {
            if let error, !error.isEmpty { return error }
            if lateSec > 0 { return "It was \(PipelineStatus.age(TimeInterval(lateSec))) late, past the window." }
            return reason
        }
    }

    /// The last inbox poll (inboxFacet). Counts only: a bad-file name can carry a posting.
    public struct Inbox: Decodable, Equatable, Sendable {
        public var ok: Bool
        public var reason: String?
        public var at: Date?
        public var error: String?
        public var bad: Int
        public var unreadable: Int
        public var remaining: Int
        public var folded: Int
        public var newCount: Int
        public var duplicate: Int
        public var live: Int

        enum CodingKeys: String, CodingKey {
            case ok, reason, at, error, bad, unreadable, remaining, folded, duplicate, live
            case newCount = "new"
        }

        public init(ok: Bool = false, reason: String? = nil, at: Date? = nil, error: String? = nil,
                    bad: Int = 0, unreadable: Int = 0, remaining: Int = 0, folded: Int = 0,
                    newCount: Int = 0, duplicate: Int = 0, live: Int = 0) {
            self.ok = ok
            self.reason = reason
            self.at = at
            self.error = error
            self.bad = bad
            self.unreadable = unreadable
            self.remaining = remaining
            self.folded = folded
            self.newCount = newCount
            self.duplicate = duplicate
            self.live = live
        }

        public init(from decoder: Decoder) throws {
            guard let c = try? decoder.container(keyedBy: CodingKeys.self) else {
                self.init(ok: false, reason: "vani's inbox section did not decode")
                return
            }
            self.init(ok: (try? c.decode(Bool.self, forKey: .ok)) ?? false,
                      reason: try? c.decodeIfPresent(String.self, forKey: .reason),
                      at: try? c.decodeIfPresent(Date.self, forKey: .at),
                      error: try? c.decodeIfPresent(String.self, forKey: .error),
                      bad: (try? c.decodeIfPresent(Int.self, forKey: .bad)) ?? 0,
                      unreadable: (try? c.decodeIfPresent(Int.self, forKey: .unreadable)) ?? 0,
                      remaining: (try? c.decodeIfPresent(Int.self, forKey: .remaining)) ?? 0,
                      folded: (try? c.decodeIfPresent(Int.self, forKey: .folded)) ?? 0,
                      newCount: (try? c.decodeIfPresent(Int.self, forKey: .newCount)) ?? 0,
                      duplicate: (try? c.decodeIfPresent(Int.self, forKey: .duplicate)) ?? 0,
                      live: (try? c.decodeIfPresent(Int.self, forKey: .live)) ?? 0)
        }

        /// Counts and the last error, or why the poll could not be read.
        public var summary: String {
            if !ok {
                if let reason, !reason.isEmpty { return "Could not be read: \(reason)" }
                return "Could not be read"
            }
            var parts: [String] = []
            if let at { parts.append(at.formatted(date: .abbreviated, time: .shortened)) }
            func add(_ n: Int, _ word: String) {
                if n > 0 { parts.append("\(n) \(word)") }
            }
            add(bad, "bad")
            add(unreadable, "unreadable")
            add(remaining, "remaining")
            add(folded, "folded")
            add(newCount, "new")
            add(duplicate, "duplicate")
            add(live, "live")
            if let error, !error.isEmpty { parts.append(PipelineStatus.bounded(error, max: 200)) }
            if parts.isEmpty { return "Last poll recorded nothing to fold" }
            return parts.joined(separator: ", ")
        }

        /// A poll that failed, could not be read, or left files it could not take.
        public var needsAttention: Bool {
            !ok || !(error ?? "").isEmpty || bad > 0 || unreadable > 0
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
    /// A skipped daily slot later than the last real run. A run still marked
    /// running hides it. A facet that did not decode is not a skip.
    public var skippedSlot: Skip? {
        guard let skip = lastSkip, skip.ok, lastRun.state != .running, let slot = skip.slot else { return nil }
        if let cutoff = lastRun.finishedAt ?? lastRun.startedAt {
            return slot > cutoff ? skip : nil
        }
        return skip
    }

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
            if skippedSlot == nil { add(.warning, "No daily run has been recorded yet", lastRun.reason) }
        case .ok:
            if skippedSlot == nil, let finished = lastRun.finishedAt, now.timeIntervalSince(finished) > Self.staleAfter {
                add(.warning, "No run in over a day", "The last successful run finished \(Self.age(now.timeIntervalSince(finished))) ago.")
            }
        case .running:
            break
        }

        if let skip = lastSkip, !skip.ok {
            add(.warning, "A skipped daily slot could not be read", skip.reason)
        } else if let skip = skippedSlot {
            add(.warning, "A daily slot was skipped", skip.detail)
        }
        if let inbox {
            if !inbox.ok {
                add(.warning, "The last inbox poll could not be read", inbox.reason)
            } else if let err = inbox.error, !err.isEmpty {
                add(.warning, "The last inbox poll failed", PipelineStatus.bounded(err, max: 300))
            } else if inbox.bad > 0 || inbox.unreadable > 0 {
                add(.warning, "The last inbox poll left files unread", inbox.summary)
            }
        }

        // A notice this app cannot read is never a quiet day: say so.
        if case let .unsupported(why)? = lastRun.notice { add(.warning, "Run notices are off", why) }

        // A board that failed or was skipped is a warning even on a run that
        // succeeded: its postings silently stop updating otherwise.
        if lastRun.state != .running, let b = lastRun.boards, b.unreadCount > 0 {
            let names = b.problems(limit: 3).map(\.board).joined(separator: ", ")
            add(.warning, "\(b.unreadCount) board\(b.unreadCount == 1 ? "" : "s") could not be read last run",
                names.isEmpty ? nil : names)
        }

        if !schedule.ok { add(.warning, "The schedule could not be read", schedule.reason) }
        // A schedule nothing fires reads as fine everywhere else: say it. Quiet
        // only when nothing is overdue and nothing waits; the schedule row still
        // says it is not kept.
        if schedule.isUnkept {
            // `kept: false` without its details is a vani we cannot qualify: warn.
            let u = schedule.unkept ?? PipelineStatus.Schedule.Unkept(overdue: true)
            if u.overdue == true || (u.awaitingTriage ?? 0) > 0 {
                add(.warning, "The daily schedule is not being kept", u.summary(dailyAt: schedule.dailyAt, now: now))
            }
        }
        // A daemon started without the decision key keeps the schedule and triages nothing.
        if let d = daemon, d.running, let m = d.model, !m.enabled {
            add(.warning, "The daemon's decision model is off", m.reason)
        }
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

    /// Ask the running daemon for a manual run (`vani run-request --json`).
    /// No key is read: the daemon makes the run with its own.
    public func requestRun(_ options: ManualRunOptions) throws -> RunRequestReply {
        try VaniProcess.call(vaniBinary, options.arguments(command: "run-request") + ["--json"], configPath: configPath, stateDir: stateDir,
                             timeout: timeout, maxOutputBytes: Self.maxOutputBytes, decoder: JobsClient.decoder,
                             tooOld: "this vani cannot hand a run to the daemon; rebuild the app")
    }
}

extension PipelineClient {
    /// The latest run's failure report (`vani runs failures --json`), the one text
    /// Copy failures puts on the pasteboard. vani words it (the failed boards, the
    /// backend errors, the run's error, the log files) and bounds it; this app only
    /// carries it. An older vani has no such command (exit 2) and says so; a reply
    /// with no text is an error, never an empty copy.
    public func failureReport() throws -> FailureReport {
        let report: FailureReport = try VaniProcess.call(vaniBinary, ["runs", "failures", "--json"], configPath: configPath, stateDir: stateDir,
                                                         timeout: timeout, maxOutputBytes: FailureReport.maxReplyBytes, decoder: JobsClient.decoder,
                                                         tooOld: "this vani cannot report failures; rebuild the app")
        guard !report.text.isEmpty else { throw BankError("vani sent an empty failure report") }
        return report
    }
}

/// `vani runs failures --json`'s reply (cmd/vani/runscmd.go runFailuresJSON).
public struct FailureReport: Decodable, Equatable, Sendable {
    /// The contract version this app reads.
    public static let supportedVersion = 1
    /// vani cuts a report line at this many characters (maxReportLine).
    public static let maxLine = 600
    /// vani bounds the report at 24 KiB (maxReportBytes); the reply is that and its JSON framing.
    static let maxReplyBytes = 128 << 10

    public var id: String
    /// vani's own verdict (runHasFailures); the app's `LastRun.hasFailures` is the same rule.
    public var hasFailures: Bool
    /// The report, plain text. When there are no failures it says so.
    public var text: String
    /// Lines the report's bounds took out of `text`, and lines they shortened; both
    /// are also said in its last line. Zero from a vani that bounds nothing.
    public var cutLines: Int
    public var shortenedLines: Int

    enum CodingKeys: String, CodingKey {
        case id, text
        case hasFailures = "has_failures"
        case cutLines = "cut_lines"
        case shortenedLines = "shortened_lines"
    }

    public init(id: String, hasFailures: Bool, text: String, cutLines: Int = 0, shortenedLines: Int = 0) {
        self.id = id
        self.hasFailures = hasFailures
        self.text = text
        self.cutLines = cutLines
        self.shortenedLines = shortenedLines
    }

    /// Lenient: the bound counts are absent when zero (Go omits them).
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        hasFailures = try c.decode(Bool.self, forKey: .hasFailures)
        text = try c.decode(String.self, forKey: .text)
        cutLines = try c.decodeIfPresent(Int.self, forKey: .cutLines) ?? 0
        shortenedLines = try c.decodeIfPresent(Int.self, forKey: .shortenedLines) ?? 0
    }
}

/// `vani run-request --json`'s reply.
public struct RunRequestReply: Decodable, Equatable, Sendable {
    public var requestedAt: Date
    public var options: ManualRunOptions
    public var daemonPID: Int

    enum CodingKeys: String, CodingKey {
        case options
        case requestedAt = "requested_at"
        case daemonPID = "daemon_pid"
    }
}
