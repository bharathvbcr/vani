import Foundation
import Observation
import VaniApplyKit

/// "Build a tailored resume" and "Use for this job" for the Jobs tab: one operation at a time,
/// estimate first, spend only on confirmation. Every step is a `vani jobs resume` call off the main
/// thread (ResumeStudioClient); the words come from `ResumeWords`. Owned by `JobsModel`, which re-shows
/// the job (its `resume` summary) when something changed.
///
/// One operation at a time, app-wide: vani allows one paid build or render per state folder (`busy`), and
/// the daily cap is shared. `phase` carries the posting's key, so a detail pane shows only its own
/// posting's state. A version binds for applying in two places and both are said: vani's own store
/// (`jobs resume use`) and the browsers' copy (`ResumeBinding`, under the job's posting and application
/// addresses); if the second fails after the first, the failure says so and Use is retried, never hidden.
@MainActor
@Observable
final class ResumeStudioModel {
    enum Phase: Equatable {
        case idle
        /// Asking vani what a build would cost (`build --dry-run`). Spends nothing.
        case estimating(key: String)
        /// The estimate is in: waiting for the owner to confirm. Nothing is spent yet.
        case confirming(key: String, dry: ResumeDryRun)
        /// Opus is being asked (or, with `allowBase`, the base selection built). `cancelling`: Stop was
        /// pressed and the child is being ended.
        case building(key: String, allowBase: Bool, cancelling: Bool)
        /// A version was saved (a build or a revalidation).
        case built(key: String, saved: ResumeSaved)
        /// Dropping what went stale and rebuilding, with no model.
        case revalidating(key: String, version: Int, cancelling: Bool)
        /// `use` is checking the version, then the browsers' copy is being saved.
        case using(key: String, version: Int)
        /// The version is bound; `note` says where, and any address that was left out.
        case used(key: String, version: Int, note: String)
        case failed(key: String, failure: ResumeFailure)

        var key: String? {
            switch self {
            case .idle: nil
            case let .estimating(key), let .confirming(key, _), let .building(key, _, _), let .built(key, _),
                 let .revalidating(key, _, _), let .using(key, _), let .used(key, _, _), let .failed(key, _): key
            }
        }
    }

    /// A version's PDF this process was told the path of (a build, a revalidation or a use). `previewCopy`
    /// asks `jobs resume show` for any version's path first; this is what it falls back to when `show`
    /// cannot answer (an older vani, a reply this app cannot read).
    struct KnownPDF: Equatable {
        var version: Int
        var path: String
        var sha256: String?
    }

    private(set) var phase: Phase = .idle
    private(set) var knownPDF: [String: KnownPDF] = [:]

    private let client: (() -> ResumeStudioClient)?
    private let bankStore: () -> BankStore?
    private var flag: CancelFlag?
    private var generation = 0

    private enum Op: Equatable {
        case estimate
        case use(Int)
        case revalidate(Int)
    }
    private var lastOp: Op?

    /// Called, on the main actor, with the posting's key once something may have changed in its resume
    /// store (a version saved, a stopped build that may have saved one, a version used): JobsModel
    /// re-shows the open posting, whose `resume` summary is what vani now reports.
    var onChange: (String) async -> Void = { _ in }
    /// Where `retry()` and the offers find the posting again; set by JobsModel.
    var candidate: (String) -> Job? = { _ in nil }
    /// The Studio window's way in, set by the window's owner. Nil until it exists: the card shows
    /// "Open Studio" disabled, with a tooltip saying so, and nothing pretends to open.
    var openStudio: ((String) -> Void)?

    init(client: (() -> ResumeStudioClient)?, bankStore: @escaping () -> BankStore? = { nil }) {
        self.client = client
        self.bankStore = bankStore
    }

    /// The feature needs a runner the app built; a test host without one does not offer it.
    var isAvailable: Bool { client != nil }

    /// An operation is in flight (or waiting on the owner's confirmation), so no other may start. A Studio
    /// window's Preview or Fit counts: it holds vani's build lock too, and answers `busy` to a second one.
    var isBusy: Bool {
        if externalOps > 0 { return true }
        switch phase {
        case .estimating, .confirming, .building, .revalidating, .using: return true
        case .idle, .built, .used, .failed: return false
        }
    }

    /// Studio renders in flight (Preview or Fit; at most one app-wide, see `claimRender`).
    private(set) var externalOps = 0

    /// A Studio window's Preview or Fit takes the one slot, or is refused when any resume step is running.
    func claimRender() -> Bool {
        guard !isBusy else { return false }
        externalOps += 1
        return true
    }

    func releaseRender() { externalOps = max(0, externalOps - 1) }

    /// The Studio saved a version (a fitted edit): remember its PDF and let the Jobs tab re-show the posting.
    func noteSaved(key: String, saved: ResumeSaved) async {
        remember(key: key, saved: saved)
        await onChange(key)
    }

    /// The estimate is in and the confirmation is open.
    var confirming: (key: String, dry: ResumeDryRun)? {
        if case let .confirming(key, dry) = phase { return (key, dry) }
        return nil
    }

    /// Why nothing can start for `job` right now, or nil when it can.
    func blockedReason(for job: Job) -> String? {
        isBusy ? "Another resume step is running. Wait for it to finish, or cancel it." : nil
    }

    // MARK: Build

    /// Ask vani what building a tailored resume for `job` would cost, and whether Opus can run. Nothing is spent.
    func start(_ job: Job) {
        guard let client, blockedReason(for: job) == nil else { return }
        begin(key: job.key, phase: .estimating(key: job.key), op: .estimate)
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

    /// The owner confirmed the cost: ask Opus. This is what spends. Refused while Opus cannot run or the cap is spent.
    func confirm() {
        guard case let .confirming(key, dry) = phase, ResumeWords.confirmation(for: dry).canConfirm else { return }
        build(key: key, allowBase: false)
    }

    /// The owner's explicit choice when Opus cannot run: build the base selection instead (`--allow-base`).
    /// Offered only on a confirmation that says Opus cannot run, and never taken for the paid path.
    func confirmBase() {
        guard case let .confirming(key, dry) = phase, !dry.opusUsable else { return }
        build(key: key, allowBase: true)
    }

    private func build(key: String, allowBase: Bool) {
        guard let client else { return }
        begin(key: key, phase: .building(key: key, allowBase: allowBase, cancelling: false), op: .estimate)
        let c = client(), flag = flag, gen = generation
        Task {
            let result = await BlockingWork.value { c.build(key, allowBase: allowBase, cancellation: flag) }
            guard gen == generation else { return }
            switch result {
            case let .success(saved):
                remember(key: key, saved: saved)
                phase = .built(key: key, saved: saved)
                await onChange(key)
            case let .failure(f):
                phase = .failed(key: key, failure: f)
            case .cancelled:
                // A version saved a moment before the stop is kept (vani writes it on a context a stop does
                // not end): look, say nothing.
                phase = .idle
                await onChange(key)
            }
        }
    }

    // MARK: Revalidate

    /// Drop what went stale from version `version` and rebuild it with fit. No model, so nothing is spent.
    func revalidate(_ job: Job, version: Int) {
        guard let client, blockedReason(for: job) == nil else { return }
        begin(key: job.key, phase: .revalidating(key: job.key, version: version, cancelling: false), op: .revalidate(version))
        let c = client(), key = job.key, flag = flag, gen = generation
        Task {
            let result = await BlockingWork.value { c.revalidate(key, version: version, cancellation: flag) }
            guard gen == generation else { return }
            switch result {
            case let .success(saved):
                remember(key: key, saved: saved)
                phase = .built(key: key, saved: saved)
                await onChange(key)
            case let .failure(f):
                phase = .failed(key: key, failure: f)
            case .cancelled:
                phase = .idle
                await onChange(key)
            }
        }
    }

    // MARK: Use

    private enum UseResult: Sendable {
        case bound(ResumeUsed, note: String)
        case failed(ResumeFailure)
        case cancelled
    }

    /// "Use for this job": `use KEY vN` checks the version afresh (a stale or unchecked one is refused), then
    /// its PDF is read, checked and saved for the browsers under the job's posting and application addresses.
    func use(_ job: Job, version: Int) {
        guard let client, blockedReason(for: job) == nil else { return }
        begin(key: job.key, phase: .using(key: job.key, version: version), op: .use(version))
        let c = client(), key = job.key, flag = flag, gen = generation, bank = bankStore()
        let targets = ResumeBinding.targets(url: job.url, applyURL: job.applyURL)
        Task {
            let result = await BlockingWork.value { () -> UseResult in
                switch c.use(key, version: version, cancellation: flag) {
                case .cancelled: return .cancelled
                case let .failure(f): return .failed(f)
                case let .success(used):
                    guard let bank else {
                        return .failed(Self.bindFailure(used, "the answer bank is not open in this window, so there is nowhere to save it"))
                    }
                    let resumes = ResumeStore(directory: bank.directory.appendingPathComponent("resumes", isDirectory: true))
                    do {
                        let out = try ResumeBinding.bindUsed(used, targets: targets, resumes: resumes, store: bank)
                        return .bound(used, note: ResumeBinding.note(for: out, targets: targets))
                    } catch let f as ResumeStore.Failure {
                        return .failed(Self.bindFailure(used, f.message))
                    } catch {
                        return .failed(Self.bindFailure(used, String(describing: error)))
                    }
                }
            }
            guard gen == generation else { return }
            switch result {
            case let .bound(used, note):
                knownPDF[key] = KnownPDF(version: used.version, path: used.pdfPath, sha256: used.pdfSHA256)
                phase = .used(key: key, version: used.version, note: note)
                await onChange(key)
            case let .failed(f):
                phase = .failed(key: key, failure: f)
                // `use` may have marked the version used in vani before the browsers' copy failed.
                if f.code == "bind" { await onChange(key) }
            case .cancelled:
                phase = .idle
            }
        }
    }

    nonisolated private static func bindFailure(_ used: ResumeUsed, _ why: String) -> ResumeFailure {
        ResumeFailure(code: "bind", message: "v\(used.version) is marked in use in Vani, but its PDF could not be saved for the browsers (\(why)). Use it again to retry.")
    }

    // MARK: Stop, retry, offers

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
        case let .building(key, allowBase, _):
            flag?.set()
            phase = .building(key: key, allowBase: allowBase, cancelling: true)
        case let .revalidating(key, version, _):
            flag?.set()
            phase = .revalidating(key: key, version: version, cancelling: true)
        default:
            break
        }
    }

    /// The confirmation was closed without confirming. Only that: a build that has begun is stopped by
    /// `cancel()`, never by a sheet going away.
    func dismissConfirmation() {
        guard case .confirming = phase else { return }
        cancel()
    }

    /// Close a result, an error or a note.
    func dismiss() {
        switch phase {
        case .built, .used, .failed: phase = .idle
        default: break
        }
    }

    /// Try the same step again. A build goes back through the cost confirmation, never straight to a spend.
    func retry() {
        guard case let .failed(key, _) = phase, let job = candidate(key), let op = lastOp else { return }
        phase = .idle
        switch op {
        case .estimate: start(job)
        case let .use(v): use(job, version: v)
        case let .revalidate(v): revalidate(job, version: v)
        }
    }

    /// The owner chose what a failure offered. Each goes back through its own confirmation or check:
    /// nothing here spends on its own, and "build the base" only reaches vani from the confirmation that
    /// says Opus cannot run.
    func take(_ offer: ResumeOffer, for job: Job) {
        let version: Int?
        if case let .use(v)? = lastOp { version = v } else if case let .revalidate(v)? = lastOp { version = v } else { version = nil }
        let target = version ?? job.resume?.current
        phase = .idle
        switch offer {
        case .revalidate:
            if let target { revalidate(job, version: target) }
        case .buildAgain, .buildBase:
            start(job)
        }
    }

    // MARK: Preview

    /// The runner the Studio window's editor uses; nil in a host without one.
    func makeClient() -> ResumeStudioClient? { client?() }

    /// A copy of the job's current version's PDF Preview can open. `jobs resume show` names the PDF of any
    /// version that has one, so that is asked first (stored files only, no check). When it cannot answer
    /// (an older vani, a reply this app cannot read), the PDF this process was told the path of is used,
    /// else the browsers' copy when that version is the one in use. Failure is a sentence, never an empty
    /// success.
    func previewCopy(for job: Job, summary: ResumeSummary?) async -> Result<URL, ResumePreviewError> {
        let bank = bankStore()
        let known = knownPDF[job.key]
        let current = summary?.current ?? 0
        let usedIsCurrent = current > 0 && summary?.used == current
        let urls = [job.url, job.applyURL].compactMap { $0 }
        let name = ResumeWords.resumeFileName(company: job.company)
        var asked: String?
        if current > 0, let client {
            let c = client(), key = job.key
            switch await BlockingWork.value({ c.show(key, version: current, check: false) }) {
            case let .success(shown):
                if let v = shown.versions.first(where: { $0.version == current }), let path = v.pdfPath, let sha = v.pdfSHA256, !sha.isEmpty {
                    let copy = await BlockingWork.run { try ResumeStore.previewCopy(name: name, data: try ResumeBinding.loadPDF(path: path, sha256: sha)) }
                    switch copy {
                    case let .success(url): return .success(url)
                    case let .failure(error): return .failure(ResumePreviewError(message: (error as? ResumeStore.Failure)?.message ?? String(describing: error)))
                    }
                }
                if shown.versions.contains(where: { $0.version == current }), shown.versionsError == nil {
                    return .failure(ResumePreviewError(message: "This version has no PDF: its layout was never fitted to pages. Fit it in the Studio window."))
                }
                asked = "Vani did not name this version's PDF"
            case let .failure(f):
                asked = ResumeWords.failure(f)?.message ?? f.message
            case .cancelled:
                asked = "the request was stopped"
            }
        }
        let askedNote = asked
        let result = await BlockingWork.run { () throws -> URL in
            if let known, known.version == current, let sha = known.sha256 {
                let data = try ResumeBinding.loadPDF(path: known.path, sha256: sha)
                return try ResumeStore.previewCopy(name: name, data: data)
            }
            guard usedIsCurrent, let bank else {
                throw ResumePreviewError(message: "This version's PDF is not on hand" + (askedNote.map { " (\($0))" } ?? "")
                    + ". Build or use it from here to open it in Preview, or open the Studio window.")
            }
            let resumes = ResumeStore(directory: bank.directory.appendingPathComponent("resumes", isDirectory: true))
            guard let hit = ResumeStore.match(urls: urls, listed: try resumes.catalog()), let got = try resumes.get(key: hit.key) else {
                throw ResumePreviewError(message: "The browsers have no saved copy of this version. Use it again to save one.")
            }
            return try ResumeStore.previewCopy(name: got.entry.name, data: got.data)
        }
        switch result {
        case let .success(url): return .success(url)
        case let .failure(error):
            if let e = error as? ResumePreviewError { return .failure(e) }
            return .failure(ResumePreviewError(message: String(describing: error)))
        }
    }

    // MARK: Internals

    private func remember(key: String, saved: ResumeSaved) {
        guard let path = saved.pdfPath, saved.version.hasPDF else { return }
        knownPDF[key] = KnownPDF(version: saved.version.version, path: path, sha256: saved.version.pdfSHA256)
    }

    private func begin(key: String, phase next: Phase, op: Op) {
        generation += 1
        flag = CancelFlag()
        lastOp = op
        phase = next
    }
}

/// Why a PDF could not be opened in Preview.
struct ResumePreviewError: Error, Equatable, CustomStringConvertible {
    var message: String
    var description: String { message }
}
