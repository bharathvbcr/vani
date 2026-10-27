import Darwin
import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

@MainActor
final class AppModelAdversarialTests: XCTestCase {
    private func folder() throws -> URL {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("vani-appmodel-adv-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        return dir
    }

    /// The tick's read has the bank an external write just made; a user edit lands before the read
    /// returns. The old result must not overwrite the edit, and the next tick must converge on
    /// the newest bank.
    func testEditDuringTickReadWins() async throws {
        let dir = try folder()
        let store = BankStore(directory: dir)
        _ = try store.update { $0.settings.captureSelfId = true }
        _ = try BankStore(directory: dir).update { $0.settings.captureSelfId = false } // rev 2 on disk
        let held = HeldRead()
        let m3 = AppModel(store: store, autoRegisterAgent: false, autoStartTimer: false, readBank: { try held.read($0) })
        held.arm()
        _ = try BankStore(directory: dir).update { $0.settings.captureSelfId = false } // moves the stamp (rev 3)
        let tick = Task { await m3.reloadIfChanged() }
        await held.waitEntered() // the tick has read rev 3's bank and is parked
        XCTAssertEqual(m3.snapshot.rev, 2, "launch read")
        await m3.setCaptureSelfId(true) // user edit: CAS on current disk rev (3) -> rev 4
        XCTAssertEqual(m3.snapshot.rev, 4)
        held.release()
        await tick.value
        XCTAssertEqual(m3.snapshot.rev, 4, "the tick's older result overwrote the edit")
        XCTAssertTrue(m3.settings.captureSelfId)
        // The next tick converges: another external write is picked up.
        held.disarm()
        _ = try BankStore(directory: dir).update { $0.settings.captureSelfId = false }
        await m3.reloadIfChanged()
        XCTAssertEqual(m3.snapshot.rev, 5)
        XCTAssertFalse(m3.settings.captureSelfId)
    }

    /// A Retry (reload) while a tick read is in flight supersedes it too.
    func testReloadDuringTickReadSupersedesIt() async throws {
        let dir = try folder()
        let store = BankStore(directory: dir)
        _ = try store.update { $0.settings.captureSelfId = true }
        let held = HeldRead()
        let m = AppModel(store: store, autoRegisterAgent: false, autoStartTimer: false, readBank: { try held.read($0) })
        held.arm()
        _ = try BankStore(directory: dir).update { $0.settings.captureSelfId = false } // rev 2
        let tick = Task { await m.reloadIfChanged() }
        await held.waitEntered()
        _ = try BankStore(directory: dir).update { $0.settings.captureSelfId = true } // rev 3 lands
        held.disarm()
        m.reload() // user Retry: reads rev 3 now
        XCTAssertEqual(m.snapshot.rev, 3)
        held.release()
        await tick.value
        XCTAssertEqual(m.snapshot.rev, 3, "the parked tick (rev 2) overwrote the newer reload")
        // And the stamp bookkeeping converged: nothing left to read.
        let before = held.count
        await m.reloadIfChanged()
        XCTAssertEqual(held.count, before, "a settled app still re-reads the bank")
    }

    /// Timer ticks that overlap a slow read do not stack reads.
    func testOverlappingTicksShareOneRead() async throws {
        let dir = try folder()
        let store = BankStore(directory: dir)
        _ = try store.update { $0.settings.captureSelfId = true }
        let held = HeldRead()
        let m = AppModel(store: store, autoRegisterAgent: false, autoStartTimer: false, readBank: { try held.read($0) })
        held.arm()
        _ = try BankStore(directory: dir).update { $0.settings.captureSelfId = false }
        let first = held.count
        let a = Task { await m.reloadIfChanged() }
        await held.waitEntered()
        let b = Task { await m.reloadIfChanged() }
        let c = Task { await m.reloadIfChanged() }
        await b.value; await c.value
        XCTAssertEqual(held.count, first + 1, "overlapping ticks started extra reads")
        held.disarm(); held.release()
        await a.value
        XCTAssertEqual(m.snapshot.rev, 2)
    }

    /// bank.json replaced by an atomic rename with the same size and the same mtime but a new
    /// revision: the tick sees it (inode) and applies it.
    func testAtomicSameSizeSameMtimeReplaceIsAppliedByTheTick() async throws {
        let dir = try folder()
        let store = BankStore(directory: dir)
        _ = try store.update { $0.settings.captureSelfId = true }
        let m = AppModel(store: store, autoRegisterAgent: false, autoStartTimer: false)
        XCTAssertEqual(m.snapshot.rev, 1)
        let url = dir.appendingPathComponent("bank.json")
        var text = try String(contentsOf: url, encoding: .utf8)
        let range = try XCTUnwrap(text.range(of: #""rev"\s*:\s*1"#, options: .regularExpression), text)
        text.replaceSubrange(range, with: text[range].dropLast() + "2")
        var st = stat()
        XCTAssertEqual(stat(url.path, &st), 0)
        let temp = dir.appendingPathComponent("bank.tmp")
        try Data(text.utf8).write(to: temp)
        var times = [st.st_atimespec, st.st_mtimespec]
        XCTAssertEqual(utimensat(AT_FDCWD, temp.path, &times, 0), 0)
        XCTAssertEqual(rename(temp.path, url.path), 0)
        await m.reloadIfChanged()
        XCTAssertEqual(m.snapshot.rev, 2, "a same-size same-mtime atomic replace was missed by the tick")
    }

    /// bank.json deleted while the app runs, then recreated by another process.
    func testBankMissingThenCreatedIsPickedUp() async throws {
        let dir = try folder()
        let store = BankStore(directory: dir)
        let m = AppModel(store: store, autoRegisterAgent: false, autoStartTimer: false)
        XCTAssertEqual(m.snapshot.rev, 0)
        for _ in 0..<3 { await m.reloadIfChanged() }
        _ = try BankStore(directory: dir).update { $0.settings.captureSelfId = true }
        await m.reloadIfChanged()
        XCTAssertEqual(m.snapshot.rev, 1)
        XCTAssertNil(m.storeError)
    }

    /// Symlinked store directory.
    func testSymlinkedStoreDirectoryTicks() async throws {
        let real = try folder()
        let link = try folder().appendingPathComponent("link")
        try FileManager.default.createSymbolicLink(at: link, withDestinationURL: real)
        let m = AppModel(store: BankStore(directory: link), autoRegisterAgent: false, autoStartTimer: false)
        _ = try BankStore(directory: real).update { $0.settings.captureSelfId = true }
        await m.reloadIfChanged()
        XCTAssertEqual(m.snapshot.rev, 1)
    }

    /// Corrupt bank then fixed: the error shows, is retried, and clears when the file is fixed.
    func testCorruptThenFixedBankClearsTheErrorOnTheTick() async throws {
        let dir = try folder()
        let store = BankStore(directory: dir)
        _ = try store.update { $0.settings.captureSelfId = true }
        let m = AppModel(store: store, autoRegisterAgent: false, autoStartTimer: false)
        let url = dir.appendingPathComponent("bank.json")
        let good = try Data(contentsOf: url)
        try Data("{broken".utf8).write(to: url)
        await m.reloadIfChanged()
        XCTAssertNotNil(m.storeError)
        XCTAssertEqual(m.snapshot.rev, 1, "the last good snapshot stays on screen")
        try good.write(to: url) // same rev as the snapshot: the error must still clear
        await m.reloadIfChanged()
        XCTAssertNil(m.storeError, "a fixed bank left the error banner up")
    }

    /// bankBytes after an edit that changes the content but not the size or the rev's digit count.
    func testBankBytesFollowsContentNotRev() async throws {
        let store = BankStore(directory: try folder())
        let m = AppModel(store: store, autoRegisterAgent: false, autoStartTimer: false)
        await m.setCaptureSelfId(true)
        let small = m.bankBytes
        await m.setCaptureSelfId(false)
        let other = m.bankBytes
        XCTAssertNotEqual(small, other, "true -> false changes the encoded size; the cache returned the old one")
        _ = try BankStore(directory: store.directory).update { $0.settings.captureSelfId = true }
        await m.reloadIfChanged()
        XCTAssertEqual(m.bankBytes, small, "an external write was not re-measured")
    }

    /// The draft settings file edited by hand with no in-app save: stale until activation, then fresh.
    func testDraftSettingsEditedExternallyRefreshesOnActivation() throws {
        let dir = try folder()
        let m = AppModel(store: BankStore(directory: dir), autoRegisterAgent: false, autoStartTimer: false)
        try DraftSettings(vaniConfig: "/tmp/external.json", vaniStateDir: "").save(to: dir)
        XCTAssertNotEqual(m.draftSettings.vaniConfig, "/tmp/external.json", "not read on every access")
        m.refreshDraftSettings()
        XCTAssertEqual(m.draftSettings.vaniConfig, "/tmp/external.json")
        try Data("{bad".utf8).write(to: dir.appendingPathComponent("draft-settings.json"))
        m.refreshDraftSettings()
        XCTAssertNotNil(m.draftSettingsProblem)
        try DraftSettings(vaniConfig: "/tmp/fixed.json", vaniStateDir: "").save(to: dir)
        m.refreshDraftSettings()
        XCTAssertNil(m.draftSettingsProblem)
        XCTAssertEqual(m.draftSettings.vaniConfig, "/tmp/fixed.json")
        // A reload() (Retry) refreshes it too.
        try DraftSettings(vaniConfig: "/tmp/third.json", vaniStateDir: "").save(to: dir)
        m.reload()
        XCTAssertEqual(m.draftSettings.vaniConfig, "/tmp/third.json")
    }
}

/// A read seam that can be armed to park once, after it has read.
private final class HeldRead: @unchecked Sendable {
    private let entered = DispatchSemaphore(value: 0), go = DispatchSemaphore(value: 0)
    private let lock = NSLock()
    private var armed = false, calls = 0
    var count: Int { lock.lock(); defer { lock.unlock() }; return calls }
    func arm() { lock.lock(); armed = true; lock.unlock() }
    func disarm() { lock.lock(); armed = false; lock.unlock() }
    func read(_ store: BankStore) throws -> Snapshot {
        lock.lock(); calls += 1; let park = armed; lock.unlock()
        let snap = try store.read()
        if park { entered.signal(); go.wait() }
        return snap
    }
    func waitEntered() async { await withCheckedContinuation { c in DispatchQueue.global().async { self.entered.wait(); c.resume() } } }
    func release() { go.signal() }
}
