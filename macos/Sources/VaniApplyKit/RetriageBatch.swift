import Foundation

// "Re-triage the selection with Sonnet": the owner selects several roles in the Jobs list and asks
// Sonnet to read them all. This is a client of `vani jobs retriage --batch … --json` (cmd/vani,
// jobsretriagebatch.go), which owns the question, the cap, the prices, the store and the order the
// postings are asked in. Like Retriage.swift, nothing here asks a model or prices a call.
//
//   --batch --dry-run   one JSON object: each key's state and what asking would cost (spends nothing)
//   --batch             one JSON object per line as it goes: start, an item per posting, done
//
// A batch that stops early (the cap, no key, the provider down, the owner's Stop) keeps every reading
// it already made; `done` says what was not asked and why.

/// One key's state before anything is asked.
public struct RetriageBatchItem: Decodable, Equatable, Sendable {
    public var key: String
    /// Why it will not be asked: `unknown_key`, `text_gone`, `untriaged`, `already_read`; nil when it will.
    public var skip: String?
    public var reason: String?
    /// "current" or "stale" when Sonnet has read it before.
    public var read: String?

    public init(key: String, skip: String? = nil, reason: String? = nil, read: String? = nil) {
        self.key = key
        self.skip = skip
        self.reason = reason
        self.read = read
    }

    private enum CodingKeys: String, CodingKey { case key, skip, reason, read }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        key = try c.decode(String.self, forKey: .key)
        func text(_ k: CodingKeys) -> String? { (try? c.decode(String.self, forKey: k)).flatMap { $0.isEmpty ? nil : $0 } }
        skip = text(.skip)
        reason = text(.reason)
        read = text(.read)
    }

    public var isEligible: Bool { skip == nil }
    public var alreadyRead: Bool { read == "current" }
}

/// `jobs retriage --batch --dry-run`.
public struct RetriageBatchPlan: Decodable, Equatable, Sendable {
    public var model: String?
    public var items: [RetriageBatchItem]
    public var eligible: Int
    public var alreadyRead: Int
    /// Asking every eligible posting.
    public var estimate: RetriageEstimate
    /// Asking only those Sonnet has no current reading of; nil when vani did not say.
    public var estimateUnread: RetriageEstimate?

    public init(model: String? = nil, items: [RetriageBatchItem], eligible: Int, alreadyRead: Int,
                estimate: RetriageEstimate, estimateUnread: RetriageEstimate? = nil) {
        self.model = model
        self.items = items
        self.eligible = eligible
        self.alreadyRead = alreadyRead
        self.estimate = estimate
        self.estimateUnread = estimateUnread
    }

    private enum CodingKeys: String, CodingKey {
        case batch, model, items, eligible, estimate
        case alreadyRead = "already_read", estimateUnread = "estimate_unread"
    }

    /// `batch` must be true and the estimate present: a single posting's reply, or one with no cost,
    /// is not a plan to confirm. The counts are recomputed from the items, which are what is shown.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        guard (try? c.decode(Bool.self, forKey: .batch)) == true else {
            throw DecodingError.dataCorruptedError(forKey: .batch, in: c, debugDescription: "not a batch plan")
        }
        model = try? c.decode(String.self, forKey: .model)
        items = try c.decode([RetriageBatchItem].self, forKey: .items)
        estimate = try c.decode(RetriageEstimate.self, forKey: .estimate)
        estimateUnread = try? c.decode(RetriageEstimate.self, forKey: .estimateUnread)
        eligible = items.filter(\.isEligible).count
        alreadyRead = items.filter { $0.isEligible && $0.alreadyRead }.count
    }

    /// The keys that would be asked, in order.
    public func keysToAsk(skipRead: Bool) -> [String] {
        items.filter { $0.isEligible && !(skipRead && $0.alreadyRead) }.map(\.key)
    }

    /// The estimate for the choice; with `skipRead` and no separate estimate, nil (unknown).
    public func estimate(skipRead: Bool) -> RetriageEstimate? {
        guard skipRead, alreadyRead > 0 else { return estimate }
        return estimateUnread
    }
}

/// A batch's totals, from its `done` line.
public struct RetriageBatchSummary: Decodable, Equatable, Sendable {
    public var kept: Int
    public var failed: Int
    public var notAsked: Int
    public var skipped: Int
    /// Kept readings whose role fit differs from what ranking used before.
    public var changed: Int
    public var usd: Double?
    /// The code that ended it early (`cap_reached`, `no_key`, `provider`, `cancelled`, …); nil when every
    /// posting was asked.
    public var stopped: String?
    public var message: String?
    public var notAskedKeys: [String]

    public init(kept: Int = 0, failed: Int = 0, notAsked: Int = 0, skipped: Int = 0, changed: Int = 0, usd: Double? = nil,
                stopped: String? = nil, message: String? = nil, notAskedKeys: [String] = []) {
        self.kept = kept
        self.failed = failed
        self.notAsked = notAsked
        self.skipped = skipped
        self.changed = changed
        self.usd = usd
        self.stopped = stopped
        self.message = message
        self.notAskedKeys = notAskedKeys
    }

    private enum CodingKeys: String, CodingKey {
        case kept, failed, skipped, changed, usd, stopped, message
        case notAsked = "not_asked", notAskedKeys = "not_asked_keys"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        func count(_ k: CodingKeys) -> Int { (try? c.decode(Int.self, forKey: k)).flatMap { $0 >= 0 ? $0 : nil } ?? 0 }
        kept = count(.kept)
        failed = count(.failed)
        notAsked = count(.notAsked)
        skipped = count(.skipped)
        changed = count(.changed)
        usd = (try? c.decode(Double.self, forKey: .usd)).flatMap { $0.isFinite && $0 >= 0 ? $0 : nil }
        stopped = (try? c.decode(String.self, forKey: .stopped)).flatMap { $0.isEmpty ? nil : $0 }
        message = try? c.decode(String.self, forKey: .message)
        notAskedKeys = (try? c.decode([String].self, forKey: .notAskedKeys)) ?? []
    }
}

/// One line of a batch run.
public enum RetriageBatchEvent: Equatable, Sendable {
    case start(model: String?, total: Int, skipped: [RetriageBatchItem])
    case item(key: String, outcome: Result<RetriageResult, RetriageFailure>)
    case done(RetriageBatchSummary)

    private struct Wire: Decodable {
        var event: String
        var model: String?
        var total: Int?
        var skipped: [RetriageBatchItem]?
        var key: String?
        var result: RetriageResult?
        var error: String?
        var code: String?
        var cost: RetriageCost?
        var summary: RetriageBatchSummary?

        private enum CodingKeys: String, CodingKey { case event, model, total, skipped, key, result, error, code, cost, summary }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            event = try c.decode(String.self, forKey: .event)
            model = try? c.decode(String.self, forKey: .model)
            total = try? c.decode(Int.self, forKey: .total)
            skipped = try? c.decode([RetriageBatchItem].self, forKey: .skipped)
            key = try? c.decode(String.self, forKey: .key)
            result = try? c.decode(RetriageResult.self, forKey: .result)
            error = try? c.decode(String.self, forKey: .error)
            code = try? c.decode(String.self, forKey: .code)
            cost = try? c.decode(RetriageCost.self, forKey: .cost)
            summary = try? c.decode(RetriageBatchSummary.self, forKey: .summary)
        }
    }

    /// One line, or nil for a line that is not an event this app reads (a newer kind is skipped, not
    /// fatal). An item must name its key, and a result for a different key is refused.
    public static func decode(line: Data) -> RetriageBatchEvent? {
        let decoder = JobsClient.decoder
        guard let w = try? decoder.decode(Wire.self, from: line) else { return nil }
        switch w.event {
        case "start":
            return .start(model: w.model, total: max(w.total ?? 0, 0), skipped: w.skipped ?? [])
        case "item":
            guard let key = w.key, !key.isEmpty else { return nil }
            if let r = w.result {
                if let named = r.key, named != key { return nil }
                return .item(key: key, outcome: .success(r))
            }
            guard let message = w.error else { return nil }
            return .item(key: key, outcome: .failure(RetriageFailure(code: w.code.flatMap { $0.isEmpty ? nil : $0 } ?? "unknown",
                                                                     message: message, cost: w.cost)))
        case "done":
            return w.summary.map { .done($0) }
        default:
            return nil
        }
    }
}

/// Splits a byte stream into lines. A line longer than `maxLine` is dropped whole (counted in
/// `dropped`), so one runaway line cannot grow the buffer without bound.
public struct LineSplitter: Sendable {
    public let maxLine: Int
    private var buffer = Data()
    private var overflowing = false
    public private(set) var dropped = 0

    public init(maxLine: Int) { self.maxLine = maxLine }

    /// The complete lines `chunk` ends, without their newline; empty lines are left out.
    public mutating func feed(_ chunk: Data) -> [Data] {
        var lines: [Data] = []
        var rest = chunk[...]
        while let nl = rest.firstIndex(of: 0x0A) {
            let part = rest[rest.startIndex..<nl]
            if overflowing {
                overflowing = false
            } else if buffer.count + part.count > maxLine {
                dropped += 1
            } else {
                buffer.append(part)
                if !buffer.isEmpty { lines.append(buffer) }
            }
            buffer = Data()
            rest = rest[rest.index(after: nl)...]
        }
        if overflowing { return lines }
        if buffer.count + rest.count > maxLine {
            buffer = Data()
            overflowing = true
            dropped += 1
        } else {
            buffer.append(contentsOf: rest)
        }
        return lines
    }

    /// What is left after the stream ended with no newline: a torn last line. Never decoded as a line.
    public var tail: Data { buffer }
}

/// Runs `vani jobs retriage --batch`. The key reaches the child as it does for one posting (the draft
/// runner, `VANI_NO_KEYCHAIN=1`); `plan` starts `vani` with no key.
public struct RetriageBatchClient: Sendable {
    /// Postings asked at once (vani's default).
    public static let parallel = 3
    /// The most keys one batch takes (vani's maxRetriageBatch).
    public static let maxKeys = 500
    static let maxLineBytes = 256 * 1024
    static let maxReplyBytes = 64 * 1024

    public let runner: DraftRunner
    let perPosting: TimeInterval
    let quickTimeout: TimeInterval

    public init(runner: DraftRunner, perPosting: TimeInterval = RetriageClient.runTimeout,
                quickTimeout: TimeInterval = RetriageClient.quickTimeout) {
        self.runner = runner
        self.perPosting = perPosting
        self.quickTimeout = quickTimeout
    }

    /// The ceiling on a run of `n` postings: each one's own ceiling, `parallel` at a time, plus a
    /// minute to read the ledger and start; never above five hours.
    public func runTimeout(postings n: Int) -> TimeInterval {
        let waves = (max(n, 1) + Self.parallel - 1) / Self.parallel
        return min(60 + Double(waves) * perPosting, 5 * 3600)
    }

    static func arguments(_ keys: [String], dryRun: Bool, skipRead: Bool) -> [String] {
        ["jobs", "retriage", "--json", "--batch"] + (dryRun ? ["--dry-run"] : []) + (skipRead ? ["--skip-read"] : []) + keys
    }

    /// The keys as passed: none refused by `RetriageClient.refuse`, no repeats, at most `maxKeys`.
    static func refuse(keys: [String]) -> RetriageFailure? {
        if keys.isEmpty { return RetriageFailure(code: "bad_request", message: "no roles are selected") }
        if keys.count > maxKeys {
            return RetriageFailure(code: "bad_request", message: "\(keys.count) roles are selected; one batch takes at most \(maxKeys)")
        }
        for k in keys { if let r = RetriageClient.refuse(key: k) { return r } }
        return nil
    }

    /// What reading `keys` would cost and which can be read. Spends nothing. Blocks.
    public func plan(_ keys: [String], cancellation: CancelFlag? = nil) -> RetriageOutcome<RetriageBatchPlan> {
        if let refusal = Self.refuse(keys: keys) { return .failure(refusal) }
        let invocation = runner.invoke(arguments: Self.arguments(keys, dryRun: true, skipRead: false), timeout: quickTimeout,
                                       usesModel: false, maxOutputBytes: 4 << 20,
                                       extraEnvironment: ["VANI_NO_KEYCHAIN": "1"], cancellation: cancellation)
        switch invocation {
        case let .failure(reply):
            if reply.code == "cancelled" { return .cancelled }
            return .failure(RetriageFailure(code: reply.code ?? "provider", message: reply.error ?? "vani could not be started"))
        case let .output(out):
            if out.truncated { return .failure(RetriageFailure(code: "too_large", message: "vani's estimate was larger than 4 MB")) }
            if let f = Self.failure(in: out.stdout) {
                return f.code == "cancelled" ? .cancelled : .failure(f)
            }
            if out.status == 0, let plan = try? JobsClient.decoder.decode(RetriageBatchPlan.self, from: out.stdout) {
                let asked = Set(keys)
                guard plan.items.allSatisfy({ asked.contains($0.key) }) else {
                    return .failure(RetriageFailure(code: "unreadable_reply", message: "vani's estimate names a role that was not asked about"))
                }
                return .success(plan)
            }
            let tooOld = out.status == 2 ? " (this vani cannot re-triage several roles at once; rebuild the app)" : ""
            return .failure(RetriageFailure(code: "no_reply", message: "vani jobs retriage --batch exited \(out.status ?? -1) without an estimate\(tooOld)"))
        }
    }

    /// Ask Sonnet about `keys` and keep its readings; `onEvent` is called, off the main thread and in
    /// order, with each line as it arrives (an item for a key not asked is dropped). Spends. Blocks for
    /// up to `runTimeout`. The outcome is the `done` summary, which may say it stopped early;
    /// `.cancelled` when the owner stopped it (the items already delivered were kept by vani).
    public func run(_ keys: [String], skipRead: Bool, cancellation: CancelFlag? = nil,
                    onEvent: @escaping @Sendable (RetriageBatchEvent) -> Void) -> RetriageOutcome<RetriageBatchSummary> {
        if let refusal = Self.refuse(keys: keys) { return .failure(refusal) }
        let stream = EventStream(asked: Set(keys), onEvent: onEvent)
        let invocation = runner.invoke(arguments: Self.arguments(keys, dryRun: false, skipRead: skipRead),
                                       timeout: runTimeout(postings: keys.count), usesModel: true, maxOutputBytes: Self.maxReplyBytes,
                                       extraEnvironment: ["VANI_NO_KEYCHAIN": "1"], cancellation: cancellation,
                                       onStdout: { stream.feed($0) })
        switch invocation {
        case let .failure(reply):
            if reply.code == "cancelled" { return .cancelled }
            return .failure(RetriageFailure(code: reply.code ?? "provider", message: reply.error ?? "vani could not be started"))
        case let .output(out):
            // A batch that stopped itself (or ended on a TERM it was not sent by this app) still
            // reports what it kept and why it stopped.
            if let summary = stream.summary { return .success(summary) }
            if let f = Self.failure(in: out.stdout) {
                return f.code == "cancelled" ? .cancelled : .failure(f)
            }
            if let refusal = AnthropicAccount.refusal(in: out.stderr) {
                return .failure(RetriageFailure(code: "account", message: refusal))
            }
            let tooOld = out.status == 2 ? " (this vani cannot re-triage several roles at once; rebuild the app)" : ""
            return .failure(RetriageFailure(code: "no_reply", message: "vani jobs retriage --batch exited \(out.status ?? -1) before it finished\(tooOld)"))
        }
    }

    private struct Failure: Decodable {
        var error: String
        var code: String?
        var cost: RetriageCost?
    }

    /// vani's `{"error","code"}` reply, when the whole of stdout is one.
    static func failure(in stdout: Data) -> RetriageFailure? {
        guard let f = try? JobsClient.decoder.decode(Failure.self, from: stdout) else { return nil }
        return RetriageFailure(code: f.code.flatMap { $0.isEmpty ? nil : $0 } ?? "unknown", message: f.error, cost: f.cost)
    }

    /// The reader thread's state: the splitter and the summary, behind a lock.
    private final class EventStream: @unchecked Sendable {
        private let lock = NSLock()
        private var splitter = LineSplitter(maxLine: RetriageBatchClient.maxLineBytes)
        private var done: RetriageBatchSummary?
        private let asked: Set<String>
        private let onEvent: @Sendable (RetriageBatchEvent) -> Void

        init(asked: Set<String>, onEvent: @escaping @Sendable (RetriageBatchEvent) -> Void) {
            self.asked = asked
            self.onEvent = onEvent
        }

        var summary: RetriageBatchSummary? {
            lock.lock()
            defer { lock.unlock() }
            return done
        }

        func feed(_ chunk: Data) {
            lock.lock()
            let lines = splitter.feed(chunk)
            lock.unlock()
            for line in lines {
                guard let ev = RetriageBatchEvent.decode(line: line) else { continue }
                if case let .item(key, _) = ev, !asked.contains(key) { continue }
                if case let .done(s) = ev {
                    lock.lock()
                    done = s
                    lock.unlock()
                }
                onEvent(ev)
            }
        }
    }
}

/// How a selected role stands in the batch, for its row.
public enum RetriageRowState: Equatable, Sendable {
    /// Waiting its turn (or being asked).
    case queued
    /// Sonnet's reading was kept; `changed` when its role fit differs from what ranking used.
    case done(changed: Bool)
    case failed(String)
    /// The batch stopped before it was asked.
    case notAsked
}

/// The confirmation sheet's content for a batch.
public struct RetriageBatchConfirmation: Equatable, Sendable {
    /// "Ask Sonnet about 12 roles".
    public var title: String
    /// "About $0.045 typically, at most $0.18 · 13,400 input tokens · daily cap $30, $14.68 left".
    public var costText: String
    /// Said when not every posting fits in what is left at its most; nil when they all do.
    public var capWarning: String?
    /// "3 of the selected roles already have a current Sonnet reading." nil when none do.
    public var alreadyReadText: String?
    /// One line per kind of skipped role: "2 not triaged yet".
    public var skippedLines: [String]
    public var sentText: String
    public var priceSource: String?
    public var canConfirm: Bool
    public var blockedReason: String?
    /// The keys that would be asked, in order.
    public var keys: [String]
}

/// The words of the batch, as pure functions.
public enum RetriageBatchPresentation {
    /// The roles of a selection, and how many of them could be read.
    public struct Selection: Equatable, Sendable {
        public var total: Int
        public var untriaged: Int
        /// Triaged roles with a current Sonnet reading.
        public var alreadyRead: Int
        public var readable: Int { total - untriaged }
    }

    public static func selection(_ jobs: [Job]) -> Selection {
        Selection(total: jobs.count, untriaged: jobs.filter(\.isUntriaged).count,
                  alreadyRead: jobs.filter { !$0.isUntriaged && $0.manualTriage?.current == true }.count)
    }

    /// Why the batch cannot start for these roles, or nil when it can. `busy`: a re-triage is running.
    public static func blockedReason(_ s: Selection, busy: Bool) -> String? {
        if s.total == 0 { return "No roles are selected." }
        if s.total > RetriageBatchClient.maxKeys { return "Select at most \(RetriageBatchClient.maxKeys) roles; \(s.total) are selected." }
        if s.readable == 0 { return s.total == 1 ? RetriageWords.untriagedReason : "None of these roles has been triaged yet, so there is no reading to compare." }
        if busy { return "Another re-triage is running. Wait for it to finish, or stop it." }
        return nil
    }

    /// "Re-triage 12 with Sonnet…"
    public static func actionTitle(_ s: Selection) -> String {
        "Re-triage \(roles(s.readable)) with Sonnet…"
    }

    /// "12 roles selected · 2 not triaged · 3 already read by Sonnet".
    public static func selectionLine(_ s: Selection) -> String {
        var parts = ["\(roles(s.total)) selected"]
        if s.untriaged > 0 { parts.append("\(s.untriaged) not triaged") }
        if s.alreadyRead > 0 { parts.append("\(s.alreadyRead) already read by Sonnet") }
        return parts.joined(separator: " · ")
    }

    static func roles(_ n: Int, locale: Locale = .current) -> String {
        "\(n.formatted(.number.locale(locale))) \(n == 1 ? "role" : "roles")"
    }

    public static func confirmation(for plan: RetriageBatchPlan, skipRead: Bool, locale: Locale = .current) -> RetriageBatchConfirmation {
        let keys = plan.keysToAsk(skipRead: skipRead)
        let n = keys.count
        var parts: [String] = []
        var blocked: String?
        let est = plan.estimate(skipRead: skipRead)
        if let e = est {
            switch (e.typicalUSD, e.maxUSD) {
            case let (typical?, max?): parts.append("About \(RetriagePresentation.money(typical)) typically, at most \(RetriagePresentation.money(max))")
            case let (nil, max?): parts.append("At most \(RetriagePresentation.money(max))")
            case let (typical?, nil): parts.append("About \(RetriagePresentation.money(typical)) typically")
            case (nil, nil): parts.append("Vani did not estimate the cost")
            }
            if let t = e.inputTokens, t > 0 { parts.append("\(t.formatted(.number.locale(locale))) input tokens") }
            parts.append(RetriagePresentation.capText(e))
        } else {
            parts.append("Vani did not estimate the cost of this choice")
        }

        var warning: String?
        if n == 0 {
            blocked = plan.eligible == 0 ? "None of the selected roles can be read." : "Every role that can be read already has a current Sonnet reading. Turn on “Read them again” to ask anyway."
        } else if let e = est {
            if e.capReached {
                blocked = "Today's daily cap is spent (decide.daily_usd.llm in Vani's config), so Sonnet cannot be asked until it resets."
            } else if e.typicalUSD == nil && e.maxUSD == nil {
                blocked = "Vani did not say what this would cost, so it cannot be confirmed."
            } else if let fit = e.fitAtMax, fit < n {
                warning = "At their most, only \(fit) of \(n) fit in what is left of today's cap. Typical readings cost far less, but if the cap is reached the batch stops there and keeps what Sonnet already read."
            }
        } else {
            blocked = "Vani did not say what this would cost, so it cannot be confirmed."
        }

        var skipped: [String] = []
        let groups = Dictionary(grouping: plan.items.compactMap(\.skip), by: { $0 })
        for (code, words) in [("untriaged", "not triaged yet"), ("text_gone", "no posting text left (the role closed)"),
                              ("unknown_key", "no longer in Vani's ledger")] {
            if let c = groups[code]?.count, c > 0 { skipped.append("\(roles(c, locale: locale)) skipped: \(words)") }
        }
        let read = plan.alreadyRead > 0
            ? "\(roles(plan.alreadyRead, locale: locale)) already \(plan.alreadyRead == 1 ? "has" : "have") a current Sonnet reading."
            : nil
        return RetriageBatchConfirmation(title: n == 0 ? "Nothing to ask Sonnet" : "Ask Sonnet about \(roles(n, locale: locale))",
                                         costText: parts.joined(separator: " · "), capWarning: warning, alreadyReadText: read,
                                         skippedLines: skipped, sentText: RetriageWords.sentLine(model: plan.model),
                                         priceSource: est?.priceSource, canConfirm: blocked == nil, blockedReason: blocked, keys: keys)
    }

    /// "7 of 12 read · 1 failed · $0.03 so far".
    public static func progressLine(done: Int, failed: Int, total: Int, usd: Double) -> String {
        var parts = ["\(done + failed) of \(total) read"]
        if failed > 0 { parts.append("\(failed) failed") }
        if usd > 0 { parts.append("\(RetriagePresentation.money(usd)) so far") }
        return parts.joined(separator: " · ")
    }

    /// The headline of a finished batch: "Sonnet read 11 of 12 roles · role fit changed on 4".
    public static func summaryLine(_ s: RetriageBatchSummary) -> String {
        let asked = s.kept + s.failed + s.notAsked
        var parts = ["Sonnet read \(s.kept) of \(roles(asked))"]
        if s.kept > 0 { parts.append(s.changed == 0 ? "no role fit changed" : "role fit changed on \(s.changed)") }
        return parts.joined(separator: " · ")
    }

    /// The details under the headline: failures, what was not asked, skipped, and the cost.
    public static func summaryDetails(_ s: RetriageBatchSummary) -> [String] {
        var lines: [String] = []
        if s.failed > 0 { lines.append("\(roles(s.failed)) could not be read; nothing was saved for \(s.failed == 1 ? "it" : "them").") }
        if s.notAsked > 0 {
            let why = s.stopped.flatMap { stopWords($0, message: s.message) } ?? "the batch stopped early"
            lines.append("\(roles(s.notAsked)) not asked: \(why)")
        }
        if s.skipped > 0 { lines.append("\(roles(s.skipped)) skipped before asking.") }
        lines.append(s.usd.map { "Charged about \(RetriagePresentation.money($0)) in all." } ?? "Vani did not report the cost.")
        lines.append(RetriageWords.ruleSeven)
        return lines
    }

    /// Why a batch stopped, in words.
    static func stopWords(_ code: String, message: String?) -> String? {
        switch code {
        case "cancelled": return "you stopped it."
        case "cap_reached": return "today's Claude spending cap was reached (decide.daily_usd.llm)."
        case "no_key": return "no Anthropic key was available."
        case "provider": return "Sonnet could not be reached."
        default:
            return RetriagePresentation.errorText(for: RetriageFailure(code: code, message: message ?? ""))?.message
        }
    }

    /// The row's state as a short label and its tooltip.
    public static func rowLabel(_ state: RetriageRowState) -> (text: String, help: String) {
        switch state {
        case .queued: ("Sonnet…", "Waiting for Sonnet's reading.")
        case .done(true): ("Fit changed", "Sonnet's reading changed this role's fit.")
        case .done(false): ("Sonnet ✓", "Sonnet read this role; its fit did not change.")
        case let .failed(message): ("Failed", message)
        case .notAsked: ("Not asked", "The batch stopped before this role was asked.")
        }
    }
}
