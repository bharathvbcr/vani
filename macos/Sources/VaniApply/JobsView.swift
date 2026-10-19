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
            if jobs.showInsights {
                InsightsPanel()
                Divider()
            }
            HSplitView {
                JobList()
                    .frame(minWidth: 300, idealWidth: 380, maxWidth: 560)
                JobDetail()
                    .frame(minWidth: 380, maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
            jobs.reloadAfterActivation()
        }
        .searchable(text: $jobs.query, placement: .toolbar, prompt: "Title, company, location or note")
        .task(id: jobs.query) {
            // Debounce typing; the first run is the initial load.
            if !jobs.query.isEmpty { try? await Task.sleep(for: .milliseconds(250)) }
            guard !Task.isCancelled else { return }
            await jobs.refresh()
        }
        .toolbar {
            ToolbarItemGroup {
                Menu {
                    Picker("Sort", selection: $jobs.sort) {
                        ForEach(JobSort.allCases) { Text($0.title).tag($0) }
                    }
                    .pickerStyle(.inline)
                } label: {
                    Label("Sort", systemImage: "arrow.up.arrow.down")
                }
                .help("Sort: \(jobs.sort.title)")
                FilterMenu()
                Toggle(isOn: $jobs.showInsights) {
                    Label("Insights", systemImage: "chart.dots.scatter")
                }
                .keyboardShortcut("i")
                .help("Charts over the roles in this list (⌘I)")
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

/// Filters on triage readings, age and closed roles. Each is a `vani jobs`
/// flag; the view counts above ignore them, the list and Insights do not.
private struct FilterMenu: View {
    @Environment(JobsModel.self) private var jobs

    var body: some View {
        Menu {
            Section("Role fit") {
                ForEach(Job.roleFitOrder + ["none"], id: \.self) { toggle(\.fits, $0) }
            }
            Section("Work mode") {
                ForEach(Job.workModeOptions + ["none"], id: \.self) { toggle(\.workModes, $0) }
            }
            Section("Sponsorship") {
                ForEach(Job.sponsorshipOptions + ["none"], id: \.self) { toggle(\.sponsorships, $0) }
            }
            Section("Location (as the board lists it)") {
                Toggle("Listed as remote", isOn: Binding(get: { jobs.filter.remoteOnly }, set: { jobs.filter.setRemoteOnly($0) }))
                // Widens the chosen states and areas: remote, or in these places.
                Toggle("Or remote anywhere", isOn: Binding(get: { jobs.filter.orRemote && !jobs.filter.remoteOnly }, set: { jobs.filter.setOrRemote($0) }))
                .disabled(jobs.filter.states.isEmpty && jobs.filter.areas.isEmpty)
                Menu("State") {
                    ForEach(stateOptions, id: \.self) { code in
                        placeToggle(\.states, code, label: code == "none" ? "No state listed" : code)
                    }
                }
                Menu("Area") {
                    ForEach(areaOptions, id: \.self) { area in
                        placeToggle(\.areas, area, label: area)
                    }
                }
                .disabled(areaOptions.isEmpty)
            }
            Section("Pay (as the posting states it)") {
                Menu(jobs.filter.minSalary.map { "At least \(Job.Pay.short(Double($0))) a year" } ?? "Any salary") {
                    ForEach([nil, 50_000, 75_000, 100_000, 125_000, 150_000, 175_000, 200_000, 250_000, 300_000] as [Int?], id: \.self) { s in
                        Button {
                            jobs.filter.minSalary = s
                        } label: {
                            if jobs.filter.minSalary == s { Image(systemName: "checkmark") }
                            Text(s.map { "At least \(Job.Pay.short(Double($0))) a year" } ?? "Any salary")
                        }
                    }
                }
                .help("By the top of the stated range. Roles that state no pay stay; roles that state pay only in another currency are left out.")
                Toggle("Only roles that state pay", isOn: Binding(get: { jobs.filter.salaryListed }, set: { jobs.filter.salaryListed = $0 }))
                Menu("Currency: \(jobs.filter.currency)") {
                    ForEach(Job.payCurrencies, id: \.self) { c in
                        Button {
                            jobs.filter.currency = c
                        } label: {
                            if jobs.filter.currency == c { Image(systemName: "checkmark") }
                            Text(c)
                        }
                    }
                }
                .help("The currency of the salary filter, the salary sort and the pay chart")
            }
            Section("Seniority") {
                ForEach(Job.seniorityOptions + ["none"], id: \.self) { toggle(\.seniorities, $0) }
            }
            Section("Relevance") {
                ForEach([nil, 25, 50, 75] as [Int?], id: \.self) { r in
                    Button {
                        jobs.filter.minRelevance = r
                    } label: {
                        if jobs.filter.minRelevance == r { Image(systemName: "checkmark") }
                        Text(r.map { "At least \($0)" } ?? "Any relevance")
                    }
                }
            }
            Section("Age") {
                ForEach([nil, 3, 7, 14, 30, 60] as [Int?], id: \.self) { d in
                    Button {
                        jobs.filter.maxAgeDays = d
                    } label: {
                        if jobs.filter.maxAgeDays == d { Image(systemName: "checkmark") }
                        Text(d.map { "Posted within \($0) days" } ?? "Any age")
                    }
                }
            }
            Divider()
            Toggle("Hide closed roles", isOn: Binding(get: { jobs.filter.openOnly }, set: { jobs.filter.openOnly = $0 }))
            Button("Clear Filters") { jobs.clearFilter() }
                .disabled(jobs.filter.isEmpty)
        } label: {
            Label(jobs.filter.isEmpty ? "Filter" : "Filter (\(jobs.filter.activeCount))",
                  systemImage: jobs.filter.isEmpty ? "line.3.horizontal.decrease.circle" : "line.3.horizontal.decrease.circle.fill")
        }
        .help(jobs.filter.isEmpty ? "Filter by fit, work mode, sponsorship, location, pay, seniority, relevance or age" : "\(jobs.filter.activeCount) filters on")
    }

    /// The states the current selection names, most first, plus any already
    /// chosen (so a chosen state never vanishes from the menu), then "none".
    private var stateOptions: [String] {
        var codes = (jobs.stats?.statesByCount ?? []).prefix(15).map(\.code)
        for c in jobs.filter.states.sorted() where c != "none" && !codes.contains(c) { codes.append(c) }
        return codes + ["none"]
    }

    /// The most-named areas in the current selection, plus any already chosen.
    private var areaOptions: [String] {
        var out: [String] = []
        for a in jobs.stats?.area ?? [] where !out.contains(a.area) && !a.area.contains(",") { out.append(a.area) }
        for a in jobs.filter.areas.sorted() where !out.contains(a) { out.append(a) }
        return out
    }

    private func placeToggle(_ path: WritableKeyPath<JobFilter, Set<String>>, _ value: String, label: String) -> some View {
        Toggle(label, isOn: Binding(
            get: { jobs.filter[keyPath: path].contains(value) },
            set: { on in
                if on { jobs.filter[keyPath: path].insert(value) } else { jobs.filter[keyPath: path].remove(value) }
            }
        ))
    }

    private func toggle(_ path: WritableKeyPath<JobFilter, Set<String>>, _ code: String) -> some View {
        Toggle(Job.label(for: code), isOn: Binding(
            get: { jobs.filter[keyPath: path].contains(code) },
            set: { on in
                if on { jobs.filter[keyPath: path].insert(code) } else { jobs.filter[keyPath: path].remove(code) }
            }
        ))
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
        if !jobs.query.isEmpty || !jobs.filter.isEmpty { return "No matches" }
        switch jobs.view {
        case .new: return "Nothing new"
        case .saved: return "No saved roles"
        case .applied: return "No applications yet"
        case .hidden: return "Nothing hidden"
        case .all: return "No roles yet"
        }
    }

    private var emptySymbol: String {
        jobs.query.isEmpty && jobs.filter.isEmpty ? JobBadge.symbol(for: jobs.view) : "magnifyingglass"
    }

    private var emptyText: String {
        if !jobs.filter.isEmpty { return "No role in this view passes the filters. Clear them from the Filter menu." }
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
    @Environment(JobsModel.self) private var jobs
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
                    if let pay = job.payLabel(preferring: jobs.filter.currency) { Chip(text: pay, tint: .teal) }
                    if job.closed == true { Chip(text: "Closed", tint: .red) }
                    ForEach(targetKeywords, id: \.term) { Chip(text: $0.term, tint: .green) }
                    if let first = job.reasons.first, first.kind != .other {
                        Text(verbatim: "\(first.kind.label): \(first.text)")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                }
            }
            Spacer(minLength: 4)
            VStack(alignment: .trailing, spacing: 3) {
                if let age = job.ageLabel {
                    Text(verbatim: age)
                        .font(.caption.monospacedDigit())
                        .foregroundStyle(freshTint)
                        .help(ageHelp)
                }
                if let p = job.priority {
                    ScoreBar(value: p)
                        .frame(width: 34, height: 4)
                        .help("Priority \(Int(p.rounded())) of 100: relevance discounted by age")
                }
            }
        }
        .padding(.vertical, 3)
        .accessibilityElement(children: .combine)
        .accessibilityLabel(accessibilityText)
    }

    /// Up to three keywords your target summary names.
    private var targetKeywords: [Job.Keyword] {
        Array(job.keywordList.filter { $0.target == true }.prefix(3))
    }

    private var freshTint: Color {
        guard let d = job.ageDays else { return .secondary }
        switch d {
        case ..<4: return .green
        case ..<15: return .primary
        default: return .secondary
        }
    }

    private var ageHelp: String {
        guard let at = job.freshAt else { return "" }
        let when = at.formatted(date: .abbreviated, time: .omitted)
        return job.freshSource == .seen
            ? "First seen \(when); the board gives no posted date, so it may be older"
            : "Posted \(when)"
    }

    private var accessibilityText: String {
        var parts = [job.title, subtitle]
        if let fit = job.fitLabel { parts.append(fit) }
        if let pay = job.payLabel(preferring: jobs.filter.currency) { parts.append("pay " + pay) }
        if let age = job.ageDays { parts.append(job.freshSource == .seen ? "first seen \(age) days ago" : "posted \(age) days ago") }
        if let p = job.priority { parts.append("priority \(Int(p.rounded()))") }
        if let mark = job.mark { parts.append(mark.rawValue) }
        if job.closed == true { parts.append("closed") }
        return parts.joined(separator: ", ")
    }

    private var subtitle: String {
        var parts = [job.company]
        if let w = job.whereLabel { parts.append(w) }
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
                scores
                if !job.reasons.isEmpty { reasons }
                if !job.keywordList.isEmpty { keywords }
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
            Text(verbatim: [job.company, job.whereLabel ?? "", job.department ?? ""].filter { !$0.isEmpty }.joined(separator: " · "))
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
            ForEach(job.pay ?? [], id: \.self) { p in
                Label {
                    Text(verbatim: p.label)
                } icon: {
                    Image(systemName: "banknote")
                }
                .font(.callout)
                .help(p.text.map { "Read from the posting: “\($0)”" } ?? "")
            }
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

    /// Relevance, priority and freshness, with what each rests on.
    @ViewBuilder
    private var scores: some View {
        if job.relevance != nil || job.ageDays != nil {
            HStack(alignment: .top, spacing: 20) {
                if let r = job.relevance {
                    score("Relevance", r, detail: basisText)
                }
                if let p = job.priority {
                    score("Priority", p, detail: "relevance, at most halved by age")
                }
                if let age = job.ageDays {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Age").font(.caption).foregroundStyle(.secondary)
                        Text(verbatim: age == 0 ? "today" : "\(age) \(age == 1 ? "day" : "days")").font(.title3.monospacedDigit())
                        Text(job.freshSource == .seen ? "since first seen (no board date)" : "since the board posted it")
                            .font(.caption2).foregroundStyle(.secondary)
                    }
                }
            }
            .accessibilityElement(children: .combine)
        }
    }

    private var basisText: String {
        switch job.relevanceBasis {
        case "rank": "from DevPrism rank and target keywords"
        case "fit": "from triage fit and target keywords"
        default: "no rank or fit reading: keywords only"
        }
    }

    private func score(_ title: String, _ value: Double, detail: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.caption).foregroundStyle(.secondary)
            HStack(spacing: 6) {
                Text("\(Int(value.rounded()))").font(.title3.monospacedDigit())
                ScoreBar(value: value).frame(width: 60, height: 5)
            }
            Text(detail).font(.caption2).foregroundStyle(.secondary)
        }
        .help("\(title) \(Int(value.rounded())) of 100, \(detail). Display only: it sorts and plots, never drops a role.")
    }

    private var reasons: some View {
        GroupBox {
            VStack(alignment: .leading, spacing: 6) {
                ForEach(Array(job.reasons.enumerated()), id: \.offset) { _, r in
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        if r.kind != .other {
                            Text(r.kind.label)
                                .font(.caption.bold())
                                .foregroundStyle(reasonTint(r.kind))
                                .frame(minWidth: 44, alignment: .leading)
                        }
                        Text(verbatim: r.text).textSelection(.enabled)
                        if let pct = r.pct {
                            Spacer(minLength: 4)
                            Text("\(pct)%")
                                .font(.caption.monospacedDigit())
                                .foregroundStyle(.secondary)
                                .help("The decision model's probability for this pick")
                        }
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        } label: {
            Text("Why this role")
        }
    }

    private var keywords: some View {
        GroupBox {
            VStack(alignment: .leading, spacing: 6) {
                WrapLayout(spacing: 4) {
                    ForEach(job.keywordList, id: \.term) { k in
                        Chip(text: k.n > 1 ? "\(k.term) ×\(k.n)" : k.term, tint: keywordTint(k))
                            .help(keywordHelp(k))
                    }
                }
                HStack(spacing: 12) {
                    legend(.green, "in your target summary")
                    legend(.orange, "DevPrism: missing or weak")
                    legend(.secondary, "mentioned")
                }
                .font(.caption2)
                .foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        } label: {
            Text("Keywords")
        }
    }

    private func legend(_ tint: Color, _ text: String) -> some View {
        HStack(spacing: 3) {
            Circle().fill(tint).frame(width: 6, height: 6)
            Text(text)
        }
    }

    private func keywordTint(_ k: Job.Keyword) -> Color {
        if k.gap == true { return .orange }
        if k.target == true { return .green }
        return .secondary
    }

    private func keywordHelp(_ k: Job.Keyword) -> String {
        var parts = ["\(k.cat): mentioned \(k.n) \(k.n == 1 ? "time" : "times")"]
        if k.target == true { parts.append("your target summary names it") }
        if k.gap == true { parts.append("DevPrism found it missing or weak in your knowledge base") }
        return parts.joined(separator: "; ")
    }

    private var facts: some View {
        Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 12, verticalSpacing: 6) {
            if let missing = job.missing, !missing.isEmpty { row("Missing", missing) }
            if let unsure = job.unsure, !unsure.isEmpty { row("Unsure", unsure) }
            if let cov = job.coverage { row("Coverage", "\(Int(cov.rounded()))% of must-haves") }
            if let lca = job.lca, !lca.isEmpty { row("Sponsorship history", lca) }
            if let emp = job.employer, !emp.isEmpty, emp != "no employer-list match" { row("Employer", emp) }
            if let posted = job.postedAt { row("Posted", posted.formatted(date: .abbreviated, time: .omitted)) }
            if let first = job.firstSeen { row("First seen", first.formatted(date: .abbreviated, time: .shortened)) }
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
            // The board's remote listing only when triage read no work mode, so
            // the two never contradict each other side by side.
            if let r = job.remoteLabel, job.triage?["work_mode"] == nil { Chip(text: r, tint: .secondary) }
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
    fitColor(code: job.triage?["role_fit"] ?? "none")
}

/// A 0-100 score as a thin bar.
private struct ScoreBar: View {
    let value: Double

    var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                Capsule().fill(.quaternary)
                Capsule().fill(tint).frame(width: geo.size.width * min(max(value, 0), 100) / 100)
            }
        }
        .accessibilityHidden(true)
    }

    private var tint: Color {
        switch value {
        case 60...: .green
        case 35...: .accentColor
        default: .secondary
        }
    }
}

/// Lays chips out in rows, wrapping at the proposed width.
private struct WrapLayout: Layout {
    var spacing: CGFloat = 4

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rows = arrange(proposal.width ?? .infinity, subviews)
        let width = rows.map(\.width).max() ?? 0
        let height = rows.map(\.height).reduce(0, +) + spacing * CGFloat(max(rows.count - 1, 0))
        return CGSize(width: width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        for row in arrange(bounds.width, subviews) {
            var x = bounds.minX
            for i in row.items {
                let size = subviews[i].sizeThatFits(.unspecified)
                subviews[i].place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
                x += size.width + spacing
            }
            y += row.height + spacing
        }
    }

    private struct Row {
        var items: [Int] = []
        var width: CGFloat = 0
        var height: CGFloat = 0
    }

    private func arrange(_ maxWidth: CGFloat, _ subviews: Subviews) -> [Row] {
        var rows: [Row] = [Row()]
        for i in subviews.indices {
            let size = subviews[i].sizeThatFits(.unspecified)
            let needed = rows[rows.count - 1].items.isEmpty ? size.width : rows[rows.count - 1].width + spacing + size.width
            if needed > maxWidth, !rows[rows.count - 1].items.isEmpty {
                rows.append(Row())
            }
            var r = rows[rows.count - 1]
            r.width = r.items.isEmpty ? size.width : r.width + spacing + size.width
            r.height = max(r.height, size.height)
            r.items.append(i)
            rows[rows.count - 1] = r
        }
        return rows.filter { !$0.items.isEmpty }
    }
}
