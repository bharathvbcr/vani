import Foundation

// Pure presentation rules for the Jobs pane: which postings are the same role,
// a company's monogram, the score a row shows, and the wording of the list's
// state lines. No SwiftUI here, so each rule is tested without a window.

/// Postings of one role. Boards repost a role (a new requisition, a second
/// location), so the same company and title can appear under several keys.
public struct PostingGroups: Sendable {
    /// Job keys by group, in the order each group first appears in the list.
    public let groups: [[String]]
    private let siblingsByKey: [String: [Job]]

    /// Groups by the normalized (company, title). A job with a blank company or
    /// title is its own group: two unnamed postings are not known to be one role.
    public init(_ jobs: [Job]) {
        var order: [String] = []
        var members: [String: [Job]] = [:]
        var slot: [String: Int] = [:]
        var out: [[String]] = []
        for job in jobs {
            guard let k = JobListPresentation.groupKey(company: job.company, title: job.title) else {
                out.append([job.key])
                continue
            }
            if members[k] == nil {
                order.append(k)
                slot[k] = out.count
                out.append([])
            }
            members[k, default: []].append(job)
            out[slot[k]!].append(job.key)
        }
        var sib: [String: [Job]] = [:]
        for k in order {
            let all = members[k] ?? []
            guard all.count > 1 else { continue }
            for job in all {
                // A repeated key is one posting listed twice, not a sibling.
                sib[job.key] = all.filter { $0.key != job.key }
            }
        }
        groups = out
        siblingsByKey = sib
    }

    /// The other postings of the same role, in list order. Empty when this one
    /// stands alone or the key is unknown.
    public func others(of key: String) -> [Job] { siblingsByKey[key] ?? [] }
}

/// A company's identity mark: up to two initials and a stable hue.
public struct CompanyMonogram: Equatable, Sendable {
    public let initials: String
    /// 0..<1, from a hash that is the same on every launch and every Mac.
    public let hue: Double

    public init(company: String) {
        initials = Self.initials(of: company)
        hue = Self.hue(of: company)
    }

    /// First letter or digit of the first two words ("Acme Robotics" is "AR",
    /// "OpenAI" is "O"). A name with none (an emoji) uses its first visible
    /// character; a blank name uses "?".
    static func initials(of company: String) -> String {
        var picked: [String] = []
        for word in company.split(whereSeparator: \.isWhitespace) {
            guard let c = word.first(where: { $0.isLetter || $0.isNumber }) else { continue }
            picked.append(String(c).uppercased())
            if picked.count == 2 { break }
        }
        if !picked.isEmpty { return picked.joined() }
        if let c = company.first(where: { !$0.isWhitespace }) { return String(c) }
        return "?"
    }

    /// FNV-1a over the folded name's unicode scalars. Swift's `hashValue` is
    /// seeded per process, so it would recolour every company on each launch.
    static func hue(of company: String) -> Double {
        var h: UInt32 = 2_166_136_261
        for s in JobListPresentation.normalized(company).unicodeScalars {
            h ^= s.value
            h = h &* 16_777_619
        }
        return Double(h % 360) / 360
    }
}

/// The score a row shows, and the word for how it grades.
public struct JobScore: Equatable, Sendable {
    public enum Basis: String, Sendable { case priority, relevance }
    public enum Grade: Sendable { case high, medium, low }

    /// 0-100, rounded.
    public let value: Int
    public let basis: Basis
    public let grade: Grade

    /// The cut points the score bars use: 60 and up is high, 35 and up medium.
    public static func grade(_ v: Int) -> Grade { v >= 60 ? .high : v >= 35 ? .medium : .low }

    /// A finite score clamped to 0...100; nil for a missing or NaN/infinite one.
    /// A check that could not run is not drawn as a zero.
    public static func display(_ v: Double?) -> Int? {
        guard let v, v.isFinite else { return nil }
        return Int(min(max(v, 0), 100).rounded())
    }

    /// The score the list's sort ranks by: relevance for the Relevance sort,
    /// priority for every other, each falling back to the other when absent.
    public static func forRow(_ job: Job, sort: JobSort) -> JobScore? {
        // An untriaged posting has no score; one a reply carried anyway is not drawn.
        guard !job.isUntriaged else { return nil }
        let order: [Basis] = sort == .relevance ? [.relevance, .priority] : [.priority, .relevance]
        for basis in order {
            if let v = display(basis == .priority ? job.priority : job.relevance) {
                return JobScore(value: v, basis: basis, grade: grade(v))
            }
        }
        return nil
    }

    public var helpText: String {
        switch basis {
        case .priority: "Priority \(value) of 100: relevance discounted by age"
        case .relevance: "Relevance \(value) of 100: overlap with your target summary and the role's rank or fit"
        }
    }
}

public enum JobListPresentation {
    /// Lower-cased, accents folded, runs of whitespace collapsed.
    public static func normalized(_ s: String) -> String {
        s.folding(options: [.caseInsensitive, .diacriticInsensitive], locale: nil)
            .split(whereSeparator: \.isWhitespace)
            .joined(separator: " ")
    }

    /// The identity of a role, or nil when the company or title is blank.
    public static func groupKey(company: String, title: String) -> String? {
        let c = normalized(company), t = normalized(title)
        guard !c.isEmpty, !t.isEmpty else { return nil }
        return c + "\u{1F}" + t
    }

    /// The sort in words, and which score the badge shows.
    public static func sortCaption(_ sort: JobSort, view: JobView = .new) -> String {
        guard view.isRanked else {
            return "Sorted by \(sort.title). Not triaged yet: no fit, relevance or priority until a run reads these."
        }
        let badge = sort == .relevance ? "relevance" : "priority"
        return "Sorted by \(sort.title). Badge: \(badge)."
    }

    /// "Also posted 12d ago" for one other posting, "3 other postings" for more;
    /// nil when the role stands alone.
    public static func alsoPosted(_ others: [Job]) -> String? {
        switch others.count {
        case 0: return nil
        case 1:
            guard let age = others[0].ageLabel else { return "Also posted once more" }
            return age.hasSuffix("today") ? "Also posted today" : "Also posted \(age) ago"
        default: return "\(others.count) other postings"
        }
    }

    /// The count a view tab shows, or nil when the page or its count is unknown.
    /// Unknown is not zero.
    public static func count(_ page: JobsPage?, _ view: JobView) -> Int? {
        page?.counts[view.rawValue]
    }

    /// "1,186 awaiting triage", or nil when the count is unknown or nothing waits.
    public static func triageBanner(_ pending: PendingTriage?, locale: Locale = .current) -> String? {
        guard let pending, pending.total > 0 else { return nil }
        return "\(pending.total.formatted(.number.locale(locale))) awaiting triage"
    }

    /// Vani's `insight.warning` (a fixed sentence, never posting text) for the
    /// banner above the list, trimmed; nil when it is absent, empty or blank.
    public static func insightWarning(_ insight: JobsInsight?) -> String? {
        guard let w = insight?.warning?.trimmingCharacters(in: .whitespacesAndNewlines), !w.isEmpty else { return nil }
        return w
    }

    /// "Showing 1,000 of 1,682", or nil when the list is whole or the numbers
    /// make no sense.
    public static func capNotice(shown: Int, total: Int, locale: Locale = .current) -> String? {
        guard shown >= 0, total > shown else { return nil }
        let n = IntegerFormatStyle<Int>.number.locale(locale)
        return "Showing \(shown.formatted(n)) of \(total.formatted(n))"
    }

    /// A description as paragraphs: split at blank lines, line breaks inside a
    /// paragraph kept (bullets), surrounding whitespace trimmed, empties dropped.
    public static func paragraphs(_ text: String) -> [String] {
        var out: [String] = []
        var current: [Substring] = []
        func flush() {
            let p = current.joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
            if !p.isEmpty { out.append(p) }
            current = []
        }
        for line in text.replacingOccurrences(of: "\r\n", with: "\n").replacingOccurrences(of: "\r", with: "\n")
            .split(separator: "\n", omittingEmptySubsequences: false) {
            if line.allSatisfy(\.isWhitespace) { flush() } else { current.append(line) }
        }
        flush()
        return out
    }
}
