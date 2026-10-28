import Foundation
import XCTest
@testable import VaniApplyKit

/// A stand-in for `vani jobs` that replies with `reply` and records its arguments in `args.txt` beside it.
private func recordingVani(_ t: XCTestCase, reply: String) throws -> (binary: URL, args: URL) {
    let dir = try tempDir(t)
    let url = dir.appendingPathComponent("vani")
    let args = dir.appendingPathComponent("args.txt")
    let script = """
    #!/bin/sh
    printf '%s\\n' "$*" >> '\(args.path)'
    cat <<'EOF'
    \(reply)
    EOF
    """
    try Data(script.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return (url, args)
}

private func jobJSON(_ extra: String) -> String {
    #"{"key":"gh:acme:1","company":"Acme","title":"Inference Engineer","status":"digested","mark":"applied","marked_at":"2026-10-01T09:00:00Z"\#(extra)}"#
}

private func decode(_ json: String) throws -> Job {
    let d = JSONDecoder()
    d.dateDecodingStrategy = .iso8601
    return try d.decode(Job.self, from: Data(json.utf8))
}

/// Post-apply stages (internal/marks stages.go) as the app reads them. The mark stays "applied" at every
/// stage, so a stage is an extra field beside it: an older app ignores it, and this one must read both an
/// older vani's reply (no stage) and a newer vani's stage it does not know.
final class JobStagesTests: XCTestCase {
    func testDecodesEveryStageBesideTheAppliedMark() throws {
        for stage in JobStage.allCases {
            let j = try decode(jobJSON(#","stage":"\#(stage.rawValue)","stage_at":"2026-10-05T10:00:00Z","furthest_stage":"interview""#))
            XCTAssertEqual(j.mark, .applied, "the mark stays applied at \(stage)")
            XCTAssertEqual(j.stage, stage)
            XCTAssertEqual(j.furthestStage, .interview)
            XCTAssertNotNil(j.stageAt)
            XCTAssertNotEqual(j.markedAt, j.stageAt, "the date first applied is not the stage's date")
        }
    }

    func testAnOlderVaniSendsNoStage() throws {
        let j = try decode(jobJSON(""))
        XCTAssertEqual(j.mark, .applied)
        XCTAssertNil(j.stage)
        XCTAssertNil(j.furthestStage)
        XCTAssertNil(j.stageAt)
    }

    /// A stage this app does not know (a later vani added one), or one of the wrong type, reads as no stage:
    /// it never fails the job, and so never the whole list.
    func testAnUnknownOrMalformedStageDoesNotFailTheJob() throws {
        for bad in [#""ghosted""#, "42", "null", "[]", "{}", #""""#] {
            let j = try decode(jobJSON(#","stage":\#(bad),"furthest_stage":\#(bad)"#))
            XCTAssertEqual(j.mark, .applied, "\(bad)")
            XCTAssertNil(j.stage, "\(bad)")
            XCTAssertNil(j.furthestStage, "\(bad)")
        }
    }

    /// Why vani keeps the mark "applied" and sends the stage beside it: a mark value this app does not know
    /// fails the job, and with it the whole list. (Characterizes JobMark, which is unchanged.)
    func testAnUnknownMarkWouldFailTheWholeList() {
        let list = #"{"view":"applied","jobs":[\#(jobJSON("").replacingOccurrences(of: #""mark":"applied""#, with: #""mark":"interview""#))],"total":1,"shown":1}"#
        XCTAssertThrowsError(try JSONDecoder().decode(JobsPage.self, from: Data(list.utf8)))
        XCTAssertEqual(JobMark.allCases, [.saved, .applied, .hidden])
    }

    func testStagesAreTheOnesVaniAccepts() {
        XCTAssertEqual(JobStage.allCases.map(\.rawValue), ["screening", "interview", "offer", "rejected", "withdrawn"])
        XCTAssertEqual(JobStage.allCases.filter(\.isTerminal), [.rejected, .withdrawn])
        XCTAssertEqual(JobStage.interview.title, "Interview")
        for s in JobStage.allCases { XCTAssertNil(JobMark(rawValue: s.rawValue), "\(s) is a stage, never a mark") }
    }

    /// One command sets the marks and the stages: `jobs mark KEY WORD`.
    func testTheClientSetsAStageWithTheMarkCommand() throws {
        let (binary, args) = try recordingVani(self, reply: jobJSON(#","stage":"interview","furthest_stage":"interview""#))
        let c = JobsClient(vaniBinary: binary)
        let j = try c.stage("gh:acme:1", .interview)
        XCTAssertEqual(j.stage, .interview)
        XCTAssertEqual(j.mark, .applied)
        _ = try c.stage("gh:acme:1", nil)
        _ = try c.mark("gh:acme:1", .applied)
        let lines = try String(contentsOf: args, encoding: .utf8).split(separator: "\n").map(String.init)
        XCTAssertEqual(lines, ["jobs mark --json gh:acme:1 interview", "jobs mark --json gh:acme:1 unstage", "jobs mark --json gh:acme:1 applied"])
    }

    /// An older vani refuses a stage word it does not know; the error reaches the caller, not a silent success.
    func testARefusedStageIsAnError() throws {
        let dir = try tempDir(self)
        let url = dir.appendingPathComponent("vani")
        try Data("#!/bin/sh\necho '{\"error\":\"jobs mark: \\\"interview\\\" is not saved, applied, hidden or clear\"}'\nexit 2\n".utf8).write(to: url)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
        XCTAssertThrowsError(try JobsClient(vaniBinary: url).stage("gh:acme:1", .interview))
    }
}
