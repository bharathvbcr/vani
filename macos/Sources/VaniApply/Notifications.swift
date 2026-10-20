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
    let pipeline: PipelineModel
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
        jobs = JobsModel { model.jobsClient }
        // Without a bundle (a bare `swift run`) there is no notification center,
        // so the app announces nothing and leaves the notifier lock, and with it
        // the run notices, to the daemon.
        let announcer = Bundle.main.bundleIdentifier == nil ? nil
            : RunAnnouncer(store: DefaultsAnnouncedRuns(), poster: UserNotificationPoster(), lock: NotifierLock())
        pipeline = PipelineModel(client: { model.pipelineClient }, announcer: announcer)
        super.init()
    }

    func applicationWillFinishLaunching(_ notification: Notification) {
        guard Bundle.main.bundleIdentifier != nil else { return }
        UNUserNotificationCenter.current().delegate = self
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        if Bundle.main.bundleIdentifier != nil { Self.requestPermissionOnce() }
        pollTask = Task { [pipeline] in await pipeline.poll() }
    }

    func applicationWillTerminate(_ notification: Notification) {
        pollTask?.cancel()
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

    func showJobs() {
        model.pane = .jobs
        NSApp.activate()
        NSApp.windows.first { $0.canBecomeMain }?.makeKeyAndOrderFront(nil)
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
