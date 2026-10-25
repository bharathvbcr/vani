import SwiftUI
import VaniApplyKit

/// The detail pane's "Sonnet re-triage" card for a triaged posting: the stored reading (or that it is
/// out of date), the action, and the progress, comparison or error of the one re-triage that is in
/// flight for this posting. Logic is `RetriageModel` and `RetriagePresentation`; this only draws it.
struct RetriageSection: View {
    @Environment(JobsModel.self) private var jobs
    let job: Job
    /// False while the note has focus: the shortcut then belongs to the note, as ⌘S and ⌘⌫ do.
    let shortcutEnabled: Bool

    private var retriage: RetriageModel { jobs.retriage }
    /// This posting's state; another posting's is not shown here.
    private var phase: RetriageModel.Phase { retriage.phase.key == job.key ? retriage.phase : .idle }
    private var workingOnThis: Bool {
        switch phase {
        case .estimating, .confirming, .running, .clearing: true
        default: false
        }
    }

    var body: some View {
        if retriage.isAvailable, !job.isUntriaged {
            VStack(alignment: .leading, spacing: 12) {
                HStack(alignment: .firstTextBaseline) {
                    Text("Sonnet re-triage").font(.headline)
                    Spacer()
                    Text("manual: you confirm the cost first").font(.caption).foregroundStyle(.secondary)
                }
                state
                if !workingOnThis { actionRow }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .cardStyle()
            .accessibilityElement(children: .contain)
            .accessibilityLabel("Re-triage with Sonnet")
        }
    }

    // MARK: State

    @ViewBuilder
    private var state: some View {
        switch phase {
        case .idle:
            if let m = job.manualTriage { storedReading(m) }
        case .estimating:
            progress("Asking Vani what this would cost…", detail: "Nothing is spent yet.", stoppable: true, stopLabel: "Cancel")
        case .confirming:
            progress("Waiting for your confirmation…", detail: "Nothing is spent until you confirm.", stoppable: false, stopLabel: "Cancel")
        case let .running(_, cancelling):
            progress(cancelling ? "Stopping…" : "Asking Sonnet…",
                     detail: cancelling ? "Vani is being stopped. If Sonnet had already answered, its reading appears here."
                                        : "This can take up to \(Int(RetriageClient.runTimeout)) s.",
                     stoppable: !cancelling, stopLabel: "Cancel")
        case let .succeeded(_, result, at):
            comparison(result, at: at)
        case let .failed(_, failure):
            failureView(failure)
            if let m = job.manualTriage { storedReading(m) }
        case .clearing:
            progress("Clearing Sonnet's reading…", detail: "Nothing is spent.", stoppable: false, stopLabel: "Cancel")
        case let .cleared(_, result, before):
            clearedNote(result, before: before)
        }
    }

    private func progress(_ title: String, detail: String, stoppable: Bool, stopLabel: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            ProgressView().controlSize(.small)
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                Text(detail).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            if stoppable {
                Button(stopLabel) { retriage.cancel() }
                    .accessibilityIdentifier("retriage.cancel")
                    .help("Stop. Nothing more is asked or spent.")
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("retriage.progress")
    }

    // MARK: Stored reading

    private func storedReading(_ m: ManualTriage) -> some View {
        let stale = RetriagePresentation.staleNote(m)
        return VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Label(RetriagePresentation.header(at: m.at), systemImage: "sparkles")
                    .font(.subheadline.weight(.semibold))
                if stale != nil { Chip(text: "out of date", tint: .orange) }
                Spacer()
                Button("Clear", systemImage: "xmark.circle") { retriage.clear(job) }
                    .buttonStyle(.borderless)
                    .disabled(retriage.isBusy || retriage.otherBusy())
                    .accessibilityIdentifier("retriage.clear")
                    .help("Remove Sonnet's reading. Ranking goes back to the readings it used before. Spends nothing.")
            }
            Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 12, verticalSpacing: 4) {
                ForEach(RetriagePresentation.valueRows(m)) { row in
                    GridRow {
                        Text(row.title).foregroundStyle(.secondary).gridColumnAlignment(.trailing)
                        Text(verbatim: row.value)
                    }
                    .accessibilityElement(children: .combine)
                }
            }
            .font(.callout)
            if let stale {
                Label(stale, systemImage: "clock.badge.exclamationmark")
                    .font(.caption)
                    .foregroundStyle(.orange)
                    .fixedSize(horizontal: false, vertical: true)
            }
            ForEach(RetriagePresentation.ruleNotes(status: job.status), id: \.self) { note in
                Text(note).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        }
        // A reading ranking ignores is shown muted: never as current.
        .opacity(stale == nil ? 1 : 0.65)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("retriage.stored")
    }

    // MARK: Result

    private func comparison(_ result: RetriageResult, at: Date) -> some View {
        let rows = RetriagePresentation.diffRows(before: result.before, after: result.after)
        let scores = [RetriagePresentation.scoreLine("Relevance", before: result.before?.relevance, after: result.after.relevance),
                      RetriagePresentation.scoreLine("Priority", before: result.before?.priority, after: result.after.priority)]
            .compactMap { $0 }
        return VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline) {
                Label(RetriagePresentation.header(at: result.after.at ?? at), systemImage: "sparkles")
                    .font(.subheadline.weight(.semibold))
                Spacer()
                Button("Done") { retriage.dismiss() }
                    .accessibilityIdentifier("retriage.done")
            }
            Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 10, verticalSpacing: 6) {
                ForEach(rows) { row in
                    GridRow {
                        Text(row.title).foregroundStyle(.secondary).gridColumnAlignment(.trailing)
                        Text(verbatim: row.before).foregroundStyle(row.changed ? .secondary : .primary)
                        Image(systemName: row.changed ? "arrow.right" : "equal")
                            .foregroundStyle(row.changed ? Color.accentColor : .secondary)
                            .accessibilityHidden(true)
                        Text(verbatim: row.after).fontWeight(row.changed ? .semibold : .regular)
                        if row.changed {
                            Chip(text: "changed", tint: .accentColor)
                        } else {
                            Color.clear.frame(width: 1, height: 1)
                        }
                    }
                    .accessibilityElement(children: .combine)
                    .accessibilityLabel("\(row.title): \(row.before), now \(row.after)\(row.changed ? ", changed" : ", unchanged")")
                }
            }
            .font(.callout)
            ForEach(scores, id: \.self) { line in
                Text(verbatim: line).font(.callout.monospacedDigit())
            }
            Text(RetriagePresentation.orderNote(before: result.before, after: result.after))
                .font(.caption).foregroundStyle(.secondary)
            ForEach(RetriagePresentation.ruleNotes(status: result.status), id: \.self) { note in
                Text(note).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            if let cost = RetriagePresentation.costSummary(result.cost) {
                Text(verbatim: cost).font(.caption).foregroundStyle(.secondary)
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("retriage.result")
    }

    private func clearedNote(_ result: RetriageCleared, before: Double?) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline) {
                Label(result.hadManual == false ? "There was no manual reading to clear." : "Sonnet's reading was cleared.",
                      systemImage: "checkmark.circle")
                Spacer()
                Button("Done") { retriage.dismiss() }
                    .accessibilityIdentifier("retriage.done")
            }
            if let line = RetriagePresentation.scoreLine("Relevance", before: before, after: result.relevance) {
                Text(verbatim: line).font(.callout.monospacedDigit())
            }
            Text("Nothing was spent.").font(.caption).foregroundStyle(.secondary)
        }
        .accessibilityIdentifier("retriage.cleared")
    }

    // MARK: Error

    @ViewBuilder
    private func failureView(_ failure: RetriageFailure) -> some View {
        if let text = RetriagePresentation.errorText(for: failure) {
            VStack(alignment: .leading, spacing: 6) {
                Label {
                    Text(verbatim: text.message).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                } icon: {
                    Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                }
                if let cost = text.costNote {
                    Text(verbatim: cost).font(.callout).fixedSize(horizontal: false, vertical: true)
                }
                HStack {
                    if text.canRetry {
                        Button("Retry") { retriage.retry() }
                            .accessibilityIdentifier("retriage.retry")
                            .help("Ask for the cost again. You confirm before anything is spent.")
                    }
                    Button("Dismiss") { retriage.dismiss() }
                        .accessibilityIdentifier("retriage.dismiss")
                }
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("retriage.error")
        }
    }

    // MARK: Action

    private var actionRow: some View {
        let reason = retriage.blockedReason(for: job)
        let again = job.manualTriage != nil
        return VStack(alignment: .leading, spacing: 4) {
            Button { jobs.startRetriage(job) } label: {
                Label(again ? "Re-run with Sonnet" : "Re-triage with Sonnet", systemImage: "sparkles")
            }
            .buttonStyle(.bordered)
            .controlSize(.large)
            .keyboardShortcut(shortcutEnabled ? RetriageShortcut.keyboardShortcut : nil)
            .disabled(reason != nil)
            .help(reason ?? "Ask Sonnet to read this posting again, with the same four questions (\(RetriageShortcut.label)). It shows the cost first; nothing is spent until you confirm.")
            .accessibilityIdentifier("retriage.start")
            if let reason {
                Text(reason).font(.caption).foregroundStyle(.secondary)
            }
        }
    }
}

/// ⌥⌘R. Not used elsewhere in the app (⌘R and ⇧⌘R are Refresh and Run Now), and not a standard menu key.
enum RetriageShortcut {
    static let keyboardShortcut = KeyboardShortcut("r", modifiers: [.command, .option])
    static let label = "⌥⌘R"
}

/// The cost confirmation: what it would cost, what is sent, and the only button that spends.
struct RetriageConfirmSheet: View {
    @Environment(JobsModel.self) private var jobs

    var body: some View {
        if let pending = jobs.retriage.confirming {
            content(key: pending.key, dry: pending.dry)
        }
    }

    private func content(key: String, dry: RetriageDryRun) -> some View {
        let confirmation = RetriagePresentation.confirmation(for: dry)
        let job = jobs.jobs.first { $0.key == key }
        return VStack(alignment: .leading, spacing: 12) {
            Text("Re-triage with Sonnet").font(.headline)
            if let job {
                Text(verbatim: [job.title, job.company].filter { !$0.isEmpty }.joined(separator: " · "))
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
            Divider()
            Text(verbatim: confirmation.costText)
                .font(.body.monospacedDigit())
                .fixedSize(horizontal: false, vertical: true)
                .help(confirmation.priceSource ?? "")
                .accessibilityIdentifier("retriage.cost")
            Text(verbatim: confirmation.sentText)
                .font(.callout).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if let replaces = confirmation.replacesText {
                Text(replaces).font(.callout).foregroundStyle(.secondary)
            }
            Text("This spends from the same daily cap as drafts and the daily run. " + RetriageWords.ruleSeven)
                .font(.caption).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if let blocked = confirmation.blockedReason {
                Label(blocked, systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("retriage.blocked")
            }
            HStack {
                Spacer()
                Button("Cancel", role: .cancel) { jobs.retriage.cancel() }
                    .keyboardShortcut(.cancelAction)
                    .accessibilityIdentifier("retriage.sheetCancel")
                Button("Ask Sonnet") { jobs.retriage.confirm() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(!confirmation.canConfirm)
                    .accessibilityIdentifier("retriage.confirm")
            }
        }
        .padding(20)
        .frame(width: 460)
    }
}
