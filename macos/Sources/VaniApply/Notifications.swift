import AppKit
import UserNotifications
import VaniApplyKit

/// Owns the app's models and the notification center's delegate. The delegate
/// is installed in applicationWillFinishLaunching, so a click that launches the
/// app still reaches it, and the pipeline poll starts here rather than in a
/// view, so a run is announced while the window is closed too.
@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, UNUserNotificationCenterDelegate {
    let model: AppModel
    let jobs: JobsModel
    let audit: AuditModel
    let pipeline: PipelineModel
    let exclusions: ExclusionsEditor
    private var pollTask: Task<Void, Never>?

    override init() {
        // A screenshot or fixture rig passes -VaniApplyNoAgent YES so it does not
        // take the per-user login agent from the owner's copy (AgentLaunch).
        let launch = AgentLaunch.from(arguments: CommandLine.arguments)
        if let problem = launch.problem {
            FileHandle.standardError.write(Data("Vani Apply: \(problem)\n".utf8))
        }
        let model = AppModel(autoRegisterAgent: launch.mode == .register)
        self.model = model
        let jobs = JobsModel(client: { model.jobsClient }, retriage: { model.retriageClient },
                             resume: { model.resumeStudioClient }, bankStore: { model.store })
        self.jobs = jobs
        // A saved exclusion re-lists Jobs, which hides the roles it matches.
        exclusions = ExclusionsEditor(client: { model.draftSettings.exclusionsClient(vaniBinary: model.vaniBinary) },
                                      onSaved: { jobs.reload() })
        audit = AuditModel { model.auditClient }
        // Without a bundle (a bare `swift run`) there is no notification center,
        // so the app announces nothing and leaves the notifier lock, and with it
        // the run notices, to the daemon. A launch by the daemon to announce a
        // run (AnnounceLaunch) also announces the run that just finished.
        let announcer = Bundle.main.bundleIdentifier == nil ? nil
            : RunAnnouncer(store: DefaultsAnnouncedRuns(), poster: UserNotificationPoster(), lock: NotifierLock(),
                           handoffGrace: AnnounceLaunch.handoffGrace(arguments: CommandLine.arguments))
        pipeline = PipelineModel(client: { model.pipelineClient }, launcher: { model.manualRunLauncher }, announcer: announcer)
        super.init()
    }

    func applicationWillFinishLaunching(_ notification: Notification) {
        guard Bundle.main.bundleIdentifier != nil else { return }
        UNUserNotificationCenter.current().delegate = self
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        if Bundle.main.bundleIdentifier != nil { Self.requestPermissionOnce() }
        pollTask = Task { [pipeline] in await pipeline.poll() }
        // The label table is read once at launch, before any pane shows a menu or a chart.
        Task { [jobs] in await jobs.loadLabels() }
    }

    func applicationWillTerminate(_ notification: Notification) {
        pollTask?.cancel()
    }

    /// Closing the window leaves the app running, still holding the notifier
    /// lock and announcing runs. A lone `Window` scene would otherwise quit the
    /// app with its window, and the daemon would announce instead. Quit
    /// (Command-Q) hands notices back to the daemon.
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        false
    }

    /// The Dock icon reopens a closed window.
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows: Bool) -> Bool {
        if !hasVisibleWindows { revealMainWindow() }
        return true
    }

    /// Asks only while the user has not decided; macOS keeps the answer, and
    /// Settings > Notifications is where it changes afterwards.
    static func requestPermissionOnce() {
        Task {
            let center = UNUserNotificationCenter.current()
            guard await center.notificationSettings().authorizationStatus == .notDetermined else { return }
            do {
                _ = try await center.requestAuthorization(options: [.alert, .sound])
            } catch {
                NSLog("Vani Apply: notification permission: %@", String(describing: error))
            }
        }
    }

    /// A notice that arrives while the app is in front still shows.
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter,
                                            willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
        [.banner, .list, .sound]
    }

    /// A click opens the Jobs tab, where the digest's roles are.
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        await MainActor.run { self.showJobs() }
    }

    /// openWindow(id: "main"), captured by the scene's root view when it first
    /// appears (VaniApplyApp). It stays valid after that window closes.
    var openMainWindow: (() -> Void)?

    func showJobs() {
        model.pane = .jobs
        revealMainWindow()
    }

    private func revealMainWindow() {
        NSApp.activate()
        let reveal = MainWindowReveal(open: openMainWindow) {
            // The main window, not a Resume Studio window (those are titled "Resume — ...").
            guard let window = NSApp.windows.first(where: { $0.canBecomeMain && $0.title == "Vani Apply" && ($0.isVisible || $0.isMiniaturized) }) else {
                return false
            }
            window.makeKeyAndOrderFront(nil)
            return true
        }
        if reveal.reveal() == .noWindow {
            NSLog("Vani Apply: no window to show for a notification click")
        }
    }
}

/// Posts through UNUserNotificationCenter.
final class UserNotificationPoster: NoticePoster {
    func post(_ notice: RunNotice) {
        let content = UNMutableNotificationContent()
        content.title = notice.title
        content.body = notice.body
        content.threadIdentifier = notice.threadID
        content.sound = .default
        let request = UNNotificationRequest(identifier: notice.key, content: content, trigger: nil)
        UNUserNotificationCenter.current().add(request) { error in
            if let error { NSLog("Vani Apply: could not post a notification: %@", String(describing: error)) }
        }
    }
}
