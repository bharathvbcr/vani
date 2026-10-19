import AppKit
import SwiftUI
import UniformTypeIdentifiers
import VaniApplyKit

struct SettingsView: View {
    @Environment(AppModel.self) private var model
    @Environment(JobsModel.self) private var jobs
    @State private var newHost = ""
    @State private var importing = false
    @State private var draftConfig = ""
    @State private var draftState = ""
    @State private var draftAccount = ""
    @State private var accounts: [String] = []
    @State private var accountsProblem: String?

    private var editedDraftSettings: DraftSettings {
        DraftSettings(vaniConfig: draftConfig, vaniStateDir: draftState, anthropicAccount: draftAccount)
    }

    /// The listed accounts, plus a saved one the registry no longer lists, so
    /// the picker always shows what is saved.
    private var accountChoices: [String] {
        accounts.contains(draftAccount) || draftAccount.isEmpty ? accounts : accounts + [draftAccount]
    }

    var body: some View {
        Form {
            Section("Self-identification") {
                Toggle("Save and fill voluntary self-identification answers", isOn: Binding(
                    get: { model.settings.captureSelfId },
                    set: { model.setCaptureSelfId($0) }
                ))
                .disabled(model.storeError != nil)
                Text("Gender, race and ethnicity, veteran, disability and orientation questions. Off by default; shown masked in the list.")
                    .font(.caption).foregroundStyle(.secondary)
            }

            Section("Sites where Vani Apply stays off") {
                ForEach(model.settings.disabledHosts, id: \.self) { host in
                    HStack {
                        Text(host)
                        Spacer()
                        Button("Remove") { model.enable(host: host) }
                    }
                }
                HStack {
                    TextField("Hostname", text: $newHost, prompt: Text("jobs.example.com"))
                        .onSubmit(addHost)
                    Button("Add", action: addHost).disabled(newHost.isEmpty || model.storeError != nil)
                }
            }

            Section("Vani (Jobs and Claude drafting)") {
                PathRow(label: "Vani config", path: $draftConfig, directory: false)
                PathRow(label: "Vani state folder", path: $draftState, directory: true)
                Picker("Anthropic account", selection: $draftAccount) {
                    Text(accounts.first.map { "Default (\($0))" } ?? "Default").tag("")
                    ForEach(accountChoices, id: \.self) { Text($0).tag($0) }
                }
                if let accountsProblem {
                    Text(accountsProblem).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                }
                HStack {
                    Spacer()
                    Button("Save") {
                        model.saveDraftSettings(editedDraftSettings)
                        jobs.reload()
                    }
                        .disabled(editedDraftSettings == model.draftSettings)
                }
                Text("Jobs lists the roles in that state folder's ledger and keeps your saved, applied and hidden marks and notes beside it (marks/); it spends nothing. Drafts run Vani's `vani draft` with the model and daily spending cap in that config (llm.model, decide.daily_usd.llm), shared with the daily listings run. Empty means Vani's default in ~/Library/Application Support/Vani. When that config's llm.provider is anthropic, Vani starts through anthropic-account, which reads the chosen account's key (and its workspace id, if listed) from your login Keychain and ~/.anthropic-accounts; this app never sees the key. Default is the first account listed.")
                    .font(.caption).foregroundStyle(.secondary)
            }

            Section("Browser suggestions (Vani Apply agent)") {
                LabeledContent("Agent", value: model.agentSummary)
                HStack {
                    Spacer()
                    Button("Register again") { model.registerAgent(repair: true) }
                    Button("Open Login Items") { model.openLoginItems() }
                }
                Text("Suggest in the browser panel sends the page's new free-text questions (labels only) to the decision model, then has Claude draft the ones it reads as essays, using the config above. Visa, consent and self-identification questions never reach a model. Safari can only reach Vani through this agent.")
                    .font(.caption).foregroundStyle(.secondary)
            }

            Section("Bank") {
                LabeledContent("Answers", value: "\(model.answers.count) · \(model.bankBytes / 1000) of 900 KB")
                LabeledContent("Revision", value: "\(model.snapshot.rev)")
                HStack {
                    Button("Export…", action: export)
                    Button("Import…") { importing = true }.disabled(model.storeError != nil)
                    if let dir = model.store?.directory {
                        Button("Show Store in Finder") { NSWorkspace.shared.activateFileViewerSelecting([dir]) }
                    }
                }
                Text("Import replaces every answer with the file's, after the same checks capture applies (secret-looking answers are dropped). The bank is a private file in Vani Apply's App Group folder on this Mac; nothing is sent anywhere.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
        .fileImporter(isPresented: $importing, allowedContentTypes: [.json]) { result in
            switch result {
            case let .success(url): model.importBank(from: url)
            case let .failure(err): model.actionError = err.localizedDescription
            }
        }
        .navigationTitle("Settings")
        .onAppear {
            let s = model.draftSettings
            draftConfig = s.vaniConfig
            draftState = s.vaniStateDir
            draftAccount = s.anthropicAccount
        }
        .task {
            let listed = await model.anthropicAccounts()
            accounts = listed.names
            accountsProblem = listed.problem
        }
    }

    private struct PathRow: View {
        let label: String
        @Binding var path: String
        let directory: Bool

        var body: some View {
            LabeledContent(label) {
                HStack {
                    TextField("", text: $path, prompt: Text("default")).font(.body.monospaced())
                    Button("Choose…") {
                        let panel = NSOpenPanel()
                        panel.canChooseFiles = !directory
                        panel.canChooseDirectories = directory
                        panel.allowsMultipleSelection = false
                        if !directory { panel.allowedContentTypes = [.json] }
                        if panel.runModal() == .OK, let url = panel.url { path = url.path }
                    }
                }
            }
        }
    }

    private func addHost() {
        if model.disable(host: newHost) { newHost = "" }
    }

    private func export() {
        guard let data = model.exportData() else { return }
        let panel = NSSavePanel()
        panel.allowedContentTypes = [.json]
        panel.nameFieldStringValue = "vani-answers.json"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        do { try data.write(to: url, options: .atomic) } catch { model.actionError = "Export failed: \(error)" }
    }
}
