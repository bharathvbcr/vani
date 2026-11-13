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
        "impressions log"*) cat >/dev/null; echo '{"logged":1}'; exit 0 ;;
        "jobs show"*) k="${4}"; echo "{\\"key\\":\\"$k\\",\\"company\\":\\"Co\\",\\"title\\":\\"Title $k\\",\\"status\\":\\"live\\"}"; exit 0 ;;
        esac
        exit 2
        """
        try Data(script.utf8).write(to: url)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
        return Fake(vani: url, log: log)
    }

    /// A defaults suite of its own, so no test reads a quota another stored.
    private func freshDefaults() -> UserDefaults {
        let name = "vani-audit-model-\(UUID().uuidString)"
        addTeardownBlock { UserDefaults.standard.removePersistentDomain(forName: name) }
        return UserDefaults(suiteName: name)!
    }

    private func model(_ f: Fake, defaults: UserDefaults? = nil) -> AuditModel {
        AuditModel(client: { AuditClient(jobs: JobsClient(vaniBinary: f.vani, timeout: 10)) }, defaults: defaults ?? freshDefaults())
    }

    func testTheDisagreementQuotaReachesTheDrawAndIsKept() async throws {
        let f = try fake()
        let d = freshDefaults()
        let m = model(f, defaults: d)
        XCTAssertEqual(m.disagree, 0, "off until the owner sets it")
        m.disagree = 4
        await m.start()
        XCTAssertEqual(f.calls.first, "audit --json --blind --disagree=4")
        XCTAssertEqual(model(f, defaults: d).disagree, 4, "the quota is kept across launches")
        d.set(999, forKey: AuditModel.disagreeKey)
        XCTAssertEqual(model(f, defaults: d).disagree, AuditClient.maxDisagree, "a stored value out of range is clamped")
    }

    func testTheOverlayQuotasReachTheDrawAndAreKept() async throws {
        let f = try fake()
        let d = freshDefaults()
        let m = model(f, defaults: d)
        XCTAssertEqual(m.neighbours, 0, "off until the owner sets it")
        XCTAssertEqual(m.engineering, 0, "off until the owner sets it")
        m.disagree = 10
        XCTAssertEqual(m.maxNeighbours, AuditClient.maxDisagree - 10, "the neighbour picks share the disagreement picks' cap")
        m.neighbours = 5
        m.engineering = 20
        XCTAssertEqual(m.maxDisagree, AuditClient.maxDisagree - 5)
        await m.start()
        XCTAssertEqual(f.calls.first, "audit --json --blind --disagree=10 --yes-neighbours=5 --engineering=20")
        let again = model(f, defaults: d)
        XCTAssertEqual([again.disagree, again.neighbours, again.engineering], [10, 5, 20], "kept across launches")
        d.set(25, forKey: AuditModel.disagreeKey)
        d.set(25, forKey: AuditModel.neighboursKey)
        d.set(999, forKey: AuditModel.engineeringKey)
        let clamped = model(f, defaults: d)
        XCTAssertEqual([clamped.disagree, clamped.neighbours, clamped.engineering],
                       [25, AuditClient.maxDisagree - 25, AuditClient.maxEngineering], "stored values out of range are clamped")
    }

    func testAMaybeWaitsForItsReasonThenWritesIt() async throws {
        let f = try fake()
        let m = model(f)
        await m.start()
        let before = f.calls.count
        await m.choose(.maybe)
        XCTAssertEqual(m.pendingVerdict, .maybe)
        XCTAssertEqual(f.calls.count, before, "nothing is written until the reason is given")
        XCTAssertEqual(m.session?.answered, 0)
        await m.choose(.no) // a change of mind replaces the pending verdict
        XCTAssertEqual(m.pendingVerdict, .no)
        await m.giveReason(.location)
        XCTAssertNil(m.pendingVerdict)
        XCTAssertEqual(m.session?.answered, 1)
        XCTAssertTrue(f.calls.contains("audit answer --json --key=gh:a:1 --verdict=no --source=decile:1 --p-select=0.5 --reason=location"), "\(f.calls)")
        // No reason is a plain answer; cancel leaves the card unanswered.
        await m.choose(.maybe)
        m.cancelReason()
        XCTAssertNil(m.pendingVerdict)
        await m.giveReason(nil) // nothing pending: ignored
        XCTAssertEqual(m.session?.answered, 1)
        await m.choose(.maybe)
        await m.giveReason(nil)
        XCTAssertTrue(f.calls.contains("audit answer --json --key=gh:a:2 --verdict=maybe --source=decile:2 --p-select=0.25"), "\(f.calls)")
        XCTAssertTrue(m.session?.isDone == true)
    }

    func testYesAndCantTellAreWrittenAtOnce() async throws {
        let f = try fake()
        let m = model(f)
        await m.start()
        await m.choose(.yes)
        XCTAssertNil(m.pendingVerdict)
        XCTAssertEqual(m.session?.answered, 1)
        XCTAssertTrue(f.calls.contains("audit answer --json --key=gh:a:1 --verdict=yes --source=decile:1 --p-select=0.5"), "\(f.calls)")
    }

    func testAFailedReasonedWriteKeepsTheVerdictPending() async throws {
        let m = model(try fake(answerStatus: 1))
        await m.start()
        await m.choose(.no)
        await m.giveReason(.domain)
        XCTAssertEqual(m.pendingVerdict, .no, "the owner can try again without re-choosing")
        XCTAssertEqual(m.session?.answered, 0)
        XCTAssertNotNil(m.answerError)
    }

    func testStartDrawsBlindAndShowsTheFirstCard() async throws {
        let f = try fake()
        let m = model(f)
        await m.start()
        XCTAssertNil(m.loadError)
        XCTAssertEqual(m.seed, "2026-W43")
        XCTAssertEqual(m.session?.picks.count, 2)
        XCTAssertEqual(m.card?.title, "Title gh:a:1")
        XCTAssertEqual(f.calls, ["audit --json --blind", "jobs show --json gh:a:1",
                                 "impressions log --json --surface audit --slate 2026-w43 --section decile:1"])
        XCTAssertNil(m.impressionsError)
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
