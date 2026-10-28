import SwiftUI
import VaniApplyKit

/// One-click stages for an applied job (screening to withdrawn): a button for each, the current one tinted,
/// and one that sets the stage back to plain applied. The job stays Applied at every stage.
struct JobStageRow: View {
    @Environment(JobsModel.self) private var jobs
    let job: Job

    var body: some View {
        HStack(spacing: 6) {
            Text("Stage").font(.callout).foregroundStyle(.secondary)
            ForEach(JobStage.allCases, id: \.self) { s in
                Button(s.title) { Task { await jobs.setStage(job.key, s) } }
                    .buttonStyle(.bordered)
                    .tint(job.stage == s ? JobStageViews.tint(s) : nil)
                    .disabled(JobStageViews.blocked(s, from: job.stage))
                    .help(JobStageViews.blocked(s, from: job.stage)
                          ? "This application is already \(job.stage?.rawValue ?? ""); set it back first."
                          : "Record that this application is \(s.rawValue)")
            }
            if job.stage != nil {
                Button("Undo", systemImage: "arrow.uturn.backward") { Task { await jobs.setStage(job.key, nil) } }
                    .buttonStyle(.borderless)
                    .help("Set the stage back to Applied")
            }
            if let f = job.furthestStage, job.stage?.isTerminal == true {
                Text("Reached \(f.rawValue)").font(.callout).foregroundStyle(.secondary)
            }
        }
        .controlSize(.small)
    }
}

/// The same actions in the row's context menu.
struct JobStageMenu: View {
    @Environment(JobsModel.self) private var jobs
    let job: Job

    var body: some View {
        Menu("Stage", systemImage: "flag") {
            ForEach(JobStage.allCases, id: \.self) { s in
                Button(s.title, systemImage: job.stage == s ? "checkmark" : "circle") {
                    Task { await jobs.setStage(job.key, s) }
                }
                .disabled(JobStageViews.blocked(s, from: job.stage))
            }
            if job.stage != nil {
                Divider()
                Button("Set Back to Applied", systemImage: "arrow.uturn.backward") {
                    Task { await jobs.setStage(job.key, nil) }
                }
            }
        }
    }
}

enum JobStageViews {
    /// vani refuses to swap rejected and withdrawn (marks.CheckTransition); the buttons say so up front.
    static func blocked(_ next: JobStage, from current: JobStage?) -> Bool {
        guard let current else { return false }
        return current.isTerminal && next.isTerminal && current != next
    }

    static func tint(_ s: JobStage) -> Color {
        switch s {
        case .screening, .interview: .blue
        case .offer: .green
        case .rejected: .red
        case .withdrawn: .gray
        }
    }
}
