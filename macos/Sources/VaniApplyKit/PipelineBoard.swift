import Foundation

/// The Pipeline progress board: the state mark, the four-step rail, the
/// fraction, the outcome bar and the count tiles. Views lay this out; they
/// do not decide which number is which. A count that was not reported is
/// `.unavailable`, never 0.
public struct PipelineBoard: Equatable, Sendable {
    public struct Mark: Equatable, Sendable {
        public enum Tone: Equatable, Sendable {
            case running, succeeded, cancelled, failed
        }

        public var symbol: String
        public var title: String
        public var detail: String
        public var tone: Tone
    }

    public struct Step: Equatable, Sendable, Identifiable {
        public enum Kind: String, Equatable, Sendable {
            case start, boards, triage, digest
        }

        /// `.done` is a check. `.current` is the step in hand. `.failed` is
        /// where a failed run stopped. `.later` is still ahead.
        public enum Phase: Equatable, Sendable {
            case done, current, later, failed
        }

        public var kind: Kind
        public var title: String
        public var phase: Phase
        /// A spinner while this step is current and has no real fraction.
        /// A stopped run does not spin.
        public var spinner: Bool
        public var id: Kind { kind }
    }

    public struct Fraction: Equatable, Sendable {
        public var done: Int
        public var total: Int
        public var caption: String
    }

    public struct Segment: Equatable, Sendable, Identifiable {
        public enum Kind: String, Equatable, Sendable {
            case scraped, broken, limited, skipped, left
        }

        public var kind: Kind
        public var count: Int
        public var caption: String
        public var id: Kind { kind }
    }

    public enum CountValue: Equatable, Sendable {
        case number(Int)
        case unavailable
    }

    public struct Count: Equatable, Sendable, Identifiable {
        public var id: String
        public var title: String
        public var symbol: String
        public var value: CountValue
    }

    public struct BoardRow: Equatable, Sendable, Identifiable {
        public var id: String
        public var symbol: String
        public var title: String
        public var token: String
        public var listed: CountValue
        public var added: CountValue
        public var note: String
        public var problem: Bool
    }

    public var mark: Mark
    public var steps: [Step]
    public var stageLine: String
    /// Set only while the run is going and the step has a real fraction.
    /// A stopped run keeps `placeCaption` and does not draw this.
    public var fraction: Fraction?
    public var placeCaption: String
    public var segments: [Segment]
    public var counts: [Count]
    public var showsCounts: Bool
    public var current: BoardRow?
    public var rows: [BoardRow]
    /// Problems is the list's default when any recorded board was not read.
    public var problemsDefault: Bool
    public var footer: String
    /// Said when the run stopped before each board was written down.
    /// An empty list is not "no boards", so this is empty then too.
    public var listNote: String
    /// False when the snapshot could not be read: the mark stays, the rest does not.
    public var showsVisuals: Bool
    public var updatedAt: Date?
    /// The ledger's size change. Nil unless both ends were recorded.
    public var ledgerLine: String?

    /// Nil when there is no run to draw (none recorded, or the record itself
    /// could not be read).
    public static func present(_ run: PipelineStatus.LastRun) -> PipelineBoard? {
        guard run.ok else { return nil }
        switch run.state {
        case .never, .unknown: return nil
        case .ok, .failed, .cancelled, .running, .interrupted: break
        }
        let progress = run.progress
        let unreadable = progress?.ok == false
        let mark = mark(of: run, progress: progress, unreadable: unreadable)
        if unreadable {
            return PipelineBoard(
                mark: mark, steps: [], stageLine: "", fraction: nil, placeCaption: "",
                segments: [], counts: [], showsCounts: false, current: nil, rows: [],
                problemsDefault: false, footer: "", listNote: "", showsVisuals: false,
                updatedAt: nil, ledgerLine: HomeOverview.ledgerDelta(run))
        }
        let stage = progress?.ok == true ? progress?.stage ?? "" : ""
        let known = stepIndex(of: stage)
        let running = run.state == .running
        let place = place(of: progress, running: running)
        let numbers = numbers(of: run, progress: progress)
        let rows = rows(of: run.boards)
        let steps = steps(of: run, stage: stage, known: known, running: running, hasFraction: place.fraction != nil)
        return PipelineBoard(
            mark: mark,
            steps: steps,
            stageLine: stageLine(of: progress, known: known),
            fraction: place.fraction,
            placeCaption: place.caption,
            segments: numbers.segments,
            counts: numbers.counts,
            showsCounts: numbers.show,
            current: running ? currentBoard(progress) : nil,
            rows: rows,
            problemsDefault: rows.contains(where: \.problem),
            footer: footer(of: run.boards),
            listNote: listNote(of: run, showingCounts: numbers.show, rows: rows),
            showsVisuals: true,
            updatedAt: progress?.ok == true ? progress?.updatedAt : nil,
            ledgerLine: HomeOverview.ledgerDelta(run))
    }

    // MARK: - Mark

    private static func mark(of run: PipelineStatus.LastRun, progress: PipelineStatus.LastRun.Progress?, unreadable: Bool) -> Mark {
        if unreadable {
            let why = PipelineStatus.bounded(progress?.reason ?? "unreadable", max: 300)
            return Mark(symbol: "exclamationmark.triangle.fill", title: "Live progress could not be read", detail: why, tone: .failed)
        }
        switch run.state {
        case .running:
            if progress?.ok == true, stepIndex(of: progress?.stage ?? "") == nil, let stage = progress?.stage, !stage.isEmpty {
                return Mark(symbol: "arrow.triangle.2.circlepath", title: PipelineStatus.bounded(stage, max: 80), detail: "", tone: .running)
            }
            return Mark(symbol: "arrow.triangle.2.circlepath", title: "Running", detail: "", tone: .running)
        case .ok:
            return Mark(symbol: "checkmark.circle.fill", title: "Succeeded", detail: "", tone: .succeeded)
        case .cancelled:
            return Mark(symbol: "stop.circle.fill", title: "Cancelled", detail: "", tone: .cancelled)
        case .failed:
            return Mark(symbol: "xmark.octagon.fill", title: "Failed", detail: "", tone: .failed)
        case .interrupted:
            return Mark(symbol: "xmark.octagon.fill", title: "Did not finish",
                        detail: "The process is gone: a crash, a shutdown, or a kill.", tone: .failed)
        case .never, .unknown:
            return Mark(symbol: "questionmark.circle", title: "Unknown", detail: "", tone: .failed)
        }
    }

    // MARK: - Rail

    private static let rail: [(Step.Kind, String, Set<String>)] = [
        (.start, "Start", ["starting", "preparing", "inbox"]),
        (.boards, "Boards", ["listing"]),
        (.triage, "Triage", ["near_dup", "exclusions", "logos", "warm", "advance", "backfill"]),
        (.digest, "Digest", ["digest"]),
    ]

    private static func stepIndex(of stage: String) -> Int? {
        rail.firstIndex { $0.2.contains(stage) }
    }

    /// Where a finished run stopped, from what the record kept. Digest is the
    /// record's stage. A pipeline failure is triage when a triage stage left
    /// a report, boards when the listing did, and start otherwise.
    private static func stopIndex(of run: PipelineStatus.LastRun) -> Int {
        if run.stage == "digest" { return 3 }
        if run.advance != nil || run.nearDup != nil || run.exclusions != nil || run.logos != nil || run.semanticPre != nil {
            return 2
        }
        if run.boards != nil { return 1 }
        return 0
    }

    private static func steps(of run: PipelineStatus.LastRun, stage: String, known: Int?, running: Bool, hasFraction: Bool) -> [Step] {
        let current: Int?
        let failed: Bool
        switch run.state {
        case .ok:
            current = nil
            failed = false
        case .failed:
            current = known ?? stopIndex(of: run)
            failed = true
        case .cancelled:
            current = known ?? stopIndex(of: run)
            failed = false
        case .interrupted:
            current = known
            failed = false
        case .running:
            current = known ?? (stage.isEmpty ? 0 : nil)
            failed = false
        case .never, .unknown:
            current = nil
            failed = false
        }
        return rail.enumerated().map { index, step in
            let phase: Step.Phase
            if run.state == .ok {
                phase = .done
            } else if let current, index < current {
                phase = .done
            } else if let current, index == current {
                phase = failed ? .failed : .current
            } else {
                phase = .later
            }
            let spinner = running && phase == .current && !hasFraction
            return Step(kind: step.0, title: step.1, phase: phase, spinner: spinner)
        }
    }

    private static func stageLine(of progress: PipelineStatus.LastRun.Progress?, known: Int?) -> String {
        guard let progress, progress.ok, known != nil else { return "" }
        return progress.headline
    }

    // MARK: - Fraction

    private static func place(of progress: PipelineStatus.LastRun.Progress?, running: Bool) -> (fraction: Fraction?, caption: String) {
        guard let progress, progress.ok else { return (nil, "") }
        switch progress.stage {
        case "listing":
            guard progress.index > 0, progress.total > 0, progress.index <= progress.total else { return (nil, "") }
            let caption = "board \(progress.index) of \(progress.total)"
            return (running ? Fraction(done: progress.index, total: progress.total, caption: caption) : nil, caption)
        case "advance":
            guard progress.attempted > 0, progress.limit > 0, progress.attempted <= progress.limit else { return (nil, "") }
            let caption = "\(progress.attempted) of \(progress.limit)"
            return (running ? Fraction(done: progress.attempted, total: progress.limit, caption: caption) : nil, caption)
        default:
            return (nil, "")
        }
    }

    // MARK: - Counts

    private struct Numbers {
        var segments: [Segment]
        var counts: [Count]
        var show: Bool
    }

    private static func numbers(of run: PipelineStatus.LastRun, progress: PipelineStatus.LastRun.Progress?) -> Numbers {
        if let boards = run.boards {
            let broken = boards.failed - min(max(boards.rateLimited, 0), max(boards.failed, 0))
            return Numbers(
                segments: segments(total: boards.configured, scraped: boards.read, broken: broken,
                                   limited: boards.rateLimited, skipped: boards.skipped, left: boards.notReached),
                counts: tiles(scraped: count(boards.read), left: count(boards.notReached),
                              listed: count(boards.listed), added: boards.added.map(count) ?? .unavailable),
                show: true)
        }
        guard let progress, progress.ok, reportsListing(progress) else {
            return Numbers(segments: [], counts: [], show: false)
        }
        let left = progress.total >= progress.done ? progress.total - progress.done : nil
        let broken = progress.failed - progress.rateLimited
        return Numbers(
            segments: segments(total: progress.total, scraped: progress.read, broken: broken,
                               limited: progress.rateLimited, skipped: progress.skipped, left: left ?? 0),
            counts: tiles(scraped: count(progress.read), left: left.map(count) ?? .unavailable,
                          listed: count(progress.listed), added: progress.added.map(count) ?? .unavailable),
            show: true)
    }

    /// Listing, and every later stage that carries listing totals. Before
    /// listing there is nothing to tile.
    private static func reportsListing(_ progress: PipelineStatus.LastRun.Progress) -> Bool {
        switch progress.stage {
        case "listing", "near_dup", "exclusions", "logos", "warm", "advance", "backfill", "digest":
            break
        default:
            return false
        }
        if progress.added != nil { return true }
        return progress.total > 0 || progress.done > 0 || progress.read > 0 || progress.listed > 0 || progress.failed > 0
    }

    private static func count(_ n: Int) -> CountValue {
        n < 0 ? .unavailable : .number(n)
    }

    private static func tiles(scraped: CountValue, left: CountValue, listed: CountValue, added: CountValue) -> [Count] {
        [
            Count(id: "scraped", title: "Scraped", symbol: "doc.text.magnifyingglass", value: scraped),
            Count(id: "left", title: "Left", symbol: "hourglass", value: left),
            Count(id: "listed", title: "Listed", symbol: "list.bullet", value: listed),
            Count(id: "added", title: "Added", symbol: "plus.circle", value: added),
        ]
    }

    /// Drawn only when the total is known and above zero. A zero segment is
    /// left out, so its caption is not a second row of tiles. Rate-limited is
    /// its own segment; it is not added on top of failed.
    private static func segments(total: Int, scraped: Int, broken: Int, limited: Int, skipped: Int, left: Int) -> [Segment] {
        guard total > 0 else { return [] }
        var out: [Segment] = []
        func add(_ kind: Segment.Kind, _ n: Int, _ word: String) {
            guard n > 0 else { return }
            out.append(Segment(kind: kind, count: n, caption: "\(n) \(word)"))
        }
        add(.scraped, scraped, "scraped")
        add(.broken, broken, "broken")
        add(.limited, limited, "rate-limited")
        add(.skipped, skipped, "skipped")
        add(.left, left, "left")
        return out
    }

    // MARK: - Boards

    private static func currentBoard(_ progress: PipelineStatus.LastRun.Progress?) -> BoardRow? {
        guard let progress, progress.ok, progress.stage == "listing", progress.index > 0 else { return nil }
        let company = progress.company
        let token = progress.portalID
        let title = company.isEmpty ? (token.isEmpty ? "A board" : token) : company
        let note = progress.phase == "reconciling" ? "saving the listing" : ""
        let inflight = progress.phase != "done"
        return BoardRow(
            id: "current",
            symbol: outcomeSymbol(progress.outcome, inFlight: inflight),
            title: PipelineStatus.bounded(title, max: 120),
            token: company.isEmpty ? "" : token,
            listed: .unavailable, added: .unavailable, note: note, problem: false)
    }

    private static func rows(of boards: PipelineStatus.LastRun.Boards?) -> [BoardRow] {
        guard let boards else { return [] }
        return boards.items.enumerated().map { index, item in
            let company = item.company?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            let token = item.token.isEmpty ? item.ats : "\(item.ats):\(item.token)"
            let read = item.outcome == .read
            return BoardRow(
                id: "\(index)-\(token)",
                symbol: outcomeSymbol(rawOutcome(item.outcome), inFlight: false),
                title: PipelineStatus.bounded(company.isEmpty ? token : company, max: 120),
                token: company.isEmpty ? "" : token,
                listed: item.listed.map(count) ?? .unavailable,
                added: read ? count(item.reconcileNew ?? 0) : .unavailable,
                note: "",
                problem: !read)
        }
    }

    private static func rawOutcome(_ outcome: PipelineStatus.LastRun.Boards.Item.Outcome) -> String {
        switch outcome {
        case .read: "read"
        case .failed: "failed"
        case .rateLimited: "rate_limited"
        case .skipped: "skipped"
        case .unknown: "unknown"
        }
    }

    private static func outcomeSymbol(_ outcome: String, inFlight: Bool) -> String {
        if inFlight { return "arrow.down.circle" }
        switch outcome {
        case "read": return "checkmark.circle.fill"
        case "failed": return "xmark.circle.fill"
        case "rate_limited": return "exclamationmark.triangle.fill"
        case "skipped": return "minus.circle.fill"
        default: return "questionmark.circle"
        }
    }

    private static func footer(of boards: PipelineStatus.LastRun.Boards?) -> String {
        guard let boards else { return "" }
        var parts: [String] = []
        if let more = boards.moreNote(limit: boards.items.count) { parts.append(more) }
        if boards.unrecorded > 0 {
            let n = boards.unrecorded
            parts.append("\(n) board\(n == 1 ? "" : "s") not recorded")
        }
        return parts.joined(separator: " · ")
    }

    private static func listNote(of run: PipelineStatus.LastRun, showingCounts: Bool, rows: [BoardRow]) -> String {
        guard showingCounts, rows.isEmpty, run.state != .running, run.boards == nil else { return "" }
        return "The boards read before this run stopped were not written down."
    }
}
