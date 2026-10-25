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
    /// What this app knows of the job's resumes: unknown (loading, or `jobs show` failed) is never drawn as "no resume".
    private var known: ResumeKnowledge {
        ResumeKnowledge.of(detail: jobs.detail?.key == job.key ? jobs.detail : nil, job: job,
                           detailFailed: jobs.selection == job.key && jobs.detailError != nil)
    }

    private var summary: ResumeSummary? { known.summary }

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
                ResumePhaseView(job: job)
                if !workingOnThis {
                    switch known {
                    case .known:
                        summaryView
                        actionRow
                    case .loading:
                        HStack(spacing: 8) {
                            ProgressView().controlSize(.small)
                            Text("Checking this job's resumes…").font(.callout).foregroundStyle(.secondary)
                        }
                        .accessibilityIdentifier("resume.loading")
                    case .failed:
                        Label("This job's details could not be read, so its tailored resumes are not known. Select another role and come back to try again.",
                              systemImage: "exclamationmark.triangle.fill")
                            .font(.callout).foregroundStyle(.orange)
                            .fixedSize(horizontal: false, vertical: true)
                            .accessibilityIdentifier("resume.unknown")
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .cardStyle()
            .accessibilityElement(children: .contain)
            .accessibilityLabel("Tailored resume")
            .accessibilityIdentifier("resume.card")
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
                Button("Open Studio") { jobs.openStudio(job) }
                    .disabled(resume.openStudio == nil)
                    .help(resume.openStudio == nil ? "The Studio window cannot be opened from here." : "Open the Resume Studio window for this job: edit its bullets and see the page.")
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
            ResumeConfirmContent(key: pending.key, dry: pending.dry)
        }
    }
}

/// The confirmation's content: the Jobs window's sheet shows it, and so does a Studio window's Redraft sheet
/// for a redraft it started (`ResumeStudioModel.confirmationHost`).
struct ResumeConfirmContent: View {
    @Environment(JobsModel.self) private var jobs
    let key: String
    let dry: ResumeDryRun
    var width: CGFloat? = 480

    var body: some View {
        let redraft = jobs.resume.activeRedraft
        let isRedraft = redraft != nil || dry.from != nil
        let confirmation = ResumeWords.confirmation(for: dry, redraft: redraft)
        let job = jobs.jobs.first { $0.key == key } ?? jobs.resume.candidate(key)
        return VStack(alignment: .leading, spacing: 12) {
            Text(isRedraft ? "Redraft this resume" : "Build a tailored resume").font(.headline)
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
            if let what = confirmation.redraftText {
                Text(verbatim: what).font(.callout).fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("resume.redraftText")
            }
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
                Button("\(isRedraft ? "Redraft" : "Build") with \(ResumeWords.displayModel(dry.model))") { jobs.resume.confirm() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(!confirmation.canConfirm)
                    .accessibilityIdentifier("resume.confirm")
            }
        }
        .padding(20)
        .frame(width: width)
    }
}
