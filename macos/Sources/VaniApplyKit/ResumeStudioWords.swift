import Foundation

// The words of Resume Studio, as pure functions so the views stay thin and the wording is tested.
//
// One rule runs through all of it: a check that did not run is never worded like one that passed.
// Each check state maps to a `ResumeCheckLevel`; only `.ok` is drawn as a pass, and `notRun` (not run,
// could not run, unknown, an unrecognised value, or no value at all) is never `.ok`.

public enum ResumeCheckLevel: Equatable, Sendable {
    /// The check ran and passed.
    case ok
    /// The check ran and passed with a note the owner should read.
    case caution
    /// The check ran and failed, or the version is out of date.
    case bad
    /// The check did not run, could not run, or this app does not know its state.
    case notRun
}

/// One check state in words: a short chip and a sentence.
public struct ResumeCheckWord: Equatable, Sendable {
    public var short: String
    public var long: String
    public var level: ResumeCheckLevel
}

/// What the owner can do about a failure. Each is a choice, never done on its own.
public enum ResumeOffer: Equatable, Sendable {
    /// Drop what went stale and rebuild (no model, nothing spent).
    case revalidate
    /// Build a fresh tailored version (goes back through the cost confirmation).
    case buildAgain
    /// Build the owner's base selection instead (`--allow-base`), because Opus cannot run.
    case buildBase
}

/// A failure in plain words, whether trying again makes sense, and what else the owner may choose.
public struct ResumeFailureText: Equatable, Sendable {
    public var message: String
    /// "The model was paid $0.031 ... " for a build that cost money and saved nothing; nil otherwise.
    public var costNote: String?
    public var canRetry: Bool
    public var offer: ResumeOffer?
}

/// The cost confirmation's content, from the estimate.
public struct ResumeConfirmation: Equatable, Sendable {
    /// "At most $0.42 · up to 2 turns · 18,400 input tokens · daily cap $20, $18.75 left".
    public var costText: String?
    public var sentText: String
    public var priceSource: String?
    /// Opus can run and the cap leaves room: Build may spend.
    public var canConfirm: Bool
    /// Why Build is disabled; nil when it is not.
    public var blockedReason: String?
    /// Opus cannot run: the explicit "Build base selection anyway" choice is offered (it spends nothing).
    public var offersBase: Bool
    /// What a base build is, said beside its button.
    public var baseNote: String?
    /// The job already has a current version, which a new build is made from.
    public var madeFromText: String?
}

public enum ResumeWords {
    /// What the app calls the tailoring model.
    public static let modelName = "Opus 5.5"

    /// The sentence on the card and the confirmation: what is sent, and what never is.
    public static func sentLine(model: String?) -> String {
        "Sent to \(displayModel(model)): the posting's text and the bullets your knowledge base offers for it. Never your name, contact details or work authorization."
    }

    /// "claude-opus-5-5" reads as "Opus 5.5"; any other id is shown as it is.
    public static func displayModel(_ id: String?) -> String {
        guard let id = id?.trimmingCharacters(in: .whitespacesAndNewlines), !id.isEmpty else { return modelName }
        return id == "claude-opus-5-5" ? modelName : id
    }

    // MARK: Check states

    /// A version's eligibility. `fresh` is true when vani just checked it (a `show` without
    /// `--no-check`); false for the state stored when the version was built, which `use` checks
    /// again before binding, so "current" is then worded as what it was.
    public static func eligibility(_ state: String?, fresh: Bool = false) -> ResumeCheckWord {
        switch state {
        case "current":
            return fresh
                ? ResumeCheckWord(short: "Vouched for", long: "Your knowledge base still vouches for every bullet on this version.", level: .ok)
                : ResumeCheckWord(short: "Vouched for when built",
                                  long: "Your knowledge base vouched for every bullet when this version was built. Using it checks again.", level: .ok)
        case "stale":
            return ResumeCheckWord(short: "Out of date",
                                   long: "Some bullets are no longer vouched for by your knowledge base. Revalidate the version, or build again.", level: .bad)
        case "unknown":
            return ResumeCheckWord(short: "Not checked",
                                   long: "This version could not be checked against your knowledge base, so it is not treated as vouched for.", level: .notRun)
        case nil, "":
            return ResumeCheckWord(short: "Not checked", long: "Vani did not say whether your knowledge base vouches for this version.", level: .notRun)
        case let other?:
            return ResumeCheckWord(short: "Unrecognised", long: "Vani reported an eligibility state this app does not know (\(clipped(other, 40))), so it is not treated as vouched for.",
                                   level: .notRun)
        }
    }

    public static func fit(_ state: String?) -> ResumeCheckWord {
        switch state {
        case "passed":
            return ResumeCheckWord(short: "Fits", long: "The page was fitted in Chrome and fits.", level: .ok)
        case "soft":
            return ResumeCheckWord(short: "Fits, with a note",
                                   long: "The page fits, with a layout flaw rkb flagged (see the notes). It can be used; check it in Preview.", level: .caution)
        case "failed":
            return ResumeCheckWord(short: "Does not fit", long: "The page did not fit. This version cannot be used.", level: .bad)
        case "not_run":
            return ResumeCheckWord(short: "Layout not measured", long: "The layout was never measured, so there is no PDF to use.", level: .notRun)
        case "could_not_run":
            return ResumeCheckWord(short: "Layout could not be measured",
                                   long: "Chrome or the PDF tools could not measure the layout, so this page is not known to fit.", level: .notRun)
        case nil, "":
            return ResumeCheckWord(short: "Layout not known", long: "Vani did not say whether the page fits.", level: .notRun)
        case let other?:
            return ResumeCheckWord(short: "Unrecognised", long: "Vani reported a fit state this app does not know (\(clipped(other, 40))), so the page is not treated as fitting.",
                                   level: .notRun)
        }
    }

    public static func lint(_ state: String?) -> ResumeCheckWord {
        switch state {
        case "passed": return ResumeCheckWord(short: "Lint passed", long: "rkb's lint passed.", level: .ok)
        case "failed": return ResumeCheckWord(short: "Lint failed", long: "rkb's lint failed on this page.", level: .bad)
        case "not_run": return ResumeCheckWord(short: "Lint not run", long: "rkb's lint did not run.", level: .notRun)
        default: return ResumeCheckWord(short: "Lint unknown", long: "Vani did not report a lint result this app knows, so the page is not treated as linted.", level: .notRun)
        }
    }

    public static func figures(_ state: String?) -> ResumeCheckWord {
        switch state {
        case "verified": return ResumeCheckWord(short: "Figures verified", long: "Every figure on the page is one the validator backs.", level: .ok)
        case "dropped":
            return ResumeCheckWord(short: "Figures dropped", long: "A figure the validator does not back was left off the page.", level: .caution)
        case "not_run": return ResumeCheckWord(short: "Figures not checked", long: "Figures were not checked.", level: .notRun)
        default: return ResumeCheckWord(short: "Figures unknown", long: "Vani did not report a figures result this app knows, so they are not treated as verified.", level: .notRun)
        }
    }

    /// "Tailored by Opus", "Base selection", "Edited by hand", "Revalidated".
    public static func source(_ source: String?) -> String {
        switch source {
        case "tailored": return "tailored"
        case "base": return "base selection (not tailored)"
        case "edited": return "edited by hand"
        case "revalidated": return "revalidated"
        case nil, "": return "source not known"
        case let other?: return clipped(other, 30)
        }
    }

    // MARK: Money

    /// "$0.42", "<$0.001": the retriage word for money, so the two features say a price one way.
    public static func money(_ v: Double) -> String { RetriagePresentation.money(v) }

    /// "$0.031", or "at least $0.031" when a turn's cost was unknown, so the figure is a lower bound.
    public static func costPhrase(_ cost: ResumeCost) -> String? {
        guard let usd = cost.usd, usd > 0 else { return nil }
        return cost.complete ? money(usd) : "at least \(money(usd))"
    }

    // MARK: Before the spend

    public static func confirmation(for dry: ResumeDryRun, locale: Locale = .current) -> ResumeConfirmation {
        let model = displayModel(dry.model)
        let madeFrom = dry.current > 0 ? "A new version is made from v\(dry.current), the current one, and replaces it as the current version." : nil
        guard dry.opusUsable, let e = dry.estimate else {
            let why = dry.noModel.map { clipped($0, 200) } ?? "no reason was given"
            return ResumeConfirmation(
                costText: nil, sentText: sentLine(model: dry.model), priceSource: nil, canConfirm: false,
                blockedReason: "\(model) cannot run right now (\(why)), so a tailored resume cannot be built and nothing can be spent.",
                offersBase: true,
                baseNote: "Build base selection anyway uses your base choice of bullets, with no model and no cost. It is a fallback, not tailored to this posting, and it does not replace a current version this job already has.",
                madeFromText: madeFrom)
        }
        var parts = ["At most \(money(e.maxUSD))", "up to \(e.turns) turn\(e.turns == 1 ? "" : "s")",
                     "\(e.inputTokens.formatted(.number.locale(locale))) input tokens at most", capText(dry.budget, capReached: e.capReached)]
        parts.removeAll { $0.isEmpty }
        let blocked = e.capReached
            ? "What is left of today's cap (decide.daily_usd.llm in Vani's config) is less than the first turn can cost, so \(model) cannot be asked until it resets."
            : nil
        return ResumeConfirmation(costText: parts.joined(separator: " · "), sentText: sentLine(model: dry.model), priceSource: e.priceSource,
                                  canConfirm: blocked == nil, blockedReason: blocked, offersBase: false, baseNote: nil, madeFromText: madeFrom)
    }

    /// The cap clause: spent, how much is left, or that the spend could not be read.
    static func capText(_ b: ResumeBudget, capReached: Bool) -> String {
        if capReached { return b.dailyCapUSD.map { "the daily cap of \(money($0)) is nearly spent" } ?? "the daily cap is nearly spent" }
        switch (b.dailyCapUSD, b.remainingUSD) {
        case let (cap?, remaining?): return "daily cap \(money(cap)), \(money(remaining)) left"
        case let (cap?, nil): return "daily cap \(money(cap)), today's spend could not be read"
        case let (nil, remaining?): return "\(money(remaining)) left today"
        case (nil, nil): return "the daily cap was not reported, and today's spend could not be read"
        }
    }

    // MARK: After

    /// "v3 saved: tailored, 2 pages, fits. It is now the current version." / why it is not current.
    public static func savedLine(_ saved: ResumeSaved) -> String {
        let v = saved.version
        var line = "v\(v.version) saved: \(source(v.source)), \(pagesPhrase(v.pages)), \(fit(v.checks.fit).short.lowercased())."
        if saved.currentMoved {
            line += " It is now the current version."
        } else if let note = saved.currentNote, !note.isEmpty {
            line += " " + clipped(note, 240)
        }
        if !saved.droppedStale.isEmpty { line += " Dropped: \(saved.droppedStale.joined(separator: ", "))." }
        return line
    }

    public static func pagesPhrase(_ pages: Int) -> String {
        pages <= 0 ? "pages not known" : "\(pages) page\(pages == 1 ? "" : "s")"
    }

    /// "Charged $0.031" for a version's own build cost; nil when no money was spent or none was reported.
    public static func costLine(_ cost: ResumeCost?) -> String? {
        guard let cost, let phrase = costPhrase(cost) else { return nil }
        return "Charged \(phrase) over \(cost.turns) turn\(cost.turns == 1 ? "" : "s")"
    }

    // MARK: Errors

    /// Every code vani's `jobs resume` can write (cmd/vani/jobsresume.go, internal/rkbresume) and the
    /// ones this app adds for what it could not ask. A test words each one.
    public static let knownCodes: [String] = [
        "bad_request", "unknown_key", "ledger", "config", "store", "not_found", "damaged", "unsupported", "full", "busy",
        "not_usable", "stale", "unchecked", "not_stale", "failed", "kb", "render", "lint", "fit", "too_large", "pages",
        "timeout", "cancelled", "kb_changed", "no_model", "ineligible", "bad_edit", "figures",
        "account", "keychain", "settings", "provider", "budget", "budget_exhausted", "cap_reached", "no_reply", "unreadable_reply", "bind",
    ]

    /// Plain words for a failure; nil for a `cancelled` that cost nothing (the owner stopped it).
    public static func failure(_ f: ResumeFailure) -> ResumeFailureText? {
        let detail = clipped(f.message)
        var message: String
        var retry = false
        var offer: ResumeOffer?
        switch f.code {
        case "cancelled":
            guard f.cost != nil else { return nil }
            message = "Stopped. Nothing was saved."
        case "busy":
            message = "Another resume build or render is already running (Vani allows one at a time). Nothing was spent. Try again when it finishes."
            retry = true
        case "kb_changed":
            message = "Your knowledge base changed while the resume was being built, so the build was thrown away. Build again."
            offer = .buildAgain
            retry = true
        case "no_model":
            message = "\(modelName) cannot run right now (\(detail)), so no tailored resume was built and nothing was spent."
            offer = .buildBase
        case "stale":
            let what = f.eligibility?.summary.map { " (\($0))" } ?? ""
            message = "This version is out of date: your knowledge base no longer vouches for every bullet on it\(what). Revalidate it, or build again."
            offer = .revalidate
        case "unchecked":
            message = "This version could not be checked against your knowledge base, so Vani will not use it. Nothing was changed. Try again."
            retry = true
        case "ineligible":
            message = "The edit uses a bullet the knowledge base does not offer for this page: \(detail)"
        case "bad_edit":
            message = "The edit is not a page Vani can build: \(detail)"
        case "not_stale":
            message = "Your knowledge base still vouches for this version, so there is nothing to revalidate. If it cannot be used for another reason (its layout was never measured, or it was recorded unchecked), build again."
            offer = .buildAgain
        case "not_usable":
            message = "This version cannot be used: its layout did not pass, its bullets were not vouched for when it was built, or it has no PDF. \(detail) Build again."
            offer = .buildAgain
        case "timeout":
            message = "Vani did not finish in time. Nothing is saved unless a new version appears in the list."
            retry = true
        case "config": message = "Vani's configuration cannot build a resume: \(detail)"
        case "kb": message = "The knowledge base could not be read: \(detail)"
        case "fit": message = "The page could not be made to fit its page budget: \(detail)"
        case "lint": message = "rkb's lint refused the page: \(detail)"
        case "render": message = "rkb could not render the page: \(detail)"
        case "pages": message = "The page is not one to two pages long: \(detail)"
        case "figures": message = "A figure on the page is not one the validator backs: \(detail)"
        case "too_large": message = detail
        case "unknown_key": message = "Vani no longer has this posting in its ledger. Refresh the list."
        case "ledger": message = "Vani could not read its ledger: \(detail)"
        case "bad_request": message = "Vani did not accept the request: \(detail)"
        case "not_found": message = "That version does not exist any more: \(detail)"
        case "damaged":
            message = "This job's saved resume is damaged: \(detail) Nothing was changed. It is not the same as having no resume."
        case "unsupported":
            message = "This job's saved resumes were written by a newer Vani than this one can read: \(detail) Nothing was changed."
        case "store": message = "Vani could not read or write its resume store: \(detail)"
        case "full": message = "Vani's resume store is full (20 versions for a job, 200 jobs): \(detail)"
        case "failed": message = "Vani could not finish this resume step: \(detail)"
        case "budget", "budget_exhausted", "cap_reached":
            message = "Today's Claude spending cap is spent (decide.daily_usd.llm in Vani's config). Try again tomorrow."
        case "provider":
            message = "\(modelName) could not be reached, or did not answer in time. Nothing was saved."
            retry = true
        case "account":
            message = "anthropic-account could not start Vani with an Anthropic key: \(detail). Check the Anthropic account in Settings, or run `anthropic-account doctor`."
        case "keychain":
            message = detail
            retry = true
        case "settings": message = "Vani Apply's settings could not be read: \(detail)"
        case "no_reply", "unreadable_reply":
            message = detail
            retry = true
        case "bind":
            // This app's own: vani bound the version, the browsers' copy could not be saved. The sentence is complete.
            message = f.message
            retry = true
        default:
            message = detail.isEmpty ? "The resume step failed (\(clipped(f.code, 40)))." : detail
        }
        var note: String?
        if let cost = f.cost, let phrase = costPhrase(cost) {
            note = "\(modelName) was asked and charged \(phrase), but nothing was saved"
                + (f.attemptRecorded ? "; Vani recorded the attempt." : "; Vani could not record the attempt.")
        }
        return ResumeFailureText(message: message, costNote: note, canRetry: retry, offer: offer)
    }

    static func clipped(_ s: String, _ max: Int = 240) -> String {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.count <= max ? t : String(t.prefix(max)) + "…"
    }
}
