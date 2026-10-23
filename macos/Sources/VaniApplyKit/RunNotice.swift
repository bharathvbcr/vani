import Foundation

// The app announces each finished daily run once, as a native notification
// whose click opens the Jobs tab. It reads the run from `vani status --json`
// (PipelineStatus.lastRun), the same read the Pipeline tab polls, and never the
// state folder itself.
//
// vani owns the notice: whether to announce, the title and body, the key and
// the thread (cmd/vani/runnotice.go: decideNotice). The app posts what vani
// says and keeps only what needs this process: the notifier lock, and the
// record of which key it already posted. A notice of a version this app does
// not read is refused loudly, never guessed at.

/// The `notice` object in `last_run` (cmd/vani/runnotice.go: noticeFacet).
public enum NoticeWire: Decodable, Equatable, Sendable {
    /// The notice version this app reads (runNoticeVersion in vani).
    public static let supportedVersion = 1

    case post(title: String, body: String, threadID: String, key: String)
    /// Nothing to announce, and why.
    case skip(String)
    /// A notice this app cannot read: the reason says so, and the app announces nothing.
    case unsupported(String)

    enum CodingKeys: String, CodingKey {
        case v, announce, reason, title, body, key
        case threadID = "thread_id"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let version = try c.decodeIfPresent(Int.self, forKey: .v)
        guard version == Self.supportedVersion else {
            self = .unsupported("vani's run notice is version \(version.map(String.init) ?? "missing"); "
                + "this app reads version \(Self.supportedVersion). Rebuild the app.")
            return
        }
        guard try c.decode(Bool.self, forKey: .announce) else {
            self = .skip(try c.decodeIfPresent(String.self, forKey: .reason) ?? "vani gave no reason")
            return
        }
        guard let title = try c.decodeIfPresent(String.self, forKey: .title), !title.isEmpty,
              let body = try c.decodeIfPresent(String.self, forKey: .body), !body.isEmpty,
              let thread = try c.decodeIfPresent(String.self, forKey: .threadID),
              let key = try c.decodeIfPresent(String.self, forKey: .key), !key.isEmpty else {
            self = .unsupported("vani's run notice says to announce but has no title, body, thread or key")
            return
        }
        self = .post(title: title, body: body, threadID: thread, key: key)
    }
}

/// One notification for one run.
public struct RunNotice: Equatable, Sendable {
    public var title: String
    public var body: String
    /// Groups a day's notices together (UNNotificationContent.threadIdentifier).
    public var threadID: String
    /// Names the run; a run whose key was announced is never announced again.
    public var key: String
    /// When the run ended: a run that ended before the app held the notifier
    /// lock was the daemon's to announce.
    public var finishedAt: Date
}

/// What the announcer decided for one read of the run.
public enum RunNoticeDecision: Equatable, Sendable {
    case post(RunNotice)
    /// Nothing to post, and why: a check that could not run says so here
    /// rather than looking like a quiet day.
    case skip(String)
}

extension RunNotice {
    /// vani's notice for the last run, or why there is none. Pure: the caller
    /// checks the lock and the key against what was already announced.
    public static func read(_ run: PipelineStatus.LastRun) -> RunNoticeDecision {
        switch run.notice {
        case nil:
            return .skip("this vani sends no run notice, so none is announced; rebuild vani")
        case let .unsupported(why)?, let .skip(why)?:
            return .skip(why)
        case let .post(title, body, threadID, key)?:
            guard let finished = run.finishedAt else {
                return .skip("vani's run notice has no end time to check against the notifier lock")
            }
            return .post(RunNotice(title: title, body: body, threadID: threadID, key: key, finishedAt: finished))
        }
    }
}

// MARK: - Announcing once

/// Remembers which runs were announced. Only the latest run is ever in the
/// record, so the latest key is all there is to remember.
public protocol AnnouncedRuns: AnyObject {
    var lastAnnounced: String? { get set }
}

/// AnnouncedRuns in UserDefaults.
public final class DefaultsAnnouncedRuns: AnnouncedRuns {
    static let key = "lastAnnouncedRun"
    private let defaults: UserDefaults

    public init(_ defaults: UserDefaults = .standard) { self.defaults = defaults }

    public var lastAnnounced: String? {
        get { defaults.string(forKey: Self.key) }
        set { defaults.set(newValue, forKey: Self.key) }
    }
}

/// Delivers a notice (UNUserNotificationCenter in the app, a recorder in tests).
public protocol NoticePoster: AnyObject {
    func post(_ notice: RunNotice)
}

/// Takes vani's notice, dedupes and posts.
public final class RunAnnouncer {
    private let store: AnnouncedRuns
    private let poster: NoticePoster
    /// Why the last read posted nothing, or nil after a post.
    public private(set) var lastSkip: String?

    private let lock: NotifierLock
    /// How long before taking the lock a run may have finished and still be
    /// this app's to announce: AnnounceLaunch.grace when the daemon launched the
    /// app to announce it, else 0.
    private let handoffGrace: TimeInterval

    public init(store: AnnouncedRuns, poster: NoticePoster, lock: NotifierLock, handoffGrace: TimeInterval = 0) {
        self.store = store
        self.poster = poster
        self.lock = lock
        self.handoffGrace = max(0, handoffGrace)
    }

    /// Announces the run if vani's notice says to and it was never posted.
    ///
    /// Every read first takes (or keeps) the notifier lock in stateDir, so the
    /// daemon stops posting as soon as the app is up. A run is the app's only
    /// if it finished after the lock was taken; one that finished before it was
    /// the daemon's, and the app stays quiet rather than announce it twice.
    ///
    /// The key is stored before posting: a crash in between loses one notice
    /// rather than posting it twice.
    @discardableResult
    public func consider(_ run: PipelineStatus.LastRun, stateDir: String, at now: Date = Date()) -> RunNoticeDecision {
        let held = lock.hold(stateDir: stateDir, at: now)
        let decision = RunNotice.read(run)
        switch decision {
        case let .skip(why):
            lastSkip = why
            return decision
        case let .post(notice):
            if let held {
                return skip("the daemon announces runs, because this app does not hold the notifier lock: " + held)
            }
            if let since = lock.heldSince, notice.finishedAt < since.addingTimeInterval(-handoffGrace) {
                return skip("the run finished before this app took over notices, so the daemon announced it")
            }
            if store.lastAnnounced == notice.key {
                let why = "this run was already announced"
                lastSkip = why
                return .skip(why)
            }
            store.lastAnnounced = notice.key
            lastSkip = nil
            poster.post(notice)
            return decision
        }
    }

    private func skip(_ why: String) -> RunNoticeDecision {
        lastSkip = why
        return .skip(why)
    }

    /// vani could not answer at all: nothing is posted, and the reason is kept.
    public func unreadable(_ error: String) {
        lastSkip = "vani status could not be read: " + PipelineStatus.bounded(error, max: 120)
    }
}

/// The daemon's handoff (internal/notify ViaApp). When the app is not running,
/// the daemon launches it in the background with `-VaniApplyAnnounce YES`
/// instead of posting through osascript, which macOS attributes to Script
/// Editor. The run that prompted the launch has often just finished, before the
/// app could take the notifier lock, so such a launch announces runs that
/// finished up to `grace` before it. The daemon passes exactly YES; anything
/// else is an ordinary launch, which leaves earlier runs to the daemon.
public enum AnnounceLaunch {
    public static let argument = "-VaniApplyAnnounce"
    /// Longer than the daemon's wait for the lock (20 s) plus a slow first
    /// status read, and short enough that an old run is not news.
    public static let grace: TimeInterval = 10 * 60

    /// The handoff grace for this launch: `grace` when `arguments`
    /// (CommandLine.arguments, executable first) end the switch with YES, else 0.
    public static func handoffGrace(arguments: [String]) -> TimeInterval {
        let args = Array(arguments.dropFirst())
        guard let i = args.lastIndex(of: argument), i + 1 < args.count, args[i + 1] == "YES" else { return 0 }
        return grace
    }
}
