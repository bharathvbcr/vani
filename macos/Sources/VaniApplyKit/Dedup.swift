import Foundation

// Duplicate unification for the answer bank. Pure: it reads a Bank and returns
// proposals, or applies a MergePlan to a Bank value; the app writes the result
// through BankStore.update, which validates it and bumps the revision once.
//
// Rows are grouped by: the same intent or concept key over a row's key and
// wordings (Classifier.conceptKey; these chain), a similar-wording pair supplied
// by the caller (a row joins only on a direct pair with the cluster's survivor or
// deterministic group; pairs of tier "possible" form clusters unchecked by
// default), and, only among rows nothing else groups, an identical
// answer under different questions (never selected by default). Every merge is
// guarded: a row whose sensitivity differs, a secret, self-identification while
// the bank's setting is off, choice mixed with text, and a merge that would
// reclassify the survivor are all refused with a reason naming the row.

public enum Dedup {
    /// An answer shorter than this (after normalizing) is too generic to group by ("yes", "no").
    static let minValueChars = 5
    /// Largest counter the validator keeps. A larger sum is read back as 0, and sqlite3_bind_int cannot store it.
    static let maxCount = Int(Int32.max)

    public struct ValueChoice: Equatable, Sendable {
        /// The row whose value (and draft flag) this choice takes.
        public let fromKey: String
        public let value: [String]
        public let preselected: Bool
    }

    public struct Cluster: Equatable, Identifiable, Sendable {
        public let keys: [String]
        public let survivor: String
        public let valueChoices: [ValueChoice]
        public let reasons: [String]
        /// Why this cluster cannot be merged as proposed, or nil.
        public let blocked: String?
        /// Choice and text answers are mixed; merging needs `allowCrossKind`.
        public let crossKind: Bool
        /// False for clusters that rest on an identical answer alone.
        public let defaultSelected: Bool

        public var id: String { keys.joined(separator: "\u{1F}") }

        public var preselectedValueKey: String {
            valueChoices.first(where: \.preselected)?.fromKey ?? survivor
        }

        public func plan(allowCrossKind: Bool = false) -> MergePlan {
            MergePlan(survivor: survivor, merged: keys.filter { $0 != survivor }, valueFrom: preselectedValueKey,
                      allowCrossKind: allowCrossKind)
        }
    }

    public struct MergePlan: Equatable, Sendable {
        public var survivor: String
        public var merged: [String]
        public var valueFrom: String
        public var allowCrossKind: Bool

        public init(survivor: String, merged: [String], valueFrom: String, allowCrossKind: Bool = false) {
            self.survivor = survivor
            self.merged = merged
            self.valueFrom = valueFrom
            self.allowCrossKind = allowCrossKind
        }
    }

    public struct Merged: Equatable, Sendable {
        public let answer: Answer
        public let droppedLabels: Int
        public let droppedSignatures: Int
    }

    /// A row the merge may touch at all: not a secret, and self-identification only when the bank opts in.
    public static func offered(_ a: Answer, settings: Settings) -> Bool {
        switch a.sensitivity {
        case .secret: false
        case .selfID: settings.captureSelfId
        case .standard: true
        }
    }

    // MARK: - Finding clusters

    public static func clusters(bank: Bank, similar: [SimilarPair]) -> [Cluster] {
        let rows = bank.answers.values.filter { offered($0, settings: bank.settings) }.sorted { $0.key < $1.key }
        guard rows.count > 1 else { return [] }
        var at: [String: Int] = [:]
        for (i, r) in rows.enumerated() { at[r.key] = i }

        var uf = UnionFind(count: rows.count)
        var reasonAt: [(Int, String)] = []

        // Same intent or concept key, over a row's key and each wording.
        var concept: [String: String] = [:]
        var buckets: [String: [Int]] = [:]
        var intentKeys = Set<String>()
        for (i, r) in rows.enumerated() {
            for text in [r.key] + r.labels {
                let c: String
                if let hit = concept[text] { c = hit } else {
                    c = Classifier.conceptKey(label: text, autocomplete: "")
                    concept[text] = c
                    if !c.isEmpty, c == Classifier.intentOf(text) { intentKeys.insert(c) }
                }
                if c.isEmpty || buckets[c]?.last == i { continue }
                buckets[c, default: []].append(i)
            }
        }
        for (c, members) in buckets where members.count > 1 {
            for m in members.dropFirst() { uf.union(members[0], m) }
            reasonAt.append((members[0], intentKeys.contains(c) ? "same intent: \(c)" : "same concept: \(c)"))
        }

        // Similar-wording pairs. Unknown keys, self pairs, repeats and non-finite scores are ignored.
        // A pair is an edge for one hop only: it never unites groups (no chaining).
        var seenPairs = Set<String>()
        var edges = [[(to: Int, cos: Double, possible: Bool)]](repeating: [], count: rows.count)
        for p in similar {
            guard p.cos.isFinite, p.a != p.b, let i = at[p.a], let j = at[p.b] else { continue }
            let id = p.a < p.b ? "\(p.a)\u{1F}\(p.b)" : "\(p.b)\u{1F}\(p.a)"
            guard seenPairs.insert(id).inserted else { continue }
            let possible = p.tier == "possible"
            edges[i].append((j, p.cos, possible))
            edges[j].append((i, p.cos, possible))
        }

        var members: [Int: [Int]] = [:]
        for i in rows.indices { members[uf.find(i), default: []].append(i) }
        var reasons: [Int: Set<String>] = [:]
        for (i, r) in reasonAt { reasons[uf.find(i), default: []].insert(r) }

        // Deterministic groups are the seeds, best survivor first. A row grouped by
        // nothing else joins a seed only on a direct pair with it (with any member of
        // a deterministic group, else with the seed row itself, which outranks every
        // row it claims), so A~B and B~C never put C with A. Unchecked "possible"
        // pairs are a second round over what the first left alone.
        let order = rows.indices.sorted { ranksBefore(rows[$0], rows[$1]) }
        var assigned = [Bool](repeating: false, count: rows.count)
        var out: [Cluster] = []

        func claim(_ seed: [Int], possible: Bool, extra: Set<String>) {
            var joined: [Int] = []
            var lowest: Double?
            for m in seed {
                for e in edges[m] where e.possible == possible && !assigned[e.to] && members[uf.find(e.to)]!.count == 1 {
                    assigned[e.to] = true
                    joined.append(e.to)
                    lowest = min(lowest ?? e.cos, e.cos)
                }
            }
            guard seed.count > 1 || !joined.isEmpty else { return }
            seed.forEach { assigned[$0] = true }
            var rs = extra
            if let c = lowest { rs.insert("similar wording \(String(format: "%.2f", c))") }
            out.append(cluster((seed + joined).map { rows[$0] }, reasons: rs.sorted(), settings: bank.settings, selected: !possible))
        }

        for i in order where !assigned[i] {
            let group = members[uf.find(i)]!
            if group.count > 1 { claim(group, possible: false, extra: reasons[uf.find(i)] ?? []) }
        }
        for i in order where !assigned[i] { claim([i], possible: false, extra: []) }
        for i in order where !assigned[i] { claim([i], possible: true, extra: []) }
        let singles = rows.indices.filter { !assigned[$0] }

        // The same answer under different questions, among rows nothing else groups.
        var byValue: [String: [Int]] = [:]
        for i in singles {
            let r = rows[i]
            let v = r.value.map { $0.split(whereSeparator: \.isWhitespace).joined(separator: " ").lowercased() }
                .joined(separator: "\n")
            guard v.utf16.count >= minValueChars else { continue }
            byValue["\(r.sensitivity.rawValue)|\(r.kind.isChoice)|\(v)", default: []].append(i)
        }
        for (_, idx) in byValue where idx.count > 1 {
            out.append(cluster(idx.sorted().map { rows[$0] }, reasons: ["same answer, different question"],
                               settings: bank.settings, selected: false))
        }

        return out.sorted { l, r in
            if l.defaultSelected != r.defaultSelected { return l.defaultSelected }
            return l.keys.lexicographicallyPrecedes(r.keys)
        }
    }

    /// The survivor order: most used, then newest, then the smaller key.
    private static func ranksBefore(_ l: Answer, _ r: Answer) -> Bool {
        if l.uses != r.uses { return l.uses > r.uses }
        if l.updatedAt != r.updatedAt { return l.updatedAt > r.updatedAt }
        return l.key < r.key
    }

    private static func cluster(_ rows: [Answer], reasons: [String], settings: Settings, selected: Bool) -> Cluster {
        let keys = rows.map(\.key).sorted()
        let survivor = rows.min(by: ranksBefore)!
        let others = rows.filter { $0.key != survivor.key }.sorted { $0.key < $1.key }
        // Newest value first; on a tie the survivor's, then the smaller key.
        let byNewest = rows.sorted { l, r in
            if l.updatedAt != r.updatedAt { return l.updatedAt > r.updatedAt }
            if (l.key == survivor.key) != (r.key == survivor.key) { return l.key == survivor.key }
            return l.key < r.key
        }
        var choices: [ValueChoice] = []
        var seen = Set<[String]>()
        for r in byNewest where seen.insert(r.value).inserted {
            choices.append(ValueChoice(fromKey: r.key, value: r.value, preselected: choices.isEmpty))
        }
        return Cluster(keys: keys, survivor: survivor.key, valueChoices: choices, reasons: reasons,
                       blocked: guardReason(survivor: survivor, others: others, settings: settings, allowCrossKind: false),
                       crossKind: Set(rows.map(\.kind.isChoice)).count > 1, defaultSelected: selected)
    }

    // MARK: - Guards

    /// Why merging `others` into `survivor` is refused, or nil. Messages name the row at fault.
    static func guardReason(survivor: Answer, others: [Answer], settings: Settings, allowCrossKind: Bool) -> String? {
        let all = [survivor] + others
        if let a = all.first(where: { $0.sensitivity == .secret }) {
            return "“\(a.key)” looks like a secret; secrets are never merged."
        }
        if settings.captureSelfId == false, let a = all.first(where: { $0.sensitivity == .selfID }) {
            return "“\(a.key)” is a self-identification answer; turn self-identification on in Settings to merge it."
        }
        if let a = others.first(where: { $0.sensitivity != survivor.sensitivity }) {
            return "“\(a.key)” is \(a.sensitivity.rawValue) but “\(survivor.key)” is \(survivor.sensitivity.rawValue)."
        }
        if !allowCrossKind, let a = others.first(where: { $0.kind.isChoice != survivor.kind.isChoice }) {
            return "“\(a.key)” is a \(a.kind.rawValue) answer and “\(survivor.key)” is \(survivor.kind.rawValue): choice and text answers are not merged unless you allow it."
        }
        let labels = mergedLabels(survivor: survivor, others: others).labels
        if classify(survivor, labels: labels) != survivor.sensitivity {
            let culprit = others.first { classify(survivor, labels: mergedLabels(survivor: survivor, others: [$0]).labels) != survivor.sensitivity }
            let named = culprit.map { "“\($0.key)”" } ?? "these rows"
            return "Merging \(named) into “\(survivor.key)” would change its sensitivity."
        }
        return nil
    }

    private static func classify(_ a: Answer, labels: [String]) -> Sensitivity {
        Classifier.classify(kind: a.kind, label: ([a.key] + labels).joined(separator: " "), name: "", autocomplete: "")
    }

    /// The survivor's wordings, then each merged row's key, then theirs; over the cap the
    /// merged keys are kept first (they are what the form is matched by), then wordings in order.
    static func mergedLabels(survivor: Answer, others: [Answer]) -> (labels: [String], dropped: Int) {
        var order: [(text: String, must: Bool)] = []
        var at: [String: Int] = [:]
        func add(_ s: String, must: Bool) {
            if let i = at[s] { if must { order[i].must = true }; return }
            at[s] = order.count
            order.append((s, must))
        }
        for l in survivor.labels { add(l, must: false) }
        for o in others { add(o.key, must: true) }
        for o in others { for l in o.labels { add(l, must: false) } }
        let cap = Limits.maxLabels
        if order.count <= cap { return (order.map(\.text), 0) }
        var room = cap
        var keep = [Bool](repeating: false, count: order.count)
        for (i, e) in order.enumerated() where e.must && room > 0 { keep[i] = true; room -= 1 }
        for (i, e) in order.enumerated() where !e.must && room > 0 { keep[i] = true; room -= 1 }
        let labels = order.indices.filter { keep[$0] }.map { order[$0].text }
        return (labels, order.count - labels.count)
    }

    // MARK: - Merging

    /// The row a plan would produce, or why it cannot. Reads only.
    public static func merged(bank: Bank, plan: MergePlan, now: Date = Date()) throws -> Merged {
        guard !plan.merged.isEmpty else { throw BankError("Choose at least one row to merge into “\(plan.survivor)”.") }
        guard Set(plan.merged).count == plan.merged.count, !plan.merged.contains(plan.survivor) else {
            throw BankError("The rows to merge must be distinct from each other and from “\(plan.survivor)”.")
        }
        var rows: [String: Answer] = [:]
        for k in [plan.survivor] + plan.merged {
            guard let a = bank.answers[k] else { throw BankError("“\(k)” is no longer in the bank; nothing was merged.") }
            rows[k] = a
        }
        guard let source = rows[plan.valueFrom] else {
            throw BankError("The chosen answer must come from one of the merged rows.")
        }
        let survivor = rows[plan.survivor]!
        let others = plan.merged.map { rows[$0]! }
        if let why = guardReason(survivor: survivor, others: others, settings: bank.settings, allowCrossKind: plan.allowCrossKind) {
            throw BankError(why)
        }

        var out = survivor
        let (labels, droppedLabels) = mergedLabels(survivor: survivor, others: others)
        out.labels = labels
        var sigs: [String] = []
        var seen = Set<String>()
        for s in ([survivor] + others).flatMap(\.signatures) where seen.insert(s).inserted { sigs.append(s) }
        out.signatures = Array(sigs.prefix(Limits.maxSignatures))
        out.uses = saturatingSum(([survivor] + others).map(\.uses))
        out.fills = saturatingSum(([survivor] + others).map(\.fills))
        out.value = source.value
        out.review = source.review
        out.updatedAt = isoNow(now)
        return Merged(answer: out, droppedLabels: droppedLabels, droppedSignatures: sigs.count - out.signatures.count)
    }

    /// Apply plans in order, all or nothing: on any refusal `bank` is left as it was.
    public static func merge(bank: inout Bank, plans: [MergePlan], now: Date = Date()) throws {
        var work = bank
        for plan in plans {
            let m = try merged(bank: work, plan: plan, now: now)
            work.answers[plan.survivor] = m.answer
            for k in plan.merged { work.answers.removeValue(forKey: k) }
        }
        bank = work
    }

    public static func merge(bank: inout Bank, plan: MergePlan, now: Date = Date()) throws {
        try merge(bank: &bank, plans: [plan], now: now)
    }

    private static func saturatingSum(_ xs: [Int]) -> Int {
        var total = 0
        for x in xs {
            let (s, overflow) = total.addingReportingOverflow(max(0, x))
            total = overflow ? maxCount : min(s, maxCount)
        }
        return total
    }

    // MARK: - A new wording

    /// `key`'s row with `label` added as another wording of the same question.
    public static func addingWording(_ label: String, to key: String, in bank: Bank, now: Date = Date()) throws -> Answer {
        let text = Classifier.normalizeLabel(label)
        if text.utf16.count < 2 { throw EditError.emptyQuestion }
        guard let a = bank.answers[key] else { throw BankError("“\(key)” is no longer in the bank.") }
        guard offered(a, settings: bank.settings) else {
            throw BankError("“\(key)” is a \(a.sensitivity.rawValue) answer and cannot take new wordings here.")
        }
        if text == a.key || a.labels.contains(text) { return a }
        if bank.answers[text] != nil { throw EditError.duplicate(text) }
        if a.labels.count >= Limits.maxLabels {
            throw BankError("“\(key)” already has \(Limits.maxLabels) wordings.")
        }
        var out = a
        out.labels.append(text)
        if classify(out, labels: out.labels) != a.sensitivity {
            throw BankError("That wording would change the sensitivity of “\(key)”, so it was not added.")
        }
        out.updatedAt = isoNow(now)
        return out
    }
}

private struct UnionFind {
    private var parent: [Int]
    init(count: Int) { parent = Array(0..<count) }

    mutating func find(_ x: Int) -> Int {
        var root = x
        while parent[root] != root { root = parent[root] }
        var cur = x
        while parent[cur] != root {
            let next = parent[cur]
            parent[cur] = root
            cur = next
        }
        return root
    }

    mutating func union(_ a: Int, _ b: Int) {
        let (ra, rb) = (find(a), find(b))
        if ra != rb { parent[max(ra, rb)] = min(ra, rb) }
    }
}
