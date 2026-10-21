import Foundation
import XCTest
@testable import VaniApplyKit

final class SQLiteBankStoreTests: XCTestCase {
    func testSchemaCreationAndEmptyRead() throws {
        let dir = try tempDir(self).appendingPathComponent("sqlite_test")
        let store = try SQLiteBankStore(directory: dir)
        let snap = try store.read()
        XCTAssertEqual(snap.rev, 0)
        XCTAssertEqual(snap.bank.answers.count, 0)
        XCTAssertFalse(snap.bank.settings.captureSelfId)
    }

    func testAutomaticMigrationFromBankJSON() throws {
        let dir = try tempDir(self).appendingPathComponent("mig_test")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let jsonURL = dir.appendingPathComponent("bank.json")

        // Write a legacy bank.json file
        let legacyBank = Bank(
            answers: [
                "full_name": Answer(key: "full_name", labels: ["name"], kind: .text, value: ["Ada Lovelace"], sensitivity: .standard, signatures: ["sig:1"], uses: 2, fills: 1, updatedAt: "2026-01-01T00:00:00.000Z", review: false)
            ],
            settings: Settings(captureSelfId: true, disabledHosts: ["ignore.example"])
        )
        struct Stored: Codable {
            let rev: Int
            let bank: Bank
        }
        let enc = JSONEncoder()
        let data = try enc.encode(Stored(rev: 7, bank: legacyBank))
        try data.write(to: jsonURL)

        // Opening SQLiteBankStore should automatically migrate from bank.json
        let store = try SQLiteBankStore(directory: dir)
        let snap = try store.read()
        XCTAssertEqual(snap.rev, 7)
        XCTAssertTrue(snap.bank.settings.captureSelfId)
        XCTAssertEqual(snap.bank.settings.disabledHosts, ["ignore.example"])
        XCTAssertEqual(snap.bank.answers["full_name"]?.value, ["Ada Lovelace"])
        XCTAssertEqual(snap.bank.answers["full_name"]?.signatures, ["sig:1"])
    }

    func testGranularPatchUpsertAndDelete() throws {
        let dir = try tempDir(self).appendingPathComponent("patch_test")
        let store = try SQLiteBankStore(directory: dir)

        let a1 = Answer(key: "city", labels: ["location"], kind: .text, value: ["London"], sensitivity: .standard, signatures: ["gh:city"], uses: 1, fills: 0, updatedAt: "2026-01-01T00:00:00.000Z", review: false)
        let a2 = Answer(key: "secret_ssn", labels: ["ssn"], kind: .text, value: ["000-00-0000"], sensitivity: .secret, signatures: [], uses: 0, fills: 0, updatedAt: "2026-01-01T00:00:00.000Z", review: false)

        let res = try store.patch(upsert: [a1, a2], deleteKeys: [])
        XCTAssertEqual(res.rev, 1)
        XCTAssertEqual(res.droppedSecret, 1) // secret is dropped

        let snap1 = try store.read()
        XCTAssertEqual(snap1.bank.answers.count, 1)
        XCTAssertEqual(snap1.bank.answers["city"]?.value, ["London"])

        // Delete key
        let res2 = try store.patch(upsert: [], deleteKeys: ["city"])
        XCTAssertEqual(res2.rev, 2)
        let snap2 = try store.read()
        XCTAssertEqual(snap2.bank.answers.count, 0)
    }

    private func row(_ key: String, labels: [String] = [], value: String = "v") -> Answer {
        Answer(key: key, labels: labels, kind: .text, value: [value], sensitivity: .standard, signatures: [], uses: 0, fills: 0,
               updatedAt: "2026-01-01T00:00:00.000Z", review: false)
    }

    func testResyncReplacesRowsAndSetsRevision() throws {
        let store = try SQLiteBankStore(directory: try tempDir(self).appendingPathComponent("resync"))
        _ = try store.patch(upsert: [row("stale_row")], deleteKeys: [])
        let bank = Bank(answers: ["fresh_row": row("fresh_row", labels: ["l"])], settings: Settings(captureSelfId: true, disabledHosts: ["h.example"]))
        try store.resync(Snapshot(rev: 7, bank: bank))
        let snap = try store.read()
        XCTAssertEqual(snap.rev, 7)
        XCTAssertEqual(Array(snap.bank.answers.keys), ["fresh_row"])
        XCTAssertEqual(snap.bank.answers["fresh_row"]?.labels, ["l"])
        XCTAssertEqual(snap.bank.settings.disabledHosts, ["h.example"])
        // Rev 7 is now the base for a patch.
        XCTAssertEqual(try store.patch(upsert: [], deleteKeys: [], baseRev: 7).rev, 8)
    }
}
