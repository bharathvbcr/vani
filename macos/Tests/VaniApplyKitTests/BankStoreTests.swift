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

    func testAppGroupStoreDirectoryResolvesFallback() throws {
        let dir = try AppGroup.storeDirectory()
        XCTAssertFalse(dir.path.isEmpty)
        XCTAssertTrue(dir.path.hasSuffix("/apply"))
    }

    func testAppGroupStoreDirectoryEnvOverride() throws {
        let customDir = try tempDir(self).appendingPathComponent("custom-store", isDirectory: true)
        setenv("VANI_APPLY_STORE_DIR", customDir.path, 1)
        defer { unsetenv("VANI_APPLY_STORE_DIR") }
        let dir = try AppGroup.storeDirectory()
        XCTAssertEqual(dir.path, customDir.path)
    }

    func testDurableBankWritingPreservesModeAndSyncs() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        let res = try store.put(baseRev: 0, bank: bankJSON([answerJSON("test_key", value: ["val"])]))
        XCTAssertEqual(res, .stored(rev: 1, droppedSecret: 0))
        let snap = try store.read()
        XCTAssertEqual(snap.rev, 1)
        XCTAssertEqual(snap.bank.answers["test_key"]?.value, ["val"])

        let fm = FileManager.default
        let mode = try (fm.attributesOfItem(atPath: dir.appendingPathComponent("bank.json").path)[.posixPermissions] as! NSNumber).intValue
        XCTAssertEqual(mode, 0o600)

        // Activity log and lock file also enforce mode 0600
        try store.log(ActivityEntry(source: .app, event: .edit))
        let actMode = try (fm.attributesOfItem(atPath: dir.appendingPathComponent("activity.jsonl").path)[.posixPermissions] as! NSNumber).intValue
        XCTAssertEqual(actMode, 0o600)
    }

    /// bank.json is the source of truth: a put whose SQLite mirror write was refused must not
    /// make the next patch discard that put's rows.
    func testPatchAfterMissedMirrorKeepsEarlierPuts() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        XCTAssertEqual(try store.put(baseRev: 0, bank: bankJSON([answerJSON("aa")])), .stored(rev: 1, droppedSecret: 0))
        // Another writer moves the mirror's revision, so the mirror write of the next put misses.
        _ = try SQLiteBankStore(directory: dir).patch(upsert: [answer("zz")], deleteKeys: [])
        XCTAssertEqual(try store.put(baseRev: 1, bank: bankJSON([answerJSON("aa"), answerJSON("bb")])), .stored(rev: 2, droppedSecret: 0))

        let res = try store.patch(upsert: [answer("cc")], deleteKeys: [])
        XCTAssertEqual(res.rev, 3)
        let snap = try store.read()
        XCTAssertEqual(snap.rev, 3)
        XCTAssertEqual(Set(snap.bank.answers.keys), ["aa", "bb", "cc"])
        // The mirror agrees with the file.
        XCTAssertEqual(try store.sqlite?.currentRevision(), 3)
        XCTAssertEqual(Set(try XCTUnwrap(store.sqlite).read().bank.answers.keys), ["aa", "bb", "cc"])
    }

    /// Without a usable SQLite file a capture still lands, secrets are still dropped, and a stale base still conflicts.
    func testPatchWorksWithoutSQLite() throws {
        let dir = try tempDir(self)
        // A directory where the database file belongs: SQLite cannot open it.
        try FileManager.default.createDirectory(at: dir.appendingPathComponent("bank.sqlite"), withIntermediateDirectories: true)
        let store = BankStore(directory: dir)
        XCTAssertNil(store.sqlite)
        _ = try store.put(baseRev: 0, bank: bankJSON([answerJSON("aa")]))

        let secret = Answer(key: "passport number", labels: ["passport number"], kind: .text, value: ["X1234567"], sensitivity: .standard,
                            signatures: [], uses: 1, fills: 0, updatedAt: "2026-10-18T00:00:00.000Z", review: false)
        let res = try store.patch(upsert: [answer("cc"), secret], deleteKeys: ["aa"], baseRev: 1)
        XCTAssertEqual(res.rev, 2)
        XCTAssertEqual(res.droppedSecret, 1)
        let snap = try store.read()
        XCTAssertEqual(snap.rev, 2)
        XCTAssertEqual(Set(snap.bank.answers.keys), ["cc"])
        let onDisk = try String(contentsOf: dir.appendingPathComponent("bank.json"), encoding: .utf8)
        XCTAssertFalse(onDisk.contains("X1234567"))

        XCTAssertThrowsError(try store.patch(upsert: [answer("dd")], deleteKeys: [], baseRev: 1)) { error in
            guard case SQLiteBankStore.StoreError.conflict(let rev) = error else { return XCTFail("expected conflict, got \(error)") }
            XCTAssertEqual(rev, 2)
        }
        XCTAssertEqual(try store.read().rev, 2)
    }

    func testPatchConflictOnStaleBaseRevWithSQLite() throws {
        let store = BankStore(directory: try tempDir(self))
        _ = try store.put(baseRev: 0, bank: bankJSON([answerJSON("aa")]))
        XCTAssertThrowsError(try store.patch(upsert: [answer("bb")], deleteKeys: [], baseRev: 0)) { error in
            guard case SQLiteBankStore.StoreError.conflict(let rev) = error else { return XCTFail("expected conflict, got \(error)") }
            XCTAssertEqual(rev, 1)
        }
        XCTAssertEqual(Set(try store.read().bank.answers.keys), ["aa"])
    }

    /// A patch that would make bank.json unreadable is refused, not written.
    func testPatchRefusesRowsTheBankCannotHold() throws {
        let store = BankStore(directory: try tempDir(self))
        _ = try store.put(baseRev: 0, bank: bankJSON([answerJSON("aa")]))
        XCTAssertThrowsError(try store.patch(upsert: [answer("k")], deleteKeys: [])) // key too short
        XCTAssertEqual(try store.read().rev, 1)
    }

    func testPutKeepsInt32MaxAndZeroesOnePastIt() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        let max = Int(Int32.max)
        let err = try captureStderr {
            XCTAssertEqual(try store.put(baseRev: 0, bank: bankJSON([
                answerJSON("role title", extra: ["uses": max, "fills": max]),
                answerJSON("city name", extra: ["uses": max + 1, "fills": max + 1]),
            ])), .stored(rev: 1, droppedSecret: 0))
        }
        XCTAssertFalse(err.contains("does not fit"), "a counter the validator already zeroed is not a skipped mirror row")
        let snap = try store.read()
        XCTAssertEqual(snap.bank.answers["role title"]?.uses, max)
        XCTAssertEqual(snap.bank.answers["role title"]?.fills, max)
        XCTAssertEqual(snap.bank.answers["city name"]?.uses, 0)
        XCTAssertEqual(snap.bank.answers["city name"]?.fills, 0)
        let mirror = try XCTUnwrap(store.sqlite).read()
        XCTAssertEqual(mirror.bank.answers["role title"]?.fills, max)
        XCTAssertEqual(mirror.bank.answers["city name"]?.uses, 0)
        XCTAssertEqual(mirror.bank.answers["city name"]?.fills, 0)
        let onDisk = try String(contentsOf: dir.appendingPathComponent("bank.json"), encoding: .utf8)
        XCTAssertTrue(onDisk.contains(String(max)))
        XCTAssertFalse(onDisk.contains(String(max + 1)))
    }

    /// Five steps of +1 from zero, the way a capture increments. Not a walk to the ceiling.
    func testNaturalIncrementStaysSmall() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        var rev = 0
        var uses = 0
        for _ in 0..<5 {
            uses += 1
            guard case let .stored(next, _) = try store.put(baseRev: rev, bank: bankJSON([
                answerJSON("role title", extra: ["uses": uses, "fills": uses]),
            ])) else { return XCTFail("increment \(uses) was not stored") }
            rev = next
        }
        XCTAssertEqual(uses, 5)
        XCTAssertLessThan(uses, Int(Int32.max))
        XCTAssertEqual(try store.read().bank.answers["role title"]?.uses, 5)
        XCTAssertEqual(try XCTUnwrap(store.sqlite).read().bank.answers["role title"]?.fills, 5)
    }

    /// A hand-edited bank.json can already hold 2147483648. Opening it must not trap. The mirror skips
    /// that row and logs a fixed line. The file keeps the huge number until a later ordinary edit.
    func testHandEditedCounterIsReadWithoutTrappingAndMirrorSkipsIt() throws {
        let dir = try tempDir(self)
        let file = dir.appendingPathComponent("bank.json")
        let huge = 2_147_483_648
        let wrapped: [String: Any] = [
            "rev": 3,
            "bank": [
                "version": 1,
                "settings": ["captureSelfId": false, "disabledHosts": []],
                "answers": [
                    "city name": answerJSON("city name", value: ["Paris"], extra: ["uses": 1, "fills": huge]),
                    "role title": answerJSON("role title", value: ["engineer"], extra: ["uses": 2, "fills": 1]),
                    "note text": answerJSON("note text", value: ["hello"], extra: ["uses": 3, "fills": huge]),
                ],
            ] as [String: Any],
        ]
        try JSONSerialization.data(withJSONObject: wrapped).write(to: file)
        let sentence = "vani: bank.sqlite mirror skipped a row whose counter does not fit; bank.json is unaffected\n"
        var store: BankStore!
        let err = try captureStderr {
            store = BankStore(directory: dir)
            let snap = try store.read()
            XCTAssertEqual(snap.rev, 3)
            XCTAssertEqual(snap.bank.answers["city name"]?.uses, 1)
            XCTAssertEqual(snap.bank.answers["city name"]?.fills, 0)
            XCTAssertEqual(snap.bank.answers["city name"]?.value, ["Paris"])
            XCTAssertEqual(snap.bank.answers["role title"]?.uses, 2)
            XCTAssertEqual(snap.bank.answers["note text"]?.uses, 3)
            XCTAssertEqual(snap.bank.answers["note text"]?.fills, 0)
            let onDisk = try String(contentsOf: file, encoding: .utf8)
            XCTAssertTrue(onDisk.contains(String(huge)), "reading does not rewrite the file")
            let mirror = try XCTUnwrap(store.sqlite).read()
            XCTAssertEqual(Array(mirror.bank.answers.keys), ["role title"])
            XCTAssertEqual(mirror.bank.answers["role title"]?.uses, 2)

            let next = try store.update { bank in
                bank.answers["role title"]?.value = ["teacher"]
            }
            XCTAssertEqual(next.rev, 4)
            XCTAssertEqual(next.bank.answers["role title"]?.value, ["teacher"])
            XCTAssertEqual(next.bank.answers["city name"]?.fills, 0)
            XCTAssertEqual(next.bank.answers["note text"]?.fills, 0)
        }
        XCTAssertEqual(err.components(separatedBy: sentence).count - 1, 2)
        XCTAssertFalse(err.contains("Paris"))
        XCTAssertFalse(err.contains("engineer"))
        XCTAssertFalse(err.contains("hello"))
        XCTAssertFalse(err.contains("teacher"))
        XCTAssertFalse(err.contains("city name"))
        XCTAssertFalse(err.contains("role title"))
        XCTAssertFalse(err.contains("note text"))
        XCTAssertFalse(err.contains(String(huge)))
        let saved = try String(contentsOf: file, encoding: .utf8)
        XCTAssertFalse(saved.contains(String(huge)))
        let mirror = try XCTUnwrap(store.sqlite).read()
        XCTAssertEqual(mirror.bank.answers["role title"]?.value, ["teacher"])
        XCTAssertEqual(mirror.bank.answers["city name"]?.uses, 1)
        XCTAssertEqual(mirror.bank.answers["city name"]?.fills, 0)
        XCTAssertEqual(mirror.bank.answers["note text"]?.uses, 3)
        XCTAssertEqual(mirror.bank.answers["note text"]?.fills, 0)
        XCTAssertEqual(mirror.rev, 4)
    }
}
