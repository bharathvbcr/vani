import Foundation

// The Vani Apply agent: a login item (SMAppService) that runs long `vani` jobs
// for both browsers. Safari's sandboxed appex cannot start `vani`, and a
// browser message must not stay open for minutes, so a browser starts a job
// (`jobStart`) and polls for it (`jobResult`) over XPC. The Mach service name
// is prefixed by the App Group id, which is what lets the sandboxed appex
// look it up. The agent accepts connections only from code signed by this
// team, so another process cannot spend the owner's model budget through it.

public enum AgentConstants {
    public static let machService = "\(AppGroup.identifier).agent"
    public static let plistName = "dev.vbcr.vani.apply.agent.plist"
    /// Every piece of Vani Apply is signed by this team.
    public static let teamID = "HAUHVD8M37"
    public static let clientRequirement = "anchor apple generic and certificate leaf[subject.OU] = \"\(teamID)\""
}

/// The XPC interface. Requests and replies are JSON bytes, so the agent stays
/// a thin job table over `vani` and every shape check has one owner.
@objc public protocol AgentXPC {
    /// Start a job. Replies with a job id, or an error message.
    func start(kind: String, request: Data, reply: @escaping (String?, String?) -> Void)
    /// Ask for a job. Replies (result JSON, pending, error): pending is true
    /// while it runs; a finished job's result is handed out once.
    func result(job: String, reply: @escaping (Data?, Bool, String?) -> Void)
    /// `vani jobs find --url`: which tracked posting a page is. Not a job: it answers
    /// at once and is never held up by a running draft. Replies (JobMatch JSON, error).
    func find(url: String, reply: @escaping (Data?, String?) -> Void)
    /// Mark a posting Applied (the only mark a browser may set). Replies an error, or nil.
    func markApplied(key: String, reply: @escaping (String?) -> Void)
    /// `vani jobs ingest --json`: queue scraped jobs. Takes the request JSON, replies (vani's JSON, error).
    func ingest(payload: Data, reply: @escaping (Data?, String?) -> Void)
    /// The app bundle this agent runs from (AgentIdentity.bundlePath), whose `vani` it starts.
    func identity(reply: @escaping (String) -> Void)
}

/// Which copy of Vani Apply the agent belongs to. The launchd job is one per user
/// (AgentConstants.plistName), so any copy of the app that registers it, such as a
/// screenshot rig's copy with a fixture `vani`, takes it over from the app the owner
/// uses, and an agent that merely answers says nothing about whose `vani` it runs.
public enum AgentIdentity {
    /// The `.app` that holds `helpers` (its Contents/Helpers folder), symlinks resolved;
    /// the folder itself when it is not inside a bundle (a `swift build` product).
    public static func bundlePath(helpers: URL) -> String {
        let dir = helpers.resolvingSymlinksInPath().standardizedFileURL
        let contents = dir.deletingLastPathComponent()
        let app = contents.deletingLastPathComponent()
        if dir.lastPathComponent == "Helpers", contents.lastPathComponent == "Contents", app.pathExtension == "app" {
            return app.path
        }
        return dir.path
    }

    /// Why an enabled registration must be replaced by this app's own, or nil to keep it.
    public enum Reregister: Equatable, Sendable {
        /// Settings > Register again.
        case repair
        /// The agent does not answer (or predates `identity`): launchd's copy of the job is stale.
        case unreachable
        /// The agent answers from another copy of the app, at this path.
        case foreign(String)
    }

    /// `agentBundle` is the agent's `identity` reply, nil when it did not answer.
    public static func reregister(repair: Bool, agentBundle: String?, ownBundle: URL) -> Reregister? {
        if repair { return .repair }
        guard let agentBundle else { return .unreachable }
        let own = ownBundle.resolvingSymlinksInPath().standardizedFileURL.path
        let theirs = URL(fileURLWithPath: agentBundle).resolvingSymlinksInPath().standardizedFileURL.path
        return own == theirs ? nil : .foreign(String(agentBundle.prefix(512)))
    }
}

/// The kinds of job the agent runs, each one `vani <kind>` with JSON on stdin.
public enum JobKind: String, CaseIterable, Sendable {
    /// `resume` builds a PDF for one job with DevPrism (`vani resume`); it calls no model.
    case draft, suggest, resume

    /// Ceiling on one job. A suggest triages (45 s) and writes up to three
    /// drafts (90 s each); a draft is DraftRunner.timeout; a resume starts
    /// DevPrism and compiles one Typst document (about a second today).
    public var timeout: TimeInterval {
        switch self {
        case .draft: return DraftRunner.timeout
        case .suggest: return 330
        case .resume: return 180
        }
    }

    /// Requests larger than this are refused before `vani` starts.
    public var maxRequestBytes: Int {
        switch self {
        case .draft: return 64 * 1024
        case .suggest: return 256 * 1024
        case .resume: return 128 * 1024
        }
    }

    /// The most `vani <kind>` may write. A resume reply carries the PDF in base64
    /// (ResumeStore.maxBytes is about 700 KB encoded), and still has to fit Chrome's
    /// 1 MiB native-messaging reply once wrapped by jobResult.
    public var maxReplyBytes: Int {
        switch self {
        case .draft, .suggest: return DraftRunner.maxOutputBytes
        case .resume: return 900 * 1024
        }
    }

    /// Whether the job spends model budget. One that does not runs `vani` directly:
    /// no anthropic-account launcher and no Keychain read for a model key.
    public var usesModel: Bool { self != .resume }
}

/// The agent's job table: at most one job runs at a time (each spends real
/// money), finished results wait `ttl` to be fetched, and the table is bounded.
public final class AgentJobs: @unchecked Sendable {
    public typealias Runner = @Sendable (JobKind, Data) -> Data

    public static let maxJobs = 16
    private let lock = NSLock()
    private var jobs: [String: Entry] = [:]
    private let runner: Runner
    private let ttl: TimeInterval
    private let now: @Sendable () -> Date
    private let queue = DispatchQueue(label: "dev.vbcr.vani.agent.jobs")

    private struct Entry {
        var result: Data?
        var finished: Date?
        var started: Date
    }

    public init(ttl: TimeInterval = 600, now: @escaping @Sendable () -> Date = Date.init, runner: @escaping Runner) {
        self.ttl = ttl
        self.now = now
        self.runner = runner
    }

    /// True while a job runs or a result waits to be fetched.
    public var hasWork: Bool {
        lock.lock(); defer { lock.unlock() }
        expire()
        return !jobs.isEmpty
    }

    public func start(kind rawKind: String, request: Data) -> (id: String?, error: String?) {
        guard let kind = JobKind(rawValue: rawKind) else { return (nil, "unknown job kind \(String(rawKind.prefix(40)))") }
        guard !request.isEmpty, request.count <= kind.maxRequestBytes else {
            return (nil, "\(kind.rawValue) request must be 1..\(kind.maxRequestBytes) bytes")
        }
        guard (try? JSONSerialization.jsonObject(with: request)) is [String: Any] else {
            return (nil, "\(kind.rawValue) request is not a JSON object")
        }
        lock.lock()
        expire()
        if jobs.values.contains(where: { $0.finished == nil }) {
            lock.unlock()
            return (nil, "busy")
        }
        if jobs.count >= Self.maxJobs {
            lock.unlock()
            return (nil, "too many unfetched results")
        }
        let id = UUID().uuidString
        jobs[id] = Entry(result: nil, finished: nil, started: now())
        lock.unlock()

        queue.async { [runner, now] in
            let out = runner(kind, request)
            self.lock.lock()
            if self.jobs[id] != nil { self.jobs[id] = Entry(result: out, finished: now(), started: self.jobs[id]!.started) }
            self.lock.unlock()
        }
        return (id, nil)
    }

    /// Whether a job has finished, without handing out its result.
    func isFinished(_ job: String) -> Bool {
        lock.lock(); defer { lock.unlock() }
        return jobs[job]?.finished != nil
    }

    public func result(job: String) -> (data: Data?, pending: Bool, error: String?) {
        lock.lock(); defer { lock.unlock() }
        expire()
        guard let e = jobs[job] else { return (nil, false, "no such job (it finished more than \(Int(ttl)) s ago, or the agent restarted)") }
        guard e.finished != nil, let data = e.result else { return (nil, true, nil) }
        jobs[job] = nil
        return (data, false, nil)
    }

    /// Drop results nobody fetched in time. Caller holds the lock.
    private func expire() {
        let t = now()
        jobs = jobs.filter { _, e in
            guard let f = e.finished else { return true }
            return t.timeIntervalSince(f) < ttl
        }
    }
}

/// What NativeProtocol needs from the agent; XPCAgentClient in production.
public protocol AgentClient: Sendable {
    func start(kind: String, request: Data) -> [String: Any]
    func result(job: String) -> [String: Any]
}

/// Talks to the agent's Mach service. Each call is bounded; an agent that is
/// not registered, not approved or not answering reads as code "agent".
public struct XPCAgentClient: AgentClient {
    public static let callTimeout: TimeInterval = 5
    /// Room for `vani jobs ingest` (JobsClient.ingestTimeout) plus the hop.
    public static let ingestCallTimeout: TimeInterval = JobsClient.ingestTimeout + 5
    let serviceName: String

    public init(serviceName: String = AgentConstants.machService) {
        self.serviceName = serviceName
    }

    static var unreachable: [String: Any] { [
        "error": "The Vani Apply agent isn't running. Open Vani Apply and allow it in System Settings > General > Login Items.",
        "code": "agent",
    ] }

    func call(timeout: TimeInterval = XPCAgentClient.callTimeout, _ body: (AgentXPC, DispatchSemaphore, Box) -> Void) -> [String: Any] {
        let conn = NSXPCConnection(machServiceName: serviceName, options: [])
        conn.remoteObjectInterface = NSXPCInterface(with: AgentXPC.self)
        conn.resume()
        defer { conn.invalidate() }
        let done = DispatchSemaphore(value: 0)
        let box = Box()
        let proxy = conn.remoteObjectProxyWithErrorHandler { _ in
            box.set(Self.unreachable)
            done.signal()
        }
        guard let agent = proxy as? AgentXPC else { return Self.unreachable }
        body(agent, done, box)
        if done.wait(timeout: .now() + timeout) == .timedOut { return Self.unreachable }
        return box.get() ?? Self.unreachable
    }

    /// True when the agent answers at all (any reply, even "no such job"):
    /// launchd could start it and it accepted this process's signature.
    public func reachable() -> Bool {
        let r = call { agent, done, box in
            agent.result(job: "reachability-probe") { _, _, _ in
                box.set(["alive": true])
                done.signal()
            }
        }
        return r["alive"] as? Bool == true
    }

    /// The app bundle the agent runs from, or nil when it does not answer. An agent
    /// built before `identity` existed fails the call, and reads as not answering.
    public func identity() -> String? {
        let r = call { agent, done, box in
            agent.identity { path in
                box.set(["bundle": path])
                done.signal()
            }
        }
        guard let path = r["bundle"] as? String, path.hasPrefix("/"), path.utf8.count <= 4096 else { return nil }
        return path
    }

    public func start(kind: String, request: Data) -> [String: Any] {
        call { agent, done, box in
            agent.start(kind: kind, request: request) { id, error in
                if let id { box.set(["job": id]) } else { box.set(["error": error ?? "the agent refused the job", "code": error == "busy" ? "busy" : "bad_request"]) }
                done.signal()
            }
        }
    }

    public func result(job: String) -> [String: Any] {
        call { agent, done, box in
            agent.result(job: job) { data, pending, error in
                if pending {
                    box.set(["pending": true])
                } else if let data, let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] {
                    box.set(["done": true, "reply": obj])
                } else {
                    box.set(["error": error ?? "the agent sent no result", "code": "agent"])
                }
                done.signal()
            }
        }
    }
}

/// One reply crossing threads; the first value set wins.
final class Box: @unchecked Sendable {
    private let lock = NSLock()
    private var value: [String: Any]?
    func set(_ v: [String: Any]) { lock.lock(); if value == nil { value = v }; lock.unlock() }
    func get() -> [String: Any]? { lock.lock(); defer { lock.unlock() }; return value }
}
