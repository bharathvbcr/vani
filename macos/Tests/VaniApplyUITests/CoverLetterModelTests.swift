import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// The cover letter flow in this process against a fake `vani jobs cover`. No real vani, no
/// Keychain, no owner state.
@MainActor
final class CoverLetterModelTests: XCTestCase {
    private struct Rig {
        let root: URL
        let bank: URL
        let state: URL
        let vani: URL
    }

    private func rig(body: String = "Dear team, a short synthetic letter.") throws -> Rig {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("vani-apply-cover-\(UUID().uuidString)")
        let bank = root.appendingPathComponent("bank"), state = root.appendingPathComponent("state")
        let letters = state.appendingPathComponent("letters")
        try FileManager.default.createDirectory(at: bank, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: letters, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: root) }
        let text = letters.appendingPathComponent("v1.txt")
        try Data(body.utf8).write(to: text)
        let vani = root.appendingPathComponent("vani")
        let script = """
        #!/bin/sh
        case " $* " in
        *" build "*) echo '{"version":1,"pdf":false}' ;;
        *" use "*) echo '{"version":1,"pdf":false,"text_path":"\(text.path)"}' ;;
        esac
        """
        try Data(script.utf8).write(to: vani)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: vani.path)
        return Rig(root: root, bank: bank, state: state, vani: vani)
    }

    private func model(_ r: Rig, store: BankStore?) -> CoverLetterModel {
        let runner = DraftRunner(vaniBinary: r.vani, stateDir: r.state.path, key: { nil }, decisionKey: { nil })
        return CoverLetterModel(client: { CoverLetterClient(runner: runner) }, bankStore: { store })
    }

    private func eventually(_ what: String, _ done: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(8)
        while !done() {
            if Date() > deadline { return XCTFail("timed out waiting for \(what)") }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    private let job = Job(key: "fx:alpha:1", company: "Acme", title: "Engineer")

    func testUseBindsTheLetterAndLogsIt() async throws {
        let r = try rig()
        let store = BankStore(directory: r.bank, lockWait: 5)
        let m = model(r, store: store)
        m.use(job)
        XCTAssertTrue(m.busy)
        XCTAssertEqual(m.status, "Using the letter…")
        try await eventually("use") { !m.busy }
        XCTAssertEqual(m.status, "v1 is the letter the browser may attach.")
        XCTAssertFalse(try store.recentActivity().isEmpty)
    }

    func testBuildReportsTheSavedVersion() async throws {
        let r = try rig()
        let m = model(r, store: nil)
        m.build(job)
        XCTAssertTrue(m.busy)
        try await eventually("build") { !m.busy }
        XCTAssertEqual(m.status, "Saved the text of v1. The PDF could not be drawn.")
    }

    func testAnEmptyLetterAndNoStoreAreReportedNotBound() async throws {
        let empty = try rig(body: "")
        let m = model(empty, store: BankStore(directory: empty.bank))
        m.use(job)
        try await eventually("empty") { !m.busy }
        XCTAssertEqual(m.status, "The used letter could not be read.")

        let r = try rig()
        let n = model(r, store: nil)
        n.use(job)
        try await eventually("no store") { !n.busy }
        XCTAssertEqual(n.status, "The letter was marked used, but this app cannot hand it to the browser.")
    }

    func testASecondActionWhileBusyIsRefused() async throws {
        let r = try rig()
        let m = model(r, store: BankStore(directory: r.bank))
        m.use(job)
        m.build(job)
        XCTAssertEqual(m.status, "Using the letter…")
        try await eventually("use") { !m.busy }
    }

    /// Use logs to the bank, which takes its exclusive lock. Held by someone else, the wait is off
    /// the main actor: it keeps running, the model stays busy, and the use finishes on release.
    func testALockedBankDoesNotBlockTheMainActorDuringUse() async throws {
        let r = try rig()
        let store = BankStore(directory: r.bank, lockWait: 5)
        _ = try store.update { $0.settings.captureSelfId = true }
        let m = model(r, store: store)

        let fd = open(r.bank.appendingPathComponent("bank.lock").path, O_RDWR | O_CREAT, 0o600)
        XCTAssertGreaterThanOrEqual(fd, 0)
        XCTAssertEqual(flock(fd, LOCK_EX), 0)
        defer { flock(fd, LOCK_UN); close(fd) }

        m.use(job)
        var last = Date(), worst: TimeInterval = 0
        let until = Date().addingTimeInterval(0.8)
        while Date() < until {
            try await Task.sleep(nanoseconds: 10_000_000)
            worst = max(worst, Date().timeIntervalSince(last))
            last = Date()
        }
        XCTAssertTrue(m.busy, "the use finished while the bank lock was still held")
        XCTAssertEqual(m.status, "Using the letter…")
        XCTAssertLessThan(worst, 0.25, "the main actor stalled for \(worst) s while the log waited for the bank lock")
        flock(fd, LOCK_UN)
        try await eventually("use after release") { !m.busy }
        XCTAssertEqual(m.status, "v1 is the letter the browser may attach.")
        XCTAssertFalse(try store.recentActivity().isEmpty)
    }
}
