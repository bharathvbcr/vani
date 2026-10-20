import Foundation
import XCTest

/// A fake `vani jobs` over four made-up roles. It records each argument list
/// in calls.log and replies from the JSON files beside it: the list in
/// standard order, by freshness for `--sort fresh`, strong fits only for
/// `--fit strong_fit`; the stats; and each job for `jobs show`.
struct JobsFixture {
    struct Role {
        var key, company, title, fit, freshAt: String
        var ageDays: Int
        var relevance: Double
    }

    /// In the standard order (rank, then fit). The scatter places them apart
    /// (age on a square-root axis, relevance 0-100), so a click on one dot is
    /// never within reach of another.
    static let roles: [Role] = [
        Role(key: "fx:alpha:1", company: "Alpha Labs", title: "Platform Engineer", fit: "strong_fit", freshAt: "2026-10-10T12:00:00Z", ageDays: 9, relevance: 85),
        Role(key: "fx:delta:4", company: "Delta Tools", title: "Backend Engineer", fit: "strong_fit", freshAt: "2026-10-15T12:00:00Z", ageDays: 4, relevance: 20),
        Role(key: "fx:gamma:3", company: "Gamma Works", title: "Site Reliability Engineer", fit: "adjacent", freshAt: "2026-08-01T12:00:00Z", ageDays: 79, relevance: 60),
        Role(key: "fx:beta:2", company: "Beta Systems", title: "Data Engineer", fit: "partial_fit", freshAt: "2026-10-18T12:00:00Z", ageDays: 1, relevance: 40),
    ]

    static var standardOrder: [String] { roles.map(\.key) }
    /// fresh_at descending (RFC 3339 UTC strings order as their times do).
    static var freshOrder: [String] { roles.sorted { $0.freshAt > $1.freshAt }.map(\.key) }
    static var strongOrder: [String] { roles.filter { $0.fit == "strong_fit" }.map(\.key) }

    let dir: URL

    init(_ t: XCTestCase, labels: Bool = true) throws {
        dir = FileManager.default.temporaryDirectory.appendingPathComponent("vani-apply-ui-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let dir = dir
        t.addTeardownBlock { try? FileManager.default.removeItem(at: dir) }

        let script = """
        #!/bin/sh
        d=$(dirname "$0")
        printf '%s\\n' "$*" >> "$d/calls.log"
        case "$1 $2" in
        "jobs list")
            case " $* " in
            *" --fit strong_fit "*) exec cat "$d/list-strong.json" ;;
            *" --sort fresh "*) exec cat "$d/list-fresh.json" ;;
            *) exec cat "$d/list.json" ;;
            esac ;;
        "labels --json") exec cat "$d/labels.json" ;;
        "jobs stats") exec cat "$d/stats.json" ;;
        "jobs gaps") exec cat "$d/gaps.json" ;;
        "jobs show")
            for last; do :; done
            case "$last" in fx:*) exec cat "$d/show-$last.json" ;; esac ;;
        esac
        printf '{"error":"fake vani: no reply for %s"}\\n' "$1 $2"
        exit 1
        """
        let vani = dir.appendingPathComponent("vani")
        try Data(script.utf8).write(to: vani)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: vani.path)

        let byKey = Dictionary(uniqueKeysWithValues: Self.roles.map { ($0.key, $0) })
        try write("list.json", page(Self.standardOrder.compactMap { byKey[$0] }))
        try write("list-fresh.json", page(Self.freshOrder.compactMap { byKey[$0] }))
        try write("list-strong.json", page(Self.strongOrder.compactMap { byKey[$0] }))
        try write("stats.json", stats())
        if labels {
            // Go's own table, with one word changed so a test can tell it from the built-in one.
            let golden = URL(fileURLWithPath: #filePath)
                .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
                .appendingPathComponent("internal/jobview/testdata/labels.golden.json")
            let text = try String(contentsOf: golden, encoding: .utf8).replacingOccurrences(of: "\"Strong fit\"", with: "\"Best fit\"")
            try Data(text.utf8).write(to: dir.appendingPathComponent("labels.json"))
        }
        try Data(Self.gapsReply.utf8).write(to: dir.appendingPathComponent("gaps.json"))
        for r in Self.roles { try write("show-\(r.key).json", job(r)) }
    }

    /// `vani jobs gaps --json` over the four roles: DevPrism-found gaps first.
    static let gapsReply = """
    {"view":"new","roles":4,"with_missing":2,"target_known":true,"total_gaps":3,"gaps":[\
    {"term":"Kubernetes","cat":"infra","roles":2,"missing":2,"weight":90.5,"missing_weight":90.5,"companies":2,\
    "examples":[{"key":"fx:alpha:1","company":"Alpha Labs","title":"Platform Engineer"},{"key":"fx:gamma:3","company":"Gamma Works","title":"Site Reliability Engineer"}]},\
    {"term":"Terraform","cat":"infra","roles":1,"missing":0,"weight":30,"missing_weight":0,"companies":1,\
    "examples":[{"key":"fx:delta:4","company":"Delta Tools","title":"Backend Engineer"}]}],\
    "insight":{"target":true,"target_terms":5},"ledger":"/l","records":4}
    """

    /// Replaces what the fake `vani jobs gaps` answers.
    func setGapsReply(_ json: String) throws {
        try Data(json.utf8).write(to: dir.appendingPathComponent("gaps.json"))
    }

    func calls() -> [String] {
        let log = (try? String(contentsOf: dir.appendingPathComponent("calls.log"), encoding: .utf8)) ?? ""
        return log.split(separator: "\n").map(String.init)
    }

    private func write(_ name: String, _ object: [String: Any]) throws {
        try JSONSerialization.data(withJSONObject: object, options: [.sortedKeys]).write(to: dir.appendingPathComponent(name))
    }

    private func job(_ r: Role) -> [String: Any] {
        ["key": r.key, "company": r.company, "title": r.title, "status": "digested",
         "triage": ["role_fit": r.fit], "fresh_at": r.freshAt, "fresh_src": "posted",
         "age_days": r.ageDays, "relevance": r.relevance, "relevance_basis": "fit"]
    }

    private func page(_ rs: [Role]) -> [String: Any] {
        ["view": "new", "jobs": rs.map(job), "total": rs.count, "shown": rs.count,
         "counts": ["new": Self.roles.count, "all": Self.roles.count], "records": Self.roles.count]
    }

    private func stats() -> [String: Any] {
        var fit: [String: Int] = [:]
        for r in Self.roles { fit[r.fit, default: 0] += 1 }
        return [
            "view": "new", "total": Self.roles.count, "fit": fit,
            "work_mode": [String: Int](), "sponsorship": [String: Int](), "seniority": [String: Int](),
            "basis": ["fit": Self.roles.count], "fresh_source": ["posted": Self.roles.count],
            "age": [["label": "this week", "max_days": 7, "posted": 2, "seen": 0],
                    ["label": "this month", "max_days": 30, "posted": 1, "seen": 0],
                    ["label": "older", "max_days": -1, "posted": 1, "seen": 0]],
            "relevance": [0, 1, 1, 1, 1], "priority": [Int](),
            "companies": Self.roles.map { ["company": $0.company, "n": 1, "strong": $0.fit == "strong_fit" ? 1 : 0, "mean_relevance": $0.relevance] },
            "keywords": [[String: Any]](),
        ]
    }
}
