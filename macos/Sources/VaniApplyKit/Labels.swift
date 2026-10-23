import Foundation

/// The label vocabularies, owned by Go (internal/jobview/labels.go) and read
/// once from `vani labels --json`: fit, status, category and the triage slots'
/// options, each as value and display string in display order. Menus, chips and
/// charts show these strings. The golden replay is
/// internal/jobview/testdata/labels.golden.json (LabelsTests).
///
/// A table of a schema version this build does not know fails to decode,
/// loudly; the app then shows the built-in `fallback` and says so
/// (`Labels.source`), never silently.
public struct LabelVocabulary: Decodable, Equatable, Sendable {
    /// The schema version this build reads (jobview.LabelsVersion).
    public static let supportedVersion = 1

    public struct DecodeError: Error, Equatable, CustomStringConvertible, LocalizedError {
        public var version: Int
        public var description: String {
            "label table schema version \(version) is not supported (this app reads \(LabelVocabulary.supportedVersion)); update Vani Apply or vani"
        }
        public var errorDescription: String? { description }
    }

    public struct Entry: Decodable, Equatable, Sendable {
        public var value: String
        public var label: String
        public init(_ value: String, _ label: String) { self.value = value; self.label = label }
    }

    public var v: Int
    public var fit: [Entry]
    public var status: [Entry]
    public var category: [Entry]
    /// seniority, work_mode and sponsorship options.
    public var triage: [String: [Entry]]

    private let words: [String: String]

    public init(v: Int, fit: [Entry], status: [Entry], category: [Entry], triage: [String: [Entry]]) {
        self.v = v
        self.fit = fit
        self.status = status
        self.category = category
        self.triage = triage
        var w: [String: String] = [:]
        for e in fit + status + category + triage.values.flatMap({ $0 }) { w[e.value] = e.label }
        words = w
    }

    private enum CodingKeys: String, CodingKey { case v, fit, status, category, triage }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        // The version is checked before any other field: a changed shape is a
        // version problem, not a missing key.
        let v = try c.decode(Int.self, forKey: .v)
        guard v == Self.supportedVersion else { throw DecodeError(version: v) }
        self.init(v: v,
                  fit: try c.decode([Entry].self, forKey: .fit),
                  status: try c.decode([Entry].self, forKey: .status),
                  category: try c.decode([Entry].self, forKey: .category),
                  triage: try c.decode([String: [Entry]].self, forKey: .triage))
    }

    /// The words for an option value of any vocabulary, or nil when none has it.
    public func label(for value: String) -> String? { words[value] }

    public func values(_ entries: [Entry]) -> [String] { entries.map(\.value) }

    /// FALLBACK: the vocabularies as of this build, used only while `vani
    /// labels --json` cannot be read (an older vani, a failed launch).
    /// LabelsTests fails if it drifts from the golden file; the Go table is the owner.
    public static let fallback = LabelVocabulary(
        v: supportedVersion,
        fit: [Entry("strong_fit", "Strong fit"), Entry("partial_fit", "Partial fit"), Entry("adjacent", "Adjacent"),
              Entry("mismatch_seniority", "Seniority mismatch"), Entry("mismatch_domain", "Different field")],
        status: [Entry("discovered", "Discovered"), Entry("live", "Live"), Entry("triaged", "Triaged"), Entry("unsure", "Unsure"),
                 Entry("scored", "Scored"), Entry("explained", "Explained"), Entry("digested", "Digested"),
                 Entry("dead", "Closed"), Entry("duplicate", "Duplicate"), Entry("dropped", "Dropped")],
        category: [Entry("ai-engineer", "AI Engineer"), Entry("agent-systems", "Agent Systems"),
                   Entry("ml-inference-systems", "ML Inference Systems"), Entry("ml-research", "ML Research"),
                   Entry("biomedical-ai", "Biomedical AI"), Entry("biomedical-research", "Biomedical Research"),
                   Entry("human-factors-medical-devices", "Human Factors"), Entry("other", "Other")],
        triage: [
            "sponsorship": [Entry("sponsors_visa", "Sponsors visas"), Entry("no_sponsorship", "No sponsorship"),
                            Entry("citizenship_or_clearance_required", "Citizenship or clearance"), Entry("not_stated", "Not stated")],
            "work_mode": [Entry("remote", "Remote"), Entry("hybrid", "Hybrid"), Entry("onsite", "On-site"), Entry("not_stated", "Not stated")],
            "seniority": [Entry("intern_or_new_grad", "Intern / new grad"), Entry("mid", "Mid-level"), Entry("senior", "Senior"),
                          Entry("staff_plus", "Staff+"), Entry("manager", "Manager")],
        ])
}

/// The vocabulary the app shows, process-wide. It starts as the built-in
/// fallback with `source == .fallback`; `install` replaces it with vani's table.
public enum Labels {
    public enum Source: Equatable, Sendable {
        /// Read from `vani labels --json`.
        case vani
        /// The built-in table: vani's could not be read.
        case fallback
    }

    private static let lock = NSLock()
    nonisolated(unsafe) private static var state = (vocabulary: LabelVocabulary.fallback, source: Source.fallback)

    public static var current: LabelVocabulary { lock.withLock { state.vocabulary } }
    public static var source: Source { lock.withLock { state.source } }

    /// Make vani's table the one shown.
    public static func install(_ vocabulary: LabelVocabulary) {
        lock.withLock { state = (vocabulary, .vani) }
    }

    /// Back to the built-in table (tests, and a vani that stopped answering).
    public static func useFallback() {
        lock.withLock { state = (.fallback, .fallback) }
    }

    /// An option value in words: "none" is no reading; a value no table knows
    /// shows with underscores and hyphens as spaces.
    public static func label(for value: String) -> String {
        if value == "none" { return "No reading" }
        return current.label(for: value) ?? value.replacingOccurrences(of: "_", with: " ").replacingOccurrences(of: "-", with: " ")
    }
}

extension JobsClient {
    /// `vani labels --json`: reads no config, ledger or state.
    public func labels() throws -> LabelVocabulary { try call(["labels", "--json"]) }
}
