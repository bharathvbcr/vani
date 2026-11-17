import SwiftUI
import VaniApplyKit

/// Settings' model switch: which decision model triages postings (Liquid over OpenRouter,
/// SubQ Snap, Gemini Decision) and which language model drafts and triages (Claude through
/// Manvi, or Google AI Studio), plus the keys they need, pasted into the login Keychain.
/// Vani owns the choices and writes the config (`vani config models`); a key goes to the
/// Keychain only and never through vani, a file or a log.
struct ModelSettingsSection: View {
    let client: ModelSwitchClient
    var keys: any ModelKeyStore = KeychainModelKeyStore()

    @State private var choices: ModelChoices?
    @State private var decision = ""
    @State private var llm = ""
    @State private var draftModel = ""
    @State private var triageModel = ""
    @State private var busy = false
    @State private var problem: String?
    @State private var pasted: [String: String] = [:]
    @State private var stored: [String: Bool] = [:]
    @State private var keyProblem: String?

    var body: some View {
        Section("Models") {
            if let choices {
                Picker("Decision model", selection: $decision) {
                    ForEach(choices.decision.choices) { c in
                        Text(verbatim: "\(c.label) \u{00B7} \(c.model)").tag(c.id)
                    }
                }
                Picker("Language model", selection: $llm) {
                    if choices.llm.provider.isEmpty { Text("Not set").tag("") }
                    ForEach(choices.llm.choices) { p in Text(p.label).tag(p.id) }
                }
                .onChange(of: llm) { _, id in pickModels(for: id) }
                if let p = choices.llm.choices.first(where: { $0.id == llm }) {
                    Picker("Drafts", selection: $draftModel) {
                        ForEach(p.models) { m in Text(m.label).tag(m.id) }
                    }
                    Picker("Triage", selection: $triageModel) {
                        ForEach(p.models) { m in Text(m.label).tag(m.id) }
                    }
                }
                HStack {
                    Spacer()
                    Button("Reload", action: load).disabled(busy)
                    Button("Save models", action: save).disabled(busy || selection(choices) == nil)
                }
                if let problem {
                    Text(problem).font(.caption).foregroundStyle(.red).textSelection(.enabled)
                }
                Text("The decision model answers each posting's triage questions; the language model writes drafts and re-reads the postings the decision model was unsure about. Saved in the Vani config above, with each model's prices and the provider's host, in one write Vani checks first. Spend stays within decide.daily_usd (openrouter caps whichever decision model is chosen; llm caps the language model). Readings are filed under each model's own name, and the drop floor was tuned on Liquid: check `vani eval` after switching the decision model.")
                    .font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)

                ForEach(choices.keyItems) { item in keyRow(item) }
                if let keyProblem {
                    Text(keyProblem).font(.caption).foregroundStyle(.red).textSelection(.enabled)
                }
                Text("A pasted key is saved in your login Keychain for this Mac user and is handed only to the vani this app starts, in its environment. Claude's key stays with anthropic-account (the account picker above).")
                    .font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            } else if let problem {
                Text(problem).font(.caption).foregroundStyle(.red).textSelection(.enabled)
                Button("Reload", action: load).disabled(busy)
            } else {
                ProgressView().controlSize(.small)
            }
        }
        .task(id: client.configPath) { load() }
    }

    @ViewBuilder
    private func keyRow(_ item: ModelKeyItem) -> some View {
        LabeledContent(item.title) {
            HStack(spacing: 8) {
                SecureField("Paste key", text: Binding(
                    get: { pasted[item.service] ?? "" },
                    set: { pasted[item.service] = $0 }))
                    .labelsHidden()
                    .textFieldStyle(.roundedBorder)
                    .frame(width: 220)
                    .accessibilityLabel("\(item.title), paste to replace")
                Button("Save") { saveKey(item) }
                    .disabled((pasted[item.service] ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                Button("Remove") { removeKey(item) }
                    .disabled(stored[item.service] != true)
            }
        }
        Text(verbatim: "\(statusText(item.service)) \u{00B7} used by \(item.usedBy.joined(separator: ", "))")
            .font(.caption).foregroundStyle(.secondary)
    }

    private func statusText(_ service: String) -> String {
        switch stored[service] {
        case true?: "Saved in the Keychain"
        case false?: "Not set"
        case nil: "The Keychain could not say"
        }
    }

    /// The selection that differs from what is saved, or nil when nothing does.
    private func selection(_ c: ModelChoices) -> ModelSelection? {
        var s = ModelSelection()
        if !decision.isEmpty, decision != c.decision.provider { s.decisionProvider = decision }
        if !llm.isEmpty, llm != c.llm.provider || draftModel != c.llm.model || triageModel != c.llm.triageModel {
            s.llmProvider = llm
            s.model = draftModel
            s.triageModel = triageModel
        }
        return s.decisionProvider.isEmpty && s.llmProvider.isEmpty ? nil : s
    }

    /// Switching provider picks its first model for both roles, unless the saved ones are its.
    private func pickModels(for id: String) {
        guard let c = choices, let p = c.llm.choices.first(where: { $0.id == id }) else { return }
        let ids = p.models.map(\.id)
        if !ids.contains(draftModel) { draftModel = ids.contains(c.llm.model) ? c.llm.model : ids.first ?? "" }
        if !ids.contains(triageModel) { triageModel = ids.contains(c.llm.triageModel) ? c.llm.triageModel : draftModel }
    }

    private func show(_ c: ModelChoices) {
        choices = c
        decision = c.decision.provider
        llm = c.llm.provider
        draftModel = c.llm.model
        triageModel = c.llm.triageModel
        problem = nil
        refreshKeys(c)
    }

    private func refreshKeys(_ c: ModelChoices) {
        var out: [String: Bool] = [:]
        for item in c.keyItems { out[item.service] = keys.exists(service: item.service) }
        stored = out
    }

    @MainActor
    private func run(_ op: @escaping @Sendable (ModelSwitchClient) throws -> ModelChoices) {
        let client = client
        busy = true
        Task {
            let result = await BlockingWork.run { try op(client) }
            busy = false
            switch result {
            case let .success(c): show(c)
            case let .failure(err): problem = String(describing: err)
            }
        }
    }

    private func load() { run { try $0.load() } }

    private func save() {
        guard let c = choices, let s = selection(c) else { return }
        run { try $0.save(s) }
    }

    private func saveKey(_ item: ModelKeyItem) {
        let raw = pasted[item.service] ?? ""
        do {
            try keys.save(raw, service: item.service)
            pasted[item.service] = ""
            keyProblem = nil
        } catch {
            keyProblem = String(describing: error)
        }
        if let choices { refreshKeys(choices) }
    }

    private func removeKey(_ item: ModelKeyItem) {
        do {
            try keys.remove(service: item.service)
            keyProblem = nil
        } catch {
            keyProblem = String(describing: error)
        }
        if let choices { refreshKeys(choices) }
    }
}
