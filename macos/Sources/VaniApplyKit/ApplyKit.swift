import Foundation

// The logic behind the Apply kit card in the Jobs detail pane (VaniApply/ApplyKitView.swift):
// what the card says about the saved answers, which application link it may offer, what a
// draft is prefilled with, and where the four steps stand. No UI and no I/O, so XCTest covers it.
//
// The resume is deliberately absent. A resume is bound to the extension's page key
// (extension/src/applied.ts pageKey, Go twin internal/joburl), which has no Swift
// implementation; the app cannot tell which key a job's apply page will have, so the card
// only explains where the resume is chosen (see ApplyKit.resumeNote).

public enum ApplyKit {
    /// The longest posting text a draft is prefilled with. A scraped description can be
    /// megabytes; a text editor holding that stalls the sheet, and Claude needs far less.
    public static let maxPostingChars = 20_000

    /// Where the per-job resume is chosen. Shown instead of a picker, because the app has no
    /// way to turn a job's URL into the page key the resume is stored under.
    public static let resumeNote = "Choose the resume in the browser popup on the application page. Vani Apply keeps it for that page."

    /// What the draft sheet opens with: the job's own fields, the description cut to
    /// `maxPostingChars`. Values are untrusted board text and are only ever set as text.
    public struct DraftPrefill: Equatable, Sendable {
        public var company: String
        public var title: String
        public var posting: String
    }

    /// `description` is the text the detail pane loaded, which a list row may not carry;
    /// when nil the row's own description is used.
    public static func draftPrefill(_ job: Job, description: String? = nil) -> DraftPrefill {
        DraftPrefill(company: job.company, title: job.title,
                     posting: String((description ?? job.description ?? "").prefix(maxPostingChars)))
    }

    /// "27 saved answers · 3 to review", "No saved answers yet", "1 saved answer".
    public static func readinessLine(total: Int, review: Int) -> String {
        let total = max(0, total)
        let review = min(max(0, review), total)
        guard total > 0 else { return "No saved answers yet" }
        let base = "\(total) saved \(total == 1 ? "answer" : "answers")"
        return review > 0 ? base + " · \(review) to review" : base
    }

    /// The application form's link, only when it is a real http(s) link that is not the
    /// posting itself (compared without the fragment and a trailing slash, and with scheme
    /// and host in lower case), so the card never repeats the main Open Posting button.
    public static func distinctApplyLink(_ job: Job) -> URL? {
        guard let apply = job.applyLink else { return nil }
        guard let posting = job.postingLink else { return apply }
        return comparable(apply) == comparable(posting) ? nil : apply
    }

    static func comparable(_ u: URL) -> String {
        guard var c = URLComponents(url: u, resolvingAgainstBaseURL: false) else { return u.absoluteString }
        c.fragment = nil
        c.scheme = c.scheme?.lowercased()
        c.host = c.host?.lowercased()
        if c.path.count > 1, c.path.hasSuffix("/") { c.path.removeLast() }
        return c.string ?? u.absoluteString
    }

    public struct AnswerCounts: Equatable, Sendable {
        public var total: Int
        public var review: Int
        public init(total: Int, review: Int) { self.total = total; self.review = review }
        public var line: String { ApplyKit.readinessLine(total: total, review: review) }
    }

    public enum StepState: Equatable, Sendable {
        case todo
        /// Nothing to measure (the resume is chosen in the browser).
        case manual
        case done
        case unavailable(String)
    }

    public struct Step: Equatable, Sendable, Identifiable {
        public var number: Int
        public var title: String
        public var state: StepState
        public var id: Int { number }
    }

    /// The four steps. Drafting counts as done only once there are saved answers and none
    /// waits for review; a bank that could not be read is unavailable, never "no answers".
    public static func steps(job: Job, answers: Reading<AnswerCounts>) -> [Step] {
        let draft: StepState
        switch answers {
        case .pending: draft = .todo
        case let .failed(why): draft = .unavailable(why)
        case let .ready(a): draft = a.total > 0 && a.review == 0 ? .done : .todo
        }
        return [
            Step(number: 1, title: "Draft answers", state: draft),
            Step(number: 2, title: "Attach resume", state: .manual),
            Step(number: 3, title: "Open posting & Fill with the extension", state: .todo),
            Step(number: 4, title: "Mark applied", state: job.mark == .applied ? .done : .todo),
        ]
    }
}
