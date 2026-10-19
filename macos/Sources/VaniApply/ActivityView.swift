import SwiftUI
import VaniApplyKit

struct ActivityView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        List(Array(model.activity.enumerated()), id: \.offset) { _, e in
            HStack(spacing: 10) {
                Image(systemName: symbol(e.source)).frame(width: 18).foregroundStyle(.secondary)
                VStack(alignment: .leading, spacing: 2) {
                    Text(title(e))
                    Text(detail(e)).font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                Text(time(e.at)).font(.caption).foregroundStyle(.secondary)
            }
        }
        .overlay {
            if model.activity.isEmpty {
                ContentUnavailableView("No activity yet", systemImage: "clock",
                                       description: Text("Saves, fills and edits from Chrome, Safari and this app appear here. Answer text is never logged."))
            }
        }
        .navigationTitle("Activity")
    }

    private func symbol(_ s: Source) -> String {
        switch s {
        case .chrome: "globe"
        case .safari: "safari"
        case .app: "macwindow"
        }
    }

    private func title(_ e: ActivityEntry) -> String {
        let verb: String = switch e.event {
        case .capture: "Saved answers"
        case .fill: "Filled a form"
        case .edit: "Edited answers"
        case .delete: "Deleted answers"
        case .import: "Imported a bank"
        case .export: "Exported the bank"
        case .settings: "Changed settings"
        case .draft: "Drafted an answer with Claude"
        }
        return e.host.map { "\(verb) on \($0)" } ?? verb
    }

    private func detail(_ e: ActivityEntry) -> String {
        let counts = (e.counts ?? [:]).sorted { $0.key < $1.key }.map { "\($0.key) \($0.value)" }
        return ([e.source.rawValue.capitalized] + counts).joined(separator: " · ")
    }

    /// One parser for every row: building an ISO8601DateFormatter per row on
    /// each render of a 500-row list is the expensive part of drawing it.
    @MainActor private static let isoParser: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()

    private func time(_ iso: String) -> String {
        guard let d = Self.isoParser.date(from: iso) else { return iso }
        return d.formatted(date: .abbreviated, time: .shortened)
    }
}
