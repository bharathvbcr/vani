import Foundation

// The app announces each finished daily run once, as a native notification
// whose click opens the Jobs tab. It reads the run from `vani status --json`
// (PipelineStatus.lastRun), the same read the Pipeline tab polls, and never the
// state folder itself.
//
// The wording is the daemon's (cmd/vani/runnotice.go: failureNotice and
// digestBody): a fixed vocabulary keyed on the run's state, stage and stop
// reason. The run's error text is never shown: it can carry fetched URLs or
// page content.

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
    static let seeStatus = " Run vani status for details."
    /// A run that finished longer ago than this is history, not news: the app
    /// opened days after a run does not announce it.
    public static let maxAge: TimeInterval = PipelineStatus.staleAfter

    /// The notice for the last run, or why there is none. Pure: the caller
    /// checks the key against what was already announced.
    public static func decide(_ run: PipelineStatus.LastRun, at now: Date = Date(),
                              calendar: Calendar = .current) -> RunNoticeDecision {
        guard run.ok else {
            return .skip("the run record could not be read" + (run.reason.map { ": " + PipelineStatus.bounded($0, max: 120) } ?? ""))
        }
        switch run.state {
        case .never: return .skip("no run has been recorded yet")
        case .running: return .skip("the run is still going")
        case .cancelled: return .skip("the run was cancelled")
        case .interrupted: return .skip("the run did not finish (its process is gone)")
        case .unknown: return .skip("the run's state is one this app does not know")
        case .ok, .failed: break
        }
        guard let started = run.startedAt, let finished = run.finishedAt else {
            return .skip("the run record has no start or end time")
        }
        if now.timeIntervalSince(finished) > maxAge {
            return .skip("the run finished over \(PipelineStatus.age(maxAge)) ago")
        }
        let key = "run-" + String(Int64((started.timeIntervalSince1970 * 1000).rounded()))
        let thread = "vani-run-" + day(finished, calendar)

        if run.state == .failed {
            let (title, body) = failure(run)
            return .post(RunNotice(title: title, body: body, threadID: thread, key: key, finishedAt: finished))
        }
        guard let roles = digestedRoles(run) else {
            return .skip("the run record has no ledger counts, so the digest's size is unknown")
        }
        guard roles > 0 else { return .skip("the digest listed no roles") }
        return .post(RunNotice(title: "Vani digest", body: digestBody(roles: roles, advance: run.advance),
                               threadID: thread, key: key, finishedAt: finished))
    }

    /// failureNotice's text for the cases the run record can tell apart. A run
    /// stopped by a fatal ledger error, a held-back digest or a stale config is
    /// not marked as such in the record, so it reads as the stage's default.
    static func failure(_ run: PipelineStatus.LastRun) -> (String, String) {
        switch run.advance?.stopped {
        case .unreadStreak?, .noneRead?:
            return ("Vani run failed", "No decision backend could read the postings; they stay live for the next run." + seeStatus)
        case .failStreak?, .noneAdvanced?:
            return ("Vani run failed", "Postings kept failing a stage; they wait for the next run." + seeStatus)
        default:
            break
        }
        if run.stage == "pipeline" {
            return ("Vani run failed", "The pipeline stopped before the digest." + seeStatus)
        }
        return ("Vani run failed", "The digest could not be finished." + seeStatus)
    }

    /// Postings the run's digest marked digested: the run's own change in the
    /// ledger's digested count. Nil when either end was not recorded.
    static func digestedRoles(_ run: PipelineStatus.LastRun) -> Int? {
        guard let before = run.countsBefore, let after = run.countsAfter else { return nil }
        return (after["digested"] ?? 0) - (before["digested"] ?? 0)
    }

    /// digestBody: how many roles to review and, when the advance phase left
    /// postings it could not read or score, how many wait for the next run.
    static func digestBody(roles: Int, advance: PipelineStatus.LastRun.Advance?) -> String {
        var body = roles == 1 ? "1 role to review" : "\(roles) roles to review"
        guard let advance else { return body }
        let left = advance.unread + advance.failed
        if left == 1 {
            body += "; 1 posting could not be read or scored and waits for the next run"
        } else if left > 1 {
            body += "; \(left) postings could not be read or scored and wait for the next run"
        }
        return body
    }

    static func day(_ d: Date, _ calendar: Calendar) -> String {
        let c = calendar.dateComponents([.year, .month, .day], from: d)
        return String(format: "%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0)
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

/// Decides, dedupes and posts.
public final class RunAnnouncer {
    private let store: AnnouncedRuns
    private let poster: NoticePoster
    /// Why the last read posted nothing, or nil after a post.
    public private(set) var lastSkip: String?

    private let lock: NotifierLock

    public init(store: AnnouncedRuns, poster: NoticePoster, lock: NotifierLock) {
        self.store = store
        self.poster = poster
        self.lock = lock
    }

    /// Announces the run if it needs a notice that was never posted.
    ///
    /// Every read first takes (or keeps) the notifier lock in stateDir, so the
    /// daemon stops posting as soon as the app is up. A run is the app's only
    /// if it finished after the lock was taken; one that finished before it was
    /// the daemon's, and the app stays quiet rather than announce it twice.
    ///
    /// The key is stored before posting: a crash in between loses one notice
    /// rather than posting it twice.
    @discardableResult
    public func consider(_ run: PipelineStatus.LastRun, stateDir: String, at now: Date = Date(),
                         calendar: Calendar = .current) -> RunNoticeDecision {
        let held = lock.hold(stateDir: stateDir, at: now)
        let decision = RunNotice.decide(run, at: now, calendar: calendar)
        switch decision {
        case let .skip(why):
            lastSkip = why
            return decision
        case let .post(notice):
            if let held {
                return skip("the daemon announces runs, because this app does not hold the notifier lock: " + held)
            }
            if let since = lock.heldSince, notice.finishedAt < since {
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
