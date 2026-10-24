import Foundation

// The schedule agent keeps Vani's daily schedule: a second login item (beside the
// on-demand vani-apply-agent) that runs `vani daemon`, the one thing that fires
// `schedule.daily_at`, as its child. The Go daemon already schedules, announces itself
// in runs/daemon.json and takes manual runs through `vani run-request`; this only
// starts it, with the environment a manual run gets (ManualRunLauncher), and keeps it
// up.
//
// The decision model's key is read from the login Keychain by the same code path as a
// manual run (DraftRunner.prepare(withDecisionKey:), bounded by `keychainTimeout`) and
// is passed only in the child's environment: never in the launchd plist, never in a
// file, never in the agent's log. A missing, refused or unanswered read does not stop
// the daemon (listing, scoring and the digest need no model); the daemon starts
// without the key and says so in daemon.json, which status and the Pipeline tab show.

public enum ScheduleAgentConstants {
    /// The plist the app registers (SMAppService.agent(plistName:)); in
    /// Contents/Library/LaunchAgents, and what macos/Support/LaunchAgents holds.
    public static let plistName = "dev.vbcr.vani.schedule.agent.plist"
    public static let label = "dev.vbcr.vani.schedule.agent"
    /// The helper in Contents/Helpers that the plist's BundleProgram names.
    public static let helperName = "vani-schedule-agent"
    /// How long a daemon gets to end a run on SIGTERM before SIGKILL. The plist's
    /// ExitTimeOut is longer, so launchd never kills the helper first.
    public static let stopGrace: TimeInterval = 45
    /// The Keychain read's bound. A first read can wait on an "allow access" prompt nobody
    /// answers; the daemon then starts without the key rather than not at all.
    public static let keychainTimeout: TimeInterval = 30
    /// `vani daemon` exits with this status (sysexits EX_TEMPFAIL) when a
    /// process-lifetime field changed: the schedule, or devprism.binary /
    /// devprism.tools_hash. cmd/vani's exitConfigRestart is the same number.
    /// A life under `configRestartMinLife` is a crash loop and uses the normal
    /// backoff instead of starting again at once.
    public static let configRestartStatus: Int32 = 75
    public static let configRestartMinLife: TimeInterval = 1
    /// What the helper logs when it starts the daemon again for that exit.
    /// A fixed phrase: no config body and no key.
    public static let configRestartNote = "daemon asked to restart so a schedule or devprism change applies"
    /// Each settings read and key-existence check on the once-a-minute watch.
    /// Two of them, then stopGrace and a 2s reap, stay inside the signal
    /// handler's wait (stopGrace + 10) and launchd's ExitTimeOut (60).
    public static let watchBound: TimeInterval = 2
    /// Logged when the App Group store cannot be resolved. A fixed sentence:
    /// no path and no account.
    public static let storeFailureLine = "the apply store is unavailable and no writable store directory could be resolved"
}

/// How long to wait before starting the daemon again after it ends. It never gives up
/// (the schedule must be kept), and never spins: the wait doubles from `base` to `cap`
/// while the daemon keeps ending soon after it starts, and starts over once one has lived
/// `stableAfter`. A second daemon refused by the ledger lock exits at once and is waited out
/// the same way, so it takes over within `cap` of the first one ending.
public struct RelaunchPolicy: Sendable, Equatable {
    public var base: TimeInterval
    public var cap: TimeInterval
    public var stableAfter: TimeInterval
    public private(set) var shortRuns = 0

    public init(base: TimeInterval = 10, cap: TimeInterval = 300, stableAfter: TimeInterval = 300) {
        self.base = base
        self.cap = cap
        self.stableAfter = stableAfter
    }

    /// The wait before the next start, given how long the daemon that just ended (or failed to
    /// start: 0) had been up.
    public mutating func exited(after lifetime: TimeInterval) -> TimeInterval {
        if lifetime >= stableAfter { shortRuns = 0 }
        let wait = min(cap, base * pow(2, Double(min(shortRuns, 30))))
        shortRuns += 1
        return wait
    }
}

/// What one start of the daemon needs.
public enum ScheduleAttempt: Sendable {
    /// Start `launch`, its output appended to `log`. `fingerprint` names the settings it was
    /// built from, to tell when they change.
    case start(DraftRunner.Launch, log: URL, fingerprint: String)
    /// It cannot start, and why (a sentence for the log). `log` is where to write it, when known.
    case cannotStart(String, log: URL?)
}

public enum ScheduleAgent {
    /// Whether this bundle can register the schedule agent. `.absent` is no plist.
    /// `.unusable` is a file that must not be registered; the string is a fixed sentence
    /// and never the file's bytes.
    public enum BundleCopy: Equatable, Sendable {
        case ready
        case absent
        case unusable(String)
    }

    /// Above this, the plist is refused unread past the cap. The shipped file is about 1 KiB.
    public static let plistByteCap = 64 * 1024

    /// What ServiceManagement would be asked to register. A link is followed, and the
    /// resolved path has to stay inside `bundle`. The plist has to be this agent (the
    /// label and the helper path), with no listener and no environment, and the helper
    /// has to be an executable file in the bundle.
    public static func inspect(bundle: URL) -> BundleCopy {
        switch BundleMember.place(bundle: bundle, relative: "Contents/Library/LaunchAgents/\(ScheduleAgentConstants.plistName)") {
        case .absent: return .absent
        case .outside: return .unusable("The schedule agent plist is outside this copy of the app.")
        case .notFile: return .unusable("The schedule agent plist is not a file.")
        case let .file(url): return inspectPlist(url, bundle: bundle)
        }
    }

    private static let forbiddenPlistKeys = [
        "EnvironmentVariables", "MachServices", "Program", "ProgramArguments", "Sockets",
        "StandardErrorPath", "StandardOutPath", "UserName", "WatchPaths", "inetdCompatibility",
    ]

    private static func inspectPlist(_ url: URL, bundle: URL) -> BundleCopy {
        let data: Data
        switch BundleMember.read(file: url, bundle: bundle, maxBytes: plistByteCap) {
        case let .data(bytes): data = bytes
        case .oversized: return .unusable("The schedule agent plist is larger than 64 KiB.")
        case .outside: return .unusable("The schedule agent plist is outside this copy of the app.")
        case .unreadable: return .unusable("The schedule agent plist could not be read.")
        }
        if data.isEmpty { return .unusable("The schedule agent plist is empty.") }
        guard let object = try? PropertyListSerialization.propertyList(from: data, format: nil) else {
            return .unusable("The schedule agent plist is not a property list.")
        }
        guard let dict = object as? [String: Any] else {
            return .unusable("The schedule agent plist is not a dictionary.")
        }
        if let key = forbiddenPlistKeys.first(where: { dict[$0] != nil }) {
            return .unusable("The schedule agent plist contains \(key), so it was not registered.")
        }
        guard (dict["Label"] as? String) == ScheduleAgentConstants.label else {
            return .unusable("The schedule agent plist is not the schedule agent.")
        }
        guard (dict["BundleProgram"] as? String) == "Contents/Helpers/\(ScheduleAgentConstants.helperName)" else {
            return .unusable("The schedule agent plist does not name its helper.")
        }
        switch BundleMember.place(bundle: bundle, relative: "Contents/Helpers/\(ScheduleAgentConstants.helperName)") {
        case .absent: return .unusable("The schedule agent helper is not in this copy of the app.")
        case .outside: return .unusable("The schedule agent helper is outside this copy of the app.")
        case .notFile: return .unusable("The schedule agent helper is not a file.")
        case let .file(helper):
            switch BundleMember.executable(helper, bundle: bundle) {
            case .yes: return .ready
            case .outside: return .unusable("The schedule agent helper is outside this copy of the app.")
            case .no: return .unusable("The schedule agent helper cannot be run.")
            }
        }
    }

    /// The config and state folder the app resolved: its saved settings, else Vani's own
    /// defaults. Both are pinned in the daemon's environment, so the daemon never resolves
    /// them from a launchd environment of its own.
    public struct Paths: Equatable, Sendable {
        public var configPath: String
        public var stateDir: String
    }

    public static func resolve(_ settings: DraftSettings) -> Paths {
        Paths(configPath: settings.vaniConfig.isEmpty ? VaniProcess.defaultConfigPath : settings.vaniConfig,
              stateDir: settings.vaniStateDir.isEmpty ? VaniProcess.defaultStateDir : settings.vaniStateDir)
    }

    /// `<state dir>/logs/vani.err.log`: where `vani launchd plist` sends a daemon's output, so
    /// the log the Pipeline tab names is the same either way.
    public static func logURL(stateDir: String) -> URL {
        URL(fileURLWithPath: stateDir).appendingPathComponent("logs/vani.err.log")
    }

    /// Writes the fixed store-failure line before the helper exits 1. launchd
    /// discards the helper's stderr, so this is the line that says why it is
    /// exiting. The error is not interpolated: it can carry a path.
    public static func reportStoreFailure(to url: URL, log: ScheduleLog = ScheduleLog()) {
        log.write(ScheduleAgentConstants.storeFailureLine, to: url)
    }

    static func fingerprint(_ settings: DraftSettings, _ paths: Paths) -> String {
        "\(paths.configPath)\n\(paths.stateDir)\n\(settings.anthropicAccount)"
    }

    /// The fingerprint of the settings saved now; nil when they cannot be read, so an edit in
    /// progress never stops a daemon that is running.
    public static func currentFingerprint(settingsDir: URL) -> String? {
        guard let s = try? DraftSettings.load(from: settingsDir).validated() else { return nil }
        return fingerprint(s, resolve(s))
    }

    /// The start of the daemon for the settings saved in `settingsDir`: `vani daemon` with the
    /// environment a manual run gets, the decision key included when the Keychain gives one.
    /// It may wait on a Keychain prompt, up to `keychainTimeout`.
    public static func attempt(settingsDir: URL, vaniBinary: URL,
                               keys: DraftRunner.KeySource = .anthropicAccount(AnthropicAccount.locate()),
                               keychainTimeout: TimeInterval = ScheduleAgentConstants.keychainTimeout,
                               decisionKey: @escaping KeyLookup = { try Keychain.genericPassword(service: Keychain.openRouterService) }) -> ScheduleAttempt {
        let settings: DraftSettings
        do { settings = try DraftSettings.load(from: settingsDir).validated() } catch {
            return .cannotStart(String(describing: error), log: nil)
        }
        let paths = resolve(settings)
        var pinned = settings
        pinned.vaniConfig = paths.configPath
        pinned.vaniStateDir = paths.stateDir
        let runner = pinned.runner(vaniBinary: vaniBinary, keys: keys, keychainTimeout: keychainTimeout, decisionKey: decisionKey)
        let log = logURL(stateDir: paths.stateDir)
        switch runner.prepare(["daemon"], withDecisionKey: true) {
        case let .refused(reply):
            return .cannotStart(reply.error ?? "vani could not be started", log: log)
        case let .launch(launch):
            return .start(launch, log: log, fingerprint: fingerprint(settings, paths))
        }
    }

    /// What the log says about the key: a fixed phrase, never a value.
    public static func describe(_ key: DraftRunner.KeyOutcome) -> String {
        switch key {
        case .present: "decision key read from the Keychain"
        case .notRequested: "decision key not requested"
        case .notFound: "no decision key in the Keychain (\(Keychain.openRouterService)); the daemon runs with the model disabled"
        case let .failed(why): "decision key not read (\(why)); the daemon runs with the model disabled"
        }
    }
}

/// Lines in the daemon's log from the helper itself: timestamped, one line, bounded, and
/// built from fixed phrases and exit statuses, so no key can be in one.
public struct ScheduleLog: Sendable {
    public static let maxLineCharacters = 400
    /// A log past this is moved to `<name>.1` (replacing the last) before a start.
    public static let maxBytes = 4 << 20

    public init() {}

    public static func line(_ message: String, at date: Date = Date()) -> String {
        let one = message.split(whereSeparator: \.isNewline).joined(separator: " ")
        let bounded = one.count > maxLineCharacters ? String(one.prefix(maxLineCharacters)) + "…" : one
        return "\(ISO8601DateFormatter().string(from: date)) vani-schedule-agent: \(bounded)\n"
    }

    /// Appends one line to `url` (created 0600; a symlink is refused). A log that cannot be
    /// written is reported on stderr, which launchd discards: the agent still runs.
    public func write(_ message: String, to url: URL) {
        do {
            try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true,
                                                    attributes: [.posixPermissions: 0o700])
            let fd = open(url.path, O_WRONLY | O_CREAT | O_APPEND | O_NOFOLLOW | O_CLOEXEC, 0o600)
            guard fd >= 0 else { throw BankError(String(cString: strerror(errno))) }
            let handle = FileHandle(fileDescriptor: fd, closeOnDealloc: true)
            try handle.write(contentsOf: Data(Self.line(message).utf8))
            try handle.close()
        } catch {
            FileHandle.standardError.write(Data("vani-schedule-agent: cannot write \(url.path): \(error)\n".utf8))
        }
    }

    /// Moves a log larger than `maxBytes` aside. A long-lived daemon's own output is some KB a
    /// day; this keeps a crash loop from filling the disk.
    public static func rotateIfLarge(_ url: URL, maxBytes: Int = ScheduleLog.maxBytes) {
        var st = stat()
        guard lstat(url.path, &st) == 0, (st.st_mode & S_IFMT) == S_IFREG, Int(st.st_size) > maxBytes else { return }
        _ = rename(url.path, url.path + ".1")
    }
}

/// Runs the daemon as a child and starts it again when it ends, until `stop()`.
///
/// One thread runs `run()`; `stop()` is safe from any other (a signal handler's queue). A stop
/// SIGTERMs the running daemon at once, even while a watch check is in flight, then waits up to
/// `stopGrace` and SIGKILLs it.
public final class ScheduleSupervisor: @unchecked Sendable {
    private let attempt: @Sendable () -> ScheduleAttempt
    private let currentFingerprint: @Sendable () -> String?
    private let keyExists: @Sendable () -> Bool?
    private let log: ScheduleLog
    private let fallbackLog: URL
    private let watchInterval: TimeInterval
    private let stopGrace: TimeInterval
    private let watchBound: TimeInterval
    private let clock: @Sendable () -> Date
    private var policy: RelaunchPolicy

    private let cond = NSCondition()
    private var stopped = false
    private var childExited = false
    private var childStatus: Int32 = 0
    /// The daemon `supervise` is watching, if one has a pid. `stop` signals it
    /// without waiting for the watch thread to leave a Keychain or file read.
    private var child: LaunchedJob?
    /// How many times the daemon has been started (tests and the log).
    public private(set) var starts = 0

    /// - Parameters:
    ///   - attempt: builds the next start; called before each one, so a key added since is read.
    ///   - currentFingerprint: the saved settings' fingerprint, nil when unreadable; a daemon
    ///     started from other settings is restarted.
    ///   - keyExists: whether the Keychain holds the decision key (never reads it); a daemon
    ///     started without one because there was none is restarted when it appears.
    ///   - fallbackLog: where to write when the attempt names no log.
    public init(attempt: @escaping @Sendable () -> ScheduleAttempt,
                currentFingerprint: @escaping @Sendable () -> String? = { nil },
                keyExists: @escaping @Sendable () -> Bool? = { nil },
                policy: RelaunchPolicy = RelaunchPolicy(),
                log: ScheduleLog = ScheduleLog(),
                fallbackLog: URL,
                watchInterval: TimeInterval = 60,
                stopGrace: TimeInterval = ScheduleAgentConstants.stopGrace,
                watchBound: TimeInterval = ScheduleAgentConstants.watchBound,
                clock: @escaping @Sendable () -> Date = { Date() }) {
        self.attempt = attempt
        self.currentFingerprint = currentFingerprint
        self.keyExists = keyExists
        self.policy = policy
        self.log = log
        self.fallbackLog = fallbackLog
        self.watchInterval = watchInterval
        self.stopGrace = stopGrace
        self.watchBound = watchBound
        self.clock = clock
    }

    public var isStopped: Bool { cond.lock(); defer { cond.unlock() }; return stopped }

    /// Ends the daemon and makes `run()` return. Returns at once. The running child is SIGTERMed
    /// here, not when the watch thread next notices, so a stuck settings or Keychain check cannot
    /// keep the daemon up. `run()` returns once the child is gone (within `stopGrace` plus a moment).
    /// Safe to call twice.
    public func stop() {
        cond.lock()
        stopped = true
        let job = child
        cond.broadcast()
        cond.unlock()
        job?.stop()
    }

    /// SIGKILL the running child. The signal handler calls this when `run()` has not returned
    /// before its wait ends. Safe to call twice, and when no child is running.
    public func forceStopChild() {
        cond.lock()
        let job = child
        cond.unlock()
        job?.forceStop()
    }

    /// Blocks, starting the daemon and starting it again, until `stop()`.
    public func run() {
        while !isStopped {
            let wait: TimeInterval
            switch attempt() {
            case let .cannotStart(why, logURL):
                note("cannot start the daemon: \(why)", logURL)
                wait = policy.exited(after: 0)
                note("trying again in \(Int(wait)) s", logURL)
            case let .start(launch, logURL, fingerprint):
                if isStopped { return } // a stop that came during a Keychain wait starts nothing
                wait = supervise(launch, log: logURL, fingerprint: fingerprint)
            }
            pause(wait)
        }
    }

    /// One daemon, start to end; returns how long to wait before the next.
    private func supervise(_ launch: DraftRunner.Launch, log logURL: URL, fingerprint: String) -> TimeInterval {
        ScheduleLog.rotateIfLarge(logURL)
        note("starting vani daemon (\(ScheduleAgent.describe(launch.decisionKey)))", logURL)
        cond.lock(); childExited = false; childStatus = 0; cond.unlock()
        let started = clock()
        let job: LaunchedJob
        do {
            job = try VaniProcess.launch(launch.program, arguments: launch.arguments, environment: launch.environment,
                                         log: logURL, append: true) { [self] status in
                cond.lock()
                childExited = true
                childStatus = status
                cond.broadcast()
                cond.unlock()
            }
        } catch {
            note("cannot start the daemon: \(error)", logURL)
            let wait = policy.exited(after: 0)
            note("trying again in \(Int(wait)) s", logURL)
            return wait
        }
        // Visible to stop() before the watch loop, which drops the lock while it reads
        // settings and the Keychain.
        cond.lock()
        child = job
        cond.unlock()
        starts += 1
        defer {
            cond.lock()
            child = nil
            cond.unlock()
        }
        var reason: String?
        cond.lock()
        var checkAt = Date().addingTimeInterval(watchInterval)
        while !childExited && !stopped && reason == nil {
            if !cond.wait(until: checkAt) {
                cond.unlock()
                reason = restartReason(launch.decisionKey, fingerprint)
                cond.lock()
                checkAt = Date().addingTimeInterval(watchInterval)
            }
        }
        let alreadyGone = childExited
        cond.unlock()
        if !alreadyGone {
            // Signal before any log write: a stuck log must not leave the child running.
            end(job)
            note(isStopped ? "stopping the daemon" : "restarting the daemon: \(reason ?? "")", logURL)
        }
        cond.lock(); let status = childStatus; cond.unlock()
        let lived = clock().timeIntervalSince(started)
        if isStopped {
            note("daemon ended (status \(status)) after \(Int(lived)) s; the agent is stopping", logURL)
            return 0
        }
        // Exit 75 is the daemon asking to be replaced so a schedule or DevPrism
        // change applies. One restart, immediately, once it has actually been up.
        // An instant 75 is the crash-loop backoff, or a bug would spin.
        if status == ScheduleAgentConstants.configRestartStatus && lived >= ScheduleAgentConstants.configRestartMinLife {
            note(ScheduleAgentConstants.configRestartNote, logURL)
            return 0
        }
        let wait = policy.exited(after: lived)
        note("daemon ended (status \(status)) after \(Int(lived)) s; starting it again in \(Int(wait)) s", logURL)
        return wait
    }

    /// Why the running daemon should be replaced, or nil. Each check is bounded.
    /// A check that does not return is not a change: a hang must not restart the
    /// daemon and must not hold up `stop()`. A nil fingerprint (settings unreadable)
    /// is a value, not a timeout.
    private func restartReason(_ key: DraftRunner.KeyOutcome, _ startedFrom: String) -> String? {
        switch bound(currentFingerprint, seconds: watchBound) {
        case .timedOut:
            return nil
        case let .value(now):
            if let now, now != startedFrom { return "the app's Vani settings changed" }
        }
        if isStopped { return nil }
        switch bound(keyExists, seconds: watchBound) {
        case .timedOut:
            return nil
        case let .value(exists):
            if key == .notFound, exists == true { return "the decision key is now in the Keychain" }
        }
        return nil
    }

    /// SIGTERM, then SIGKILL after `stopGrace`, then a moment to be reaped.
    private func end(_ job: LaunchedJob) {
        job.stop()
        cond.lock()
        let deadline = Date().addingTimeInterval(stopGrace)
        while !childExited, cond.wait(until: deadline) {}
        let gone = childExited
        cond.unlock()
        if !gone {
            job.forceStop()
            cond.lock()
            let reap = Date().addingTimeInterval(2)
            while !childExited, cond.wait(until: reap) {}
            cond.unlock()
        }
    }

    /// Waits `seconds`, or until `stop()`.
    private func pause(_ seconds: TimeInterval) {
        guard seconds > 0 else { return }
        let deadline = Date().addingTimeInterval(seconds)
        cond.lock()
        while !stopped, cond.wait(until: deadline) {}
        cond.unlock()
    }

    private func note(_ message: String, _ url: URL?) {
        log.write(message, to: url ?? fallbackLog)
    }
}

/// One watch-path read. `.value` is what it returned, including nil. `.timedOut`
/// means it did not return: that is not a change and must not be flattened into
/// the nil a missing settings file already returns.
private enum WatchBound<T: Sendable>: Sendable {
    case value(T)
    case timedOut
}

/// One stored result. The outer optional is "was it stored", so a stored nil
/// (T itself optional) is not the same as a read that has not finished.
private struct WatchSlot<T: Sendable>: Sendable {
    var value: T
}

/// Holds one result across the watch thread and the bounded call.
private final class WatchBox<T: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var slot: WatchSlot<T>?
    func set(_ v: T) {
        lock.lock()
        slot = WatchSlot(value: v)
        lock.unlock()
    }
    func get() -> WatchSlot<T>? {
        lock.lock()
        defer { lock.unlock() }
        return slot
    }
}

/// Runs `work` and waits up to `seconds`. The work is abandoned, not cancelled,
/// if it outlives the wait: the same pattern as DraftRunner.bounded. The box
/// stays retained by the queued block, so a late finish does not use a freed box.
private func bound<T: Sendable>(_ work: @escaping @Sendable () -> T, seconds: TimeInterval) -> WatchBound<T> {
    let done = DispatchSemaphore(value: 0)
    let box = WatchBox<T>()
    DispatchQueue.global(qos: .utility).async {
        box.set(work())
        done.signal()
    }
    guard done.wait(timeout: .now() + seconds) == .success, let slot = box.get() else {
        return .timedOut
    }
    return .value(slot.value)
}
