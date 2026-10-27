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

    private func appModel(_ store: BankStore) -> AppModel {
        AppModel(store: store, autoRegisterAgent: false, autoStartTimer: false)
    }

    /// Exclusive lock on the bank. The caller unlocks; same-process `flock` blocks `BankStore`.
    private func lockBank(_ dir: URL) throws -> Int32 {
        let fd = open(dir.appendingPathComponent("bank.lock").path, O_RDWR | O_CREAT, 0o600)
        XCTAssertGreaterThanOrEqual(fd, 0, String(cString: strerror(errno)))
        XCTAssertEqual(flock(fd, LOCK_EX), 0, String(cString: strerror(errno)))
        return fd
    }

    /// How long the main actor goes without running, while other work waits on the bank lock.
    private func worstGap(for seconds: TimeInterval) async throws -> TimeInterval {
        var last = Date(), worst: TimeInterval = 0
        let until = Date().addingTimeInterval(seconds)
        while Date() < until {
            try await Task.sleep(nanoseconds: 10_000_000)
            worst = max(worst, Date().timeIntervalSince(last))
            last = Date()
        }
        return worst
    }

    /// `edit` must return within `seconds`. BlockingWork is not interruptible; this only fails a hang.
    private func finished(_ edit: Task<Void, Never>, within seconds: TimeInterval) async -> Bool {
        await withTaskGroup(of: Bool.self) { group in
            group.addTask { await edit.value; return true }
            group.addTask {
                try? await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
                return false
            }
            let first = await group.next() ?? false
            group.cancelAll()
            return first
        }
    }

    /// An activity file over the store cap: the edit still lands, and the log already on screen stays.
    /// `try?` swallows the re-read, so a throw here must not roll the snapshot back.
    func testOversizedActivityKeepsThePreviousListAndTheEdit() async throws {
        let cap = 4096
        let dir = try folder()
        let store = BankStore(directory: dir, maxActivityBytes: cap)
        _ = try store.update { $0.settings.captureSelfId = true }
        let seeded = [
            ActivityEntry(at: "2026-10-18T00:00:00.000Z", source: .app, event: .edit, counts: ["added": 1]),
            ActivityEntry(at: "2026-10-18T00:00:01.000Z", source: .app, event: .settings, counts: ["edited": 1]),
        ]
        for entry in seeded { try store.log(entry) }
        let live = dir.appendingPathComponent("activity.jsonl")
        let seededBytes = try Data(contentsOf: live).count
        XCTAssertLessThanOrEqual(seededBytes, cap, "the fixture itself is over the activity cap")
        let m = appModel(store)
        for _ in 0..<20 where m.activity.isEmpty {
            await m.reloadIfChanged()
        }
        let kept = m.activity
        XCTAssertEqual(kept, seeded.reversed(), "the seeded log never reached the model")
        var over = try Data(contentsOf: live)
        over.append(Data(repeating: 0x0A, count: cap - over.count + 1))
        XCTAssertGreaterThan(over.count, cap)
        try over.write(to: live)
        XCTAssertThrowsError(try store.recentActivity()) { error in
            XCTAssertEqual(error as? BankError, BankError("activity log is larger than \(cap) bytes"))
        }
        let before = m.snapshot.rev
        await m.setCaptureSelfId(false)
        let disk = try BankStore(directory: dir, maxActivityBytes: cap).read()
        XCTAssertGreaterThan(disk.rev, before, "the bank write did not land")
        XCTAssertFalse(disk.bank.settings.captureSelfId)
        XCTAssertEqual(m.snapshot.rev, disk.rev, "the edit wrote rev \(disk.rev) but the model kept \(m.snapshot.rev) after the activity re-read failed (actionError=\(m.actionError ?? "nil"))")
        XCTAssertFalse(m.settings.captureSelfId)
        XCTAssertEqual(m.activity, kept, "the oversized activity re-read replaced the previous log")
    }

    /// Two edits blocked on the same lock both finish after it is released. The later revision wins,
    /// including when the earlier edit applies on the main actor second.
    func testSecondEditBlockedOnTheLockBothFinishAndHigherRevWins() async throws {
        let dir = try folder()
        let store = BankStore(directory: dir, lockWait: 5)
        _ = try store.update { $0.settings.captureSelfId = true }
        let m = appModel(store)
        XCTAssertEqual(m.snapshot.rev, 1)
        let fd = try lockBank(dir)
        defer { flock(fd, LOCK_UN); close(fd) }
        let first = Task { await m.setCaptureSelfId(false) }
        let second = Task { await m.setCaptureSelfId(true) }
        let worst = try await worstGap(for: 0.8)
        XCTAssertEqual(m.snapshot.rev, 1, "an edit finished while the bank lock was still held")
        XCTAssertTrue(m.settings.captureSelfId, "an edit finished while the bank lock was still held")
        XCTAssertLessThan(worst, 0.25, "the main actor stalled for \(worst) s while two edits waited for the lock")
        print("MAC-2 two-edit worst gap \(worst) s")
        flock(fd, LOCK_UN)
        let firstBack = await finished(first, within: 3)
        let secondBack = await finished(second, within: 3)
        XCTAssertTrue(firstBack, "the first edit did not return within 3s of the unlock")
        XCTAssertTrue(secondBack, "the second edit did not return within 3s of the unlock")
        let disk = try BankStore(directory: dir).read()
        XCTAssertEqual(disk.rev, 3, "both edits did not each take a revision; disk rev is \(disk.rev)")
        XCTAssertEqual(m.snapshot.rev, disk.rev, "the model kept rev \(m.snapshot.rev) instead of the higher disk rev \(disk.rev)")
        XCTAssertEqual(m.settings.captureSelfId, disk.bank.settings.captureSelfId)
        XCTAssertNil(m.actionError)
    }

    /// Cancelling the task that is awaiting an edit must not deadlock. The work is not interruptible
    /// and may still finish; the snapshot is not required to roll back.
    func testCancellingAnEditReturns() async throws {
        let dir = try folder()
        let store = BankStore(directory: dir, lockWait: 5)
        _ = try store.update { $0.settings.captureSelfId = true }
        let m = appModel(store)
        let fd = try lockBank(dir)
        defer { flock(fd, LOCK_UN); close(fd) }
        let edit = Task { await m.setCaptureSelfId(false) }
        try await Task.sleep(nanoseconds: 50_000_000)
        edit.cancel()
        let worst = try await worstGap(for: 0.4)
        XCTAssertLessThan(worst, 0.25, "the main actor stalled for \(worst) s after the edit was cancelled")
        XCTAssertEqual(m.snapshot.rev, 1, "the edit finished while the bank lock was still held")
        flock(fd, LOCK_UN)
        let back = await finished(edit, within: 3)
        XCTAssertTrue(back, "cancelling the edit deadlocked; the task did not return within 3s")
        XCTAssertTrue(edit.isCancelled)
        print("MAC-2 cancel returned; snapshot rev \(m.snapshot.rev)")
    }

    /// A store directory whose name has a space and a non-ASCII character still edits off the actor.
    func testUnicodeDirectoryEditWaitsOffTheMainActor() async throws {
        let dir = try folder().appendingPathComponent("bänk α")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        XCTAssertTrue(dir.path.contains(" "))
        XCTAssertTrue(dir.path.unicodeScalars.contains { $0.value > 127 }, dir.path)
        let store = BankStore(directory: dir, lockWait: 5)
        _ = try store.update { $0.settings.captureSelfId = true }
        let m = appModel(store)
        XCTAssertEqual(m.snapshot.rev, 1)
        let fd = try lockBank(dir)
        defer { flock(fd, LOCK_UN); close(fd) }
        let edit = Task { await m.setCaptureSelfId(false) }
        let worst = try await worstGap(for: 0.8)
        XCTAssertEqual(m.snapshot.rev, 1, "the edit finished while the bank lock was still held")
        XCTAssertTrue(m.settings.captureSelfId)
        XCTAssertLessThan(worst, 0.25, "the main actor stalled for \(worst) s while an edit in \(dir.lastPathComponent) waited for the lock")
        print("MAC-2 unicode-path worst gap \(worst) s")
        flock(fd, LOCK_UN)
        let back = await finished(edit, within: 3)
        XCTAssertTrue(back, "the edit in the non-ASCII directory did not return within 3s")
        XCTAssertEqual(m.snapshot.rev, 2)
        XCTAssertFalse(m.settings.captureSelfId)
        XCTAssertNil(m.actionError)
        let disk = try BankStore(directory: dir).read()
        XCTAssertEqual(disk.rev, 2)
        XCTAssertFalse(disk.bank.settings.captureSelfId)
    }

    /// No bank.json yet: turning capture on writes rev 1 and does not trap.
    func testEmptyBankSetCaptureSelfIdDoesNotTrap() async throws {
        let dir = try folder()
        XCTAssertEqual(PathProbe.leaf(dir.appendingPathComponent("bank.json")), .absent)
        let m = appModel(BankStore(directory: dir))
        XCTAssertEqual(m.snapshot.rev, 0)
        XCTAssertFalse(m.settings.captureSelfId)
        XCTAssertNil(m.storeError)
        await m.setCaptureSelfId(true)
        XCTAssertNil(m.actionError)
        XCTAssertEqual(m.snapshot.rev, 1)
        XCTAssertTrue(m.settings.captureSelfId)
        let disk = try BankStore(directory: dir).read()
        XCTAssertEqual(disk.rev, 1)
        XCTAssertTrue(disk.bank.settings.captureSelfId)
        XCTAssertTrue(disk.bank.answers.isEmpty)
    }

    /// One byte over the cap, sparse: refuse before the body is read, and do not replace the store.
    func testImportBankSparseOneByteOverCapDoesNotReadOrReplace() async throws {
        XCTAssertEqual(Limits.maxBankBytes, 50_000_000)
        let dir = try folder()
        let store = BankStore(directory: dir)
        let kept = Answer(key: "kept", labels: ["kept"], kind: .text, value: ["stored"], sensitivity: .standard,
                          signatures: [], uses: 1, fills: 0, updatedAt: "2026-10-18T00:00:00.000Z", review: false)
        _ = try store.update { $0.answers["kept"] = kept }
        let bankURL = dir.appendingPathComponent("bank.json")
        let before = try Data(contentsOf: bankURL)
        let huge = dir.appendingPathComponent("incoming.json")
        let fd = open(huge.path, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0o600)
        XCTAssertGreaterThanOrEqual(fd, 0, String(cString: strerror(errno)))
        XCTAssertEqual(ftruncate(fd, off_t(Limits.maxBankBytes + 1)), 0, String(cString: strerror(errno)))
        close(fd)
        var st = stat()
        XCTAssertEqual(lstat(huge.path, &st), 0)
        XCTAssertEqual(st.st_size, off_t(Limits.maxBankBytes + 1))
        let atime = st.st_atimespec
        let incoming = st.st_ino
        let m = appModel(store)
        XCTAssertEqual(m.snapshot.bank.answers["kept"]?.value, ["stored"])
        let start = Date()
        await m.importBank(from: huge)
        XCTAssertLessThan(Date().timeIntervalSince(start), 2, "import must refuse before reading the body")
        XCTAssertEqual(m.actionError, "Import refused: bank file is larger than 50000000 bytes")
        XCTAssertNil(m.notice)
        XCTAssertEqual(m.snapshot.bank.answers["kept"]?.value, ["stored"])
        XCTAssertEqual(try Data(contentsOf: bankURL), before)
        var after = stat()
        XCTAssertEqual(lstat(huge.path, &after), 0)
        XCTAssertEqual(after.st_size, st.st_size)
        XCTAssertEqual(after.st_ino, incoming)
        XCTAssertEqual(after.st_atimespec.tv_sec, atime.tv_sec)
        XCTAssertEqual(after.st_atimespec.tv_nsec, atime.tv_nsec, "import read the sparse file body (atime moved)")
    }

    /// An answer value with non-ASCII, combining, and non-BMP characters survives `change` unchanged.
    func testUnicodeAnswerValueRoundTripsThroughChange() async throws {
        let store = BankStore(directory: try folder())
        let m = appModel(store)
        let value = "Ångströmstraße — 東京 «café» \u{1F9EA}\u{200D}\u{1F9EA} مرحبا \u{2028}e\u{0301}"
        let question = "Preferred city"
        let added = await m.add(question: question, kind: .text, text: value)
        XCTAssertTrue(added, m.actionError ?? "add returned false")
        let key = Classifier.normalizeLabel(question)
        XCTAssertEqual(m.snapshot.bank.answers[key]?.value, [value])
        XCTAssertEqual(m.snapshot.bank.answers[key]?.value.first?.utf16.count, value.utf16.count)
        let longer = value + " " + value
        let edited = await m.edit(key: key, text: longer)
        XCTAssertTrue(edited, m.actionError ?? "edit returned false")
        XCTAssertEqual(m.snapshot.bank.answers[key]?.value, [longer])
        XCTAssertEqual(m.snapshot.bank.answers[key]?.value.first?.utf8.count, longer.utf8.count)
        let disk = try store.read()
        XCTAssertEqual(disk.bank.answers[key]?.value, [longer])
        XCTAssertNil(m.actionError)
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
