import Darwin
import Foundation
import XCTest
@testable import VaniApplyKit

/// Attacks on the bank.json stamp that gates the app's tick: what does and does not move it.
/// Temp folders only.
final class BankStampAdversarialTests: XCTestCase {
    private func seeded() throws -> (BankStore, URL) {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        _ = try store.update { $0.settings.captureSelfId = true }
        return (store, dir.appendingPathComponent("bank.json"))
    }

    /// The bytes with the revision digit changed 1 -> 2: same size, different content.
    private func bumped(_ data: Data) throws -> Data {
        var text = try XCTUnwrap(String(data: data, encoding: .utf8))
        let range = try XCTUnwrap(text.range(of: #""rev"\s*:\s*1"#, options: .regularExpression), text)
        text.replaceSubrange(range, with: text[range].dropLast() + "2")
        XCTAssertEqual(text.utf8.count, data.count)
        return Data(text.utf8)
    }

    /// A temp file renamed over bank.json with the same size and the very same mtime: only the
    /// inode differs, and the stamp must carry it.
    func testRenameWithSameSizeAndSameMtimeMovesTheStamp() throws {
        let (store, url) = try seeded()
        let before = store.bankStamp()
        let old = try Data(contentsOf: url)
        var st = stat()
        XCTAssertEqual(stat(url.path, &st), 0)
        let temp = url.deletingLastPathComponent().appendingPathComponent("bank.tmp")
        try bumped(old).write(to: temp)
        var times = [st.st_atimespec, st.st_mtimespec]
        XCTAssertEqual(utimensat(AT_FDCWD, temp.path, &times, 0), 0)
        XCTAssertEqual(rename(temp.path, url.path), 0)
        let after = store.bankStamp()
        XCTAssertEqual(after.size, before.size)
        XCTAssertEqual(after.modifiedNanoseconds, before.modifiedNanoseconds)
        XCTAssertNotEqual(after, before, "an atomic same-size same-mtime replace went unseen")
    }

    /// An in-place same-size overwrite right after the previous write: the nanosecond mtime
    /// must move every time, or the tick would miss an edit.
    func testInPlaceSameSizeEditsInQuickSuccessionAreAllSeen() throws {
        let (store, url) = try seeded()
        let one = try Data(contentsOf: url), two = try bumped(one)
        let handle = try FileHandle(forWritingTo: url)
        defer { try? handle.close() }
        var unseen = 0
        for i in 0..<500 {
            let before = store.bankStamp()
            try handle.seek(toOffset: 0)
            try handle.write(contentsOf: i % 2 == 0 ? two : one)
            try handle.synchronize()
            if store.bankStamp() == before { unseen += 1 }
        }
        XCTAssertEqual(unseen, 0, "\(unseen) of 500 immediate same-size in-place edits left the stamp unchanged")
    }

    /// A missing file stamps as nothing, and creating it moves the stamp.
    func testMissingThenCreated() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        let missing = store.bankStamp()
        XCTAssertEqual(missing, BankStore.BankStamp())
        XCTAssertEqual(store.bankStamp(), missing)
        _ = try store.update { $0.settings.captureSelfId = true }
        XCTAssertNotEqual(store.bankStamp(), missing)
        try FileManager.default.removeItem(at: dir.appendingPathComponent("bank.json"))
        XCTAssertEqual(store.bankStamp(), missing, "removing the file stamps as missing again")
    }

    /// A store directory reached through a symlink stamps the real file.
    func testSymlinkedStoreDirectory() throws {
        let real = try tempDir(self)
        let link = try tempDir(self).appendingPathComponent("link")
        try FileManager.default.createSymbolicLink(at: link, withDestinationURL: real)
        let viaLink = BankStore(directory: link), direct = BankStore(directory: real)
        let before = viaLink.bankStamp()
        _ = try direct.update { $0.settings.captureSelfId = true }
        XCTAssertNotEqual(viaLink.bankStamp(), before)
        XCTAssertEqual(viaLink.bankStamp(), direct.bankStamp())
        let read = try viaLink.read()
        XCTAssertEqual(read.rev, 1)
    }

    /// Characterization of the stamp's limit, printed not asserted: a same-size in-place edit
    /// that restores the old mtime (cp -p, rsync --inplace -t) is invisible to size+mtime+inode.
    func testProbeInPlaceEditWithRestoredMtime() throws {
        let (store, url) = try seeded()
        let one = try Data(contentsOf: url)
        var st = stat()
        XCTAssertEqual(stat(url.path, &st), 0)
        let before = store.bankStamp()
        let handle = try FileHandle(forWritingTo: url)
        try handle.write(contentsOf: try bumped(one))
        try handle.close()
        var times = [st.st_atimespec, st.st_mtimespec]
        XCTAssertEqual(utimensat(AT_FDCWD, url.path, &times, 0), 0)
        print("PROBE restored-mtime in-place edit seen by stamp: \(store.bankStamp() != before)")
    }
}
