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
        didSet {
            guard oldValue != view else { return }
            selection = nil
            selectedKeys = []
            // A sort still at the old view's default follows the view; one the owner
            // picked stays. Setting `sort` relists, so only one list is asked for.
            let next = JobSort.defaultSort(for: view)
            // A picked order the new view cannot offer (priority in Awaiting triage)
            // also gives way to the view's own.
            if (sort == JobSort.defaultSort(for: oldValue) || !JobSort.options(for: view).contains(sort)), sort != next {
                sort = next
            } else {
                reload()
            }
        }
    }
    var query = ""
    var sort: JobSort = JobSort.defaultSort(for: .new) {
        didSet { if oldValue != sort { reload() } }
    }
    var filter = JobFilter() {
        didSet { if oldValue != filter { reload() } }
    }
    /// The Insights pane (charts over the current selection) is open.
    var showInsights = false {
        didSet { if showInsights, !oldValue { reload() } }
    }
    /// The one open role, shown in the detail pane; nil when none is, or several are selected. Setting
    /// it selects that role alone.
    var selection: String? {
        didSet {
            if oldValue != selection { loadDetail() }
            if let selection {
                if selectedKeys != [selection] { selectedKeys = [selection] }
            } else if selectedKeys.count == 1 {
                selectedKeys = []
            }
        }
    }
    /// Every selected row (⌘-click, ⇧-click). One key makes it the open role (`selection`); several
    /// show the selection pane, whose action is the batch re-triage. Mark shortcuts stay one-role.
    var selectedKeys: Set<String> = [] {
        didSet {
            guard oldValue != selectedKeys else { return }
            let single = selectedKeys.count == 1 ? selectedKeys.first : nil
            if selection != single { selection = single }
        }
    }

    /// Several rows are selected.
    var isMultiSelection: Bool { selectedKeys.count > 1 }

    /// The selected roles in list order (selected rows a reload removed are left out).
    var selectedJobs: [Job] { jobs.filter { selectedKeys.contains($0.key) } }

    private(set) var page: JobsPage?
    /// Distributions over the selection, loaded with the list while Insights is open.
    private(set) var stats: JobsStats?
    private(set) var statsError: String?
    /// "Skills to build" (`vani jobs gaps`) over the same selection, loaded with the stats.
    /// Display-only: nothing here reaches the list. Cleared when a reload fails, so a
    /// failure never sits under another selection's skills.
    private(set) var gaps: JobsGaps?
    private(set) var gapsError: String?
    private(set) var loading = false
    private(set) var error: String?
    /// The selected job as `vani jobs show` returned it (with its description).
    private(set) var detail: Job?
    private(set) var detailError: String?
    /// The last failed mark or note, shown as an alert.
    var actionError: String?
    /// An export is being written.
    private(set) var exporting = false
    /// The workbook the last export wrote and what it holds, shown as an alert.
    var exported: (url: URL, summary: String)?

    /// The "Saved searches" section is open. It reads the watchlist when opened.
    var showSearches = false {
        didSet { if showSearches, !oldValue { reloadSearches() } }
    }
    private(set) var searches: [WatchlistEntry] = []
    /// Why the watchlist could not be read; shown in place of the list, never as an empty list.
    private(set) var searchesError: String?
    private(set) var searchesLoading = false

    private let client: () -> JobsClient
    private var searchesGeneration = 0
    private var listGeneration = 0
    private var detailGeneration = 0

    /// "Re-triage with Sonnet": its state is shared by the detail pane and the row menu, and a
    /// reading made or cleared re-lists the jobs here. `retriage` builds the runner that holds the
    /// draft keys; without one (a test host) the action is not offered.
    let retriage: RetriageModel
    /// "Re-triage the selection with Sonnet": the same runner, many roles; busy for `retriage` and it
    /// for this.
    let batch: RetriageBatchModel

    /// "Build a tailored resume" and "Use for this job": the same detail pane and row menu, its own one
    /// operation at a time. A version built, revalidated or used re-shows the open posting, whose
    /// `resume` summary is what vani now reports. Without a runner (a test host) the actions are not offered.
    let resume: ResumeStudioModel

    init(client: @escaping () -> JobsClient, retriage: (() -> RetriageClient)? = nil,
         resume: (() -> ResumeStudioClient)? = nil, bankStore: @escaping () -> BankStore? = { nil }) {
        self.client = client
        self.resume = ResumeStudioModel(client: resume, bankStore: bankStore)
        self.retriage = RetriageModel(client: retriage)
        self.batch = RetriageBatchModel(client: retriage.map { make in { RetriageBatchClient(runner: make().runner) } })
        self.resume.onChange = { [weak self] key in self?.reloadAfterResume(key) }
        self.resume.candidate = { [weak self] key in
            guard let self else { return nil }
            return self.detail?.key == key ? self.detail : self.jobs.first { $0.key == key }
        }
        self.retriage.otherBusy = { [weak self] in self?.batch.isBusy ?? false }
        self.batch.otherBusy = { [weak self] in self?.retriage.isBusy ?? false }
        self.batch.onManualChange = { [weak self] in
            guard let self else { return }
            await refresh()
            if selection != nil { loadDetail() }
        }
        self.retriage.onManualChange = { [weak self] key in await self?.reloadAfterManualTriage(key) }
        self.retriage.candidate = { [weak self] key in
            guard let self else { return nil }
            return self.detail?.key == key ? self.detail : self.jobs.first { $0.key == key }
        }
    }

    /// Re-list (the reading moves the job in the order) and re-read the open posting, so its
    /// `manual_triage` and score factors are the ones vani now reports.
    private func reloadAfterManualTriage(_ key: String) async {
        await refresh()
        if selection == key { loadDetail() }
    }

    /// Re-read the open posting after its resume changed, so its `resume` summary is the one vani reports.
    private func reloadAfterResume(_ key: String) {
        if selection == key { loadDetail() }
    }

    /// Build a tailored resume from a row or the detail: show the posting (the progress, the answer and any
    /// error appear in its detail pane) and ask vani for the cost.
    func startResume(_ job: Job) {
        guard resume.isAvailable, resume.blockedReason(for: job) == nil else { return }
        selection = job.key
        resume.start(job)
    }

    /// Open the Resume Studio window for a job (the window for a job already open is brought forward).
    /// The posting is remembered by key so the window keeps its title and its Use after the list changes.
    func openStudio(_ job: Job) {
        guard resume.isAvailable, let open = resume.openStudio else { return }
        resume.studioJobs[job.key] = job
        open(job.key)
    }

    /// Re-triage from a row or the detail: show the posting (the progress, the answer and any error
    /// appear in its detail pane) and ask vani for the cost.
    func startRetriage(_ job: Job) {
        guard retriage.blockedReason(for: job) == nil else { return }
        selection = job.key
        retriage.start(job)
    }

    /// Re-triage every selected role at once: ask vani for the cost, then confirm.
    func startBatchRetriage() {
        batch.start(selectedJobs)
    }

    /// Select the first `n` of `shown` (the rows on screen, in their order) and ask what
    /// re-triaging them would cost. One role just opens. A blocked selection stays selected
    /// and is not asked. Nothing is spent here: the batch still confirms the cost first.
    func selectTopForTriage(_ n: Int, in shown: [Job]) {
        let picked = RetriageBatchPresentation.topJobs(shown, limit: n)
        guard !picked.isEmpty else { return }
        selectedKeys = Set(picked.map(\.key))
        guard picked.count > 1, batch.isAvailable, batch.blockedReason(for: picked) == nil else { return }
        batch.start(picked)
    }

    /// Select the roles whose fit Sonnet changed in the last batch, to look them over.
    func selectChanged() {
        let keys = Set(batch.changedKeys).intersection(jobs.map(\.key))
        guard !keys.isEmpty else { return }
        selectedKeys = keys
    }

    /// Ask again about the roles of the last batch that failed or were not asked.
    func retryBatch() {
        let keys = Set(batch.retryable)
        let again = jobs.filter { keys.contains($0.key) }
        batch.dismiss()
        guard !again.isEmpty else { return }
        selectedKeys = Set(again.map(\.key))
        batch.start(again)
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

    /// The app came to the front: a mark may have been written meanwhile (the browser
    /// extension marks a job Applied through `vani jobs mark`), so re-read the list and
    /// the open job rather than show the state from before.
    func reloadAfterActivation() {
        reload()
        if showSearches { reloadSearches() }
        if selection != nil { loadDetail() }
    }

    /// Why the label table is the built-in fallback (vani's could not be read), or
    /// nil once vani's own is loaded. Shown beside the filters: fallback words are
    /// never presented as vani's.
    private(set) var labelsNote: String? = "Vani's label table has not been read yet; showing built-in labels."
    private var labelsLoaded = false

    /// Read `vani labels --json` once; on failure keep the fallback, say so in
    /// `labelsNote` and try again at the next refresh.
    func loadLabels() async {
        guard !labelsLoaded else { return }
        let c = client()
        let result = await BlockingWork.run { try c.labels() }
        switch result {
        case let .success(table):
            Labels.install(table)
            labelsLoaded = true
            labelsNote = nil
        case let .failure(err):
            Labels.useFallback()
            labelsNote = "Showing built-in labels: vani labels could not be read (\(err))"
        }
    }

    func refresh() async {
        await loadLabels()
        listGeneration += 1
        let gen = listGeneration
        loading = true
        // Stats and gaps chart fit, relevance and priority, which postings awaiting
        // triage have none of: vani refuses them, so they are not asked.
        let c = client(), v = view, q = query, s = sort, f = filter, wantStats = showInsights && view.isRanked
        async let listed = BlockingWork.run { try c.list(v, query: q, sort: s, filter: f) }
        async let counted: Result<JobsStats, Error>? = wantStats
            ? BlockingWork.run { try c.stats(v, query: q, filter: f) }
            : nil
        async let gapped: Result<JobsGaps, Error>? = wantStats
            ? BlockingWork.run { try c.gaps(v, query: q, filter: f) }
            : nil
        let (result, statsResult, gapsResult) = await (listed, counted, gapped)
        guard gen == listGeneration else { return } // a newer list superseded this one
        loading = false
        switch result {
        case let .success(p):
            page = p
            error = nil
            // Only ranked roles were put in front of the owner to pass over; an
            // unranked posting awaiting triage is not "seen and passed".
            if v.isRanked {
                await reportImpressions(p.jobs.prefix(JobsClient.impressionRows).map(\.key), view: v)
            }
        case let .failure(err):
            error = String(describing: err)
        }
        switch statsResult {
        case let .success(st)?:
            stats = st
            statsError = nil
        case let .failure(err)?:
            statsError = String(describing: err)
        case nil:
            break
        }
        switch gapsResult {
        case let .success(g)?:
            gaps = g
            gapsError = nil
        case let .failure(err)?:
            gaps = nil
            gapsError = String(describing: err)
        case nil:
            break
        }
    }

    /// Why the rows on screen could not be reported to `vani impressions log`;
    /// nil when they were. Reporting is measurement only: a failure never blocks
    /// the list, and it is kept here rather than dropped.
    private(set) var impressionsError: String?

    /// Tells vani which rows the list put on screen (keys and positions only), so
    /// an unmarked job counts as seen-and-passed. One slate per view per day:
    /// refreshing again the same day logs only what is new.
    private func reportImpressions(_ keys: [String], view: JobView) async {
        let c = client(), slate = JobsClient.jobsSlate(view: view, day: Date())
        let result = await BlockingWork.run {
            try c.logImpressions(surface: .jobs, slate: slate, section: view.rawValue, keys: keys)
        }
        switch result {
        case .success: impressionsError = nil
        case let .failure(err): impressionsError = String(describing: err)
        }
    }

    func reloadSearches() {
        Task { await refreshSearches() }
    }

    /// Read `vani watchlist list --json`. A failure keeps the message and clears the list, so
    /// stale boards are not shown beside the reason they could not be refreshed.
    func refreshSearches() async {
        searchesGeneration += 1
        let gen = searchesGeneration
        searchesLoading = true
        let c = client()
        let result = await BlockingWork.run { try c.watchlist() }
        guard gen == searchesGeneration else { return }
        searchesLoading = false
        switch result {
        case let .success(list):
            searches = list
            searchesError = nil
        case let .failure(err):
            searches = []
            searchesError = String(describing: err)
        }
    }

    /// Turn every filter off.
    func clearFilter() { filter.clear() }

    private func loadDetail() {
        detailGeneration += 1
        let gen = detailGeneration
        detailError = nil
        guard let key = selection else { detail = nil; return }
        if detail?.key != key { detail = nil }
        let c = client()
        Task {
            let result = await BlockingWork.run { try c.show(key) }
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
        let result = await BlockingWork.run { try c.mark(key, mark) }
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

    /// Set the stage of an applied job, or with nil set it back to plain applied. The job stays Applied, so
    /// it stays in its view and selection does not move.
    func setStage(_ key: String, _ stage: JobStage?) async {
        let c = client()
        let result = await BlockingWork.run { try c.stage(key, stage) }
        switch result {
        case let .success(j):
            if detail?.key == key { detail = merged(j) }
        case let .failure(err):
            actionError = String(describing: err)
            return
        }
        await refresh()
    }

    /// Adds the job's company to the exclusions Settings edits, and saves them;
    /// the re-list then hides its unmarked roles, and the next run drops them.
    /// Selection moves on as it does when a mark takes a job out of the view.
    func excludeCompany(of job: Job, using exclusions: ExclusionsEditor) async {
        let index = jobs.firstIndex { $0.key == job.key }
        if let problem = await exclusions.add(job.company, to: \.companies) {
            actionError = problem
            return
        }
        await refresh()
        if selection == job.key, !jobs.contains(where: { $0.key == job.key }) {
            selection = index.flatMap { jobs.isEmpty ? nil : jobs[min($0, jobs.count - 1)].key }
        }
    }

    /// What exporting `scope` of the list as it is viewed would write, written nowhere: how many of
    /// those roles an earlier export wrote. Nil when the check failed (`actionError` says why).
    func checkExport(_ scope: JobsExportScope) async -> JobsExport? {
        guard !exporting else { return nil }
        exporting = true
        defer { exporting = false }
        let c = client(), v = view, q = query, s = sort, f = filter
        switch await BlockingWork.run({ try c.exportCheck(v, query: q, sort: s, filter: f, scope) }) {
        case let .success(reply): return reply
        case let .failure(err):
            actionError = "Export failed: \(err)"
            return nil
        }
    }

    /// Write the list as it is viewed, sorted and filtered (its first N roles, or the roles `scope`
    /// names) to `url` as an Excel workbook. `already` is the owner's answer about roles exported
    /// before; nil when the check found none, so vani refuses if one was exported meanwhile.
    func export(_ scope: JobsExportScope, to url: URL, already: JobsAlreadyExported?) async {
        guard !exporting else { return }
        exporting = true
        defer { exporting = false }
        let c = client(), v = view, q = query, s = sort, f = filter
        let result = await BlockingWork.run { try c.export(v, query: q, sort: s, filter: f, scope, to: url, already: already) }
        switch result {
        case let .success(reply): exported = (URL(fileURLWithPath: reply.path), reply.summary(scope))
        case let .failure(err): actionError = "Export failed: \(err)"
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
        let result = await BlockingWork.run { try c.note(key, trimmed) }
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

    /// A mark or note reply carries no description and no `resume` summary (only `jobs show` does); keep
    /// the ones already shown, so saving a job never turns its resumes into "no resume".
    private func merged(_ j: Job) -> Job {
        var out = j
        if out.description == nil { out.description = detail?.description }
        if out.resume == nil, detail?.key == out.key { out.resume = detail?.resume }
        return out
    }
}
