import Charts
import SwiftUI
import VaniApplyKit

/// Charts over the jobs the list currently selects (view, search and
/// filters): where relevance and freshness fall, how triage read the roles,
/// which skills they ask for and who is hiring. The scores are Vani's
/// display-only ones (`vani jobs`); nothing here changes what the engine keeps.
struct InsightsPanel: View {
    @Environment(JobsModel.self) private var jobs

    var body: some View {
        if jobs.view.isRanked {
            charts
        } else {
            // Vani refuses stats of postings no run has triaged (no fit or score to chart).
            Label("These charts show fit, relevance and priority, which postings awaiting triage do not have yet. Open another view to chart ranked roles.",
                  systemImage: "hourglass")
                .font(.callout)
                .foregroundStyle(.secondary)
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
        }
    }

    private var charts: some View {
        VStack(alignment: .leading, spacing: 8) {
            summary
            if let stats = jobs.stats {
                ScrollView(.horizontal) {
                    HStack(alignment: .top, spacing: 12) {
                        card("Relevance × age", help: "Each dot is a role in the list: up is more relevant, left is newer. Triangles are dated by when Vani first saw them; the board gave no date. Click a dot to open it.") {
                            RelevanceScatter()
                        }
                        card("Role fit", help: "How triage read each role against your target roles. Click a bar to filter by it.") {
                            FitBars(stats: stats)
                        }
                        SkillsToBuildCard()
                        card("Age", help: "How long ago roles were posted. The lighter part is dated by first sight, an upper bound on age.") {
                            AgeBars(stats: stats)
                        }
                        card("Top keywords", help: "Skills and tools the most roles mention, without each company's standard blurb. Green: your target summary names it.") {
                            KeywordBars(stats: stats)
                        }
                        card("Companies", help: "Roles per company; the darker part is roles triage read as a strong fit.") {
                            CompanyBars(stats: stats)
                        }
                        if let pay = stats.salary {
                            card("Pay (\(pay.currency))", help: "Roles by the middle of the yearly pay their posting states, in the filter's currency. Roles stating no pay, or pay only in another currency, are counted below, not charted.") {
                                PayBars(salary: pay)
                            }
                        }
                        if stats.state != nil {
                            card("Where", help: "Roles the board lists as remote, then roles per US state, as the board's location names them (not triage's work-mode reading). Click a bar to filter by it.") {
                                WhereBars(stats: stats)
                            }
                        }
                    }
                    .padding(.bottom, 6)
                }
                .scrollIndicators(.visible)
            } else if let err = jobs.statsError {
                Label {
                    Text(verbatim: err)
                } icon: {
                    Image(systemName: "exclamationmark.triangle")
                }
                .foregroundStyle(.secondary)
            } else {
                ProgressView().controlSize(.small).frame(maxWidth: .infinity, minHeight: 120)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
    }

    @ViewBuilder
    private var summary: some View {
        HStack(spacing: 8) {
            if let s = jobs.stats {
                Text("\(s.total) \(s.total == 1 ? "role" : "roles")").font(.headline)
                Text(verbatim: basisText(s)).foregroundStyle(.secondary)
                if let w = s.insight?.warning {
                    Label {
                        Text(verbatim: w)
                    } icon: {
                        Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.yellow)
                    }
                    .lineLimit(1)
                    .help(w)
                }
            }
            Spacer()
            if !jobs.filter.isEmpty {
                Button("Clear Filters") { jobs.clearFilter() }
                    .controlSize(.small)
            }
        }
        .font(.callout)
    }

    /// Says what relevance rests on, so a sparse rank is not mistaken for a
    /// low one.
    private func basisText(_ s: JobsStats) -> String {
        let rank = s.basis["rank"] ?? 0, fit = s.basis["fit"] ?? 0, none = s.basis["none"] ?? 0
        var parts: [String] = []
        if rank > 0 { parts.append("\(rank) by DevPrism rank") }
        if fit > 0 { parts.append("\(fit) by triage fit") }
        if none > 0 { parts.append("\(none) unread") }
        let dated = s.freshSource["posted"] ?? 0, seen = s.freshSource["seen"] ?? 0
        return "relevance: " + parts.joined(separator: ", ") + " · dated by board \(dated), by first sight \(seen)"
    }

    private func card<C: View>(_ title: String, help: String, @ViewBuilder content: () -> C) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.subheadline.bold())
            content()
                .frame(width: 260, height: 150)
        }
        .padding(8)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 8))
        .help(help)
    }
}

// MARK: - Charts

private struct RelevanceScatter: View {
    @Environment(JobsModel.self) private var jobs
    /// Ages past a year are drawn at the edge, and the axis is square-root
    /// scaled, so the recent weeks spread out while months-old roles bunch up.
    static let maxAge = 365

    private var points: [Job] { jobs.jobs.filter { $0.ageDays != nil && $0.relevance != nil } }

    var body: some View {
        let pts = points
        if pts.isEmpty {
            ContentUnavailableView("No dated roles", systemImage: "circle.dotted")
        } else {
            Chart(pts) { job in
                PointMark(
                    x: .value("Age (days)", min(job.ageDays ?? 0, Self.maxAge)),
                    y: .value("Relevance", job.relevance ?? 0)
                )
                .foregroundStyle(by: .value("Fit", Job.label(for: job.triage?["role_fit"] ?? "none")))
                .symbol(job.freshSource == .seen ? .triangle : .circle)
                .symbolSize(job.key == jobs.selection ? 120 : 28)
                .opacity(jobs.selection == nil || job.key == jobs.selection ? 0.9 : 0.45)
            }
            .chartForegroundStyleScale(domain: fitDomain, range: fitDomain.map { fitColor(label: $0) })
            .chartLegend(.hidden)
            .chartXScale(domain: 0 ... Self.maxAge, type: .squareRoot)
            .chartXAxis { AxisMarks(values: [0, 7, 30, 90, 180, 365]) }
            .chartYScale(domain: 0 ... 100)
            .chartXAxisLabel("days old")
            .chartYAxisLabel("relevance")
            .chartOverlay { proxy in
                GeometryReader { geo in
                    Rectangle().fill(.clear).contentShape(Rectangle())
                        .onTapGesture { loc in select(at: loc, proxy: proxy, geo: geo, among: pts) }
                }
            }
            .accessibilityLabel("Relevance against age for \(pts.count) roles")
        }
    }

    private var fitDomain: [String] { (Job.roleFitOrder + ["none"]).map { Job.label(for: $0) } }

    /// Selects the role whose dot is nearest the click (ChartHit's rule).
    private func select(at loc: CGPoint, proxy: ChartProxy, geo: GeometryProxy, among pts: [Job]) {
        guard let plot = proxy.plotFrame else { return }
        let origin = geo[plot].origin
        let placed: [CGPoint?] = pts.map { j in
            guard let x = proxy.position(forX: min(j.ageDays ?? 0, Self.maxAge)),
                  let y = proxy.position(forY: j.relevance ?? 0) else { return nil }
            return CGPoint(x: x + origin.x, y: y + origin.y)
        }
        if let i = ChartHit.nearest(to: loc, among: placed) { jobs.selection = pts[i].key }
    }
}

private struct FitBars: View {
    @Environment(JobsModel.self) private var jobs
    let stats: JobsStats

    var body: some View {
        let codes = Job.roleFitOrder + ["none"]
        LabeledBars(rows: codes.map { code in
            let on = jobs.filter.fits.isEmpty || jobs.filter.fits.contains(code)
            return BarRow(id: code, label: Job.label(for: code), segments: [(stats.fit[code] ?? 0, fitColor(code: code).opacity(on ? 1 : 0.3))])
        }) { code in
            jobs.filter.fits.formSymmetricDifference([code])
        }
    }
}

/// One row of a labeled bar list: segments stack left to right.
struct BarRow: Identifiable {
    var id: String
    var label: String
    var segments: [(n: Int, color: Color)]
    var total: Int { segments.reduce(0) { $0 + $1.n } }
}

/// Horizontal bars with the label beside each bar and the count after it:
/// legible at card size, and each row is one accessible element. With onTap,
/// rows are buttons.
struct LabeledBars: View {
    let rows: [BarRow]
    var onTap: ((String) -> Void)?

    var body: some View {
        let top = max(rows.map(\.total).max() ?? 0, 1)
        // Rows are HStacks with fixed label and count widths, not GridRows:
        // modifiers on a GridRow reach each cell, which would make three
        // VoiceOver elements per row.
        VStack(alignment: .leading, spacing: 5) {
            ForEach(rows) { row in
                HStack(spacing: 6) {
                    Text(verbatim: row.label)
                        .font(.caption)
                        .lineLimit(1)
                        .frame(width: 104, alignment: .leading)
                    GeometryReader { geo in
                        HStack(spacing: 0) {
                            ForEach(Array(row.segments.enumerated()), id: \.offset) { _, s in
                                Rectangle().fill(s.color)
                                    .frame(width: geo.size.width * CGFloat(s.n) / CGFloat(top))
                            }
                        }
                        .clipShape(RoundedRectangle(cornerRadius: 2))
                    }
                    .frame(height: 9)
                    Text("\(row.total)")
                        .font(.caption2.monospacedDigit())
                        .foregroundStyle(.secondary)
                        .frame(width: 28, alignment: .trailing)
                }
                .contentShape(Rectangle())
                .onTapGesture { onTap?(row.id) }
                .accessibilityElement(children: .ignore)
                .accessibilityLabel("\(row.label): \(row.total)")
                .accessibilityAddTraits(onTap == nil ? [] : .isButton)
                .accessibilityAction { onTap?(row.id) }
            }
        }
        .frame(maxHeight: .infinity, alignment: .top)
    }
}

private struct AgeBars: View {
    let stats: JobsStats

    private struct Row: Identifiable {
        var label: String
        var source: String
        var n: Int
        var id: String { label + source }
    }

    var body: some View {
        let rows = stats.age.flatMap { [Row(label: $0.label, source: "Board date", n: $0.posted),
                                        Row(label: $0.label, source: "First seen", n: $0.seen)] }
        Chart(rows) { r in
            BarMark(x: .value("Age", r.label), y: .value("Roles", r.n))
                .foregroundStyle(by: .value("Dated by", r.source))
        }
        .chartForegroundStyleScale(["Board date": Color.accentColor, "First seen": Color.accentColor.opacity(0.35)])
        .chartLegend(position: .bottom, spacing: 2)
        .accessibilityLabel("Age: " + stats.age.map { "\($0.label) \($0.posted + $0.seen)" }.joined(separator: ", "))
    }
}

/// Stated yearly pay, by the middle of each role's range.
private struct PayBars: View {
    let salary: JobsStats.Salary

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            if salary.listed == 0 {
                ContentUnavailableView("No stated pay", systemImage: "banknote")
            } else {
                Chart(salary.buckets) { b in
                    BarMark(x: .value("Pay", b.label), y: .value("Roles", b.n))
                        .foregroundStyle(Color.teal)
                }
                .accessibilityLabel("Pay: " + salary.buckets.map { "\($0.label) \($0.n)" }.joined(separator: ", "))
            }
            Text(verbatim: caption)
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }

    private var caption: String {
        var parts = ["\(salary.listed) state pay"]
        if salary.listed > 0 { parts[0] += ", median \(Job.Pay.short(salary.median))" }
        if salary.other > 0 { parts.append("\(salary.other) in another currency") }
        parts.append("\(salary.unlisted) none")
        return parts.joined(separator: " · ")
    }
}

private struct KeywordBars: View {
    let stats: JobsStats

    var body: some View {
        let rows = Array(stats.keywords.prefix(8))
        if rows.isEmpty {
            ContentUnavailableView("No keywords", systemImage: "text.magnifyingglass")
        } else {
            LabeledBars(rows: rows.map {
                BarRow(id: $0.term, label: $0.term, segments: [($0.n, $0.target == true ? Color.green : Color.secondary.opacity(0.5))])
            })
        }
    }
}

private struct CompanyBars: View {
    let stats: JobsStats

    var body: some View {
        let top = Array(stats.companies.prefix(8))
        if top.isEmpty {
            ContentUnavailableView("No companies", systemImage: "building.2")
        } else {
            LabeledBars(rows: top.map {
                BarRow(id: $0.company, label: $0.company,
                       segments: [($0.strong, Color.green), ($0.n - $0.strong, Color.secondary.opacity(0.4))])
            })
        }
    }
}

/// Remote first (the darker part has a stated scope), then the states the
/// most roles name, then roles that name no state. Tapping filters.
private struct WhereBars: View {
    @Environment(JobsModel.self) private var jobs
    let stats: JobsStats
    static let remoteID = "\u{0}remote"

    var body: some View {
        let remote = stats.remoteListed ?? 0
        let unstated = stats.remoteScope?["unstated"] ?? 0
        var rows: [BarRow] = []
        if remote > 0 {
            rows.append(BarRow(id: Self.remoteID, label: "Remote",
                               segments: [(remote - unstated, tint(remoteIn)),
                                          (unstated, tint(remoteIn).opacity(0.45))]))
        }
        for s in stats.statesByCount.prefix(remote > 0 ? 5 : 6) {
            rows.append(BarRow(id: s.code, label: s.code, segments: [(s.n, tint(jobs.filter.states.isEmpty || jobs.filter.states.contains(s.code)))]))
        }
        if let none = stats.state?["none"], none > 0 {
            rows.append(BarRow(id: "none", label: "No state listed",
                               segments: [(none, tint(jobs.filter.states.isEmpty || jobs.filter.states.contains("none")).opacity(0.6))]))
        }
        return Group {
            if rows.isEmpty {
                ContentUnavailableView("No locations", systemImage: "mappin.slash")
            } else {
                LabeledBars(rows: rows) { id in
                    if id == Self.remoteID {
                        jobs.filter.setRemoteOnly(!jobs.filter.remoteOnly)
                    } else {
                        jobs.filter.states.formSymmetricDifference([id])
                    }
                }
            }
        }
    }

    private var isClear: Bool { !jobs.filter.remoteOnly && jobs.filter.states.isEmpty }
    /// Remote roles are in the selection: only remote, widened to remote, or no place filter.
    private var remoteIn: Bool { jobs.filter.remoteOnly || jobs.filter.widensToRemote || isClear }

    private func tint(_ on: Bool) -> Color { Color.accentColor.opacity(on ? 1 : 0.3) }
}

// MARK: - Shared tints

/// Tint for a role_fit option value ("none" for no reading).
func fitColor(code: String) -> Color {
    switch code {
    case "strong_fit": .green
    case "partial_fit": .teal
    case "adjacent": .blue
    case "mismatch_seniority": .orange
    case "mismatch_domain": .gray
    default: .secondary
    }
}

private func fitColor(label: String) -> Color {
    let code = (Job.roleFitOrder + ["none"]).first { Job.label(for: $0) == label } ?? "none"
    return fitColor(code: code)
}
