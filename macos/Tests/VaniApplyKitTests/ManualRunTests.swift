import Foundation
import XCTest
@testable import VaniApplyKit

/// A stand-in `vani` that writes its argv and two environment variables to its
/// output, then runs `body`.
private func fakeRunVani(_ t: XCTestCase, body: String) throws -> URL {
    let url = try tempDir(t).appendingPathComponent("vani")
    let script = """
    #!/bin/sh
    printf 'args: %s\\n' "$*"
    printf 'state: %s\\n' "$VANI_STATE_DIR"
    printf 'openrouter: %s\\n' "${OPENROUTER_API_KEY:-none}"
    \(body)
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return url
}

/// Collects one exit status from a job's handler.
private final class ExitBox: @unchecked Sendable {
    private let lock = NSLock()
    private var _code: Int32?
    var code: Int32? { lock.withLock { _code } }
    func set(_ c: Int32) { lock.withLock { _code = c } }
}

final class ManualRunTests: XCTestCase {
    // MARK: Options

    func testDefaultOptionsAreAPlainRunOnce() throws {
        let o = ManualRunOptions()
        XCTAssertTrue(o.isDefault)
        XCTAssertNil(o.summary)
        XCTAssertEqual(try o.arguments(), ["run-once"])
    }

    func testEveryOverrideBecomesItsFlag() throws {
        let o = ManualRunOptions(advance: 40, boards: ["gh:acme", "wd:intel/External"], noDigest: true)
        XCTAssertEqual(try o.arguments(), ["run-once", "--advance", "40", "--board", "gh:acme", "--board", "wd:intel/External", "--no-digest"])
        XCTAssertEqual(o.summary, "2 boards; advance cap 40; no digest")
        let listOnly = ManualRunOptions(listOnly: true)
        XCTAssertEqual(try listOnly.arguments(), ["run-once", "--list-only"])
        XCTAssertEqual(listOnly.summary, "list only")
    }

    /// The bounds vani's checkRunOptions applies are refused before vani starts.
    func testOptionsVaniWouldRefuseAreRefusedFirst() {
        let bad: [ManualRunOptions] = [
            ManualRunOptions(advance: -1),
            ManualRunOptions(advance: ManualRunOptions.maxAdvance + 1),
            ManualRunOptions(advance: 5, listOnly: true),
            ManualRunOptions(boards: Array(repeating: "gh:acme", count: ManualRunOptions.maxBoards + 1)),
            ManualRunOptions(boards: ["acme"]),
            ManualRunOptions(boards: [":acme"]),
            ManualRunOptions(boards: ["gh:"]),
            ManualRunOptions(boards: [""]),
            ManualRunOptions(boards: ["--list-only:x"]),
        ]
        for o in bad {
            XCTAssertThrowsError(try o.arguments(), "\(o)")
        }
        XCTAssertNoThrow(try ManualRunOptions(advance: ManualRunOptions.maxAdvance).arguments())
    }

    func testOptionsRoundTripThroughGoNames() throws {
        let o = ManualRunOptions(advance: 7, boards: ["gh:a"], listOnly: false, noDigest: true)
        let data = try JSONEncoder().encode(o)
        let obj = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertEqual(obj["no_digest"] as? Bool, true)
        XCTAssertEqual(obj["advance"] as? Int, 7)
        XCTAssertEqual(try JSONDecoder().decode(ManualRunOptions.self, from: data), o)
        // Go omits zero values.
        XCTAssertEqual(try JSONDecoder().decode(ManualRunOptions.self, from: Data(#"{"list_only":true}"#.utf8)),
                       ManualRunOptions(listOnly: true))
    }

    // MARK: Status

    private func lastRun(_ json: String) throws -> PipelineStatus.LastRun {
        try JobsClient.decoder.decode(PipelineStatus.LastRun.self, from: Data(json.utf8))
    }

    func testStatusCarriesTheTriggerAndOptions() throws {
        let r = try lastRun(#"{"ok":true,"state":"ok","trigger":"manual","options":{"advance":7,"boards":["gh:a"],"no_digest":true}}"#)
        XCTAssertEqual(r.trigger, .manual)
        XCTAssertEqual(r.options, ManualRunOptions(advance: 7, boards: ["gh:a"], noDigest: true))
        XCTAssertEqual(try lastRun(#"{"ok":true,"state":"ok","trigger":"schedule"}"#).trigger, .schedule)
        XCTAssertEqual(try lastRun(#"{"ok":true,"state":"ok","trigger":"webhook"}"#).trigger, .unknown("webhook"))
    }

    /// An older vani records neither; a malformed options object is display
    /// detail, so it is dropped and never fails the status read.
    func testOlderAndMalformedRunsStillDecode() throws {
        let old = try lastRun(#"{"ok":true,"state":"ok"}"#)
        XCTAssertNil(old.trigger)
        XCTAssertNil(old.options)
        let bad = try lastRun(#"{"ok":true,"state":"failed","trigger":"manual","options":"advance=7"}"#)
        XCTAssertEqual(bad.state, .failed)
        XCTAssertNil(bad.options)
        XCTAssertEqual(bad.trigger, .manual)
    }

    // MARK: Launch

    private func waitForExit(_ box: ExitBox, within seconds: TimeInterval = 10) -> Int32? {
        let deadline = Date().addingTimeInterval(seconds)
        while box.code == nil && Date() < deadline { usleep(20_000) }
        return box.code
    }

    func testLaunchWritesTheLogAndReportsTheExit() throws {
        let vani = try fakeRunVani(self, body: "echo 'vani: first' >&2; echo 'vani: another vani holds it' >&2; exit 1")
        let state = try tempDir(self).path
        let runner = DraftRunner(vaniBinary: vani, stateDir: state, keys: .noKey, decisionKey: { "or-test-key" })
        let box = ExitBox()
        let job = try ManualRunLauncher(runner: runner).start(ManualRunOptions(advance: 3), stateDir: state) { box.set($0) }
        XCTAssertEqual(waitForExit(box), 1)
        XCTAssertEqual(job.exitStatus, 1)
        XCTAssertEqual(job.log.path, state + "/runs/manual.log")
        let log = try String(contentsOf: job.log, encoding: .utf8)
        XCTAssertTrue(log.contains("args: run-once --advance 3"), log)
        XCTAssertTrue(log.contains("state: \(state)"), log)
        XCTAssertTrue(log.contains("openrouter: or-test-key"), "the run gets the decision key drafting gets: \(log)")
        let mode = try FileManager.default.attributesOfItem(atPath: job.log.path)[.posixPermissions] as? Int
        XCTAssertEqual(mode, 0o600)
        XCTAssertEqual(VaniProcess.lastVaniLine(inLog: job.log), "another vani holds it")
    }

    /// Each run replaces the log; a log path that is a symlink is refused.
    func testLogIsTruncatedAndNeverFollowsASymlink() throws {
        let state = try tempDir(self).path
        let log = ManualRunLauncher.logURL(stateDir: state)
        try FileManager.default.createDirectory(at: log.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data(String(repeating: "old line\n", count: 1000).utf8).write(to: log)
        let vani = try fakeRunVani(self, body: "exit 0")
        let runner = DraftRunner(vaniBinary: vani, stateDir: state, keys: .noKey, decisionKey: { nil })
        let box = ExitBox()
        _ = try ManualRunLauncher(runner: runner).start(ManualRunOptions(), stateDir: state) { box.set($0) }
        XCTAssertEqual(waitForExit(box), 0)
        let text = try String(contentsOf: log, encoding: .utf8)
        XCTAssertFalse(text.contains("old line"))
        XCTAssertNil(VaniProcess.lastVaniLine(inLog: log), "no vani line in the log")

        let target = try tempDir(self).appendingPathComponent("elsewhere")
        try Data("keep".utf8).write(to: target)
        try FileManager.default.removeItem(at: log)
        try FileManager.default.createSymbolicLink(at: log, withDestinationURL: target)
        XCTAssertThrowsError(try ManualRunLauncher(runner: runner).start(ManualRunOptions(), stateDir: state) { _ in })
        XCTAssertEqual(try String(contentsOf: target, encoding: .utf8), "keep")
    }

    /// Stop sends SIGTERM to the job this app started, which ends it.
    func testStopEndsTheJob() throws {
        let vani = try fakeRunVani(self, body: "trap 'echo \"vani: run cancelled\" >&2; exit 1' TERM; while :; do sleep 0.1; done")
        let state = try tempDir(self).path
        let runner = DraftRunner(vaniBinary: vani, stateDir: state, keys: .noKey, decisionKey: { nil })
        let box = ExitBox()
        let job = try ManualRunLauncher(runner: runner).start(ManualRunOptions(), stateDir: state) { box.set($0) }
        usleep(300_000)
        XCTAssertTrue(job.isRunning)
        XCTAssertNil(job.exitStatus)
        job.stop()
        XCTAssertNotNil(waitForExit(box), "the job did not end after Stop")
        XCTAssertFalse(job.isRunning)
        XCTAssertEqual(VaniProcess.lastVaniLine(inLog: job.log), "run cancelled")
    }

    func testRefusalsBeforeAnythingStarts() throws {
        let state = try tempDir(self).path
        let runner = DraftRunner(vaniBinary: URL(fileURLWithPath: "/nonexistent/vani"), keys: .noKey, decisionKey: { nil })
        let launcher = ManualRunLauncher(runner: runner)
        XCTAssertThrowsError(try launcher.start(ManualRunOptions(), stateDir: "") { _ in })
        XCTAssertThrowsError(try launcher.start(ManualRunOptions(advance: -1), stateDir: state) { _ in })
        XCTAssertThrowsError(try launcher.start(ManualRunOptions(), stateDir: state) { _ in }) { err in
            XCTAssertTrue(String(describing: err).contains("missing"), "\(err)")
        }
        XCTAssertFalse(FileManager.default.fileExists(atPath: ManualRunLauncher.logURL(stateDir: state).path),
                       "a refused run must not create its log")
    }

    /// Only the log's tail is read, and the line is bounded to one line.
    func testLastVaniLineIsBounded() throws {
        let log = try tempDir(self).appendingPathComponent("manual.log")
        var text = "vani: early line\n" + String(repeating: "x", count: 40_000) + "\n"
        text += "vani: " + String(repeating: "y", count: 1000) + "\u{202E}\n"
        try Data(text.utf8).write(to: log)
        let line = try XCTUnwrap(VaniProcess.lastVaniLine(inLog: log, maxCharacters: 50))
        XCTAssertEqual(line.count, 50)
        XCTAssertTrue(line.hasPrefix("yyy"))
        XCTAssertNil(VaniProcess.lastVaniLine(inLog: log.deletingLastPathComponent().appendingPathComponent("missing")))
    }
}
