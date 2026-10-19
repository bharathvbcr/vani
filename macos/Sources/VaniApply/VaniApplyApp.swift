import SwiftUI
import VaniApplyKit

@main
struct VaniApplyApp: App {
    @State private var model: AppModel
    @State private var jobs: JobsModel

    init() {
        let model = AppModel()
        _model = State(initialValue: model)
        _jobs = State(initialValue: JobsModel { model.jobsClient })
    }

    var body: some Scene {
        Window("Vani Apply", id: "main") {
            ContentView()
                .environment(model)
                .environment(jobs)
                .frame(minWidth: 820, minHeight: 520)
        }
        .defaultSize(width: 1180, height: 760)
        .commands {
            CommandGroup(replacing: .newItem) {}
        }
    }
}

enum Pane: String, CaseIterable, Identifiable {
    case jobs = "Jobs", answers = "Answers", activity = "Activity", browsers = "Browsers", settings = "Settings"
    var id: String { rawValue }
    var symbol: String {
        switch self {
        case .jobs: "briefcase"
        case .answers: "text.bubble"
        case .activity: "clock.arrow.circlepath"
        case .browsers: "safari"
        case .settings: "gearshape"
        }
    }
}

struct ContentView: View {
    @Environment(AppModel.self) private var model
    @State private var section: Pane? = .jobs

    var body: some View {
        NavigationSplitView {
            List(Pane.allCases, selection: $section) { s in
                Label(s.rawValue, systemImage: s.symbol).tag(s)
            }
            .navigationSplitViewColumnWidth(min: 160, ideal: 180)
        } detail: {
            VStack(spacing: 0) {
                if let err = model.storeError {
                    StoreErrorBanner(message: err)
                }
                switch section ?? .jobs {
                case .jobs: JobsView()
                case .answers: AnswersView()
                case .activity: ActivityView()
                case .browsers: BrowsersView()
                case .settings: SettingsView()
                }
            }
        }
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
