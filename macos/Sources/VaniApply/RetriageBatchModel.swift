import Foundation
import Observation
import VaniApplyKit

/// "Re-triage the selection with Sonnet": several roles at once, estimate first, spend only on
/// confirmation, progress as each reading lands. One `vani jobs retriage --batch` call off the main
/// thread (RetriageBatchClient); the words come from `RetriageBatchPresentation`. Owned by
/// `JobsModel`, which re-lists once the batch ends (not after every reading, so rows do not jump
/// while the owner is looking at them).
///
/// It and the one-posting `RetriageModel` are busy for each other: they share the daily cap and the
/// side store, and vani refuses a second asker (`busy`) anyway.
@MainActor
@Observable
final class RetriageBatchModel {
    enum Phase: Equatable {
        case idle
        /// Asking vani what it would cost (`--dry-run`). Spends nothing.
        case estimating(count: Int)
        /// The estimate is in: waiting for the owner. Nothing is spent yet.
        case confirming(RetriageBatchPlan)
        /// Sonnet is being asked. `cancelling`: Stop was pressed and vani is being ended.
        case running(total: Int, cancelling: Bool)
        case finished(RetriageBatchSummary)
        case failed(RetriageFailure)
    }

    private(set) var phase: Phase = .idle
    /// The confirmation's choice: leave out roles Sonnet already has a current reading of. On by
    /// default, so a second pass over the same selection pays only for what is new.
    var skipRead = true
    /// Each asked role's state, for its row; cleared when the result is dismissed.
    private(set) var rows: [String: RetriageRowState] = [:]
    private(set) var kept = 0
    private(set) var failedCount = 0
    private(set) var usd = 0.0
    /// Roles whose role fit Sonnet changed, in the order the readings landed.
    private(set) var changedKeys: [String] = []

    private let client: (() -> RetriageBatchClient)?
    /// Called on the main actor once a batch ends (finished, failed after it started, or stopped):
    /// JobsModel re-lists and reloads the open posting.
    var onManualChange: () async -> Void = {}
    /// The one-posting re-triage is in flight.
    var otherBusy: () -> Bool = { false }
    private var flag: CancelFlag?
    private var generation = 0

    init(client: (() -> RetriageBatchClient)?) {
        self.client = client
    }

    var isAvailable: Bool { client != nil }

    var isBusy: Bool {
        switch phase {
        case .estimating, .confirming, .running: true
        case .idle, .finished, .failed: false
        }
    }

    var confirming: RetriageBatchPlan? {
        if case let .confirming(plan) = phase { return plan }
        return nil
    }

    /// Why `jobs` cannot be re-triaged together now, or nil.
    func blockedReason(for jobs: [Job]) -> String? {
        RetriageBatchPresentation.blockedReason(RetriageBatchPresentation.selection(jobs), busy: isBusy || otherBusy())
    }

    // MARK: Steps

    /// Ask vani what reading `jobs` would cost. Nothing is spent. Untriaged roles are passed too, so
    /// the confirmation can say they were skipped.
    func start(_ jobs: [Job]) {
        guard let client, blockedReason(for: jobs) == nil else { return }
        let keys = jobs.map(\.key)
        begin(.estimating(count: keys.count))
        let c = client(), flag = flag, gen = generation
        Task {
            let result = await BlockingWork.value { c.plan(keys, cancellation: flag) }
            guard gen == generation else { return }
            switch result {
            case let .success(plan): phase = .confirming(plan)
            case let .failure(f): phase = .failed(f)
            case .cancelled: phase = .idle
            }
        }
    }

    /// The owner confirmed: ask Sonnet. This is what spends.
    func confirm() {
        guard let client, let plan = confirming else { return }
        let confirmation = RetriageBatchPresentation.confirmation(for: plan, skipRead: skipRead)
        guard confirmation.canConfirm else { return }
        let keys = confirmation.keys, skip = skipRead
        begin(.running(total: keys.count, cancelling: false))
        rows = Dictionary(uniqueKeysWithValues: keys.map { ($0, RetriageRowState.queued) })
        let c = client(), flag = flag, gen = generation
        Task {
            let result = await BlockingWork.value {
                c.run(keys, skipRead: skip, cancellation: flag) { ev in
                    // In order, on the main queue: an item never lands after the done that counts it.
                    DispatchQueue.main.async { MainActor.assumeIsolated { self.apply(ev, gen: gen) } }
                }
            }
            // Let the events queued before the call returned land first.
            await withCheckedContinuation { cont in DispatchQueue.main.async { cont.resume() } }
            guard gen == generation else { return }
            switch result {
            case let .success(summary): finish(summary)
            case .cancelled: finish(localSummary(total: keys.count, stopped: "cancelled"))
            case let .failure(f):
                // Readings may have landed before it failed: keep their rows, say what failed.
                markNotAsked()
                phase = .failed(f)
            }
            await onManualChange()
        }
    }

    private func apply(_ ev: RetriageBatchEvent, gen: Int) {
        guard gen == generation else { return }
        switch ev {
        case .start, .done:
            break
        case let .item(key, .success(result)):
            let changed = result.before?.slots[.roleFit]?.usableValue != result.after.slots[.roleFit]?.usableValue
            rows[key] = .done(changed: changed)
            kept += 1
            usd += result.cost?.usd ?? 0
            if changed { changedKeys.append(key) }
        case let .item(key, .failure(f)):
            rows[key] = .failed(RetriagePresentation.errorText(for: f)?.message ?? f.message)
            failedCount += 1
            usd += f.cost?.usd ?? 0
        }
    }

    private func finish(_ summary: RetriageBatchSummary) {
        markNotAsked()
        phase = .finished(summary)
    }

    private func markNotAsked() {
        for (k, s) in rows where s == .queued { rows[k] = .notAsked }
    }

    /// What the app saw, for a batch whose own summary never arrived (the owner stopped it).
    private func localSummary(total: Int, stopped: String) -> RetriageBatchSummary {
        let notAsked = rows.filter { $0.value == .queued }.map(\.key)
        return RetriageBatchSummary(kept: kept, failed: failedCount, notAsked: max(total - kept - failedCount, 0),
                                    changed: changedKeys.count, usd: usd, stopped: stopped, notAskedKeys: notAsked)
    }

    /// Stop what is running, or close the confirmation.
    func cancel() {
        switch phase {
        case .estimating, .confirming:
            generation += 1
            flag?.set()
            phase = .idle
        case let .running(total, _):
            flag?.set()
            phase = .running(total: total, cancelling: true)
        default:
            break
        }
    }

    /// The confirmation was closed without confirming.
    func dismissConfirmation() {
        guard case .confirming = phase else { return }
        cancel()
    }

    /// Close a finished batch or an error, and forget the rows' states.
    func dismiss() {
        switch phase {
        case .finished, .failed:
            phase = .idle
            rows = [:]
            changedKeys = []
        default: break
        }
    }

    /// The roles that failed or were not asked, to try again (through the confirmation).
    var retryable: [String] {
        rows.filter { if case .done = $0.value { false } else { true } }.map(\.key).sorted()
    }

    private func begin(_ next: Phase) {
        generation += 1
        flag = CancelFlag()
        kept = 0
        failedCount = 0
        usd = 0
        changedKeys = []
        if case .estimating = next { rows = [:] }
        phase = next
    }
}
