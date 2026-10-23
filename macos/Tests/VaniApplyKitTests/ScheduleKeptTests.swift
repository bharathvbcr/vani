import XCTest
@testable import VaniApplyKit

/// The additive `schedule.kept` / `schedule.unkept` fields of `vani status --json`
/// (cmd/vani/schedulekept.go): an older vani omits them, a newer one adds them,
/// and the status version stays 1, so both must decode.
final class ScheduleKeptTests: XCTestCase {
    private func status(schedule: String) throws -> PipelineStatus {
        let json = """
        {"version":1,"generated_at":"2026-10-22T18:00:00Z","state_dir":"/s",\
        "config":{"ok":true,"boards":3},\
        "ledger":{"ok":true,"exists":true,"path":"/s/ledger","total":2,"by_status":{"live":2}},\
        "last_run":{"ok":true,"state":"none"},\
        "schedule":\(schedule),\
        "backends":{"ok":true,"day":"2026-10-22","items":[]},\
        "decide":{"ok":true,"local_configured":false,"lappi_configured":false}}
        """
        return try JobsClient.decoder.decode(PipelineStatus.self, from: Data(json.utf8))
    }

    func testAnOlderVaniWithoutTheFieldsDecodesAndIsNeitherKeptNorUnkept() throws {
        let s = try status(schedule: #"{"ok":true,"daily_at":"08:30","timezone":"America/Los_Angeles","next_run":"2026-10-23T15:30:00Z"}"#)
        XCTAssertNil(s.schedule.kept)
        XCTAssertNil(s.schedule.unkept)
        XCTAssertFalse(s.schedule.isUnkept)
    }

    func testAKeptScheduleCarriesNoDetails() throws {
        let s = try status(schedule: #"{"ok":true,"daily_at":"08:30","timezone":"UTC","kept":true}"#)
        XCTAssertEqual(s.schedule.kept, true)
        XCTAssertNil(s.schedule.unkept)
        XCTAssertFalse(s.schedule.isUnkept)
    }

    func testDecodesWhatGoPrintsForAnUnkeptSchedule() throws {
        let s = try status(schedule: """
        {"ok":true,"daily_at":"08:30","timezone":"America/Los_Angeles","next_run":"2026-10-23T15:30:00Z","kept":false,\
        "unkept":{"why":"no daemon has announced itself","last_run_at":"2026-10-21T12:05:00.123456Z","due_at":"2026-10-22T15:30:00Z","overdue":true,\
        "awaiting_triage":8525,"advance_per_run":750,"runs_needed":12,"hint":"a daemon keeps the schedule"}}
        """)
        XCTAssertTrue(s.schedule.isUnkept)
        let u = try XCTUnwrap(s.schedule.unkept)
        XCTAssertEqual(u.why, "no daemon has announced itself")
        XCTAssertNotNil(u.lastRunAt)
        XCTAssertNotNil(u.dueAt)
        XCTAssertEqual(u.overdue, true)
        XCTAssertEqual(u.awaitingTriage, 8525)
        XCTAssertEqual(u.advancePerRun, 750)
        XCTAssertEqual(u.runsNeeded, 12)
        XCTAssertEqual(u.hint, "a daemon keeps the schedule")
    }

    /// The app points at its own switch, never at vani's terminal route: a hand-installed
    /// launchd agent with the key written into its plist is the wrong advice in the app.
    func testTheAppHintNamesTheSwitchNotTheTerminalRoute() throws {
        let terminal = "turn on Run on schedule ... Without the app, `vani launchd plist` prints an agent ... EnvironmentVariables"
        let s = try status(schedule: #"{"ok":true,"daily_at":"08:30","timezone":"UTC","kept":false,"unkept":{"awaiting_triage":3,"hint":"\#(terminal)"}}"#)
        let hint = PipelineStatus.Schedule.Unkept.appHint
        XCTAssertTrue(hint.contains("Run on schedule"), hint)
        for word in ["launchd", "plist", "EnvironmentVariables", "vani "] {
            XCTAssertFalse(hint.contains(word), "the app hint names \(word): \(hint)")
        }
        let summary = try XCTUnwrap(s.schedule.unkept).summary(dailyAt: "08:30", now: Date())
        XCTAssertTrue(summary.contains("Turn on Run on schedule"), summary)
        XCTAssertFalse(summary.contains("launchd"), summary)
    }

    /// Absent means unknown, not zero: a ledger vani could not count is not an empty one.
    func testAbsentDetailsStayNil() throws {
        let s = try status(schedule: #"{"ok":true,"daily_at":"08:30","timezone":"UTC","kept":false,"unkept":{"hint":"h"}}"#)
        let u = try XCTUnwrap(s.schedule.unkept)
        XCTAssertNil(u.awaitingTriage)
        XCTAssertNil(u.overdue)
        XCTAssertNil(u.lastRunAt)
    }

    /// A detail that does not decode never fails the whole status read.
    func testAMalformedUnkeptFieldDoesNotFailTheStatus() throws {
        let s = try status(schedule: #"{"ok":true,"daily_at":"08:30","timezone":"UTC","kept":false,"unkept":{"awaiting_triage":"many","overdue":true,"hint":"h"}}"#)
        let u = try XCTUnwrap(s.schedule.unkept)
        XCTAssertNil(u.awaitingTriage)
        XCTAssertEqual(u.overdue, true)
        let notAnObject = try status(schedule: #"{"ok":true,"daily_at":"08:30","timezone":"UTC","kept":false,"unkept":"nope"}"#)
        XCTAssertEqual(notAnObject.schedule.unkept, PipelineStatus.Schedule.Unkept())
    }

    /// A schedule vani could not read has nothing to keep: not unkept.
    func testAnUnreadableScheduleIsNotUnkept() throws {
        let s = try status(schedule: #"{"ok":false,"reason":"the config could not be read","kept":false}"#)
        XCTAssertFalse(s.schedule.isUnkept)
    }
}
