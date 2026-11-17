import Foundation
import Observation

/// The owner's exclusions (`exclude` in Vani's config): postings whose title,
/// company, location or description they match are dropped without asking a
/// model. Read and saved through `vani config exclude`; Vani owns the bounds
/// and the checks and writes the config, and the app only shows what it replies.
public struct ExclusionLists: Codable, Equatable, Sendable {
    public var titles: [String]
    public var titleUnless: [String]
    public var companies: [String]
    public var locations: [String]
    public var onlyLocations: [String]
    public var descriptions: [String]

    public init(titles: [String] = [], titleUnless: [String] = [], companies: [String] = [],
                locations: [String] = [], onlyLocations: [String] = [], descriptions: [String] = []) {
        self.titles = titles
        self.titleUnless = titleUnless
        self.companies = companies
        self.locations = locations
        self.onlyLocations = onlyLocations
        self.descriptions = descriptions
    }

    enum CodingKeys: String, CodingKey {
        case titles, companies, locations, descriptions
        case titleUnless = "title_unless"
        case onlyLocations = "only_locations"
    }

    /// Adds what the owner typed or pasted to `list`, one entry per line, each
    /// trimmed. A line is refused when it is empty, has a control character,
    /// is over `maxBytes`, repeats an entry (ignoring case and runs of
    /// whitespace, as Vani compares them) or would pass `cap`; the others are
    /// added. Returns why the first refused line was refused, or nil. Vani
    /// checks the whole list again when it saves, so this only spares the
    /// owner a refused save.
    public static func add(_ input: String, to list: inout [String], cap: Int?, maxBytes: Int?) -> String? {
        var problem: String?
        for line in input.split(whereSeparator: \.isNewline) {
            let entry = line.trimmingCharacters(in: .whitespaces)
            if entry.isEmpty { continue }
            let why: String? =
                if entry.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }) {
                    "\u{201C}\(entry)\u{201D} contains a control character"
                } else if let maxBytes, entry.utf8.count > maxBytes {
                    "\u{201C}\(entry.prefix(24))…\u{201D} is over \(maxBytes) bytes"
                } else if list.contains(where: { fold($0) == fold(entry) }) {
                    "\u{201C}\(entry)\u{201D} is already listed"
                } else if let cap, list.count >= cap {
                    "this list holds at most \(cap) entries"
                } else {
                    nil
                }
            if let why {
                problem = problem ?? why
            } else {
                list.append(entry)
            }
        }
        return problem
    }

    /// Lowercased, with each run of whitespace one space.
    static func fold(_ s: String) -> String {
        s.lowercased().split(whereSeparator: \.isWhitespace).joined(separator: " ")
    }

    static var allLists: [WritableKeyPath<ExclusionLists, [String]>] {
        [\.titles, \.titleUnless, \.companies, \.locations, \.onlyLocations, \.descriptions]
    }

    /// What the owner changed, `edited` against the `base` they started from,
    /// applied to `current`: entries they removed leave it and entries they
    /// added join it (entries compare as Vani compares them), so what someone
    /// else saved in between stays. The merge of a stale copy, never its replacement.
    public static func rebase(_ edited: ExclusionLists, from base: ExclusionLists, onto current: ExclusionLists) -> ExclusionLists {
        var out = current
        for list in allLists {
            let before = Set(base[keyPath: list].map(fold)), after = Set(edited[keyPath: list].map(fold))
            out[keyPath: list].removeAll { before.contains(fold($0)) && !after.contains(fold($0)) }
            for entry in edited[keyPath: list] where !before.contains(fold(entry)) {
                if !out[keyPath: list].contains(where: { fold($0) == fold(entry) }) { out[keyPath: list].append(entry) }
            }
        }
        return out
    }
}

/// What `vani config exclude` replies: the saved lists and Vani's bounds.
public struct Exclusions: Decodable, Equatable, Sendable {
    public var lists: ExclusionLists
    /// Identifies `lists`; a save that hands it back is refused (`ExclusionsStale`)
    /// when the config's lists changed since. Nil from a vani that predates it.
    public var revision: String?
    /// Vani's cap on entries per list, but descriptions.
    public var maxEntries: Int
    /// Vani's cap on description phrases.
    public var maxDescriptions: Int
    /// Vani's cap on one entry, in bytes.
    public var maxEntryBytes: Int

    enum CodingKeys: String, CodingKey {
        case lists = "exclude"
        case revision
        case maxEntries = "max_entries"
        case maxDescriptions = "max_descriptions"
        case maxEntryBytes = "max_entry_bytes"
    }
}

public struct ExclusionsClient: Sendable {
    public static let timeout: TimeInterval = 15
    static let maxOutputBytes = 512 << 10

    public let vaniBinary: URL
    public let configPath: String?
    public let stateDir: String?
    let timeout: TimeInterval

    public init(vaniBinary: URL, configPath: String? = nil, stateDir: String? = nil, timeout: TimeInterval = ExclusionsClient.timeout) {
        self.vaniBinary = vaniBinary
        self.configPath = configPath
        self.stateDir = stateDir
        self.timeout = timeout
    }

    public func load() throws -> Exclusions {
        try call(["config", "exclude"], input: nil)
    }

    /// Replace every list; empty lists clear them. Vani validates the whole
    /// config before it writes, so refused exclusions leave the file as it was.
    /// The lists travel on stdin as JSON, never as arguments. With `revision`
    /// (the one a read replied with) Vani refuses the save, throwing
    /// `ExclusionsStale`, when the lists changed since; without it the save
    /// replaces whatever is there.
    public func save(_ lists: ExclusionLists, revision: String? = nil) throws -> Exclusions {
        let encoder = JSONEncoder()
        encoder.outputFormatting = .sortedKeys
        return try call(["config", "exclude", "--set"] + (revision.map { ["--revision", $0] } ?? []),
                        input: try encoder.encode(lists))
    }

    /// The code of vani's reply to a stale save (config.ErrStale). Matched on the code, never the
    /// error text: a refusal echoes the owner's entry, which can spell any phrase.
    static let staleCode = "stale"

    private func call(_ args: [String], input: Data?) throws -> Exclusions {
        do {
            return try VaniProcess.call(vaniBinary, args, configPath: configPath, stateDir: stateDir, input: input,
                                        timeout: timeout, maxOutputBytes: Self.maxOutputBytes,
                                        tooOld: "this vani is too old for exclusions; rebuild the app")
        } catch let error as BankError where error.code == Self.staleCode {
            throw ExclusionsStale()
        }
    }
}

/// A save was refused because the config's exclusions changed after they were read.
public struct ExclusionsStale: Error, Equatable, CustomStringConvertible {
    public var description: String { "the exclusions changed since they were read" }
}

extension DraftSettings {
    /// Exclusions in the same config drafting and Jobs use.
    public func exclusionsClient(vaniBinary: URL) -> ExclusionsClient {
        ExclusionsClient(vaniBinary: vaniBinary,
                         configPath: vaniConfig.isEmpty ? nil : vaniConfig,
                         stateDir: vaniStateDir.isEmpty ? nil : vaniStateDir)
    }
}

/// The exclusions as the app edits them: one copy for Settings and Jobs, owned
/// by the app delegate so it outlives a pane switch. Every change to `lists`
/// is saved through `vani config exclude --set` at once, one call at a time;
/// changes made while a save runs go out in the next one. A refused save keeps
/// the edited lists and says why, and nothing is retried until the owner edits
/// again or asks.
@MainActor
@Observable
public final class ExclusionsEditor {
    /// What Settings shows and edits. Setting it saves.
    public var lists = ExclusionLists() {
        didSet { if !applying, lists != oldValue { notice = nil; requestSave() } }
    }
    /// Said when a save was merged with exclusions changed elsewhere; the next edit clears it.
    public private(set) var notice: String?
    /// The last reply from Vani: the lists in the config and Vani's caps. Nil
    /// until a read succeeds, and editing waits for it.
    public private(set) var saved: Exclusions?
    /// Why the last read or save failed.
    public private(set) var problem: String?
    /// A `vani config exclude` call is running.
    public private(set) var busy = false

    @ObservationIgnored private let client: () -> ExclusionsClient
    @ObservationIgnored private let onSaved: @MainActor () -> Void
    @ObservationIgnored private var applying = false
    @ObservationIgnored private var saving: Task<Void, Never>?
    @ObservationIgnored private var loading: Task<Void, Never>?

    /// `onSaved` runs after each save Vani accepts (the app reloads Jobs).
    public init(client: @escaping () -> ExclusionsClient, onSaved: @escaping @MainActor () -> Void = {}) {
        self.client = client
        self.onSaved = onSaved
    }

    /// The lists differ from the config's: a save is running or was refused.
    public var unsaved: Bool { saved.map { $0.lists != lists } ?? false }

    /// Reads the config's lists. Does nothing while a call runs, so it never
    /// overtakes a save. The read replaces the shown lists, except that what
    /// the owner edited while it ran is kept and merged onto it (`rebase`), then
    /// saved: an old read never writes old lists back over an edit.
    public func load() async {
        guard !busy else { return }
        busy = true
        let before = lists // taken here, before anything else can edit
        let reading = Task { await self.read(startedWith: before) }
        loading = reading
        await reading.value
    }

    private func read(startedWith before: ExclusionLists) async {
        let c = client()
        let result = await BlockingWork.run { try c.load() }
        loading = nil
        busy = saving != nil
        switch result {
        case let .success(x):
            saved = x
            problem = nil
            apply(lists == before ? x.lists : ExclusionLists.rebase(lists, from: before, onto: x.lists))
            if lists != x.lists { requestSave() }
        case let .failure(err):
            problem = String(describing: err)
        }
    }

    /// Reads the config unless there are edits it has not taken, which a read
    /// would throw away: what Settings does when it appears.
    public func refresh() async {
        if !unsaved { await load() }
    }

    /// Adds an entry (a posting's company, from Jobs) and waits for its save.
    /// The entry comes from a posting, so its whitespace, newlines included, is
    /// collapsed to single spaces, which Vani's matching ignores anyway, and it
    /// stays one entry. Returns why it was not added or not saved, or nil.
    public func add(_ entry: String, to list: WritableKeyPath<ExclusionLists, [String]>) async -> String? {
        let one = entry.split(whereSeparator: \.isWhitespace).joined(separator: " ")
        guard !one.isEmpty else { return "nothing to exclude: the entry is empty" }
        if saved == nil { await load() }
        guard let saved else { return problem ?? "the exclusions could not be read" }
        var edited = lists
        let cap = list == \ExclusionLists.descriptions ? saved.maxDescriptions : saved.maxEntries
        if let why = ExclusionLists.add(one, to: &edited[keyPath: list], cap: cap, maxBytes: saved.maxEntryBytes) {
            return "not excluded: \(why)"
        }
        lists = edited
        await settled()
        return unsaved ? (problem ?? "not saved") : nil
    }

    /// Saves the edited lists again after a refused save.
    public func retry() { requestSave() }

    /// Waits for the save that is running, if any.
    public func settled() async {
        while let task = saving { await task.value }
    }

    private func requestSave() {
        guard saved != nil, saving == nil else { return }
        saving = Task { await saveUntilCurrent() }
    }

    /// How many times in a row a save may find the config changed under it
    /// before the owner is told instead.
    static let maxStaleRetries = 3

    /// Saves until the config holds what the editor shows, or Vani refuses.
    /// It first waits for a read in flight, so that read's lists are the base
    /// of the save and never overwrite it. Each save names the revision it is
    /// built on; when the config changed since (`ExclusionsStale`) it reads
    /// again, applies the owner's changes to what is there now and saves that,
    /// so an exclusion added elsewhere is never replaced by this copy.
    private func saveUntilCurrent() async {
        busy = true
        defer { busy = false; saving = nil }
        if let loading { await loading.value }
        var stale = 0
        while let base = saved, lists != base.lists {
            let sending = lists
            let c = client()
            let result = await BlockingWork.run { try c.save(sending, revision: base.revision) }
            switch result {
            case let .success(x):
                stale = 0
                self.saved = x
                problem = nil
                // Vani trims what it saves; show that, unless the owner edited meanwhile.
                if lists == sending { apply(x.lists) }
                onSaved()
            case let .failure(err) where err is ExclusionsStale:
                stale += 1
                guard stale <= Self.maxStaleRetries else {
                    problem = "the exclusions keep changing elsewhere; your edit was not saved. Try again."
                    return
                }
                let current = await BlockingWork.run { try c.load() }
                switch current {
                case let .success(x):
                    let merged = ExclusionLists.rebase(lists, from: base.lists, onto: x.lists)
                    saved = x
                    apply(merged)
                    notice = "The exclusions were changed elsewhere; your edit was merged with them."
                case let .failure(err):
                    problem = String(describing: err)
                    return
                }
            case let .failure(err):
                problem = String(describing: err)
                return
            }
        }
    }

    private func apply(_ l: ExclusionLists) {
        applying = true
        lists = l
        applying = false
    }
}
