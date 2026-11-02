import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// A fake `vani` for the Pipeline tab: `status --json` prints status.json, and
/// `run-once` acts out the run named in the file `mode`, rewriting status.json
/// the way a real run rewrites its record. `run-request` acts out a daemon that
/// picks the request up (`daemon-picks`) or drops it (`daemon-drops`), in the
/// background as the real daemon would. `daemon` is the status's daemon facet
/// (none: an older vani). Every call is appended to `calls`.
private struct PipelineFixture {
    let dir: URL
    var vani: URL { dir.appendingPathComponent("vani") }

    init(_ t: XCTestCase, mode: String, lastRun: String, daemon: String? = nil) throws {
        dir = FileManager.default.temporaryDirectory.appendingPathComponent("vani-pipeline-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let d = dir
        t.addTeardownBlock { try? FileManager.default.removeItem(at: d) }
        let script = #"""
        #!/bin/sh
        D='\#(dir.path)'
        echo "$*" >> "$D/calls"
        # status LAST_RUN [RUN_REQUEST]: rewrite status.json, with the daemon facet from the file daemon.
        status() {
          extra=""
          [ -f "$D/daemon" ] && extra=",\"daemon\":$(cat "$D/daemon")"
          [ -n "$2" ] && extra="$extra,\"run_request\":$2"
          printf '{"version":1,"generated_at":"2026-10-20T00:00:00Z","state_dir":"%s","config":{"ok":true,"boards":1},"ledger":{"ok":true,"exists":true,"path":"/l","total":0,"by_status":{}},"last_run":%s,"schedule":{"ok":true,"daily_at":"08:30","timezone":"UTC"},"backends":{"ok":true,"day":"2026-10-20","items":[]},"decide":{"ok":true,"local_configured":false,"lappi_configured":false}%s}' "$D" "$1" "$extra" > "$D/status.tmp" && mv "$D/status.tmp" "$D/status.json"
        }
        now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
        running="{\"ok\":true,\"state\":\"running\",\"trigger\":\"manual\",\"started_at\":\"$now\"}"
        case "$1" in
        status) cat "$D/status.json"; exit 0 ;;
        run-once) ;;
        run-request)
          old=$(cat "$D/lastrun")
          status "$old" "{\"ok\":true,\"pending\":true,\"requested_at\":\"$now\",\"claimed\":false}"
          case "$(cat "$D/mode")" in
          daemon-picks)
            ( sleep 1
              status "$running" "{\"ok\":true,\"pending\":false,\"claimed\":true,\"claimed_requested_at\":\"$now\"}"
              sleep 1
              status "{\"ok\":true,\"state\":\"ok\",\"trigger\":\"manual\",\"options\":{\"advance\":9},\"started_at\":\"$now\",\"finished_at\":\"$now\"}" "{\"ok\":true,\"pending\":false,\"claimed\":false}"
            ) >/dev/null 2>&1 &
            ;;
          daemon-drops)
            ( sleep 1; status "$old" "{\"ok\":true,\"pending\":false,\"claimed\":false}" ) >/dev/null 2>&1 &
            ;;
          esac
          printf '{"requested_at":"%s","options":{"advance":9},"daemon_pid":4242}\n' "$now"
          exit 0 ;;
        *) exit 2 ;;
        esac
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
        try Data(lastRun.utf8).write(to: dir.appendingPathComponent("lastrun"))
        var extra = ""
        if let daemon {
            try Data(daemon.utf8).write(to: dir.appendingPathComponent("daemon"))
            extra = #","daemon":\#(daemon)"#
        }
        // The record before the run, in the shape the script's status() writes.
        let status = #"{"version":1,"generated_at":"2026-10-20T00:00:00Z","state_dir":"\#(dir.path)","config":{"ok":true,"boards":1},"ledger":{"ok":true,"exists":true,"path":"/l","total":0,"by_status":{}},"last_run":\#(lastRun),"schedule":{"ok":true,"daily_at":"08:30","timezone":"UTC"},"backends":{"ok":true,"day":"2026-10-20","items":[]},"decide":{"ok":true,"local_configured":false,"lappi_configured":false}\#(extra)}"#
        try Data(status.utf8).write(to: dir.appendingPathComponent("status.json"))
    }

    func calls() -> [String] {
        ((try? String(contentsOf: dir.appendingPathComponent("calls"), encoding: .utf8)) ?? "")
            .split(separator: "\n").map(String.init)
    }
}

private let oldRun = #"{"ok":true,"state":"ok","trigger":"schedule","started_at":"2026-10-14T08:30:00Z","finished_at":"2026-10-14T08:35:00Z"}"#
private let daemonUp = #"{"ok":true,"running":true,"pid":4242,"since":"2026-10-19T08:00:00Z"}"#

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

    /// A daily run this app did not start is still re-read every few seconds.
    /// The minute poll is only for a quiet app.
    func testADaemonRunIsRereadWithinAFewSeconds() async throws {
        XCTAssertEqual(PipelineModel.statusPollInterval(runInProgress: false, waitingForDaemon: false, startingRun: false),
                       PipelineModel.pollInterval)
        XCTAssertEqual(PipelineModel.statusPollInterval(runInProgress: true, waitingForDaemon: false, startingRun: false),
                       PipelineModel.runningPollInterval)
        XCTAssertEqual(PipelineModel.statusPollInterval(runInProgress: false, waitingForDaemon: true, startingRun: false),
                       PipelineModel.runningPollInterval)
        XCTAssertEqual(PipelineModel.statusPollInterval(runInProgress: false, waitingForDaemon: false, startingRun: true),
                       PipelineModel.runningPollInterval)

        let running = #"{"ok":true,"state":"running","trigger":"schedule","started_at":"2026-10-20T00:00:00Z"}"#
        let f = try PipelineFixture(self, mode: "refused", lastRun: running)
        let m = try model(f)
        let task = Task { await m.poll() }
        defer { task.cancel() }
        try await eventually("a second status read", within: 8) {
            f.calls().filter { $0.hasPrefix("status") }.count >= 2
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
        XCTAssertEqual(m.runError, "A run is going.")
        XCTAssertNil(m.manualRun)
        XCTAssertEqual(m.savedOptions, ManualRunOptions(), "a blocked start keeps nothing")
        m.stopRun() // nothing of this app's to stop
        XCTAssertFalse(f.calls().contains { $0.hasPrefix("run-once") }, "\(f.calls())")
    }

    /// With the daemon running, Run Now asks it (`run-request`) instead of
    /// starting run-once, waits while the request is pending, and follows the
    /// daemon's run once it starts. Nothing of the daemon's is ever signalled.
    func testWithTheDaemonRunningTheRunIsAskedOfIt() async throws {
        let f = try PipelineFixture(self, mode: "daemon-picks", lastRun: oldRun, daemon: daemonUp)
        let m = try model(f)
        await m.refresh()
        XCTAssertTrue(m.daemonRunning)
        await m.startRun(ManualRunOptions(advance: 9))
        XCTAssertNil(m.runError)
        XCTAssertTrue(m.waitingForDaemon, "the daemon has not picked it up yet")
        XCTAssertNotNil(m.runBlocker, "a second run cannot be asked for while one waits")
        XCTAssertNil(m.manualRun, "no process of this app's own")
        m.stopRun() // nothing of this app's to stop
        try await eventually("the daemon's run to end") { !m.waitingForDaemon && m.status?.lastRun.state == .ok && !m.runInProgress }
        XCTAssertNil(m.runError)
        XCTAssertEqual(m.status?.lastRun.trigger, .manual)
        XCTAssertEqual(m.status?.lastRun.options, ManualRunOptions(advance: 9))
        XCTAssertEqual(m.savedOptions, ManualRunOptions(advance: 9))
        XCTAssertTrue(f.calls().contains("run-request --advance 9 --json"), "\(f.calls())")
        XCTAssertFalse(f.calls().contains { $0.hasPrefix("run-once") }, "\(f.calls())")
        XCTAssertNil(m.runBlocker)
    }

    /// A request the daemon drops without a run (stale, invalid) is reported,
    /// rather than waited on for ever.
    func testARequestTheDaemonDropsSaysSo() async throws {
        let f = try PipelineFixture(self, mode: "daemon-drops", lastRun: oldRun, daemon: daemonUp)
        let m = try model(f)
        await m.refresh()
        await m.startRun(ManualRunOptions(advance: 9))
        XCTAssertTrue(m.waitingForDaemon)
        try await eventually("the dropped request") { !m.waitingForDaemon }
        XCTAssertTrue(m.runError?.contains("dropped") == true, m.runError ?? "")
        XCTAssertEqual(m.status?.lastRun.trigger, .schedule, "the old record stays")
    }

    /// A daemon facet saying no daemon runs (or none at all, from an older vani)
    /// keeps the run-once path.
    func testWithoutARunningDaemonRunOnceIsStarted() async throws {
        let f = try PipelineFixture(self, mode: "refused", lastRun: oldRun,
                                    daemon: #"{"ok":true,"running":false,"reason":"no daemon has announced itself"}"#)
        let m = try model(f)
        await m.refresh()
        XCTAssertFalse(m.daemonRunning)
        await m.startRun(ManualRunOptions(noDigest: true))
        try await eventually("run-once's refusal") { m.runError != nil && m.manualRun == nil }
        XCTAssertTrue(f.calls().contains("run-once --no-digest"), "\(f.calls())")
        XCTAssertFalse(f.calls().contains { $0.hasPrefix("run-request") }, "\(f.calls())")
    }

    /// Triage now only proposes options. It writes nothing and remembers
    /// nothing, and a run that cannot start is not offered a sheet.
    func testProposeRunDoesNotStartOrRemember() async throws {
        let f = try PipelineFixture(self, mode: "refused", lastRun: oldRun)
        let m = try model(f)
        m.proposeRun(.backlog)
        XCTAssertNil(m.runSheet, "no status yet")
        m.openRunNow()
        XCTAssertNil(m.runSheet, "no status yet")
        XCTAssertEqual(m.savedOptions, ManualRunOptions())
        XCTAssertTrue(f.calls().isEmpty)
        await m.refresh()
        m.proposeRun(.backlog)
        XCTAssertEqual(m.runSheet?.source, .triageNow)
        XCTAssertEqual(m.runSheet?.options, .backlog)
        XCTAssertEqual(m.runSheet?.remembers, false)
        XCTAssertEqual(m.runSheet?.banner, ManualRunOptions.triageNowBanner)
        XCTAssertEqual(m.runSheet?.presentation(daemonRunning: m.daemonRunning).footnote,
                       ManualRunOptions.runFootnote(daemonRunning: false))
        XCTAssertEqual(m.savedOptions, ManualRunOptions(), "remembered only when Start Run is pressed")
        XCTAssertTrue(f.calls().allSatisfy { $0.hasPrefix("status") }, "\(f.calls())")
    }

    func testProposeRunIsRefusedWhileARunIsGoing() async throws {
        let f = try PipelineFixture(self, mode: "refused",
                                    lastRun: #"{"ok":true,"state":"running","trigger":"schedule","started_at":"2026-10-20T00:00:00Z"}"#)
        let m = try model(f)
        await m.refresh()
        m.proposeRun(.backlog)
        m.openRunNow()
        XCTAssertNil(m.runSheet)
        XCTAssertTrue(f.calls().allSatisfy { $0.hasPrefix("status") }, "\(f.calls())")
    }

    /// Run Now opens the sheet on the remembered options and keeps a confirmation.
    func testOpenRunNowUsesTheRememberedOptions() async throws {
        let f = try PipelineFixture(self, mode: "refused", lastRun: oldRun)
        let m = try model(f)
        await m.refresh()
        let kept = ManualRunOptions(advance: 12, boards: ["gh:acme"])
        await m.startRun(kept)
        try await eventually("the refusal") { m.runError != nil && m.manualRun == nil }
        XCTAssertEqual(m.savedOptions, kept)
        m.openRunNow()
        XCTAssertEqual(m.runSheet?.source, .runNow)
        XCTAssertEqual(m.runSheet?.options, kept)
        XCTAssertEqual(m.runSheet?.remembers, true)
        XCTAssertNil(m.runSheet?.banner)
        let shown = try XCTUnwrap(m.runSheet).presentation(daemonRunning: m.daemonRunning)
        XCTAssertNil(shown.banner)
        XCTAssertTrue(shown.remembers)
        XCTAssertEqual(shown.footnote, ManualRunOptions.runFootnote(daemonRunning: false))
    }

    /// A Triage now confirmation does not replace remembered Run Now options,
    /// including when vani refuses the run before it records anything.
    func testTriageNowDoesNotReplaceRememberedOptionsWhenVaniRefuses() async throws {
        let f = try PipelineFixture(self, mode: "refused", lastRun: oldRun)
        let m = try model(f)
        await m.refresh()
        let kept = ManualRunOptions(advance: 12, boards: ["gh:acme"])
        await m.startRun(kept)
        try await eventually("the first refusal") { m.runError != nil && m.manualRun == nil }
        await m.startRun(.backlog, remember: false)
        try await eventually("the second refusal") { m.runError != nil && !m.startingRun && m.manualRun == nil }
        XCTAssertEqual(m.savedOptions, kept)
        XCTAssertTrue(f.calls().contains("run-once --no-digest"), "\(f.calls())")
        XCTAssertTrue(f.calls().contains("run-once --advance 12 --board gh:acme"), "\(f.calls())")
    }

    /// The same holds when the daemon makes the run: the request is filed and
    /// the remembered options stay.
    func testTriageNowRequestDoesNotReplaceRememberedOptions() async throws {
        let f = try PipelineFixture(self, mode: "refused", lastRun: oldRun, daemon: daemonUp)
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "vani-pipeline-tests-\(UUID().uuidString)"))
        let kept = ManualRunOptions(advance: 12, boards: ["gh:acme"])
        defaults.set(try JSONEncoder().encode(kept), forKey: PipelineModel.optionsKey)
        let vani = f.vani
        let m = PipelineModel(client: { PipelineClient(vaniBinary: vani, timeout: 10) },
                              launcher: { ManualRunLauncher(runner: DraftRunner(vaniBinary: vani, keys: .noKey, decisionKey: { nil })) },
                              defaults: defaults)
        await m.refresh()
        XCTAssertTrue(m.daemonRunning)
        XCTAssertEqual(m.savedOptions, kept)
        await m.startRun(.backlog, remember: false)
        XCTAssertNil(m.runError)
        XCTAssertTrue(m.waitingForDaemon)
        XCTAssertEqual(m.savedOptions, kept)
        XCTAssertTrue(f.calls().contains("run-request --no-digest --json"), "\(f.calls())")
        XCTAssertFalse(f.calls().contains { $0.contains("--advance") }, "\(f.calls())")
    }

    /// Start pressed before status has arrived says why, and starts nothing.
    func testStartBeforeStatusSaysWhy() async throws {
        let f = try PipelineFixture(self, mode: "refused", lastRun: oldRun)
        let m = try model(f)
        await m.startRun(.backlog)
        XCTAssertEqual(m.runError, "Vani has not reported its status yet.")
        XCTAssertNil(m.manualRun)
        XCTAssertEqual(m.savedOptions, ManualRunOptions())
        XCTAssertTrue(f.calls().isEmpty)
    }

    /// A status with no state folder is named, and vani is not started.
    func testStartWithNoStateFolderSaysWhy() async throws {
        let f = try PipelineFixture(self, mode: "refused", lastRun: oldRun)
        let statusURL = f.dir.appendingPathComponent("status.json")
        let raw = try String(contentsOf: statusURL, encoding: .utf8)
        let emptied = raw.replacingOccurrences(of: "\"state_dir\":\"\(f.dir.path)\"", with: "\"state_dir\":\"\"")
        XCTAssertNotEqual(raw, emptied)
        try Data(emptied.utf8).write(to: statusURL)
        let m = try model(f)
        await m.refresh()
        XCTAssertEqual(m.status?.stateDir, "")
        XCTAssertNil(m.runBlocker)
        await m.startRun(.backlog)
        XCTAssertEqual(m.runError, "Vani has not reported its state folder yet.")
        XCTAssertNil(m.manualRun)
        XCTAssertEqual(m.savedOptions, ManualRunOptions())
        XCTAssertFalse(f.calls().contains { $0.hasPrefix("run-once") || $0.hasPrefix("run-request") }, "\(f.calls())")
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
