import Foundation
import Observation
import VaniApplyKit

/// State for the Pipeline tab: the last `vani status --json`, re-read on a
/// timer while the app runs and on demand. Each read is also offered to the
/// announcer, which posts a finished run's notification once. A read is local files only (no
/// network, no key), so polling it costs nothing.
///
/// It also starts a run by hand (`vani run-once` with the chosen overrides) and
/// follows it through the same status read, faster while it is going.
@MainActor
@Observable
final class PipelineModel {
    /// How often the app re-reads while it is open.
    static let pollInterval: Duration = .seconds(60)
    /// How often it re-reads while a run is going.
    static let runningPollInterval: Duration = .seconds(3)
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

    private let client: () -> PipelineClient
    private let launcher: () -> ManualRunLauncher?
    /// Announces each finished run once (RunNotice); nil posts nothing.
    private let announcer: RunAnnouncer?
    private let defaults: UserDefaults
    private var generation = 0
    private var watchTask: Task<Void, Never>?
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

    /// Why Run Now is off, or nil when a run can start.
    var runBlocker: String? {
        if startingRun { return "A run is starting." }
        if runInProgress { return "A run is going." }
        if status == nil { return "Vani has not reported its status yet." }
        return nil
    }

    /// The options the last manual run used, or none.
    var savedOptions: ManualRunOptions {
        guard let data = defaults.data(forKey: Self.optionsKey),
              let o = try? JSONDecoder().decode(ManualRunOptions.self, from: data) else { return ManualRunOptions() }
        return o
    }

    func refresh() async {
        generation += 1
        let gen = generation
        loading = true
        let c = client()
        let result = await Task.detached(priority: .utility) { Result { try c.status() } }.value
        guard gen == generation else { return } // a newer read superseded this one
        loading = false
        checkedAt = Date()
        switch result {
        case let .success(s):
            status = s
            error = nil
            announcer?.consider(s.lastRun, stateDir: s.stateDir)
        case let .failure(err):
            // Keep the last good status on screen, marked stale by the error banner.
            error = String(describing: err)
            announcer?.unreadable(String(describing: err))
        }
    }

    /// Re-read until the task is cancelled (AppDelegate owns it, so the poll
    /// runs with the window closed).
    func poll() async {
        while !Task.isCancelled {
            await refresh()
            try? await Task.sleep(for: Self.pollInterval)
        }
    }

    /// Start `vani run-once` with these options. vani refuses a run while
    /// another holds the ledger (the daemon); that refusal reaches `runError`.
    func startRun(_ options: ManualRunOptions) async {
        guard runBlocker == nil, let stateDir = status?.stateDir else { return }
        runError = nil
        guard let launcher = launcher() else {
            runError = "This copy of the app cannot start vani."
            return
        }
        startingRun = true
        if let data = try? JSONEncoder().encode(options) { defaults.set(data, forKey: Self.optionsKey) }
        // An exit that lands before manualRun is set is picked up below.
        let onExit: @Sendable (Int32) -> Void = { [weak self] code in
            Task { @MainActor in
                guard let self, let job = self.manualRun else { return }
                await self.runExited(code, job: job)
            }
        }
        let result = await Task.detached(priority: .userInitiated) {
            Result { try launcher.start(options, stateDir: stateDir, onExit: onExit) }
        }.value
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
    /// A run started elsewhere (the daemon, a shell) is never signalled.
    func stopRun() {
        manualRun?.stop()
    }

    /// Re-read every few seconds while a run is going, then once more.
    private func follow() {
        watchTask?.cancel()
        watchTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(1))
            while !Task.isCancelled {
                guard let self else { return }
                await self.refresh()
                if !self.runInProgress { return }
                try? await Task.sleep(for: Self.runningPollInterval)
            }
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
