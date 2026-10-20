import SwiftUI
import VaniApplyKit

/// The landing pane: what is new, what needs doing and whether the daily run is
/// healthy, with every card a way into the pane that owns the detail. All text
/// and ranking come from `HomeOverview` (VaniApplyKit, tested); this file lays
/// them out. A source that is still loading, or could not be read, has its own
/// look on every card: it never reads as zero or as fine.
struct HomeView: View {
    @Binding var section: Pane?
    @Environment(AppModel.self) private var model
    @Environment(JobsModel.self) private var jobs
    @Environment(PipelineModel.self) private var pipeline
    @State private var savingKey: String?

    private static let topRoleCount = 5
    private static let activityCount = 6

    var body: some View {
        let now = Date()
        let jobsReading = self.jobsReading
        let pipelineReading = self.pipelineReading
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                Header(now: now, jobs: jobsReading, pipeline: pipelineReading, refreshing: jobs.loading || pipeline.loading, refresh: refresh)
                SetupCard(items: setupItems(pipelineReading)) { go($0) }
                // Four tiles share one full-width row from the 820 pt minimum window up;
                // pipeline health has its own card below, so it is not repeated as a tile.
                LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 12), count: 4), spacing: 12) {
                    jobTile(.new, "New roles", "sparkles", .blue, jobsReading) { $0.new }
                    jobTile(.saved, "Saved", "bookmark.fill", .orange, jobsReading) { $0.saved }
                    jobTile(.applied, "Applied", "paperplane.fill", .green, jobsReading) { $0.applied }
                    answersTile
                }
                TopRolesCard(jobs: jobs, savingKey: savingKey, save: save, open: open, showNew: { jobs.view = .new }, goJobs: { section = .jobs }, count: Self.topRoleCount)
                // Side by side, splitting the width, when there is room; stacked otherwise.
                ViewThatFits(in: .horizontal) {
                    HStack(alignment: .top, spacing: 12) { pipelineCard(pipelineReading, now: now); activityCard }
                        .frame(minWidth: 640)
                    VStack(spacing: 12) { pipelineCard(pipelineReading, now: now); activityCard }
                }
            }
            .paneColumn()
        }
        .background(.background)
        .navigationTitle("Home")
        .task {
            if jobs.page == nil, !jobs.loading { await jobs.refresh() }
        }
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
            jobs.reloadAfterActivation()
        }
    }

    // MARK: - Readings

    private var jobsReading: Reading<JobCounts> {
        if let e = jobs.error { return .failed(e) }
        if let p = jobs.page { return .ready(JobCounts(page: p)) }
        return .pending
    }

    private var pipelineReading: Reading<PipelineStatus> {
        if let e = pipeline.error { return .failed(e) }
        if let s = pipeline.status { return .ready(s) }
        return .pending
    }

    private func setupItems(_ reading: Reading<PipelineStatus>) -> [HomeOverview.SetupItem] {
        HomeOverview.setupItems(.init(
            storeError: model.storeError,
            answerCount: model.answers.count,
            pipeline: reading,
            browsers: HomeOverview.browserLink(chrome: Array(model.chromeStatus.values), safariEnabled: model.safariEnabled),
            // An agent running another copy's `vani` is not this app's agent.
            agentEnabled: model.agentStatus == .enabled && model.agentError == nil))
    }

    // MARK: - Actions

    private func go(_ target: HomeTarget) {
        section = switch target {
        case .jobs: .jobs
        case .pipeline: .pipeline
        case .answers: .answers
        case .activity: .activity
        case .browsers: .browsers
        case .settings: .settings
        }
    }

    private func refresh() {
        jobs.reload()
        Task { await pipeline.refresh() }
        model.reload()
    }

    private func save(_ job: Job) {
        guard savingKey == nil else { return }
        savingKey = job.key
        Task {
            await jobs.setMark(job.key, .saved)
            savingKey = nil
        }
    }

    /// Jobs lists New by default and clears its selection when the view
    /// changes, so the view is set first and the selection after.
    private func open(_ job: Job) {
        if jobs.view != .new { jobs.view = .new }
        jobs.selection = job.key
        section = .jobs
    }

    // MARK: - Tiles

    private func jobTile(_ view: JobView, _ title: String, _ symbol: String, _ tint: Color,
                         _ reading: Reading<JobCounts>, _ pick: (JobCounts) -> Int) -> some View {
        let value: Tile.Value
        var note: String?
        switch reading {
        case .pending: value = .loading
        case .failed: value = .unavailable("Could not read")
        case let .ready(c):
            value = .number(HomeOverview.count(pick(c)))
            if c.torn { note = "Ledger was mid-write" }
        }
        return Tile(title: title, symbol: symbol, tint: tint, value: value, note: note) {
            if jobs.view != view { jobs.view = view }
            section = .jobs
        }
    }

    private var answersTile: some View {
        let value: Tile.Value = model.storeError != nil ? .unavailable("Bank unreadable") : .number(HomeOverview.count(model.answers.count))
        return Tile(title: "Answers", symbol: "text.bubble.fill", tint: .purple, value: value,
                    note: model.storeError == nil && model.answers.isEmpty ? "Nothing saved yet" : nil) { section = .answers }
    }

    // MARK: - Cards

    private func pipelineCard(_ reading: Reading<PipelineStatus>, now: Date) -> some View {
        PipelineCard(reading: reading, error: pipeline.error, checkedAt: pipeline.checkedAt, now: now,
                     refresh: { Task { await pipeline.refresh() } }, open: { section = .pipeline })
    }

    private var activityCard: some View {
        ActivityCard(rows: HomeOverview.recentActivity(model.activity, limit: Self.activityCount),
                     storeError: model.storeError, open: { section = .activity })
    }
}

extension PipelineHealth {
    /// The tint Home and Pipeline show this health in.
    var tint: Color {
        switch self {
        case .healthy: .green
        case .warning: .orange
        case .problem: .red
        }
    }
}

// MARK: - Header

private struct Header: View {
    let now: Date
    let jobs: Reading<JobCounts>
    let pipeline: Reading<PipelineStatus>
    let refreshing: Bool
    let refresh: () -> Void

    var body: some View {
        HStack(alignment: .top) {
            VStack(alignment: .leading, spacing: 4) {
                Text(now, format: .dateTime.weekday(.wide).month(.wide).day())
                    .font(.subheadline).foregroundStyle(.secondary).textCase(.uppercase)
                Text(HomeOverview.greeting(hour: Calendar.current.component(.hour, from: now)))
                    .font(.largeTitle.weight(.bold))
                Text(HomeOverview.statusLine(jobs: jobs, pipeline: pipeline, now: now, timeText: Self.timeText(now: now)))
                    .font(.title3).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 12)
            Button(action: refresh) {
                if refreshing { ProgressView().controlSize(.small) } else { Label("Refresh", systemImage: "arrow.clockwise") }
            }
            .disabled(refreshing)
            .help("Read roles, pipeline status and answers again")
        }
    }

    /// Today's times as "6:02 AM"; earlier ones with their date.
    static func timeText(now: Date) -> (Date) -> String {
        { d in
            Calendar.current.isDate(d, inSameDayAs: now)
                ? d.formatted(date: .omitted, time: .shortened)
                : d.formatted(date: .abbreviated, time: .shortened)
        }
    }
}

// MARK: - Card and tile chrome

private struct Card<Content: View, Trailing: View>: View {
    let title: String
    let symbol: String
    @ViewBuilder var trailing: Trailing
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Label(title, systemImage: symbol).font(.headline)
                Spacer(minLength: 8)
                trailing
            }
            content
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator))
    }
}

private struct Tile: View {
    enum Value {
        case loading
        case unavailable(String)
        case number(String)
        case text(String)
    }

    let title: String
    let symbol: String
    let tint: Color
    let value: Value
    var note: String?
    let action: () -> Void
    @State private var hovering = false

    var body: some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: 8) {
                HStack {
                    Image(systemName: symbol).foregroundStyle(tint).font(.title3)
                    Text(title).font(.subheadline.weight(.medium)).foregroundStyle(.secondary)
                    Spacer(minLength: 0)
                    Image(systemName: "chevron.right").font(.caption).foregroundStyle(.tertiary)
                }
                valueView
                Text(note ?? " ").font(.caption).foregroundStyle(.secondary).lineLimit(2)
                    .opacity(note == nil ? 0 : 1)
            }
            .padding(14)
            .frame(maxWidth: .infinity, minHeight: 112, alignment: .topLeading)
            .background(.background.secondary, in: RoundedRectangle(cornerRadius: 12))
            .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(hovering ? AnyShapeStyle(tint.opacity(0.6)) : AnyShapeStyle(.separator)))
            .contentShape(RoundedRectangle(cornerRadius: 12))
        }
        .buttonStyle(.plain)
        .onHover { hovering = $0 }
        .accessibilityElement(children: .combine)
        .accessibilityHint("Opens \(title)")
    }

    @ViewBuilder private var valueView: some View {
        switch value {
        case .loading:
            ProgressView().controlSize(.small).frame(height: 34, alignment: .leading)
        case let .unavailable(why):
            Label(why, systemImage: "exclamationmark.triangle.fill")
                .font(.headline).foregroundStyle(.orange).frame(height: 34, alignment: .leading)
        case let .number(n):
            Text(n).font(.system(size: 32, weight: .bold, design: .rounded)).monospacedDigit()
                .lineLimit(1).minimumScaleFactor(0.5)
        case let .text(t):
            Text(t).font(.system(size: 24, weight: .bold, design: .rounded)).foregroundStyle(tint)
                .lineLimit(1).minimumScaleFactor(0.6).frame(height: 34, alignment: .leading)
        }
    }
}

/// A line under a card's title that says why there is nothing to show.
private struct Placeholder: View {
    let symbol: String
    let text: String
    var tint: Color = .secondary

    var body: some View {
        Label(text, systemImage: symbol).foregroundStyle(tint).font(.callout)
            .frame(maxWidth: .infinity, alignment: .leading).padding(.vertical, 6)
    }
}

// MARK: - Setup

private struct SetupCard: View {
    let items: [HomeOverview.SetupItem]
    let go: (HomeTarget) -> Void

    private var open: [HomeOverview.SetupItem] {
        let rank: (HomeOverview.SetupItem.State) -> Int = { s in
            switch s {
            case .problem: 0
            case .todo: 1
            case .unknown: 2
            case .done: 3
            }
        }
        return items.filter { $0.state != .done }.sorted { rank($0.state) < rank($1.state) }
    }

    var body: some View {
        let open = self.open
        if !open.isEmpty {
            Card(title: "Needs your attention", symbol: "checklist", trailing: {
                Text("\(items.count - open.count) of \(items.count) ready").font(.caption).foregroundStyle(.secondary)
            }, content: {
                VStack(spacing: 0) {
                    ForEach(Array(open.enumerated()), id: \.element.id) { i, item in
                        if i > 0 { Divider() }
                        Button { go(item.target) } label: {
                            HStack(spacing: 10) {
                                Image(systemName: symbol(item.state)).foregroundStyle(color(item.state)).frame(width: 20)
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(item.title).fontWeight(.medium)
                                    Text(item.detail).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                                }
                                Spacer(minLength: 8)
                                Text(actionTitle(item)).font(.callout).foregroundStyle(.tint)
                                Image(systemName: "chevron.right").font(.caption).foregroundStyle(.tertiary)
                            }
                            .padding(.vertical, 8)
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                    }
                }
            })
        }
    }

    private func symbol(_ s: HomeOverview.SetupItem.State) -> String {
        switch s {
        case .done: "checkmark.circle.fill"
        case .todo: "circle.dashed"
        case .problem: "xmark.octagon.fill"
        case .unknown: "questionmark.circle.fill"
        }
    }

    private func color(_ s: HomeOverview.SetupItem.State) -> Color {
        switch s {
        case .done: .green
        case .todo: .blue
        case .problem: .red
        case .unknown: .orange
        }
    }

    private func actionTitle(_ item: HomeOverview.SetupItem) -> String {
        switch item.state {
        case .todo: "Set up"
        case .problem: "Fix"
        case .unknown: "Check"
        case .done: ""
        }
    }
}

// MARK: - Top roles

private struct TopRolesCard: View {
    let jobs: JobsModel
    let savingKey: String?
    let save: (Job) -> Void
    let open: (Job) -> Void
    let showNew: () -> Void
    let goJobs: () -> Void
    let count: Int

    var body: some View {
        Card(title: "Top new roles", symbol: "sparkles", trailing: {
            Button("All in Jobs", action: goJobs).buttonStyle(.link)
        }, content: { content })
    }

    @ViewBuilder private var content: some View {
        if let err = jobs.error {
            Placeholder(symbol: "exclamationmark.triangle.fill", text: "Could not load roles: \(HomeOverview.oneLine(err, max: 160))", tint: .orange)
            Button("Try again") { jobs.reload() }
        } else if jobs.page == nil {
            HStack(spacing: 8) { ProgressView().controlSize(.small); Text("Loading roles…").foregroundStyle(.secondary) }
        } else if jobs.view != .new {
            Placeholder(symbol: "line.3.horizontal.decrease.circle", text: "Jobs is showing “\(jobs.view.title)”, so the new roles are not loaded.")
            Button("Show new roles", action: showNew)
        } else {
            let top = HomeOverview.topRoles(jobs.jobs, limit: count)
            if top.isEmpty {
                empty
            } else {
                VStack(spacing: 0) {
                    ForEach(Array(top.enumerated()), id: \.element.key) { i, job in
                        if i > 0 { Divider() }
                        RoleRow(job: job, saving: savingKey == job.key, busy: savingKey != nil, save: { save(job) }, open: { open(job) })
                    }
                }
                if !jobs.filter.isEmpty || !jobs.query.isEmpty {
                    Text("Filtered by what Jobs has set.").font(.caption).foregroundStyle(.secondary)
                }
            }
        }
    }

    @ViewBuilder private var empty: some View {
        let hidden = (jobs.page?.count(.new) ?? 0) > 0 && (!jobs.filter.isEmpty || !jobs.query.isEmpty)
        if hidden {
            Placeholder(symbol: "line.3.horizontal.decrease.circle", text: "New roles exist, but the search or filters set in Jobs hide them.")
            Button("Open Jobs", action: goJobs)
        } else if (jobs.page?.count(.new) ?? 0) > 0 {
            Placeholder(symbol: "briefcase", text: "None of the new roles is open and unmarked.")
        } else {
            Placeholder(symbol: "tray", text: "No new roles right now." + (jobs.page?.pending?.footerNote.map { " \($0)." } ?? ""))
        }
    }
}

private struct RoleRow: View {
    let job: Job
    let saving: Bool
    let busy: Bool
    let save: () -> Void
    let open: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            VStack(alignment: .leading, spacing: 3) {
                Text(job.title).fontWeight(.semibold).lineLimit(2)
                HStack(spacing: 6) {
                    Text(job.company).foregroundStyle(.secondary)
                    if let w = job.whereLabel { Text("·").foregroundStyle(.tertiary); Text(w).foregroundStyle(.secondary).lineLimit(1) }
                    if let age = job.ageLabel { Text("·").foregroundStyle(.tertiary); Text(age).foregroundStyle(.secondary) }
                }
                .font(.callout)
                if let note = HomeOverview.fitNote(job) {
                    Text(note).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                }
            }
            Spacer(minLength: 8)
            if let r = job.priority ?? job.relevance, r.isFinite {
                Text("\(Int(min(max(r, 0), 100).rounded()))")
                    .font(.caption.weight(.semibold)).monospacedDigit()
                    .padding(.horizontal, 7).padding(.vertical, 3)
                    .background(.tint.opacity(0.15), in: Capsule())
                    .help(job.priority != nil ? "Priority: relevance discounted by age" : "Relevance to your target summary")
            }
            Button(action: save) {
                if saving { ProgressView().controlSize(.small) } else { Label("Save", systemImage: "bookmark") }
            }
            .disabled(busy)
            .help("Save this role")
            Button(action: open) { Label("Open", systemImage: "arrow.right.circle") }
                .help("Open in Jobs")
        }
        .labelStyle(.titleAndIcon)
        .controlSize(.small)
        .padding(.vertical, 8)
    }
}

// MARK: - Pipeline

private struct PipelineCard: View {
    let reading: Reading<PipelineStatus>
    let error: String?
    let checkedAt: Date?
    let now: Date
    let refresh: () -> Void
    let open: () -> Void

    var body: some View {
        Card(title: "Pipeline", symbol: "waveform.path.ecg", trailing: {
            Button("Open Pipeline", action: open).buttonStyle(.link)
        }, content: {
            switch reading {
            case .pending:
                HStack(spacing: 8) { ProgressView().controlSize(.small); Text("Reading status…").foregroundStyle(.secondary) }
            case let .failed(why):
                Placeholder(symbol: "xmark.octagon.fill", text: "vani could not report its status: \(HomeOverview.oneLine(why, max: 200))", tint: .red)
                Button("Try again", action: refresh)
            case let .ready(s):
                let health = s.health(at: now)
                let issues = s.issues(at: now)
                HStack(spacing: 8) {
                    Image(systemName: HomeOverview.healthSymbol(health)).foregroundStyle(health.tint).font(.title2)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(HomeOverview.healthTitle(health)).font(.title3.weight(.semibold))
                        Text(HomeOverview.runHeadline(s.lastRun, now: now)).font(.callout).foregroundStyle(.secondary)
                    }
                }
                ForEach(Array(issues.prefix(3).enumerated()), id: \.offset) { _, issue in
                    HStack(alignment: .top, spacing: 6) {
                        Image(systemName: issue.severity == .problem ? "xmark.circle.fill" : "exclamationmark.triangle.fill")
                            .foregroundStyle(issue.severity == .problem ? .red : .orange)
                        Text(issue.title).font(.callout)
                    }
                }
                if issues.count > 3 {
                    Text("and \(issues.count - 3) more").font(.caption).foregroundStyle(.secondary)
                }
                if let next = s.schedule.nextRun {
                    Label("Next run \(next.formatted(date: .abbreviated, time: .shortened))", systemImage: "calendar")
                        .font(.caption).foregroundStyle(.secondary)
                } else if !s.schedule.ok {
                    Label("Next run unknown: the schedule could not be read", systemImage: "calendar.badge.exclamationmark")
                        .font(.caption).foregroundStyle(.orange)
                }
                if let checkedAt {
                    Text("Checked \(checkedAt.formatted(date: .omitted, time: .shortened))").font(.caption2).foregroundStyle(.tertiary)
                }
            }
        })
    }
}

// MARK: - Activity

private struct ActivityCard: View {
    let rows: [HomeOverview.ActivityRow]
    let storeError: String?
    let open: () -> Void

    var body: some View {
        Card(title: "Recent activity", symbol: "clock.arrow.circlepath", trailing: {
            Button("See all", action: open).buttonStyle(.link)
        }, content: {
            if rows.isEmpty {
                if storeError != nil {
                    Placeholder(symbol: "exclamationmark.triangle.fill", text: "The activity log is unavailable: the answer bank could not be read.", tint: .orange)
                } else {
                    Placeholder(symbol: "clock", text: "No activity yet. Saves, fills and edits from your browsers and this app appear here.")
                }
            } else {
                VStack(spacing: 0) {
                    ForEach(rows) { row in
                        if row.id > 0 { Divider() }
                        HStack(spacing: 10) {
                            Image(systemName: row.symbol).frame(width: 18).foregroundStyle(.secondary)
                            VStack(alignment: .leading, spacing: 2) {
                                Text(row.title).lineLimit(1)
                                Text(row.detail).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                            }
                            Spacer(minLength: 8)
                            if let at = row.at {
                                Text(at, style: .relative).font(.caption).foregroundStyle(.secondary).monospacedDigit()
                            } else {
                                Text("time unknown").font(.caption).foregroundStyle(.tertiary)
                            }
                        }
                        .padding(.vertical, 6)
                    }
                }
            }
        })
    }
}
