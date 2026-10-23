import Foundation
import XCTest
@testable import VaniApplyKit

/// Adversarial review of B2-1/B2-7/B2-8/B2-9. Every test here passes against 58334aa.
final class AdversarialChunkTests: XCTestCase {
    // MARK: helpers

    /// Seeded generator so the property test is reproducible.
    struct SplitMix: RandomNumberGenerator {
        var s: UInt64
        mutating func next() -> UInt64 {
            s &+= 0x9E37_79B9_7F4A_7C15
            var z = s
            z = (z ^ (z >> 30)) &* 0xBF58_476D_1CE4_E5B9
            z = (z ^ (z >> 27)) &* 0x94D0_49BB_1331_11EB
            return z ^ (z >> 31)
        }
    }

    func wireSize(_ reply: [String: Any]) -> Int { NativeMessaging.encodeReply(reply).count - 4 }

    func assertTiles(_ data: Data, _ ranges: [Range<Int>], file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertFalse(ranges.isEmpty, file: file, line: line)
        XCTAssertEqual(ranges.first?.lowerBound, 0, file: file, line: line)
        XCTAssertEqual(ranges.last?.upperBound, data.count, file: file, line: line)
        for (a, b) in zip(ranges, ranges.dropFirst()) { XCTAssertEqual(a.upperBound, b.lowerBound, file: file, line: line) }
    }

    func store(values: [String]) throws -> BankStore {
        let s = BankStore(directory: try tempDir(self))
        var answers: [String: Answer] = [:]
        for (i, v) in values.enumerated() { answers[String(format: "k%04d", i)] = answer(String(format: "k%04d", i), v) }
        _ = try s.put(baseRev: 0, bank: Bank(answers: answers))
        return s
    }

    /// Walk the protocol as the host frames it; returns joined bytes, declared total and the largest framed body.
    func fetchAll(_ s: BankStore) throws -> (joined: Data, total: Int, maxBody: Int, chunks: Int) {
        let g = NativeProtocol.handle(["op": "get"], store: s, source: .chrome)
        guard g["chunked"] as? Bool == true else { return (Data(), 0, wireSize(g), 0) }
        let rev = try XCTUnwrap(g["rev"] as? Int)
        let n = try XCTUnwrap(g["chunkCount"] as? Int)
        var joined = Data(try XCTUnwrap(g["chunk"] as? String).utf8)
        var maxBody = wireSize(g)
        for i in 1..<max(n, 1) {
            let r = NativeProtocol.handle(["op": "getChunk", "rev": rev, "chunkIndex": i], store: s, source: .chrome)
            joined.append(Data(try XCTUnwrap(r["chunk"] as? String, "chunk \(i): \(r)").utf8))
            maxBody = max(maxBody, wireSize(r))
        }
        return (joined, try XCTUnwrap(g["totalBytes"] as? Int), maxBody, n)
    }

    func bankOf(_ v: String, count: Int = 120) -> Bank {
        var answers: [String: Answer] = [:]
        for i in 0..<count { answers[String(format: "k%04d", i)] = answer(String(format: "k%04d", i), v) }
        return Bank(answers: answers)
    }

    // MARK: 1. chunking

    func testChunkRangesPropertyRandomValidUTF8() {
        var rng = SplitMix(s: 0xC0FFEE)
        let pools: [[Unicode.Scalar]] = [["a", "\"", "\\", "/", "\u{01}", "\u{7F}"], ["\u{E9}", "\u{7FF}"],
                                         ["\u{800}", "\u{20AC}", "\u{2028}", "\u{2029}"], ["\u{10000}", "\u{1F600}", "\u{10FFFF}"]]
        for iter in 0..<200 {
            var str = String.UnicodeScalarView()
            let target = Int.random(in: 0...3_000_000, using: &rng) / (iter % 7 == 0 ? 1 : 10)
            var bytes = 0
            while bytes < target {
                let pool = pools[Int.random(in: 0..<pools.count, using: &rng)]
                let sc = pool[Int.random(in: 0..<pool.count, using: &rng)]
                str.append(sc)
                bytes += String(sc).utf8.count
            }
            let data = Data(String(str).utf8)
            let ranges = NativeMessaging.chunkRanges(of: data)
            assertTiles(data, ranges)
            var rejoined = Data()
            for r in ranges {
                let slice = data[r]
                guard let s = String(data: slice, encoding: .utf8) else { XCTFail("iter \(iter): range \(r) not valid UTF-8"); continue }
                XCTAssertEqual(Data(s.utf8), Data(slice), "iter \(iter)")
                XCTAssertLessThanOrEqual(r.count, NativeMessaging.chunkSize, "iter \(iter)")
                let reply: [String: Any] = ["rev": 1_000_000, "chunkIndex": 99_999, "chunkCount": 99_999, "chunk": s]
                XCTAssertLessThanOrEqual(wireSize(reply), NativeMessaging.maxReplyBytes, "iter \(iter)")
                rejoined.append(slice)
            }
            XCTAssertEqual(rejoined, data, "iter \(iter)")
        }
    }

    func testChunkRangesOnlyFourByteScalarsAndEveryByteEscapes() {
        let four = Data(String(repeating: "\u{1F600}", count: 600_000).utf8)
        let fr = NativeMessaging.chunkRanges(of: four)
        assertTiles(four, fr)
        for r in fr { XCTAssertEqual(r.lowerBound % 4, 0); XCTAssertEqual(r.count % 4, 0) }
        let ctl = Data(repeating: 0x01, count: 2_000_000)
        let rs = NativeMessaging.chunkRanges(of: ctl)
        assertTiles(ctl, rs)
        for r in rs {
            let reply: [String: Any] = ["rev": Int.max, "chunkIndex": Int.max, "chunkCount": Int.max, "totalBytes": Int.max,
                                        "chunked": true, "chunk": String(decoding: ctl[r], as: UTF8.self)]
            XCTAssertLessThanOrEqual(wireSize(reply), NativeMessaging.maxReplyBytes)
            XCTAssertFalse(String(decoding: NativeMessaging.encodeReply(reply), as: UTF8.self).contains("too large"))
        }
    }

    func testChunkRangesMalformedBytesTerminateAndTile() {
        var crafted: [[UInt8]] = [
            [0x80], [0x80, 0x80, 0x80], [0xE2, 0x82], [0xF0, 0x9F, 0x98], [0xFF, 0xFE],
            [0x41, 0x80, 0x42], [0xC0, 0x80], [0x01, 0x80, 0x22], [0xE2, 0x82, 0xAC, 0x80, 0x80],
        ]
        crafted.append(Array(repeating: 0x80, count: 1_200_000))
        crafted.append(Array(repeating: 0xE2, count: 1_200_000))
        crafted.append([0xF0] + Array(repeating: 0x80, count: 1_100_000))
        for bytes in crafted {
            let d = Data(bytes)
            assertTiles(d, NativeMessaging.chunkRanges(of: d))
        }
    }

    func testBankExactlyAtAndAroundChunkThreshold() throws {
        func make(pad: Int) -> Bank {
            var a: [String: Answer] = [:]
            for i in 0..<90 { a[String(format: "f%02d", i)] = answer(String(format: "f%02d", i), String(repeating: "a", count: 8_000)) }
            // pad split across answers (each value is capped); keys are fixed width so the total is exact in `pad`
            var left = pad, i = 0
            while left > 0 { let n = min(left, 7_000); a[String(format: "zpad%02d", i)] = answer(String(format: "zpad%02d", i), String(repeating: "b", count: n)); left -= n; i += 1 }
            return Bank(answers: a)
        }
        let base = try make(pad: 0).encoded().count
        let want = NativeMessaging.chunkThreshold
        for delta in [-1, 0, 1] {
            // each extra answer adds a fixed overhead, so size by search: grow pad until encoded == target
            var pad = max(0, want - base + delta - 400)
            while try make(pad: pad).encoded().count < want + delta { pad += 1 }
            XCTAssertEqual(try make(pad: pad).encoded().count, want + delta, "exact size reachable")
            guard pad >= 0 else { XCTFail("filler too large"); continue }
            let s = BankStore(directory: try tempDir(self))
            _ = try s.put(baseRev: 0, bank: make(pad: pad))
            let enc = try s.read().bank.encoded()
            XCTAssertEqual(enc.count, want + delta)
            let g = NativeProtocol.handle(["op": "get"], store: s, source: .chrome)
            XCTAssertEqual(g["chunked"] as? Bool == true, delta > 0, "delta \(delta)")
            XCTAssertLessThanOrEqual(wireSize(g), NativeMessaging.maxReplyBytes)
            if delta > 0 { XCTAssertEqual(try fetchAll(s).joined, enc) }
        }
    }

    func testFullFramedRepliesFitForEscapeHeavyAndSpecialScalarBanks() throws {
        let specials = ["\u{01}\u{02}\u{1F}", "\u{7F}", "\u{2028}\u{2029}", "\"\\/", "\u{1F600}\u{20AC}\u{E9}", "\u{0}"]
        for sp in specials {
            let v = String(repeating: sp, count: 7_000 / sp.utf16.count)
            let s = try store(values: Array(repeating: v, count: 120))
            let enc = try s.read().bank.encoded()
            guard enc.count > NativeMessaging.chunkThreshold else { XCTFail("bank too small for \(sp.debugDescription): \(enc.count)"); continue }
            let f = try fetchAll(s)
            XCTAssertEqual(f.joined, enc, sp.debugDescription)
            XCTAssertEqual(f.total, enc.count)
            XCTAssertLessThanOrEqual(f.maxBody, NativeMessaging.maxReplyBytes, "\(sp.debugDescription): framed reply over the cap")
            XCTAssertEqual(try JSONDecoder().decode(Bank.self, from: f.joined).answers["k0000"]?.value, [v])
        }
    }

    func testGetChunkIndexAbuse() throws {
        let s = try store(values: Array(repeating: String(repeating: "x", count: 8_000), count: 120))
        let g = NativeProtocol.handle(["op": "get"], store: s, source: .chrome)
        let n = try XCTUnwrap(g["chunkCount"] as? Int)
        XCTAssertGreaterThan(n, 1)
        func chunk(_ idx: Any, rev: Int = 1) -> [String: Any] {
            NativeProtocol.handle(["op": "getChunk", "rev": rev, "chunkIndex": idx], store: s, source: .chrome)
        }
        for idx: Any in [n, n + 1, -1, Int.max, Int.min, "1", NSNull(), [1], ["a": 1]] {
            let r = chunk(idx)
            XCTAssertNotNil(r["error"], "\(idx) -> \(r.keys)")
            XCTAssertNil(r["chunk"], "\(idx)")
        }
        XCTAssertEqual(chunk(n - 1)["chunkIndex"] as? Int, n - 1)
        XCTAssertEqual(chunk(0)["chunkIndex"] as? Int, 0)
        XCTAssertEqual(chunk(1, rev: 0)["conflict"] as? Bool, true)
        XCTAssertEqual(chunk(Int.max, rev: 0)["conflict"] as? Bool, true)
    }

    func testMidTransferChangeIsAConflictEvenWhenTotalLengthWouldMatch() throws {
        let a = String(repeating: "a", count: 8_000), b = String(repeating: "b", count: 8_000)
        let s = try store(values: Array(repeating: a, count: 120))
        let g = NativeProtocol.handle(["op": "get"], store: s, source: .chrome)
        let rev = try XCTUnwrap(g["rev"] as? Int)
        let totalA = try XCTUnwrap(g["totalBytes"] as? Int)
        let next = bankOf(b)
        XCTAssertEqual(try next.encoded().count, totalA, "construct a same-length different bank")
        _ = try s.put(baseRev: rev, bank: next)
        let r = NativeProtocol.handle(["op": "getChunk", "rev": rev, "chunkIndex": 1], store: s, source: .chrome)
        XCTAssertEqual(r["conflict"] as? Bool, true)
        XCTAssertNil(r["chunk"], "no chunk of the new bank may be served under the old rev")
        _ = try s.put(baseRev: rev + 1, bank: bankOf(a))  // A -> B -> A
        XCTAssertEqual(NativeProtocol.handle(["op": "getChunk", "rev": rev, "chunkIndex": 1], store: s, source: .chrome)["conflict"] as? Bool, true)
    }

    func testRacingGetChunkWithPutNeverMixesBanks() throws {
        let s = try store(values: Array(repeating: String(repeating: "a", count: 8_000), count: 120))
        let g = NativeProtocol.handle(["op": "get"], store: s, source: .chrome)
        let rev0 = try XCTUnwrap(g["rev"] as? Int)
        let n = try XCTUnwrap(g["chunkCount"] as? Int)
        let queue = DispatchQueue(label: "race", attributes: .concurrent)
        let group = DispatchGroup()
        let lock = NSLock()
        var bad: [String] = []
        let marker = String(repeating: "b", count: 50)
        for i in 1..<n {
            group.enter()
            queue.async {
                for _ in 0..<20 {
                    let r = NativeProtocol.handle(["op": "getChunk", "rev": rev0, "chunkIndex": i], store: s, source: .chrome)
                    if let chunk = r["chunk"] as? String {
                        if (r["rev"] as? Int) != rev0 || chunk.contains(marker) { lock.lock(); bad.append("chunk \(i) rev \(String(describing: r["rev"]))"); lock.unlock() }
                    } else if r["conflict"] as? Bool != true {
                        lock.lock(); bad.append("neither chunk nor conflict: \(r.keys)"); lock.unlock()
                    }
                }
                group.leave()
            }
        }
        group.enter()
        queue.async {
            _ = try? s.put(baseRev: rev0, bank: self.bankOf(String(repeating: "b", count: 8_000)))
            group.leave()
        }
        XCTAssertEqual(group.wait(timeout: .now() + 120), .success)
        XCTAssertEqual(bad, [])
    }

    // MARK: 3. patch

    func testPatchDuplicateAndOverlappingKeysAndHugeBatch() throws {
        let s = BankStore(directory: try tempDir(self))
        func row(_ k: String, _ v: String) -> [String: Any] { ["key": k, "kind": "text", "value": [v]] }
        var r = NativeProtocol.handle(["op": "patch", "upsert": [row("dup", "first"), row("dup", "second")]], store: s, source: .chrome)
        XCTAssertEqual(r["ok"] as? Bool, true)
        XCTAssertEqual(try s.read().bank.answers["dup"]?.value, ["second"])
        // delete and upsert of the same key in one patch: delete runs first, so the row survives
        r = NativeProtocol.handle(["op": "patch", "upsert": [row("both", "kept")], "delete": ["both", "dup"]], store: s, source: .chrome)
        XCTAssertEqual(r["ok"] as? Bool, true)
        XCTAssertEqual(try s.read().bank.answers["both"]?.value, ["kept"])
        XCTAssertNil(try s.read().bank.answers["dup"])
        // 10^5 rows: refused as a whole by the answer cap, nothing applied, rev unchanged, prompt
        let revBefore = try s.read().rev
        let t0 = Date()
        r = NativeProtocol.handle(["op": "patch", "upsert": (0..<30_000).map { row("r\($0)", "v") }], store: s, source: .chrome)
        XCTAssertLessThan(Date().timeIntervalSince(t0), 60)
        XCTAssertNotNil(r["error"], "\(r.keys)")
        XCTAssertEqual(try s.read().rev, revBefore)
        XCTAssertEqual(Set(try s.read().bank.answers.keys), ["both"])
        r = NativeProtocol.handle(["op": "patch", "delete": (0..<100_000).map { "gone\($0)" }], store: s, source: .chrome)
        XCTAssertEqual(r["ok"] as? Bool, true)
    }

    func testPatchBaseRevHostileValues() throws {
        let s = BankStore(directory: try tempDir(self))
        _ = try s.put(baseRev: 0, bank: bankJSON([answerJSON("aa")]))
        let row: [String: Any] = ["key": "bb", "kind": "text", "value": ["x"]]
        for bad: Any in ["1", -1, true, NSNull(), [1], ["a": 1]] {
            let r = NativeProtocol.handle(["op": "patch", "upsert": [row], "baseRev": bad], store: s, source: .chrome)
            XCTAssertNotNil(r["error"], "baseRev \(bad) -> \(r)")
            XCTAssertNil(r["ok"], "baseRev \(bad)")
            XCTAssertEqual(try s.read().rev, 1, "baseRev \(bad) must not apply")
        }
        for huge: Double in [1e300, 1e19, 9.3e18] {
            let r = NativeProtocol.handle(["op": "patch", "upsert": [row], "baseRev": huge], store: s, source: .chrome)
            XCTAssertNil(r["ok"], "baseRev \(huge) -> \(r)")
            XCTAssertEqual(try s.read().rev, 1)
        }
    }

    // MARK: 5. DraftSettings

    func testDraftSettingsEdgeCases() throws {
        let fm = FileManager.default
        func fresh() throws -> (URL, URL) { let d = try tempDir(self); return (d, d.appendingPathComponent("draft-settings.json")) }

        var (d, f) = try fresh()
        fm.createFile(atPath: f.path, contents: Data())
        XCTAssertThrowsError(try DraftSettings.load(from: d)) { XCTAssertTrue($0 is DraftSettings.LoadError) }

        (d, f) = try fresh()
        try Data(#"{"vaniConfig":"/a/config.json"}"#.utf8).write(to: f)
        try fm.setAttributes([.posixPermissions: 0], ofItemAtPath: f.path)
        defer { try? fm.setAttributes([.posixPermissions: 0o600], ofItemAtPath: f.path) }
        if getuid() != 0 {
            XCTAssertThrowsError(try DraftSettings.load(from: d)) { XCTAssertTrue($0 is DraftSettings.LoadError, "\($0)") }
            XCTAssertEqual(DraftSettings.runner(loadingFrom: d, vaniBinary: URL(fileURLWithPath: "/x/vani"), keys: .noKey).refusal?.code, "settings")
        }

        for body in ["null", "5", "\"s\"", "[]", #"{"vaniConfig":["a"]}"#, #"{"vaniConfig":null,"vaniStateDir":7}"#] {
            let (dd, ff) = try fresh()
            try Data(body.utf8).write(to: ff)
            XCTAssertThrowsError(try DraftSettings.load(from: dd), body) { XCTAssertTrue($0 is DraftSettings.LoadError, body) }
        }

        (d, f) = try fresh()
        try Data(#"{"vaniConfig":"/a/config.json","vaniStateDir":"/a/state","futureField":{"x":[1,2]},"another":true}"#.utf8).write(to: f)
        let loaded = try DraftSettings.load(from: d)
        XCTAssertEqual(loaded.vaniConfig, "/a/config.json")
        XCTAssertEqual(loaded.vaniStateDir, "/a/state")
    }
}
