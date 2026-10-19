import Foundation
import Observation
import VaniApplyKit

/// State for the Jobs tab. Every read and write is a `vani jobs` call off the
/// main thread; a list is about a tenth of a second, so after each change the
/// model simply re-lists rather than patching its copy.
@MainActor
@Observable
final class JobsModel {
    var view: JobView = .new {
        didSet { if oldValue != view { selection = nil; reload() } }
    }
    var query = ""
    var selection: String? {
        didSet { if oldValue != selection { loadDetail() } }
    }

    private(set) var page: JobsPage?
    private(set) var loading = false
    private(set) var error: String?
    /// The selected job as `vani jobs show` returned it (with its description).
    private(set) var detail: Job?
    private(set) var detailError: String?
    /// The last failed mark or note, shown as an alert.
    var actionError: String?

    private let client: () -> JobsClient
    private var listGeneration = 0
    private var detailGeneration = 0

    init(client: @escaping () -> JobsClient) {
        self.client = client
    }

    var jobs: [Job] { page?.jobs ?? [] }

    /// The selected job: the full one once shown, the list's copy until then.
    var selected: Job? {
        guard let selection else { return nil }
        if let detail, detail.key == selection { return detail }
        return jobs.first { $0.key == selection }
    }

    func reload() {
        Task { await refresh() }
    }

    func refresh() async {
        listGeneration += 1
        let gen = listGeneration
        loading = true
        let c = client(), v = view, q = query
        let result = await Task.detached(priority: .userInitiated) { Result { try c.list(v, query: q) } }.value
        guard gen == listGeneration else { return } // a newer list superseded this one
        loading = false
        switch result {
        case let .success(p):
            page = p
            error = nil
        case let .failure(err):
            error = String(describing: err)
        }
    }

    private func loadDetail() {
        detailGeneration += 1
        let gen = detailGeneration
        detailError = nil
        guard let key = selection else { detail = nil; return }
        if detail?.key != key { detail = nil }
        let c = client()
        Task {
            let result = await Task.detached(priority: .userInitiated) { Result { try c.show(key) } }.value
            guard gen == detailGeneration else { return }
            switch result {
            case let .success(j): detail = j
            case let .failure(err): detailError = String(describing: err)
            }
        }
    }

    /// Set (or, with nil, clear) a job's mark. When the job leaves the current
    /// view, selection moves to the job that took its place, so a list can be
    /// worked through from the keyboard.
    func setMark(_ key: String, _ mark: JobMark?) async {
        let index = jobs.firstIndex { $0.key == key }
        let c = client()
        let result = await Task.detached(priority: .userInitiated) { Result { try c.mark(key, mark) } }.value
        switch result {
        case let .success(j):
            if detail?.key == key { detail = merged(j) }
        case let .failure(err):
            actionError = String(describing: err)
            return
        }
        await refresh()
        if selection == key, !jobs.contains(where: { $0.key == key }) {
            selection = index.flatMap { jobs.isEmpty ? nil : jobs[min($0, jobs.count - 1)].key }
        }
    }

    /// Toggle: marking a job with the mark it already has clears it.
    func toggle(_ job: Job, _ mark: JobMark) async {
        await setMark(job.key, job.mark == mark ? nil : mark)
    }

    @discardableResult
    func saveNote(_ key: String, _ text: String) async -> Bool {
        let c = client()
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let result = await Task.detached(priority: .userInitiated) { Result { try c.note(key, trimmed) } }.value
        switch result {
        case let .success(j):
            if detail?.key == key { detail = merged(j) }
            await refresh()
            return true
        case let .failure(err):
            actionError = String(describing: err)
            return false
        }
    }

    /// A mark or note reply carries no description; keep the one already shown.
    private func merged(_ j: Job) -> Job {
        var out = j
        if out.description == nil { out.description = detail?.description }
        return out
    }
}
