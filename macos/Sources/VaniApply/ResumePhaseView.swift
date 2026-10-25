import SwiftUI
import VaniApplyKit

/// The progress, result or error of the one resume step in flight for this posting (`ResumeStudioModel.phase`),
/// drawn the same in the detail pane's card and in the Studio window: nothing for another posting's step.
struct ResumePhaseView: View {
    @Environment(JobsModel.self) private var jobs
    let job: Job

    private var resume: ResumeStudioModel { jobs.resume }
    /// This posting's state; another posting's is not shown here.
    private var phase: ResumeStudioModel.Phase { resume.phase.key == job.key ? resume.phase : .idle }

    var body: some View {
        switch phase {
        case .idle:
            EmptyView()
        case .estimating:
            progress("Asking Vani what this would cost…", detail: "Nothing is spent yet.", stoppable: true)
        case .confirming:
            progress("Waiting for your confirmation…", detail: "Nothing is spent until you confirm.", stoppable: false)
        case let .building(_, allowBase, cancelling):
            let asking = resume.activeRedraft.map { "Asking \(ResumeWords.modelName) to redraft v\($0.from)…" } ?? "Asking \(ResumeWords.modelName)…"
            progress(cancelling ? "Stopping…" : (allowBase ? "Building the base selection…" : asking),
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
}
