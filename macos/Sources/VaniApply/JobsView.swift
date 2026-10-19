import AppKit
import SwiftUI
import VaniApplyKit

/// Browse the roles Vani retrieved, and keep the ones that matter: Save,
/// mark Applied, Hide, and a private note. Posting text is board content, so
/// it is shown verbatim (never as Markdown) and only http(s) links open.
struct JobsView: View {
    @Environment(JobsModel.self) private var jobs

    var body: some View {
        @Bindable var jobs = jobs
        VStack(spacing: 0) {
            Picker("View", selection: $jobs.view) {
                ForEach(JobView.allCases) { v in
                    Text(label(v)).tag(v)
                }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            Divider()
            HSplitView {
                JobList()
                    .frame(minWidth: 300, idealWidth: 380, maxWidth: 560)
                JobDetail()
                    .frame(minWidth: 380, maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .searchable(text: $jobs.query, placement: .toolbar, prompt: "Title, company, location or note")
        .task(id: jobs.query) {
            // Debounce typing; the first run is the initial load.
            if !jobs.query.isEmpty { try? await Task.sleep(for: .milliseconds(250)) }
            guard !Task.isCancelled else { return }
            await jobs.refresh()
        }
        .toolbar {
            ToolbarItem {
                Button { jobs.reload() } label: { Label("Refresh", systemImage: "arrow.clockwise") }
                    .keyboardShortcut("r")
                    .help("Reload the list (⌘R)")
            }
        }
        .navigationTitle("Jobs")
        .alert("Jobs", isPresented: Binding(get: { jobs.actionError != nil }, set: { if !$0 { jobs.actionError = nil } })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(verbatim: jobs.actionError ?? "")
        }
    }

    private func label(_ v: JobView) -> String {
        guard let page = jobs.page else { return v.title }
        return "\(v.title)  \(page.count(v))"
    }
}

// MARK: - List

private struct JobList: View {
    @Environment(JobsModel.self) private var jobs

    var body: some View {
        @Bindable var jobs = jobs
        VStack(spacing: 0) {
            if let err = jobs.error, jobs.page == nil {
                ContentUnavailableView {
                    Label("Couldn't load jobs", systemImage: "exclamationmark.triangle")
                } description: {
                    Text(verbatim: err)
                    Text("Check the Vani config and state folder in Settings.")
                } actions: {
                    Button("Try Again") { jobs.reload() }
                }
            } else if jobs.page == nil {
                ProgressView("Loading jobs…").frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if jobs.page?.records == 0 {
                ContentUnavailableView {
                    Label("No postings in this Vani state folder", systemImage: "folder.badge.questionmark")
                } description: {
                    Text(verbatim: "The ledger at \(jobs.page?.ledger ?? "the default location") is empty.")
                    Text("If Vani runs with another state folder (VANI_STATE_DIR), choose it under Settings › Vani, or run `vani run-once` to retrieve roles here.")
                }
            } else if jobs.jobs.isEmpty {
                ContentUnavailableView(emptyTitle, systemImage: emptySymbol, description: Text(emptyText))
            } else {
                ScrollViewReader { proxy in
                    List(jobs.jobs, selection: $jobs.selection) { job in
                        JobRow(job: job)
                            .tag(job.key)
                            .contextMenu { JobMenu(job: job) }
                    }
                    .listStyle(.inset)
                    // A reload rebuilds the list; keep the selected role in view.
                    .onChange(of: jobs.page) {
                        if let s = jobs.selection { proxy.scrollTo(s) }
                    }
                }
            }
            if let page = jobs.page {
                footer(page)
            }
        }
    }

    @ViewBuilder
    private func footer(_ page: JobsPage) -> some View {
        Divider()
        HStack(spacing: 6) {
            if jobs.loading { ProgressView().controlSize(.small) }
            if page.shown < page.total {
                Text("Showing \(page.shown) of \(page.total). Search to narrow the list.")
            } else {
                Text("\(page.total) \(page.total == 1 ? "role" : "roles")")
            }
            if page.torn == true {
                Text("· a run is writing; refresh soon").foregroundStyle(.orange)
            }
            if let err = jobs.error {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.yellow).help(err)
            }
            Spacer()
            if let ledger = page.ledger {
                Image(systemName: "externaldrive").help("Reading \(ledger) (\(page.records ?? 0) postings)")
            }
        }
        .font(.caption)
        .foregroundStyle(.secondary)
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
    }

    private var emptyTitle: String {
        if !jobs.query.isEmpty { return "No matches" }
        switch jobs.view {
        case .new: return "Nothing new"
        case .saved: return "No saved roles"
        case .applied: return "No applications yet"
        case .hidden: return "Nothing hidden"
        case .all: return "No roles yet"
        }
    }

    private var emptySymbol: String {
        jobs.query.isEmpty ? JobBadge.symbol(for: jobs.view) : "magnifyingglass"
    }

    private var emptyText: String {
        if !jobs.query.isEmpty { return "No role in this view matches “\(jobs.query)”." }
        switch jobs.view {
        case .new: return "Every role Vani kept has been saved, applied to or hidden. New ones arrive with the daily run."
        case .saved: return "Save a role (⌘S) to keep it here."
        case .applied: return "Mark a role Applied (⇧⌘A) to track it here with the date."
        case .hidden: return "Hidden roles (⌘⌫) leave New and wait here."
        case .all: return "Run `vani run-once` to retrieve roles."
        }
    }
}

private struct JobRow: View {
    let job: Job

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            JobBadge(mark: job.mark)
                .frame(width: 16)
            VStack(alignment: .leading, spacing: 3) {
                Text(verbatim: job.title)
                    .font(.headline)
                    .lineLimit(2)
                Text(verbatim: subtitle)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                HStack(spacing: 4) {
                    if let fit = job.fitLabel { Chip(text: fit, tint: fitTint(job)) }
                    if let rank = job.rank { Chip(text: "Rank \(Int(rank.rounded()))", tint: .accentColor) }
                    if job.closed == true { Chip(text: "Closed", tint: .red) }
                    if let first = job.reasons.first, first.kind != .other {
                        Text(verbatim: "\(first.kind.rawValue): \(first.text)")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                }
            }
        }
        .padding(.vertical, 3)
    }

    private var subtitle: String {
        var parts = [job.company]
        if let loc = job.location, !loc.isEmpty { parts.append(loc) }
        if job.remote == true, !(job.location ?? "").localizedCaseInsensitiveContains("remote") { parts.append("Remote") }
        return parts.joined(separator: " · ")
    }
}

/// The mark actions, shared by the row's context menu and the detail pane.
private struct JobMenu: View {
    @Environment(JobsModel.self) private var jobs
    let job: Job

    var body: some View {
        Button(job.mark == .saved ? "Unsave" : "Save", systemImage: "bookmark") { act(.saved) }
        Button(job.mark == .applied ? "Unmark Applied" : "Mark Applied", systemImage: "checkmark.seal") { act(.applied) }
        Button(job.mark == .hidden ? "Unhide" : "Hide", systemImage: "eye.slash") { act(.hidden) }
        Divider()
        if let link = job.postingLink {
            Button("Open Posting", systemImage: "safari") { NSWorkspace.shared.open(link) }
            Button("Copy Link", systemImage: "link") {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(link.absoluteString, forType: .string)
            }
        }
    }

    private func act(_ mark: JobMark) {
        Task { await jobs.toggle(job, mark) }
    }
}

// MARK: - Detail

private struct JobDetail: View {
    @Environment(JobsModel.self) private var jobs

    var body: some View {
        if let job = jobs.selected {
            JobDetailContent(job: job)
                .id(job.key)
        } else {
            ContentUnavailableView("Select a role", systemImage: "briefcase",
                                   description: Text("Save it, mark it applied, hide it, or keep a private note."))
        }
    }
}

private struct JobDetailContent: View {
    @Environment(JobsModel.self) private var jobs
    let job: Job
    @State private var noteDraft = ""
    @State private var savingNote = false
    @State private var showDescription = false
    // While the note has focus, ⌘⌫ and ⌘S belong to the note, not to Hide and Save.
    @FocusState private var noteFocused: Bool

    private var noteBytes: Int { noteDraft.trimmingCharacters(in: .whitespacesAndNewlines).utf8.count }
    private var noteChanged: Bool { noteDraft.trimmingCharacters(in: .whitespacesAndNewlines) != (job.note ?? "") }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                header
                actions
                if !job.reasons.isEmpty { reasons }
                facts
                note
                description
            }
            .padding(20)
            .frame(maxWidth: 760, alignment: .leading)
        }
        .onAppear { noteDraft = job.note ?? "" }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(verbatim: job.title)
                .font(.title2.bold())
                .textSelection(.enabled)
            Text(verbatim: [job.company, job.location ?? "", job.department ?? ""].filter { !$0.isEmpty }.joined(separator: " · "))
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
            FlowChips(job: job)
        }
    }

    /// Full labels when they fit, icons (with help tags) when the pane is narrow.
    private var actions: some View {
        ViewThatFits(in: .horizontal) {
            actionRow.labelStyle(.titleAndIcon)
            actionRow.labelStyle(.iconOnly)
        }
    }

    private var actionRow: some View {
        HStack(spacing: 8) {
            markButton(.saved, on: "Saved", off: "Save", symbol: "bookmark")
                .keyboardShortcut(noteFocused ? nil : KeyboardShortcut("s"))
                .help("Save to come back to it (⌘S)")
            markButton(.applied, on: "Applied", off: "Mark Applied", symbol: "checkmark.seal")
                .keyboardShortcut(noteFocused ? nil : KeyboardShortcut("a", modifiers: [.command, .shift]))
                .help("Record that you applied, with the date (⇧⌘A)")
            markButton(.hidden, on: "Hidden", off: "Hide", symbol: "eye.slash")
                .keyboardShortcut(noteFocused ? nil : KeyboardShortcut(.delete))
                .help("Not interested: move it out of New (⌘⌫)")
            Spacer(minLength: 8)
            if let link = job.postingLink {
                Button("Open Posting", systemImage: "safari") { NSWorkspace.shared.open(link) }
                    .keyboardShortcut("o")
                    .help("Open the posting in your browser (⌘O)")
            }
            if let apply = job.applyLink, apply != job.postingLink {
                Button("Apply", systemImage: "paperplane") { NSWorkspace.shared.open(apply) }
                    .help("Open the application form; Vani Apply fills it from your answers")
            }
        }
        .controlSize(.large)
    }

    private func markButton(_ mark: JobMark, on: String, off: String, symbol: String) -> some View {
        let active = job.mark == mark
        return Button {
            Task { await jobs.toggle(job, mark) }
        } label: {
            Label(active ? on : off, systemImage: active ? symbol + ".fill" : symbol)
        }
        .buttonStyle(.bordered)
        .tint(active ? JobBadge.tint(for: mark) : nil)
    }

    private var reasons: some View {
        GroupBox {
            VStack(alignment: .leading, spacing: 6) {
                ForEach(Array(job.reasons.enumerated()), id: \.offset) { _, r in
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        if r.kind != .other {
                            Text(r.kind.rawValue)
                                .font(.caption.bold())
                                .foregroundStyle(reasonTint(r.kind))
                                .frame(width: 44, alignment: .leading)
                        }
                        Text(verbatim: r.text).textSelection(.enabled)
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        } label: {
            Text("Why this role")
        }
    }

    private var facts: some View {
        Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 12, verticalSpacing: 6) {
            if let missing = job.missing, !missing.isEmpty { row("Missing", missing) }
            if let unsure = job.unsure, !unsure.isEmpty { row("Unsure", unsure) }
            if let cov = job.coverage { row("Coverage", "\(Int(cov.rounded()))% of must-haves") }
            if let lca = job.lca, !lca.isEmpty { row("Sponsorship history", lca) }
            if let emp = job.employer, !emp.isEmpty, emp != "no employer-list match" { row("Employer", emp) }
            if let first = job.firstSeen { row("First seen", first.formatted(date: .abbreviated, time: .shortened)) }
            if let posted = job.postedAt { row("Posted", posted.formatted(date: .abbreviated, time: .omitted)) }
            if let mark = job.mark, let at = job.markedAt {
                row(mark.rawValue.capitalized, at.formatted(date: .abbreviated, time: .shortened))
            }
        }
        .font(.callout)
    }

    private func row(_ label: String, _ value: String) -> some View {
        GridRow {
            Text(label).foregroundStyle(.secondary).gridColumnAlignment(.trailing)
            Text(verbatim: value).textSelection(.enabled)
        }
    }

    private var note: some View {
        GroupBox {
            VStack(alignment: .leading, spacing: 6) {
                TextEditor(text: $noteDraft)
                    .focused($noteFocused)
                    .font(.body)
                    .frame(minHeight: 70, maxHeight: 160)
                    .scrollContentBackground(.hidden)
                HStack {
                    if let at = job.noteAt, !noteChanged {
                        Text("Saved \(at.formatted(date: .abbreviated, time: .shortened)). Kept on this Mac; never sent to a model.")
                    } else {
                        Text("Kept on this Mac in Vani's state folder. Never sent to a model.")
                    }
                    Spacer()
                    Text("\(noteBytes) / \(JobsClient.maxNoteBytes)")
                        .monospacedDigit()
                        .foregroundStyle(noteBytes > JobsClient.maxNoteBytes ? .red : .secondary)
                    Button("Save Note") {
                        savingNote = true
                        Task {
                            await jobs.saveNote(job.key, noteDraft)
                            savingNote = false
                        }
                    }
                    .disabled(!noteChanged || noteBytes > JobsClient.maxNoteBytes || savingNote)
                    .keyboardShortcut(noteFocused ? KeyboardShortcut("s") : nil)
                    .help("Save the note (⌘S while typing)")
                }
                .font(.caption)
                .foregroundStyle(.secondary)
            }
        } label: {
            Text("Private note")
        }
    }

    @ViewBuilder
    private var description: some View {
        DisclosureGroup("Job description", isExpanded: $showDescription) {
            if let text = jobs.detail?.key == job.key ? jobs.detail?.description : nil {
                Text(verbatim: text.isEmpty ? "The board gave no description." : text)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.top, 6)
            } else if let err = jobs.detailError {
                Text(verbatim: err).foregroundStyle(.red)
            } else {
                ProgressView().controlSize(.small)
            }
        }
    }

    private func reasonTint(_ k: Job.Reason.Kind) -> Color {
        switch k {
        case .fits: .green
        case .gap: .orange
        case .check: .blue
        case .other: .secondary
        }
    }
}

private struct FlowChips: View {
    let job: Job

    var body: some View {
        HStack(spacing: 4) {
            if let fit = job.fitLabel { Chip(text: fit, tint: fitTint(job)) }
            ForEach(job.triageLabels, id: \.self) { Chip(text: $0, tint: .secondary) }
            // The board's remote flag only when triage read no work mode, so the
            // two never contradict each other side by side.
            if job.remote == true, job.triage?["work_mode"] == nil { Chip(text: "Remote", tint: .secondary) }
            if let rank = job.rank { Chip(text: "Rank \(Int(rank.rounded()))", tint: .accentColor) }
            if job.closed == true { Chip(text: "Closed: no longer listed", tint: .red) }
        }
    }
}

// MARK: - Pieces

private struct Chip: View {
    let text: String
    let tint: Color

    var body: some View {
        Text(verbatim: text)
            .font(.caption)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(tint.opacity(0.14), in: Capsule())
            .foregroundStyle(tint == .secondary ? Color.secondary : tint)
    }
}

private struct JobBadge: View {
    let mark: JobMark?

    var body: some View {
        if let mark {
            Image(systemName: Self.symbol(for: mark) + ".fill")
                .foregroundStyle(Self.tint(for: mark))
                .help(mark.rawValue.capitalized)
        } else {
            Color.clear.frame(width: 1, height: 1)
        }
    }

    static func symbol(for mark: JobMark) -> String {
        switch mark {
        case .saved: "bookmark"
        case .applied: "checkmark.seal"
        case .hidden: "eye.slash"
        }
    }

    static func symbol(for view: JobView) -> String {
        switch view {
        case .new: "tray"
        case .saved: "bookmark"
        case .applied: "checkmark.seal"
        case .hidden: "eye.slash"
        case .all: "briefcase"
        }
    }

    static func tint(for mark: JobMark) -> Color {
        switch mark {
        case .saved: .yellow
        case .applied: .green
        case .hidden: .gray
        }
    }
}

private func fitTint(_ job: Job) -> Color {
    switch job.triage?["role_fit"] {
    case "strong_fit": .green
    case "partial_fit": .teal
    case "adjacent": .blue
    default: .secondary
    }
}
