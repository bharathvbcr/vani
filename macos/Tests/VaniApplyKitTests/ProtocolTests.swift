import Foundation
import XCTest
@testable import VaniApplyKit

final class ProtocolTests: XCTestCase {
    func roundTrip(_ req: Any, _ store: BankStore) throws -> [String: Any] {
        // Through JSON both ways, as the Chrome host sees it.
        let data = try JSONSerialization.data(withJSONObject: req)
        let reply = NativeProtocol.handle(try JSONSerialization.jsonObject(with: data), store: store, source: .chrome)
        return try JSONSerialization.jsonObject(with: JSONSerialization.data(withJSONObject: reply)) as! [String: Any]
    }

    func testPingGetPutConflict() throws {
        let store = BankStore(directory: try tempDir(self))
        let ping = try roundTrip(["op": "ping"], store)
        XCTAssertEqual(ping["ok"] as? Bool, true)
        XCTAssertEqual(ping["protocol"] as? Int, 1)
        XCTAssertEqual(ping["rev"] as? Int, 0)

        let get = try roundTrip(["op": "get"], store)
        XCTAssertEqual(get["rev"] as? Int, 0)
        XCTAssertEqual((get["bank"] as? [String: Any])?["version"] as? Int, 1)

        let put = try roundTrip(["op": "put", "baseRev": 0, "bank": bankJSON([answerJSON("aa"), answerJSON("ssn")])], store)
        XCTAssertEqual(put["rev"] as? Int, 1)
        XCTAssertEqual(put["droppedSecret"] as? Int, 1)

        let stale = try roundTrip(["op": "put", "baseRev": 0, "bank": bankJSON([])], store)
        XCTAssertEqual(stale["conflict"] as? Bool, true)
        XCTAssertEqual(stale["rev"] as? Int, 1)
        XCTAssertNotNil(((stale["bank"] as? [String: Any])?["answers"] as? [String: Any])?["aa"])
    }

    func testRefusals() throws {
        let store = BankStore(directory: try tempDir(self))
        for req: Any in [
            [1], ["op": 3], ["op": "drop"], ["op": "put", "bank": bankJSON([])],
            ["op": "put", "baseRev": -1, "bank": bankJSON([])], ["op": "put", "baseRev": true, "bank": bankJSON([])],
            ["op": "put", "baseRev": 0], ["op": "log"], ["op": "log", "entry": ["event": "steal"]],
        ] {
            let reply = try roundTrip(req, store)
            XCTAssertNotNil(reply["error"] as? String, "\(req) should be refused, got \(reply)")
        }
        XCTAssertEqual(try store.read().rev, 0)
    }

    func testLogAcceptsOnlyNamesHostsAndCounts() throws {
        let store = BankStore(directory: try tempDir(self))
        XCTAssertEqual(try roundTrip(["op": "log", "entry": ["event": "capture", "host": "Boards.Greenhouse.io", "counts": ["added": 3]]], store)["ok"] as? Bool, true)
        for entry: [String: Any] in [
            ["event": "capture", "host": "not a host"],
            ["event": "capture", "host": "evil.example/path?q=Ada"],
            ["event": "capture", "counts": ["first name: Ada": 1]],
            ["event": "capture", "counts": ["added": "Ada"]],
            ["event": "capture", "counts": ["added": -1]],
            ["event": "capture", "counts": ["added": 1.5]],
        ] {
            XCTAssertNotNil(try roundTrip(["op": "log", "entry": entry], store)["error"], "\(entry) should be refused")
        }
        let logged = try store.recentActivity()
        XCTAssertEqual(logged.count, 1)
        XCTAssertEqual(logged.first?.host, "boards.greenhouse.io")
        XCTAssertEqual(logged.first?.source, .chrome)
    }

    func testChunkedGetAndGetChunk() throws {
        let store = BankStore(directory: try tempDir(self))
        // Create a bank that exceeds chunkThreshold (750 KB)
        var answers: [String: Answer] = [:]
        for i in 0..<100 {
            let key = "question_\(i)"
            answers[key] = Answer(key: key, labels: ["label"], kind: .text, value: [String(repeating: "a", count: 8_000)], sensitivity: .standard, signatures: [], uses: 0, fills: 0, updatedAt: "2026-01-01T00:00:00.000Z", review: false)
        }
        let largeBank = Bank(answers: answers)
        _ = try store.put(baseRev: 0, bank: largeBank)

        let first = try roundTrip(["op": "get"], store)
        XCTAssertEqual(first["chunked"] as? Bool, true)
        XCTAssertEqual(first["rev"] as? Int, 1)
        XCTAssertEqual(first["chunkIndex"] as? Int, 0)
        guard let count = first["chunkCount"] as? Int else {
            XCTFail("missing chunkCount")
            return
        }
        XCTAssertGreaterThan(count, 1)
        var allChunks: [String] = [first["chunk"] as! String]

        for i in 1..<count {
            let next = try roundTrip(["op": "getChunk", "rev": 1, "chunkIndex": i], store)
            XCTAssertEqual(next["rev"] as? Int, 1)
            XCTAssertEqual(next["chunkIndex"] as? Int, i)
            allChunks.append(next["chunk"] as! String)
        }

        let fullJSON = allChunks.joined()
        let parsed = try JSONSerialization.jsonObject(with: Data(fullJSON.utf8)) as? [String: Any]
        XCTAssertNotNil(parsed)
        let parsedAnswers = parsed?["answers"] as? [String: Any]
        XCTAssertEqual(parsedAnswers?.count, 100)
    }

    func testGranularPatchAndCandidatesQuery() throws {
        let store = BankStore(directory: try tempDir(self))
        let a1: [String: Any] = [
            "key": "first_name",
            "kind": "text",
            "value": ["Ada"],
            "labels": ["first name", "given name"],
            "signatures": ["gh:fn:1"]
        ]
        let a2: [String: Any] = [
            "key": "email",
            "kind": "email",
            "value": ["ada@example.com"],
            "labels": ["email address"],
            "signatures": ["gh:em:2"]
        ]

        let patchRes = try roundTrip(["op": "patch", "upsert": [a1, a2], "delete": []], store)
        XCTAssertEqual(patchRes["ok"] as? Bool, true)
        XCTAssertEqual(patchRes["rev"] as? Int, 1)

        // Query candidates by signature
        let sigQuery = try roundTrip(["op": "queryCandidates", "signatures": ["gh:fn:1"]], store)
        let candidates = sigQuery["candidates"] as? [[String: Any]]
        XCTAssertEqual(candidates?.count, 1)
        XCTAssertEqual(candidates?.first?["key"] as? String, "first_name")
        XCTAssertEqual(candidates?.first?["value"] as? [String], ["Ada"])

        // Query candidates by label
        let lblQuery = try roundTrip(["op": "queryCandidates", "labels": ["email address"]], store)
        let emailCandidates = lblQuery["candidates"] as? [[String: Any]]
        XCTAssertEqual(emailCandidates?.count, 1)
        XCTAssertEqual(emailCandidates?.first?["key"] as? String, "email")

        // Patch delete
        let delRes = try roundTrip(["op": "patch", "upsert": [], "delete": ["first_name"]], store)
        XCTAssertEqual(delRes["ok"] as? Bool, true)
        XCTAssertEqual(delRes["rev"] as? Int, 2)

        let afterDel = try roundTrip(["op": "queryCandidates", "signatures": ["gh:fn:1"]], store)
        XCTAssertEqual((afterDel["candidates"] as? [[String: Any]])?.count, 0)
    }

    func testListAnswersPaginationAndSearch() throws {
        let store = BankStore(directory: try tempDir(self))
        var upsert: [[String: Any]] = []
        for i in 0..<10 {
            upsert.append([
                "key": "key_\(i)",
                "kind": "text",
                "value": ["val_\(i)"],
                "labels": ["lbl_\(i)"]
            ])
        }
        _ = try roundTrip(["op": "patch", "upsert": upsert, "delete": []], store)

        // First page
        let page1 = try roundTrip(["op": "listAnswers", "offset": 0, "limit": 4], store)
        XCTAssertEqual(page1["total"] as? Int, 10)
        let ans1 = page1["answers"] as? [[String: Any]]
        XCTAssertEqual(ans1?.count, 4)

        // Second page
        let page2 = try roundTrip(["op": "listAnswers", "offset": 4, "limit": 4], store)
        let ans2 = page2["answers"] as? [[String: Any]]
        XCTAssertEqual(ans2?.count, 4)

        // Search
        let searchRes = try roundTrip(["op": "listAnswers", "offset": 0, "limit": 10, "search": "key_3"], store)
        XCTAssertEqual(searchRes["total"] as? Int, 1)
        let found = searchRes["answers"] as? [[String: Any]]
        XCTAssertEqual(found?.first?["key"] as? String, "key_3")
    }
}
