import Foundation
import XCTest
@testable import VaniApplyKit

// vani owns the run notice (cmd/vani/runnotice.go: decideNotice; its cases live
// in cmd/vani/runnoticewire_test.go). These tests hold what stays in the app:
// decoding vani's notice, refusing a version it does not read, the notifier
// lock and announcing each key once. The golden files under Fixtures/runnotice
// are written by `go test ./cmd/vani -run TestRunNoticeGolden -update`.

private let now = Date(timeIntervalSince1970: 1_792_000_000) // 2026-10-14
private let finished = now.addingTimeInterval(-3600)
private let started = finished.addingTimeInterval(-300)

private typealias Run = PipelineStatus.LastRun

/// A run as vani would print it, with the notice a vani of this version sends.
private func lastRun(_ state: Run.State, stage: String? = nil, startedAt: Date? = started, finishedAt: Date? = finished) -> Run {
    let notice: NoticeWire
    switch (state, startedAt) {
    case (.ok, let s?), (.failed, let s?):
        let day = ISO8601DateFormatter().string(from: finishedAt ?? finished).prefix(10)
        notice = .post(title: state == .ok ? "Vani digest" : "Vani run failed", body: "body",
                       threadID: "vani-run-\(day)", key: "run-\(Int64((s.timeIntervalSince1970 * 1000).rounded()))")
    default:
        notice = .skip("the run is still going")
    }
    return Run(state: state, startedAt: startedAt, finishedAt: finishedAt, stage: stage, notice: notice)
}

private func golden(_ name: String) throws -> Run {
    let url = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        .appendingPathComponent("Fixtures/runnotice/\(name).json")
    return try JobsClient.decoder.decode(Run.self, from: try Data(contentsOf: url))
}

private final class Recorder: NoticePoster {
    var posted: [RunNotice] = []
    func post(_ notice: RunNotice) { posted.append(notice) }
}

private final class MemoryStore: AnnouncedRuns {
    var lastAnnounced: String?
}

final class RunNoticeTests: XCTestCase {
    // MARK: Replaying vani's golden notices

    private func goldenNames() throws -> [String] {
        let dir = URL(fileURLWithPath: #filePath).deletingLastPathComponent().appendingPathComponent("Fixtures/runnotice")
        return try FileManager.default.contentsOfDirectory(atPath: dir.path)
            .filter { $0.hasSuffix(".json") }.map { String($0.dropLast(5)) }.sorted()
    }

    /// Every golden file decodes, and the app posts exactly the title and body
    /// vani wrote when it says to announce, and nothing, with vani's reason,
    /// when it does not. Only the notifier lock and the key are the app's.
    func testGoldenNoticesArePostedAsWritten() throws {
        let names = try goldenNames()
        XCTAssertGreaterThanOrEqual(names.count, 10, "the golden set was not found")
        var posts = 0, skips = 0
        for name in names {
            let run = try golden(name)
            let raw = try JSONSerialization.jsonObject(with: Data(contentsOf: URL(fileURLWithPath: #filePath)
                .deletingLastPathComponent().appendingPathComponent("Fixtures/runnotice/\(name).json"))) as! [String: Any]
            let notice = raw["notice"] as! [String: Any]
            XCTAssertEqual(notice["v"] as? Int, NoticeWire.supportedVersion, name)
            let rec = Recorder()
            // Held since before the run, so only vani's own word decides.
            let dir = try tempDir(self).path
            let held = NotifierLock()
            XCTAssertNil(held.hold(stateDir: dir, at: started.addingTimeInterval(-60)))
            let announcer = RunAnnouncer(store: MemoryStore(), poster: rec, lock: held)
            let d = announcer.consider(run, stateDir: dir, at: now)
            if notice["announce"] as? Bool == true {
                posts += 1
                XCTAssertEqual(rec.posted.map(\.title), [notice["title"] as? String], name)
                XCTAssertEqual(rec.posted.map(\.body), [notice["body"] as? String], name)
                XCTAssertEqual(rec.posted.map(\.threadID), [notice["thread_id"] as? String], name)
                XCTAssertEqual(rec.posted.map(\.key), [notice["key"] as? String], name)
                XCTAssertNil(announcer.lastSkip, name)
            } else {
                skips += 1
                XCTAssertTrue(rec.posted.isEmpty, name)
                XCTAssertEqual(d, .skip(notice["reason"] as! String), name)
            }
        }
        XCTAssertGreaterThan(posts, 0)
        XCTAssertGreaterThan(skips, 0)
    }

    func testGoldenWordsAreVanisNotTheApps() throws {
        guard case let .post(n) = RunNotice.read(try golden("failed_config")) else { return XCTFail("did not post") }
        XCTAssertEqual(n.title, "Vani config not reloaded")
        XCTAssertEqual(n.body, "The config file could not be read, so this run used the previous watchlist. Run vani status for details.")
        XCTAssertEqual(n.threadID, "vani-run-2026-10-14")
        XCTAssertEqual(n.key, "run-1791996100000")
        XCTAssertEqual(n.finishedAt, finished.addingTimeInterval(0))
        guard case let .post(d) = RunNotice.read(try golden("digest")) else { return XCTFail("did not post") }
        XCTAssertEqual(d.body, "12 roles to review")
    }

    // MARK: Versions and absent or malformed notices are loud

    private func run(notice: String?) throws -> Run {
        let extra = notice.map { #","notice":\#($0)"# } ?? ""
        let raw = Data(#"{"ok":true,"state":"ok","started_at":"2026-10-14T07:55:00Z","finished_at":"2026-10-14T08:00:00Z"\#(extra)}"#.utf8)
        return try JobsClient.decoder.decode(Run.self, from: raw)
    }

    func testAnUnknownNoticeVersionIsRefusedLoudly() throws {
        let future = try run(notice: #"{"v":2,"announce":true,"title":"t","body":"b","thread_id":"x","key":"k"}"#)
        let want = RunNoticeDecision.skip("vani's run notice is version 2; this app reads version 1. Rebuild the app.")
        XCTAssertEqual(RunNotice.read(future), want)
        let rec = Recorder()
        let a = RunAnnouncer(store: MemoryStore(), poster: rec, lock: NotifierLock())
        XCTAssertEqual(a.consider(future, stateDir: try tempDir(self).path, at: now), want)
        XCTAssertTrue(rec.posted.isEmpty)
        XCTAssertNotNil(a.lastSkip)

        let missing = try run(notice: #"{"announce":true,"title":"t","body":"b","thread_id":"x","key":"k"}"#)
        XCTAssertEqual(RunNotice.read(missing), .skip("vani's run notice is version missing; this app reads version 1. Rebuild the app."))
    }

    func testAMalformedNoticeIsRefusedNotPosted() throws {
        for bad in [#"[]"#, #""text""#, #"{"v":1}"#, #"{"v":1,"announce":true,"title":"","body":"b","thread_id":"x","key":"k"}"#,
                    #"{"v":1,"announce":true,"title":"t","body":"b","thread_id":"x"}"#] {
            guard case .skip = RunNotice.read(try run(notice: bad)) else { return XCTFail("posted for \(bad)") }
        }
    }

    func testAnAbsentNoticeIsNotAQuietDay() throws {
        guard case let .skip(why) = RunNotice.read(try run(notice: nil)) else { return XCTFail("posted without a notice") }
        XCTAssertTrue(why.contains("sends no run notice"), why)
    }

    func testAnnounceWithoutAnEndTimeIsRefused() throws {
        let r = Run(state: .ok, startedAt: started, finishedAt: nil,
                    notice: .post(title: "t", body: "b", threadID: "x", key: "k"))
        guard case .skip = RunNotice.read(r) else { return XCTFail("posted with no end time") }
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

    func testErrorTextNeverReachesThePostedNotice() throws {
        // The app never sees the run's error text as notice text: it posts only vani's words.
        let r = try run(notice: #"{"v":1,"announce":true,"title":"Vani run failed","body":"The digest could not be finished.","thread_id":"vani-run-2026-10-14","key":"k"}"#)
        guard case let .post(n) = RunNotice.read(r) else { return XCTFail("did not post") }
        XCTAssertEqual([n.title, n.body], ["Vani run failed", "The digest could not be finished."])
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
        a.consider(r, stateDir: dir, at: now)
        a.consider(r, stateDir: dir, at: now)
        a.consider(r, stateDir: dir, at: now.addingTimeInterval(60))
        XCTAssertEqual(rec.posted.count, 1)
        XCTAssertEqual(a.lastSkip, "this run was already announced")

        // The app relaunched before the run ended (a new lock, the same store): still quiet.
        lock.release()
        let again = Recorder()
        let (b, _, _) = try announcer(store: store, poster: again, stateDir: dir)
        b.consider(r, stateDir: dir, at: now)
        XCTAssertTrue(again.posted.isEmpty)
        XCTAssertEqual(b.lastSkip, "this run was already announced")
    }

    func testTheNextRunIsAnnounced() throws {
        let rec = Recorder()
        let (a, _, dir) = try announcer(poster: rec)
        a.consider(lastRun(.ok), stateDir: dir, at: now)
        let next = lastRun(.failed, stage: "pipeline", startedAt: started.addingTimeInterval(86400),
                           finishedAt: finished.addingTimeInterval(86400))
        a.consider(next, stateDir: dir, at: now.addingTimeInterval(86400))
        XCTAssertEqual(rec.posted.map(\.title), ["Vani digest", "Vani run failed"])
        XCTAssertNotEqual(rec.posted[0].key, rec.posted[1].key)
        XCTAssertEqual(rec.posted[1].threadID, "vani-run-2026-10-15")
    }

    func testARunningRecordThenItsEndAnnouncesTheEnd() throws {
        let rec = Recorder()
        let (a, _, dir) = try announcer(poster: rec)
        a.consider(lastRun(.running, finishedAt: nil), stateDir: dir, at: now)
        XCTAssertTrue(rec.posted.isEmpty)
        XCTAssertEqual(a.lastSkip, "the run is still going")
        a.consider(lastRun(.ok), stateDir: dir, at: now)
        XCTAssertEqual(rec.posted.count, 1)
        XCTAssertNil(a.lastSkip)
    }

    func testSkippedReadsLeaveTheStoreAlone() throws {
        let store = MemoryStore()
        let (a, _, dir) = try announcer(store: store, poster: Recorder())
        a.consider(Run(ok: false, reason: "corrupt", state: .unknown), stateDir: dir, at: now)
        a.consider(Run(state: .never), stateDir: dir, at: now)
        XCTAssertNil(store.lastAnnounced)
    }

    func testDefaultsStorePersists() throws {
        let name = "vani-apply-tests-\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: name))
        addTeardownBlock { UserDefaults(suiteName: name)?.removePersistentDomain(forName: name) }
        let rec = Recorder()
        let (a, lock, dir) = try announcer(store: DefaultsAnnouncedRuns(defaults), poster: rec)
        a.consider(lastRun(.ok), stateDir: dir, at: now)
        lock.release()
        let (b, _, _) = try announcer(store: DefaultsAnnouncedRuns(defaults), poster: rec, stateDir: dir)
        b.consider(lastRun(.ok), stateDir: dir, at: now)
        XCTAssertEqual(rec.posted.count, 1)
    }

    // MARK: Daemon as the fallback

    func testARunThatEndedBeforeTheAppTookTheLockIsTheDaemons() throws {
        let rec = Recorder()
        let store = MemoryStore()
        // The app opened after the run ended: the daemon saw no lock and posted.
        let (a, _, dir) = try announcer(store: store, poster: rec, lockedAt: finished.addingTimeInterval(60))
        let d = a.consider(lastRun(.ok), stateDir: dir, at: now)
        XCTAssertEqual(d, .skip("the run finished before this app took over notices, so the daemon announced it"))
        XCTAssertTrue(rec.posted.isEmpty)
        XCTAssertNil(store.lastAnnounced)
    }

    // MARK: The daemon's handoff (AnnounceLaunch)

    func testAHandoffLaunchAnnouncesTheRunThatJustEnded() throws {
        let dir = try tempDir(self).path
        let rec = Recorder()
        let a = RunAnnouncer(store: MemoryStore(), poster: rec, lock: NotifierLock(), handoffGrace: AnnounceLaunch.grace)
        // The daemon finished the run, then launched the app: it took the lock 30 s later.
        let d = a.consider(lastRun(.failed, stage: "pipeline"), stateDir: dir, at: finished.addingTimeInterval(30), calendar: utc)
        guard case .post = d else { return XCTFail("decision \(d)") }
        XCTAssertEqual(rec.posted.map(\.title), ["Vani run failed"])
    }

    func testAHandoffLaunchStillLeavesOlderRunsToTheDaemon() throws {
        let rec = Recorder()
        let store = MemoryStore()
        let dir = try tempDir(self).path
        let a = RunAnnouncer(store: store, poster: rec, lock: NotifierLock(), handoffGrace: AnnounceLaunch.grace)
        let late = finished.addingTimeInterval(AnnounceLaunch.grace + 1)
        let d = a.consider(lastRun(.ok), stateDir: dir, at: late, calendar: utc)
        XCTAssertEqual(d, .skip("the run finished before this app took over notices, so the daemon announced it"))
        XCTAssertTrue(rec.posted.isEmpty)
        XCTAssertNil(store.lastAnnounced)
    }

    func testAHandoffLaunchDoesNotRepeatAnAnnouncedRun() throws {
        let dir = try tempDir(self).path
        let rec = Recorder()
        let store = MemoryStore()
        let a = RunAnnouncer(store: store, poster: rec, lock: NotifierLock(), handoffGrace: AnnounceLaunch.grace)
        a.consider(lastRun(.ok), stateDir: dir, at: finished.addingTimeInterval(5), calendar: utc)
        // The daemon's second notice for the same run (config stale after the
        // digest) finds the lock held; a relaunch would also find the key.
        a.consider(lastRun(.ok), stateDir: dir, at: finished.addingTimeInterval(65), calendar: utc)
        XCTAssertEqual(rec.posted.count, 1)
    }

    func testOnlyYesMakesAHandoffLaunch() {
        let exe = "/Applications/Vani Apply.app/Contents/MacOS/VaniApply"
        XCTAssertEqual(AnnounceLaunch.handoffGrace(arguments: [exe, "-VaniApplyAnnounce", "YES"]), AnnounceLaunch.grace)
        XCTAssertEqual(AnnounceLaunch.handoffGrace(arguments: [exe]), 0)
        XCTAssertEqual(AnnounceLaunch.handoffGrace(arguments: [exe, "-VaniApplyAnnounce"]), 0)
        XCTAssertEqual(AnnounceLaunch.handoffGrace(arguments: [exe, "-VaniApplyAnnounce", "NO"]), 0)
        XCTAssertEqual(AnnounceLaunch.handoffGrace(arguments: [exe, "-VaniApplyAnnounce", "yes "]), 0)
        XCTAssertEqual(AnnounceLaunch.handoffGrace(arguments: [exe, "-VaniApplyAnnounce=YES"]), 0)
        // The executable path is never read as the switch.
        XCTAssertEqual(AnnounceLaunch.handoffGrace(arguments: ["-VaniApplyAnnounce", "YES"]), 0)
        XCTAssertEqual(AnnounceLaunch.handoffGrace(arguments: []), 0)
        // Last occurrence wins.
        XCTAssertEqual(AnnounceLaunch.handoffGrace(arguments: [exe, "-VaniApplyAnnounce", "YES", "-VaniApplyAnnounce", "NO"]), 0)
    }

    func testANegativeGraceIsNoGrace() throws {
        let dir = try tempDir(self).path
        let rec = Recorder()
        let lock = NotifierLock()
        XCTAssertNil(lock.hold(stateDir: dir, at: finished))
        let a = RunAnnouncer(store: MemoryStore(), poster: rec, lock: lock, handoffGrace: -3600)
        // Finished exactly at the lock: still the app's, as without a grace.
        a.consider(lastRun(.ok), stateDir: dir, at: now, calendar: utc)
        XCTAssertEqual(rec.posted.count, 1)
    }

    func testTheFirstReadTakesTheLockAndLeavesEarlierRunsToTheDaemon() throws {
        let dir = try tempDir(self).path
        let rec = Recorder()
        let lock = NotifierLock()
        let a = RunAnnouncer(store: MemoryStore(), poster: rec, lock: lock)
        a.consider(lastRun(.ok), stateDir: dir, at: now)
        XCTAssertTrue(rec.posted.isEmpty)
        XCTAssertEqual(lock.heldSince, now)
        XCTAssertEqual(lock.path, URL(fileURLWithPath: dir).appendingPathComponent("app/notifier.lock").path)
        // The next day's run ends while the app is up: the app announces it.
        let next = lastRun(.ok, startedAt: started.addingTimeInterval(86400), finishedAt: finished.addingTimeInterval(86400))
        a.consider(next, stateDir: dir, at: now.addingTimeInterval(86400))
        XCTAssertEqual(rec.posted.count, 1)
        XCTAssertEqual(lock.heldSince, now, "keeping the lock does not move its start")
    }

    func testWithoutTheLockTheAppPostsNothing() throws {
        let rec = Recorder()
        let (_, holder, dir) = try announcer(poster: Recorder()) // another copy holds it
        let a = RunAnnouncer(store: MemoryStore(), poster: rec, lock: NotifierLock())
        let d = a.consider(lastRun(.ok), stateDir: dir, at: now)
        XCTAssertNotNil(holder.heldSince)
        XCTAssertEqual(d, .skip("the daemon announces runs, because this app does not hold the notifier lock: another copy of Vani Apply announces runs"))
        XCTAssertTrue(rec.posted.isEmpty)

        let none = RunAnnouncer(store: MemoryStore(), poster: rec, lock: NotifierLock())
        if case let .skip(why) = none.consider(lastRun(.ok), stateDir: "", at: now) {
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

