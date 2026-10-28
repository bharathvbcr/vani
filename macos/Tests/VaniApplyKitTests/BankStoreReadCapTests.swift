import Foundation
import SQLite3
import XCTest
@testable import VaniApplyKit

/// Oversized regular files are refused from their st_size, before Data(contentsOf:) reads the body.
final class BankStoreReadCapTests: XCTestCase {
    private func sparse(_ url: URL, bytes: Int) throws {
        let fd = open(url.path, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0o600)
        XCTAssertGreaterThanOrEqual(fd, 0, String(cString: strerror(errno)))
        defer { close(fd) }
        XCTAssertEqual(ftruncate(fd, off_t(bytes)), 0, String(cString: strerror(errno)))
    }

    private func sizeAndInode(_ url: URL) throws -> (Int64, UInt64) {
        var st = stat()
        XCTAssertEqual(lstat(url.path, &st), 0)
        return (Int64(st.st_size), UInt64(st.st_ino))
    }

    private func answerRows(_ dir: URL) throws -> Int {
        var db: OpaquePointer?
        let path = dir.appendingPathComponent("bank.sqlite").path
        XCTAssertEqual(sqlite3_open_v2(path, &db, SQLITE_OPEN_READONLY, nil), SQLITE_OK)
        guard let db else { throw SQLiteBankStore.StoreError.openFailed("nil handle") }
        defer { sqlite3_close(db) }
        var stmt: OpaquePointer?
        XCTAssertEqual(sqlite3_prepare_v2(db, "SELECT COUNT(*) FROM answers;", -1, &stmt, nil), SQLITE_OK)
        defer { sqlite3_finalize(stmt) }
        XCTAssertEqual(sqlite3_step(stmt), SQLITE_ROW)
        return Int(sqlite3_column_int(stmt, 0))
    }

    func testReadUnlockedRefusesSparseBankOverMaxBankBytes() throws {
        let dir = try tempDir(self).appendingPathComponent("bänk-α")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent("bank.json")
        let bytes = Limits.maxBankBytes + 1
        try sparse(url, bytes: bytes)
        let before = try sizeAndInode(url)
        XCTAssertEqual(before.0, Int64(bytes))
        let store = BankStore(directory: dir)
        let start = Date()
        XCTAssertThrowsError(try store.read()) { error in
            XCTAssertEqual(error as? BankError, BankError("the stored answer bank is larger than 50000000 bytes; it was left untouched"))
        }
        XCTAssertLessThan(Date().timeIntervalSince(start), 2, "over-cap read must fail before the body is loaded")
        XCTAssertEqual(try sizeAndInode(url).0, before.0)
        XCTAssertEqual(try sizeAndInode(url).1, before.1)
        XCTAssertFalse(FileManager.default.fileExists(atPath: dir.appendingPathComponent("bank.corrupt-unused.json").path))
    }

    func testSmallBankStillLoads() throws {
        let dir = try tempDir(self).appendingPathComponent("答案")
        let store = BankStore(directory: dir)
        XCTAssertEqual(try store.put(baseRev: 0, bank: bankJSON([answerJSON("aa")])), .stored(rev: 1, droppedSecret: 0))
        let snap = try store.read()
        XCTAssertEqual(snap.rev, 1)
        XCTAssertEqual(snap.bank.answers["aa"]?.value, ["v"])
    }

    func testDanglingBankSymlinkKeepsTheUnreadableLinkSentence() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        try FileManager.default.createSymbolicLink(
            atPath: dir.appendingPathComponent("bank.json").path,
            withDestinationPath: dir.appendingPathComponent("missing.json").path)
        XCTAssertThrowsError(try store.read()) { error in
            XCTAssertEqual(
                error as? BankError,
                BankError("the stored answer bank is unreadable (a link to a missing file); it was left untouched"))
        }
    }

    func testDirectoryAtBankJsonIsUnreadableWithoutReading() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        try FileManager.default.createDirectory(at: dir.appendingPathComponent("bank.json"), withIntermediateDirectories: true)
        XCTAssertThrowsError(try store.read()) { error in
            XCTAssertEqual(error as? BankError, BankError("the stored answer bank is unreadable; it was left untouched"))
        }
    }

    func testFifoAtBankJsonIsUnreadableWithoutBlocking() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        let path = dir.appendingPathComponent("bank.json").path
        XCTAssertEqual(mkfifo(path, 0o600), 0, String(cString: strerror(errno)))
        let done = expectation(description: "read returns")
        let errors = NSLock()
        nonisolated(unsafe) var failure: String?
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                _ = try store.read()
                errors.lock(); failure = "read returned a bank"; errors.unlock()
            } catch {
                errors.lock(); failure = String(describing: error); errors.unlock()
            }
            done.fulfill()
        }
        wait(for: [done], timeout: 2)
        let message = try XCTUnwrap(failure, "read blocked on the FIFO")
        XCTAssertTrue(message.contains("unreadable"), message)
        XCTAssertFalse(message.contains("50000000"), message)
    }

    func testSymlinkToOversizedBankIsRefusedBeforeReading() throws {
        let dir = try tempDir(self)
        let target = dir.appendingPathComponent("huge.json")
        try sparse(target, bytes: Limits.maxBankBytes + 1)
        let store = BankStore(directory: dir)
        try FileManager.default.createSymbolicLink(atPath: dir.appendingPathComponent("bank.json").path, withDestinationPath: target.path)
        let before = try sizeAndInode(target)
        let start = Date()
        XCTAssertThrowsError(try store.read()) { error in
            XCTAssertEqual(error as? BankError, BankError("the stored answer bank is larger than 50000000 bytes; it was left untouched"))
        }
        XCTAssertLessThan(Date().timeIntervalSince(start), 2)
        XCTAssertEqual(try sizeAndInode(target).0, before.0)
        XCTAssertEqual(try sizeAndInode(target).1, before.1)
    }

    func testMigrateFromJSONThrowsOnSparseBankOverCapAndInsertsNothing() throws {
        let dir = try tempDir(self)
        let url = dir.appendingPathComponent("bank.json")
        try sparse(url, bytes: Limits.maxBankBytes + 1)
        let before = try sizeAndInode(url)
        let start = Date()
        XCTAssertThrowsError(try SQLiteBankStore(directory: dir)) { error in
            XCTAssertEqual(error as? BankError, BankError("bank file is larger than 50000000 bytes"))
        }
        XCTAssertLessThan(Date().timeIntervalSince(start), 2, "migration must refuse before reading the body")
        XCTAssertEqual(try answerRows(dir), 0)
        XCTAssertEqual(try sizeAndInode(url).0, before.0)
        XCTAssertEqual(try sizeAndInode(url).1, before.1)
    }

    func testMigrateFromJSONStillSkipsSmallCorruptFile() throws {
        let dir = try tempDir(self)
        try Data("{not json".utf8).write(to: dir.appendingPathComponent("bank.json"))
        let store = try SQLiteBankStore(directory: dir)
        XCTAssertEqual(try store.read().rev, 0)
        XCTAssertEqual(try store.read().bank.answers.count, 0)
        XCTAssertEqual(try answerRows(dir), 0)
    }

    func testRecentActivityThrowsOverCapAndReadsExactCap() throws {
        let cap = 128
        let dir = try tempDir(self)
        let store = BankStore(directory: dir, maxActivityBytes: cap)
        let enc = JSONEncoder()
        enc.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        var line = try enc.encode(ActivityEntry(at: "2026-10-18T00:00:00.000Z", source: .app, event: .edit))
        line.append(0x0A)
        XCTAssertLessThanOrEqual(line.count, cap)
        var exact = line
        exact.append(Data(repeating: 0x0A, count: cap - exact.count))
        XCTAssertEqual(exact.count, cap)
        let live = dir.appendingPathComponent("activity.jsonl")
        try exact.write(to: live)
        let got = try store.recentActivity()
        XCTAssertEqual(got.count, 1)
        XCTAssertEqual(got.first?.event, .edit)
        XCTAssertEqual(got.first?.source, .app)

        var over = exact
        over.append(contentsOf: Data(repeating: 0x0A, count: 4096))
        try over.write(to: live)
        XCTAssertThrowsError(try store.recentActivity()) { error in
            XCTAssertEqual(error as? BankError, BankError("activity log is larger than \(cap) bytes"))
        }

        try exact.write(to: live)
        let rotated = dir.appendingPathComponent("activity.1.jsonl")
        try over.write(to: rotated)
        XCTAssertThrowsError(try store.recentActivity()) { error in
            XCTAssertEqual(error as? BankError, BankError("activity log is larger than \(cap) bytes"))
        }
    }

    func testRecentActivitySkipsMissingDanglingAndDoesNotBlockOnFifo() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir, maxActivityBytes: 64)
        XCTAssertEqual(try store.recentActivity(), [])
        try FileManager.default.createSymbolicLink(
            atPath: dir.appendingPathComponent("activity.jsonl").path,
            withDestinationPath: dir.appendingPathComponent("missing.jsonl").path)
        XCTAssertEqual(try store.recentActivity(), [])
        try FileManager.default.removeItem(at: dir.appendingPathComponent("activity.jsonl"))
        XCTAssertEqual(mkfifo(dir.appendingPathComponent("activity.jsonl").path, 0o600), 0)
        let done = expectation(description: "activity read returns")
        let errors = NSLock()
        nonisolated(unsafe) var failure: String?
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                let rows = try store.recentActivity()
                errors.lock(); failure = rows.isEmpty ? "" : "partial \(rows.count)"; errors.unlock()
            } catch {
                errors.lock(); failure = String(describing: error); errors.unlock()
            }
            done.fulfill()
        }
        wait(for: [done], timeout: 2)
        XCTAssertEqual(failure, "")
    }

    func testMissingBankInUnicodeDirectoryIsAnEmptySnapshot() throws {
        let dir = try unicodeStoreDir(self)
        XCTAssertEqual(PathProbe.leaf(dir.appendingPathComponent("bank.json")), .absent)
        let snap = try BankStore(directory: dir).read()
        XCTAssertEqual(snap.rev, 0)
        XCTAssertEqual(snap.bank, .empty)
    }

    func testDanglingBankSymlinkInUnicodeDirectoryKeepsTheUnreadableLinkSentence() throws {
        let dir = try unicodeStoreDir(self)
        let bank = dir.appendingPathComponent("bank.json")
        try FileManager.default.createSymbolicLink(atPath: bank.path, withDestinationPath: "missing.json")
        XCTAssertEqual(PathProbe.leaf(bank), .danglingLink)
        XCTAssertThrowsError(try BankStore(directory: dir).read()) { error in
            XCTAssertEqual(
                error as? BankError,
                BankError("the stored answer bank is unreadable (a link to a missing file); it was left untouched"))
        }
    }

    func testSymlinkToSmallBankInUnicodeDirectoryStillReads() throws {
        let dir = try unicodeStoreDir(self)
        let store = BankStore(directory: dir)
        XCTAssertEqual(try store.put(baseRev: 0, bank: bankJSON([answerJSON("aa")])), .stored(rev: 1, droppedSecret: 0))
        let bank = dir.appendingPathComponent("bank.json")
        let real = dir.appendingPathComponent("real.json")
        try FileManager.default.moveItem(at: bank, to: real)
        try FileManager.default.createSymbolicLink(atPath: bank.path, withDestinationPath: real.lastPathComponent)
        var link = stat()
        XCTAssertEqual(lstat(bank.path, &link), 0)
        XCTAssertEqual(link.st_mode & S_IFMT, S_IFLNK)
        XCTAssertLessThan(link.st_size, off_t(Limits.maxBankBytes))
        var target = stat()
        XCTAssertEqual(lstat(real.path, &target), 0)
        XCTAssertEqual(PathProbe.leaf(bank), .regular(Int64(target.st_size)))
        let snap = try store.read()
        XCTAssertEqual(snap.rev, 1)
        XCTAssertEqual(snap.bank.answers["aa"]?.value, ["v"])
    }

    func testSymlinkChainToOversizedBankIsRefusedByTargetSize() throws {
        let dir = try unicodeStoreDir(self)
        let huge = dir.appendingPathComponent("huge.json")
        try sparse(huge, bytes: Limits.maxBankBytes + 1)
        let mid = dir.appendingPathComponent("mid.json")
        let bank = dir.appendingPathComponent("bank.json")
        try FileManager.default.createSymbolicLink(atPath: mid.path, withDestinationPath: huge.lastPathComponent)
        try FileManager.default.createSymbolicLink(atPath: bank.path, withDestinationPath: mid.lastPathComponent)
        var link = stat()
        XCTAssertEqual(lstat(bank.path, &link), 0)
        XCTAssertLessThan(link.st_size, off_t(Limits.maxBankBytes))
        let before = try sizeAndInode(huge)
        XCTAssertEqual(PathProbe.leaf(bank), .regular(Int64(Limits.maxBankBytes + 1)))
        let start = Date()
        XCTAssertThrowsError(try BankStore(directory: dir).read()) { error in
            XCTAssertEqual(error as? BankError, BankError("the stored answer bank is larger than 50000000 bytes; it was left untouched"))
        }
        XCTAssertLessThan(Date().timeIntervalSince(start), 2)
        XCTAssertEqual(try sizeAndInode(huge).0, before.0)
        XCTAssertEqual(try sizeAndInode(huge).1, before.1)
        XCTAssertEqual(try FileManager.default.destinationOfSymbolicLink(atPath: bank.path), mid.lastPathComponent)
        XCTAssertFalse(FileManager.default.fileExists(atPath: dir.appendingPathComponent("bank.corrupt-unused.json").path))
    }

    func testSymlinkToFifoAtBankJsonIsUnreadableWithoutOpening() throws {
        let dir = try unicodeStoreDir(self)
        let fifo = dir.appendingPathComponent("pipe")
        XCTAssertEqual(mkfifo(fifo.path, 0o600), 0, String(cString: strerror(errno)))
        let bank = dir.appendingPathComponent("bank.json")
        try FileManager.default.createSymbolicLink(atPath: bank.path, withDestinationPath: fifo.lastPathComponent)
        let message = finishWithin(self, seconds: 1, name: "symlink fifo bank") {
            XCTAssertEqual(PathProbe.leaf(bank), .notRegular)
            let store = BankStore(directory: dir)
            do {
                _ = try store.read()
                return "read returned a bank"
            } catch {
                return (error as? BankError)?.description ?? "other \(error)"
            }
        }
        XCTAssertEqual(try XCTUnwrap(message, "read blocked on the symlink to the FIFO"), "the stored answer bank is unreadable; it was left untouched")
        assertFifoHasNoReader(fifo)
    }

    func testSymlinkToDirectoryAtBankJsonIsUnreadable() throws {
        let dir = try unicodeStoreDir(self)
        let sub = dir.appendingPathComponent("sub")
        try FileManager.default.createDirectory(at: sub, withIntermediateDirectories: true)
        let bank = dir.appendingPathComponent("bank.json")
        try FileManager.default.createSymbolicLink(atPath: bank.path, withDestinationPath: sub.lastPathComponent)
        XCTAssertEqual(PathProbe.leaf(bank), .notRegular)
        let start = Date()
        XCTAssertThrowsError(try BankStore(directory: dir).read()) { error in
            XCTAssertEqual(error as? BankError, BankError("the stored answer bank is unreadable; it was left untouched"))
        }
        XCTAssertLessThan(Date().timeIntervalSince(start), 1)
    }

    func testSymlinkLoopAtBankJsonIsUnavailableAndDoesNotHang() throws {
        let dir = try unicodeStoreDir(self)
        let bank = dir.appendingPathComponent("bank.json")
        let other = dir.appendingPathComponent("other.json")
        try FileManager.default.createSymbolicLink(atPath: bank.path, withDestinationPath: other.lastPathComponent)
        try FileManager.default.createSymbolicLink(atPath: other.path, withDestinationPath: bank.lastPathComponent)
        var st = stat()
        XCTAssertEqual(lstat(bank.path, &st), 0)
        XCTAssertNotEqual(stat(bank.path, &st), 0)
        XCTAssertEqual(errno, ELOOP)
        XCTAssertEqual(PathProbe.leaf(bank), .unavailable)
        let message = finishWithin(self, seconds: 1, name: "bank symlink loop") {
            do {
                _ = try BankStore(directory: dir).read()
                return "read returned a bank"
            } catch {
                return (error as? BankError)?.description ?? "other \(error)"
            }
        }
        XCTAssertEqual(try XCTUnwrap(message, "read blocked on the symlink loop"), "the stored answer bank is unreadable; it was left untouched")
        XCTAssertEqual(lstat(bank.path, &st), 0)
        XCTAssertEqual(st.st_mode & S_IFMT, S_IFLNK)
    }

    func testMigrateFollowsSymlinkToSmallBankAndStillSkipsCorrupt() throws {
        let dir = try unicodeStoreDir(self)
        let real = dir.appendingPathComponent("real.json")
        let raw: [String: Any] = ["rev": 1, "bank": bankJSON([answerJSON("aa")])]
        try JSONSerialization.data(withJSONObject: raw).write(to: real)
        let bank = dir.appendingPathComponent("bank.json")
        try FileManager.default.createSymbolicLink(atPath: bank.path, withDestinationPath: real.lastPathComponent)
        let store = try SQLiteBankStore(directory: dir)
        XCTAssertEqual(try store.read().rev, 1)
        XCTAssertEqual(try store.read().bank.answers["aa"]?.value, ["v"])
        XCTAssertEqual(try answerRows(dir), 1)

        let corruptDir = try unicodeStoreDir(self)
        let junk = corruptDir.appendingPathComponent("junk.json")
        try Data("{not json".utf8).write(to: junk)
        try FileManager.default.createSymbolicLink(
            atPath: corruptDir.appendingPathComponent("bank.json").path,
            withDestinationPath: junk.lastPathComponent)
        XCTAssertNoThrow(try SQLiteBankStore(directory: corruptDir))
        XCTAssertEqual(try answerRows(corruptDir), 0)
    }

    func testMigrateSymlinkToSparseOverCapThrowsAndInsertsNothing() throws {
        let dir = try unicodeStoreDir(self)
        let huge = dir.appendingPathComponent("huge.json")
        try sparse(huge, bytes: Limits.maxBankBytes + 1)
        let bank = dir.appendingPathComponent("bank.json")
        try FileManager.default.createSymbolicLink(atPath: bank.path, withDestinationPath: huge.lastPathComponent)
        var link = stat()
        XCTAssertEqual(lstat(bank.path, &link), 0)
        XCTAssertLessThan(link.st_size, off_t(Limits.maxBankBytes))
        XCTAssertEqual(PathProbe.leaf(bank), .regular(Int64(Limits.maxBankBytes + 1)))
        let before = try sizeAndInode(huge)
        let start = Date()
        XCTAssertThrowsError(try SQLiteBankStore(directory: dir)) { error in
            XCTAssertEqual(error as? BankError, BankError("bank file is larger than 50000000 bytes"))
        }
        XCTAssertLessThan(Date().timeIntervalSince(start), 2)
        XCTAssertEqual(try answerRows(dir), 0)
        XCTAssertEqual(try sizeAndInode(huge).0, before.0)
        XCTAssertEqual(try sizeAndInode(huge).1, before.1)
        XCTAssertEqual(try FileManager.default.destinationOfSymbolicLink(atPath: bank.path), huge.lastPathComponent)
    }

    func testMigrateSymlinkToFifoDoesNotOpenOrInsert() throws {
        let dir = try unicodeStoreDir(self)
        let fifo = dir.appendingPathComponent("pipe")
        XCTAssertEqual(mkfifo(fifo.path, 0o600), 0, String(cString: strerror(errno)))
        let bank = dir.appendingPathComponent("bank.json")
        try FileManager.default.createSymbolicLink(atPath: bank.path, withDestinationPath: fifo.lastPathComponent)
        let message = finishWithin(self, seconds: 1, name: "migrate fifo symlink") {
            XCTAssertEqual(PathProbe.leaf(bank), .notRegular)
            do {
                _ = try SQLiteBankStore(directory: dir)
                return "opened"
            } catch {
                return (error as? BankError)?.description ?? "other \(error)"
            }
        }
        XCTAssertEqual(try XCTUnwrap(message, "migrate blocked on the symlink to the FIFO"), "opened")
        XCTAssertEqual(try answerRows(dir), 0)
        assertFifoHasNoReader(fifo)
        var st = stat()
        XCTAssertEqual(lstat(fifo.path, &st), 0)
        XCTAssertEqual(st.st_mode & S_IFMT, S_IFIFO)
    }

    func testMigrateSymlinkLoopDoesNotHangOrInsert() throws {
        let dir = try unicodeStoreDir(self)
        let bank = dir.appendingPathComponent("bank.json")
        let other = dir.appendingPathComponent("other.json")
        try FileManager.default.createSymbolicLink(atPath: bank.path, withDestinationPath: other.lastPathComponent)
        try FileManager.default.createSymbolicLink(atPath: other.path, withDestinationPath: bank.lastPathComponent)
        var st = stat()
        XCTAssertNotEqual(stat(bank.path, &st), 0)
        XCTAssertEqual(errno, ELOOP)
        XCTAssertEqual(PathProbe.leaf(bank), .unavailable)
        let message = finishWithin(self, seconds: 1, name: "migrate symlink loop") {
            do {
                _ = try SQLiteBankStore(directory: dir)
                return "opened"
            } catch {
                return (error as? BankError)?.description ?? "other \(error)"
            }
        }
        XCTAssertEqual(try XCTUnwrap(message, "migrate blocked on the symlink loop"), "opened")
        XCTAssertEqual(try answerRows(dir), 0)
    }

    func testRecentActivityOneByteOverCapDoesNotReturnTheFollowingFile() throws {
        let dir = try unicodeStoreDir(self)
        let capture = try activityLine(.capture)
        let delete = try activityLine(.delete)
        let edit = try activityLine(.edit)
        let cap = max(capture.count, delete.count, edit.count)
        let over = padded(delete, to: cap + 1, byte: 0x20)
        let following = padded(capture, to: cap, byte: 0x0A)
        let exact = padded(edit, to: cap, byte: 0x0A)
        XCTAssertEqual(over.count, cap + 1)
        XCTAssertEqual(following.count, cap)
        XCTAssertEqual(exact.count, cap)
        let live = dir.appendingPathComponent("activity.jsonl")
        let rotated = dir.appendingPathComponent("activity.1.jsonl")
        try over.write(to: live)
        try following.write(to: rotated)
        let store = BankStore(directory: dir, maxActivityBytes: cap)
        do {
            let rows = try store.recentActivity()
            XCTFail("partial log \(rows.map(\.event))")
        } catch {
            XCTAssertEqual(error as? BankError, BankError("activity log is larger than \(cap) bytes"))
        }
        try exact.write(to: live)
        XCTAssertEqual(PathProbe.leaf(live), .regular(Int64(cap)))
        let rows = try store.recentActivity()
        XCTAssertEqual(rows.map(\.event), [.edit, .capture])
    }

    func testRecentActivitySymlinkOneByteOverCapUsesTargetSizeNotLinkSize() throws {
        let dir = try unicodeStoreDir(self)
        let capture = try activityLine(.capture)
        let delete = try activityLine(.delete)
        let edit = try activityLine(.edit)
        let live = dir.appendingPathComponent("activity.jsonl")
        try FileManager.default.createSymbolicLink(atPath: live.path, withDestinationPath: "t")
        var link = stat()
        XCTAssertEqual(lstat(live.path, &link), 0)
        let cap = max(capture.count, delete.count, edit.count, Int(link.st_size) + 1)
        XCTAssertLessThan(Int(link.st_size), cap)
        let over = padded(delete, to: cap + 1, byte: 0x20)
        let following = padded(capture, to: cap, byte: 0x0A)
        let exact = padded(edit, to: cap, byte: 0x0A)
        let target = dir.appendingPathComponent("t")
        try over.write(to: target)
        try following.write(to: dir.appendingPathComponent("activity.1.jsonl"))
        XCTAssertEqual(PathProbe.leaf(live), .regular(Int64(cap + 1)))
        let store = BankStore(directory: dir, maxActivityBytes: cap)
        do {
            let rows = try store.recentActivity()
            XCTFail("partial log \(rows.map(\.event))")
        } catch {
            XCTAssertEqual(error as? BankError, BankError("activity log is larger than \(cap) bytes"))
        }
        try exact.write(to: target)
        XCTAssertEqual(PathProbe.leaf(live), .regular(Int64(cap)))
        XCTAssertEqual(try store.recentActivity().map(\.event), [.edit, .capture])
    }

    func testRecentActivitySymlinkToFifoIsSkippedAndDoesNotBlock() throws {
        let dir = try unicodeStoreDir(self)
        let capture = try activityLine(.capture)
        let fifo = dir.appendingPathComponent("pipe")
        XCTAssertEqual(mkfifo(fifo.path, 0o600), 0, String(cString: strerror(errno)))
        let live = dir.appendingPathComponent("activity.jsonl")
        try FileManager.default.createSymbolicLink(atPath: live.path, withDestinationPath: fifo.lastPathComponent)
        try capture.write(to: dir.appendingPathComponent("activity.1.jsonl"))
        let message = finishWithin(self, seconds: 1, name: "activity fifo symlink") {
            XCTAssertEqual(PathProbe.leaf(live), .notRegular)
            let store = BankStore(directory: dir, maxActivityBytes: max(capture.count, 64))
            do {
                let rows = try store.recentActivity()
                return rows.map { "\($0.event)" }.joined(separator: ",")
            } catch {
                return "throw \(error)"
            }
        }
        XCTAssertEqual(try XCTUnwrap(message, "activity read blocked on the symlink to the FIFO"), "capture")
        assertFifoHasNoReader(fifo)
    }

    func testRecentActivitySymlinkLoopIsUnavailableAndDoesNotBlock() throws {
        let dir = try unicodeStoreDir(self)
        let capture = try activityLine(.capture)
        let live = dir.appendingPathComponent("activity.jsonl")
        let other = dir.appendingPathComponent("other.jsonl")
        try FileManager.default.createSymbolicLink(atPath: live.path, withDestinationPath: other.lastPathComponent)
        try FileManager.default.createSymbolicLink(atPath: other.path, withDestinationPath: live.lastPathComponent)
        try capture.write(to: dir.appendingPathComponent("activity.1.jsonl"))
        var st = stat()
        XCTAssertNotEqual(stat(live.path, &st), 0)
        XCTAssertEqual(errno, ELOOP)
        XCTAssertEqual(PathProbe.leaf(live), .unavailable)
        let message = finishWithin(self, seconds: 1, name: "activity symlink loop") {
            let store = BankStore(directory: dir, maxActivityBytes: max(capture.count, 64))
            do {
                let rows = try store.recentActivity()
                return rows.map { "\($0.event)" }.joined(separator: ",")
            } catch {
                return "throw \(error)"
            }
        }
        XCTAssertEqual(try XCTUnwrap(message, "activity read blocked on the symlink loop"), "capture")
    }
}

final class DraftSettingsReadCapTests: XCTestCase {
    private func sparse(_ url: URL, bytes: Int) throws {
        let fd = open(url.path, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0o600)
        XCTAssertGreaterThanOrEqual(fd, 0, String(cString: strerror(errno)))
        defer { close(fd) }
        XCTAssertEqual(ftruncate(fd, off_t(bytes)), 0, String(cString: strerror(errno)))
    }

    func testMissingDraftSettingsAreDefaults() throws {
        XCTAssertEqual(try DraftSettings.load(from: try tempDir(self)), DraftSettings())
    }

    func testSmallDraftSettingsStillLoad() throws {
        let dir = try tempDir(self)
        try DraftSettings(vaniConfig: "/tmp/vani-config.json", vaniStateDir: "/tmp/vani-state").save(to: dir)
        XCTAssertEqual(try DraftSettings.load(from: dir), DraftSettings(vaniConfig: "/tmp/vani-config.json", vaniStateDir: "/tmp/vani-state"))
    }

    func testDraftSettingsOverCapThrowsBeforeReading() throws {
        let dir = try tempDir(self)
        let cap = 256 * 1024
        try sparse(dir.appendingPathComponent(DraftSettings.fileName), bytes: cap + 1)
        let start = Date()
        XCTAssertThrowsError(try DraftSettings.load(from: dir)) { error in
            guard let load = error as? DraftSettings.LoadError else {
                return XCTFail("\(error)")
            }
            XCTAssertTrue(load.reason.contains("262144"), load.reason)
            XCTAssertFalse(load.reason.contains("not valid settings JSON"), load.reason)
        }
        XCTAssertLessThan(Date().timeIntervalSince(start), 2)
    }

    func testDraftSettingsAtExactCapIsDecoded() throws {
        let dir = try tempDir(self)
        try sparse(dir.appendingPathComponent(DraftSettings.fileName), bytes: 256 * 1024)
        XCTAssertThrowsError(try DraftSettings.load(from: dir)) { error in
            guard let load = error as? DraftSettings.LoadError else {
                return XCTFail("\(error)")
            }
            XCTAssertTrue(load.reason.contains("not valid settings JSON"), load.reason)
            XCTAssertFalse(load.reason.contains("262144"), load.reason)
        }
    }

    func testDanglingDraftSettingsSymlinkIsLoadErrorNotDefaults() throws {
        let dir = try tempDir(self)
        try FileManager.default.createSymbolicLink(
            atPath: dir.appendingPathComponent(DraftSettings.fileName).path,
            withDestinationPath: dir.appendingPathComponent("nowhere.json").path)
        XCTAssertThrowsError(try DraftSettings.load(from: dir)) { error in
            XCTAssertTrue(error is DraftSettings.LoadError, "\(error)")
            XCTAssertNotEqual(try? DraftSettings.load(from: dir), DraftSettings())
        }
    }

    func testMissingDraftSettingsInUnicodeDirectoryAreDefaults() throws {
        XCTAssertEqual(try DraftSettings.load(from: try unicodeStoreDir(self)), DraftSettings())
    }

    func testDraftSettingsSymlinkOverCapThrowsBeforeReading() throws {
        let dir = try unicodeStoreDir(self)
        let target = dir.appendingPathComponent("body.json")
        try sparse(target, bytes: 256 * 1024 + 1)
        let file = dir.appendingPathComponent(DraftSettings.fileName)
        try FileManager.default.createSymbolicLink(atPath: file.path, withDestinationPath: target.lastPathComponent)
        var link = stat()
        XCTAssertEqual(lstat(file.path, &link), 0)
        XCTAssertLessThan(link.st_size, 256 * 1024)
        XCTAssertEqual(PathProbe.leaf(file), .regular(Int64(256 * 1024 + 1)))
        let before = try sizeInode(target)
        let start = Date()
        XCTAssertThrowsError(try DraftSettings.load(from: dir)) { error in
            guard let load = error as? DraftSettings.LoadError else {
                return XCTFail("\(error)")
            }
            XCTAssertEqual(load.reason, "larger than 262144 bytes")
            XCTAssertFalse(load.reason.contains("not valid settings JSON"), load.reason)
        }
        XCTAssertLessThan(Date().timeIntervalSince(start), 2)
        XCTAssertEqual(try sizeInode(target).0, before.0)
        XCTAssertEqual(try sizeInode(target).1, before.1)
    }

    func testDraftSettingsSymlinkAtExactCapRequiresJSONError() throws {
        let dir = try unicodeStoreDir(self)
        let target = dir.appendingPathComponent("body.json")
        try sparse(target, bytes: 256 * 1024)
        let file = dir.appendingPathComponent(DraftSettings.fileName)
        try FileManager.default.createSymbolicLink(atPath: file.path, withDestinationPath: target.lastPathComponent)
        XCTAssertEqual(PathProbe.leaf(file), .regular(Int64(256 * 1024)))
        XCTAssertThrowsError(try DraftSettings.load(from: dir)) { error in
            guard let load = error as? DraftSettings.LoadError else {
                return XCTFail("\(error)")
            }
            XCTAssertTrue(load.reason.contains("not valid settings JSON"), load.reason)
            XCTAssertFalse(load.reason.contains("262144"), load.reason)
            XCTAssertNotEqual(load.reason, "cannot be read")
        }
    }

    func testDraftSettingsSymlinkToFifoIsLoadErrorWithoutOpening() throws {
        let dir = try unicodeStoreDir(self)
        let fifo = dir.appendingPathComponent("pipe")
        XCTAssertEqual(mkfifo(fifo.path, 0o600), 0, String(cString: strerror(errno)))
        let file = dir.appendingPathComponent(DraftSettings.fileName)
        try FileManager.default.createSymbolicLink(atPath: file.path, withDestinationPath: fifo.lastPathComponent)
        let message = finishWithin(self, seconds: 1, name: "draft fifo symlink") {
            XCTAssertEqual(PathProbe.leaf(file), .notRegular)
            do {
                let loaded = try DraftSettings.load(from: dir)
                return loaded == DraftSettings() ? "defaults" : "loaded"
            } catch let error as DraftSettings.LoadError {
                return error.reason
            } catch {
                return "other \(error)"
            }
        }
        let reason = try XCTUnwrap(message, "draft load blocked on the symlink to the FIFO")
        XCTAssertEqual(reason, "cannot be read")
        XCTAssertFalse(reason.contains("not valid settings JSON"))
        assertFifoHasNoReader(fifo)
    }

    func testDraftSettingsSymlinkLoopIsLoadErrorNotDefaults() throws {
        let dir = try unicodeStoreDir(self)
        let file = dir.appendingPathComponent(DraftSettings.fileName)
        let other = dir.appendingPathComponent("other.json")
        try FileManager.default.createSymbolicLink(atPath: file.path, withDestinationPath: other.lastPathComponent)
        try FileManager.default.createSymbolicLink(atPath: other.path, withDestinationPath: file.lastPathComponent)
        var st = stat()
        XCTAssertNotEqual(stat(file.path, &st), 0)
        XCTAssertEqual(errno, ELOOP)
        XCTAssertEqual(PathProbe.leaf(file), .unavailable)
        let message = finishWithin(self, seconds: 1, name: "draft symlink loop") {
            do {
                let loaded = try DraftSettings.load(from: dir)
                return loaded == DraftSettings() ? "defaults" : "loaded"
            } catch let error as DraftSettings.LoadError {
                return error.reason
            } catch {
                return "other \(error)"
            }
        }
        let reason = try XCTUnwrap(message, "draft load blocked on the symlink loop")
        XCTAssertEqual(reason, "cannot be read")
        XCTAssertFalse(reason.contains("not valid settings JSON"))
        XCTAssertNotEqual(reason, "defaults")
    }

    func testDraftSettingsSymlinkToSmallFileStillLoads() throws {
        let dir = try unicodeStoreDir(self)
        let targetDir = dir.appendingPathComponent("real")
        try FileManager.default.createDirectory(at: targetDir, withIntermediateDirectories: true)
        let settings = DraftSettings(vaniConfig: "/tmp/vani-config.json", vaniStateDir: "/tmp/vani-state")
        try settings.save(to: targetDir)
        let saved = targetDir.appendingPathComponent(DraftSettings.fileName)
        let file = dir.appendingPathComponent(DraftSettings.fileName)
        let relative = "real/\(DraftSettings.fileName)"
        try FileManager.default.createSymbolicLink(atPath: file.path, withDestinationPath: relative)
        var link = stat()
        XCTAssertEqual(lstat(file.path, &link), 0)
        XCTAssertEqual(link.st_size, off_t(relative.utf8.count))
        var target = stat()
        XCTAssertEqual(lstat(saved.path, &target), 0)
        XCTAssertGreaterThan(target.st_size, link.st_size)
        XCTAssertEqual(PathProbe.leaf(file), .regular(Int64(target.st_size)))
        XCTAssertEqual(try DraftSettings.load(from: dir), settings)
    }

    private func sizeInode(_ url: URL) throws -> (Int64, UInt64) {
        var st = stat()
        XCTAssertEqual(lstat(url.path, &st), 0)
        return (Int64(st.st_size), UInt64(st.st_ino))
    }
}

fileprivate func unicodeStoreDir(_ test: XCTestCase) throws -> URL {
    let dir = try tempDir(test).appendingPathComponent("目录").appendingPathComponent("bänk-α")
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    return dir
}

fileprivate func activityLine(_ event: ActivityEntry.Event) throws -> Data {
    let enc = JSONEncoder()
    enc.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
    var line = try enc.encode(ActivityEntry(at: "2026-10-18T00:00:00.000Z", source: .app, event: event))
    line.append(0x0A)
    return line
}

fileprivate func padded(_ data: Data, to count: Int, byte: UInt8) -> Data {
    var out = data
    if out.count < count { out.append(Data(repeating: byte, count: count - out.count)) }
    return out
}

fileprivate func finishWithin(_ test: XCTestCase, seconds: TimeInterval, name: String, _ body: @escaping @Sendable () -> String) -> String? {
    let done = test.expectation(description: name)
    let lock = NSLock()
    nonisolated(unsafe) var text: String?
    DispatchQueue.global(qos: .userInitiated).async {
        let value = body()
        lock.lock()
        text = value
        lock.unlock()
        done.fulfill()
    }
    test.wait(for: [done], timeout: seconds)
    lock.lock()
    let out = text
    lock.unlock()
    return out
}

fileprivate func assertFifoHasNoReader(_ fifo: URL, file: StaticString = #filePath, line: UInt = #line) {
    let fd = open(fifo.path, O_WRONLY | O_NONBLOCK)
    if fd >= 0 {
        close(fd)
        XCTFail("FIFO was opened", file: file, line: line)
    } else {
        XCTAssertEqual(errno, ENXIO, String(cString: strerror(errno)), file: file, line: line)
    }
}
