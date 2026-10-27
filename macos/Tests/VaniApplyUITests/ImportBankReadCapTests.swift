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

    func testImportBankRefusesSparseFileOverCapWithoutReplacingStore() throws {
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
        model.importBank(from: huge)
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

    func testImportBankStillAcceptsASmallBank() throws {
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
        model.importBank(from: incoming)
        XCTAssertNil(model.actionError)
        XCTAssertEqual(model.snapshot.bank.answers["aa"]?.value, ["Ada"])
        XCTAssertNil(model.snapshot.bank.answers["old"])
        XCTAssertEqual(model.notice, "Imported 1 answers.")
    }
}
