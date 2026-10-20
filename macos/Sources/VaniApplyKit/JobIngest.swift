import Foundation

// The extension's `jobsIngest` op (docs/EXTENSION_INGEST.md): jobs scraped from a
// listing page, sent to `vani jobs ingest --json` with the request on stdin.
// The page URL, title and every job are untrusted text from the browser. They
// are checked against the contract's bounds here, before `vani` starts, and the
// request vani receives is rebuilt from the known fields only. Nothing about
// them is ever logged: the log gets the event and counts.

public enum JobIngest {
    /// The contract's bounds (EXTENSION_INGEST.md "Bounds"). Over a bound is refused, never cut.
    public static let maxPayloadBytes = 1 << 20
    public static let maxJobs = 200
    public static let maxTitleBytes = 300
    public static let maxCompanyBytes = 200
    public static let maxLocationBytes = 200
    public static let maxSnippetBytes = 4000
    public static let maxPostedBytes = 64
    public static let maxPageTitleBytes = 300

    /// A request that breaks the contract. The message names the rule, never the text that broke it.
    public struct Refusal: Error, CustomStringConvertible, Equatable {
        public let description: String
        init(_ description: String) { self.description = description }
    }

    /// The JSON `vani jobs ingest --json` reads on stdin: the native request minus `op`,
    /// with only the contract's fields. A JSON null reads as absent.
    public static func payload(from req: [String: Any]) throws -> Data {
        let saveSearch: Bool
        if let raw = present(req["save_search"]) {
            guard let n = raw as? NSNumber, CFGetTypeID(n) == CFBooleanGetTypeID() else { throw Refusal("save_search must be true or false") }
            saveSearch = n.boolValue
        } else {
            saveSearch = false
        }

        guard let page = req["page"] as? [String: Any] else { throw Refusal("jobsIngest needs a page object") }
        guard let pageURL = JobLinks.checkedURL(page["url"]) else {
            throw Refusal("page.url must be an http(s) URL of at most \(JobLinks.maxURLBytes) bytes")
        }
        if saveSearch, !pageURL.lowercased().hasPrefix("https://") { throw Refusal("a saved search needs an https page.url") }
        var outPage: [String: Any] = ["url": pageURL]
        if let title = try string(page, "title", max: maxPageTitleBytes, what: "page.title") { outPage["title"] = title }

        guard let jobs = req["jobs"] as? [Any] else { throw Refusal("jobsIngest needs a jobs array") }
        guard (1...maxJobs).contains(jobs.count) else { throw Refusal("jobs must hold 1..\(maxJobs) jobs") }
        var outJobs: [[String: Any]] = []
        outJobs.reserveCapacity(jobs.count)
        for (i, raw) in jobs.enumerated() {
            guard let job = raw as? [String: Any] else { throw Refusal("jobs[\(i)] is not an object") }
            outJobs.append(try checked(job, index: i))
        }

        let body: [String: Any] = ["page": outPage, "save_search": saveSearch, "jobs": outJobs]
        guard JSONSerialization.isValidJSONObject(body), let data = try? JSONSerialization.data(withJSONObject: body, options: [.sortedKeys]) else {
            throw Refusal("jobsIngest request is not JSON")
        }
        guard data.count <= maxPayloadBytes else { throw Refusal("jobsIngest request is over \(maxPayloadBytes >> 20) MiB") }
        return data
    }

    private static func checked(_ job: [String: Any], index i: Int) throws -> [String: Any] {
        var out: [String: Any] = [:]
        guard let title = try string(job, "title", max: maxTitleBytes, what: "jobs[\(i)].title"), !title.isEmpty else {
            throw Refusal("jobs[\(i)].title is required")
        }
        out["title"] = title
        guard let url = JobLinks.checkedURL(job["url"]) else {
            throw Refusal("jobs[\(i)].url must be an http(s) URL of at most \(JobLinks.maxURLBytes) bytes")
        }
        out["url"] = url
        for (key, max) in [("company", maxCompanyBytes), ("location", maxLocationBytes), ("snippet", maxSnippetBytes), ("posted", maxPostedBytes)] {
            if let s = try string(job, key, max: max, what: "jobs[\(i)].\(key)") { out[key] = s }
        }
        return out
    }

    private static func present(_ v: Any?) -> Any? {
        guard let v, !(v is NSNull) else { return nil }
        return v
    }

    /// An optional string field of at most `max` bytes: nil when absent or null, refused when it is not a string or too long.
    private static func string(_ d: [String: Any], _ key: String, max: Int, what: String) throws -> String? {
        guard let v = present(d[key]) else { return nil }
        guard let s = v as? String, s.utf8.count <= max else { throw Refusal("\(what) must be a string of at most \(max) bytes") }
        return s
    }
}

extension JobsClient {
    /// The most `vani jobs ingest` may write: counts and at most 200 short reasons.
    static let maxIngestReplyBytes = 512 * 1024

    /// Queueing a batch can drain the inbox into the ledger when no daemon holds it, which
    /// outlasts the short ceiling the Chrome host puts on `jobs find`.
    public static let ingestTimeout: TimeInterval = 30

    /// `vani jobs ingest --json` with `payload` on stdin; vani's JSON reply, unchanged.
    public func ingest(payload: Data) throws -> Data {
        try ingest(payload: payload, timeout: Self.ingestTimeout)
    }

    func ingest(payload: Data, timeout: TimeInterval) throws -> Data {
        try VaniProcess.callRaw(vaniBinary, ["jobs", "ingest", "--json"], configPath: configPath, stateDir: stateDir,
                                input: payload, timeout: timeout, maxOutputBytes: Self.maxIngestReplyBytes,
                                tooOld: "this vani is too old to ingest jobs, or no job in the batch was valid")
    }
}
