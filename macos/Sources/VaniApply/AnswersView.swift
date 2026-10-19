import SwiftUI
import VaniApplyKit

struct AnswersView: View {
    @Environment(AppModel.self) private var model
    @State private var selection = Set<String>()
    @State private var search = ""
    @State private var editing: Answer?
    @State private var adding = false
    @State private var drafting = false
    @State private var confirmDelete = false

    private var rows: [Answer] {
        let q = Classifier.normalizeLabel(search)
        guard !q.isEmpty else { return model.answers }
        return model.answers.filter { a in
            a.key.contains(q) || a.labels.contains { $0.contains(q) }
                || (a.sensitivity != .selfID && a.value.contains { $0.localizedCaseInsensitiveContains(search) })
        }
    }

    var body: some View {
        Table(rows, selection: $selection) {
            TableColumn("Question") { a in
                VStack(alignment: .leading, spacing: 2) {
                    Text(a.key)
                    if a.labels.count > 1 {
                        Text("\(a.labels.count) wordings").font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
            TableColumn("Answer") { a in
                HStack(spacing: 6) {
                    if a.sensitivity == .selfID {
                        Text("•••••• self-identification").foregroundStyle(.secondary)
                    } else {
                        Text(a.value.joined(separator: ", ")).lineLimit(2)
                    }
                    if a.review {
                        Text("draft").font(.caption2).padding(.horizontal, 4).background(.yellow.opacity(0.3), in: .capsule)
                    }
                }
            }
            TableColumn("Kind") { a in Text(a.kind.rawValue).foregroundStyle(.secondary) }
                .width(70)
            TableColumn("Saved") { a in Text("\(a.uses)") }.width(44)
            TableColumn("Filled") { a in Text("\(a.fills)") }.width(44)
            TableColumn("Updated") { a in Text(a.updatedAt.prefix(10)).foregroundStyle(.secondary) }.width(84)
        }
        .contextMenu(forSelectionType: String.self) { keys in
            if keys.count == 1, let k = keys.first, let a = model.snapshot.bank.answers[k] {
                Button("Edit…") { editing = a }
            }
            Button("Delete", role: .destructive) { selection = keys; confirmDelete = true }
        } primaryAction: { keys in
            if let k = keys.first, let a = model.snapshot.bank.answers[k] { editing = a }
        }
        .searchable(text: $search, prompt: "Search questions and answers")
        .overlay {
            if model.answers.isEmpty && model.storeError == nil {
                ContentUnavailableView("No answers yet", systemImage: "text.bubble",
                                       description: Text("Submit an application with the browser extension, or add an answer here."))
            }
        }
        .toolbar {
            ToolbarItemGroup {
                Text("\(model.answers.count) answers · \(model.bankBytes / 1000) of 900 KB")
                    .foregroundStyle(.secondary).font(.callout)
                Button { drafting = true } label: { Label("Draft with Claude", systemImage: "sparkles") }
                    .disabled(model.storeError != nil)
                Button { adding = true } label: { Label("Add Answer", systemImage: "plus") }
                    .disabled(model.storeError != nil)
                Button { confirmDelete = true } label: { Label("Delete", systemImage: "trash") }
                    .disabled(selection.isEmpty || model.storeError != nil)
            }
        }
        .confirmationDialog("Delete \(selection.count) answer\(selection.count == 1 ? "" : "s")?", isPresented: $confirmDelete) {
            Button("Delete", role: .destructive) {
                model.delete(keys: selection)
                selection.removeAll()
            }
        } message: {
            Text("The next form will leave these questions blank until you answer them again.")
        }
        .sheet(item: $editing) { a in AnswerEditor(answer: a) }
        .sheet(isPresented: $adding) { AddAnswerSheet() }
        .sheet(isPresented: $drafting) { DraftSheet() }
        .navigationTitle("Answers")
    }
}

struct AnswerEditor: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let answer: Answer
    @State private var text = ""
    @State private var revealed = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(answer.key).font(.headline)
            if answer.labels.count > 1 {
                DisclosureGroup("Seen as \(answer.labels.count) wordings") {
                    ForEach(answer.labels, id: \.self) { Text($0).foregroundStyle(.secondary) }
                }
            }
            if answer.sensitivity == .selfID && !revealed {
                HStack {
                    Text("Self-identification answer hidden.").foregroundStyle(.secondary)
                    Button("Show") { revealed = true }
                }
            } else {
                Text(answer.kind.isChoice ? "One choice per line" : "Answer").font(.caption).foregroundStyle(.secondary)
                TextEditor(text: $text)
                    .font(.body)
                    .frame(minHeight: 120)
                    .border(.separator)
            }
            Text(caption).font(.caption).foregroundStyle(.secondary)
            HStack {
                Spacer()
                Button("Cancel", role: .cancel) { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Save") { if model.edit(key: answer.key, text: text) { dismiss() } }
                    .keyboardShortcut(.defaultAction)
                    .disabled(answer.sensitivity == .selfID && !revealed)
            }
        }
        .padding(20)
        .frame(width: 480)
        .onAppear { text = Editing.text(of: answer) }
    }

    private var caption: String {
        var parts = ["Kind: \(answer.kind.rawValue)", "saved \(answer.uses)×", "filled \(answer.fills)×"]
        if !answer.signatures.isEmpty { parts.append("known on \(Set(answer.signatures.map { $0.split(separator: "|").first ?? "" }).count) sites") }
        parts.append("Over \(Limits.reviewChars) characters fills as a yellow draft.")
        return parts.joined(separator: " · ")
    }
}

struct AddAnswerSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var question = ""
    @State private var kind: FieldKind = .text
    @State private var text = ""

    private var sensitivity: Sensitivity {
        Classifier.classify(kind: kind, label: question, name: "", autocomplete: "")
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Add an answer").font(.headline)
            Form {
                TextField("Question", text: $question, prompt: Text("as the form asks it, e.g. Notice period"))
                Picker("Kind", selection: $kind) {
                    ForEach(Editing.manualKinds, id: \.self) { Text($0.rawValue).tag($0) }
                }
            }
            Text(kind.isChoice ? "Choices to pick, one per line (matched to the form's options)" : "Answer")
                .font(.caption).foregroundStyle(.secondary)
            TextEditor(text: $text).frame(minHeight: 100).border(.separator)
            switch sensitivity {
            case .secret:
                Label(EditError.secret.description, systemImage: "lock.fill").foregroundStyle(.red).font(.callout)
            case .selfID where !model.settings.captureSelfId:
                Label("Self-identification: saved, but filled only after you turn it on in Settings.", systemImage: "eye.slash")
                    .foregroundStyle(.secondary).font(.callout)
            default:
                Text("Matching uses the question's wording; the extension also learns other wordings when you submit.")
                    .foregroundStyle(.secondary).font(.caption)
            }
            HStack {
                Spacer()
                Button("Cancel", role: .cancel) { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Add") { if model.add(question: question, kind: kind, text: text) { dismiss() } }
                    .keyboardShortcut(.defaultAction)
                    .disabled(sensitivity == .secret || question.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
        .padding(20)
        .frame(width: 480)
    }
}
