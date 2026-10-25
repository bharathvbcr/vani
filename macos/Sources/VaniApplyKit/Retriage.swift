import Foundation

// "Re-triage with Sonnet": the owner asks the configured Claude triage model (Sonnet) to read ONE
// posting again, by hand, from the Jobs tab. This is a client of `vani jobs retriage … --json`
// (cmd/vani, the Go half), which owns the question, the cap, the price table and the side store
// (<state>/retriage/). Nothing here asks a model or prices a call: the app runs the command,
// decodes its one JSON reply, and words it.
//
// Three verbs, one command:
//   --dry-run   what it would cost; asks nothing and spends nothing (no key is given to it)
//   (none)      asks Sonnet and stores the reading; spends from the same daily llm cap as drafts
//   --clear     forgets the stored reading; spends nothing (no key is given to it)
//
// A manual reading only changes the order (Rule 7): it never drops a posting or brings a dropped
// one back. The reply decodes leniently, like the rest of Jobs.swift: unknown fields are ignored,
// absent optional fields are nil, and a malformed `manual_triage` on a job never fails the list.

public enum RetriageWords {
    /// What the app calls a reading Sonnet made on request ("backend":"manual").
    public static let manualName = "Sonnet (manual)"
    /// Said wherever a manual reading is shown or made.
    public static let ruleSeven = "A manual reading only changes the order. It never drops a posting or brings a dropped one back."
    /// Said when the posting is already dropped.
    public static let droppedNote = "This posting is dropped, and this reading does not bring it back."
    /// Why the action is not offered on a posting no run has triaged.
    public static let untriagedReason = "Not triaged yet: no run has read this posting, so there is no reading to compare."
    /// What Sonnet is given. Never the owner's personal details.
    public static func sentLine(model: String?) -> String {
        "Sent to \(model.flatMap { $0.isEmpty ? nil : $0 } ?? "Sonnet"): the posting's text and your target summary, both marked as untrusted. Never your name, contact details or work authorization."
    }
}

/// The four questions triage asks, in the order the cards show them.
public enum RetriageSlot: String, CaseIterable, Sendable {
    case roleFit = "role_fit", seniority, sponsorship, workMode = "work_mode"

    public var title: String {
        switch self {
        case .roleFit: "Role fit"
        case .seniority: "Seniority"
        case .sponsorship: "Sponsorship"
        case .workMode: "Work mode"
        }
    }
}

/// One slot as ranking used it: its value (nil when there was no reading), the backend that gave it
/// ("manual", "escalate", else the primary's name, or nil) and the backend's probability.
public struct RetriageReading: Codable, Equatable, Sendable {
    public var value: String?
    public var backend: String?
    public var p: Double?

    public init(value: String? = nil, backend: String? = nil, p: Double? = nil) {
        self.value = value
        self.backend = backend
        self.p = p
    }

    private enum CodingKeys: String, CodingKey { case value, backend, p }

    /// Each field alone: one of the wrong type reads as absent. An empty value is no reading.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let v = (try? c.decode(String.self, forKey: .value))?.trimmingCharacters(in: .whitespacesAndNewlines)
        value = v.flatMap { $0.isEmpty ? nil : $0 }
        backend = try? c.decode(String.self, forKey: .backend)
        p = (try? c.decode(Double.self, forKey: .p)).flatMap { $0.isFinite && $0 >= 0 && $0 <= 1 ? $0 : nil }
    }

    /// The value, or nil for "no reading" (absent, empty or the vocabulary's "none").
    public var usableValue: String? { value == "none" ? nil : value }
}

/// What ranking used for a posting at one moment: the four slots and the numbers they led to.
public struct RetriageRanking: Decodable, Equatable, Sendable {
    public var slots: [RetriageSlot: RetriageReading]
    public var relevance: Double?
    public var priority: Double?
    /// Set on the `after` side: the model that read it and when.
    public var model: String?
    public var at: Date?

    public init(slots: [RetriageSlot: RetriageReading] = [:], relevance: Double? = nil, priority: Double? = nil,
                model: String? = nil, at: Date? = nil) {
        self.slots = slots
        self.relevance = relevance
        self.priority = priority
        self.model = model
        self.at = at
    }

    private enum CodingKeys: String, CodingKey { case slots, relevance, priority, model, at }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let raw = (try? c.decode([String: Lenient<RetriageReading>].self, forKey: .slots)) ?? [:]
        var out: [RetriageSlot: RetriageReading] = [:]
        for slot in RetriageSlot.allCases {
            if let reading = raw[slot.rawValue]?.value { out[slot] = reading }
        }
        slots = out
        relevance = (try? c.decode(Double.self, forKey: .relevance)).flatMap { $0.isFinite ? $0 : nil }
        priority = (try? c.decode(Double.self, forKey: .priority)).flatMap { $0.isFinite ? $0 : nil }
        model = try? c.decode(String.self, forKey: .model)
        at = try? c.decode(Date.self, forKey: .at)
    }
}

/// `manual_triage` on a job: Sonnet's stored reading. `current` false means the posting text changed
/// (or is gone) since it was made, so ranking ignores it; it is then shown as out of date, never as
/// current. An absent `current` reads as false: a reading is not called current unless vani said so.
public struct ManualTriage: Codable, Equatable, Sendable {
    /// slot name (`RetriageSlot.rawValue`) to option value.
    public var slots: [String: String]
    public var model: String?
    public var at: Date?
    public var current: Bool
    /// "text_changed" or "text_gone"; present only when `current` is false.
    public var staleReason: String?

    public init(slots: [String: String], model: String? = nil, at: Date? = nil, current: Bool = true, staleReason: String? = nil) {
        self.slots = slots
        self.model = model
        self.at = at
        self.current = current
        self.staleReason = staleReason
    }

    private enum CodingKeys: String, CodingKey {
        case slots, model, at, current
        case staleReason = "stale_reason"
    }

    /// Throws for anything that is not an object holding at least one of the four readings: such a
    /// value has nothing to show, and `Job` reads it as no manual reading.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let raw = (try? c.decode([String: Lenient<String>].self, forKey: .slots)) ?? [:]
        var out: [String: String] = [:]
        for slot in RetriageSlot.allCases {
            if let v = raw[slot.rawValue]?.value?.trimmingCharacters(in: .whitespacesAndNewlines), !v.isEmpty { out[slot.rawValue] = v }
        }
        guard !out.isEmpty else {
            throw DecodingError.dataCorruptedError(forKey: .slots, in: c, debugDescription: "manual_triage holds no reading")
        }
        slots = out
        model = try? c.decode(String.self, forKey: .model)
        at = try? c.decode(Date.self, forKey: .at)
        current = (try? c.decode(Bool.self, forKey: .current)) ?? false
        staleReason = try? c.decode(String.self, forKey: .staleReason)
    }

    public func value(for slot: RetriageSlot) -> String? { slots[slot.rawValue] }
}

/// What a paid call used and cost, as vani reports it. Present on a success and on a failure whose
/// call completed (refused, unsure, unreadable, store), so the money is never hidden.
public struct RetriageCost: Decodable, Equatable, Sendable {
    public var inputTokens: Int?
    public var outputTokens: Int?
    public var usd: Double?

    public init(inputTokens: Int? = nil, outputTokens: Int? = nil, usd: Double? = nil) {
        self.inputTokens = inputTokens
        self.outputTokens = outputTokens
        self.usd = usd
    }

    private enum CodingKeys: String, CodingKey {
        case usd
        case inputTokens = "input_tokens"
        case outputTokens = "output_tokens"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        inputTokens = (try? c.decode(Int.self, forKey: .inputTokens)).flatMap { $0 >= 0 ? $0 : nil }
        outputTokens = (try? c.decode(Int.self, forKey: .outputTokens)).flatMap { $0 >= 0 ? $0 : nil }
        usd = (try? c.decode(Double.self, forKey: .usd)).flatMap { $0.isFinite && $0 >= 0 ? $0 : nil }
    }
}

/// `--dry-run`'s estimate. `spentUSD` and `remainingUSD` are nil when today's spend could not be
/// read: never shown as zero.
public struct RetriageEstimate: Decodable, Equatable, Sendable {
    public var inputTokens: Int?
    public var typicalOutputTokens: Int?
    public var maxOutputTokens: Int?
    public var typicalUSD: Double?
    public var maxUSD: Double?
    public var priceSource: String?
    public var dailyCapUSD: Double?
    public var spentUSD: Double?
    public var remainingUSD: Double?
    public var capReached: Bool

    public init(inputTokens: Int? = nil, typicalOutputTokens: Int? = nil, maxOutputTokens: Int? = nil,
                typicalUSD: Double? = nil, maxUSD: Double? = nil, priceSource: String? = nil,
                dailyCapUSD: Double? = nil, spentUSD: Double? = nil, remainingUSD: Double? = nil, capReached: Bool = false) {
        self.inputTokens = inputTokens
        self.typicalOutputTokens = typicalOutputTokens
        self.maxOutputTokens = maxOutputTokens
        self.typicalUSD = typicalUSD
        self.maxUSD = maxUSD
        self.priceSource = priceSource
        self.dailyCapUSD = dailyCapUSD
        self.spentUSD = spentUSD
        self.remainingUSD = remainingUSD
        self.capReached = capReached
    }

    private enum CodingKeys: String, CodingKey {
        case inputTokens = "input_tokens", typicalOutputTokens = "typical_output_tokens", maxOutputTokens = "max_output_tokens"
        case typicalUSD = "typical_usd", maxUSD = "max_usd", priceSource = "price_source"
        case dailyCapUSD = "daily_cap_usd", spentUSD = "spent_usd", remainingUSD = "remaining_usd", capReached = "cap_reached"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        func count(_ k: CodingKeys) -> Int? { (try? c.decode(Int.self, forKey: k)).flatMap { $0 >= 0 ? $0 : nil } }
        func money(_ k: CodingKeys) -> Double? { (try? c.decode(Double.self, forKey: k)).flatMap { $0.isFinite && $0 >= 0 ? $0 : nil } }
        inputTokens = count(.inputTokens)
        typicalOutputTokens = count(.typicalOutputTokens)
        maxOutputTokens = count(.maxOutputTokens)
        typicalUSD = money(.typicalUSD)
        maxUSD = money(.maxUSD)
        priceSource = try? c.decode(String.self, forKey: .priceSource)
        dailyCapUSD = money(.dailyCapUSD)
        spentUSD = money(.spentUSD)
        remainingUSD = money(.remainingUSD)
        capReached = (try? c.decode(Bool.self, forKey: .capReached)) ?? false
    }
}

/// A reply that names the posting it is about, so one for another posting is refused.
protocol RetriageKeyed {
    var key: String? { get }
}

/// `jobs retriage --dry-run`.
public struct RetriageDryRun: Decodable, Equatable, Sendable, RetriageKeyed {
    public var key: String?
    public var model: String?
    public var estimate: RetriageEstimate
    /// The reading already stored for this posting, when there is one.
    public var manualTriage: ManualTriage?

    private enum CodingKeys: String, CodingKey {
        case key, model, estimate
        case manualTriage = "manual_triage"
    }

    public init(key: String? = nil, model: String? = nil, estimate: RetriageEstimate, manualTriage: ManualTriage? = nil) {
        self.key = key
        self.model = model
        self.estimate = estimate
        self.manualTriage = manualTriage
    }

    /// An estimate is required: without one there is no cost to confirm.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        key = try? c.decode(String.self, forKey: .key)
        model = try? c.decode(String.self, forKey: .model)
        estimate = try c.decode(RetriageEstimate.self, forKey: .estimate)
        manualTriage = (try? c.decode(Lenient<ManualTriage>.self, forKey: .manualTriage))?.value
    }
}

/// A completed re-triage.
public struct RetriageResult: Decodable, Equatable, Sendable, RetriageKeyed {
    public var key: String?
    public var model: String?
    /// The posting's ledger status ("scored", "dropped", "digested", …).
    public var status: String?
    public var before: RetriageRanking?
    public var after: RetriageRanking
    public var cost: RetriageCost?

    public init(key: String? = nil, model: String? = nil, status: String? = nil, before: RetriageRanking? = nil,
                after: RetriageRanking, cost: RetriageCost? = nil) {
        self.key = key
        self.model = model
        self.status = status
        self.before = before
        self.after = after
        self.cost = cost
    }

    private enum CodingKeys: String, CodingKey { case key, model, status, before, after, cost }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        key = try? c.decode(String.self, forKey: .key)
        model = try? c.decode(String.self, forKey: .model)
        status = try? c.decode(String.self, forKey: .status)
        before = try? c.decode(RetriageRanking.self, forKey: .before)
        after = try c.decode(RetriageRanking.self, forKey: .after)
        cost = try? c.decode(RetriageCost.self, forKey: .cost)
    }
}

/// `jobs retriage --clear`.
public struct RetriageCleared: Decodable, Equatable, Sendable, RetriageKeyed {
    public var key: String?
    public var hadManual: Bool?
    public var relevance: Double?
    public var priority: Double?

    private enum CodingKeys: String, CodingKey {
        case key, cleared, relevance, priority
        case hadManual = "had_manual"
    }

    public init(key: String? = nil, hadManual: Bool? = nil, relevance: Double? = nil, priority: Double? = nil) {
        self.key = key
        self.hadManual = hadManual
        self.relevance = relevance
        self.priority = priority
    }

    /// `cleared` must be true: any other reply is not a confirmation.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        guard try c.decode(Bool.self, forKey: .cleared) else {
            throw DecodingError.dataCorruptedError(forKey: .cleared, in: c, debugDescription: "the reading was not cleared")
        }
        key = try? c.decode(String.self, forKey: .key)
        hadManual = try? c.decode(Bool.self, forKey: .hadManual)
        relevance = (try? c.decode(Double.self, forKey: .relevance)).flatMap { $0.isFinite ? $0 : nil }
        priority = (try? c.decode(Double.self, forKey: .priority)).flatMap { $0.isFinite ? $0 : nil }
    }
}

/// Why a re-triage step did not complete. `code` is vani's (`unknown_key`, `no_key`, `cap_reached`,
/// …) or one of this app's own for what vani could not say: `timeout`, `too_large`, `no_reply`,
/// `unreadable_reply`, `bad_request` for a key the app refuses to pass, and the runner's refusals
/// (`account`, `keychain`, `settings`, `config`). `cost` is set when a paid call completed.
public struct RetriageFailure: Error, Equatable, Sendable {
    public var code: String
    public var message: String
    public var cost: RetriageCost?

    public init(code: String, message: String, cost: RetriageCost? = nil) {
        self.code = code
        self.message = message
        self.cost = cost
    }
}

/// A step's result. `cancelled` is the owner stopping it: not a failure, and it has no message.
public enum RetriageOutcome<Value: Sendable>: Sendable {
    case success(Value)
    case failure(RetriageFailure)
    case cancelled
}

extension RetriageOutcome: Equatable where Value: Equatable {}

/// Runs `vani jobs retriage` for one posting. It reuses the draft runner, so the key reaches the child
/// exactly as it does for `vani draft` (through `anthropic-account` when the config names the
/// anthropic provider), and the launch is refused when drafting would be. `VANI_NO_KEYCHAIN=1` is set
/// on every call: the app is the one reader of keys for a `vani` it starts, so vani never asks the
/// Keychain for the owner's OpenRouter key behind it. `estimate` and `clear` start `vani` with no key.
public struct RetriageClient: Sendable {
    /// Ceiling on one paid call.
    public static let runTimeout: TimeInterval = 90
    /// Ceiling on an estimate or a clear: local files only, like any `vani jobs` call.
    public static let quickTimeout: TimeInterval = JobsClient.timeout
    static let maxReplyBytes = 64 * 1024
    static let maxKeyBytes = 512

    public let runner: DraftRunner
    let runTimeout: TimeInterval
    let quickTimeout: TimeInterval

    public init(runner: DraftRunner, runTimeout: TimeInterval = RetriageClient.runTimeout,
                quickTimeout: TimeInterval = RetriageClient.quickTimeout) {
        self.runner = runner
        self.runTimeout = runTimeout
        self.quickTimeout = quickTimeout
    }

    /// The command's flags. The key is last, as the command takes it.
    enum Mode {
        case estimate, run, clear

        var flags: [String] {
            switch self {
            case .estimate: ["--dry-run"]
            case .run: []
            case .clear: ["--clear"]
            }
        }
    }

    static func arguments(_ key: String, _ mode: Mode) -> [String] {
        ["jobs", "retriage", "--json"] + mode.flags + [key]
    }

    /// A key vani could not have issued, or one that would read as a flag, is never passed.
    static func refuse(key: String) -> RetriageFailure? {
        guard !key.isEmpty, key.utf8.count <= maxKeyBytes, !key.hasPrefix("-"),
              !key.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }) else {
            return RetriageFailure(code: "bad_request", message: "this posting's key is not one Vani issued")
        }
        return nil
    }

    /// What it would cost. Asks nothing and spends nothing. Blocks: call it off the main thread.
    public func estimate(_ key: String, cancellation: CancelFlag? = nil) -> RetriageOutcome<RetriageDryRun> {
        perform(key, .estimate, cancellation: cancellation)
    }

    /// Ask Sonnet and store its reading. Spends. Blocks for up to `runTimeout`: call it off the main
    /// thread. `cancellation` stops the child cleanly and returns `.cancelled`.
    public func run(_ key: String, cancellation: CancelFlag? = nil) -> RetriageOutcome<RetriageResult> {
        perform(key, .run, cancellation: cancellation)
    }

    /// Forget the stored reading. Spends nothing.
    public func clear(_ key: String, cancellation: CancelFlag? = nil) -> RetriageOutcome<RetriageCleared> {
        perform(key, .clear, cancellation: cancellation)
    }

    private func perform<T: Decodable & Sendable & RetriageKeyed>(_ key: String, _ mode: Mode,
                                                                  cancellation: CancelFlag?) -> RetriageOutcome<T> {
        if let refusal = Self.refuse(key: key) { return .failure(refusal) }
        let invocation = runner.invoke(arguments: Self.arguments(key, mode), timeout: mode == .run ? runTimeout : quickTimeout,
                                       usesModel: mode == .run, maxOutputBytes: Self.maxReplyBytes,
                                       extraEnvironment: ["VANI_NO_KEYCHAIN": "1"], cancellation: cancellation)
        switch invocation {
        case let .failure(reply):
            if reply.code == "cancelled" { return .cancelled }
            return .failure(RetriageFailure(code: reply.code ?? "provider", message: reply.error ?? "vani could not be started"))
        case let .output(out):
            return decode(out, key: key, usedKey: mode == .run)
        }
    }

    private struct Failure: Decodable {
        var error: String?
        var code: String?
        var cost: RetriageCost?

        private enum CodingKeys: String, CodingKey { case error, code, cost }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            error = try? c.decode(String.self, forKey: .error)
            code = try? c.decode(String.self, forKey: .code)
            cost = try? c.decode(RetriageCost.self, forKey: .cost)
        }
    }

    /// vani's reply, whatever its exit status: its `{"error","code"}` object when it wrote one (the code
    /// and the cost are what the owner is shown), else the success shape.
    private func decode<T: Decodable & Sendable & RetriageKeyed>(_ out: VaniProcess.Outcome, key: String,
                                                                 usedKey: Bool) -> RetriageOutcome<T> {
        if out.truncated {
            return .failure(RetriageFailure(code: "too_large", message: "vani's reply was larger than \(Self.maxReplyBytes >> 10) KB"))
        }
        let decoder = JobsClient.decoder
        if let failure = try? decoder.decode(Failure.self, from: out.stdout), let message = failure.error {
            let code = failure.code.flatMap { $0.isEmpty ? nil : $0 } ?? "unknown"
            // vani ended itself on the SIGTERM this app sent: the owner's own stop, not an error.
            if code == "cancelled" { return .cancelled }
            return .failure(RetriageFailure(code: code, message: message, cost: failure.cost))
        }
        if out.status == 0 {
            do {
                let value = try decoder.decode(T.self, from: out.stdout)
                if let named = value.key, named != key {
                    return .failure(RetriageFailure(code: "unreadable_reply", message: "vani answered for a different posting"))
                }
                return .success(value)
            } catch {
                // A paid call whose reply this app cannot read still names what it cost.
                let cost = (try? decoder.decode(Failure.self, from: out.stdout))?.cost
                return .failure(RetriageFailure(code: "unreadable_reply", message: "vani sent a reply this app cannot read", cost: cost))
            }
        }
        if usedKey, let refusal = AnthropicAccount.refusal(in: out.stderr) {
            return .failure(RetriageFailure(code: "account", message: refusal))
        }
        let tooOld = out.status == 2 ? " (this vani has no re-triage command; rebuild the app)" : ""
        return .failure(RetriageFailure(code: "no_reply", message: "vani jobs retriage exited \(out.status ?? -1) without a reply\(tooOld)"))
    }
}

/// One slot of a comparison: what ranking used before, and what it uses now.
public struct RetriageDiffRow: Equatable, Sendable, Identifiable {
    public var slot: RetriageSlot
    public var title: String { slot.title }
    public var before: String
    public var after: String
    public var changed: Bool
    public var id: String { slot.rawValue }
}

/// One slot of a stored reading.
public struct RetriageValueRow: Equatable, Sendable, Identifiable {
    public var slot: RetriageSlot
    public var title: String { slot.title }
    public var value: String
    public var id: String { slot.rawValue }
}

/// The confirmation sheet's content, from the estimate.
public struct RetriageConfirmation: Equatable, Sendable {
    /// "About $0.005 typically, at most $0.016 · 1,800 input tokens · daily cap $20, $18.75 left".
    public var costText: String
    public var sentText: String
    /// Where the prices come from, for a tooltip; nil when vani did not say.
    public var priceSource: String?
    public var canConfirm: Bool
    /// Why Confirm is disabled; nil when it is not.
    public var blockedReason: String?
    /// Said when a reading already exists and this would replace it.
    public var replacesText: String?
}

/// What an error is shown as: the words, the cost line for a paid call that came to nothing, and
/// whether trying again makes sense (it goes back through the confirmation, never straight to a spend).
public struct RetriageErrorText: Equatable, Sendable {
    public var message: String
    public var costNote: String?
    public var canRetry: Bool
}

/// The words of the feature, as pure functions so the views stay thin and the wording is tested.
public enum RetriagePresentation {
    // MARK: Whether and when

    /// Why the action is unavailable for `job`, or nil when it is offered. `busy`: another re-triage
    /// (this posting's or another's) is running.
    public static func blockedReason(for job: Job, busy: Bool) -> String? {
        if job.isUntriaged { return RetriageWords.untriagedReason }
        if busy { return "Another re-triage is running. Wait for it to finish, or cancel it." }
        return nil
    }

    // MARK: Money and counts

    /// "$0.005", "$0.016", "$18.75", "$20", "<$0.001".
    public static func money(_ v: Double) -> String {
        guard v.isFinite, v > 0 else { return "$0" }
        if v < 0.001 { return "<$0.001" }
        if v < 1 {
            let s = String(format: "%.3f", v)
            return s == "1.000" ? "$1" : "$" + s
        }
        let r = (v * 100).rounded() / 100
        return r == r.rounded() ? String(format: "$%.0f", r) : String(format: "$%.2f", r)
    }

    private static func count(_ n: Int, _ noun: String, locale: Locale) -> String {
        "\(n.formatted(.number.locale(locale))) \(noun)\(n == 1 ? "" : "s")"
    }

    // MARK: Before the spend

    public static func confirmation(for dry: RetriageDryRun, locale: Locale = .current) -> RetriageConfirmation {
        let e = dry.estimate
        var parts: [String] = []
        switch (e.typicalUSD, e.maxUSD) {
        case let (typical?, max?): parts.append("About \(money(typical)) typically, at most \(money(max))")
        case let (nil, max?): parts.append("At most \(money(max))")
        case let (typical?, nil): parts.append("About \(money(typical)) typically")
        case (nil, nil): parts.append("Vani did not estimate the cost")
        }
        if let n = e.inputTokens { parts.append(count(n, "input token", locale: locale)) }
        parts.append(capText(e))

        var blocked: String?
        if e.capReached {
            blocked = "Today's daily cap is already spent (decide.daily_usd.llm in Vani's config), so Sonnet cannot be asked until it resets."
        } else if e.typicalUSD == nil && e.maxUSD == nil {
            blocked = "Vani did not say what this would cost, so it cannot be confirmed."
        }
        let replaces = dry.manualTriage.map { _ in "A reading from Sonnet already exists for this posting. Asking again replaces it." }
        return RetriageConfirmation(costText: parts.joined(separator: " · "), sentText: RetriageWords.sentLine(model: dry.model),
                                    priceSource: e.priceSource, canConfirm: blocked == nil, blockedReason: blocked, replacesText: replaces)
    }

    /// The cap clause: spent, how much is left, or that the spend could not be read.
    static func capText(_ e: RetriageEstimate) -> String {
        if e.capReached {
            return e.dailyCapUSD.map { "the daily cap of \(money($0)) is spent" } ?? "the daily cap is spent"
        }
        switch (e.dailyCapUSD, e.remainingUSD) {
        case let (cap?, remaining?): return "daily cap \(money(cap)), \(money(remaining)) left"
        case let (cap?, nil): return "daily cap \(money(cap)), today's spend could not be read"
        case let (nil, remaining?): return "\(money(remaining)) left today"
        case (nil, nil): return "the daily cap was not reported, and today's spend could not be read"
        }
    }

    // MARK: After the spend

    /// "Sonnet (manual) · 2 minutes ago"; without a time, the name alone.
    public static func header(at: Date?, now: Date = Date(), locale: Locale = .current) -> String {
        guard let at else { return RetriageWords.manualName }
        return RetriageWords.manualName + " · " + relative(at, now: now, locale: locale)
    }

    public static func relative(_ at: Date, now: Date, locale: Locale = .current) -> String {
        if abs(now.timeIntervalSince(at)) < 5 { return "just now" }
        let f = RelativeDateTimeFormatter()
        f.locale = locale
        f.unitsStyle = .full
        return f.localizedString(for: at, relativeTo: now)
    }

    /// One row per slot, before and after, in the slots' own order. An absent reading is "no reading".
    public static func diffRows(before: RetriageRanking?, after: RetriageRanking) -> [RetriageDiffRow] {
        RetriageSlot.allCases.map { slot in
            let b = before?.slots[slot]?.usableValue, a = after.slots[slot]?.usableValue
            return RetriageDiffRow(slot: slot, before: words(b), after: words(a), changed: b != a)
        }
    }

    /// The four values of a stored reading, in the slots' order; a slot it lacks reads "no reading".
    public static func valueRows(_ m: ManualTriage) -> [RetriageValueRow] {
        RetriageSlot.allCases.map { RetriageValueRow(slot: $0, value: words(m.value(for: $0))) }
    }

    private static func words(_ value: String?) -> String {
        guard let value, !value.isEmpty, value != "none" else { return "no reading" }
        return Labels.label(for: value)
    }

    /// "Relevance 71 → 74", "Relevance 74 (unchanged)", "Relevance 74" with no before; nil with no number.
    public static func scoreLine(_ title: String, before: Double?, after: Double?) -> String? {
        guard let a = JobScore.display(after) else { return nil }
        guard let b = JobScore.display(before) else { return "\(title) \(a)" }
        return b == a ? "\(title) \(a) (unchanged)" : "\(title) \(b) → \(a)"
    }

    /// What the refreshed list did, from the numbers the order rests on.
    public static func orderNote(before: RetriageRanking?, after: RetriageRanking) -> String {
        let moved = [(before?.priority, after.priority), (before?.relevance, after.relevance)].contains { b, a in
            guard let b = JobScore.display(b), let a = JobScore.display(a) else { return false }
            return b != a
        }
        if moved { return "The list has been re-sorted." }
        let known = JobScore.display(after.priority) != nil && JobScore.display(before?.priority) != nil
        return known ? "The list has been refreshed; its order did not change." : "The list has been refreshed."
    }

    /// "Charged $0.007 · 1,790 input and 212 output tokens", from what vani reported.
    public static func costSummary(_ cost: RetriageCost?, locale: Locale = .current) -> String? {
        guard let cost else { return nil }
        var parts: [String] = []
        if let usd = cost.usd { parts.append("Charged \(money(usd))") }
        switch (cost.inputTokens, cost.outputTokens) {
        case let (i?, o?): parts.append("\(i.formatted(.number.locale(locale))) input and \(count(o, "output token", locale: locale))")
        case let (i?, nil): parts.append(count(i, "input token", locale: locale))
        case let (nil, o?): parts.append(count(o, "output token", locale: locale))
        case (nil, nil): break
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    /// Shown under a stored reading that no longer applies; nil for a current one.
    public static func staleNote(_ m: ManualTriage) -> String? {
        guard !m.current else { return nil }
        switch m.staleReason {
        case "text_changed": return "This reading is out of date: the posting text changed since Sonnet read it. Ranking ignores it."
        case "text_gone": return "This reading is out of date: the posting text is gone, since the role closed. Ranking ignores it."
        default: return "This reading is out of date. Ranking ignores it."
        }
    }

    /// The Rule-7 sentence, and for a dropped posting the one saying the reading does not bring it back.
    public static func ruleNotes(status: String?) -> [String] {
        status == "dropped" ? [RetriageWords.ruleSeven, RetriageWords.droppedNote] : [RetriageWords.ruleSeven]
    }

    // MARK: Errors

    /// Plain words for a failure; nil for `cancelled` and for nothing to say (the owner stopped it).
    public static func errorText(for failure: RetriageFailure) -> RetriageErrorText? {
        let detail = clipped(failure.message)
        let message: String
        var retry = false
        switch failure.code {
        case "cancelled":
            return nil
        case "bad_request": message = "Vani did not accept the request: \(detail)"
        case "unknown_key": message = "Vani no longer has this posting in its ledger. Refresh the list."
        case "text_gone": message = "The posting's text was cleared after the role closed, so there is nothing to send to Sonnet."
        case "untriaged": message = "No run has triaged this posting yet. Let a run read it first, then re-triage it."
        case "no_llm": message = "No language-model provider is configured. Set llm.provider in Vani's config, then try again."
        case "no_key": message = "No Anthropic key is available. Add it in Settings, then try again."
        case "cap_reached": message = "Today's Claude spending cap is spent (decide.daily_usd.llm in Vani's config). Try again tomorrow."
        case "provider":
            message = "Sonnet could not be reached, or did not answer in time. Nothing was saved."
            retry = true
        case "timeout":
            message = "Sonnet did not answer within \(Int(RetriageClient.runTimeout)) s. Nothing was saved."
            retry = true
        case "refused": message = "Sonnet declined to read this posting. Nothing was saved."
        case "unsure": message = "Sonnet could not tell on at least one of the four readings, so nothing was saved."
        case "unreadable":
            message = "Sonnet's reply could not be used. Nothing was saved."
            retry = true
        case "busy":
            message = "Another re-triage is already running. Wait for it to finish, then try again."
            retry = true
        case "store":
            message = "Sonnet answered, but the reading could not be saved."
            retry = true
        case "config": message = "Vani's configuration cannot be used for this: \(detail)"
        case "account": message = "anthropic-account could not start Vani with an Anthropic key: \(detail). Check the Anthropic account in Settings, or run `anthropic-account doctor`."
        case "keychain":
            message = detail
            retry = true
        case "settings": message = "Vani Apply's settings could not be read: \(detail)"
        case "too_large", "no_reply", "unreadable_reply":
            message = detail
            retry = true
        default: message = detail.isEmpty ? "Re-triage failed (\(clipped(failure.code, max: 40)))." : detail
        }
        return RetriageErrorText(message: message, costNote: failedCostNote(failure), canRetry: retry)
    }

    /// "Sonnet was asked and charged $0.007 but the reply could not be used." for a paid call that came
    /// to nothing; nil when no money was reported (or none was spent).
    static func failedCostNote(_ failure: RetriageFailure) -> String? {
        guard let cost = failure.cost, let usd = cost.usd, usd > 0 else { return nil }
        return "Sonnet was asked and charged \(money(usd)) but the reply could not be used."
    }

    private static func clipped(_ s: String, max: Int = 240) -> String {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.count <= max ? t : String(t.prefix(max)) + "…"
    }
}
