import Foundation
import XCTest
@testable import VaniApplyKit

private let sentinel = "sk-or-v1-SENTINEL-must-reach-only-the-childs-environment"

/// The schedule agent: the daemon's launch (environment, key outcomes), its relaunch policy
/// and supervision (real child processes), its log, and the plist and bundle script that ship
/// it. Nothing registers anything with launchd and nothing reads the real Keychain.
final class ScheduleAgentTests: XCTestCase {

    private func settingsDir(config: String = "", state: String = "", account: String = "") throws -> URL {
        let dir = try tempDir(self)
        try DraftSettings(vaniConfig: config, vaniStateDir: state, anthropicAccount: account).save(to: dir)
        return dir
    }

    private let vani = URL(fileURLWithPath: "/opt/vani-under-test/vani")

    private func attempt(_ dir: URL, keys: DraftRunner.KeySource = .noKey, timeout: TimeInterval = 5,
                         key: @escaping KeyLookup) -> ScheduleAttempt {
        ScheduleAgent.attempt(settingsDir: dir, vaniBinary: vani, keys: keys, keychainTimeout: timeout, decisionKey: key)
    }

    private func launch(_ a: ScheduleAttempt, file: StaticString = #filePath, line: UInt = #line) throws -> (DraftRunner.Launch, URL, String) {
        guard case let .start(l, log, fp) = a else {
            XCTFail("expected a start, got \(a)", file: file, line: line)
            throw XCTSkip("no launch")
        }
        return (l, log, fp)
    }

    // MARK: the environment builder

    func testTheDaemonGetsTheSavedPairAndTheKeyInItsEnvironmentOnly() throws {
        let dir = try settingsDir(config: "/Users/o/cfg/config.json", state: "/Users/o/cfg/state")
        let (l, log, _) = try launch(attempt(dir) { sentinel })
        XCTAssertEqual(l.program, vani)
        XCTAssertEqual(l.arguments, ["daemon"])
        XCTAssertEqual(l.environment["VANI_CONFIG"], "/Users/o/cfg/config.json")
        XCTAssertEqual(l.environment["VANI_STATE_DIR"], "/Users/o/cfg/state")
        XCTAssertEqual(l.environment["OPENROUTER_API_KEY"], sentinel)
        XCTAssertEqual(l.decisionKey, .present)
        // The same minimal environment a manual run gets: nothing of this process's.
        XCTAssertEqual(Set(l.environment.keys), ["PATH", "HOME", "USER", "TMPDIR", "VANI_CONFIG", "VANI_STATE_DIR", "OPENROUTER_API_KEY"])
        XCTAssertEqual(log.path, "/Users/o/cfg/state/logs/vani.err.log", "the log `vani launchd plist` also uses")
        // The same environment ManualRunLauncher builds, key included.
        let manual = DraftSettings(vaniConfig: "/Users/o/cfg/config.json", vaniStateDir: "/Users/o/cfg/state")
            .runner(vaniBinary: vani, keys: .noKey, decisionKey: { sentinel })
        guard case let .launch(m) = manual.prepare(["run-once"], withDecisionKey: true) else { return XCTFail("manual run refused") }
        XCTAssertEqual(m.environment, l.environment)
    }

    func testEmptySettingsPinTheDefaultPairNotNothing() throws {
        let (l, log, _) = try launch(attempt(try settingsDir()) { nil })
        XCTAssertEqual(l.environment["VANI_CONFIG"], VaniProcess.defaultConfigPath)
        XCTAssertEqual(l.environment["VANI_STATE_DIR"], VaniProcess.defaultStateDir)
        XCTAssertEqual(VaniProcess.defaultStateDir, NSHomeDirectory() + "/Library/Application Support/Vani/state", "config.Paths on macOS")
        XCTAssertEqual(log.path, VaniProcess.defaultStateDir + "/logs/vani.err.log")
    }

    func testAMissingKeyStartsTheDaemonWithoutItAndSaysNotFound() throws {
        for lookup in [{ nil }, { "" }, { "  \n" }] as [KeyLookup] {
            let (l, _, _) = try launch(attempt(try settingsDir(), key: lookup))
            XCTAssertNil(l.environment["OPENROUTER_API_KEY"])
            XCTAssertEqual(l.decisionKey, .notFound, "a missing key is a visible outcome, not a quiet no-op")
            XCTAssertTrue(ScheduleAgent.describe(l.decisionKey).contains("model disabled"))
        }
    }

    func testADeniedKeychainReadStartsTheDaemonWithoutTheKeyAndSaysWhy() throws {
        let (l, _, _) = try launch(attempt(try settingsDir()) { throw BankError("Keychain refused access to openrouter-api-key (status -25293)") })
        XCTAssertNil(l.environment["OPENROUTER_API_KEY"])
        guard case let .failed(why) = l.decisionKey else { return XCTFail("outcome \(l.decisionKey)") }
        XCTAssertTrue(why.contains("-25293"))
        XCTAssertTrue(ScheduleAgent.describe(l.decisionKey).contains("model disabled"))
    }

    func testAKeychainPromptNobodyAnswersIsBoundedAndTheDaemonStartsWithoutTheKey() throws {
        let started = Date()
        let (l, _, _) = try launch(attempt(try settingsDir(), timeout: 0.3) {
            Thread.sleep(forTimeInterval: 30)
            return sentinel
        })
        XCTAssertLessThan(Date().timeIntervalSince(started), 3, "the read is abandoned at its bound, not waited out")
        XCTAssertNil(l.environment["OPENROUTER_API_KEY"])
        guard case let .failed(why) = l.decisionKey else { return XCTFail("outcome \(l.decisionKey)") }
        XCTAssertTrue(why.contains("not answered"))
    }

    func testTheKeyIsNeverInTheLogOrTheDescriptions() throws {
        let (l, _, _) = try launch(attempt(try settingsDir()) { sentinel })
        XCTAssertFalse(ScheduleAgent.describe(l.decisionKey).contains("SENTINEL"))
        XCTAssertFalse(ScheduleLog.line(ScheduleAgent.describe(l.decisionKey)).contains("SENTINEL"))
        // A denied read's reason names the service and status, never a value.
        for outcome in [DraftRunner.KeyOutcome.present, .notFound, .notRequested, .failed("x")] {
            XCTAssertFalse(ScheduleAgent.describe(outcome).contains("SENTINEL"))
        }
    }

    func testUnreadableOrRelativeSettingsRefuseInsteadOfStartingAgainstTheWrongPair() throws {
        let broken = try tempDir(self)
        try Data("{not json".utf8).write(to: broken.appendingPathComponent("draft-settings.json"))
        guard case let .cannotStart(why, _) = attempt(broken, key: { sentinel }) else { return XCTFail("started on unreadable settings") }
        XCTAssertTrue(why.contains("unreadable"), why)
        XCTAssertNil(ScheduleAgent.currentFingerprint(settingsDir: broken), "an edit in progress never restarts a running daemon")

        let relative = try tempDir(self)
        try Data(#"{"vaniConfig":"rel/config.json"}"#.utf8).write(to: relative.appendingPathComponent("draft-settings.json"))
        guard case let .cannotStart(why2, _) = attempt(relative, key: { sentinel }) else { return XCTFail("started on a relative path") }
        XCTAssertTrue(why2.contains("absolute"), why2)
    }

    func testAnAnthropicConfigWithoutTheLauncherRefusesRatherThanStartKeyless() throws {
        let dir = try tempDir(self)
        let cfg = dir.appendingPathComponent("config.json")
        try Data(#"{"llm":{"provider":"anthropic"}}"#.utf8).write(to: cfg)
        let settings = try settingsDir(config: cfg.path, state: dir.appendingPathComponent("state").path)
        guard case let .cannotStart(why, log) = attempt(settings, keys: .anthropicAccount(nil), key: { nil }) else {
            return XCTFail("started an anthropic config with no anthropic-account")
        }
        XCTAssertTrue(why.contains("anthropic-account is not installed"), why)
        XCTAssertNotNil(log, "the refusal is written to the daemon log")
    }

    func testTheFingerprintTracksThePairAndTheAccount() throws {
        let a = try settingsDir(config: "/a/config.json", state: "/a/state")
        let b = try settingsDir(config: "/b/config.json", state: "/a/state")
        let c = try settingsDir(config: "/a/config.json", state: "/a/state", account: "work")
        let fa = try XCTUnwrap(ScheduleAgent.currentFingerprint(settingsDir: a))
        XCTAssertNotEqual(fa, ScheduleAgent.currentFingerprint(settingsDir: b))
        XCTAssertNotEqual(fa, ScheduleAgent.currentFingerprint(settingsDir: c))
        XCTAssertEqual(fa, ScheduleAgent.currentFingerprint(settingsDir: try settingsDir(config: "/a/config.json", state: "/a/state")))
    }

    // MARK: relaunch policy

    func testRelaunchWaitDoublesToACapAndNeverSpins() {
        var p = RelaunchPolicy(base: 10, cap: 300, stableAfter: 300)
        XCTAssertEqual((0..<9).map { _ in p.exited(after: 1) }, [10, 20, 40, 80, 160, 300, 300, 300, 300])
        // A crash loop for a day is a bounded number of starts, not a tight loop.
        var loop = RelaunchPolicy()
        var waited = 0.0, starts = 0
        while waited < 86_400 { waited += loop.exited(after: 0); starts += 1 }
        XCTAssertLessThan(starts, 400)
        // Never zero and never past the cap, whatever the history.
        for _ in 0..<10_000 { let w = loop.exited(after: 0); XCTAssertTrue(w >= 10 && w <= 300) }
    }

    func testALongLivedDaemonStartsTheBackoffOver() {
        var p = RelaunchPolicy(base: 10, cap: 300, stableAfter: 300)
        for _ in 0..<8 { _ = p.exited(after: 2) }
        XCTAssertEqual(p.exited(after: 600), 10, "it ran ten minutes: this is a fresh failure")
        XCTAssertEqual(p.exited(after: 2), 20)
        XCTAssertEqual(p.exited(after: 299), 40, "just under stable is still a short run")
    }

    // MARK: log

    func testLogLinesAreOneBoundedLineEach() {
        let line = ScheduleLog.line("a\nb\r\nc " + String(repeating: "x", count: 1000), at: Date(timeIntervalSince1970: 0))
        XCTAssertTrue(line.hasPrefix("1970-01-01T00:00:00Z vani-schedule-agent: a b c "))
        XCTAssertEqual(line.filter { $0 == "\n" }.count, 1)
        XCTAssertLessThan(line.count, 500)
    }

    func testLogAppendsRefusesSymlinksAndRotatesWhenLarge() throws {
        let dir = try tempDir(self)
        let log = dir.appendingPathComponent("logs/vani.err.log")
        let writer = ScheduleLog()
        writer.write("first", to: log)
        writer.write("second", to: log)
        let text = try String(contentsOf: log, encoding: .utf8)
        XCTAssertEqual(text.split(separator: "\n").count, 2)
        XCTAssertEqual(try FileManager.default.attributesOfItem(atPath: log.path)[.posixPermissions] as? Int, 0o600)

        ScheduleLog.rotateIfLarge(log, maxBytes: 5)
        XCTAssertFalse(FileManager.default.fileExists(atPath: log.path))
        XCTAssertEqual(try String(contentsOf: URL(fileURLWithPath: log.path + ".1"), encoding: .utf8), text)

        // A symlink planted at the log is not written through.
        let target = dir.appendingPathComponent("elsewhere")
        try Data("untouched".utf8).write(to: target)
        try FileManager.default.createSymbolicLink(at: log, withDestinationURL: target)
        writer.write("through the link", to: log)
        XCTAssertEqual(try String(contentsOf: target, encoding: .utf8), "untouched")
        ScheduleLog.rotateIfLarge(log, maxBytes: 1)
        XCTAssertNotNil(try? FileManager.default.destinationOfSymbolicLink(atPath: log.path), "a symlink is not rotated either")
    }

    func testAppendKeepsEarlierOutputAndTheDefaultStillTruncates() throws {
        let dir = try tempDir(self)
        let log = dir.appendingPathComponent("l.log")
        func run(_ say: String, append: Bool) throws {
            let done = expectation(description: say)
            _ = try VaniProcess.launch(URL(fileURLWithPath: "/bin/echo"), arguments: [say], environment: [:], log: log, append: append) { _ in done.fulfill() }
            wait(for: [done], timeout: 10)
        }
        try run("one", append: true)
        try run("two", append: true)
        XCTAssertEqual(try String(contentsOf: log, encoding: .utf8), "one\ntwo\n")
        try run("three", append: false)
        XCTAssertEqual(try String(contentsOf: log, encoding: .utf8), "three\n")
    }

    // MARK: supervisor (real children)

    private struct Rig {
        let dir: URL
        let log: URL
        func script(_ body: String) throws -> URL {
            let url = dir.appendingPathComponent("daemon-\(UUID().uuidString).sh")
            try Data("#!/bin/sh\n\(body)\n".utf8).write(to: url)
            try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
            return url
        }
        func start(_ program: URL, env: [String: String] = [:], key: DraftRunner.KeyOutcome = .present, fingerprint: String = "fp") -> ScheduleAttempt {
            .start(DraftRunner.Launch(program: program, arguments: ["daemon"], environment: env, decisionKey: key), log: log, fingerprint: fingerprint)
        }
        var logText: String { (try? String(contentsOf: log, encoding: .utf8)) ?? "" }
    }

    private func rig() throws -> Rig {
        let dir = try tempDir(self)
        return Rig(dir: dir, log: dir.appendingPathComponent("logs/vani.err.log"))
    }

    private func supervise(_ rig: Rig, attempt: @escaping @Sendable () -> ScheduleAttempt,
                           fingerprint: @escaping @Sendable () -> String? = { nil },
                           keyExists: @escaping @Sendable () -> Bool? = { nil },
                           policy: RelaunchPolicy = RelaunchPolicy(base: 0.05, cap: 0.1, stableAfter: 60),
                           watch: TimeInterval = 0.1, grace: TimeInterval = 5) -> (ScheduleSupervisor, DispatchSemaphore) {
        let s = ScheduleSupervisor(attempt: attempt, currentFingerprint: fingerprint, keyExists: keyExists, policy: policy,
                                   fallbackLog: rig.log, watchInterval: watch, stopGrace: grace)
        let done = DispatchSemaphore(value: 0)
        Thread { s.run(); done.signal() }.start()
        return (s, done)
    }

    private func waitUntil(_ timeout: TimeInterval = 10, _ condition: () -> Bool) -> Bool {
        let end = Date().addingTimeInterval(timeout)
        while Date() < end { if condition() { return true }; Thread.sleep(forTimeInterval: 0.02) }
        return condition()
    }

    func testADaemonThatKeepsCrashingIsStartedAgainWithABoundedWaitAndNeverGivenUpOn() throws {
        let r = try rig()
        // The second daemon the ledger lock refuses looks the same: it exits at once, non-zero.
        let program = try r.script("echo 'vani: ledger: locked by another process'; exit 3")
        let (s, done) = supervise(r, attempt: { r.start(program) })
        XCTAssertTrue(waitUntil { s.starts >= 4 }, "it kept starting it: \(r.logText)")
        s.stop()
        XCTAssertEqual(done.wait(timeout: .now() + 10), .success)
        XCTAssertLessThan(s.starts, 40, "bounded by the wait, not a tight loop")
        XCTAssertTrue(r.logText.contains("daemon ended (status 3)"))
        XCTAssertTrue(r.logText.contains("starting it again in"))
        XCTAssertTrue(r.logText.contains("vani: ledger: locked"), "the daemon's own output is in the same log, appended not truncated")
    }

    func testAStartThatCannotBeBuiltIsRetriedAndLoggedNotSpun() throws {
        let r = try rig()
        let calls = Counter()
        let (s, done) = supervise(r, attempt: { calls.bump(); return .cannotStart("settings are unreadable", log: r.log) })
        XCTAssertTrue(waitUntil { calls.value >= 3 })
        s.stop()
        XCTAssertEqual(done.wait(timeout: .now() + 10), .success)
        XCTAssertEqual(s.starts, 0)
        XCTAssertLessThan(calls.value, 40)
        XCTAssertTrue(r.logText.contains("cannot start the daemon: settings are unreadable"))
    }

    func testAMissingBinaryIsLoggedAndRetried() throws {
        let r = try rig()
        let (s, done) = supervise(r, attempt: { r.start(URL(fileURLWithPath: "/nonexistent/vani")) })
        XCTAssertTrue(waitUntil { r.logText.contains("trying again in") })
        s.stop()
        XCTAssertEqual(done.wait(timeout: .now() + 10), .success)
        XCTAssertTrue(r.logText.contains("the vani binary is missing"))
    }

    func testTheKeyReachesTheChildAndNothingElseWritesIt() throws {
        let r = try rig()
        let marker = r.dir.appendingPathComponent("marker")
        let program = try r.script("printf '%s' \"$OPENROUTER_API_KEY\" > '\(marker.path)'; exec sleep 30")
        let (s, done) = supervise(r, attempt: { r.start(program, env: ["OPENROUTER_API_KEY": sentinel, "PATH": "/usr/bin:/bin"]) })
        XCTAssertTrue(waitUntil { (try? String(contentsOf: marker, encoding: .utf8)) == sentinel }, "the child holds the key")
        s.stop()
        XCTAssertEqual(done.wait(timeout: .now() + 10), .success)
        XCTAssertFalse(r.logText.contains("SENTINEL"), "the agent's own log lines never hold it")
        XCTAssertTrue(r.logText.contains("decision key read from the Keychain"))
    }

    func testStopEndsTheDaemonWithSigtermAndWaitsForIt() throws {
        let r = try rig()
        let pidFile = r.dir.appendingPathComponent("pid"), cleaned = r.dir.appendingPathComponent("cleaned")
        // A daemon that cleans up on SIGTERM (it removes daemon.json) takes a moment to do so.
        let program = try r.script("echo $$ > '\(pidFile.path)'; trap 'sleep 0.3; echo done > \"\(cleaned.path)\"; exit 0' TERM; while :; do sleep 0.05; done")
        let (s, done) = supervise(r, attempt: { r.start(program) })
        XCTAssertTrue(waitUntil { FileManager.default.fileExists(atPath: pidFile.path) })
        let started = Date()
        s.stop()
        XCTAssertEqual(done.wait(timeout: .now() + 10), .success)
        XCTAssertGreaterThan(Date().timeIntervalSince(started), 0.25, "it waited for the daemon's cleanup")
        XCTAssertTrue(FileManager.default.fileExists(atPath: cleaned.path), "the daemon ran its SIGTERM cleanup before the agent returned")
        let pid = try XCTUnwrap(Int32(String(contentsOf: pidFile, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)))
        XCTAssertTrue(kill(pid, 0) != 0 && errno == ESRCH, "no daemon is left behind")
        XCTAssertEqual(s.starts, 1, "a stop is not followed by a restart")
    }

    func testADaemonThatIgnoresSigtermIsKilledAfterTheGrace() throws {
        let r = try rig()
        let pidFile = r.dir.appendingPathComponent("pid")
        let program = try r.script("echo $$ > '\(pidFile.path)'; trap '' TERM; while :; do sleep 0.05; done")
        let (s, done) = supervise(r, attempt: { r.start(program) }, grace: 0.4)
        XCTAssertTrue(waitUntil { FileManager.default.fileExists(atPath: pidFile.path) })
        s.stop()
        XCTAssertEqual(done.wait(timeout: .now() + 10), .success)
        let pid = try XCTUnwrap(Int32(String(contentsOf: pidFile, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)))
        XCTAssertTrue(waitUntil(3) { kill(pid, 0) != 0 && errno == ESRCH }, "SIGKILL after the grace")
    }

    func testAStopDuringTheBackoffReturnsAtOnce() throws {
        let r = try rig()
        let program = try r.script("exit 1")
        let slow = RelaunchPolicy(base: 600, cap: 600, stableAfter: 3600)
        let (s, done) = supervise(r, attempt: { r.start(program) }, policy: slow)
        XCTAssertTrue(waitUntil { r.logText.contains("starting it again in 600 s") })
        let t = Date()
        s.stop()
        XCTAssertEqual(done.wait(timeout: .now() + 5), .success)
        XCTAssertLessThan(Date().timeIntervalSince(t), 2)
    }

    func testChangedSettingsRestartTheDaemonOnTheNewPair() throws {
        let r = try rig()
        let program = try r.script("exec sleep 30")
        let saved = Box("old")
        let (s, done) = supervise(r, attempt: { r.start(program, fingerprint: saved.get()) }, fingerprint: { saved.get() })
        XCTAssertTrue(waitUntil { s.starts >= 1 })
        Thread.sleep(forTimeInterval: 0.4)
        XCTAssertEqual(s.starts, 1, "unchanged settings: left running")
        saved.set("new") // the daemon was built from "old"
        XCTAssertTrue(waitUntil { r.logText.contains("the app's Vani settings changed") }, r.logText)
        XCTAssertTrue(waitUntil { s.starts >= 2 }, "it started again from the new settings: \(r.logText)")
        s.stop()
        XCTAssertEqual(done.wait(timeout: .now() + 10), .success)
    }

    func testUnreadableSettingsDoNotRestartARunningDaemon() throws {
        let r = try rig()
        let program = try r.script("exec sleep 30")
        let (s, done) = supervise(r, attempt: { r.start(program) }, fingerprint: { nil })
        XCTAssertTrue(waitUntil { s.starts >= 1 })
        Thread.sleep(forTimeInterval: 0.5)
        XCTAssertEqual(s.starts, 1)
        s.stop()
        XCTAssertEqual(done.wait(timeout: .now() + 10), .success)
    }

    func testAKeyAddedAfterAKeylessStartRestartsTheDaemonOnce() throws {
        let r = try rig()
        let program = try r.script("exec sleep 30")
        let present = Box("no")
        let (s, done) = supervise(r, attempt: { r.start(program, key: present.get() == "yes" ? .present : .notFound) },
                                  keyExists: { present.get() == "yes" })
        XCTAssertTrue(waitUntil { s.starts >= 1 })
        Thread.sleep(forTimeInterval: 0.4)
        XCTAssertEqual(s.starts, 1, "no key yet: left running")
        XCTAssertTrue(r.logText.contains("no decision key in the Keychain"))
        present.set("yes")
        XCTAssertTrue(waitUntil { s.starts >= 2 }, r.logText)
        Thread.sleep(forTimeInterval: 0.4)
        XCTAssertEqual(s.starts, 2, "started with the key: not restarted again")
        XCTAssertTrue(r.logText.contains("the decision key is now in the Keychain"))
        s.stop()
        XCTAssertEqual(done.wait(timeout: .now() + 10), .success)
    }

    func testAKeyThatFailedToReadIsNotRetriedBehindTheOwnersBack() throws {
        // A refused or unanswered read would prompt again: only the owner's toggle retries it.
        let r = try rig()
        let program = try r.script("exec sleep 30")
        let (s, done) = supervise(r, attempt: { r.start(program, key: .failed("Keychain refused")) }, keyExists: { true })
        XCTAssertTrue(waitUntil { s.starts >= 1 })
        Thread.sleep(forTimeInterval: 0.5)
        XCTAssertEqual(s.starts, 1)
        s.stop()
        XCTAssertEqual(done.wait(timeout: .now() + 10), .success)
    }

    // MARK: shipped files

    private var macos: URL {
        URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    }

    func testThePlistRegistersAHelperThatListensOnNothingAndHoldsNoKey() throws {
        let url = macos.appendingPathComponent("Support/LaunchAgents/\(ScheduleAgentConstants.plistName)")
        let data = try Data(contentsOf: url)
        let plist = try XCTUnwrap(PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any])
        XCTAssertEqual(plist["Label"] as? String, ScheduleAgentConstants.label)
        XCTAssertEqual(plist["BundleProgram"] as? String, "Contents/Helpers/\(ScheduleAgentConstants.helperName)")
        XCTAssertEqual(plist["RunAtLoad"] as? Bool, true)
        XCTAssertEqual((plist["KeepAlive"] as? [String: Any])?["SuccessfulExit"] as? Bool, false,
                       "restart the helper after a crash; a clean exit (turning the switch off) stays down")
        XCTAssertGreaterThanOrEqual(plist["ThrottleInterval"] as? Int ?? 0, 10)
        XCTAssertGreaterThan(Double(plist["ExitTimeOut"] as? Int ?? 0), ScheduleAgentConstants.stopGrace,
                             "launchd must not kill the helper before the daemon's grace is up")
        // Nothing widens access: no listener of any kind, and no key or environment in the job.
        for forbidden in ["MachServices", "Sockets", "EnvironmentVariables", "ProgramArguments", "StandardOutPath", "StandardErrorPath", "WatchPaths"] {
            XCTAssertNil(plist[forbidden], "\(forbidden) must not be in the schedule agent's plist")
        }
        let text = try XCTUnwrap(String(data: data, encoding: .utf8))
        for secret in ["OPENROUTER", "ANTHROPIC", "sk-"] { XCTAssertFalse(text.contains(secret), secret) }
    }

    func testTheBundleScriptCopiesAndSignsTheHelperAndItsPlist() throws {
        let script = try String(contentsOf: macos.appendingPathComponent("scripts/bundle.sh"), encoding: .utf8)
        for want in ["cp \"$BIN/vani-schedule-agent\" \"$APP/Contents/Helpers/vani-schedule-agent\"",
                     "Support/LaunchAgents/dev.vbcr.vani.schedule.agent.plist\" \"$APP/Contents/Library/LaunchAgents/\"",
                     "--identifier dev.vbcr.vani.schedule.agent \"$APP/Contents/Helpers/vani-schedule-agent\""] {
            XCTAssertTrue(script.contains(want), "bundle.sh lacks: \(want)")
        }
        let package = try String(contentsOf: macos.appendingPathComponent("Package.swift"), encoding: .utf8)
        XCTAssertTrue(package.contains("vani-schedule-agent"))
        // The helper reads the settings in the App Group store: it is signed with the group entitlement only.
        let ent = try String(contentsOf: macos.appendingPathComponent("Support/Group.entitlements"), encoding: .utf8)
        XCTAssertTrue(ent.contains("com.apple.security.application-groups"))
        XCTAssertFalse(ent.contains("network.server"), "no listener entitlement")
    }

    func testTheHelperIsTheOnlyPlaceTheDaemonIsStartedFromAndItReadsNoSocket() throws {
        let main = try String(contentsOf: macos.appendingPathComponent("Sources/vani-schedule-agent/main.swift"), encoding: .utf8)
        for forbidden in ["NSXPCListener", "machServiceName", "socket(", "bind(", "listen(", "URLSession"] {
            XCTAssertFalse(main.contains(forbidden), "the schedule agent must not listen or connect: \(forbidden)")
        }
    }
}

private final class Counter: @unchecked Sendable {
    private let lock = NSLock()
    private var n = 0
    func bump() { lock.lock(); n += 1; lock.unlock() }
    var value: Int { lock.lock(); defer { lock.unlock() }; return n }
}

private final class Box: @unchecked Sendable {
    private let lock = NSLock()
    private var v: String
    init(_ v: String) { self.v = v }
    func get() -> String { lock.lock(); defer { lock.unlock() }; return v }
    func set(_ new: String) { lock.lock(); v = new; lock.unlock() }
}
