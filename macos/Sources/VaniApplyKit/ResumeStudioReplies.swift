import Foundation

// The replies and the request added with redraft, version comparison, restore and coverage
// (docs/RUNTIME.md "Resume Studio" and "Coverage"; cmd/vani/jobsresume{,diff,coverage}.go):
//
//   build --from vN --scope NAME --lock NAME ... --instruction TEXT    a redraft (ResumeRedraft)
//   diff KEY vA vB                                                     what differs between two stored versions
//   current KEY vN                                                     make a stored version the current one
//   coverage KEY [vN]                                                  which posting keywords a version covers
//   gaps [--dry-run] KEY                                               hand the keywords no bullet covers to the career session
//
// Every reply decodes strictly where a missing field would turn "did not run" or "nothing differs" into a
// good answer (a diff's lists and `same`, a coverage's lists and totals, a hand-off's `written`), and
// leniently everywhere else (unknown fields are ignored). All of them are Codable, so the shapes round-trip.

// MARK: - Redraft request

/// What a redraft asks of `build`: the version it starts from and, optionally, a scope, locks and the
/// owner's one-line instruction. `from` is always named: the Studio redrafts the version it shows.
public struct ResumeRedraft: Equatable, Sendable {
    public var from: Int
    /// A section title or an entry id of that version: only that part is re-picked. Nil re-picks the page.
    public var scope: String?
    /// Section titles kept as they are.
    public var lock: [String]
    /// The owner's preference, one line of at most 500 characters.
    public var instruction: String?

    public init(from: Int, scope: String? = nil, lock: [String] = [], instruction: String? = nil) {
        self.from = from
        self.scope = scope
        self.lock = lock
        self.instruction = instruction
    }

    /// A scope or a lock keeps parts of the version, which the base fallback would not.
    public var keeps: Bool { (scope?.isEmpty == false) || !lock.isEmpty }

    /// The text is trimmed and an empty one is no instruction, as Go does.
    var trimmedInstruction: String? {
        let t = instruction?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return t.isEmpty ? nil : t
    }

    /// The flags, in the `--flag=value` form so a value that starts with a dash is never read as a flag.
    var arguments: [String] {
        var out = ["--from=v\(from)"]
        if let scope, !scope.isEmpty { out.append("--scope=\(scope)") }
        for l in lock { out.append("--lock=\(l)") }
        if let text = trimmedInstruction { out.append("--instruction=\(text)") }
        return out
    }
}

// MARK: - diff

/// `diff KEY vA vB`: what a stored version `to` changed against `from`. Every list is present, and a
/// reply without one is unreadable, never "nothing differs".
public struct ResumeDiff: Codable, Equatable, Sendable {
    public struct Side: Codable, Equatable, Sendable {
        public var version: Int
        public var source: String
        public var parent: Int
        public var instruction: String?
        public var scope: String?
        public var lock: [String]
        public var created: Date?

        private enum CodingKeys: String, CodingKey { case version, source, parent, instruction, scope, lock, created }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            version = try c.decode(Int.self, forKey: .version)
            guard version >= 1 else {
                throw DecodingError.dataCorruptedError(forKey: .version, in: c, debugDescription: "a version is numbered from 1")
            }
            source = try c.decode(String.self, forKey: .source)
            parent = (try? c.decode(Int.self, forKey: .parent)) ?? 0
            instruction = (try? c.decode(String.self, forKey: .instruction)).flatMap { $0.isEmpty ? nil : $0 }
            scope = (try? c.decode(String.self, forKey: .scope)).flatMap { $0.isEmpty ? nil : $0 }
            lock = (try? c.decode([String].self, forKey: .lock)) ?? []
            created = try? c.decode(Date.self, forKey: .created)
        }
    }

    /// Where a bullet sits: its section, its entry (none in a summary section) and its place in that list.
    public struct Place: Codable, Equatable, Sendable {
        public var section: String
        public var entry: String?
        public var index: Int
    }

    public struct BulletAt: Codable, Equatable, Sendable, Identifiable {
        public var id: String
        public var section: String
        public var entry: String?
        public var index: Int
    }

    public struct Moved: Codable, Equatable, Sendable, Identifiable {
        public var id: String
        public var from: Place
        public var to: Place
    }

    public struct Bullets: Codable, Equatable, Sendable {
        public var added: [BulletAt]
        public var removed: [BulletAt]
        public var moved: [Moved]
    }

    public struct Override: Codable, Equatable, Sendable {
        public var entry: String
        public var field: String
        public var from: String
        public var to: String
    }

    public struct Order: Codable, Equatable, Sendable {
        public var from: [String]
        public var to: [String]
    }

    public struct Keywords: Codable, Equatable, Sendable {
        public var added: [String]
        public var removed: [String]
    }

    public var key: String
    public var from: Side
    public var to: Side
    /// The two choices are identical in bullets, places, overrides, section order and keywords. The sides can
    /// still differ in how they were made.
    public var same: Bool
    public var bullets: Bullets
    public var overrides: [Override]
    public var sectionOrder: Order?
    public var keywords: Keywords

    private enum CodingKeys: String, CodingKey {
        case key, from, to, same, bullets, overrides, keywords
        case sectionOrder = "section_order"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        key = try c.decode(String.self, forKey: .key)
        from = try c.decode(Side.self, forKey: .from)
        to = try c.decode(Side.self, forKey: .to)
        same = try c.decode(Bool.self, forKey: .same)
        bullets = try c.decode(Bullets.self, forKey: .bullets)
        overrides = try c.decode([Override].self, forKey: .overrides)
        sectionOrder = try? c.decode(Order.self, forKey: .sectionOrder)
        keywords = try c.decode(Keywords.self, forKey: .keywords)
    }

    /// Nothing differs, in all five respects. `same` is vani's word; this must agree with the lists, or the
    /// reply contradicts itself and is not drawn as "no difference".
    public var listsAreEmpty: Bool {
        bullets.added.isEmpty && bullets.removed.isEmpty && bullets.moved.isEmpty && overrides.isEmpty
            && sectionOrder == nil && keywords.added.isEmpty && keywords.removed.isEmpty
    }
}

// MARK: - current

/// `current KEY vN`: the version now shown and redrafted from. `previous` is the one that was current.
public struct ResumeCurrentSet: Codable, Equatable, Sendable {
    public var key: String
    public var version: Int
    public var previous: Int?
    /// False when vN already was the current version: nothing moved.
    public var changed: Bool

    private enum CodingKeys: String, CodingKey { case key, version, previous, changed }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        key = try c.decode(String.self, forKey: .key)
        version = try c.decode(Int.self, forKey: .version)
        guard version >= 1 else {
            throw DecodingError.dataCorruptedError(forKey: .version, in: c, debugDescription: "a version is numbered from 1")
        }
        previous = (try? c.decode(Int.self, forKey: .previous)).flatMap { $0 >= 1 ? $0 : nil }
        changed = try c.decode(Bool.self, forKey: .changed)
    }
}

// MARK: - coverage

/// `coverage KEY [vN]`: which of the posting's keywords a version's bullets cover. Terms and ids only: the
/// wording is the pool's. A reply without one of its lists is unreadable: a missing `miss` must never read as
/// "nothing missed".
public struct ResumeCoverage: Codable, Equatable, Sendable {
    /// A term some chosen bullet covers: the ids (at most 40, page order) and how many bullets cover it.
    public struct Hit: Codable, Equatable, Sendable, Identifiable {
        public var term: String
        public var bullets: [String]
        public var n: Int
        public var id: String { term }
    }

    public var key: String
    /// The version measured; nil when the job has none and its base selection was measured instead.
    public var version: Int?
    public var base: Bool
    /// "posting", "jd_keywords", both joined by "+", or "none": whose terms were judged.
    public var keywordsSource: String
    /// Why the posting's own keywords could not be read; the judgement then holds the version's terms alone.
    public var keywordsError: String?
    public var hit: [Hit]
    /// Judged terms no chosen bullet covers.
    public var miss: [String]
    /// The part of `miss` no eligible bullet in the whole pool covers either: a swap cannot fix those.
    public var uncovered: [String]
    /// Phrases the matcher cannot hold: in none of hit, miss and uncovered, so never read as a miss.
    public var unjudged: [String]
    /// Chosen bullet ids with no eligible wording now: they cover nothing here.
    public var unresolved: [String]
    public var termsTotal: Int
    /// More terms were asked about than were judged.
    public var termsTruncated: Bool
    /// What DevPrism found missing or weak for the posting.
    public var gapMissing: [String]

    private enum CodingKeys: String, CodingKey {
        case key, version, base, hit, miss, uncovered, unjudged, unresolved
        case keywordsSource = "keywords_source", keywordsError = "keywords_error"
        case termsTotal = "terms_total", termsTruncated = "terms_truncated", gapMissing = "gap_missing"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        key = try c.decode(String.self, forKey: .key)
        version = (try? c.decode(Int.self, forKey: .version)).flatMap { $0 >= 1 ? $0 : nil }
        base = (try? c.decode(Bool.self, forKey: .base)) ?? false
        guard version != nil || base else {
            throw DecodingError.dataCorruptedError(forKey: .version, in: c, debugDescription: "coverage names neither a version nor the base selection")
        }
        keywordsSource = try c.decode(String.self, forKey: .keywordsSource)
        keywordsError = (try? c.decode(String.self, forKey: .keywordsError)).flatMap { $0.isEmpty ? nil : $0 }
        hit = try c.decode([Hit].self, forKey: .hit)
        miss = try c.decode([String].self, forKey: .miss)
        uncovered = try c.decode([String].self, forKey: .uncovered)
        unjudged = try c.decode([String].self, forKey: .unjudged)
        unresolved = try c.decode([String].self, forKey: .unresolved)
        termsTotal = try c.decode(Int.self, forKey: .termsTotal)
        guard termsTotal >= 0 else {
            throw DecodingError.dataCorruptedError(forKey: .termsTotal, in: c, debugDescription: "a count is not negative")
        }
        termsTruncated = (try? c.decode(Bool.self, forKey: .termsTruncated)) ?? false
        gapMissing = (try? c.decode([String].self, forKey: .gapMissing)) ?? []
    }

    /// The terms vani judged: those covered and those not.
    public var judged: Int { hit.count + miss.count }

    /// Missed terms a swap could fix: the bank has a bullet for them.
    public var swappable: [String] { miss.filter { m in !uncovered.contains { $0.caseInsensitiveCompare(m) == .orderedSame } } }
}

// MARK: - gaps

/// `gaps [--dry-run] KEY`: the terms handed to the career session. `written` is whether a line was added to
/// `file`; a dry run, a job with nothing to hand over and a retry that adds nothing all say `written: false`.
public struct ResumeGaps: Codable, Equatable, Sendable {
    public var key: String
    public var terms: [String]
    public var written: Bool
    public var file: String?
    public var dryRun: Bool
    /// The file's newest line for this job already held exactly these terms.
    public var unchanged: Bool
    public var termsTruncated: Bool
    public var note: String?

    private enum CodingKeys: String, CodingKey {
        case key, terms, written, file, unchanged, note
        case dryRun = "dry_run", termsTruncated = "terms_truncated"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        key = try c.decode(String.self, forKey: .key)
        terms = try c.decode([String].self, forKey: .terms)
        written = try c.decode(Bool.self, forKey: .written)
        file = (try? c.decode(String.self, forKey: .file)).flatMap { $0.isEmpty ? nil : $0 }
        dryRun = (try? c.decode(Bool.self, forKey: .dryRun)) ?? false
        unchanged = (try? c.decode(Bool.self, forKey: .unchanged)) ?? false
        termsTruncated = (try? c.decode(Bool.self, forKey: .termsTruncated)) ?? false
        note = (try? c.decode(String.self, forKey: .note)).flatMap { $0.isEmpty ? nil : $0 }
    }
}
