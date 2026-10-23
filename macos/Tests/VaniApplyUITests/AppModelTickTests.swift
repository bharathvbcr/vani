import Darwin
import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

private final class Counter: @unchecked Sendable {
    private let lock = NSLock()
    private var n = 0
    var count: Int { lock.lock(); defer { lock.unlock() }; return n }
    func bump() { lock.lock(); n += 1; lock.unlock() }
}

/// The bank tick, and the values views ask for on every body evaluation. Temp folders only:
/// never the live App Group bank.
@MainActor
final class AppModelTickTests: XCTestCase {
    private func folder() throws -> URL {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("vani-appmodel-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        return dir
    }

    private func model(_ store: BankStore, reads: Counter = Counter(), drafts: Counter = Counter(), encodes: Counter = Counter()) -> AppModel {
        AppModel(store: store, autoRegisterAgent: false, autoStartTimer: false,
                 readBank: { reads.bump(); return try $0.read() },
                 loadDraftSettings: { drafts.bump(); return try DraftSettings.load(from: $0) },
                 measureBank: { encodes.bump(); return (try? $0.encoded().count) ?? 0 })
    }

    /// B2-2: with the bank unchanged, 50 ticks open and parse nothing.
    func testUnchangedBankIsNotReadByTheTick() async throws {
        let store = BankStore(directory: try folder())
        _ = try store.update { $0.settings.captureSelfId = true }
        let reads = Counter()
        let m = model(store, reads: reads)
        let afterLaunch = reads.count
        XCTAssertEqual(afterLaunch, 1)
        for _ in 0..<50 { await m.reloadIfChanged() }
        XCTAssertEqual(reads.count, afterLaunch, "50 ticks on an unchanged bank read it \(reads.count - afterLaunch) times")
        XCTAssertEqual(m.snapshot.rev, 1)

        // Another process writes: the next tick reads once, applies it, and the ticks after are quiet again.
        let other = BankStore(directory: store.directory)
        _ = try other.update { $0.settings.captureSelfId = false }
        await m.reloadIfChanged()
        XCTAssertEqual(reads.count, afterLaunch + 1)
        XCTAssertEqual(m.snapshot.rev, 2)
        for _ in 0..<50 { await m.reloadIfChanged() }
        XCTAssertEqual(reads.count, afterLaunch + 1)
    }

    /// A read that failed (a lock timeout clears by itself) is tried again every tick, and
    /// recovery clears the error.
    func testAFailedReadIsRetriedUntilItWorks() async throws {
        let store = BankStore(directory: try folder())
        _ = try store.update { $0.settings.captureSelfId = true }
        let fail = Counter(), reads = Counter()
        let m = AppModel(store: store, autoRegisterAgent: false, autoStartTimer: false, readBank: {
            reads.bump()
            if fail.count > 0 { throw BankError("locked") }
            return try $0.read()
        })
        fail.bump()
        let other = BankStore(directory: store.directory)
        _ = try other.update { $0.settings.captureSelfId = false } // moves the stamp
        await m.reloadIfChanged()
        XCTAssertNotNil(m.storeError)
        let failed = reads.count
        await m.reloadIfChanged()
        XCTAssertEqual(reads.count, failed + 1, "an errored store is read again on the next tick")
    }

    /// B2-2: a writer holding the exclusive lock (the read would wait its 5 s for it) does not
    /// stop the main actor: it keeps running while the tick waits, and the tick finishes when the
    /// lock is released.
    func testALockedBankDoesNotBlockTheMainActor() async throws {
        let dir = try folder()
        let store = BankStore(directory: dir, lockWait: 5)
        _ = try store.update { $0.settings.captureSelfId = true }
        let m = model(store)
        _ = try BankStore(directory: dir).update { $0.settings.captureSelfId = false } // moves the stamp

        let fd = open(dir.appendingPathComponent("bank.lock").path, O_RDWR | O_CREAT, 0o600)
        XCTAssertGreaterThanOrEqual(fd, 0)
        XCTAssertEqual(flock(fd, LOCK_EX), 0)
        defer { flock(fd, LOCK_UN); close(fd) }

        let tick = Task { await m.reloadIfChanged() }
        var last = Date(), worst: TimeInterval = 0
        let until = Date().addingTimeInterval(0.8)
        while Date() < until {
            try await Task.sleep(nanoseconds: 10_000_000)
            worst = max(worst, Date().timeIntervalSince(last))
            last = Date()
        }
        XCTAssertEqual(m.snapshot.rev, 1, "the read is still waiting for the lock")
        XCTAssertLessThan(worst, 0.25, "the main actor stalled for \(worst) s while the tick waited for the lock")
        flock(fd, LOCK_UN)
        await tick.value
        XCTAssertEqual(m.snapshot.rev, 2)
        XCTAssertNil(m.storeError)
    }

    /// An edit made while a background read is in flight is not overwritten by that read's older result.
    func testAnEditWinsOverAnOlderTickRead() async throws {
        let dir = try folder()
        let store = BankStore(directory: dir, lockWait: 5)
        _ = try store.update { $0.settings.captureSelfId = true }
        let m = model(store)
        _ = try BankStore(directory: dir).update { $0.settings.captureSelfId = false }
        let fd = open(dir.appendingPathComponent("bank.lock").path, O_RDWR | O_CREAT, 0o600)
        XCTAssertEqual(flock(fd, LOCK_EX), 0)
        defer { close(fd) }
        let tick = Task { await m.reloadIfChanged() }
        try await Task.sleep(nanoseconds: 100_000_000)
        flock(fd, LOCK_UN)
        m.setCaptureSelfId(true) // an edit here: its snapshot is the newest
        await tick.value
        XCTAssertEqual(m.snapshot.rev, 3)
        XCTAssertTrue(m.settings.captureSelfId)
    }

    /// B2-3: bankBytes encodes once per snapshot, however many times a body asks.
    func testBankBytesIsEncodedOncePerSnapshot() throws {
        let store = BankStore(directory: try folder())
        let encodes = Counter()
        let m = model(store, encodes: encodes)
        for _ in 0..<100 { _ = m.bankBytes }
        XCTAssertEqual(encodes.count, 1)
        m.setCaptureSelfId(true)
        let size = m.bankBytes
        for _ in 0..<100 { XCTAssertEqual(m.bankBytes, size) }
        XCTAssertEqual(encodes.count, 2, "one more encode for the new snapshot")
    }

    /// B2-3: the draft settings are read from disk at load, on a save and on activation, not per access.
    func testDraftSettingsReadDiskOncePerSaveOrActivation() throws {
        let dir = try folder()
        let store = BankStore(directory: dir)
        let drafts = Counter()
        let m = model(store, drafts: drafts)
        let afterLaunch = drafts.count
        for _ in 0..<100 { _ = m.draftSettings; _ = m.draftSettingsProblem }
        XCTAssertEqual(drafts.count, afterLaunch)

        m.saveDraftSettings(DraftSettings(vaniConfig: "/tmp/c.json", vaniStateDir: "/tmp/s"))
        XCTAssertEqual(drafts.count, afterLaunch + 1)
        for _ in 0..<100 { XCTAssertEqual(m.draftSettings.vaniConfig, "/tmp/c.json") }
        XCTAssertEqual(drafts.count, afterLaunch + 1)

        m.refreshDraftSettings() // activation
        XCTAssertEqual(drafts.count, afterLaunch + 2)
    }

    /// The cache must not hide a bad file: the problem is kept and shown, and clears once fixed.
    func testDraftSettingsProblemIsKeptUntilTheFileIsFixed() throws {
        let dir = try folder()
        let m = model(BankStore(directory: dir))
        XCTAssertNil(m.draftSettingsProblem)
        try Data("{not json".utf8).write(to: dir.appendingPathComponent("draft-settings.json"))
        m.refreshDraftSettings()
        XCTAssertNotNil(m.draftSettingsProblem)
        XCTAssertEqual(m.draftSettings, DraftSettings(), "a corrupt file shows as defaults")
        for _ in 0..<10 { XCTAssertNotNil(m.draftSettingsProblem) }
        try Data("{}".utf8).write(to: dir.appendingPathComponent("draft-settings.json"))
        m.refreshDraftSettings()
        XCTAssertNil(m.draftSettingsProblem)
    }
}
