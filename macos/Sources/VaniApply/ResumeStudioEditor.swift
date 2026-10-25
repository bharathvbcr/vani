import Foundation
import Observation
import VaniApplyKit

/// The Resume Studio window's model, one per job window. It shows one version of the job's resume, holds
/// the owner's draft of it, and runs the steps that need vani: `show` (the versions, the editor's pool, the
/// posting's keywords), `render` (a page to look at, nothing saved) and `render --fit` (a new "edited"
/// version). It owns no rule: the draft and its checks are `ResumeDraft` (VaniApplyKit), what Go refuses is
/// shown in `ResumeWords`' sentences, and "Use for this job" and "Revalidate" go through the card's own
/// `ResumeStudioModel`, the one owner of those two steps (one step at a time app-wide, vani's build lock).
///
/// The draft is never lost to a step: a failed Preview or Fit leaves it as it was, a reload that would
/// replace it (a version switch, a reset, a close) asks first (`pending`).
@MainActor
@Observable
final class ResumeStudioEditor {
    enum Loading: Equatable {
        case loading
        case loaded
        /// `show` failed and there is nothing to show yet.
        case failed(ResumeFailure)
    }

    /// The one step this window is running. `fitting(cancelling:)`: Stop was pressed and vani is being ended.
    enum Step: Equatable {
        case loading
        case previewing
        case fitting(cancelling: Bool)
    }

    /// What the owner was about to do that would replace the draft, waiting on Discard or Keep editing.
    enum Pending: Equatable {
        case switchTo(Int)
        case reset
        case close
    }

    struct PDFPreview: Equatable {
        var version: Int
        var data: Data
    }

    /// An unfitted render, read from the file vani left and already deleted (it held the header).
    struct HTMLPreview: Equatable {
        var id: Int
        var page: String
        var checks: ResumeChecks
        var notes: [String]
    }

    let key: String
    private let resume: ResumeStudioModel

    private(set) var job: Job?
    private(set) var loading: Loading = .loading
    private(set) var shown: ResumeShown?
    /// The version the draft was made from (and the one Fit builds on), nil for a job with none yet.
    private(set) var shownVersion: Int?
    private(set) var pool: ResumePool?
    private(set) var seed: ResumeDraft?
    private(set) var draft: ResumeDraft?
    /// Why the shown version's choice could not become a draft, when it could not.
    private(set) var selectionProblem: String?
    private(set) var step: Step?
    /// The last step's failure, as vani said it. Shown until the next step or Dismiss.
    private(set) var failure: ResumeFailure?
    /// What Go refused, put on the row it names (or on the page).
    private(set) var remoteIssues: [ResumeDraft.Issue] = []
    /// A sentence about what just happened ("v5 saved: ...").
    private(set) var notice: String?
    /// Why the last edit was refused.
    private(set) var editNote: String?
    private(set) var pdf: PDFPreview?
    private(set) var html: HTMLPreview?
    /// Why the shown version's PDF could not be drawn.
    private(set) var previewError: String?
    var pending: Pending?
    /// Discard was chosen for a close: the window may now close without asking again.
    private(set) var closeApproved = false

    private var generation = 0
    private var htmlCount = 0
    private var flag = CancelFlag()

    init(key: String, resume: ResumeStudioModel, job: Job?) {
        self.key = key
        self.resume = resume
        self.job = job ?? resume.candidate(key)
    }

    // MARK: Words for the window

    var title: String {
        guard let job else { return "Resume Studio" }
        let name = [job.company, job.title].filter { !$0.isEmpty }.joined(separator: ", ")
        return name.isEmpty ? "Resume Studio" : "Resume — \(name)"
    }

    var failureText: ResumeFailureText? { failure.flatMap(ResumeWords.failure) }

    /// The shown version's record (without its choice, which is the draft's seed).
    var version: ResumeVersion? {
        guard let shownVersion else { return nil }
        return shown?.versions.first { $0.version == shownVersion }
    }

    var versions: [ResumeVersion] { shown?.versions.sorted { $0.version > $1.version } ?? [] }

    var keywordTerms: [String] { shown?.keywords.map(\.term) ?? [] }

    /// What the top bar is not sure of, from `show` itself: never silent.
    var warnings: [String] {
        guard let shown else { return [] }
        var out: [String] = []
        if let e = shown.versionsError, !e.isEmpty { out.append("Not every saved version could be read (\(ResumeWords.clipped(e))). The list may be incomplete.") }
        if let e = shown.checkError, !e.isEmpty {
            out.append("Your knowledge base could not be checked (\(ResumeWords.clipped(e))), so no version is treated as vouched for and editing is paused.")
        }
        if let e = shown.keywordsError, !e.isEmpty { out.append("The posting's keywords could not be read (\(ResumeWords.clipped(e))).") }
        if let e = selectionProblem { out.append(e) }
        return out
    }

    /// The version's eligibility in words: fresh when this `show` checked it, as built otherwise.
    var eligibilityWord: ResumeCheckWord? {
        guard let v = version else { return nil }
        let fresh = (shown?.eligibilityChecked ?? false) && v.eligibility != nil
        return ResumeWords.eligibility(v.eligibility?.state ?? v.checks.eligibility, fresh: fresh)
    }

    var fitWord: ResumeCheckWord? { version.map { ResumeWords.fit($0.checks.fit) } }

    /// "2 pages", "pages not known".
    var pagesWord: String? { version.map { ResumeWords.pagesPhrase($0.pages) } }

    /// rkb's own note about the page ("cut ~2 lines"), as it said it.
    var noteLine: String? { version?.notes.first.map { ResumeWords.clipped($0, 140) } }

    var costWord: String? { ResumeWords.costChip(version?.cost) }

    var sourceWord: String? { version.map { ResumeWords.source($0.source) } }

    /// "Resume_<Company>.pdf", the file Save PDF As offers.
    var suggestedFileName: String { ResumeWords.resumeFileName(company: job?.company ?? "") }

    // MARK: The draft

    var isDirty: Bool { draft != nil && draft != seed }

    var isFitting: Bool {
        if case .fitting? = step { return true }
        return false
    }

    var issues: [ResumeDraft.Issue] { (draft?.issues(pool: pool) ?? []) + remoteIssues }

    var blockingIssues: [ResumeDraft.Issue] { issues.filter(\.blocking) }

    func issues(for target: ResumeDraft.Issue.Target) -> [ResumeDraft.Issue] { issues.filter { $0.target == target } }

    /// The sections whose order may change (see `ResumePool.reorderableSections`).
    var reorderable: Set<String> { pool?.reorderableSections ?? [] }

    /// Why the outline cannot be edited right now, nil when it can.
    var editBlockedReason: String? {
        if draft == nil {
            return loading == .loading ? "Loading…" : (selectionProblem ?? "There is no choice to edit yet.")
        }
        if pool == nil { return "Editing is paused: your knowledge base could not be checked, so what may go where is not known." }
        switch step {
        case .loading?: return "Loading…"
        case .fitting?: return "A fit is running."
        default: return nil
        }
    }

    var canEdit: Bool { editBlockedReason == nil }

    /// A line count for a bullet, only while the draft is exactly the fitted version (a count on a moved or
    /// swapped bullet would be a guess).
    func lines(for bulletID: String) -> Int? {
        guard !isDirty, let v = version, v.checks.fit == "passed" || v.checks.fit == "soft" else { return nil }
        return v.fitLines[bulletID]
    }

    func candidates(in place: ResumeDraft.Place) -> [(bullet: ResumePool.Bullet, hits: [String])] {
        guard let draft, let pool else { return [] }
        let terms = keywordTerms
        let all = draft.candidates(in: place, pool: pool).map { ($0, ResumeKeywordHits.hits(in: $0.text, terms: terms)) }
        // Bullets that answer more of the posting first; the pool's order among equals.
        return all.enumerated().sorted { a, b in
            a.element.1.count != b.element.1.count ? a.element.1.count > b.element.1.count : a.offset < b.offset
        }.map { (bullet: $0.element.0, hits: $0.element.1) }
    }

    func addableEntries(in section: String) -> [ResumePool.Entry] {
        guard let draft, let pool else { return [] }
        return draft.addableEntries(in: section, pool: pool)
    }

    /// Run one edit on the draft. Nothing is sent anywhere; Go's refusal for the old draft is cleared (it was
    /// about a page that no longer is).
    @discardableResult
    func mutate(_ body: (inout ResumeDraft, ResumePool) -> String?) -> String? {
        if let reason = editBlockedReason {
            editNote = reason
            return reason
        }
        guard var d = draft, let pool else { return nil }
        let refusal = body(&d, pool)
        draft = d
        remoteIssues = []
        editNote = refusal
        return refusal
    }

    func setIncluded(_ id: String, _ on: Bool, in place: ResumeDraft.Place) {
        mutate { $0.setIncluded(id, on, in: place, pool: $1) }
    }

    func moveGroup(in place: ResumeDraft.Place, from: Int, toOffset: Int) {
        mutate { d, p in d.moveGroup(in: place, from: from, toOffset: toOffset, pool: p); return nil }
    }

    func addBullet(_ id: String, to place: ResumeDraft.Place) { mutate { $0.addBullet(id, to: place, pool: $1) } }

    func swapBullet(_ old: String, for new: String, in place: ResumeDraft.Place) { mutate { $0.swapBullet(old, for: new, in: place, pool: $1) } }

    func addEntry(_ id: String, to section: String) { mutate { $0.addEntry(id, to: section, pool: $1) } }

    func removeEntry(_ id: String) { mutate { d, _ in d.removeEntry(id); return nil } }

    func moveEntry(_ id: String, by delta: Int) { mutate { d, _ in d.moveEntry(id, by: delta); return nil } }

    func moveSection(_ title: String, by delta: Int) {
        mutate { d, p in d.moveSection(title, by: delta, among: p.reorderableSections); return nil }
    }

    func setOverride(_ field: ResumeOverrideField, _ value: String, entry id: String) {
        mutate { d, _ in d.setOverride(field, value, entry: id); return nil }
    }

    // MARK: Loading

    /// Open the window: every version, with the fresh check and the pool (`show`, up to a minute).
    func open() async {
        await reload(version: nil, check: true)
    }

    /// Ask vani for the job's resumes. `check: false` reads the stored files only (5 s): the versions and the
    /// asked-for version's choice, with the verdicts and the pool of the last full `show` carried over, since
    /// switching versions changes neither the knowledge base nor the posting. `keepDraft` refreshes the list
    /// and leaves the owner's draft and preview alone.
    func reload(version: Int?, check: Bool, keepDraft: Bool = false) async {
        guard let client = resume.makeClient() else {
            loading = .failed(ResumeFailure(code: "config", message: "Vani Apply has no way to run vani in this window."))
            return
        }
        generation += 1
        let gen = generation
        flag = CancelFlag()
        let cancel = flag, key = key
        step = .loading
        let result = await BlockingWork.value { client.show(key, version: version, check: check, cancellation: cancel) }
        guard gen == generation else { return }
        step = nil
        switch result {
        case let .success(s):
            await apply(s, check: check, keepDraft: keepDraft, generation: gen)
        case let .failure(f):
            if shown == nil { loading = .failed(f) } else { failure = f }
        case .cancelled:
            if shown == nil { loading = .failed(ResumeFailure(code: "cancelled", message: "Stopped before anything was shown.")) }
        }
    }

    private func apply(_ incoming: ResumeShown, check: Bool, keepDraft: Bool, generation gen: Int) async {
        var s = incoming
        if !check, let old = shown {
            s.pool = old.pool
            s.eligibilityChecked = old.eligibilityChecked
            s.checkError = old.checkError
            if s.keywords.isEmpty { s.keywords = old.keywords }
            for i in s.versions.indices {
                if let o = old.versions.first(where: { $0.version == s.versions[i].version }) { s.versions[i].eligibility = o.eligibility }
            }
        }
        shown = s
        pool = ResumePool(s.pool)
        guard !keepDraft else {
            loading = .loaded
            return
        }
        let meta = s.version > 0 ? s.versions.first { $0.version == s.version } : nil
        shownVersion = meta?.version
        selectionProblem = nil
        var next: ResumeDraft?
        if let meta {
            next = ResumeDraft.parse(selection: meta.selection, pool: pool)
            if next == nil { selectionProblem = "v\(meta.version)'s saved choice could not be read, so it cannot be edited here." }
        } else if let base = pool?.base {
            next = ResumeDraft.parse(selection: base, pool: pool)
            if next == nil { selectionProblem = "Your base selection could not be read, so there is nothing to start from." }
        }
        seed = next
        draft = next
        remoteIssues = []
        html = nil
        editNote = nil
        loading = .loaded
        await loadPDF(generation: gen)
    }

    private func loadPDF(generation gen: Int) async {
        pdf = nil
        previewError = nil
        guard let v = version, let path = v.pdfPath, let sha = v.pdfSHA256, !sha.isEmpty else { return }
        let result = await BlockingWork.run { try ResumeBinding.loadPDF(path: path, sha256: sha) }
        guard gen == generation else { return }
        switch result {
        case let .success(data): pdf = PDFPreview(version: v.version, data: data)
        case let .failure(e): previewError = "v\(v.version)'s PDF cannot be shown: " + ((e as? ResumeStore.Failure)?.message ?? String(describing: e))
        }
    }

    // MARK: Replacing the draft

    /// Switch the shown version. With unsaved edits it waits for Discard or Keep editing.
    func requestSwitch(to version: Int) async {
        guard version != shownVersion, !isFitting else { return }
        if isDirty {
            pending = .switchTo(version)
            return
        }
        await reload(version: version, check: false)
    }

    /// Start over from the owner's base selection (the pool's `base`).
    func requestReset() async {
        guard !isFitting else { return }
        guard pool?.base != nil else {
            editNote = "Your base selection is not known (the knowledge base could not be checked)."
            return
        }
        if isDirty {
            pending = .reset
            return
        }
        performReset()
    }

    private func performReset() {
        guard let pool, let base = ResumeDraft.parse(selection: pool.base, pool: pool) else {
            editNote = "Your base selection could not be read."
            return
        }
        draft = base
        remoteIssues = []
        html = nil
        editNote = nil
    }

    /// The window's close button: true when it may close now. With unsaved edits it asks first.
    func requestClose() -> Bool {
        if closeApproved || !isDirty {
            shutdown()
            return true
        }
        pending = .close
        return false
    }

    /// Discard was chosen: do what was waiting.
    func confirmPending() async {
        guard let p = pending else { return }
        pending = nil
        switch p {
        case let .switchTo(v):
            await reload(version: v, check: false)
        case .reset:
            performReset()
        case .close:
            closeApproved = true
        }
    }

    func keepEditing() { pending = nil }

    /// The window is closing: stop what is running. A page vani left for Preview is deleted by the step
    /// that reads it, which runs whether or not anyone is still looking.
    func shutdown() {
        generation += 1
        flag.set()
        step = nil
    }

    // MARK: Preview and Fit

    private func editData() -> Data? {
        guard let draft else { return nil }
        do { return try draft.editJSON() } catch {
            failure = ResumeFailure(code: "bad_edit", message: "the edit could not be written: \(error.localizedDescription)")
            return nil
        }
    }

    /// Why Preview cannot run now.
    var previewBlockedReason: String? {
        if let r = editBlockedReason { return r }
        if step != nil { return "Another step of this window is running." }
        if let first = blockingIssues.first { return first.message }
        if resume.isBusy { return "Another resume step is running. Wait for it to finish." }
        return nil
    }

    /// Why Fit cannot run now.
    var fitBlockedReason: String? {
        if let r = previewBlockedReason { return r }
        if !isDirty, let v = version, v.hasPDF { return "Nothing changed since v\(v.version) was fitted." }
        return nil
    }

    /// Render the draft without fitting it: a page to look at, nothing saved, no model.
    func preview() async {
        guard previewBlockedReason == nil, let client = resume.makeClient(), let edit = editData(), resume.claimRender() else { return }
        generation += 1
        let gen = generation
        flag = CancelFlag()
        let cancel = flag, key = key
        step = .previewing
        failure = nil
        notice = nil
        let result = await BlockingWork.value { client.renderPreview(key, edit: edit, cancellation: cancel) }
        resume.releaseRender()
        switch result {
        case let .success(rendered):
            // Read and delete the page now, whatever became of the window: it holds the header.
            let path = rendered.htmlPath
            let page = await BlockingWork.run { try ResumePreviewFile.take(path: path) }
            guard gen == generation else { return }
            step = nil
            switch page {
            case let .success(text):
                htmlCount += 1
                html = HTMLPreview(id: htmlCount, page: text, checks: rendered.checks, notes: rendered.notes)
            case let .failure(e):
                failure = ResumeFailure(code: "render", message: (e as? ResumePreviewFile.Failure)?.message ?? String(describing: e))
            }
        case let .failure(f):
            guard gen == generation else { return }
            step = nil
            fail(f)
        case .cancelled:
            guard gen == generation else { return }
            step = nil
        }
    }

    /// Fit the draft in Chrome and save it as a new version made from the shown one (`render --fit`, up to
    /// 275 s, no model), then show that version.
    func fit() async {
        guard fitBlockedReason == nil, let client = resume.makeClient(), let edit = editData(), resume.claimRender() else { return }
        generation += 1
        let gen = generation
        flag = CancelFlag()
        let cancel = flag, key = key, from = shownVersion
        step = .fitting(cancelling: false)
        failure = nil
        notice = nil
        let result = await BlockingWork.value { client.renderFit(key, edit: edit, from: from, cancellation: cancel) }
        resume.releaseRender()
        switch result {
        case let .success(saved):
            // The version exists whether or not this window is still open: tell the card.
            await resume.noteSaved(key: key, saved: saved)
            guard gen == generation else { return }
            step = nil
            notice = ResumeWords.savedLine(saved)
            pending = nil
            await reload(version: saved.version.version, check: true)
            if shownVersion != saved.version.version {
                // The version is saved; only the look at it failed. The draft stays as it is.
                notice = ResumeWords.savedLine(saved) + " This window could not reload it: close and reopen it to see it."
            }
        case let .failure(f):
            guard gen == generation else { return }
            step = nil
            fail(f)
        case .cancelled:
            guard gen == generation else { return }
            step = nil
            // A version saved a moment before the stop is kept (vani writes it on a context a stop does not
            // end): look, and leave the draft alone.
            await reload(version: shownVersion, check: false, keepDraft: true)
        }
    }

    /// Stop what is running (the child gets SIGTERM and is reaped).
    func stop() {
        switch step {
        case .previewing?: flag.set()
        case .fitting?:
            flag.set()
            step = .fitting(cancelling: true)
        default: break
        }
    }

    func dismissFailure() {
        failure = nil
        remoteIssues = []
    }

    /// Show the saved version's PDF again, not the unfitted render.
    func showSaved() { html = nil }

    /// A refusal from Go, on the row it names when it names one: a quoted id the draft holds.
    private func fail(_ f: ResumeFailure) {
        failure = f
        remoteIssues = []
        guard let text = ResumeWords.failure(f), let draft else { return }
        var targets: [ResumeDraft.Issue.Target] = []
        for s in draft.sections {
            for b in s.bullets where f.message.contains("\"\(b.id)\"") { targets.append(.bullet(b.id)) }
            for e in s.entries {
                if f.message.contains("\"\(e.id)\"") { targets.append(.entry(e.id)) }
                for b in e.bullets where f.message.contains("\"\(b.id)\"") { targets.append(.bullet(b.id)) }
            }
        }
        guard f.code == "ineligible" || f.code == "bad_edit" || f.code == "pages" || f.code == "figures" || f.code == "lint" else { return }
        remoteIssues = (targets.isEmpty ? [.page] : targets).map { ResumeDraft.Issue(target: $0, message: text.message) }
    }

    // MARK: Use, Revalidate (the card's own steps)

    /// What "Use for this job" does for the shown version, from its fresh states.
    var useAction: ResumeUseAction {
        guard let v = version else { return .none }
        let state = v.eligibility?.state ?? v.checks.eligibility
        return ResumeCardPresentation.useAction(ResumeSummary(current: v.version, used: v.used ? v.version : 0, versions: 1, pages: v.pages,
                                                              fit: v.checks.fit, eligibility: state, source: v.source))
    }

    /// Why Use cannot run now; nil when it can.
    var useBlockedReason: String? {
        if isDirty { return "Fit your edits to save them as a version first. Use works on a saved version." }
        if let step { return step == .loading ? "Loading…" : "A step of this window is running." }
        guard let job, resume.isAvailable else { return "This job is not in the Jobs list right now. Select it there and open the Studio again." }
        if let reason = resume.blockedReason(for: job) { return reason }
        switch useAction {
        case .none: return "There is no saved version to use yet. Fit your draft first."
        case let .inUse(v): return "v\(v) is already in use for this job."
        case let .blocked(_, reason, _): return reason
        case .available: return nil
        }
    }

    /// Revalidate is what the version needs: it is out of date or was not checked, and nothing is unsaved.
    var canRevalidate: Bool {
        guard !isDirty, step == nil, let job, resume.blockedReason(for: job) == nil else { return false }
        if case .blocked(_, _, .revalidate) = useAction { return true }
        return false
    }

    func use() {
        guard useBlockedReason == nil, let job, let v = shownVersion else { return }
        resume.use(job, version: v)
    }

    func revalidate() {
        guard canRevalidate, let job, let v = shownVersion else { return }
        resume.revalidate(job, version: v)
    }

    /// The card's step for this job ended (used, revalidated, built): look again. A new version made while
    /// the draft is clean is shown; with unsaved edits the list is refreshed and the draft kept.
    func phaseChanged(_ phase: ResumeStudioModel.Phase) async {
        guard phase.key == key else { return }
        switch phase {
        case .used:
            await reload(version: shownVersion, check: true, keepDraft: isDirty)
        case let .built(_, saved):
            if isDirty {
                await reload(version: shownVersion, check: false, keepDraft: true)
                notice = ResumeWords.savedLine(saved) + " Your edits here are unchanged."
            } else {
                await reload(version: saved.version.version, check: true)
                notice = ResumeWords.savedLine(saved)
            }
        default:
            break
        }
    }

    // MARK: Files

    enum FileFailure: Error, Equatable {
        case none
        case message(String)
        var text: String {
            switch self {
            case .none: "This version has no PDF: its layout was never fitted to pages."
            case let .message(m): m
            }
        }
    }

    /// The shown version's PDF, checked against the hash vani reported. Save PDF As, Open in Preview and
    /// Reveal all start here.
    func pdfBytes() async -> Result<Data, FileFailure> {
        guard let v = version, let path = v.pdfPath, let sha = v.pdfSHA256, !sha.isEmpty else { return .failure(.none) }
        let result = await BlockingWork.run { try ResumeBinding.loadPDF(path: path, sha256: sha) }
        switch result {
        case let .success(d): return .success(d)
        case let .failure(e): return .failure(.message((e as? ResumeStore.Failure)?.message ?? String(describing: e)))
        }
    }

    /// A copy for Preview.app (a temporary copy under the owner's file name; the store's own file is not opened).
    func previewCopy() async -> Result<URL, FileFailure> {
        switch await pdfBytes() {
        case let .failure(f): return .failure(f)
        case let .success(data):
            let name = suggestedFileName
            switch await BlockingWork.run({ try ResumeStore.previewCopy(name: name, data: data) }) {
            case let .success(url): return .success(url)
            case let .failure(e): return .failure(.message((e as? ResumeStore.Failure)?.message ?? String(describing: e)))
            }
        }
    }

    /// The PDF's path, for Reveal in Finder, when the shown version has a PDF that is still there.
    var pdfURL: URL? {
        guard let path = version?.pdfPath, FileManager.default.fileExists(atPath: path) else { return nil }
        return URL(fileURLWithPath: path)
    }

    var hasPDF: Bool { version?.hasPDF ?? false }
}
