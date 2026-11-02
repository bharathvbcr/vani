import Foundation

// The Home pane's logic, kept apart from its views so it can be tested: the
// status line, the top-roles pick, the setup checklist and the recent-activity
// rows. Everything here is a pure function of values the app already loaded
// (the jobs page, the pipeline status, the bank, the browser states); nothing
// reads a file, runs vani or calls Date(). A source that has not been read, or
// could not be read, is its own case and never reads as zero.

/// Where one data source stands: not read yet, unreadable, or read.
public enum Reading<Value: Equatable & Sendable>: Equatable, Sendable {
    case pending
    case failed(String)
    case ready(Value)
}

/// The panes Home can send you to.
public enum HomeTarget: Equatable, Sendable {
    case jobs, pipeline, answers, activity, browsers, settings
}

/// The counts the three KPI tiles show, from the jobs page's per-view counts
/// (which vani computes ignoring the query and the filters).
public struct JobCounts: Equatable, Sendable {
    public var new: Int
    public var saved: Int
    public var applied: Int
    /// A writer was mid-append: the counts are complete up to it.
    public var torn: Bool

    public init(new: Int, saved: Int, applied: Int, torn: Bool = false) {
        self.new = max(new, 0)
        self.saved = max(saved, 0)
        self.applied = max(applied, 0)
        self.torn = torn
    }

    public init(page: JobsPage) {
        self.init(new: page.count(.new), saved: page.count(.saved), applied: page.count(.applied), torn: page.torn == true)
    }
}

public enum HomeOverview {
    // MARK: - Header

    /// By the hour only: Home never reads a name from the bank.
    public static func greeting(hour: Int) -> String {
        switch hour {
        case 5..<12: "Good morning"
        case 12..<17: "Good afternoon"
        case 17..<22: "Good evening"
        default: "Welcome back"
        }
    }

    /// "12 new roles · pipeline healthy · last run 6:02 AM". A source that is
    /// loading, unreadable and empty each print differently.
    public static func statusLine(jobs: Reading<JobCounts>, pipeline: Reading<PipelineStatus>,
                                  now: Date, timeText: (Date) -> String) -> String {
        var parts: [String] = []
        switch jobs {
        case .pending: parts.append("Loading roles…")
        case .failed: parts.append("Roles unavailable")
        case let .ready(c): parts.append(c.new == 0 ? "No new roles" : "\(count(c.new)) new \(c.new == 1 ? "role" : "roles")")
        }
        switch pipeline {
        case .pending: parts.append("pipeline not checked yet")
        case .failed: parts.append("pipeline status unavailable")
        case let .ready(s):
            parts.append(healthPhrase(s.health(at: now)))
            if let run = runPhrase(s.lastRun, timeText: timeText) { parts.append(run) }
        }
        return parts.joined(separator: " · ")
    }

    private static func runPhrase(_ run: PipelineStatus.LastRun, timeText: (Date) -> String) -> String? {
        guard run.ok else { return "last run unreadable" }
        switch run.state {
        case .never: return "no run recorded"
        case .running: return liveHeadline(run) ?? "run in progress"
        case .unknown: return "last run unreadable"
        case .ok, .failed, .cancelled, .interrupted:
            guard let at = run.finishedAt ?? run.startedAt else { return nil }
            return "last run \(timeText(at))"
        }
    }

    /// An error message or other untrusted text fit for one line of a card.
    public static func oneLine(_ s: String, max: Int) -> String { PipelineStatus.bounded(s, max: max) }

    // MARK: - Numbers

    /// 1234567 → "1,234,567"; a negative count reads as 0.
    public static func count(_ n: Int, locale: Locale = .current) -> String {
        max(n, 0).formatted(.number.locale(locale))
    }

    // MARK: - Pipeline

    public static func healthTitle(_ h: PipelineHealth) -> String {
        switch h {
        case .healthy: "Healthy"
        case .warning: "Needs a look"
        case .problem: "Needs attention"
        }
    }

    static func healthPhrase(_ h: PipelineHealth) -> String {
        switch h {
        case .healthy: "pipeline healthy"
        case .warning: "pipeline needs a look"
        case .problem: "pipeline needs attention"
        }
    }

    public static func healthSymbol(_ h: PipelineHealth) -> String {
        switch h {
        case .healthy: "checkmark.circle.fill"
        case .warning: "exclamationmark.triangle.fill"
        case .problem: "xmark.octagon.fill"
        }
    }

    /// The portal and place while a run is going. Nil when there is nothing
    /// trustworthy to name, so the card stays "Running now".
    private static func liveHeadline(_ run: PipelineStatus.LastRun) -> String? {
        guard let progress = run.progress, progress.ok, !progress.headline.isEmpty else { return nil }
        if progress.placeText.isEmpty { return progress.headline }
        return "\(progress.headline) · \(progress.placeText)"
    }

    /// "Succeeded 3 h ago · +12 postings", "Failed 5 min ago (digest)": the
    /// one line the Pipeline card leads with.
    public static func runHeadline(_ run: PipelineStatus.LastRun, now: Date) -> String {
        guard run.ok else { return "Last run unreadable" + (run.reason.map { ": " + PipelineStatus.bounded($0, max: 80) } ?? "") }
        func ago(_ d: Date?) -> String { d.map { " " + PipelineStatus.age(now.timeIntervalSince($0)) + " ago" } ?? "" }
        switch run.state {
        case .never: return "No run recorded yet"
        case .running: return liveHeadline(run) ?? "Running now"
        case .unknown: return "Last run unreadable"
        case .ok:
            var s = "Succeeded" + ago(run.finishedAt)
            if let added = run.postingsAdded { s += " · \(added < 0 ? "-" : "+")\(count(added.magnitude > UInt(Int.max) ? Int.max : abs(added))) postings" }
            return s
        case .failed: return "Failed" + ago(run.finishedAt) + (run.stage.map { " (\($0))" } ?? "")
        case .cancelled: return "Cancelled" + ago(run.finishedAt)
        case .interrupted: return "Did not finish" + ago(run.startedAt)
        }
    }

    // MARK: - Top new roles

    /// The best `limit` roles to look at first: unmarked, not closed, one per
    /// key (the first copy wins), by priority (else relevance) highest first.
    /// A role with no finite score goes after every scored one; ties keep
    /// vani's own order.
    public static func topRoles(_ jobs: [Job], limit: Int) -> [Job] {
        guard limit > 0 else { return [] }
        var seen = Set<String>()
        var candidates: [(offset: Int, score: Double?, job: Job)] = []
        for (i, j) in jobs.enumerated() where j.mark == nil && j.closed != true && seen.insert(j.key).inserted {
            let s = [j.priority, j.relevance].compactMap { $0 }.first { $0.isFinite }
            candidates.append((i, s, j))
        }
        let ranked = candidates.sorted { a, b in
            switch (a.score, b.score) {
            case let (x?, y?): x != y ? x > y : a.offset < b.offset
            case (_?, nil): true
            case (nil, _?): false
            case (nil, nil): a.offset < b.offset
            }
        }
        return ranked.prefix(limit).map(\.job)
    }

    /// "Strong fit · Matches the platform work you listed", bounded: the
    /// posting text behind it is untrusted. Nil when Vani read nothing.
    public static func fitNote(_ job: Job, maxLength: Int = 90) -> String? {
        let reason = job.reasons.first { $0.kind == .fits }?.text ?? job.reasons.first { $0.kind == .other }?.text
        let parts = [job.fitLabel, reason.map { PipelineStatus.bounded($0, max: maxLength) }]
            .compactMap { $0 }.filter { !$0.isEmpty }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    // MARK: - Setup

    public struct SetupItem: Identifiable, Equatable, Sendable {
        public enum State: Equatable, Sendable {
            case done
            /// Not done, and the user can do it.
            case todo
            /// Broken: something is wrong, not merely unfinished.
            case problem
            /// The check could not run: neither done nor missing.
            case unknown
        }

        public var id: String
        public var title: String
        public var detail: String
        public var state: State
        public var target: HomeTarget
    }

    public enum BrowserLink: Equatable, Sendable { case connected, notConnected, unknown }

    /// Connected if either Chrome-family browser is connected or Safari's
    /// extension is on. Safari's state not read yet is unknown, not "off".
    public static func browserLink(chrome: [ChromeConnector.Status], safariEnabled: Bool?) -> BrowserLink {
        if chrome.contains(.connected) || safariEnabled == true { return .connected }
        return safariEnabled == nil ? .unknown : .notConnected
    }

    public struct SetupInputs: Sendable {
        public var storeError: String?
        public var answerCount: Int
        public var pipeline: Reading<PipelineStatus>
        public var browsers: BrowserLink
        public var agentEnabled: Bool

        public init(storeError: String?, answerCount: Int, pipeline: Reading<PipelineStatus>, browsers: BrowserLink, agentEnabled: Bool) {
            self.storeError = storeError
            self.answerCount = answerCount
            self.pipeline = pipeline
            self.browsers = browsers
            self.agentEnabled = agentEnabled
        }
    }

    public static func setupItems(_ i: SetupInputs) -> [SetupItem] {
        var out: [SetupItem] = []
        func add(_ id: String, _ title: String, _ detail: String, _ state: SetupItem.State, _ target: HomeTarget) {
            out.append(SetupItem(id: id, title: title, detail: detail, state: state, target: target))
        }

        if let e = i.storeError {
            add("bank", "Answer bank", "Could not be read: " + PipelineStatus.bounded(e, max: 120), .problem, .answers)
        } else if i.answerCount <= 0 {
            add("bank", "Answer bank", "Empty. Save answers from a form, or add them by hand.", .todo, .answers)
        } else {
            add("bank", "Answer bank", "\(count(i.answerCount)) \(i.answerCount == 1 ? "answer" : "answers") saved", .done, .answers)
        }

        switch i.pipeline {
        case .pending:
            for (id, title) in [("config", "Vani config"), ("ledger", "Job ledger"), ("run", "Daily run")] {
                add(id, title, "Not checked yet", .unknown, .pipeline)
            }
        case let .failed(why):
            let detail = "vani did not answer: " + PipelineStatus.bounded(why, max: 120)
            for (id, title) in [("config", "Vani config"), ("ledger", "Job ledger"), ("run", "Daily run")] {
                add(id, title, detail, .unknown, .pipeline)
            }
        case let .ready(s):
            if !s.config.ok {
                add("config", "Vani config", "Could not be read" + (s.config.reason.map { ": " + PipelineStatus.bounded($0, max: 120) } ?? ""), .problem, .settings)
            } else {
                add("config", "Vani config", s.config.boards.map { "\(count($0)) \($0 == 1 ? "board" : "boards") configured" } ?? "Readable", .done, .settings)
            }
            if !s.ledger.ok {
                add("ledger", "Job ledger", "Could not be read" + (s.ledger.reason.map { ": " + PipelineStatus.bounded($0, max: 120) } ?? ""), .problem, .pipeline)
            } else if !s.ledger.exists || s.ledger.total <= 0 {
                add("ledger", "Job ledger", "No postings yet. The first run creates them.", .todo, .pipeline)
            } else {
                add("ledger", "Job ledger", "\(count(s.ledger.total)) postings", .done, .pipeline)
            }
            switch (s.lastRun.ok, s.lastRun.state) {
            case (false, _), (_, .unknown):
                add("run", "Daily run", "The last run could not be read", .unknown, .pipeline)
            case (_, .never):
                add("run", "Daily run", "No run recorded yet", .todo, .pipeline)
            case (_, .failed), (_, .interrupted):
                add("run", "Daily run", "The last run did not finish cleanly", .problem, .pipeline)
            case (_, .ok), (_, .running), (_, .cancelled):
                add("run", "Daily run", "Has run", .done, .pipeline)
            }
        }

        switch i.browsers {
        case .connected: add("browsers", "Browser extension", "Connected", .done, .browsers)
        case .notConnected: add("browsers", "Browser extension", "Not connected to Safari or Chrome", .todo, .browsers)
        case .unknown: add("browsers", "Browser extension", "Safari's state could not be read", .unknown, .browsers)
        }

        if i.agentEnabled {
            add("agent", "Drafting agent", "Registered", .done, .settings)
        } else {
            add("agent", "Drafting agent", "Not running. Browsers need it for drafts and suggestions.", .todo, .settings)
        }
        return out
    }

    // MARK: - Recent activity

    public struct ActivityRow: Identifiable, Equatable, Sendable {
        public var id: Int
        public var symbol: String
        public var title: String
        public var detail: String
        /// Nil when the entry's timestamp does not parse; the row says so rather than guessing.
        public var at: Date?
    }

    /// The first `limit` entries (the store lists newest first) as rows.
    public static func recentActivity(_ entries: [ActivityEntry], limit: Int) -> [ActivityRow] {
        guard limit > 0 else { return [] }
        return entries.prefix(limit).enumerated().map { i, e in
            let verb: String = switch e.event {
            case .capture: "Saved answers"
            case .fill: "Filled a form"
            case .edit: "Edited answers"
            case .delete: "Deleted answers"
            case .import: "Imported a bank"
            case .export: "Exported the bank"
            case .settings: "Changed settings"
            case .draft: "Drafted an answer"
            case .resume: "Chose a resume"
            }
            let host = e.host.map { PipelineStatus.bounded($0, max: 60) }.flatMap { $0.isEmpty ? nil : $0 }
            let symbol: String = switch e.source {
            case .chrome: "globe"
            case .safari: "safari"
            case .app: "macwindow"
            }
            // Only counts that happened: a row of "skippedEmpty 0" says nothing at a glance.
            let counts = (e.counts ?? [:]).filter { $0.value > 0 }.sorted { $0.key < $1.key }.map { "\($0.key) \(count($0.value))" }
            return ActivityRow(id: i, symbol: symbol, title: host.map { "\(verb) on \($0)" } ?? verb,
                               detail: ([e.source.rawValue.capitalized] + counts).joined(separator: " · "), at: parseTime(e.at))
        }
    }

    /// RFC 3339 with or without fractional seconds, as the browsers and the app write it.
    static func parseTime(_ s: String) -> Date? {
        (try? Date.ISO8601FormatStyle(includingFractionalSeconds: true).parse(s)) ?? (try? Date.ISO8601FormatStyle().parse(s))
    }
}
