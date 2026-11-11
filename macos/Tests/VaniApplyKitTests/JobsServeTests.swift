import XCTest
@testable import VaniApplyKit

final class JobsServeTests: XCTestCase {
    // MARK: reading response lines

    func testLineIDAndOKResult() {
        let line = Data(#"{"id":"12","ok":true,"result":{"view":"new","jobs":[{"title":"a > b"}]}}"#.utf8)
        XCTAssertEqual(JobsServeClient.lineID(line), "12")
        XCTAssertEqual(JobsServeClient.okResult(line, id: "12").map { String(decoding: $0, as: UTF8.self) },
                       #"{"view":"new","jobs":[{"title":"a > b"}]}"#, "the result bytes, unparsed")
        XCTAssertNil(JobsServeClient.okResult(line, id: "1"), "another id is not this request's result")
        XCTAssertNil(JobsServeClient.okResult(Data(#"{"id":"12","ok":false,"error":{}}"#.utf8), id: "12"))
        XCTAssertNil(JobsServeClient.okResult(Data(#"{"id":"12","ok":true,"result":"#.utf8), id: "12"), "a cut line")
        for bad in ["", "not json", #"{"op":"x"}"#, #"{"id":12}"#, #"{"id":""# + String(repeating: "9", count: 40) + #""}"#] {
            XCTAssertNil(JobsServeClient.lineID(Data(bad.utf8)), bad)
        }
    }

    func testAnswerMapsServeErrors() throws {
        func line(_ code: String) -> Data {
            Data(#"{"id":"7","ok":false,"error":{"code":"\#(code)","message":"why","retryable":true}}"#.utf8)
        }
        XCTAssertEqual(try JobsServeClient.answer(line("E_COMMAND"), id: "7", name: "vani jobs show", timeout: 30), .failed(message: "why"))
        XCTAssertThrowsError(try JobsServeClient.answer(line("E_SUPERSEDED"), id: "7", name: "vani jobs list", timeout: 30)) {
            XCTAssertEqual($0 as? VaniCallCancelled, VaniCallCancelled(command: "vani jobs list"))
        }
        XCTAssertThrowsError(try JobsServeClient.answer(line("E_TIMEOUT"), id: "7", name: "vani jobs list", timeout: 30)) {
            XCTAssertEqual(($0 as? BankError)?.description, "vani jobs list did not finish within 30 s")
        }
        for fallBack in ["E_BUSY", "E_BAD_REQUEST", "E_INTERNAL", "E_TOO_LARGE", "E_SOMETHING_NEW"] {
            XCTAssertNil(try JobsServeClient.answer(line(fallBack), id: "7", name: "vani jobs list", timeout: 30), fallBack)
        }
        XCTAssertNil(try JobsServeClient.answer(line("E_COMMAND"), id: "8", name: "n", timeout: 30), "another request's line")
        // A success in a layout okResult does not read still yields the result's value.
        let spaced = Data(#"{ "ok": true, "id": "7", "result": {"a": [1, 2]} }"#.utf8)
        guard case let .reply(data)? = try JobsServeClient.answer(spaced, id: "7", name: "n", timeout: 30) else {
            return XCTFail("no reply")
        }
        XCTAssertEqual(try JSONSerialization.jsonObject(with: data) as? [String: [Int]], ["a": [1, 2]])
    }

    // MARK: against the real vani (build/vani; skipped when it is not built)

    private func realVani() throws -> URL {
        let vani = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("build/vani")
        guard FileManager.default.isExecutableFile(atPath: vani.path) else {
            throw XCTSkip("no Go vani at \(vani.path); build it to run the contract test")
        }
        return vani
    }

    private func ledger(_ state: URL) throws {
        let dir = state.appendingPathComponent("ledger")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        var lines: [String] = []
        for i in 0..<12 {
            let key = "gh:co\(i % 3):\(i)", at = "2026-10-\(10 + i)T12:00:00Z"
            let status = ["digested", "scored", "live"][i % 3]
            let desc = "Go and Rust on Kubernetes. Pay: $\(150 + i),000 - $\(200 + i),000 per year."
            lines.append(#"{"v":1,"at":"\#(at)","key":"\#(key)","status":"\#(status)","reason":"new","posting":{"key":"\#(key)","ats":"gh","company":"Co \#(i % 3)","title":"Engineer \#(i)","description":"\#(desc)","seen_at":"\#(at)"},"annotations":{"triage.role_fit":"strong_fit"}}"#)
        }
        try Data((lines.joined(separator: "\n") + "\n").utf8).write(to: dir.appendingPathComponent("events.jsonl"))
    }

    /// Waits until serve has said hello, asking (which starts it) until then.
    private func waitReady(_ serve: JobsServeClient, _ c: JobsClient) throws {
        let deadline = Date().addingTimeInterval(60)
        while !serve.isReady {
            _ = try c.list(.new)
            guard Date() < deadline else { return XCTFail("serve never said hello: \(serve.unavailable ?? "no reason")") }
            Thread.sleep(forTimeInterval: 0.05)
        }
    }

    /// The same request through serve and through the command decodes alike; a serve that dies
    /// is not waited on (the command answers), is started once more, and is not started a third
    /// time.
    func testServeAnswersAsTheCommandAndFallsBack() throws {
        let vani = try realVani()
        let state = try tempDir(self)
        try ledger(state)
        let serve = JobsServeClient(binary: vani, configPath: nil, stateDir: state.path)
        defer { serve.stop() }
        let viaServe = JobsClient(vaniBinary: vani, stateDir: state.path, serve: serve)
        let direct = JobsClient(vaniBinary: vani, stateDir: state.path)

        XCTAssertEqual(try viaServe.list(.new).jobs.map(\.key), try direct.list(.new).jobs.map(\.key),
                       "before serve has said hello, the command answers")
        try waitReady(serve, viaServe)
        let before = serve.answered
        for (sort, view) in [(JobSort.standard, JobView.new), (.company, .all), (.salary, .new)] {
            let a = try viaServe.list(view, sort: sort), b = try direct.list(view, sort: sort)
            XCTAssertEqual(a.jobs.map(\.key), b.jobs.map(\.key), "\(view) \(sort)")
            XCTAssertEqual(a.total, b.total)
            XCTAssertEqual(a.jobs.map(\.priority), b.jobs.map(\.priority))
        }
        XCTAssertEqual(try viaServe.stats(.new).total, try direct.stats(.new).total)
        XCTAssertEqual(try viaServe.show("gh:co0:0").title, "Engineer 0")
        XCTAssertThrowsError(try viaServe.show("gh:nope:1"), "the command's failure, thrown as the command's")
        XCTAssertEqual(serve.answered - before, 6, "every request went to serve")

        // Serve dies: the next request runs the command, and serve starts again after the backoff.
        for start in 1...2 {
            guard let pid = serve.pid else { return XCTFail("no serve running before death \(start)") }
            kill(pid, SIGKILL)
            let answered = serve.answered
            XCTAssertEqual(try viaServe.list(.new).jobs.map(\.key), try direct.list(.new).jobs.map(\.key))
            XCTAssertEqual(serve.answered, answered, "a dead serve answered nothing")
            if start == 1 {
                Thread.sleep(forTimeInterval: JobsServeClient.restartBackoff + 0.2)
                try waitReady(serve, viaServe)
                XCTAssertEqual(try viaServe.list(.new).jobs.map(\.key), try direct.list(.new).jobs.map(\.key))
                XCTAssertGreaterThan(serve.answered, answered, "the restarted serve answers")
            }
        }
        Thread.sleep(forTimeInterval: JobsServeClient.restartBackoff + 0.2)
        _ = try viaServe.list(.new)
        XCTAssertNotNil(serve.unavailable, "a serve that stopped twice is not started a third time")
        XCTAssertFalse(serve.isReady)
    }

    /// A list cancelled while serve computes it throws at once, and serve goes on answering.
    func testCancelledServeRequest() throws {
        let vani = try realVani()
        let state = try tempDir(self)
        try ledger(state)
        let serve = JobsServeClient(binary: vani, configPath: nil, stateDir: state.path)
        defer { serve.stop() }
        let c = JobsClient(vaniBinary: vani, stateDir: state.path, serve: serve)
        try waitReady(serve, c)
        let flag = CancelFlag()
        flag.set()
        XCTAssertThrowsError(try c.list(.new, cancellation: flag)) { XCTAssertTrue($0 is VaniCallCancelled) }
        XCTAssertFalse(try c.list(.new).jobs.isEmpty)
    }
}
