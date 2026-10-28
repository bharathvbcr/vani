import AppKit
import SwiftUI
import VaniApplyKit

/// The detail pane while several roles are selected: what the selection holds, the batch Sonnet
/// re-triage, and the roles themselves (each opens on click). Logic is `RetriageBatchModel` and
/// `RetriageBatchPresentation`; this only draws it.
struct SelectionPane: View {
    @Environment(JobsModel.self) private var jobs
    /// Rows listed before "and N more".
    private static let shownRoles = 100

    var body: some View {
        let picked = jobs.selectedJobs
        let s = RetriageBatchPresentation.selection(picked)
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                VStack(alignment: .leading, spacing: 6) {
                    Text(verbatim: RetriageBatchPresentation.selectionLine(s)).font(.title3.weight(.semibold))
                    Text("⌘-click or ⇧-click rows in the list to change the selection.")
                        .font(.caption).foregroundStyle(.secondary)
                    Button("Clear Selection") { jobs.selectedKeys = [] }
                        .accessibilityIdentifier("selection.clear")
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .cardStyle()
                if jobs.batch.isAvailable { batchCard(picked, s) }
                rolesCard(picked)
            }
            .padding(16)
        }
        .accessibilityIdentifier("selection.pane")
    }

    private func batchCard(_ picked: [Job], _ s: RetriageBatchPresentation.Selection) -> some View {
        let reason = jobs.batch.blockedReason(for: picked)
        return VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline) {
                Text("Sonnet re-triage").font(.headline)
                Spacer()
                Text("manual: you confirm the cost first").font(.caption).foregroundStyle(.secondary)
            }
            Text("Sonnet reads each role with the same four questions triage asks, a few at a time. You see the total cost first; nothing is spent until you confirm, and you can stop at any point and keep what was read.")
                .font(.callout).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if jobs.batch.isBusy, case .running = jobs.batch.phase {
                RetriageBatchProgress()
            } else {
                Button { jobs.startBatchRetriage() } label: {
                    Label(RetriageBatchPresentation.actionTitle(s), systemImage: "sparkles")
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .keyboardShortcut(RetriageShortcut.keyboardShortcut)
                .disabled(reason != nil)
                .help(reason ?? "Ask Sonnet to read the selected roles again (\(RetriageShortcut.label)). The cost is shown first.")
                .accessibilityIdentifier("batch.start")
                if let reason {
                    Text(reason).font(.caption).foregroundStyle(.secondary)
                }
            }
            Text(RetriageWords.ruleSeven).font(.caption).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .cardStyle()
    }

    private func rolesCard(_ picked: [Job]) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Selected roles").font(.headline)
            ForEach(picked.prefix(Self.shownRoles)) { job in
                Button { jobs.selection = job.key } label: {
                    HStack(spacing: 8) {
                        VStack(alignment: .leading, spacing: 1) {
                            Text(verbatim: job.title).lineLimit(1)
                            Text(verbatim: job.company).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                        }
                        Spacer(minLength: 8)
                        if let state = jobs.batch.rows[job.key] {
                            let label = RetriageBatchPresentation.rowLabel(state)
                            Chip(text: label.text, tint: batchTint(state)).help(label.help)
                        } else if job.isUntriaged {
                            Chip(text: "Not triaged yet", tint: .orange)
                        } else if job.manualTriage?.current == true {
                            Chip(text: "Sonnet", tint: .purple)
                        }
                        Image(systemName: "chevron.right").font(.caption).foregroundStyle(.tertiary)
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .help("Open this role alone")
            }
            if picked.count > Self.shownRoles {
                Text("and \(picked.count - Self.shownRoles) more").font(.caption).foregroundStyle(.secondary)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .cardStyle()
    }
}

/// The right-click menu over several selected rows.
struct SelectionMenu: View {
    @Environment(JobsModel.self) private var jobs
    let picked: [Job]

    var body: some View {
        if jobs.batch.isAvailable {
            let reason = jobs.batch.blockedReason(for: picked)
            Button(RetriageBatchPresentation.actionTitle(RetriageBatchPresentation.selection(picked)), systemImage: "sparkles") {
                jobs.selectedKeys = Set(picked.map(\.key))
                jobs.batch.start(picked)
            }
            .disabled(reason != nil)
            .help(reason ?? "Ask Sonnet to read these roles again. The cost is shown first.")
            Divider()
        }
        Button("Copy \(picked.count) Titles", systemImage: "character.cursor.ibeam") {
            copy(picked.map { "\($0.title) · \($0.company)" })
        }
        let links = picked.compactMap { ($0.shareLink ?? $0.postingLink)?.absoluteString }
        if !links.isEmpty {
            Button("Copy \(links.count) Links", systemImage: "link") { copy(links) }
        }
        Button("Export \(picked.count) to Excel…", systemImage: "tablecells") {
            exportJobs(jobs, .keys(picked.map(\.key)))
        }
        .disabled(jobs.exporting)
    }

    private func copy(_ lines: [String]) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(lines.joined(separator: "\n"), forType: .string)
    }
}

/// A batch's progress bar, counts and Stop.
struct RetriageBatchProgress: View {
    @Environment(JobsModel.self) private var jobs

    var body: some View {
        let b = jobs.batch
        if case let .running(total, cancelling) = b.phase {
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 10) {
                    ProgressView(value: Double(min(b.kept + b.failedCount, total)), total: Double(max(total, 1)))
                    if cancelling {
                        Text("Stopping…").font(.caption).foregroundStyle(.secondary)
                    } else {
                        Button("Stop") { b.cancel() }
                            .accessibilityIdentifier("batch.stop")
                            .help("Stop asking. Readings Sonnet already made are kept; nothing more is spent.")
                    }
                }
                Text(verbatim: RetriageBatchPresentation.progressLine(done: b.kept, failed: b.failedCount, total: total, usd: b.usd))
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("batch.progress")
        }
    }
}

/// Above the list, whatever is selected: a batch being estimated, running, finished or failed.
struct RetriageBatchBanner: View {
    @Environment(JobsModel.self) private var jobs

    var body: some View {
        let b = jobs.batch
        switch b.phase {
        case .idle, .confirming:
            EmptyView()
        case let .estimating(count):
            banner(tint: .secondary) {
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text("Asking Vani what reading \(count) \(count == 1 ? "role" : "roles") would cost… Nothing is spent yet.")
                        .font(.callout)
                    Spacer()
                    Button("Cancel") { b.cancel() }
                }
            }
        case .running:
            banner(tint: .purple) {
                VStack(alignment: .leading, spacing: 4) {
                    Label("Sonnet re-triage", systemImage: "sparkles").font(.subheadline.weight(.semibold))
                    RetriageBatchProgress()
                    Text("The list re-sorts when the batch ends, so rows stay put while you look.")
                        .font(.caption).foregroundStyle(.secondary)
                }
            }
        case let .finished(summary):
            banner(tint: summary.stopped == nil && summary.failed == 0 ? .green : .orange) {
                VStack(alignment: .leading, spacing: 6) {
                    Label(RetriageBatchPresentation.summaryLine(summary),
                          systemImage: summary.stopped == nil && summary.failed == 0 ? "checkmark.circle.fill" : "exclamationmark.circle.fill")
                        .font(.subheadline.weight(.semibold))
                    ForEach(RetriageBatchPresentation.summaryDetails(summary), id: \.self) { line in
                        Text(verbatim: line).font(.caption).foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    HStack {
                        if !b.changedKeys.isEmpty {
                            Button("Select \(b.changedKeys.count) Changed") { jobs.selectChanged() }
                                .help("Select the roles whose fit Sonnet changed, to look them over")
                                .accessibilityIdentifier("batch.selectChanged")
                        }
                        if !b.retryable.isEmpty {
                            Button("Retry \(b.retryable.count)…") { jobs.retryBatch() }
                                .help("Ask again about the roles that failed or were not asked. The cost is shown first.")
                                .accessibilityIdentifier("batch.retry")
                        }
                        Spacer()
                        Button("Done") { b.dismiss() }
                            .accessibilityIdentifier("batch.done")
                    }
                }
            }
        case let .failed(failure):
            let text = RetriagePresentation.errorText(for: failure)
            banner(tint: .orange) {
                VStack(alignment: .leading, spacing: 6) {
                    Label {
                        Text(verbatim: text?.message ?? failure.message).textSelection(.enabled)
                            .fixedSize(horizontal: false, vertical: true)
                    } icon: {
                        Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                    }
                    if let cost = text?.costNote {
                        Text(verbatim: cost).font(.caption)
                    }
                    if b.kept > 0 {
                        Text("\(b.kept) \(b.kept == 1 ? "reading was" : "readings were") kept before it failed.")
                            .font(.caption).foregroundStyle(.secondary)
                    }
                    HStack {
                        Spacer()
                        Button("Dismiss") { b.dismiss() }
                            .accessibilityIdentifier("batch.dismiss")
                    }
                }
            }
        }
    }

    private func banner(tint: Color, @ViewBuilder _ content: () -> some View) -> some View {
        VStack(spacing: 0) {
            content()
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
                .background(tint.opacity(0.08))
            Divider()
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("batch.banner")
    }
}

/// The batch's cost confirmation: the total, what the cap allows, what is skipped, and the only button
/// that spends.
struct RetriageBatchConfirmSheet: View {
    @Environment(JobsModel.self) private var jobs

    var body: some View {
        if let plan = jobs.batch.confirming {
            content(plan)
        }
    }

    private func content(_ plan: RetriageBatchPlan) -> some View {
        @Bindable var batch = jobs.batch
        let c = RetriageBatchPresentation.confirmation(for: plan, skipRead: batch.skipRead)
        return VStack(alignment: .leading, spacing: 12) {
            Text(verbatim: c.title).font(.headline)
            Divider()
            Text(verbatim: c.costText)
                .font(.body.monospacedDigit())
                .fixedSize(horizontal: false, vertical: true)
                .help(c.priceSource ?? "")
                .accessibilityIdentifier("batch.cost")
            if let warning = c.capWarning {
                Label(warning, systemImage: "gauge.with.dots.needle.67percent")
                    .font(.callout)
                    .foregroundStyle(.orange)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let read = c.alreadyReadText {
                VStack(alignment: .leading, spacing: 2) {
                    Toggle("Read them again", isOn: Binding(get: { !batch.skipRead }, set: { batch.skipRead = !$0 }))
                        .accessibilityIdentifier("batch.readAgain")
                    Text(verbatim: read + (batch.skipRead ? " They are left out." : " Asking again replaces their readings."))
                        .font(.caption).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            ForEach(c.skippedLines, id: \.self) { line in
                Label(line, systemImage: "minus.circle").font(.callout).foregroundStyle(.secondary)
            }
            Text(verbatim: c.sentText)
                .font(.callout).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            Text("This spends from the same daily cap as drafts and the daily run. You can stop at any point and keep what was read. " + RetriageWords.ruleSeven)
                .font(.caption).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if let blocked = c.blockedReason {
                Label(blocked, systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("batch.blocked")
            }
            HStack {
                Spacer()
                Button("Cancel", role: .cancel) { batch.cancel() }
                    .keyboardShortcut(.cancelAction)
                    .accessibilityIdentifier("batch.sheetCancel")
                Button("Ask Sonnet") { batch.confirm() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(!c.canConfirm)
                    .accessibilityIdentifier("batch.confirm")
            }
        }
        .padding(20)
        .frame(width: 480)
    }
}
