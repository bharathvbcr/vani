import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// Attack tests for the CoverLetterModel off-main-actor change: ordering of the bank store
/// lookup, hostile letter files, a hung child, lock contention, concurrency and release mid-call.
@MainActor
final class CoverLetterModelAttackTests: XCTestCase {
    private struct Rig {
        let root: URL
        let bank: URL
        let state: URL
        let letters: URL
        let vani: URL
        let calls: URL
    }

    private final class Counter: @unchecked Sendable {
        private let lock = NSLock()
        private var n = 0
        func bump() { lock.lock(); n += 1; lock.unlock() }
        var value: Int { lock.lock(); defer { lock.unlock() }; return n }
    }

    /// useBody / buildBody are shell fragments run for the matching subcommand.
    private func rig(text: Data? = Data("Dear team, a short synthetic letter.".utf8), useBody: String? = nil, buildBody: String = "echo '{\"version\":1,\"pdf\":false}'") throws -> Rig {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("vani-apply-cover-attack-\(UUID().uuidString)")
        let bank = root.appendingPathComponent("bank"), state = root.appendingPathComponent("state")
        let letters = state.appendingPathComponent("letters")
        try FileManager.default.createDirectory(at: bank, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: letters, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: root) }
        let textURL = letters.appendingPathComponent("v1.txt")
        if let text { try text.write(to: textURL) }
        let calls = root.appendingPathComponent("calls.log")
        let use = useBody?.replacingOccurrences(of: "@TEXT@", with: textURL.path) ?? "echo '{\"version\":1,\"pdf\":false,\"text_path\":\"\(textURL.path)\"}'"
        let vani = root.appendingPathComponent("vani")
        let script = """
        #!/bin/sh
        echo "$*" >> "\(calls.path)"
        case " $* " in
        *" build "*) \(buildBody) ;;
        *" use "*) \(use) ;;
        esac
        """
        try Data(script.utf8).write(to: vani)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: vani.path)
        return Rig(root: root, bank: bank, state: state, letters: letters, vani: vani, calls: calls)
    }

    private func model(_ r: Rig, timeout: TimeInterval = 120, bank: @escaping () -> BankStore?) -> CoverLetterModel {
        let runner = DraftRunner(vaniBinary: r.vani, stateDir: r.state.path, key: { nil }, decisionKey: { nil })
        return CoverLetterModel(client: { CoverLetterClient(runner: runner, timeout: timeout) }, bankStore: bank)
    }

    private func eventually(_ what: String, seconds: TimeInterval = 10, _ done: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(seconds)
        while !done() {
            if Date() > deadline { return XCTFail("timed out waiting for \(what)") }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    /// Longest gap between 10 ms main-actor ticks over `seconds`.
    private func worstStall(for seconds: TimeInterval) async throws -> TimeInterval {
        var last = Date(), worst: TimeInterval = 0
        let until = Date().addingTimeInterval(seconds)
        while Date() < until {
            try await Task.sleep(nanoseconds: 10_000_000)
            worst = max(worst, Date().timeIntervalSince(last))
            last = Date()
        }
        return worst
    }

    private func lockFile(_ url: URL) -> Int32 {
        let fd = open(url.path, O_RDWR | O_CREAT, 0o600)
        XCTAssertGreaterThanOrEqual(fd, 0)
        XCTAssertEqual(flock(fd, LOCK_EX), 0)
        return fd
    }

    private func callCount(_ r: Rig) -> Int {
        ((try? String(contentsOf: r.calls, encoding: .utf8)) ?? "").split(separator: "\n").count
    }

    private let job = Job(key: "fx:alpha:1", company: "Acme", title: "Engineer")

    private func store0(_ r: Rig) -> BankStore { BankStore(directory: r.bank) }

    private func lettersStore(_ r: Rig) -> CoverStore {
        CoverStore(directory: r.bank.appendingPathComponent("letters", isDirectory: true))
    }

    // MARK: bankStore timing

    func testBankStoreIsResolvedOnceAtUseStartAndNeverForBuild() async throws {
        let r = try rig()
        let c = Counter()
        let store = BankStore(directory: r.bank)
        let m = model(r) { c.bump(); return store }
        m.build(job)
        try await eventually("build") { !m.busy }
        XCTAssertEqual(c.value, 0, "build must not resolve the bank store")
        m.use(job)
        XCTAssertEqual(c.value, 1, "use resolves the store synchronously, before the child runs")
        try await eventually("use") { !m.busy }
        XCTAssertEqual(c.value, 1)
    }

    /// The store is read at the start. One that only appears while the child runs is not seen, and
    /// the message is the no-store line even though the app could now hand the letter over.
    func testStoreThatAppearsAfterUseStartedIsNotPickedUp() async throws {
        let r2 = try rig()
        let c = Counter()
        let store = BankStore(directory: r2.bank)
        let m = model(r2) { c.bump(); return c.value == 1 ? nil : store }
        m.use(job)
        try await eventually("use") { !m.busy }
        XCTAssertEqual(m.status, "The letter was marked used, but this app cannot hand it to the browser.")
        XCTAssertNil(try lettersStore(r2).info(key: job.key))
    }

    /// The reverse: a store present at the start is used even if the closure would now say nil.
    func testStoreThatVanishesAfterUseStartedStillBinds() async throws {
        let r = try rig()
        let c = Counter()
        let store = BankStore(directory: r.bank)
        let m = model(r) { c.bump(); return c.value == 1 ? store : nil }
        m.use(job)
        try await eventually("use") { !m.busy }
        XCTAssertEqual(m.status, "v1 is the letter the browser may attach.")
        XCTAssertNotNil(try lettersStore(r).info(key: job.key))
    }

    // MARK: busy guard

    func testUseWhileBuildIsRunningIsRefusedAndRunsNoChild() async throws {
        let r = try rig(buildBody: "sleep 0.6; echo '{\"version\":1,\"pdf\":true}'")
        let m = model(r) { BankStore(directory: r.bank) }
        m.build(job)
        m.use(job)
        m.use(job)
        XCTAssertEqual(m.status, "Building the letter…")
        try await eventually("build") { !m.busy }
        XCTAssertEqual(m.status, "Saved v1.")
        XCTAssertEqual(callCount(r), 1, "only the build child may have run")
    }

    func testRapidDoubleUseRunsOneChild() async throws {
        let r = try rig()
        let m = model(r) { BankStore(directory: r.bank) }
        m.use(job)
        m.use(job)
        try await eventually("use") { !m.busy }
        XCTAssertEqual(callCount(r), 1)
    }

    // MARK: release mid-call

    func testModelReleasedMidCallFinishesWithoutCrashAndBindsTheLetter() async throws {
        let r = try rig(useBody: "sleep 0.5; echo '{\"version\":1,\"pdf\":false,\"text_path\":\"@TEXT@\"}'")
        weak var weakModel: CoverLetterModel?
        let store = BankStore(directory: r.bank)
        do {
            let m = model(r) { store }
            weakModel = m
            m.use(job)
        }
        // The only strong reference is gone. The task keeps the model alive until the work ends.
        try await eventually("bound") { (try? self.lettersStore(r).info(key: self.job.key)) != nil }
        try await eventually("model released after the call") { weakModel == nil }
        XCTAssertFalse(try store.recentActivity().isEmpty)
    }

    // MARK: hung child

    func testAHungChildTimesOutOffTheMainActorAndFreesTheModel() async throws {
        let r = try rig(useBody: "sleep 30")
        let m = model(r, timeout: 1) { BankStore(directory: r.bank) }
        m.use(job)
        let started = Date()
        let worst = try await worstStall(for: 0.8)
        XCTAssertTrue(m.busy)
        XCTAssertLessThan(worst, 0.25, "main actor stalled \(worst) s behind a hung child")
        try await eventually("timeout", seconds: 12) { !m.busy }
        XCTAssertLessThan(Date().timeIntervalSince(started), 10)
        XCTAssertTrue(m.status.contains("did not finish"), "status was: \(m.status)")
        XCTAssertNil(try lettersStore(r).info(key: job.key))
        // and the model is usable again
        m.build(job)
        XCTAssertEqual(m.status, "Building the letter…")
        try await eventually("build") { !m.busy }
    }

    // MARK: hostile letter files

    private func assertUse(_ r: Rig, status: String, line: UInt = #line) async throws {
        let m = model(r) { BankStore(directory: r.bank) }
        m.use(job)
        try await eventually("use") { !m.busy }
        XCTAssertEqual(m.status, status, line: line)
    }

    func testOversizedTextIsRefusedAsUnreadable() async throws {
        let r = try rig(text: Data(repeating: 0x61, count: CoverStore.maxTextBytes + 1))
        try await assertUse(r, status: "The used letter could not be read.")
        XCTAssertNil(try lettersStore(r).info(key: job.key))
    }

    func testTextExactlyAtTheCapIsAccepted() async throws {
        let r = try rig(text: Data(repeating: 0x61, count: CoverStore.maxTextBytes))
        try await assertUse(r, status: "v1 is the letter the browser may attach.")
    }

    func testNonUTF8AndEmptyTextAreUnreadable() async throws {
        let bad = try rig(text: Data([0xff, 0xfe, 0xfd, 0x80]))
        try await assertUse(bad, status: "The used letter could not be read.")
        let empty = try rig(text: Data())
        try await assertUse(empty, status: "The used letter could not be read.")
        let missing = try rig(text: nil)
        try await assertUse(missing, status: "The used letter could not be read.")
    }

    func testUnicodeLetterRoundTrips() async throws {
        let body = "Dear team \u{1F600} \u{202E}fdp.exe\u{0301} \u{0000} end"
        let r = try rig(text: Data(body.utf8))
        try await assertUse(r, status: "v1 is the letter the browser may attach.")
        let got = try lettersStore(r).get(key: job.key)
        XCTAssertEqual(got?.body, body)
    }

    func testTextPathOutsideLettersAndTraversalAreRefused() async throws {
        let outside = try rig(useBody: "echo '{\"version\":1,\"pdf\":false,\"text_path\":\"/etc/hosts\"}'")
        try await assertUse(outside, status: "The used letter could not be read.")
        let t = try rig(useBody: "echo '{\"version\":1,\"pdf\":false,\"text_path\":\"STATE/letters/../../vani\"}'")
        let script = try String(contentsOf: t.vani, encoding: .utf8).replacingOccurrences(of: "STATE", with: t.state.path)
        try Data(script.utf8).write(to: t.vani)
        try await assertUse(t, status: "The used letter could not be read.")
    }

    func testSymlinkedLetterIsRefused() async throws {
        let r = try rig()
        let link = r.letters.appendingPathComponent("link.txt")
        try FileManager.default.createSymbolicLink(at: link, withDestinationURL: URL(fileURLWithPath: "/etc/hosts"))
        let script = try String(contentsOf: r.vani, encoding: .utf8).replacingOccurrences(of: "v1.txt", with: "link.txt")
        try Data(script.utf8).write(to: r.vani)
        try await assertUse(r, status: "The used letter could not be read.")
    }

    func testOversizedPDFIsDroppedAndTheTextStillBinds() async throws {
        let r = try rig()
        let big = r.letters.appendingPathComponent("v1.pdf")
        try Data("%PDF-".utf8 + Data(repeating: 0x20, count: CoverStore.maxBytes)).write(to: big)
        let script = try String(contentsOf: r.vani, encoding: .utf8)
            .replacingOccurrences(of: "\"pdf\":false", with: "\"pdf\":true,\"pdf_path\":\"\(big.path)\"")
        try Data(script.utf8).write(to: r.vani)
        try await assertUse(r, status: "v1 is the letter the browser may attach.")
        let got = try lettersStore(r).get(key: job.key)
        XCTAssertNil(got?.pdf)
    }

    /// A PDF file that is not a PDF makes put throw, which rejects the whole letter, text included.
    func testAFakePDFRejectsTheBindWithAnError() async throws {
        let r = try rig()
        let fake = r.letters.appendingPathComponent("v1.pdf")
        try Data("not a pdf".utf8).write(to: fake)
        let script = try String(contentsOf: r.vani, encoding: .utf8)
            .replacingOccurrences(of: "\"pdf\":false", with: "\"pdf\":true,\"pdf_path\":\"\(fake.path)\"")
        try Data(script.utf8).write(to: r.vani)
        let m = model(r) { BankStore(directory: r.bank) }
        m.use(job)
        try await eventually("use") { !m.busy }
        // The status is CoverStore.Failure.localizedDescription, which is not a readable sentence
        // (pre-existing: the old code used the same call). Pin only that it is not a success line.
        XCTAssertFalse(m.status.isEmpty)
        XCTAssertNotEqual(m.status, "v1 is the letter the browser may attach.")
        XCTAssertNotEqual(m.status, "Using the letter…")
        XCTAssertNil(try lettersStore(r).info(key: job.key))
    }

    // MARK: store failures

    func testPutThrowingIsReportedAndLeavesTheModelUsable() async throws {
        let r = try rig()
        // The letters folder is a regular file, so the store cannot create it.
        try Data("x".utf8).write(to: r.bank.appendingPathComponent("letters"))
        let m = model(r) { BankStore(directory: r.bank) }
        m.use(job)
        try await eventually("use") { !m.busy }
        XCTAssertNotEqual(m.status, "v1 is the letter the browser may attach.")
        XCTAssertNotEqual(m.status, "Using the letter…")
        XCTAssertFalse(m.status.isEmpty)
        XCTAssertFalse(m.busy)
        XCTAssertTrue(try store0(r).recentActivity().isEmpty, "a failed bind must not log a chosen letter")
    }

    /// store.log failing is swallowed with try?. The letter is bound and the status says so; the
    /// activity entry is lost silently. Pre-existing (the old code had the same try?).
    func testALogFailureIsSwallowedAndTheStatusStillReportsSuccess() async throws {
        let r = try rig()
        let store = BankStore(directory: r.bank, lockWait: 0.2)
        _ = try store.update { $0.settings.captureSelfId = true }
        let fd = lockFile(r.bank.appendingPathComponent("bank.lock"))
        defer { flock(fd, LOCK_UN); close(fd) }
        let m = model(r) { store }
        m.use(job)
        try await eventually("use") { !m.busy }
        XCTAssertEqual(m.status, "v1 is the letter the browser may attach.")
        flock(fd, LOCK_UN)
        XCTAssertTrue(try store.recentActivity().isEmpty, "the activity entry was lost, with no signal")
    }

    /// The cover store's own index lock held by someone else: the wait is off the main actor.
    func testAHeldCoverIndexLockDoesNotBlockTheMainActor() async throws {
        let r = try rig()
        let dir = r.bank.appendingPathComponent("letters", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let fd = lockFile(dir.appendingPathComponent("index.lock"))
        defer { flock(fd, LOCK_UN); close(fd) }
        let m = model(r) { BankStore(directory: r.bank) }
        m.use(job)
        let worst = try await worstStall(for: 0.8)
        XCTAssertLessThan(worst, 0.25, "main actor stalled \(worst) s on the cover index lock")
        XCTAssertTrue(m.busy)
        flock(fd, LOCK_UN)
        try await eventually("use after release") { !m.busy }
        XCTAssertEqual(m.status, "v1 is the letter the browser may attach.")
    }

    // MARK: concurrency

    func testTwoModelsUsingTheSameKeyAtOnceLeaveAConsistentStore() async throws {
        let r = try rig()
        let store = BankStore(directory: r.bank)
        let a = model(r) { store }, b = model(r) { store }
        a.use(job)
        b.use(job)
        try await eventually("both") { !a.busy && !b.busy }
        XCTAssertEqual(a.status, "v1 is the letter the browser may attach.")
        XCTAssertEqual(b.status, "v1 is the letter the browser may attach.")
        let got = try lettersStore(r).get(key: job.key)
        XCTAssertEqual(got?.body, "Dear team, a short synthetic letter.")
        XCTAssertEqual(try store.recentActivity().count, 2)
    }

    func testManyModelsUsingDifferentKeysAtOnceAllBind() async throws {
        let r = try rig()
        let store = BankStore(directory: r.bank)
        let models = (0..<12).map { _ in model(r) { store } }
        for (i, m) in models.enumerated() { m.use(Job(key: "fx:k:\(i)", company: "A", title: "B")) }
        try await eventually("all", seconds: 20) { models.allSatisfy { !$0.busy } }
        for (i, m) in models.enumerated() {
            XCTAssertEqual(m.status, "v1 is the letter the browser may attach.", "model \(i)")
            XCTAssertNotNil(try lettersStore(r).info(key: "fx:k:\(i)"))
        }
    }

    func testABadKeyIsRefusedBeforeAnyChildRuns() async throws {
        let r = try rig()
        let m = model(r) { BankStore(directory: r.bank) }
        m.use(Job(key: "--flag", company: "A", title: "B"))
        try await eventually("use") { !m.busy }
        XCTAssertEqual(m.status, "this posting's key is not one Vani issued")
        XCTAssertEqual(callCount(r), 0)
    }
}
