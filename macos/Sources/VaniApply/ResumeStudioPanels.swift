import AppKit
import SwiftUI
import VaniApplyKit

// The Studio window's right pane (the page, the coverage, the versions) and the Redraft sheet. Logic is
// `ResumeStudioEditor` and the pure types in VaniApplyKit (`ResumeCoveragePresentation`, `ResumeDiffPresentation`,
// `ResumeVersions`, `ResumeRedraftForm`); this only draws it. `flat` draws without a scroll view (the view smoke
// test: `ImageRenderer` draws nothing inside one) and with every hit expanded.

struct ResumeRightPane: View {
    @Bindable var editor: ResumeStudioEditor
    var flat = false

    var body: some View {
        VStack(spacing: 0) {
            Picker("Pane", selection: $editor.rightPane) {
                ForEach(ResumeStudioEditor.RightPane.allCases) { Text($0.rawValue).tag($0) }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .padding(8)
            .help("The page as drawn, how well the version covers the posting's keywords, or the saved versions and what changed between them.")
            .accessibilityIdentifier("studio.rightPane")
            Divider()
            switch editor.rightPane {
            case .page: ResumePreviewPane(editor: editor, flat: flat)
            case .coverage: ResumeCoveragePanel(editor: editor, flat: flat)
            case .versions: ResumeVersionsPanel(editor: editor, flat: flat)
            }
        }
    }
}

// MARK: - Coverage

struct ResumeCoveragePanel: View {
    let editor: ResumeStudioEditor
    var flat = false
    @State private var expanded: Set<String> = []

    var body: some View {
        Group {
            if flat { content } else { ScrollView { content } }
        }
        .background(.background)
        // Measures when the pane is shown and again when another version is: one pool read, no model.
        .task(id: editor.shownVersion ?? 0) { await editor.loadCoverage() }
        .accessibilityIdentifier("studio.coverage")
    }

    private var content: some View {
        VStack(alignment: .leading, spacing: 12) {
            switch editor.coverage {
            case .idle, .loading:
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text("Measuring which keywords this version covers…").font(.callout).foregroundStyle(.secondary)
                }
                .accessibilityIdentifier("studio.coverage.loading")
            case let .failed(f):
                failure(f)
            case .loaded:
                if let view = editor.coverageView { loaded(view) }
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .topLeading)
    }

    @ViewBuilder
    private func failure(_ f: ResumeFailure) -> some View {
        if let text = ResumeWords.failure(f) {
            VStack(alignment: .leading, spacing: 6) {
                Label { Text(verbatim: text.message).fixedSize(horizontal: false, vertical: true) } icon: {
                    Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                }
                Text("No coverage is shown: a measurement that did not run is not a pass.").font(.caption).foregroundStyle(.secondary)
                Button("Try again") { Task { await editor.loadCoverage(force: true) } }
            }
            .accessibilityIdentifier("studio.coverage.error")
        }
    }

    @ViewBuilder
    private func loaded(_ v: ResumeCoverageView) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline) {
                Text(verbatim: v.headline).font(.headline).fixedSize(horizontal: false, vertical: true)
                Spacer()
                Button { Task { await editor.loadCoverage(force: true) } } label: { Image(systemName: "arrow.clockwise") }
                    .buttonStyle(.borderless)
                    .help("Measure again.")
                    .accessibilityIdentifier("studio.coverage.refresh")
            }
            Text(verbatim: v.scopeLine).font(.caption).foregroundStyle(.secondary)
            Text(verbatim: v.sourceLine).font(.caption).foregroundStyle(.secondary)
            ForEach(v.notes, id: \.self) { n in
                Label { Text(verbatim: n).fixedSize(horizontal: false, vertical: true) } icon: { Image(systemName: "info.circle") }
                    .font(.caption).foregroundStyle(.orange)
            }
        }
        if !v.hits.isEmpty {
            group("Covered", systemImage: "checkmark.circle.fill", tint: .green) {
                ForEach(v.hits) { hit in hitRow(hit) }
            }
        }
        if !v.swappable.isEmpty {
            group("Missed, and a swap could fix it", systemImage: "arrow.left.arrow.right.circle", tint: .orange,
                  note: "Your knowledge base has an eligible bullet that says these. Swap one in from the outline.") {
                terms(v.swappable)
            }
        }
        if !v.uncovered.isEmpty {
            group("Missed, and no eligible bullet says it", systemImage: "xmark.circle.fill", tint: .red,
                  note: "Your knowledge base lacks these, so no swap can fix them.") {
                terms(v.uncovered)
            }
        }
        if !v.unjudged.isEmpty {
            group("Could not be matched", systemImage: "questionmark.circle", tint: .gray,
                  note: "Phrases the matcher cannot hold. They are not counted as hits or misses.") {
                terms(v.unjudged)
            }
        }
        if !v.unresolved.isEmpty {
            group("Chosen bullets no longer offered", systemImage: "exclamationmark.triangle.fill", tint: .orange,
                  note: "These cover nothing here (the knowledge base changed). Revalidate the version.") {
                terms(v.unresolved)
            }
        }
        if !v.gapMissing.isEmpty {
            group("Flagged missing by DevPrism", systemImage: "flag", tint: .secondary) { terms(v.gapMissing) }
        }
        Divider()
        HandOffBlock(editor: editor)
    }

    private func group<Content: View>(_ title: String, systemImage: String, tint: Color, note: String? = nil,
                                      @ViewBuilder _ body: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Label(title, systemImage: systemImage).font(.subheadline.weight(.semibold)).foregroundStyle(tint)
            if let note { Text(note).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true) }
            body()
        }
    }

    private func terms(_ list: [String]) -> some View {
        WrapLayout(spacing: 6) {
            ForEach(list, id: \.self) { Chip(text: $0, tint: .secondary) }
        }
    }

    private func hitRow(_ hit: ResumeCoverageHitRow) -> some View {
        DisclosureGroup(isExpanded: Binding(get: { flat || expanded.contains(hit.term) },
                                            set: { if $0 { expanded.insert(hit.term) } else { expanded.remove(hit.term) } })) {
            VStack(alignment: .leading, spacing: 4) {
                ForEach(hit.bullets) { b in
                    Button { editor.select(bullet: b.id) } label: {
                        Text(verbatim: b.text ?? "\(b.id) (no longer offered by your knowledge base)")
                            .font(.callout).multilineTextAlignment(.leading).fixedSize(horizontal: false, vertical: true)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .buttonStyle(.plain)
                    .help("Show this bullet in the outline.")
                    .accessibilityIdentifier("studio.coverage.bullet.\(b.id)")
                }
                if hit.count > hit.bullets.count {
                    Text("and \(hit.count - hit.bullets.count) more bullets").font(.caption).foregroundStyle(.secondary)
                }
            }
            .padding(.leading, 4)
        } label: {
            HStack {
                Text(verbatim: hit.term).font(.callout.weight(.medium))
                Text("\(hit.count) bullet\(hit.count == 1 ? "" : "s")").font(.caption).foregroundStyle(.secondary)
            }
        }
        .accessibilityIdentifier("studio.coverage.hit.\(hit.term)")
    }
}

/// "Send uncovered terms to the career session": which terms first (a dry run), written only on confirmation.
private struct HandOffBlock: View {
    let editor: ResumeStudioEditor

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            switch editor.handOff {
            case .idle:
                Button("Send uncovered terms to the career session…") { Task { await editor.startHandOff() } }
                    .help("Shows which terms would go first. Nothing is written until you confirm.")
                    .accessibilityIdentifier("studio.handoff")
                Text("The terms your knowledge base has no bullet for, so your career session can work them into the bank.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            case .asking:
                HStack(spacing: 8) { ProgressView().controlSize(.small); Text("Asking Vani which terms…").font(.callout) }
            case let .confirming(g):
                if g.terms.isEmpty {
                    Text(verbatim: ResumeWords.handOff(g)).font(.callout).fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("studio.handoff.none")
                    Button("Close") { editor.dismissHandOff() }
                } else {
                    Text("These \(g.terms.count) terms would be written for your career session to read:").font(.callout.weight(.semibold))
                    WrapLayout(spacing: 6) { ForEach(g.terms, id: \.self) { Chip(text: $0, tint: .secondary) } }
                    if let note = editor.handOffNote { Text(verbatim: note).font(.caption).foregroundStyle(.orange) }
                    if g.termsTruncated { Text("Only the first terms fit one hand-off line; the rest are left out.").font(.caption).foregroundStyle(.orange) }
                    Text("Nothing has been written yet.").font(.caption).foregroundStyle(.secondary)
                    HStack {
                        Button("Cancel") { editor.dismissHandOff() }.accessibilityIdentifier("studio.handoff.cancel")
                        Button("Send \(g.terms.count) term\(g.terms.count == 1 ? "" : "s")") { Task { await editor.confirmHandOff() } }
                            .keyboardShortcut(.defaultAction)
                            .accessibilityIdentifier("studio.handoff.confirm")
                    }
                }
            case .sending:
                HStack(spacing: 8) { ProgressView().controlSize(.small); Text("Writing the hand-off…").font(.callout) }
            case let .done(g):
                Label { Text(verbatim: ResumeWords.handOff(g)).fixedSize(horizontal: false, vertical: true) } icon: {
                    Image(systemName: g.written ? "paperplane.fill" : "info.circle").foregroundStyle(g.written ? Color.green : Color.orange)
                }
                .accessibilityIdentifier("studio.handoff.done")
                Button("Done") { editor.dismissHandOff() }
            case let .failed(f):
                if let text = ResumeWords.failure(f) {
                    Label { Text(verbatim: text.message).fixedSize(horizontal: false, vertical: true) } icon: {
                        Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                    }
                    Text("Nothing was written.").font(.caption).foregroundStyle(.secondary)
                    Button("Dismiss") { editor.dismissHandOff() }
                }
            }
        }
    }
}

// MARK: - Versions

struct ResumeVersionsPanel: View {
    let editor: ResumeStudioEditor
    var flat = false

    var body: some View {
        Group {
            if flat { content } else { ScrollView { content } }
        }
        .background(.background)
        .accessibilityIdentifier("studio.versionsPanel")
    }

    private var content: some View {
        VStack(alignment: .leading, spacing: 12) {
            if let note = editor.versionsNote {
                HStack(alignment: .firstTextBaseline) {
                    Label(note, systemImage: "checkmark.circle").font(.callout)
                    Spacer()
                    Button("OK") { editor.dismissVersionsMessage() }.buttonStyle(.borderless)
                }
                .accessibilityIdentifier("studio.versions.note")
            }
            if let f = editor.versionsFailure, let text = ResumeWords.failure(f) {
                HStack(alignment: .firstTextBaseline) {
                    Label { Text(verbatim: text.message).fixedSize(horizontal: false, vertical: true) } icon: {
                        Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                    }
                    Spacer()
                    Button("Dismiss") { editor.dismissVersionsMessage() }.buttonStyle(.borderless)
                }
                .accessibilityIdentifier("studio.versions.error")
            }
            let rows = editor.versionRows
            if rows.isEmpty {
                Text(editor.loading == .loading ? "Loading…" : "No saved version yet. Fit a draft, or build one from the Jobs tab.")
                    .font(.callout).foregroundStyle(.secondary)
            } else {
                ForEach(rows) { row in VersionRowView(editor: editor, row: row) }
            }
            comparison
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .topLeading)
    }

    @ViewBuilder
    private var comparison: some View {
        switch editor.comparison {
        case nil:
            EmptyView()
        case let .loading(from, to):
            HStack(spacing: 8) { ProgressView().controlSize(.small); Text("Comparing v\(from) with v\(to)…").font(.callout) }
        case let .failed(from, to, f):
            if let text = ResumeWords.failure(f) {
                VStack(alignment: .leading, spacing: 4) {
                    Label { Text(verbatim: "v\(from) and v\(to) could not be compared: \(text.message)").fixedSize(horizontal: false, vertical: true) } icon: {
                        Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                    }
                    Button("Close") { editor.clearComparison() }
                }
                .accessibilityIdentifier("studio.compare.error")
            }
        case .loaded:
            if let view = editor.comparisonView { DiffCard(editor: editor, view: view) }
        }
    }
}

private struct VersionRowView: View {
    let editor: ResumeStudioEditor
    let row: ResumeVersionRow

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(verbatim: row.title).font(.subheadline.weight(.semibold))
                ForEach(row.badges, id: \.self) { Chip(text: $0, tint: $0 == "current" ? .blue : .green) }
                if editor.shownVersion == row.version { Chip(text: "shown", tint: .secondary) }
                Spacer()
                Text(row.created, format: .dateTime.month(.abbreviated).day().hour().minute()).font(.caption).foregroundStyle(.secondary)
            }
            if row.parent >= 1 { Text("made from v\(row.parent)").font(.caption).foregroundStyle(.secondary) }
            if let kept = row.kept { Text(verbatim: kept).font(.caption).foregroundStyle(.secondary) }
            if let line = row.instruction {
                Text(verbatim: line.display)
                    .font(.callout.italic())
                    .foregroundStyle(line.from == nil ? Color.primary : Color.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
                    .help(line.from == nil ? "What this version was asked with. It is never printed on the page."
                          : "This page was not shaped by a model; this is what the version it came from was asked with.")
                    .accessibilityIdentifier("studio.version.instruction.\(row.version)")
            }
            HStack(spacing: 8) {
                if editor.shownVersion != row.version {
                    Button("Show") { Task { await editor.requestSwitch(to: row.version) } }
                        .disabled(editor.step != nil)
                        .help("Show this version in the outline and the page. Unsaved edits ask first.")
                }
                Button("Compare with parent") { Task { await editor.compareWithParent(row.version) } }
                    .disabled(!hasParent)
                    .help(hasParent ? "What this version changed against v\(row.parent), the one it was made from."
                          : "This version was not made from another one.")
                    .accessibilityIdentifier("studio.compareParent.\(row.version)")
                Menu("Compare with…") {
                    ForEach(ResumeVersions.comparable(to: row.version, in: editor.shown?.versions ?? []), id: \.self) { other in
                        Button("v\(other)") { Task { await editor.compare(from: other, to: row.version) } }
                    }
                }
                .menuStyle(.borderlessButton).fixedSize()
                .help("What this version changed against another one you choose.")
                let blocked = editor.makeCurrentBlockedReason(row.version)
                Button("Make current") { Task { await editor.makeCurrent(row.version) } }
                    .disabled(blocked != nil)
                    .help(blocked ?? "Make this the version the Studio shows and a redraft starts from. Nothing is deleted and the version in use does not change.")
                    .accessibilityIdentifier("studio.makeCurrent.\(row.version)")
            }
            .buttonStyle(.borderless)
        }
        .padding(10)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 8))
        .accessibilityIdentifier("studio.version.\(row.version)")
    }

    private var hasParent: Bool {
        row.parent >= 1 && (editor.shown?.versions.contains { $0.version == row.parent } ?? false)
    }
}

private struct DiffCard: View {
    let editor: ResumeStudioEditor
    let view: ResumeDiffView

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text(verbatim: view.title).font(.headline)
                Spacer()
                Button("Close") { editor.clearComparison() }.buttonStyle(.borderless).accessibilityIdentifier("studio.compare.close")
            }
            Text(verbatim: view.headline).font(.callout).fixedSize(horizontal: false, vertical: true)
            Text(verbatim: "From: " + view.fromLine).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            Text(verbatim: "To: " + view.toLine).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            if !view.added.isEmpty { bullets("Added", view.added, tint: .green, symbol: "plus.circle.fill") }
            if !view.removed.isEmpty { bullets("Removed", view.removed, tint: .red, symbol: "minus.circle.fill") }
            if !view.moved.isEmpty {
                VStack(alignment: .leading, spacing: 4) {
                    Label("Moved", systemImage: "arrow.up.arrow.down.circle.fill").font(.subheadline.weight(.semibold)).foregroundStyle(.orange)
                    ForEach(view.moved) { m in
                        VStack(alignment: .leading, spacing: 1) {
                            Text(verbatim: m.text ?? m.id).font(.callout).fixedSize(horizontal: false, vertical: true)
                            Text(verbatim: m.change).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                }
            }
            if !view.overrides.isEmpty {
                VStack(alignment: .leading, spacing: 2) {
                    Label("Overrides", systemImage: "slider.horizontal.3").font(.subheadline.weight(.semibold))
                    ForEach(view.overrides, id: \.self) { Text(verbatim: $0).font(.callout).fixedSize(horizontal: false, vertical: true) }
                }
            }
            if let order = view.sectionOrder {
                VStack(alignment: .leading, spacing: 2) {
                    Label("Section order", systemImage: "list.number").font(.subheadline.weight(.semibold))
                    Text(verbatim: order).font(.callout).fixedSize(horizontal: false, vertical: true)
                }
            }
            if !view.keywordsAdded.isEmpty || !view.keywordsRemoved.isEmpty {
                VStack(alignment: .leading, spacing: 4) {
                    Label("Keywords", systemImage: "tag").font(.subheadline.weight(.semibold))
                    WrapLayout(spacing: 6) {
                        ForEach(view.keywordsAdded, id: \.self) { Chip(text: "+ \($0)", tint: .green) }
                        ForEach(view.keywordsRemoved, id: \.self) { Chip(text: "− \($0)", tint: .red) }
                    }
                }
            }
        }
        .padding(12)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 8))
        .accessibilityIdentifier("studio.compare")
    }

    private func bullets(_ title: String, _ list: [ResumeDiffBullet], tint: Color, symbol: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Label(title, systemImage: symbol).font(.subheadline.weight(.semibold)).foregroundStyle(tint)
            ForEach(list) { b in
                VStack(alignment: .leading, spacing: 1) {
                    Text(verbatim: b.text ?? "\(b.id) (not offered by your knowledge base now)").font(.callout).fixedSize(horizontal: false, vertical: true)
                    Text(verbatim: b.place).font(.caption).foregroundStyle(.secondary)
                }
            }
        }
    }
}

// MARK: - Redraft

/// The Redraft sheet: the form, then (for the cost it asked) the confirmation, in this window. A refusal comes
/// back to the form, on it.
struct RedraftSheet: View {
    @Environment(JobsModel.self) private var jobs
    let editor: ResumeStudioEditor

    var body: some View {
        Group {
            if let pending = jobs.resume.confirmingInStudio(editor.key) {
                ResumeConfirmContent(key: pending.key, dry: pending.dry)
            } else {
                RedraftFormView(editor: editor)
            }
        }
        .frame(width: 520)
    }
}

struct RedraftFormView: View {
    @Environment(JobsModel.self) private var jobs
    @Bindable var editor: ResumeStudioEditor

    private var phase: ResumeStudioModel.Phase { jobs.resume.phase.key == editor.key ? jobs.resume.phase : .idle }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Redraft v\(editor.shownVersion ?? 0)").font(.headline)
            if let name = editor.jobName { Text(verbatim: name).foregroundStyle(.secondary).lineLimit(2) }
            Text("\(ResumeWords.modelName) picks the bullets again, starting from this version as saved. The cost is shown next; nothing is spent until you confirm it.")
                .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            Divider()
            if let targets = editor.redraftTargets {
                scopePicker(targets)
                locks(targets)
            }
            instruction
            refusal
            ForEach(editor.redraftProblems, id: \.self) { p in
                Label(p, systemImage: "exclamationmark.circle.fill").font(.caption).foregroundStyle(.red)
                    .accessibilityIdentifier("redraft.problem")
            }
            HStack {
                if case .estimating = phase {
                    ProgressView().controlSize(.small)
                    Text("Asking Vani what this would cost…").font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                Button("Cancel", role: .cancel) { editor.closeRedraft() }
                    .keyboardShortcut(.cancelAction)
                    .accessibilityIdentifier("redraft.cancel")
                Button("Estimate cost…") { editor.askRedraftCost() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(askBlocked != nil)
                    .help(askBlocked ?? "Ask Vani what the redraft would cost at most. Nothing is spent yet.")
                    .accessibilityIdentifier("redraft.estimate")
            }
        }
        .padding(20)
    }

    private var askBlocked: String? {
        if case .estimating = phase { return "Asking already." }
        if let r = editor.redraftBlockedReason, !isOurOwnStep { return r }
        return editor.redraftProblems.first
    }

    /// The editor's own estimate step makes `redraftBlockedReason` say another step is running: that is this one.
    private var isOurOwnStep: Bool {
        switch phase {
        case .estimating, .confirming, .failed: true
        default: false
        }
    }

    private func scopePicker(_ targets: ResumeRedraftTargets) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("What to re-pick").font(.subheadline.weight(.semibold))
            Picker("What to re-pick", selection: $editor.redraftForm.scope) {
                Text("The whole page").tag(ResumeRedraftForm.Scope.whole)
                ForEach(targets.sections) { section in
                    Text(verbatim: "Only the section “\(section.title)”").tag(ResumeRedraftForm.Scope.section(section.title))
                    ForEach(section.entries) { entry in
                        Text(verbatim: "Only the entry “\(entry.title)” (in \(section.title))").tag(ResumeRedraftForm.Scope.entry(entry.id))
                    }
                }
            }
            .labelsHidden()
            .accessibilityIdentifier("redraft.scope")
            Text("The rest of the page stays exactly as it is in this version.").font(.caption).foregroundStyle(.secondary)
        }
    }

    private func locks(_ targets: ResumeRedraftTargets) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Keep as it is").font(.subheadline.weight(.semibold))
            ForEach(targets.sections) { section in
                let lockable = editor.redraftForm.isLockable(section.title, targets)
                Toggle(isOn: Binding(
                    get: { lockable && editor.redraftForm.locked.contains(section.title) },
                    set: { on in if on { editor.redraftForm.locked.insert(section.title) } else { editor.redraftForm.locked.remove(section.title) } })) {
                    Text(verbatim: section.title)
                }
                .disabled(!lockable)
                .help(lockable ? "Keep “\(section.title)” as it is in this version." : "This is the part being re-picked, so it cannot also be kept.")
                .accessibilityIdentifier("redraft.lock.\(section.title)")
            }
        }
    }

    private var instruction: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text("Instruction (optional)").font(.subheadline.weight(.semibold))
                Spacer()
                Text(ResumeRedraftRules.counter(editor.redraftForm.instruction))
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(ResumeRedraftRules.instructionCount(editor.redraftForm.instruction) > ResumeRedraftRules.maxInstruction ? Color.red : Color.secondary)
                    .accessibilityIdentifier("redraft.counter")
            }
            TextField("One line, e.g. lead with the serving work", text: $editor.redraftForm.instruction)
                .textFieldStyle(.roundedBorder)
                .accessibilityIdentifier("redraft.instruction")
            if let p = ResumeRedraftRules.instructionProblem(editor.redraftForm.instruction) {
                Text(verbatim: p).font(.caption).foregroundStyle(.red)
            }
            Text("Sent to \(ResumeWords.modelName) with the posting and saved with the version, never printed on the page. Leave out your name, email address, phone number and address: Vani refuses an email address or a phone number.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
    }

    /// What Vani refused, in its words, on the form: the instruction, the scope, or the model's own reply.
    @ViewBuilder
    private var refusal: some View {
        if case let .failed(_, f) = phase, let text = ResumeWords.failure(f) {
            VStack(alignment: .leading, spacing: 4) {
                Label { Text(verbatim: text.message).textSelection(.enabled).fixedSize(horizontal: false, vertical: true) } icon: {
                    Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                }
                if let cost = text.costNote { Text(verbatim: cost).font(.caption).fixedSize(horizontal: false, vertical: true) }
                Button("Dismiss") { jobs.resume.dismiss() }.buttonStyle(.borderless)
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("redraft.refusal")
        }
    }
}
