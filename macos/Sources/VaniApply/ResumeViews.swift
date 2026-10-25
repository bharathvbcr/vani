import AppKit
import SwiftUI
import VaniApplyKit

/// The detail pane's "Tailored resume" card: the job's current version in honest words (what ran and what
/// did not), the action that is next, and the progress, result or error of the one resume step in flight
/// for this posting. Logic is `ResumeStudioModel`, `ResumeCardPresentation` and `ResumeWords`; this only
/// draws it.
struct ResumeSection: View {
    @Environment(JobsModel.self) private var jobs
    let job: Job
    /// False while the note has focus: the shortcut then belongs to the note, as ⌘S and ⌘⌫ do.
    let shortcutEnabled: Bool
    @State private var previewing = false
    @State private var previewError: String?

    private var resume: ResumeStudioModel { jobs.resume }
    /// This posting's state; another posting's is not shown here.
    private var phase: ResumeStudioModel.Phase { resume.phase.key == job.key ? resume.phase : .idle }
    private var summary: ResumeSummary? { jobs.detail?.key == job.key ? jobs.detail?.resume : job.resume }
    private var card: ResumeCard { ResumeCardPresentation.card(for: summary) }
    private var workingOnThis: Bool {
        switch phase {
        case .estimating, .confirming, .building, .revalidating, .using: true
        default: false
        }
    }

    var body: some View {
        if resume.isAvailable {
            VStack(alignment: .leading, spacing: 10) {
                HStack(alignment: .firstTextBaseline) {
                    Text("Tailored resume").font(.headline)
                    Spacer()
                    Text("\(ResumeWords.modelName): you confirm the cost first").font(.caption).foregroundStyle(.secondary)
                }
                stateView
                if !workingOnThis {
                    summaryView
                    actionRow
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .cardStyle()
            .accessibilityElement(children: .contain)
            .accessibilityLabel("Tailored resume")
            .accessibilityIdentifier("resume.card")
        }
    }

    // MARK: Phase

    @ViewBuilder
    private var stateView: some View {
        switch phase {
        case .idle:
            EmptyView()
        case .estimating:
            progress("Asking Vani what this would cost…", detail: "Nothing is spent yet.", stoppable: true)
        case .confirming:
            progress("Waiting for your confirmation…", detail: "Nothing is spent until you confirm.", stoppable: false)
        case let .building(_, allowBase, cancelling):
            progress(cancelling ? "Stopping…" : (allowBase ? "Building the base selection…" : "Asking \(ResumeWords.modelName)…"),
                     detail: cancelling ? "Vani is being stopped. A version saved a moment before the stop still appears here."
                                        : "This can take up to \(Int(ResumeStudioClient.Timeouts.standard.build / 60)) minutes. Stop keeps nothing half-made.",
                     stoppable: !cancelling)
        case let .revalidating(_, version, cancelling):
            progress(cancelling ? "Stopping…" : "Revalidating v\(version)…", detail: "Dropping what your knowledge base no longer vouches for, then fitting the page. Nothing is spent.",
                     stoppable: !cancelling)
        case let .using(_, version):
            progress("Checking v\(version) and saving it for the browsers…", detail: "A version that is out of date or could not be checked is refused.", stoppable: false)
        case let .built(_, saved):
            built(saved)
        case let .used(_, version, note):
            usedNote(version: version, note: note)
        case let .failed(_, failure):
            failureView(failure)
        }
    }

    private func progress(_ title: String, detail: String, stoppable: Bool) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            ProgressView().controlSize(.small)
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                Text(detail).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            Spacer()
            if stoppable {
                Button("Cancel") { resume.cancel() }
                    .accessibilityIdentifier("resume.cancel")
                    .help("Stop. Nothing more is asked or spent.")
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("resume.progress")
    }

    private func built(_ saved: ResumeSaved) -> some View {
        let v = saved.version
        let action = ResumeCardPresentation.useAction(forSaved: v)
        return VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .firstTextBaseline) {
                Label(ResumeWords.savedLine(saved), systemImage: "doc.badge.checkmark")
                    .font(.subheadline.weight(.semibold))
                    .fixedSize(horizontal: false, vertical: true)
                Spacer()
                Button("Done") { resume.dismiss() }.accessibilityIdentifier("resume.done")
            }
            if let cost = ResumeWords.costLine(v.cost) {
                Text(verbatim: cost).font(.caption).foregroundStyle(.secondary)
            }
            ForEach(v.notes, id: \.self) { note in
                Text(verbatim: note).font(.caption).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
            }
            if !v.unverifiedDropped.isEmpty {
                Text("Left off: a figure the validator does not back (\(v.unverifiedDropped.joined(separator: ", "))).")
                    .font(.caption).foregroundStyle(.secondary)
            }
            switch action {
            case .available:
                Button("Use v\(v.version) for this job") { resume.use(job, version: v.version) }
                    .accessibilityIdentifier("resume.useBuilt")
                    .help("Check this version afresh, then save it for the browsers under this job's addresses.")
            case let .blocked(_, reason, _):
                Text(reason).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            default:
                EmptyView()
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("resume.built")
    }

    private func usedNote(version: Int, note: String) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline) {
                Label("v\(version) is in use for this job.", systemImage: "checkmark.circle.fill")
                    .foregroundStyle(.green)
                Spacer()
                Button("Done") { resume.dismiss() }.accessibilityIdentifier("resume.done")
            }
            Text(verbatim: note).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            Text("Open the application page and press Fill in the Vani Apply extension: it attaches this resume.")
                .font(.caption).foregroundStyle(.secondary)
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("resume.used")
    }

    @ViewBuilder
    private func failureView(_ failure: ResumeFailure) -> some View {
        if let text = ResumeWords.failure(failure) {
            VStack(alignment: .leading, spacing: 6) {
                Label {
                    Text(verbatim: text.message).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                } icon: {
                    Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                }
                if let cost = text.costNote {
                    Text(verbatim: cost).font(.callout).fixedSize(horizontal: false, vertical: true)
                }
                HStack {
                    if let offer = text.offer { offerButton(offer) }
                    if text.canRetry, text.offer == nil {
                        Button("Retry") { resume.retry() }
                            .accessibilityIdentifier("resume.retry")
                            .help("Try the step again. A build asks for the cost again and you confirm before anything is spent.")
                    }
                    Button("Dismiss") { resume.dismiss() }.accessibilityIdentifier("resume.dismiss")
                }
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("resume.error")
        }
    }

    @ViewBuilder
    private func offerButton(_ offer: ResumeOffer) -> some View {
        switch offer {
        case .revalidate:
            Button("Revalidate") { resume.take(.revalidate, for: job) }
                .accessibilityIdentifier("resume.revalidate")
                .help("Drop what your knowledge base no longer vouches for and rebuild the page. No model, nothing is spent.")
        case .buildAgain:
            Button("Build again…") { resume.take(.buildAgain, for: job) }
                .accessibilityIdentifier("resume.buildAgain")
                .help("Ask for the cost again. You confirm before anything is spent.")
        case .buildBase:
            Button("Build base anyway…") { resume.take(.buildBase, for: job) }
                .accessibilityIdentifier("resume.buildBaseAnyway")
                .help("Asks for the estimate again; the choice to build your base selection without the model is on that confirmation.")
        }
    }

    // MARK: The job's resume

    @ViewBuilder
    private var summaryView: some View {
        switch card.kind {
        case .none:
            Text("No tailored resume yet. A build picks the bullets from your knowledge base that answer this posting.")
                .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("resume.none")
        case let .unreadable(words):
            Label {
                Text(verbatim: words).fixedSize(horizontal: false, vertical: true)
            } icon: {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
            }
            .accessibilityIdentifier("resume.unreadable")
        case .ready:
            VStack(alignment: .leading, spacing: 6) {
                if let headline = card.headline {
                    WrapLayout(spacing: 6) {
                        Text(verbatim: headline).font(.subheadline.weight(.semibold))
                        if let fit = card.fit { Chip(text: fit.short, tint: tint(fit.level)).help(fit.long) }
                        if let eligibility = card.eligibility { Chip(text: eligibility.short, tint: tint(eligibility.level)).help(eligibility.long) }
                        if let source = card.sourceText { Chip(text: source, tint: .secondary) }
                    }
                    .accessibilityElement(children: .combine)
                    .accessibilityIdentifier("resume.current")
                }
                if let used = card.usedLine {
                    Text(verbatim: used).font(.callout).accessibilityIdentifier("resume.usedLine")
                }
                if case let .blocked(_, reason, _) = card.use {
                    Text(verbatim: reason).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("resume.blockedReason")
                }
            }
        }
    }

    private func tint(_ level: ResumeCheckLevel) -> Color {
        switch level {
        case .ok: .green
        case .caution: .orange
        case .bad: .red
        case .notRun: .gray
        }
    }

    // MARK: Actions

    private var actionRow: some View {
        let blocked = resume.blockedReason(for: job)
        let unreadable: Bool = { if case .unreadable = card.kind { true } else { false } }()
        // The unreadable card already says why; the button's tooltip repeats it.
        let reason = unreadable ? "This job's saved resumes could not be read, so a build is refused until that is fixed." : blocked
        let shownReason = unreadable ? nil : blocked
        return VStack(alignment: .leading, spacing: 6) {
            WrapLayout(spacing: 8) {
                Button { jobs.startResume(job) } label: {
                    Label(card.kind == .ready ? "Build again…" : "Build tailored resume…", systemImage: "doc.badge.gearshape")
                }
                .buttonStyle(.bordered)
                .keyboardShortcut(shortcutEnabled ? ResumeShortcut.keyboardShortcut : nil)
                .disabled(reason != nil)
                .help(reason ?? "Ask \(ResumeWords.modelName) to pick this posting's bullets (\(ResumeShortcut.label)). It shows the cost first; nothing is spent until you confirm.")
                .accessibilityIdentifier("resume.start")
                useButton
                // A version never fitted to pages has no PDF to open.
                if card.kind == .ready, card.currentVersion > 0, (summary?.pages ?? 0) > 0 { previewButton }
                Button("Open Studio") { resume.openStudio?(job.key) }
                    .disabled(resume.openStudio == nil)
                    .help(resume.openStudio == nil ? "Studio window coming" : "Open the Resume Studio window for this job.")
                    .accessibilityIdentifier("resume.openStudio")
            }
            if let previewError {
                Text(verbatim: previewError).font(.caption).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
            }
            if let shownReason {
                Text(shownReason).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    @ViewBuilder
    private var useButton: some View {
        switch card.use {
        case let .available(v):
            Button("Use v\(v) for this job") { resume.use(job, version: v) }
                .disabled(resume.blockedReason(for: job) != nil)
                .help("Check this version afresh, then save it for the browsers under this job's posting and application addresses.")
                .accessibilityIdentifier("resume.use")
        case let .blocked(v, _, .revalidate):
            Button("Revalidate v\(v)") { resume.revalidate(job, version: v) }
                .disabled(resume.blockedReason(for: job) != nil)
                .help("Drop what your knowledge base no longer vouches for and rebuild the page. No model, nothing is spent. Use is offered once it passes.")
                .accessibilityIdentifier("resume.revalidate")
        case .inUse, .blocked, .none:
            EmptyView()
        }
    }

    private var previewButton: some View {
        Button(previewing ? "Opening…" : "Open in Preview") { openPreview() }
            .disabled(previewing)
            .help("Open this version's PDF in Preview and check it before you use it.")
            .accessibilityIdentifier("resume.preview")
    }

    private func openPreview() {
        guard !previewing else { return }
        previewing = true
        previewError = nil
        let summary = summary
        Task {
            let result = await resume.previewCopy(for: job, summary: summary)
            previewing = false
            switch result {
            case let .success(url):
                if !NSWorkspace.shared.open(url) { previewError = "Preview did not open the resume." }
            case let .failure(error):
                previewError = error.message
            }
        }
    }
}

/// ⌥⌘B. Not used elsewhere in the app (⌘I Insights, ⌘R Refresh, ⌘O Open Posting, ⌘S Save, ⇧⌘A Applied, ⌘⌫ Hide,
/// ⇧⌘C Copy, ⇧⌘R Run Now, ⌥⌘R Re-triage), and not a standard menu key.
enum ResumeShortcut {
    static let keyboardShortcut = KeyboardShortcut("b", modifiers: [.command, .option])
    static let label = "⌥⌘B"
}

/// The cost confirmation: what it would cost, what is sent, and the only buttons that build.
struct ResumeConfirmSheet: View {
    @Environment(JobsModel.self) private var jobs

    var body: some View {
        if let pending = jobs.resume.confirming {
            content(key: pending.key, dry: pending.dry)
        }
    }

    private func content(key: String, dry: ResumeDryRun) -> some View {
        let confirmation = ResumeWords.confirmation(for: dry)
        let job = jobs.jobs.first { $0.key == key }
        return VStack(alignment: .leading, spacing: 12) {
            Text("Build a tailored resume").font(.headline)
            if let job {
                Text(verbatim: [job.title, job.company].filter { !$0.isEmpty }.joined(separator: " · "))
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
            Divider()
            if let cost = confirmation.costText {
                Text(verbatim: cost)
                    .font(.body.monospacedDigit())
                    .fixedSize(horizontal: false, vertical: true)
                    .help(confirmation.priceSource ?? "")
                    .accessibilityIdentifier("resume.cost")
            }
            Text(verbatim: confirmation.sentText)
                .font(.callout).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if let from = confirmation.madeFromText {
                Text(from).font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            if dry.opusUsable {
                Text("This spends from the same daily cap as drafts, re-triage and the daily run.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            if let blocked = confirmation.blockedReason {
                Label(blocked, systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("resume.blocked")
            }
            if let base = confirmation.baseNote {
                Text(base).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                Spacer()
                Button("Cancel", role: .cancel) { jobs.resume.cancel() }
                    .keyboardShortcut(.cancelAction)
                    .accessibilityIdentifier("resume.sheetCancel")
                if confirmation.offersBase {
                    Button("Build base selection anyway") { jobs.resume.confirmBase() }
                        .accessibilityIdentifier("resume.confirmBase")
                        .help("No model and no cost: your base choice of bullets, not tailored to this posting.")
                }
                Button("Build with \(ResumeWords.displayModel(dry.model))") { jobs.resume.confirm() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(!confirmation.canConfirm)
                    .accessibilityIdentifier("resume.confirm")
            }
        }
        .padding(20)
        .frame(width: 480)
    }
}
