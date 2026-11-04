import SwiftUI
import VaniApplyKit

/// The progress board. `compact` is the Home card: the mark, the rail, the
/// outcome bar and the four numbers, without the board list.
struct PipelineBoardView: View {
    @Environment(PipelineModel.self) private var pipeline
    let board: PipelineBoard
    var compact: Bool = false
    /// Stop and Show Log, while this board is standing in for the running banner.
    var showsRunControls: Bool = false

    @State private var problemsOnly: Bool?

    private var problems: Bool { problemsOnly ?? board.problemsDefault }
    private var visibleRows: [PipelineBoard.BoardRow] {
        problems ? board.rows.filter(\.problem) : board.rows
    }

    var body: some View {
        VStack(alignment: .leading, spacing: compact ? 8 : 12) {
            mark
            if board.showsVisuals {
                if !board.steps.isEmpty { rail }
                if !board.stageLine.isEmpty {
                    Text(board.stageLine)
                        .font(compact ? .caption : .callout)
                        .foregroundStyle(.secondary)
                }
                if !compact, board.fraction != nil || !board.placeCaption.isEmpty {
                    fraction
                }
                if !board.segments.isEmpty { outcome }
                if board.showsCounts { tiles }
                if !compact {
                    if let current = board.current { boardLine(current, prominent: true) }
                    if !board.rows.isEmpty || !board.listNote.isEmpty || !board.footer.isEmpty {
                        list
                    }
                    if let ledger = board.ledgerLine {
                        Text(ledger).font(.callout).foregroundStyle(.secondary)
                    }
                    if let note = updatedNote, !note.isEmpty {
                        Text(note).font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
        }
        .padding(compact ? 0 : 12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background { if !compact { RoundedRectangle(cornerRadius: 12).fill(.background.secondary) } }
        .overlay { if !compact { RoundedRectangle(cornerRadius: 12).strokeBorder(.separator) } }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier(showsRunControls ? "pipeline.running" : "pipeline.board")
    }

    private var mark: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: board.mark.symbol)
                .font(compact ? .body : .title3)
                .foregroundStyle(toneColor)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(board.mark.title).font(compact ? .callout.weight(.semibold) : .headline)
                if !board.mark.detail.isEmpty {
                    Text(board.mark.detail)
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            Spacer(minLength: 8)
            if showsRunControls { controls }
        }
        .padding(compact ? 8 : 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(wash, in: RoundedRectangle(cornerRadius: 12))
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder private var controls: some View {
        HStack(spacing: 8) {
            if let log = pipeline.lastLog, pipeline.manualRun != nil {
                Button("Show Log") { NSWorkspace.shared.activateFileViewerSelecting([log]) }
                    .help(log.path)
            }
            if pipeline.manualRun?.isRunning == true {
                Button("Stop", role: .destructive) { pipeline.stopRun() }
                    .help("Cancel this run; what it already wrote to the ledger stays")
            }
        }
        .controlSize(.small)
    }

    private var rail: some View {
        HStack(alignment: .top, spacing: 0) {
            ForEach(board.steps) { step in
                VStack(spacing: 4) {
                    ZStack {
                        if step.spinner {
                            ProgressView().controlSize(.small)
                        } else {
                            Image(systemName: stepSymbol(step))
                                .foregroundStyle(stepColor(step))
                        }
                    }
                    .frame(width: compact ? 14 : 18, height: compact ? 14 : 18)
                    Text(step.title)
                        .font(compact ? .caption2 : .caption)
                        .fontWeight(step.phase == .current || step.phase == .failed ? .semibold : .regular)
                        .foregroundStyle(step.phase == .later ? .tertiary : .primary)
                        .lineLimit(1)
                }
                .frame(maxWidth: .infinity)
                .accessibilityElement(children: .combine)
                .accessibilityLabel(stepAccessibility(step))
            }
        }
    }

    private var fraction: some View {
        VStack(alignment: .leading, spacing: 4) {
            if let fraction = board.fraction {
                ProgressView(value: Double(fraction.done), total: Double(max(fraction.total, 1)))
                    .tint(.accentColor)
                    .accessibilityLabel("Progress")
                    .accessibilityValue(fraction.caption)
            }
            if !board.placeCaption.isEmpty {
                Text(board.placeCaption)
                    .font(.callout)
                    .monospacedDigit()
                    .foregroundStyle(.secondary)
            }
        }
    }

    private var outcome: some View {
        VStack(alignment: .leading, spacing: 6) {
            GeometryReader { geo in
                let sum = max(board.segments.reduce(0) { $0 + $1.count }, 1)
                HStack(spacing: 0) {
                    ForEach(board.segments) { segment in
                        Rectangle()
                            .fill(segmentColor(segment.kind))
                            .frame(width: max(geo.size.width * CGFloat(segment.count) / CGFloat(sum), 0))
                    }
                }
            }
            .frame(height: compact ? 8 : 12)
            .clipShape(RoundedRectangle(cornerRadius: 3))
            .accessibilityHidden(true)
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 10) { legend }
                VStack(alignment: .leading, spacing: 2) { legend }
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel(board.segments.map(\.caption).joined(separator: ", "))
    }

    @ViewBuilder private var legend: some View {
        ForEach(board.segments) { segment in
            HStack(spacing: 4) {
                Circle().fill(segmentColor(segment.kind)).frame(width: 8, height: 8)
                Text(segment.caption).font(.caption2).foregroundStyle(.secondary)
            }
        }
    }

    private var tiles: some View {
        LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: compact ? 6 : 8), count: 4), spacing: compact ? 6 : 8) {
            ForEach(board.counts) { count in
                VStack(alignment: .leading, spacing: compact ? 2 : 4) {
                    HStack(spacing: 4) {
                        Image(systemName: count.symbol)
                            .foregroundStyle(countColor(count.id))
                            .font(compact ? .caption2 : .caption)
                        Text(count.title)
                            .font(compact ? .caption2 : .caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                    countValue(count.value, large: true)
                }
                .padding(compact ? 6 : 10)
                .frame(maxWidth: .infinity, minHeight: compact ? 52 : 72, alignment: .topLeading)
                .background(.quaternary.opacity(0.45), in: RoundedRectangle(cornerRadius: 12))
                .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator))
                .accessibilityElement(children: .combine)
            }
        }
    }

    private var list: some View {
        VStack(alignment: .leading, spacing: 8) {
            if !board.rows.isEmpty {
                HStack(spacing: 8) {
                    chip("All", selected: !problems) { problemsOnly = false }
                    chip("Problems", selected: problems) { problemsOnly = true }
                }
                if visibleRows.isEmpty {
                    Text("No boards in this filter.")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                } else {
                    VStack(alignment: .leading, spacing: 0) {
                        ForEach(visibleRows) { row in
                            boardLine(row, prominent: false)
                            if row.id != visibleRows.last?.id { Divider() }
                        }
                    }
                }
            }
            if !board.listNote.isEmpty {
                Text(board.listNote).font(.callout).foregroundStyle(.secondary)
            }
            if !board.footer.isEmpty {
                Text(board.footer).font(.caption).foregroundStyle(.secondary)
            }
        }
    }

    private func boardLine(_ row: PipelineBoard.BoardRow, prominent: Bool) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Image(systemName: row.symbol)
                .foregroundStyle(row.problem ? Color.red : Color.secondary)
                .frame(width: 16)
            VStack(alignment: .leading, spacing: 2) {
                Text(row.title).fontWeight(prominent ? .semibold : .medium).lineLimit(1)
                if !row.token.isEmpty {
                    Text(row.token).font(.caption.monospaced()).foregroundStyle(.secondary).lineLimit(1)
                }
                if !row.note.isEmpty {
                    Text(row.note).font(.caption).foregroundStyle(.secondary)
                }
            }
            Spacer(minLength: 8)
            if !prominent {
                labeledCount("listed", row.listed)
                labeledCount("added", row.added)
            }
        }
        .padding(.vertical, 4)
        .accessibilityElement(children: .combine)
    }

    private func labeledCount(_ label: String, _ value: PipelineBoard.CountValue) -> some View {
        VStack(alignment: .trailing, spacing: 0) {
            Text(label).font(.caption2).foregroundStyle(.tertiary)
            countValue(value, large: false)
        }
        .frame(minWidth: 44, alignment: .trailing)
    }

    @ViewBuilder private func countValue(_ value: PipelineBoard.CountValue, large: Bool) -> some View {
        switch value {
        case let .number(n):
            Text(HomeOverview.count(n))
                .font(.system(size: large ? (compact ? 18 : 28) : 13, weight: .bold, design: .rounded))
                .monospacedDigit()
                .lineLimit(1)
                .minimumScaleFactor(0.5)
        case .unavailable:
            Label("Not reported", systemImage: "questionmark.circle")
                .font(large ? (compact ? .caption2 : .caption) : .caption2)
                .foregroundStyle(.orange)
                .labelStyle(.titleAndIcon)
                .lineLimit(1)
                .minimumScaleFactor(0.6)
        }
    }

    private func chip(_ title: String, selected: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title)
                .font(.caption.weight(.medium))
                .padding(.horizontal, 10)
                .padding(.vertical, 4)
                .background(selected ? Color.accentColor.opacity(0.18) : Color.clear, in: Capsule())
                .overlay(Capsule().strokeBorder(selected ? Color.accentColor : Color.secondary.opacity(0.45)))
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? [.isButton, .isSelected] : .isButton)
    }

    private var updatedNote: String? {
        guard let at = board.updatedAt else { return nil }
        return PipelineStatus.LastRun.Progress(ok: true, updatedAt: at).updatedText(at: Date())
    }

    private var toneColor: Color {
        switch board.mark.tone {
        case .running: .accentColor
        case .succeeded: .green
        case .cancelled: .orange
        case .failed: .red
        }
    }

    private var wash: Color {
        switch board.mark.tone {
        case .running: Color.accentColor.opacity(0.12)
        case .succeeded: Color.green.opacity(0.12)
        case .cancelled: Color.orange.opacity(0.14)
        case .failed: Color.red.opacity(0.10)
        }
    }

    private func stepSymbol(_ step: PipelineBoard.Step) -> String {
        switch step.phase {
        case .done: "checkmark.circle.fill"
        case .current: "circle.fill"
        case .later: "circle"
        case .failed: "xmark.circle.fill"
        }
    }

    private func stepColor(_ step: PipelineBoard.Step) -> Color {
        switch step.phase {
        case .done: .green
        case .current: .accentColor
        case .later: .secondary.opacity(0.45)
        case .failed: .red
        }
    }

    private func stepAccessibility(_ step: PipelineBoard.Step) -> String {
        switch step.phase {
        case .done: "\(step.title), done"
        case .current: "\(step.title), current"
        case .later: "\(step.title), not yet"
        case .failed: "\(step.title), failed"
        }
    }

    private func segmentColor(_ kind: PipelineBoard.Segment.Kind) -> Color {
        switch kind {
        case .scraped: .accentColor
        case .broken: .red
        case .limited: .orange
        case .skipped: .secondary
        case .left: .indigo
        }
    }

    private func countColor(_ id: String) -> Color {
        switch id {
        case "scraped": .accentColor
        case "left": .indigo
        case "listed": .teal
        case "added": .green
        default: .secondary
        }
    }
}
