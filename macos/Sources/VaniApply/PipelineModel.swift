import Foundation
import Observation
import VaniApplyKit

/// State for the Pipeline tab: the last `vani status --json`, re-read on a
/// timer while the app runs and on demand. Each read is also offered to the
/// announcer, which posts a finished run's notification once. A read is local files only (no
/// network, no key), so polling it costs nothing.
///
/// Any run the status shows as going, including the daemon's daily run, is
/// re-read every few seconds so the live portal moves. A run this app just
/// started is followed the same way without waiting out the rest of a minute
/// sleep. While the daemon holds the ledger, Run Now asks it (`vani run-request`)
/// instead of starting `vani run-once`.
@MainActor
@Observable
final class PipelineModel {
    /// How often the app re-reads while it is open and nothing is running.
    static let pollInterval: Duration = .seconds(60)
    /// How often it re-reads while a run is going or a request is waiting.
    static let runningPollInterval: Duration = .seconds(3)

    /// The gap before the next status read. A run that is already going, one
    /// this app is starting, or a request waiting on the daemon, is short.
    /// The minute wait is only for a quiet app.
    static func statusPollInterval(runInProgress: Bool, waitingForDaemon: Bool, startingRun: Bool) -> Duration {
        (runInProgress || waitingForDaemon || startingRun) ? runningPollInterval : pollInterval
    }

    /// Where the last run's options are kept between launches.
    static let optionsKey = "pipeline.manualRunOptions"

    private(set) var status: PipelineStatus?
    /// Why vani could not answer at all (distinct from a section it could not read).
    private(set) var error: String?
    private(set) var loading = false
    private(set) var checkedAt: Date?

    /// The run this app started, while this app has it.
    private(set) var manualRun: LaunchedJob?
    /// Getting the keys and starting vani (a Keychain prompt can hold it).
    private(set) var startingRun = false
    /// Why the last manual run could not start, or ended before vani recorded it.
    private(set) var runError: String?
    /// The log of the last run this app started.
    private(set) var lastLog: URL?
    /// When this app asked the daemon for a run that has not started yet.
    private(set) var requestedAt: Date?
    /// The Run Now sheet, when it is open. Nil dismisses it. Triage now and
    /// Run Now are different sources: only a Run Now confirmation is kept.
    var runSheet: RunSheet?
    /// Where Copy failures stands: vani words the report, so copying is a call to it.
    private(set) var failureCopy = FailureCopy.idle

    enum FailureCopy: Equatable {
        case idle
        case copying
        /// The report is on the pasteboard.
        case copied
        /// Nothing was copied, and why (vani too old, timed out, empty reply).
        case failed(String)
    }

    private let client: () -> PipelineClient
    private let launcher: () -> ManualRunLauncher?
    /// Announces each finished run once (RunNotice); nil posts nothing.
    private let announcer: RunAnnouncer?
    private let defaults: UserDefaults
    private var generation = 0
    private var watchTask: Task<Void, Never>?
    /// Bumped each time `follow` starts, so a cancelled follower does not clear the new one.
    private var followGeneration = 0
    /// True while `follow` is the fast reader. `poll` skips its own read then, so a
    /// run this app started is not asked of vani twice.
    private var following = false
    /// The job whose exit is being handled (runExited).
    private var exitingRun: LaunchedJob?

    init(client: @escaping () -> PipelineClient, launcher: @escaping () -> ManualRunLauncher? = { nil },
         announcer: RunAnnouncer? = nil, defaults: UserDefaults = .standard) {
        self.client = client
        self.launcher = launcher
        self.announcer = announcer
        self.defaults = defaults
    }

    /// The overall state for the sidebar: a vani that cannot answer is a problem.
    var health: PipelineHealth? {
        if error != nil { return .problem }
        return status?.health(at: Date())
    }

    /// A run is going: the one this app started, or any the run record shows.
    var runInProgress: Bool {
        manualRun?.isRunning == true || status?.lastRun.state == .running
    }

    /// A daemon holds the ledger: Run Now asks it rather than start run-once.
    var daemonRunning: Bool { status?.daemon?.running == true }

    /// This app's run request is waiting for the daemon to pick it up.
    var waitingForDaemon: Bool { requestedAt != nil }

    /// Why Run Now is off, or nil when a run can start.
    var runBlocker: String? {
        if startingRun { return "A run is starting." }
        if waitingForDaemon { return "Your run is waiting for the daemon." }
        if runInProgress { return "A run is going." }
        if status == nil { return "Vani has not reported its status yet." }
        if let r = status?.runRequest, r.pending, !r.stale { return "A run request is already waiting for the daemon." }
        return nil
    }

    /// The options the last manual run used, or none.
    var savedOptions: ManualRunOptions {
        guard let data = defaults.data(forKey: Self.optionsKey),
              let o = try? JSONDecoder().decode(ManualRunOptions.self, from: data) else { return ManualRunOptions() }
        return o
    }

    /// `interactive` is a Refresh the user asked for: it shows the spinner.
    /// The timer passes false, so a run that is re-read every few seconds
    /// does not flash the button.
    func refresh(interactive: Bool = true) async {
        generation += 1
        let gen = generation
        if interactive { loading = true }
        let c = client()
        let result = await BlockingWork.run { try c.status() }
        guard gen == generation else { return } // a newer read superseded this one
        loading = false
        checkedAt = Date()
        switch result {
        case let .success(s):
            status = s
            error = nil
            settleRequest(s)
            announcer?.consider(s.lastRun, stateDir: s.stateDir)
        case let .failure(err):
            // Keep the last good status on screen, marked stale by the error banner.
            error = String(describing: err)
            announcer?.unreadable(String(describing: err))
        }
    }

    /// Ask vani for the latest run's failure report (`vani runs failures`) and hand
    /// its text to `write`, the pasteboard. The call is bounded by the client's
    /// timeout; when it fails nothing is written and `failureCopy` says why, so the
    /// pasteboard never gets an empty or half report.
    func copyFailures(write: (String) -> Void) async {
        guard failureCopy != .copying else { return }
        failureCopy = .copying
        let c = client()
        let result = await BlockingWork.run { try c.failureReport() }
        switch result {
        case let .success(report):
            write(report.text)
            failureCopy = .copied
        case let .failure(err):
            failureCopy = .failed("Could not copy the failures: \(err)")
        }
    }

    /// Forget a copy's outcome (the checkmark times out; a failure is dismissed).
    func resetFailureCopy() {
        if failureCopy != .copying { failureCopy = .idle }
    }

    /// Re-read until the task is cancelled (AppDelegate owns it, so the poll
    /// runs with the window closed). A run the status already shows as going
    /// is read on the short interval; `follow` covers the minute of sleep
    /// still left when this app itself starts one.
    func poll() async {
        while !Task.isCancelled {
            if !following {
                await refresh(interactive: false)
            }
            let wait = Self.statusPollInterval(runInProgress: runInProgress,
                                               waitingForDaemon: waitingForDaemon,
                                               startingRun: startingRun)
            try? await Task.sleep(for: wait)
        }
    }

    /// Offer these options from Triage now. A run that cannot start is not
    /// offered. Confirming that sheet does not keep the choices.
    func proposeRun(_ options: ManualRunOptions) {
        guard runBlocker == nil else { return }
        runSheet = RunSheet(options: options, source: .triageNow)
    }

    /// Offer the remembered options from Run Now. Confirming that sheet keeps
    /// the choices. A run that cannot start is not offered.
    func openRunNow() {
        guard runBlocker == nil else { return }
        runSheet = RunSheet(options: savedOptions, source: .runNow)
    }

    /// Start `vani run-once` with these options, or, while the daemon runs,
    /// ask it for the run. vani refuses a run-once while another holds the
    /// ledger; that refusal, like a refused request, reaches `runError`.
    /// `remember` is false for a sheet Triage now opened, so a refused start
    /// leaves the options Run Now will offer next time.
    func startRun(_ options: ManualRunOptions, remember: Bool = true) async {
        if let blocker = runBlocker {
            runError = blocker
            return
        }
        guard let stateDir = status?.stateDir, !stateDir.isEmpty else {
            runError = "Vani has not reported its state folder yet."
            return
        }
        runError = nil
        if daemonRunning {
            await requestRun(options, remember: remember)
            return
        }
        guard let launcher = launcher() else {
            runError = "This copy of the app cannot start vani."
            return
        }
        startingRun = true
        storeOptions(options, remember: remember)
        // An exit that lands before manualRun is set is picked up below.
        let onExit: @Sendable (Int32) -> Void = { [weak self] code in
            Task { @MainActor in
                guard let self, let job = self.manualRun else { return }
                await self.runExited(code, job: job)
            }
        }
        let result = await BlockingWork.run {
            try launcher.start(options, stateDir: stateDir, onExit: onExit)
        }
        startingRun = false
        switch result {
        case let .success(job):
            manualRun = job
            lastLog = job.log
            if let code = job.exitStatus {
                await runExited(code, job: job)
            } else {
                follow()
            }
        case let .failure(err):
            runError = String(describing: err)
        }
    }

    /// SIGTERM to the run this app started; vani records it as cancelled.
    /// A run started elsewhere (the daemon, a shell) is never signalled, nor
    /// is one this app asked the daemon for.
    func stopRun() {
        manualRun?.stop()
    }

    /// Keep choices for the next Run Now. A Triage now confirmation passes
    /// false, including when vani then refuses the run.
    private func storeOptions(_ options: ManualRunOptions, remember: Bool) {
        guard remember, let data = try? JSONEncoder().encode(options) else { return }
        defaults.set(data, forKey: Self.optionsKey)
    }

    /// Ask the daemon for the run (`vani run-request`), then follow it.
    private func requestRun(_ options: ManualRunOptions, remember: Bool) async {
        startingRun = true
        storeOptions(options, remember: remember)
        let c = client()
        let result = await BlockingWork.run { try c.requestRun(options) }
        startingRun = false
        switch result {
        case let .success(reply):
            requestedAt = reply.requestedAt
            follow()
        case let .failure(err):
            runError = String(describing: err)
        }
    }

    /// How far a run record's start may precede the request's time and still
    /// be its run (the two times are written by one clock, to the millisecond).
    static let requestSlack: TimeInterval = 1

    /// Settle this app's pending request against a fresh status: picked up
    /// (a manual run recorded since it was made), still waiting, or gone
    /// without a run (the daemon dropped it, or stopped).
    private func settleRequest(_ s: PipelineStatus) {
        guard let at = requestedAt else { return }
        let since = at.addingTimeInterval(-Self.requestSlack)
        if s.lastRun.trigger == .manual, let started = s.lastRun.startedAt, started >= since {
            requestedAt = nil // the run record follows it from here
            return
        }
        let r = s.runRequest
        let mine: (Date?) -> Bool = { d in d.map { abs($0.timeIntervalSince(at)) <= Self.requestSlack } ?? false }
        if r?.claimed == true, mine(r?.claimedRequestedAt) {
            return // the daemon is starting it: the record comes next
        }
        if r?.pending == true, mine(r?.requestedAt) {
            if r?.stale == true {
                runError = "The daemon did not pick up your run within an hour, so it will not run. Ask again."
                requestedAt = nil
            } else if s.daemon?.running != true {
                runError = "The daemon stopped before it picked up your run. The request waits up to an hour for it to start again."
                requestedAt = nil
            }
            return
        }
        runError = "The daemon dropped your run request without running it; its log (vani.err.log) says why."
        requestedAt = nil
    }

    /// Re-read every few seconds while a run is going or a request waits,
    /// then once more. Started when this app asks for a run, so the read
    /// does not wait out whatever is left of `poll`'s minute.
    private func follow() {
        watchTask?.cancel()
        followGeneration += 1
        let generation = followGeneration
        following = true
        watchTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(1))
            while !Task.isCancelled {
                guard let self else { return }
                await self.refresh(interactive: false)
                if !self.runInProgress && !self.waitingForDaemon { break }
                try? await Task.sleep(for: Self.runningPollInterval)
            }
            guard let self, self.followGeneration == generation else { return }
            self.following = false
            self.watchTask = nil
        }
    }

    /// The run this app started has exited. A non-zero exit whose run vani never
    /// recorded (it was refused before it began) is reported here; a run that
    /// was recorded reports itself in the Last run section.
    private func runExited(_ code: Int32, job: LaunchedJob) async {
        // Once per job: whichever of the exit handler and startRun comes first.
        guard manualRun === job, exitingRun !== job else { return }
        exitingRun = job
        // Re-read before letting go of the job, so the tab never shows the
        // record from before the run as this run's outcome.
        await refresh()
        let recorded = status?.lastRun.startedAt.map { $0 >= job.startedAt.addingTimeInterval(-2) } ?? false
        if code != 0 && !recorded {
            let why = VaniProcess.lastVaniLine(inLog: job.log) ?? "vani run-once exited \(code); see \(job.log.path)"
            runError = why
        }
        manualRun = nil
        exitingRun = nil
    }
}
