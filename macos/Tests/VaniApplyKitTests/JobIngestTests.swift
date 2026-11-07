import Foundation
import XCTest
@testable import VaniApplyKit

/// A stand-in for `vani`: writes its arguments (one per line) to `argv` and its stdin to `stdin`, then
/// prints `out` and exits `status`, after sleeping `sleep` seconds. `bytes` > 0 prints that many bytes instead.
private func fakeVani(_ t: XCTestCase, out: String, status: Int32 = 0, sleep: Int = 0, bytes: Int = 0) throws -> (vani: URL, argv: URL, stdin: URL) {
    let dir = try tempDir(t)
    let argv = dir.appendingPathComponent("argv.txt")
    let stdin = dir.appendingPathComponent("stdin.json")
    let vani = dir.appendingPathComponent("vani")
    let emit = bytes > 0 ? "head -c \(bytes) /dev/zero | tr '\\0' 'a'" : "cat <<'EOF'\n\(out)\nEOF"
    let script = """
    #!/bin/sh
    printf '%s\\n' "$@" > '\(argv.path)'
    cat > '\(stdin.path)'
    [ \(sleep) -gt 0 ] && sleep \(sleep)
    \(emit)
    exit \(status)
    """
    try Data(script.utf8).write(to: vani)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: vani.path)
    return (vani, argv, stdin)
}

private let ingestReply = #"{"queued":2,"duplicates":1,"rejected":[{"index":3,"reason":"url is not http(s)"}],"search":{"status":"added","ats":"pg","token":"careers-acme","company":"Acme"},"analysis":"daemon"}"#

/// A JobsBackend that records what it was asked and answers from a closure.
private final class RecordingBackend: JobsBackend, @unchecked Sendable {
    private let lock = NSLock()
    private var _payloads: [Data] = []
    let answer: @Sendable (Data) throws -> Data
    init(answer: @escaping @Sendable (Data) throws -> Data = { _ in Data(ingestReply.utf8) }) { self.answer = answer }

    var payloads: [Data] { lock.lock(); defer { lock.unlock() }; return _payloads }
    func find(url: String) throws -> JobMatch { throw BankError("unused") }
    func markApplied(key: String) throws { throw BankError("unused") }
    func ingest(payload: Data) throws -> Data {
        lock.lock(); _payloads.append(payload); lock.unlock()
        return try answer(payload)
    }
}

final class JobIngestTests: XCTestCase {
    private func request(_ mutate: (inout [String: Any]) -> Void = { _ in }) -> [String: Any] {
        var req: [String: Any] = [
            "op": "jobsIngest",
            "page": ["url": "https://careers.acme.com/search?q=engineer", "title": "Jobs at Acme"],
            "save_search": true,
            "jobs": [
                ["title": "Inference Engineer", "company": "Acme", "location": "Remote", "url": "https://careers.acme.com/jobs/1",
                 "snippet": "Build things.", "posted": "2026-10-18T09:00:00Z"],
                ["title": "Staff Engineer", "url": "https://careers.acme.com/jobs/2"],
            ] as [[String: Any]],
        ]
        mutate(&req)
        return req
    }

    private func call(_ req: [String: Any], jobs: JobsBackend?, source: Source = .chrome, agent: AgentClient? = nil, store: BankStore? = nil) throws -> [String: Any] {
        let store = try store ?? BankStore(directory: tempDir(self))
        let data = try JSONSerialization.data(withJSONObject: req)
        let reply = NativeProtocol.handle(try JSONSerialization.jsonObject(with: data), store: store, source: source, agent: agent, jobs: jobs)
        return try JSONSerialization.jsonObject(with: JSONSerialization.data(withJSONObject: reply)) as! [String: Any]
    }

    private func object(_ d: Data) throws -> [String: Any] { try XCTUnwrap(try JSONSerialization.jsonObject(with: d) as? [String: Any]) }

    // MARK: the op

    func testAValidRequestPassesItsPayloadThroughAndTheReplyComesBackUnchanged() throws {
        let backend = RecordingBackend()
        let reply = try call(request(), jobs: backend)
        XCTAssertEqual(reply["queued"] as? Int, 2, "\(reply)")
        XCTAssertEqual(reply["analysis"] as? String, "daemon")
        XCTAssertEqual((reply["search"] as? [String: Any])?["status"] as? String, "added")
        XCTAssertEqual((reply["rejected"] as? [[String: Any]])?.first?["reason"] as? String, "url is not http(s)")
        XCTAssertEqual(backend.payloads.count, 1)
        let sent = try object(try XCTUnwrap(backend.payloads.first))
        XCTAssertNil(sent["op"], "the native request minus op")
        XCTAssertEqual(sent["save_search"] as? Bool, true)
        XCTAssertEqual((sent["page"] as? [String: Any])?["url"] as? String, "https://careers.acme.com/search?q=engineer")
        let jobs = try XCTUnwrap(sent["jobs"] as? [[String: Any]])
        XCTAssertEqual(jobs.count, 2)
        XCTAssertEqual(jobs[0]["title"] as? String, "Inference Engineer")
        XCTAssertEqual(jobs[0]["posted"] as? String, "2026-10-18T09:00:00Z")
        XCTAssertEqual(jobs[1]["url"] as? String, "https://careers.acme.com/jobs/2")
    }

    func testOnlyTheContractsFieldsReachVani() throws {
        let backend = RecordingBackend()
        _ = try call(request { r in
            r["extra"] = "x"
            r["page"] = ["url": "https://careers.acme.com/s", "cookie": "secret"] as [String: Any]
            r["jobs"] = [["title": "T", "url": "https://x.example/1", "salary": "9", "html": "<b>"]] as [[String: Any]]
        }, jobs: backend)
        let sent = try object(try XCTUnwrap(backend.payloads.first))
        XCTAssertEqual(Set(sent.keys), ["page", "save_search", "jobs"])
        XCTAssertEqual(Set((sent["page"] as! [String: Any]).keys), ["url"])
        XCTAssertEqual(Set(((sent["jobs"] as! [[String: Any]])[0]).keys), ["title", "url"])
    }

    func testSaveSearchDefaultsToFalseAndNullOptionalsReadAsAbsent() throws {
        let backend = RecordingBackend()
        let reply = try call(request { r in
            r.removeValue(forKey: "save_search")
            r["page"] = ["url": "http://careers.acme.com/s", "title": NSNull()] as [String: Any]
            r["jobs"] = [["title": "T", "url": "https://x.example/1", "company": NSNull(), "snippet": NSNull(), "posted": NSNull()]] as [[String: Any]]
        }, jobs: backend)
        XCTAssertNil(reply["error"], "\(reply)")
        let sent = try object(try XCTUnwrap(backend.payloads.first))
        XCTAssertEqual(sent["save_search"] as? Bool, false)
        XCTAssertEqual(Set(((sent["jobs"] as! [[String: Any]])[0]).keys), ["title", "url"])
    }

    func testTheLargestAcceptedRequestIsAccepted() throws {
        let backend = RecordingBackend()
        let job: [String: Any] = ["title": String(repeating: "t", count: 300), "company": String(repeating: "c", count: 200),
                                  "location": String(repeating: "l", count: 200), "url": "https://x.example/" + String(repeating: "a", count: JobLinks.maxURLBytes - 18),
                                  "snippet": String(repeating: "s", count: 4000), "posted": String(repeating: "p", count: 64)]
        // 200 jobs of this size would be over 1 MiB, so keep the count where the whole request still fits.
        let reply = try call(request { $0["jobs"] = [[String: Any]](repeating: job, count: 100); $0["page"] = ["url": "https://x.example/s", "title": String(repeating: "t", count: 300)] as [String: Any] }, jobs: backend)
        XCTAssertNil(reply["error"], "\(reply)")
        XCTAssertEqual(backend.payloads.count, 1)
        let two: [[String: Any]] = [["title": "a", "url": "https://x.example/1"]]
        XCTAssertNil(try call(request { $0["jobs"] = [[String: Any]](repeating: two[0], count: 200) }, jobs: backend)["error"], "200 jobs is the bound")
    }

    func testEveryBoundIsRefusedWithBadRequestAndVaniNeverStarts() throws {
        let longURL = "https://x.example/" + String(repeating: "a", count: JobLinks.maxURLBytes)
        let okJob: [String: Any] = ["title": "T", "url": "https://x.example/1"]
        func jobs(_ edit: (inout [String: Any]) -> Void) -> [[String: Any]] { var j = okJob; edit(&j); return [j] }
        let cases: [(String, (inout [String: Any]) -> Void)] = [
            ("no page", { $0.removeValue(forKey: "page") }),
            ("page is a string", { $0["page"] = "https://x.example/" }),
            ("page without url", { $0["page"] = ["title": "x"] as [String: Any] }),
            ("page url not http", { $0["page"] = ["url": "javascript:alert(1)"] as [String: Any] }),
            ("page url ftp", { $0["page"] = ["url": "ftp://x.example/a"] as [String: Any] }),
            ("page url too long", { $0["page"] = ["url": longURL] as [String: Any] }),
            ("page url http with save_search", { $0["page"] = ["url": "http://x.example/s"] as [String: Any]; $0["save_search"] = true }),
            ("page title too long", { $0["page"] = ["url": "https://x.example/s", "title": String(repeating: "t", count: 301)] as [String: Any] }),
            ("page title wrong type", { $0["page"] = ["url": "https://x.example/s", "title": 5] as [String: Any] }),
            ("save_search a string", { $0["save_search"] = "true" }),
            ("save_search a number", { $0["save_search"] = 1 }),
            ("no jobs", { $0.removeValue(forKey: "jobs") }),
            ("jobs is an object", { $0["jobs"] = ["title": "T"] as [String: Any] }),
            ("jobs empty", { $0["jobs"] = [Any]() }),
            ("201 jobs", { $0["jobs"] = [[String: Any]](repeating: okJob, count: 201) }),
            ("job is a string", { $0["jobs"] = ["x"] }),
            ("job title missing", { $0["jobs"] = jobs { $0.removeValue(forKey: "title") } }),
            ("job title empty", { $0["jobs"] = jobs { $0["title"] = "" } }),
            ("job title 301 bytes", { $0["jobs"] = jobs { $0["title"] = String(repeating: "t", count: 301) } }),
            ("job title 151 two-byte chars", { $0["jobs"] = jobs { $0["title"] = String(repeating: "é", count: 151) } }),
            ("job title a number", { $0["jobs"] = jobs { $0["title"] = 7 } }),
            ("job url missing", { $0["jobs"] = jobs { $0.removeValue(forKey: "url") } }),
            ("job url not http", { $0["jobs"] = jobs { $0["url"] = "javascript:alert(1)" } }),
            ("job url file", { $0["jobs"] = jobs { $0["url"] = "file:///etc/passwd" } }),
            ("job url relative", { $0["jobs"] = jobs { $0["url"] = "/jobs/1" } }),
            ("job url with a space", { $0["jobs"] = jobs { $0["url"] = "https://x.example/a b" } }),
            ("job url too long", { $0["jobs"] = jobs { $0["url"] = longURL } }),
            ("job url an array", { $0["jobs"] = jobs { $0["url"] = ["https://x.example/1"] } }),
            ("company 201 bytes", { $0["jobs"] = jobs { $0["company"] = String(repeating: "c", count: 201) } }),
            ("location 201 bytes", { $0["jobs"] = jobs { $0["location"] = String(repeating: "l", count: 201) } }),
            ("snippet 4001 bytes", { $0["jobs"] = jobs { $0["snippet"] = String(repeating: "s", count: 4001) } }),
            ("posted 65 bytes", { $0["jobs"] = jobs { $0["posted"] = String(repeating: "p", count: 65) } }),
            ("posted a number", { $0["jobs"] = jobs { $0["posted"] = 20261018 } }),
            ("whole request over 1 MiB", {
                $0["jobs"] = [[String: Any]](repeating: ["title": String(repeating: "t", count: 300), "company": String(repeating: "c", count: 200),
                                                         "location": String(repeating: "l", count: 200), "snippet": String(repeating: "s", count: 4000),
                                                         "url": "https://x.example/" + String(repeating: "a", count: JobLinks.maxURLBytes - 18)], count: 200)
            }),
        ]
        let backend = RecordingBackend()
        for (name, edit) in cases {
            let reply = try call(request(edit), jobs: backend)
            XCTAssertEqual(reply["code"] as? String, "bad_request", "\(name) -> \(reply["error"] ?? reply)")
            XCTAssertNotNil(reply["error"], name)
        }
        XCTAssertTrue(backend.payloads.isEmpty, "a refused request never reaches vani")
    }

    func testARefusalNamesTheRuleNotTheText() throws {
        let secret = "SECRET-TITLE-" + String(repeating: "x", count: 400)
        let reply = try call(request { $0["jobs"] = [["title": secret, "url": "https://x.example/1"]] as [[String: Any]] }, jobs: RecordingBackend())
        XCTAssertFalse((reply["error"] as? String ?? "").contains("SECRET"), "\(reply)")
    }

    func testWithoutAJobsBackendTheOpIsUnsupported() throws {
        for source in [Source.safari, .chrome] {
            XCTAssertEqual(try call(request(), jobs: nil, source: source)["code"] as? String, "unsupported")
        }
        // Even an invalid request answers unsupported first: nothing here can run it.
        XCTAssertEqual(try call(["op": "jobsIngest"], jobs: nil)["code"] as? String, "unsupported")
    }

    func testABackendErrorIsCodedJobs() throws {
        let failing = RecordingBackend { _ in throw BankError("jobs ingest: the inbox is not writable") }
        let reply = try call(request(), jobs: failing)
        XCTAssertEqual(reply["code"] as? String, "jobs")
        XCTAssertTrue((reply["error"] as? String ?? "").contains("inbox is not writable"), "\(reply)")
        XCTAssertNil(reply["queued"])
        let notAnObject = RecordingBackend { _ in Data("[1,2]".utf8) }
        XCTAssertEqual(try call(request(), jobs: notAnObject)["code"] as? String, "jobs")
        let notJSON = RecordingBackend { _ in Data("queued".utf8) }
        XCTAssertEqual(try call(request(), jobs: notJSON)["code"] as? String, "jobs")
    }

    func testNothingAboutThePageOrTheJobsReachesTheActivityLog() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        _ = try call(request(), jobs: RecordingBackend(), store: store)
        _ = try call(request { $0["jobs"] = [Any]() }, jobs: RecordingBackend(), store: store)
        let raw = (try? String(contentsOf: dir.appendingPathComponent("activity.jsonl"), encoding: .utf8)) ?? ""
        XCTAssertFalse(raw.contains("acme") || raw.contains("Inference") || raw.contains("Staff"), "the log holds only events and counts")
    }

    // MARK: the CLI

    func testTheClientSendsTheRequestOnStdinAndReturnsVaniReplyUnchanged() throws {
        let fake = try fakeVani(self, out: ingestReply)
        let payload = try JobIngest.payload(from: request())
        let out = try JobsClient(vaniBinary: fake.vani, stateDir: "/state").ingest(payload: payload)
        XCTAssertEqual(String(decoding: out, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines), ingestReply)
        XCTAssertEqual(try String(contentsOf: fake.argv, encoding: .utf8), "jobs\ningest\n--json\n")
        XCTAssertEqual(try Data(contentsOf: fake.stdin), payload, "the request JSON, not argv, carries the jobs")
    }

    func testTheClientRefusesWhatIsNotAJSONObjectAndReportsFailures() throws {
        let payload = try JobIngest.payload(from: request())
        let notJSON = try fakeVani(self, out: "queued 2")
        XCTAssertThrowsError(try JobsClient(vaniBinary: notJSON.vani).ingest(payload: payload))
        let array = try fakeVani(self, out: "[1]")
        XCTAssertThrowsError(try JobsClient(vaniBinary: array.vani).ingest(payload: payload))
        let empty = try fakeVani(self, out: "")
        XCTAssertThrowsError(try JobsClient(vaniBinary: empty.vani).ingest(payload: payload))
        let refused = try fakeVani(self, out: #"{"error":"jobs ingest: no job is valid"}"#, status: 1)
        XCTAssertThrowsError(try JobsClient(vaniBinary: refused.vani).ingest(payload: payload)) {
            XCTAssertTrue(String(describing: $0).contains("no job is valid"), "\($0)")
        }
        // A success status with an error object is still a reply, so it passes through for the caller to read.
        let usage = try fakeVani(self, out: "", status: 2)
        XCTAssertThrowsError(try JobsClient(vaniBinary: usage.vani).ingest(payload: payload))
        XCTAssertThrowsError(try JobsClient(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani")).ingest(payload: payload))
    }

    func testTheClientBoundsTimeAndOutput() throws {
        let payload = try JobIngest.payload(from: request())
        let slow = try fakeVani(self, out: ingestReply, sleep: 5)
        let started = Date()
        XCTAssertThrowsError(try JobsClient(vaniBinary: slow.vani).ingest(payload: payload, timeout: 1))
        XCTAssertLessThan(Date().timeIntervalSince(started), 4, "a hung vani is cut off")
        let huge = try fakeVani(self, out: "", bytes: JobsClient.maxIngestReplyBytes + 1000)
        XCTAssertThrowsError(try JobsClient(vaniBinary: huge.vani).ingest(payload: payload)) {
            XCTAssertTrue(String(describing: $0).contains("larger than"), "\($0)")
        }
    }

    func testTheOpRunsTheCLIEndToEndAndBadRequestsNeverStartIt() throws {
        let fake = try fakeVani(self, out: ingestReply)
        let jobs = JobsClient(vaniBinary: fake.vani)
        XCTAssertEqual(try call(request { $0["jobs"] = [Any]() }, jobs: jobs)["code"] as? String, "bad_request")
        XCTAssertFalse(FileManager.default.fileExists(atPath: fake.argv.path), "vani was never started")
        let reply = try call(request(), jobs: jobs)
        XCTAssertEqual(reply["queued"] as? Int, 2, "\(reply)")
        XCTAssertEqual(reply["analysis"] as? String, "daemon")
    }

    // MARK: Safari: through the agent

    func testTheAgentChecksTheRequestAgainBeforeRunningVani() throws {
        let fake = try fakeVani(self, out: ingestReply)
        let ops = AgentJobsOps(jobs: JobsClient(vaniBinary: fake.vani))
        for bad in [Data(), Data("not json".utf8), Data("[]".utf8), Data(#"{"op":"jobsIngest"}"#.utf8),
                    Data(#"{"page":{"url":"javascript:x"},"jobs":[{"title":"T","url":"https://x.example/1"}]}"#.utf8),
                    Data(count: JobIngest.maxPayloadBytes + 1)] {
            XCTAssertNotNil(ops.ingest(payload: bad).error)
        }
        XCTAssertFalse(FileManager.default.fileExists(atPath: fake.argv.path), "vani was never started")
        let ok = ops.ingest(payload: try JobIngest.payload(from: request()))
        XCTAssertNil(ok.error)
        XCTAssertEqual(try object(try XCTUnwrap(ok.reply))["queued"] as? Int, 2)
    }

    func testSafariReachesIngestThroughTheAgent() throws {
        let fake = try fakeVani(self, out: ingestReply)
        let agent = JobAppliedTests.AgentWithJobs(ops: AgentJobsOps(jobs: JobsClient(vaniBinary: fake.vani)))
        let reply = try call(request(), jobs: nil, source: .safari, agent: agent)
        XCTAssertEqual(reply["queued"] as? Int, 2, "\(reply)")
        let failing = JobAppliedTests.AgentWithJobs(ops: AgentJobsOps(jobs: RecordingBackend { _ in throw BankError("jobs: boom") }))
        XCTAssertEqual(try call(request(), jobs: nil, source: .safari, agent: failing)["code"] as? String, "jobs")
    }

    func testIngestCrossesAnXPCConnection() throws {
        let fake = try fakeVani(self, out: ingestReply)
        let service = JobAppliedTests.Service(ops: AgentJobsOps(jobs: JobsClient(vaniBinary: fake.vani)))
        let listener = NSXPCListener.anonymous()
        listener.delegate = service
        listener.resume()
        let conn = NSXPCConnection(listenerEndpoint: listener.endpoint)
        conn.remoteObjectInterface = NSXPCInterface(with: AgentXPC.self)
        conn.resume()
        defer { conn.invalidate(); listener.invalidate() }
        let proxy = try XCTUnwrap(conn.synchronousRemoteObjectProxyWithErrorHandler { XCTFail("\($0)") } as? AgentXPC)
        var data: Data?, error: String?
        proxy.ingest(payload: try JobIngest.payload(from: request())) { data = $0; error = $1 }
        XCTAssertNil(error)
        XCTAssertEqual(try object(try XCTUnwrap(data))["queued"] as? Int, 2)
        proxy.ingest(payload: Data("{}".utf8)) { data = $0; error = $1 }
        XCTAssertNil(data)
        XCTAssertNotNil(error)
    }

    func testAnUnreachableAgentFailsIngestWithAnError() throws {
        let client = XPCAgentClient(serviceName: "\(AppGroup.identifier).no-such-service-\(UUID().uuidString)")
        let started = Date()
        XCTAssertThrowsError(try client.ingest(payload: try JobIngest.payload(from: request())))
        XCTAssertLessThan(Date().timeIntervalSince(started), XPCAgentClient.ingestCallTimeout, "an unreachable agent is not waited out")
    }

    // MARK: Saved searches

    private let watchlistJSON = """
    [{"ats":"gh","token":"acme","company":"Acme","future_field":{"a":1}},
     {"ats":"wd","token":"nvidia","company":"NVIDIA","host":"nvidia.wd5.myworkdayjobs.com","search":"inference","searches":["inference","kernels"," "]},
     {"ats":"pg","token":"careers-acme-1a2b","company":"Acme","url":"https://careers.acme.com/search?q=engineer","link_pattern":"^https://careers\\\\.acme\\\\.com/jobs/"},
     {"ats":"pg","token":"plain","company":"","url":"http://insecure.example/jobs"},
     {"ats":"zz","token":"new","company":"Newboard","region":"eu"}]
    """

    func testTheWatchlistDecodesLeniently() throws {
        let fake = try fakeVani(self, out: watchlistJSON)
        let list = try JobsClient(vaniBinary: fake.vani, stateDir: "/state").watchlist()
        XCTAssertEqual(try String(contentsOf: fake.argv, encoding: .utf8), "watchlist\nlist\n--json\n")
        XCTAssertEqual(list.count, 5)
        XCTAssertEqual(list[0].kind, "Greenhouse")
        XCTAssertEqual(list[0].terms, [])
        XCTAssertEqual(list[1].kind, "Workday")
        XCTAssertEqual(list[1].host, "nvidia.wd5.myworkdayjobs.com")
        XCTAssertEqual(list[1].terms, ["inference", "kernels"], "searches win over search's duplicate, blanks drop")
        XCTAssertEqual(list[2].kind, "Careers page")
        XCTAssertEqual(list[2].linkPattern, #"^https://careers\.acme\.com/jobs/"#)
        XCTAssertEqual(list[2].openURL?.absoluteString, "https://careers.acme.com/search?q=engineer")
        XCTAssertNil(list[3].openURL, "a page that is not https is never opened")
        XCTAssertEqual(list[3].title, "plain", "no company reads as the token")
        XCTAssertEqual(list[4].kind, "zz", "an ats code this app does not know shows as it came")
        XCTAssertEqual(list[4].region, "eu")
        XCTAssertNil(list[0].openURL, "only a page board opens")
        XCTAssertEqual(Set(list.map(\.id)).count, 5)
    }

    func testThePerTenantBoardsHaveNames() {
        for (code, name) in ["rc": "Recruitee", "pp": "Pinpoint", "bh": "BambooHR", "ps": "Personio"] {
            XCTAssertEqual(WatchlistEntry(ats: code, token: "acme", company: "Acme").kind, name)
        }
    }

    func testTalentBrewAndEnterpriseBoardsHaveNames() {
        XCTAssertEqual(WatchlistEntry(ats: "tb", token: "intuit", company: "Intuit").kind, "TalentBrew")
        for (code, name) in ["az": "Amazon", "gg": "Google", "ap": "Apple", "ef": "Eightfold", "jb": "Jibe"] {
            XCTAssertEqual(WatchlistEntry(ats: code, token: "acme", company: "Acme").kind, name)
        }
    }

    func testOnlyHTTPSPageURLsOpen() {
        func entry(_ url: String?) -> WatchlistEntry { WatchlistEntry(ats: "pg", token: "t", company: "C", url: url) }
        XCTAssertNotNil(entry("https://careers.acme.com/s").openURL)
        for bad in ["http://careers.acme.com/s", "javascript:alert(1)", "file:///etc/passwd", "https://", "//careers.acme.com", "", nil,
                    "https://x.example/" + String(repeating: "a", count: JobLinks.maxURLBytes)] {
            XCTAssertNil(entry(bad).openURL, bad ?? "nil")
        }
        XCTAssertNil(WatchlistEntry(ats: "gh", token: "t", company: "C", url: "https://x.example/").openURL)
    }

    func testAnEmptyWatchlistIsEmptyAndAFailureIsNeverAnEmptyList() throws {
        XCTAssertEqual(try JobsClient(vaniBinary: fakeVani(self, out: "[]").vani).watchlist(), [])
        XCTAssertEqual(try JobsClient(vaniBinary: fakeVani(self, out: "null").vani).watchlist(), [], "Go prints a nil slice as null")
        XCTAssertThrowsError(try JobsClient(vaniBinary: fakeVani(self, out: #"{"error":"watchlist: config: no such file"}"#, status: 1).vani).watchlist()) {
            XCTAssertTrue(String(describing: $0).contains("no such file"), "\($0)")
        }
        for out in ["not json", "{}", #"[{"token":"x"}]"#, #"[{"ats":1,"token":"x","company":"C"}]"#, ""] {
            XCTAssertThrowsError(try JobsClient(vaniBinary: fakeVani(self, out: out).vani).watchlist(), out)
        }
        XCTAssertThrowsError(try JobsClient(vaniBinary: fakeVani(self, out: "[]", status: 2).vani).watchlist(), "an old vani without the command")
        XCTAssertThrowsError(try JobsClient(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani")).watchlist())
        XCTAssertThrowsError(try JobsClient(vaniBinary: fakeVani(self, out: "[]", sleep: 5).vani, timeout: 1).watchlist(), "a hung vani is cut off")
    }
}
