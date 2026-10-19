import Foundation
import XCTest
@testable import VaniApplyKit

final class BankStoreTests: XCTestCase {
    func testEmptyStoreReadsRevisionZero() throws {
        let store = BankStore(directory: try tempDir(self).appendingPathComponent("apply"))
        let snap = try store.read()
        XCTAssertEqual(snap.rev, 0)
        XCTAssertEqual(snap.bank, .empty)
    }

    func testCompareAndSwap() throws {
        let store = BankStore(directory: try tempDir(self))
        XCTAssertEqual(try store.put(baseRev: 0, bank: bankJSON([answerJSON("aa")])), .stored(rev: 1, droppedSecret: 0))
        // A writer that read revision 0 loses and gets what is there now.
        guard case let .conflict(snap) = try store.put(baseRev: 0, bank: bankJSON([answerJSON("bb")])) else {
            return XCTFail("stale put must conflict")
        }
        XCTAssertEqual(snap.rev, 1)
        XCTAssertEqual(Array(snap.bank.answers.keys), ["aa"])
        XCTAssertEqual(try store.put(baseRev: 1, bank: bankJSON([answerJSON("aa"), answerJSON("bb")])), .stored(rev: 2, droppedSecret: 0))
        XCTAssertEqual(try store.read().bank.answers.count, 2)
    }

    func testInvalidPutChangesNothing() throws {
        let store = BankStore(directory: try tempDir(self))
        _ = try store.put(baseRev: 0, bank: bankJSON([answerJSON("aa")]))
        XCTAssertThrowsError(try store.put(baseRev: 1, bank: bankJSON([answerJSON("aa", kind: "nope")])))
        XCTAssertEqual(try store.read().rev, 1)
    }

    func testSecretAnswersNeverReachDisk() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        XCTAssertEqual(try store.put(baseRev: 0, bank: bankJSON([answerJSON("passport number", value: ["X1234567"])])),
                       .stored(rev: 1, droppedSecret: 1))
        let onDisk = try String(contentsOf: dir.appendingPathComponent("bank.json"), encoding: .utf8)
        XCTAssertFalse(onDisk.contains("X1234567"))
    }

    func testFilesArePrivate() throws {
        let dir = try tempDir(self).appendingPathComponent("apply")
        let store = BankStore(directory: dir)
        _ = try store.put(baseRev: 0, bank: bankJSON([answerJSON("aa")]))
        try store.log(ActivityEntry(source: .app, event: .edit))
        let fm = FileManager.default
        func mode(_ p: String) throws -> Int { try (fm.attributesOfItem(atPath: p)[.posixPermissions] as! NSNumber).intValue }
        XCTAssertEqual(try mode(dir.path), 0o700)
        XCTAssertEqual(try mode(dir.appendingPathComponent("bank.json").path), 0o600)
        XCTAssertEqual(try mode(dir.appendingPathComponent("activity.jsonl").path), 0o600)
    }

    func testCorruptBankIsReportedAndLeftAlone() throws {
        let dir = try tempDir(self)
        let file = dir.appendingPathComponent("bank.json")
        try Data("{\"rev\":3,\"bank\":{\"version\":9}}".utf8).write(to: file)
        let store = BankStore(directory: dir)
        XCTAssertThrowsError(try store.read())
        XCTAssertThrowsError(try store.put(baseRev: 3, bank: bankJSON([answerJSON("aa")])))
        XCTAssertThrowsError(try store.update { $0.answers["aa"] = answer("aa") })
        XCTAssertEqual(try String(contentsOf: file, encoding: .utf8), "{\"rev\":3,\"bank\":{\"version\":9}}")

        let moved = try XCTUnwrap(try store.quarantineCorruptBank())
        XCTAssertTrue(FileManager.default.fileExists(atPath: moved.path))
        XCTAssertEqual(try store.read().rev, 0)
    }

    func testQuarantineLeavesAGoodBankAlone() throws {
        let store = BankStore(directory: try tempDir(self))
        _ = try store.put(baseRev: 0, bank: bankJSON([answerJSON("aa")]))
        XCTAssertNil(try store.quarantineCorruptBank())
        XCTAssertEqual(try store.read().rev, 1)
    }

    func testUpdateValidatesAndBumpsRevision() throws {
        let store = BankStore(directory: try tempDir(self))
        let snap = try store.update { $0.answers["first name"] = answer("first name", "Ada") }
        XCTAssertEqual(snap.rev, 1)
        XCTAssertThrowsError(try store.update { $0.answers["k"] = answer("k") }) // key too short
        XCTAssertEqual(try store.read().rev, 1)
    }

    /// Writers in separate threads, each with its own lock descriptor (as separate
    /// processes have), retrying on conflict as the extension does: nobody's answer is lost.
    func testConcurrentWritersLoseNothing() throws {
        let dir = try tempDir(self)
        let writers = 16
        let errors = NSLock()
        nonisolated(unsafe) var failures: [String] = []
        DispatchQueue.concurrentPerform(iterations: writers) { i in
            let store = BankStore(directory: dir)
            for _ in 0..<200 {
                do {
                    let snap = try store.read()
                    var bank = snap.bank
                    bank.answers["writer \(i)"] = answer("writer \(i)")
                    if case .stored = try store.put(baseRev: snap.rev, bank: bank) { return }
                } catch {
                    errors.lock(); failures.append("\(i): \(error)"); errors.unlock()
                    return
                }
            }
            errors.lock(); failures.append("\(i): never stored"); errors.unlock()
        }
        XCTAssertEqual(failures, [])
        let final = try BankStore(directory: dir).read()
        XCTAssertEqual(final.bank.answers.count, writers)
        XCTAssertEqual(final.rev, writers)
    }

    func testActivityLogRotatesAndReadsNewestFirst() throws {
        let cap = 4096
        let store = BankStore(directory: try tempDir(self), maxActivityBytes: cap)
        for i in 0..<3 { try store.log(ActivityEntry(source: .chrome, event: .capture, host: "h\(i).example", counts: ["added": i])) }
        let recent = try store.recentActivity(limit: 2)
        XCTAssertEqual(recent.map(\.host), ["h2.example", "h1.example"])

        let fm = FileManager.default
        let rotated = store.directory.appendingPathComponent("activity.1.jsonl")
        var writes = 0
        // Each line is well over 40 bytes, so the cap is crossed long before 4 x cap / 40 writes.
        while !fm.fileExists(atPath: rotated.path) && writes < 4 * cap / 40 {
            try store.log(ActivityEntry(source: .safari, event: .fill, host: "jobs.example", counts: ["filled": 5]))
            writes += 1
        }
        XCTAssertTrue(fm.fileExists(atPath: rotated.path), "no rotation after \(writes) writes")
        // Two more rotations: only one old file is kept, and the live file stays under the cap.
        for _ in 0..<(2 * cap / 40) {
            try store.log(ActivityEntry(source: .safari, event: .fill, host: "jobs.example", counts: ["filled": 5]))
        }
        let size = try (fm.attributesOfItem(atPath: store.directory.appendingPathComponent("activity.jsonl").path)[.size] as! NSNumber).intValue
        XCTAssertLessThanOrEqual(size, cap)
        XCTAssertFalse(fm.fileExists(atPath: store.directory.appendingPathComponent("activity.2.jsonl").path))
        XCTAssertLessThanOrEqual(try store.recentActivity(limit: 10_000).count, 2 * cap / 40)
    }

    /// The app re-reads the activity log only when this stamp changes, so it
    /// must change with every append and rotation, and with nothing else.
    func testActivityStampChangesOnlyWithTheLog() throws {
        let store = BankStore(directory: try tempDir(self), maxActivityBytes: 1024)
        let empty = store.activityStamp()
        XCTAssertEqual(store.activityStamp(), empty, "no log yet: stable")

        try store.log(ActivityEntry(source: .chrome, event: .capture, host: "a.example"))
        let one = store.activityStamp()
        XCTAssertNotEqual(one, empty)
        XCTAssertEqual(store.activityStamp(), one, "unchanged log: same stamp")
        _ = try store.recentActivity()
        XCTAssertEqual(store.activityStamp(), one, "a read is not a change")

        _ = try store.put(baseRev: 0, bank: bankJSON([answerJSON("aa")]))
        XCTAssertEqual(store.activityStamp(), one, "a bank write is not a log change")

        try store.log(ActivityEntry(source: .safari, event: .fill, host: "b.example"))
        let two = store.activityStamp()
        XCTAssertNotEqual(two, one)

        // Rotation moves the live file aside; the stamp follows both files.
        let rotated = store.directory.appendingPathComponent("activity.1.jsonl")
        var last = two
        while !FileManager.default.fileExists(atPath: rotated.path) {
            try store.log(ActivityEntry(source: .safari, event: .fill, host: "jobs.example"))
            let now = store.activityStamp()
            XCTAssertNotEqual(now, last)
            last = now
        }
    }

    /// The lock wait is bounded by lockWait, and a lock released partway
    /// through is taken promptly rather than at the next coarse poll.
    func testLockWaitIsBoundedAndTakesAReleasedLock() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir, lockWait: 0.3)
        _ = try store.read() // creates the directory and the lock file
        let fd = open(dir.appendingPathComponent("bank.lock").path, O_RDWR | O_CLOEXEC)
        XCTAssertGreaterThanOrEqual(fd, 0)
        defer { close(fd) }

        XCTAssertEqual(flock(fd, LOCK_EX), 0)
        var start = Date()
        XCTAssertThrowsError(try store.read()) { err in
            XCTAssertTrue(String(describing: err).contains("locked by another process"), "\(err)")
        }
        let waited = Date().timeIntervalSince(start)
        XCTAssertGreaterThanOrEqual(waited, 0.3)
        XCTAssertLessThan(waited, 0.3 + 0.2, "no sleep may run past the deadline")

        // Held 50 ms, then released: the reader gets it well before its 2 s limit.
        let patient = BankStore(directory: dir, lockWait: 2)
        DispatchQueue.global().asyncAfter(deadline: .now() + 0.05) { flock(fd, LOCK_UN) }
        start = Date()
        XCTAssertEqual(try patient.read().rev, 0)
        XCTAssertLessThan(Date().timeIntervalSince(start), 0.5)
    }
}
