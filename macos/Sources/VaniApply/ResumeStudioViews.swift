import AppKit
import SwiftUI
import UniformTypeIdentifiers
import VaniApplyKit

/// The Resume Studio window: one per job (`WindowGroup(for: String.self)`, keyed by the posting's key, so
/// opening the same job again brings its window forward and another job gets its own). Logic is
/// `ResumeStudioEditor`, `ResumeDraft` and `ResumeWords`; this only draws it.
struct ResumeStudioWindow: View {
    @Environment(JobsModel.self) private var jobs
    let key: String
    @State private var editor: ResumeStudioEditor?
    @State private var closer = WindowCloser()

    var body: some View {
        Group {
            if let editor {
                ResumeStudioContent(editor: editor, closer: closer)
            } else {
                ProgressView().controlSize(.small).frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .frame(minWidth: 960, minHeight: 620)
        .navigationTitle(editor?.title ?? "Resume Studio")
        .task {
            guard editor == nil else { return }
            let made = ResumeStudioEditor(key: key, resume: jobs.resume, job: nil)
            editor = made
            await made.open()
        }
    }
}

/// The window's four bands: the top bar, what is happening, the outline beside the preview, the file buttons.
struct ResumeStudioContent: View {
    @Environment(JobsModel.self) private var jobs
    let editor: ResumeStudioEditor
    let closer: WindowCloser
    /// For the view smoke test only. `ImageRenderer` cannot draw an AppKit split view around this content
    /// (it traps), draws nothing inside a scroll view, and draws a placeholder for PDFKit and WebKit, so the
    /// test draws the outline and the preview's frame side by side with none of them. The window never sets it.
    var flat = false

    var body: some View {
        VStack(spacing: 0) {
            ResumeStudioTopBar(editor: editor)
            Divider()
            ResumeStudioStatus(editor: editor)
            if flat {
                HStack(alignment: .top, spacing: 0) {
                    ResumeOutline(editor: editor, flat: true).frame(width: 520)
                    Divider()
                    ResumePreviewPane(editor: editor, flat: true).frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            } else {
                HSplitView {
                    ResumeOutline(editor: editor)
                        .frame(minWidth: 380, idealWidth: 460)
                    ResumePreviewPane(editor: editor)
                        .frame(minWidth: 380, idealWidth: 520)
                }
            }
            Divider()
            ResumeStudioFileBar(editor: editor)
        }
        .background(WindowCloseGuard(isEdited: editor.isDirty, closer: closer) { editor.requestClose() })
        .onChange(of: jobs.resume.phase) { _, phase in
            Task { await editor.phaseChanged(phase) }
        }
        .onDisappear { editor.shutdown() }
        .confirmationDialog(pendingTitle, isPresented: Binding(get: { editor.pending != nil }, set: { if !$0 { editor.keepEditing() } }),
                            titleVisibility: .visible) {
            Button("Discard edits", role: .destructive) {
                Task {
                    let closing = editor.pending == .close
                    await editor.confirmPending()
                    if closing { closer.closeApproved() }
                }
            }
            Button("Keep editing", role: .cancel) { editor.keepEditing() }
        } message: {
            Text("Your edits to this page have not been fitted, so they are not saved as a version. Discarding them cannot be undone.")
        }
    }

    private var pendingTitle: String {
        switch editor.pending {
        case .close?: "Close this window and discard your edits?"
        case .reset?: "Start over from your base selection and discard your edits?"
        case let .switchTo(v)?: "Show v\(v) and discard your edits?"
        case nil: ""
        }
    }
}

// MARK: - Top bar

struct ResumeStudioTopBar: View {
    let editor: ResumeStudioEditor

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                versionPicker
                chips
                Spacer(minLength: 8)
            }
            HStack(spacing: 8) {
                Button { Task { await editor.preview() } } label: { Label("Preview", systemImage: "eye") }
                    .disabled(editor.previewBlockedReason != nil)
                    .help(editor.previewBlockedReason ?? "Draw your draft as a page to look at. Nothing is saved and the layout is not measured.")
                    .accessibilityIdentifier("studio.preview")
                Button { Task { await editor.fit() } } label: { Label("Fit", systemImage: "rectangle.compress.vertical") }
                    .disabled(editor.fitBlockedReason != nil)
                    .help(editor.fitBlockedReason ?? "Fit the page in Chrome and save it as a new version. No model, nothing is spent.")
                    .accessibilityIdentifier("studio.fit")
                UseButton(editor: editor)
                if editor.canRevalidate {
                    Button("Revalidate") { editor.revalidate() }
                        .help("Drop what your knowledge base no longer vouches for and rebuild the page. No model, nothing is spent.")
                        .accessibilityIdentifier("studio.revalidate")
                }
                if editor.step == .previewing || editor.isFitting {
                    Button("Stop") { editor.stop() }
                        .disabled(editor.step == .fitting(cancelling: true))
                        .help("Stop. Your draft is kept.")
                        .accessibilityIdentifier("studio.stop")
                }
                Spacer(minLength: 8)
                Button("Reset to base") { Task { await editor.requestReset() } }
                    .disabled(editor.pool?.base == nil || editor.isFitting)
                    .help(editor.pool?.base == nil ? "Your base selection is not known: the knowledge base could not be checked."
                          : "Start over from your base selection.")
                    .accessibilityIdentifier("studio.reset")
                Button { Task { await editor.reload(version: editor.shownVersion, check: true, keepDraft: editor.isDirty) } } label: {
                    Image(systemName: "arrow.clockwise")
                }
                .disabled(editor.step != nil)
                .help("Ask Vani again: the versions, the knowledge base check and the posting's keywords. Your draft is kept.")
                .accessibilityIdentifier("studio.refresh")
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
    }

    @ViewBuilder
    private var versionPicker: some View {
        if editor.versions.isEmpty {
            Text(editor.loading == .loading ? "Loading…" : "No version yet").font(.headline).foregroundStyle(.secondary)
        } else {
            Picker("Version", selection: Binding(get: { editor.shownVersion ?? 0 }, set: { v in Task { await editor.requestSwitch(to: v) } })) {
                ForEach(editor.versions, id: \.version) { v in
                    Text(verbatim: label(v)).tag(v.version)
                }
            }
            .labelsHidden()
            .frame(maxWidth: 360)
            .disabled(editor.step != nil)
            .help("Switch to another saved version. Unsaved edits ask first.")
            .accessibilityIdentifier("studio.versions")
        }
    }

    private func label(_ v: ResumeVersion) -> String {
        var parts = ["v\(v.version)", ResumeWords.source(v.source), ResumeWords.pagesPhrase(v.pages)]
        if v.current { parts.append("current") }
        if v.used { parts.append("in use") }
        return parts.joined(separator: " · ")
    }

    private var chips: some View {
        WrapLayout(spacing: 6) {
            if editor.isDirty { Chip(text: "Unsaved edits", tint: .orange).help("Fit saves your edits as a new version.") }
            if let s = editor.sourceWord { Chip(text: s, tint: .secondary) }
            if let pages = editor.pagesWord { Chip(text: pages, tint: .secondary) }
            if let fit = editor.fitWord { Chip(text: fit.short, tint: levelColor(fit.level)).help(fit.long) }
            if let el = editor.eligibilityWord { Chip(text: el.short, tint: levelColor(el.level)).help(el.long) }
            if let cost = editor.costWord { Chip(text: cost, tint: .secondary).help("What the model turns that made this version charged. Unknown is never shown as $0.") }
            if let note = editor.noteLine { Text(verbatim: note).font(.caption).foregroundStyle(.secondary).help(editor.version?.notes.joined(separator: "\n") ?? "") }
        }
    }
}

private func levelColor(_ level: ResumeCheckLevel) -> Color {
    switch level {
    case .ok: .green
    case .caution: .orange
    case .bad: .red
    case .notRun: .gray
    }
}

private struct UseButton: View {
    let editor: ResumeStudioEditor

    var body: some View {
        let reason = editor.useBlockedReason
        Button { editor.use() } label: {
            Label(editor.useAction.isInUse ? "In use" : "Use for this job", systemImage: "checkmark.circle")
        }
        .disabled(reason != nil)
        .help(reason ?? "Check this version afresh, then save it for the browsers under this job's addresses.")
        .accessibilityIdentifier("studio.use")
    }
}

extension ResumeUseAction {
    fileprivate var isInUse: Bool {
        if case .inUse = self { return true }
        return false
    }
}

// MARK: - What is happening

struct ResumeStudioStatus: View {
    @Environment(JobsModel.self) private var jobs
    let editor: ResumeStudioEditor

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if case let .failed(f) = editor.loading { failureBlock(f, retry: true) }
            if let step = editor.step { stepLine(step) }
            if let job = editor.job { ResumePhaseView(job: job) }
            if let failure = editor.failure, editor.loading == .loaded || editor.shown != nil { failureBlock(failure, retry: false) }
            if let notice = editor.notice {
                HStack(alignment: .firstTextBaseline) {
                    Label(notice, systemImage: "doc.badge.checkmark").font(.callout).fixedSize(horizontal: false, vertical: true)
                    Spacer()
                }
                .accessibilityIdentifier("studio.notice")
            }
            ForEach(editor.warnings, id: \.self) { w in
                Label(w, systemImage: "exclamationmark.triangle.fill").font(.callout).foregroundStyle(.orange)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let note = editor.editNote {
                Text(verbatim: note).font(.caption).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("studio.editNote")
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 14)
        .padding(.vertical, hasContent ? 8 : 0)
    }

    private var hasContent: Bool {
        editor.step != nil || editor.failure != nil || editor.notice != nil || !editor.warnings.isEmpty || editor.editNote != nil
            || { if case .failed = editor.loading { true } else { false } }()
            || (editor.job.map { jobs.resume.phase.key == $0.key } ?? false)
    }

    private func stepLine(_ step: ResumeStudioEditor.Step) -> some View {
        let (title, detail): (String, String) = switch step {
        case .loading: ("Asking Vani about this job's resumes…", "This checks every version against your knowledge base and can take up to a minute.")
        case .previewing: ("Drawing your draft…", "Nothing is saved and nothing is spent.")
        case let .fitting(cancelling): (cancelling ? "Stopping…" : "Fitting the page in Chrome…",
                                         "This can take up to \(Int(ResumeStudioClient.Timeouts.standard.renderFit / 60)) minutes. Your draft is kept if you stop.")
        }
        return HStack(alignment: .firstTextBaseline, spacing: 10) {
            ProgressView().controlSize(.small)
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                Text(detail).font(.caption).foregroundStyle(.secondary)
            }
        }
        .accessibilityIdentifier("studio.progress")
    }

    @ViewBuilder
    private func failureBlock(_ f: ResumeFailure, retry: Bool) -> some View {
        if let text = ResumeWords.failure(f) {
            VStack(alignment: .leading, spacing: 4) {
                Label { Text(verbatim: text.message).textSelection(.enabled).fixedSize(horizontal: false, vertical: true) } icon: {
                    Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                }
                if let cost = text.costNote { Text(verbatim: cost).font(.caption).fixedSize(horizontal: false, vertical: true) }
                HStack {
                    if retry { Button("Try again") { Task { await editor.open() } } }
                    else { Button("Dismiss") { editor.dismissFailure() } }
                }
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("studio.error")
        }
    }
}

// MARK: - Outline

struct ResumeOutline: View {
    let editor: ResumeStudioEditor
    /// No scroll view (the view smoke test: `ImageRenderer` draws nothing inside one).
    var flat = false

    var body: some View {
        if flat { content } else { ScrollView { content } }
    }

    private var content: some View {
        VStack(alignment: .leading, spacing: 14) {
            if let draft = editor.draft {
                ForEach(Array(draft.sections.enumerated()), id: \.element.id) { _, section in
                    ResumeSectionBlock(editor: editor, section: section)
                }
            } else if editor.loading == .loading {
                ProgressView().controlSize(.small)
            } else {
                Text(verbatim: editor.editBlockedReason ?? "There is nothing to edit yet.")
                    .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .topLeading)
        .background(.background)
        .accessibilityIdentifier("studio.outline")
    }
}

private struct ResumeSectionBlock: View {
    let editor: ResumeStudioEditor
    let section: ResumeDraft.Section

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text(verbatim: section.title).font(.headline)
                if section.isEmpty { Text("not on the page").font(.caption).foregroundStyle(.secondary) }
                Spacer()
                if editor.reorderable.contains(section.title) {
                    Button { editor.moveSection(section.title, by: -1) } label: { Image(systemName: "chevron.up") }
                        .buttonStyle(.borderless).disabled(!editor.canEdit).help("Move this section up among the project sections.")
                    Button { editor.moveSection(section.title, by: 1) } label: { Image(systemName: "chevron.down") }
                        .buttonStyle(.borderless).disabled(!editor.canEdit).help("Move this section down among the project sections.")
                }
                if section.kind == .entries { addEntryMenu }
                if section.kind == .bullets { SwapButton(editor: editor, place: .summary(section.title), title: "Add bullet") }
            }
            ForEach(editor.issues(for: .section(section.title))) { IssueText(issue: $0) }
            switch section.kind {
            case .bullets:
                BulletGroups(editor: editor, place: .summary(section.title), rows: section.bullets)
            case .entries:
                ForEach(section.entries) { entry in
                    ResumeEntryBlock(editor: editor, section: section, entry: entry)
                }
            }
        }
    }

    @ViewBuilder
    private var addEntryMenu: some View {
        let addable = editor.addableEntries(in: section.title)
        Menu("Add entry") {
            ForEach(addable) { e in
                Button(entryLabel(e)) { editor.addEntry(e.id, to: section.title) }
            }
        }
        .menuStyle(.borderlessButton)
        .fixedSize()
        .disabled(addable.isEmpty || !editor.canEdit)
        .help(addable.isEmpty ? "Every entry your knowledge base offers for this section is on the page already."
              : "Put another entry from your knowledge base in this section.")
    }

    private func entryLabel(_ e: ResumePool.Entry) -> String {
        [e.title, e.org].filter { !$0.isEmpty }.joined(separator: " · ")
    }
}

private struct ResumeEntryBlock: View {
    let editor: ResumeStudioEditor
    let section: ResumeDraft.Section
    let entry: ResumeDraft.EntryRow
    @State private var showOverrides = false

    private var pool: ResumePool.Entry? { editor.pool?.entry(entry.id) }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                VStack(alignment: .leading, spacing: 1) {
                    Text(verbatim: entry.overrides.title.isEmpty ? (pool?.title ?? entry.id) : entry.overrides.title).font(.subheadline.weight(.semibold))
                    let sub = [entry.overrides.org.isEmpty ? pool?.org ?? "" : entry.overrides.org,
                               entry.overrides.dates.isEmpty ? pool?.dates ?? "" : entry.overrides.dates].filter { !$0.isEmpty }.joined(separator: " · ")
                    if !sub.isEmpty { Text(verbatim: sub).font(.caption).foregroundStyle(.secondary) }
                }
                Spacer()
                SwapButton(editor: editor, place: .entry(entry.id), title: "Swap")
                Button { showOverrides.toggle() } label: { Image(systemName: showOverrides ? "slider.horizontal.3" : "slider.horizontal.3") }
                    .buttonStyle(.borderless)
                    .help("Override this entry's title, organization, stack, lead line or dates for this resume.")
                    .accessibilityIdentifier("studio.overrides.\(entry.id)")
                Button { editor.moveEntry(entry.id, by: -1) } label: { Image(systemName: "chevron.up") }
                    .buttonStyle(.borderless).disabled(!editor.canEdit).help("Move this entry up.")
                Button { editor.moveEntry(entry.id, by: 1) } label: { Image(systemName: "chevron.down") }
                    .buttonStyle(.borderless).disabled(!editor.canEdit).help("Move this entry down.")
                Button { editor.removeEntry(entry.id) } label: { Image(systemName: "xmark.circle") }
                    .buttonStyle(.borderless).disabled(!editor.canEdit).help("Take this entry off the page.")
            }
            ForEach(editor.issues(for: .entry(entry.id))) { IssueText(issue: $0) }
            if showOverrides || !entry.overrides.isEmpty { overrides }
            BulletGroups(editor: editor, place: .entry(entry.id), rows: entry.bullets)
        }
        .padding(10)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 8))
        .overlay(RoundedRectangle(cornerRadius: 8).strokeBorder(editor.issues(for: .entry(entry.id)).contains { $0.blocking } ? Color.red.opacity(0.6) : Color.clear))
    }

    private var overrides: some View {
        VStack(alignment: .leading, spacing: 4) {
            ForEach(ResumeOverrideField.allCases, id: \.self) { field in
                VStack(alignment: .leading, spacing: 1) {
                    HStack {
                        Text(field.label).font(.caption).foregroundStyle(.secondary).frame(width: 90, alignment: .leading)
                        TextField(placeholder(field), text: Binding(get: { entry.overrides[field] }, set: { editor.setOverride(field, $0, entry: entry.id) }))
                            .textFieldStyle(.roundedBorder)
                            .disabled(!editor.canEdit)
                            .accessibilityIdentifier("studio.override.\(entry.id).\(field.rawValue)")
                    }
                    ForEach(editor.issues(for: .override(entry: entry.id, field: field))) { IssueText(issue: $0) }
                }
            }
            Text("An empty field keeps what your knowledge base says. A lead of - leaves the lead line out.")
                .font(.caption2).foregroundStyle(.secondary)
        }
    }

    private func placeholder(_ f: ResumeOverrideField) -> String {
        switch f {
        case .title: pool?.title ?? ""
        case .org: pool?.org ?? ""
        case .dates: pool?.dates ?? ""
        case .stack, .lead: ""
        }
    }
}

private struct IssueText: View {
    let issue: ResumeDraft.Issue

    var body: some View {
        Label { Text(verbatim: issue.message).fixedSize(horizontal: false, vertical: true) } icon: {
            Image(systemName: issue.blocking ? "exclamationmark.circle.fill" : "info.circle")
        }
        .font(.caption)
        .foregroundStyle(issue.blocking ? Color.red : Color.orange)
        .accessibilityIdentifier("studio.issue")
    }
}

// MARK: Bullets

private struct BulletGroups: View {
    let editor: ResumeStudioEditor
    let place: ResumeDraft.Place
    let rows: [ResumeDraft.BulletRow]

    private var groups: [Range<Int>] { ResumeDraft.groups(rows, follows: editor.pool?.follows ?? [:]) }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            ForEach(Array(groups.enumerated()), id: \.offset) { index, range in
                VStack(alignment: .leading, spacing: 2) {
                    ForEach(Array(rows[range].enumerated()), id: \.element.id) { offset, row in
                        BulletRowView(editor: editor, place: place, row: row, attached: offset > 0)
                    }
                }
                .padding(.vertical, 2)
                .draggable(DragTag.encode(place: place, index: index))
                .dropDestination(for: String.self) { items, _ in
                    guard let from = items.compactMap({ DragTag.decode($0, place: place) }).first, from != index else { return false }
                    editor.moveGroup(in: place, from: from, toOffset: from < index ? index + 1 : index)
                    return true
                }
                .contextMenu {
                    Button("Move up") { editor.moveGroup(in: place, from: index, toOffset: index - 1) }.disabled(index == 0)
                    Button("Move down") { editor.moveGroup(in: place, from: index, toOffset: index + 2) }.disabled(index >= groups.count - 1)
                }
                .help(range.count > 1 ? "Drag to reorder. The bullets that continue this one move with it." : "Drag to reorder within this entry.")
            }
        }
    }
}

/// What a dragged bullet group carries: where it came from and its position, so a drop on another place is ignored.
private enum DragTag {
    static func encode(place: ResumeDraft.Place, index: Int) -> String { "\(tag(place))\u{1F}\(index)" }

    static func decode(_ s: String, place: ResumeDraft.Place) -> Int? {
        let parts = s.components(separatedBy: "\u{1F}")
        guard parts.count == 2, parts[0] == tag(place) else { return nil }
        return Int(parts[1])
    }

    private static func tag(_ p: ResumeDraft.Place) -> String {
        switch p {
        case let .summary(t): "summary:\(t)"
        case let .entry(id): "entry:\(id)"
        }
    }
}

private struct BulletRowView: View {
    let editor: ResumeStudioEditor
    let place: ResumeDraft.Place
    let row: ResumeDraft.BulletRow
    let attached: Bool

    private var bullet: ResumePool.Bullet? { editor.pool?.bullet(row.id, in: place) }

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                if attached { Image(systemName: "arrow.turn.down.right").font(.caption).foregroundStyle(.secondary).help("Continues the bullet above: it moves and switches with it.") }
                Toggle("", isOn: Binding(get: { row.included }, set: { editor.setIncluded(row.id, $0, in: place) }))
                    .labelsHidden()
                    .toggleStyle(.checkbox)
                    .disabled(!editor.canEdit)
                    .help(row.included ? "On the page. Uncheck to leave it off (it keeps its place)." : "Off the page. Check to put it back.")
                Text(verbatim: bullet?.text ?? row.id)
                    .font(.callout)
                    .foregroundStyle(row.included ? Color.primary : Color.secondary)
                    .strikethrough(!row.included)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
                Spacer(minLength: 4)
                if row.included, let lines = editor.lines(for: row.id) {
                    Text("\(lines) line\(lines == 1 ? "" : "s")").font(.caption2).foregroundStyle(.secondary)
                        .help("How many lines this bullet takes on the fitted page.")
                }
            }
            ForEach(editor.issues(for: .bullet(row.id))) { IssueText(issue: $0) }
        }
        .accessibilityIdentifier("studio.bullet.\(row.id)")
    }
}

/// "Swap" on an entry (or "Add bullet" on the summary): the pool's bullets that are not on the page for this
/// place, each with the posting's keywords it holds, and a way to put it on the page or in another's place.
private struct SwapButton: View {
    let editor: ResumeStudioEditor
    let place: ResumeDraft.Place
    let title: String
    @State private var open = false

    var body: some View {
        Button(title) { open = true }
            .buttonStyle(.borderless)
            .disabled(!editor.canEdit)
            .help("Bullets your knowledge base offers here that are not on the page, with the posting's keywords they hold.")
            .accessibilityIdentifier("studio.swap")
            .popover(isPresented: $open, arrowEdge: .bottom) { SwapPopover(editor: editor, place: place, close: { open = false }) }
    }
}

private struct SwapPopover: View {
    let editor: ResumeStudioEditor
    let place: ResumeDraft.Place
    let close: () -> Void

    var body: some View {
        let candidates = editor.candidates(in: place)
        let current = (editor.draft?.rows(in: place) ?? []).filter { $0.included && editor.pool?.follows[$0.id] == nil }
        VStack(alignment: .leading, spacing: 8) {
            Text("Not on the page").font(.headline)
            if candidates.isEmpty {
                Text("Your knowledge base offers nothing else for this place.").font(.callout).foregroundStyle(.secondary)
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: 10) {
                        ForEach(candidates, id: \.bullet.id) { c in
                            VStack(alignment: .leading, spacing: 4) {
                                HighlightedText(text: c.bullet.text, terms: editor.keywordTerms).font(.callout).fixedSize(horizontal: false, vertical: true)
                                HStack {
                                    if !c.hits.isEmpty { Text(verbatim: c.hits.joined(separator: ", ")).font(.caption).foregroundStyle(.orange) }
                                    Spacer()
                                    if c.bullet.follows != nil { Text("continues another bullet").font(.caption2).foregroundStyle(.secondary) }
                                    Button("Add") { editor.addBullet(c.bullet.id, to: place); close() }
                                    if c.bullet.follows == nil, !current.isEmpty {
                                        Menu("Replace…") {
                                            ForEach(current) { r in
                                                Button(editor.pool?.bullet(r.id, in: place)?.text.prefix(60).description ?? r.id) {
                                                    editor.swapBullet(r.id, for: c.bullet.id, in: place)
                                                    close()
                                                }
                                            }
                                        }
                                        .menuStyle(.borderlessButton).fixedSize()
                                    }
                                }
                            }
                            Divider()
                        }
                    }
                }
                .frame(maxHeight: 360)
            }
        }
        .padding(14)
        .frame(width: 440)
    }
}

/// A bullet's text with the posting's keywords it contains set apart.
struct HighlightedText: View {
    let text: String
    let terms: [String]

    var body: some View {
        ResumeKeywordHits.segments(in: text, terms: terms).reduce(Text("")) { acc, run in
            acc + (run.hit ? Text(verbatim: run.text).bold().foregroundColor(.orange) : Text(verbatim: run.text))
        }
    }
}

// MARK: - Preview

struct ResumePreviewPane: View {
    let editor: ResumeStudioEditor
    /// No PDFKit or WebKit view (the view smoke test: `ImageRenderer` draws only a placeholder for them).
    var flat = false

    var body: some View {
        VStack(spacing: 0) {
            banner
            if flat {
                Text(verbatim: editor.html != nil ? "(the unfitted page is drawn here)" : editor.pdf != nil ? "(the fitted PDF is drawn here)" : placeholder)
                    .font(.callout).foregroundStyle(.secondary).multilineTextAlignment(.center)
                    .frame(maxWidth: .infinity, maxHeight: .infinity).padding(24)
            } else if let html = editor.html {
                ResumeHTMLView(page: html.page, id: html.id)
                    .accessibilityIdentifier("studio.html")
            } else if let pdf = editor.pdf {
                ResumePDFView(data: pdf.data, token: "v\(pdf.version)-\(pdf.data.count)")
                    .accessibilityIdentifier("studio.pdf")
            } else {
                Text(verbatim: placeholder).font(.callout).foregroundStyle(.secondary).multilineTextAlignment(.center)
                    .frame(maxWidth: .infinity, maxHeight: .infinity).padding(24)
            }
        }
        .background(.background.secondary)
    }

    private var placeholder: String {
        if let e = editor.previewError { return e }
        if editor.loading == .loading { return "Loading…" }
        if let v = editor.version, !v.hasPDF { return "v\(v.version) was never fitted to pages, so it has no PDF. Preview draws your draft; Fit saves it as a fitted version." }
        return "Nothing to show yet. Preview draws your draft as a page; Fit saves it as a version with a PDF."
    }

    @ViewBuilder
    private var banner: some View {
        if let html = editor.html {
            VStack(alignment: .leading, spacing: 4) {
                HStack {
                    Label("Layout not fitted — Fit to check pages", systemImage: "exclamationmark.triangle.fill")
                        .font(.callout.weight(.semibold)).foregroundStyle(.orange)
                    Spacer()
                    if editor.pdf != nil { Button("Show v\(editor.shownVersion ?? 0) as saved") { editor.showSaved() } }
                }
                WrapLayout(spacing: 6) {
                    let lint = ResumeWords.lint(html.checks.lint), figures = ResumeWords.figures(html.checks.figures)
                    Chip(text: lint.short, tint: levelColor(lint.level)).help(lint.long)
                    Chip(text: figures.short, tint: levelColor(figures.level)).help(figures.long)
                    ForEach(html.notes, id: \.self) { Text(verbatim: $0).font(.caption).foregroundStyle(.secondary) }
                }
                Text("This is how rkb draws the page on screen. The page count and the line breaks are not measured until you Fit.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            .padding(10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color.orange.opacity(0.12))
            .accessibilityIdentifier("studio.unfitted")
        } else if let pdf = editor.pdf {
            HStack {
                Text("v\(pdf.version) as fitted").font(.caption).foregroundStyle(.secondary)
                if editor.isDirty {
                    Text("Your edits are not drawn here. Preview shows them; Fit saves them.").font(.caption).foregroundStyle(.orange)
                }
                Spacer()
            }
            .padding(.horizontal, 10).padding(.vertical, 6)
        }
    }
}

// MARK: - Files

struct ResumeStudioFileBar: View {
    let editor: ResumeStudioEditor
    @State private var message: String?
    @State private var working = false

    var body: some View {
        HStack(spacing: 10) {
            Button { Task { await saveAs() } } label: { Label("Save PDF As…", systemImage: "square.and.arrow.down") }
                .disabled(!editor.hasPDF || working)
                .help(editor.hasPDF ? "Save a copy of this version's PDF where you choose." : "This version has no PDF: Fit it first.")
                .accessibilityIdentifier("studio.saveAs")
            Button { reveal() } label: { Label("Reveal in Finder", systemImage: "folder") }
                .disabled(editor.pdfURL == nil)
                .help(editor.pdfURL == nil ? "This version has no PDF on disk." : "Show this version's PDF in Finder.")
                .accessibilityIdentifier("studio.reveal")
            Button { Task { await openInPreview() } } label: { Label("Open in Preview", systemImage: "doc.richtext") }
                .disabled(!editor.hasPDF || working)
                .help(editor.hasPDF ? "Open a copy of this version's PDF in Preview." : "This version has no PDF: Fit it first.")
                .accessibilityIdentifier("studio.openPreview")
            if let message { Text(verbatim: message).font(.caption).foregroundStyle(.orange).lineLimit(2).fixedSize(horizontal: false, vertical: true) }
            Spacer()
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
    }

    private func saveAs() async {
        working = true
        defer { working = false }
        message = nil
        switch await editor.pdfBytes() {
        case let .failure(f): message = f.text
        case let .success(data):
            let panel = NSSavePanel()
            panel.allowedContentTypes = [.pdf]
            panel.nameFieldStringValue = editor.suggestedFileName
            panel.canCreateDirectories = true
            guard panel.runModal() == .OK, let url = panel.url else { return }
            do { try data.write(to: url, options: .atomic) } catch { message = "The PDF could not be saved: \(error.localizedDescription)" }
        }
    }

    private func reveal() {
        message = nil
        guard let url = editor.pdfURL else { return }
        NSWorkspace.shared.activateFileViewerSelecting([url])
    }

    private func openInPreview() async {
        working = true
        defer { working = false }
        message = nil
        switch await editor.previewCopy() {
        case let .failure(f): message = f.text
        case let .success(url): if !NSWorkspace.shared.open(url) { message = "Preview did not open the resume." }
        }
    }
}
