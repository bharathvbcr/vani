import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// A fake `vani` for the Pipeline tab: `status --json` prints status.json, and
/// `run-once` acts out the run named in the file `mode`, rewriting status.json
/// the way a real run rewrites its record. Every call is appended to `calls`.
private struct PipelineFixture {
    let dir: URL
    var vani: URL { dir.appendingPathComponent("vani") }

    init(_ t: XCTestCase, mode: String, lastRun: String) throws {
        dir = FileManager.default.temporaryDirectory.appendingPathComponent("vani-pipeline-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let d = dir
        t.addTeardownBlock { try? FileManager.default.removeItem(at: d) }
        let script = #"""
        #!/bin/sh
        D='\#(dir.path)'
        echo "$*" >> "$D/calls"
        status() {
          printf '{"version":1,"generated_at":"2026-10-20T00:00:00Z","state_dir":"%s","config":{"ok":true,"boards":1},"ledger":{"ok":true,"exists":true,"path":"/l","total":0,"by_status":{}},"last_run":%s,"schedule":{"ok":true,"daily_at":"08:30","timezone":"UTC"},"backends":{"ok":true,"day":"2026-10-20","items":[]},"decide":{"ok":true,"local_configured":false,"lappi_configured":false}}' "$D" "$1" > "$D/status.tmp" && mv "$D/status.tmp" "$D/status.json"
        }
        case "$1" in
        status) cat "$D/status.json"; exit 0 ;;
        run-once) ;;
        *) exit 2 ;;
        esac
        now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
        running="{\"ok\":true,\"state\":\"running\",\"trigger\":\"manual\",\"started_at\":\"$now\"}"
        case "$(cat "$D/mode")" in
        refused)
          echo 'vani: ledger: directory is locked by another process: another vani holds it (the daemon)' >&2
          exit 1 ;;
        recorded)
          status "$running"
          sleep 1
          status "{\"ok\":true,\"state\":\"ok\",\"trigger\":\"manual\",\"options\":{\"no_digest\":true},\"started_at\":\"$now\",\"finished_at\":\"$now\"}"
          exit 0 ;;
        stoppable)
          trap 'status "{\"ok\":true,\"state\":\"cancelled\",\"trigger\":\"manual\",\"started_at\":\"$now\",\"finished_at\":\"$now\"}"; echo "vani: run cancelled" >&2; exit 1' TERM
          status "$running"
          while :; do sleep 0.1; done ;;
        esac
        exit 3
        """#
        try Data(script.utf8).write(to: vani)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: vani.path)
        try Data(mode.utf8).write(to: dir.appendingPathComponent("mode"))
        // The record before the run, in the shape the script's status() writes.
        let status = #"{"version":1,"generated_at":"2026-10-20T00:00:00Z","state_dir":"\#(dir.path)","config":{"ok":true,"boards":1},"ledger":{"ok":true,"exists":true,"path":"/l","total":0,"by_status":{}},"last_run":\#(lastRun),"schedule":{"ok":true,"daily_at":"08:30","timezone":"UTC"},"backends":{"ok":true,"day":"2026-10-20","items":[]},"decide":{"ok":true,"local_configured":false,"lappi_configured":false}}"#
        try Data(status.utf8).write(to: dir.appendingPathComponent("status.json"))
    }

    func calls() -> [String] {
        ((try? String(contentsOf: dir.appendingPathComponent("calls"), encoding: .utf8)) ?? "")
            .split(separator: "\n").map(String.init)
    }
}

private let oldRun = #"{"ok":true,"state":"ok","trigger":"schedule","started_at":"2026-10-14T08:30:00Z","finished_at":"2026-10-14T08:35:00Z"}"#

@MainActor
final class PipelineModelTests: XCTestCase {
    private func model(_ f: PipelineFixture) throws -> PipelineModel {
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "vani-pipeline-tests-\(UUID().uuidString)"))
        let vani = f.vani
        return PipelineModel(client: { PipelineClient(vaniBinary: vani, timeout: 10) },
                             launcher: { ManualRunLauncher(runner: DraftRunner(vaniBinary: vani, keys: .noKey, decisionKey: { nil })) },
                             defaults: defaults)
    }

    private func eventually(_ what: String, within seconds: TimeInterval = 10, _ done: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(seconds)
        while !done() {
            if Date() > deadline { return XCTFail("timed out waiting for \(what)") }
            try await Task.sleep(nanoseconds: 20_000_000)
        }
    }

    /// vani refuses the run before it records anything (the daemon holds the
    /// ledger): the tab says why, from the log, and the old record stays.
    func testARunRefusedBeforeItStartsSaysWhy() async throws {
        let f = try PipelineFixture(self, mode: "refused", lastRun: oldRun)
        let m = try model(f)
        await m.refresh()
        XCTAssertNil(m.runBlocker)
        let options = ManualRunOptions(advance: 9, noDigest: true)
        await m.startRun(options)
        try await eventually("the refusal") { m.runError != nil && m.manualRun == nil }
        XCTAssertTrue(m.runError?.contains("another vani holds it") == true, m.runError ?? "")
        XCTAssertFalse(m.runInProgress)
        XCTAssertEqual(m.status?.lastRun.trigger, .schedule)
        XCTAssertTrue(f.calls().contains("run-once --advance 9 --no-digest"), "\(f.calls())")
        XCTAssertEqual(m.savedOptions, options, "the choices are remembered for next time")
    }

    /// A run vani records is followed to its end, and its own record reports it:
    /// no error banner, whatever the record's outcome.
    func testARecordedRunIsFollowedToItsEnd() async throws {
        let f = try PipelineFixture(self, mode: "recorded", lastRun: oldRun)
        let m = try model(f)
        await m.refresh()
        await m.startRun(ManualRunOptions(noDigest: true))
        XCTAssertTrue(m.runInProgress)
        XCTAssertNotNil(m.runBlocker, "a second run cannot start while one is going")
        try await eventually("the run's end") { m.manualRun == nil }
        XCTAssertFalse(m.runInProgress)
        XCTAssertEqual(m.status?.lastRun.state, .ok, "the handle is let go only after the run's own record was read")
        XCTAssertNil(m.runError)
        XCTAssertEqual(m.status?.lastRun.trigger, .manual)
        XCTAssertEqual(m.status?.lastRun.options, ManualRunOptions(noDigest: true))
        XCTAssertNil(m.runBlocker)
    }

    /// Stop ends the run this app started; vani records it cancelled, which is
    /// the record's to report, not an error banner.
    func testStopCancelsTheRunThisAppStarted() async throws {
        let f = try PipelineFixture(self, mode: "stoppable", lastRun: oldRun)
        let m = try model(f)
        await m.refresh()
        await m.startRun(ManualRunOptions())
        try await eventually("the run's record") { m.status?.lastRun.state == .running }
        m.stopRun()
        try await eventually("the cancelled record") { m.manualRun == nil && m.status?.lastRun.state == .cancelled }
        XCTAssertNil(m.runError)
        XCTAssertFalse(m.runInProgress)
    }

    /// A run the record shows going (the daemon's) blocks Run Now, and this app
    /// never starts a second one or signals the first.
    func testARunGoingElsewhereBlocksRunNow() async throws {
        let f = try PipelineFixture(self, mode: "refused",
                                    lastRun: #"{"ok":true,"state":"running","trigger":"schedule","started_at":"2026-10-20T00:00:00Z"}"#)
        let m = try model(f)
        XCTAssertNotNil(m.runBlocker, "no status yet")
        await m.refresh()
        XCTAssertTrue(m.runInProgress)
        XCTAssertNotNil(m.runBlocker)
        await m.startRun(ManualRunOptions())
        XCTAssertNil(m.manualRun)
        m.stopRun() // nothing of this app's to stop
        XCTAssertFalse(f.calls().contains { $0.hasPrefix("run-once") }, "\(f.calls())")
    }

    /// Options vani would refuse never start a process.
    func testRefusedOptionsStartNothing() async throws {
        let f = try PipelineFixture(self, mode: "recorded", lastRun: oldRun)
        let m = try model(f)
        await m.refresh()
        await m.startRun(ManualRunOptions(advance: 3, listOnly: true))
        XCTAssertNotNil(m.runError)
        XCTAssertNil(m.manualRun)
        XCTAssertFalse(f.calls().contains { $0.hasPrefix("run-once") })
    }
}
