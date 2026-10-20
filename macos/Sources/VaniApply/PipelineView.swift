import SwiftUI
import VaniApplyKit

/// Whether the daily run happened, how it ended, what it spent against each
/// cap and how the decision models are set up: everything from
/// `vani status --json`, so a failed run (an unset key, a refused request, a
/// spent budget) is visible without opening the logs. A section vani could not
/// read says so; it never shows as zero or fine.
struct PipelineView: View {
    @Environment(PipelineModel.self) private var pipeline

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                if let err = pipeline.error {
                    ErrorBanner(title: "Vani could not report its status", message: err, stale: pipeline.status != nil)
                }
                if let s = pipeline.status {
                    StatusHeader(status: s)
                    LastRunSection(status: s)
                    SpendSection(backends: s.backends)
                    LedgerSection(ledger: s.ledger)
                    DecideSection(decide: s.decide, config: s.config)
                } else if pipeline.error == nil {
                    ProgressView("Reading status…").frame(maxWidth: .infinity, minHeight: 200)
                }
            }
            .padding(16)
            .frame(maxWidth: 760, alignment: .leading)
            .frame(maxWidth: .infinity)
        }
        .navigationTitle("Pipeline")
        .toolbar {
            ToolbarItem {
                Button { Task { await pipeline.refresh() } } label: { Label("Refresh", systemImage: "arrow.clockwise") }
                    .keyboardShortcut("r")
                    .disabled(pipeline.loading)
                    .help("Read the status again (⌘R)")
            }
        }
        .task { await pipeline.refresh() }
    }
}

private struct ErrorBanner: View {
    let title: String
    let message: String
    let stale: Bool
    @State private var copied = false

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: "exclamationmark.octagon.fill").foregroundStyle(.red)
            VStack(alignment: .leading, spacing: 4) {
                Text(title).fontWeight(.semibold)
                Text(message).textSelection(.enabled)
                if stale { Text("What is shown below is from the last successful read.").foregroundStyle(.secondary) }
            }
            Spacer(minLength: 0)
            Button {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString("\(title)\n\(message)", forType: .string)
                copied = true
                Task {
                    try? await Task.sleep(for: .milliseconds(1500))
                    copied = false
                }
            } label: {
                Image(systemName: copied ? "checkmark" : "doc.on.doc")
                    .foregroundStyle(copied ? .green : .secondary)
            }
            .buttonStyle(.borderless)
            .controlSize(.small)
            .help("Copy error details to clipboard")
        }
        .padding(10)
        .background(.red.opacity(0.10), in: RoundedRectangle(cornerRadius: 8))
    }
}

private struct StatusHeader: View {
    let status: PipelineStatus

    var body: some View {
        let issues = status.issues(at: Date())
        let health = status.health(at: Date())
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Image(systemName: symbol(health)).font(.title2).foregroundStyle(color(health))
                Text(title(health)).font(.title2.weight(.semibold))
            }
            ForEach(Array(issues.enumerated()), id: \.offset) { _, issue in
                HStack(alignment: .top, spacing: 8) {
                    Image(systemName: issue.severity == .problem ? "xmark.circle.fill" : "exclamationmark.triangle.fill")
                        .foregroundStyle(issue.severity == .problem ? .red : .orange)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(issue.title)
                        if let d = issue.detail, !d.isEmpty {
                            Text(d).font(.callout).foregroundStyle(.secondary).textSelection(.enabled)
                        }
                    }
                }
            }
        }
    }

    private func title(_ h: PipelineHealth) -> String {
        switch h {
        case .healthy: "Pipeline is healthy"
        case .warning: "Pipeline needs a look"
        case .problem: "Pipeline needs attention"
        }
    }

    private func symbol(_ h: PipelineHealth) -> String {
        switch h {
        case .healthy: "checkmark.circle.fill"
        case .warning: "exclamationmark.triangle.fill"
        case .problem: "xmark.octagon.fill"
        }
    }

    private func color(_ h: PipelineHealth) -> Color {
        switch h {
        case .healthy: .green
        case .warning: .orange
        case .problem: .red
        }
    }
}

private struct Panel<Content: View>: View {
    let title: String
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title).font(.headline)
            VStack(alignment: .leading, spacing: 6) { content }
                .padding(12)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 8))
        }
    }
}

private struct Row: View {
    let label: String
    let value: String
    var secondary = false

    var body: some View {
        HStack(alignment: .firstTextBaseline) {
            Text(label).foregroundStyle(.secondary).frame(width: 130, alignment: .leading)
            Text(value).foregroundStyle(secondary ? .secondary : .primary).textSelection(.enabled)
            Spacer(minLength: 0)
        }
    }
}

private struct LastRunSection: View {
    let status: PipelineStatus

    var body: some View {
        let run = status.lastRun
        Panel(title: "Last run") {
            if !run.ok {
                Row(label: "State", value: "Unknown: \(run.reason ?? "unreadable")")
            } else if run.state == .never {
                Row(label: "State", value: "No run recorded yet")
            } else {
                Row(label: "State", value: stateText(run))
                if let started = run.startedAt {
                    Row(label: "Started", value: "\(started.formatted(date: .abbreviated, time: .shortened)) (\(ago(started)) ago)")
                }
                if let secs = run.durationSec {
                    Row(label: "Took", value: duration(secs))
                }
                if let added = run.postingsAdded {
                    Row(label: "Ledger", value: "\(added >= 0 ? "+" : "")\(added) postings (\(run.postingsBefore ?? 0) → \(run.postingsAfter ?? 0))")
                }
                let changes = run.statusChanges
                if !changes.isEmpty {
                    Row(label: "Status changes", value: changes.map { "\($0.status) \($0.delta > 0 ? "+" : "")\($0.delta)" }.joined(separator: ", "), secondary: true)
                }
                if let e = run.error, !e.isEmpty {
                    Row(label: run.stage.map { "Failed at \($0)" } ?? "Error", value: e)
                }
                if let adv = run.advance {
                    AdvanceRows(advance: adv)
                }
                if let errs = run.backendErrors {
                    BackendErrorRows(errors: errs)
                }
            }
            if let next = status.schedule.nextRun {
                Row(label: "Next run", value: next.formatted(date: .abbreviated, time: .shortened)
                    + (status.schedule.dailyAt.map { " (daily at \($0) \(status.schedule.timezone ?? ""))" } ?? ""), secondary: true)
            } else if !status.schedule.ok {
                Row(label: "Next run", value: "Unknown: \(status.schedule.reason ?? "unreadable")")
            }
        }
    }

    private func stateText(_ run: PipelineStatus.LastRun) -> String {
        switch run.state {
        case .ok: "Succeeded"
        case .failed: "Failed"
        case .cancelled: "Cancelled"
        case .running: "Running now"
        case .interrupted: "Did not finish (its process is gone)"
        case .never: "No run recorded yet"
        case .unknown: "Unknown"
        }
    }

    private func ago(_ d: Date) -> String { PipelineStatus.age(Date().timeIntervalSince(d)) }

    private func duration(_ s: Double) -> String {
        s < 90 ? "\(Int(s.rounded())) s" : "\(Int((s / 60).rounded())) min"
    }
}

/// What the run's advance phase got through, and what the per-run cap or an
/// early stop left waiting for the next run.
private struct AdvanceRows: View {
    let advance: PipelineStatus.LastRun.Advance

    var body: some View {
        Row(label: "Advanced", value: advance.summary)
            .help("Eligible postings are every one waiting for a stage (triage, score or explain), so this can be more than the Jobs tab's awaiting-triage count.")
        if let o = advance.outcome {
            Row(label: "", value: o, secondary: true)
        }
        if let s = advance.stopNote {
            Row(label: "", value: s, secondary: advance.stopped == .cap)
        }
    }
}

/// Why decision backends refused calls this run. The examples are provider
/// error text: shown verbatim, one bounded line each.
private struct BackendErrorRows: View {
    let errors: PipelineStatus.LastRun.BackendErrors

    var body: some View {
        Row(label: "Backend errors", value: errors.summary)
        ForEach(Array(errors.lines().enumerated()), id: \.offset) { _, line in
            HStack(alignment: .firstTextBaseline) {
                // Not a Spacer: the stack drops its spacing next to one, which
                // would put the causes left of the other rows' values.
                Color.clear.frame(width: 130, height: 1)
                VStack(alignment: .leading, spacing: 2) {
                    Text(verbatim: "\(line.backend): \(line.count) call\(line.count == 1 ? "" : "s")").fontWeight(.medium)
                    Text(verbatim: line.example)
                        .font(.callout.monospaced()).foregroundStyle(.secondary)
                        .lineLimit(3).textSelection(.enabled)
                }
                Spacer(minLength: 0)
            }
        }
        if let more = errors.moreNote() {
            Row(label: "", value: more, secondary: true)
        }
    }
}

private struct SpendSection: View {
    let backends: PipelineStatus.Backends

    var body: some View {
        Panel(title: "Spend today (UTC \(backends.day))") {
            if !backends.ok {
                Text("Unknown: \(backends.reason ?? "unreadable")").foregroundStyle(.secondary)
            } else if backends.items.isEmpty {
                Text("No paid backend is configured.").foregroundStyle(.secondary)
            }
            ForEach(backends.items) { b in
                VStack(alignment: .leading, spacing: 4) {
                    HStack {
                        Text(b.name).fontWeight(.medium)
                        Text(b.kind == "llm" ? "LLM" : "OpenRouter").font(.caption).foregroundStyle(.secondary)
                        Spacer()
                        Text(amount(b)).monospacedDigit().foregroundStyle(b.ok ? Color.primary : Color.secondary)
                    }
                    if let f = b.fractionSpent {
                        ProgressView(value: f).tint(f >= 1 ? .red : f >= 0.8 ? .orange : .accentColor)
                    } else if let r = b.reason {
                        Text("Unknown: \(r)").font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                    }
                }
            }
        }
    }

    private func amount(_ b: PipelineStatus.Backend) -> String {
        guard b.ok, let spent = b.spentUSD, let rem = b.remainingUSD else { return "unknown of \(usd(b.capUSD))" }
        return b.exhausted == true ? "\(usd(spent)) of \(usd(b.capUSD)): spent" : "\(usd(spent)) of \(usd(b.capUSD)) (\(usd(rem)) left)"
    }

    private func usd(_ v: Double) -> String { v.formatted(.currency(code: "USD").precision(.fractionLength(2...3))) }
}

private struct LedgerSection: View {
    let ledger: PipelineStatus.LedgerFacet

    var body: some View {
        Panel(title: "Ledger") {
            if !ledger.ok {
                Text("Unknown: \(ledger.reason ?? "unreadable")").foregroundStyle(.secondary)
            } else if !ledger.exists {
                Text(ledger.reason ?? "No ledger yet.").foregroundStyle(.secondary)
            } else {
                Row(label: "Postings", value: "\(ledger.total)")
                ForEach(ledger.byStatus.sorted { $0.value > $1.value || ($0.value == $1.value && $0.key < $1.key) }, id: \.key) { s, n in
                    Row(label: s.capitalized, value: "\(n)", secondary: true)
                }
                if ledger.torn == true {
                    Text("A run was writing as this was read; the counts are complete up to it.").font(.caption).foregroundStyle(.secondary)
                }
            }
        }
    }
}

private struct DecideSection: View {
    let decide: PipelineStatus.Decide
    let config: PipelineStatus.ConfigFacet

    var body: some View {
        Panel(title: "Configuration") {
            if !config.ok {
                Row(label: "Config", value: "Unknown: \(config.reason ?? "unreadable")")
            } else if let boards = config.boards {
                Row(label: "Boards", value: "\(boards)")
            }
            if decide.ok {
                Row(label: "Shadow model", value: decide.shadow.flatMap { $0.isEmpty ? nil : $0 } ?? "off")
                if decide.localConfigured {
                    Row(label: "Local model", value: "\(decide.localModel ?? "unnamed"), \(decide.localMode == "server" ? "server started for each run" : "external endpoint")")
                } else {
                    Row(label: "Local model", value: "not configured", secondary: true)
                }
                Row(label: "Lappi", value: decide.lappiConfigured ? "configured" : "not configured", secondary: !decide.lappiConfigured)
            } else {
                Row(label: "Decision models", value: "Unknown: \(decide.reason ?? "unreadable")")
            }
        }
    }
}
