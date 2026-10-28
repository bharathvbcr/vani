import SwiftUI
import VaniApplyKit

/// Answers that ask the same question, or give the same answer, with a preview of what
/// one merged row would hold. Nothing is written until "Merge selected" is confirmed.
struct DuplicatesSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    private struct Choice {
        var selected: Bool
        var survivor: String
        var valueFrom: String
        var allowCrossKind = false
    }

    @State private var clusters: [Dedup.Cluster] = []
    @State private var choices: [String: Choice] = [:]
    @State private var semantic: SimilarResult?
    @State private var confirming = false

    private func choice(_ c: Dedup.Cluster) -> Choice {
        choices[c.id] ?? Choice(selected: false, survivor: c.survivor, valueFrom: c.preselectedValueKey)
    }

    private func plan(_ c: Dedup.Cluster) -> Dedup.MergePlan {
        let ch = choice(c)
        return Dedup.MergePlan(survivor: ch.survivor, merged: c.keys.filter { $0 != ch.survivor },
                               valueFrom: ch.valueFrom, allowCrossKind: ch.allowCrossKind)
    }

    private func outcome(_ c: Dedup.Cluster) -> Result<Dedup.Merged, Error> {
        Result { try Dedup.merged(bank: model.snapshot.bank, plan: plan(c)) }
    }

    private var chosen: [Dedup.Cluster] {
        clusters.filter { c in
            guard choice(c).selected, case .success = outcome(c) else { return false }
            return true
        }
    }

    private func load(keeping: Bool) {
        clusters = model.duplicateClusters(similar: semantic)
        var next: [String: Choice] = [:]
        for c in clusters {
            next[c.id] = (keeping ? choices[c.id] : nil)
                ?? Choice(selected: c.defaultSelected && c.blocked == nil, survivor: c.survivor, valueFrom: c.preselectedValueKey)
        }
        choices = next
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Find duplicates").font(.headline)
            Text("Rows that ask the same question are merged into one. The merged row keeps every wording, so forms still match.")
                .font(.callout).foregroundStyle(.secondary)
            if clusters.isEmpty {
                ContentUnavailableView("No duplicates found", systemImage: "checkmark.circle",
                                       description: Text("No two answers share an intent, a concept or an answer."))
                    .frame(maxWidth: .infinity, minHeight: 200)
            } else {
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 10) {
                        ForEach(clusters) { clusterRow($0) }
                    }
                    .padding(.vertical, 2)
                }
                .frame(minHeight: 280)
            }
            Text(footer).font(.caption).foregroundStyle(.secondary)
            HStack {
                Spacer()
                Button("Close", role: .cancel) { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Merge selected (\(chosen.count))") { confirming = true }
                    .keyboardShortcut(.defaultAction)
                    .disabled(chosen.isEmpty || model.storeError != nil)
            }
        }
        .padding(20)
        .frame(width: 680, height: 560)
        .confirmationDialog("Merge \(chosen.count) group\(chosen.count == 1 ? "" : "s")?", isPresented: $confirming) {
            Button("Merge", role: .destructive) {
                let plans = chosen.map(plan)
                Task { if await model.merge(plans) { load(keeping: false) } }
            }
        } message: {
            let rows = chosen.reduce(0) { $0 + $1.keys.count - 1 }
            Text("\(rows) row\(rows == 1 ? "" : "s") will be folded into the rows you picked and removed. Their questions stay as wordings.")
        }
        .onAppear { load(keeping: false) }
        .task {
            // The deterministic groups are already on screen; the embedding pass is bounded and may add more.
            let found = await model.findSimilarWordings()
            semantic = found
            load(keeping: true)
        }
    }

    private var footer: String {
        switch semantic {
        case nil: "Similar-wording grouping: checking…"
        case let .available(_, model)?: "Similar-wording grouping: \(model ?? "available")"
        case let .unavailable(reason)?: "Similar-wording grouping unavailable: \(reason)"
        }
    }

    @ViewBuilder
    private func clusterRow(_ c: Dedup.Cluster) -> some View {
        let ch = choice(c)
        let result = outcome(c)
        let blockedReason: String? = { if case let .failure(e) = result { String(describing: e) } else { nil } }()
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline) {
                Toggle("", isOn: Binding(get: { ch.selected }, set: { v in mutate(c) { $0.selected = v } }))
                    .labelsHidden()
                    .disabled(blockedReason != nil)
                VStack(alignment: .leading, spacing: 2) {
                    Text(c.keys.joined(separator: "  ·  ")).font(.body)
                    Text(c.reasons.joined(separator: "; ")).font(.caption).foregroundStyle(.secondary)
                }
            }
            if let blockedReason {
                Label(blockedReason, systemImage: "hand.raised").font(.caption).foregroundStyle(.red)
            }
            if c.crossKind {
                Toggle("Allow merging choice answers with text answers", isOn: Binding(
                    get: { ch.allowCrossKind }, set: { v in mutate(c) { $0.allowCrossKind = v } }))
                    .font(.caption)
            }
            HStack {
                Picker("Keep row", selection: Binding(get: { ch.survivor }, set: { v in mutate(c) { $0.survivor = v } })) {
                    ForEach(c.keys, id: \.self) { Text($0).tag($0) }
                }
                Picker("Answer", selection: Binding(get: { ch.valueFrom }, set: { v in mutate(c) { $0.valueFrom = v } })) {
                    ForEach(c.valueChoices, id: \.fromKey) { v in
                        Text(valueLabel(v)).tag(v.fromKey)
                    }
                }
            }
            .pickerStyle(.menu)
            if case let .success(m) = result {
                Text(preview(m)).font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(10)
        .background(.quaternary.opacity(0.4), in: .rect(cornerRadius: 8))
    }

    private func mutate(_ c: Dedup.Cluster, _ f: (inout Choice) -> Void) {
        var ch = choice(c)
        f(&ch)
        choices[c.id] = ch
    }

    private func valueLabel(_ v: Dedup.ValueChoice) -> String {
        let hidden = model.snapshot.bank.answers[v.fromKey]?.sensitivity == .selfID
        let text = hidden ? "•••••• self-identification" : v.value.joined(separator: ", ")
        let clipped = text.count > 60 ? String(text.prefix(60)) + "…" : text
        return (v.preselected ? "Newest: " : "") + clipped + "  (from \(v.fromKey))"
    }

    private func preview(_ m: Dedup.Merged) -> String {
        let a = m.answer
        var parts = ["Result: “\(a.key)”, \(a.labels.count) wording\(a.labels.count == 1 ? "" : "s")",
                     "saved \(a.uses)×", "filled \(a.fills)×"]
        if m.droppedLabels > 0 { parts.append("\(m.droppedLabels) older wording\(m.droppedLabels == 1 ? "" : "s") over the \(Limits.maxLabels) limit dropped") }
        if a.review { parts.append("draft") }
        return parts.joined(separator: " · ")
    }
}
