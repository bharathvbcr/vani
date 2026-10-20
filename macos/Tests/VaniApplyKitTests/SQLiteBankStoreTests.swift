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

    func testQueryCandidatesWithIndexes() throws {
        let dir = try tempDir(self).appendingPathComponent("query_test")
        let store = try SQLiteBankStore(directory: dir)

        let ans = Answer(
            key: "phone_number",
            labels: ["mobile", "cell"],
            kind: .tel,
            value: ["+15551234"],
            sensitivity: .standard,
            signatures: ["sig:gh:phone"],
            uses: 0,
            fills: 0,
            updatedAt: "2026-01-01T00:00:00.000Z",
            review: false
        )
        _ = try store.patch(upsert: [ans], deleteKeys: [])

        // 1. By signature
        let bySig = try store.queryCandidates(signatures: ["sig:gh:phone"])
        XCTAssertEqual(bySig.count, 1)
        XCTAssertEqual(bySig.first?.key, "phone_number")

        // 2. By label
        let byLabel = try store.queryCandidates(labels: ["mobile"])
        XCTAssertEqual(byLabel.count, 1)
        XCTAssertEqual(byLabel.first?.key, "phone_number")

        // 3. By key
        let byKey = try store.queryCandidates(keys: ["phone_number"])
        XCTAssertEqual(byKey.count, 1)

        // 4. Mismatch
        let byNone = try store.queryCandidates(signatures: ["unknown"])
        XCTAssertEqual(byNone.count, 0)
    }

    func testPaginationAndSearch() throws {
        let dir = try tempDir(self).appendingPathComponent("list_test")
        let store = try SQLiteBankStore(directory: dir)

        var list: [Answer] = []
        for i in 0..<25 {
            list.append(Answer(
                key: "question_\(String(format: "%02d", i))",
                labels: ["tag_\(i)"],
                kind: .text,
                value: ["Answer text \(i)"],
                sensitivity: .standard,
                signatures: [],
                uses: 0,
                fills: 0,
                updatedAt: "2026-01-\(String(format: "%02d", i + 1))T00:00:00.000Z",
                review: false
            ))
        }
        _ = try store.patch(upsert: list, deleteKeys: [])

        // Page 1
        let p1 = try store.listAnswers(offset: 0, limit: 10)
        XCTAssertEqual(p1.total, 25)
        XCTAssertEqual(p1.answers.count, 10)
        XCTAssertEqual(p1.answers.first?.key, "question_24") // Newest first

        // Search
        let s = try store.listAnswers(offset: 0, limit: 10, search: "question_05")
        XCTAssertEqual(s.total, 1)
        XCTAssertEqual(s.answers.first?.key, "question_05")
    }

    private func row(_ key: String, labels: [String] = [], value: String = "v") -> Answer {
        Answer(key: key, labels: labels, kind: .text, value: [value], sensitivity: .standard, signatures: [], uses: 0, fills: 0,
               updatedAt: "2026-01-01T00:00:00.000Z", review: false)
    }

    func testSearchFindsAWordingOnlyInLabels() throws {
        let store = try SQLiteBankStore(directory: try tempDir(self).appendingPathComponent("label_search"))
        _ = try store.patch(upsert: [row("question_a", labels: ["Notice period wording"]), row("question_b", labels: ["other"])], deleteKeys: [])
        let hit = try store.listAnswers(search: "notice period")
        XCTAssertEqual(hit.total, 1)
        XCTAssertEqual(hit.answers.map(\.key), ["question_a"])
    }

    func testSearchTreatsWildcardsAndQuotesLiterally() throws {
        let store = try SQLiteBankStore(directory: try tempDir(self).appendingPathComponent("literal_search"))
        _ = try store.patch(upsert: [
            row("plain_one", value: "100% sure"),
            row("plain_two", value: "1000 sure"),
            row("under_score", labels: ["a_b"]),
            row("underXscore", labels: ["aXb"]),
            row("quote_row", value: "it's fine"),
        ], deleteKeys: [])
        XCTAssertEqual(try store.listAnswers(search: "%").answers.map(\.key), ["plain_one"])
        XCTAssertEqual(try store.listAnswers(search: "0% s").answers.map(\.key), ["plain_one"])
        XCTAssertEqual(try store.listAnswers(search: "a_b").answers.map(\.key), ["under_score"])
        XCTAssertEqual(try store.listAnswers(search: "it's").answers.map(\.key), ["quote_row"])
        // Injection attempts match nothing and do not damage the table.
        XCTAssertEqual(try store.listAnswers(search: "x' OR '1'='1").total, 0)
        XCTAssertEqual(try store.listAnswers(search: "'; DROP TABLE answers; --").total, 0)
        XCTAssertEqual(try store.listAnswers().total, 5)
        // The escape character itself is literal.
        XCTAssertEqual(try store.listAnswers(search: "\\").total, 0)
    }

    func testSearchMatchesValuesTheJSONEncoderEscapes() throws {
        let store = try SQLiteBankStore(directory: try tempDir(self).appendingPathComponent("escaped_search"))
        _ = try store.patch(upsert: [
            row("linkedin_row", value: "https://www.linkedin.com/in/x"),
            row("escapes_row", value: "say \"hi\" at C:\\temp"),
            row("other_row", value: "nothing here"),
        ], deleteKeys: [])
        XCTAssertEqual(try store.listAnswers(search: "linkedin.com/in").answers.map(\.key), ["linkedin_row"])
        XCTAssertEqual(try store.listAnswers(search: "https://").answers.map(\.key), ["linkedin_row"])
        XCTAssertEqual(try store.listAnswers(search: "\"hi\"").answers.map(\.key), ["escapes_row"])
        XCTAssertEqual(try store.listAnswers(search: "C:\\temp").answers.map(\.key), ["escapes_row"])
        XCTAssertEqual(try store.listAnswers(search: "\\").answers.map(\.key), ["escapes_row"])
        XCTAssertEqual(try store.listAnswers(search: "\"").total, 1)
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
