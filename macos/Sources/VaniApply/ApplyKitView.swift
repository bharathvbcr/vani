import AppKit
import SwiftUI
import VaniApplyKit

/// The Apply kit card for the Jobs detail pane: the steps from reading a role to having
/// applied, each backed by something the app already does.
///
///  1. Draft answers   DraftSheet, opened with this job's company, title and description.
///  2. Attach resume   Explained only. The resume is bound to the extension's page key, which
///                     the app cannot derive from a job's URL (ApplyKit.resumeNote).
///  3. Open & Fill     The apply link when it differs from the posting; Fill runs in the browser.
///  4. Mark applied    JobsModel.setMark.
struct ApplyKitView: View {
    let job: Job
    /// The description the detail pane loaded; a list row's `job` may not carry one.
    var description: String? = nil

    /// Optional: a host that shows Jobs without the answer bank (the UI-test host) has no
    /// AppModel. The bank then reads as unavailable, never as empty, and drafting is off.
    @Environment(AppModel.self) private var model: AppModel?
    @Environment(JobsModel.self) private var jobs
    @State private var drafting = false
    @State private var marking = false

    private var answers: Reading<ApplyKit.AnswerCounts> {
        guard let model else { return .failed("the answer bank is not open in this window") }
        if let e = model.storeError { return .failed(e) }
        let all = model.answers
        return .ready(.init(total: all.count, review: all.filter(\.review).count))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label("Apply kit", systemImage: "paperplane").font(.headline)
            ForEach(ApplyKit.steps(job: job, answers: answers)) { step in
                row(step)
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator))
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Apply kit for \(job.shareSubject)")
        .sheet(isPresented: $drafting) {
            let p = ApplyKit.draftPrefill(job, description: description)
            DraftSheet(company: p.company, title: p.title, posting: p.posting)
        }
    }

    @ViewBuilder
    private func row(_ step: ApplyKit.Step) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            marker(step)
            VStack(alignment: .leading, spacing: 4) {
                Text(step.title).font(.subheadline.weight(.medium))
                detail(step)
            }
            Spacer(minLength: 0)
        }
        .accessibilityElement(children: .combine)
    }

    private func marker(_ step: ApplyKit.Step) -> some View {
        Group {
            switch step.state {
            case .done:
                Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                    .accessibilityLabel("Step \(step.number), done")
            case .unavailable:
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                    .accessibilityLabel("Step \(step.number), could not be checked")
            case .todo, .manual:
                Image(systemName: "\(step.number).circle").foregroundStyle(.secondary)
                    .accessibilityLabel("Step \(step.number)")
            }
        }
        .frame(width: 18)
    }

    @ViewBuilder
    private func detail(_ step: ApplyKit.Step) -> some View {
        switch step.number {
        case 1:
            if case let .unavailable(why) = step.state {
                Text("The saved answers could not be read: \(why)").font(.caption).foregroundStyle(.orange)
                    .fixedSize(horizontal: false, vertical: true)
            } else if case let .ready(c) = answers {
                Text(c.line).font(.caption).foregroundStyle(.secondary)
            }
            Button("Draft an answer with Claude…") { drafting = true }
                .controlSize(.small)
                .disabled(model == nil || model?.storeError != nil)
                .help("Draft a long free-text answer with Claude from this posting, then save it as a review-flagged answer")
        case 2:
            Text(ApplyKit.resumeNote).font(.caption).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        case 3:
            if let link = ApplyKit.distinctApplyLink(job) {
                Button("Open application page") { NSWorkspace.shared.open(link) }
                    .controlSize(.small)
                    .help("Open the application form in your browser, then press Fill in the Vani Apply extension")
            } else {
                Text("Open the posting above, go to its application form, then press Fill in the Vani Apply extension.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        default:
            if step.state == .done {
                Text("Marked applied").font(.caption).foregroundStyle(.secondary)
            } else {
                Button(marking ? "Marking…" : "Mark applied") { mark() }
                    .controlSize(.small)
                    .disabled(marking)
                    .help("Record that you applied to this job")
            }
        }
    }

    private func mark() {
        guard !marking else { return }
        marking = true
        Task {
            await jobs.setMark(job.key, .applied)
            marking = false
        }
    }
}
