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
            HStack {
                ViewSwitcher()
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            Divider()
            if let err = jobs.impressionsError {
                Text(verbatim: String(err.prefix(500)))
                    .font(.caption)
                    .foregroundStyle(.orange)
                    .lineLimit(3)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 4)
            }
            if jobs.showInsights {
                InsightsPanel()
                Divider()
            }
            SavedSearchesSection()
            ActiveFiltersBar()
            HSplitView {
                JobList()
                    .frame(minWidth: 300, idealWidth: 380, maxWidth: 560)
                JobDetail()
                    .frame(minWidth: 380, maxWidth: .infinity, maxHeight: .infinity)
            }
            .frame(maxHeight: .infinity)
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
                        ForEach(JobSort.options(for: jobs.view)) { Text($0.title).tag($0) }
                    }
                    .pickerStyle(.inline)
                } label: {
                    // The title stays "Sort": the active order is spelled out in
                    // words above the list, and in this tooltip.
                    Label("Sort", systemImage: "arrow.up.arrow.down")
                }
                .help("Sort: \(jobs.sort.title)")
                .labelStyle(.titleAndIcon)
                FilterMenu()
                    .labelStyle(.titleAndIcon)
                Toggle(isOn: $jobs.showInsights) {
                    Label("Insights", systemImage: "chart.dots.scatter")
                }
                .keyboardShortcut("i")
                .labelStyle(.titleAndIcon)
                .help("Charts over the roles in this list (⌘I)")
                Button { jobs.reload() } label: { Label("Refresh", systemImage: "arrow.clockwise") }
                    .keyboardShortcut("r")
                    .labelStyle(.titleAndIcon)
                    .help("Reload the list (⌘R)")
            }
        }
        .navigationTitle("Jobs")
        .alert("Jobs", isPresented: Binding(get: { jobs.actionError != nil }, set: { if !$0 { jobs.actionError = nil } })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(verbatim: jobs.actionError ?? "")
        }
        // The cost of a Sonnet re-triage, asked about before anything is spent. Closing it any
        // other way than Ask Sonnet cancels; a dismissal after Ask Sonnet must not stop the run.
        .sheet(isPresented: Binding(get: { jobs.retriage.confirming != nil },
                                    set: { if !$0 { jobs.retriage.dismissConfirmation() } })) {
            RetriageConfirmSheet()
        }
        .sheet(isPresented: Binding(get: { jobs.batch.confirming != nil },
                                    set: { if !$0 { jobs.batch.dismissConfirmation() } })) {
            RetriageBatchConfirmSheet()
        }
    }
}

/// The boards Vani searches (`vani watchlist list --json`), including pages saved from the
/// browser extension. Closed until opened; a failed read shows its reason, not an empty list.
/// A page board's URL came from a browser page, so only an https one gets an Open button.
private struct SavedSearchesSection: View {
    @Environment(JobsModel.self) private var jobs

    var body: some View {
        @Bindable var jobs = jobs
        VStack(spacing: 0) {
            DisclosureGroup(isExpanded: $jobs.showSearches) {
                content
                    .padding(.top, 4)
            } label: {
                Label(title, systemImage: "magnifyingglass")
                    .font(.callout.weight(.medium))
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            Divider()
        }
    }

    private var title: String {
        jobs.showSearches && jobs.searchesError == nil && !jobs.searchesLoading
            ? "Saved searches (\(jobs.searches.count))" : "Saved searches"
    }

    @ViewBuilder private var content: some View {
        if let message = jobs.searchesError {
            HStack(spacing: 6) {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                Text(verbatim: "Couldn't read the saved searches: \(message)")
                    .font(.callout).foregroundStyle(.secondary)
                    .textSelection(.enabled)
                Spacer(minLength: 0)
                Button("Retry") { jobs.reloadSearches() }.controlSize(.small)
            }
        } else if jobs.searchesLoading && jobs.searches.isEmpty {
            ProgressView().controlSize(.small)
        } else if jobs.searches.isEmpty {
            Text("Nothing saved yet. Save a listing page from the browser extension, or add a board with `vani watchlist add`.")
                .font(.callout).foregroundStyle(.secondary)
        } else {
            ScrollView {
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(jobs.searches) { row($0) }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .frame(maxHeight: 140)
        }
    }

    private func row(_ s: WatchlistEntry) -> some View {
        HStack(spacing: 8) {
            Text(verbatim: s.title).font(.callout.weight(.medium)).lineLimit(1)
            Text(verbatim: s.kind)
                .font(.caption2.weight(.medium))
                .padding(.horizontal, 6).padding(.vertical, 1)
                .background(Color.secondary.opacity(0.16), in: Capsule())
            Text(verbatim: s.terms.isEmpty ? "no search terms" : s.terms.joined(separator: ", "))
                .font(.caption).foregroundStyle(.secondary).lineLimit(1)
                .help(s.terms.joined(separator: "\n"))
            Spacer(minLength: 0)
            if let url = s.openURL {
                Button("Open") { NSWorkspace.shared.open(url) }
                    .controlSize(.small)
                    .help(url.absoluteString)
            }
        }
    }
}

/// New / Saved / Applied / Hidden / All with a count beside each. An empty view
/// is dimmed, and a count vani did not send is left off rather than shown as 0.
private struct ViewSwitcher: View {
    @Environment(JobsModel.self) private var jobs

    var body: some View {
        HStack(spacing: 2) {
            ForEach(JobView.allCases) { v in tab(v) }
        }
        .padding(2)
        .background(.quaternary.opacity(0.5), in: Capsule())
    }

    private func tab(_ v: JobView) -> some View {
        let selected = jobs.view == v
        let n = JobListPresentation.count(jobs.page, v)
        let empty = n == 0
        return Button {
            jobs.view = v
        } label: {
            HStack(spacing: 5) {
                Image(systemName: JobBadge.symbol(for: v)).font(.caption)
                Text(v.title)
                if let n {
                    Text(n.formatted())
                        .font(.caption2.weight(.medium).monospacedDigit())
                        .padding(.horizontal, 5).padding(.vertical, 1)
                        .background(selected ? Color.accentColor.opacity(0.18) : Color.secondary.opacity(0.16), in: Capsule())
                }
            }
            .foregroundStyle(selected ? Color.primary : (empty ? Color.secondary.opacity(0.55) : Color.secondary))
            .padding(.horizontal, 10).padding(.vertical, 4)
            .background {
                if selected { Capsule().fill(.background).shadow(color: .black.opacity(0.12), radius: 1, y: 0.5) }
            }
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .help(v.isRanked
              ? (n.map { "\(v.title): \($0) \($0 == 1 ? "role" : "roles")" } ?? v.title)
              : "\(v.label(count: n)): read from the boards, not yet triaged, so unranked and not mixed into the other views")
        .accessibilityLabel(v.label(count: n))
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

/// Filters on triage readings, age and closed roles. Each is a `vani jobs`
/// flag; the view counts above ignore them, the list and Insights do not.
private struct FilterMenu: View {
    @Environment(JobsModel.self) private var jobs
    @AppStorage(JobList.hideDuplicatesKey) private var hideDuplicates = false

    var body: some View {
        Menu {
            Section("Category folder") {
                ForEach(Job.categoryOptions + ["none"], id: \.self) { toggle(\.categories, $0) }
            }
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
            Toggle("Hide possible duplicates", isOn: $hideDuplicates)
            Button("Clear Filters") { jobs.clearFilter() }
                .disabled(jobs.filter.isEmpty)
            if let note = jobs.labelsNote {
                Divider()
                Text(note)
            }
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

/// Interactive horizontal bar of chips for each active filter.
private struct ActiveFiltersBar: View {
    @Environment(JobsModel.self) private var jobs

    var body: some View {
        if !jobs.filter.isEmpty {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 6) {
                    Text("Filters:").font(.caption).foregroundStyle(.secondary)
                    ForEach(filterChips) { chip in
                        HStack(spacing: 4) {
                            Text(chip.label)
                            Button {
                                chip.remove()
                            } label: {
                                Image(systemName: "xmark")
                                    .font(.system(size: 8, weight: .bold))
                            }
                            .buttonStyle(.plain)
                            .foregroundStyle(.secondary)
                            .help("Remove filter: \(chip.label)")
                        }
                        .font(.caption)
                        .padding(.horizontal, 7)
                        .padding(.vertical, 3)
                        .background(.quaternary.opacity(0.6), in: Capsule())
                    }
                    Button("Clear All") {
                        jobs.clearFilter()
                    }
                    .buttonStyle(.borderless)
                    .font(.caption)
                    .foregroundStyle(Color.accentColor)
                    .help("Clear all active filters")
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 6)
            }
            Divider()
        }
    }

    private struct ChipItem: Identifiable {
        let id: String
        let label: String
        let remove: () -> Void
    }

    private var filterChips: [ChipItem] {
        var chips: [ChipItem] = []
        let f = jobs.filter
        for code in f.categories.sorted() {
            chips.append(ChipItem(id: "category:\(code)", label: Job.label(for: code)) {
                jobs.filter.categories.remove(code)
            })
        }
        for code in f.fits.sorted() {
            chips.append(ChipItem(id: "fit:\(code)", label: Job.label(for: code)) {
                jobs.filter.fits.remove(code)
            })
        }
        for code in f.workModes.sorted() {
            chips.append(ChipItem(id: "workMode:\(code)", label: Job.label(for: code)) {
                jobs.filter.workModes.remove(code)
            })
        }
        for code in f.sponsorships.sorted() {
            chips.append(ChipItem(id: "sponsorship:\(code)", label: Job.label(for: code)) {
                jobs.filter.sponsorships.remove(code)
            })
        }
        for code in f.seniorities.sorted() {
            chips.append(ChipItem(id: "seniority:\(code)", label: Job.label(for: code)) {
                jobs.filter.seniorities.remove(code)
            })
        }
        for code in f.states.sorted() {
            chips.append(ChipItem(id: "state:\(code)", label: code == "none" ? "No state" : "State: \(code)") {
                jobs.filter.states.remove(code)
            })
        }
        for area in f.areas.sorted() {
            chips.append(ChipItem(id: "area:\(area)", label: area) {
                jobs.filter.areas.remove(area)
            })
        }
        if f.remoteOnly {
            chips.append(ChipItem(id: "remoteOnly", label: "Remote only") {
                jobs.filter.remoteOnly = false
            })
        }
        if f.widensToRemote {
            chips.append(ChipItem(id: "orRemote", label: "Or remote") {
                jobs.filter.orRemote = false
            })
        }
        if let s = f.minSalary {
            chips.append(ChipItem(id: "minSalary", label: "≥ \(Job.Pay.short(Double(s)))/yr") {
                jobs.filter.minSalary = nil
            })
        }
        if f.salaryListed {
            chips.append(ChipItem(id: "salaryListed", label: "Stating pay") {
                jobs.filter.salaryListed = false
            })
        }
        if let r = f.minRelevance {
            chips.append(ChipItem(id: "minRelevance", label: "Relevance ≥ \(r)") {
                jobs.filter.minRelevance = nil
            })
        }
        if let d = f.maxAgeDays {
            chips.append(ChipItem(id: "maxAge", label: "≤ \(d) days") {
                jobs.filter.maxAgeDays = nil
            })
        }
        if f.openOnly {
            chips.append(ChipItem(id: "openOnly", label: "Hide closed") {
                jobs.filter.openOnly = false
            })
        }
        return chips
    }
}

/// The round button the job list and the detail pane float at their
/// bottom-trailing corner once scrolled past their top. No keyboard shortcut:
/// ⌘↑ belongs to the note editor, and the two panes would claim one key.
private struct BackToTopButton: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    let scroll: () -> Void

    var body: some View {
        Button {
            withAnimation(reduceMotion ? nil : .easeInOut(duration: 0.3)) { scroll() }
        } label: {
            Image(systemName: "arrow.up")
                .font(.body.weight(.semibold))
                .frame(width: 32, height: 32)
                .background(.regularMaterial, in: Circle())
                .shadow(color: .black.opacity(0.18), radius: 3, y: 1)
                .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .padding(12)
        .help("Back to top")
        .accessibilityLabel("Back to top")
    }
}

/// The list's on-screen rows, held outside `JobList`'s state. Rows report from
/// their appear and disappear callbacks while the List lays them out; a write
/// here redraws only `BackToTopOverlay`, which reads it. Kept in view state, each
/// report re-rendered the whole List mid-layout and rows were drawn over each other.
@MainActor
@Observable
private final class VisibleRowsTracker {
    var rows = VisibleRows()
}

/// `BackToTopButton` while the list is scrolled past its first row, read here so
/// a row report redraws this overlay and not the List.
private struct BackToTopOverlay: View {
    let tracker: VisibleRowsTracker
    let first: String?
    let scroll: () -> Void

    var body: some View {
        BackToTopFade(shown: tracker.rows.scrolledPast(first: first), scroll: scroll)
    }
}

/// Fades `BackToTopButton` in and out. The animation is scoped to the button:
/// attached to the scrolled view, it animated that view's rows and layout too.
private struct BackToTopFade: View {
    let shown: Bool
    let scroll: () -> Void

    var body: some View {
        ZStack {
            if shown { BackToTopButton(scroll: scroll).transition(.opacity) }
        }
        .animation(.easeInOut(duration: 0.15), value: shown)
    }
}

private extension View {
    /// Overlays `BackToTopButton` while `shown`.
    func backToTop(_ shown: Bool, scroll: @escaping () -> Void) -> some View {
        overlay(alignment: .bottomTrailing) { BackToTopFade(shown: shown, scroll: scroll) }
    }
}

// MARK: - List

private struct JobList: View {
    @Environment(JobsModel.self) private var jobs
    static let hideDuplicatesKey = "jobs.hidePossibleDuplicates"
    /// Off by default: duplicates are noted on their rows, not removed.
    @AppStorage(JobList.hideDuplicatesKey) private var hideDuplicates = false
    /// The rows on screen, for the back-to-top button: List rows load lazily,
    /// so their appear/disappear tracks scrolling.
    @State private var visibleRows = VisibleRowsTracker()

    var body: some View {
        @Bindable var jobs = jobs
        VStack(spacing: 0) {
            RetriageBatchBanner()
            if let page = jobs.page, !jobs.jobs.isEmpty {
                listHeader(page)
            }
            // Each state fills the column, so an empty tab keeps the list's
            // height and the footer stays at the bottom.
            Group {
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
                    ContentUnavailableView {
                        Label(emptyTitle, systemImage: emptySymbol)
                    } description: {
                        Text(emptyText)
                    } actions: {
                        if !jobs.filter.isEmpty {
                            Button("Clear Filters") { jobs.clearFilter() }
                        }
                        if !jobs.query.isEmpty {
                            Button("Clear Search") { jobs.query = "" }
                        }
                        if let waiting = jobs.page?.pending?.total, waiting > 0 {
                            TriageNowButton(waiting: waiting)
                        }
                    }
                } else if hideDuplicates && Job.hidingPossibleDuplicates(jobs.jobs).isEmpty {
                    ContentUnavailableView {
                        Label("Only possible duplicates here", systemImage: "square.on.square")
                    } description: {
                        Text("Every role in this list may repeat another one, and “Hide possible duplicates” is on.")
                    } actions: {
                        Button("Show Them") { hideDuplicates = false }
                    }
                } else {
                    // Rows stay one per posting, so the ⌘S / ⇧⌘A / ⌘⌫ shortcuts keep
                    // acting on the one open job key; a repost is noted on its rows and
                    // listed in the detail. Several selected rows (⌘-click, ⇧-click)
                    // open the selection pane instead, whose action is the batch re-triage.
                    let postings = PostingGroups(jobs.jobs)
                    let shown = hideDuplicates ? Job.hidingPossibleDuplicates(jobs.jobs) : jobs.jobs
                    // Targets are named from the whole loaded list, hidden rows included.
                    let loaded = Dictionary(jobs.jobs.map { ($0.key, $0) }, uniquingKeysWith: { first, _ in first })
                    ScrollViewReader { proxy in
                        List(shown, selection: $jobs.selectedKeys) { job in
                            JobRow(job: job, others: postings.others(of: job.key), duplicates: job.duplicateBadges(loaded: loaded))
                                .tag(job.key)
                                .onAppear { visibleRows.rows.appeared(job.key) }
                                .onDisappear { visibleRows.rows.disappeared(job.key) }
                        }
                        .listStyle(.inset)
                        // Right-click acts on the selection when the row is in it, else on the row.
                        .contextMenu(forSelectionType: String.self) { keys in
                            let picked = jobs.jobs.filter { keys.contains($0.key) }
                            if picked.count == 1, let job = picked.first {
                                JobMenu(job: job)
                            } else if picked.count > 1 {
                                SelectionMenu(picked: picked)
                            }
                        }
                        // A reload rebuilds the list; keep the selected role in view.
                        .onChange(of: jobs.page) {
                            if let s = jobs.selection { proxy.scrollTo(s) }
                        }
                        .overlay(alignment: .bottomTrailing) {
                            BackToTopOverlay(tracker: visibleRows, first: shown.first?.key) {
                                if let first = shown.first { proxy.scrollTo(first.key, anchor: .top) }
                            }
                        }
                    }
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            if let page = jobs.page {
                footer(page)
            }
        }
    }

    /// What the list is ordered by, and the states that change what it holds:
    /// postings waiting for triage, a capped list, a run still writing.
    @ViewBuilder
    private func listHeader(_ page: JobsPage) -> some View {
        // In the Awaiting triage view the list itself is the backlog: no pill for it.
        let triage = jobs.view == .pending ? nil : JobListPresentation.triageBanner(page.pending)
        let cap = JobListPresentation.capNotice(shown: page.shown, total: page.total)
        let waiting = page.pending?.total ?? 0
        let warning = JobListPresentation.insightWarning(page.insight)
        VStack(alignment: .leading, spacing: 6) {
            Text(JobListPresentation.sortCaption(jobs.sort, view: jobs.view))
                .font(.caption)
                .foregroundStyle(.secondary)
            if let warning {
                // The same yellow-triangle label Insights shows for this warning.
                Label {
                    Text(verbatim: warning)
                } icon: {
                    Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.yellow)
                }
                .font(.caption)
                .lineLimit(2)
                .fixedSize(horizontal: false, vertical: true)
                .help(warning)
            }
            if triage != nil || cap != nil || page.torn == true || waiting > 0 {
                WrapLayout(spacing: 6) {
                    if waiting > 0 {
                        TriageNowButton(waiting: waiting)
                    }
                    if let triage, let pending = page.pending {
                        // A button, so the backlog is one click from the ranked views.
                        Button { jobs.view = .pending } label: {
                            StatePill(symbol: "hourglass", text: triage, tint: .orange)
                        }
                        .buttonStyle(.plain)
                        .help(pending.footerHelp)
                    }
                    if let cap {
                        StatePill(symbol: "line.3.horizontal.decrease", text: cap + " · search for more", tint: .blue)
                            .help("The list is capped at \(page.shown) roles; \(page.total) match. Search or filter to see the rest.")
                    }
                    if page.torn == true {
                        StatePill(symbol: "arrow.triangle.2.circlepath", text: "A run is writing: refresh soon", tint: .yellow)
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        Divider()
    }

    @ViewBuilder
    private func footer(_ page: JobsPage) -> some View {
        Divider()
        HStack(spacing: 6) {
            if jobs.loading { ProgressView().controlSize(.small) }
            if page.shown < page.total {
                Text("\(page.shown) of \(page.total) roles")
            } else {
                Text("\(page.total) \(page.total == 1 ? "role" : "roles")")
            }
            if let err = jobs.error {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.yellow).help(err)
            }
            if let note = jobs.labelsNote {
                Label("Built-in labels", systemImage: "textformat").foregroundStyle(.orange).help(note)
            }
            Spacer()
            if let ledger = page.ledger {
                // A count vani did not send is unknown, not zero postings.
                Image(systemName: "externaldrive")
                    .help(page.records.map { "Reading \(ledger) (\($0) postings)" } ?? "Reading \(ledger)")
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
        case .pending: return "Nothing awaiting triage"
        }
    }

    private var emptySymbol: String {
        jobs.query.isEmpty && jobs.filter.isEmpty ? JobBadge.symbol(for: jobs.view) : "magnifyingglass"
    }

    private var emptyText: String {
        let waiting = jobs.page?.pending?.emptySearchNote(query: jobs.query, in: jobs.view).map { " " + $0 } ?? ""
        if !jobs.filter.isEmpty { return "No role in this view passes the filters. Clear them from the Filter menu." + waiting }
        if !jobs.query.isEmpty { return "No role in this view matches “\(jobs.query)”." + waiting }
        switch jobs.view {
        case .new:
            return "Every role Vani kept has been saved, applied to or hidden. New ones arrive with the daily run."
                + (jobs.page?.pending?.emptyListNote(in: jobs.view).map { " " + $0 } ?? "")
        case .saved: return "Save a role (⌘S) to keep it here."
        case .applied: return "Mark a role Applied (⇧⌘A) to track it here with the date."
        case .hidden: return "Hidden roles (⌘⌫) leave New and wait here."
        case .all: return "Run `vani run-once` to retrieve roles."
        case .pending: return "Every posting Vani read from the boards has been triaged. New ones wait here until a run reads them."
        }
    }
}

private struct JobRow: View {
    @Environment(JobsModel.self) private var jobs
    let job: Job
    /// The other postings of this role in the list (same company and title).
    var others: [Job] = []
    /// Possible-duplicate and same-role-elsewhere notes (Job.duplicateBadges).
    var duplicates: [DuplicateBadge] = []

    var body: some View {
        let score = JobScore.forRow(job, sort: jobs.sort)
        HStack(alignment: .top, spacing: 10) {
            CompanyAvatar(company: job.company, logo: job.logoPath, size: 34)
            VStack(alignment: .leading, spacing: 3) {
                HStack(alignment: .firstTextBaseline, spacing: 4) {
                    if let mark = job.mark { JobBadge(mark: mark) }
                    Text(verbatim: job.title)
                        .font(.headline)
                        .lineLimit(2)
                }
                HStack(spacing: 4) {
                    Text(verbatim: job.company)
                        .lineLimit(1)
                    if let w = job.whereLabel {
                        Text("·").foregroundStyle(.tertiary)
                        Text(verbatim: w).lineLimit(1)
                    }
                    // Plain text, never wrapped mid-range; the place truncates first.
                    if let pay = job.payLabel(preferring: jobs.filter.currency) {
                        Text("·").foregroundStyle(.tertiary)
                        Text(verbatim: pay)
                            .lineLimit(1)
                            .fixedSize()
                            .layoutPriority(1)
                            .help("Pay as the posting states it")
                    }
                }
                .font(.subheadline)
                .foregroundStyle(.secondary)
                WrapLayout(spacing: 4) {
                    if let cat = job.categoryLabel { Chip(text: cat, tint: .indigo) }
                    // The strongest fit is the expectation, so it gets no chip.
                    if let fit = job.fitLabel, job.triage?["role_fit"] != Job.roleFitOrder.first {
                        Chip(text: fit, tint: fitTint(job))
                    }
                    if let rank = job.rank { Chip(text: "Rank \(Int(rank.rounded()))", tint: .accentColor) }
                    if job.closed == true { Chip(text: "Closed", tint: .red) }
                    // No fit, rank or score for these: say so where they would be.
                    if job.isUntriaged { Chip(text: "Not triaged yet", tint: .orange).help(Job.untriagedNote) }
                    if let drop = job.prefilterNote { Chip(text: "Next run drops it", tint: .secondary).help(drop) }
                    // A batch re-triage's state for this row, else whether Sonnet has read it.
                    if let state = jobs.batch.rows[job.key] {
                        let label = RetriageBatchPresentation.rowLabel(state)
                        Chip(text: label.text, tint: batchTint(state)).help(label.help)
                    } else if let m = job.manualTriage, m.current {
                        Chip(text: "Sonnet", tint: .purple).help(RetriagePresentation.header(at: m.at))
                    }
                    ForEach(targetKeywords, id: \.term) { Chip(text: $0.term, tint: .secondary) }
                }
                if let first = job.reasons.first, first.kind != .other {
                    Text(verbatim: "\(first.kind.label): \(first.text)")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                if let note = JobListPresentation.alsoPosted(others) {
                    Label(note, systemImage: "square.on.square")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .help("The same company and title is listed \(others.count == 1 ? "once" : "\(others.count) times") more; open the role to see where")
                }
                ForEach(duplicates, id: \.text) { d in
                    Label {
                        Text(verbatim: d.text)
                    } icon: {
                        Image(systemName: d.kind == .candidate ? "exclamationmark.square" : "mappin.and.ellipse")
                    }
                    .font(.caption)
                    .foregroundStyle(d.kind == .candidate ? Color.orange : Color.secondary)
                    .lineLimit(1)
                    .help(d.help)
                }
            }
            Spacer(minLength: 4)
            VStack(alignment: .trailing, spacing: 4) {
                if let score { ScoreBadge(score: score) }
                if let age = job.ageLabel {
                    Text(verbatim: age)
                        .font(.caption.monospacedDigit())
                        .foregroundStyle(freshTint)
                        .help(ageHelp)
                }
            }
        }
        .padding(.vertical, 4)
        .opacity(job.closed == true ? 0.72 : 1.0)
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
        if job.isUntriaged { parts.append("not triaged yet") }
        if let pay = job.payLabel(preferring: jobs.filter.currency) { parts.append("pay " + pay) }
        if let age = job.ageDays { parts.append(job.freshSource == .seen ? "first seen \(age) days ago" : "posted \(age) days ago") }
        if let p = JobScore.display(job.priority) { parts.append("priority \(p)") }
        if let also = JobListPresentation.alsoPosted(others) { parts.append(also) }
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

/// Format job details for clipboard copying.
func copyJobDetailsToPasteboard(job: Job, description: String? = nil) {
    var lines: [String] = []
    var heading = job.title
    if !job.company.isEmpty {
        heading += " at \(job.company)"
    }
    lines.append(heading)
    if let whereLabel = job.whereLabel, !whereLabel.isEmpty {
        lines.append(whereLabel)
    }
    if let link = job.shareLink ?? job.postingLink {
        lines.append(link.absoluteString)
    }
    let desc = description ?? job.description ?? ""
    if !desc.isEmpty {
        lines.append("")
        lines.append(desc)
    }
    NSPasteboard.general.clearContents()
    NSPasteboard.general.setString(lines.joined(separator: "\n"), forType: .string)
}

/// The mark actions, shared by the row's context menu and the detail pane.
private struct JobMenu: View {
    @Environment(ExclusionsEditor.self) private var exclusions
    @Environment(JobsModel.self) private var jobs
    let job: Job

    var body: some View {
        Button(job.mark == .saved ? "Unsave" : "Save", systemImage: "bookmark") { act(.saved) }
        Button(job.mark == .applied ? "Unmark Applied" : "Mark Applied", systemImage: "checkmark.seal") { act(.applied) }
        Button(job.mark == .hidden ? "Unhide" : "Hide", systemImage: "eye.slash") { act(.hidden) }
        // Shows the cost first and spends only on confirmation; not offered for a posting no run has triaged.
        if jobs.retriage.isAvailable {
            let reason = jobs.retriage.blockedReason(for: job)
            Button(job.manualTriage == nil ? "Re-triage with Sonnet…" : "Re-run with Sonnet…", systemImage: "sparkles") {
                jobs.startRetriage(job)
            }
            .disabled(reason != nil)
            .help(reason ?? "Ask Sonnet to read this posting again. The cost is shown first.")
        }
        // Every role at the company, not just this one; undone in Settings ›
        // Exclude postings. Roles you saved, applied to or hid stay.
        if !job.company.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            Button("Exclude \u{201C}\(job.company)\u{201D}", systemImage: "nosign") {
                Task { await jobs.excludeCompany(of: job, using: exclusions) }
            }
            .help("Add this company to Settings › Exclude postings: Jobs hides its roles and the next run drops them.")
        }
        Divider()
        Button("Copy Job Details", systemImage: "doc.on.doc") {
            copyJobDetailsToPasteboard(job: job, description: jobs.detail?.key == job.key ? jobs.detail?.description : nil)
        }
        Button("Copy Job Title", systemImage: "character.cursor.ibeam") {
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(job.title, forType: .string)
        }
        if let link = job.shareLink ?? job.postingLink {
            Button("Copy Link", systemImage: "link") {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(link.absoluteString, forType: .string)
            }
        }
        if let posting = job.postingLink {
            Button("Open Posting", systemImage: "safari") { NSWorkspace.shared.open(posting) }
        }
        if let link = job.shareLink {
            ShareLink(item: link, subject: Text(job.title), message: Text(job.shareSubject)) {
                Label("Share…", systemImage: "square.and.arrow.up")
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
        if jobs.isMultiSelection {
            SelectionPane()
        } else if let job = jobs.selected {
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
    @State private var saveStatusMessage: String?
    @State private var showDescription = true
    @State private var noteOpen = false
    @State private var copiedDetails = false
    @State private var copiedTitle = false
    @State private var copiedDescription = false
    // While the note has focus, ⌘⌫ and ⌘S belong to the note, not to Hide and Save.
    @FocusState private var noteFocused: Bool
    /// The header card has scrolled out of view: offer the way back.
    @State private var headerScrolledOff = false
    private static let topID = "jobDetailTop"

    private var noteBytes: Int { noteDraft.trimmingCharacters(in: .whitespacesAndNewlines).utf8.count }
    private var noteChanged: Bool { noteDraft.trimmingCharacters(in: .whitespacesAndNewlines) != (job.note ?? "") }

    /// Other postings in the list of the same company and title.
    private var otherPostings: [Job] {
        guard let mine = JobListPresentation.groupKey(company: job.company, title: job.title) else { return [] }
        return jobs.jobs.filter {
            $0.key != job.key && JobListPresentation.groupKey(company: $0.company, title: $0.title) == mine
        }
    }

    private func otherPostingsCard(_ others: [Job]) -> some View {
        section("Also posted", caption: "same company and title") {
            VStack(alignment: .leading, spacing: 4) {
                ForEach(others) { o in
                    Button {
                        jobs.selection = o.key
                    } label: {
                        HStack(spacing: 8) {
                            Text(verbatim: o.ageLabel ?? "age unknown")
                                .monospacedDigit()
                                .frame(minWidth: 56, alignment: .leading)
                            Text(verbatim: o.whereLabel ?? "no location listed")
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                            if let mark = o.mark { JobBadge(mark: mark) }
                            Spacer()
                            Image(systemName: "chevron.right").font(.caption).foregroundStyle(.tertiary)
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .help("Open this posting")
                }
            }
        }
    }
    private var currentDescription: String? {
        jobs.detail?.key == job.key ? jobs.detail?.description : job.description
    }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    headerCard
                        .onGeometryChange(for: Bool.self) { $0.frame(in: .scrollView).maxY < 0 } action: { headerScrolledOff = $0 }
                    ApplyKitView(job: job, description: currentDescription)
                    if !job.reasons.isEmpty { reasons }
                    RetriageSection(job: job, shortcutEnabled: !noteFocused)
                    if let ranking = job.rankingBreakdown { rankingCard(ranking) }
                    if !job.keywordList.isEmpty { keywords }
                    let others = otherPostings
                    if !others.isEmpty { otherPostingsCard(others) }
                    facts
                    note
                    description
                        .cardStyle()
                }
                .padding(20)
                .frame(maxWidth: 760, alignment: .leading)
                .id(Self.topID)
            }
            .backToTop(headerScrolledOff) { proxy.scrollTo(Self.topID, anchor: .top) }
        }
        .onAppear { noteDraft = job.note ?? "" }
        .onChange(of: job.key) { oldKey, newKey in
            if noteChanged && noteBytes <= JobsClient.maxNoteBytes && !savingNote {
                let key = oldKey
                let text = noteDraft
                Task { await jobs.saveNote(key, text) }
            }
            noteDraft = job.note ?? ""
            saveStatusMessage = nil
        }
        .onChange(of: noteFocused) { _, isFocused in
            if !isFocused && noteChanged && noteBytes <= JobsClient.maxNoteBytes && !savingNote {
                saveNoteAction()
            }
        }
    }

    private func saveNoteAction() {
        savingNote = true
        Task {
            let success = await jobs.saveNote(job.key, noteDraft)
            savingNote = false
            if success {
                saveStatusMessage = "Saved"
                try? await Task.sleep(for: .seconds(2))
                saveStatusMessage = nil
            }
        }
    }

    private func copyDetailsAction() {
        copyJobDetailsToPasteboard(job: job, description: currentDescription)
        copiedDetails = true
        Task {
            try? await Task.sleep(for: .milliseconds(1500))
            copiedDetails = false
        }
    }

    /// The header card: who and what, the three metrics across the card, then the
    /// actions. Anything that belongs right under it (the apply kit) goes
    /// directly after `headerCard` in `body`.
    private var headerCard: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(alignment: .top, spacing: 14) {
                CompanyAvatar(company: job.company, logo: job.logoPath, size: 52)
                header
            }
            if job.isUntriaged {
                Divider()
                VStack(alignment: .leading, spacing: 4) {
                    Label(Job.untriagedNote, systemImage: "hourglass")
                        .font(.callout)
                        .foregroundStyle(.orange)
                    if let drop = job.prefilterNote {
                        Text(drop).font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
            if job.relevance != nil || job.ageDays != nil {
                Divider()
                scores
            }
            Divider()
            actions
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .cardStyle(padding: 16)
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline) {
                Text(verbatim: job.title)
                    .font(.title2.bold())
                    .textSelection(.enabled)
                Button {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(job.title, forType: .string)
                    copiedTitle = true
                    Task {
                        try? await Task.sleep(for: .milliseconds(1500))
                        copiedTitle = false
                    }
                } label: {
                    Image(systemName: copiedTitle ? "checkmark" : "doc.on.doc")
                        .foregroundStyle(copiedTitle ? .green : .secondary)
                }
                .buttonStyle(.borderless)
                .controlSize(.small)
                .help("Copy job title")
            }
            Text(verbatim: [job.company, job.whereLabel ?? "", job.department ?? ""].filter { !$0.isEmpty }.joined(separator: " · "))
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
            if let pay = job.pay, !pay.isEmpty {
                WrapLayout(spacing: 12) {
                    ForEach(pay, id: \.self) { p in
                        Label {
                            Text(verbatim: p.label)
                        } icon: {
                            Image(systemName: "banknote")
                        }
                        .font(.callout)
                        .help(p.text.map { "Read from the posting: “\($0)”" } ?? "")
                    }
                }
            }
            FlowChips(job: job)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
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
            // One prominent action: the posting. With no posting link the
            // application form takes its place.
            if let link = job.postingLink {
                Button("Open Posting", systemImage: "safari") { NSWorkspace.shared.open(link) }
                    .buttonStyle(.borderedProminent)
                    .keyboardShortcut("o")
                    .help("Open the posting in your browser (⌘O)")
            }
            if let apply = job.applyLink, apply != job.postingLink {
                if job.postingLink == nil {
                    applyButton(apply).buttonStyle(.borderedProminent)
                } else {
                    applyButton(apply).buttonStyle(.bordered)
                }
            }
            markButton(.saved, on: "Saved", off: "Save", symbol: "bookmark")
                .keyboardShortcut(noteFocused ? nil : KeyboardShortcut("s"))
                .help("Save to come back to it (⌘S)")
            markButton(.applied, on: "Applied", off: "Mark Applied", symbol: "checkmark.seal")
                .keyboardShortcut(noteFocused ? nil : KeyboardShortcut("a", modifiers: [.command, .shift]))
                .help("Record that you applied, with the date (⇧⌘A)")
            Spacer(minLength: 8)
            Menu {
                Button(job.mark == .hidden ? "Unhide" : "Hide", systemImage: "eye.slash") {
                    Task { await jobs.toggle(job, .hidden) }
                }
                .keyboardShortcut(noteFocused ? nil : KeyboardShortcut(.delete))
                .help("Not interested: move it out of New (⌘⌫)")
                Divider()
                Button("Copy Job Details", systemImage: "doc.on.doc") {
                    copyDetailsAction()
                }
                .keyboardShortcut("c", modifiers: [.command, .shift])
                Button("Copy Title", systemImage: "character.cursor.ibeam") {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(job.title, forType: .string)
                }
                if let desc = currentDescription, !desc.isEmpty {
                    Button("Copy Description", systemImage: "text.alignleft") {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(desc, forType: .string)
                    }
                }
                if let link = job.shareLink ?? job.postingLink {
                    Button("Copy Link", systemImage: "link") {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(link.absoluteString, forType: .string)
                    }
                }
                if let link = job.shareLink {
                    Divider()
                    ShareLink(item: link, subject: Text(job.title), message: Text(job.shareSubject)) {
                        Label("Share…", systemImage: "square.and.arrow.up")
                    }
                }
            } label: {
                Image(systemName: copiedDetails ? "checkmark.circle.fill" : "ellipsis.circle")
                    .foregroundStyle(copiedDetails ? .green : .primary)
            }
            .menuIndicator(.hidden)
            .fixedSize()
            .help("More: Hide (⌘⌫), Copy Details (⇧⌘C), Copy Title, Description or Link, Share")
        }
        .controlSize(.large)
    }

    private func applyButton(_ apply: URL) -> some View {
        Button("Apply", systemImage: "paperplane") { NSWorkspace.shared.open(apply) }
            .help("Open the application form; Vani Apply fills it from your answers")
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
            // The three metrics share the card's width equally.
            HStack(alignment: .top, spacing: 0) {
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
                    .frame(maxWidth: .infinity, alignment: .leading)
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
        let shown = JobScore.display(value)
        return VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.caption).foregroundStyle(.secondary)
            HStack(spacing: 8) {
                if let shown {
                    Text("\(shown)")
                        .font(.title2.weight(.semibold).monospacedDigit())
                        .foregroundStyle(gradeTint(JobScore.grade(shown)))
                    ScoreBar(value: value).frame(width: 60, height: 5)
                } else {
                    Text("not available").foregroundStyle(.secondary)
                }
            }
            Text(detail).font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .help("\(title) \(shown.map(String.init) ?? "unavailable") of 100, \(detail). Display only: it sorts and plots, never drops a role.")
    }

    /// A titled card in the Home style; `caption` sits at the trailing edge.
    private func section<C: View>(_ title: String, caption: String? = nil, @ViewBuilder _ content: () -> C) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline) {
                Text(title).font(.headline)
                Spacer()
                if let caption { Text(caption).font(.caption).foregroundStyle(.secondary) }
            }
            content()
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .cardStyle()
    }

    private var reasons: some View {
        // The percentage is jdreasons' decision-model probability for the pick, in percent
        // (internal/decide/jdreasons): how sure the decision model is of it.
        section("Why this role", caption: job.reasons.contains { $0.pct != nil } ? "% = how sure the model is of each pick" : nil) {
            VStack(alignment: .leading, spacing: 8) {
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
                            HStack(spacing: 5) {
                                ScoreBar(value: Double(pct)).frame(width: 34, height: 4)
                                Text("\(min(max(pct, 0), 100))%")
                                    .font(.caption.monospacedDigit())
                                    .foregroundStyle(.secondary)
                            }
                            .help("How sure the decision model is of this pick: its probability, in percent")
                        }
                    }
                }
            }
        }
    }

    /// Why the role sits where it does: each component of the priority, then
    /// the posting lines that matched your roles best. Display only.
    private func rankingCard(_ ranking: RankingBreakdown) -> some View {
        section("Why it ranks here", caption: "display only: orders roles, never drops one") {
            VStack(alignment: .leading, spacing: 10) {
                if !ranking.factors.isEmpty {
                    Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 12, verticalSpacing: 6) {
                        ForEach(ranking.ordered, id: \.name) { f in
                            GridRow {
                                Text(f.title).font(.callout)
                                factorValue(f)
                                Text(verbatim: factorNote(f, ageKnown: ranking.ageKnown))
                                    .font(.caption).foregroundStyle(.secondary)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                            .help(f.explanation)
                        }
                    }
                }
                if !ranking.evidence.isEmpty {
                    VStack(alignment: .leading, spacing: 6) {
                        HStack(alignment: .firstTextBaseline) {
                            Text("Closest to your résumé").font(.subheadline.weight(.semibold))
                            if let s = ranking.semanticScore, s.isFinite {
                                Text("match \(Int((min(max(s, 0), 1) * 100).rounded()))%")
                                    .font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                            }
                        }
                        ForEach(Array(ranking.evidence.enumerated()), id: \.offset) { _, e in
                            evidenceRow(e)
                        }
                    }
                }
            }
        }
    }

    @ViewBuilder
    private func factorValue(_ f: ScoreFactor) -> some View {
        if !f.known {
            Text("not known").font(.callout).foregroundStyle(.tertiary)
        } else {
            HStack(spacing: 6) {
                Text(f.valueLabel).font(.callout.monospacedDigit())
                if !f.isMultiplier { ScoreBar(value: f.fraction * 100).frame(width: 44, height: 4) }
            }
        }
    }

    /// The factor's note, and for a fit that rests on another model's reading, whose it is
    /// ("read by Sonnet (manual)", "read by the escalation model").
    private func factorNote(_ f: ScoreFactor, ageKnown: Bool) -> String {
        [factorBaseNote(f, ageKnown: ageKnown), f.sourceNote ?? ""].filter { !$0.isEmpty }.joined(separator: " · ")
    }

    /// The factor's share of the blend, or what a multiplier did to it.
    private func factorBaseNote(_ f: ScoreFactor, ageKnown: Bool) -> String {
        // An undated posting is assumed to be as old as Vani's first sighting plus a prior (the
        // value shown is that assumption); a vani from before that reports the floor as known.
        if f.name == "fresh" && !ageKnown {
            if f.note == "age_neutral" {
                return "age unknown: ×" + String(format: "%.2f", f.fraction)
                    + " held flat; this employer closes postings by application cap, not by age"
            }
            return f.known ? "age unknown: held at the floor"
                : "age unknown: ×" + String(format: "%.2f", f.fraction) + " assumed from when Vani first saw it"
        }
        // Score counts an unknown component at a neutral prior, so say that, not "left out".
        if !f.known { return [f.weightLabel, f.unknownNote].compactMap { $0 }.joined(separator: ", ") }
        if let w = f.weightLabel { return w }
        if f.isMultiplier { return f.fraction >= 0.995 ? "no effect" : (f.fraction < 1 ? "lowers the score" : "raises the score") }
        return ""
    }

    private func evidenceRow(_ e: SemanticMatch.Evidence) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 6) {
            Text(verbatim: "“\(e.snippet)”").textSelection(.enabled)
            Text("↔").foregroundStyle(.secondary)
            Text(verbatim: e.label.isEmpty ? "your résumé" : e.label).fontWeight(.medium)
            Spacer(minLength: 4)
            Text(verbatim: String(format: "%.2f", e.cosine))
                .font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                .help("Cosine similarity of the posting line and that part of your résumé")
        }
        .font(.callout)
    }

    private var keywords: some View {
        section("Keywords") {
            VStack(alignment: .leading, spacing: 8) {
                WrapLayout(spacing: 4) {
                    ForEach(job.keywordList, id: \.term) { k in
                        Chip(text: k.n > 1 ? "\(k.term) ×\(k.n)" : k.term, tint: keywordTint(k))
                            .help(keywordHelp(k))
                    }
                }
                // The legend shows the chip colours themselves.
                WrapLayout(spacing: 4) {
                    Text("Key").font(.caption2).foregroundStyle(.tertiary)
                    Chip(text: "in your target summary", tint: .green)
                    Chip(text: "DevPrism: missing or weak", tint: .orange)
                    Chip(text: "mentioned", tint: .secondary)
                }
                .opacity(0.85)
            }
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
            if let cat = job.categoryLabel { row("Category folder", cat) }
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

    /// A one-line prompt until there is a note or you start one; then the editor.
    @ViewBuilder
    private var note: some View {
        if noteOpen || noteFocused || !noteDraft.isEmpty {
            noteEditor
        } else {
            HStack(spacing: 8) {
                Button {
                    noteOpen = true
                } label: {
                    Label("Add a private note", systemImage: "square.and.pencil")
                }
                .buttonStyle(.borderless)
                .help("Write a note for this role. It stays on this Mac and is never sent to a model.")
                Text("Kept on this Mac; never sent to a model.")
                    .font(.caption)
                    .foregroundStyle(.tertiary)
                Spacer()
            }
            .padding(.vertical, 2)
        }
    }

    private var noteEditor: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Private note").font(.headline)
            VStack(alignment: .leading, spacing: 6) {
                TextEditor(text: $noteDraft)
                    .focused($noteFocused)
                    .font(.body)
                    .frame(minHeight: 56, maxHeight: 160)
                    .scrollContentBackground(.hidden)
                    .onAppear { if noteOpen { noteFocused = true } }
                    .onChange(of: noteFocused) { _, isFocused in
                        // An empty note that lost focus folds back to the prompt.
                        if !isFocused && noteDraft.isEmpty { noteOpen = false }
                    }
                HStack {
                    if let msg = saveStatusMessage {
                        Label(msg, systemImage: "checkmark.circle.fill")
                            .foregroundStyle(.green)
                    } else if let at = job.noteAt, !noteChanged {
                        Text("Saved \(at.formatted(date: .abbreviated, time: .shortened)). Kept on this Mac; never sent to a model.")
                    } else {
                        Text("Kept on this Mac in Vani's state folder. Never sent to a model.")
                    }
                    Spacer()
                    Text("\(noteBytes) / \(JobsClient.maxNoteBytes)")
                        .monospacedDigit()
                        .foregroundStyle(noteBytes > JobsClient.maxNoteBytes ? .red : .secondary)
                    Button(savingNote ? "Saving…" : "Save Note") {
                        saveNoteAction()
                    }
                    .disabled(!noteChanged || noteBytes > JobsClient.maxNoteBytes || savingNote)
                    .keyboardShortcut(noteFocused ? KeyboardShortcut("s") : nil)
                    .help("Save the note (⌘S while typing)")
                }
                .font(.caption)
                .foregroundStyle(.secondary)
            }
        }
        .cardStyle(padding: 12)
    }

    @ViewBuilder
    private var description: some View {
        DisclosureGroup(isExpanded: $showDescription) {
            if let text = currentDescription {
                // Posting text is shown verbatim, a paragraph at a time.
                let paragraphs = JobListPresentation.paragraphs(text)
                LazyVStack(alignment: .leading, spacing: 10) {
                    if paragraphs.isEmpty {
                        Text("The board gave no description.").foregroundStyle(.secondary)
                    }
                    ForEach(Array(paragraphs.enumerated()), id: \.offset) { _, p in
                        Text(verbatim: p)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
                .padding(.top, 8)
            } else if let err = jobs.detailError {
                Text(verbatim: err).foregroundStyle(.red)
            } else {
                ProgressView().controlSize(.small)
            }
        } label: {
            HStack {
                Text("Job description")
                if let text = currentDescription, !text.isEmpty {
                    let words = text.split(whereSeparator: \.isWhitespace).count
                    Text("· \(words) words")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                if let text = currentDescription, !text.isEmpty {
                    Button {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(text, forType: .string)
                        copiedDescription = true
                        Task {
                            try? await Task.sleep(for: .milliseconds(1500))
                            copiedDescription = false
                        }
                    } label: {
                        Label(copiedDescription ? "Copied" : "Copy", systemImage: copiedDescription ? "checkmark" : "doc.on.doc")
                    }
                    .buttonStyle(.borderless)
                    .controlSize(.small)
                    .help("Copy job description to clipboard")
                }
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
        WrapLayout(spacing: 4) {
            if job.mark == .hidden { Chip(text: "Hidden", tint: .secondary) }
            if let cat = job.categoryLabel { Chip(text: cat, tint: .indigo) }
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

struct Chip: View {
    let text: String
    let tint: Color
    // On a selected list row the system paints the accent colour behind the
    // row; a tinted chip would wash out there, so it turns white.
    @Environment(\.backgroundProminence) private var prominence

    var body: some View {
        let selected = prominence == .increased
        Text(verbatim: text)
            .font(.caption)
            .lineLimit(1)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(selected ? Color.white.opacity(0.22) : tint.opacity(0.14), in: Capsule())
            .foregroundStyle(selected ? Color.white : (tint == .secondary ? Color.secondary : tint))
    }
}

/// A company's icon, cached by Vani (Job.logoPath), on a light tile; without
/// one, its rounded monogram, coloured by a stable hash of the name.
private struct CompanyAvatar: View {
    let company: String
    var logo: String? = nil
    var size: CGFloat = 34

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: size * 0.28, style: .continuous)
        Group {
            if let image = logo.flatMap(CompanyLogoImages.image) {
                shape.fill(Color.white)
                    .overlay {
                        Image(nsImage: image)
                            .resizable()
                            .interpolation(.high)
                            .aspectRatio(contentMode: .fit)
                            .padding(size * 0.1)
                    }
                    .clipShape(shape)
                    .overlay { shape.strokeBorder(Color.primary.opacity(0.12), lineWidth: 0.5) }
            } else {
                let m = CompanyMonogram(company: company)
                shape.fill(Color(hue: m.hue, saturation: 0.5, brightness: 0.66))
                    .overlay {
                        Text(verbatim: m.initials)
                            .font(.system(size: size * 0.4, weight: .semibold, design: .rounded))
                            .foregroundStyle(.white)
                    }
            }
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }
}

/// Decoded company icons by file path, so a list of a thousand rows reads each
/// file once. A file that does not decode is remembered as absent. Icons are
/// refetched monthly at the same path; a running app keeps the one it read.
@MainActor
private enum CompanyLogoImages {
    private static var cache: [String: NSImage?] = [:]
    private static let maxEntries = 512

    static func image(_ path: String) -> NSImage? {
        if let hit = cache[path] { return hit }
        var image = NSImage(contentsOf: URL(fileURLWithPath: path))
        if let i = image, !i.isValid || i.size.width <= 0 || i.size.height <= 0 { image = nil }
        if cache.count >= maxEntries { cache.removeAll() }
        cache[path] = image
        return image
    }
}

/// The 0-100 score as a number, graded by colour like Home's badge.
private struct ScoreBadge: View {
    let score: JobScore
    @Environment(\.backgroundProminence) private var prominence

    var body: some View {
        let selected = prominence == .increased
        Text("\(score.value)")
            .font(.callout.weight(.semibold)).monospacedDigit()
            .frame(minWidth: 24)
            .padding(.horizontal, 7).padding(.vertical, 2)
            .background(selected ? Color.white.opacity(0.22) : gradeTint(score.grade).opacity(0.16), in: Capsule())
            .foregroundStyle(selected ? Color.white : gradeTint(score.grade))
            .help(score.helpText)
            .accessibilityHidden(true)
    }
}

private func gradeTint(_ g: JobScore.Grade) -> Color {
    switch g {
    case .high: .green
    case .medium: .accentColor
    case .low: .secondary
    }
}

/// Opens Pipeline's Run Now sheet prefilled for one advance of the backlog.
/// Start Run is still required; this button spends nothing.
private struct TriageNowButton: View {
    @Environment(AppModel.self) private var model
    @Environment(PipelineModel.self) private var pipeline
    let waiting: Int

    var body: some View {
        Button("Triage now") {
            pipeline.proposeRun(.backlog)
            if pipeline.runSheet?.source == .triageNow { model.pane = .pipeline }
        }
        .buttonStyle(.bordered)
        .controlSize(.small)
        .disabled(pipeline.runBlocker != nil)
        .help(pipeline.runBlocker ?? ManualRunOptions.backlogHelp(
            waiting: waiting,
            kept: pipeline.status?.schedule.kept,
            advancePerRun: pipeline.status?.schedule.unkept?.advancePerRun))
    }
}

/// A state line above the list ("1,186 awaiting triage"), not fine print.
private struct StatePill: View {
    let symbol: String
    let text: String
    let tint: Color

    var body: some View {
        Label(text, systemImage: symbol)
            .font(.caption.weight(.medium))
            .lineLimit(1)
            .padding(.horizontal, 8).padding(.vertical, 3)
            .background(tint.opacity(0.14), in: Capsule())
            .foregroundStyle(tint)
    }
}

extension View {
    /// The rounded secondary card Home uses.
    func cardStyle(padding: CGFloat = 14) -> some View {
        self.padding(padding)
            .background(.background.secondary, in: RoundedRectangle(cornerRadius: 12))
            .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator))
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
        case .pending: "hourglass"
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

func batchTint(_ state: RetriageRowState) -> Color {
    switch state {
    case .queued: .secondary
    case .done(true): .accentColor
    case .done(false): .purple
    case .failed: .orange
    case .notAsked: .secondary
    }
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
struct WrapLayout: Layout {
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
