import SwiftUI
import VaniApplyKit

@main
struct VaniApplyApp: App {
    @State private var model: AppModel
    @State private var jobs: JobsModel
    @State private var pipeline: PipelineModel

    init() {
        // A screenshot or fixture rig passes -VaniApplyNoAgent YES so it does not
        // take the per-user login agent from the owner's copy (AgentLaunch).
        let launch = AgentLaunch.from(arguments: CommandLine.arguments)
        if let problem = launch.problem {
            FileHandle.standardError.write(Data("Vani Apply: \(problem)\n".utf8))
        }
        let model = AppModel(autoRegisterAgent: launch.mode == .register)
        _model = State(initialValue: model)
        _jobs = State(initialValue: JobsModel { model.jobsClient })
        _pipeline = State(initialValue: PipelineModel { model.pipelineClient })
    }

    var body: some Scene {
        Window("Vani Apply", id: "main") {
            ContentView()
                .environment(model)
                .environment(jobs)
                .environment(pipeline)
                .frame(minWidth: 820, minHeight: 520)
        }
        .defaultSize(width: 1180, height: 760)
        .commands {
            CommandGroup(replacing: .newItem) {}
        }
    }
}

enum Pane: String, CaseIterable, Identifiable {
    case jobs = "Jobs", pipeline = "Pipeline", answers = "Answers", activity = "Activity", browsers = "Browsers", settings = "Settings"
    var id: String { rawValue }
    var symbol: String {
        switch self {
        case .jobs: "briefcase"
        case .pipeline: "waveform.path.ecg"
        case .answers: "text.bubble"
        case .activity: "clock.arrow.circlepath"
        case .browsers: "safari"
        case .settings: "gearshape"
        }
    }
}

struct ContentView: View {
    @Environment(AppModel.self) private var model
    @Environment(PipelineModel.self) private var pipeline
    @State private var section: Pane? = .jobs

    var body: some View {
        NavigationSplitView {
            List(Pane.allCases, selection: $section) { s in
                HStack {
                    Label(s.rawValue, systemImage: s.symbol)
                    if s == .pipeline, let badge = pipelineBadge {
                        Spacer()
                        Image(systemName: badge.symbol).foregroundStyle(badge.color)
                            .help(badge.help)
                    }
                }
                .tag(s)
            }
            .navigationSplitViewColumnWidth(min: 160, ideal: 180)
        } detail: {
            VStack(spacing: 0) {
                if let err = model.storeError {
                    StoreErrorBanner(message: err)
                }
                switch section ?? .jobs {
                case .jobs: JobsView()
                case .pipeline: PipelineView()
                case .answers: AnswersView()
                case .activity: ActivityView()
                case .browsers: BrowsersView()
                case .settings: SettingsView()
                }
            }
        }
        // Read the pipeline's status while the app is open, so a failed daily run
        // shows on the sidebar without opening the tab.
        .task { await pipeline.poll() }
        .alert("Vani Apply", isPresented: Binding(get: { model.actionError != nil }, set: { if !$0 { model.actionError = nil } })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(model.actionError ?? "")
        }
        .alert("Vani Apply", isPresented: Binding(get: { model.notice != nil }, set: { if !$0 { model.notice = nil } })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(model.notice ?? "")
        }
    }
}

extension ContentView {
    /// The sidebar mark for the Pipeline row: nothing while healthy or unread.
    fileprivate var pipelineBadge: (symbol: String, color: Color, help: String)? {
        switch pipeline.health {
        case .problem?: ("xmark.circle.fill", .red, "The pipeline needs attention")
        case .warning?: ("exclamationmark.triangle.fill", .orange, "The pipeline needs a look")
        case .healthy?, nil: nil
        }
    }
}

struct StoreErrorBanner: View {
    @Environment(AppModel.self) private var model
    let message: String

    var body: some View {
        HStack(alignment: .top) {
            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.yellow)
            VStack(alignment: .leading, spacing: 4) {
                Text(message).textSelection(.enabled)
                Text("Editing is paused so nothing overwrites your answers.").foregroundStyle(.secondary)
            }
            Spacer()
            if model.store != nil {
                Button("Retry") { model.reload() }
                Button("Move Aside and Start Empty") { model.quarantineCorruptBank() }
            }
        }
        .padding(10)
        .background(.yellow.opacity(0.12))
    }
}
