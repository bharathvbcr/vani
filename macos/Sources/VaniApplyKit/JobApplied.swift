import Foundation

// The browser extension's two Jobs ops, `jobFind` and `jobMark` (NativeProtocol).
// "Which tracked job is this page?" is a read that may run on every submit;
// "mark it Applied" is a write that only follows the user's click. They are
// separate ops so that the extension cannot mark a job as a side effect of
// finding one, and `jobMark` can set nothing but Applied.
//
// A page URL is untrusted text from the browser. It is checked here before
// any process starts, then handed to `vani jobs find --url URL` as one
// argument (no shell is involved), and vani parses it again. It is never
// fetched.

/// `vani jobs find --json` (cmd/vani/jobsfind.go).
public struct JobMatch: Codable, Equatable, Sendable {
    public struct Ref: Codable, Equatable, Sendable {
        public var key: String
        public var company: String
        public var title: String
        public var status: String
        public var closed: Bool?
        public var mark: JobMark?
        /// `url` or `apply_url`: which stored link the page matched.
        public var matchedOn: String
        /// Vani's versioned row (JobRow); nil from a vani older than the contract.
        public var row: JobRow?

        enum CodingKeys: String, CodingKey {
            case key, company, title, status, closed, mark, row
            case matchedOn = "matched_on"
        }
    }

    /// The page as compared: scheme, host and path.
    public var url: String
    public var matches: Int
    /// More than one posting claims the page, so none is offered.
    public var ambiguous: Bool?
    public var job: Ref?
    public var torn: Bool?
    public var ledger: String?
    public var records: Int?

    /// What a browser is told: the job to ask about, or that there is none. The
    /// ledger path and record count stay on this side.
    public var asJSONObject: [String: Any] {
        var out: [String: Any] = ["found": job != nil, "url": url, "matches": matches]
        if ambiguous == true { out["ambiguous"] = true }
        if let job {
            var j: [String: Any] = ["key": job.key, "company": job.company, "title": job.title, "status": job.status, "matchedOn": job.matchedOn]
            if job.closed == true { j["closed"] = true }
            if let mark = job.mark { j["mark"] = mark.rawValue }
            if let row = job.row?.jsonObject { j["row"] = row }
            out["job"] = j
        }
        return out
    }
}

/// What NativeProtocol needs from the Jobs side; JobsClient in production.
public protocol JobsBackend: Sendable {
    func find(url: String) throws -> JobMatch
    func markApplied(key: String) throws
    /// `vani jobs ingest --json`: queue scraped jobs (JobIngest). `payload` is the checked
    /// request JSON; the reply is vani's JSON, unchanged.
    func ingest(payload: Data) throws -> Data
}

/// A backend that answers every call with one error: what stands in when the settings that say
/// where `vani jobs` keeps its state could not be read.
struct RefusingJobsBackend: JobsBackend {
    let error: BankError
    func find(url: String) throws -> JobMatch { throw error }
    func markApplied(key: String) throws { throw error }
    func ingest(payload: Data) throws -> Data { throw error }
}

extension JobsClient: JobsBackend {
    public func find(url: String) throws -> JobMatch {
        try call(["jobs", "find", "--json", "--url", url])
    }

    public func markApplied(key: String) throws {
        let job = try mark(key, .applied)
        guard job.mark == .applied else { throw BankError("vani did not record the mark") }
    }
}

/// What the agent does for the two Jobs calls over XPC: the same checks as
/// NativeProtocol (the agent trusts no caller's input), then `vani jobs`. Safari's
/// sandboxed extension cannot start `vani`, so it reaches the Jobs tab's marks this way.
public struct AgentJobsOps: Sendable {
    let jobs: JobsBackend

    public init(jobs: JobsBackend) { self.jobs = jobs }

    public func find(url: String) -> (match: Data?, error: String?) {
        guard let checked = JobLinks.checkedURL(url) else { return (nil, "not an http(s) page URL") }
        do { return (try JSONEncoder().encode(try jobs.find(url: checked)), nil) } catch { return (nil, String(describing: error)) }
    }

    public func markApplied(key: String) -> String? {
        guard let checked = JobLinks.checkedKey(key) else { return "not a posting key" }
        do { try jobs.markApplied(key: checked); return nil } catch { return String(describing: error) }
    }

    /// Re-checks the request, since the agent trusts no caller, and hands vani the rebuilt one.
    public func ingest(payload: Data) -> (reply: Data?, error: String?) {
        do {
            guard payload.count <= JobIngest.maxPayloadBytes, let req = try JSONSerialization.jsonObject(with: payload) as? [String: Any] else {
                return (nil, "not a jobsIngest request")
            }
            return (try jobs.ingest(payload: try JobIngest.payload(from: req)), nil)
        } catch { return (nil, String(describing: error)) }
    }
}

/// Safari's route: the Jobs ops go to the agent, which runs `vani jobs`. Unlike drafts and
/// suggestions these are not jobs: they answer in one call and are never queued behind one.
extension XPCAgentClient: JobsBackend {
    public func find(url: String) throws -> JobMatch {
        let r = call { agent, done, box in
            agent.find(url: url) { data, error in
                if let data { box.set(["match": data]) } else { box.set(["error": error ?? "the agent sent no result"]) }
                done.signal()
            }
        }
        guard let data = r["match"] as? Data else { throw BankError(r["error"] as? String ?? "the agent sent no result") }
        do { return try JSONDecoder().decode(JobMatch.self, from: data) } catch { throw BankError("the agent's reply was not understood") }
    }

    public func markApplied(key: String) throws {
        let r = call { agent, done, box in
            agent.markApplied(key: key) { error in
                box.set(error.map { ["error": $0] } ?? ["ok": true])
                done.signal()
            }
        }
        guard r["ok"] as? Bool == true else { throw BankError(r["error"] as? String ?? "the agent did not confirm the mark") }
    }

    public func ingest(payload: Data) throws -> Data {
        // vani may drain the inbox into the ledger, so this waits longer than the other calls.
        let r = call(timeout: Self.ingestCallTimeout) { agent, done, box in
            agent.ingest(payload: payload) { data, error in
                if let data { box.set(["reply": data]) } else { box.set(["error": error ?? "the agent sent no result"]) }
                done.signal()
            }
        }
        guard let data = r["reply"] as? Data else { throw BankError(r["error"] as? String ?? "the agent sent no result") }
        return data
    }
}

public enum JobLinks {
    /// vani refuses a longer URL; so does the extension, so a page URL never gets this far.
    public static let maxURLBytes = 2048
    /// marks.MaxKeyBytes.
    public static let maxKeyBytes = 512

    /// An http(s) URL with a host and no whitespace, control or markup
    /// characters, or nil. A page URL that fails is refused before `vani` starts,
    /// which also keeps vani's exit 2 (a usage error) from reading as "this vani is old".
    public static func checkedURL(_ raw: Any?) -> String? {
        guard let s = raw as? String, !s.isEmpty, s.utf8.count <= maxURLBytes,
              let u = Job.httpURL(s), u.host?.isEmpty == false,
              s.unicodeScalars.allSatisfy({ !CharacterSet.whitespacesAndNewlines.contains($0) && !CharacterSet.controlCharacters.contains($0)
                  && $0 != "<" && $0 != ">" && $0 != "\\" }) else { return nil }
        return s
    }

    /// A posting key as vani writes them (`ats:board:id`): bounded, printable,
    /// with an ats prefix, and never starting with "-" so it cannot read as a flag.
    public static func checkedKey(_ raw: Any?) -> String? {
        guard let s = raw as? String, !s.isEmpty, s.utf8.count <= maxKeyBytes, !s.hasPrefix("-"), s.contains(":"),
              s.unicodeScalars.allSatisfy({ !CharacterSet.whitespacesAndNewlines.contains($0) && !CharacterSet.controlCharacters.contains($0) })
        else { return nil }
        return s
    }
}
