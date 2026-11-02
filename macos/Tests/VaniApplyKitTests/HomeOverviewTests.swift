import Foundation
import XCTest
@testable import VaniApplyKit

private let now = Date(timeIntervalSince1970: 1_792_000_000)

private func iso(_ d: Date) -> String { d.formatted(Date.ISO8601FormatStyle()) }

/// A `vani status --json` reply; each facet can be swapped for a bad one.
private func status(config: String = #"{"ok":true,"boards":42}"#,
                    ledger: String = #"{"ok":true,"exists":true,"path":"/s","total":812,"by_status":{}}"#,
                    lastRun: String? = nil) throws -> PipelineStatus {
    let run = lastRun ?? #"{"ok":true,"state":"ok","finished_at":"\#(iso(now.addingTimeInterval(-3 * 3600)))","postings_before":780,"postings_after":812}"#
    let json = """
    {"version":1,"generated_at":"\(iso(now))","state_dir":"/s","config":\(config),"ledger":\(ledger),"last_run":\(run),\
    "schedule":{"ok":true},"backends":{"ok":true,"day":"2026-10-14","items":[]},\
    "decide":{"ok":true,"local_configured":false,"lappi_configured":false}}
    """
    return try JobsClient.decoder.decode(PipelineStatus.self, from: Data(json.utf8))
}

private func job(_ key: String, priority: Double? = nil, relevance: Double? = nil, mark: JobMark? = nil, closed: Bool? = nil) -> Job {
    var j = Job(key: key, company: "Co", title: "T " + key)
    j.priority = priority
    j.relevance = relevance
    j.mark = mark
    j.closed = closed
    return j
}

private let en = Locale(identifier: "en_US")
private func clock(_ d: Date) -> String { "T\(Int(d.timeIntervalSince1970) % 100000)" }

final class HomeOverviewTests: XCTestCase {
    // MARK: status line

    func testStatusLineHealthy() throws {
        let line = HomeOverview.statusLine(jobs: .ready(JobCounts(new: 12, saved: 0, applied: 0)),
                                           pipeline: .ready(try status()), now: now, timeText: { _ in "6:02 AM" })
        XCTAssertEqual(line, "12 new roles · pipeline healthy · last run 6:02 AM")
    }

    func testStatusLineDistinguishesUnloadedFailedAndZero() throws {
        func line(_ jobs: Reading<JobCounts>) -> String {
            HomeOverview.statusLine(jobs: jobs, pipeline: .pending, now: now, timeText: clock)
        }
        let pending = line(.pending), failed = line(.failed("boom")), zero = line(.ready(JobCounts(new: 0, saved: 0, applied: 0)))
        XCTAssertEqual(Set([pending, failed, zero]).count, 3, "loading, unreadable and zero must read differently")
        XCTAssertTrue(zero.hasPrefix("No new roles"))
        XCTAssertTrue(failed.hasPrefix("Roles unavailable"))
        XCTAssertTrue(pending.hasPrefix("Loading roles"))
    }

    func testStatusLineSingularAndPipelineFailure() {
        let line = HomeOverview.statusLine(jobs: .ready(JobCounts(new: 1, saved: 0, applied: 0)),
                                           pipeline: .failed("vani missing"), now: now, timeText: clock)
        XCTAssertEqual(line, "1 new role · pipeline status unavailable")
    }

    func testStatusLineWithoutRunTimestampOmitsTheRunPhrase() throws {
        let s = try status(lastRun: #"{"ok":true,"state":"ok"}"#)
        let line = HomeOverview.statusLine(jobs: .pending, pipeline: .ready(s), now: now, timeText: clock)
        XCTAssertFalse(line.contains("last run"), line)
    }

    func testStatusLineNeverRunAndUnreadableRun() throws {
        let never = try status(lastRun: #"{"ok":true,"state":"none"}"#)
        XCTAssertTrue(HomeOverview.statusLine(jobs: .pending, pipeline: .ready(never), now: now, timeText: clock).hasSuffix("no run recorded"))
        let bad = try status(lastRun: #"{"ok":false,"state":"unknown","reason":"torn"}"#)
        XCTAssertTrue(HomeOverview.statusLine(jobs: .pending, pipeline: .ready(bad), now: now, timeText: clock).hasSuffix("last run unreadable"))
        let progress = #"{"ok":true,"stage":"listing","phase":"listing","index":2,"total":5,"company":"Acme","ats":"gh","token":"acme"}"#
        let going = try status(lastRun: #"{"ok":true,"state":"running","progress":\#(progress)}"#)
        XCTAssertTrue(HomeOverview.statusLine(jobs: .pending, pipeline: .ready(going), now: now, timeText: clock).hasSuffix("Reading Acme · 2 of 5"))
    }

    func testHugeCountsAreGroupedAndNegativesClamp() {
        XCTAssertEqual(HomeOverview.count(1_234_567_890, locale: en), "1,234,567,890")
        XCTAssertEqual(HomeOverview.count(-5, locale: en), "0")
        XCTAssertEqual(HomeOverview.count(Int.max, locale: en), "9,223,372,036,854,775,807")
        XCTAssertEqual(JobCounts(new: -3, saved: -1, applied: -2).new, 0)
        let line = HomeOverview.statusLine(jobs: .ready(JobCounts(new: 1_000_000, saved: 0, applied: 0)), pipeline: .pending, now: now, timeText: clock)
        XCTAssertTrue(line.contains(HomeOverview.count(1_000_000)), line)
    }

    func testJobCountsFromPageTreatsMissingViewsAsZeroAndKeepsTorn() {
        let page = JobsPage(view: "new", jobs: [], total: 0, shown: 0, counts: ["new": 4, "saved": 2], torn: true)
        XCTAssertEqual(JobCounts(page: page), JobCounts(new: 4, saved: 2, applied: 0, torn: true))
    }

    func testGreetingByHour() {
        XCTAssertEqual(HomeOverview.greeting(hour: 8), "Good morning")
        XCTAssertEqual(HomeOverview.greeting(hour: 12), "Good afternoon")
        XCTAssertEqual(HomeOverview.greeting(hour: 19), "Good evening")
        XCTAssertEqual(HomeOverview.greeting(hour: 3), "Welcome back")
        XCTAssertEqual(HomeOverview.greeting(hour: -1), "Welcome back")
        XCTAssertEqual(HomeOverview.greeting(hour: 99), "Welcome back")
    }

    // MARK: top roles

    func testTopRolesRanksByPriorityThenRelevanceAndKeepsTies() {
        let jobs = [job("a", priority: 40), job("b", relevance: 90), job("c", priority: 40), job("d"), job("e", priority: 95, relevance: 1)]
        XCTAssertEqual(HomeOverview.topRoles(jobs, limit: 10).map(\.key), ["e", "b", "a", "c", "d"])
    }

    func testTopRolesSkipsMarkedClosedAndDuplicates() {
        let jobs = [job("a", priority: 10, mark: .hidden), job("b", priority: 20, closed: true), job("c", priority: 5),
                    job("c", priority: 99), job("d", priority: 7)]
        let top = HomeOverview.topRoles(jobs, limit: 10)
        XCTAssertEqual(top.map(\.key), ["d", "c"])
        XCTAssertEqual(top.first { $0.key == "c" }?.priority, 5, "the first copy of a duplicate wins")
    }

    func testTopRolesEmptyAllHiddenAndBadLimits() {
        XCTAssertEqual(HomeOverview.topRoles([], limit: 3), [])
        XCTAssertEqual(HomeOverview.topRoles([job("a", mark: .hidden), job("b", closed: true)], limit: 3), [])
        XCTAssertEqual(HomeOverview.topRoles([job("a")], limit: 0), [])
        XCTAssertEqual(HomeOverview.topRoles([job("a")], limit: -4), [])
        XCTAssertEqual(HomeOverview.topRoles([job("a"), job("b")], limit: 1).map(\.key), ["a"])
        XCTAssertEqual(HomeOverview.topRoles([job("a")], limit: Int.max).count, 1)
    }

    func testTopRolesTreatsNonFiniteScoresAsMissing() {
        let jobs = [job("nan", priority: .nan), job("inf", priority: .infinity, relevance: 30), job("real", priority: 50)]
        XCTAssertEqual(HomeOverview.topRoles(jobs, limit: 3).map(\.key), ["real", "inf", "nan"])
    }

    func testTopRolesOfAHugeListIsBounded() {
        let jobs = (0..<20_000).map { job("k\($0)", priority: Double($0 % 100)) }
        let top = HomeOverview.topRoles(jobs, limit: 5)
        XCTAssertEqual(top.count, 5)
        XCTAssertTrue(top.allSatisfy { $0.priority == 99 })
    }

    func testFitNoteJoinsLabelAndReasonAndBoundsUntrustedText() {
        var j = job("a")
        XCTAssertNil(HomeOverview.fitNote(j))
        j.triage = ["role_fit": "strong_fit"]
        XCTAssertEqual(HomeOverview.fitNote(j), "Strong fit")
        j.reasonList = [.init(kind: .gap, text: "no Rust"), .init(kind: .fits, text: "Platform\nwork " + String(repeating: "x", count: 500))]
        let note = try! XCTUnwrap(HomeOverview.fitNote(j, maxLength: 40))
        XCTAssertTrue(note.hasPrefix("Strong fit · Platform work x"), note)
        XCTAssertFalse(note.contains("\n"))
        XCTAssertLessThanOrEqual(note.count, "Strong fit · ".count + 40)
        XCTAssertTrue(note.hasSuffix("…"))
    }

    // MARK: setup checklist

    private func items(_ i: HomeOverview.SetupInputs) -> [String: HomeOverview.SetupItem.State] {
        Dictionary(uniqueKeysWithValues: HomeOverview.setupItems(i).map { ($0.id, $0.state) })
    }

    func testSetupEverythingDone() throws {
        let st = items(.init(storeError: nil, answerCount: 12, pipeline: .ready(try status()), browsers: .connected, agentEnabled: true))
        XCTAssertEqual(Set(st.values), [.done])
        XCTAssertEqual(st.count, 6)
    }

    func testSetupFreshInstall() throws {
        let fresh = try status(ledger: #"{"ok":true,"exists":false,"path":"/s","total":0,"by_status":{}}"#, lastRun: #"{"ok":true,"state":"none"}"#)
        let st = items(.init(storeError: nil, answerCount: 0, pipeline: .ready(fresh), browsers: .notConnected, agentEnabled: false))
        XCTAssertEqual(st, ["bank": .todo, "config": .done, "ledger": .todo, "run": .todo, "browsers": .todo, "agent": .todo])
    }

    func testSetupAnUnreadableSourceIsNeverDone() throws {
        for pipeline in [Reading<PipelineStatus>.pending, .failed("no vani")] {
            let st = items(.init(storeError: nil, answerCount: 3, pipeline: pipeline, browsers: .unknown, agentEnabled: true))
            XCTAssertEqual(st["config"], .unknown)
            XCTAssertEqual(st["ledger"], .unknown)
            XCTAssertEqual(st["run"], .unknown)
            XCTAssertEqual(st["browsers"], .unknown)
        }
    }

    func testSetupStoreErrorIsAProblemNotEmpty() throws {
        let st = items(.init(storeError: "corrupt bank", answerCount: 0, pipeline: .pending, browsers: .connected, agentEnabled: true))
        XCTAssertEqual(st["bank"], .problem)
    }

    func testSetupBadConfigLedgerAndRun() throws {
        let bad = try status(config: #"{"ok":false,"reason":"no such file"}"#,
                             ledger: #"{"ok":false,"reason":"corrupt","exists":true,"path":"/s","total":0,"by_status":{}}"#,
                             lastRun: #"{"ok":true,"state":"failed","stage":"digest"}"#)
        let st = items(.init(storeError: nil, answerCount: 1, pipeline: .ready(bad), browsers: .connected, agentEnabled: true))
        XCTAssertEqual(st["config"], .problem)
        XCTAssertEqual(st["ledger"], .problem)
        XCTAssertEqual(st["run"], .problem)
        XCTAssertEqual(HomeOverview.setupItems(.init(storeError: nil, answerCount: 1, pipeline: .ready(bad), browsers: .connected, agentEnabled: true))
            .first { $0.id == "config" }?.target, .settings)
    }

    func testSetupHugeAnswerCountAndSingular() throws {
        let one = HomeOverview.setupItems(.init(storeError: nil, answerCount: 1, pipeline: .pending, browsers: .connected, agentEnabled: true)).first!
        XCTAssertEqual(one.detail, "1 answer saved")
        let many = HomeOverview.setupItems(.init(storeError: nil, answerCount: Int.max, pipeline: .pending, browsers: .connected, agentEnabled: true)).first!
        XCTAssertEqual(many.state, .done)
    }

    func testBrowserLink() {
        XCTAssertEqual(HomeOverview.browserLink(chrome: [.connected], safariEnabled: nil), .connected)
        XCTAssertEqual(HomeOverview.browserLink(chrome: [], safariEnabled: true), .connected)
        XCTAssertEqual(HomeOverview.browserLink(chrome: [.notConnected, .browserNotFound], safariEnabled: nil), .unknown)
        XCTAssertEqual(HomeOverview.browserLink(chrome: [.stale(reason: "moved")], safariEnabled: false), .notConnected)
        XCTAssertEqual(HomeOverview.browserLink(chrome: [], safariEnabled: nil), .unknown)
    }

    // MARK: pipeline headline

    func testRunHeadline() throws {
        let ok = try status().lastRun
        XCTAssertEqual(HomeOverview.runHeadline(ok, now: now), "Succeeded 3 h ago · +32 postings")
        let shrunk = PipelineStatus.LastRun(state: .ok, finishedAt: now.addingTimeInterval(-60), postingsBefore: 100, postingsAfter: 90)
        XCTAssertEqual(HomeOverview.runHeadline(shrunk, now: now), "Succeeded under 2 min ago · -10 postings")
        XCTAssertEqual(HomeOverview.runHeadline(.init(state: .failed, finishedAt: now, stage: "digest"), now: now), "Failed under 2 min ago (digest)")
        XCTAssertEqual(HomeOverview.runHeadline(.init(state: .never), now: now), "No run recorded yet")
        XCTAssertEqual(HomeOverview.runHeadline(.init(state: .running), now: now), "Running now")
        let reading = PipelineStatus.LastRun(state: .running, progress: .init(
            ok: true, stage: "listing", phase: "listing", index: 2, total: 5, ats: "gh", token: "acme", company: "Acme"))
        XCTAssertEqual(HomeOverview.runHeadline(reading, now: now), "Reading Acme · 2 of 5")
        XCTAssertEqual(HomeOverview.runHeadline(.init(state: .ok), now: now), "Succeeded", "no timestamp: no invented age")
        XCTAssertTrue(HomeOverview.runHeadline(.init(ok: false, reason: "torn\nfile", state: .unknown), now: now).hasPrefix("Last run unreadable: torn file"))
        let future = PipelineStatus.LastRun(state: .ok, finishedAt: now.addingTimeInterval(9999))
        XCTAssertEqual(HomeOverview.runHeadline(future, now: now), "Succeeded under 2 min ago", "clock skew must not print a negative age")
    }

    // MARK: activity

    func testRecentActivityRowsAndTimestamps() {
        let entries = [
            ActivityEntry(at: "2026-10-14T10:00:00.123Z", source: .chrome, event: .fill, host: "jobs.lever.co", counts: ["filled": 7, "skipped": 1]),
            ActivityEntry(at: "2026-10-14T09:00:00Z", source: .app, event: .edit),
            ActivityEntry(at: "yesterday-ish", source: .safari, event: .capture, host: "a\nb"),
            ActivityEntry(at: "", source: .app, event: .resume),
        ]
        let rows = HomeOverview.recentActivity(entries, limit: 3)
        XCTAssertEqual(rows.count, 3)
        XCTAssertEqual(rows[0].title, "Filled a form on jobs.lever.co")
        XCTAssertEqual(rows[0].detail, "Chrome · filled 7 · skipped 1")
        XCTAssertEqual(rows[0].symbol, "globe")
        XCTAssertNotNil(rows[0].at, "fractional seconds parse")
        XCTAssertNotNil(rows[1].at, "plain seconds parse")
        XCTAssertNil(rows[2].at, "garbage is no time, not a guessed one")
        XCTAssertEqual(rows[2].title, "Saved answers on a b")
        XCTAssertEqual(rows.map(\.id), [0, 1, 2])
    }

    func testRecentActivityEmptyAndBadLimit() {
        XCTAssertEqual(HomeOverview.recentActivity([], limit: 5), [])
        XCTAssertEqual(HomeOverview.recentActivity([ActivityEntry(source: .app, event: .edit)], limit: 0), [])
        XCTAssertEqual(HomeOverview.recentActivity([ActivityEntry(source: .app, event: .edit)], limit: -1), [])
        XCTAssertEqual(HomeOverview.recentActivity([ActivityEntry(source: .app, event: .edit)], limit: Int.max).count, 1)
        XCTAssertEqual(HomeOverview.recentActivity([ActivityEntry(source: .app, event: .import, counts: ["answers": 2_000_000_000])], limit: 1).first?.detail.contains("answers 2"), true)
    }

    /// The compact Home row names only counts that happened; zero counters are noise there
    /// (the Activity pane keeps the full record).
    func testRecentActivityDropsZeroCounts() {
        let capture = ActivityEntry(source: .safari, event: .capture, host: "jobs.example.org",
                                    counts: ["added": 4, "skippedEmpty": 0, "skippedSecret": 0])
        XCTAssertEqual(HomeOverview.recentActivity([capture], limit: 1).first?.detail, "Safari · added 4")
        let allZero = ActivityEntry(source: .chrome, event: .fill, counts: ["filled": 0, "skipped": 0])
        XCTAssertEqual(HomeOverview.recentActivity([allZero], limit: 1).first?.detail, "Chrome")
        let negative = ActivityEntry(source: .app, event: .edit, counts: ["changed": -3])
        XCTAssertEqual(HomeOverview.recentActivity([negative], limit: 1).first?.detail, "App")
    }
}
