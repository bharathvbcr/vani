import Foundation
import Observation
import VaniApplyKit

/// The Pipeline tab's doctor: price a review (`vani doctor --dry-run`, spends nothing), confirm a budget,
/// run it (`vani doctor --budget-usd`, spends), and show the report. The newest report written, from here
/// or a shell, is read through `vani doctor --last`. Every call runs off the main thread (DoctorClient).
///
/// What must look different, and does: nothing to ask; a complete review; a partial review or a part
/// that failed (a failure, its report still shown); a refusal before anything was sent; a failure after
/// a request may have been sent; the owner stopping it.
@MainActor
@Observable
final class DoctorModel {
    enum Phase: Equatable {
        case idle
        /// Asking vani what a review would send and cost. Spends nothing.
        case estimating
        /// The estimate is in: waiting for the owner. Nothing is spent yet.
        case confirming(DoctorReply)
        /// The model is being asked. `cancelling`: Stop was pressed and vani is being ended.
        case running(cancelling: Bool)
        /// vani exited 0: a complete review, or nothing to ask.
        case finished(DoctorReply)
        case failed(Failure)
        /// The owner stopped a review. A request already sent was paid for.
        case stopped
    }

    struct Failure: Equatable {
        enum Stage: Equatable { case estimate, run }
        var stage: Stage
        var failure: RetriageFailure
        /// vani's own reply, when it wrote one: `sent` and, for a partial review, `result`.
        var reply: DoctorReply?

        /// What the owner is told first.
        var title: String {
            switch stage {
            case .estimate: return "The review could not be priced"
            case .run:
                if reply?.result != nil { return "The review is incomplete" }
                if reply?.sent == false { return "The review was refused; nothing was sent" }
                if reply?.sent == true { return "The review failed after a request was sent" }
                return "The review failed"
            }
        }

        /// A request may have been paid for. vani's reply says; without one, a run that timed out or
        /// answered unreadably may have, and one that never started (a launch or argument refusal,
        /// the account launcher's refusal) did not.
        var mayHaveSpent: Bool {
            guard stage == .run else { return false }
            if let reply { return reply.sent }
            return Self.unknownOutcome.contains(failure.code)
        }

        static let unknownOutcome: Set<String> = ["timeout", "no_reply", "too_large", "unknown"]

        /// vani refused because the worst case grew past the budget (new runs since the estimate):
        /// pricing again is the way on.
        var wantsNewEstimate: Bool { reply?.sent == false && failure.message.contains("over --budget-usd") }
    }

    private(set) var phase: Phase = .idle
    /// The budget for the review being confirmed, in dollars.
    var budget: Double = 0
    /// The newest report written, as `vani doctor --last` read it; nil until read or when none.
    private(set) var last: DoctorLast?
    /// Why the newest report could not be read.
    private(set) var lastError: String?
    private(set) var lastLoaded = false

    private let client: (() -> DoctorClient)?
    private var flag: CancelFlag?
    private var generation = 0

    init(client: (() -> DoctorClient)?) {
        self.client = client
    }

    var isAvailable: Bool { client != nil }

    var isBusy: Bool {
        switch phase {
        case .estimating, .confirming, .running: true
        case .idle, .finished, .failed, .stopped: false
        }
    }

    var confirming: DoctorReply? {
        if case let .confirming(r) = phase { return r }
        return nil
    }

    /// The report to show: the run just finished (or its partial report), else the newest written.
    var report: (report: DoctorReport, path: String?)? {
        switch phase {
        case let .finished(r):
            if let result = r.result { return (result, r.report) }
        case let .failed(f):
            if let result = f.reply?.result { return (result, f.reply?.report) }
        default:
            break
        }
        if let result = last?.result { return (result, last?.report) }
        return nil
    }

    // MARK: Steps

    /// Read the newest report written. Spends nothing.
    func loadLast() async {
        guard let client else { return }
        let c = client()
        let result = await BlockingWork.value { c.last() }
        lastLoaded = true
        switch result {
        case let .success(l):
            last = l.result == nil ? nil : l
            lastError = nil
        case let .failure(f):
            lastError = f.message
        case .cancelled:
            break
        }
    }

    /// Ask vani what a review would send and cost. Nothing is spent.
    func estimate() {
        guard let client, !isBusy else { return }
        begin(.estimating)
        let c = client(), flag = flag, gen = generation
        Task {
            let result = await BlockingWork.value { c.plan(cancellation: flag) }
            guard gen == generation else { return }
            switch result {
            case let .success(reply):
                budget = DoctorPresentation.suggestedBudget(worstUSD: reply.worstCaseUSD) ?? 0
                phase = .confirming(reply)
            case let .failure(f): phase = .failed(Failure(stage: .estimate, failure: f))
            case .cancelled: phase = .idle
            }
        }
    }

    /// Why the confirmed review cannot run, or nil.
    func runBlocker(_ plan: DoctorReply) -> String? {
        if plan.nothingToAsk { return "Nothing to ask: no board tripped a signal and no dropped posting was sampled." }
        if DoctorPresentation.suggestedBudget(worstUSD: plan.worstCaseUSD) == nil {
            return "The worst case, \(DoctorPresentation.usd(plan.worstCaseUSD)), is over the $\(Int(DoctorClient.maxBudgetUSD)) a review may spend."
        }
        if !DoctorPresentation.covers(budget: budget, worstUSD: plan.worstCaseUSD) {
            return "The budget must cover the worst case, \(DoctorPresentation.usd(plan.worstCaseUSD)), and be at most $\(Int(DoctorClient.maxBudgetUSD))."
        }
        return nil
    }

    /// The owner confirmed: ask the model. This is what spends.
    func confirm() {
        guard let client, let plan = confirming, runBlocker(plan) == nil else { return }
        let budget = (self.budget * 100).rounded() / 100, seed = plan.seed
        begin(.running(cancelling: false))
        let c = client(), flag = flag, gen = generation
        Task {
            let outcome = await BlockingWork.value { c.run(budgetUSD: budget, seed: seed, cancellation: flag) }
            guard gen == generation else { return }
            switch outcome {
            case let .finished(reply):
                phase = .finished(reply)
                if let result = reply.result { last = DoctorLast(report: reply.report, result: result) }
            case let .failed(f, reply):
                phase = .failed(Failure(stage: .run, failure: f, reply: reply))
                if let result = reply?.result {
                    last = DoctorLast(report: reply?.report, result: result)
                } else if reply?.sent != false {
                    // A review that failed without saying so may still have written a report.
                    await loadLast()
                }
            case .cancelled:
                phase = .stopped
                // A stopped review writes what it had (each part's error), so show that if it did.
                await loadLast()
            }
        }
    }

    /// Stop what is running, or close the confirmation.
    func cancel() {
        switch phase {
        case .estimating, .confirming:
            generation += 1
            flag?.set()
            phase = .idle
        case .running:
            flag?.set()
            phase = .running(cancelling: true)
        default:
            break
        }
    }

    /// The confirmation was closed without confirming. Only a confirmation is closed: the sheet closing
    /// after Run Review must not stop the review it just started.
    func dismissConfirmation() {
        guard case .confirming = phase else { return }
        cancel()
    }

    /// Close a finished review, a failure or a stop.
    func dismiss() {
        switch phase {
        case .finished, .failed, .stopped: phase = .idle
        default: break
        }
    }

    private func begin(_ next: Phase) {
        generation += 1
        flag = CancelFlag()
        phase = next
    }
}
