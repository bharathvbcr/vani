import Foundation

// The Studio's redraft form, versions list, comparison and coverage view, as pure data: what to ask vani for,
// and how its replies read in words. No window, no vani. Go owns every rule; what is checked here only lets
// the sheet say so before a round trip, and only for what Go certainly refuses.

// MARK: - Redraft rules

/// The instruction and the names of a redraft, held to the bounds Go holds them to
/// (internal/rkbresume/redraft.go: 500 characters, one printable line; a name is at most 120 characters;
/// at most 16 locks). The phone and email screen is Go's alone: its refusal is shown as it comes.
public enum ResumeRedraftRules {
    public static let maxInstruction = 500
    public static let maxName = 120
    public static let maxLocks = 16

    /// The characters Go counts: the trimmed text's Unicode scalars (`utf8.RuneCountInString` after `TrimSpace`).
    public static func instructionCount(_ text: String) -> Int {
        text.trimmingCharacters(in: .whitespacesAndNewlines).unicodeScalars.count
    }

    /// "62 / 500".
    public static func counter(_ text: String) -> String { "\(instructionCount(text)) / \(maxInstruction)" }

    /// Why the instruction cannot be sent, or nil. Whitespace only is no instruction (it is not sent).
    public static func instructionProblem(_ text: String) -> String? {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if t.isEmpty { return nil }
        if t.unicodeScalars.count > maxInstruction {
            return "The instruction is \(t.unicodeScalars.count) characters, over \(maxInstruction)."
        }
        if t.unicodeScalars.contains(where: { !ResumeOverrideRules.isPrintable($0) }) {
            return "The instruction must be one printable line: it has a control, formatting or invisible character."
        }
        return nil
    }

    /// Why a scope or a lock name cannot be sent, or nil.
    public static func nameProblem(_ what: String, _ name: String) -> String? {
        let t = name.trimmingCharacters(in: .whitespacesAndNewlines)
        if t.isEmpty { return "\(what) is empty." }
        if t.unicodeScalars.count > maxName { return "\(what) is over \(maxName) characters." }
        if t.unicodeScalars.contains(where: { !ResumeOverrideRules.isPrintable($0) }) { return "\(what) must be one printable line." }
        return nil
    }

    public static func namesProblem(scope: String?, lock: [String]) -> String? {
        if let scope, !scope.isEmpty, let p = nameProblem("The scope", scope) { return p }
        if lock.count > maxLocks { return "More than \(maxLocks) sections are locked." }
        for l in lock { if let p = nameProblem("A lock", l) { return p } }
        return nil
    }
}

/// What a redraft may be scoped to or lock, from the shown version and the pool: the sections the model fills
/// (the pool's section titles, so a lines or education section is not offered) and the entries on the page.
public struct ResumeRedraftTargets: Equatable, Sendable {
    public struct Entry: Equatable, Sendable, Identifiable {
        public var id: String
        public var title: String
    }

    public struct Section: Equatable, Sendable, Identifiable {
        public var title: String
        public var entries: [Entry]
        public var id: String { title }
    }

    public var sections: [Section]

    /// Nil without a pool or a readable selection: what the model fills is then not known, and a redraft is not offered.
    public static func of(selection: ResumeJSON?, pool: ResumePool?) -> ResumeRedraftTargets? {
        guard let pool, let list = selection?["sections"]?.arrayValue else { return nil }
        let fillable = Set(pool.sectionTitles)
        var out: [Section] = []
        for s in list {
            guard let title = s["title"]?.stringValue, fillable.contains(title), !out.contains(where: { $0.title == title }) else { continue }
            let entries = (s["entries"]?.arrayValue ?? []).compactMap { e -> Entry? in
                guard let id = e["entry"]?.stringValue, !id.isEmpty else { return nil }
                let shown = e["title"]?.stringValue.flatMap { $0.isEmpty ? nil : $0 } ?? pool.entry(id)?.title ?? id
                return Entry(id: id, title: shown)
            }
            out.append(Section(title: title, entries: entries))
        }
        return out.isEmpty ? nil : ResumeRedraftTargets(sections: out)
    }

    public func section(containingEntry id: String) -> String? { sections.first { $0.entries.contains { $0.id == id } }?.title }
}

/// The redraft sheet's fields. `request` is what is sent; `problems` is what stops it before a round trip.
public struct ResumeRedraftForm: Equatable, Sendable {
    public enum Scope: Hashable, Sendable {
        case whole
        case section(String)
        case entry(String)
    }

    public var scope: Scope = .whole
    public var locked: Set<String> = []
    public var instruction = ""

    public init(scope: Scope = .whole, locked: Set<String> = [], instruction: String = "") {
        self.scope = scope
        self.locked = locked
        self.instruction = instruction
    }

    /// The section the scope is in, when there is a scope.
    public func scopedSection(_ targets: ResumeRedraftTargets) -> String? {
        switch scope {
        case .whole: nil
        case let .section(t): t
        case let .entry(id): targets.section(containingEntry: id)
        }
    }

    /// A scoped section is the one being re-picked: it cannot also be kept.
    public func isLockable(_ title: String, _ targets: ResumeRedraftTargets) -> Bool { scopedSection(targets) != title }

    /// The locks that are sent: in the page's order, without the scoped section.
    public func effectiveLocks(_ targets: ResumeRedraftTargets) -> [String] {
        targets.sections.map(\.title).filter { locked.contains($0) && isLockable($0, targets) }
    }

    public func request(from version: Int, targets: ResumeRedraftTargets) -> ResumeRedraft {
        let name: String? = switch scope {
        case .whole: nil
        case let .section(t): t
        case let .entry(id): id
        }
        return ResumeRedraft(from: version, scope: name, lock: effectiveLocks(targets), instruction: instruction)
    }

    /// Why the redraft cannot be asked for yet, each in a sentence.
    public func problems(_ targets: ResumeRedraftTargets) -> [String] {
        var out: [String] = []
        if let p = ResumeRedraftRules.instructionProblem(instruction) { out.append(p) }
        switch scope {
        case .whole: break
        case let .section(t):
            if !targets.sections.contains(where: { $0.title == t }) { return out + ["The section to re-pick is not on this version."] }
        case let .entry(id):
            if targets.section(containingEntry: id) == nil { return out + ["The entry to re-pick is not on this version."] }
        }
        // A scope leaves its own section open, so only a page with no scope can have every section locked.
        if effectiveLocks(targets).count == targets.sections.count {
            out.append("Every section is locked, so there is nothing to redraft.")
        }
        return out
    }
}

// MARK: - Versions

/// One line of the versions list.
public struct ResumeVersionRow: Equatable, Sendable, Identifiable {
    public var version: Int
    /// "v3 · tailored · 2 pages".
    public var title: String
    public var created: Date
    /// "current", "in use".
    public var badges: [String]
    /// The version it was made from, 0 for none.
    public var parent: Int
    /// "re-picked only GPU Systems · kept Summary, Experience"; nil when it kept nothing.
    public var kept: String?
    public var instruction: ResumeInstructionLine?
    public var id: Int { version }
}

/// The instruction a version shows: its own, or, for a page no model shaped (an edit or a revalidation), the one
/// of the version it came from, said as "from v3: ...". Never invented: a version that was asked with none shows none.
public struct ResumeInstructionLine: Equatable, Sendable {
    public var text: String
    /// The version the instruction was given to, when it is not this one.
    public var from: Int?

    /// "prefer the tracing work" or "from v3: prefer the tracing work".
    public var display: String { from.map { "from v\($0): \(text)" } ?? text }
}

public enum ResumeVersions {
    /// A version no model shaped: its page is the owner's edit or a revalidation of another's, so it keeps no
    /// instruction, scope or lock of its own (docs/RUNTIME.md).
    static func isDerived(_ source: String) -> Bool { source == "edited" || source == "revalidated" }

    /// `v`'s instruction: its own, else for a derived page the nearest ancestor's (named). Bounded by the list.
    public static func instruction(of v: ResumeVersion, in all: [ResumeVersion]) -> ResumeInstructionLine? {
        if let own = v.instruction, !own.isEmpty { return ResumeInstructionLine(text: own, from: nil) }
        guard isDerived(v.source) else { return nil }
        let byNumber = Dictionary(all.map { ($0.version, $0) }, uniquingKeysWith: { a, _ in a })
        var at = v
        for _ in 0..<all.count {
            guard at.parent >= 1, at.parent < at.version, let p = byNumber[at.parent] else { return nil }
            if let text = p.instruction, !text.isEmpty { return ResumeInstructionLine(text: text, from: p.version) }
            guard isDerived(p.source) else { return nil }
            at = p
        }
        return nil
    }

    /// "re-picked only GPU Systems · kept Summary, Experience", nil when nothing was kept.
    public static func keptLine(scope: String?, lock: [String]) -> String? {
        var parts: [String] = []
        if let scope, !scope.isEmpty { parts.append("re-picked only \(ResumeWords.clipped(scope, 60))") }
        if !lock.isEmpty { parts.append("kept " + lock.map { ResumeWords.clipped($0, 40) }.joined(separator: ", ")) }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    /// The versions, newest first.
    public static func rows(_ versions: [ResumeVersion]) -> [ResumeVersionRow] {
        versions.sorted { $0.version > $1.version }.map { v in
            var badges: [String] = []
            if v.current { badges.append("current") }
            if v.used { badges.append("in use") }
            return ResumeVersionRow(
                version: v.version,
                title: ["v\(v.version)", ResumeWords.source(v.source), ResumeWords.pagesPhrase(v.pages)].joined(separator: " · "),
                created: v.created, badges: badges, parent: v.parent,
                kept: keptLine(scope: v.scope, lock: v.lock), instruction: instruction(of: v, in: versions))
        }
    }

    /// The versions a version can be compared with: every other one, newest first.
    public static func comparable(to version: Int, in versions: [ResumeVersion]) -> [Int] {
        versions.map(\.version).filter { $0 != version }.sorted(by: >)
    }
}

// MARK: - Comparison

/// A bullet in a comparison: its text from the pool when the pool offers it, its id otherwise.
public struct ResumeDiffBullet: Equatable, Sendable, Identifiable {
    public var id: String
    /// The wording the pool offers now; nil when it does not (the id is then all there is).
    public var text: String?
    /// "Experience · ML Engineer · #2".
    public var place: String
}

public struct ResumeDiffMoved: Equatable, Sendable, Identifiable {
    public var id: String
    public var text: String?
    /// "Projects · Project A · #1 → Open Source · Project B · #3".
    public var change: String
}

public struct ResumeDiffView: Equatable, Sendable {
    public var title: String
    /// One line a reader can stop at.
    public var headline: String
    /// How each side was made: "v2 · edited by hand", "v5 · tailored · re-picked only ... · from v3: ...".
    public var fromLine: String
    public var toLine: String
    public var same: Bool
    public var added: [ResumeDiffBullet]
    public var removed: [ResumeDiffBullet]
    public var moved: [ResumeDiffMoved]
    /// "ML Engineer · Title: (no override) → Principal Engineer".
    public var overrides: [String]
    /// "Summary, Experience, Open Source → Summary, Open Source, Experience".
    public var sectionOrder: String?
    public var keywordsAdded: [String]
    public var keywordsRemoved: [String]
}

public enum ResumeDiffPresentation {
    public static func make(_ d: ResumeDiff, pool: ResumePool?, versions: [ResumeVersion]) -> ResumeDiffView {
        func entryName(_ id: String?) -> String? {
            guard let id else { return nil }
            return pool?.entry(id)?.title ?? id
        }
        func place(_ section: String, _ entry: String?, _ index: Int) -> String {
            ([section] + [entryName(entry)].compactMap { $0 } + ["#\(index + 1)"]).joined(separator: " · ")
        }
        func text(_ id: String) -> String? { pool?.bulletText(id) }
        let added = d.bullets.added.map { ResumeDiffBullet(id: $0.id, text: text($0.id), place: place($0.section, $0.entry, $0.index)) }
        let removed = d.bullets.removed.map { ResumeDiffBullet(id: $0.id, text: text($0.id), place: place($0.section, $0.entry, $0.index)) }
        let moved = d.bullets.moved.map {
            ResumeDiffMoved(id: $0.id, text: text($0.id),
                            change: place($0.from.section, $0.from.entry, $0.from.index) + " → " + place($0.to.section, $0.to.entry, $0.to.index))
        }
        let overrides = d.overrides.map { o -> String in
            let field = ResumeOverrideField(rawValue: o.field)?.label ?? o.field
            func side(_ s: String) -> String { s.isEmpty ? "(no override)" : s }
            return "\(entryName(o.entry) ?? o.entry) · \(field): \(side(o.from)) → \(side(o.to))"
        }
        let order = d.sectionOrder.map { $0.from.joined(separator: ", ") + " → " + $0.to.joined(separator: ", ") }
        let counts = [(added.count, "added"), (removed.count, "removed"), (moved.count, "moved"), (overrides.count, "override\(overrides.count == 1 ? "" : "s") changed"),
                      (d.sectionOrder == nil ? 0 : 1, "section order changed"),
                      (d.keywords.added.count + d.keywords.removed.count, "keyword\(d.keywords.added.count + d.keywords.removed.count == 1 ? "" : "s") changed")]
            .filter { $0.0 > 0 }.map { "\($0.0) \($0.1)" }
        let headline = d.same
            ? "No difference in bullets, overrides, section order or keywords. The two versions can still have been made differently."
            : "v\(d.from.version) to v\(d.to.version): " + counts.joined(separator: ", ")
        func side(_ s: ResumeDiff.Side) -> String {
            var parts = ["v\(s.version)", ResumeWords.source(s.source)]
            if let k = ResumeVersions.keptLine(scope: s.scope, lock: s.lock) { parts.append(k) }
            if let own = s.instruction {
                parts.append("asked: \(ResumeWords.clipped(own, 140))")
            } else if let v = versions.first(where: { $0.version == s.version }), let line = ResumeVersions.instruction(of: v, in: versions) {
                parts.append("asked \(ResumeWords.clipped(line.display, 160))")
            }
            return parts.joined(separator: " · ")
        }
        return ResumeDiffView(title: "v\(d.from.version) → v\(d.to.version)", headline: headline, fromLine: side(d.from), toLine: side(d.to), same: d.same,
                              added: added, removed: removed, moved: moved, overrides: overrides, sectionOrder: order,
                              keywordsAdded: d.keywords.added, keywordsRemoved: d.keywords.removed)
    }
}

// MARK: - Coverage

public struct ResumeCoverageHitRow: Equatable, Sendable, Identifiable {
    public struct Bullet: Equatable, Sendable, Identifiable {
        public var id: String
        public var text: String?
    }

    public var term: String
    public var count: Int
    /// The bullets that cover it, at most 40 (`count` says how many in all).
    public var bullets: [Bullet]
    public var id: String { term }
}

public struct ResumeCoverageView: Equatable, Sendable {
    public var headline: String
    /// What was measured: "v3 as saved: your unsaved edits are not counted.", or the base selection.
    public var scopeLine: String
    public var sourceLine: String
    /// Notes that change how to read the numbers: the posting's keywords could not be read, terms past the cap.
    public var notes: [String]
    public var hits: [ResumeCoverageHitRow]
    /// Missed, but a swap could fix it.
    public var swappable: [String]
    /// Missed, and no eligible bullet says it: the bank lacks it.
    public var uncovered: [String]
    public var unjudged: [String]
    public var unresolved: [String]
    public var gapMissing: [String]
}

public enum ResumeCoveragePresentation {
    public static func make(_ c: ResumeCoverage, pool: ResumePool?, dirty: Bool) -> ResumeCoverageView {
        let what = c.base ? "your base selection" : "v\(c.version ?? 0)"
        let judged = c.judged
        let headline = judged == 0
            ? "No keyword could be judged, so there is no coverage to report."
            : "\(what) covers \(c.hit.count) of \(judged) judged keywords."
        let scopeLine = c.base
            ? "Measured on your base selection: this job has no saved version yet."
            : "Measured on v\(c.version ?? 0) as saved." + (dirty ? " Your unsaved edits are not counted." : "")
        var notes: [String] = []
        if let e = c.keywordsError { notes.append(ResumeWords.clipped(e)) }
        if c.termsTruncated { notes.append("\(c.termsTotal) terms were asked about and only the first 200 were judged. The rest are in no list below.") }
        if !c.unjudged.isEmpty { notes.append("\(c.unjudged.count) phrase\(c.unjudged.count == 1 ? "" : "s") could not be matched at all. They are not counted as misses.") }
        let hits = c.hit.map { h in
            ResumeCoverageHitRow(term: h.term, count: h.n, bullets: h.bullets.map { .init(id: $0, text: pool?.bulletText($0)) })
        }
        return ResumeCoverageView(headline: headline, scopeLine: scopeLine, sourceLine: sourceLine(c.keywordsSource), notes: notes, hits: hits,
                                  swappable: c.swappable, uncovered: c.uncovered, unjudged: c.unjudged, unresolved: c.unresolved, gapMissing: c.gapMissing)
    }

    /// Whose terms were judged, in words.
    public static func sourceLine(_ source: String) -> String {
        switch source {
        case "posting": return "Judged on the posting's keywords."
        case "jd_keywords": return "Judged on the keywords Opus took from the posting (the posting's own could not be read)."
        case "posting+jd_keywords": return "Judged on the posting's keywords and the ones Opus took from it."
        case "none": return "No keywords were available to judge."
        default: return "Judged on keywords this app does not recognise (\(ResumeWords.clipped(source, 40)))."
        }
    }
}

// MARK: - Hand-off

extension ResumeWords {
    /// What a `gaps` reply says happened. Only `written: true` is worded as sent: a dry run, a job with nothing
    /// to hand over and a retry that adds nothing all say `written: false`, and each is worded as what it is.
    public static func handOff(_ g: ResumeGaps) -> String {
        let n = g.terms.count
        let cut = g.termsTruncated ? " Only the first terms were kept: the rest were over the hand-off's bound." : ""
        let file = g.file.map { URL(fileURLWithPath: $0).lastPathComponent } ?? "the hand-off file"
        if g.written {
            return "Sent \(n) term\(n == 1 ? "" : "s") to your career session: written to \(file) in Vani's resumes folder. It reads them from there." + cut
        }
        if g.dryRun {
            return n == 0 ? "Nothing to hand over." + (g.note.map { " " + clipped($0, 200) } ?? "")
                          : "\(n) term\(n == 1 ? "" : "s") would be written. Nothing has been written yet." + cut
        }
        if g.unchanged { return "Nothing was added: the file's newest line for this job already holds exactly these terms." }
        return "Nothing was written." + (g.note.map { " " + clipped($0, 200) } ?? "")
    }
}

// MARK: - Orphan last lines

public enum ResumeOrphans {
    /// The bullets rkb's fit report named with an orphan last line. They are not a structured field of the
    /// version: vani writes them into a note as `... (orphan b1, orphan b2, long b3)` (cmd, internal/rkbresume
    /// `fitIDs`, at most 8 of each kind), so they are read back from the note text and only ids that are on the
    /// page count. A note that names more than 8 is cut by vani, so this can miss some, never invent one.
    public static func ids(inNotes notes: [String], bullets: [ResumeBulletRef]) -> Set<String> {
        let onPage = Set(bullets.map(\.id))
        var out: Set<String> = []
        for note in notes {
            var rest = Substring(note)
            while let r = rest.range(of: "orphan ") {
                rest = rest[r.upperBound...]
                let id = rest.prefix { !$0.isWhitespace && $0 != "," && $0 != ")" }
                if onPage.contains(String(id)) { out.insert(String(id)) }
            }
        }
        return out
    }

    /// What the owner is told on the bullet.
    public static let words = "ends with a one-word line"
}

// MARK: - Quitting with unsaved edits

public struct ResumeQuitPrompt: Equatable, Sendable {
    public var title: String
    public var message: String
    public var quit: String
    public var stay: String
}

public enum ResumeQuit {
    /// What to ask when the app is asked to quit: nil when no Studio window holds unsaved edits. One question
    /// for all of them, naming the jobs.
    public static func prompt(jobs: [String]) -> ResumeQuitPrompt? {
        let names = jobs.map { ResumeWords.clipped($0, 80) }.filter { !$0.isEmpty }
        guard !jobs.isEmpty else { return nil }
        let shown = names.prefix(8).joined(separator: "\n")
        let more = jobs.count > 8 ? "\nand \(jobs.count - 8) more" : ""
        let one = jobs.count == 1
        return ResumeQuitPrompt(
            title: one ? "Quit and discard your unsaved resume edits?" : "Quit and discard unsaved resume edits in \(jobs.count) windows?",
            message: "These Resume Studio windows have edits that were not fitted, so they are not saved as a version:\n\(shown)\(more)\nQuitting discards them. This cannot be undone.",
            quit: "Quit and Discard", stay: "Keep Editing")
    }
}
