import SwiftUI
import VaniApplyKit

/// Whether the daily run happened, how it ended, what it spent against each
/// cap and how the decision models are set up: everything from
/// `vani status --json`, so a failed run (an unset key, a refused request, a
/// spent budget) is visible without opening the logs. A section vani could not
/// read says so; it never shows as zero or fine.
struct PipelineView: View {
    @Environment(PipelineModel.self) private var pipeline
    @State private var showRunSheet = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                if let err = pipeline.error {
                    ErrorBanner(title: "Vani could not report its status", message: err, stale: pipeline.status != nil)
                }
                if let err = pipeline.runError {
                    ErrorBanner(title: "The manual run could not start", message: err, stale: false)
                }
                if pipeline.startingRun || pipeline.runInProgress || pipeline.waitingForDaemon {
                    RunningBanner()
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
                Button { showRunSheet = true } label: { Label("Run Now…", systemImage: "play.fill") }
                    .keyboardShortcut("r", modifiers: [.command, .shift])
                    .disabled(pipeline.runBlocker != nil)
                    .help(pipeline.runBlocker ?? "Run the pipeline now, with options for this run only (⇧⌘R)")
            }
            ToolbarItem {
                Button { Task { await pipeline.refresh() } } label: { Label("Refresh", systemImage: "arrow.clockwise") }
                    .keyboardShortcut("r")
                    .disabled(pipeline.loading)
                    .help("Read the status again (⌘R)")
            }
        }
        .task { await pipeline.refresh() }
        .sheet(isPresented: $showRunSheet) {
            RunNowSheet(initial: pipeline.savedOptions) { options in
                Task { await pipeline.startRun(options) }
            }
        }
    }
}

/// A run going now: started here (Stop sends it SIGTERM, and vani records it
/// cancelled) or elsewhere (the daemon, a shell), which this app never signals.
private struct RunningBanner: View {
    @Environment(PipelineModel.self) private var pipeline

    var body: some View {
        HStack(spacing: 10) {
            ProgressView().controlSize(.small)
            VStack(alignment: .leading, spacing: 2) {
                Text(title).fontWeight(.semibold)
                Text(detail).font(.callout).foregroundStyle(.secondary)
            }
            Spacer(minLength: 0)
            if let log = pipeline.lastLog, pipeline.manualRun != nil {
                Button("Show Log") { NSWorkspace.shared.activateFileViewerSelecting([log]) }
                    .help(log.path)
            }
            if pipeline.manualRun?.isRunning == true {
                Button("Stop", role: .destructive) { pipeline.stopRun() }
                    .help("Cancel this run; what it already wrote to the ledger stays")
            }
        }
        .padding(10)
        .background(.blue.opacity(0.08), in: RoundedRectangle(cornerRadius: 8))
    }

    private var title: String {
        if pipeline.startingRun { return "Starting a run…" }
        if pipeline.waitingForDaemon { return "Waiting for the daemon to pick up your run" }
        return pipeline.manualRun != nil ? "Your run is going" : "A run is going"
    }

    private var detail: String {
        if pipeline.startingRun {
            return pipeline.daemonRunning ? "Asking the daemon, which holds the ledger, to make this run." : "Reading the model keys and starting vani."
        }
        if pipeline.waitingForDaemon {
            return "The daemon checks for it every few seconds, after any run it is making now. It runs with the daemon's own keys and can be stopped only by stopping the daemon."
        }
        let run = pipeline.status?.lastRun
        let started = run?.state == .running ? run?.startedAt.map { "Started \(PipelineStatus.age(Date().timeIntervalSince($0))) ago. " } ?? "" : ""
        let elsewhere = pipeline.manualRun == nil ? "It was started by the daemon or a shell, so it can be stopped only there. " : ""
        return started + elsewhere + "This page updates every few seconds."
    }
}

/// The options for one run started by hand: `vani run-once`'s flags. They
/// change this run only; the config, and the daily run, stay as they are.
private struct RunNowSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let start: (ManualRunOptions) -> Void

    @State private var allBoards: Bool
    @State private var chosen: Set<String>
    @State private var advance: Bool
    @State private var capOn: Bool
    @State private var cap: Int
    @State private var digest: Bool
    @State private var boards: [WatchlistEntry] = []
    @State private var boardsError: String?
    @State private var loadingBoards = true
    @State private var filter = ""

    init(initial: ManualRunOptions, start: @escaping (ManualRunOptions) -> Void) {
        self.start = start
        _allBoards = State(initialValue: initial.boards.isEmpty)
        _chosen = State(initialValue: Set(initial.boards))
        _advance = State(initialValue: !initial.listOnly)
        _capOn = State(initialValue: initial.advance > 0)
        _cap = State(initialValue: initial.advance > 0 ? initial.advance : 50)
        _digest = State(initialValue: !initial.noDigest)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Run the pipeline now").font(.headline)
            Text("These choices apply to this run only. Your config and the daily run stay as they are.")
                .font(.callout).foregroundStyle(.secondary)
            Form {
                Section("Boards") {
                    Picker("Boards", selection: $allBoards) {
                        Text("Every board on the watchlist").tag(true)
                        Text("Only the boards I choose").tag(false)
                    }
                    .pickerStyle(.radioGroup)
                    .labelsHidden()
                    if !allBoards { boardPicker }
                }
                Section("Triage, scoring and explaining") {
                    Toggle("Advance new postings this run", isOn: $advance)
                        .help("Off lists and reconciles boards only: new postings stay live for the next run. No decision model is called, but one must still be configured.")
                    if advance {
                        Toggle("Cap the postings advanced this run (instead of advance_per_run)", isOn: $capOn)
                        if capOn {
                            Stepper(value: $cap, in: 1...ManualRunOptions.maxAdvance, step: cap < 50 ? 5 : 25) {
                                HStack {
                                    Text("At most")
                                    TextField("Postings", value: $cap, format: .number)
                                        .frame(width: 70)
                                        .multilineTextAlignment(.trailing)
                                    Text("postings")
                                }
                            }
                        }
                    }
                }
                Section("Digest") {
                    Toggle("Write a digest when the run ends", isOn: $digest)
                    Text(digest
                         ? "The roles it lists are marked digested, so the next daily digest will not list them again."
                         : "The run's roles stay pending and the next digest lists them.")
                        .font(.caption).foregroundStyle(.secondary)
                }
            }
            .formStyle(.grouped)
            if let problem {
                Text(problem).font(.callout).foregroundStyle(.red)
            }
            HStack {
                Text("A run is refused while the daemon or another vani holds the ledger.")
                    .font(.caption).foregroundStyle(.secondary)
                Spacer()
                Button("Cancel", role: .cancel) { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Start Run") {
                    start(options)
                    dismiss()
                }
                .keyboardShortcut(.defaultAction)
                .disabled(problem != nil)
            }
        }
        .padding(20)
        .frame(width: 560, height: allBoards ? 560 : 720)
        .task { await loadBoards() }
    }

    private var options: ManualRunOptions {
        ManualRunOptions(advance: advance && capOn ? cap : 0,
                         boards: allBoards ? [] : boards.map(ref).filter(chosen.contains),
                         listOnly: !advance,
                         noDigest: !digest)
    }

    /// Why Start Run is off: what run-once would refuse.
    private var problem: String? {
        if !allBoards {
            if loadingBoards { return "Reading the watchlist…" }
            if boardsError != nil { return "The watchlist could not be read, so boards cannot be chosen." }
            if options.boards.isEmpty { return "Choose at least one board, or run every board." }
        }
        do { _ = try options.arguments() } catch { return String(describing: error) }
        return nil
    }

    private func ref(_ b: WatchlistEntry) -> String { ManualRunOptions.ref(ats: b.ats, token: b.token) }

    @ViewBuilder private var boardPicker: some View {
        if loadingBoards {
            ProgressView().controlSize(.small)
        } else if let boardsError {
            Text(boardsError).font(.callout).foregroundStyle(.red).textSelection(.enabled)
        } else if boards.isEmpty {
            Text("The watchlist has no boards.").foregroundStyle(.secondary)
        } else {
            TextField("Filter", text: $filter, prompt: Text("Company or token"))
            let shown = boards.filter { filter.isEmpty || $0.title.localizedCaseInsensitiveContains(filter) || $0.token.localizedCaseInsensitiveContains(filter) }
            List(shown, id: \.id) { b in
                Toggle(isOn: Binding(get: { chosen.contains(ref(b)) },
                                     set: { if $0 { chosen.insert(ref(b)) } else { chosen.remove(ref(b)) } })) {
                    HStack {
                        Text(b.title)
                        Text("\(b.kind) · \(b.token)").font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
            .frame(minHeight: 160, maxHeight: 220)
            HStack {
                Text("\(options.boards.count) of \(boards.count) chosen").font(.caption).foregroundStyle(.secondary)
                Spacer()
                Button("Choose Shown") { shown.forEach { chosen.insert(ref($0)) } }.controlSize(.small)
                Button("Clear") { chosen.removeAll() }.controlSize(.small)
            }
        }
    }

    private func loadBoards() async {
        let client = model.jobsClient
        let result = await Task.detached(priority: .userInitiated) { Result { try client.watchlist() } }.value
        loadingBoards = false
        switch result {
        case let .success(list):
            // One row per board as run-once names it (ats:token, case aside).
            var seen = Set<String>()
            boards = list.filter { seen.insert(ref($0).lowercased()).inserted }
            // A board saved last time and since removed is dropped, not sent.
            let known = Set(boards.map(ref))
            chosen = chosen.filter(known.contains)
        case let .failure(err):
            boardsError = String(describing: err)
        }
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
                if let trigger = run.trigger {
                    Row(label: "Started by", value: triggerText(trigger) + (run.options?.summary.map { ": \($0)" } ?? ""))
                }
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
                if let boards = run.boards {
                    BoardRows(boards: boards)
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

    private func triggerText(_ t: PipelineStatus.LastRun.Trigger) -> String {
        switch t {
        case .schedule: "The daily schedule"
        case .manual: "You (Run Now or vani run-once)"
        case .unknown(let raw): PipelineStatus.bounded(raw, max: 40)
        }
    }

    private func ago(_ d: Date) -> String { PipelineStatus.age(Date().timeIntervalSince(d)) }

    private func duration(_ s: Double) -> String {
        s < 90 ? "\(Int(s.rounded())) s" : "\(Int((s / 60).rounded())) min"
    }
}

/// Which boards the run read, and why any was not: a board that fails to list
/// leaves its postings as they were, so without this it looks like a quiet board.
private struct BoardRows: View {
    let boards: PipelineStatus.LastRun.Boards

    var body: some View {
        Row(label: "Boards", value: boards.summary)
        if let o = boards.outcome {
            Row(label: "", value: o, secondary: true)
        }
        ForEach(Array(boards.problems().enumerated()), id: \.offset) { _, line in
            HStack(alignment: .firstTextBaseline) {
                // Not a Spacer: see BackendErrorRows.
                Color.clear.frame(width: 130, height: 1)
                VStack(alignment: .leading, spacing: 2) {
                    Text(verbatim: "\(line.board): \(line.outcome)").fontWeight(.medium)
                    if !line.error.isEmpty {
                        Text(verbatim: line.error)
                            .font(.callout.monospaced()).foregroundStyle(.secondary)
                            .lineLimit(3).textSelection(.enabled)
                    }
                }
                Spacer(minLength: 0)
            }
        }
        if let more = boards.moreNote() {
            Row(label: "", value: more, secondary: true)
        }
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
                        Text(b.kind == "llm" ? "LLM" : b.kind == "openrouter_notes" ? "OpenRouter notes" : "OpenRouter").font(.caption).foregroundStyle(.secondary)
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
