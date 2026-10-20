import Foundation
import Observation
import VaniApplyKit

/// State for the Pipeline tab: the last `vani status --json`, re-read on a
/// timer while the app runs and on demand. Each read is also offered to the
/// announcer, which posts a finished run's notification once. A read is local files only (no
/// network, no key), so polling it costs nothing.
@MainActor
@Observable
final class PipelineModel {
    /// How often the app re-reads while it is open.
    static let pollInterval: Duration = .seconds(60)

    private(set) var status: PipelineStatus?
    /// Why vani could not answer at all (distinct from a section it could not read).
    private(set) var error: String?
    private(set) var loading = false
    private(set) var checkedAt: Date?

    private let client: () -> PipelineClient
    /// Announces each finished run once (RunNotice); nil posts nothing.
    private let announcer: RunAnnouncer?
    private var generation = 0

    init(client: @escaping () -> PipelineClient, announcer: RunAnnouncer? = nil) {
        self.client = client
        self.announcer = announcer
    }

    /// The overall state for the sidebar: a vani that cannot answer is a problem.
    var health: PipelineHealth? {
        if error != nil { return .problem }
        return status?.health(at: Date())
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
            announcer?.consider(s.lastRun)
        case let .failure(err):
            // Keep the last good status on screen, marked stale by the error banner.
            error = String(describing: err)
            announcer?.unreadable(String(describing: err))
        }
    }

    /// Re-read until the task is cancelled (the view's `.task` owns it).
    func poll() async {
        while !Task.isCancelled {
            await refresh()
            try? await Task.sleep(for: Self.pollInterval)
        }
    }
}
