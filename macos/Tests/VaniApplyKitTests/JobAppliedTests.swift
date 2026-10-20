import Foundation
import XCTest
@testable import VaniApplyKit

/// A stand-in for `vani jobs`: writes the arguments it was given, one per line,
/// to `argvFile`, then answers `find` and `mark` from fixed replies.
private func fakeJobsVani(_ t: XCTestCase, find: String, mark: String = "", status: Int32 = 0, sleep: Int = 0) throws -> (vani: URL, argv: URL) {
    let dir = try tempDir(t)
    let argv = dir.appendingPathComponent("argv.txt")
    let vani = dir.appendingPathComponent("vani")
    let script = """
    #!/bin/sh
    printf '%s\\n' "$@" > '\(argv.path)'
    [ \(sleep) -gt 0 ] && sleep \(sleep)
    case "$2" in
    find) cat <<'EOF'
    \(find)
    EOF
    ;;
    mark) cat <<'EOF'
    \(mark)
    EOF
    ;;
    esac
    exit \(status)
    """
    try Data(script.utf8).write(to: vani)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: vani.path)
    return (vani, argv)
}

private let foundReply = """
{"url":"https://boards.greenhouse.io/acme/jobs/1","matches":1,"job":{"key":"gh:acme:1","company":"Acme","title":"Inference Engineer",\
"status":"digested","matched_on":"url"},"ledger":"/state/ledger","records":12}
"""

private let markedReply = """
{"key":"gh:acme:1","company":"Acme","title":"Inference Engineer","status":"digested","mark":"applied","marked_at":"2026-10-19T09:00:00Z","first_seen":"2026-10-18T21:15:20Z"}
"""

final class JobAppliedTests: XCTestCase {
    private func call(_ req: [String: Any], store: BankStore, jobs: JobsBackend?, source: Source = .chrome, agent: AgentClient? = nil) throws -> [String: Any] {
        let data = try JSONSerialization.data(withJSONObject: req)
        let reply = NativeProtocol.handle(try JSONSerialization.jsonObject(with: data), store: store, source: source, agent: agent, jobs: jobs)
        return try JSONSerialization.jsonObject(with: JSONSerialization.data(withJSONObject: reply)) as! [String: Any]
    }

    private func argv(_ url: URL) -> [String]? {
        guard let s = try? String(contentsOf: url, encoding: .utf8) else { return nil }
        return s.split(separator: "\n", omittingEmptySubsequences: false).dropLast().map(String.init)
    }

    func testFindAnswersWithTheJobAndNothingElse() throws {
        let store = BankStore(directory: try tempDir(self))
        let fake = try fakeJobsVani(self, find: foundReply)
        let jobs = JobsClient(vaniBinary: fake.vani, stateDir: "/state")
        let page = "https://boards.greenhouse.io/acme/jobs/1"
        let reply = try call(["op": "jobFind", "url": page], store: store, jobs: jobs)
        XCTAssertEqual(reply["found"] as? Bool, true, "\(reply)")
        let job = try XCTUnwrap(reply["job"] as? [String: Any])
        XCTAssertEqual(job["key"] as? String, "gh:acme:1")
        XCTAssertEqual(job["company"] as? String, "Acme")
        XCTAssertEqual(job["matchedOn"] as? String, "url")
        XCTAssertNil(job["mark"], "an unmarked job carries no mark")
        XCTAssertNil(reply["ledger"], "the state folder stays on this side")
        XCTAssertEqual(argv(fake.argv), ["jobs", "find", "--json", "--url", page], "find only finds: it never marks")
    }

    func testFindReportsTheMarkSoTheExtensionCanSkipAppliedJobs() throws {
        let store = BankStore(directory: try tempDir(self))
        let reply = """
        {"url":"https://x.example/a","matches":1,"job":{"key":"lv:x:2","company":"X","title":"T","status":"scored","closed":true,"mark":"applied","matched_on":"apply_url"}}
        """
        let jobs = JobsClient(vaniBinary: try fakeJobsVani(self, find: reply).vani)
        let out = try call(["op": "jobFind", "url": "https://x.example/a/apply"], store: store, jobs: jobs)
        let job = try XCTUnwrap(out["job"] as? [String: Any])
        XCTAssertEqual(job["mark"] as? String, "applied")
        XCTAssertEqual(job["closed"] as? Bool, true)
        XCTAssertEqual(job["matchedOn"] as? String, "apply_url")
    }

    func testNoMatchAndAmbiguousOfferNoJob() throws {
        let store = BankStore(directory: try tempDir(self))
        let none = JobsClient(vaniBinary: try fakeJobsVani(self, find: #"{"url":"https://x.example/a","matches":0,"ledger":"/s","records":3}"#).vani)
        let a = try call(["op": "jobFind", "url": "https://x.example/a"], store: store, jobs: none)
        XCTAssertEqual(a["found"] as? Bool, false)
        XCTAssertNil(a["job"])
        XCTAssertEqual(a["matches"] as? Int, 0)

        let two = JobsClient(vaniBinary: try fakeJobsVani(self, find: #"{"url":"https://x.example/a","matches":2,"ambiguous":true}"#).vani)
        let b = try call(["op": "jobFind", "url": "https://x.example/a"], store: store, jobs: two)
        XCTAssertEqual(b["found"] as? Bool, false)
        XCTAssertEqual(b["ambiguous"] as? Bool, true)
        XCTAssertNil(b["job"])
    }

    // The page URL is one argument to vani: no shell reads it, so $(…), `…`, ; and & stay text.
    func testAHostileURLReachesVaniAsOneLiteralArgument() throws {
        let store = BankStore(directory: try tempDir(self))
        let marker = FileManager.default.temporaryDirectory.appendingPathComponent("vani-injected-\(UUID().uuidString)")
        let fake = try fakeJobsVani(self, find: #"{"url":"https://x.example/a","matches":0}"#)
        let jobs = JobsClient(vaniBinary: fake.vani)
        let hostile = "https://x.example/$(touch${IFS}\(marker.path));`touch${IFS}\(marker.path)`&&x|y;--url=-json"
        let out = try call(["op": "jobFind", "url": hostile], store: store, jobs: jobs)
        XCTAssertEqual(out["found"] as? Bool, false, "\(out)")
        XCTAssertEqual(argv(fake.argv), ["jobs", "find", "--json", "--url", hostile])
        XCTAssertFalse(FileManager.default.fileExists(atPath: marker.path), "the URL was run as a command")
    }

    func testBadURLsAreRefusedBeforeVaniStarts() throws {
        let store = BankStore(directory: try tempDir(self))
        let fake = try fakeJobsVani(self, find: foundReply)
        let jobs = JobsClient(vaniBinary: fake.vani)
        let long = "https://x.example/" + String(repeating: "a", count: JobLinks.maxURLBytes)
        let bad: [Any] = [
            "", "   ", "javascript:alert(1)", "data:text/html,x", "ftp://x.example/a", "file:///etc/passwd", "//x.example/a", "/a",
            "https://", "https://x.example/a b", "https://x.example/a\nb", "https://x.example/a\u{1b}[2J", "https://x.example/<b>",
            "https://x.example/a\\b", long, 42, true, ["https://x.example/a"], NSNull(),
        ]
        for url in bad {
            let reply = try call(["op": "jobFind", "url": url], store: store, jobs: jobs)
            XCTAssertEqual(reply["code"] as? String, "bad_request", "\(url) -> \(reply)")
        }
        XCTAssertEqual(try call(["op": "jobFind"], store: store, jobs: jobs)["code"] as? String, "bad_request")
        XCTAssertNil(argv(fake.argv), "vani was never started")
        // The longest accepted URL is accepted.
        let edge = "https://x.example/" + String(repeating: "a", count: JobLinks.maxURLBytes - 18)
        XCTAssertNotNil(JobLinks.checkedURL(edge))
        XCTAssertNil(JobLinks.checkedURL(edge + "a"))
    }

    func testMarkSetsAppliedAndNothingElse() throws {
        let store = BankStore(directory: try tempDir(self))
        let fake = try fakeJobsVani(self, find: foundReply, mark: markedReply)
        let jobs = JobsClient(vaniBinary: fake.vani, stateDir: "/state")
        // Any other mark, or none, in the request changes nothing: a browser can only say "applied".
        for extra: [String: Any] in [[:], ["mark": "hidden"], ["mark": "clear"], ["status": "saved"]] {
            var req: [String: Any] = ["op": "jobMark", "key": "gh:acme:1"]
            req.merge(extra) { $1 }
            let reply = try call(req, store: store, jobs: jobs)
            XCTAssertEqual(reply["ok"] as? Bool, true, "\(reply)")
            XCTAssertEqual(reply["mark"] as? String, "applied")
            XCTAssertEqual(reply["key"] as? String, "gh:acme:1")
            XCTAssertEqual(argv(fake.argv), ["jobs", "mark", "--json", "gh:acme:1", "applied"])
        }
    }

    func testBadKeysAreRefusedBeforeVaniStarts() throws {
        let store = BankStore(directory: try tempDir(self))
        let fake = try fakeJobsVani(self, find: foundReply, mark: markedReply)
        let jobs = JobsClient(vaniBinary: fake.vani)
        let bad: [Any] = [
            "", "-json", "--url", "nocolon", "gh:acme: 1", "gh:acme:1\n", "gh:acme:1\u{1b}[2J",
            "gh:" + String(repeating: "a", count: JobLinks.maxKeyBytes), 7, NSNull(), ["gh:a:1"],
        ]
        for key in bad {
            let reply = try call(["op": "jobMark", "key": key], store: store, jobs: jobs)
            XCTAssertEqual(reply["code"] as? String, "bad_request", "\(key) -> \(reply)")
        }
        XCTAssertEqual(try call(["op": "jobMark"], store: store, jobs: jobs)["code"] as? String, "bad_request")
        XCTAssertNil(argv(fake.argv), "vani was never started")
    }

    func testAMarkVaniDidNotRecordIsAFailure() throws {
        let store = BankStore(directory: try tempDir(self))
        let unmarked = #"{"key":"gh:acme:1","company":"Acme","title":"T","status":"digested"}"#
        let jobs = JobsClient(vaniBinary: try fakeJobsVani(self, find: foundReply, mark: unmarked).vani)
        let reply = try call(["op": "jobMark", "key": "gh:acme:1"], store: store, jobs: jobs)
        XCTAssertEqual(reply["code"] as? String, "jobs")
        XCTAssertNil(reply["ok"])
    }

    func testVaniFailuresAreCodedAndTimeoutsAreBounded() throws {
        let store = BankStore(directory: try tempDir(self))
        let refused = JobsClient(vaniBinary: try fakeJobsVani(self, find: #"{"error":"jobs: read the ledger: no such file"}"#, mark: #"{"error":"jobs: no posting \"gh:a:typo\" in the ledger"}"#, status: 1).vani)
        let a = try call(["op": "jobFind", "url": "https://x.example/a"], store: store, jobs: refused)
        XCTAssertEqual(a["code"] as? String, "jobs")
        XCTAssertTrue((a["error"] as? String ?? "").contains("read the ledger"), "\(a)")
        let b = try call(["op": "jobMark", "key": "gh:a:typo"], store: store, jobs: refused)
        XCTAssertEqual(b["code"] as? String, "jobs")
        XCTAssertTrue((b["error"] as? String ?? "").contains("no posting"), "\(b)")

        let missing = JobsClient(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani"))
        XCTAssertEqual(try call(["op": "jobFind", "url": "https://x.example/a"], store: store, jobs: missing)["code"] as? String, "jobs")

        let slow = JobsClient(vaniBinary: try fakeJobsVani(self, find: foundReply, sleep: 5).vani, timeout: 1)
        let started = Date()
        XCTAssertEqual(try call(["op": "jobFind", "url": "https://x.example/a"], store: store, jobs: slow)["code"] as? String, "jobs")
        XCTAssertLessThan(Date().timeIntervalSince(started), 4, "a hung vani is cut off at the client's timeout")
    }

    func testWithoutAJobsBackendBothOpsAreUnsupported() throws {
        let store = BankStore(directory: try tempDir(self))
        for source in [Source.safari, .chrome] {
            for req: [String: Any] in [["op": "jobFind", "url": "https://x.example/a"], ["op": "jobMark", "key": "gh:a:1"]] {
                XCTAssertEqual(try call(req, store: store, jobs: nil, source: source)["code"] as? String, "unsupported", "\(req)")
            }
        }
    }

    // MARK: Safari: the same ops through the agent

    /// An agent client whose Jobs calls run the agent's own code over a fake vani, without XPC.
    struct AgentWithJobs: AgentClient, JobsBackend {
        let ops: AgentJobsOps
        func start(kind: String, request: Data) -> [String: Any] { ["error": "unused", "code": "bad_request"] }
        func result(job: String) -> [String: Any] { ["error": "unused", "code": "agent"] }
        func find(url: String) throws -> JobMatch {
            let r = ops.find(url: url)
            guard let data = r.match else { throw BankError(r.error ?? "none") }
            return try JSONDecoder().decode(JobMatch.self, from: data)
        }
        func markApplied(key: String) throws {
            if let e = ops.markApplied(key: key) { throw BankError(e) }
        }
        func ingest(payload: Data) throws -> Data {
            let r = ops.ingest(payload: payload)
            guard let data = r.reply else { throw BankError(r.error ?? "none") }
            return data
        }
    }

    func testSafariReachesTheJobsOpsThroughTheAgent() throws {
        let store = BankStore(directory: try tempDir(self))
        let fake = try fakeJobsVani(self, find: foundReply, mark: markedReply)
        let agent = AgentWithJobs(ops: AgentJobsOps(jobs: JobsClient(vaniBinary: fake.vani)))
        let found = try call(["op": "jobFind", "url": "https://boards.greenhouse.io/acme/jobs/1"], store: store, jobs: nil, source: .safari, agent: agent)
        XCTAssertEqual((found["job"] as? [String: Any])?["key"] as? String, "gh:acme:1", "\(found)")
        let marked = try call(["op": "jobMark", "key": "gh:acme:1"], store: store, jobs: nil, source: .safari, agent: agent)
        XCTAssertEqual(marked["ok"] as? Bool, true, "\(marked)")
        XCTAssertEqual(argv(fake.argv), ["jobs", "mark", "--json", "gh:acme:1", "applied"])
    }

    func testAnAgentWithoutJobsOpsStaysUnsupportedAndAJobsBackendWins() throws {
        let store = BankStore(directory: try tempDir(self))
        // The job-table agent of AgentTests is not a JobsBackend.
        let plain = LocalAgent(jobs: AgentJobs { _, _ in Data("{}".utf8) })
        XCTAssertEqual(try call(["op": "jobFind", "url": "https://x.example/a"], store: store, jobs: nil, source: .safari, agent: plain)["code"] as? String, "unsupported")
        // With both, the host's own client is used and the agent is not asked.
        let direct = try fakeJobsVani(self, find: foundReply)
        let viaAgent = try fakeJobsVani(self, find: foundReply)
        let reply = try call(["op": "jobFind", "url": "https://x.example/a"], store: store,
                             jobs: JobsClient(vaniBinary: direct.vani),
                             agent: AgentWithJobs(ops: AgentJobsOps(jobs: JobsClient(vaniBinary: viaAgent.vani))))
        XCTAssertEqual(reply["found"] as? Bool, true)
        XCTAssertNotNil(argv(direct.argv))
        XCTAssertNil(argv(viaAgent.argv))
    }

    func testTheAgentChecksWhatItIsAskedEvenWhenTheCallerDid() throws {
        let fake = try fakeJobsVani(self, find: foundReply, mark: markedReply)
        let ops = AgentJobsOps(jobs: JobsClient(vaniBinary: fake.vani))
        for url in ["", "javascript:alert(1)", "https://x.example/a b", "https://x.example/" + String(repeating: "a", count: 3000)] {
            XCTAssertNotNil(ops.find(url: url).error, url)
        }
        for key in ["", "-json", "nocolon", "gh:a: 1"] { XCTAssertNotNil(ops.markApplied(key: key), key) }
        XCTAssertNil(argv(fake.argv), "vani was never started")
        XCTAssertNotNil(ops.find(url: "https://x.example/a").match)
        XCTAssertNil(ops.markApplied(key: "gh:acme:1"))
        let refused = AgentJobsOps(jobs: JobsClient(vaniBinary: try fakeJobsVani(self, find: #"{"error":"jobs: no ledger"}"#, mark: #"{"error":"jobs: no posting"}"#, status: 1).vani))
        XCTAssertTrue(refused.find(url: "https://x.example/a").error?.contains("no ledger") == true)
        XCTAssertTrue(refused.markApplied(key: "gh:a:1")?.contains("no posting") == true)
    }

    func testUnreachableAgentFailsTheJobsOpsQuickly() throws {
        let client = XPCAgentClient(serviceName: "\(AppGroup.identifier).no-such-service-\(UUID().uuidString)")
        let started = Date()
        XCTAssertThrowsError(try client.find(url: "https://x.example/a"))
        XCTAssertThrowsError(try client.markApplied(key: "gh:a:1"))
        XCTAssertLessThan(Date().timeIntervalSince(started), 2 * XPCAgentClient.callTimeout + 1)
    }

    /// The XPC interface itself: both calls cross a real in-process connection with their replies intact.
    final class Service: NSObject, AgentXPC, NSXPCListenerDelegate {
        let ops: AgentJobsOps
        init(ops: AgentJobsOps) { self.ops = ops }
        func listener(_ listener: NSXPCListener, shouldAcceptNewConnection c: NSXPCConnection) -> Bool {
            c.exportedInterface = NSXPCInterface(with: AgentXPC.self)
            c.exportedObject = self
            c.resume()
            return true
        }
        func start(kind: String, request: Data, reply: @escaping (String?, String?) -> Void) { reply(nil, "unused") }
        func result(job: String, reply: @escaping (Data?, Bool, String?) -> Void) { reply(nil, false, "unused") }
        func find(url: String, reply: @escaping (Data?, String?) -> Void) { let r = ops.find(url: url); reply(r.match, r.error) }
        func markApplied(key: String, reply: @escaping (String?) -> Void) { reply(ops.markApplied(key: key)) }
        func ingest(payload: Data, reply: @escaping (Data?, String?) -> Void) { let r = ops.ingest(payload: payload); reply(r.reply, r.error) }
        func identity(reply: @escaping (String) -> Void) { reply("/Applications/Vani Apply.app") }
    }

    func testTheJobsCallsCrossAnXPCConnection() throws {
        let fake = try fakeJobsVani(self, find: foundReply, mark: markedReply)
        let service = Service(ops: AgentJobsOps(jobs: JobsClient(vaniBinary: fake.vani)))
        let listener = NSXPCListener.anonymous()
        listener.delegate = service
        listener.resume()
        let conn = NSXPCConnection(listenerEndpoint: listener.endpoint)
        conn.remoteObjectInterface = NSXPCInterface(with: AgentXPC.self)
        conn.resume()
        defer { conn.invalidate(); listener.invalidate() }
        let proxy = try XCTUnwrap(conn.synchronousRemoteObjectProxyWithErrorHandler { XCTFail("\($0)") } as? AgentXPC)

        var match: Data?, findError: String?, markError: String? = "unset"
        proxy.find(url: "https://boards.greenhouse.io/acme/jobs/1") { match = $0; findError = $1 }
        XCTAssertNil(findError)
        let decoded = try JSONDecoder().decode(JobMatch.self, from: XCTUnwrap(match))
        XCTAssertEqual(decoded.job?.key, "gh:acme:1")
        proxy.markApplied(key: "gh:acme:1") { markError = $0 }
        XCTAssertNil(markError)
        proxy.find(url: "javascript:alert(1)") { match = $0; findError = $1 }
        XCTAssertNil(match)
        XCTAssertNotNil(findError)
    }

    func testNothingAboutThePageReachesTheActivityLog() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        let jobs = JobsClient(vaniBinary: try fakeJobsVani(self, find: foundReply, mark: markedReply).vani)
        _ = try call(["op": "jobFind", "url": "https://boards.greenhouse.io/acme/jobs/1"], store: store, jobs: jobs)
        _ = try call(["op": "jobMark", "key": "gh:acme:1"], store: store, jobs: jobs)
        let log = dir.appendingPathComponent("activity.jsonl")
        let raw = (try? String(contentsOf: log, encoding: .utf8)) ?? ""
        XCTAssertFalse(raw.contains("greenhouse") || raw.contains("gh:acme"), "the log holds only events and counts")
    }
}
