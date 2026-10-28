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

    func testReadLoadsEveryAnswerRow() throws {
        let store = try SQLiteBankStore(directory: try tempDir(self).appendingPathComponent("full_map"))
        let keys = ["a_key", "b_key", "it's_key", "c_key"]
        _ = try store.patch(upsert: keys.map { row($0, value: "v-\($0)") }, deleteKeys: [])
        let answers = try store.read().bank.answers
        XCTAssertEqual(Set(answers.keys), Set(keys))
        for k in keys { XCTAssertEqual(answers[k]?.value, ["v-\(k)"]) }
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

    func testResyncKeepsInt32Max() throws {
        let store = try SQLiteBankStore(directory: try tempDir(self).appendingPathComponent("max"))
        let max = Int(Int32.max)
        let row = Answer(key: "role title", labels: ["role title"], kind: .text, value: ["engineer"], sensitivity: .standard,
                         signatures: [], uses: max, fills: max, updatedAt: "2026-01-01T00:00:00.000Z", review: false)
        try store.resync(Snapshot(rev: 2, bank: Bank(answers: [row.key: row])))
        let snap = try store.read()
        XCTAssertEqual(snap.bank.answers["role title"]?.uses, max)
        XCTAssertEqual(snap.bank.answers["role title"]?.fills, max)
    }

    /// A snapshot that bypasses count() must not trap in sqlite3_bind_int. The unfit row is skipped and logged.
    func testResyncSkipsCounterPastInt32Max() throws {
        let store = try SQLiteBankStore(directory: try tempDir(self).appendingPathComponent("skip"))
        let huge = Int(Int32.max) + 1
        let overflow = Answer(key: "overflow row", labels: ["overflow row"], kind: .text, value: ["skipped-row"], sensitivity: .standard,
                              signatures: [], uses: huge, fills: 1, updatedAt: "2026-01-01T00:00:00.000Z", review: false)
        let kept = Answer(key: "kept row", labels: ["kept row"], kind: .text, value: ["kept-row"], sensitivity: .standard,
                          signatures: [], uses: 4, fills: 2, updatedAt: "2026-01-01T00:00:00.000Z", review: false)
        let sentence = "vani: bank.sqlite mirror skipped a row whose counter does not fit; bank.json is unaffected\n"
        let err = try captureStderr {
            try store.resync(Snapshot(rev: 4, bank: Bank(answers: [overflow.key: overflow, kept.key: kept])))
        }
        XCTAssertEqual(err, sentence)
        let snap = try store.read()
        XCTAssertEqual(snap.rev, 4)
        XCTAssertNil(snap.bank.answers["overflow row"])
        XCTAssertEqual(snap.bank.answers["kept row"]?.value, ["kept-row"])
        XCTAssertEqual(snap.bank.answers["kept row"]?.uses, 4)
        XCTAssertEqual(snap.bank.answers["kept row"]?.fills, 2)

        let fixed = Answer(key: "overflow row", labels: ["overflow row"], kind: .text, value: ["now-kept"], sensitivity: .standard,
                           signatures: [], uses: 1, fills: 1, updatedAt: "2026-01-01T00:00:00.000Z", review: false)
        try store.resync(Snapshot(rev: 5, bank: Bank(answers: [fixed.key: fixed, kept.key: kept])))
        let again = try store.read()
        XCTAssertEqual(again.bank.answers["overflow row"]?.value, ["now-kept"])
        XCTAssertEqual(again.bank.answers["overflow row"]?.uses, 1)
        XCTAssertEqual(again.bank.answers["kept row"]?.uses, 4)
    }

    func testPatchSkipsCounterPastInt32MaxAndLaterPatchSaves() throws {
        let store = try SQLiteBankStore(directory: try tempDir(self).appendingPathComponent("patch-skip"))
        let huge = Int(Int32.max) + 1
        let overflow = Answer(key: "overflow row", labels: ["overflow row"], kind: .text, value: ["skipped-row"], sensitivity: .standard,
                              signatures: [], uses: 2, fills: huge, updatedAt: "2026-01-01T00:00:00.000Z", review: false)
        let kept = Answer(key: "kept row", labels: ["kept row"], kind: .text, value: ["kept-row"], sensitivity: .standard,
                          signatures: [], uses: 1, fills: 0, updatedAt: "2026-01-01T00:00:00.000Z", review: false)
        let sentence = "vani: bank.sqlite mirror skipped a row whose counter does not fit; bank.json is unaffected\n"
        let err = try captureStderr {
            let res = try store.patch(upsert: [overflow, kept], deleteKeys: [])
            XCTAssertEqual(res.rev, 1)
            XCTAssertEqual(res.droppedSecret, 0)
        }
        XCTAssertEqual(err, sentence)
        let snap = try store.read()
        XCTAssertNil(snap.bank.answers["overflow row"])
        XCTAssertEqual(snap.bank.answers["kept row"]?.value, ["kept-row"])

        let fixed = Answer(key: "overflow row", labels: ["overflow row"], kind: .text, value: ["now-kept"], sensitivity: .standard,
                           signatures: [], uses: 2, fills: 3, updatedAt: "2026-01-01T00:00:00.000Z", review: false)
        XCTAssertEqual(try store.patch(upsert: [fixed], deleteKeys: []).rev, 2)
        let again = try store.read()
        XCTAssertEqual(again.bank.answers["overflow row"]?.value, ["now-kept"])
        XCTAssertEqual(again.bank.answers["overflow row"]?.fills, 3)
        XCTAssertEqual(again.bank.answers["kept row"]?.uses, 1)
    }
}
