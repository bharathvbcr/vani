import SwiftUI
import VaniApplyKit

/// The weekly Audit: one posting at a time, "would you apply?", one key each
/// (Y, M, N, S). Vani's own readings are hidden so they cannot sway the answer;
/// the answers are the labels `vani relevance` measures the ranking against.
struct AuditView: View {
    @Environment(AuditModel.self) private var model

    var body: some View {
        @Bindable var model = model
        VStack(spacing: 0) {
            if let err = model.impressionsError {
                Text(verbatim: String(err.prefix(500)))
                    .font(.caption)
                    .foregroundStyle(.orange)
                    .lineLimit(3)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 16)
                    .padding(.vertical, 6)
            }
            content
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .navigationTitle("Audit")
        .task { if model.session == nil, !model.loading { await model.start() } }
        .alert("Vani Apply", isPresented: Binding(get: { model.answerError != nil }, set: { if !$0 { model.answerError = nil } })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(model.answerError ?? "")
        }
    }

    @ViewBuilder private var content: some View {
        if let err = model.loadError {
            Message(title: "The audit could not be read", detail: err, button: "Try Again") { Task { await model.start() } }
        } else if model.loading, model.session == nil {
            ProgressView("Drawing this week's postings…").padding(40)
        } else if let s = model.session {
            if s.picks.isEmpty {
                Message(title: "Nothing to audit", detail: "Every posting in the scored and dropped pools has an answer already.", button: "Check Again") { Task { await model.start() } }
            } else if s.isDone {
                Done(session: s, seed: model.seed) { Task { await model.start() } }
            } else {
                Cards(session: s, seed: model.seed)
            }
        } else {
            ProgressView().padding(40)
        }
    }
}

private struct Message: View {
    let title: String
    let detail: String
    let button: String
    let action: () -> Void

    var body: some View {
        VStack(spacing: 10) {
            Text(title).font(.title3).fontWeight(.semibold)
            Text(detail).foregroundStyle(.secondary).textSelection(.enabled).multilineTextAlignment(.center)
            Button(button, action: action)
        }
        .padding(40)
    }
}

private struct Done: View {
    let session: AuditSession
    let seed: String?
    let again: () -> Void

    var body: some View {
        VStack(spacing: 10) {
            Text("Audit done").font(.title3).fontWeight(.semibold)
            Text("\(session.answered) answered" + (seed.map { " (draw \($0))" } ?? ""))
                .foregroundStyle(.secondary)
            HStack(spacing: 16) {
                ForEach(AuditVerdict.allCases, id: \.self) { v in
                    Text("\(v.title): \(session.count(v))").monospacedDigit()
                }
            }
            Button("Check for More", action: again)
        }
        .padding(40)
    }
}

private struct Cards: View {
    @Environment(AuditModel.self) private var model
    let session: AuditSession
    let seed: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                Text("Weekly audit").font(.headline)
                if let seed { Text(seed).foregroundStyle(.secondary).monospacedDigit() }
                Spacer()
                Text(session.position).foregroundStyle(.secondary).monospacedDigit()
            }
            ProgressView(value: Double(session.answered), total: Double(max(session.picks.count, 1)))
            if let card = model.card {
                CardBody(card: card)
            } else if let err = model.cardError {
                VStack(alignment: .leading, spacing: 8) {
                    Text("This posting could not be read, and it cannot be judged unseen.").fontWeight(.medium)
                    Text(err).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                    Button("Try Again") { Task { await model.loadCard() } }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            } else {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            }
            HStack(spacing: 10) {
                ForEach(AuditVerdict.allCases, id: \.self) { v in
                    Button {
                        Task { await model.answer(v) }
                    } label: {
                        Text("\(v.title)  \(String(v.shortcut).uppercased())").frame(maxWidth: .infinity)
                    }
                    .keyboardShortcut(KeyEquivalent(v.shortcut), modifiers: [])
                    .controlSize(.large)
                    .disabled(model.card == nil || model.saving)
                }
            }
            Text("Would you apply to this posting? Vani's own reading is hidden so it cannot sway you.")
                .font(.caption).foregroundStyle(.secondary)
        }
        .padding(20)
        .frame(maxWidth: 760, maxHeight: .infinity, alignment: .topLeading)
    }
}

/// Untrusted board text, shown as plain text and never as Markdown or a link.
private struct CardBody: View {
    let card: AuditCard

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(verbatim: card.title).font(.title2).fontWeight(.semibold).textSelection(.enabled)
            Text(verbatim: card.company).font(.title3)
            if let loc = card.location { Text(verbatim: loc).foregroundStyle(.secondary) }
            Divider()
            ScrollView {
                Text(verbatim: card.description ?? "No description.")
                    .foregroundStyle(card.description == nil ? .secondary : .primary)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}
