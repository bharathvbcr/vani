import Foundation
import XCTest
@testable import VaniApplyKit

private let key = "sk-or-v1-REALVANI-SENTINEL-never-in-a-file"

/// The schedule agent against the real Go `vani daemon` (build/vani), end to end except for
/// launchd and the Keychain: the supervisor starts the daemon with the launch the agent builds,
/// `vani status --json` sees it (keeping the schedule, the model on or off), a second daemon is
/// refused by the ledger lock, and a stop removes the daemon's record. Skipped when build/vani is
/// missing or predates `daemon.model`.
final class ScheduleAgentRealVaniTests: XCTestCase {

    private var vani: URL {
        URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("build/vani")
    }

    private struct World {
        let settings: URL
        let config: String
        let state: String
    }

    private func world() throws -> World {
        guard FileManager.default.isExecutableFile(atPath: vani.path) else {
            throw XCTSkip("no Go vani at \(vani.path); build it to run the contract test")
        }
        let dir = try tempDir(self)
        let config = dir.appendingPathComponent("config.json").path, state = dir.appendingPathComponent("state").path
        let initRun = VaniProcess.run(vani, arguments: ["init"], environment: VaniProcess.environment(configPath: config, stateDir: state),
                                      timeout: 30, maxOutputBytes: 64 * 1024)
        guard initRun.status == 0 else { throw XCTSkip("vani init failed: \(initRun.launchError ?? String(decoding: initRun.stderr, as: UTF8.self))") }
        let settings = dir.appendingPathComponent("settings")
        try DraftSettings(vaniConfig: config, vaniStateDir: state).save(to: settings)
        return World(settings: settings, config: config, state: state)
    }

    private func waitUntil(_ timeout: TimeInterval = 30, _ condition: () -> Bool) -> Bool {
        let end = Date().addingTimeInterval(timeout)
        while Date() < end { if condition() { return true }; Thread.sleep(forTimeInterval: 0.1) }
        return condition()
    }

    private func supervisor(_ w: World, key: String?) -> (ScheduleSupervisor, DispatchSemaphore) {
        let s = ScheduleSupervisor(
            attempt: { [vani = self.vani, w] in
                ScheduleAgent.attempt(settingsDir: w.settings, vaniBinary: vani, keys: .noKey, keychainTimeout: 5, decisionKey: { key })
            },
            fallbackLog: ScheduleAgent.logURL(stateDir: w.state))
        let done = DispatchSemaphore(value: 0)
        Thread { s.run(); done.signal() }.start()
        return (s, done)
    }

    private func status(_ w: World) throws -> PipelineStatus {
        try PipelineClient(vaniBinary: vani, configPath: w.config, stateDir: w.state).status()
    }

    private func announced(_ w: World) -> Bool {
        FileManager.default.fileExists(atPath: w.state + "/runs/daemon.json")
    }

    private func requireModelField(_ s: PipelineStatus) throws {
        guard s.daemon?.model != nil else { throw XCTSkip("build/vani predates daemon.model; rebuild it") }
    }

    func testTheSupervisedDaemonKeepsTheScheduleWithTheKey() throws {
        let w = try world()
        let (s, done) = supervisor(w, key: key)
        XCTAssertTrue(waitUntil { announced(w) }, "the daemon announced itself: \(String(describing: try? String(contentsOfFile: w.state + "/logs/vani.err.log", encoding: .utf8)))")
        let st = try status(w)
        try requireModelField(st)
        XCTAssertEqual(st.daemon?.running, true)
        XCTAssertEqual(st.daemon?.model?.enabled, true)
        XCTAssertEqual(st.schedule.kept, true, "the schedule-not-kept warning is gone once a daemon announces itself")
        XCTAssertEqual(st.issues().filter { $0.title.contains("schedule is not being kept") }.count, 0)
        XCTAssertEqual(ScheduleToggle.make(registration: .enabled, error: nil, daemon: st.daemon, leftAlone: false).tone, .normal)

        // A second daemon (another copy, a shell) is refused by the ledger lock and exits.
        let launch = try XCTUnwrap({ () -> DraftRunner.Launch? in
            if case let .start(l, _, _) = ScheduleAgent.attempt(settingsDir: w.settings, vaniBinary: vani, keys: .noKey, keychainTimeout: 5, decisionKey: { key }) { return l }
            return nil
        }())
        let second = VaniProcess.run(launch.program, arguments: launch.arguments, environment: launch.environment, timeout: 30, maxOutputBytes: 64 * 1024)
        XCTAssertFalse(second.timedOut)
        XCTAssertNotEqual(second.status, 0, "a second daemon must not run beside the first")
        XCTAssertEqual(try status(w).daemon?.pid, st.daemon?.pid, "the first daemon is still the one announced")

        s.stop()
        XCTAssertEqual(done.wait(timeout: .now() + 60), .success)
        XCTAssertFalse(announced(w), "a stop ends the daemon with SIGTERM, which removes daemon.json")
        XCTAssertEqual(try status(w).daemon?.running, false)

        let log = try String(contentsOfFile: w.state + "/logs/vani.err.log", encoding: .utf8)
        XCTAssertTrue(log.contains("starting vani daemon (decision key read from the Keychain)"), log)
        XCTAssertTrue(log.contains("daemon started, daily at"), "the daemon's own output is in the same log")
        for file in ["/logs/vani.err.log", "/runs/daemon.json"] {
            XCTAssertFalse(((try? String(contentsOfFile: w.state + file, encoding: .utf8)) ?? "").contains("SENTINEL"), file)
        }
        XCTAssertEqual(s.starts, 1)
    }

    func testTheSupervisedDaemonWithoutTheKeyRunsAndStatusSaysTheModelIsOff() throws {
        let w = try world()
        let (s, done) = supervisor(w, key: nil)
        XCTAssertTrue(waitUntil { announced(w) })
        let st = try status(w)
        try requireModelField(st)
        XCTAssertEqual(st.daemon?.running, true, "no key does not stop the schedule being kept")
        XCTAssertEqual(st.schedule.kept, true)
        XCTAssertEqual(st.daemon?.model?.enabled, false)
        XCTAssertTrue(st.daemon?.model?.reason?.contains("OPENROUTER_API_KEY is not set") == true, "\(String(describing: st.daemon?.model))")
        let issue = try XCTUnwrap(st.issues().first { $0.title == "The daemon's decision model is off" })
        XCTAssertEqual(issue.severity, .warning)
        let toggle = ScheduleToggle.make(registration: .enabled, error: nil, daemon: st.daemon, leftAlone: false)
        XCTAssertEqual(toggle.tone, .warning)
        XCTAssertTrue(toggle.headline.contains("decision model off"))
        // The text status says it too.
        let text = VaniProcess.run(vani, arguments: ["status"], environment: VaniProcess.environment(configPath: w.config, stateDir: w.state),
                                   timeout: 30, maxOutputBytes: 64 * 1024)
        XCTAssertTrue(String(decoding: text.stdout, as: UTF8.self).contains("daemon: model disabled: OPENROUTER_API_KEY is not set"))

        s.stop()
        XCTAssertEqual(done.wait(timeout: .now() + 60), .success)
        let log = try String(contentsOfFile: w.state + "/logs/vani.err.log", encoding: .utf8)
        XCTAssertTrue(log.contains("no decision key in the Keychain"), log)
        XCTAssertTrue(log.contains("vani: daemon: OPENROUTER_API_KEY is not set"), "the daemon said so itself")
    }
}
