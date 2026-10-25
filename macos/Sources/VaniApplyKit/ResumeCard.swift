import Foundation

// The Resume card in a job's detail pane, and the line in the Apply kit's step 2, as data: what a job's
// `resume` summary (`vani jobs show --json`, internal/jobview) says, in words, and what the owner may do.
//
// The summary describes the CURRENT version. Its fit and eligibility are what was recorded when the
// version was built; `use` checks eligibility again before binding, so a "current" here is worded as
// "vouched for when built" and a Use can still come back stale.

/// `resume` on a job: the version counts and the current version's checks. `error` is "unreadable" or
/// "unsupported" when the job's store could not be read; the numbers are then not known, and that is
/// never the same as having no resume.
public struct ResumeSummary: Codable, Equatable, Sendable {
    public var current: Int
    public var used: Int
    public var versions: Int
    public var pages: Int
    public var fit: String?
    public var eligibility: String?
    public var source: String?
    public var error: String?

    public init(current: Int = 0, used: Int = 0, versions: Int = 0, pages: Int = 0, fit: String? = nil,
                eligibility: String? = nil, source: String? = nil, error: String? = nil) {
        self.current = current
        self.used = used
        self.versions = versions
        self.pages = pages
        self.fit = fit
        self.eligibility = eligibility
        self.source = source
        self.error = error
    }

    /// What a `resume` value this app cannot read is shown as: never as "no resume".
    public static let unreadableReply = ResumeSummary(error: "unreadable_reply")

    private enum CodingKeys: String, CodingKey { case current, used, versions, pages, fit, eligibility, source, error }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        func count(_ k: CodingKeys) -> Int { (try? c.decode(Int.self, forKey: k)).flatMap { $0 >= 0 ? $0 : nil } ?? 0 }
        current = count(.current)
        used = count(.used)
        versions = count(.versions)
        pages = count(.pages)
        fit = try? c.decode(String.self, forKey: .fit)
        eligibility = try? c.decode(String.self, forKey: .eligibility)
        source = try? c.decode(String.self, forKey: .source)
        error = try? c.decode(String.self, forKey: .error)
    }
}

/// What "Use for this job" does for the current version.
public enum ResumeUseAction: Equatable, Sendable {
    /// There is no current version to use.
    case none
    /// The current version is the one in use.
    case inUse(version: Int)
    /// It can be used: it fits, was vouched for when built and has a PDF. Use checks it again.
    case available(version: Int)
    /// It cannot be used as it is. `offer` says what would help, as a choice.
    case blocked(version: Int, reason: String, offer: ResumeOffer)
}

public struct ResumeCard: Equatable, Sendable {
    public enum Kind: Equatable, Sendable {
        /// The job has no resume version.
        case none
        /// The store could not be read: this is not "no resume".
        case unreadable(String)
        case ready
    }

    public var kind: Kind
    /// "v3 · 2 pages" for the current version.
    public var headline: String?
    public var eligibility: ResumeCheckWord?
    public var fit: ResumeCheckWord?
    public var sourceText: String?
    /// "v2 is in use for this job.", "v2 is in use; v3 is the current version.", nil when none is.
    public var usedLine: String?
    public var use: ResumeUseAction
    public var versions: Int
    /// The version in use, 0 when none.
    public var usedVersion: Int
    public var currentVersion: Int
}

public enum ResumeCardPresentation {
    public static func card(for summary: ResumeSummary?) -> ResumeCard {
        guard let s = summary else {
            return ResumeCard(kind: .none, use: .none, versions: 0, usedVersion: 0, currentVersion: 0)
        }
        if let error = s.error {
            let words: String
            switch error {
            case "unsupported": words = "This job's saved resumes were written by a newer Vani than this one can read. Nothing was changed."
            case "unreadable": words = "This job's saved resumes could not be read; the folder may be damaged. Nothing was changed. This is not the same as having none."
            default: words = "Vani's report of this job's saved resumes could not be read by this app. This is not the same as having none."
            }
            return ResumeCard(kind: .unreadable(words), use: .none, versions: 0, usedVersion: 0, currentVersion: 0)
        }
        guard s.versions > 0 || s.current > 0 || s.used > 0 else {
            return ResumeCard(kind: .none, use: .none, versions: 0, usedVersion: 0, currentVersion: 0)
        }
        var card = ResumeCard(kind: .ready, use: .none, versions: s.versions, usedVersion: s.used, currentVersion: s.current)
        if s.current > 0 {
            card.headline = "v\(s.current) · \(ResumeWords.pagesPhrase(s.pages))"
            card.eligibility = ResumeWords.eligibility(s.eligibility)
            card.fit = ResumeWords.fit(s.fit)
            card.sourceText = ResumeWords.source(s.source)
        }
        switch (s.used, s.current) {
        case (0, _): card.usedLine = nil
        case let (u, c) where u == c: card.usedLine = "v\(u) is in use for this job."
        case let (u, 0): card.usedLine = "v\(u) is in use for this job."
        case let (u, c): card.usedLine = "v\(u) is in use; v\(c) is the current version."
        }
        card.use = useAction(s)
        return card
    }

    /// Whether the current version can be used, from what was recorded when it was built.
    static func useAction(_ s: ResumeSummary) -> ResumeUseAction {
        guard s.current > 0 else { return .none }
        if s.used == s.current { return .inUse(version: s.current) }
        let v = s.current
        let elig = ResumeWords.eligibility(s.eligibility)
        if elig.level != .ok {
            // Stale, not checked, or a state this app does not know: Revalidate re-checks it against the bank.
            return .blocked(version: v, reason: elig.long, offer: .revalidate)
        }
        let fit = ResumeWords.fit(s.fit)
        if fit.level != .ok && fit.level != .caution {
            return .blocked(version: v, reason: fit.long, offer: .buildAgain)
        }
        if s.pages <= 0 {
            return .blocked(version: v, reason: "This version was never fitted to pages, so it has no PDF to use.", offer: .buildAgain)
        }
        return .available(version: v)
    }

    /// The Apply kit's step 2 line about the tailored resume; nil when the job has none (the step then says
    /// only what the browsers' own copy says).
    public static func applyKitLine(for summary: ResumeSummary?, browserCopy: Bool) -> String? {
        guard let s = summary, s.error == nil else { return nil }
        if s.used > 0 {
            return browserCopy
                ? "Tailored resume v\(s.used) is the one attached for this job."
                : "v\(s.used) is marked in use, but the browsers have no saved copy of its PDF. Use it again to save one."
        }
        if s.current > 0 { return "A tailored resume (v\(s.current)) is built but not in use yet." }
        return nil
    }
}
