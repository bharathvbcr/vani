import SwiftUI
import VaniApplyKit

/// Draft a long free-text answer with Claude (through `vani draft`), edit it, and
/// save it as a review-flagged answer.
struct DraftSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var question = ""
    @State private var company = ""
    @State private var title = ""
    @State private var posting = ""
    @State private var words = 180
    @State private var draft = ""
    @State private var running = false
    @State private var status: String?

    /// Opens with the job's fields filled in (the Apply kit); no arguments opens it empty.
    init(company: String = "", title: String = "", posting: String = "") {
        _company = State(initialValue: company)
        _title = State(initialValue: title)
        _posting = State(initialValue: posting)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Draft an answer with Claude").font(.headline)
            Form {
                TextField("Question", text: $question, prompt: Text("Why do you want to work here?"))
                TextField("Company", text: $company)
                TextField("Job title", text: $title)
                Stepper("At most \(words) words", value: $words, in: 40...400, step: 20)
            }
            Text("Job posting (paste the description)").font(.caption).foregroundStyle(.secondary)
            TextEditor(text: $posting).frame(minHeight: 90).border(.separator)
            HStack {
                Button(running ? "Drafting…" : "Draft") { Task { await run() } }
                    .disabled(running || question.trimmingCharacters(in: .whitespaces).isEmpty)
                if running { ProgressView().controlSize(.small) }
                Spacer()
                Text("Claude sees the question, the posting and matching knowledge-base bullets. Never your name, contact details or work authorization.")
                    .font(.caption).foregroundStyle(.secondary).multilineTextAlignment(.trailing)
            }
            if let status {
                Text(status).font(.callout).foregroundStyle(.secondary).textSelection(.enabled)
            }
            Text("Draft (edit before saving)").font(.caption).foregroundStyle(.secondary)
            TextEditor(text: $draft).frame(minHeight: 150).border(.separator)
            HStack {
                Text("Saved drafts fill yellow, for you to review on each form.").font(.caption).foregroundStyle(.secondary)
                Spacer()
                Button("Close", role: .cancel) { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Save as Answer") { if model.saveDraft(question: question, text: draft) { dismiss() } }
                    .keyboardShortcut(.defaultAction)
                    .disabled(draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || model.storeError != nil)
            }
        }
        .padding(20)
        .frame(width: 620)
    }

    private func run() async {
        running = true
        status = nil
        defer { running = false }
        let job = DraftRequest.Job(company: company.isEmpty ? nil : company,
                                   title: title.isEmpty ? nil : title,
                                   description: posting.isEmpty ? nil : posting)
        let reply = await model.draft(DraftRequest(question: question, job: job, maxWords: words))
        if let text = reply.draft {
            draft = text
            status = "Drafted with \(reply.model ?? "Claude") from \(reply.evidence_items ?? 0) knowledge-base entries."
        } else {
            status = message(for: reply)
        }
    }

    private func message(for r: DraftReply) -> String {
        switch r.code {
        case "budget_exhausted": "Today's Claude budget is spent (decide.daily_usd.llm in Vani's config). Try again tomorrow."
        case "account": "anthropic-account could not start Vani with an Anthropic key: \(r.error ?? ""). Check the Anthropic account in Settings, or run `anthropic-account doctor`."
        case "config": "Vani is not set up for drafting: \(r.error ?? ""). Check Settings ▸ Claude drafting."
        case "timeout": "Claude did not answer in time. Try again."
        default: r.error ?? "Drafting failed."
        }
    }
}
