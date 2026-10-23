import Foundation
import XCTest
@testable import VaniApplyKit

private let now = Date(timeIntervalSince1970: 1_792_000_000) // 2026-10-14
private let finished = now.addingTimeInterval(-3600)
private let started = finished.addingTimeInterval(-300)

private var utc: Calendar {
    var c = Calendar(identifier: .gregorian)
    c.timeZone = TimeZone(identifier: "UTC")!
    return c
}

private typealias Run = PipelineStatus.LastRun
private typealias Advance = Run.Advance

private func lastRun(_ state: Run.State, stage: String? = nil, error: String? = nil, ok: Bool = true, reason: String? = nil,
                 cause: Run.Cause? = nil, digestRoles: Int? = nil,
                 digestedBefore: Int? = 300, digestedAfter: Int? = 312, advance: Advance? = nil,
                 startedAt: Date? = started, finishedAt: Date? = finished) -> Run {
    Run(ok: ok, reason: reason, state: state, startedAt: startedAt, finishedAt: finishedAt, stage: stage, error: error,
        cause: cause, digestRoles: digestRoles,
        countsBefore: digestedBefore.map { ["digested": $0, "live": 40] },
        countsAfter: digestedAfter.map { ["digested": $0, "live": 28] },
        advance: advance)
}

private func notice(_ r: Run, at: Date = now) -> RunNotice? {
    if case let .post(n) = RunNotice.decide(r, at: at, calendar: utc) { return n }
    return nil
}

private func skip(_ r: Run, at: Date = now) -> String? {
    if case let .skip(why) = RunNotice.decide(r, at: at, calendar: utc) { return why }
    return nil
}

private final class Recorder: NoticePoster {
    var posted: [RunNotice] = []
    func post(_ notice: RunNotice) { posted.append(notice) }
}

private final class MemoryStore: AnnouncedRuns {
    var lastAnnounced: String?
}

final class RunNoticeTests: XCTestCase {
    // MARK: Mapping (failureNotice and digestBody in cmd/vani/runnotice.go)

    func testDigestNoticeCountsTheRunsDigestedPostings() {
        let n = notice(lastRun(.ok))
        XCTAssertEqual(n?.title, "Vani digest")
        XCTAssertEqual(n?.body, "12 roles to review")
        XCTAssertEqual(n?.threadID, "vani-run-2026-10-14")
    }

    func testDigestBodyWording() {
        XCTAssertEqual(RunNotice.digestBody(roles: 1, advance: nil), "1 role to review")
        let one = Advance(eligible: 10, limit: 10, attempted: 10, unread: 1, failed: 0, deferred: 0)
        XCTAssertEqual(RunNotice.digestBody(roles: 2, advance: one),
                       "2 roles to review; 1 posting could not be read or scored and waits for the next run")
        let many = Advance(eligible: 10, limit: 10, attempted: 10, unread: 2, failed: 3, deferred: 0)
        XCTAssertEqual(RunNotice.digestBody(roles: 5, advance: many),
                       "5 roles to review; 5 postings could not be read or scored and wait for the next run")
        let clean = Advance(eligible: 10, limit: 10, attempted: 10, deferred: 0)
        XCTAssertEqual(RunNotice.digestBody(roles: 5, advance: clean), "5 roles to review")
    }

    func testEmptyDigestPostsNothing() {
        XCTAssertEqual(skip(lastRun(.ok, digestedBefore: 300, digestedAfter: 300)), "the digest listed no roles")
        // A count that fell (postings left digested during the run) is no digest either.
        XCTAssertNotNil(skip(lastRun(.ok, digestedBefore: 300, digestedAfter: 299)))
    }

    func testDigestWithoutCountsSaysWhy() {
        XCTAssertEqual(skip(lastRun(.ok, digestedBefore: nil, digestedAfter: nil)),
                       "the run record has no ledger counts, so the digest's size is unknown")
    }

    func testFailureVocabulary() {
        let see = " Run vani status for details."
        let cases: [(Run, String)] = [
            (lastRun(.failed, stage: "pipeline", advance: Advance(eligible: 0, limit: 0, attempted: 0, deferred: 0, stopped: .noBackend)),
             "No decision backend is set up, so the boards were listed but nothing was triaged; the postings are in Jobs." + see),
            (lastRun(.failed, stage: "pipeline", advance: Advance(eligible: 9, limit: 9, attempted: 5, unread: 5, deferred: 4, stopped: .unreadStreak)),
             "No decision backend could read the postings; they stay live for the next run." + see),
            (lastRun(.failed, stage: "digest", advance: Advance(eligible: 9, limit: 9, attempted: 9, unread: 9, deferred: 0, stopped: .noneRead)),
             "No decision backend could read the postings; they stay live for the next run." + see),
            (lastRun(.failed, stage: "pipeline", advance: Advance(eligible: 9, limit: 9, attempted: 5, failed: 5, deferred: 4, stopped: .failStreak)),
             "Postings kept failing a stage; they wait for the next run." + see),
            (lastRun(.failed, stage: "digest", advance: Advance(eligible: 9, limit: 9, attempted: 9, failed: 9, deferred: 0, stopped: .noneAdvanced)),
             "Postings kept failing a stage; they wait for the next run." + see),
            (lastRun(.failed, stage: "pipeline"), "The pipeline stopped before the digest." + see),
            (lastRun(.failed, stage: "pipeline", advance: Advance(eligible: 9, limit: 3, attempted: 3, deferred: 6, stopped: .cap)),
             "The pipeline stopped before the digest." + see),
            (lastRun(.failed, stage: "digest"), "The digest could not be finished." + see),
            (lastRun(.failed), "The digest could not be finished." + see),
            (lastRun(.failed, stage: "digest", advance: Advance(eligible: 1, limit: 1, attempted: 1, deferred: 0, stopped: .unknown("new_reason"))),
             "The digest could not be finished." + see),
        ]
        for (r, body) in cases {
            let n = notice(r)
            XCTAssertEqual(n?.title, "Vani run failed", "\(r)")
            XCTAssertEqual(n?.body, body, "\(r)")
        }
    }

    func testCauseVocabulary() {
        let see = " Run vani status for details."
        let unread = Advance(eligible: 9, limit: 9, attempted: 5, unread: 5, deferred: 4, stopped: .unreadStreak)
        let stopped = ("Vani stopped", "The ledger could not be written safely, so Vani stopped. See vani's log.")
        let heldBack = ("Vani digest held back", "Your Jobs marks could not be read; see vani's log")
        let stale = ("Vani config not reloaded", "The config file could not be read, so this run used the previous watchlist." + see)
        let cases: [(Run, (String, String))] = [
            (lastRun(.failed, stage: "digest", cause: .ledgerFatal), stopped),
            // failureNotice's order: a fatal ledger outranks a stop reason and the stage.
            (lastRun(.failed, stage: "pipeline", cause: .ledgerFatal, advance: unread), stopped),
            (lastRun(.failed, stage: "digest", cause: .digestHeldBack), heldBack),
            (lastRun(.failed, stage: "digest", cause: .digestHeldBack, advance: unread), heldBack),
            (lastRun(.failed, stage: "digest", cause: .configStale), stale),
            // A stale config ranks below the stop reason and a pipeline failure.
            (lastRun(.failed, stage: "pipeline", cause: .configStale), ("Vani run failed", "The pipeline stopped before the digest." + see)),
            (lastRun(.failed, stage: "digest", cause: .configStale, advance: unread),
             ("Vani run failed", "No decision backend could read the postings; they stay live for the next run." + see)),
            // A cause a newer vani adds reads as the stage's text.
            (lastRun(.failed, stage: "digest", cause: .unknown("new_cause")), ("Vani run failed", "The digest could not be finished." + see)),
            (lastRun(.failed, stage: "pipeline", cause: .unknown("new_cause")), ("Vani run failed", "The pipeline stopped before the digest." + see)),
        ]
        for (r, want) in cases {
            let n = notice(r)
            XCTAssertEqual(n?.title, want.0, "\(r)")
            XCTAssertEqual(n?.body, want.1, "\(r)")
        }
    }

    func testConfigStaleRunWithItsDigestWrittenIsNotADigestFailure() {
        // The digest was written (digest_roles), but the run is failed at stage
        // digest because the config could not be re-read.
        let n = notice(lastRun(.failed, stage: "digest", cause: .configStale, digestRoles: 5))
        XCTAssertEqual(n?.title, "Vani config not reloaded")
        XCTAssertFalse(n?.body.contains("could not be finished") ?? true)
        // An older vani records no cause: the stage's text, as before, even
        // when the error text names the cause.
        XCTAssertEqual(notice(lastRun(.failed, stage: "digest", error: "config reload failed; this run uses the previous watchlist",
                                      digestRoles: 5))?.body,
                       "The digest could not be finished. Run vani status for details.")
    }

    func testDigestRolesIsTheDigestsSize() {
        // Reconcile moved 12 digested postings to dead in the same run: the
        // ledger's digested count did not move, but the digest listed 12.
        XCTAssertEqual(notice(lastRun(.ok, digestRoles: 12, digestedBefore: 300, digestedAfter: 300))?.body, "12 roles to review")
        // And the other way: the count rose by 12, the digest listed 3.
        XCTAssertEqual(notice(lastRun(.ok, digestRoles: 3))?.body, "3 roles to review")
        XCTAssertEqual(skip(lastRun(.ok, digestRoles: 0)), "the digest listed no roles")
        // Recorded with no ledger counts: the size is still known.
        XCTAssertEqual(notice(lastRun(.ok, digestRoles: 1, digestedBefore: nil, digestedAfter: nil))?.body, "1 role to review")
        // An older vani records none: the counts' difference, as before.
        XCTAssertEqual(RunNotice.digestedRoles(lastRun(.ok)), 12)
    }

    func testDecodesCauseAndDigestRoles() throws {
        func run(_ extra: String) throws -> Run {
            let raw = Data(#"{"ok":true,"state":"failed","started_at":"2026-10-14T07:55:00Z","finished_at":"2026-10-14T08:00:00Z","stage":"digest"\#(extra)}"#.utf8)
            return try JobsClient.decoder.decode(Run.self, from: raw)
        }
        let full = try run(#","cause":"config_stale","digest_roles":4"#)
        XCTAssertEqual(full.cause, .configStale)
        XCTAssertEqual(full.digestRoles, 4)
        XCTAssertEqual(try run(#","cause":"ledger_fatal""#).cause, .ledgerFatal)
        XCTAssertEqual(try run(#","cause":"digest_held_back""#).cause, .digestHeldBack)
        XCTAssertEqual(try run(#","cause":"new_cause""#).cause, .unknown("new_cause"))
        XCTAssertNil(try run(#","cause":"""#).cause)
        let old = try run("")
        XCTAssertNil(old.cause)
        XCTAssertNil(old.digestRoles)
        XCTAssertEqual(try run(#","digest_roles":0"#).digestRoles, 0)
    }

    func testErrorTextNeverReachesTheNotice() {
        let secret = "GET https://boards.example.com/jobs?token=abc123: Jane Doe <jane@example.com>"
        let n = notice(lastRun(.failed, stage: "digest", error: secret))
        XCTAssertNotNil(n)
        for s in [n?.title ?? "", n?.body ?? "", n?.threadID ?? "", n?.key ?? ""] {
            XCTAssertFalse(s.contains("example"), s)
            XCTAssertFalse(s.contains("abc123"), s)
        }
    }

    func testRunsThatNeedNoNotice() {
        XCTAssertEqual(skip(lastRun(.running, finishedAt: nil)), "the run is still going")
        XCTAssertEqual(skip(lastRun(.cancelled, stage: "pipeline")), "the run was cancelled")
        XCTAssertEqual(skip(lastRun(.interrupted, finishedAt: nil)), "the run did not finish (its process is gone)")
        XCTAssertEqual(skip(lastRun(.unknown)), "the run's state is one this app does not know")
    }

    // MARK: Missing or corrupt records

    func testMissingRecordPostsNothingAndSaysSo() {
        let never = Run(state: .never)
        XCTAssertEqual(skip(never), "no run has been recorded yet")
    }

    func testCorruptRecordPostsNothingAndSaysSo() {
        let corrupt = Run(ok: false, reason: "run record is corrupt: unexpected EOF", state: .unknown)
        XCTAssertEqual(skip(corrupt), "the run record could not be read: run record is corrupt: unexpected EOF")
        let unexplained = Run(ok: false, state: .unknown)
        XCTAssertEqual(skip(unexplained), "the run record could not be read")
    }

    func testCorruptStatusJSONPostsNothing() throws {
        // A last_run vani could not have written: the decode fails, and the
        // announcer is told vani could not be read.
        let raw = Data(#"{"ok":true,"state":"ok","started_at":"not a time"}"#.utf8)
        XCTAssertThrowsError(try JobsClient.decoder.decode(Run.self, from: raw))
        let rec = Recorder()
        let a = RunAnnouncer(store: MemoryStore(), poster: rec, lock: NotifierLock())
        a.unreadable("vani printed something that is not JSON")
        XCTAssertTrue(rec.posted.isEmpty)
        XCTAssertEqual(a.lastSkip, "vani status could not be read: vani printed something that is not JSON")
    }

    func testRecordWithoutTimesPostsNothing() {
        XCTAssertEqual(skip(lastRun(.failed, stage: "digest", startedAt: nil)), "the run record has no start or end time")
        XCTAssertEqual(skip(lastRun(.ok, finishedAt: nil)), "the run record has no start or end time")
    }

    func testOldRunIsNotNews() {
        XCTAssertNotNil(notice(lastRun(.ok), at: finished.addingTimeInterval(RunNotice.maxAge - 1)))
        XCTAssertNotNil(skip(lastRun(.ok), at: finished.addingTimeInterval(RunNotice.maxAge + 1)))
    }

    func testDecodesWhatVaniPrints() throws {
        let raw = Data("""
        {"ok":true,"state":"failed","started_at":"2026-10-14T07:55:00.123456789Z","finished_at":"2026-10-14T08:00:00Z",\
        "stage":"pipeline","error":"pipeline: GET https://x.example/jobs: 503",\
        "counts_before":{"digested":3},"counts_after":{"digested":3},\
        "advance":{"eligible":4,"limit":4,"attempted":4,"unread":4,"failed":0,"deferred":0,"stopped":"none_read"}}
        """.utf8)
        let r = try JobsClient.decoder.decode(Run.self, from: raw)
        let n = notice(r, at: Date(timeIntervalSince1970: 1_792_000_000).addingTimeInterval(-6 * 3600))
        XCTAssertEqual(n?.body, "No decision backend could read the postings; they stay live for the next run. Run vani status for details.")
        XCTAssertEqual(n?.threadID, "vani-run-2026-10-14")
    }

    // MARK: Announcing once, while the app holds the notifier lock

    /// An announcer whose app was already open (lock taken at `lockedAt`) when
    /// the daemon's run ended.
    private func announcer(store: AnnouncedRuns = MemoryStore(), poster: NoticePoster, stateDir: String? = nil,
                           lockedAt: Date = started.addingTimeInterval(-60)) throws -> (RunAnnouncer, NotifierLock, String) {
        let dir = try stateDir ?? tempDir(self).path
        let lock = NotifierLock()
        XCTAssertNil(lock.hold(stateDir: dir, at: lockedAt))
        return (RunAnnouncer(store: store, poster: poster, lock: lock), lock, dir)
    }

    func testARunIsAnnouncedOnce() throws {
        let rec = Recorder()
        let store = MemoryStore()
        let (a, lock, dir) = try announcer(store: store, poster: rec)
        let r = lastRun(.ok)
        a.consider(r, stateDir: dir, at: now, calendar: utc)
        a.consider(r, stateDir: dir, at: now, calendar: utc)
        a.consider(r, stateDir: dir, at: now.addingTimeInterval(60), calendar: utc)
        XCTAssertEqual(rec.posted.count, 1)
        XCTAssertEqual(a.lastSkip, "this run was already announced")

        // The app relaunched before the run ended (a new lock, the same store): still quiet.
        lock.release()
        let again = Recorder()
        let (b, _, _) = try announcer(store: store, poster: again, stateDir: dir)
        b.consider(r, stateDir: dir, at: now, calendar: utc)
        XCTAssertTrue(again.posted.isEmpty)
        XCTAssertEqual(b.lastSkip, "this run was already announced")
    }

    func testTheNextRunIsAnnounced() throws {
        let rec = Recorder()
        let (a, _, dir) = try announcer(poster: rec)
        a.consider(lastRun(.ok), stateDir: dir, at: now, calendar: utc)
        let next = lastRun(.failed, stage: "pipeline", startedAt: started.addingTimeInterval(86400),
                           finishedAt: finished.addingTimeInterval(86400))
        a.consider(next, stateDir: dir, at: now.addingTimeInterval(86400), calendar: utc)
        XCTAssertEqual(rec.posted.map(\.title), ["Vani digest", "Vani run failed"])
        XCTAssertNotEqual(rec.posted[0].key, rec.posted[1].key)
        XCTAssertEqual(rec.posted[1].threadID, "vani-run-2026-10-15")
    }

    func testARunningRecordThenItsEndAnnouncesTheEnd() throws {
        let rec = Recorder()
        let (a, _, dir) = try announcer(poster: rec)
        a.consider(lastRun(.running, finishedAt: nil), stateDir: dir, at: now, calendar: utc)
        XCTAssertTrue(rec.posted.isEmpty)
        XCTAssertEqual(a.lastSkip, "the run is still going")
        a.consider(lastRun(.ok), stateDir: dir, at: now, calendar: utc)
        XCTAssertEqual(rec.posted.count, 1)
        XCTAssertNil(a.lastSkip)
    }

    func testSkippedReadsLeaveTheStoreAlone() throws {
        let store = MemoryStore()
        let (a, _, dir) = try announcer(store: store, poster: Recorder())
        a.consider(Run(ok: false, reason: "corrupt", state: .unknown), stateDir: dir, at: now, calendar: utc)
        a.consider(Run(state: .never), stateDir: dir, at: now, calendar: utc)
        XCTAssertNil(store.lastAnnounced)
    }

    func testDefaultsStorePersists() throws {
        let name = "vani-apply-tests-\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: name))
        addTeardownBlock { UserDefaults(suiteName: name)?.removePersistentDomain(forName: name) }
        let rec = Recorder()
        let (a, lock, dir) = try announcer(store: DefaultsAnnouncedRuns(defaults), poster: rec)
        a.consider(lastRun(.ok), stateDir: dir, at: now, calendar: utc)
        lock.release()
        let (b, _, _) = try announcer(store: DefaultsAnnouncedRuns(defaults), poster: rec, stateDir: dir)
        b.consider(lastRun(.ok), stateDir: dir, at: now, calendar: utc)
        XCTAssertEqual(rec.posted.count, 1)
    }

    // MARK: Daemon as the fallback

    func testARunThatEndedBeforeTheAppTookTheLockIsTheDaemons() throws {
        let rec = Recorder()
        let store = MemoryStore()
        // The app opened after the run ended: the daemon saw no lock and posted.
        let (a, _, dir) = try announcer(store: store, poster: rec, lockedAt: finished.addingTimeInterval(60))
        let d = a.consider(lastRun(.ok), stateDir: dir, at: now, calendar: utc)
        XCTAssertEqual(d, .skip("the run finished before this app took over notices, so the daemon announced it"))
        XCTAssertTrue(rec.posted.isEmpty)
        XCTAssertNil(store.lastAnnounced)
    }

    func testTheFirstReadTakesTheLockAndLeavesEarlierRunsToTheDaemon() throws {
        let dir = try tempDir(self).path
        let rec = Recorder()
        let lock = NotifierLock()
        let a = RunAnnouncer(store: MemoryStore(), poster: rec, lock: lock)
        a.consider(lastRun(.ok), stateDir: dir, at: now, calendar: utc)
        XCTAssertTrue(rec.posted.isEmpty)
        XCTAssertEqual(lock.heldSince, now)
        XCTAssertEqual(lock.path, URL(fileURLWithPath: dir).appendingPathComponent("app/notifier.lock").path)
        // The next day's run ends while the app is up: the app announces it.
        let next = lastRun(.ok, startedAt: started.addingTimeInterval(86400), finishedAt: finished.addingTimeInterval(86400))
        a.consider(next, stateDir: dir, at: now.addingTimeInterval(86400), calendar: utc)
        XCTAssertEqual(rec.posted.count, 1)
        XCTAssertEqual(lock.heldSince, now, "keeping the lock does not move its start")
    }

    func testWithoutTheLockTheAppPostsNothing() throws {
        let rec = Recorder()
        let (_, holder, dir) = try announcer(poster: Recorder()) // another copy holds it
        let a = RunAnnouncer(store: MemoryStore(), poster: rec, lock: NotifierLock())
        let d = a.consider(lastRun(.ok), stateDir: dir, at: now, calendar: utc)
        XCTAssertNotNil(holder.heldSince)
        XCTAssertEqual(d, .skip("the daemon announces runs, because this app does not hold the notifier lock: another copy of Vani Apply announces runs"))
        XCTAssertTrue(rec.posted.isEmpty)

        let none = RunAnnouncer(store: MemoryStore(), poster: rec, lock: NotifierLock())
        if case let .skip(why) = none.consider(lastRun(.ok), stateDir: "", at: now, calendar: utc) {
            XCTAssertTrue(why.hasSuffix("vani reported no state folder"), why)
        } else {
            XCTFail("posted without a state folder")
        }
    }

    // MARK: The lock itself

    func testTheLockExcludesAnotherHolderUntilReleased() throws {
        let dir = try tempDir(self).path
        let a = NotifierLock(), b = NotifierLock()
        XCTAssertNil(a.hold(stateDir: dir))
        XCTAssertEqual(b.hold(stateDir: dir), "another copy of Vani Apply announces runs")
        XCTAssertNil(b.heldSince)
        a.release()
        XCTAssertNil(b.hold(stateDir: dir))
        let attrs = try FileManager.default.attributesOfItem(atPath: try XCTUnwrap(b.path))
        XCTAssertEqual((attrs[.posixPermissions] as? NSNumber)?.intValue, 0o600)
    }

    func testTheLockFollowsTheStateFolder() throws {
        let one = try tempDir(self).path, two = try tempDir(self).path
        let lock = NotifierLock(), other = NotifierLock()
        XCTAssertNil(lock.hold(stateDir: one))
        XCTAssertNil(lock.hold(stateDir: two))
        // The first folder's lock was let go when the app moved on.
        XCTAssertNil(other.hold(stateDir: one))
    }

    func testTheLockGoesWithItsHolder() throws {
        let dir = try tempDir(self).path
        do {
            let gone = NotifierLock()
            XCTAssertNil(gone.hold(stateDir: dir))
        }
        XCTAssertNil(NotifierLock().hold(stateDir: dir))
    }

    func testTheLockRefusesASymlink() throws {
        let dir = try tempDir(self)
        let app = dir.appendingPathComponent("app")
        try FileManager.default.createDirectory(at: app, withIntermediateDirectories: true)
        let target = dir.appendingPathComponent("elsewhere")
        try Data().write(to: target)
        try FileManager.default.createSymbolicLink(at: app.appendingPathComponent("notifier.lock"), withDestinationURL: target)
        XCTAssertNotNil(NotifierLock().hold(stateDir: dir.path))
    }
}

