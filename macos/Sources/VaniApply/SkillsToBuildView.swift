import SwiftUI
import VaniApplyKit

/// "Skills to build": the skills the roles in the list ask for that you lack,
/// from `vani jobs gaps --json`. Vani ranks them (skills DevPrism found missing
/// first, then keywords your target summary does not name); this only shows
/// them. It never filters or reorders the list: clicking an example role just
/// selects it, as clicking a dot in the scatter does.
struct SkillsToBuildCard: View {
    @Environment(JobsModel.self) private var jobs

    static let width: CGFloat = 340

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Skills to build").font(.subheadline.bold())
            content
                .frame(width: Self.width, height: 150, alignment: .topLeading)
        }
        .padding(8)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 8))
        .help("Skills the roles in this list ask for that you lack. DevPrism-found gaps come first, then skills your target summary does not name. Click a skill to see example roles; click a role to open it. This does not change the list.")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Skills to build")
    }

    @ViewBuilder
    private var content: some View {
        if let g = jobs.gaps {
            GapsList(report: g)
        } else if let err = jobs.gapsError {
            Label {
                Text(verbatim: err)
            } icon: {
                Image(systemName: "exclamationmark.triangle")
            }
            .font(.caption)
            .foregroundStyle(.secondary)
        } else {
            ProgressView().controlSize(.small).frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }
}

private struct GapsList: View {
    let report: JobsGaps
    @State private var expanded: Set<String> = []

    var body: some View {
        ScrollView(.vertical) {
            VStack(alignment: .leading, spacing: 6) {
                Text(verbatim: report.summaryLine)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                if let w = report.insight?.warning {
                    note(w, symbol: "exclamationmark.triangle.fill", tint: .yellow)
                }
                if let t = report.targetNote {
                    note(t, symbol: "info.circle", tint: .secondary)
                }
                if let s = report.statusText {
                    Text(verbatim: s)
                        .font(.caption)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("skills-to-build-status")
                }
                ForEach(report.gaps) { gap in
                    GapRow(gap: gap, open: expanded.contains(gap.term)) {
                        if !expanded.insert(gap.term).inserted { expanded.remove(gap.term) }
                    }
                }
                if let s = report.shownNote {
                    Text(verbatim: s)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .accessibilityIdentifier("skills-to-build-shown")
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .scrollIndicators(.visible)
    }

    private func note(_ text: String, symbol: String, tint: Color) -> some View {
        Label {
            Text(verbatim: text).fixedSize(horizontal: false, vertical: true)
        } icon: {
            Image(systemName: symbol).foregroundStyle(tint)
        }
        .font(.caption2)
        .foregroundStyle(.secondary)
    }
}

private struct GapRow: View {
    @Environment(JobsModel.self) private var jobs
    let gap: JobsGaps.Gap
    let open: Bool
    let toggle: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Button(action: toggle) {
                HStack(alignment: .firstTextBaseline, spacing: 4) {
                    Image(systemName: open ? "chevron.down" : "chevron.right")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .frame(width: 10)
                    VStack(alignment: .leading, spacing: 1) {
                        HStack(spacing: 4) {
                            Text(verbatim: gap.term).font(.caption.bold()).lineLimit(1)
                            if !gap.category.isEmpty {
                                Text(verbatim: gap.category).font(.caption2).foregroundStyle(.secondary).lineLimit(1)
                            }
                            Spacer(minLength: 0)
                            Text(verbatim: gap.reachLabel).font(.caption2.monospacedDigit()).foregroundStyle(.secondary)
                        }
                        Text(verbatim: gap.basisLabel)
                            .font(.caption2)
                            .foregroundStyle(gap.missing > 0 ? Color.orange : Color.secondary)
                            .lineLimit(1)
                    }
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .help(gap.weightHelp)
            .accessibilityLabel("\(gap.term), \(gap.category.isEmpty ? "" : gap.category + ", ")\(gap.reachLabel), \(gap.basisLabel)")
            .accessibilityValue(open ? "expanded" : "collapsed")
            if open {
                ForEach(gap.examples) { ex in
                    Button { jobs.selection = ex.key } label: {
                        Text(verbatim: "\(ex.company): \(ex.title)")
                            .font(.caption2)
                            .lineLimit(1)
                            .truncationMode(.tail)
                            .foregroundStyle(jobs.selection == ex.key ? Color.accentColor : Color.primary)
                            .padding(.leading, 14)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .help("Open this role in the list")
                    .accessibilityLabel("Open \(ex.title) at \(ex.company)")
                }
            }
        }
    }
}
