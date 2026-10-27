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
}
