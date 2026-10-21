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

    // MARK: patch validation (B2-7)

    func testPatchWithAnyBadRowAppliesNothingAndKeepsTheRevision() throws {
        let store = BankStore(directory: try tempDir(self))
        let good: [String: Any] = ["key": "good", "kind": "text", "value": ["x"], "labels": ["good"]]
        _ = try roundTrip(["op": "patch", "upsert": [good], "delete": []], store)
        XCTAssertEqual(try store.read().rev, 1)

        let fresh: [String: Any] = ["key": "fresh", "kind": "text", "value": ["y"]]
        let bad: [(String, Any)] = [
            ("one bad row among good", [fresh, ["key": "nokind", "value": ["z"]], good]),
            ("upsert as a string", "not an array"),
            ("upsert as an object", ["key": "x"]),
            ("unknown kind", [fresh, ["key": "k", "kind": "telepathy", "value": ["z"]]]),
            ("non-string value element", [fresh, ["key": "k", "kind": "text", "value": ["a", 1]]]),
            ("value as a string", [fresh, ["key": "k", "kind": "text", "value": "a"]]),
            ("non-object element", [fresh, "oops"]),
            ("null element", [fresh, NSNull()]),
            ("non-string key", [fresh, ["key": 7, "kind": "text", "value": ["a"]]]),
            ("labels of the wrong type", [fresh, ["key": "k", "kind": "text", "value": ["a"], "labels": "l"]]),
            ("uses not a number", [fresh, ["key": "k", "kind": "text", "value": ["a"], "uses": "many"]]),
        ]
        for (name, upsert) in bad {
            let reply = try roundTrip(["op": "patch", "upsert": upsert, "delete": ["good"]], store)
            XCTAssertEqual(reply["code"] as? String, "bad_request", "\(name): \(reply)")
            XCTAssertNotNil(reply["error"] as? String, name)
            XCTAssertNil(reply["ok"], name)
            let snap = try store.read()
            XCTAssertEqual(snap.rev, 1, "\(name): rev must not move")
            XCTAssertEqual(Set(snap.bank.answers.keys), ["good"], "\(name): nothing applied, and the delete in the same patch did not run")
        }
        for (name, req) in [("delete not an array", ["op": "patch", "upsert": [fresh], "delete": "good"] as [String: Any]),
                            ("delete with a non-string", ["op": "patch", "upsert": [fresh], "delete": ["good", 3]]),
                            ("baseRev not a number", ["op": "patch", "upsert": [fresh], "baseRev": "1"])] {
            XCTAssertEqual(try roundTrip(req, store)["code"] as? String, "bad_request", name)
            XCTAssertEqual(try store.read().rev, 1, name)
        }
        // A valid patch, and a delete-only one with no upsert key, still apply.
        XCTAssertEqual(try roundTrip(["op": "patch", "upsert": [fresh]], store)["rev"] as? Int, 2)
        XCTAssertEqual(try roundTrip(["op": "patch", "delete": ["fresh"]], store)["rev"] as? Int, 3)
    }

    // MARK: listAnswers paging (B2-8)

    func testListAnswersOverTheProtocolClampsOffsetAndLimit() throws {
        let store = BankStore(directory: try tempDir(self))
        let rows: [[String: Any]] = (0..<(ListPaging.maxLimit + 20)).map {
            ["key": String(format: "k%04d", $0), "kind": "text", "value": ["v"]]
        }
        _ = try roundTrip(["op": "patch", "upsert": rows], store)
        func count(_ offset: Int, _ limit: Int) throws -> Int {
            ((try roundTrip(["op": "listAnswers", "offset": offset, "limit": limit], store))["answers"] as? [Any])?.count ?? -1
        }
        XCTAssertEqual(try count(0, -1), 1)
        XCTAssertEqual(try count(0, 0), 1)
        XCTAssertEqual(try count(0, 1), 1)
        XCTAssertEqual(try count(-10, 3), 3)
        XCTAssertEqual(try count(0, 10_000_000), ListPaging.maxLimit)
        XCTAssertEqual(try count(ListPaging.maxLimit + 19, 10), 1)
    }

    // MARK: chunked get (B2-1)

    /// Fetch the bank the way the extension does: get, then getChunk 1..<chunkCount, joined as UTF-8 bytes.
    /// Returns the joined bytes and every reply's encoded size on the wire.
    private func fetchChunked(_ store: BankStore, rev: Int) throws -> (joined: Data, wireSizes: [Int]) {
        let firstReply = NativeProtocol.handle(["op": "get"], store: store, source: .chrome)
        XCTAssertEqual(firstReply["chunked"] as? Bool, true)
        let count = try XCTUnwrap(firstReply["chunkCount"] as? Int)
        var joined = Data(try XCTUnwrap(firstReply["chunk"] as? String).utf8)
        var sizes = [NativeMessaging.encodeReply(firstReply).count - 4]
        for i in 1..<count {
            let r = NativeProtocol.handle(["op": "getChunk", "rev": rev, "chunkIndex": i], store: store, source: .chrome)
            XCTAssertEqual(r["chunkCount"] as? Int, count, "chunkCount must agree between get and getChunk")
            joined.append(Data(try XCTUnwrap(r["chunk"] as? String).utf8))
            sizes.append(NativeMessaging.encodeReply(r).count - 4)
        }
        XCTAssertEqual(firstReply["totalBytes"] as? Int, joined.count)
        return (joined, sizes)
    }

    /// A bank over the chunk threshold whose scalar of `width` bytes starts at byte `offset` of encoded().
    private func bankWithScalar(_ scalar: String, at offset: Int) throws -> Bank {
        func build(pad: Int) -> Bank {
            var answers: [String: Answer] = [:]
            for i in 0..<60 { answers[String(format: "f%02d", i)] = answer(String(format: "f%02d", i), String(repeating: "a", count: 8_000)) }
            answers["zmark"] = answer("zmark", String(repeating: "b", count: pad) + scalar + "b")
            for i in 0..<40 { answers[String(format: "zzt%02d", i)] = answer(String(format: "zzt%02d", i), String(repeating: "c", count: 8_000)) }
            return Bank(answers: answers)
        }
        func position(of bank: Bank) throws -> Int {
            let data = try bank.encoded()
            return try XCTUnwrap(data.range(of: Data(scalar.utf8))).lowerBound
        }
        let at0 = try position(of: build(pad: 0))
        let pad = offset - at0
        XCTAssertTrue((0..<10_000).contains(pad), "filler leaves pad \(pad) out of range")
        let bank = build(pad: pad)
        XCTAssertEqual(try position(of: bank), offset)
        return bank
    }

    func testChunkedBankWithScalarsAcrossBoundaryRoundTripsByteIdentical() throws {
        for scalar in ["\u{E9}", "\u{20AC}", "\u{1F600}"] { // 2-, 3- and 4-byte scalars
            for offset in 499_998...500_002 {
                let store = BankStore(directory: try tempDir(self))
                let bank = try bankWithScalar(scalar, at: offset)
                XCTAssertGreaterThan(try bank.encoded().count, 750_000)
                _ = try store.put(baseRev: 0, bank: bank)
                let want = try store.read().bank.encoded()
                let got = try fetchChunked(store, rev: 1)
                XCTAssertEqual(got.joined, want, "\(scalar.utf8.count)-byte scalar at \(offset) did not round-trip")
                XCTAssertFalse(String(decoding: got.joined, as: UTF8.self).contains("\u{FFFD}"))
            }
        }
    }

    func testChunkBoundariesAreScalarAlignedAndFitTheReplyCapWorstCase() throws {
        // Raw bytes that all cost the most when a chunk is re-encoded as a JSON string:
        // control characters (6 bytes), quotes and backslashes (2).
        let worst: [[UInt8]] = [Array(repeating: 0x01, count: 1_500_000), Array(repeating: 0x22, count: 1_500_000),
                                Array(repeating: 0x5C, count: 1_500_000)]
        for bytes in worst {
            let data = Data(bytes)
            let ranges = NativeMessaging.chunkRanges(of: data)
            XCTAssertEqual(ranges.first?.lowerBound, 0)
            XCTAssertEqual(ranges.last?.upperBound, data.count)
            for (a, b) in zip(ranges, ranges.dropFirst()) { XCTAssertEqual(a.upperBound, b.lowerBound) }
            for r in ranges {
                let reply: [String: Any] = ["rev": 1, "chunkIndex": 0, "chunkCount": ranges.count,
                                            "chunk": String(decoding: data[r], as: UTF8.self)]
                let size = NativeMessaging.encodeReply(reply).count - 4
                XCTAssertLessThanOrEqual(size, NativeMessaging.maxReplyBytes)
                XCTAssertFalse(String(decoding: Data(NativeMessaging.encodeReply(reply).dropFirst(4)), as: UTF8.self).contains("too large"))
            }
        }
        // Mixed multi-byte text: every boundary must start a scalar.
        let text = Data(String(repeating: "a\u{E9}\u{20AC}\u{1F600}\"", count: 200_000).utf8)
        for r in NativeMessaging.chunkRanges(of: text) {
            if r.lowerBound < text.count { XCTAssertNotEqual(text[r.lowerBound] & 0xC0, 0x80) }
            XCTAssertNotNil(String(data: text[r], encoding: .utf8))
        }
        XCTAssertEqual(NativeMessaging.chunkRanges(of: Data()).count, 1)
    }

    func testChunkedBankOfEscapeHeavyValuesStaysUnderTheReplyCap() throws {
        let store = BankStore(directory: try tempDir(self))
        var answers: [String: Answer] = [:]
        for i in 0..<100 {
            let v = String(repeating: "\"\\", count: 4_000) // 8,000 chars; encoded as \" \\ pairs, escaped again in a chunk
            answers["q\(i)"] = answer("q\(i)", v)
        }
        _ = try store.put(baseRev: 0, bank: Bank(answers: answers))
        let want = try store.read().bank.encoded()
        let got = try fetchChunked(store, rev: 1)
        XCTAssertEqual(got.joined, want)
        for s in got.wireSizes { XCTAssertLessThanOrEqual(s, NativeMessaging.maxReplyBytes) }
    }
}
