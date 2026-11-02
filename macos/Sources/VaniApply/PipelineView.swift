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
        @Bindable var pipeline = pipeline
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
                    ScheduleSection(daemon: s.daemon)
                    LastRunSection(status: s)
                    SpendSection(backends: s.backends)
                    LedgerSection(ledger: s.ledger)
                    DecideSection(decide: s.decide, config: s.config)
                } else if pipeline.error == nil {
                    ProgressView("Reading status…").frame(maxWidth: .infinity, minHeight: 200)
                }
            }
            .paneColumn()
        }
        .navigationTitle("Pipeline")
        .toolbar {
            ToolbarItem {
                Button {
                    pipeline.openRunNow()
                } label: { Label("Run Now…", systemImage: "play.fill") }
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
        .sheet(item: $pipeline.runSheet) { sheet in
            let shown = sheet.presentation(daemonRunning: pipeline.daemonRunning)
            RunNowSheet(presentation: shown) { options in
                let remember = shown.remembers
                Task { await pipeline.startRun(options, remember: remember) }
            }
        }
    }
}

/// A run going now: started here (Stop sends it SIGTERM, and vani records it
/// cancelled) or elsewhere (the daemon, a shell), which this app never signals.
private struct RunningBanner: View {
    @Environment(PipelineModel.self) private var pipeline

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .top, spacing: 10) {
                if liveProgress?.meter == nil {
                    ProgressView().controlSize(.small).padding(.top, 2)
                }
                VStack(alignment: .leading, spacing: 2) {
                    Text(title).fontWeight(.semibold)
                    if let id = liveProgress?.portalID, !id.isEmpty {
                        Text(verbatim: id)
                            .font(.callout.monospaced())
                            .foregroundStyle(.secondary)
                            .textSelection(.enabled)
                    }
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
            if let live = liveProgress, live.ok, let meter = live.meter {
                ProgressView(value: Double(meter.done), total: Double(max(meter.total, 1)))
                    .tint(tint(live.attention))
                    .accessibilityLabel("Progress")
                    .accessibilityValue(live.placeText)
                VStack(alignment: .leading, spacing: 2) {
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(live.placeText).monospacedDigit()
                        if !live.phaseNote.isEmpty {
                            Text(verbatim: live.phaseNote)
                        }
                    }
                    if !live.countsText.isEmpty {
                        Text(verbatim: live.countsText)
                            .monospacedDigit()
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .font(.callout)
                .foregroundStyle(.secondary)
            } else if !detail.isEmpty {
                Text(detail).font(.callout).foregroundStyle(.secondary)
            }
            if !pipeline.startingRun, !pipeline.waitingForDaemon {
                Text(footnote)
                    .font(footnoteIsQuiet ? .caption : .callout)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(wash, in: RoundedRectangle(cornerRadius: 8))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("pipeline.running")
    }

    /// The snapshot of a run that is already going. Hidden while this app is
    /// still starting one or waiting for the daemon to pick it up.
    private var liveProgress: PipelineStatus.LastRun.Progress? {
        guard !pipeline.startingRun, !pipeline.waitingForDaemon else { return nil }
        guard pipeline.status?.lastRun.state == .running else { return nil }
        return pipeline.status?.lastRun.progress
    }

    private var title: String {
        if pipeline.startingRun { return "Starting a run…" }
        if pipeline.waitingForDaemon { return "Waiting for the daemon to pick up your run" }
        if let live = liveProgress, live.ok, !live.headline.isEmpty { return live.headline }
        return pipeline.manualRun != nil ? "Your run is going" : "A run is going"
    }

    /// The sentence under the title when there is no bar: starting, waiting,
    /// a snapshot that could not be read, or a stage with no fraction.
    private var detail: String {
        if pipeline.startingRun {
            return pipeline.daemonRunning ? "Asking the daemon, which holds the ledger, to make this run." : "Reading the model keys and starting vani."
        }
        if pipeline.waitingForDaemon {
            return "The daemon checks for it every few seconds, after any run it is making now. It runs with the daemon's own keys and can be stopped only by stopping the daemon."
        }
        guard let live = liveProgress else { return "" }
        if !live.ok {
            let why = PipelineStatus.bounded(live.reason ?? "unreadable", max: 300)
            return "Live progress could not be read: \(why)."
        }
        return live.line
    }

    /// Quiet once the bar or the progress line is already on screen.
    private var footnoteIsQuiet: Bool {
        guard let live = liveProgress, live.ok else { return false }
        return live.meter != nil || !live.line.isEmpty
    }

    /// How long the run has been going, and that a run started outside this
    /// app cannot be stopped from the button.
    private var footnote: String {
        let run = pipeline.status?.lastRun
        let started = run?.state == .running ? run?.startedAt.map { "Started \(PipelineStatus.age(Date().timeIntervalSince($0))) ago. " } ?? "" : ""
        let elsewhere = pipeline.manualRun == nil ? "It was started by the daemon or a shell, so it can be stopped only there. " : ""
        let fresh = liveProgress?.updatedText(at: Date()) ?? ""
        let tail = fresh.isEmpty ? "This page updates every few seconds." : fresh
        return started + elsewhere + tail
    }

    private var wash: Color {
        switch liveProgress?.attention {
        case .caution: Color.orange.opacity(0.12)
        case .problem: Color.red.opacity(0.10)
        default: Color.blue.opacity(0.08)
        }
    }

    private func tint(_ attention: PipelineStatus.LastRun.Progress.Attention) -> Color {
        switch attention {
        case .calm: .accentColor
        case .caution: .orange
        case .problem: .red
        }
    }
}

/// The options for one run started by hand: `vani run-once`'s flags. They
/// change this run only; the config, and the daily run, stay as they are.
private struct RunNowSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let presentation: RunSheetPresentation
    let start: (ManualRunOptions) -> Void

    @State private var allBoards: Bool
    @State private var chosen: Set<String>
    @State private var advance: Bool
    @State private var capOn: Bool
    @State private var cap: Int
    @State private var digest: Bool
    @State private var usFirst: Bool
    @State private var boards: [WatchlistEntry] = []
    @State private var boardsError: String?
    @State private var loadingBoards = true
    @State private var filter = ""

    init(presentation: RunSheetPresentation, start: @escaping (ManualRunOptions) -> Void) {
        self.presentation = presentation
        self.start = start
        let initial = presentation.options
        _allBoards = State(initialValue: initial.boards.isEmpty)
        _chosen = State(initialValue: Set(initial.boards))
        _advance = State(initialValue: !initial.listOnly)
        _capOn = State(initialValue: initial.advance > 0)
        _cap = State(initialValue: initial.advance > 0 ? initial.advance : 50)
        _digest = State(initialValue: !initial.noDigest)
        _usFirst = State(initialValue: initial.usFirst && !initial.listOnly)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            if let banner = presentation.banner {
                Text(banner).font(.callout).foregroundStyle(.secondary)
            }
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
                        .help("Off lists and reconciles boards only: new postings stay live for the next run. No decision model is called, and none needs to be configured.")
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
                        Toggle("Triage US postings first", isOn: $usFirst)
                        Text("Advance postings located in the United States before the rest. It only changes the order; nothing is dropped.")
                            .font(.caption).foregroundStyle(.secondary)
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
            HStack(alignment: .center) {
                Text(presentation.footnote)
                    .font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
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
        .frame(width: 560, height: (allBoards ? 560 : 720) + (presentation.banner == nil ? 0 : 110))
        .task { await loadBoards() }
    }

    private var options: ManualRunOptions {
        ManualRunOptions(advance: advance && capOn ? cap : 0,
                         boards: allBoards ? [] : boards.map(ref).filter(chosen.contains),
                         listOnly: !advance,
                         noDigest: !digest,
                         usFirst: advance && usFirst)
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
        let result = await BlockingWork.run { try client.watchlist() }
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
                Image(systemName: symbol(health)).font(.title2).foregroundStyle(health.tint)
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
    /// Untrusted text (a stage note, a poll error) is shown as written, never as Markdown.
    var verbatim = false

    var body: some View {
        HStack(alignment: .firstTextBaseline) {
            Text(label).foregroundStyle(.secondary).frame(width: 130, alignment: .leading)
            Group {
                if verbatim {
                    Text(verbatim: value)
                } else {
                    Text(value)
                }
            }
            .foregroundStyle(secondary ? .secondary : .primary)
            .textSelection(.enabled)
            Spacer(minLength: 0)
        }
    }
}

/// "Run on schedule": registers the schedule agent, the login item that keeps `vani daemon`
/// running (ScheduleAgent.swift). Off by default; what it says comes from ScheduleToggle, from
/// what macOS says about the registration and what the daemon announces in `vani status`.
private struct ScheduleSection: View {
    @Environment(AppModel.self) private var app
    let daemon: PipelineStatus.Daemon?

    var body: some View {
        let state = ScheduleToggle.make(registration: app.scheduleStatus, error: app.scheduleError,
                                        daemon: daemon, leftAlone: app.agentLeftAlone, copy: app.scheduleCopy)
        Panel(title: "Run on schedule") {
            Toggle(isOn: Binding(get: { state.isOn }, set: { app.setScheduleEnabled($0) })) {
                Text(state.headline).foregroundStyle(tone(state.tone))
            }
            .disabled(!state.isEnabled || app.scheduleBusy)
            .accessibilityIdentifier("pipeline.runOnSchedule")
            if let detail = state.detail, !detail.isEmpty {
                Text(detail).font(.callout).foregroundStyle(.secondary).textSelection(.enabled)
            }
            if state.offersLoginItems || state.offersRepair {
                HStack {
                    if state.offersLoginItems {
                        Button("Open Login Items…") { app.openScheduleLoginItems() }
                    }
                    if state.offersRepair {
                        Button("Register Again") { app.setScheduleEnabled(true, repair: true) }
                            .disabled(app.scheduleBusy)
                    }
                }
            }
            Text("Runs Vani's daemon in the background, so the daily run happens with this app closed. The decision key is read from your login Keychain and held only by that process; it is never written to a file.")
                .font(.caption).foregroundStyle(.secondary)
        }
        .task { app.refreshSchedule() }
    }

    private func tone(_ t: ScheduleToggle.Tone) -> Color {
        switch t {
        case .normal: .primary
        case .warning: .orange
        case .problem: .red
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
                if run.state == .running, let progress = run.progress {
                    Row(label: "Now", value: progress.nowText, verbatim: true)
                }
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
                    Row(label: "Status changes", value: changes.map { "\(Labels.label(for: $0.status)) \($0.delta > 0 ? "+" : "")\($0.delta)" }.joined(separator: ", "), secondary: true)
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
                if let err = run.impressionsError, !err.isEmpty {
                    Row(label: "Impressions", value: PipelineStatus.bounded(err, max: 500), verbatim: true)
                }
                if run.logDropped > 0 {
                    Row(label: "Run log", value: "\(run.logDropped) line(s) dropped after the 1 MiB cap")
                }
                if let shadow = run.shadow, shadow.isFailure {
                    Row(label: "Shadow", value: shadow.summary, verbatim: true)
                }
                if let exclusions = run.exclusions {
                    Row(label: "Exclusions", value: exclusions.summary, secondary: !exclusions.isFailure, verbatim: exclusions.isFailure)
                }
                if let logos = run.logos {
                    Row(label: "Logos", value: logos.summary, secondary: !logos.isFailure, verbatim: logos.isFailure)
                }
                if let near = run.nearDup {
                    Row(label: "Near-duplicates", value: near.summary, secondary: !near.isFailure,
                        verbatim: near.isFailure || !(near.similarityNote ?? "").isEmpty)
                }
                if let pre = run.semanticPre {
                    Row(label: "Semantic, before advance", value: pre.summary, secondary: !pre.isFailure, verbatim: pre.isFailure)
                }
                if let sem = run.semantic {
                    Row(label: "Semantic", value: sem.summary, secondary: !sem.isFailure, verbatim: sem.isFailure)
                }
                if let warn = run.digestWarning, !warn.isEmpty {
                    Row(label: "Digest", value: PipelineStatus.bounded(warn, max: 500), verbatim: true)
                }
                if let notes = run.explain {
                    Row(label: "Notes", value: notes.summary, secondary: !notes.isFailure, verbatim: true)
                }
            }
            if let skip = status.lastSkip, !skip.ok {
                Row(label: "Skipped slot", value: "Could not be read: \(skip.reason ?? "unreadable")", verbatim: true)
            } else if let skip = status.skippedSlot {
                Row(label: "Skipped slot", value: skip.detail ?? "A daily slot was skipped", verbatim: true)
            }
            if let inbox = status.inbox {
                Row(label: "Inbox", value: PipelineStatus.bounded(inbox.summary, max: 500), secondary: !inbox.needsAttention, verbatim: true)
            }
            RunToolsRow(status: status)
            if status.schedule.isUnkept {
                // Nothing fires the schedule, so no "next run" is promised.
                let at = status.schedule.dailyAt.map { "daily at \($0) \(status.schedule.timezone ?? "")" } ?? "daily"
                Row(label: "Schedule", value: "Not kept: no daemon is running, so the run set \(at) does not happen on its own")
                if let why = status.schedule.unkept?.why, !why.isEmpty {
                    Row(label: "Daemon", value: why, secondary: true)
                }
                Row(label: "To keep it", value: PipelineStatus.Schedule.Unkept.appHint, secondary: true)
            } else if let next = status.schedule.nextRun {
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

/// Copy failures and the run's files. The failure text is vani's (`vani runs
/// failures`): this row asks for it and copies it, and says so when it cannot.
/// The button is there only when the last run has something to report; the
/// Logs menu lists only the files that exist when the row is drawn.
private struct RunToolsRow: View {
    @Environment(PipelineModel.self) private var pipeline
    let status: PipelineStatus

    var body: some View {
        let files = RunFiles.existing(stateDir: status.stateDir)
        let hasFailures = status.lastRun.hasFailures
        if hasFailures || !files.isEmpty {
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 8) {
                    if hasFailures { copyButton }
                    if !files.isEmpty { logsMenu(files) }
                    Spacer(minLength: 0)
                }
                if case let .failed(message) = pipeline.failureCopy {
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        Text(message).foregroundStyle(.red).textSelection(.enabled)
                        Button("Dismiss") { pipeline.resetFailureCopy() }.buttonStyle(.link)
                    }
                    .font(.callout)
                }
            }
            .padding(.top, 4)
        }
    }

    private var copyButton: some View {
        Button {
            Task {
                await pipeline.copyFailures { text in
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(text, forType: .string)
                }
                if pipeline.failureCopy == .copied {
                    try? await Task.sleep(for: .milliseconds(1500))
                    pipeline.resetFailureCopy()
                }
            }
        } label: {
            switch pipeline.failureCopy {
            case .copying: Label("Copying…", systemImage: "doc.on.doc")
            case .copied: Label("Copied", systemImage: "checkmark")
            default: Label("Copy failures", systemImage: "doc.on.doc")
            }
        }
        .disabled(pipeline.failureCopy == .copying)
        .help("Copy the last run's failed boards, backend errors, stage gaps, error and log paths as plain text")
    }

    private func logsMenu(_ files: RunFiles) -> some View {
        Menu("Logs") {
            if let log = files.manualLog {
                Button("Open Log") { NSWorkspace.shared.open(log) }
                Button("Show Log in Finder") { NSWorkspace.shared.activateFileViewerSelecting([log]) }
            }
            if let record = files.record {
                Button("Open Run Record") { NSWorkspace.shared.open(record) }
                Button("Show Run Record in Finder") { NSWorkspace.shared.activateFileViewerSelecting([record]) }
            }
            if let history = files.historyFolder {
                Button("Show Run History in Finder") { NSWorkspace.shared.open(history) }
            }
        }
        .fixedSize()
        .help("Open or reveal the last manual run's log, the run record, and the kept history of earlier runs")
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
        if let fetch = boards.fetchSummary {
            Row(label: "Board fetches", value: fetchLine(fetch), secondary: boards.payFailed == 0, verbatim: true)
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
            .contentShape(Rectangle())
            .contextMenu {
                // The same line vani's failure report prints for this board.
                Button("Copy error") {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(line.copyText, forType: .string)
                }
            }
        }
        if let more = boards.moreNote() {
            Row(label: "", value: more, secondary: true)
        }
    }

    /// The fetch counts, plus the pay error when a pay fetch failed. Built
    /// outside `body` because a ViewBuilder cannot assign into a local.
    private func fetchLine(_ fetch: String) -> String {
        guard boards.payFailed > 0, let err = boards.payError, !err.isEmpty else { return fetch }
        return fetch + ": " + PipelineStatus.bounded(err, max: 200)
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
        if let line = advance.failOpen.summary {
            Row(label: "", value: line, secondary: !advance.failOpen.isFailure, verbatim: true)
        }
        if let line = advance.notes.summary {
            Row(label: "", value: line, secondary: !advance.notes.isFailure, verbatim: true)
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
                    Row(label: Labels.label(for: s), value: "\(n)", secondary: true)
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
