import Foundation

// The Resume Studio editor's data, as pure types so the rules are tested without a window or a vani.
//
//   ResumePool    what `jobs resume show` says the owner may place (its `pool`)
//   ResumeDraft   the owner's working copy of a version's `selection`: sections, entries, bullets, overrides
//   ResumeDraft.issues(pool:)   the page-safety rules that are cheap to check here; the row they name is marked
//   ResumeDraft.editJSON()      the draft in the shape `jobs resume render` reads on stdin
//
// Go owns every rule (internal/rkbresume: checkSafe, checkOverrides, continuations.check); what is
// checked here only lets the editor mark a row before a round trip. A rule this file cannot check
// (a family of two framings of one piece of work, a bullet whose figure the validator does not back) is Go's
// to refuse, and its refusal is shown as it comes. Which sections reorder and where a heading may stay bare
// are Go's too, and the pool says them (`reorderable_sections`, `headings`): nothing here infers them.

// MARK: - JSON access

extension ResumeJSON {
    public subscript(key: String) -> ResumeJSON? {
        if case let .object(o) = self { return o[key] }
        return nil
    }

    public var stringValue: String? {
        if case let .string(s) = self { return s }
        return nil
    }

    public var arrayValue: [ResumeJSON]? {
        if case let .array(a) = self { return a }
        return nil
    }
}

// MARK: - Pool

/// What the owner may place: the eligible bullets by entry and where an entry may go (`show`'s `pool`,
/// rkbresume.PoolView). Built only from a reply that has the whole shape: a pool this app cannot read is
/// no pool, never an empty one.
public struct ResumePool: Equatable, Sendable {
    public struct Bullet: Equatable, Sendable, Identifiable {
        public var id: String
        public var text: String
        public var textHash: String
        /// The bullet this one must directly follow, under the same entry.
        public var follows: String?
    }

    public struct Entry: Equatable, Sendable, Identifiable {
        public var id: String
        public var title: String
        public var org: String
        public var dates: String
        /// The titles of the sections it may be placed in.
        public var sections: [String]
        public var bullets: [Bullet]
    }

    /// A place an entry may stay on the page with no bullets: every job, and an entry in a section whose base
    /// lists it bare (`keepsHeading`, the rule a build applies).
    public struct Heading: Hashable, Sendable {
        public var section: String
        public var entry: String
    }

    public var summary: [Bullet]
    public var summarySections: [String]
    public var entries: [Entry]
    /// The base selection's choice in the edit shape: "Reset to base" and a job's first draft.
    public var base: ResumeJSON?
    /// Bullet id -> the bullet it continues, over every entry and the summary.
    public let follows: [String: String]
    /// The sections whose order may change: the base's run of project sections (`reorderable_sections`). Go
    /// ignores the order of any other section, so none is offered.
    public let reorderableSections: Set<String>
    /// The same sections, in the base's order.
    public let reorderableOrder: [String]
    /// Where an entry may stay as a heading with no bullets (`headings`). Go refuses it anywhere else.
    public let headings: Set<Heading>

    /// A pool needs `reorderable_sections` and `headings` as well as its bullets and entries: a pool from a vani
    /// that does not say which sections reorder or where a heading may stay is no pool, never one that guesses.
    public init?(_ json: ResumeJSON?) {
        guard let json, case .object = json, let summaryJSON = json["summary"]?.arrayValue,
              let entriesJSON = json["entries"]?.arrayValue,
              let reorderableJSON = json["reorderable_sections"]?.arrayValue,
              let headingsJSON = json["headings"]?.arrayValue else { return nil }
        func bullets(_ list: [ResumeJSON]) -> [Bullet] {
            list.compactMap { b in
                guard let id = b["id"]?.stringValue, !id.isEmpty else { return nil }
                let follows = b["follows"]?.stringValue
                return Bullet(id: id, text: b["text"]?.stringValue ?? "", textHash: b["text_hash"]?.stringValue ?? "",
                              follows: (follows?.isEmpty ?? true) ? nil : follows)
            }
        }
        func strings(_ v: ResumeJSON?) -> [String] { (v?.arrayValue ?? []).compactMap(\.stringValue) }
        summary = bullets(summaryJSON)
        summarySections = strings(json["summary_sections"])
        entries = entriesJSON.compactMap { e in
            guard let id = e["id"]?.stringValue, !id.isEmpty else { return nil }
            return Entry(id: id, title: e["title"]?.stringValue ?? "", org: e["org"]?.stringValue ?? "", dates: e["dates"]?.stringValue ?? "",
                         sections: strings(e["sections"]), bullets: bullets(e["bullets"]?.arrayValue ?? []))
        }
        base = json["base"]
        var follows: [String: String] = [:]
        for b in summary + entries.flatMap(\.bullets) {
            if let f = b.follows { follows[b.id] = f }
        }
        self.follows = follows
        reorderableOrder = reorderableJSON.compactMap(\.stringValue)
        reorderableSections = Set(reorderableOrder)
        headings = Set(headingsJSON.compactMap { h in
            guard let s = h["section"]?.stringValue, let e = h["entry"]?.stringValue, !s.isEmpty, !e.isEmpty else { return nil }
            return Heading(section: s, entry: e)
        })
    }

    public func entry(_ id: String) -> Entry? { entries.first { $0.id == id } }

    /// Whether `entry` may stay in `section` with no bullets, as the pool says.
    public func mayStayAsHeading(_ entry: String, in section: String) -> Bool {
        headings.contains(Heading(section: section, entry: entry))
    }

    /// The wording of a pool bullet by id (the summary or any entry), nil when the pool does not offer it.
    public func bulletText(_ id: String) -> String? {
        if let b = summary.first(where: { $0.id == id }) { return b.text }
        for e in entries { if let b = e.bullets.first(where: { $0.id == id }) { return b.text } }
        return nil
    }

    /// Every section title the pool can fill, summary first, in the order the pool lists them.
    public var sectionTitles: [String] {
        var out: [String] = []
        for t in summarySections + entries.flatMap(\.sections) where !out.contains(t) { out.append(t) }
        return out
    }

    /// The pool bullet `id` of `place`, wherever it is listed.
    public func bullet(_ id: String, in place: ResumeDraft.Place) -> Bullet? {
        switch place {
        case .summary: summary.first { $0.id == id }
        case let .entry(e): entry(e)?.bullets.first { $0.id == id }
        }
    }
}

// MARK: - Overrides

/// The five fields of an entry an owner's edit may override (rkb's own selection keys).
public enum ResumeOverrideField: String, CaseIterable, Sendable {
    case title, org, stack, lead, dates

    /// Runes (Go counts `utf8.RuneCountInString`): title, org, stack 120; lead 300; dates 40.
    public var limit: Int {
        switch self {
        case .title, .org, .stack: 120
        case .lead: 300
        case .dates: 40
        }
    }

    public var label: String {
        switch self {
        case .title: "Title"
        case .org: "Organization"
        case .stack: "Stack"
        case .lead: "Lead line"
        case .dates: "Dates"
        }
    }
}

public struct ResumeOverrides: Equatable, Sendable {
    public var title = ""
    public var org = ""
    public var stack = ""
    public var lead = ""
    public var dates = ""

    public init(title: String = "", org: String = "", stack: String = "", lead: String = "", dates: String = "") {
        self.title = title
        self.org = org
        self.stack = stack
        self.lead = lead
        self.dates = dates
    }

    public var isEmpty: Bool { self == ResumeOverrides() }

    public subscript(field: ResumeOverrideField) -> String {
        get {
            switch field {
            case .title: title
            case .org: org
            case .stack: stack
            case .lead: lead
            case .dates: dates
            }
        }
        set {
            switch field {
            case .title: title = newValue
            case .org: org = newValue
            case .stack: stack = newValue
            case .lead: lead = newValue
            case .dates: dates = newValue
            }
        }
    }
}

/// `checkOverride` (internal/rkbresume/overrides.go) for one field: one plain printable line of the field's
/// length. Nil is fine (an empty field overrides nothing, and a lead of exactly "-" suppresses the lead).
public enum ResumeOverrideRules {
    static let forbidden = ["{{", "}}", "](", "*", "`", "<", ">"]

    public static func problem(_ field: ResumeOverrideField, _ value: String) -> String? {
        if value.isEmpty || (field == .lead && value == "-") { return nil }
        let name = field.label
        if value.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return "\(name) is only whitespace; leave it empty to override nothing."
        }
        if value.trimmingCharacters(in: .whitespacesAndNewlines) != value { return "\(name) has space around it." }
        if value.unicodeScalars.count > field.limit { return "\(name) is over \(field.limit) characters." }
        for s in value.unicodeScalars {
            if !isPrintable(s) { return "\(name) must be one printable line: it has a control, formatting or invisible character." }
            if field == .dates, !isDatesScalar(s) { return "Dates may hold only letters, digits, spaces and – - / , ." }
            if field == .lead, isNumber(s) { return "The lead may not hold a figure: figures on the page come only from verified claims." }
        }
        for bad in forbidden where value.contains(bad) {
            return "\(name) may not contain \(bad): rkb would render it."
        }
        return nil
    }

    /// Go's `unicode.IsPrint`: a letter, mark, number, punctuation or symbol, or the ASCII space.
    static func isPrintable(_ s: Unicode.Scalar) -> Bool {
        switch s.properties.generalCategory {
        case .spaceSeparator: s == " "
        case .lineSeparator, .paragraphSeparator, .control, .format, .surrogate, .privateUse, .unassigned: false
        default: true
        }
    }

    static func isLetter(_ s: Unicode.Scalar) -> Bool {
        switch s.properties.generalCategory {
        case .uppercaseLetter, .lowercaseLetter, .titlecaseLetter, .modifierLetter, .otherLetter: true
        default: false
        }
    }

    static func isNumber(_ s: Unicode.Scalar) -> Bool {
        switch s.properties.generalCategory {
        case .decimalNumber, .letterNumber, .otherNumber: true
        default: false
        }
    }

    static func isDatesScalar(_ s: Unicode.Scalar) -> Bool {
        isLetter(s) || s.properties.generalCategory == .decimalNumber || " –-/,.".unicodeScalars.contains(s)
    }
}

// MARK: - Draft

/// The owner's working copy of a version: what the outline shows and what Preview and Fit send.
public struct ResumeDraft: Equatable, Sendable {
    /// Where a bullet sits: a summary section, or an entry (an entry is on a page once, so its id is its place).
    public enum Place: Hashable, Sendable {
        case summary(String)
        case entry(String)
    }

    /// A bullet in an entry or the summary. A row that is not `included` stays in its place, dimmed, so
    /// turning it back on keeps its position; it is not written into the edit.
    public struct BulletRow: Equatable, Sendable, Identifiable {
        public var id: String
        public var included: Bool
        public init(id: String, included: Bool = true) {
            self.id = id
            self.included = included
        }
    }

    public struct EntryRow: Equatable, Sendable, Identifiable {
        public var id: String
        public var bullets: [BulletRow]
        public var overrides: ResumeOverrides
        public init(id: String, bullets: [BulletRow] = [], overrides: ResumeOverrides = ResumeOverrides()) {
            self.id = id
            self.bullets = bullets
            self.overrides = overrides
        }
    }

    public enum Kind: Equatable, Sendable {
        /// The summary: bullets only.
        case bullets
        /// Entries, each with bullets.
        case entries
    }

    public struct Section: Equatable, Sendable, Identifiable {
        public var title: String
        public var kind: Kind
        public var bullets: [BulletRow]
        public var entries: [EntryRow]
        /// The section was in the selection the draft was made from, so it is written even when emptied:
        /// Go keeps a listed section's place (a section the owner never touched stays off the page).
        public var listed: Bool
        public var id: String { title }

        public init(title: String, kind: Kind, bullets: [BulletRow] = [], entries: [EntryRow] = [], listed: Bool = true) {
            self.title = title
            self.kind = kind
            self.bullets = bullets
            self.entries = entries
            self.listed = listed
        }

        /// Nothing is on the page under this heading.
        public var isEmpty: Bool {
            switch kind {
            case .bullets: !bullets.contains { $0.included }
            case .entries: entries.isEmpty
            }
        }
    }

    public var sections: [Section]
    /// `jd_keywords` of the selection the draft was made from, passed through as they were (never invented).
    public var keywords: [String]

    public init(sections: [Section], keywords: [String] = []) {
        self.sections = sections
        self.keywords = keywords
    }

    // MARK: Reading a selection

    /// The draft of an edit-shape selection (a version's `selection`, or the pool's `base`). With a pool,
    /// the sections it can fill that the selection leaves out follow the listed ones, empty, so an entry
    /// can be added to them; without one the draft has only what the selection lists. Nil when the value
    /// is not a selection (no `sections` list).
    public static func parse(selection: ResumeJSON?, pool: ResumePool?) -> ResumeDraft? {
        guard let selection, let list = selection["sections"]?.arrayValue else { return nil }
        var sections: [Section] = []
        for s in list {
            guard let title = s["title"]?.stringValue, !title.isEmpty else { return nil }
            let entriesJSON = s["entries"]?.arrayValue
            let bulletsJSON = s["bullets"]?.arrayValue
            let kind: Kind
            if entriesJSON != nil { kind = .entries }
            else if bulletsJSON != nil { kind = .bullets }
            else { kind = (pool?.summarySections.contains(title) ?? false) ? .bullets : .entries }
            var section = Section(title: title, kind: kind)
            switch kind {
            case .bullets:
                section.bullets = (bulletsJSON ?? []).compactMap { $0.stringValue }.map { BulletRow(id: $0) }
            case .entries:
                for e in entriesJSON ?? [] {
                    guard let id = e["entry"]?.stringValue, !id.isEmpty else { return nil }
                    var o = ResumeOverrides()
                    for f in ResumeOverrideField.allCases { o[f] = e[f.rawValue]?.stringValue ?? "" }
                    section.entries.append(EntryRow(id: id, bullets: (e["bullets"]?.arrayValue ?? []).compactMap(\.stringValue).map { BulletRow(id: $0) },
                                                    overrides: o))
                }
            }
            sections.append(section)
        }
        if let pool {
            let have = Set(sections.map(\.title))
            for title in pool.sectionTitles where !have.contains(title) {
                sections.append(Section(title: title, kind: pool.summarySections.contains(title) ? .bullets : .entries, listed: false))
            }
        }
        let keywords = (selection["jd_keywords"]?.arrayValue ?? []).compactMap(\.stringValue)
        return ResumeDraft(sections: sections, keywords: keywords)
    }

    // MARK: Lookup

    public func section(_ title: String) -> Section? { sections.first { $0.title == title } }

    /// The section an entry is under.
    public func sectionTitle(ofEntry id: String) -> String? {
        sections.first { $0.entries.contains { $0.id == id } }?.title
    }

    public func entry(_ id: String) -> EntryRow? {
        for s in sections { if let e = s.entries.first(where: { $0.id == id }) { return e } }
        return nil
    }

    public func rows(in place: Place) -> [BulletRow]? {
        switch place {
        case let .summary(title): section(title).flatMap { $0.kind == .bullets ? $0.bullets : nil }
        case let .entry(id): entry(id)?.bullets
        }
    }

    /// Every bullet id on the page (included rows only).
    public var includedBulletIDs: [String] {
        sections.flatMap { s in s.bullets.filter(\.included).map(\.id) + s.entries.flatMap { $0.bullets.filter(\.included).map(\.id) } }
    }

    private func rowIDs(_ place: Place) -> Set<String> { Set((rows(in: place) ?? []).map(\.id)) }

    // MARK: Groups (a lead and the continuations that must stay directly after it)

    /// Row ranges, each a bullet with the continuations attached to it. Moving or switching off a lead
    /// acts on its whole group.
    public static func groups(_ rows: [BulletRow], follows: [String: String]) -> [Range<Int>] {
        var out: [Range<Int>] = []
        var start = 0
        for i in rows.indices where i > 0 {
            if follows[rows[i].id] == rows[i - 1].id { continue }
            out.append(start..<i)
            start = i
        }
        if !rows.isEmpty { out.append(start..<rows.count) }
        return out
    }

    // MARK: Editing

    private mutating func mutateRows<R>(_ place: Place, _ body: (inout [BulletRow]) -> R) -> R? {
        switch place {
        case let .summary(title):
            guard let i = sections.firstIndex(where: { $0.title == title && $0.kind == .bullets }) else { return nil }
            return body(&sections[i].bullets)
        case let .entry(id):
            for i in sections.indices {
                if let j = sections[i].entries.firstIndex(where: { $0.id == id }) { return body(&sections[i].entries[j].bullets) }
            }
            return nil
        }
    }

    /// Switch a bullet on or off. Switching a lead off switches off the continuations after it; switching a
    /// continuation on needs its lead on. Returns why when it refuses.
    @discardableResult
    public mutating func setIncluded(_ id: String, _ on: Bool, in place: Place, pool: ResumePool) -> String? {
        mutateRows(place) { rows -> String? in
            guard let i = rows.firstIndex(where: { $0.id == id }) else { return "That bullet is not here." }
            if on {
                if let lead = pool.follows[id], !(i > 0 && rows[i - 1].id == lead && rows[i - 1].included) {
                    return "This bullet continues the one before it. Switch that one on first."
                }
                rows[i].included = true
            } else {
                rows[i].included = false
                var j = i + 1
                while j < rows.count, pool.follows[rows[j].id] == rows[j - 1].id {
                    rows[j].included = false
                    j += 1
                }
            }
            return nil
        } ?? "That bullet is not here."
    }

    /// Move a group within its place: `from` and `toOffset` are group indices, the destination in the
    /// coordinates of the list before the move (as SwiftUI's `onMove` gives them).
    public mutating func moveGroup(in place: Place, from: Int, toOffset: Int, pool: ResumePool) {
        _ = mutateRows(place) { rows in
            let ranges = Self.groups(rows, follows: pool.follows)
            guard ranges.indices.contains(from), (0...ranges.count).contains(toOffset) else { return }
            var chunks = ranges.map { Array(rows[$0]) }
            let moved = chunks.remove(at: from)
            chunks.insert(moved, at: toOffset > from ? toOffset - 1 : toOffset)
            rows = chunks.flatMap { $0 }
        }
    }

    /// Put a pool bullet on the page under `place`. A bullet already switched off is switched on; a
    /// continuation goes directly after its lead, which must be on. Returns why when it refuses.
    @discardableResult
    public mutating func addBullet(_ id: String, to place: Place, pool: ResumePool) -> String? {
        guard pool.bullet(id, in: place) != nil else { return "Your knowledge base does not offer that bullet here." }
        if let rows = rows(in: place), rows.contains(where: { $0.id == id && $0.included }) { return "That bullet is already on the page." }
        if rowIDs(place).contains(id) { return setIncluded(id, true, in: place, pool: pool) }
        if let lead = pool.follows[id] {
            return mutateRows(place) { rows -> String? in
                guard let at = rows.firstIndex(where: { $0.id == lead && $0.included }) else {
                    return "This bullet continues another one. Add that one first."
                }
                rows.insert(BulletRow(id: id), at: at + 1)
                return nil
            } ?? "That place is not on the page."
        }
        return mutateRows(place) { rows -> String? in
            rows.append(BulletRow(id: id))
            return nil
        } ?? "That place is not on the page."
    }

    /// Replace a bullet (and the continuations attached to it) with another pool bullet, in the same spot.
    @discardableResult
    public mutating func swapBullet(_ old: String, for new: String, in place: Place, pool: ResumePool) -> String? {
        guard pool.bullet(new, in: place) != nil else { return "Your knowledge base does not offer that bullet here." }
        guard pool.follows[new] == nil else { return "That bullet continues another one; add it with its lead instead." }
        if rowIDs(place).contains(new) { return "That bullet is already on the page." }
        return mutateRows(place) { rows -> String? in
            guard let i = rows.firstIndex(where: { $0.id == old }) else { return "That bullet is not here." }
            var end = i + 1
            while end < rows.count, pool.follows[rows[end].id] == rows[end - 1].id { end += 1 }
            rows.replaceSubrange(i..<end, with: [BulletRow(id: new)])
            return nil
        } ?? "That bullet is not here."
    }

    /// Put a pool entry in a section, with its first bullet that is not a continuation (an entry with no
    /// bullet is only allowed for a job). Returns why when it refuses.
    @discardableResult
    public mutating func addEntry(_ id: String, to title: String, pool: ResumePool) -> String? {
        guard let e = pool.entry(id), e.sections.contains(title) else { return "That entry does not go in this section." }
        guard entry(id) == nil else { return "That entry is already on the page." }
        guard let i = sections.firstIndex(where: { $0.title == title && $0.kind == .entries }) else { return "That section is not on this page." }
        let used = Set(sections.flatMap { s in s.bullets.map(\.id) + s.entries.flatMap { $0.bullets.map(\.id) } })
        let first = e.bullets.first { $0.follows == nil && !used.contains($0.id) }
        sections[i].entries.append(EntryRow(id: id, bullets: first.map { [BulletRow(id: $0.id)] } ?? []))
        return nil
    }

    public mutating func removeEntry(_ id: String) {
        for i in sections.indices { sections[i].entries.removeAll { $0.id == id } }
    }

    /// Move an entry one step up or down in its section.
    public mutating func moveEntry(_ id: String, by delta: Int) {
        for i in sections.indices {
            guard let j = sections[i].entries.firstIndex(where: { $0.id == id }) else { continue }
            let k = j + delta
            guard sections[i].entries.indices.contains(k) else { return }
            sections[i].entries.swapAt(j, k)
            return
        }
    }

    /// Move a section past the next reorderable one above or below it.
    public mutating func moveSection(_ title: String, by delta: Int, among movable: Set<String>) {
        guard movable.contains(title) else { return }
        let slots = sections.indices.filter { movable.contains(sections[$0].title) }
        guard let p = slots.firstIndex(where: { sections[$0].title == title }) else { return }
        let q = p + (delta < 0 ? -1 : 1)
        guard delta != 0, slots.indices.contains(q) else { return }
        sections.swapAt(slots[p], slots[q])
    }

    public mutating func setOverride(_ field: ResumeOverrideField, _ value: String, entry id: String) {
        for i in sections.indices {
            if let j = sections[i].entries.firstIndex(where: { $0.id == id }) {
                sections[i].entries[j].overrides[field] = value
                return
            }
        }
    }

    // MARK: What the pool still offers

    /// The pool's bullets for a place that have no row there yet (switched off ones are rows, and are
    /// switched back on in place), in the pool's order.
    public func candidates(in place: Place, pool: ResumePool) -> [ResumePool.Bullet] {
        let have = rowIDs(place)
        let all: [ResumePool.Bullet]
        switch place {
        case .summary: all = pool.summary
        case let .entry(id): all = pool.entry(id)?.bullets ?? []
        }
        return all.filter { !have.contains($0.id) }
    }

    /// The pool's entries that may go in the section and are not on the page yet.
    public func addableEntries(in title: String, pool: ResumePool) -> [ResumePool.Entry] {
        pool.entries.filter { $0.sections.contains(title) && entry($0.id) == nil }
    }

    // MARK: Issues

    public struct Issue: Equatable, Sendable, Identifiable {
        public enum Target: Hashable, Sendable {
            case page
            case section(String)
            case entry(String)
            case bullet(String)
            case override(entry: String, field: ResumeOverrideField)
        }

        public var target: Target
        public var message: String
        /// False for a rule this file can only approximate: it is shown, and Go decides.
        public var blocking = true
        public var id: String { "\(target)|\(message)" }

        public init(target: Target, message: String, blocking: Bool = true) {
            self.target = target
            self.message = message
            self.blocking = blocking
        }
    }

    /// What Go would refuse that is cheap to see here, each on the row it is about. With no pool only the
    /// rules that need none are checked (an override, an id used twice, an empty page). A bullet or entry
    /// that is switched off is not on the page and is not checked.
    public func issues(pool: ResumePool?) -> [Issue] {
        var out: [Issue] = []
        func add(_ t: Issue.Target, _ m: String, blocking: Bool = true) { out.append(Issue(target: t, message: m, blocking: blocking)) }
        var seenBullets: Set<String> = []
        var seenEntries: Set<String> = []
        var any = false

        func checkBullets(_ rows: [BulletRow], place: Place) {
            var previous: String?
            for row in rows where row.included {
                any = true
                if !seenBullets.insert(row.id).inserted { add(.bullet(row.id), "This bullet is on the page twice; each bullet may be used once.") }
                if let pool {
                    if pool.bullet(row.id, in: place) == nil {
                        add(.bullet(row.id), "Your knowledge base does not offer this bullet here any more.")
                    }
                    if let lead = pool.follows[row.id], previous != lead {
                        add(.bullet(row.id), "This bullet continues another one and must come directly after it.")
                    }
                }
                previous = row.id
            }
        }

        for s in sections {
            switch s.kind {
            case .bullets:
                if let pool, s.bullets.contains(where: \.included), !pool.summarySections.contains(s.title) {
                    add(.section(s.title), "This section does not take bullets from your knowledge base.")
                }
                checkBullets(s.bullets, place: .summary(s.title))
            case .entries:
                for e in s.entries {
                    any = true
                    if !seenEntries.insert(e.id).inserted { add(.entry(e.id), "This entry is on the page twice.") }
                    for f in ResumeOverrideField.allCases {
                        if let p = ResumeOverrideRules.problem(f, e.overrides[f]) { add(.override(entry: e.id, field: f), p) }
                    }
                    if let pool {
                        let bare = !e.bullets.contains(where: \.included)
                        if let pe = pool.entry(e.id) {
                            if !pe.sections.contains(s.title) { add(.entry(e.id), "This entry does not go in \(s.title).") }
                            if bare, !pool.mayStayAsHeading(e.id, in: s.title) {
                                add(.entry(e.id), "This entry has no bullets. Only a job, or an entry your base resume lists with none, may stay as a heading.")
                            }
                        } else if !(bare && pool.mayStayAsHeading(e.id, in: s.title)) {
                            // An entry the base lists bare has no eligible bullet, so the pool has no entry for it;
                            // it is on offer as a heading, and only as one.
                            add(.entry(e.id), "Your knowledge base does not offer this entry any more.")
                        }
                    }
                    checkBullets(e.bullets, place: .entry(e.id))
                }
            }
        }
        if !any { add(.page, "Nothing is on the page. Add a bullet or an entry.") }
        return out
    }

    // MARK: Writing the edit

    private struct WireEntry: Encodable {
        var entry: String
        var bullets: [String]
        var title: String?
        var stack: String?
        var lead: String?
        var org: String?
        var dates: String?
    }

    private struct WireSection: Encodable {
        var title: String
        var bullets: [String]?
        var entries: [WireEntry]?
    }

    private struct Wire: Encodable {
        var sections: [WireSection]
        var jd_keywords: [String]?
    }

    /// The edit as `jobs resume render` reads it: only `sections` (each `title` and its `bullets` or
    /// `entries`, each entry with its `bullets` and any override) and `jd_keywords`. A section is written
    /// when it was in the selection the draft came from or has something on it; an entry that stays as a
    /// heading lists no bullets.
    public func editJSON() throws -> Data {
        var wire = Wire(sections: [], jd_keywords: keywords.isEmpty ? nil : keywords)
        for s in sections where s.listed || !s.isEmpty {
            switch s.kind {
            case .bullets:
                let ids = s.bullets.filter(\.included).map(\.id)
                wire.sections.append(WireSection(title: s.title, bullets: ids.isEmpty ? nil : ids, entries: nil))
            case .entries:
                let entries = s.entries.map { e -> WireEntry in
                    func over(_ f: ResumeOverrideField) -> String? { e.overrides[f].isEmpty ? nil : e.overrides[f] }
                    return WireEntry(entry: e.id, bullets: e.bullets.filter(\.included).map(\.id), title: over(.title), stack: over(.stack),
                                     lead: over(.lead), org: over(.org), dates: over(.dates))
                }
                wire.sections.append(WireSection(title: s.title, bullets: nil, entries: entries.isEmpty ? nil : entries))
            }
        }
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        return try encoder.encode(wire)
    }
}

// MARK: - Keywords and names

public enum ResumeKeywordHits {
    /// `text` cut into runs, each marked when it is (part of) a keyword hit: every case-insensitive
    /// occurrence of any term, overlaps merged. The runs joined are `text`.
    public static func segments(in text: String, terms: [String]) -> [(text: String, hit: Bool)] {
        let ns = text as NSString
        var ranges: [NSRange] = []
        for t in terms where !t.isEmpty {
            var from = 0
            while from < ns.length {
                let r = ns.range(of: t, options: [.caseInsensitive, .diacriticInsensitive], range: NSRange(location: from, length: ns.length - from))
                if r.location == NSNotFound || r.length == 0 { break }
                ranges.append(r)
                from = r.location + r.length
            }
        }
        ranges.sort { $0.location < $1.location }
        var merged: [NSRange] = []
        for r in ranges {
            if let last = merged.last, r.location <= last.location + last.length {
                merged[merged.count - 1].length = max(last.length, r.location + r.length - last.location)
            } else {
                merged.append(r)
            }
        }
        var out: [(text: String, hit: Bool)] = []
        var at = 0
        for r in merged {
            if r.location > at { out.append((ns.substring(with: NSRange(location: at, length: r.location - at)), false)) }
            out.append((ns.substring(with: r), true))
            at = r.location + r.length
        }
        if at < ns.length { out.append((ns.substring(from: at), false)) }
        return out
    }

    /// The posting's keywords a bullet's text contains (case-insensitive substring, in the order given,
    /// each once). Empty terms match nothing.
    public static func hits(in text: String, terms: [String]) -> [String] {
        var out: [String] = []
        for t in terms where !t.isEmpty && !out.contains(where: { $0.caseInsensitiveCompare(t) == .orderedSame }) {
            if text.range(of: t, options: [.caseInsensitive, .diacriticInsensitive]) != nil { out.append(t) }
        }
        return out
    }
}

extension ResumeWords {
    /// The file a version's PDF is named for the owner: `Resume_<Company>.pdf` (vani's `resumeFileName`,
    /// cmd/vani/resumecmd.go: runs of letters and digits joined by `_`, at most 60 characters), `Resume.pdf`
    /// when the company has none.
    public static func resumeFileName(company: String) -> String {
        var words: [String] = []
        var word = ""
        for s in company.unicodeScalars {
            if ResumeOverrideRules.isLetter(s) || s.properties.generalCategory == .decimalNumber {
                word.unicodeScalars.append(s)
            } else if !word.isEmpty {
                words.append(word)
                word = ""
            }
        }
        if !word.isEmpty { words.append(word) }
        var joined = Array(words.joined(separator: "_").unicodeScalars)
        if joined.count > 60 {
            joined = Array(joined.prefix(60))
            while joined.last == "_" { joined.removeLast() }
        }
        guard !joined.isEmpty else { return "Resume.pdf" }
        var name = "Resume_"
        name.unicodeScalars.append(contentsOf: joined)
        return name + ".pdf"
    }

    /// The cost of a version in a chip: "$0.031", "at least $0.031" when a turn's cost was unknown, nil when
    /// none was spent or reported (unknown is never "$0").
    public static func costChip(_ cost: ResumeCost?) -> String? {
        guard let cost else { return nil }
        return costPhrase(cost)
    }
}
