import XCTest
@testable import VaniApplyKit

/// Keys `vani status --json` added without a version bump (cmd/vani/statuscmd.go,
/// summarycheck.go, boardhealthstatus.go, pidalive.go): `target_summary`,
/// `knowledge_base`, `board_health` with its `lists_nothing*` keys, and the
/// daemon's `binary` and `exe`. This app reads none of them yet, so what is
/// pinned is that a status carrying them still decodes, whole, and that the
/// sections the app does read are untouched by them.
final class StatusAdditiveKeysTests: XCTestCase {
    private func status(daemon: String, extra: String) throws -> PipelineStatus {
        let json = """
        {"version":1,"generated_at":"2026-11-12T18:00:00Z","state_dir":"/s",\
        "config":{"ok":true,"boards":3},\
        "ledger":{"ok":true,"exists":true,"path":"/s/ledger","total":2,"by_status":{"live":2}},\
        "last_run":{"ok":true,"state":"none"},\
        "schedule":{"ok":true,"daily_at":"08:30","timezone":"UTC","kept":true},\
        "backends":{"ok":true,"day":"2026-11-12","items":[]},\
        "decide":{"ok":true,"local_configured":false,"lappi_configured":false},\
        "daemon":\(daemon)\(extra)}
        """
        return try JobsClient.decoder.decode(PipelineStatus.self, from: Data(json.utf8))
    }

    private let additive = """
    ,"target_summary":{"ok":true,"reason":"target_summary is empty: ranking uses the generic default target","generic_default":true},\
    "knowledge_base":{"ok":true,"modified":"2026-10-01T09:00:00Z","age_days":42,"stale_after_days":30,"stale":true},\
    "board_health":{"ok":true,"tracked":true,"updated":"2026-11-12T12:00:00Z",\
    "stale":[{"ats":"gh","token":"acme","last_ok":"2026-10-20T00:00:00Z","since":"2026-10-20T00:00:00Z","fails":4,"outcome":"timeout","error":"timed out"}],\
    "stale_count":1,"untracked":["lv:beta"],"untracked_count":1,\
    "empty":[{"ats":"ab","token":"gamma","closed":3,"held":1}],"empty_count":1,\
    "lists_nothing":[{"ats":"gh","token":"delta","reads":5,"since":"2026-11-01T00:00:00Z"}],\
    "lists_nothing_count":1,"lists_nothing_note":"the ledger could not be read"}
    """

    func testAStatusWithEveryAdditiveKeyDecodesAndKeepsWhatTheAppReads() throws {
        let s = try status(daemon: #"{"ok":true,"reason":"the daemon, pid 4242, is running a different vani binary: /opt/vani/bin/vani","running":true,"binary":"other","exe":"/opt/vani/bin/vani","pid":4242,"since":"2026-11-12T10:00:00Z"}"#,
                           extra: additive)
        XCTAssertEqual(s.version, 1)
        XCTAssertEqual(s.config.boards, 3)
        XCTAssertEqual(s.ledger.total, 2)
        XCTAssertEqual(s.schedule.kept, true)
        let d = try XCTUnwrap(s.daemon)
        XCTAssertTrue(d.ok)
        XCTAssertTrue(d.running, "a live daemon of another vani binary is running")
        XCTAssertEqual(d.pid, 4242)
        XCTAssertNotNil(d.since)
    }

    func testTheDaemonOfThisBinaryAndAnOlderVaniWithoutTheKeysDecodeToo() throws {
        let this = try status(daemon: #"{"ok":true,"running":true,"binary":"this","pid":7,"since":"2026-11-12T10:00:00Z"}"#, extra: "")
        XCTAssertEqual(this.daemon?.running, true)
        let old = try status(daemon: #"{"ok":true,"running":true,"pid":7,"since":"2026-11-12T10:00:00Z"}"#, extra: "")
        XCTAssertEqual(old.daemon?.running, true)
        let gone = try status(daemon: #"{"ok":true,"reason":"the daemon recorded as pid 7 is gone (it did not exit cleanly)","running":false,"pid":7}"#, extra: additive)
        XCTAssertEqual(gone.daemon?.running, false)
        XCTAssertEqual(gone.daemon?.reason, "the daemon recorded as pid 7 is gone (it did not exit cleanly)")
    }

    func testABoardHealthSectionThatIsNotWhatThisAppExpectsDoesNotFailTheStatus() throws {
        // An unknown section may change shape: it is not this app's to reject.
        let s = try status(daemon: #"{"ok":true,"running":false}"#,
                           extra: #","board_health":"unreadable","target_summary":[],"knowledge_base":null"#)
        XCTAssertEqual(s.config.boards, 3)
    }
}
