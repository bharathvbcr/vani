import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// AuditModel in this process against a fake `vani`. It drives the model
/// directly, so it needs no accessibility trust and always runs.
@MainActor
final class AuditModelTests: XCTestCase {
    private struct Fake {
        var vani: URL
        var log: URL
        var calls: [String] { ((try? String(contentsOf: log, encoding: .utf8)) ?? "").split(separator: "\n").map(String.init) }
    }

    /// `answerStatus` 1 makes every answer fail with vani's {"error"} reply;
    /// `drawStatus` 1 makes the draw fail.
    private func fake(answerStatus: Int = 0, drawStatus: Int = 0) throws -> Fake {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("vani-audit-model-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        let log = dir.appendingPathComponent("calls.log"), url = dir.appendingPathComponent("vani")
        let answerReply = answerStatus == 0
            ? #"{"key":"k","verdict":"yes","stratum":"decile:1","p_select":0.5}"# : #"{"error":"refused"}"#
        let drawReply = drawStatus == 0
            ? #"{"seed":"2026-W43","blind":true,"picks":[{"key":"gh:a:1","stratum":"decile:1","p_select":0.5},{"key":"gh:a:2","stratum":"decile:2","p_select":0.25}]}"#
            : #"{"error":"labels unreadable"}"#
        let script = """
        #!/bin/sh
        printf '%s\\n' "$*" >> '\(log.path)'
        case "$*" in
        "audit answer"*) echo '\(answerReply)'; exit \(answerStatus) ;;
        "audit --json"*) echo '\(drawReply)'; exit \(drawStatus) ;;
        "jobs show"*) k="${4}"; echo "{\\"key\\":\\"$k\\",\\"company\\":\\"Co\\",\\"title\\":\\"Title $k\\",\\"status\\":\\"live\\"}"; exit 0 ;;
        esac
        exit 2
        """
        try Data(script.utf8).write(to: url)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
        return Fake(vani: url, log: log)
    }

    private func model(_ f: Fake) -> AuditModel {
        AuditModel { AuditClient(jobs: JobsClient(vaniBinary: f.vani, timeout: 10)) }
    }

    func testStartDrawsBlindAndShowsTheFirstCard() async throws {
        let f = try fake()
        let m = model(f)
        await m.start()
        XCTAssertNil(m.loadError)
        XCTAssertEqual(m.seed, "2026-W43")
        XCTAssertEqual(m.session?.picks.count, 2)
        XCTAssertEqual(m.card?.title, "Title gh:a:1")
        XCTAssertEqual(f.calls, ["audit --json --blind", "jobs show --json gh:a:1"])
    }

    func testAnAnswerIsWrittenThenTheNextCardShows() async throws {
        let f = try fake()
        let m = model(f)
        await m.start()
        await m.answer(.maybe)
        XCTAssertEqual(m.session?.answered, 1)
        XCTAssertEqual(m.card?.title, "Title gh:a:2")
        XCTAssertTrue(f.calls.contains("audit answer --json --key=gh:a:1 --verdict=maybe --source=decile:1 --p-select=0.5"), "\(f.calls)")
        await m.answer(.skip)
        XCTAssertTrue(m.session?.isDone == true)
        XCTAssertNil(m.card)
        XCTAssertEqual(m.session?.count(.skip), 1)
    }

    func testAFailedWriteLeavesTheCardAndCountsNothing() async throws {
        let m = model(try fake(answerStatus: 1))
        await m.start()
        await m.answer(.yes)
        XCTAssertEqual(m.session?.answered, 0)
        XCTAssertEqual(m.session?.current?.key, "gh:a:1")
        XCTAssertEqual(m.card?.key, "gh:a:1")
        XCTAssertTrue(m.answerError?.contains("refused") == true, "\(String(describing: m.answerError))")
        XCTAssertFalse(m.saving)
    }

    func testADrawThatFailsIsShownAsAnErrorNotAnEmptyAudit() async throws {
        let m = model(try fake(drawStatus: 1))
        await m.start()
        XCTAssertTrue(m.loadError?.contains("labels unreadable") == true, "\(String(describing: m.loadError))")
        XCTAssertNil(m.session)
        await m.answer(.yes) // nothing to answer: ignored
        XCTAssertNil(m.answerError)
    }

    func testAnAnswerBeforeAnyCardIsIgnored() async throws {
        let f = try fake()
        let m = model(f)
        await m.answer(.yes)
        XCTAssertTrue(f.calls.isEmpty, "\(f.calls)")
    }
}
