import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

@MainActor
final class ImportBankReadCapTests: XCTestCase {
    private func folder() throws -> URL {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("vani-import-cap-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        return dir
    }

    private func sparse(_ url: URL, bytes: Int) throws {
        let fd = open(url.path, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0o600)
        XCTAssertGreaterThanOrEqual(fd, 0, String(cString: strerror(errno)))
        defer { close(fd) }
        XCTAssertEqual(ftruncate(fd, off_t(bytes)), 0, String(cString: strerror(errno)))
    }

    func testImportBankRefusesSparseFileOverCapWithoutReplacingStore() async throws {
        let dir = try folder()
        let store = BankStore(directory: dir)
        let kept = Answer(key: "kept", labels: ["kept"], kind: .text, value: ["stored"], sensitivity: .standard,
                          signatures: [], uses: 1, fills: 0, updatedAt: "2026-10-18T00:00:00.000Z", review: false)
        _ = try store.update { $0.answers["kept"] = kept }
        let bankURL = dir.appendingPathComponent("bank.json")
        let before = try Data(contentsOf: bankURL)
        let huge = dir.appendingPathComponent("incoming.json")
        try sparse(huge, bytes: Limits.maxBankBytes + 1)
        var st = stat()
        XCTAssertEqual(lstat(huge.path, &st), 0)
        let incoming = st.st_ino
        let model = AppModel(store: store, autoRegisterAgent: false, autoStartTimer: false)
        XCTAssertEqual(model.snapshot.bank.answers["kept"]?.value, ["stored"])
        let start = Date()
        await model.importBank(from: huge)
        XCTAssertLessThan(Date().timeIntervalSince(start), 2, "import must refuse before reading the body")
        XCTAssertEqual(model.actionError, "Import refused: bank file is larger than 50000000 bytes")
        XCTAssertNil(model.notice)
        XCTAssertEqual(model.snapshot.bank.answers["kept"]?.value, ["stored"])
        XCTAssertNil(model.snapshot.bank.answers["aa"])
        XCTAssertEqual(try Data(contentsOf: bankURL), before)
        var after = stat()
        XCTAssertEqual(lstat(huge.path, &after), 0)
        XCTAssertEqual(after.st_size, st.st_size)
        XCTAssertEqual(after.st_ino, incoming)
    }

    func testImportBankStillAcceptsASmallBank() async throws {
        let dir = try folder()
        let store = BankStore(directory: dir)
        let old = Answer(key: "old", labels: ["old"], kind: .text, value: ["v"], sensitivity: .standard,
                         signatures: [], uses: 1, fills: 0, updatedAt: "2026-10-18T00:00:00.000Z", review: false)
        _ = try store.update { $0.answers["old"] = old }
        let model = AppModel(store: store, autoRegisterAgent: false, autoStartTimer: false)
        let incoming = dir.appendingPathComponent("incoming.json")
        let added = Answer(key: "aa", labels: ["aa"], kind: .text, value: ["Ada"], sensitivity: .standard,
                           signatures: [], uses: 1, fills: 0, updatedAt: "2026-10-18T00:00:00.000Z", review: false)
        try Editing.exportData(Bank(answers: ["aa": added])).write(to: incoming)
        await model.importBank(from: incoming)
        XCTAssertNil(model.actionError)
        XCTAssertEqual(model.snapshot.bank.answers["aa"]?.value, ["Ada"])
        XCTAssertNil(model.snapshot.bank.answers["old"])
        XCTAssertEqual(model.notice, "Imported 1 answers.")
    }

    func testImportBankFollowsSymlinkToASmallBank() async throws {
        let (dir, model, _, _) = try keptBank()
        let target = dir.appendingPathComponent("incoming.json")
        let added = Answer(key: "aa", labels: ["aa"], kind: .text, value: ["v"], sensitivity: .standard,
                           signatures: [], uses: 1, fills: 0, updatedAt: "2026-10-18T00:00:00.000Z", review: false)
        try Editing.exportData(Bank(answers: ["aa": added])).write(to: target)
        let link = dir.appendingPathComponent("link.json")
        try FileManager.default.createSymbolicLink(atPath: link.path, withDestinationPath: target.lastPathComponent)
        var linkSt = stat()
        XCTAssertEqual(lstat(link.path, &linkSt), 0)
        var targetSt = stat()
        XCTAssertEqual(lstat(target.path, &targetSt), 0)
        XCTAssertGreaterThan(targetSt.st_size, linkSt.st_size)
        XCTAssertEqual(PathProbe.leaf(link), .regular(Int64(targetSt.st_size)))
        await model.importBank(from: link)
        XCTAssertNil(model.actionError)
        XCTAssertEqual(model.snapshot.bank.answers["aa"]?.value, ["v"])
        XCTAssertNil(model.snapshot.bank.answers["kept"])
        XCTAssertEqual(model.notice, "Imported 1 answers.")
    }

    func testImportBankSymlinkToSparseOverCapDoesNotReplaceStore() async throws {
        let (dir, model, bankURL, before) = try keptBank()
        let huge = dir.appendingPathComponent("huge.json")
        try sparse(huge, bytes: Limits.maxBankBytes + 1)
        let link = dir.appendingPathComponent("link.json")
        try FileManager.default.createSymbolicLink(atPath: link.path, withDestinationPath: huge.lastPathComponent)
        var linkSt = stat()
        XCTAssertEqual(lstat(link.path, &linkSt), 0)
        XCTAssertLessThan(linkSt.st_size, off_t(Limits.maxBankBytes))
        var target = stat()
        XCTAssertEqual(lstat(huge.path, &target), 0)
        XCTAssertEqual(PathProbe.leaf(link), .regular(Int64(Limits.maxBankBytes + 1)))
        let start = Date()
        await model.importBank(from: link)
        XCTAssertLessThan(Date().timeIntervalSince(start), 2, "import must refuse the symlink before reading the body")
        XCTAssertEqual(model.actionError, "Import refused: bank file is larger than 50000000 bytes")
        XCTAssertNil(model.notice)
        XCTAssertEqual(model.snapshot.bank.answers["kept"]?.value, ["stored"])
        XCTAssertNil(model.snapshot.bank.answers["aa"])
        XCTAssertEqual(try Data(contentsOf: bankURL), before)
        var after = stat()
        XCTAssertEqual(lstat(huge.path, &after), 0)
        XCTAssertEqual(after.st_size, target.st_size)
        XCTAssertEqual(after.st_ino, target.st_ino)
        XCTAssertEqual(try FileManager.default.destinationOfSymbolicLink(atPath: link.path), huge.lastPathComponent)
    }

    func testImportBankSymlinkToFifoDoesNotOpenOrReplaceStore() async throws {
        let (dir, model, bankURL, before) = try keptBank()
        let fifo = dir.appendingPathComponent("pipe")
        XCTAssertEqual(mkfifo(fifo.path, 0o600), 0, String(cString: strerror(errno)))
        let link = dir.appendingPathComponent("link.json")
        try FileManager.default.createSymbolicLink(atPath: link.path, withDestinationPath: fifo.lastPathComponent)
        XCTAssertEqual(PathProbe.leaf(link), .notRegular)
        let start = Date()
        await model.importBank(from: link)
        XCTAssertLessThan(Date().timeIntervalSince(start), 1, "import blocked on the symlink to the FIFO")
        XCTAssertEqual(model.actionError, "Import refused: bank file is unreadable")
        XCTAssertNil(model.notice)
        XCTAssertFalse(model.actionError?.contains("50000000") ?? false)
        XCTAssertEqual(model.snapshot.bank.answers["kept"]?.value, ["stored"])
        XCTAssertEqual(try Data(contentsOf: bankURL), before)
        let fd = open(fifo.path, O_WRONLY | O_NONBLOCK)
        if fd >= 0 {
            close(fd)
            XCTFail("FIFO was opened")
        } else {
            XCTAssertEqual(errno, ENXIO)
        }
    }

    func testImportBankSymlinkLoopDoesNotHangOrReplaceStore() async throws {
        let (dir, model, bankURL, before) = try keptBank()
        let link = dir.appendingPathComponent("link.json")
        let other = dir.appendingPathComponent("other.json")
        try FileManager.default.createSymbolicLink(atPath: link.path, withDestinationPath: other.lastPathComponent)
        try FileManager.default.createSymbolicLink(atPath: other.path, withDestinationPath: link.lastPathComponent)
        var st = stat()
        XCTAssertNotEqual(stat(link.path, &st), 0)
        XCTAssertEqual(errno, ELOOP)
        XCTAssertEqual(PathProbe.leaf(link), .unavailable)
        let start = Date()
        await model.importBank(from: link)
        XCTAssertLessThan(Date().timeIntervalSince(start), 1, "import blocked on the symlink loop")
        XCTAssertNotNil(model.actionError)
        XCTAssertNil(model.notice)
        XCTAssertEqual(model.snapshot.bank.answers["kept"]?.value, ["stored"])
        XCTAssertNil(model.snapshot.bank.answers["aa"])
        XCTAssertEqual(try Data(contentsOf: bankURL), before)
        XCTAssertEqual(lstat(link.path, &st), 0)
        XCTAssertEqual(st.st_mode & S_IFMT, S_IFLNK)
    }

    private func unicodeFolder() throws -> URL {
        let dir = try folder().appendingPathComponent("目录").appendingPathComponent("bänk-α")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    private func keptBank() throws -> (dir: URL, model: AppModel, bankURL: URL, before: Data) {
        let dir = try unicodeFolder()
        let store = BankStore(directory: dir)
        let kept = Answer(key: "kept", labels: ["kept"], kind: .text, value: ["stored"], sensitivity: .standard,
                          signatures: [], uses: 1, fills: 0, updatedAt: "2026-10-18T00:00:00.000Z", review: false)
        _ = try store.update { $0.answers["kept"] = kept }
        let bankURL = dir.appendingPathComponent("bank.json")
        let before = try Data(contentsOf: bankURL)
        let model = AppModel(store: store, autoRegisterAgent: false, autoStartTimer: false)
        XCTAssertEqual(model.snapshot.bank.answers["kept"]?.value, ["stored"])
        return (dir, model, bankURL, before)
    }
}
