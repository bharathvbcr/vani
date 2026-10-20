import Foundation
import XCTest
@testable import VaniApplyKit

/// A whole `vani status --json` object with `extra` spliced in after `decide`.
private func statusJSON(_ extra: String) -> Data {
    let base = #"{"version":1,"generated_at":"2026-10-20T00:00:00Z","state_dir":"/s","config":{"ok":true,"boards":1},"ledger":{"ok":true,"exists":true,"path":"/l","total":0,"by_status":{}},"last_run":{"ok":true,"state":"none"},"schedule":{"ok":true,"daily_at":"08:30","timezone":"UTC"},"backends":{"ok":true,"day":"2026-10-20","items":[]},"decide":{"ok":true,"local_configured":false,"lappi_configured":false}"#
    return Data((base + extra + "}").utf8)
}

/// A stand-in `vani` that appends its argv to `calls` and runs `body`.
private func fakeVani(_ t: XCTestCase, body: String) throws -> (vani: URL, calls: URL) {
    let dir = try tempDir(t)
    let vani = dir.appendingPathComponent("vani"), calls = dir.appendingPathComponent("calls")
    let script = """
    #!/bin/sh
    echo "$*" >> '\(calls.path)'
    \(body)
    """
    try Data(script.utf8).write(to: vani)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: vani.path)
    return (vani, calls)
}

final class RunRequestTests: XCTestCase {
    // MARK: Status facets

    /// A vani from before run requests reports neither facet: both are nil,
    /// which the Pipeline tab reads as "no daemon known" (run-once, as before).
    func testOlderStatusHasNoDaemonFacets() throws {
        let s = try JobsClient.decoder.decode(PipelineStatus.self, from: statusJSON(""))
        XCTAssertNil(s.daemon)
        XCTAssertNil(s.runRequest)
    }

    func testDaemonAndRunRequestDecode() throws {
        let s = try JobsClient.decoder.decode(PipelineStatus.self, from: statusJSON(
            #","daemon":{"ok":true,"running":true,"pid":4242,"since":"2026-10-19T08:00:00.123456789Z"},"# +
            #""run_request":{"ok":true,"pending":true,"requested_at":"2026-10-20T00:00:01.5Z","options":{"advance":7,"no_digest":true},"claimed":false}"#))
        XCTAssertEqual(s.daemon?.running, true)
        XCTAssertEqual(s.daemon?.pid, 4242)
        XCTAssertNotNil(s.daemon?.since)
        let r = try XCTUnwrap(s.runRequest)
        XCTAssertTrue(r.ok && r.pending && !r.claimed && !r.stale)
        XCTAssertEqual(r.options, ManualRunOptions(advance: 7, noDigest: true))
        XCTAssertNotNil(r.requestedAt)

        let claimed = try JobsClient.decoder.decode(PipelineStatus.self, from: statusJSON(
            #","daemon":{"ok":true,"running":false,"reason":"no daemon has announced itself"},"# +
            #""run_request":{"ok":true,"pending":false,"claimed":true,"claimed_requested_at":"2026-10-20T00:00:01Z"}"#))
        XCTAssertEqual(claimed.daemon?.running, false)
        XCTAssertEqual(claimed.runRequest?.claimed, true)
        XCTAssertNotNil(claimed.runRequest?.claimedRequestedAt)
    }

    /// A facet in a shape this app does not know is not ok, and never fails
    /// the whole status read (the tab would go blank over a detail).
    func testMalformedFacetsNeverFailTheRead() throws {
        let s = try JobsClient.decoder.decode(PipelineStatus.self, from: statusJSON(
            #","daemon":"yes","run_request":{"ok":"maybe","pending":1,"requested_at":"yesterday","options":"all"}"#))
        XCTAssertEqual(s.daemon?.ok, false)
        XCTAssertEqual(s.daemon?.running, false, "an undecodable daemon facet is never read as running")
        XCTAssertEqual(s.runRequest?.ok, false)
        XCTAssertEqual(s.runRequest?.pending, false)
        XCTAssertNil(s.runRequest?.requestedAt)
        XCTAssertNil(s.runRequest?.options)
        let nulls = try JobsClient.decoder.decode(PipelineStatus.self, from: statusJSON(#","daemon":null,"run_request":null"#))
        XCTAssertNil(nulls.daemon)
        XCTAssertNil(nulls.runRequest)
    }

    // MARK: Client

    func testRunRequestArguments() throws {
        XCTAssertEqual(try ManualRunOptions(advance: 7, boards: ["gh:a"], noDigest: true).arguments(command: "run-request"),
                       ["run-request", "--advance", "7", "--board", "gh:a", "--no-digest"])
        XCTAssertThrowsError(try ManualRunOptions(advance: 3, listOnly: true).arguments(command: "run-request"))
    }

    func testRequestRunCallsRunRequestAndDecodesTheReply() throws {
        let f = try fakeVani(self, body: #"printf '{"requested_at":"2026-10-20T00:00:01.25Z","options":{"advance":7},"daemon_pid":4242}\n'"#)
        let reply = try PipelineClient(vaniBinary: f.vani, timeout: 10).requestRun(ManualRunOptions(advance: 7))
        XCTAssertEqual(reply.daemonPID, 4242)
        XCTAssertEqual(reply.options, ManualRunOptions(advance: 7))
        let calls = try String(contentsOf: f.calls, encoding: .utf8)
        XCTAssertEqual(calls, "run-request --advance 7 --json\n")
    }

    /// vani's refusal ({"error"}, exit 1) is the error shown; a vani without
    /// run-request (exit 2) says to rebuild the app.
    func testRequestRunRefusals() throws {
        let refused = try fakeVani(self, body: #"printf '{"error":"no running vani daemon: the daemon recorded as pid 9 is gone"}\n'; exit 1"#)
        XCTAssertThrowsError(try PipelineClient(vaniBinary: refused.vani, timeout: 10).requestRun(ManualRunOptions())) {
            XCTAssertTrue(String(describing: $0).contains("no running vani daemon"), String(describing: $0))
        }
        let old = try fakeVani(self, body: "exit 2")
        XCTAssertThrowsError(try PipelineClient(vaniBinary: old.vani, timeout: 10).requestRun(ManualRunOptions())) {
            XCTAssertTrue(String(describing: $0).contains("rebuild the app"), String(describing: $0))
        }
        // Options vani would refuse never start it.
        let unused = try fakeVani(self, body: "exit 0")
        XCTAssertThrowsError(try PipelineClient(vaniBinary: unused.vani, timeout: 10).requestRun(ManualRunOptions(advance: -1)))
        XCTAssertFalse(FileManager.default.fileExists(atPath: unused.calls.path))
    }
}
