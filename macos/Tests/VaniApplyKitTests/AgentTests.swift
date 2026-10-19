import Foundation
import XCTest
@testable import VaniApplyKit

/// An AgentClient backed by an in-process job table, so the protocol is tested
/// without launchd or XPC.
struct LocalAgent: AgentClient {
    let jobs: AgentJobs
    func start(kind: String, request: Data) -> [String: Any] {
        let r = jobs.start(kind: kind, request: request)
        if let id = r.id { return ["job": id] }
        return ["error": r.error ?? "", "code": r.error == "busy" ? "busy" : "bad_request"]
    }
    func result(job: String) -> [String: Any] {
        let r = jobs.result(job: job)
        if r.pending { return ["pending": true] }
        guard let data = r.data, let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return ["error": r.error ?? "none", "code": "agent"]
        }
        return ["done": true, "reply": obj]
    }
}

final class Clock: @unchecked Sendable {
    private let lock = NSLock()
    private var t = Date(timeIntervalSince1970: 1_000_000)
    func now() -> Date { lock.lock(); defer { lock.unlock() }; return t }
    func advance(_ s: TimeInterval) { lock.lock(); t += s; lock.unlock() }
}

func waitDone(_ jobs: AgentJobs, _ id: String, file: StaticString = #filePath, line: UInt = #line) -> (Data?, String?) {
    for _ in 0..<200 {
        let r = jobs.result(job: id)
        if !r.pending { return (r.data, r.error) }
        Thread.sleep(forTimeInterval: 0.01)
    }
    XCTFail("job never finished", file: file, line: line)
    return (nil, nil)
}

final class AgentTests: XCTestCase {
    func testJobRunsOnceAndItsResultIsHandedOutOnce() throws {
        let jobs = AgentJobs { kind, req in Data(#"{"kind":"\#(kind.rawValue)","n":\#(req.count)}"#.utf8) }
        let s = jobs.start(kind: "suggest", request: Data(#"{"fields":[]}"#.utf8))
        let id = try XCTUnwrap(s.id, s.error ?? "")
        let (data, _) = waitDone(jobs, id)
        let obj = try XCTUnwrap(try JSONSerialization.jsonObject(with: XCTUnwrap(data)) as? [String: Any])
        XCTAssertEqual(obj["kind"] as? String, "suggest")
        XCTAssertNotNil(jobs.result(job: id).error, "a fetched result is gone")
        XCTAssertFalse(jobs.hasWork)
    }

    func testOneJobAtATime() throws {
        let gate = DispatchSemaphore(value: 0)
        let jobs = AgentJobs { _, _ in gate.wait(); return Data("{}".utf8) }
        let first = try XCTUnwrap(jobs.start(kind: "draft", request: Data("{}".utf8)).id)
        XCTAssertEqual(jobs.start(kind: "draft", request: Data("{}".utf8)).error, "busy")
        XCTAssertTrue(jobs.result(job: first).pending)
        XCTAssertTrue(jobs.hasWork)
        gate.signal()
        _ = waitDone(jobs, first)
        XCTAssertNotNil(jobs.start(kind: "draft", request: Data("{}".utf8)).id, "free again once finished")
        gate.signal()
    }

    func testRefusesBadJobsBeforeRunning() {
        let ran = NSLock()
        nonisolated(unsafe) var count = 0
        let jobs = AgentJobs { _, _ in ran.lock(); count += 1; ran.unlock(); return Data("{}".utf8) }
        XCTAssertNotNil(jobs.start(kind: "rm", request: Data("{}".utf8)).error)
        XCTAssertNotNil(jobs.start(kind: "draft", request: Data()).error)
        XCTAssertNotNil(jobs.start(kind: "draft", request: Data("[1]".utf8)).error, "not an object")
        XCTAssertNotNil(jobs.start(kind: "draft", request: Data("{nope".utf8)).error)
        XCTAssertNotNil(jobs.start(kind: "draft", request: Data(repeating: 0x20, count: JobKind.draft.maxRequestBytes + 1)).error)
        XCTAssertNotNil(jobs.result(job: "no-such-job").error)
        Thread.sleep(forTimeInterval: 0.05)
        ran.lock(); XCTAssertEqual(count, 0); ran.unlock()
    }

    func testUnfetchedResultsExpire() throws {
        let clock = Clock()
        let ran = DispatchSemaphore(value: 0)
        let jobs = AgentJobs(ttl: 60, now: clock.now) { _, _ in ran.signal(); return Data("{}".utf8) }
        let id = try XCTUnwrap(jobs.start(kind: "draft", request: Data("{}".utf8)).id)
        XCTAssertEqual(ran.wait(timeout: .now() + 2), .success)
        // The runner returned; give the table a moment to record the result.
        for _ in 0..<200 where !jobs.isFinished(id) { Thread.sleep(forTimeInterval: 0.01) }
        clock.advance(59)
        XCTAssertTrue(jobs.hasWork, "kept within the ttl")
        clock.advance(2)
        XCTAssertNotNil(jobs.result(job: id).error, "expired")
        XCTAssertFalse(jobs.hasWork)
    }

    func testRunJobRunsTheNamedSubcommand() throws {
        let vani = try fakeVani(self, body: """
        #!/bin/sh
        cat >/dev/null
        k=unset
        [ "$OPENROUTER_API_KEY" = "or-test" ] && k=match
        printf '{"fields":[{"id":"q1","status":"manual","sub":"%s","or":"%s"}]}\\n' "$1" "$k"
        """)
        func first(_ data: Data) throws -> [String: Any] {
            let obj = try XCTUnwrap(try JSONSerialization.jsonObject(with: data) as? [String: Any])
            return try XCTUnwrap((obj["fields"] as? [[String: Any]])?.first)
        }
        let runner = DraftRunner(vaniBinary: vani, key: { nil }, decisionKey: { "or-test" })
        let suggest = try first(runner.runJob(.suggest, input: Data(#"{"fields":[]}"#.utf8)))
        XCTAssertEqual(suggest["sub"] as? String, "suggest")
        XCTAssertEqual(suggest["or"] as? String, "match", "suggest gets the decision model's key")
        XCTAssertEqual(try first(runner.runJob(.draft, input: Data("{}".utf8)))["or"] as? String, "unset", "a draft never gets it")
        // A Keychain refusal for the decision key is not fatal: vani falls back to Claude.
        let denied = DraftRunner(vaniBinary: vani, key: { nil }, decisionKey: { throw BankError("denied") })
        XCTAssertEqual(try first(denied.runJob(.suggest, input: Data("{}".utf8)))["or"] as? String, "unset")

        let garbage = try fakeVani(self, body: "#!/bin/sh\ncat >/dev/null\necho nope\n")
        let bad = try XCTUnwrap(try JSONSerialization.jsonObject(with: DraftRunner(vaniBinary: garbage, key: { nil }, decisionKey: { nil }).runJob(.suggest, input: Data("{}".utf8))) as? [String: Any])
        XCTAssertEqual(bad["code"] as? String, "provider")
        let missing = try XCTUnwrap(try JSONSerialization.jsonObject(with: DraftRunner(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani"), key: { nil }, decisionKey: { nil }).runJob(.draft, input: Data("{}".utf8))) as? [String: Any])
        XCTAssertEqual(missing["code"] as? String, "config")
    }

    func testProtocolJobOpsFromBothBrowsers() throws {
        let store = BankStore(directory: try tempDir(self))
        let agent = LocalAgent(jobs: AgentJobs { _, _ in Data(#"{"fields":[{"id":"q1","status":"drafted","draft":"Because."}]}"#.utf8) })
        for source in [Source.safari, .chrome] {
            let started = NativeProtocol.handle(["op": "jobStart", "kind": "suggest", "request": ["fields": [["id": "q1", "label": "Why us?", "kind": "textarea"]]]],
                                                store: store, source: source, agent: agent)
            let id = try XCTUnwrap(started["job"] as? String, "\(started)")
            var reply: [String: Any] = ["pending": true]
            for _ in 0..<200 where reply["pending"] as? Bool == true {
                reply = NativeProtocol.handle(["op": "jobResult", "job": id], store: store, source: source, agent: agent)
                Thread.sleep(forTimeInterval: 0.01)
            }
            XCTAssertEqual(reply["done"] as? Bool, true, "\(reply)")
            let fields = (reply["reply"] as? [String: Any])?["fields"] as? [[String: Any]]
            XCTAssertEqual(fields?.first?["draft"] as? String, "Because.")
        }
        let raw = try String(contentsOf: store.directory.appendingPathComponent("activity.jsonl"), encoding: .utf8)
        XCTAssertFalse(raw.contains("Why us"), "labels must not reach the log")

        for bad: [String: Any] in [
            ["op": "jobStart", "kind": "shell", "request": [:]],
            ["op": "jobStart", "kind": "suggest"],
            ["op": "jobStart", "kind": "suggest", "request": "text"],
            ["op": "jobResult"],
            ["op": "jobResult", "job": String(repeating: "x", count: 65)],
        ] {
            XCTAssertEqual(NativeProtocol.handle(bad, store: store, source: .safari, agent: agent)["code"] as? String, "bad_request", "\(bad)")
        }
        let none = NativeProtocol.handle(["op": "jobStart", "kind": "suggest", "request": [:]], store: store, source: .safari)
        XCTAssertEqual(none["code"] as? String, "unsupported", "no agent client, no jobs")
    }

    func testUnansweredKeychainPromptIsBounded() throws {
        let vani = try fakeVani(self, body: "#!/bin/sh\ncat >/dev/null\necho '{\"fields\":[]}'\n")
        let hang: KeyLookup = { Thread.sleep(forTimeInterval: 5); return "late" }

        // The Anthropic key is required: a prompt nobody answers fails the job, quickly.
        let started = Date()
        let r = DraftRunner(vaniBinary: vani, keychainTimeout: 0.3, key: hang, decisionKey: { nil })
        let reply = try XCTUnwrap(try JSONSerialization.jsonObject(with: r.runJob(.suggest, input: Data("{}".utf8))) as? [String: Any])
        XCTAssertEqual(reply["code"] as? String, "keychain")
        XCTAssertLessThan(Date().timeIntervalSince(started), 2)

        // The decision key is optional: a hung prompt only skips it.
        let optional = DraftRunner(vaniBinary: vani, keychainTimeout: 0.3, key: { nil }, decisionKey: hang)
        let ok = try XCTUnwrap(try JSONSerialization.jsonObject(with: optional.runJob(.suggest, input: Data("{}".utf8))) as? [String: Any])
        XCTAssertNotNil(ok["fields"])

        // A lookup that answers in time, or throws, passes straight through.
        XCTAssertEqual(try DraftRunner.bounded({ "k" }, seconds: 1), "k")
        XCTAssertThrowsError(try DraftRunner.bounded({ throw BankError("denied") }, seconds: 1))
    }

    func testHelpersDirectoryIgnoresARelativeArgv0() {
        // launchd starts the agent with argv[0] = the plist's relative BundleProgram.
        let exe = URL(fileURLWithPath: "/Applications/Vani Apply.app/Contents/Helpers/vani-apply-agent")
        XCTAssertEqual(VaniProcess.helpersDirectory(argv0: "Contents/Helpers/vani-apply-agent", executable: exe).path,
                       "/Applications/Vani Apply.app/Contents/Helpers")
        // Without a known executable it falls back to argv[0].
        XCTAssertEqual(VaniProcess.helpersDirectory(argv0: "/opt/x/vani-apply-host", executable: nil).path, "/opt/x")
        // The running test binary: an absolute folder, never "/Contents/Helpers".
        XCTAssertTrue(VaniProcess.helpersDirectory().path.hasPrefix("/"))
        XCTAssertNotEqual(VaniProcess.helpersDirectory().path, "/Contents/Helpers")
    }

    func testUnreachableAgentIsCodedAndFast() {
        let client = XPCAgentClient(serviceName: "\(AppGroup.identifier).no-such-service-\(UUID().uuidString)")
        let started = Date()
        let r = client.start(kind: "suggest", request: Data("{}".utf8))
        XCTAssertEqual(r["code"] as? String, "agent")
        XCTAssertLessThan(Date().timeIntervalSince(started), XPCAgentClient.callTimeout + 1)
    }
}
