import AppKit
import SwiftUI
import VaniApplyKit

/// The Apply kit card for the Jobs detail pane: the steps from reading a role to having
/// applied, each backed by something the app already does.
///
///  1. Draft answers   DraftSheet, opened with this job's company, title and description.
///  2. Attach resume   The resume saved for this job, opened in Preview. Choosing another
///                     file still happens in the browser popup (ApplyKit.resumeNote).
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
    @State private var listed: [ResumeStore.Listed] = []
    @State private var matched: ResumeStore.Listed?
    @State private var loaded = false
    @State private var resumeError: String?
    @State private var opening = false
    @State private var openError: String?
    @State private var showingSaved = false

    private var answers: Reading<ApplyKit.AnswerCounts> {
        guard let model else { return .failed("the answer bank is not open in this window") }
        if let e = model.storeError { return .failed(e) }
        let all = model.answers
        return .ready(.init(total: all.count, review: all.filter(\.review).count))
    }

    private var resumePresence: ApplyKit.ResumePresence {
        if let resumeError { return .unavailable(resumeError) }
        if !loaded { return .unknown }
        if let matched { return .saved(matched.name) }
        return .none
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label("Apply kit", systemImage: "paperplane").font(.headline)
            ForEach(ApplyKit.steps(job: job, answers: answers, resume: resumePresence)) { step in
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
        .sheet(isPresented: $showingSaved) {
            SavedResumesSheet(items: listed, opening: opening, error: openError, open: openListed)
        }
        .task(id: resumeTaskID) { await reloadResumes() }
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
            guard !opening else { return }
            Task { await reloadResumes() }
        }
    }

    private var resumeTaskID: String { "\(job.key)|\(job.url ?? "")|\(job.applyURL ?? "")" }

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
            switch resumePresence {
            case .unknown:
                Text("Checking saved resumes…").font(.caption).foregroundStyle(.secondary)
            case let .unavailable(why):
                Text("Saved resumes could not be read: \(why)").font(.caption).foregroundStyle(.orange)
                    .fixedSize(horizontal: false, vertical: true)
            case .none:
                Text(listed.isEmpty ? "No resume is saved yet." : "No resume is saved for this posting.")
                    .font(.caption).foregroundStyle(.secondary)
            case .saved:
                if let matched {
                    Text(verbatim: ApplyKit.resumeDetail(name: matched.name, bytes: matched.bytes, pickedAt: matched.pickedAt))
                        .font(.caption)
                        .fixedSize(horizontal: false, vertical: true)
                    Button(opening ? "Opening…" : "View resume") { openListed(matched) }
                        .controlSize(.small)
                        .disabled(opening)
                        .help("Open this job's resume in Preview")
                }
            }
            if !listed.isEmpty {
                Button("Saved resumes (\(listed.count))") { showingSaved = true }
                    .controlSize(.small)
                    .help("Every resume saved for a job, including ones this posting did not match")
            }
            if let openError {
                Text(openError).font(.caption).foregroundStyle(.orange)
                    .fixedSize(horizontal: false, vertical: true)
            }
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

    private func reloadResumes() async {
        openError = nil
        guard let model, model.storeError == nil, let dir = model.store?.directory else {
            listed = []
            matched = nil
            loaded = true
            resumeError = model?.storeError ?? "the answer bank is not open in this window"
            return
        }
        let urls = [job.url, job.applyURL].compactMap { $0 }
        let result = await BlockingWork.run { () throws -> (ResumeStore.Listed?, [ResumeStore.Listed]) in
            let resumes = ResumeStore(directory: dir.appendingPathComponent("resumes", isDirectory: true))
            let all = try resumes.catalog()
            return (ResumeStore.match(urls: urls, listed: all), all)
        }
        switch result {
        case let .success((hit, all)):
            matched = hit
            listed = all
            resumeError = nil
            loaded = true
        case let .failure(error):
            matched = nil
            listed = []
            resumeError = String(describing: error)
            loaded = true
        }
    }

    private func openListed(_ item: ResumeStore.Listed) {
        guard !opening, let dir = model?.store?.directory else { return }
        opening = true
        openError = nil
        let key = item.key
        Task {
            let result = await BlockingWork.run { () throws -> URL in
                let resumes = ResumeStore(directory: dir.appendingPathComponent("resumes", isDirectory: true))
                guard let got = try resumes.get(key: key) else {
                    throw OpenFailure(message: "that resume is no longer saved")
                }
                // The name just read with the bytes, not the one the list was showing.
                return try ResumeStore.previewCopy(name: got.entry.name, data: got.data)
            }
            opening = false
            switch result {
            case let .success(url):
                if !NSWorkspace.shared.open(url) {
                    openError = "Preview did not open the resume"
                }
            case let .failure(error):
                openError = String(describing: error)
            }
        }
    }
}

/// A resume the list still names, whose file the store no longer has.
private struct OpenFailure: Error, CustomStringConvertible {
    var message: String
    var description: String { message }
}

/// Every resume in the store, so a key that does not match this job still leaves the file reachable.
private struct SavedResumesSheet: View {
    let items: [ResumeStore.Listed]
    let opening: Bool
    let error: String?
    let open: (ResumeStore.Listed) -> Void
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Saved resumes").font(.headline)
            Text("Each file is the resume chosen for one job. Open it in Preview and check it before you submit.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if let error {
                Text(error).font(.caption).foregroundStyle(.orange)
                    .fixedSize(horizontal: false, vertical: true)
            }
            ScrollView {
                VStack(alignment: .leading, spacing: 8) {
                    ForEach(items, id: \.key) { item in
                        HStack(alignment: .firstTextBaseline) {
                            Text(verbatim: ApplyKit.resumeDetail(name: item.name, bytes: item.bytes, pickedAt: item.pickedAt))
                                .font(.callout)
                                .lineLimit(2)
                            Spacer(minLength: 8)
                            Button("View") { open(item) }
                                .controlSize(.small)
                                .disabled(opening)
                        }
                    }
                }
            }
            HStack {
                Spacer()
                Button("Done") { dismiss() }
            }
        }
        .padding(16)
        .frame(minWidth: 420, minHeight: 280)
    }
}
