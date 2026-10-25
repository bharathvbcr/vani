import Foundation
import Observation
import VaniApplyKit

/// "Re-triage with Sonnet" for the Jobs tab: one posting at a time, estimate first, spend only on
/// confirmation. Every step is a `vani jobs retriage` call off the main thread (RetriageClient);
/// the words come from `RetriagePresentation`. Owned by `JobsModel`, which is what refreshes the
/// list after a reading is made or cleared.
///
/// One operation at a time, app-wide: the daily cap and the side store are shared, and vani refuses
/// a second run (`busy`) anyway. `phase` carries the posting's key, so a detail pane shows only its
/// own posting's state.
@MainActor
@Observable
final class RetriageModel {
    enum Phase: Equatable {
        case idle
        /// Asking vani what it would cost (`--dry-run`). Spends nothing.
        case estimating(key: String)
        /// The estimate is in: waiting for the owner to confirm. Nothing is spent yet.
        case confirming(key: String, dry: RetriageDryRun)
        /// Sonnet is being asked. `cancelling`: Stop was pressed and the child is being ended.
        case running(key: String, cancelling: Bool)
        case succeeded(key: String, result: RetriageResult, at: Date)
        case failed(key: String, failure: RetriageFailure)
        case clearing(key: String)
        case cleared(key: String, result: RetriageCleared, relevanceBefore: Double?)

        var key: String? {
            switch self {
            case .idle: nil
            case let .estimating(key), let .confirming(key, _), let .running(key, _), let .succeeded(key, _, _),
                 let .failed(key, _), let .clearing(key), let .cleared(key, _, _): key
            }
        }
    }

    private(set) var phase: Phase = .idle

    private let client: (() -> RetriageClient)?
    /// Called, on the main actor, with the posting's key once a reading was made, may have been made
    /// (a cancelled run) or was cleared: JobsModel re-lists and reloads the open posting.
    var onManualChange: (String) async -> Void = { _ in }
    private var flag: CancelFlag?
    private var generation = 0

    init(client: (() -> RetriageClient)?) {
        self.client = client
    }

    /// The feature needs a runner the app built; a test host without one does not offer it.
    var isAvailable: Bool { client != nil }

    /// An operation is in flight (or waiting on the owner's confirmation), so no other may start.
    var isBusy: Bool {
        switch phase {
        case .estimating, .confirming, .running, .clearing: true
        case .idle, .succeeded, .failed, .cleared: false
        }
    }

    /// The estimate is in and the confirmation is open.
    var confirming: (key: String, dry: RetriageDryRun)? {
        if case let .confirming(key, dry) = phase { return (key, dry) }
        return nil
    }

    /// Why `job` cannot be re-triaged right now, or nil when it can.
    func blockedReason(for job: Job) -> String? {
        RetriagePresentation.blockedReason(for: job, busy: isBusy)
    }

    // MARK: Steps

    /// Ask vani what re-triaging `job` would cost. Nothing is spent.
    func start(_ job: Job) {
        guard let client, blockedReason(for: job) == nil else { return }
        begin(key: job.key, phase: .estimating(key: job.key))
        let c = client(), key = job.key, flag = flag, gen = generation
        Task {
            let result = await BlockingWork.value { c.estimate(key, cancellation: flag) }
            guard gen == generation else { return }
            switch result {
            case let .success(dry): phase = .confirming(key: key, dry: dry)
            case let .failure(f): phase = .failed(key: key, failure: f)
            case .cancelled: phase = .idle
            }
        }
    }

    /// The owner confirmed the cost: ask Sonnet. This is what spends.
    func confirm() {
        guard let client, case let .confirming(key, dry) = phase,
              RetriagePresentation.confirmation(for: dry).canConfirm else { return }
        begin(key: key, phase: .running(key: key, cancelling: false))
        let c = client(), flag = flag, gen = generation
        Task {
            let result = await BlockingWork.value { c.run(key, cancellation: flag) }
            guard gen == generation else { return }
            switch result {
            case let .success(r):
                phase = .succeeded(key: key, result: r, at: Date())
                await onManualChange(key)
            case let .failure(f):
                phase = .failed(key: key, failure: f)
            case .cancelled:
                // The child may have stored its reading before it was stopped: look, say nothing.
                phase = .idle
                await onManualChange(key)
            }
        }
    }

    /// Stop what is running (the child gets SIGTERM and is reaped), or close the confirmation.
    func cancel() {
        switch phase {
        case .estimating:
            generation += 1
            flag?.set()
            phase = .idle
        case .confirming:
            generation += 1
            phase = .idle
        case let .running(key, _):
            flag?.set()
            phase = .running(key: key, cancelling: true)
        default:
            break
        }
    }

    /// The confirmation was closed without confirming. Only that: a run that has begun is stopped
    /// by `cancel()`, never by a sheet going away.
    func dismissConfirmation() {
        guard case .confirming = phase else { return }
        cancel()
    }

    /// Try the same posting again from the confirmation (a retry never spends without it).
    func retry() {
        guard case let .failed(key, _) = phase, let job = candidate(key) else { return }
        phase = .idle
        start(job)
    }

    /// Forget the stored reading. Spends nothing.
    func clear(_ job: Job) {
        guard let client, !isBusy else { return }
        begin(key: job.key, phase: .clearing(key: job.key))
        let c = client(), key = job.key, before = job.relevance, flag = flag, gen = generation
        Task {
            let result = await BlockingWork.value { c.clear(key, cancellation: flag) }
            guard gen == generation else { return }
            switch result {
            case let .success(r):
                phase = .cleared(key: key, result: r, relevanceBefore: before)
                await onManualChange(key)
            case let .failure(f): phase = .failed(key: key, failure: f)
            case .cancelled: phase = .idle
            }
        }
    }

    /// Close a result, an error or a note.
    func dismiss() {
        switch phase {
        case .succeeded, .failed, .cleared: phase = .idle
        default: break
        }
    }

    /// Where `retry()` finds the posting again; set by JobsModel.
    var candidate: (String) -> Job? = { _ in nil }

    private func begin(key: String, phase next: Phase) {
        generation += 1
        flag = CancelFlag()
        phase = next
    }
}
